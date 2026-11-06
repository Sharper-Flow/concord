package payloadschema

import (
	"encoding/json"
	"testing"
)

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
