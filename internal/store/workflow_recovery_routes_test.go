package store

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// workflowExpectedRecoveryRoutes holds the one declared route table each
// family owns (CD-0201 D1/D3, CD-0172 D3). Released versions resolve the
// same routes through the quarantined table; the authored versions declare
// them. A route names the step that records the bad evaluation, hosts the
// late-verdict context (CD-0204), or completes under the disproved
// contract, the trigger that opens the return, the action the route admits,
// and the target step that re-produces the artifact the evaluator judges.
// Only implementation and break_fix carry the complete-step correction
// route; CD-0186 keeps every other family's complete step outside it.
func workflowExpectedRecoveryRoutes(ref string) []WorkflowRecoveryRoute {
	switch ref {
	case "workflow.implementation":
		return []WorkflowRecoveryRoute{
			{Step: "acceptance", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
			{Step: "release", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
			{Step: "release", Trigger: WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: "execution"},
		}
	case "workflow.break_fix":
		return []WorkflowRecoveryRoute{
			{Step: "verify", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair"},
			{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair"},
			{Step: "complete", Trigger: WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: "repair"},
		}
	case "workflow.research":
		return []WorkflowRecoveryRoute{
			{Step: "conclude", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate"},
			{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate"},
		}
	case "workflow.architecture_spike":
		return []WorkflowRecoveryRoute{
			{Step: "review", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record"},
			{Step: "acceptance", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record"},
			{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record"},
		}
	case "workflow.ops_runbook":
		return []WorkflowRecoveryRoute{
			{Step: "health", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
			{Step: "cleanup", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
			{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
		}
	case "workflow.static_analysis":
		return []WorkflowRecoveryRoute{
			{Step: "review", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze"},
			{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze"},
		}
	case "workflow.generic_one_off":
		return []WorkflowRecoveryRoute{
			{Step: "verify", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
			{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
		}
	}
	return nil
}

// workflowRecoveryRoutePopulation splits the registered history into released
// versions (nil declared table, quarantined resolution) and authored versions
// (declared table), failing the test when the split is not the two-shape
// population the contract pins: released definitions keep nil tables, and one
// new authored version per family declares its table.
func workflowRecoveryRoutePopulation(t *testing.T) (released, declared []WorkflowDefinition) {
	t.Helper()
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		if definition.RecoveryRoutes != nil {
			declared = append(declared, definition)
			continue
		}
		released = append(released, definition)
	}
	declaredRefs := map[string]bool{}
	for _, definition := range declared {
		declaredRefs[definition.Ref] = true
	}
	if len(declaredRefs) != 7 {
		t.Fatalf("declared recovery-route tables cover %d families, want 7", len(declaredRefs))
	}
	if len(released) != 108 {
		t.Fatalf("released versions with no declared table number %d, want 108", len(released))
	}
	return released, declared
}

// workflowAssertRouteInvariants checks one resolved route against the graph it
// serves: the route sits on the step that records its trigger's condition —
// an unhealthy_verdict route on a step that records a verdict or confirms a
// premise, a disproved_premise_at_complete route on the complete action step
// — the producer rule is graph-derived (the target reaches the route step
// through forward edges), the target is neither terminal nor approval-gated,
// and the trigger pairs with its action.
func workflowAssertRouteInvariants(t *testing.T, definition WorkflowDefinition, route WorkflowRecoveryRoute) {
	t.Helper()
	steps := map[string]WorkflowStep{}
	for _, candidate := range definition.StepGraph.Steps {
		steps[candidate.ID] = candidate
	}
	routeStep, ok := steps[route.Step]
	if !ok {
		t.Fatalf("%s v%d route names step %q that the graph does not declare", definition.Ref, definition.Version, route.Step)
	}
	switch route.Trigger {
	case WorkflowRecoveryTriggerUnhealthyVerdict:
		if !containsString(routeStep.Actions, "record_verdict") && !containsString(routeStep.Actions, "confirm_premise") && !containsString(definition.StepGraph.TerminalSteps, route.Step) {
			t.Fatalf("%s v%d unhealthy_verdict route sits on step %q, which declares neither record_verdict nor confirm_premise and hosts no late-verdict context", definition.Ref, definition.Version, route.Step)
		}
	case WorkflowRecoveryTriggerDisprovedPremiseAtComplete:
		if !containsString(routeStep.Actions, "complete") {
			t.Fatalf("%s v%d complete-trigger route sits on step %q, which declares no complete action", definition.Ref, definition.Version, route.Step)
		}
	default:
		t.Fatalf("%s v%d route carries unknown trigger %q", definition.Ref, definition.Version, route.Trigger)
	}
	if route.Action != workflowRecoveryRouteActionForTrigger(route.Trigger) {
		t.Fatalf("%s v%d route pairs trigger %q with action %q", definition.Ref, definition.Version, route.Trigger, route.Action)
	}
	target, ok := steps[route.Target]
	if !ok {
		t.Fatalf("%s v%d route names target %q that the graph does not declare", definition.Ref, definition.Version, route.Target)
	}
	if route.Target == route.Step {
		t.Fatalf("%s v%d route target %q is the route step itself", definition.Ref, definition.Version, route.Target)
	}
	if containsString(definition.StepGraph.TerminalSteps, route.Target) {
		t.Fatalf("%s v%d route target %q is terminal", definition.Ref, definition.Version, route.Target)
	}
	if containsString(target.Actions, "approve_contract") {
		t.Fatalf("%s v%d route target %q declares approve_contract", definition.Ref, definition.Version, route.Target)
	}
	if !workflowStepReachesByForwardEdges(definition.StepGraph, route.Target, route.Step) {
		t.Fatalf("%s v%d route target %q does not reach route step %q through forward edges", definition.Ref, definition.Version, route.Target, route.Step)
	}
}

// workflowAssertSingleFailureEdges holds the registration bound: no step of
// any registered version carries more than one failure edge.
func workflowAssertSingleFailureEdges(t *testing.T, definition WorkflowDefinition) {
	t.Helper()
	counts := map[string]int{}
	for _, edge := range definition.StepGraph.Edges {
		if edge.Kind == WorkflowEdgeFailure {
			counts[edge.From]++
		}
	}
	for step, count := range counts {
		if count > 1 {
			t.Fatalf("%s v%d step %q carries %d failure edges, want at most one", definition.Ref, definition.Version, step, count)
		}
	}
}

// TestReleasedDigestsIgnoreRecoveryRouteField is the digest-compatibility
// bound (CD-0115 D1/D3): a nil table and an empty table both leave the frozen
// canonical bytes and digests of every released version byte-identical, and
// the field never appears in a released manifest. A declared table changes
// the digest, which is why it ships as a new version.
func TestReleasedDigestsIgnoreRecoveryRouteField(t *testing.T) {
	t.Parallel()
	released, _ := workflowRecoveryRoutePopulation(t)
	if len(released) == 0 {
		t.Fatal("no released definitions found")
	}
	for _, definition := range released {
		registered, ok := builtinWorkflowRegistry.Lookup(definition.Ref, definition.Version)
		if !ok {
			t.Fatalf("%s v%d is not registered", definition.Ref, definition.Version)
		}
		frozen, err := CanonicalWorkflowDefinition(definition)
		if err != nil {
			t.Fatalf("%s v%d canonical bytes cannot be computed: %v", definition.Ref, definition.Version, err)
		}
		if bytes.Contains(frozen, []byte("recovery_routes")) {
			t.Fatalf("%s v%d canonical manifest carries recovery_routes; a released digest would move", definition.Ref, definition.Version)
		}
		empty := cloneWorkflowDefinition(definition)
		empty.RecoveryRoutes = []WorkflowRecoveryRoute{}
		emptyBytes, err := CanonicalWorkflowDefinition(empty)
		if err != nil {
			t.Fatalf("%s v%d empty-table canonical bytes cannot be computed: %v", definition.Ref, definition.Version, err)
		}
		if !bytes.Equal(frozen, emptyBytes) {
			t.Fatalf("%s v%d empty table changed the canonical bytes", definition.Ref, definition.Version)
		}
		emptyDigest, err := WorkflowDefinitionDigest(empty)
		if err != nil {
			t.Fatalf("%s v%d empty-table digest cannot be computed: %v", definition.Ref, definition.Version, err)
		}
		if emptyDigest != registered.Digest {
			t.Fatalf("%s v%d empty-table digest %s drifted from the pin %s", definition.Ref, definition.Version, emptyDigest, registered.Digest)
		}
	}
}

// TestWorkflowRecoveryRoutesGoldenPerReleasedVersion pins the quarantined
// output per released (ref, version): every released version resolves exactly
// its family's routes, every route satisfies the graph invariants against the
// version that resolves it, and no released version resolves to nothing.
func TestWorkflowRecoveryRoutesGoldenPerReleasedVersion(t *testing.T) {
	t.Parallel()
	released, _ := workflowRecoveryRoutePopulation(t)
	for _, definition := range released {
		resolved := workflowRecoveryRoutes(definition)
		want := workflowExpectedRecoveryRoutes(definition.Ref)
		if len(want) == 0 {
			t.Fatalf("%s has no expected route table; the test population is incomplete", definition.Ref)
		}
		if len(resolved) == 0 {
			t.Fatalf("%s v%d resolves no recovery routes; a stranded pin on it would have no admitted return", definition.Ref, definition.Version)
		}
		if !reflect.DeepEqual(resolved, want) {
			t.Fatalf("%s v%d resolves routes %+v, want %+v", definition.Ref, definition.Version, resolved, want)
		}
		for _, route := range resolved {
			workflowAssertRouteInvariants(t, definition, route)
		}
		workflowAssertSingleFailureEdges(t, definition)
	}
}

// TestCompleteStepCorrectionRoutesCoverOnlyTheSupportedFamilies pins the
// CD-0172 D3 declaration boundary across every registered version, released
// and authored: exactly implementation and break_fix resolve a
// disproved_premise_at_complete route, it sits on the complete action step
// that version's own graph declares (release for implementation, complete
// for break_fix), and it targets the external-effect producer D3 names. Every
// other family resolves no complete-step correction route (CD-0186), so the
// per-family quarantine range is proven by each version's actual graph, not
// assumed by the range bound.
func TestCompleteStepCorrectionRoutesCoverOnlyTheSupportedFamilies(t *testing.T) {
	t.Parallel()
	supportedTargets := map[string]string{
		"workflow.implementation": "execution",
		"workflow.break_fix":      "repair",
	}
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		steps := map[string]WorkflowStep{}
		for _, candidate := range definition.StepGraph.Steps {
			steps[candidate.ID] = candidate
		}
		var completeRoutes []WorkflowRecoveryRoute
		for _, route := range workflowRecoveryRoutes(definition) {
			if route.Trigger == WorkflowRecoveryTriggerDisprovedPremiseAtComplete {
				completeRoutes = append(completeRoutes, route)
			}
		}
		wantTarget, isSupported := supportedTargets[definition.Ref]
		if !isSupported {
			if len(completeRoutes) != 0 {
				t.Errorf("%s v%d resolves a complete-step correction route outside break-fix and implementation: %+v", definition.Ref, definition.Version, completeRoutes)
			}
			continue
		}
		if len(completeRoutes) != 1 {
			t.Errorf("%s v%d resolves %d complete-step correction routes, want exactly one", definition.Ref, definition.Version, len(completeRoutes))
			continue
		}
		route := completeRoutes[0]
		if route.Action != "supersede_contract" {
			t.Errorf("%s v%d complete-step route admits %q, want supersede_contract", definition.Ref, definition.Version, route.Action)
		}
		if route.Target != wantTarget {
			t.Errorf("%s v%d complete-step route targets %q, want %q", definition.Ref, definition.Version, route.Target, wantTarget)
		}
		step, ok := steps[route.Step]
		if !ok || !containsString(step.Actions, "complete") {
			t.Errorf("%s v%d complete-step route sits on %q, which is not a complete action step of that version", definition.Ref, definition.Version, route.Step)
		}
	}
}

// TestWorkflowReleasedRecoveryRoutesRefuseUnknownRefAndVersion is the
// fail-closed quarantine bound: an unknown ref or version resolves no routes,
// and the reader never guesses a table from the graph or the work kind.
func TestWorkflowReleasedRecoveryRoutesRefuseUnknownRefAndVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref     string
		version int64
	}{
		{"workflow.unknown_family", 1},
		{"workflow.implementation", 0},
		{"workflow.implementation", 99},
		{"workflow.break_fix", 99},
		{"workflow.generic_one_off", 99},
		{"", 1},
	}
	for _, testCase := range cases {
		if routes := workflowReleasedRecoveryRoutes(testCase.ref, testCase.version); routes != nil {
			t.Fatalf("quarantined table guessed routes for %s v%d: %+v", testCase.ref, testCase.version, routes)
		}
	}
	unregistered := cloneWorkflowDefinition(implementationProposalOutOfScopeV22())
	unregistered.Ref = "workflow.unreleased_family"
	if routes := workflowRecoveryRoutes(unregistered); routes != nil {
		t.Fatalf("reader guessed routes for an unregistered ref: %+v", routes)
	}
}

// TestAuthoredRecoveryRoutesOverrideTheQuarantine holds the reader's choice
// order: a declared table wins over any quarantined data for the same
// (ref, version), the returned slice is a defensive copy, and the reader does
// not mutate the definition it read.
func TestAuthoredRecoveryRoutesOverrideTheQuarantine(t *testing.T) {
	t.Parallel()
	_, declared := workflowRecoveryRoutePopulation(t)
	for _, definition := range declared {
		if workflowReleasedRecoveryRoutes(definition.Ref, definition.Version) != nil {
			t.Fatalf("%s v%d is both declared and quarantined; one owner must hold the routes", definition.Ref, definition.Version)
		}
		resolved := workflowRecoveryRoutes(definition)
		if !reflect.DeepEqual(resolved, definition.RecoveryRoutes) {
			t.Fatalf("%s v%d resolved %+v, want the declared table %+v", definition.Ref, definition.Version, resolved, definition.RecoveryRoutes)
		}
		if len(resolved) == 0 {
			t.Fatalf("%s v%d declared an empty table", definition.Ref, definition.Version)
		}
		for _, route := range resolved {
			workflowAssertRouteInvariants(t, definition, route)
		}
		resolved[0].Target = "mutated"
		if reflect.DeepEqual(workflowRecoveryRoutes(definition), resolved) {
			t.Fatalf("%s v%d caller mutation reached the authoritative route table", definition.Ref, definition.Version)
		}
	}
}

// TestWorkflowRecoveryRoutesDoNotMutateOldCanonicalDefinitions holds the
// compatibility-reader bound: resolving routes leaves the canonical bytes of
// a released definition and a declared definition unchanged.
func TestWorkflowRecoveryRoutesDoNotMutateOldCanonicalDefinitions(t *testing.T) {
	t.Parallel()
	released, declared := workflowRecoveryRoutePopulation(t)
	subjects := append([]WorkflowDefinition{}, released[0], declared[0])
	for _, definition := range subjects {
		before, err := CanonicalWorkflowDefinition(definition)
		if err != nil {
			t.Fatalf("%s v%d canonical bytes cannot be computed: %v", definition.Ref, definition.Version, err)
		}
		_ = workflowRecoveryRoutes(definition)
		after, err := CanonicalWorkflowDefinition(definition)
		if err != nil {
			t.Fatalf("%s v%d canonical bytes cannot be recomputed: %v", definition.Ref, definition.Version, err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%s v%d canonical bytes changed across a route read", definition.Ref, definition.Version)
		}
	}
}

// TestNewAuthoredVersionsDeclareRouteTables holds the authoring act: the
// current version of every family declares the D3 route table, registers
// under ValidateWorkflowDefinition, and covers exactly the evaluator steps
// the family's graph declares.
func TestNewAuthoredVersionsDeclareRouteTables(t *testing.T) {
	t.Parallel()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.RecoveryRoutes == nil {
			t.Fatalf("%s v%d declares no recovery-route table", definition.Ref, definition.Version)
		}
		want := workflowExpectedRecoveryRoutes(definition.Ref)
		if !reflect.DeepEqual(definition.RecoveryRoutes, want) {
			t.Fatalf("%s v%d declares %+v, want %+v", definition.Ref, definition.Version, definition.RecoveryRoutes, want)
		}
		if err := ValidateWorkflowDefinition(definition); err != nil {
			t.Fatalf("%s v%d does not validate: %v", definition.Ref, definition.Version, err)
		}
	}
}

// TestRecoveryRouteRegistrationRefusals is the D4 structural gate: each
// mutated route table refuses at registration with a typed failure.
func TestRecoveryRouteRegistrationRefusals(t *testing.T) {
	t.Parallel()
	base := cloneWorkflowDefinition(implementationProposalOutOfScopeV22())
	base.Version = 99
	base.RecoveryRoutes = workflowExpectedRecoveryRoutes("workflow.implementation")
	opsBase := cloneWorkflowDefinition(opsRunbookAcceptDeliveryV15())
	opsBase.Version = 99
	opsBase.RecoveryRoutes = workflowExpectedRecoveryRoutes("workflow.ops_runbook")

	missing := cloneWorkflowDefinition(base)
	// Only the terminal step resolves a route: acceptance — the step that
	// records the verdict — resolves none, so the coverage clause refuses
	// the table.
	missing.RecoveryRoutes = []WorkflowRecoveryRoute{{Step: "release", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"}}
	ambiguous := cloneWorkflowDefinition(base)
	ambiguous.RecoveryRoutes = append(append([]WorkflowRecoveryRoute{}, base.RecoveryRoutes...), base.RecoveryRoutes[0])
	unknownTrigger := cloneWorkflowDefinition(base)
	unknownTrigger.RecoveryRoutes[0].Trigger = "failed_attempt"
	unknownStep := cloneWorkflowDefinition(base)
	unknownStep.RecoveryRoutes[0].Step = "nonexistent"
	unknownAction := cloneWorkflowDefinition(base)
	unknownAction.RecoveryRoutes[0].Action = "reject_worker_result"
	unknownTarget := cloneWorkflowDefinition(base)
	unknownTarget.RecoveryRoutes[0].Target = "nonexistent"
	unhealthyActionMismatch := cloneWorkflowDefinition(base)
	unhealthyActionMismatch.RecoveryRoutes[0].Action = "supersede_contract"
	completeActionMismatch := cloneWorkflowDefinition(base)
	completeActionMismatch.RecoveryRoutes[2].Action = "request_correction"
	completeOnEvaluatorStep := cloneWorkflowDefinition(base)
	completeOnEvaluatorStep.RecoveryRoutes[2].Step = "acceptance"
	unhealthyOnNonEvaluatorStep := cloneWorkflowDefinition(base)
	unhealthyOnNonEvaluatorStep.RecoveryRoutes[0].Step = "refine"
	targetIsStep := cloneWorkflowDefinition(base)
	targetIsStep.RecoveryRoutes[0].Target = base.RecoveryRoutes[0].Step
	targetTerminal := cloneWorkflowDefinition(base)
	targetTerminal.RecoveryRoutes[0].Target = "release"
	targetApprovalGated := cloneWorkflowDefinition(base)
	targetApprovalGated.RecoveryRoutes[0].Target = "planning"
	notUpstream := cloneWorkflowDefinition(opsBase)
	notUpstream.RecoveryRoutes = []WorkflowRecoveryRoute{{Step: "health", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "cleanup"}}
	doubleFailureEdge := cloneWorkflowDefinition(base)
	doubleFailureEdge.StepGraph.Edges = append(doubleFailureEdge.StepGraph.Edges, WorkflowEdge{From: base.RecoveryRoutes[0].Step, To: "execution", Kind: WorkflowEdgeFailure})

	for name, mutation := range map[string]WorkflowDefinition{
		"missing evaluator route":                   missing,
		"ambiguous step and trigger":                ambiguous,
		"unknown trigger":                           unknownTrigger,
		"unknown step":                              unknownStep,
		"unknown action":                            unknownAction,
		"unknown target":                            unknownTarget,
		"unhealthy trigger pairs wrong action":      unhealthyActionMismatch,
		"complete trigger pairs wrong action":       completeActionMismatch,
		"complete trigger off the complete step":    completeOnEvaluatorStep,
		"unhealthy trigger on a non-evaluator step": unhealthyOnNonEvaluatorStep,
		"target is the route step":                  targetIsStep,
		"target is terminal":                        targetTerminal,
		"target is approval-gated":                  targetApprovalGated,
		"target not upstream":                       notUpstream,
		"second failure edge":                       doubleFailureEdge,
	} {
		if err := ValidateWorkflowDefinition(mutation); err == nil {
			t.Fatalf("%s was admitted", name)
		} else if !strings.Contains(err.Error(), "recovery") && !strings.Contains(err.Error(), "failure edge") {
			t.Fatalf("%s refused with an unrelated error: %v", name, err)
		}
		registry := NewWorkflowDefinitionRegistry()
		if _, err := registry.Register(mutation); err == nil {
			t.Fatalf("%s was registered", name)
		}
	}
	if err := ValidateWorkflowDefinition(base); err != nil {
		t.Fatalf("the unmutated declared table was refused: %v", err)
	}
	registry := NewWorkflowDefinitionRegistry()
	if _, err := registry.Register(base); err != nil {
		t.Fatalf("the unmutated declared table did not register: %v", err)
	}
}

// TestUnreleasedEvaluatorDefinitionsWithoutATableRefuse is the CON-846 gate.
// A definition whose steps record verdicts or confirm premises registers
// only with routes the one owner resolves: a nil or empty table on an
// unreleased ref or a version above the quarantined range resolves no
// quarantined data, so the coverage clause refuses the definition instead of
// admitting an evaluator step with no declared return to its producer.
func TestUnreleasedEvaluatorDefinitionsWithoutATableRefuse(t *testing.T) {
	t.Parallel()
	unreleasedFamily := cloneWorkflowDefinition(genericOneOffAcceptDeliveryV13())
	unreleasedFamily.Ref = "workflow.synthetic_unreleased"
	unreleasedFamily.Version = 1
	unreleasedFamily.RecoveryRoutes = nil
	futureImplementation := cloneWorkflowDefinition(implementationProposalOutOfScopeV22())
	futureImplementation.Version = 99
	futureImplementation.RecoveryRoutes = nil
	futureImplementationEmpty := cloneWorkflowDefinition(futureImplementation)
	futureImplementationEmpty.RecoveryRoutes = []WorkflowRecoveryRoute{}
	futureBreakFix := cloneWorkflowDefinition(breakFixAcceptDeliveryV19())
	futureBreakFix.Version = 99
	futureBreakFix.RecoveryRoutes = nil
	for name, definition := range map[string]WorkflowDefinition{
		"unreleased family with a nil table":        unreleasedFamily,
		"future implementation version nil table":   futureImplementation,
		"future implementation version empty table": futureImplementationEmpty,
		"future break_fix version nil table":        futureBreakFix,
	} {
		err := ValidateWorkflowDefinition(definition)
		if err == nil {
			t.Fatalf("%s was admitted", name)
		}
		if !strings.Contains(err.Error(), "resolves no unhealthy_verdict recovery route") {
			t.Fatalf("%s refused with an unrelated error: %v", name, err)
		}
		if _, err := NewWorkflowDefinitionRegistry().Register(definition); err == nil {
			t.Fatalf("%s was registered", name)
		}
	}
}

// TestReleasedVersionsRegisterWithoutDeclaredTables holds the compatibility
// boundary: a released definition with a nil table still registers, because
// its routes resolve through the quarantined table instead.
func TestReleasedVersionsRegisterWithoutDeclaredTables(t *testing.T) {
	t.Parallel()
	registry := NewWorkflowDefinitionRegistry()
	registered, err := registry.Register(cloneWorkflowDefinition(genericOneOffAcceptDeliveryV13()))
	if err != nil {
		t.Fatalf("released generic_one_off v13 did not register: %v", err)
	}
	if registered.Definition.RecoveryRoutes != nil {
		t.Fatal("registering a released definition manufactured a route table")
	}
	if routes := workflowRecoveryRoutes(registered.Definition); !reflect.DeepEqual(routes, workflowExpectedRecoveryRoutes(registered.Definition.Ref)) {
		t.Fatalf("released generic_one_off v13 resolves %+v, want declared evaluator and terminal returns", routes)
	}
}

func TestLateVerdictAdmissionRequiresDeclaredRecoveryRoute(t *testing.T) {
	definition := cloneWorkflowDefinition(genericOneOffRecoveryRoutesV14())
	definition.RecoveryRoutes = definition.RecoveryRoutes[:1]
	state := admissionModelState{step: "complete", contracts: 1, verdict: "bad"}
	if admissionLateVerdictRoute(definition, state) {
		t.Fatal("late verdict admitted without a declared return route at complete")
	}
}

func TestRecoveryRouteTargetMustProduceArtifact(t *testing.T) {
	definition := cloneWorkflowDefinition(genericOneOffRecoveryRoutesV14())
	definition.StepGraph.Steps = append(definition.StepGraph.Steps, step("inspection", WorkflowStepInternalSQLite, "bind_evidence"))
	definition.StepGraph.Edges = append(definition.StepGraph.Edges, WorkflowEdge{From: "inspection", To: "verify", Kind: WorkflowEdgeForward})
	definition.RecoveryRoutes[0].Target = "inspection"
	if err := validateWorkflowRecoveryRoutes(definition); err == nil {
		t.Fatal("recovery accepted an upstream step with no artifact-producing mechanism")
	}
}

func TestDeclaredEvaluatorAdmitsObjectiveRevision(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, route := range workflowRecoveryRoutes(definition) {
			if route.Trigger != WorkflowRecoveryTriggerUnhealthyVerdict || stepDeclaresAction(definition, route.Step, "complete") {
				continue
			}
			if !workflowContractCorrectionCheckpoint(definition, route.Step) {
				t.Errorf("%s %s refuses objective revision at its declared evaluator", definition.Ref, route.Step)
			}
			state := admissionModelState{step: route.Step, contracts: 1, verdict: "bad", artifactStale: true}
			if !admissionContractCorrection(definition, state) {
				t.Errorf("%s %s model refuses objective revision", definition.Ref, route.Step)
			}
		}
	}
}
