package store

import (
	"encoding/json"
	"strings"
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
	return record
}

// TestLawSpecAdmitsPredicateCriterionBindings holds the third binding form
// (CD-0180): a spec criterion resolves through a work-item predicate
// reference, the two fields stand or fall together, and the mixed or
// malformed shapes refuse with the typed note-proof failure.
func TestLawSpecAdmitsPredicateCriterionBindings(t *testing.T) {
	t.Parallel()
	workID := "work-" + strings.Repeat("a", 24)
	valid := []KnowledgeCriterionBinding{
		{Criterion: 1, Scenario: "scenario-one"},
		{Criterion: 2, Exemption: "A recorded reason for this exemption."},
		{Criterion: 3, WorkID: workID, PredicateID: "predicate:criterion-bindings-predicate-form"},
	}
	record := criterionBindingLawRecord(t, valid)
	if err := validateKnowledgeCriterionBindings(record); err != nil {
		t.Fatalf("the three binding forms compose one valid law record: %v", err)
	}
	bindings := record.CriterionBindings
	if len(bindings) != 3 || bindings[2].WorkID != workID || bindings[2].PredicateID != "predicate:criterion-bindings-predicate-form" {
		t.Fatalf("predicate binding did not round-trip: %+v", bindings)
	}

	for name, bindings := range map[string][]KnowledgeCriterionBinding{
		"half predicate reference":     {{Criterion: 1, WorkID: workID}},
		"predicate id without work id": {{Criterion: 1, PredicateID: "predicate:criterion-bindings-predicate-form"}},
		"scenario and predicate mixed": {{Criterion: 1, Scenario: "scenario-one", WorkID: workID, PredicateID: "predicate:criterion-bindings-predicate-form"}},
		"scenario and exemption mixed": {{Criterion: 1, Scenario: "scenario-one", Exemption: "A recorded reason for this exemption."}},
		"malformed work id":            {{Criterion: 1, WorkID: "job-1234", PredicateID: "predicate:criterion-bindings-predicate-form"}},
		"malformed predicate prefix":   {{Criterion: 1, WorkID: workID, PredicateID: "pred:criterion-bindings-predicate-form"}},
		"oversized work id":            {{Criterion: 1, WorkID: "work-" + strings.Repeat("a", 200), PredicateID: "predicate:criterion-bindings-predicate-form"}},
	} {
		record := criterionBindingLawRecord(t, bindings)
		requireFailureKind(t, validateKnowledgeCriterionBindings(record), KindInvalidNoteProof, name)
	}
}
