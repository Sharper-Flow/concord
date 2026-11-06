package store

// WorkflowPlanActionContract returns the live catalog declaration used by plan admission.
func WorkflowPlanActionContract(id string) (WorkflowActionDefinition, error) {
	return catalogAction(id)
}

// WorkflowPlanContractUpdatePayload returns the declaration used by revise_intent.
func WorkflowPlanContractUpdatePayload() WorkflowPayloadDefinition {
	return WorkflowPayloadDefinition{Closed: true, Fields: BuiltinWorkflowContractUpdateFields()}
}
