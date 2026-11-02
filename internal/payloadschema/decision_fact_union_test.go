package payloadschema

import (
	"encoding/json"
	"testing"
)

// Every contract declares its decision fact. The initial approve_contract
// schema and complete contract updates both require the fact while the store
// validates each call against the pinned family.
func TestDecisionFactContractUnion(t *testing.T) {
	for _, action := range []struct {
		name              string
		schema            string
		field             string
		factBranches      int
		noFactBranches    int
		noFactSchemaValid bool
	}{
		{name: "approve_contract", schema: "work_transition_action_variant_approve_contract", field: "fields", factBranches: 1},
		{name: "revise_intent", schema: "work_define_revise_input", field: "contract", factBranches: 1},
	} {
		t.Run(action.name, func(t *testing.T) {
			name := action.schema
			document := Document()
			defs := document["$defs"].(map[string]any)
			variant := defs[name].(map[string]any)
			fieldsNode := variant["properties"].(map[string]any)[action.field].(map[string]any)
			if ref, ok := fieldsNode["$ref"].(string); ok {
				fieldsNode = defs[ref[len("#/$defs/"):]].(map[string]any)
			}
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
			base[action.field] = corpusSample(document, fieldsNode)
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
					fields := input[action.field].(map[string]any)
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
	for _, test := range []struct{ name, field string }{{"work_transition_action_variant_approve_contract", "fields"}, {"work_define_revise_input", "contract"}} {
		name := test.name
		base := corpusSample(document, defs[name].(map[string]any)).(map[string]any)
		base[test.field] = corpusSample(document, defs[name].(map[string]any)["properties"].(map[string]any)[test.field].(map[string]any))
		field := test.field
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
			input[field].(map[string]any)["decision_domains"] = test.domains
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(name, raw); (err == nil) != test.valid {
				t.Errorf("%s domains %v: valid=%v, want %v: %v", name, test.domains, err == nil, test.valid, err)
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
