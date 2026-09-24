package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// seedAuditCommentCase enables a linear_enabled Product with a declared
// connection, seeds the work item, and walks its link to the requested state,
// mirroring the enqueue and drain lifecycle the audit comment route guards
// on. An empty linkState leaves the work item unlinked.
func seedAuditCommentCase(t *testing.T, s *Store, productID, workID, linkState string) {
	t.Helper()
	ctx := context.Background()
	projectID := productID + "-project"
	setupLinearProduct(t, s, productID)
	setupLinearConnectionResource(t, s, productID, map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "project_ids": map[string]string{projectID: "project-uuid-1"}, "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, productID, PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, workID, projectID, "Audit title", "Audit value")
	if linkState == "" {
		return
	}
	steps := []struct {
		state string
		hash  string
	}{{LinearLinkUnpublished, ""}}
	if linkState != LinearLinkUnpublished {
		steps = append(steps, struct {
			state string
			hash  string
		}{LinearLinkPending, ""})
	}
	if linkState == LinearLinkConfirmed {
		steps = append(steps, struct {
			state string
			hash  string
		}{LinearLinkConfirmed, "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"})
	}
	for _, step := range steps {
		remoteUUID := "remote-" + workID + "-uuid"
		if err := s.RecordLinearLink(ctx, workID, remoteUUID, "CON-397", "https://linear.app/example/issue/CON-397", "2026-09-09T12:00:00Z", step.hash, step.state); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinearAuditCommentEnqueueRequiresConfirmedLink(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()

	// A work item with no link refuses before any row is written.
	seedAuditCommentCase(t, s, "audit-nolink-product", "audit-nolink-work", "")
	if _, err := s.EnqueueLinearIssueAuditComment(ctx, "audit-nolink-product", "audit-nolink-work", "Correction with source."); err == nil || !strings.Contains(err.Error(), "no Linear link exists to audit") {
		t.Fatalf("no-link error = %v, want typed refusal", err)
	}

	// An unconfirmed link refuses: the audit comment follows a confirmed
	// identity, never a queued one.
	seedAuditCommentCase(t, s, "audit-pending-product", "audit-pending-work", LinearLinkPending)
	if _, err := s.EnqueueLinearIssueAuditComment(ctx, "audit-pending-product", "audit-pending-work", "Correction with source."); err == nil || !strings.Contains(err.Error(), "not confirmed") {
		t.Fatalf("pending-link error = %v, want typed refusal", err)
	}

	// A work item in another Product refuses the cross-Product scope.
	seedAuditCommentCase(t, s, "audit-confirmed-product", "audit-confirmed-work", LinearLinkConfirmed)
	if _, err := s.EnqueueLinearIssueAuditComment(ctx, "audit-nolink-product", "audit-confirmed-work", "Correction with source."); err == nil || !failureKindIs(err, KindInvalidRelation) {
		t.Fatalf("cross-product error = %v, want invalid_relation", err)
	}

	// An empty audit body refuses: the route publishes sourced corrections,
	// never a placeholder.
	if _, err := s.EnqueueLinearIssueAuditComment(ctx, "audit-confirmed-product", "audit-confirmed-work", "   "); err == nil || !strings.Contains(err.Error(), "audit comment body is empty") {
		t.Fatalf("empty-body error = %v, want typed refusal", err)
	}
	if _, err := s.EnqueueLinearIssueAuditComment(ctx, "audit-confirmed-product", "audit-ghost", "Correction with source."); err == nil || !failureKindIs(err, KindUnknownScope) {
		t.Fatalf("unknown work error = %v, want unknown_scope", err)
	}
}

func TestLinearAuditCommentEnqueueQueuesTypedOperation(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	seedAuditCommentCase(t, s, "audit-product", "audit-typed-work", LinearLinkConfirmed)
	const body = "- CON-397 states the export ends at 500 rows; the measured limit is 1,000 rows (issue CON-397 comment thread)."
	op, err := s.EnqueueLinearIssueAuditComment(ctx, "audit-product", "audit-typed-work", body)
	if err != nil {
		t.Fatalf("EnqueueLinearIssueAuditComment() error = %v", err)
	}
	if op.OpKind != LinearOpIssueAuditComment || op.IdempotencyKey == "" || op.OperationID != "linear-"+op.IdempotencyKey {
		t.Fatalf("operation = %+v, want a typed audit comment with its client UUID identity", op)
	}
	var state, payload string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state, payload FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&state, &payload); err != nil {
		t.Fatal(err)
	}
	if state != LinearOutboxQueued {
		t.Fatalf("outbox state = %s, want queued", state)
	}
	var decoded struct {
		ClientUUID        string `json:"client_uuid"`
		ProductID         string `json:"product_id"`
		TeamID            string `json:"team_id"`
		ConnectionVersion int64  `json:"connection_version"`
		RemoteIssueUUID   string `json:"remote_issue_uuid"`
		AuditComment      string `json:"audit_comment"`
		Title             string `json:"title"`
		Description       string `json:"description"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ClientUUID != op.IdempotencyKey || decoded.ProductID != "audit-product" || decoded.TeamID != "team-uuid-1" {
		t.Fatalf("payload identity = %+v", decoded)
	}
	if decoded.RemoteIssueUUID != "remote-audit-typed-work-uuid" {
		t.Fatalf("payload names remote issue %q, want the confirmed linked issue", decoded.RemoteIssueUUID)
	}
	if decoded.AuditComment != body {
		t.Fatalf("audit body %q was not stored verbatim", decoded.AuditComment)
	}
	if decoded.Title != "" || decoded.Description != "" {
		t.Fatalf("audit comment payload carries title %q or description %q; the route is comment-only", decoded.Title, decoded.Description)
	}
}

func TestLinearAuditCommentCompletionKeepsRecordedFreshness(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	seedAuditCommentCase(t, s, "audit-product", "audit-fresh-work", LinearLinkConfirmed)
	op, err := s.EnqueueLinearIssueAuditComment(ctx, "audit-product", "audit-fresh-work", "Correction with source.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// The comment-only drain read no issue, so the identity carries no
	// fresh timestamp and no content digest.
	if err := s.CompleteLinearOperation(ctx, op.OperationID, LinearRemoteIdentity{RemoteUUID: "remote-audit-uuid", HumanKey: "CON-397", URL: "https://linear.app/example/issue/CON-397"}); err != nil {
		t.Fatalf("CompleteLinearOperation() error = %v", err)
	}
	var outboxState string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&outboxState); err != nil {
		t.Fatal(err)
	}
	if outboxState != LinearOutboxDone {
		t.Fatalf("outbox state = %s, want done", outboxState)
	}
	var linkState, remoteUpdatedAt, contentHash string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT link_state, remote_updated_at, content_hash FROM linear_issue_links WHERE work_id='audit-fresh-work'`).Scan(&linkState, &remoteUpdatedAt, &contentHash); err != nil {
		t.Fatal(err)
	}
	if linkState != LinearLinkConfirmed {
		t.Fatalf("link state = %s, want confirmed", linkState)
	}
	if remoteUpdatedAt != "2026-09-09T12:00:00Z" {
		t.Fatalf("remote_updated_at = %q, want the recorded freshness kept", remoteUpdatedAt)
	}
	if contentHash != "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("content_hash = %q, want the recorded digest standing", contentHash)
	}
}
