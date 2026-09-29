package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// An absorbed Linear configuration refusal (CD-0171 D3) stays a local no-op
// but must surface: the work item's confirmed issue link moves to degraded,
// link_counts.degraded carries the standing sync loss, and the next fold
// enqueue the repaired mapping admits restores confirmed.

func confirmLinearIssueLink(t *testing.T, s *Store, ctx context.Context, workID, remoteUUID string) {
	t.Helper()
	for _, state := range []string{LinearLinkUnpublished, LinearLinkPending, LinearLinkConfirmed} {
		if err := s.RecordLinearLink(ctx, workID, remoteUUID, "DEG-1", "https://linear.app/example/issue/DEG-1", "", "", state); err != nil {
			t.Fatal(err)
		}
	}
}

func linearLinkState(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var state string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT link_state FROM linear_issue_links WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func transitionLinearWorkForDegraded(t *testing.T, s *Store, workID, from, to string, expected, resulting int64, eventID string, at time.Time) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"from": from, "to": to, "reason": "degraded link test", "expected_version": expected, "resulting_version": resulting})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: eventID, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: at, PayloadVersion: 1,
		Payload: payload,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}}); err != nil {
		t.Fatal(err)
	}
}

// setupDegradedLinkProduct wires a linear_enabled Product whose connection
// maps the lifecycle statuses but deliberately carries no project:<id> label,
// which is exactly the configuration that refused every issue_update of the
// observed drift case.
func setupDegradedLinkProduct(t *testing.T, s *Store, productID string) {
	t.Helper()
	setupLinearProduct(t, s, productID)
	setupLinearConnectionResource(t, s, productID, map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"label_ids": map[string]string{},
	}})
	if _, err := s.SetProductPlanningMode(context.Background(), productID, PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
}

func TestLinearConfigurationRefusalMarksLinkDegraded(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupDegradedLinkProduct(t, s, "degraded-product")
	seedLinearWorkItem(t, s, "degraded-work", "degraded-product-project", "Degraded title", "Degraded value")
	confirmLinearIssueLink(t, s, ctx, "degraded-work", "remote-degraded")

	before, err := s.ReadLinearIntegrationHealth(ctx, "degraded-product")
	if err != nil {
		t.Fatal(err)
	}
	if before.LinkCounts[LinearLinkConfirmed] != 1 || before.LinkCounts[LinearLinkDegraded] != 0 {
		t.Fatalf("health before the refusal = %v, want confirmed 1 degraded 0", before.LinkCounts)
	}

	transitionLinearWorkForDegraded(t, s, "degraded-work", "needed", "in_progress", 1, 2, "degraded-work-start", time.Unix(1, 0).UTC())

	if state := linearLinkState(t, s, "degraded-work"); state != LinearLinkDegraded {
		t.Fatalf("link state after the absorbed refusal = %s, want degraded", state)
	}
	after, err := s.ReadLinearIntegrationHealth(ctx, "degraded-product")
	if err != nil {
		t.Fatal(err)
	}
	if after.LinkCounts[LinearLinkDegraded] != 1 || after.LinkCounts[LinearLinkConfirmed] != 0 {
		t.Fatalf("health after the refusal = %v, want degraded 1 confirmed 0", after.LinkCounts)
	}
	if after.OutboxDepth != 0 {
		t.Fatalf("outbox depth after the absorbed refusal = %d, want 0", after.OutboxDepth)
	}
}

func TestDegradedLinkRecoversOnNextSuccessfulFold(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupDegradedLinkProduct(t, s, "recover-product")
	seedLinearWorkItem(t, s, "recover-work", "recover-product-project", "Recover title", "Recover value")
	confirmLinearIssueLink(t, s, ctx, "recover-work", "remote-recover")

	transitionLinearWorkForDegraded(t, s, "recover-work", "needed", "in_progress", 1, 2, "recover-work-start", time.Unix(1, 0).UTC())
	if state := linearLinkState(t, s, "recover-work"); state != LinearLinkDegraded {
		t.Fatalf("link state after the absorbed refusal = %s, want degraded", state)
	}

	// Mapping the member Project's label repairs the configuration, and the
	// next fold enqueue the connection admits restores the link.
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "recover-mapping-repair", ResourceID: "linear-conn-recover-product", ProductID: "recover-product",
		LabelIDs:                map[string]string{"project:recover-product-project": "label-recover-repo"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	transitionLinearWorkForDegraded(t, s, "recover-work", "in_progress", "completed", 2, 3, "recover-work-complete", time.Unix(2, 0).UTC())

	if state := linearLinkState(t, s, "recover-work"); state != LinearLinkConfirmed {
		t.Fatalf("link state after the successful fold enqueue = %s, want confirmed", state)
	}
	health, err := s.ReadLinearIntegrationHealth(ctx, "recover-product")
	if err != nil {
		t.Fatal(err)
	}
	if health.LinkCounts[LinearLinkConfirmed] != 1 || health.LinkCounts[LinearLinkDegraded] != 0 {
		t.Fatalf("health after recovery = %v, want confirmed 1 degraded 0", health.LinkCounts)
	}
	var opKind string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT op_kind FROM linear_outbox WHERE work_id=?`, "recover-work").Scan(&opKind); err != nil {
		t.Fatal(err)
	}
	if opKind != LinearOpIssueUpdate {
		t.Fatalf("recovery queued %s, want %s", opKind, LinearOpIssueUpdate)
	}
}

func TestConfigurationRefusalNeverFailsLocalTransition(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupDegradedLinkProduct(t, s, "noop-product")
	seedLinearWorkItem(t, s, "noop-work", "noop-product-project", "Noop title", "Noop value")
	confirmLinearIssueLink(t, s, ctx, "noop-work", "remote-noop")

	transitionLinearWorkForDegraded(t, s, "noop-work", "needed", "in_progress", 1, 2, "noop-work-start", time.Unix(1, 0).UTC())

	var lifecycle string
	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT lifecycle, version FROM work_items WHERE id=?`, "noop-work").Scan(&lifecycle, &version); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "in_progress" || version != 2 {
		t.Fatalf("local transition after the absorbed refusal = %s at version %d, want in_progress at version 2", lifecycle, version)
	}
	var ops int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_outbox WHERE work_id=?`, "noop-work").Scan(&ops); err != nil {
		t.Fatal(err)
	}
	if ops != 0 {
		t.Fatalf("the absorbed refusal queued %d operations, want 0", ops)
	}
}
