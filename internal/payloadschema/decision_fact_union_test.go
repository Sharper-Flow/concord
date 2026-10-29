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
						fields["law_change_removals"] = []string{"law-remove"}
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

func TestSixClassDecisionFactSchemas(t *testing.T) {
	document := Document()
	defs := document["$defs"].(map[string]any)
	for _, action := range []string{"approve_contract", "supersede_contract"} {
		name := "work_transition_action_variant_" + action
		base := corpusSample(document, defs[name].(map[string]any)).(map[string]any)
		for _, test := range []struct {
			domains any
			valid   bool
		}{
			{domains: []string{}, valid: true},
			{domains: []string{"increased_spending"}, valid: true},
			{domains: []string{"legal"}, valid: true},
			{domains: []string{"production_data"}, valid: true},
			{domains: []string{"security"}, valid: true},
			{domains: []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux"}, valid: true},
			{domains: []string{"preference"}},
			{domains: []string{"unknown"}},
			{domains: []string{"legal", "legal"}},
			{domains: []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux", "unknown"}},
			{domains: nil},
			{domains: "security"},
			{domains: []bool{true}},
		} {
			input := corpusCopy(base).(map[string]any)
			input["fields"].(map[string]any)["decision_domains"] = test.domains
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(name, raw); (err == nil) != test.valid {
				t.Errorf("%s domains %v: valid=%v, want %v: %v", action, test.domains, err == nil, test.valid, err)
			}
		}
	}
	// Read schemas preserve stored preference, rather than reauthoring that fact.
	for _, domains := range [][]string{{"preference"}, {"preference", "product", "ui_ux"}, {"increased_spending", "legal", "product", "production_data", "security", "ui_ux"}} {
		input := corpusSample(document, defs["workflow_contract"].(map[string]any)).(map[string]any)
		input["decision_domains"] = domains
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate("workflow_contract", raw); err != nil {
			t.Errorf("stored domains %v refused by read schema: %v", domains, err)
		}
	}
}
