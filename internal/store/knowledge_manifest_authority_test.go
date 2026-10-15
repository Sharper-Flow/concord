package store

// Declared legislative references use the same work-ID grammar as predicates.

import (
	"fmt"
	"strings"
	"testing"
)

func authorityGrammarManifest(t *testing.T, legislatedBy string, version string) []byte {
	t.Helper()
	template := `{"schema_version":"1.3","supported_kinds":["decision"],"indexed_kinds":["decision"],"domain_registry":{"schema_version":"1.0","product_key":"concord","root_domain_id":"product-root:concord","domains":[{"domain_id":"product-root:concord","name":"Concord","purpose":"Product-wide Concord law and architecture","status":"current","architecture_relations":[]}]},"records":[{"id":"CD-0001","kind":"decision","path":".concord/docs/decisions/CD-0001.md","status":"accepted","date":"2026-08-10T00:00:00Z","title":"Decision","summary":"Summary","tags":[],"authority":{"tier":"legislated","legislated_by":"%s","contract_version":%s},"scopes":{"mode":"home","product_ids":[],"project_ids":[],"domain_ids":[],"tag_ids":[]},"home_domain_id":"product-root:concord","product_wide_rationale":"Fixture law binds every child Domain.","sha256":"sha256:` + strings.Repeat("a", 64) + `"}]}`
	return []byte(fmt.Sprintf(template, legislatedBy, version))
}

func TestAuthorityGrammarRefusesNonWorkLegislatedBy(t *testing.T) {
	for _, by := range []string{"operator", "fixture-authority", "CON-908", "work contract"} {
		if _, err := parseKnowledgeManifest(authorityGrammarManifest(t, by, "1")); err == nil {
			t.Fatalf("legislated_by %q must refuse: a declared reference must name a work item", by)
		}
	}
}

func TestAuthorityGrammarRefusesMalformedWorkSuffix(t *testing.T) {
	for _, by := range []string{"work-", "work-123", "work-ABCDEFGH", "work-abcg1234", "work-0f1e2d3c4b5a6978 "} {
		if _, err := parseKnowledgeManifest(authorityGrammarManifest(t, by, "1")); err == nil {
			t.Fatalf("legislated_by %q must refuse: the work- id form is 8-64 hex characters", by)
		}
	}
}

func TestAuthorityGrammarRefusesNonPositiveOrBooleanVersion(t *testing.T) {
	for _, version := range []string{"0", "-1"} {
		if _, err := parseKnowledgeManifest(authorityGrammarManifest(t, "work-0f1e2d3c4b5a6978", version)); err == nil {
			t.Fatalf("contract_version %s must refuse: the version must be a positive integer", version)
		}
	}
	if _, err := parseKnowledgeManifest(authorityGrammarManifest(t, "work-0f1e2d3c4b5a6978", "true")); err == nil {
		t.Fatal("a boolean contract_version must refuse: the version must be a non-boolean integer")
	}
}

func TestAuthorityGrammarAcceptsWellFormedWorkReference(t *testing.T) {
	manifest, err := parseKnowledgeManifest(authorityGrammarManifest(t, "work-0f1e2d3c4b5a6978", "7"))
	if err != nil {
		t.Fatalf("a well-formed work reference with a positive version must parse: %v", err)
	}
	if manifest.Records[0].Authority.LegislatedBy != "work-0f1e2d3c4b5a6978" {
		t.Fatalf("the parsed authority must carry the declared work reference: %q", manifest.Records[0].Authority.LegislatedBy)
	}
}

func TestAuthorityGrammarKeepsDerivedAndLegacyRules(t *testing.T) {
	derived := `{"schema_version":"1.3","supported_kinds":["decision"],"indexed_kinds":["decision"],"domain_registry":{"schema_version":"1.0","product_key":"concord","root_domain_id":"product-root:concord","domains":[{"domain_id":"product-root:concord","name":"Concord","purpose":"Product-wide Concord law and architecture","status":"current","architecture_relations":[]}]},"records":[{"id":"CD-0001","kind":"decision","path":".concord/docs/decisions/CD-0001.md","status":"accepted","date":"2026-08-10T00:00:00Z","title":"Decision","summary":"Summary","tags":[],"authority":{"tier":"derived","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"scopes":{"mode":"home","product_ids":[],"project_ids":[],"domain_ids":[],"tag_ids":[]},"home_domain_id":"product-root:concord","product_wide_rationale":"Fixture law binds every child Domain.","sha256":"sha256:` + strings.Repeat("a", 64) + `"}]}`
	if _, err := parseKnowledgeManifest([]byte(derived)); err == nil {
		t.Fatal("a derived record carrying legislative fields must refuse")
	}
	tierless := strings.Replace(derived, `"authority":{"tier":"derived","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},`, "", 1)
	tierless = strings.Replace(tierless, `"schema_version":"1.3"`, `"schema_version":"1.2"`, 1)
	if _, err := parseKnowledgeManifest([]byte(tierless)); err != nil {
		t.Fatalf("a schema 1.2 record without authority must keep the applied derived default: %v", err)
	}
}
