package store

// The declared input is the direct producer observation only. The recorded
// block's origin, core-derived identity, input population, sequence, and
// commit members are core-derived and are never authorable: the generated
// worker_failure_observation schema is closed over exactly observer, phase,
// cause, basis, sources, and diagnosis.

// workflowFailureAttributionActive reads the current composition's capability.
func workflowFailureAttributionActive(definition WorkflowDefinition) bool {
	return workflowCapabilities(definition).has(WorkflowCapabilityFailureAttribution)
}

// workflowFailureAttributionField declares the optional failure_attribution
// action input: one closed object whose generated schema owns the shape and
// bounds. No whole-value declaration rides the field — the store-owned
// 8192-byte block bound is the core's own bound, the same division the
// acceptance oracle's writer bound takes.
func workflowFailureAttributionField() WorkflowPayloadField {
	return actionObjectField("failure_attribution", false, "worker_failure_observation")
}

// workflowFailureAttributionFallbackFields restates one correction action's
// classic shared payload beside the optional attribution field. It serves the
// gate-active pins whose chain carries no in-definition correction entry —
// research — and gains exactly the attribution field over the payload that
// family already resolved. The classic fields are copied, never shared: the
// shared policy table stays byte-identical, so every released version keeps
// its digest.
func workflowFailureAttributionFallbackFields(actionID string) []WorkflowPayloadField {
	classic := workflowCorrectionActionDefinition()
	if actionID == "request_correction" {
		classic = workflowCorrectionRequestActionDefinition()
	}
	fields := make([]WorkflowPayloadField, 0, len(classic.Payload.Fields)+1)
	fields = append(fields, classic.Payload.Fields...)
	return append(fields, workflowFailureAttributionField())
}

// workflowCorrectionActionDefinitionFor resolves one correction action's
// payload for the pinned definition. The pinned definition's own declaration
// is the truth when its chain carries one; the engine-owned recovery shape
// resolves the rest, gaining the optional attribution field exactly when the
// pin activates the semantics. Released pins resolve the payload they shipped
// with, so the structural payload validation refuses the field on them.
func workflowCorrectionActionDefinitionFor(definition WorkflowDefinition, actionID string) WorkflowActionDefinition {
	for index := range definition.ActionDefinitions {
		if definition.ActionDefinitions[index].ID == actionID {
			return definition.ActionDefinitions[index]
		}
	}
	var action WorkflowActionDefinition
	switch actionID {
	case "reject_worker_result":
		action = workflowCorrectionActionDefinition()
	case "request_correction":
		action = workflowCorrectionRequestActionDefinition()
	default:
		return WorkflowActionDefinition{}
	}
	if !workflowFailureAttributionActive(definition) {
		return action
	}
	action.Payload = WorkflowPayloadDefinition{Closed: true, Fields: workflowFailureAttributionFallbackFields(actionID)}
	return action
}

var failureAttributionCorrection = withAppendedPayloadField(workflowFailureAttributionField)
