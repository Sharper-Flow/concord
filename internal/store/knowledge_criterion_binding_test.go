package store

import (
	"encoding/json"
	"testing"
)

// criterionBindingLawRecord decodes a spec law's frontmatter carrying exactly
// the criterion bindings under test, so each case drives the retained
// validator over the authored metadata shape a committed law file carries.
func criterionBindingLawRecord(t *testing.T, bindings []KnowledgeCriterionBinding) KnowledgeRecord {
	t.Helper()
	encoded, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	front := `{"id":"spec-1","title":"Fixture spec","authority":{"tier":"derived"},"home_domain_id":"memory","applies_to_domain_ids":[],"criterion_bindings":` + string(encoded) + `}`
	var record KnowledgeRecord
	if err := json.Unmarshal([]byte(front), &record); err != nil {
		t.Fatal(err)
	}
	// Folder kind is derived, not authored metadata: a specs/-folder law
	// decodes with the spec kind the placement assigns.
	record.Kind = "spec"
	return record
}

func TestLawSpecCriterionBindings(t *testing.T) {
	t.Parallel()
	valid := []KnowledgeCriterionBinding{
		{Criterion: 1, Scenario: "scenario-one"},
		{Criterion: 2, Exemption: "A recorded reason for this exemption."},
	}
	record := criterionBindingLawRecord(t, valid)
	if err := validateKnowledgeCriterionBindings(record); err != nil {
		t.Fatalf("scenario and exemption compose one valid law record: %v", err)
	}
	for name, bindings := range map[string][]KnowledgeCriterionBinding{
		"scenario and exemption mixed": {{Criterion: 1, Scenario: "scenario-one", Exemption: "A recorded reason for this exemption."}},
	} {
		record := criterionBindingLawRecord(t, bindings)
		requireFailureKind(t, validateKnowledgeCriterionBindings(record), KindInvalidNoteProof, name)
	}
}

func TestLawSnapshotRefusesRetiredWorkPredicateCriterionBinding(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{
		".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
		".concord/law/specs/spec-1.md":                 lawSnapshotTierLawContent("SPEC-1", "Fixture spec", `"criterion_bindings":[{"criterion":1,"work_id":"work-0f1e2d3c4b5a6978","predicate_id":"predicate:one"}]`),
	})
	_, err := loadLawFolderSnapshot(t.Context(), repo, commit)
	requireFailureKind(t, err, KindInvalidNoteProof, "retired work predicate binding")
}
