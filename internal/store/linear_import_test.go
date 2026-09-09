package store

import (
	"context"
	"strings"
	"testing"
)

func enableLinearForImport(t *testing.T, s *Store, productID string) {
	t.Helper()
	ctx := context.Background()
	setupLinearConnectionResource(t, s, productID, map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}})
	if _, err := s.SetProductPlanningMode(ctx, productID, PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
}

func TestLinearInitiativeImportGuards(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "import-product")

	// local_only refuses before any read of Linear.
	if _, err := s.ImportLinearInitiative(ctx, "import-product", "ini-uuid-1", "Example initiative", "Example description"); err == nil || !strings.Contains(err.Error(), "local_only") {
		t.Fatalf("local_only error = %v, want typed refusal", err)
	}

	// linear_enabled without a declared connection refuses as missing setup.
	if _, err := s.SetProductPlanningMode(ctx, "import-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportLinearInitiative(ctx, "import-product", "ini-uuid-1", "Example initiative", "Example description"); err == nil || !strings.Contains(err.Error(), "no declared Linear connection") {
		t.Fatalf("missing-setup error = %v, want typed refusal", err)
	}

	// An empty name refuses.
	setupLinearConnectionResourceAtVersion(t, s, "import-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}}, 3)
	if _, err := s.ImportLinearInitiative(ctx, "import-product", "ini-uuid-1", "", "Example description"); err == nil || !strings.Contains(err.Error(), "initiative name") {
		t.Fatalf("empty-name error = %v, want typed refusal", err)
	}
}

func TestLinearInitiativeImportIsOnce(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "import-once-product")
	enableLinearForImport(t, s, "import-once-product")

	first, err := s.ImportLinearInitiative(ctx, "import-once-product", "ini-uuid-1", "Example initiative", "Example description")
	if err != nil {
		t.Fatalf("ImportLinearInitiative() error = %v", err)
	}
	if !strings.HasPrefix(first.WorkID, "initiative-") || first.ExternalRef != "linear:ini-uuid-1" {
		t.Fatalf("import = %+v", first)
	}
	var kind, externalRef, title string
	var version int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT kind, json_extract(intent_json,'$.external_ref'), title, version FROM work_items WHERE id=?`, first.WorkID).Scan(&kind, &externalRef, &title, &version); err != nil {
		t.Fatalf("imported work item: %v", err)
	}
	if kind != "initiative" || externalRef != "linear:ini-uuid-1" || title != "Example initiative" || version != 2 {
		t.Fatalf("imported = %s/%s/%s v%d", kind, externalRef, title, version)
	}
	var role string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT role FROM work_projects WHERE work_id=?`, first.WorkID).Scan(&role); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if role != "primary" {
		t.Fatalf("membership role = %s, want primary", role)
	}

	// The second import of the same identity refuses typed.
	if _, err := s.ImportLinearInitiative(ctx, "import-once-product", "ini-uuid-1", "Example initiative", "Example description"); err == nil || !failureKindIs(err, KindIdempotencyConflict) {
		t.Fatalf("duplicate error = %v, want idempotency_conflict", err)
	}
}
