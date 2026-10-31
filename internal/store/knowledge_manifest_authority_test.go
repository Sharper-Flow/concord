package store

// Declared legislative references use the same work-ID grammar as predicates.
// The grammar is one retained validator applied to the frontmatter a law file
// authors; the retired manifest parse applied the same rules.

import (
	"encoding/json"
	"strings"
	"testing"
)

func authorityGrammarLawRecord(t *testing.T, legislatedBy, version string) (KnowledgeRecord, error) {
	t.Helper()
	front := `{"id":"CD-0001","kind":"decision","title":"Decision","authority":{"tier":"legislated","legislated_by":"` + legislatedBy + `","contract_version":` + version + `},"home_domain_id":"product-root:concord","applies_to_domain_ids":[],"product_wide_rationale":"Fixture law binds every child Domain."}`
	var record KnowledgeRecord
	err := json.Unmarshal([]byte(front), &record)
	return record, err
}

func TestAuthorityGrammarRefusesNonWorkLegislatedBy(t *testing.T) {
	for _, by := range []string{"operator", "fixture-authority", "CON-908", "work contract"} {
		record, err := authorityGrammarLawRecord(t, by, "1")
		if err != nil {
			t.Fatalf("legislated_by %q must decode: %v", by, err)
		}
		if err := validateKnowledgeRecordAuthority(record); err == nil {
			t.Fatalf("legislated_by %q must refuse: a declared reference must name a work item", by)
		}
	}
}

func TestAuthorityGrammarRefusesMalformedWorkSuffix(t *testing.T) {
	for _, by := range []string{"work-", "work-123", "work-ABCDEFGH", "work-abcg1234", "work-0f1e2d3c4b5a6978 "} {
		record, err := authorityGrammarLawRecord(t, by, "1")
		if err != nil {
			t.Fatalf("legislated_by %q must decode: %v", by, err)
		}
		if err := validateKnowledgeRecordAuthority(record); err == nil {
			t.Fatalf("legislated_by %q must refuse: the work- id form is 8-64 hex characters", by)
		}
	}
}

func TestAuthorityGrammarRefusesNonPositiveOrBooleanVersion(t *testing.T) {
	for _, version := range []string{"0", "-1"} {
		record, err := authorityGrammarLawRecord(t, "work-0f1e2d3c4b5a6978", version)
		if err != nil {
			t.Fatalf("contract_version %s must decode as JSON: %v", version, err)
		}
		if err := validateKnowledgeRecordAuthority(record); err == nil {
			t.Fatalf("contract_version %s must refuse: the version must be a positive integer", version)
		}
	}
	// A boolean contract_version is not a non-boolean integer: the frontmatter
	// decode itself refuses it.
	if _, err := authorityGrammarLawRecord(t, "work-0f1e2d3c4b5a6978", "true"); err == nil {
		t.Fatal("a boolean contract_version must refuse: the version must be a non-boolean integer")
	}
}

func TestAuthorityGrammarAcceptsWellFormedWorkReference(t *testing.T) {
	record, err := authorityGrammarLawRecord(t, "work-0f1e2d3c4b5a6978", "7")
	if err != nil {
		t.Fatalf("a well-formed work reference with a positive version must decode: %v", err)
	}
	if err := validateKnowledgeRecordAuthority(record); err != nil {
		t.Fatalf("a well-formed work reference with a positive version must pass: %v", err)
	}
	if record.Authority.LegislatedBy != "work-0f1e2d3c4b5a6978" {
		t.Fatalf("the decoded authority must carry the declared work reference: %q", record.Authority.LegislatedBy)
	}
}

func TestAuthorityGrammarKeepsTheDerivedRule(t *testing.T) {
	derived := `{"id":"CD-0001","kind":"decision","title":"Decision","authority":{"tier":"derived","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product-root:concord","applies_to_domain_ids":[],"product_wide_rationale":"Fixture law binds every child Domain."}`
	var record KnowledgeRecord
	if err := json.Unmarshal([]byte(derived), &record); err != nil {
		t.Fatal(err)
	}
	if err := validateKnowledgeRecordAuthority(record); err == nil {
		t.Fatal("a derived record carrying legislative fields must refuse")
	}
}
