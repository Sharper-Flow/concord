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
	confirmPremiseOperatorPayload = withClosedPayload(confirmPremiseOperatorFields)
	proposalOutOfScopePayload     = withClosedPayload(proposalOutOfScopeActionFields)
	canonicalVerdictPayload       = withClosedPayload(canonicalVerdictActionFields)
	openFindingsCorrection        = withAppendedPayloadField(workflowOpenFindingsCorrectionField)
)

func confirmPremiseOperatorFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		{Name: "selected_choice", ValueType: PayloadString, Required: true, Envelope: true, Enum: []string{"confirm", "revise", "stop"}},
		{Name: "decision_context_digest", ValueType: PayloadDigest, Required: true, Envelope: true},
		actionIntegerField("contract_version", false, 2147483647),
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

func canonicalVerdictActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionIntegerField("contract_version", false, 2147483647),
		actionItemArrayField("verdicts", true, 1, 8, "workflow_verdict_batch_entry"),
	}
}

// The engine cross-field declaration covers both scalar and batch inputs;
// current publication projects its rules onto the batch-only action fields.
func verdictBatchActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionIntegerField("contract_version", false, 2147483647),
		actionRefField("predicate_id", false),
		actionEnumField("verdict_kind", false, "ok", "outcome_mismatch", "insufficient_evidence"),
		actionStringField("verdict_actor_ref", false, 70),
		actionListField("evaluation_evidence", false, 1, 32),
		{Name: "incomparable_with_approved", ValueType: PayloadBoolean},
		actionItemArrayField("verdicts", false, 1, 8, "workflow_verdict_batch_entry"),
	}
}
