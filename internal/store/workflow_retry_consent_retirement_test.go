package store

import (
	"reflect"
	"testing"
)

func TestRetryConsentRetirementKeepsDeclaredApprovals(t *testing.T) {
	for _, consent := range []ActionApproval{ActionApprovalNone, ActionApprovalConditional, ActionApprovalRequired} {
		definition := approvalPolicyDefinition("", "record_delivery")
		definition.ActionDefinitions[0].Approval = consent
		result, err := ClassifyWorkflowApproval(definition, "record_delivery", WorkflowApprovalFacts{})
		if err != nil || result.Consent != WorkflowApprovalConsent(consent) {
			t.Fatalf("declared %s approval changed: result=%+v err=%v", consent, result, err)
		}
	}
	for _, shape := range []struct {
		value any
		field string
	}{
		{WorkflowAdmissionState{}, "FailedWorkerRetry"},
		{WorkflowAdmissionState{}, "FailedRetryApproved"},
		{WorkflowAdmissionDecision{}, "ApprovalRequired"},
		{WorkflowApprovalFacts{}, "FailedRetryBelowWall"},
		{WorkflowApprovalBoundaryState{}, "RetryBinding"},
		{WorkflowActionExecutionRequest{}, "FailedRetryApproved"},
	} {
		if _, exists := reflect.TypeOf(shape.value).FieldByName(shape.field); exists {
			t.Errorf("%T retains the retired consent field %s", shape.value, shape.field)
		}
	}
}
