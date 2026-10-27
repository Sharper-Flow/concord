package store

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// CD-0243 D2/D4 make decision_domains the consent classifier. Retired
// descriptive inputs must refuse, not silently erase a caller's declaration.
func TestLawChangeRetiredInputsRefuseCurrentContracts(t *testing.T) {
	document := payloadschema.Document()
	defs := document["$defs"].(map[string]any)
	for _, ref := range []string{"workflow.break_fix", "workflow.implementation", "workflow.research"} {
		live, err := LiveWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"approve_contract", "supersede_contract"} {
			t.Run(ref+"/"+action, func(t *testing.T) {
				fields := map[string]any{
					"premise":            "Revise the declared law inside this contract.",
					"decision_domains":   []string{},
					"law_change_reason":  "Keep the declared law consistent.",
					"law_modifies":       []string{"law:fixture"},
					"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law", "immutable_subject_ref": "commit:law", "expected_result": "pass"}}},
				}
				validate := func(raw json.RawMessage) error {
					if action == "supersede_contract" {
						return validateWorkflowContractRecoveryPayload(live.Definition, raw)
					}
					return validateWorkflowActionPayload(live.Definition, action, raw)
				}
				if action == "supersede_contract" {
					fields["contract_version"] = 2
					fields["required_evidence"] = []string{}
					fields["route_conventions"] = []string{}
					fields["spec_mandate"] = []string{}
					fields["rigor_class"] = "prototype_internal"
					fields["supersede_reason"] = "Revise the declared law."
					fields["audit_evidence"] = []string{"evidence:law"}
				}
				variant := defs["work_transition_action_variant_"+action].(map[string]any)
				published := variant["properties"].(map[string]any)["fields"].(map[string]any)
				assert := func(want bool) {
					t.Helper()
					raw := mustJSONValue(fields)
					decoder := json.NewDecoder(bytes.NewReader(raw))
					decoder.UseNumber()
					var value any
					if err := decoder.Decode(&value); err != nil {
						t.Fatal(err)
					}
					coreErr := validate(raw)
					publishedErr := payloadschema.ValidateValue(value, published, document, "fields")
					if (coreErr == nil) != want || (publishedErr == nil) != want {
						t.Errorf("fields=%s core=%v publication=%v, want admission %v", raw, coreErr, publishedErr, want)
					}
					if !want && !hasFailureKind(coreErr, KindInvalidPayload) {
						t.Errorf("retired input returned %v, want invalid_payload", coreErr)
					}
				}
				assert(true)
				for _, name := range []string{"law_change_removes_user_visible_behavior", "law_change_touches_security_or_data_safety"} {
					for _, value := range []bool{true, false} {
						fields[name] = value
						assert(false)
					}
					delete(fields, name)
				}
			})
		}
	}
}

func TestLawChangeVersionZeroDescriptiveFactsRemainReadable(t *testing.T) {
	var change WorkflowLawChange
	raw := json.RawMessage(`{"reason":"Historical law change.","removes_user_visible_behavior":true,"touches_security_or_data_safety":true,"crucial":true,"serious":true,"law_ids":[],"law_before":[],"registry_before":null}`)
	if err := decodeWorkflowActionPayloadStrict(raw, &change); err != nil {
		t.Fatal(err)
	}
	if change.Version != 0 || !change.RemovesUserVisibleBehavior || !change.TouchesSecurityOrDataSafe || !change.Crucial || !change.Serious {
		t.Fatalf("historical facts lost: %+v", change)
	}
	if err := validateWorkflowLawChangeShape(&change, nil, nil, nil); err != nil {
		t.Fatalf("historical shape refused: %v", err)
	}
}
