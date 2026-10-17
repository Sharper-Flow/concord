package store

// WorkflowCapability names a versioned behavior a workflow definition enables.
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

// A zero firstVersion denotes the Ref-only gates. Other rows retain the
// released gates' open-ended version ranges rather than following live heads.
var releasedWorkflowCapabilities = [...]struct {
	ref          string
	firstVersion int64
	capability   WorkflowCapability
}{
	{"workflow.implementation", 28, WorkflowCapabilityDerivedStepStart},
	{"workflow.break_fix", 25, WorkflowCapabilityDerivedStepStart},
	{"workflow.implementation", 29, WorkflowCapabilityDerivedWorkerFailure},
	{"workflow.break_fix", 26, WorkflowCapabilityDerivedWorkerFailure},
	{"workflow.research", 17, WorkflowCapabilityDerivedWorkerFailure},
	{"workflow.implementation", 31, WorkflowCapabilityFailureAttribution},
	{"workflow.break_fix", 28, WorkflowCapabilityFailureAttribution},
	{"workflow.research", 18, WorkflowCapabilityFailureAttribution},
	{"workflow.implementation", 24, WorkflowCapabilityWorkerJobs},
	{"workflow.break_fix", 21, WorkflowCapabilityWorkerJobs},
	{"workflow.implementation", 21, WorkflowCapabilityAcceptDelivery},
	{"workflow.break_fix", 19, WorkflowCapabilityAcceptDelivery},
	{"workflow.implementation", 18, WorkflowCapabilityRefineProof},
	{"workflow.break_fix", 16, WorkflowCapabilityRefineProof},
	{"workflow.implementation", 0, WorkflowCapabilityProducesDelivery},
	{"workflow.break_fix", 0, WorkflowCapabilityProducesDelivery},
	{"workflow.research", 0, WorkflowCapabilityRootCauseAnalysis},
}

func workflowCapabilities(definition WorkflowDefinition) workflowCapabilitySet {
	var capabilities workflowCapabilitySet
	for _, row := range releasedWorkflowCapabilities {
		if definition.Ref == row.ref && (row.firstVersion == 0 || definition.Version >= row.firstVersion) {
			capabilities |= workflowCapabilitySet(row.capability)
		}
	}
	return capabilities
}
