package store

import (
	"context"
	"testing"
)

func TestTerminalFailureDoesNotAddRetryConsent(t *testing.T) {
	const workID = "terminal-failure-without-consent"
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := seedAuthorizedFailedWorkerWithDefinition(t, workID, entry)
	// Failure history must add no gate of its own.
	definition := entry.Definition
	state, _, _, err := loadWorkflowAdmissionStateTxForRegistry(context.Background(), s.DatabaseForTesting(), workID, definition, "execution", "retry_consent_test", BuiltinWorkflowRegistry())
	if err != nil {
		t.Fatal(err)
	}
	decision := workflowAdmit(definition, state, "dispatch_worker")
	if !decision.Admitted || decision.Failure != nil {
		t.Fatalf("terminal failure added a retry gate: %#v", decision)
	}
	state.PendingWorkerAuthorization = true
	decision = workflowAdmit(definition, state, "dispatch_worker")
	if decision.Admitted || !hasFailureKind(decision.Failure, KindOperationConflict) {
		t.Fatalf("unknown launch effect must still require reconciliation: %#v", decision)
	}
}
