package payloadschema

import (
	"encoding/json"
	"testing"
)

// Contract declarations admit both legacy fields and the consequence_v1
// extension. Law-change declarations belong only to the extension: making
// every added field optional on one flat object would erase that boundary.
func TestDecisionFactContractUnion(t *testing.T) {
	for _, action := range []string{"approve_contract", "supersede_contract"} {
		t.Run(action, func(t *testing.T) {
			name := "work_transition_action_variant_" + action
			document := Document()
			defs := document["$defs"].(map[string]any)
			base := corpusSample(document, defs[name].(map[string]any)).(map[string]any)
			for _, test := range []struct {
				name     string
				decision bool
				law      bool
				valid    bool
			}{
				{name: "legacy", valid: true},
				{name: "engineering extension", decision: true, valid: true},
				{name: "law extension", decision: true, law: true, valid: true},
				{name: "law without decision fact", law: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					input := corpusCopy(base).(map[string]any)
					fields := input["fields"].(map[string]any)
					if test.decision {
						fields["decision_domains"] = []string{}
					}
					if test.law {
						fields["law_change_reason"] = "Revise the bounded law."
					}
					raw, err := json.Marshal(input)
					if err != nil {
						t.Fatal(err)
					}
					if err := Validate(name, raw); (err == nil) != test.valid {
						t.Fatalf("valid=%v, want %v: %v", err == nil, test.valid, err)
					}
				})
			}
		})
	}
}
