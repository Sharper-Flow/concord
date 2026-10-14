package store

import "slices"

// The declared input is the direct producer observation only. The recorded
// block's origin, core-derived identity, input population, sequence, and
// commit members are core-derived and are never authorable: the generated
// worker_failure_observation schema is closed over exactly observer, phase,
// cause, basis, sources, and diagnosis.

// workflowFailureAttributionActive reports whether a pinned definition
// carries the failure-attribution semantics. Earlier
// versions keep the payload, behavior, and digest they shipped with, exactly
// as workflowDerivedWorkerFailureActive holds the CD-0198 D5 boundary. The
// four retired families stay outside the gate.
func workflowFailureAttributionActive(definition WorkflowDefinition) bool {
	switch definition.Ref {
	case "workflow.implementation":
		return definition.Version >= 31
	case "workflow.break_fix":
		return definition.Version >= 28
	case "workflow.research":
		return definition.Version >= 18
	default:
		return false
	}
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

// withFailureAttributionCorrection appends the optional attribution field to
// request_correction's in-definition payload, whatever earlier composers
// restated it with — the owner-oracle families' open-findings member keeps
// its place. reject_worker_result carries no in-definition entry on any
// version — the engine-owned recovery route resolves it — so
// workflowCorrectionActionDefinitionFor owns its payload at every version,
// and this composer touches nothing else. The restated fields are the
// definition's own registry content, so every earlier version keeps the
// payload and digest it shipped.
func withFailureAttributionCorrection(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for index := range definition.ActionDefinitions {
		if definition.ActionDefinitions[index].ID != "request_correction" {
			continue
		}
		definition.ActionDefinitions[index].Payload.Fields = append(slices.Clone(definition.ActionDefinitions[index].Payload.Fields), workflowFailureAttributionField())
	}
	return definition
}

// Each builder below ships its family's failure-attribution head (the promoted slice
// phase 2): the promoted version restates the correction-request payload and
// activates workflowFailureAttributionActive for the parent wiring.
func implementationFailureAttributionV31() WorkflowDefinition {
	d := implementationCanonicalAuthoringV30()
	d.Version = 31
	return withFailureAttributionCorrection(d)
}

func breakFixFailureAttributionV28() WorkflowDefinition {
	d := breakFixCanonicalAuthoringV27()
	d.Version = 28
	return withFailureAttributionCorrection(d)
}

func researchFailureAttributionV18() WorkflowDefinition {
	d := researchDerivedFailureV17()
	d.Version = 18
	return withFailureAttributionCorrection(d)
}
