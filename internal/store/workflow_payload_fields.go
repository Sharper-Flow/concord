package store

var designDecisionItemSchema = withPayloadFieldRevision("decisions", func(field WorkflowPayloadField) WorkflowPayloadField {
	return actionItemArrayField("decisions", field.Required, *field.MinItems, *field.MaxItems, field.SchemaRef)
})

var premiseFloor = withPayloadFieldRevision("premise", func(field WorkflowPayloadField) WorkflowPayloadField {
	field.MinLength = workflowInt(workflowStringMinBytes)
	return field
})

var (
	confirmPremiseOperatorPayload = withClosedPayload(confirmPremiseOperatorFields)
	acceptDeliveryPayload         = withClosedPayload(acceptDeliveryActionFields)
	proposalOutOfScopePayload     = withClosedPayload(proposalOutOfScopeActionFields)
	canonicalVerdictPayload       = withClosedPayload(canonicalVerdictActionFields)
	acceptanceOracleRevision      = withAppendedPayloadField(acceptanceOracleActionField)
	openFindingsCorrection        = withAppendedPayloadField(workflowOpenFindingsCorrectionField)
)

func confirmPremiseOperatorFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		{Name: "selected_choice", ValueType: PayloadString, Required: true, Envelope: true, Enum: []string{"confirm", "revise", "stop"}},
		{Name: "decision_context_digest", ValueType: PayloadDigest, Required: true, Envelope: true},
		actionIntegerField("contract_version", false, 2147483647),
	}
}

func acceptDeliveryActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionRefField("attempt_id", true), actionIntegerField("attempt_epoch", true, 2147483647),
		actionRefField("delivery_artifact", false), actionEnumField("delivery_state", false, "asserted"),
	}
}

func proposalOutOfScopeActionFields() []WorkflowPayloadField {
	fields := make([]WorkflowPayloadField, 0, len(builtinActionPolicies["record_proposal"].Payload.Fields)+1)
	fields = append(fields, builtinActionPolicies["record_proposal"].Payload.Fields...)
	return append(fields, actionProseListField("out_of_scope", false, 0, 16, "proposal_text"))
}

func workflowWorkerJobsActive(definition WorkflowDefinition) bool {
	return workflowCapabilities(definition).has(WorkflowCapabilityWorkerJobs)
}

func acceptanceOracleActionField() WorkflowPayloadField {
	return actionObjectField("acceptance_oracle", true, "worker_acceptance_oracle")
}

func workflowOpenFindingsCorrectionField() WorkflowPayloadField {
	return actionIDListField("open_finding_ids", false, 1, 32)
}

func checksRepairAuthoringRevision(action WorkflowActionDefinition) WorkflowActionDefinition {
	action = withAppendedPayloadField(checksRepairRevisionsActionField)(action)
	return withAppendedPayloadField(checksRepairReasonActionField)(action)
}

func checksRepairRevisionsActionField() WorkflowPayloadField {
	return WorkflowPayloadField{Name: "repair_revisions", ValueType: PayloadArray, Required: false, MinItems: workflowInt(1), MaxItems: workflowInt(64), ItemRef: "worker_job_repair_revision"}
}

func checksRepairReasonActionField() WorkflowPayloadField {
	return actionStringField("repair_reason", false, 4096)
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
