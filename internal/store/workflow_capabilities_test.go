package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Each position holds one released gate's Ref/version predicate, independent
// of the capability owner. Step-local conditions remain in the gate callers.
func releasedWorkflowGateExpectations(d WorkflowDefinition) [8]bool {
	implementation := d.Ref == "workflow.implementation"
	breakFix := d.Ref == "workflow.break_fix"
	research := d.Ref == "workflow.research"
	return [8]bool{
		implementation && d.Version >= 28 || breakFix && d.Version >= 25,
		implementation && d.Version >= 29 || breakFix && d.Version >= 26 || research && d.Version >= 17,
		implementation && d.Version >= 31 || breakFix && d.Version >= 28 || research && d.Version >= 18,
		implementation && d.Version >= 24 || breakFix && d.Version >= 21,
		implementation && d.Version >= 21 || breakFix && d.Version >= 19,
		implementation && d.Version >= 18 || breakFix && d.Version >= 16,
		implementation || breakFix,
		research,
	}
}

func TestWorkflowCapabilitiesMatchReleasedGates(t *testing.T) {
	t.Parallel()
	for _, definition := range BuiltinWorkflowDefinitions() {
		t.Run(fmt.Sprintf("%s/v%d", definition.Ref, definition.Version), func(t *testing.T) {
			want := releasedWorkflowGateExpectations(definition)
			capabilities := [...]WorkflowCapability{
				WorkflowCapabilityDerivedStepStart,
				WorkflowCapabilityDerivedWorkerFailure,
				WorkflowCapabilityFailureAttribution,
				WorkflowCapabilityWorkerJobs,
				WorkflowCapabilityAcceptDelivery,
				WorkflowCapabilityRefineProof,
				WorkflowCapabilityProducesDelivery,
				WorkflowCapabilityRootCauseAnalysis,
			}
			var wantCapabilities workflowCapabilitySet
			for i, active := range want {
				if active {
					wantCapabilities |= workflowCapabilitySet(capabilities[i])
				}
			}
			if got := workflowCapabilities(definition); got != wantCapabilities {
				t.Errorf("capabilities = %08b, want %08b", got, wantCapabilities)
			}
			got := [4]bool{
				workflowDerivedStepStartActive(definition),
				workflowDerivedWorkerFailureActive(definition),
				workflowFailureAttributionActive(definition),
				workflowWorkerJobsActive(definition),
			}
			for i, active := range got {
				if active != want[i] {
					t.Errorf("gate %d = %t, want %t", i, active, want[i])
				}
			}
			if got := workflowDefinitionProducesDelivery(definition); got != want[6] {
				t.Errorf("produces delivery = %t, want %t", got, want[6])
			}
			steps := []string{"", "not-a-step"}
			for _, step := range definition.StepGraph.Steps {
				steps = append(steps, step.ID)
			}
			for _, step := range steps {
				refine := step != "" && workflowRefinementStepID(definition) == step
				if got := workflowAcceptDeliveryAdmissionActive(definition, step); got != (want[4] && refine) {
					t.Errorf("accept delivery at %q = %t, want %t", step, got, want[4] && refine)
				}
				if got := workflowRefineProofGateActive(definition, step); got != (want[5] && refine) {
					t.Errorf("refine proof at %q = %t, want %t", step, got, want[5] && refine)
				}
			}
		})
	}
}

func TestWorkflowRootCauseCapabilityMatchesReleasedGates(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	const shape = "capability-mismatch"
	if err := captureDefectWork(t, s, "work-bug", "bug", defectIntakeJSON(shape, nil, "")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, definition := range BuiltinWorkflowDefinitions() {
		t.Run(fmt.Sprintf("%s/v%d", definition.Ref, definition.Version), func(t *testing.T) {
			cause := fmt.Sprintf("work-rca-%d", i)
			if err := captureDefectWork(t, s, cause, "research", defectIntakeJSON(shape, []string{"work-bug"}, "")); err != nil {
				t.Fatal(err)
			}
			entry, ok := BuiltinWorkflowRegistry().Lookup(definition.Ref, definition.Version)
			if !ok {
				t.Fatal("definition is not registered")
			}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: cause, Definition: entry, Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/" + cause, ActorClass: ActorOperator}, Now: time.Unix(3, 0).UTC()})
			}); err != nil {
				t.Fatal(err)
			}
			if releasedWorkflowGateExpectations(definition)[7] {
				forceWorkLifecycleTransition(t, s, cause, "completed", "root cause analysis recorded")
			}
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			classification := &DefectClassification{FailureShape: shape, RootCauseWorkID: cause}
			err = admitRecurrenceBehindRootCauseTx(ctx, tx, "work-bug", classification, []string{"work-bug"})
			if releasedWorkflowGateExpectations(definition)[7] {
				if err != nil {
					t.Fatalf("research RCA refused: %v", err)
				}
			} else {
				assertRecurrenceDiagnostic(t, err, KindInvalidPayload, "root cause work item "+cause+" pins workflow family "+definition.Ref+", not workflow.research", shape, []string{"work-bug"})
			}
		})
	}
}
