package store

import (
	"context"
	"testing"
)

func enableTestLinearProduct(t *testing.T, s *Store, productID string) {
	t.Helper()
	if _, err := s.SetProductPlanningMode(context.Background(), productID, PlanningModeLinear, "native alignment test", "operator", 2); err != nil {
		t.Fatal(err)
	}
}

func TestLinearCreationIntentIsStableAcrossRepeatedEnqueue(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "stable-product")
	setupLinearConnectionResource(t, s, "stable-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-stable", "project_id": "project-stable", "auth_mode": "personal_api_key",
	}})
	enableTestLinearProduct(t, s, "stable-product")
	seedLinearWorkItem(t, s, "stable-work", "stable-product-project", "Stable", "Stable value")

	first, err := s.EnqueueLinearIssueForWork(ctx, "stable-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnqueueLinearIssueForWork(ctx, "stable-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	if first.OperationID != second.OperationID || first.IdempotencyKey != second.IdempotencyKey {
		t.Fatalf("repeated intent = %+v / %+v", first, second)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_outbox WHERE work_id=? AND op_kind=?`, "stable-work", LinearOpIssueCreate).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("creation operations = %d, want 1", count)
	}
}

func TestAdoptLinearIssueIsIdempotentAndRejectsConflictingWork(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "adopt-product")
	setupLinearConnectionResource(t, s, "adopt-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-adopt", "project_id": "project-adopt", "auth_mode": "personal_api_key",
	}})
	enableTestLinearProduct(t, s, "adopt-product")
	seedLinearWorkItem(t, s, "adopt-work", "adopt-product-project", "Adopt", "Adopt value")
	seedLinearWorkItem(t, s, "adopt-other", "adopt-product-project", "Other", "Other value")
	identity := LinearRemoteIdentity{RemoteUUID: "remote-adopt", HumanKey: "CON-1", URL: "https://linear.app/example/issue/CON-1", TeamID: "team-adopt", ProjectID: "project-adopt"}
	if err := s.AdoptLinearIssue(ctx, "adopt-product", "adopt-work", identity); err != nil {
		t.Fatal(err)
	}
	if err := s.AdoptLinearIssue(ctx, "adopt-product", "adopt-work", identity); err != nil {
		t.Fatalf("repeat adoption = %v", err)
	}
	if err := s.AdoptLinearIssue(ctx, "adopt-product", "adopt-other", identity); err == nil || !isLinearFailureKind(err, KindIdempotencyConflict) {
		t.Fatalf("conflicting adoption = %v, want idempotency conflict", err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM linear_outbox`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("adoption created %d outbox operations", count)
	}
}
