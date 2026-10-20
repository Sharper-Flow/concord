package store

// WorkflowCapability names a versioned behavior a workflow definition enables.
type WorkflowCapability uint16

const (
	WorkflowCapabilityDerivedStepStart WorkflowCapability = 1 << iota
	WorkflowCapabilityDerivedWorkerFailure
	WorkflowCapabilityFailureAttribution
	WorkflowCapabilityWorkerJobs
	WorkflowCapabilityAcceptDelivery
	WorkflowCapabilityRefineProof
	WorkflowCapabilityProducesDelivery
	WorkflowCapabilityRootCauseAnalysis
	// WorkflowCapabilitySmallProof gates the small composition's proof: its
	// footprint checks and one independent review (CD-0229 D5).
	WorkflowCapabilitySmallProof
	WorkflowCapabilitySizedTiming
)

type workflowCapabilitySet uint16

func (set workflowCapabilitySet) has(capability WorkflowCapability) bool {
	return set&workflowCapabilitySet(capability) != 0
}

// releasedWorkflowCapabilities holds the gates of the chain-built versions,
// each over the closed version range it was released for. Composed versions
// take their capabilities from their parts instead
// (workflowCompositionCapabilities).
var releasedWorkflowCapabilities = [...]struct {
	ref          string
	firstVersion int64
	lastVersion  int64
	capability   WorkflowCapability
}{
	{"workflow.implementation", 28, 32, WorkflowCapabilityDerivedStepStart},
	{"workflow.break_fix", 25, 29, WorkflowCapabilityDerivedStepStart},
	{"workflow.implementation", 29, 32, WorkflowCapabilityDerivedWorkerFailure},
	{"workflow.break_fix", 26, 29, WorkflowCapabilityDerivedWorkerFailure},
	{"workflow.research", 17, 19, WorkflowCapabilityDerivedWorkerFailure},
	{"workflow.implementation", 31, 32, WorkflowCapabilityFailureAttribution},
	{"workflow.break_fix", 28, 29, WorkflowCapabilityFailureAttribution},
	{"workflow.research", 18, 19, WorkflowCapabilityFailureAttribution},
	{"workflow.implementation", 24, 32, WorkflowCapabilityWorkerJobs},
	{"workflow.break_fix", 21, 29, WorkflowCapabilityWorkerJobs},
	{"workflow.implementation", 21, 32, WorkflowCapabilityAcceptDelivery},
	{"workflow.break_fix", 19, 29, WorkflowCapabilityAcceptDelivery},
	{"workflow.implementation", 18, 32, WorkflowCapabilityRefineProof},
	{"workflow.break_fix", 16, 29, WorkflowCapabilityRefineProof},
	{"workflow.implementation", 1, 32, WorkflowCapabilityProducesDelivery},
	{"workflow.break_fix", 1, 29, WorkflowCapabilityProducesDelivery},
	{"workflow.research", 1, 19, WorkflowCapabilityRootCauseAnalysis},
}

func workflowCapabilities(definition WorkflowDefinition) workflowCapabilitySet {
	if capabilities, composed := workflowCompositionCapabilities()[registryKey(definition.Ref, definition.Version)]; composed {
		return capabilities
	}
	var capabilities workflowCapabilitySet
	for _, row := range releasedWorkflowCapabilities {
		if definition.Ref == row.ref && definition.Version >= row.firstVersion && definition.Version <= row.lastVersion {
			capabilities |= workflowCapabilitySet(row.capability)
		}
	}
	return capabilities
}
