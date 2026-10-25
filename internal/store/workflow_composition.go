package store

import (
	"fmt"
	"slices"
)

// WorkflowComposition is the source of a workflow definition (CD-0229 D2). It
// holds the family policy and the ordered part uses; compiling it yields the
// definition fields CD-0013 D1 lists, so the canonical manifest and its
// digest cover the resolved current definition (CD-0242 D14).
type WorkflowComposition struct {
	Ref                   string
	WorkKind              WorkKind
	RequiredEvidenceKinds []EvidenceKind
	OutcomeSchema         WorkflowOutcomeSchema
	RigorRules            []WorkflowRigorRule
	StalenessRules        []WorkflowStalenessRule
	CompositionRules      WorkflowCompositionRules
	EvaluatorIndependence WorkflowEvaluatorIndependence
	ApprovalPolicy        string
	Parts                 []WorkflowPartUse
}

// CompileWorkflowComposition compiles a composition against the current
// part and action catalogs. Steps follow part order. Forward edges chain the
// steps, then each retry part adds its retry edge, then each failure
// parameter adds its failure edge. Recovery routes follow part order, then
// each part's route order. The root action list and the action definitions
// list every action once, in first appearance over the compiled steps. The
// capabilities are the canonical union over the parts.
func CompileWorkflowComposition(composition WorkflowComposition) (WorkflowDefinition, error) {
	parts, err := workflowPartCatalog(builtinWorkflowParts())
	if err != nil {
		return WorkflowDefinition{}, err
	}
	catalog := builtinWorkflowActionCatalog()
	if len(composition.Parts) == 0 {
		return WorkflowDefinition{}, partFailure("a composition uses no parts")
	}
	resolved := make([]WorkflowPart, 0, len(composition.Parts))
	steps := make([]WorkflowStep, 0, len(composition.Parts))
	capabilities := make([][]WorkflowCapability, 0, len(composition.Parts))
	for _, use := range composition.Parts {
		part, ok := parts[use.Ref]
		if !ok {
			return WorkflowDefinition{}, partFailure(fmt.Sprintf("workflow part %s is not declared", use.Ref))
		}
		compiled, err := compileWorkflowPartUse(part, use)
		if err != nil {
			return WorkflowDefinition{}, err
		}
		resolved = append(resolved, part)
		steps = append(steps, compiled)
		capabilities = append(capabilities, part.Capabilities)
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
		Ref: composition.Ref, WorkKind: composition.WorkKind, ChangesProductTruth: &changesProductTruth,
		Capabilities:     workflowCapabilityUnion(capabilities...),
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
		panic(fmt.Sprintf("built-in composition %s does not compile: %v", composition.Ref, err))
	}
	return definition
}

// builtinWorkflowCompositions lists one current composition per live family.
func builtinWorkflowCompositions() []WorkflowComposition {
	return []WorkflowComposition{
		currentImplementationComposition(), currentBreakFixComposition(), currentResearchComposition(),
	}
}

func workflowPrototypeRigorRules() []WorkflowRigorRule {
	return []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification}}}
}

func forwardOnlyComposition(successors ...WorkKind) WorkflowCompositionRules {
	return WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: successors, ForbiddenCompositions: []WorkflowForbiddenComposition{}}
}

func currentImplementationComposition() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.implementation", WorkKind: WorkKindImplementation,
		ApprovalPolicy:        WorkflowApprovalPolicyConsequenceV1,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceReview, EvidenceArtifact},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{},
		CompositionRules: forwardOnlyComposition(WorkKindBreakFix, WorkKindResearch),
		Parts: []WorkflowPartUse{
			{Ref: "part.proposal"},
			{Ref: "part.alignment"},
			{Ref: "part.planning", StepID: "planning"},
			{Ref: "part.design"},
			{Ref: "part.execution", RecoveryTo: "execution"},
			{Ref: "part.refine", RecoveryTo: "execution"},
			{Ref: "part.delivery"},
			{Ref: "part.verdict", StepID: "acceptance", FailureTo: "refine", RecoveryTo: "execution"},
			{Ref: "part.close", StepID: "release", RecoveryTo: "execution", DisprovedPremiseRoute: true},
		},
	}
}

func currentBreakFixComposition() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.break_fix", WorkKind: WorkKindBreakFix,
		ApprovalPolicy:        WorkflowApprovalPolicyConsequenceV1,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification, EvidenceArtifact},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{},
		CompositionRules: forwardOnlyComposition(WorkKindImplementation, WorkKindResearch),
		Parts: []WorkflowPartUse{
			{Ref: "part.diagnosis"},
			{Ref: "part.planning", StepID: "planning"},
			{Ref: "part.repair", RecoveryTo: "repair"},
			{Ref: "part.refine", RecoveryTo: "repair"},
			{Ref: "part.delivery"},
			{Ref: "part.verdict", StepID: "verify", LeadAction: "bind_evidence", FailureTo: "refine", RecoveryTo: "repair"},
			{Ref: "part.close", StepID: "complete", RecoveryTo: "repair", DisprovedPremiseRoute: true},
		},
	}
}

func currentResearchComposition() WorkflowComposition {
	return WorkflowComposition{
		Ref: "workflow.research", WorkKind: WorkKindResearch,
		ApprovalPolicy:        WorkflowApprovalPolicyConsequenceV1,
		RequiredEvidenceKinds: []EvidenceKind{EvidenceArtifact},
		OutcomeSchema:         WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"no_change", "resolved", "report_recorded"}},
		RigorRules:            workflowPrototypeRigorRules(), StalenessRules: []WorkflowStalenessRule{},
		CompositionRules: forwardOnlyComposition(WorkKindBreakFix, WorkKindArchitectureSpike, WorkKindStaticAnalysis),
		Parts: []WorkflowPartUse{
			{Ref: "part.planning", StepID: "frame", LeadAction: "frame_research"},
			{Ref: "part.investigate"},
			{Ref: "part.findings"},
			{Ref: "part.verdict", StepID: "conclude", LeadAction: "record_conclusion", RecoveryTo: "investigate"},
			{Ref: "part.close", StepID: "complete", RecoveryTo: "investigate"},
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
