package payloadschema

import (
	"encoding/json"
	"testing"
)

// Every contract declares its decision fact. The initial approve_contract
// schema and complete contract updates both require the fact while the store
// validates each call against the pinned family.
// TestDecisionFactContractUnion retired by CON-1710 under CD-0243 D3:
// the decision-fact field is retired.

func TestContractAuthoringRejectsLawAttribution(t *testing.T) {
	document := Document()
	defs := document["$defs"].(map[string]any)
	for _, action := range []struct{ schema, field string }{
		{"work_transition_action_variant_approve_contract", "fields"},
		{"work_define_revise_input", "contract"},
	} {
		base := corpusSample(document, defs[action.schema].(map[string]any)).(map[string]any)
		base[action.field] = corpusSample(document, defs[action.schema].(map[string]any)["properties"].(map[string]any)[action.field].(map[string]any))
		for field, value := range map[string]any{"law_change_reason": "Retire obsolete law", "law_change_removals": []string{"law-old"}} {
			t.Run(action.schema+"/"+field, func(t *testing.T) {
				input := corpusCopy(base).(map[string]any)
				input[action.field].(map[string]any)[field] = value
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				if err := Validate(action.schema, raw); err == nil {
					t.Fatal("retired law-attribution input was admitted")
				}
			})
		}
	}
}

// TestSixClassDecisionFactSchemas retired by CON-1710 under CD-0243 D3:
// the decision-fact field is retired.

