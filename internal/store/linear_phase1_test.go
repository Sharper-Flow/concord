package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
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
	setupLinearConnectionResourceAtVersion(t, s, "enq-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "project_ids": map[string]string{"enq-product-project": "project-uuid-1"}, "auth_mode": "personal_api_key"}}, 3)
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
		ProductID   string `json:"product_id"`
		TeamID      string `json:"team_id"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ClientUUID == "" || decoded.Title != "Enqueue title" || decoded.Description != composeLinearIssueBody("Enqueue value statement", "", "enq-work") || decoded.ProductID != "enq-product" || decoded.TeamID != "team-uuid-1" || decoded.ProjectID != "project-uuid-1" {
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

func TestLinearClaimUsesQueuedProductIdentity(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "claim-owner-product")
	setupLinearConnectionResource(t, s, "claim-owner-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, "claim-owner-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "claim-owner-work", "claim-owner-product-project", "Title", "Value")
	op, err := s.EnqueueLinearIssueForProduct(ctx, "claim-owner-product", "claim-owner-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			productCreatedEvent("claim-other-product", "claim-other-created"),
			membershipEvent("claim-other-membership", "product_project.added", SubjectProduct, "claim-other-product", map[string]any{"product_id": "claim-other-product", "project_id": "claim-owner-product-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "claim-other-product"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimLinearOperationsForProduct(ctx, "claim-other-product", 1); err != nil {
		t.Fatal(err)
	} else if len(claimed) != 0 {
		t.Fatalf("other Product claimed %d operation(s)", len(claimed))
	}
	claimed, err := s.ClaimLinearOperationsForProduct(ctx, "claim-owner-product", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].OperationID != op.OperationID {
		t.Fatalf("owner claims = %+v", claimed)
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

func TestLinearUpdateCompletionRefreshesConfirmedLink(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "update-product")
	setupLinearConnectionResource(t, s, "update-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key", "status_ids": map[string]string{"needed": "state-needed", "in_progress": "state-in-progress", "completed": "state-completed", "cancelled": "state-cancelled", "superseded": "state-superseded"}}})
	if _, err := s.SetProductPlanningMode(ctx, "update-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "update-work", "update-product-project", "Updated title", "Updated value")
	create, err := s.EnqueueLinearIssueForWork(ctx, "update-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 1); err != nil {
		t.Fatal(err)
	}
	first := LinearRemoteIdentity{RemoteUUID: "remote-1", HumanKey: "SHA-1", URL: "https://linear.app/example/issue/SHA-1", RemoteUpdatedAt: "2026-09-09T01:00:00Z", ContentHash: "sha256:" + strings.Repeat("1", 64)}
	if err := s.CompleteLinearOperation(ctx, create.OperationID, first); err != nil {
		t.Fatal(err)
	}

	update, err := s.EnqueueLinearIssueForWork(ctx, "update-work", LinearOpIssueUpdate)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimLinearOperations(ctx, 1)
	if err != nil || len(claimed) != 1 || claimed[0].OperationID != update.OperationID {
		t.Fatalf("claimed update = %+v, error = %v", claimed, err)
	}
	second := LinearRemoteIdentity{RemoteUUID: "remote-1", HumanKey: "SHA-1", URL: "https://linear.app/example/issue/SHA-1", RemoteUpdatedAt: "2026-09-09T02:00:00Z", ContentHash: "sha256:" + strings.Repeat("2", 64)}
	if err := s.CompleteLinearOperation(ctx, update.OperationID, second); err != nil {
		t.Fatalf("update completion error = %v", err)
	}

	var outboxState, linkState, remoteUUID, updatedAt, contentHash string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state FROM linear_outbox WHERE operation_id=?`, update.OperationID).Scan(&outboxState); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT link_state, remote_issue_uuid, remote_updated_at, content_hash FROM linear_issue_links WHERE work_id=?`, "update-work").Scan(&linkState, &remoteUUID, &updatedAt, &contentHash); err != nil {
		t.Fatal(err)
	}
	if outboxState != LinearOutboxDone || linkState != LinearLinkConfirmed || remoteUUID != second.RemoteUUID || updatedAt != second.RemoteUpdatedAt || contentHash != second.ContentHash {
		t.Fatalf("completion state = %s/%s/%s/%s/%s", outboxState, linkState, remoteUUID, updatedAt, contentHash)
	}
}

func TestLinearTerminalStatusUsesDeclaredConnectionPolicy(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "status-product")
	setupLinearConnectionResource(t, s, "status-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"status_ids": map[string]string{"cancelled": "state-cancelled", "completed": "state-completed", "superseded": "state-superseded"},
	}})
	if _, err := s.SetProductPlanningMode(ctx, "status-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "status-work", "status-product-project", "Terminal title", "Terminal value")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "status-work-cancelled", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "status-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"from":"needed","to":"cancelled","reason":"cancelled for test","expected_version":1,"resulting_version":2}`),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "status-work"): 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "status-work", "remote-status", "SHA-2", "https://linear.app/example/issue/SHA-2", "", "", LinearLinkUnpublished); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "status-work", "remote-status", "SHA-2", "https://linear.app/example/issue/SHA-2", "", "", LinearLinkPending); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "status-work", "remote-status", "SHA-2", "https://linear.app/example/issue/SHA-2", "", "", LinearLinkConfirmed); err != nil {
		t.Fatal(err)
	}
	op, err := s.EnqueueLinearIssueForWork(ctx, "status-work", LinearOpIssueUpdate)
	if err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT payload FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Lifecycle string `json:"lifecycle"`
		StatusID  string `json:"status_id"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Lifecycle != "cancelled" || decoded.StatusID != "state-cancelled" {
		t.Fatalf("terminal payload = %+v", decoded)
	}
}

func TestLinearLifecycleTransitionQueuesConfirmedLinkUpdate(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "lifecycle-product")
	setupLinearConnectionResource(t, s, "lifecycle-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"status_ids": map[string]string{"needed": "state-needed", "in_progress": "state-in-progress", "completed": "state-completed", "cancelled": "state-cancelled", "superseded": "state-superseded"},
	}})
	if _, err := s.SetProductPlanningMode(ctx, "lifecycle-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "lifecycle-work", "lifecycle-product-project", "Lifecycle title", "Lifecycle value")
	for _, state := range []string{LinearLinkUnpublished, LinearLinkPending, LinearLinkConfirmed} {
		if err := s.RecordLinearLink(ctx, "lifecycle-work", "remote-lifecycle", "", "", "", "", state); err != nil {
			t.Fatal(err)
		}
	}

	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "lifecycle-work-in-progress", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "lifecycle-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"from":"needed","to":"in_progress","reason":"start execution","expected_version":1,"resulting_version":2}`),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "lifecycle-work"): 1}}); err != nil {
		t.Fatal(err)
	}
	var lifecycle, statusID string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT json_extract(payload, '$.lifecycle'), json_extract(payload, '$.status_id') FROM linear_outbox WHERE work_id=?`, "lifecycle-work").Scan(&lifecycle, &statusID); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "in_progress" || statusID != "state-in-progress" {
		t.Fatalf("automatic update = %s/%s, want in_progress/state-in-progress", lifecycle, statusID)
	}
}

func TestLinearLifecycleTransitionSkipsUnmappedLegacyConnection(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "legacy-product")
	// A legacy connection maps only the three terminal lifecycles, which the
	// read path must keep accepting.
	setupLinearConnectionResource(t, s, "legacy-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"status_ids": map[string]string{"completed": "state-completed", "cancelled": "state-cancelled", "superseded": "state-superseded"},
	}})
	if _, err := s.SetProductPlanningMode(ctx, "legacy-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "legacy-work", "legacy-product-project", "Legacy title", "Legacy value")
	for _, state := range []string{LinearLinkUnpublished, LinearLinkPending, LinearLinkConfirmed} {
		if err := s.RecordLinearLink(ctx, "legacy-work", "remote-legacy", "", "", "", "", state); err != nil {
			t.Fatal(err)
		}
	}

	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{
		EventID: "legacy-work-in-progress", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "legacy-work", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"from":"needed","to":"in_progress","reason":"start execution","expected_version":1,"resulting_version":2}`),
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "legacy-work"): 1}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_outbox WHERE work_id=?`, "legacy-work").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unmapped lifecycle enqueued %d operations, want 0", count)
	}
}

func TestLinearHealthReportsUnmappedLifecyclesOnLegacyConnection(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "health-legacy-product")
	setupLinearConnectionResource(t, s, "health-legacy-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"status_ids": map[string]string{"completed": "state-completed", "cancelled": "state-cancelled", "superseded": "state-superseded"},
	}})
	if _, err := s.SetProductPlanningMode(ctx, "health-legacy-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	health, err := s.ReadLinearIntegrationHealth(ctx, "health-legacy-product")
	if err != nil {
		t.Fatal(err)
	}
	if len(health.UnmappedLifecycles) != 2 || health.UnmappedLifecycles[0] != "in_progress" || health.UnmappedLifecycles[1] != "needed" {
		t.Fatalf("unmapped lifecycles = %v, want [in_progress needed]", health.UnmappedLifecycles)
	}
}

func TestLinearConnectionUpdateAcceptsSharedStatusAcrossLifecycles(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "shared-product")
	setupLinearConnectionResource(t, s, "shared-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
	}})
	if _, err := s.SetProductPlanningMode(ctx, "shared-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	// A default workspace holds one cancelled-category status; cancelled and
	// superseded must both be declarable against it.
	if err := s.UpdateLinearConnection(ctx, LinearConnectionUpdateRequest{
		EventID: "shared-connection-update", ResourceID: "linear-conn-shared-product", ProductID: "shared-product",
		TeamID: "team-uuid-1", StatusIDs: map[string]string{"needed": "state-todo", "in_progress": "state-in-progress", "completed": "state-done", "cancelled": "state-canceled", "superseded": "state-canceled"},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpdateLinearConnection() with shared status error = %v", err)
	}
}

// seedLinearContractPremise records a workflow contract premise directly so the
// enqueue body test owns its fixture without a full workflow instance walk.
func seedLinearContractPremise(t *testing.T, s *Store, workID, premise string) {
	t.Helper()
	actorRef := DeriveWorkflowActorRef("principal/body", "client/body", "agent/body", "session/body")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','2026-09-09T00:00:00Z'); DELETE FROM fold_guard`,
		actorRef, "principal/body", "client/body", "agent/body", "session/body"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,?,'internal_sqlite','[]','[]','2026-09-09T00:00:00Z',?,'[]','[]',1,'prototype_internal'); DELETE FROM fold_guard`,
		workID, premise, actorRef); err != nil {
		t.Fatal(err)
	}
}

func decodeLinearDescription(t *testing.T, s *Store, operationID string) string {
	t.Helper()
	var payload string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT payload FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Description
}

func TestLinearEnqueueBodyComposesPremiseAndResume(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "body-product")
	setupLinearConnectionResource(t, s, "body-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"status_ids": map[string]string{"needed": "state-needed", "in_progress": "state-in-progress", "completed": "state-completed", "cancelled": "state-cancelled", "superseded": "state-superseded"},
	}})
	if _, err := s.SetProductPlanningMode(ctx, "body-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "body-work", "body-product-project", "Body title", "Body value statement")
	seedLinearContractPremise(t, s, "body-work", "Body premise text")

	create, err := s.EnqueueLinearIssueForWork(ctx, "body-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	createDescription := decodeLinearDescription(t, s, create.OperationID)
	for _, want := range []string{"Body value statement", "## Premise\n\nBody premise text", "Concord work: body-work", "Resume: `concord zl body-work --`"} {
		if !strings.Contains(createDescription, want) {
			t.Fatalf("create description = %q, want substring %q", createDescription, want)
		}
	}

	update, err := s.EnqueueLinearIssueForWork(ctx, "body-work", LinearOpIssueUpdate)
	if err != nil {
		t.Fatal(err)
	}
	updateDescription := decodeLinearDescription(t, s, update.OperationID)
	if updateDescription != createDescription {
		t.Fatalf("update description = %q, want create description %q", updateDescription, createDescription)
	}

	// Without a current contract the Premise section is omitted, not emitted
	// as an empty label; value statement and resume footer stay.
	seedLinearWorkItem(t, s, "plain-work", "body-product-project", "Plain title", "Plain value")
	plain, err := s.EnqueueLinearIssueForWork(ctx, "plain-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	plainDescription := decodeLinearDescription(t, s, plain.OperationID)
	if strings.Contains(plainDescription, "## Premise") {
		t.Fatalf("plain description = %q, want no Premise section", plainDescription)
	}
	for _, want := range []string{"Plain value", "Concord work: plain-work", "Resume: `concord zl plain-work --`"} {
		if !strings.Contains(plainDescription, want) {
			t.Fatalf("plain description = %q, want substring %q", plainDescription, want)
		}
	}
}

func TestComposeLinearIssueBodyOmitsAbsentSections(t *testing.T) {
	body := composeLinearIssueBody("", "", "work-x")
	want := "Concord work: work-x\nResume: `concord zl work-x --`"
	if body != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if got := composeLinearIssueBody("  value ", " premise ", "work-y"); !strings.Contains(got, "value\n\n## Premise\n\npremise\n\nConcord work: work-y") {
		t.Fatalf("body = %q, want trimmed sections", got)
	}
}

func TestHasNewerLinearIssueUpdateUsesEnqueueOrderOnEqualTimestamps(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "tie-product")
	seedLinearWorkItem(t, s, "tie-work", "tie-product-project", "Tie title", "Tie value")
	payload, err := json.Marshal(map[string]any{"client_uuid": "tie-client", "product_id": "tie-product", "title": "tie title", "description": "tie description", "team_id": "team-uuid-1", "connection_version": 1})
	if err != nil {
		t.Fatal(err)
	}
	// The later update carries the lexically smaller operation id, so any
	// id-based tiebreak would classify it as the stale one.
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{OperationID: "z-older-op", WorkID: "tie-work", OpKind: LinearOpIssueUpdate, IdempotencyKey: "tie-older", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueLinearOperation(ctx, LinearOutboxEntry{OperationID: "a-newer-op", WorkID: "tie-work", OpKind: LinearOpIssueUpdate, IdempotencyKey: "tie-newer", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE linear_outbox SET created_at='2026-09-15T00:00:00Z'`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	older, err := s.HasNewerLinearIssueUpdate(ctx, "z-older-op")
	if err != nil {
		t.Fatal(err)
	}
	if !older {
		t.Fatal("first-enqueued operation with equal created_at must report a newer update")
	}
	newer, err := s.HasNewerLinearIssueUpdate(ctx, "a-newer-op")
	if err != nil {
		t.Fatal(err)
	}
	if newer {
		t.Fatal("last-enqueued operation with equal created_at must not report a newer update")
	}
}
