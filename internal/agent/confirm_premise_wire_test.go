package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// TestConfirmPremiseEnvelopeKeepsThePublishedWireFormat holds the envelope
// side of the single declaration: a confirm_premise input in the previously
// published wire shape validates unchanged, the outer level carries exactly
// the two declared fields and no contract_version, and fields stays
// forbidden. The generated schema is the projection of the registry
// declaration, so this test guards the wire callers already depend on.
func TestConfirmPremiseEnvelopeKeepsThePublishedWireFormat(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("3", 64)
	head := `{"work_id":"work-1","expected_version":7,"action_id":"confirm_premise","idempotency_key":"`
	valid := []byte(head + `wire-confirm","selected_choice":"confirm","decision_context_digest":"` + digest + `"}`)
	if err := payloadschema.Validate("work_transition_action_input", valid); err != nil {
		t.Fatalf("the published confirm_premise wire shape must validate: %v", err)
	}
	for name, raw := range map[string][]byte{
		"fields stays forbidden":                []byte(head + `wire-fields","selected_choice":"confirm","decision_context_digest":"` + digest + `","fields":{}}`),
		"selected_choice is required":           []byte(head + `wire-choice","decision_context_digest":"` + digest + `"}`),
		"decision_context_digest is required":   []byte(head + `wire-digest","selected_choice":"confirm"}`),
		"an unknown choice refuses":             []byte(head + `wire-enum","selected_choice":"restart","decision_context_digest":"` + digest + `"}`),
		"a malformed digest refuses":            []byte(head + `wire-pattern","selected_choice":"confirm","decision_context_digest":"not-a-digest"}`),
		"contract_version is no outer property": []byte(head + `wire-version","selected_choice":"confirm","decision_context_digest":"` + digest + `","contract_version":1}`),
	} {
		if err := payloadschema.Validate("work_transition_action_input", raw); err == nil {
			t.Fatalf("%s must refuse", name)
		}
	}
	// revise and stop stay schema-admissible choices; the closed-choice
	// runtime guard owns which one the open question accepts.
	revise := []byte(head + `wire-revise","selected_choice":"revise","decision_context_digest":"` + digest + `"}`)
	if err := payloadschema.Validate("work_transition_action_input", revise); err != nil {
		t.Fatalf("the schema must keep admitting revise: %v", err)
	}
	stop := []byte(head + `wire-stop","selected_choice":"stop","decision_context_digest":"` + digest + `"}`)
	if err := payloadschema.Validate("work_transition_action_input", stop); err != nil {
		t.Fatalf("the schema must keep admitting stop: %v", err)
	}
}
