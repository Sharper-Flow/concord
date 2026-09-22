package store

// This file authors every built-in workflow definition version as one
// ordered chain per family. The chain is the promotion surface: promoting a
// version appends it after the versions it succeeds, the chain's last element
// is the family's current definition, and every earlier element stays
// registered for the instances pinned to it. Retention is a property of the
// promotion act rather than of a separately maintained list: an append cannot
// drop the outgoing version, and the registry's own checks fail any change
// that would — validateBuiltinWorkflowVersionContinuity rejects a version set
// with a gap, and workflow_definition_version_pins_test.go holds every
// released version's digest.

// retainedBeforePremiseContract composes the contract surfaces a retained
// version shipped under before the current premise contract existed: the
// legacy premise, evidence-binding, and non-blank rules over the legacy
// delivery payload.
func retainedBeforePremiseContract(definition WorkflowDefinition) WorkflowDefinition {
	return withLegacyDeliveryPayload(withLegacyNonBlankContract(withLegacyEvidenceBindingReferences(withLegacyPremiseContract(definition))))
}

// retainedWithPremiseContract composes the contract surfaces a retained
// version shipped under once its builder already declared the current premise
// contract: the legacy evidence-binding and non-blank rules over the legacy
// delivery payload.
func retainedWithPremiseContract(definition WorkflowDefinition) WorkflowDefinition {
	return withLegacyDeliveryPayload(withLegacyNonBlankContract(withLegacyEvidenceBindingReferences(definition)))
}

// retainedAtPreviousVersion composes a retained version whose shipped shape
// is recovered by rebasing a later builder onto the earlier version number
// and restoring the evidence-reference fields it shipped with.
func retainedAtPreviousVersion(definition WorkflowDefinition, version int64) WorkflowDefinition {
	return withLegacyDeliveryPayload(previousWorkflowVersion(definition, version))
}

// retainedBeforeDeliveryContract composes a retained version whose only
// difference from the family's current shape is the record_delivery payload
// contract the current version ships.
func retainedBeforeDeliveryContract(definition WorkflowDefinition) WorkflowDefinition {
	return withLegacyDeliveryPayload(definition)
}

func workflowDefinitionChains() [][]WorkflowDefinition {
	return [][]WorkflowDefinition{
		implementationVersionChain(),
		breakFixVersionChain(),
		researchVersionChain(),
		architectureSpikeVersionChain(),
		opsRunbookVersionChain(),
		staticAnalysisVersionChain(),
		genericOneOffVersionChain(),
	}
}

// implementationVersionChain authors workflow.implementation from its
// version-1 shape to the current version. Appending the next promotion here
// retains the outgoing version and publishes the new current in one act.
func implementationVersionChain() []WorkflowDefinition {
	return []WorkflowDefinition{
		retainedBeforePremiseContract(withLegacyWorkerActions(legacyImplementationV1())),
		retainedBeforePremiseContract(preJoinImplementationV2()),
		retainedBeforePremiseContract(prePayloadImplementationV3()),
		retainedBeforePremiseContract(preFailureImplementationV4()),
		retainedBeforePremiseContract(preDesignImplementationV5()),
		retainedBeforePremiseContract(preProposalImplementationV6()),
		retainedBeforePremiseContract(releasedImplementationV7()),
		retainedBeforePremiseContract(implementationRefinementV8()),
		retainedBeforePremiseContract(implementationRefinementV9()),
		retainedBeforePremiseContract(implementationDesignItemSchemaV10()),
		retainedWithPremiseContract(implementationPremiseContractV11()),
		retainedAtPreviousVersion(implementationPreAlignmentV12(), 12),
		retainedAtPreviousVersion(implementationAlignmentV14(), 13),
		retainedBeforeDeliveryContract(implementationAlignmentV14()),
		implementationDeliveryV15(),
	}
}

// breakFixVersionChain authors workflow.break_fix from its version-1 shape to
// the current version.
func breakFixVersionChain() []WorkflowDefinition {
	return []WorkflowDefinition{
		retainedBeforePremiseContract(withLegacyWorkerActions(legacyBreakFixV1())),
		retainedBeforePremiseContract(preJoinBreakFixV2()),
		retainedBeforePremiseContract(prePayloadBreakFixV3()),
		retainedBeforePremiseContract(preFailureBreakFixV4()),
		retainedBeforePremiseContract(releasedBreakFixV5()),
		retainedBeforePremiseContract(breakFixEvidenceRecoveryV6()),
		retainedBeforePremiseContract(breakFixRefinementV7()),
		retainedBeforePremiseContract(breakFixRefinementV8()),
		retainedWithPremiseContract(breakFixPremiseContractV9()),
		retainedAtPreviousVersion(breakFixPreAlignmentV10(), 10),
		retainedAtPreviousVersion(breakFixAlignmentV12(), 11),
		retainedBeforeDeliveryContract(breakFixAlignmentV12()),
		breakFixDeliveryV13(),
	}
}

// researchVersionChain authors workflow.research from its version-1 shape to
// the current version.
func researchVersionChain() []WorkflowDefinition {
	return []WorkflowDefinition{
		retainedBeforePremiseContract(withLegacyWorkerActions(legacyResearchV1())),
		retainedBeforePremiseContract(preJoinResearchV2()),
		retainedBeforePremiseContract(prePayloadResearchV3()),
		retainedBeforePremiseContract(preFailureResearchV4()),
		retainedBeforePremiseContract(releasedResearchV5()),
		retainedWithPremiseContract(withWorkerActions(builtinResearch(true), true)),
		retainedAtPreviousVersion(withCurrentNonBlankContract(withWorkerActions(builtinResearch(true), true)), 7),
		retainedBeforeDeliveryContract(researchPreDeliveryV8()),
		researchDeliveryPayloadV9(),
	}
}

// architectureSpikeVersionChain authors workflow.architecture_spike from its
// version-1 shape to the current version.
func architectureSpikeVersionChain() []WorkflowDefinition {
	return []WorkflowDefinition{
		retainedBeforePremiseContract(preJoinArchitectureSpikeV1()),
		retainedBeforePremiseContract(prePayloadArchitectureSpikeV2()),
		retainedBeforePremiseContract(preFailureArchitectureSpikeV3()),
		retainedBeforePremiseContract(preDecisionPayloadArchitectureSpikeV4()),
		retainedBeforePremiseContract(releasedArchitectureSpikeV5()),
		retainedBeforePremiseContract(architectureSpikeDecisionBoundsV6()),
		retainedWithPremiseContract(architecturePremiseContractV7()),
		retainedAtPreviousVersion(withCurrentNonBlankContract(architecturePremiseContractV7()), 8),
		retainedBeforeDeliveryContract(architecturePreDeliveryV9()),
		architectureDeliveryPayloadV10(),
	}
}

// opsRunbookVersionChain authors workflow.ops_runbook from its version-1
// shape to the current version.
func opsRunbookVersionChain() []WorkflowDefinition {
	return []WorkflowDefinition{
		retainedBeforePremiseContract(preJoinOpsRunbookV1()),
		retainedBeforePremiseContract(prePayloadOpsRunbookV2()),
		retainedBeforePremiseContract(preFailureOpsRunbookV3()),
		retainedBeforePremiseContract(releasedOpsRunbookV4()),
		retainedBeforePremiseContract(preCancellationContractOpsRunbookV5()),
		retainedBeforePremiseContract(opsRunbookConditionContractV6()),
		retainedBeforePremiseContract(opsRunbookTimestampV7()),
		retainedWithPremiseContract(opsRunbookPremiseContractV8()),
		retainedAtPreviousVersion(withCurrentNonBlankContract(opsRunbookPremiseContractV8()), 9),
		retainedBeforeDeliveryContract(opsRunbookPreDeliveryV10()),
		opsRunbookDeliveryPayloadV11(),
	}
}

// staticAnalysisVersionChain authors workflow.static_analysis from its
// version-1 shape to the current version.
func staticAnalysisVersionChain() []WorkflowDefinition {
	return []WorkflowDefinition{
		retainedBeforePremiseContract(preJoinStaticAnalysisV1()),
		retainedBeforePremiseContract(prePayloadStaticAnalysisV2()),
		retainedBeforePremiseContract(preFailureStaticAnalysisV3()),
		retainedBeforePremiseContract(releasedStaticAnalysisV4()),
		retainedWithPremiseContract(withWorkerActions(builtinStaticAnalysis(true), true)),
		retainedAtPreviousVersion(withCurrentNonBlankContract(withWorkerActions(builtinStaticAnalysis(true), true)), 6),
		retainedBeforeDeliveryContract(staticAnalysisPreDeliveryV7()),
		staticAnalysisDeliveryPayloadV8(),
	}
}

// genericOneOffVersionChain authors workflow.generic_one_off from its
// version-1 shape to the current version.
func genericOneOffVersionChain() []WorkflowDefinition {
	return []WorkflowDefinition{
		retainedBeforePremiseContract(withLegacyWorkerActions(legacyGenericOneOffV1())),
		retainedBeforePremiseContract(preJoinGenericOneOffV2()),
		retainedBeforePremiseContract(prePayloadGenericOneOffV3()),
		retainedBeforePremiseContract(preFailureGenericOneOffV4()),
		retainedBeforePremiseContract(releasedGenericOneOffV5()),
		retainedWithPremiseContract(withWorkerActions(builtinGenericOneOff(true), true)),
		retainedAtPreviousVersion(withCurrentNonBlankContract(withWorkerActions(builtinGenericOneOff(true), true)), 7),
		retainedBeforeDeliveryContract(genericOneOffPreDeliveryV8()),
		genericOneOffDeliveryPayloadV9(),
	}
}
