package store

import (
	"slices"
	"testing"
)

// Each position holds one current family's expected gate, independent
// of the capability owner. Step-local conditions remain in the gate callers.
func releasedWorkflowGateExpectations(d WorkflowDefinition) [3]bool {
	implementation := d.Ref == "workflow.implementation"
	breakFix := d.Ref == "workflow.break_fix"
	return [3]bool{
		implementation || breakFix,
		implementation || breakFix,
		implementation || breakFix,
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
