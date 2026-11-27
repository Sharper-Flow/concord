package store

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

var confirmPremisePayload = withClosedPayload(confirmPremiseActionFields)

func confirmPremiseActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionContractVersionField(false),
	}
}
