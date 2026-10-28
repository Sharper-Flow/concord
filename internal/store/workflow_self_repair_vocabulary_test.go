package store

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestWorkflowSelfRepairTypedErrorVocabulary(t *testing.T) {
	t.Parallel()
	repair := WorkflowSelfRepair{
		BlockedOperation: "workflow_action.dispatch_worker",
		EvidenceRefs:     []string{"evidence:vocabulary"},
	}
	for _, kind := range payloadschema.TypedErrorKinds() {
		repair.RefusalKind = kind
		if err := validateWorkflowSelfRepairShape(repair); err != nil {
			t.Errorf("public refusal kind %q rejected: %v", kind, err)
		}
	}
	for _, kind := range []string{"", "not_a_declared_kind", "INVALID_INPUT"} {
		repair.RefusalKind = kind
		err := validateWorkflowSelfRepairShape(repair)
		if err == nil || !strings.Contains(err.Error(), "self_repair does not name one typed refusal") {
			t.Errorf("undeclared refusal kind %q: got %v", kind, err)
		}
	}
}
