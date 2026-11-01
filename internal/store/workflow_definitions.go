package store

import (
	"fmt"
	"slices"
)

// BuiltinWorkflowDefinitions returns the current definition for each live family.
func BuiltinWorkflowDefinitions() []WorkflowDefinition {
	changesTruth, noTruthChange := true, false
	definitions := []WorkflowDefinition{
		{
			Ref: "workflow.implementation", WorkKind: WorkKindImplementation, ChangesProductTruth: &changesTruth,
			Capabilities: []WorkflowCapability{WorkflowCapabilityDerivedStepStart, WorkflowCapabilityProducesDelivery, WorkflowCapabilityRefineProof},
			StepGraph: WorkflowStepGraph{
				StartStep: "proposal", TerminalSteps: []string{"release"},
				Steps: []WorkflowStep{
					{ID: "proposal", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_proposal", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
					{ID: "alignment", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_alignment", "record_work_context", "checkpoint_context", "cross_context_boundary"}},
					{ID: "planning", Kind: WorkflowStepInternalSQLite, Actions: []string{"approve_contract", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
					{ID: "design", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_design", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
					{ID: "execution", Kind: WorkflowStepExternalEffect, Actions: []string{"start_execution", "checkpoint_execution", "bind_evidence", "link_successor", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"}},
					{ID: "refine", Kind: WorkflowStepExternalEffect, Actions: []string{"checkpoint_refine", "bind_evidence", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"}},
					{ID: "delivery", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary"}},
					{ID: "acceptance", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_verdict", "confirm_premise", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"}},
					{ID: "release", Kind: WorkflowStepInternalSQLite, Actions: []string{"bind_evidence", "complete", "record_work_context", "checkpoint_context", "cross_context_boundary"}},
				},
				Edges: []WorkflowEdge{
					{From: "proposal", To: "alignment", Kind: WorkflowEdgeForward}, {From: "alignment", To: "planning", Kind: WorkflowEdgeForward},
					{From: "planning", To: "design", Kind: WorkflowEdgeForward}, {From: "design", To: "execution", Kind: WorkflowEdgeForward},
					{From: "execution", To: "refine", Kind: WorkflowEdgeForward}, {From: "refine", To: "delivery", Kind: WorkflowEdgeForward},
					{From: "delivery", To: "acceptance", Kind: WorkflowEdgeForward}, {From: "acceptance", To: "release", Kind: WorkflowEdgeForward},
					{From: "execution", To: "execution", Kind: WorkflowEdgeRetry}, {From: "acceptance", To: "refine", Kind: WorkflowEdgeFailure},
				},
			},
			RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceReview, EvidenceArtifact},
			OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
			RigorRules:            []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification}}},
			StalenessRules:        []WorkflowStalenessRule{}, CompositionRules: WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: []WorkKind{WorkKindBreakFix, WorkKindResearch}, ForbiddenCompositions: []WorkflowForbiddenComposition{}},
			RecoveryRoutes: []WorkflowRecoveryRoute{
				{Step: "execution", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
				{Step: "refine", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
				{Step: "acceptance", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
				{Step: "release", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
				{Step: "release", Trigger: WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: "execution"},
			},
		},
		{
			Ref: "workflow.break_fix", WorkKind: WorkKindBreakFix, ChangesProductTruth: &changesTruth,
			Capabilities: []WorkflowCapability{WorkflowCapabilityDerivedStepStart, WorkflowCapabilityProducesDelivery, WorkflowCapabilityRefineProof},
			StepGraph: WorkflowStepGraph{
				StartStep: "diagnosis", TerminalSteps: []string{"complete"},
				Steps: []WorkflowStep{
					{ID: "diagnosis", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_diagnosis", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
					{ID: "planning", Kind: WorkflowStepInternalSQLite, Actions: []string{"approve_contract", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
					{ID: "repair", Kind: WorkflowStepExternalEffect, Actions: []string{"checkpoint_repair", "bind_evidence", "link_successor", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"}},
					{ID: "refine", Kind: WorkflowStepExternalEffect, Actions: []string{"checkpoint_refine", "bind_evidence", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"}},
					{ID: "delivery", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary"}},
					{ID: "verify", Kind: WorkflowStepInternalSQLite, Actions: []string{"bind_evidence", "record_verdict", "confirm_premise", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"}},
					{ID: "complete", Kind: WorkflowStepInternalSQLite, Actions: []string{"bind_evidence", "complete", "record_work_context", "checkpoint_context", "cross_context_boundary"}},
				},
				Edges: []WorkflowEdge{
					{From: "diagnosis", To: "planning", Kind: WorkflowEdgeForward}, {From: "planning", To: "repair", Kind: WorkflowEdgeForward},
					{From: "repair", To: "refine", Kind: WorkflowEdgeForward}, {From: "refine", To: "delivery", Kind: WorkflowEdgeForward},
					{From: "delivery", To: "verify", Kind: WorkflowEdgeForward}, {From: "verify", To: "complete", Kind: WorkflowEdgeForward},
					{From: "repair", To: "repair", Kind: WorkflowEdgeRetry}, {From: "verify", To: "refine", Kind: WorkflowEdgeFailure},
				},
			},
			RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceArtifact},
			OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
			RigorRules:            []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification}}},
			StalenessRules:        []WorkflowStalenessRule{}, CompositionRules: WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: []WorkKind{WorkKindImplementation, WorkKindResearch}, ForbiddenCompositions: []WorkflowForbiddenComposition{}},
			RecoveryRoutes: []WorkflowRecoveryRoute{
				{Step: "repair", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair"},
				{Step: "refine", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair"},
				{Step: "verify", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair"},
				{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair"},
				{Step: "complete", Trigger: WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: "repair"},
			},
		},
		{
			Ref: "workflow.research", WorkKind: WorkKindResearch, ChangesProductTruth: &noTruthChange,
			Capabilities: []WorkflowCapability{},
			StepGraph: WorkflowStepGraph{
				StartStep: "frame", TerminalSteps: []string{"complete"},
				Steps: []WorkflowStep{
					{ID: "frame", Kind: WorkflowStepInternalSQLite, Actions: []string{"frame_research", "approve_contract", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
					{ID: "investigate", Kind: WorkflowStepCrossAuthority, Actions: []string{"record_finding", "revise_candidates", "bind_evidence", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
					{ID: "findings", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_report", "link_successor", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
					{ID: "conclude", Kind: WorkflowStepInternalSQLite, Actions: []string{"record_conclusion", "record_verdict", "confirm_premise", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"}},
					{ID: "complete", Kind: WorkflowStepInternalSQLite, Actions: []string{"bind_evidence", "complete", "record_work_context", "checkpoint_context", "cross_context_boundary"}},
				},
				Edges: []WorkflowEdge{
					{From: "frame", To: "investigate", Kind: WorkflowEdgeForward}, {From: "investigate", To: "findings", Kind: WorkflowEdgeForward},
					{From: "findings", To: "conclude", Kind: WorkflowEdgeForward}, {From: "conclude", To: "complete", Kind: WorkflowEdgeForward},
				},
			},
			RequiredEvidenceKinds: []EvidenceKind{EvidenceArtifact},
			OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"no_change", "resolved", "report_recorded"}},
			RigorRules:            []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification}}},
			StalenessRules:        []WorkflowStalenessRule{}, CompositionRules: WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: []WorkKind{WorkKindBreakFix, WorkKindArchitectureSpike, WorkKindStaticAnalysis}, ForbiddenCompositions: []WorkflowForbiddenComposition{}},
			RecoveryRoutes: []WorkflowRecoveryRoute{
				{Step: "conclude", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate"},
				{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate"},
			},
		},
	}
	for i := range definitions {
		populateBuiltinWorkflowActions(&definitions[i])
		definitions[i] = cloneWorkflowDefinition(definitions[i])
		if err := ValidateWorkflowDefinition(definitions[i]); err != nil {
			panic(fmt.Sprintf("built-in workflow %s: %v", definitions[i].Ref, err))
		}
	}
	return definitions
}

// populateBuiltinWorkflowActions preserves the first-use order of action contracts.
func populateBuiltinWorkflowActions(definition *WorkflowDefinition) {
	catalog := builtinWorkflowActionCatalog()
	for _, step := range definition.StepGraph.Steps {
		for _, id := range step.Actions {
			if slices.Contains(definition.AvailableActions, id) {
				continue
			}
			action, err := catalog.action(id)
			if err != nil {
				panic(fmt.Sprintf("built-in workflow %s: %v", definition.Ref, err))
			}
			if id == "approve_contract" {
				action.Payload.Fields = append(slices.Clone(action.Payload.Fields), workflowDecisionFactField())
			}
			definition.AvailableActions = append(definition.AvailableActions, id)
			definition.ActionDefinitions = append(definition.ActionDefinitions, action)
		}
	}
}
