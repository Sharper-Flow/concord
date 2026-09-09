package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// seedLinearWorkItem seeds a work item with title, value statement, and primary
// membership in the Product's project, so the Phase 1 enqueue path can resolve
// exactly one Product.
func seedLinearWorkItem(t *testing.T, s *Store, workID, projectID, title, valueStatement string) {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', ?, 'needed', 0, 'standard', 1, ?, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`, workID, title, `{"title":"`+title+`","value_statement":"`+valueStatement+`","kind":"task","priority":0,"urgency":"standard"}`); err != nil {
		t.Fatalf("seed work item %s: %v", workID, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, workID, projectID); err != nil {
		t.Fatalf("seed work membership %s: %v", workID, err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLinearEnqueueForWorkGuards(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "enq-product")
	seedLinearWorkItem(t, s, "enq-work", "enq-product-project", "Enqueue title", "Enqueue value statement")

	// local_only refuses before any row is written.
	if _, err := s.EnqueueLinearIssueForWork(ctx, "enq-work", LinearOpIssueCreate); err == nil || !strings.Contains(err.Error(), "local_only") {
		t.Fatalf("local_only error = %v, want typed refusal", err)
	}

	// linear_enabled without a declared connection refuses as missing setup.
	if _, err := s.SetProductPlanningMode(ctx, "enq-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueLinearIssueForWork(ctx, "enq-work", LinearOpIssueCreate); err == nil || !strings.Contains(err.Error(), "no declared Linear connection") {
		t.Fatalf("missing-setup error = %v, want typed refusal", err)
	}

	// Unknown work refuses.
	setupLinearConnectionResourceAtVersion(t, s, "enq-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}}, 3)
	if _, err := s.EnqueueLinearIssueForWork(ctx, "ghost", LinearOpIssueCreate); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("unknown work error = %v, want unknown_scope", err)
	}

	// update without a link refuses.
	if _, err := s.EnqueueLinearIssueForWork(ctx, "enq-work", LinearOpIssueUpdate); err == nil || !strings.Contains(err.Error(), "no link exists to update") {
		t.Fatalf("update-without-link error = %v, want typed refusal", err)
	}

	// The happy path writes outbox queued plus link unpublished with payload.
	op, err := s.EnqueueLinearIssueForWork(ctx, "enq-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueForWork() error = %v", err)
	}
	var state string
	var payload string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state, payload FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&state, &payload); err != nil {
		t.Fatal(err)
	}
	if state != LinearOutboxQueued {
		t.Fatalf("outbox state = %s, want queued", state)
	}
	var decoded struct {
		ClientUUID  string `json:"client_uuid"`
		Title       string `json:"title"`
		Description string `json:"description"`
		TeamID      string `json:"team_id"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ClientUUID == "" || decoded.Title != "Enqueue title" || decoded.Description != "Enqueue value statement" || decoded.TeamID != "team-uuid-1" {
		t.Fatalf("payload = %+v", decoded)
	}
	if len(decoded.ClientUUID) != 36 || !strings.Contains(decoded.ClientUUID, "-") {
		t.Fatalf("client uuid %q is not a UUID", decoded.ClientUUID)
	}
	var linkState, linkUUID string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT link_state, remote_issue_uuid FROM linear_issue_links WHERE work_id='enq-work'`).Scan(&linkState, &linkUUID); err != nil {
		t.Fatal(err)
	}
	if linkState != LinearLinkUnpublished || linkUUID != decoded.ClientUUID {
		t.Fatalf("link = %s/%s, want unpublished with client UUID", linkState, linkUUID)
	}
}

func TestLinearClaimBatchIsExclusiveAndBounded(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "claim-product")
	setupLinearConnectionResource(t, s, "claim-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, "claim-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		workID := "claim-work-" + string(rune('a'+i))
		seedLinearWorkItem(t, s, workID, "claim-product-project", "Title", "Value")
		if _, err := s.EnqueueLinearIssueForWork(ctx, workID, LinearOpIssueCreate); err != nil {
			t.Fatal(err)
		}
	}
	// Bounds refuse outside 1..25.
	if _, err := s.ClaimLinearOperations(ctx, 0); err == nil || !failureKindIs(err, KindInvalidPayload) {
		t.Fatalf("limit 0 error = %v, want invalid_payload", err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 26); err == nil || !failureKindIs(err, KindInvalidPayload) {
		t.Fatalf("limit 26 error = %v, want invalid_payload", err)
	}
	first, err := s.ClaimLinearOperations(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 {
		t.Fatalf("first claim = %d operations, want 2", len(first))
	}
	for _, op := range first {
		if op.Attempts != 1 {
			t.Fatalf("claimed attempts = %d, want 1", op.Attempts)
		}
	}
	second, err := s.ClaimLinearOperations(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("second claim = %d operations, want the remaining 1", len(second))
	}
	seen := map[string]bool{}
	for _, op := range append(first, second...) {
		seen[op.WorkID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("claims overlap or miss: %v", seen)
	}
}

func TestLinearFailClassesAndAttemptBound(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "fail-product")
	setupLinearConnectionResource(t, s, "fail-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, "fail-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "fail-work", "fail-product-project", "Title", "Value")
	op, err := s.EnqueueLinearIssueForWork(ctx, "fail-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	// An unrecognized class refuses before any state changes.
	if err := s.FailLinearOperation(ctx, op.OperationID, "sometimes", "detail"); err == nil || !strings.Contains(err.Error(), "failure class is not recognized") {
		t.Fatalf("class error = %v, want typed refusal", err)
	}
	// Retryable returns the operation to queued until the attempt bound.
	for cycle := 1; cycle <= 4; cycle++ {
		claimed, err := s.ClaimLinearOperations(ctx, 1)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("claim cycle %d = %v, %v", cycle, claimed, err)
		}
		if err := s.FailLinearOperation(ctx, op.OperationID, "retryable", "rate limited"); err != nil {
			t.Fatalf("retryable fail cycle %d: %v", cycle, err)
		}
		var state string
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != LinearOutboxQueued {
			t.Fatalf("cycle %d state = %s, want queued", cycle, state)
		}
	}
	// The fifth claim reaches the attempt bound and lands in failed.
	claimed, err := s.ClaimLinearOperations(ctx, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("final claim = %v, %v", claimed, err)
	}
	if err := s.FailLinearOperation(ctx, op.OperationID, "retryable", "rate limited"); err != nil {
		t.Fatal(err)
	}
	var state, lastError string
	var attempts int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state, attempts, last_error FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&state, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	// The failed transition itself records the terminal attempt on top of the
	// five claims, matching the Phase 0 counting (claim and fail each record).
	if state != LinearOutboxFailed || attempts != 6 || lastError != "rate limited" {
		t.Fatalf("terminal = %s/%d/%q, want failed/6/rate limited", state, attempts, lastError)
	}
}

func TestLinearCompleteConfirmsLinkIdentity(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "done-product")
	setupLinearConnectionResource(t, s, "done-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, "done-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "done-work", "done-product-project", "Title", "Value")
	op, err := s.EnqueueLinearIssueForWork(ctx, "done-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 1); err != nil {
		t.Fatal(err)
	}
	identity := LinearRemoteIdentity{
		RemoteUUID:      "68d52710-76d9-4b41-ba45-778511d0e2ed",
		HumanKey:        "SHA-1",
		URL:             "https://linear.app/example/issue/SHA-1",
		RemoteUpdatedAt: "2026-09-09T12:00:00Z",
		ContentHash:     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	if err := s.CompleteLinearOperation(ctx, op.OperationID, identity); err != nil {
		t.Fatalf("CompleteLinearOperation() error = %v", err)
	}
	var outboxState string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&outboxState); err != nil {
		t.Fatal(err)
	}
	if outboxState != LinearOutboxDone {
		t.Fatalf("outbox state = %s, want done", outboxState)
	}
	var linkState, remoteUUID, humanKey, url, contentHash string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT link_state, remote_issue_uuid, human_key, url, content_hash FROM linear_issue_links WHERE work_id='done-work'`).Scan(&linkState, &remoteUUID, &humanKey, &url, &contentHash); err != nil {
		t.Fatal(err)
	}
	if linkState != LinearLinkConfirmed || remoteUUID != identity.RemoteUUID || humanKey != "SHA-1" || url != identity.URL || contentHash != identity.ContentHash {
		t.Fatalf("link = %s/%s/%s/%s, want confirmed with identity", linkState, remoteUUID, humanKey, url)
	}
}
