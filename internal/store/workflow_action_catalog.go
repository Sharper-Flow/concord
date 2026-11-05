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

// workflowActionCatalog owns each action contract once (CD-0242 D3, D14): an
// action's definition is its policy-table base with the catalog's revisions
// for it applied in order. Workflow steps name action IDs whose definitions
// come from this catalog.
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
	clonePayload := func(payload WorkflowPayloadDefinition) WorkflowPayloadDefinition {
		payload.Fields = slices.Clone(payload.Fields)
		for i := range payload.Fields {
			field := &payload.Fields[i]
			field.Enum = slices.Clone(field.Enum)
			field.Forbidden = slices.Clone(field.Forbidden)
			for _, bound := range []**int64{&field.MinLength, &field.MaxLength, &field.MinItems, &field.MaxItems, &field.Minimum, &field.Maximum} {
				if *bound != nil {
					value := **bound
					*bound = &value
				}
			}
		}
		return payload
	}
	action.Payload = clonePayload(action.Payload)
	if action.PublicPayload != nil {
		payload := clonePayload(*action.PublicPayload)
		action.PublicPayload = &payload
	}
	return action
}

// catalogAction returns one action as the current catalog declares it.
func catalogAction(actionID string) (WorkflowActionDefinition, error) {
	return builtinWorkflowActionCatalog().action(actionID)
}

// builtinWorkflowActionCatalog is the current catalog for built-in workflows.
func builtinWorkflowActionCatalog() workflowActionCatalog {
	return workflowActionCatalog{
		"approve_contract":     {premiseFloor},
		"bind_evidence":        nil,
		"checkpoint_execution": nil,
		"checkpoint_refine":    nil,
		"checkpoint_repair":    nil,
		"complete":             nil,
		"complete_step":        nil,
		"confirm_premise":      {confirmPremisePayload},
		"frame_research":       nil,
		"insert_step":          nil,
		"link_successor":       nil,
		"pick_type":            nil,
		"record_clarify":       nil,
		"record_conclusion":    nil,
		"record_delivery":      nil,
		"record_diagnosis":     nil,
		"record_finding":       nil,
		"record_intake":        nil,
		"record_plan":          nil,
		"record_removal_scope": nil,
		"record_report":        nil,
		"record_review":        {workTransitionAction},
		"revise_candidates":    nil,
		"start_execution":      nil,
	}
}
