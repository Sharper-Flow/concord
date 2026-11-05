package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestEnumValidationErrorNamesPathAndAcceptedValues(t *testing.T) {
	t.Parallel()
	err := agentcontract.ValidateOperationPayload(
		"concord_work_define",
		"capture",
		[]byte(`{"title":"Expose schemas","kind":"invalid","project_ids":["concord"],"human_key":"EX-1","remote_issue_uuid":"00000000-0000-4000-8000-000000000001","url":"https://example.com/issue/EX-1","idempotency_key":"schema-probe"}`),
		false,
	)
	if err == nil {
		t.Fatal("invalid kind passed validation")
	}
	for _, want := range []string{"$.kind", `"task"`, `"bug"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}
