package store

import (
	"context"
	"reflect"
	"testing"
)

func TestTerminalFailureDoesNotAddRetryConsent(t *testing.T) {
	const workID = "terminal-failure-without-consent"
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := seedAuthorizedFailedWorkerWithDefinition(t, workID, entry)
	// Declared substantive approvals remain authoritative even on definitions
	// without the consequence policy; failure history must add none of its own.
	definition := entry.Definition
	definition.ApprovalPolicy = ""
	state, _, _, err := loadWorkflowAdmissionStateTxForRegistry(context.Background(), s.DatabaseForTesting(), workID, definition, "execution", "retry_consent_test", BuiltinWorkflowRegistry())
	if err != nil {
		t.Fatal(err)
	}
	decision := workflowAdmit(definition, state, "dispatch_worker")
	if !decision.Admitted || decision.Failure != nil {
		t.Fatalf("terminal failure added a retry consent gate: %#v", decision)
	}
	state.PendingWorkerAuthorization = true
	decision = workflowAdmit(definition, state, "dispatch_worker")
	if decision.Admitted || !hasFailureKind(decision.Failure, KindOperationConflict) {
		t.Fatalf("unknown launch effect must still require reconciliation: %#v", decision)
	}
}

func TestRetryConsentRetirementKeepsDeclaredApprovals(t *testing.T) {
	for _, consent := range []ActionApproval{ActionApprovalNone, ActionApprovalConditional, ActionApprovalRequired} {
		definition := approvalPolicyDefinition("", "dispatch_worker")
		definition.ActionDefinitions[0].Approval = consent
		result, err := ClassifyWorkflowApproval(definition, "dispatch_worker", WorkflowApprovalFacts{})
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
