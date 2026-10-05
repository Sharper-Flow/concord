package agent

import "testing"

func TestReviewAmendmentDegradationInput(t *testing.T) {
	for _, tc := range []struct {
		input string
		valid bool
	}{
		{`{"knowledge_id":"project/CD-0001","current_amendment_context":{"limit":1,"allow_degraded":true}}`, true},
		{`{"knowledge_id":"project/CD-0001","current_amendment_context":{"limit":1,"allow_degraded":false}}`, true},
		{`{"knowledge_id":"project/CD-0001","allow_degraded":true,"current_amendment_context":{"limit":1}}`, false},
		{`{"knowledge_id":"project/CD-0001","current_amendment_context":{"limit":1,"allow_degraded":"true"}}`, false},
	} {
		if err := ValidateOperationPayload("concord_knowledge", "resolve_note", []byte(tc.input), false); (err == nil) != tc.valid {
			t.Errorf("valid=%t input=%s error=%v", tc.valid, tc.input, err)
		}
	}
}
