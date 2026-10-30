package payloadschema

import (
	"encoding/json"
	"testing"
)

// Every contract declares its decision fact. The initial approve_contract
// schema requires it in one closed fields object; the supersede_contract
// fact is definition-conditional, so the advertised fields is one closed
// union whose fact branch serves the families that declare the fact while
// the store validates each call against the pinned family.
func TestDecisionFactContractUnion(t *testing.T) {
	for _, action := range []struct {
		name              string
		factBranches      int
		noFactBranches    int
		noFactSchemaValid bool
	}{
		{name: "approve_contract", factBranches: 1, noFactBranches: 0},
		{name: "supersede_contract", factBranches: 1, noFactBranches: 1, noFactSchemaValid: true},
	} {
		t.Run(action.name, func(t *testing.T) {
			name := "work_transition_action_variant_" + action.name
			document := Document()
			defs := document["$defs"].(map[string]any)
			variant := defs[name].(map[string]any)
			fieldsNode := variant["properties"].(map[string]any)["fields"].(map[string]any)
			fieldsRequired := map[string]bool{}
			if fieldsNodeRequired, ok := fieldsNode["required"].([]any); ok {
				for _, field := range fieldsNodeRequired {
					fieldsRequired[field.(string)] = true
				}
			}
			factBranches, noFactBranches := 0, 0
			if branches, ok := fieldsNode["oneOf"].([]any); ok {
				for _, branch := range branches {
					required := map[string]bool{}
					if branchRequired, ok := branch.(map[string]any)["required"].([]any); ok {
						for _, field := range branchRequired {
							required[field.(string)] = true
						}
					}
					if required["decision_domains"] {
						factBranches++
					} else {
						noFactBranches++
					}
				}
			} else if fieldsRequired["decision_domains"] {
				factBranches = 1
			}
			if factBranches != action.factBranches || noFactBranches != action.noFactBranches {
				t.Fatalf("fact branches=%d no-fact branches=%d, want %d/%d", factBranches, noFactBranches, action.factBranches, action.noFactBranches)
			}
			base := corpusSample(document, variant).(map[string]any)
			for _, test := range []struct {
				name     string
				decision bool
				law      bool
				valid    bool
			}{
				{name: "no decision fact", valid: action.noFactSchemaValid},
				{name: "engineering fact", decision: true, valid: true},
				{name: "law with its fact", decision: true, law: true, valid: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					input := corpusCopy(base).(map[string]any)
					fields := input["fields"].(map[string]any)
					if test.decision {
						fields["decision_domains"] = []string{}
					} else {
						delete(fields, "decision_domains")
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
