package store

import (
	"context"
	"reflect"
	"testing"
)

// Linear health mirrors the enqueue refusals of the label mappings: a declared
// connection with an incomplete label_ids map must read as unhealthy, because
// the capture and lifecycle folds absorb the enqueue refusal as a
// configuration no-op, health names the missing keys, and the lifecycle fold
// marks the linked work item's confirmed issue degraded.

func TestLinearHealthReportsUnmappedProjectLabels(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "label-gaps-product")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO projects(id, display_name, version, created_at, updated_at) VALUES('label-gaps-product-project-2', 'Second', 1, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z'); INSERT INTO product_projects(product_id, project_id, role) VALUES('label-gaps-product', 'label-gaps-product-project-2', 'secondary'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	// Without a declared connection health stays empty, not nil.
	before, err := s.ReadLinearIntegrationHealth(ctx, "label-gaps-product")
	if err != nil {
		t.Fatal(err)
	}
	if before.ConnectionState != LinearConnectionAbsent {
		t.Fatalf("connection state = %s, want absent", before.ConnectionState)
	}
	if !reflect.DeepEqual(before.UnmappedLabelKeys, []string{}) {
		t.Fatalf("unmapped label keys without a connection = %v, want empty", before.UnmappedLabelKeys)
	}

	// The connection maps the second member Project on purpose, so the
	// first member Project's missing project:<id> key is the only gap.
	setupLinearConnectionResource(t, s, "label-gaps-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
		"label_ids": map[string]string{"project:label-gaps-product-project-2": "label-repo-2"},
	}})
	if _, err := s.SetProductPlanningMode(ctx, "label-gaps-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	health, err := s.ReadLinearIntegrationHealth(ctx, "label-gaps-product")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"project:label-gaps-product-project"}
	if !reflect.DeepEqual(health.UnmappedLabelKeys, want) {
		t.Fatalf("unmapped label keys = %v, want %v", health.UnmappedLabelKeys, want)
	}
	if len(health.UnmappedLifecycles) != 0 {
		t.Fatalf("unmapped lifecycles = %v, want empty", health.UnmappedLifecycles)
	}
}

func TestLinearHealthReportsMissingOptionalLabel(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "optional-gaps-product")
	// The fixture maps the member Project but not the optional key.
	setupLinearConnectionResource(t, s, "optional-gaps-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
	}})
	if _, err := s.SetProductPlanningMode(ctx, "optional-gaps-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "optional-gaps-initiative", "optional-gaps-product-project", "Initiative", "Initiative value")
	seedLinearWorkItem(t, s, "optional-gaps-child", "optional-gaps-product-project", "Child", "Child value")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES('optional-gaps-initiative', 'optional-gaps-child', 0, 0)`); err != nil {
		t.Fatal(err)
	}

	health, err := s.ReadLinearIntegrationHealth(ctx, "optional-gaps-product")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{LinearLabelOptionalKey}
	if !reflect.DeepEqual(health.UnmappedLabelKeys, want) {
		t.Fatalf("unmapped label keys = %v, want %v", health.UnmappedLabelKeys, want)
	}

	// A required entry carries no optional label, so the gap clears without
	// the optional mapping.
	if _, err := s.DatabaseForTesting().Exec(`UPDATE initiative_entries SET required=1 WHERE initiative_work_id='optional-gaps-initiative' AND child_work_id='optional-gaps-child'`); err != nil {
		t.Fatal(err)
	}
	health, err = s.ReadLinearIntegrationHealth(ctx, "optional-gaps-product")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(health.UnmappedLabelKeys, []string{}) {
		t.Fatalf("unmapped label keys with a required entry = %v, want empty", health.UnmappedLabelKeys)
	}
}
