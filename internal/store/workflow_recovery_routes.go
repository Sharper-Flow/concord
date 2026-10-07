package store

// One declared recovery-route table per workflow definition is the only owner
// of cross-step correction (CD-0201 D1). The table names, for the step that
// records a bad evaluation or completes under a disproved contract, the
// trigger that opens the return, the action the route admits, and the one
// target step that re-produces the artifact the evaluator judges.
// Registration refuses a missing, ambiguous, or non-producer route (D4), so
// the CON-846 shape — an evaluator step with an unhealthy verdict and no
// admitted route back to its producer — cannot be authored again.
//
// Released definition versions predate the field and never change content in
// place (CD-0115 D1), so they declare no table and resolve through the
// quarantined table below, keyed only by (ref, version). No graph walk and no
// work-kind heuristic ever decides a route: the declared table or the
// quarantined data are the only sources. A table-less definition the
// quarantine does not name — an unreleased ref or a version above the
// quarantined range — resolves no routes, and registration refuses it when
// its graph declares evaluator steps.

// WorkflowRecoveryTrigger is the closed trigger enum a recovery route may
// declare. unhealthy_verdict opens the return a bad evaluation takes;
// disproved_premise_at_complete opens the pinned complete-step contract
// correction (CD-0172). Same-step attempt recovery has no target to declare
// and stays with its existing owner.
type WorkflowRecoveryTrigger string

const (
	WorkflowRecoveryTriggerUnhealthyVerdict           WorkflowRecoveryTrigger = "unhealthy_verdict"
	WorkflowRecoveryTriggerDisprovedPremiseAtComplete WorkflowRecoveryTrigger = "disproved_premise_at_complete"
)

// WorkflowRecoveryRoute declares one cross-step correction path.
type WorkflowRecoveryRoute struct {
	// Step is the step the route returns from: the step that declares
	// record_verdict or confirm_premise for an unhealthy verdict, or the
	// step that declares complete for a premise disproved at completion.
	Step string `json:"step"`
	// Trigger is the closed condition that opens the route.
	Trigger WorkflowRecoveryTrigger `json:"trigger"`
	// Action is the correction action the route admits at the step.
	Action string `json:"action"`
	// Target is the producer step the route returns to: the step that
	// re-produces the artifact the evaluator judges.
	Target string `json:"target"`
}

// workflowRecoveryRouteActions is the closed action enum a route may admit.
// Both members are engine-owned recovery actions resolvable off a pinned step
// through workflowRecoveryActionDefinition; a definition does not need to
// list them at root for a route to name them.
var workflowRecoveryRouteActions = []string{"request_correction", "supersede_contract"}

func workflowRecoveryRouteActionDeclared(actionID string) bool {
	return containsString(workflowRecoveryRouteActions, actionID)
}

func validWorkflowRecoveryTrigger(trigger WorkflowRecoveryTrigger) bool {
	return trigger == WorkflowRecoveryTriggerUnhealthyVerdict || trigger == WorkflowRecoveryTriggerDisprovedPremiseAtComplete
}

// workflowRecoveryRouteActionForTrigger pairs each trigger with the one
// correction action it admits: an unhealthy verdict opens request_correction
// (CD-0143), and a premise disproved at the pinned complete step opens
// supersede_contract (CD-0172 D1/D2). A route that crosses the pairing
// refuses at registration.
func workflowRecoveryRouteActionForTrigger(trigger WorkflowRecoveryTrigger) string {
	switch trigger {
	case WorkflowRecoveryTriggerUnhealthyVerdict:
		return "request_correction"
	case WorkflowRecoveryTriggerDisprovedPremiseAtComplete:
		return "supersede_contract"
	}
	return ""
}

// One route-content table per family (D3, CD-0172 D3). The authored versions
// declare this data and the quarantined released table resolves the same
// data, so the two sources cannot drift.

func implementationRecoveryRoutes() []WorkflowRecoveryRoute {
	return []WorkflowRecoveryRoute{
		{Step: "acceptance", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
		{Step: "release", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execution"},
		{Step: "release", Trigger: WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: "execution"},
	}
}

func breakFixRecoveryRoutes() []WorkflowRecoveryRoute {
	return []WorkflowRecoveryRoute{
		{Step: "verify", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair"},
		{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "repair"},
		{Step: "complete", Trigger: WorkflowRecoveryTriggerDisprovedPremiseAtComplete, Action: "supersede_contract", Target: "repair"},
	}
}

func researchRecoveryRoutes() []WorkflowRecoveryRoute {
	return []WorkflowRecoveryRoute{
		{Step: "conclude", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate"},
		{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate"},
	}
}

func architectureSpikeRecoveryRoutes() []WorkflowRecoveryRoute {
	return []WorkflowRecoveryRoute{
		{Step: "review", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record"},
		{Step: "acceptance", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record"},
		{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "decision_record"},
	}
}

func opsRunbookRecoveryRoutes() []WorkflowRecoveryRoute {
	return []WorkflowRecoveryRoute{
		{Step: "health", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
		{Step: "cleanup", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
		{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
	}
}

func staticAnalysisRecoveryRoutes() []WorkflowRecoveryRoute {
	return []WorkflowRecoveryRoute{
		{Step: "review", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze"},
		{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "analyze"},
	}
}

func genericOneOffRecoveryRoutes() []WorkflowRecoveryRoute {
	return []WorkflowRecoveryRoute{
		{Step: "verify", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
		{Step: "complete", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "execute"},
	}
}

// workflowReleasedRecoveryRouteFamily quarantines one family's released
// routes. through is the last released version of the family that declares no
// recovery-route table of its own; every version at or below it resolves
// exactly these routes, and no version above it exists without a declared
// table. The bound is authored data pinned per (ref, version) by
// workflow_recovery_routes_test.go; it is never derived from the registry, a
// graph, or a work kind. One route set serves the whole range only while
// every version's graph yields the same routes: implementation pins its
// complete action on release and break_fix on complete for every released
// version, and registration validates the resolved routes against each
// version's own graph, so a version whose shape drifted refuses instead of
// resolving a route its graph does not support.
type workflowReleasedRecoveryRouteFamily struct {
	through int64
	routes  []WorkflowRecoveryRoute
}

var workflowReleasedRecoveryRouteFamilies = map[string]workflowReleasedRecoveryRouteFamily{
	"workflow.implementation":     {through: 22, routes: implementationRecoveryRoutes()},
	"workflow.break_fix":          {through: 19, routes: breakFixRecoveryRoutes()},
	"workflow.research":           {through: 13, routes: researchRecoveryRoutes()},
	"workflow.architecture_spike": {through: 14, routes: architectureSpikeRecoveryRoutes()},
	"workflow.ops_runbook":        {through: 15, routes: opsRunbookRecoveryRoutes()},
	"workflow.static_analysis":    {through: 12, routes: staticAnalysisRecoveryRoutes()},
	"workflow.generic_one_off":    {through: 13, routes: genericOneOffRecoveryRoutes()},
}

// workflowReleasedRecoveryRoutes resolves the recovery routes of a released
// definition version that declares no table of its own. An unknown ref or
// version resolves nil: the quarantine never guesses a table from a graph or
// a work kind, so a definition the quarantine does not name fails closed
// until a release ships its routes (CD-0201 D4). The returned slice is a
// defensive copy; mutating it cannot alter the authoritative data.
func workflowReleasedRecoveryRoutes(ref string, version int64) []WorkflowRecoveryRoute {
	family, ok := workflowReleasedRecoveryRouteFamilies[ref]
	if !ok || version < 1 || version > family.through {
		return nil
	}
	return cloneWorkflowRecoveryRoutes(family.routes)
}

// workflowRecoveryRoutes is the one reader every recovery decision feeds
// from: the admission fold, workflowAdmit, the action guards, the folds, the
// work-pin intents, the CD-0164 correction counting and three-attempt wall,
// the CD-0172 complete-step return, and the liveness model. A definition that
// declares a table resolves it; a released version without one resolves the
// quarantined data for its (ref, version). The returned slice is a defensive
// copy and the definition is not mutated.
func workflowRecoveryRoutes(definition WorkflowDefinition) []WorkflowRecoveryRoute {
	if len(definition.RecoveryRoutes) > 0 {
		return cloneWorkflowRecoveryRoutes(definition.RecoveryRoutes)
	}
	return workflowReleasedRecoveryRoutes(definition.Ref, definition.Version)
}

// workflowRecoveryRouteTarget resolves the one target a step's route for one
// trigger declares, or "" when the step declares no such route. It is the
// table read every cross-step recovery decision uses; no caller walks the
// graph, orders the step slice, or reads a work kind to pick a target.
func workflowRecoveryRouteTarget(definition WorkflowDefinition, stepID string, trigger WorkflowRecoveryTrigger) string {
	for _, route := range workflowRecoveryRoutes(definition) {
		if route.Step == stepID && route.Trigger == trigger {
			return route.Target
		}
	}
	return ""
}

// workflowUnhealthyVerdictRouteTarget resolves the producer step the current
// step's unhealthy_verdict route returns to: the target that re-produces the
// artifact the step's evaluation judges. A step that records a verdict or
// confirms a premise resolves exactly one (D4); any other step resolves none.
func workflowUnhealthyVerdictRouteTarget(definition WorkflowDefinition, stepID string) string {
	return workflowRecoveryRouteTarget(definition, stepID, WorkflowRecoveryTriggerUnhealthyVerdict)
}

// workflowDisprovedPremiseAtCompleteRouteTarget resolves the producer step the
// pinned complete step's CD-0172 contract-correction route returns to. Only a
// step that declares the complete action may carry the route, so the resolved
// target is also the one shape test for the complete-step correction family.
func workflowDisprovedPremiseAtCompleteRouteTarget(definition WorkflowDefinition, stepID string) string {
	return workflowRecoveryRouteTarget(definition, stepID, WorkflowRecoveryTriggerDisprovedPremiseAtComplete)
}

// workflowDeliveryGateCorrectionTarget resolves the corrective return a parked
// CD-0166 delivery gate takes, through the route table: the gate serves the
// verdict step its single forward edge delivers into, so the gate's return
// follows that step's declared unhealthy_verdict route to the same producer.
// The read is one forward edge plus one table lookup — no step-kind order, no
// nearest-external-effect walk — so the gate cannot drift from the route its
// evaluator declares.
func workflowDeliveryGateCorrectionTarget(definition WorkflowDefinition, gateStep string) string {
	for _, edge := range definition.StepGraph.Edges {
		if edge.From != gateStep || edge.Kind != WorkflowEdgeForward {
			continue
		}
		return workflowUnhealthyVerdictRouteTarget(definition, edge.To)
	}
	return ""
}

// workflowCorrectionReturnTarget resolves the producer step one admitted
// request_correction returns the instance to: the step's own declared
// unhealthy_verdict route — every admitted late evaluator context, terminal
// or premise-question, declares one — or, at a CD-0166 delivery gate, the
// route of the verdict step its single forward edge delivers into. The fold,
// the admission derivation, and the work pin read this one function, so the
// recorded move and the admitted intent cannot disagree about the return. No
// step-kind order, no nearest-verdict walk, and no work kind picks the
// target: the target always comes from the declared table.
func workflowCorrectionReturnTarget(definition WorkflowDefinition, currentStep string) string {
	if target := workflowUnhealthyVerdictRouteTarget(definition, currentStep); target != "" {
		return target
	}
	if workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		return workflowDeliveryGateCorrectionTarget(definition, currentStep)
	}
	return ""
}

// workflowStalenessSpanTargets resolves the producing steps whose artifact
// the current step still owes or judges: the declared unhealthy_verdict
// route targets whose producer-to-evaluator span holds the step. A step lies
// in a route's span when the route target reaches it and it reaches the
// route step through forward edges — the producer, every intermediate step,
// and the evaluator itself. The historical staleness bit therefore survives
// the corrective return to the producer and every refine or gate step
// between producer and evaluator; a step no route spans folds unstale, so
// upstream contract and analysis steps never wear an artifact they do not
// touch. Ownership comes from the declared routes only, never from
// nearest-step order, and multiple routes naming one target collapse to it.
func workflowStalenessSpanTargets(definition WorkflowDefinition, stepID string) []string {
	var targets []string
	for _, route := range workflowRecoveryRoutes(definition) {
		if route.Trigger != WorkflowRecoveryTriggerUnhealthyVerdict {
			continue
		}
		if !workflowStepReachesByForwardEdges(definition.StepGraph, route.Target, stepID) {
			continue
		}
		if !workflowStepReachesByForwardEdges(definition.StepGraph, stepID, route.Step) {
			continue
		}
		if !containsString(targets, route.Target) {
			targets = append(targets, route.Target)
		}
	}
	return targets
}

// workflowStepArtifactActions derives the artifact-producing actions one step
// owns: the closed positive production classification (CD-0201 D5).
// Production is declared positively per owning mechanism and never derived
// by subtracting exclusions from an execution mode: record_delivery is the
// delivery-recording completion that declares the artifact delivered on the
// worker-run families' producer steps, record_finding records the findings
// the research producer owns, and record_decision records the decision the
// spike producer owns. Every other completion — a health reading, a
// candidate revision, an impact declaration, a successor link, a fenced
// start, a checkpoint, an evidence binding, or a verdict — never re-produces
// the artifact an evaluator judges, whatever its mode. A family that gains a
// new producing mechanism declares it here; nothing else changes.
func workflowStepArtifactActions(definition WorkflowDefinition, stepID string) []string {
	step := workflowStep(definition, stepID)
	if step == nil {
		return nil
	}
	var actions []string
	for _, actionID := range step.Actions {
		switch actionID {
		case "record_delivery", "record_finding", "record_decision":
			actions = append(actions, actionID)
		}
	}
	return actions
}

func cloneWorkflowRecoveryRoutes(routes []WorkflowRecoveryRoute) []WorkflowRecoveryRoute {
	if routes == nil {
		return nil
	}
	cloned := make([]WorkflowRecoveryRoute, len(routes))
	copy(cloned, routes)
	return cloned
}

// workflowStepReachesByForwardEdges reports whether source reaches target by
// following forward edges only. The producer rule is graph-derived: a target
// that cannot reach the route step this way does not produce what the
// evaluator judges.
func workflowStepReachesByForwardEdges(graph WorkflowStepGraph, source, target string) bool {
	visited := map[string]bool{}
	queue := []string{source}
	for len(queue) != 0 {
		stepID := queue[0]
		queue = queue[1:]
		if visited[stepID] {
			continue
		}
		visited[stepID] = true
		if stepID == target {
			return true
		}
		for _, edge := range graph.Edges {
			if edge.From == stepID && edge.Kind == WorkflowEdgeForward {
				queue = append(queue, edge.To)
			}
		}
	}
	return false
}

// validateWorkflowRecoveryRoutes is the D4 structural gate
// ValidateWorkflowDefinition applies to every registered definition. It reads
// the routes the one owner resolves — the declared table, or the quarantined
// data for a table-less released (ref, version) — so no table-less shape is
// exempt: an unreleased definition whose graph declares evaluator steps
// resolves no routes and the coverage clause refuses it, while a released
// table-less version must resolve quarantined routes that hold against its
// own graph. Each route must pair its trigger with its action and sit on the
// step that records the trigger's condition: an unhealthy_verdict route
// belongs to a step that records a verdict or confirms a premise, and a
// disproved_premise_at_complete route belongs to a step that declares the
// complete action (CD-0172 D1). The graph bound (at most one failure edge per
// step) holds for table-less released versions too.
func validateWorkflowRecoveryRoutes(definition WorkflowDefinition) error {
	graph := definition.StepGraph
	failureEdges := make(map[string]int, len(graph.Edges))
	for _, edge := range graph.Edges {
		if edge.Kind == WorkflowEdgeFailure {
			failureEdges[edge.From]++
		}
	}
	for stepID, count := range failureEdges {
		if count > 1 {
			return definitionFailure(KindInvalidDefinition, "definition step declares more than one failure edge: "+stepID)
		}
	}
	routes := workflowRecoveryRoutes(definition)
	if len(routes) > 32 {
		return definitionFailure(KindInvalidDefinition, "definition resolves more than 32 recovery routes")
	}
	steps := make(map[string]WorkflowStep, len(graph.Steps))
	for _, candidate := range graph.Steps {
		steps[candidate.ID] = candidate
	}
	terminals := make(map[string]bool, len(graph.TerminalSteps))
	for _, terminal := range graph.TerminalSteps {
		terminals[terminal] = true
	}
	declared := make(map[string]bool, len(routes))
	unhealthyRoutes := make(map[string]bool, len(routes))
	for _, route := range routes {
		if !validWorkflowRecoveryTrigger(route.Trigger) {
			return definitionFailure(KindInvalidDefinition, "recovery route trigger is not declared: "+string(route.Trigger))
		}
		if _, ok := steps[route.Step]; !ok {
			return definitionFailure(KindInvalidDefinition, "recovery route step is not declared: "+route.Step)
		}
		if !workflowRecoveryRouteActionDeclared(route.Action) {
			return definitionFailure(KindInvalidDefinition, "recovery route action is not declared: "+route.Action)
		}
		if route.Action != workflowRecoveryRouteActionForTrigger(route.Trigger) {
			return definitionFailure(KindInvalidDefinition, "recovery route action does not pair with its trigger: "+route.Step)
		}
		target, ok := steps[route.Target]
		if !ok {
			return definitionFailure(KindInvalidDefinition, "recovery route target is not declared: "+route.Target)
		}
		key := route.Step + "\x00" + string(route.Trigger)
		if declared[key] {
			return definitionFailure(KindInvalidDefinition, "recovery route is declared twice for one step and trigger: "+route.Step)
		}
		declared[key] = true
		routeStep := steps[route.Step]
		if route.Trigger == WorkflowRecoveryTriggerUnhealthyVerdict {
			if !containsString(routeStep.Actions, "record_verdict") && !containsString(routeStep.Actions, "confirm_premise") && !terminals[route.Step] {
				return definitionFailure(KindInvalidDefinition, "unhealthy_verdict recovery route sits on a step that records neither verdict nor premise and hosts no late-verdict context: "+route.Step)
			}
			unhealthyRoutes[route.Step] = true
		}
		if route.Trigger == WorkflowRecoveryTriggerDisprovedPremiseAtComplete && !containsString(routeStep.Actions, "complete") {
			return definitionFailure(KindInvalidDefinition, "disproved_premise_at_complete recovery route sits on a step that declares no complete action: "+route.Step)
		}
		if route.Target == route.Step {
			return definitionFailure(KindInvalidDefinition, "recovery route target is the route step: "+route.Step)
		}
		if terminals[route.Target] {
			return definitionFailure(KindInvalidDefinition, "recovery route target is terminal: "+route.Target)
		}
		if !workflowStepReachesByForwardEdges(graph, route.Target, route.Step) {
			return definitionFailure(KindInvalidDefinition, "recovery route target does not reach the route step through forward edges: "+route.Target)
		}
		if containsString(target.Actions, "approve_contract") {
			return definitionFailure(KindInvalidDefinition, "recovery route target declares approve_contract: "+route.Target)
		}
		producer := containsString(target.Actions, "dispatch_worker") || len(workflowStepArtifactActions(definition, route.Target)) > 0
		for _, actionID := range target.Actions {
			mode, ok := workflowActionExecutionMode(definition, actionID)
			producer = producer || (ok && mode == ActionFenced)
		}
		if !producer {
			return definitionFailure(KindInvalidDefinition, "recovery route target has no artifact-producing mechanism: "+route.Target)
		}
	}
	for _, candidate := range graph.Steps {
		if !containsString(candidate.Actions, "record_verdict") && !containsString(candidate.Actions, "confirm_premise") && !workflowLateVerdictServesVerdictStep(definition, candidate.ID) {
			continue
		}
		if !unhealthyRoutes[candidate.ID] {
			return definitionFailure(KindInvalidDefinition, "definition step that records a verdict, confirms a premise, or hosts a late-verdict context resolves no unhealthy_verdict recovery route: "+candidate.ID)
		}
	}
	return nil
}

// workflowLateVerdictServesVerdictStep reports whether the step hosts the
// late-verdict correction context (CD-0204) — it is the terminal step, or it
// hosts the premise question past every verdict step — and at least one step
// declaring record_verdict precedes it, so an unhealthy verdict recorded
// there demands a declared return of its own. The route table carries that
// return; no nearest-verdict walk may derive it.
func workflowLateVerdictServesVerdictStep(definition WorkflowDefinition, stepID string) bool {
	// Registration coverage requires the late verdict hosts in CD-0204 to
	// declare their own return. Runtime admission reads only those routes.
	terminal := containsString(definition.StepGraph.TerminalSteps, stepID)
	action, question := workflowOperatorQuestionAction(definition, stepID)
	if !terminal && !(question && action == "confirm_premise" && !stepDeclaresAction(definition, stepID, "record_verdict")) {
		return false
	}
	for _, candidate := range definition.StepGraph.Steps {
		if containsString(candidate.Actions, "record_verdict") && workflowStepFollows(definition, candidate.ID, stepID) {
			return true
		}
	}
	return false
}
