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

// withAppendedPayloadFields appends several optional members to one action's
// payload, the multi-member form of withAppendedPayloadField.
func withAppendedPayloadFields(fields func() []WorkflowPayloadField) workflowActionRevision {
	return func(action WorkflowActionDefinition) WorkflowActionDefinition {
		action.Payload.Fields = append(slices.Clone(action.Payload.Fields), fields()...)
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
// a family head.
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

// builtinWorkflowActionCatalogs lists the catalogs the current families use.
func builtinWorkflowActionCatalogs() []workflowActionCatalog {
	return []workflowActionCatalog{workflowActionCatalogV1(), workflowActionCatalogV3()}
}

// latestCatalogAction returns an action as the latest catalog that carries
// it declares it: the shape current definitions pin and publication projects.
func latestCatalogAction(actionID string) (WorkflowActionDefinition, error) {
	catalogs := builtinWorkflowActionCatalogs()
	for index := len(catalogs) - 1; index >= 0; index-- {
		if _, carried := catalogs[index].revisions[actionID]; carried {
			return catalogs[index].action(actionID)
		}
	}
	return WorkflowActionDefinition{}, definitionFailure(KindDefinitionActionOrStepUnknown, "action "+actionID+" is in no action catalog")
}

func builtinWorkflowActionCatalog(version int) (workflowActionCatalog, bool) {
	for _, catalog := range builtinWorkflowActionCatalogs() {
		if catalog.Version == version {
			return catalog, true
		}
	}
	return workflowActionCatalog{}, false
}

// workflowActionCatalogV1 supplies the research family's current contracts.
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
		"record_worker_job":      {workTransitionAction, acceptanceOracleRevision, checksRepairAuthoringRevision, withAppendedPayloadFields(workerJobCallerScopeActionFields)},
		"request_correction":     {workTransitionAction, openFindingsCorrection, failureAttributionCorrection},
		"revise_candidates":      nil,
		"start_execution":        nil,
	}}
}

// workerJobCallerScopeActionFields declares the optional direct-caller
// allowance the shared catalog declares on record_worker_job (CD-0227 D1 as
// amended by CD-0242 D6). Whether one job carries it or not is the
// authoring rule the store guard enforces; the declared action contract
// admits both shapes.
func workerJobCallerScopeActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionObjectField("direct_caller_allowance", false, "worker_job_direct_caller_allowance"),
	}
}

// workflowActionCatalogV3 supplies implementation and break-fix contracts
// with their dispatch packet. Every other action reuses the common catalog.
func workflowActionCatalogV3() workflowActionCatalog {
	base := workflowActionCatalogV1()
	revisions := make(map[string][]workflowActionRevision, len(base.revisions))
	for actionID, actionRevisions := range base.revisions {
		revisions[actionID] = actionRevisions
	}
	revisions["dispatch_worker"] = []workflowActionRevision{workerDispatchAction, withWorkerPacketRef(workerPacket5Schema)}
	return workflowActionCatalog{Version: 3, revisions: revisions}
}

// workerPacket5Schema is the packet closure the v3 catalog's dispatch_worker
// anchors: the checks-repair packet4's shape with the worker_job beside its
// repair metadata and the dispatch-time caller exclusions.
const workerPacket5Schema = "worker_packet5"
