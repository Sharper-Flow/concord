package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestConfirmPremiseEnvelopeKeepsThePublishedWireFormat(t *testing.T) {
	t.Parallel()
	head := `{"work_id":"work-1","expected_version":7,"action_id":"confirm_premise","idempotency_key":"wire"`
	for _, schema := range []string{"work_transition_action_input", "work_transition_action_public_input"} {
		if err := payloadschema.Validate(schema, []byte(head+`}`)); err != nil {
			t.Fatalf("%s: confirm_premise must validate without a choice: %v", schema, err)
		}
		for name, extra := range map[string]string{
			"fields":           `,"fields":{}`,
			"choice":           `,"selected_choice":"confirm"`,
			"digest":           `,"decision_context_digest":"sha256:abc"`,
			"contract version": `,"contract_version":1`,
		} {
			if err := payloadschema.Validate(schema, []byte(head+extra+`}`)); err == nil {
				t.Fatalf("%s: %s must refuse", schema, name)
			}
		}
	}
}
