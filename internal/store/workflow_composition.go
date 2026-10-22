package store

import (
	"fmt"
	"slices"
	"sync"
)

// WorkflowComposition is the source of a workflow definition (CD-0229 D2). It
// holds the family policy and the ordered part pins; compiling it yields the
// definition fields CD-0013 D1 lists, so the canonical manifest and its
// digest cover the resolved current definition (CD-0242 D14).
type WorkflowComposition struct {
	Ref                   string
	Version               int64
	WorkKind              WorkKind
	RequiredEvidenceKinds []EvidenceKind
	OutcomeSchema         WorkflowOutcomeSchema
	RigorRules            []WorkflowRigorRule
	StalenessRules        []WorkflowStalenessRule
	CompositionRules      WorkflowCompositionRules
	EvaluatorIndependence WorkflowEvaluatorIndependence
	ApprovalPolicy        string
	// ProgressPolicy is the composition-level progress-policy discriminator
	// (CD-0164 D2a). Absence compiles the released legacy definition shape;
	// WorkflowProgressPolicyObligationSatisfactionV1 declares the
	// obligation-satisfaction policy and compiles the canonical satisfaction
	// taxonomy onto the resolved actions.
	ProgressPolicy       string
	ActionCatalogVersion int
	Parts                []WorkflowPartUse
}

// CompileWorkflowComposition compiles a composition against the built-in
// part and action catalogs. Steps follow part order. Forward edges chain the
// steps, then each retry part adds its retry edge, then each failure
// parameter adds its failure edge. Recovery routes follow part order, then
// each part's route order. The root action list and the action definitions
// list every action once, in first appearance over the compiled steps.
func CompileWorkflowComposition(composition WorkflowComposition) (WorkflowDefinition, error) {
	parts, err := workflowPartCatalog(builtinWorkflowParts())
	if err != nil {
		return WorkflowDefinition{}, err
	}
	catalog, ok := builtinWorkflowActionCatalog(composition.ActionCatalogVersion)
	if !ok {
		return WorkflowDefinition{}, partFailure(fmt.Sprintf("action catalog v%d is not released", composition.ActionCatalogVersion))
	}
	if len(composition.Parts) == 0 {
		return WorkflowDefinition{}, partFailure("a composition uses no parts")
	}
	resolved := make([]WorkflowPart, 0, len(composition.Parts))
	steps := make([]WorkflowStep, 0, len(composition.Parts))
	for _, use := range composition.Parts {
		part, ok := parts[workflowPartKey(use.Ref, use.Version)]
		if !ok {
			return WorkflowDefinition{}, partFailure(fmt.Sprintf("workflow part %s is not released", workflowPartKey(use.Ref, use.Version)))
		}
		compiled, err := compileWorkflowPartUse(part, use)
		if err != nil {
			return WorkflowDefinition{}, err
		}
		resolved = append(resolved, part)
		steps = append(steps, compiled)
	}
	edges := make([]WorkflowEdge, 0, 2*len(steps))
	for i := 1; i < len(steps); i++ {
		edges = append(edges, WorkflowEdge{From: steps[i-1].ID, To: steps[i].ID, Kind: WorkflowEdgeForward})
	}
	for i, part := range resolved {
		if part.Retry {
			edges = append(edges, WorkflowEdge{From: steps[i].ID, To: steps[i].ID, Kind: WorkflowEdgeRetry})
		}
	}
	var routes []WorkflowRecoveryRoute
	for i, use := range composition.Parts {
		if use.FailureTo != "" {
			edges = append(edges, WorkflowEdge{From: steps[i].ID, To: use.FailureTo, Kind: WorkflowEdgeFailure})
		}
		for _, route := range resolved[i].Routes {
			routes = append(routes, WorkflowRecoveryRoute{Step: steps[i].ID, Trigger: route.Trigger, Action: route.Action, Target: use.RecoveryTo})
		}
		if use.DisprovedPremiseRoute {
			routes = append(routes, WorkflowRecoveryRoute{Step: steps[i].ID, Trigger: WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: use.RecoveryTo})
		}
	}
	var available []string
	var actions []WorkflowActionDefinition
	for _, step := range steps {
		for _, actionID := range step.Actions {
			if slices.Contains(available, actionID) {
				continue
			}
			action, err := catalog.action(actionID)
			if err != nil {
				return WorkflowDefinition{}, err
			}
			available = append(available, actionID)
			actions = append(actions, action)
		}
	}
	if composition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1 {
		actions = withConsequenceConfirmPremise(actions)
	}
	if composition.ProgressPolicy == WorkflowProgressPolicyObligationSatisfactionV1 {
		actions = withObligationSatisfactionTaxonomy(actions)
	}
	changesProductTruth := workKindMayChangeProductTruth(composition.WorkKind)
	definition := cloneWorkflowDefinition(WorkflowDefinition{
		Ref: composition.Ref, Version: composition.Version, WorkKind: composition.WorkKind, ChangesProductTruth: &changesProductTruth,
		StepGraph:        WorkflowStepGraph{StartStep: steps[0].ID, TerminalSteps: []string{steps[len(steps)-1].ID}, Steps: steps, Edges: edges},
		AvailableActions: available, ActionDefinitions: actions,
		RequiredEvidenceKinds: composition.RequiredEvidenceKinds, OutcomeSchema: composition.OutcomeSchema, RigorRules: composition.RigorRules,
		StalenessRules: composition.StalenessRules, CompositionRules: composition.CompositionRules, EvaluatorIndependence: composition.EvaluatorIndependence,
		RecoveryRoutes: routes, ApprovalPolicy: composition.ApprovalPolicy, ProgressPolicy: composition.ProgressPolicy,
	})
	if err := ValidateWorkflowDefinition(definition); err != nil {
		return WorkflowDefinition{}, err
	}
	return definition, nil
}

// mustCompileWorkflowComposition compiles a built-in composition. A built-in
// that does not compile is a programming error the registry tests catch.
func mustCompileWorkflowComposition(composition WorkflowComposition) WorkflowDefinition {
	definition, err := CompileWorkflowComposition(composition)
	if err != nil {
		panic(fmt.Sprintf("built-in composition %s v%d does not compile: %v", composition.Ref, composition.Version, err))
	}
	return definition
}

// builtinWorkflowCompositions lists one current composition per live family.
func builtinWorkflowCompositions() []WorkflowComposition {
	return []WorkflowComposition{
		currentImplementationComposition(), currentBreakFixComposition(), currentResearchComposition(),
	}
}

// workflowCompositionCapabilities answers which capabilities a released
// composed version enables: the union over its parts (CD-0229 D1).
var workflowCompositionCapabilities = sync.OnceValue(func() map[string]workflowCapabilitySet {
	parts, err := workflowPartCatalog(builtinWorkflowParts())
	if err != nil {
		panic(fmt.Sprintf("built-in part catalog is invalid: %v", err))
	}
	capabilities := map[string]workflowCapabilitySet{}
	for _, composition := range builtinWorkflowCompositions() {
		var set workflowCapabilitySet
		for _, use := range composition.Parts {
			set |= parts[workflowPartKey(use.Ref, use.Version)].Capabilities
		}
		capabilities[registryKey(composition.Ref, composition.Version)] = set
	}
	return capabilities
})

func workflowPrototypeRigorRules() []WorkflowRigorRule {
	return []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification}}}
}

func forwardOnlyComposition(successors ...WorkKind) WorkflowCompositionRules {
	return WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: successors, ForbiddenCompositions: []WorkflowForbiddenComposition{}}
}

func currentImplementationComposition() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.implementation", Version: 37, WorkKind: WorkKindImplementation,
		ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1, ProgressPolicy: WorkflowProgressPolicyObligationSatisfactionV1,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceReview, EvidenceArtifact},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{},
		CompositionRules:     forwardOnlyComposition(WorkKindBreakFix, WorkKindResearch),
		ActionCatalogVersion: 3,
		Parts: []WorkflowPartUse{
			{Ref: "part.proposal", Version: 1},
			{Ref: "part.alignment", Version: 1},
			{Ref: "part.design", Version: 1},
			{Ref: "part.planning", Version: 1, StepID: "planning"},
			{Ref: "part.execution", Version: 1, RecoveryTo: "execution"},
			{Ref: "part.refine", Version: 1, RecoveryTo: "execution"},
			{Ref: "part.delivery", Version: 1},
			{Ref: "part.verdict", Version: 1, StepID: "acceptance", FailureTo: "refine", RecoveryTo: "execution"},
			{Ref: "part.close", Version: 1, StepID: "release", RecoveryTo: "execution", DisprovedPremiseRoute: true},
		},
	}
}

func currentBreakFixComposition() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.break_fix", Version: 34, WorkKind: WorkKindBreakFix,
		ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1, ProgressPolicy: WorkflowProgressPolicyObligationSatisfactionV1,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceArtifact},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{},
		CompositionRules:     forwardOnlyComposition(WorkKindImplementation, WorkKindResearch),
		ActionCatalogVersion: 3,
		Parts: []WorkflowPartUse{
			{Ref: "part.diagnosis", Version: 1},
			{Ref: "part.planning", Version: 1, StepID: "planning"},
			{Ref: "part.repair", Version: 1, RecoveryTo: "repair"},
			{Ref: "part.refine", Version: 1, RecoveryTo: "repair"},
			{Ref: "part.delivery", Version: 1},
			{Ref: "part.verdict", Version: 1, StepID: "verify", LeadAction: "bind_evidence", FailureTo: "refine", RecoveryTo: "repair"},
			{Ref: "part.close", Version: 1, StepID: "complete", RecoveryTo: "repair", DisprovedPremiseRoute: true},
		},
	}
}

func currentResearchComposition() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.research", Version: 22, WorkKind: WorkKindResearch,
		ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1, ProgressPolicy: WorkflowProgressPolicyObligationSatisfactionV1,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceArtifact},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"no_change", "resolved", "report_recorded"}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{},
		CompositionRules:     forwardOnlyComposition(WorkKindBreakFix, WorkKindArchitectureSpike, WorkKindStaticAnalysis),
		ActionCatalogVersion: 1,
		Parts: []WorkflowPartUse{
			{Ref: "part.planning", Version: 1, StepID: "frame", LeadAction: "frame_research"},
			{Ref: "part.investigate", Version: 1},
			{Ref: "part.findings", Version: 1},
			{Ref: "part.verdict", Version: 1, StepID: "conclude", LeadAction: "record_conclusion", RecoveryTo: "investigate"},
			{Ref: "part.close", Version: 1, StepID: "complete", RecoveryTo: "investigate"},
		},
	}
}

// withConsequenceConfirmPremise declares the human decision-context digest
// optional on a consequence_v1 confirm_premise: engineering confirmation is
// the coordinator's own confirm choice and carries no human digest, while a
// decision-bearing confirmation still answers the operator question, whose
// digest the confirmation validates (CD-0013 D11, CD-0129). The copy leaves
// the shared catalog declaration unmutated.
func withConsequenceConfirmPremise(actions []WorkflowActionDefinition) []WorkflowActionDefinition {
	selected := slices.Clone(actions)
	optionalDigest := func(payload WorkflowPayloadDefinition) WorkflowPayloadDefinition {
		fields := slices.Clone(payload.Fields)
		for index := range fields {
			if fields[index].Name == "decision_context_digest" {
				fields[index].Required = false
			}
		}
		payload.Fields = fields
		return payload
	}
	for index := range selected {
		if selected[index].ID != "confirm_premise" {
			continue
		}
		selected[index].Payload = optionalDigest(selected[index].Payload)
		if selected[index].PublicPayload != nil {
			public := optionalDigest(*selected[index].PublicPayload)
			selected[index].PublicPayload = &public
		}
	}
	return selected
}

// withObligationSatisfactionTaxonomy declares each compiled action's
// satisfaction kind from the canonical action mapping (CD-0164 D2a). The
// copy leaves the shared catalog declarations unmutated; validation binds
// the compiled result to the same mapping, so a compiled definition and its
// declared taxonomy cannot disagree.
func withObligationSatisfactionTaxonomy(actions []WorkflowActionDefinition) []WorkflowActionDefinition {
	selected := slices.Clone(actions)
	for index := range selected {
		selected[index].SatisfactionKind = workflowActionSatisfactionKind(selected[index].ID)
	}
	return selected
}
