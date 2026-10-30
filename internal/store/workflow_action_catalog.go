package store

import (
	"fmt"
	"slices"
)

// A workflowActionRevision composes one action contract without mutating the
// policy-table slices it receives.
type workflowActionRevision func(WorkflowActionDefinition) WorkflowActionDefinition

func withRequiredCapability(capability string) workflowActionRevision {
	return func(action WorkflowActionDefinition) WorkflowActionDefinition {
		action.RequiredCapability = capability
		return action
	}
}

var workTransitionAction = withRequiredCapability("work_transition")

func withClosedPayload(fields func() []WorkflowPayloadField) workflowActionRevision {
	return func(action WorkflowActionDefinition) WorkflowActionDefinition {
		action.Payload = WorkflowPayloadDefinition{Closed: true, Fields: fields()}
		return action
	}
}

func withAppendedPayloadField(field func() WorkflowPayloadField) workflowActionRevision {
	return func(action WorkflowActionDefinition) WorkflowActionDefinition {
		action.Payload.Fields = append(slices.Clone(action.Payload.Fields), field())
		return action
	}
}

// withPayloadFieldRevision rewrites each payload field with the given name.
func withPayloadFieldRevision(name string, revise func(WorkflowPayloadField) WorkflowPayloadField) workflowActionRevision {
	return func(action WorkflowActionDefinition) WorkflowActionDefinition {
		fields := slices.Clone(action.Payload.Fields)
		for index := range fields {
			if fields[index].Name == name {
				fields[index] = revise(fields[index])
			}
		}
		action.Payload.Fields = fields
		return action
	}
}

// workflowActionCatalog owns each action contract once (CD-0229 D2): an
// action's definition is its policy-table base with the catalog's revisions
// for it applied in order. A part step names action IDs; the compiled action
// definitions come from here and never from a family head.
type workflowActionCatalog map[string][]workflowActionRevision

func (catalog workflowActionCatalog) action(actionID string) (WorkflowActionDefinition, error) {
	revisions, ok := catalog[actionID]
	if !ok {
		return WorkflowActionDefinition{}, definitionFailure(KindDefinitionActionOrStepUnknown, fmt.Sprintf("action %s is not in the action catalog", actionID))
	}
	if _, ok := builtinActionPolicies[actionID]; !ok {
		return WorkflowActionDefinition{}, definitionFailure(KindDefinitionActionOrStepUnknown, fmt.Sprintf("action %s has no policy", actionID))
	}
	action := cloneWorkflowAction(currentActionDefinition(actionID, true))
	for _, revise := range revisions {
		action = revise(action)
	}
	return action, nil
}

func cloneWorkflowAction(action WorkflowActionDefinition) WorkflowActionDefinition {
	return cloneWorkflowDefinition(WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{action}}).ActionDefinitions[0]
}

// catalogAction returns one action as the current catalog declares it.
func catalogAction(actionID string) (WorkflowActionDefinition, error) {
	return builtinWorkflowActionCatalog().action(actionID)
}

// builtinWorkflowActionCatalog is the one current action catalog every
// composition compiles against.
func builtinWorkflowActionCatalog() workflowActionCatalog {
	return workflowActionCatalog{
		"approve_contract":       {premiseFloor},
		"bind_evidence":          nil,
		"checkpoint_context":     nil,
		"checkpoint_execution":   nil,
		"checkpoint_refine":      nil,
		"checkpoint_repair":      nil,
		"complete":               nil,
		"confirm_premise":        {confirmPremiseOperatorPayload},
		"cross_context_boundary": nil,
		"frame_research":         nil,
		"link_successor":         nil,
		"record_alignment":       nil,
		"record_conclusion":      nil,
		"record_delivery":        nil,
		"record_design":          {designDecisionItemSchema},
		"record_diagnosis":       nil,
		"record_finding":         nil,
		"record_proposal":        {proposalOutOfScopePayload},
		"record_report":          nil,
		"record_review": {workTransitionAction, withClosedPayload(func() []WorkflowPayloadField {
			return []WorkflowPayloadField{actionObjectField("review", true, "workflow_review_report")}
		})},
		"record_verdict":      {canonicalVerdictPayload},
		"record_work_context": {researchContextSelection},
		"request_correction":  {workTransitionAction, openFindingsCorrection},
		"revise_candidates":   nil,
		"start_execution":     nil,
	}
}
