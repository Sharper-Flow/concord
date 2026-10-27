package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestUniqueJSONAdmissionCallers(t *testing.T) {
	t.Parallel()
	callers := map[string]func([]byte) error{
		"invoke": func(raw []byte) error {
			_, _, err := DecodeInvokeRequest(raw)
			return err
		},
		"operation": func(raw []byte) error {
			return agentcontract.ValidateOperationPayload("unregistered", "unregistered", raw, false)
		},
		"workflow fields": func(raw []byte) error {
			_, err := workflowActionFields(json.RawMessage(raw))
			return err
		},
	}
	for name, validate := range callers {
		t.Run(name, func(t *testing.T) {
			for _, test := range []struct{ input, message string }{
				{`{"a":1,"\u0061":2}`, `duplicate JSON object field "a"`},
				{`{"nested":[{"a":1,"a":2}]}`, `duplicate JSON object field "a"`},
				{`{} []`, "trailing JSON"},
				{`{} garbage`, "trailing JSON"},
			} {
				err := validate([]byte(test.input))
				if err == nil || err.Error() != test.message {
					t.Fatalf("admission(%s) = %v, want %q before downstream decoding", test.input, err, test.message)
				}
			}
		})
	}
}
