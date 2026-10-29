package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

// Each position holds one current family's expected gate, independent
// of the capability owner. Step-local conditions remain in the gate callers.
func releasedWorkflowGateExpectations(d WorkflowDefinition) [4]bool {
	implementation := d.Ref == "workflow.implementation"
	breakFix := d.Ref == "workflow.break_fix"
	research := d.Ref == "workflow.research"
	return [4]bool{
		implementation || breakFix,
		implementation || breakFix,
		implementation || breakFix,
		research,
	}
}

func TestWorkflowCapabilitiesMatchReleasedGates(t *testing.T) {
	t.Parallel()
	for _, definition := range BuiltinWorkflowDefinitions() {
		t.Run(definition.Ref, func(t *testing.T) {
			want := releasedWorkflowGateExpectations(definition)
			capabilities := [...]WorkflowCapability{
				WorkflowCapabilityDerivedStepStart,
				WorkflowCapabilityRefineProof,
				WorkflowCapabilityProducesDelivery,
				WorkflowCapabilityRootCauseAnalysis,
			}
			var wantCapabilities workflowCapabilitySet
			for i, active := range want {
				if active {
					wantCapabilities = append(wantCapabilities, capabilities[i])
				}
			}
			slices.Sort(wantCapabilities)
			if got := workflowCapabilities(definition); !slices.Equal(got, wantCapabilities) {
				t.Errorf("capabilities = %v, want %v", got, wantCapabilities)
			}
			if got := workflowDerivedStepStartActive(definition); got != want[0] {
				t.Errorf("derived start = %t, want %t", got, want[0])
			}
			if got := workflowDefinitionProducesDelivery(definition); got != want[2] {
				t.Errorf("produces delivery = %t, want %t", got, want[2])
			}
			steps := []string{"", "not-a-step"}
			for _, step := range definition.StepGraph.Steps {
				steps = append(steps, step.ID)
			}
			for _, step := range steps {
				refine := step != "" && workflowRefinementStepID(definition) == step
				if got := workflowRefineProofGateActive(definition, step); got != (want[1] && refine) {
					t.Errorf("refine proof at %q = %t, want %t", step, got, want[1] && refine)
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
		t.Run(definition.Ref, func(t *testing.T) {
			cause := fmt.Sprintf("work-rca-%d", i)
			if err := captureDefectWork(t, s, cause, "research", defectIntakeJSON(shape, []string{"work-bug"}, "")); err != nil {
				t.Fatal(err)
			}
			entry, ok := BuiltinWorkflowRegistry().Current(definition.Ref)
			if !ok {
				t.Fatal("definition is not registered")
			}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: cause, Definition: entry, Actor: WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/" + cause, ActorClass: ActorOperator}, Now: time.Unix(3, 0).UTC()})
			}); err != nil {
				t.Fatal(err)
			}
			if releasedWorkflowGateExpectations(definition)[3] {
				forceWorkLifecycleTransition(t, s, cause, "completed", "root cause analysis recorded")
			}
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			classification := &DefectClassification{FailureShape: shape, RootCauseWorkID: cause}
			err = admitRecurrenceBehindRootCauseTx(ctx, tx, "work-bug", classification, []string{"work-bug"})
			if releasedWorkflowGateExpectations(definition)[3] {
				if err != nil {
					t.Fatalf("research RCA refused: %v", err)
				}
			} else {
				assertRecurrenceDiagnostic(t, err, KindInvalidPayload, "root cause work item "+cause+" pins workflow family "+definition.Ref+", not workflow.research", shape, []string{"work-bug"})
			}
		})
	}
}
