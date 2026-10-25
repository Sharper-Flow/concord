package store

func actionDefinitions(ids []string, payloadContracts bool) []WorkflowActionDefinition {
	result := make([]WorkflowActionDefinition, 0, len(ids))
	for _, id := range ids {
		result = append(result, currentActionDefinition(id, payloadContracts))
	}
	return result
}

func graph(steps []WorkflowStep, edges []WorkflowEdge, terminal string) WorkflowStepGraph {
	return WorkflowStepGraph{StartStep: steps[0].ID, TerminalSteps: []string{terminal}, Steps: steps, Edges: edges}
}

func step(id string, kind WorkflowStepKind, actions ...string) WorkflowStep {
	return WorkflowStep{ID: id, Kind: kind, Actions: actions}
}

func forward(ids ...string) []WorkflowEdge {
	result := make([]WorkflowEdge, 0, len(ids)-1)
	for i := 0; i < len(ids)-1; i++ {
		result = append(result, WorkflowEdge{From: ids[i], To: ids[i+1], Kind: WorkflowEdgeForward})
	}
	return result
}

func addEdge(edges []WorkflowEdge, from, to string, kind WorkflowEdgeKind) []WorkflowEdge {
	return append(edges, WorkflowEdge{From: from, To: to, Kind: kind})
}

// This synthetic, non-Product-changing fixture exercises generic fold and
// transaction invariants independently of the live Product workflow policy.
func workflowFixtureBase() WorkflowDefinition {
	steps := []WorkflowStep{step("proposal", WorkflowStepInternalSQLite, "record_proposal"), step("discovery", WorkflowStepInternalSQLite, "record_discovery"), step("design", WorkflowStepInternalSQLite, "record_design"), step("planning", WorkflowStepHumanCheckpoint, "approve_contract"), step("execution", WorkflowStepExternalEffect, "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor", "record_delivery"), step("acceptance", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("release", WorkflowStepInternalSQLite, "complete")}
	ids := []string{"proposal", "discovery", "design", "planning", "execution", "acceptance", "release"}
	actions := []string{"record_proposal", "record_discovery", "record_design", "approve_contract", "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor", "record_delivery", "record_verdict", "confirm_premise", "complete", "checkpoint_context", "cross_context_boundary"}
	for i := range steps {
		steps[i].Actions = append(steps[i].Actions, "checkpoint_context", "cross_context_boundary")
	}
	edges := addEdge(forward(ids...), "execution", "execution", WorkflowEdgeRetry)
	truth := false
	declarations := actionDefinitions(actions, false)
	for i := range declarations {
		if declarations[i].ID == "checkpoint_context" || declarations[i].ID == "cross_context_boundary" {
			declarations[i] = currentActionDefinition(declarations[i].ID, true)
		}
	}
	return WorkflowDefinition{Ref: workflowFixtureRef, WorkKind: WorkKindStaticAnalysis, ChangesProductTruth: &truth,
		StepGraph: graph(steps, edges, "release"), AvailableActions: actions, ActionDefinitions: declarations,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceReview},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{}, CompositionRules: forwardOnlyComposition(WorkKindBreakFix, WorkKindResearch),
		RecoveryRoutes: []WorkflowRecoveryRoute{
			{Step: "acceptance", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
			{Step: "release", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
			{Step: "release", Trigger: WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: "execution"},
		}}
}

func genericDefinitionFixture() WorkflowDefinition {
	definition := workflowFixtureBase()
	definition.Ref = "workflow.generic_fixture"
	definition.WorkKind = WorkKindGenericOneOff
	definition.OutcomeSchema = WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateOutcome, PredicateCheck}, AllowedOutcomeTokens: []string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}}
	return definition
}
