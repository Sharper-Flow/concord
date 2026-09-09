package store

// Version-1 workflow definitions, frozen for the instances that pin them
// (issue #861). CD-0112 replaced this content in place at version 1, which
// moved the version-1 digests and left 158 live instances failing definition
// verification. These builders reproduce, byte for byte, the definitions the
// store's pinned instances were created under: the step graphs and action
// lists as they stood before CD-0112, the framing and conclusion policies as
// they stood before #836, and the cross-context boundary composed in advance
// mode. workflow_definition_version_pins_test.go holds the digests; edit
// nothing here without a new version.

// legacyV1ActionPolicies holds the action policies that changed after the
// pinned instances were created. Resolution consults these before the current
// table so a version-1 definition resolves the policy it was built with.
func legacyV1ActionPolicies() map[string]builtinActionPolicy {
	return map[string]builtinActionPolicy{
		"record_decision":        actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionCheckpoint, ActionEventCheckpoint),
		"accept_decision":        actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionHold, ActionEventTyped),
		"cross_context_boundary": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventTyped),
		"frame_question":         actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
		"frame_research":         actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
		"record_conclusion":      actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	}
}

func legacyV1ActionDefinitions(ids []string) []WorkflowActionDefinition {
	overrides := legacyV1ActionPolicies()
	result := make([]WorkflowActionDefinition, 0, len(ids))
	for _, id := range ids {
		policy, ok := builtinActionPolicies[id]
		if override, present := overrides[id]; present {
			policy, ok = override, true
		}
		if !ok {
			panic("built-in workflow action policy is not declared: " + id)
		}
		result = append(result, WorkflowActionDefinition{ID: id, Consequence: policy.Consequence, Approval: policy.Approval, ExecutionMode: policy.ExecutionMode, Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{}}})
	}
	return result
}

func legacyV1BaseDefinition(ref string, kind WorkKind, g WorkflowStepGraph, actions []string, evidence []EvidenceKind, outcome WorkflowOutcomeSchema, successors []WorkKind) WorkflowDefinition {
	changesProductTruth := workKindMayChangeProductTruth(kind)
	return WorkflowDefinition{Ref: ref, Version: 1, WorkKind: kind, ChangesProductTruth: &changesProductTruth, StepGraph: g, AvailableActions: actions, ActionDefinitions: legacyV1ActionDefinitions(actions), RequiredEvidenceKinds: evidence, OutcomeSchema: outcome, RigorRules: []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification}}}, StalenessRules: []WorkflowStalenessRule{}, CompositionRules: WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: successors, ForbiddenCompositions: []WorkflowForbiddenComposition{}}}
}

// withContinuityActionsV1 composes the continuity pair as version 1 did:
// cross_context_boundary in advance mode.
func withContinuityActionsV1(definition WorkflowDefinition) WorkflowDefinition {
	clone := withContinuityActions(definition, false)
	for i := range clone.ActionDefinitions {
		if clone.ActionDefinitions[i].ID == "cross_context_boundary" {
			clone.ActionDefinitions[i].ExecutionMode = ActionAdvance
		}
	}
	return clone
}

func legacyBreakFixV1() WorkflowDefinition {
	// Break-fix changes Product truth, so the repair route passes through a
	// human approval checkpoint between diagnosis and repair.
	ids := []string{"reproduce", "diagnose", "planning", "repair", "verify", "complete"}
	steps := []WorkflowStep{step("reproduce", WorkflowStepInternalSQLite, "record_reproduction"), step("diagnose", WorkflowStepInternalSQLite, "record_root_cause"), step("planning", WorkflowStepHumanCheckpoint, "approve_contract"), step("repair", WorkflowStepExternalEffect, "start_repair", "checkpoint_repair", "bind_evidence", "link_successor"), step("verify", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "repair", "repair", WorkflowEdgeRetry)
	actions := []string{"record_reproduction", "record_root_cause", "approve_contract", "start_repair", "checkpoint_repair", "bind_evidence", "link_successor", "record_verdict", "confirm_premise", "complete"}
	d := legacyV1BaseDefinition("workflow.break_fix", WorkKindBreakFix, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceVerification}, WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindResearch})
	return withContinuityActionsV1(d)
}

func legacyImplementationV1() WorkflowDefinition {
	ids := []string{"proposal", "discovery", "design", "planning", "execution", "acceptance", "release"}
	steps := []WorkflowStep{step("proposal", WorkflowStepInternalSQLite, "record_proposal"), step("discovery", WorkflowStepInternalSQLite, "record_discovery"), step("design", WorkflowStepInternalSQLite, "record_design"), step("planning", WorkflowStepHumanCheckpoint, "approve_contract"), step("execution", WorkflowStepExternalEffect, "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor"), step("acceptance", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("release", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "execution", "execution", WorkflowEdgeRetry)
	actions := []string{"record_proposal", "record_discovery", "record_design", "approve_contract", "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor", "record_verdict", "confirm_premise", "complete"}
	d := legacyV1BaseDefinition("workflow.implementation", WorkKindImplementation, graph(steps, edges, "release"), actions, []EvidenceKind{EvidenceVerification, EvidenceReview}, WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindBreakFix, WorkKindResearch})
	return withContinuityActionsV1(d)
}

func legacyGenericOneOffV1() WorkflowDefinition {
	ids := []string{"define", "execute", "verify", "complete"}
	steps := []WorkflowStep{step("define", WorkflowStepHumanCheckpoint, "approve_contract"), step("execute", WorkflowStepExternalEffect, "start_action", "checkpoint_action", "bind_evidence", "link_successor"), step("verify", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "execute", "execute", WorkflowEdgeRetry)
	actions := []string{"approve_contract", "start_action", "checkpoint_action", "bind_evidence", "link_successor", "record_verdict", "confirm_premise", "complete"}
	d := legacyV1BaseDefinition("workflow.generic_one_off", WorkKindGenericOneOff, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceArtifact}, WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateOutcome, PredicateCheck}, AllowedOutcomeTokens: []string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch, WorkKindArchitectureSpike, WorkKindOpsRunbook, WorkKindStaticAnalysis, WorkKindGenericOneOff})
	return withContinuityActionsV1(d)
}

func legacyResearchV1() WorkflowDefinition {
	ids := []string{"frame", "investigate", "findings", "conclude", "complete"}
	steps := []WorkflowStep{step("frame", WorkflowStepHumanCheckpoint, "frame_research", "approve_contract"), step("investigate", WorkflowStepCrossAuthority, "record_finding", "revise_candidates", "bind_evidence"), step("findings", WorkflowStepInternalSQLite, "record_report", "link_successor"), step("conclude", WorkflowStepHumanCheckpoint, "record_conclusion", "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	actions := []string{"frame_research", "approve_contract", "record_finding", "revise_candidates", "bind_evidence", "record_report", "link_successor", "record_conclusion", "record_verdict", "confirm_premise", "complete"}
	d := legacyV1BaseDefinition("workflow.research", WorkKindResearch, graph(steps, forward(ids...), "complete"), actions, []EvidenceKind{EvidenceArtifact}, WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"no_change", "resolved", "report_recorded"}, DecisionRecordRequired: false}, []WorkKind{WorkKindBreakFix, WorkKindArchitectureSpike, WorkKindStaticAnalysis})
	return withContinuityActionsV1(d)
}

// The pre-join definitions below freeze, byte for byte, the content each
// family carried before the lane-step dispatch join (#892): the four families
// at version 2 (CD-0112 content) and the three no instance pinned at their
// pre-join content at version 1. They reuse the shipped builders' base
// content and compose it with the legacy worker-action rule the join
// replaced, except research, which carried no worker actions at all. Their
// digests are pinned in workflow_definition_version_pins_test.go; edit
// nothing here without a new version (CD-0115).

func preJoinImplementationV2() WorkflowDefinition {
	d := builtinImplementation(false)
	d.Version = 2
	return withLegacyWorkerActions(d)
}

func preJoinBreakFixV2() WorkflowDefinition {
	d := builtinBreakFix(false)
	d.Version = 2
	return withLegacyWorkerActions(d)
}

func preJoinGenericOneOffV2() WorkflowDefinition {
	d := builtinGenericOneOff(false)
	d.Version = 2
	return withLegacyWorkerActions(d)
}

func preJoinResearchV2() WorkflowDefinition {
	d := builtinResearch(false)
	d.Version = 2
	return d
}

func preJoinArchitectureSpikeV1() WorkflowDefinition {
	d := builtinArchitectureSpike(false)
	d.Version = 1
	return withLegacyWorkerActions(d)
}

func preJoinOpsRunbookV1() WorkflowDefinition {
	d := builtinOpsRunbook(false)
	d.Version = 1
	return withLegacyWorkerActions(d)
}

func preJoinStaticAnalysisV1() WorkflowDefinition {
	d := builtinStaticAnalysis(false)
	d.Version = 1
	return withLegacyWorkerActions(d)
}

// These definitions freeze the released lane-step join versions immediately
// before current action payload contracts became fail-closed.
func prePayloadImplementationV3() WorkflowDefinition {
	d := builtinImplementation(false)
	d.Version = 3
	return withWorkerActionsBeforeFailure(d, false)
}

func prePayloadBreakFixV3() WorkflowDefinition {
	d := builtinBreakFix(false)
	d.Version = 3
	return withWorkerActionsBeforeFailure(d, false)
}

func prePayloadGenericOneOffV3() WorkflowDefinition {
	d := builtinGenericOneOff(false)
	d.Version = 3
	return withWorkerActionsBeforeFailure(d, false)
}

func prePayloadResearchV3() WorkflowDefinition {
	d := builtinResearch(false)
	d.Version = 3
	return withWorkerActionsBeforeFailure(d, false)
}

func prePayloadArchitectureSpikeV2() WorkflowDefinition {
	d := builtinArchitectureSpike(false)
	d.Version = 2
	return withWorkerActionsBeforeFailure(d, false)
}

func prePayloadOpsRunbookV2() WorkflowDefinition {
	d := builtinOpsRunbook(false)
	d.Version = 2
	return withWorkerActionsBeforeFailure(d, false)
}

func prePayloadStaticAnalysisV2() WorkflowDefinition {
	d := builtinStaticAnalysis(false)
	d.Version = 2
	return withWorkerActionsBeforeFailure(d, false)
}

// These definitions freeze the closed-payload worker action pair immediately
// before record_worker_failure joined the current worker action set.
func preFailureImplementationV4() WorkflowDefinition {
	d := builtinImplementation(true)
	d.Version = 4
	d = withWorkerActionsBeforeFailure(d, true)
	return withLegacyRecordDesign(d)
}

func preFailureBreakFixV4() WorkflowDefinition {
	d := builtinBreakFix(true)
	d.Version = 4
	return withWorkerActionsBeforeFailure(d, true)
}

func preFailureGenericOneOffV4() WorkflowDefinition {
	d := builtinGenericOneOff(true)
	d.Version = 4
	return withWorkerActionsBeforeFailure(d, true)
}

func preFailureResearchV4() WorkflowDefinition {
	d := builtinResearch(true)
	d.Version = 4
	return withWorkerActionsBeforeFailure(d, true)
}

func preFailureArchitectureSpikeV3() WorkflowDefinition {
	d := builtinArchitectureSpike(true)
	d.Version = 3
	return withWorkerActionsBeforeFailure(d, true)
}

func preFailureOpsRunbookV3() WorkflowDefinition {
	d := builtinOpsRunbook(true)
	d.Version = 3
	return withWorkerActionsBeforeFailure(d, true)
}

func preFailureStaticAnalysisV3() WorkflowDefinition {
	d := builtinStaticAnalysis(true)
	d.Version = 3
	return withWorkerActionsBeforeFailure(d, true)
}

// preDesignImplementationV5 freezes the implementation definition immediately
// before record_design became a typed event with a durable design projection.
func preDesignImplementationV5() WorkflowDefinition {
	d := builtinImplementation(true)
	d.Version = 5
	d = withWorkerActions(d, true)
	return withLegacyRecordDesign(d)
}

func withLegacyRecordDesign(definition WorkflowDefinition) WorkflowDefinition {
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].ID == "record_design" {
			definition.ActionDefinitions[i].Payload = WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{}}
		}
	}
	return definition
}
