package store

import (
	"fmt"
	"regexp"
	"slices"
)

// WorkflowPart is one released, versioned unit of steps (CD-0229 D1) with
// declared parameters. Each step is defined in exactly one part; a variant
// of a step is a parameter of its part, never a copy. A released part
// version is frozen (D3): a content change is a new part version, which the
// compositions that want it pin explicitly.
type WorkflowPart struct {
	Ref     string           `json:"ref"`
	Version int              `json:"version"`
	Kind    WorkflowStepKind `json:"kind"`
	// StepIDOptions lists the step IDs the part can emit. A use names one
	// when there is more than one.
	StepIDOptions []string `json:"step_id_options"`
	// LeadActionOptions lists the actions a use may place first on the
	// step, ahead of Actions. A use may also name none.
	LeadActionOptions []string `json:"lead_action_options,omitempty"`
	// Actions is the step's ordered action list. Order is behavior: the
	// delivery-gate reader reads it (workflowStepIsDeliveryGate).
	Actions []string `json:"actions"`
	// Retry declares the step's retry edge to itself.
	Retry bool `json:"retry,omitempty"`
	// FailureEdge lets a use name a failure edge target.
	FailureEdge bool `json:"failure_edge,omitempty"`
	// Routes are the recovery routes the step opens. A part with routes
	// needs a use to name the recovery target.
	Routes []WorkflowPartRoute `json:"routes,omitempty"`
	// DisprovedPremiseRoute lets a use add the route that supersedes the
	// contract when the premise is disproved at completion.
	DisprovedPremiseRoute bool `json:"disproved_premise_route,omitempty"`
	// Capabilities are the versioned behaviors the part's step enables. A
	// composed definition enables the union over its parts.
	Capabilities workflowCapabilitySet `json:"capabilities"`
}

// WorkflowPartRoute declares one recovery route a part's step opens.
type WorkflowPartRoute struct {
	Trigger WorkflowRecoveryTrigger `json:"trigger"`
	Action  string                  `json:"action"`
}

// WorkflowPartUse pins one part version in a composition and sets its
// parameters.
type WorkflowPartUse struct {
	Ref                   string
	Version               int
	StepID                string
	LeadAction            string
	FailureTo             string
	RecoveryTo            string
	DisprovedPremiseRoute bool
}

var workflowPartRefPattern = regexp.MustCompile(`^part\.[a-z][a-z0-9_.-]{1,62}$`)

func partFailure(detail string) error { return definitionFailure(KindInvalidDefinition, detail) }

const (
	workflowWorkerResultCapabilities = workflowCapabilitySet(WorkflowCapabilityDerivedWorkerFailure | WorkflowCapabilityFailureAttribution)
	workflowJobCapabilities          = workflowWorkerResultCapabilities | workflowCapabilitySet(WorkflowCapabilityWorkerJobs)
)

// builtinWorkflowParts lists every released part version. Part catalog v1
// holds the steps of the three live families.
func builtinWorkflowParts() []WorkflowPart {
	unhealthy := []WorkflowPartRoute{{Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction"}}
	return []WorkflowPart{
		{Ref: "part.proposal", Version: 1, Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"proposal"},
			Actions:      []string{"record_proposal", "checkpoint_context", "cross_context_boundary", "accept_worker_result", "dispatch_worker", "record_work_context"},
			Capabilities: workflowWorkerResultCapabilities},
		{Ref: "part.alignment", Version: 1, Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"alignment"},
			Actions: []string{"record_alignment", "record_work_context", "checkpoint_context", "cross_context_boundary"}},
		{Ref: "part.design", Version: 1, Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"design"},
			Actions:      []string{"record_design", "checkpoint_context", "cross_context_boundary", "accept_worker_result", "dispatch_worker", "record_work_context"},
			Capabilities: workflowWorkerResultCapabilities},
		{Ref: "part.diagnosis", Version: 1, Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"diagnosis"},
			Actions: []string{"record_diagnosis", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
		{Ref: "part.planning", Version: 1, Kind: WorkflowStepHumanCheckpoint, StepIDOptions: []string{"planning", "frame"},
			LeadActionOptions: []string{"frame_research"},
			Actions:           []string{"approve_contract", "checkpoint_context", "cross_context_boundary", "dispatch_worker", "accept_worker_evidence", "record_work_context"}},
		{Ref: "part.execution", Version: 1, Kind: WorkflowStepExternalEffect, StepIDOptions: []string{"execution"},
			Actions:      []string{"start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "accept_worker_result", "dispatch_worker", "record_worker_job", "record_work_context"},
			Retry:        true,
			Routes:       unhealthy,
			Capabilities: workflowJobCapabilities},
		{Ref: "part.repair", Version: 1, Kind: WorkflowStepExternalEffect, StepIDOptions: []string{"repair"},
			Actions:      []string{"checkpoint_repair", "bind_evidence", "link_successor", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "accept_worker_result", "dispatch_worker", "record_worker_job", "record_work_context"},
			Retry:        true,
			Routes:       unhealthy,
			Capabilities: workflowJobCapabilities | workflowCapabilitySet(WorkflowCapabilityDerivedStepStart)},
		{Ref: "part.investigate", Version: 1, Kind: WorkflowStepCrossAuthority, StepIDOptions: []string{"investigate"},
			Actions:      []string{"record_finding", "revise_candidates", "bind_evidence", "checkpoint_context", "cross_context_boundary", "accept_worker_result", "dispatch_worker", "record_work_context"},
			Capabilities: workflowWorkerResultCapabilities},
		{Ref: "part.findings", Version: 1, Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"findings"},
			Actions:      []string{"record_report", "link_successor", "checkpoint_context", "cross_context_boundary", "accept_worker_result", "dispatch_worker", "record_work_context"},
			Capabilities: workflowWorkerResultCapabilities | workflowCapabilitySet(WorkflowCapabilityRootCauseAnalysis)},
		{Ref: "part.refine", Version: 1, Kind: WorkflowStepExternalEffect, StepIDOptions: []string{"refine"},
			Actions:      []string{"checkpoint_refine", "bind_evidence", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "accept_worker_result", "dispatch_worker", "record_worker_job", "record_work_context"},
			Routes:       unhealthy,
			Capabilities: workflowJobCapabilities | workflowCapabilitySet(WorkflowCapabilityDerivedStepStart|WorkflowCapabilityAcceptDelivery|WorkflowCapabilityRefineProof)},
		{Ref: "part.delivery", Version: 1, Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"delivery"},
			Actions:      []string{"record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary"},
			Capabilities: workflowCapabilitySet(WorkflowCapabilityProducesDelivery)},
		{Ref: "part.verdict", Version: 1, Kind: WorkflowStepHumanCheckpoint, StepIDOptions: []string{"acceptance", "verify", "conclude"},
			LeadActionOptions: []string{"bind_evidence", "record_conclusion"},
			Actions:           []string{"record_verdict", "confirm_premise", "checkpoint_context", "cross_context_boundary", "dispatch_worker", "accept_worker_result", "record_worker_job", "record_work_context"},
			FailureEdge:       true,
			Routes:            unhealthy},
		{Ref: "part.close", Version: 1, Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"release", "complete"},
			Actions:               []string{"complete", "record_work_context", "checkpoint_context", "cross_context_boundary"},
			Routes:                unhealthy,
			DisprovedPremiseRoute: true},
	}
}

// workflowPartCatalog validates a part list and indexes it by ref and
// version. It refuses an invalid identity or shape, a duplicate version, and
// a step ID that two parts define.
func workflowPartCatalog(parts []WorkflowPart) (map[string]WorkflowPart, error) {
	catalog := make(map[string]WorkflowPart, len(parts))
	owners := map[string]string{}
	for _, part := range parts {
		if !workflowPartRefPattern.MatchString(part.Ref) || part.Version < 1 {
			return nil, partFailure("workflow part identity is invalid")
		}
		key := workflowPartKey(part.Ref, part.Version)
		if _, exists := catalog[key]; exists {
			return nil, partFailure(fmt.Sprintf("workflow part %s is declared twice", key))
		}
		if err := validateWorkflowPart(part); err != nil {
			return nil, err
		}
		for _, id := range part.StepIDOptions {
			if owner, taken := owners[id]; taken && owner != part.Ref {
				return nil, partFailure(fmt.Sprintf("step %s is defined by %s and %s", id, owner, part.Ref))
			}
			owners[id] = part.Ref
		}
		catalog[key] = part
	}
	return catalog, nil
}

func workflowPartKey(ref string, version int) string { return fmt.Sprintf("%s v%d", ref, version) }

func validateWorkflowPart(part WorkflowPart) error {
	if !validWorkflowStepKind(part.Kind) || len(part.StepIDOptions) < 1 || !uniqueStrings(part.StepIDOptions) || len(part.Actions) < 1 || !uniqueStrings(append(slices.Clone(part.Actions), part.LeadActionOptions...)) {
		return partFailure(fmt.Sprintf("workflow part %s has an invalid step shape", part.Ref))
	}
	for _, id := range part.StepIDOptions {
		if !validWorkflowID(id) {
			return partFailure(fmt.Sprintf("workflow part %s declares an invalid step ID", part.Ref))
		}
	}
	for _, route := range part.Routes {
		if !validWorkflowRecoveryTrigger(route.Trigger) || !workflowRecoveryRouteActionDeclared(route.Action) {
			return partFailure(fmt.Sprintf("workflow part %s declares an invalid recovery route", part.Ref))
		}
	}
	if part.DisprovedPremiseRoute && len(part.Routes) == 0 {
		return partFailure(fmt.Sprintf("workflow part %s offers a premise route without a recovery target", part.Ref))
	}
	return nil
}

// compileWorkflowPartUse resolves one use against its part: the step it
// emits and the use's checked parameters.
func compileWorkflowPartUse(part WorkflowPart, use WorkflowPartUse) (WorkflowStep, error) {
	stepID := use.StepID
	if stepID == "" && len(part.StepIDOptions) == 1 {
		stepID = part.StepIDOptions[0]
	}
	if !slices.Contains(part.StepIDOptions, stepID) {
		return WorkflowStep{}, partFailure(fmt.Sprintf("use of %s names step %q, not one of %v", part.Ref, use.StepID, part.StepIDOptions))
	}
	if use.LeadAction != "" && !slices.Contains(part.LeadActionOptions, use.LeadAction) {
		return WorkflowStep{}, partFailure(fmt.Sprintf("use of %s names lead action %q, not one of %v", part.Ref, use.LeadAction, part.LeadActionOptions))
	}
	if use.FailureTo != "" && !part.FailureEdge {
		return WorkflowStep{}, partFailure(fmt.Sprintf("part %s declares no failure edge", part.Ref))
	}
	if (use.RecoveryTo != "") != (len(part.Routes) > 0) {
		return WorkflowStep{}, partFailure(fmt.Sprintf("use of %s must name a recovery target exactly when the part opens recovery routes", part.Ref))
	}
	if use.DisprovedPremiseRoute && !part.DisprovedPremiseRoute {
		return WorkflowStep{}, partFailure(fmt.Sprintf("part %s offers no premise route", part.Ref))
	}
	actions := make([]string, 0, len(part.Actions)+1)
	if use.LeadAction != "" {
		actions = append(actions, use.LeadAction)
	}
	return WorkflowStep{ID: stepID, Kind: part.Kind, Actions: append(actions, part.Actions...)}, nil
}
