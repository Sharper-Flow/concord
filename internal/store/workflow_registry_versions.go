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
	return withLegacyRecordProposal(withLegacyRecordDesign(d))
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
	return withoutDecisionRecordPayload(withWorkerActionsBeforeFailure(d, true))
}

func preFailureOpsRunbookV3() WorkflowDefinition {
	d := builtinOpsRunbook(true)
	d.Version = 3
	return withOptionalConditionCancellation(withWorkerActionsBeforeFailure(d, true))
}

func preFailureStaticAnalysisV3() WorkflowDefinition {
	d := builtinStaticAnalysis(true)
	d.Version = 3
	return withWorkerActionsBeforeFailure(d, true)
}

// releasedBreakFixV5 keeps the released version-5 break-fix content available
// after version 6 becomes the latest definition.
func releasedBreakFixV5() WorkflowDefinition {
	d := builtinBreakFix(true)
	d.Version = 5
	return withLegacyNonBlankContract(withWorkerActions(d, true))
}

// breakFixEvidenceRecoveryV6 adds the CD-0124 hold route without changing the
// released version-5 definition or any definition content that it reuses.
func breakFixEvidenceRecoveryV6() WorkflowDefinition {
	d := builtinBreakFix(true)
	d.Version = 6
	for i := range d.StepGraph.Steps {
		if d.StepGraph.Steps[i].ID == "verify" {
			d.StepGraph.Steps[i].Actions = append([]string{"bind_evidence"}, d.StepGraph.Steps[i].Actions...)
			break
		}
	}
	return withLegacyNonBlankContract(withWorkerActions(d, true))
}

// preDesignImplementationV5 freezes the implementation definition immediately
// before record_design became a typed event with a durable design projection.
func preDesignImplementationV5() WorkflowDefinition {
	d := builtinImplementation(true)
	d.Version = 5
	d = withWorkerActions(d, true)
	return withLegacyRecordProposal(withLegacyRecordDesign(d))
}

func preProposalImplementationV6() WorkflowDefinition {
	d := builtinImplementation(true)
	d.Version = 6
	d = withWorkerActions(d, true)
	return withLegacyRecordProposal(d)
}

func releasedImplementationV7() WorkflowDefinition {
	d := builtinImplementation(true)
	d.Version = 7
	return withLegacyNonBlankContract(withWorkerActions(d, true))
}

func implementationRefinementV8() WorkflowDefinition {
	d := builtinImplementation(true)
	d.Version = 8
	d.RequiredEvidenceKinds = append(d.RequiredEvidenceKinds, EvidenceArtifact)
	d = withRefinementStep(d, "execution", "acceptance")
	return withLegacyNonBlankContract(withWorkerActions(d, true))
}

func implementationRefinementV9() WorkflowDefinition {
	d := implementationRefinementV8()
	d.Version = 9
	return withRefinementFailureEdge(d, "execution", "acceptance")
}

func breakFixRefinementV7() WorkflowDefinition {
	d := builtinBreakFix(true)
	d.Version = 7
	d.RequiredEvidenceKinds = append(d.RequiredEvidenceKinds, EvidenceArtifact)
	for i := range d.StepGraph.Steps {
		if d.StepGraph.Steps[i].ID == "verify" {
			d.StepGraph.Steps[i].Actions = append([]string{"bind_evidence"}, d.StepGraph.Steps[i].Actions...)
			break
		}
	}
	d = withRefinementStep(d, "repair", "verify")
	return withLegacyNonBlankContract(withWorkerActions(d, true))
}

func breakFixRefinementV8() WorkflowDefinition {
	d := breakFixRefinementV7()
	d.Version = 8
	return withLegacyPremiseContract(withRefinementFailureEdge(d, "repair", "verify"))
}

// releasedOpsRunbookV4 keeps the released version-4 ops-runbook content
// available after version 5 becomes the latest definition.
func releasedOpsRunbookV4() WorkflowDefinition {
	d := builtinOpsRunbook(true)
	d.Version = 4
	return withOptionalConditionCancellation(withWorkerActions(d, true))
}

// opsRunbookCleanupCheckpointV5 makes the cleanup step a human checkpoint. The
// step declares confirm_premise, whose approval the operator question serves,
// and that question is only reachable from a human-checkpoint step. Under the
// internal-SQLite kind the step's one advancing action refused, so the only
// edge out of cleanup was unreachable and every ops-runbook item stopped there.
func opsRunbookCleanupCheckpointV5() WorkflowDefinition {
	d := builtinOpsRunbook(true)
	d.Version = 5
	for i := range d.StepGraph.Steps {
		if d.StepGraph.Steps[i].ID == "cleanup" {
			d.StepGraph.Steps[i].Kind = WorkflowStepHumanCheckpoint
			break
		}
	}
	return withLegacyNonBlankContract(withWorkerActions(d, true))
}

func withLegacyRecordDesign(definition WorkflowDefinition) WorkflowDefinition {
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].ID == "record_design" {
			definition.ActionDefinitions[i].Payload = WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{}}
		}
	}
	return withLegacyNonBlankContract(definition)
}

func withLegacyRecordProposal(definition WorkflowDefinition) WorkflowDefinition {
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].ID == "record_proposal" {
			definition.ActionDefinitions[i].Payload = WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{}}
		}
	}
	return withLegacyNonBlankContract(definition)
}

func withLegacyDeliveryPayload(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].ID == "record_delivery" {
			definition.ActionDefinitions[i].Payload = WorkflowPayloadDefinition{Closed: definition.ActionDefinitions[i].Payload.Closed, Fields: []WorkflowPayloadField{}}
		}
	}
	return definition
}

func withCurrentDeliveryPayload(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].ID == "record_delivery" {
			definition.ActionDefinitions[i].Payload = WorkflowPayloadDefinition{
				Closed: true,
				Fields: []WorkflowPayloadField{
					actionRefField("delivery_artifact", true),
					actionEnumField("delivery_state", true, "asserted"),
				},
			}
		}
	}
	return definition
}

func withLegacyEvidenceBindingReferences(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for actionIndex := range definition.ActionDefinitions {
		if workflowCallerEvidenceBinder(definition.ActionDefinitions[actionIndex].ID) {
			for fieldIndex := range definition.ActionDefinitions[actionIndex].Payload.Fields {
				field := &definition.ActionDefinitions[actionIndex].Payload.Fields[fieldIndex]
				if field.Name != "evidence_ref" && field.Name != "immutable_subject_ref" {
					continue
				}
				field.ValueType = PayloadString
				field.NonBlank = false
				field.MinLength = workflowInt(1)
				field.MaxLength = workflowInt(2048)
			}
		}
	}
	return definition
}

func withCurrentEvidenceBindingReferences(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for actionIndex := range definition.ActionDefinitions {
		if workflowCallerEvidenceBinder(definition.ActionDefinitions[actionIndex].ID) {
			for fieldIndex := range definition.ActionDefinitions[actionIndex].Payload.Fields {
				field := &definition.ActionDefinitions[actionIndex].Payload.Fields[fieldIndex]
				if field.Name != "evidence_ref" && field.Name != "immutable_subject_ref" {
					continue
				}
				*field = actionRefField(field.Name, field.Required)
			}
		}
	}
	return definition
}

func previousWorkflowVersion(definition WorkflowDefinition, version int64) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	definition.Version = version
	definition = withLegacyEvidenceBindingReferences(definition)
	for actionIndex := range definition.ActionDefinitions {
		for fieldIndex := range definition.ActionDefinitions[actionIndex].Payload.Fields {
			field := &definition.ActionDefinitions[actionIndex].Payload.Fields[fieldIndex]
			if (field.Name == "evidence_ref" || field.Name == "immutable_subject_ref") && field.ValueType == PayloadString {
				field.NonBlank = true
			}
		}
	}
	return definition
}

// withDesignDecisionItemSchema restates the record_design decisions field so it
// names its element contract through item_ref. The frozen versions declare the
// same schema through schema_ref, where an array field naming a non-array
// schema had to be read as a per-element contract by inference. Their content
// is unchanged; only definitions from this version forward say it outright.
func withDesignDecisionItemSchema(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for actionIndex := range definition.ActionDefinitions {
		if definition.ActionDefinitions[actionIndex].ID != "record_design" {
			continue
		}
		fields := definition.ActionDefinitions[actionIndex].Payload.Fields
		for fieldIndex := range fields {
			if fields[fieldIndex].Name != "decisions" {
				continue
			}
			fields[fieldIndex] = actionItemArrayField("decisions", fields[fieldIndex].Required, *fields[fieldIndex].MinItems, *fields[fieldIndex].MaxItems, fields[fieldIndex].SchemaRef)
		}
	}
	return definition
}

// implementationDesignItemSchemaV10 ships the unambiguous decisions declaration.
func implementationDesignItemSchemaV10() WorkflowDefinition {
	d := implementationRefinementV9()
	d.Version = 10
	return withLegacyPremiseContract(withDesignDecisionItemSchema(d))
}

// withoutDecisionRecordPayload restores record_decision's empty declared
// payload. Versions up to 4 declared no fields for it, so the fold could never
// receive the decision record it requires. Version 5 declares the fields; the
// released versions keep the content they were pinned under.
func withoutDecisionRecordPayload(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for index := range definition.ActionDefinitions {
		if definition.ActionDefinitions[index].ID != "record_decision" {
			continue
		}
		definition.ActionDefinitions[index].Payload = WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{}}
	}
	return definition
}

// preDecisionPayloadArchitectureSpikeV4 freezes the architecture spike as it
// stood before record_decision declared the decision record it must carry.
func preDecisionPayloadArchitectureSpikeV4() WorkflowDefinition {
	d := builtinArchitectureSpike(true)
	d.Version = 4
	return withLegacyNonBlankContract(withoutDecisionRecordPayload(withWorkerActions(d, true)))
}

// withOptionalConditionCancellation restores cancel_condition's all-optional
// declaration. Versions up to 5 declared every field optional and left the
// authority a free reference, while the fold required all four and accepted
// only the operator as the authority. Version 6 declares what the fold
// requires; the released versions keep the content they were pinned under.
func withOptionalConditionCancellation(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for index := range definition.ActionDefinitions {
		if definition.ActionDefinitions[index].ID != "cancel_condition" {
			continue
		}
		definition.ActionDefinitions[index].Payload = WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{
			actionRefField("condition_id", false), actionRefField("cancellation_authority", false),
			actionListField("cancellation_evidence", false, 1, 32), actionRefField("cancelled_by_event", false),
		}}
	}
	return definition
}

// preCancellationContractOpsRunbookV5 freezes the ops runbook as it stood
// before cancel_condition declared the cancellation its fold requires.
func preCancellationContractOpsRunbookV5() WorkflowDefinition {
	return withOptionalConditionCancellation(opsRunbookCleanupCheckpointV5())
}

// opsRunbookConditionContractV6 ships cancel_condition's declared cancellation:
// the condition, the operator authority, its evidence, and the cancelling
// event, all required, as the fold has always demanded them.
func opsRunbookConditionContractV6() WorkflowDefinition {
	d := opsRunbookCleanupCheckpointV5()
	d.Version = 6
	return withLegacyPremiseContract(d)
}

func opsRunbookTimestampV7() WorkflowDefinition {
	d := cloneWorkflowDefinition(opsRunbookConditionContractV6())
	d.Version = 7
	for i := range d.ActionDefinitions {
		for j := range d.ActionDefinitions[i].Payload.Fields {
			field := &d.ActionDefinitions[i].Payload.Fields[j]
			if field.Name == "asserted_at" {
				field.SchemaRef = "native_report_timestamp"
			}
		}
	}
	return withLegacyPremiseContract(d)
}

func withLegacyPremiseContract(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].ID != "approve_contract" {
			continue
		}
		for j := range definition.ActionDefinitions[i].Payload.Fields {
			field := &definition.ActionDefinitions[i].Payload.Fields[j]
			if field.Name == "premise" {
				field.Required = false
				field.NonBlank = false
				field.Forbidden = nil
			}
		}
	}
	return withLegacyNonBlankContract(definition)
}

func withLegacyNonBlankContract(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for i := range definition.ActionDefinitions {
		for j := range definition.ActionDefinitions[i].Payload.Fields {
			field := &definition.ActionDefinitions[i].Payload.Fields[j]
			if field.ValueType == PayloadString && len(field.Enum) == 0 && field.Name != "premise" {
				field.NonBlank = false
			}
		}
		if definition.ActionDefinitions[i].PublicPayload != nil {
			for j := range definition.ActionDefinitions[i].PublicPayload.Fields {
				field := &definition.ActionDefinitions[i].PublicPayload.Fields[j]
				if field.ValueType == PayloadString && len(field.Enum) == 0 && field.Name != "premise" {
					field.NonBlank = false
				}
			}
		}
	}
	return definition
}

func withCurrentNonBlankContract(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for i := range definition.ActionDefinitions {
		for j := range definition.ActionDefinitions[i].Payload.Fields {
			field := &definition.ActionDefinitions[i].Payload.Fields[j]
			if field.ValueType == PayloadString && len(field.Enum) == 0 {
				field.NonBlank = true
			}
		}
		if definition.ActionDefinitions[i].PublicPayload != nil {
			for j := range definition.ActionDefinitions[i].PublicPayload.Fields {
				field := &definition.ActionDefinitions[i].PublicPayload.Fields[j]
				if field.ValueType == PayloadString && len(field.Enum) == 0 {
					field.NonBlank = true
				}
			}
		}
	}
	return definition
}

func withCurrentPremiseContract(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].ID != "approve_contract" {
			continue
		}
		for j := range definition.ActionDefinitions[i].Payload.Fields {
			if definition.ActionDefinitions[i].Payload.Fields[j].Name == "premise" {
				definition.ActionDefinitions[i].Payload.Fields[j] = actionPremiseField()
			}
		}
	}
	return definition
}

func releasedResearchV5() WorkflowDefinition {
	d := withWorkerActions(builtinResearch(true), true)
	d.Version = 5
	return withLegacyPremiseContract(d)
}

func implementationPremiseContractV11() WorkflowDefinition {
	d := withCurrentPremiseContract(implementationDesignItemSchemaV10())
	d.Version = 11
	return d
}

func breakFixPremiseContractV9() WorkflowDefinition {
	d := withCurrentPremiseContract(breakFixRefinementV8())
	d.Version = 9
	return d
}

func architecturePremiseContractV7() WorkflowDefinition {
	d := withCurrentPremiseContract(architectureSpikeDecisionBoundsV6())
	d.Version = 7
	return d
}

func opsRunbookPremiseContractV8() WorkflowDefinition {
	d := withCurrentPremiseContract(opsRunbookTimestampV7())
	d.Version = 8
	return d
}

func releasedStaticAnalysisV4() WorkflowDefinition {
	d := withWorkerActions(builtinStaticAnalysis(true), true)
	d.Version = 4
	return withLegacyPremiseContract(d)
}

func releasedGenericOneOffV5() WorkflowDefinition {
	d := withWorkerActions(builtinGenericOneOff(true), true)
	d.Version = 5
	return withLegacyPremiseContract(d)
}

func releasedArchitectureSpikeV5() WorkflowDefinition {
	d := withWorkerActions(builtinArchitectureSpike(true), true)
	d.Version = 5
	return withLegacyDeliveryPayload(withLegacyEvidenceBindingReferences(withLegacyPremiseContract(d)))
}

func architectureSpikeDecisionBoundsV6() WorkflowDefinition {
	d := cloneWorkflowDefinition(releasedArchitectureSpikeV5())
	d.Version = 6
	for i := range d.ActionDefinitions {
		if d.ActionDefinitions[i].ID != "record_decision" {
			continue
		}
		for j := range d.ActionDefinitions[i].Payload.Fields {
			field := &d.ActionDefinitions[i].Payload.Fields[j]
			if field.ValueType == PayloadStringList {
				field.ItemRef = "decision_record_text"
			}
		}
	}
	return withLegacyEvidenceBindingReferences(withLegacyPremiseContract(d))
}

// implementationPreAlignmentV12 reproduces the implementation definition that
// was current before CD-0156, registered in the exact shape it shipped so the
// instances pinned at it replay their original graph unchanged.
func implementationPreAlignmentV12() WorkflowDefinition {
	d := withCurrentNonBlankContract(implementationPremiseContractV11())
	d.Version = 12
	return d
}

// breakFixPreAlignmentV10 reproduces the break-fix definition that was
// current before CD-0156, registered in the exact shape it shipped.
func breakFixPreAlignmentV10() WorkflowDefinition {
	d := withCurrentNonBlankContract(breakFixPremiseContractV9())
	d.Version = 10
	return d
}

// implementationDeliveryV15 adds the coordinator-owned delivery gate after
// the refinement pass. Earlier definitions keep their original graph and
// payload shape for pinned instances.
func implementationDeliveryV15() WorkflowDefinition {
	d := implementationAlignmentV14()
	d.Version = 15
	return withDeliveryStep(d, "refine", "acceptance")
}

// breakFixDeliveryV13 adds the coordinator-owned delivery gate after the
// refinement pass. Earlier definitions keep their original graph and payload
// shape for pinned instances.
func breakFixDeliveryV13() WorkflowDefinition {
	d := breakFixAlignmentV12()
	d.Version = 13
	return withDeliveryStep(d, "refine", "verify")
}

func researchDeliveryPayloadV9() WorkflowDefinition {
	d := withCurrentNonBlankContract(withWorkerActions(builtinResearch(true), true))
	d.Version = 9
	return d
}

func architectureDeliveryPayloadV10() WorkflowDefinition {
	d := withCurrentEvidenceBindingReferences(withCurrentNonBlankContract(architecturePremiseContractV7()))
	d.Version = 10
	return withCurrentDeliveryPayload(d)
}

func opsRunbookDeliveryPayloadV11() WorkflowDefinition {
	d := withCurrentNonBlankContract(opsRunbookPremiseContractV8())
	d.Version = 11
	return d
}

func staticAnalysisDeliveryPayloadV8() WorkflowDefinition {
	d := withCurrentNonBlankContract(withWorkerActions(builtinStaticAnalysis(true), true))
	d.Version = 8
	return d
}

func genericOneOffDeliveryPayloadV9() WorkflowDefinition {
	d := withCurrentNonBlankContract(withWorkerActions(builtinGenericOneOff(true), true))
	d.Version = 9
	return d
}

func researchPreDeliveryV8() WorkflowDefinition {
	d := withCurrentNonBlankContract(withWorkerActions(builtinResearch(true), true))
	d.Version = 8
	return d
}

func architecturePreDeliveryV9() WorkflowDefinition {
	d := withCurrentEvidenceBindingReferences(withCurrentNonBlankContract(architecturePremiseContractV7()))
	d.Version = 9
	return d
}

func opsRunbookPreDeliveryV10() WorkflowDefinition {
	d := withCurrentNonBlankContract(opsRunbookPremiseContractV8())
	d.Version = 10
	return d
}

func staticAnalysisPreDeliveryV7() WorkflowDefinition {
	d := withCurrentNonBlankContract(withWorkerActions(builtinStaticAnalysis(true), true))
	d.Version = 7
	return d
}

func genericOneOffPreDeliveryV8() WorkflowDefinition {
	d := withCurrentNonBlankContract(withWorkerActions(builtinGenericOneOff(true), true))
	d.Version = 8
	return d
}

// implementationAlignmentV14 adds the evidence-reference payload contract
// after proposal. record_alignment remains the step's only advance exit, so
// the backlog search cannot be skipped.
func implementationAlignmentV14() WorkflowDefinition {
	d := implementationPreAlignmentV12()
	d.Version = 14
	return withAlignmentStep(d, "proposal", "discovery")
}

// breakFixAlignmentV12 adds the evidence-reference payload contract after the
// CD-0156 mandatory alignment step.
func breakFixAlignmentV12() WorkflowDefinition {
	d := breakFixPreAlignmentV10()
	d.Version = 12
	return withAlignmentStep(d, "reproduce", "diagnose")
}
