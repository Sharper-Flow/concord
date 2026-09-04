package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func seedCurrentWorkflowDomainFixture(t *testing.T, s *store.Store) {
	t.Helper()
	if err := pm1fixture.SeedCurrentProductDomain(context.Background(), s, "product-1", "project-1"); err != nil {
		t.Fatalf("pm1fixture.SeedCurrentProductDomain: %v", err)
	}
}

func workflowArchitectureBindingFixture() map[string]any {
	return map[string]any{
		"domain_registry_content_hash": pm1fixture.FixtureDomainRegistryContentHash,
		"home_domain_id":               pm1fixture.FixtureRootDomainID,
		"affected_domain_ids":          []string{pm1fixture.FixtureRootDomainID},
		"domain_modifies":              []string{},
		"domain_relation_modifies":     []any{},
		"law_additions":                []any{},
		"verification_obligations":     []any{},
	}
}

func addWorkflowContractApprovalFields(input map[string]any) {
	input["fields"] = workflowContractFieldsFixture()
}

func workflowContractFieldsFixture() map[string]any {
	return map[string]any{
		"architecture_binding": workflowArchitectureBindingFixture(),
		"outcome_predicates":   []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:fixture", "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}}},
		"spec_mandate":         []string{},
		"law_modifies":         []string{},
	}
}

func workflowContractActionInput(t *testing.T, workID string, expectedVersion int64, key, approvalRef string) json.RawMessage {
	t.Helper()
	input := map[string]any{
		"work_id":          workID,
		"expected_version": expectedVersion,
		"action_id":        "approve_contract",
		"fields":           workflowContractFieldsFixture(),
		"idempotency_key":  key,
	}
	if approvalRef != "" {
		input["approval"] = map[string]any{"approval_ref": approvalRef}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestArchitectureBindingSchemaPreservesCurrentIdentifierBounds(t *testing.T) {
	domainID := strings.Repeat("d", 256)
	lawID := strings.Repeat("l", 256)
	obligationID := strings.Repeat("o", 256)
	binding := workflowArchitectureBindingFixture()
	binding["home_domain_id"] = domainID
	binding["affected_domain_ids"] = []string{domainID}
	binding["domain_modifies"] = []string{domainID}
	binding["law_additions"] = []map[string]any{{"law_id": lawID, "home_domain_id": domainID}}
	binding["verification_obligations"] = []map[string]any{{"law_id": lawID, "obligation_id": obligationID}}
	raw, err := json.Marshal(map[string]any{
		"work_id":          "work-1",
		"expected_version": 7,
		"action_id":        "approve_contract",
		"fields": map[string]any{
			"architecture_binding": binding,
			"spec_mandate":         []string{lawID},
			"law_modifies":         []string{lawID},
		},
		"idempotency_key": "wide-architecture-ids",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("work_transition_action_input", raw); err != nil {
		t.Fatalf("current-width architecture identifiers rejected: %v", err)
	}
	for _, invalidDomainID := range []string{" " + domainID, domainID + " "} {
		invalid := workflowArchitectureBindingFixture()
		invalid["home_domain_id"] = invalidDomainID
		invalid["affected_domain_ids"] = []string{invalidDomainID}
		payload, err := json.Marshal(map[string]any{
			"work_id": "work-1", "expected_version": 7, "action_id": "approve_contract",
			"fields":          map[string]any{"architecture_binding": invalid, "spec_mandate": []string{}, "law_modifies": []string{}},
			"idempotency_key": "invalid-domain-id",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePayloadSchema("work_transition_action_input", payload); err == nil {
			t.Fatalf("Domain identifier with boundary whitespace accepted: %q", invalidDomainID)
		}
	}
}

func TestApproveContractSchemaAcceptsExplicitEvidenceAndRigor(t *testing.T) {
	fields := workflowContractFieldsFixture()
	fields["required_evidence"] = []string{"verification", "review"}
	fields["rigor_class"] = "prototype_internal"
	payload := func(key string) []byte {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"work_id":          "work-1",
			"expected_version": 7,
			"action_id":        "approve_contract",
			"fields":           fields,
			"idempotency_key":  key,
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := ValidatePayloadSchema("work_transition_action_input", payload("complete-approve-contract-fields")); err != nil {
		t.Fatalf("approve_contract evidence or rigor rejected: %v", err)
	}

	fields["required_evidence"] = []string{"unknown"}
	if err := ValidatePayloadSchema("work_transition_action_input", payload("unknown-evidence-kind")); err == nil {
		t.Fatal("approve_contract schema accepted an unknown evidence kind")
	}
	fields["required_evidence"] = []string{"verification", "review"}
	fields["rigor_class"] = "unknown"
	if err := ValidatePayloadSchema("work_transition_action_input", payload("unknown-rigor-class")); err == nil {
		t.Fatal("approve_contract schema accepted an unknown rigor class")
	}
	fields["rigor_class"] = "prototype_internal"
	fields["unknown_contract_field"] = true
	if err := ValidatePayloadSchema("work_transition_action_input", payload("unknown-approve-contract-field")); err == nil {
		t.Fatal("approve_contract schema accepted an unknown field")
	}
}
