package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The approved cross-Product shape is one shared work identity: a primary
// project in the owning Product plus explicit secondary memberships in other
// Products' projects. The Linear identity stays the primary Product's, so
// enqueue and lifecycle keep exactly one link through the owning Product's
// connection while the per-Product scope check still guards the other
// Product.
func TestLinearSharedCrossProductWorkItemKeepsOneOwningLink(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	setupLinearProduct(t, s, "shared-concord-product")
	setupLinearProduct(t, s, "shared-secondary-product")
	setupLinearConnectionResource(t, s, "shared-concord-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"status_ids": map[string]string{"needed": "state-needed", "in_progress": "state-in-progress", "completed": "state-completed", "cancelled": "state-cancelled", "superseded": "state-superseded"},
	}})
	if _, err := s.SetProductPlanningMode(ctx, "shared-concord-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "shared-cross-work", "shared-concord-product-project", "Shared title", "Shared value")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'secondary')`, "shared-cross-work", "shared-secondary-product-project"); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// The other Product's scope still guards its own surface: a payload
	// naming the secondary Product refuses as a relation the work item does
	// not carry.
	if _, err := s.EnqueueLinearIssueForProduct(ctx, "shared-secondary-product", "shared-cross-work", LinearOpIssueCreate); err == nil || !failureKindIs(err, KindInvalidRelation) {
		t.Fatalf("secondary-scoped enqueue error = %v, want invalid_relation", err)
	}

	op, err := s.EnqueueLinearIssueForWork(ctx, "shared-cross-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
	}
	var payload linearPayload
	if err := json.Unmarshal(op.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ProductID != "shared-concord-product" {
		t.Fatalf("create payload product = %q, want the primary Product shared-concord-product", payload.ProductID)
	}

	// Confirm the link the create enqueue left unpublished, then drive a
	// lifecycle transition: the automatic update must address the same
	// single Concord link through the Concord connection.
	for _, step := range []struct {
		state string
		hash  string
	}{{LinearLinkPending, ""}, {LinearLinkConfirmed, "sha256:" + strings.Repeat("ab", 32)}} {
		if err := s.RecordLinearLink(ctx, "shared-cross-work", "remote-shared", "CON-77", "https://linear.app/example/issue/CON-77", "", step.hash, step.state); err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "shared-cross-work-in-progress", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "shared-cross-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"from":"needed","to":"in_progress","reason":"start execution","expected_version":1,"resulting_version":2}`),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "shared-cross-work"): 1}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_issue_links WHERE work_id=?`, "shared-cross-work").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("link rows = %d, want exactly one link for the shared work item", count)
	}
	var productID, lifecycle, statusID string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_extract(payload, '$.product_id'), json_extract(payload, '$.lifecycle'), json_extract(payload, '$.status_id') FROM linear_outbox WHERE work_id=? AND op_kind=?`, "shared-cross-work", LinearOpIssueUpdate).Scan(&productID, &lifecycle, &statusID); err != nil {
		t.Fatal(err)
	}
	if productID != "shared-concord-product" || lifecycle != "in_progress" || statusID != "state-in-progress" {
		t.Fatalf("lifecycle update = %s/%s/%s, want shared-concord-product/in_progress/state-in-progress", productID, lifecycle, statusID)
	}
}
