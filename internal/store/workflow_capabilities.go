package store

import "slices"

// WorkflowCapability names one behavior a compiled definition enables. The
// compiled definition publishes the union over its parts in its canonical
// manifest, so its digest covers every capability it enables.
type WorkflowCapability string

const (
	WorkflowCapabilityAcceptDelivery       WorkflowCapability = "accept_delivery"
	WorkflowCapabilityDerivedStepStart     WorkflowCapability = "derived_step_start"
	WorkflowCapabilityDerivedWorkerFailure WorkflowCapability = "derived_worker_failure"
	WorkflowCapabilityFailureAttribution   WorkflowCapability = "failure_attribution"
	WorkflowCapabilityProducesDelivery     WorkflowCapability = "produces_delivery"
	WorkflowCapabilityRefineProof          WorkflowCapability = "refine_proof"
	WorkflowCapabilityRootCauseAnalysis    WorkflowCapability = "root_cause_analysis"
	WorkflowCapabilityWorkerJobs           WorkflowCapability = "worker_jobs"
)

// workflowCapabilityNames is the closed capability vocabulary in canonical
// (sorted) order.
var workflowCapabilityNames = []WorkflowCapability{
	WorkflowCapabilityAcceptDelivery,
	WorkflowCapabilityDerivedStepStart,
	WorkflowCapabilityDerivedWorkerFailure,
	WorkflowCapabilityFailureAttribution,
	WorkflowCapabilityProducesDelivery,
	WorkflowCapabilityRefineProof,
	WorkflowCapabilityRootCauseAnalysis,
	WorkflowCapabilityWorkerJobs,
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
