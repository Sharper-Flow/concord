package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// criterionBindingManifestBytes encodes a valid schema-1.3 manifest whose one
// record is a spec carrying exactly the criterion bindings under test, so
// each case drives the parse-path validation over one variable.
func criterionBindingManifestBytes(t *testing.T, bindings []KnowledgeCriterionBinding) []byte {
	t.Helper()
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.3",
		SupportedKinds: []string{"spec"},
		IndexedKinds:   []string{"spec"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "memory", Name: "Memory", Purpose: "Registry law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{{
			ID: "spec-1", Kind: "spec", Path: ".concord/docs/specs/fixture.md", Status: "accepted",
			Date: "2026-08-10T00:00:00Z", Title: "Fixture spec", Summary: "Spec summary", Tags: []string{},
			Authority:    KnowledgeAuthority{Tier: "derived"},
			Scopes:       KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
			HomeDomainID: "memory", CriterionBindings: bindings,
			SHA256: "sha256:" + strings.Repeat("b", 64),
		}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// TestKnowledgeManifestAdmitsPredicateCriterionBindings holds the third
// binding form (CD-0180): a spec criterion resolves through a work-item
// predicate reference, the two fields stand or fall together, and the mixed
// or malformed shapes refuse with the typed note-proof failure.
func TestKnowledgeManifestAdmitsPredicateCriterionBindings(t *testing.T) {
	t.Parallel()
	workID := "work-" + strings.Repeat("a", 24)
	valid := []KnowledgeCriterionBinding{
		{Criterion: 1, Scenario: "scenario-one"},
		{Criterion: 2, Exemption: "A recorded reason for this exemption."},
		{Criterion: 3, WorkID: workID, PredicateID: "predicate:criterion-bindings-predicate-form"},
	}
	manifest, err := parseKnowledgeManifest(criterionBindingManifestBytes(t, valid))
	if err != nil {
		t.Fatalf("the three binding forms compose one valid manifest: %v", err)
	}
	bindings := manifest.Records[0].CriterionBindings
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
		t.Run(name, func(t *testing.T) {
			_, err := parseKnowledgeManifest(criterionBindingManifestBytes(t, bindings))
			assertFailureKind(t, err, KindInvalidNoteProof)
		})
	}
}
