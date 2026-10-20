package store

import (
	"fmt"
	"slices"
	"sync"
)

// WorkflowComposition is the source of a workflow definition (CD-0229 D2). It
// holds the family policy and the ordered part pins; compiling it yields the
// definition fields CD-0013 D1 lists, so the canonical manifest and its
// digest are unchanged in shape. A released composition is frozen like the
// definition it compiles to (D3).
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
	ActionCatalogVersion  int
	Parts                 []WorkflowPartUse
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
	changesProductTruth := workKindMayChangeProductTruth(composition.WorkKind)
	definition := cloneWorkflowDefinition(WorkflowDefinition{
		Ref: composition.Ref, Version: composition.Version, WorkKind: composition.WorkKind, ChangesProductTruth: &changesProductTruth,
		StepGraph:        WorkflowStepGraph{StartStep: steps[0].ID, TerminalSteps: []string{steps[len(steps)-1].ID}, Steps: steps, Edges: edges},
		AvailableActions: available, ActionDefinitions: actions,
		RequiredEvidenceKinds: composition.RequiredEvidenceKinds, OutcomeSchema: composition.OutcomeSchema, RigorRules: composition.RigorRules,
		StalenessRules: composition.StalenessRules, CompositionRules: composition.CompositionRules, EvaluatorIndependence: composition.EvaluatorIndependence,
		RecoveryRoutes: routes, ApprovalPolicy: composition.ApprovalPolicy,
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

// builtinWorkflowCompositions lists every released composition.
func builtinWorkflowCompositions() []WorkflowComposition {
	return []WorkflowComposition{
		implementationCompositionV33(), implementationConsequenceV34(), implementationCompositionV35(), implementationCompositionV36(),
		breakFixCompositionV30(), breakFixConsequenceV31(), breakFixCompositionV32(), breakFixCompositionV33(),
		researchCompositionV20(), researchConsequenceV21(),
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

func implementationCompositionV33() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.implementation", Version: 33, WorkKind: WorkKindImplementation,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceReview, EvidenceArtifact},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{},
		CompositionRules:     forwardOnlyComposition(WorkKindBreakFix, WorkKindResearch),
		ActionCatalogVersion: 1,
		Parts: []WorkflowPartUse{
			{Ref: "part.proposal", Version: 1},
			{Ref: "part.alignment", Version: 1},
			{Ref: "part.design", Version: 1},
			{Ref: "part.planning", Version: 1, StepID: "planning"},
			{Ref: "part.execution", Version: 1},
			{Ref: "part.refine", Version: 1},
			{Ref: "part.delivery", Version: 1},
			{Ref: "part.verdict", Version: 1, StepID: "acceptance", FailureTo: "refine", RecoveryTo: "execution"},
			{Ref: "part.close", Version: 1, StepID: "release", RecoveryTo: "execution", DisprovedPremiseRoute: true},
		},
	}
}

func breakFixCompositionV30() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.break_fix", Version: 30, WorkKind: WorkKindBreakFix,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceArtifact},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{},
		CompositionRules:     forwardOnlyComposition(WorkKindImplementation, WorkKindResearch),
		ActionCatalogVersion: 1,
		Parts: []WorkflowPartUse{
			{Ref: "part.diagnosis", Version: 1},
			{Ref: "part.planning", Version: 1, StepID: "planning"},
			{Ref: "part.repair", Version: 1},
			{Ref: "part.refine", Version: 1},
			{Ref: "part.delivery", Version: 1},
			{Ref: "part.verdict", Version: 1, StepID: "verify", LeadAction: "bind_evidence", FailureTo: "refine", RecoveryTo: "repair"},
			{Ref: "part.close", Version: 1, StepID: "complete", RecoveryTo: "repair", DisprovedPremiseRoute: true},
		},
	}
}

func researchCompositionV20() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.research", Version: 20, WorkKind: WorkKindResearch,
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

// withConsequencePolicy promotes a released composition to the next version
// under the consequence_v1 approval policy (CD-0230 D5). Engineering
// contract approval, premise confirmation, and completion admit without an
// operator challenge; the declared decision domains, a serious law change,
// crucial size, and destructive effects still reach the operator.
func withConsequencePolicy(composition WorkflowComposition) WorkflowComposition {
	composition.Version++
	composition.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
	return composition
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

func implementationConsequenceV34() WorkflowComposition {
	return withConsequencePolicy(implementationCompositionV33())
}

func breakFixConsequenceV31() WorkflowComposition {
	return withConsequencePolicy(breakFixCompositionV30())
}

func researchConsequenceV21() WorkflowComposition {
	return withConsequencePolicy(researchCompositionV20())
}

// Checks-repair heads retain their predecessor's consequence policy and parts,
// and repin only the action catalog for the repair authoring and packet members.
func implementationCompositionV35() WorkflowComposition {
	composition := implementationConsequenceV34()
	composition.Version = 35
	composition.ActionCatalogVersion = 2
	return composition
}

func breakFixCompositionV32() WorkflowComposition {
	composition := breakFixConsequenceV31()
	composition.Version = 32
	composition.ActionCatalogVersion = 2
	return composition
}

// implementationCompositionV36 ships the size-capable implementation head
// (CD-0225 D1, CD-0227 D1): v35's every surface — the consequence_v1
// approval policy and the checks-repair authoring members included — with
// the v3 action catalog, whose record_worker_job adds the optional size
// members beside the repair authoring members and the closed split variant.
// A job under this head carries all three size members or none.
func implementationCompositionV36() WorkflowComposition {
	composition := implementationCompositionV35()
	composition.Version = 36
	composition.ActionCatalogVersion = 3
	return composition
}

// breakFixCompositionV33 ships the size-capable break-fix head: v32's every
// surface with the v3 action catalog.
func breakFixCompositionV33() WorkflowComposition {
	composition := breakFixCompositionV32()
	composition.Version = 33
	composition.ActionCatalogVersion = 3
	return composition
}
