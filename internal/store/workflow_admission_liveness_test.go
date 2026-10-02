package store

// The liveness law (CD-0201) as an exhaustive abstract check over every
// registered definition version. Old versions stay in scope because stranded
// items run on old pins (CD-0115 D2). The check is total: it proves
//   (a) every well-formed nonterminal abstract state admits at least one
//       non-continuity action — independent of the successor model, and
//   (b) from every model-reachable nonterminal state a terminal step is
//       reachable through admitted actions, counting operator-approvable
//       routes (approval-required actions are admitted; the operator can
//       approve them).
// Both checks fail with a witness and never skip.

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// admissionModelState is one node of the abstract admission state space:
// the workflow step, the folded post-rejection review debt, and the typed
// verdict of the settling review whose acceptance stands ready.
type admissionModelState struct {
	step  string
	debt  WorkflowReviewDebt
	ready string // "", "ship", "absent"
}

// admissionHoldMode reports whether the action's execution mode holds the
// work in place. The hold mode is the definition-owned base classifier; the
// successor comparison in admissionExitAction refines it for holds whose
// fold still moves the item.
func admissionHoldMode(definition WorkflowDefinition, actionID string) bool {
	mode, ok := workflowActionExecutionMode(definition, actionID)
	if !ok {
		if recovery, isRecovery := workflowRecoveryActionDefinition(actionID); isRecovery {
			mode, ok = recovery.ExecutionMode, true
		}
	}
	if !ok {
		return true // unknown actions are held conservatively: never proof of an exit
	}
	return mode == ActionHold
}

// admissionExitAction reports whether one admitted action moves the work
// out of the state: a non-hold execution mode, or a hold whose fold still
// carries the item elsewhere — request_correction is hold-moded but its fold
// returns a parked gate's change to the correction target step, so it is the
// parked gate's recovery exit, never a continuity action. An action whose
// successor equals the state holds the work in place; a state whose only
// admitted actions hold is stranded no matter how many of them admit.
func admissionExitAction(definition WorkflowDefinition, state admissionModelState, actionID string) bool {
	if admissionHoldMode(definition, actionID) && admissionSuccessor(definition, state, actionID) == state {
		return false
	}
	return true
}

// admissionStateActions resolves the action universe of one abstract state:
// the actions the step declares, plus the recovery actions whose admission
// the folded state fully owns. request_correction counts only at a
// correction workflow's delivery gate under outstanding debt — the parked
// gate admission the engine proves — because the other recoveries'
// preconditions (stale law, failed attempts, verdict histories) are not
// folded into this state yet and counting them would manufacture exits no
// engine admits.
func admissionStateActions(definition WorkflowDefinition, state admissionModelState) []string {
	seen := map[string]bool{}
	var actions []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			actions = append(actions, id)
		}
	}
	for _, step := range definition.StepGraph.Steps {
		if step.ID != state.step {
			continue
		}
		for _, actionID := range step.Actions {
			add(actionID)
		}
	}
	if workflowCorrectionWorkflow(definition) && state.debt == ReviewDebtOutstanding && workflowStepIsDeliveryGate(workflowStep(definition, state.step)) {
		if _, ok := workflowRecoveryActionDefinition("request_correction"); ok {
			add("request_correction")
		}
	}
	sort.Strings(actions)
	return actions
}

// wellFormedAdmissionStates enumerates the well-formed abstract states of one
// definition version: every step crossed with the debt and ready-review
// combinations the loader can fold. The loader leaves the debt none outside a
// correction workflow's review steps, and a ready review implies outstanding
// debt; a no-ship ready review is never folded, so it is not well-formed.
func wellFormedAdmissionStates(definition WorkflowDefinition) []admissionModelState {
	correction := workflowCorrectionWorkflow(definition)
	var states []admissionModelState
	for _, step := range definition.StepGraph.Steps {
		state := admissionModelState{step: step.ID, debt: ReviewDebtNone}
		states = append(states, state)
		if !correction || !workflowPostRejectionReviewStep(definition, step.ID) {
			continue
		}
		states = append(states,
			admissionModelState{step: step.ID, debt: ReviewDebtOutstanding},
			admissionModelState{step: step.ID, debt: ReviewDebtOutstanding, ready: "ship"},
			admissionModelState{step: step.ID, debt: ReviewDebtOutstanding, ready: "absent"},
		)
	}
	return states
}

// admissionWorkflowState lifts the abstract model state into the folded
// WorkflowAdmissionState workflowAdmit decides over.
func admissionWorkflowState(definition WorkflowDefinition, state admissionModelState) WorkflowAdmissionState {
	return WorkflowAdmissionState{
		Step:                 state.step,
		CorrectionWorkflow:   workflowCorrectionWorkflow(definition),
		ReviewStep:           workflowPostRejectionReviewStep(definition, state.step),
		ReviewDebt:           state.debt,
		ReadyReviewAttemptID: admissionReadyAttemptID(state),
		ReadyReviewVerdict:   state.ready,
	}
}

// admissionReadyAttemptID names the ready review the model carries, so the
// identity-satisfying accept route counts as admitted.
func admissionReadyAttemptID(state admissionModelState) string {
	if state.ready == "" {
		return ""
	}
	return "attempt:model:ready-" + state.ready
}

// admissionSuccessor is the abstract successor of one admitted action over
// one model state. The step move is definition-owned: an advance-mode action
// follows the forward edge; a hold or fenced action keeps the step. The
// review-family effects are the folded debt semantics: a rejection opens the
// debt, a review dispatch completes with a settling verdict, and the
// identity-satisfying accept of the ready review settles the debt and
// carries the advance its mode names.
func admissionSuccessor(definition WorkflowDefinition, state admissionModelState, actionID string) admissionModelState {
	next := state
	switch actionID {
	case "reject_worker_result":
		next.debt, next.ready = ReviewDebtOutstanding, ""
	case "dispatch_worker":
		if next.debt == ReviewDebtOutstanding && next.ready == "" {
			next.ready = "ship" // a fresh review dispatch completes and awaits acceptance
		}
	case "accept_worker_result":
		if next.ready != "" {
			next.debt, next.ready = ReviewDebtNone, ""
		}
	case "request_correction":
		if target := workflowCorrectionTargetStep(definition, state.step); target != "" {
			next.step = target
		}
	}
	if actionID != "request_correction" {
		if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
			if forward := workflowNextStep(definition, state.step); forward != "" {
				next.step = forward
			}
		}
	}
	return next
}

// admissionModelMoves resolves the admitted moves of one state: every action
// of the state's universe whose workflowAdmit decision admits it. An accept
// move stands for the identity-satisfying route the guard binds when the
// state carries a ready review.
func admissionModelMoves(definition WorkflowDefinition, state admissionModelState) []string {
	folded := admissionWorkflowState(definition, state)
	var moves []string
	for _, actionID := range admissionStateActions(definition, state) {
		if workflowAdmit(definition, folded, actionID).Admitted {
			moves = append(moves, actionID)
		}
	}
	return moves
}

func admissionTerminalStep(definition WorkflowDefinition, step string) bool {
	return containsString(definition.StepGraph.TerminalSteps, step)
}

// TestWellFormedAdmissionStateHasNonContinuityExit proves liveness clause
// (a) over every registered definition version: every well-formed nonterminal
// abstract state admits at least one non-continuity action.
func TestWellFormedAdmissionStateHasNonContinuityExit(t *testing.T) {
	definitions := builtinWorkflowDefinitionsWithHistory()
	if len(definitions) == 0 {
		t.Fatal("no built-in workflow definitions are registered")
	}
	for _, definition := range definitions {
		for _, state := range wellFormedAdmissionStates(definition) {
			if admissionTerminalStep(definition, state.step) {
				continue
			}
			exits := []string{}
			for _, actionID := range admissionModelMoves(definition, state) {
				if admissionExitAction(definition, state, actionID) {
					exits = append(exits, actionID)
				}
			}
			if len(exits) == 0 {
				t.Fatalf("%s v%d: well-formed nonterminal state (step %q debt %q ready %q) admits no non-continuity action; admitted: %s",
					definition.Ref, definition.Version, state.step, state.debt, state.ready, strings.Join(admissionModelMoves(definition, state), ", "))
			}
		}
	}
}

// TestReachableAdmissionStateReachesTerminal proves liveness clause (b) over
// every registered definition version: from every model-reachable nonterminal
// state, a terminal step is reachable through admitted actions. The witness
// names the definition, the stranded state, and the admitted moves the model
// exhausted.
func TestReachableAdmissionStateReachesTerminal(t *testing.T) {
	definitions := builtinWorkflowDefinitionsWithHistory()
	if len(definitions) == 0 {
		t.Fatal("no built-in workflow definitions are registered")
	}
	for _, definition := range definitions {
		start := admissionModelState{step: definition.StepGraph.StartStep}
		reachable := admissionReachableStates(definition, start)
		for _, state := range reachable {
			if admissionTerminalStep(definition, state.step) {
				continue
			}
			if !admissionReachesTerminal(definition, state, map[admissionModelState]bool{}) {
				t.Fatalf("%s v%d: reachable nonterminal state (step %q debt %q ready %q) reaches no terminal step; admitted moves: %s",
					definition.Ref, definition.Version, state.step, state.debt, state.ready,
					strings.Join(admissionModelMoves(definition, state), ", "))
			}
		}
	}
}

// admissionReachableStates is the BFS over the abstract successor from the
// start state through admitted moves.
func admissionReachableStates(definition WorkflowDefinition, start admissionModelState) []admissionModelState {
	seen := map[admissionModelState]bool{start: true}
	frontier := []admissionModelState{start}
	var reachable []admissionModelState
	for len(frontier) != 0 {
		var next []admissionModelState
		for _, state := range frontier {
			reachable = append(reachable, state)
			for _, actionID := range admissionModelMoves(definition, state) {
				successor := admissionSuccessor(definition, state, actionID)
				if !seen[successor] {
					seen[successor] = true
					next = append(next, successor)
				}
			}
		}
		frontier = next
	}
	return reachable
}

// admissionReachesTerminal is the depth-first terminal search over admitted
// moves, bounded by the visited set so cycles cannot loop it.
func admissionReachesTerminal(definition WorkflowDefinition, state admissionModelState, visiting map[admissionModelState]bool) bool {
	if admissionTerminalStep(definition, state.step) {
		return true
	}
	if visiting[state] {
		return false
	}
	visiting[state] = true
	for _, actionID := range admissionModelMoves(definition, state) {
		if admissionReachesTerminal(definition, admissionSuccessor(definition, state, actionID), visiting) {
			return true
		}
	}
	return false
}

// The model's witness rendering for a failing path, used by the conformance
// test's failure output.
func admissionModelPathString(definition string, path []admissionModelState) string {
	parts := make([]string, 0, len(path))
	for _, state := range path {
		parts = append(parts, fmt.Sprintf("%s(Step=%s,debt=%s,ready=%s)", definition, state.step, state.debt, state.ready))
	}
	return strings.Join(parts, " -> ")
}
