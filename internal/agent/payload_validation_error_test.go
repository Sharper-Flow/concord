package agent

import (
	"strings"
	"testing"
)

func TestEnumValidationErrorNamesPathAndAcceptedValues(t *testing.T) {
	err := ValidateOperationPayload(
		"concord_work_define",
		"capture",
		[]byte(`{"title":"Expose schemas","value_statement":"Expose machine-readable fields.","kind":"bug","project_ids":["concord"],"idempotency_key":"schema-probe","urgency":"normal"}`),
		false,
	)
	if err == nil {
		t.Fatal("invalid urgency passed validation")
	}
	for _, want := range []string{"$.urgency", `"standard"`, `"expedite"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}
