package store

// WorkflowPlanActionContract returns the live catalog declaration used by plan admission.
func WorkflowPlanActionContract(id string) (WorkflowActionDefinition, error) {
	return catalogAction(id)
}

// WorkflowPlanContractUpdatePayload returns the declaration used by revise_intent.
func WorkflowPlanContractUpdatePayload() WorkflowPayloadDefinition {
	fields := append(BuiltinWorkflowContractUpdateFields(), workflowDecisionFactField())
	return WorkflowPayloadDefinition{Closed: true, Fields: fields}
}

// WorkflowPlanAllowedPredicates returns the predicate kinds and outcome tokens
// admitted when a contract for this plan type is approved.
func WorkflowPlanAllowedPredicates(planType WorkflowStepPlanType) ([]PredicateKind, []string) {
	if planType == WorkflowStepPlanResearch {
		return []PredicateKind{PredicateOutcome}, []string{"no_change", "resolved", "report_recorded"}
	}
	return []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, []string{}
}
