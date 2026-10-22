package store

type WorkflowCapability uint8

const (
	WorkflowCapabilityDerivedStepStart WorkflowCapability = 1 << iota
	WorkflowCapabilityDerivedWorkerFailure
	WorkflowCapabilityFailureAttribution
	WorkflowCapabilityWorkerJobs
	WorkflowCapabilityAcceptDelivery
	WorkflowCapabilityRefineProof
	WorkflowCapabilityProducesDelivery
	WorkflowCapabilityRootCauseAnalysis
)

type workflowCapabilitySet uint8

func (set workflowCapabilitySet) has(capability WorkflowCapability) bool {
	return set&workflowCapabilitySet(capability) != 0
}

func workflowCapabilities(definition WorkflowDefinition) workflowCapabilitySet {
	return workflowCompositionCapabilities()[registryKey(definition.Ref, definition.Version)]
}
