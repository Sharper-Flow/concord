package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestProductKnowledgeHomeCLIDesignatesWithoutFilesystemSetup(t *testing.T) {
	t.Setenv(dbOverrideEnv, freshMigratedCLIDatabase(t))
	for _, product := range []string{"alpha", "beta", "Product_7"} {
		repo := t.TempDir()
		project := product + "-source"
		locator := product + "-home"
		runCLIJSON(t, []string{"product-create"}, map[string]any{"product_id": product, "display_name": product, "stage_maturity": "prototype", "stage_audience_commitment": "operator_only", "project_id": project, "project_display_name": project, "role": "primary"})
		runCLIJSON(t, []string{"project-locator-add"}, map[string]any{"project_id": project, "locator_id": locator, "kind": "canonical_path", "value": repo, "expected_version": 1})
		for _, version := range []int{2, 3} {
			raw := runCLIJSON(t, []string{"product-knowledge-home-designate"}, map[string]any{"product_id": product, "project_id": project, "locator_id": locator, "expected_version": version})
			assertChangedRefVersion(t, raw, "product", product, "3")
			var response map[string]json.RawMessage
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			if _, exists := response["setup"]; exists {
				t.Fatalf("designation reports filesystem setup: %s", raw)
			}
			entries, err := os.ReadDir(repo)
			if err != nil || len(entries) != 0 {
				t.Fatalf("designation created files: %v %v", entries, err)
			}
		}
	}
}
