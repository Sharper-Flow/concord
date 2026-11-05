package store

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

var designDecisionItemSchema = withPayloadFieldRevision("decisions", func(field WorkflowPayloadField) WorkflowPayloadField {
	return actionItemArrayField("decisions", field.Required, *field.MinItems, *field.MaxItems, field.SchemaRef)
})

var premiseFloor = withPayloadFieldRevision("premise", func(field WorkflowPayloadField) WorkflowPayloadField {
	field.MinLength = workflowInt(workflowStringMinBytes)
	return field
})

var (
	confirmPremisePayload     = withClosedPayload(confirmPremiseActionFields)
	proposalOutOfScopePayload = withClosedPayload(proposalOutOfScopeActionFields)
	openFindingsCorrection    = withAppendedPayloadField(workflowOpenFindingsCorrectionField)
)

func confirmPremiseActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionContractVersionField(false),
	}
}

func proposalOutOfScopeActionFields() []WorkflowPayloadField {
	fields := make([]WorkflowPayloadField, 0, len(builtinActionPolicies["record_proposal"].Payload.Fields)+1)
	fields = append(fields, builtinActionPolicies["record_proposal"].Payload.Fields...)
	return append(fields, actionProseListField("out_of_scope", false, 0, 16, "proposal_text"))
}

func workflowOpenFindingsCorrectionField() WorkflowPayloadField {
	return actionIDListField("open_finding_ids", false, 1, 32)
}
