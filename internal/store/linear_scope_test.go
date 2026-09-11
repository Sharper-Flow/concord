package store

import (
	"context"
	"testing"
)

func TestLinearOutboxScopeRefusesCrossProductAndClaimsOnlySelectedProduct(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "scope-product-a")
	setupLinearProduct(t, s, "scope-product-b")
	setupLinearConnectionResource(t, s, "scope-product-a", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "scope-team-a", "auth_mode": "personal_api_key"}})
	setupLinearConnectionResource(t, s, "scope-product-b", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "scope-team-b", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, "scope-product-a", PlanningModeLinear, "scope test", "operator", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProductPlanningMode(ctx, "scope-product-b", PlanningModeLinear, "scope test", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedScopedWorkItem(t, s, "scope-work-a", "scope-product-a-project")
	seedScopedWorkItem(t, s, "scope-work-b", "scope-product-b-project")

	if _, err := s.EnqueueLinearIssueForProduct(ctx, "scope-product-a", "scope-work-b", LinearOpIssueCreate); err == nil || !failureKindIs(err, KindAmbiguousScope) {
		t.Fatalf("cross-Product enqueue error = %v, want ambiguous_scope", err)
	}
	if _, err := s.EnqueueLinearIssueForProduct(ctx, "scope-product-a", "scope-work-a", LinearOpIssueCreate); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueLinearIssueForProduct(ctx, "scope-product-b", "scope-work-b", LinearOpIssueCreate); err != nil {
		t.Fatal(err)
	}

	claimed, err := s.ClaimLinearOperationsForProduct(ctx, "scope-product-a", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ProductID != "scope-product-a" || claimed[0].WorkID != "scope-work-a" {
		t.Fatalf("Product A claims = %+v", claimed)
	}
	health, err := s.ReadLinearIntegrationHealth(ctx, "scope-product-b")
	if err != nil {
		t.Fatal(err)
	}
	if health.OutboxDepth != 1 {
		t.Fatalf("Product B pending depth = %d, want 1", health.OutboxDepth)
	}
}

func seedScopedWorkItem(t *testing.T, s *Store, workID, projectID string) {
	t.Helper()
	seedWorkItem(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, workID, projectID); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
