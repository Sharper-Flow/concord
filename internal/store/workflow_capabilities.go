package store

import "slices"

// WorkflowCapability names one behavior a definition enables and publishes
// in its canonical manifest.
type WorkflowCapability string

const (
	WorkflowCapabilityDerivedStepStart  WorkflowCapability = "derived_step_start"
	WorkflowCapabilityProducesDelivery  WorkflowCapability = "produces_delivery"
	WorkflowCapabilityRefineProof       WorkflowCapability = "refine_proof"
	WorkflowCapabilityRootCauseAnalysis WorkflowCapability = "root_cause_analysis"
)

// workflowCapabilityNames is the closed capability vocabulary in canonical
// (sorted) order.
var workflowCapabilityNames = []WorkflowCapability{
	WorkflowCapabilityDerivedStepStart,
	WorkflowCapabilityProducesDelivery,
	WorkflowCapabilityRefineProof,
	WorkflowCapabilityRootCauseAnalysis,
}

type workflowCapabilitySet []WorkflowCapability

func (set workflowCapabilitySet) has(capability WorkflowCapability) bool {
	return slices.Contains(set, capability)
}

func workflowCapabilities(definition WorkflowDefinition) workflowCapabilitySet {
	return definition.Capabilities
}

// workflowCapabilityUnion returns the canonical sorted union of sets.
func workflowCapabilityUnion(sets ...[]WorkflowCapability) []WorkflowCapability {
	union := []WorkflowCapability{}
	for _, name := range workflowCapabilityNames {
		for _, set := range sets {
			if slices.Contains(set, name) {
				union = append(union, name)
				break
			}
		}
	}
	return union
}

// validWorkflowCapabilities reports whether capabilities is a canonical
// sorted, duplicate-free subset of the closed vocabulary.
func validWorkflowCapabilities(capabilities []WorkflowCapability) bool {
	return slices.Equal(workflowCapabilityUnion(capabilities), capabilities)
}
