package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Linear integration Phase 0 (issue #971): typed local surface tests. The
// planning-mode tests are red until migration 76 and the fold land; the outbox
// and link tests exercise the typed refusal surface directly.

func setupLinearProduct(t *testing.T, s *Store, productID string) {
	t.Helper()
	setupProductWithProject(t, s, productID, productID+"-project")
}

func TestPlanningModeDefaultsToLocalOnlyAndSetIsEventBacked(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "mode-product")

	mode, err := s.ReadProductPlanningMode(ctx, "mode-product")
	if err != nil {
		t.Fatalf("ReadProductPlanningMode() error = %v", err)
	}
	if mode.PlanningMode != PlanningModeLocalOnly {
		t.Fatalf("default planning mode = %s, want local_only", mode.PlanningMode)
	}

	if _, err := s.SetProductPlanningMode(ctx, "mode-product", PlanningModeLinear, "pilot Linear planning", "operator", 2); err != nil {
		t.Fatalf("SetProductPlanningMode() error = %v", err)
	}
	mode, err = s.ReadProductPlanningMode(ctx, "mode-product")
	if err != nil {
		t.Fatal(err)
	}
	if mode.PlanningMode != PlanningModeLinear {
		t.Fatalf("planning mode after set = %s, want linear_enabled", mode.PlanningMode)
	}
	if mode.Version != 3 {
		t.Fatalf("version after set = %d, want 3", mode.Version)
	}
	var events int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE kind='product.planning_mode_set' AND subject_id='mode-product'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("planning mode events = %d, want 1", events)
	}

	// Setting the same mode again is a typed refusal, not a no-op write.
	if _, err := s.SetProductPlanningMode(ctx, "mode-product", PlanningModeLinear, "again", "operator", 3); err == nil || !strings.Contains(err.Error(), "already uses that planning mode") {
		t.Fatalf("same-mode set error = %v, want already-uses refusal", err)
	}
	// An unknown mode is refused closed.
	if _, err := s.SetProductPlanningMode(ctx, "mode-product", "github_enabled", "why", "operator", 3); err == nil || !strings.Contains(err.Error(), "planning mode is not recognized") {
		t.Fatalf("unknown mode error = %v, want unrecognized refusal", err)
	}
	// An unknown Product refuses with unknown scope.
	if _, err := s.SetProductPlanningMode(ctx, "missing-product", PlanningModeLinear, "why", "operator", 1); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("unknown product error = %v, want unknown_scope", err)
	}
}

func TestPlanningModeResolutionRefusesAmbiguityAndMissingSetup(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "resolve-product")

	// Empty Product context refuses with ambiguous scope naming the operator
	// choice; it never infers from repository path or installation.
	if _, err := s.ResolveLinearPlanningTarget(ctx, ""); err == nil || !failureKindIs(err, KindAmbiguousScope) {
		t.Fatalf("empty product resolve error = %v, want ambiguous_scope", err)
	}

	// local_only Products resolve without any Linear surface involvement.
	mode, err := s.ResolveLinearPlanningTarget(ctx, "resolve-product")
	if err != nil {
		t.Fatalf("local_only resolve error = %v", err)
	}
	if mode.PlanningMode != PlanningModeLocalOnly {
		t.Fatalf("local_only resolve mode = %s", mode.PlanningMode)
	}

	// linear_enabled without a declared connection reports missing setup; it
	// never silently falls back to local-only operation.
	if _, err := s.SetProductPlanningMode(ctx, "resolve-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveLinearPlanningTarget(ctx, "resolve-product"); err == nil || !strings.Contains(err.Error(), "no declared Linear connection") {
		t.Fatalf("enabled-without-connection error = %v, want missing-setup refusal", err)
	}
}

func failureKindIs(err error, kind FailureKind) bool {
	if f, ok := err.(*Failure); ok {
		return f.Kind == kind
	}
	return false
}

func setupLinearConnectionResource(t *testing.T, s *Store, productID string, metadata map[string]any) {
	setupLinearConnectionResourceAtVersion(t, s, productID, metadata, 2)
}

func setupLinearConnectionResourceAtVersion(t *testing.T, s *Store, productID string, metadata map[string]any, expectedVersion int64) {
	t.Helper()
	ctx := context.Background()
	linear, _ := metadata["linear"].(map[string]any)
	if linear != nil {
		if _, exists := linear["project_ids"]; !exists {
			linear["project_ids"] = map[string]string{productID + "-project": "linear-project-1"}
		}
		if _, exists := linear["status_ids"]; !exists {
			linear["status_ids"] = map[string]string{
				"needed": "linear-status-needed", "in_progress": "linear-status-in-progress",
				"completed": "linear-status-completed", "cancelled": "linear-status-cancelled",
				"superseded": "linear-status-superseded",
			}
		}
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	raw, _ := json.Marshal(metadata)
	if _, err := CreateManagedResource(ctx, s, ManagedResourceCreateRequest{
		EventID: "linear-conn-" + productID, ResourceID: "linear-conn-" + productID, ProductID: productID,
		DisplayName: "Linear connection", Class: "saas", Kind: "saas_account", Purpose: "Linear planning connection",
		StageMaturity: "prototype", StageAudienceCommitment: "operator_only", Environments: []string{"production"},
		MetadataSchemaVersion: "linear-connection-v1", Metadata: raw, OwnerPurpose: "planning", OwnerEnvironments: []string{"production"},
		ExpectedProductVersion: expectedVersion, Actor: "operator", OccurredAt: now,
	}); err != nil {
		t.Fatalf("CreateManagedResource() error = %v", err)
	}
}

func TestLinearConnectionResolutionStates(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "conn-product")

	connection, err := s.ReadLinearConnection(ctx, "conn-product")
	if err != nil {
		t.Fatal(err)
	}
	if connection.State != LinearConnectionAbsent {
		t.Fatalf("no-resource state = %s, want absent", connection.State)
	}

	setupLinearConnectionResource(t, s, "conn-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1"}})
	connection, err = s.ReadLinearConnection(ctx, "conn-product")
	if err != nil {
		t.Fatal(err)
	}
	if connection.State != LinearConnectionPartial {
		t.Fatalf("partial state = %s, want partial", connection.State)
	}

	if _, err := s.SetProductPlanningMode(ctx, "conn-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveLinearPlanningTarget(ctx, "conn-product"); err == nil || !strings.Contains(err.Error(), "no declared Linear connection") {
		t.Fatalf("partial connection resolve error = %v, want missing-setup refusal", err)
	}
}

func TestLinearConnectionUpdateIsVersionCheckedAndPreservesMetadata(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "update-connection-product")
	setupLinearConnectionResource(t, s, "update-connection-product", map[string]any{
		"unrelated": "preserved",
		"linear":    map[string]any{"workspace_url": "https://linear.app/example", "team_id": "old-team", "auth_mode": "personal_api_key", "project_id": "legacy-project", "project_ids": map[string]string{"update-connection-product-project": "old-project", "stale-project": "stale-project"}, "status_ids": map[string]string{"cancelled": "old-cancelled", "completed": "old-completed", "superseded": "old-superseded"}},
	})
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "update-linear-connection", ResourceID: "linear-conn-update-connection-product", ProductID: "update-connection-product",
		TeamID: "new-team", ProjectIDs: map[string]string{"update-connection-product-project": "new-project"}, StatusIDs: map[string]string{"needed": "new-needed", "in_progress": "new-in-progress", "cancelled": "new-cancelled", "completed": "new-completed", "superseded": "new-superseded"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpdateLinearConnection() error = %v", err)
	}
	connection, err := s.ReadLinearConnection(ctx, "update-connection-product")
	if err != nil {
		t.Fatal(err)
	}
	if connection.TeamID != "new-team" || len(connection.ProjectIDs) != 1 || connection.ProjectIDs["update-connection-product-project"] != "new-project" || connection.StatusIDs["cancelled"] != "new-cancelled" || connection.Version != 2 {
		t.Fatalf("updated connection = %+v", connection)
	}
	var metadataJSON string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT metadata FROM managed_resources WHERE resource_id=?`, connection.ResourceID).Scan(&metadataJSON); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["unrelated"] != "preserved" {
		t.Fatalf("unrelated metadata = %v, want preserved", metadata["unrelated"])
	}
	linearMetadata, ok := metadata["linear"].(map[string]any)
	if !ok {
		t.Fatalf("linear metadata = %v, want object", metadata["linear"])
	}
	if _, exists := linearMetadata["project_id"]; exists {
		t.Fatalf("legacy project_id remains in Linear metadata: %v", linearMetadata["project_id"])
	}
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "stale-linear-connection", ResourceID: connection.ResourceID, ProductID: "update-connection-product",
		TeamID: "stale-team", StatusIDs: map[string]string{"needed": "stale-needed", "in_progress": "stale-in-progress", "cancelled": "stale-cancelled", "completed": "stale-completed", "superseded": "stale-superseded"}, ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Now().UTC(),
	}); err == nil || !failureKindIs(err, KindVersionConflict) {
		t.Fatalf("stale update error = %v, want version conflict", err)
	}
}

func TestLinearConnectionUpdateRequiresCompleteReplacementStatusMapping(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "status-update-product")
	setupLinearConnectionResource(t, s, "status-update-product", map[string]any{
		"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "old-team", "auth_mode": "personal_api_key", "status_ids": map[string]string{"cancelled": "old-cancelled", "completed": "old-completed", "superseded": "old-superseded"}},
	})
	base := LinearConnectionUpdateRequest{
		ResourceID: "linear-conn-status-update-product", ProductID: "status-update-product", TeamID: "new-team",
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC),
	}
	for name, statusIDs := range map[string]map[string]string{
		"empty":    {},
		"partial":  {"cancelled": "new-cancelled"},
		"old team": {"cancelled": "old-cancelled", "completed": "old-completed", "superseded": "old-superseded"},
	} {
		t.Run(name, func(t *testing.T) {
			req := base
			req.EventID = "status-update-" + name
			req.StatusIDs = statusIDs
			if err := s.UpdateLinearConnection(ctx, req); err == nil {
				t.Fatalf("status mapping %v was accepted", statusIDs)
			}
		})
	}
}

func TestLinearIssueEnqueueUsesTheOwningProjectMapping(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "routing-product")
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			projectCreatedEvent("other-project", "routing-secondary-project"),
			membershipEvent("routing-secondary-membership", "product_project.added", SubjectProduct, "routing-product", map[string]any{
				"product_id": "routing-product", "project_id": "other-project", "role": "secondary", "reason": "test",
				"expected_version": 2, "resulting_version": 3,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "routing-product"): 2,
			VersionRef(SubjectProject, "other-project"):   0,
		},
	}); err != nil {
		t.Fatalf("add routing Product project: %v", err)
	}
	if _, err := s.SetProductPlanningMode(ctx, "routing-product", PlanningModeLinear, "pilot", "operator", 3); err != nil {
		t.Fatal(err)
	}
	setupLinearConnectionResourceAtVersion(t, s, "routing-product", map[string]any{
		"linear": map[string]any{
			"workspace_url": "https://linear.app/example",
			"team_id":       "team-uuid-1",
			"auth_mode":     "personal_api_key",
			"project_ids": map[string]string{
				"routing-product-project": "linear-project-one",
				"other-project":           "linear-project-two",
			},
		},
	}, 4)
	seedLinearWorkItem(t, s, "routing-work", "other-project", "Routing title", "Routing value")
	op, err := s.EnqueueLinearIssueForWork(ctx, "routing-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
	}
	var payload linearPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ProjectID != "linear-project-two" {
		t.Fatalf("payload project id = %q, want linear-project-two", payload.ProjectID)
	}
}

func TestLinearIssueEnqueueRefusesAnUnmappedOwningProject(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "unmapped-product")
	setupLinearConnectionResource(t, s, "unmapped-product", map[string]any{
		"linear": map[string]any{
			"workspace_url": "https://linear.app/example",
			"team_id":       "team-uuid-1",
			"auth_mode":     "personal_api_key",
			"project_ids":   map[string]string{"other-project": "linear-project-two"},
		},
	})
	if _, err := s.SetProductPlanningMode(ctx, "unmapped-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "unmapped-work", "unmapped-product-project", "Unmapped title", "Unmapped value")
	if _, err := s.EnqueueLinearIssueForWork(ctx, "unmapped-work", LinearOpIssueCreate); err == nil || !strings.Contains(err.Error(), "Core") || !strings.Contains(err.Error(), "not mapped") {
		t.Fatalf("unmapped project error = %v, want named refusal", err)
	}
}

func TestLinearOutboxTypedSurface(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "outbox-product")
	// A work item is required before an operation can reference it.
	seedWorkItem(t, s, "outbox-work")

	payload, _ := json.Marshal(map[string]any{"title": "Example issue"})
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{OperationID: "op-1", WorkID: "outbox-work", OpKind: LinearOpIssueCreate, IdempotencyKey: "idem-1", Payload: payload}); err != nil {
		t.Fatalf("EnqueueLinearOperation() error = %v", err)
	}
	// Unknown work items refuse.
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{OperationID: "op-2", WorkID: "ghost", OpKind: LinearOpIssueCreate, IdempotencyKey: "idem-2", Payload: payload}); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("unknown work error = %v, want unknown_scope", err)
	}
	// Duplicate idempotency keys refuse.
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{OperationID: "op-3", WorkID: "outbox-work", OpKind: LinearOpIssueCreate, IdempotencyKey: "idem-1", Payload: payload}); err == nil || !failureKindIs(err, KindIdempotencyConflict) {
		t.Fatalf("duplicate key error = %v, want idempotency_conflict", err)
	}
	// Closed operation kinds refuse.
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{OperationID: "op-4", WorkID: "outbox-work", OpKind: "issue_delete", IdempotencyKey: "idem-4", Payload: payload}); err == nil || !strings.Contains(err.Error(), "operation kind is not recognized") {
		t.Fatalf("unknown kind error = %v, want unrecognized refusal", err)
	}

	// Transitions walk queued -> in_flight -> done and refuse jumps.
	if err := s.CompleteLinearOperation(ctx, "op-1", LinearRemoteIdentity{RemoteUUID: "uuid-1"}); err == nil || !failureKindIs(err, KindInvalidTransition) {
		t.Fatalf("queued->done error = %v, want invalid_transition", err)
	}
	if err := s.ClaimLinearOperation(ctx, "op-1"); err != nil {
		t.Fatalf("ClaimLinearOperation() error = %v", err)
	}
	if err := s.FailLinearOperation(ctx, "op-1", "permanent", "auth refused"); err != nil {
		t.Fatalf("FailLinearOperation() error = %v", err)
	}
	// in_flight moved to failed; requeue then complete.
	if err := s.RequeueLinearOperation(ctx, "op-1"); err != nil {
		t.Fatalf("RequeueLinearOperation() error = %v", err)
	}
	if err := s.ClaimLinearOperation(ctx, "op-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteLinearOperation(ctx, "op-1", LinearRemoteIdentity{RemoteUUID: "uuid-1"}); err != nil {
		t.Fatalf("CompleteLinearOperation() error = %v", err)
	}
	var state string
	var attempts int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state, attempts FROM linear_outbox WHERE operation_id='op-1'`).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != LinearOutboxDone || attempts != 3 {
		t.Fatalf("terminal outbox = %s/%d, want done/3", state, attempts)
	}
}

func TestLinearLinkTypedTransitions(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	seedWorkItem(t, s, "link-work")

	// A new link must start unpublished or pending.
	if err := s.RecordLinearLink(ctx, "link-work", "uuid-1", "", "", "", "", LinearLinkConfirmed); err == nil || !failureKindIs(err, KindInvalidTransition) {
		t.Fatalf("new-link confirmed error = %v, want invalid_transition", err)
	}
	if err := s.RecordLinearLink(ctx, "link-work", "uuid-1", "LIN-1", "https://linear.app/example/issue/LIN-1", "2026-09-09T00:00:00Z", "", LinearLinkUnpublished); err != nil {
		t.Fatalf("create unpublished error = %v", err)
	}
	if err := s.RecordLinearLink(ctx, "link-work", "uuid-1", "LIN-1", "", "", "", LinearLinkPending); err != nil {
		t.Fatalf("pending error = %v", err)
	}
	if err := s.RecordLinearLink(ctx, "link-work", "uuid-1", "LIN-1", "", "", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", LinearLinkConfirmed); err != nil {
		t.Fatalf("confirmed error = %v", err)
	}
	// confirmed -> unpublished is not admitted.
	if err := s.RecordLinearLink(ctx, "link-work", "uuid-1", "", "", "", "", LinearLinkUnpublished); err == nil || !failureKindIs(err, KindInvalidTransition) {
		t.Fatalf("confirmed->unpublished error = %v, want invalid_transition", err)
	}
	// An invalid content hash refuses closed.
	if err := s.RecordLinearLink(ctx, "link-work", "uuid-1", "", "", "", "md5:bad", LinearLinkDegraded); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("bad hash error = %v, want sha256 refusal", err)
	}
}

func TestLinearIntegrationHealthRead(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "health-product")
	seedLinearWorkItem(t, s, "health-work", "health-product-project", "Health title", "Health value")

	health, err := s.ReadLinearIntegrationHealth(ctx, "health-product")
	if err != nil {
		t.Fatalf("ReadLinearIntegrationHealth() error = %v", err)
	}
	if health.PlanningMode != PlanningModeLocalOnly || health.ConnectionState != LinearConnectionAbsent {
		t.Fatalf("health = %+v", health)
	}
	if health.OutboxDepth != 0 || health.OutboxOldestPendingAgeSeconds != 0 {
		t.Fatalf("empty outbox health = %+v", health)
	}
	for state, count := range health.LinkCounts {
		if count != 0 {
			t.Fatalf("empty link count %s = %d", state, count)
		}
	}

	payload, _ := json.Marshal(map[string]any{"title": "x"})
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{OperationID: "hop-1", WorkID: "health-work", OpKind: LinearOpIssueCreate, IdempotencyKey: "hidem-1", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "health-work", "uuid-h", "", "", "", "", LinearLinkUnpublished); err != nil {
		t.Fatal(err)
	}
	health, err = s.ReadLinearIntegrationHealth(ctx, "health-product")
	if err != nil {
		t.Fatal(err)
	}
	if health.OutboxDepth != 1 || health.LinkCounts[LinearLinkUnpublished] != 1 {
		t.Fatalf("populated health = %+v", health)
	}
	if err := s.ClaimLinearOperation(ctx, "hop-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteLinearOperation(ctx, "hop-1", LinearRemoteIdentity{RemoteUUID: "health-remote"}); err != nil {
		t.Fatal(err)
	}
	health, err = s.ReadLinearIntegrationHealth(ctx, "health-product")
	if err != nil {
		t.Fatal(err)
	}
	if health.OutboxDepth != 0 || health.OutboxOldestPendingAgeSeconds != 0 {
		t.Fatalf("completed outbox health = %+v", health)
	}

	// Unknown Products refuse rather than reporting empty health.
	if _, err := s.ReadLinearIntegrationHealth(ctx, "ghost"); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("unknown product health error = %v, want unknown_scope", err)
	}
}

func seedWorkItem(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, created_at, updated_at) VALUES(?, 'task', 'seed', 'needed', 0, 'standard', 1, 't', 't')`, workID); err != nil {
		t.Fatalf("seed work item %s: %v", workID, err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
