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
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "import-product")

	// local_only refuses before any read of Linear.
	if _, err := s.ImportLinearInitiative(ctx, "import-product", "ini-uuid-1", "Example initiative", "Example description", "", "https://linear.app/example/project/ini-uuid-1"); err == nil || !strings.Contains(err.Error(), "local_only") {
		t.Fatalf("local_only error = %v, want typed refusal", err)
	}

	// linear_enabled without a declared connection refuses as missing setup.
	if _, err := s.SetProductPlanningMode(ctx, "import-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportLinearInitiative(ctx, "import-product", "ini-uuid-1", "Example initiative", "Example description", "", "https://linear.app/example/project/ini-uuid-1"); err == nil || !strings.Contains(err.Error(), "no declared Linear connection") {
		t.Fatalf("missing-setup error = %v, want typed refusal", err)
	}

	// An empty name refuses.
	setupLinearConnectionResourceAtVersion(t, s, "import-product", map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"}}, 3)
	if _, err := s.ImportLinearInitiative(ctx, "import-product", "ini-uuid-1", "", "Example description", "", "https://linear.app/example/project/ini-uuid-1"); err == nil || !strings.Contains(err.Error(), "initiative name") {
		t.Fatalf("empty-name error = %v, want typed refusal", err)
	}
}

func TestLinearInitiativeImportIsOnce(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "import-once-product")
	enableLinearForImport(t, s, "import-once-product")

	first, err := s.ImportLinearInitiative(ctx, "import-once-product", "ini-uuid-1", "Example initiative", "Example description", "", "https://linear.app/example/project/ini-uuid-1")
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
	if _, err := s.ImportLinearInitiative(ctx, "import-once-product", "ini-uuid-1", "Example initiative", "Example description", "", "https://linear.app/example/project/ini-uuid-1"); err == nil || !failureKindIs(err, KindIdempotencyConflict) {
		t.Fatalf("duplicate error = %v, want idempotency_conflict", err)
	}
}

// CD-0171 correction: the import records the imported Linear Project on the
// Initiative's project link, so the entries of an imported Initiative acquire
// its Project on their next sync instead of syncing with none forever.
func TestLinearInitiativeImportRecordsTheProjectLink(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "import-link-product")
	enableLinearForImport(t, s, "import-link-product")

	imported, err := s.ImportLinearInitiative(ctx, "import-link-product", "ini-uuid-link", "Linked initiative", "Linked description", "", "https://linear.app/example/project/ini-uuid-link")
	if err != nil {
		t.Fatalf("ImportLinearInitiative() error = %v", err)
	}
	link, err := s.ReadLinearProjectLink(ctx, imported.WorkID)
	if err != nil {
		t.Fatalf("ReadLinearProjectLink() after import error = %v", err)
	}
	if link.RemoteProjectUUID != "ini-uuid-link" || link.Name != "Linked initiative" || link.URL != "https://linear.app/example/project/ini-uuid-link" {
		t.Fatalf("imported project link = %+v", link)
	}

	// An entry of the imported Initiative syncs with the imported Project.
	seedLinearWorkItem(t, s, "import-link-entry", "import-link-product-project", "Entry title", "Entry value")
	seedLinearInitiativeEntry(t, s, imported.WorkID, "import-link-entry", true)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES('import-link-entry', 'entry-remote-1', 'EX-1', '', '', '', 'confirmed', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueLinearIssueForWork(ctx, "import-link-entry", LinearOpIssueUpdate); err != nil {
		t.Fatalf("EnqueueLinearIssueForWork(entry) error = %v", err)
	}
	// The payload carries no Project: both drains resolve it at send time
	// through the same resolution the assertion reads.
	resolved, err := s.ResolveLinearProjectIDForWork(ctx, "import-link-entry")
	if err != nil {
		t.Fatalf("ResolveLinearProjectIDForWork(entry) error = %v", err)
	}
	if resolved != "ini-uuid-link" {
		t.Fatalf("resolved entry project id = %q, want the imported ini-uuid-link", resolved)
	}
}

// The import carries the Linear Project's
// markdown content as the Initiative narrative, so a later project_update
// sends the Project's own content back instead of wiping it.
func TestLinearInitiativeImportCarriesTheNarrative(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "import-narr-product")
	enableLinearForImport(t, s, "import-narr-product")

	imported, err := s.ImportLinearInitiative(ctx, "import-narr-product", "ini-uuid-narr", "Narrative initiative", "Narrative description", "The imported narrative.", "https://linear.app/example/project/ini-uuid-narr")
	if err != nil {
		t.Fatalf("ImportLinearInitiative() error = %v", err)
	}
	var narrative string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT narrative FROM work_items WHERE id=?`, imported.WorkID).Scan(&narrative); err != nil {
		t.Fatalf("imported work item: %v", err)
	}
	if narrative != "The imported narrative." {
		t.Fatalf("imported narrative = %q, want the Linear Project content (CD-0171 d3)", narrative)
	}
	// The drained Project state carries the imported content and the
	// description as the value statement.
	state, err := s.ReadLinearInitiativeProjectState(ctx, imported.WorkID)
	if err != nil {
		t.Fatalf("ReadLinearInitiativeProjectState() error = %v", err)
	}
	if state.Narrative != "The imported narrative." || state.ValueStatement != "Narrative description" || state.Title != "Narrative initiative" {
		t.Fatalf("project state = %+v, want the imported title, description, and content", state)
	}
}
