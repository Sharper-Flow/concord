package store

// The retained record taxonomy rules the committed law read still enforces:
// a non-law record authors no law-home fields, and law_relations stay a
// decision/spec graph. Status and dispositions retired with the manifest
// document: the law folder derives status from placement and knows nothing
// of dispositions.

import (
	"encoding/json"
	"testing"
)

func taxonomyLawRecord(t *testing.T, front string) KnowledgeRecord {
	t.Helper()
	var record KnowledgeRecord
	if err := json.Unmarshal([]byte(front), &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func taxonomyRegistry() KnowledgeDomainRegistry {
	return KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
		Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product-wide Concord law and architecture", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
	}
}

// A non-law record carries no law-home fields, whatever its kind. The rule
// used to name lessons; reference and research are non-law for the same
// reason and must inherit it rather than acquire a law home by omission.
func TestNonLawRecordsCannotAuthorLawHomeFields(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"lesson", "reference", "research"} {
		t.Run(kind, func(t *testing.T) {
			front := `{"id":"record-1","title":"Record","authority":{"tier":"derived"},"kind":"` + kind + `","home_domain_id":"product-root:concord","applies_to_domain_ids":[]}`
			record := taxonomyLawRecord(t, front)
			if err := validateManifestLawHome(record, taxonomyRegistry()); err == nil {
				t.Fatalf("%s authored a law home", kind)
			}
		})
	}
}

// law_relations stay a decision/spec graph. A constitution is law-bearing for
// status purposes without joining that graph, so authoring a relation on one
// is a failure rather than an unremarked extension of the law model.
func TestLawRelationsRemainDecisionAndSpecOnly(t *testing.T) {
	t.Parallel()
	constitution := taxonomyLawRecord(t, `{"id":"CO-0001","title":"Constitution","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"kind":"constitution","home_domain_id":"product-root:concord","applies_to_domain_ids":[],"product_wide_rationale":"Fixture law binds every child Domain.","law_relations":[{"kind":"refines","target_id":"CD-0001"}]}`)
	decision := taxonomyLawRecord(t, `{"id":"CD-0001","title":"Decision","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"kind":"decision","home_domain_id":"product-root:concord","applies_to_domain_ids":[],"product_wide_rationale":"Fixture law binds every child Domain."}`)
	byID := map[string]KnowledgeRecord{"CO-0001": constitution, "CD-0001": decision}
	seen := map[string]bool{}
	graph := map[string][]string{}
	if err := validateManifestRecordRelations(constitution, byID, seen, graph); err == nil {
		t.Fatal("a constitution authored a law relation")
	}
	seen = map[string]bool{}
	graph = map[string][]string{}
	if err := validateManifestRecordRelations(decision, byID, seen, graph); err != nil {
		t.Fatalf("a decision's own relation was refused: %v", err)
	}
}
