package store

import (
	"fmt"
	"slices"
)

// A workflowActionRevision is one change to one action's contract. Each
// revision is written once and serves two readers: the frozen chain
// builders apply it to the action's in-definition entry at the version that
// shipped it (reviseWorkflowAction), and the versioned action catalog folds
// it over the action's policy-table base. A revision never mutates the
// slices it receives, because the base shares them with
// builtinActionPolicies.
type workflowActionRevision func(WorkflowActionDefinition) WorkflowActionDefinition

// reviseWorkflowAction applies one revision to the named action's entry in a
// copy of the definition. Every other action and the graph are unchanged.
func reviseWorkflowAction(definition WorkflowDefinition, actionID string, revise workflowActionRevision) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	for index := range definition.ActionDefinitions {
		if definition.ActionDefinitions[index].ID == actionID {
			definition.ActionDefinitions[index] = revise(definition.ActionDefinitions[index])
		}
	}
	return definition
}

func withRequiredCapability(capability string) workflowActionRevision {
	return func(action WorkflowActionDefinition) WorkflowActionDefinition {
		action.RequiredCapability = capability
		return action
	}
}

// workTransitionAction and workerDispatchAction gate the worker actions on
// the agent capability the dispatcher must present. dispatch_worker needs
// worker_dispatch, so a worker holding only work_transition cannot dispatch
// a nested worker (CD-0059, CD-0017 D4).
var (
	workTransitionAction = withRequiredCapability("work_transition")
	workerDispatchAction = withRequiredCapability(WorkerDispatchCapability)
)

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

// withWorkerPacketRef points dispatch_worker's worker_packet field at one
// frozen packet schema closure.
func withWorkerPacketRef(schemaRef string) workflowActionRevision {
	return withPayloadFieldRevision("worker_packet", func(field WorkflowPayloadField) WorkflowPayloadField {
		field.SchemaRef = schemaRef
		return field
	})
}

// workflowActionCatalog is one released version of the action catalog
// (CD-0229 D2). It owns each action contract once: an action's definition is
// its policy-table base with the catalog's revisions for it applied in
// order. A part step names action IDs; the composition pins one catalog
// version, so the compiled action definitions come from here and never from
// a family head. A released catalog version is frozen; a contract change is
// a new catalog version.
type workflowActionCatalog struct {
	Version   int
	revisions map[string][]workflowActionRevision
}

func (catalog workflowActionCatalog) action(actionID string) (WorkflowActionDefinition, error) {
	revisions, ok := catalog.revisions[actionID]
	if !ok {
		return WorkflowActionDefinition{}, definitionFailure(KindDefinitionActionOrStepUnknown, fmt.Sprintf("action %s is not in action catalog v%d", actionID, catalog.Version))
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

// builtinWorkflowActionCatalogs lists every released action catalog version.
func builtinWorkflowActionCatalogs() []workflowActionCatalog {
	return []workflowActionCatalog{workflowActionCatalogV1(), workflowActionCatalogV2(), workflowActionCatalogV3()}
}

func builtinWorkflowActionCatalog(version int) (workflowActionCatalog, bool) {
	for _, catalog := range builtinWorkflowActionCatalogs() {
		if catalog.Version == version {
			return catalog, true
		}
	}
	return workflowActionCatalog{}, false
}

// workflowActionCatalogV1 is the action contract set the first composed
// heads ship: each action as the last chain heads declared it.
func workflowActionCatalogV1() workflowActionCatalog {
	return workflowActionCatalog{Version: 1, revisions: map[string][]workflowActionRevision{
		"accept_worker_evidence": {workTransitionAction},
		"accept_worker_result":   {workTransitionAction, acceptDeliveryPayload},
		"approve_contract":       {premiseFloor},
		"bind_evidence":          nil,
		"checkpoint_context":     nil,
		"checkpoint_execution":   nil,
		"checkpoint_refine":      nil,
		"checkpoint_repair":      nil,
		"complete":               nil,
		"confirm_premise":        {confirmPremiseOperatorPayload},
		"cross_context_boundary": nil,
		"declare_impact":         nil,
		"dispatch_worker":        {workerDispatchAction, withWorkerPacketRef(researchContextPacketSchema)},
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
		"record_verdict":         {canonicalVerdictPayload},
		"record_work_context":    {researchContextSelection},
		"record_worker_job":      {workTransitionAction, acceptanceOracleRevision},
		"request_correction":     {workTransitionAction, openFindingsCorrection, failureAttributionCorrection},
		"revise_candidates":      nil,
		"start_execution":        nil,
	}}
}

// workflowActionCatalogV2 restates record_worker_job with the checks repair's
// authoring members (CD-0205 D4): optional repair_revisions and
// repair_reason, supplied together, admitted only on the versions that pin
// this catalog. Its dispatch_worker points at the packet4 closure, the one
// named schema whose worker-job shape admits the repair metadata — the
// frozen packet2/packet3 closures keep the bytes they shipped with, and the
// generator's named additions never rewrite them. Every other action's
// contract stays byte-identical to v1.
func workflowActionCatalogV2() workflowActionCatalog {
	v1 := workflowActionCatalogV1()
	revisions := make(map[string][]workflowActionRevision, len(v1.revisions))
	for actionID, actionRevisions := range v1.revisions {
		revisions[actionID] = actionRevisions
	}
	revisions["dispatch_worker"] = []workflowActionRevision{workerDispatchAction, withWorkerPacketRef(checksRepairPacketSchema)}
	revisions["record_worker_job"] = []workflowActionRevision{workTransitionAction, acceptanceOracleRevision, checksRepairAuthoringRevision}
	return workflowActionCatalog{Version: 2, revisions: revisions}
}

func workflowActionCatalogV3() workflowActionCatalog {
	catalog := workflowActionCatalogV2()
	catalog.Version = 3
	catalog.revisions["complete"] = []workflowActionRevision{func(action WorkflowActionDefinition) WorkflowActionDefinition {
		action.Payload.Fields = append(slices.Clone(action.Payload.Fields), actionRefField("delivery_artifact", false), actionEnumField("delivery_state", false, "asserted"))
		return action
	}}
	return catalog
}
