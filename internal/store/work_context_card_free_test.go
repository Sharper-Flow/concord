package store

// Global Domain navigation is retired. These regressions pin the
// card-free reader contract: a work item's view carries only its declared
// readings and findings, a complete navigation companion in a source
// repository derives nothing, a work item without declared context reads as
// the typed absent view even beside an adopted companion, and the closed
// packet admission refuses a domain_cards member.

import (
	"context"
	"encoding/json"
	"testing"
)

// workContextCompanionRepo builds one real Git repository whose checkout
// carries a complete 1.1 navigation companion and inventory, and returns the
// repo path and its committed HEAD.
func workContextCompanionRepo(t *testing.T) (string, string) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	domains := []map[string]any{}
	for _, id := range []string{workContextTestRoot, workContextTestChild, workContextTestUnaffected} {
		cardPath := ".concord/navigation/domains/" + id + ".md"
		domains = append(domains, map[string]any{"domain_id": id, "card_path": cardPath})
		writeKnowledgeFile(t, repo, cardPath, "# Card\n\nDomain: `"+id+"`. Coarse navigation owner: `domain:"+id+"`.\n")
	}
	inventory := map[string]any{"schema_version": "1.1", "domains": domains, "unresolved_count": 0, "unresolved": []any{}}
	writeKnowledgeFile(t, repo, ".concord/navigation/inventory.json", string(mustJSONValue(inventory)))
	writeKnowledgeFile(t, repo, ".concord/domain-navigation.v1.json", `{"schema_version":"1.1","rules":[],"unresolved":[],"mechanisms":[],"catalog_bindings":[],"test_coverage":[],"dependency_interpretations":[]}`)
	writeKnowledgeFile(t, repo, "README.md", "context subject\n")
	return repo, commitKnowledgeRepo(t, repo, "companion fixture")
}

// workContextPointLawHome repoints the Product's registry home at repo and
// pins its scanned commit, the way the retired proof resolved sources.
func workContextPointLawHome(t *testing.T, f workContextFixture, repo, oid string) {
	t.Helper()
	tx, err := f.store.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE project_locators SET locator_value=?,normalized_value=? WHERE locator_id='workflow-law-locator'`, repo, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE domain_registries SET scanned_commit_oid=? WHERE product_id='product'`, oid); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// Declared context survives alone: an adopted navigation companion in the
// Product home derives no readings, and the view carries exactly the
// declared content in the contract's affected order.
func TestWorkContextViewCarriesOnlyDeclaredSourcesWithoutCompanion(t *testing.T) {
	t.Parallel()
	f := seedWorkContextFixture(t, "ctx-cardfree-declared")
	defer f.store.Close()
	repo, oid := workContextCompanionRepo(t)
	workContextPointLawHome(t, f, repo, oid)
	if err := f.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{workContextFinding(workContextTestChild, "direction", "the declared direction", "")},
	}); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err != nil {
		t.Fatalf("declared context read refused: %v", err)
	}
	if view == nil {
		t.Fatal("declared context reads as absent")
	}
	if len(view.RequiredReading) != 1 || view.RequiredReading[0].DomainID != workContextTestChild || view.RequiredReading[0].Source.Path != "internal/store/work_context.go" {
		t.Fatalf("required readings = %+v, want only the declared reading", view.RequiredReading)
	}
	if len(view.Findings) != 1 || view.Findings[0].Statement != "the declared direction" {
		t.Fatalf("findings = %+v, want only the declared finding", view.Findings)
	}
	if len(view.DomainGroups) != 1 || view.DomainGroups[0].DomainID != workContextTestChild {
		t.Fatalf("domain groups = %+v, want one child group in the affected order", view.DomainGroups)
	}
}

// A work item without declared context reads as the typed absent view even
// when its Product home carries a complete navigation companion.
func TestWorkContextViewStaysAbsentBesideCompanionOnlyHome(t *testing.T) {
	t.Parallel()
	f := seedWorkContextFixture(t, "ctx-cardfree-absent")
	defer f.store.Close()
	repo, oid := workContextCompanionRepo(t)
	workContextPointLawHome(t, f, repo, oid)
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err != nil {
		t.Fatalf("companion-only home refused the absent read: %v", err)
	}
	if view != nil {
		t.Fatalf("view = %+v, want the typed absent view", view)
	}
}

// The closed packet admission refuses a work-context view carrying a
// domain_cards member: cards are retired, never accepted.
func TestWorkerPacketWorkContextRefusesDomainCards(t *testing.T) {
	t.Parallel()
	f := seedWorkContextFixture(t, "ctx-cardfree-packet")
	defer f.store.Close()
	if err := f.recordWorkContext(t, map[string]any{
		"required_reading": []map[string]any{workContextRepoReading(workContextTestChild, "")},
		"finding_refs":     []string{},
		"context_findings": []map[string]any{workContextFinding(workContextTestChild, "direction", "the declared direction", "")},
	}); err != nil {
		t.Fatalf("declaration refused: %v", err)
	}
	view, err := readWorkContextView(context.Background(), f.store.DatabaseForTesting(), f.workID)
	if err != nil || view == nil {
		t.Fatalf("declared context = %+v err = %v", view, err)
	}
	var generic map[string]any
	if err := json.Unmarshal(mustJSONValue(view), &generic); err != nil {
		t.Fatal(err)
	}
	groups, _ := generic["domain_groups"].([]any)
	if len(groups) == 0 {
		t.Fatal("declared context lost its domain group")
	}
	for _, group := range groups {
		group.(map[string]any)["domain_cards"] = []any{}
	}
	_, err = admittedWorkerPacketWorkContext(context.Background(), f.store.DatabaseForTesting(), f.workID, mustJSONValue(map[string]any{"inputs": map[string]any{"work_context": generic}}))
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("card-bearing packet = %v, want %s", err, KindInvalidPayload)
	}
}
