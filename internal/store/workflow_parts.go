package store

import (
	"fmt"
	"regexp"
	"slices"
)

// WorkflowPart owns one unit of steps with declared parameters. Each step is
// defined in exactly one part; a variant is a parameter of its part, never a
// copy. Compositions resolve the current parts into the canonical definition.
type WorkflowPart struct {
	Ref  string           `json:"ref"`
	Kind WorkflowStepKind `json:"kind"`
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
	// Capabilities are the behaviors the part's step enables. A composed
	// definition enables the union over its parts.
	Capabilities []WorkflowCapability `json:"capabilities"`
}

// WorkflowPartRoute declares one recovery route a part's step opens.
type WorkflowPartRoute struct {
	Trigger WorkflowRecoveryTrigger `json:"trigger"`
	Action  string                  `json:"action"`
}

// WorkflowPartUse names one current part in a composition and sets its
// parameters.
type WorkflowPartUse struct {
	Ref                   string
	StepID                string
	LeadAction            string
	FailureTo             string
	RecoveryTo            string
	DisprovedPremiseRoute bool
}

var workflowPartRefPattern = regexp.MustCompile(`^part\.[a-z][a-z0-9_.-]{1,62}$`)

func partFailure(detail string) error { return definitionFailure(KindInvalidDefinition, detail) }

// builtinWorkflowParts lists the one current part per stable ref.
func builtinWorkflowParts() []WorkflowPart {
	unhealthy := []WorkflowPartRoute{{Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction"}}
	return []WorkflowPart{
		{Ref: "part.proposal", Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"proposal"},
			Actions: []string{"record_proposal", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
		{Ref: "part.alignment", Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"alignment"},
			Actions: []string{"record_alignment", "record_work_context", "checkpoint_context", "cross_context_boundary"}},
		{Ref: "part.design", Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"design"},
			Actions: []string{"record_design", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
		{Ref: "part.diagnosis", Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"diagnosis"},
			Actions: []string{"record_diagnosis", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
		{Ref: "part.planning", Kind: WorkflowStepHumanCheckpoint, StepIDOptions: []string{"planning", "frame"},
			LeadActionOptions: []string{"frame_research"},
			Actions:           []string{"approve_contract", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
		{Ref: "part.execution", Kind: WorkflowStepExternalEffect, StepIDOptions: []string{"execution"},
			Actions: []string{"start_execution", "checkpoint_execution", "bind_evidence", "link_successor", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"},
			Retry:   true,
			Routes:  unhealthy},
		{Ref: "part.repair", Kind: WorkflowStepExternalEffect, StepIDOptions: []string{"repair"},
			Actions:      []string{"checkpoint_repair", "bind_evidence", "link_successor", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"},
			Retry:        true,
			Routes:       unhealthy,
			Capabilities: []WorkflowCapability{WorkflowCapabilityDerivedStepStart}},
		{Ref: "part.investigate", Kind: WorkflowStepCrossAuthority, StepIDOptions: []string{"investigate"},
			Actions: []string{"record_finding", "revise_candidates", "bind_evidence", "checkpoint_context", "cross_context_boundary", "record_work_context"}},
		{Ref: "part.findings", Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"findings"},
			Actions:      []string{"record_report", "link_successor", "checkpoint_context", "cross_context_boundary", "record_work_context"},
			Capabilities: []WorkflowCapability{WorkflowCapabilityRootCauseAnalysis}},
		{Ref: "part.refine", Kind: WorkflowStepExternalEffect, StepIDOptions: []string{"refine"},
			Actions:      []string{"checkpoint_refine", "bind_evidence", "record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary", "record_review", "record_work_context"},
			Routes:       unhealthy,
			Capabilities: []WorkflowCapability{WorkflowCapabilityDerivedStepStart, WorkflowCapabilityRefineProof}},
		{Ref: "part.delivery", Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"delivery"},
			Actions:      []string{"record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary"},
			Capabilities: []WorkflowCapability{WorkflowCapabilityProducesDelivery}},
		{Ref: "part.verdict", Kind: WorkflowStepHumanCheckpoint, StepIDOptions: []string{"acceptance", "verify", "conclude"},
			LeadActionOptions: []string{"bind_evidence", "record_conclusion"},
			Actions:           []string{"record_verdict", "confirm_premise", "checkpoint_context", "cross_context_boundary", "record_work_context"},
			FailureEdge:       true,
			Routes:            unhealthy},
		{Ref: "part.close", Kind: WorkflowStepInternalSQLite, StepIDOptions: []string{"release", "complete"},
			Actions:               []string{"bind_evidence", "complete", "record_work_context", "checkpoint_context", "cross_context_boundary"},
			Routes:                unhealthy,
			DisprovedPremiseRoute: true},
	}
}

// workflowPartCatalog validates a part list and indexes it by ref. It
// refuses an invalid ref or shape, a duplicate ref, and a step ID that two
// parts define.
func workflowPartCatalog(parts []WorkflowPart) (map[string]WorkflowPart, error) {
	catalog := make(map[string]WorkflowPart, len(parts))
	owners := map[string]string{}
	for _, part := range parts {
		if !workflowPartRefPattern.MatchString(part.Ref) {
			return nil, partFailure("workflow part ref is invalid")
		}
		if _, exists := catalog[part.Ref]; exists {
			return nil, partFailure(fmt.Sprintf("workflow part %s is declared twice", part.Ref))
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
		catalog[part.Ref] = part
	}
	return catalog, nil
}

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
	if !validWorkflowCapabilities(part.Capabilities) {
		return partFailure(fmt.Sprintf("workflow part %s declares invalid capabilities", part.Ref))
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
