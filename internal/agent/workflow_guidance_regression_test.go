package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestWorkflowActionServedGuidance(t *testing.T) {
	data, err := os.ReadFile("../../contracts/agent-tool-surface-payloads.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		action string
		terms  []string
	}{
		{"complete_step", []string{"step_reviewed", "pin's named step", "steps[].reviewed", "concord_work_trace.resume", "unreviewed"}},
		{"approve_contract", []string{"research", "128 Unicode code points", "change", "removal"}},
		{"bind_evidence", []string{"one immutable subject", "same locator", "fields.evidence_kind is omitted", "must match every entry's kind", "fields.immutable_subject_ref", "second subject"}},
		{"record_report", []string{"one immutable subject", "same locator", "fields.evidence_kind is omitted", "must match every entry's kind"}},
		{"complete", []string{"confirm_premise", "delivery"}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			text := string(doc.Defs["work_transition_action_variant_"+tc.action])
			for _, term := range tc.terms {
				if !strings.Contains(text, term) {
					t.Errorf("served %s guidance lacks %q", tc.action, term)
				}
			}
		})
	}
}
