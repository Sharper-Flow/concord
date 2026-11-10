package store

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

var (
	confirmPremisePayload  = withClosedPayload(confirmPremiseActionFields)
	openFindingsCorrection = withAppendedPayloadField(workflowOpenFindingsCorrectionField)
)

func confirmPremiseActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionContractVersionField(false),
	}
}

func workflowOpenFindingsCorrectionField() WorkflowPayloadField {
	return actionIDListField("open_finding_ids", false, 1, 32)
}
