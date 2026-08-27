package main

import (
	"testing"
)

// The knowledge-home verbs are the production producer for PM6 §2: a fresh
// installation can designate and clear a Product's durable knowledge home
// without any fixture seeding.
func TestCLIDesignatesAndClearsProductKnowledgeHome(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(dbOverrideEnv, freshMigratedCLIDatabase(t))

	creationRaw := runCLIJSON(t, []string{"product", "create"}, map[string]any{
		"product_id":                "product-1",
		"display_name":              "Concord",
		"stage_maturity":            "prototype",
		"stage_audience_commitment": "operator_only",
		"project_id":                "project-1",
		"project_display_name":      "Concord repository",
		"role":                      "primary",
	})
	assertChangedRefVersion(t, creationRaw, "product", "product-1", "2")
	runCLIJSON(t, []string{"project-locator-add"}, map[string]any{
		"project_id":       "project-1",
		"locator_id":       "law-home",
		"kind":             "canonical_path",
		"value":            repo,
		"expected_version": 1,
	})

	designateRaw := runCLIJSON(t, []string{"product-knowledge-home-designate"}, map[string]any{
		"product_id":       "product-1",
		"project_id":       "project-1",
		"locator_id":       "law-home",
		"expected_version": 2,
		"reason":           "durable law home",
	})
	assertChangedRefVersion(t, designateRaw, "product", "product-1", "3")

	clearRaw := runCLIJSON(t, []string{"product-knowledge-home-clear"}, map[string]any{
		"product_id":       "product-1",
		"expected_version": 3,
		"reason":           "operator clear",
	})
	assertChangedRefVersion(t, clearRaw, "product", "product-1", "4")
}
