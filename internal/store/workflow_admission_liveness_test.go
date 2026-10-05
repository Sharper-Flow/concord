package store

// The liveness law (CD-0201 D2) as an exhaustive abstract check over every
// registered definition version. Old versions stay in scope because stranded
// items run on old pins (CD-0115 D2). The check is total: it proves
//   (a) every well-formed nonterminal abstract state admits at least one
//       action that moves the work out of the state — independent of the
//       successor model's reachability, and
//   (b) from every model-reachable nonterminal state the completed instance
//       is reachable through admitted actions, counting operator-approvable
//       routes (approval-required actions are admitted; the operator can
//       approve them).
// Both checks fail with a witness and never skip.
//
// The model folds these admission families: the step; the post-rejection
// review debt and its ready review; the latest worker attempt since the
// step's latest start, its disposition, the dispatch hold it implies, and the
// same-step failed-attempt wall; the active contract count; the law and
// registry pin staleness; the design currency; the latest predicate verdict;
// the investigation artifact and the observation after the latest verdict;
// the delivery start and current-epoch proof; missing and bound mandates;
// and the completed instance. Breaking impact notices and open external
// conditions are not folded: the model holds both at zero, so it counts no
// exit that either would close.

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

const admissionModelWorkID = "admission-model"

// admissionModelState is one node of the abstract admission state space.
type admissionModelState struct {
	step string
	// debt and ready are the post-rejection review debt and the typed
	// verdict of the ready review: "", "ship", "absent", or "no_ship".
	debt  WorkflowReviewDebt
	ready string
	// attempt is the latest worker attempt since the step's latest start:
	// "", "completed", "failed", "failure_recorded", or "rejected".
	attempt string
	// failed is the same-step failed-attempt count, capped at the wall.
	failed int64
	// contracts is the active contract count: 2 is the duplicated
	// projection the supersede recovery owns.
	contracts int64
	// stale is the pin staleness: "", "law" (a stale law revision every
	// action refuses on), or "registry" (the subject's own stale registry
	// pin, which attempt disposition admits).
	stale string
	// designStale is a recorded design a contract correction invalidated.
	designStale bool
	// verdict is the latest predicate verdict: "", "ok", or "bad".
	verdict string
	// artifact is the recorded investigation artifact; observed is a
	// same-work observation recorded after the latest verdict.
	artifact bool
	observed bool
	// started anchors the current delivery-bearing epoch; proof is a green
	// worktree-verify binding after that start. mandate is absent, missing,
	// or bound under the active contract.
	started bool
	proof   bool
	mandate string
	// done is the completed instance with the closed work lifecycle.
	done bool
}

func (s admissionModelState) String() string {
	return fmt.Sprintf("(step %q debt %q ready %q attempt %q failed %d contracts %d stale %q designStale %v verdict %q artifact %v observed %v started %v proof %v mandate %q done %v)",
		s.step, s.debt, s.ready, s.attempt, s.failed, s.contracts, s.stale, s.designStale, s.verdict, s.artifact, s.observed, s.started, s.proof, s.mandate, s.done)
}

func admissionModelStart(definition WorkflowDefinition) admissionModelState {
	return admissionModelState{step: definition.StepGraph.StartStep, debt: ReviewDebtNone}
}

// admissionContinuityAction names the continuity actions: they record session
// context and never move the work, whatever mode a historical pin declares.
func admissionContinuityAction(actionID string) bool {
	return actionID == "checkpoint_context" || actionID == "cross_context_boundary"
}

// admissionStateActions resolves the action universe of one abstract state:
// the actions the step declares plus every off-step recovery action. The
// admission decision and the guard preconditions decide which admit.
func admissionStateActions(definition WorkflowDefinition, state admissionModelState) []string {
	seen := map[string]bool{}
	var actions []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			actions = append(actions, id)
		}
	}
	if step := workflowStep(definition, state.step); step != nil {
		for _, actionID := range step.Actions {
			add(actionID)
		}
	}
	for _, actionID := range []string{"supersede_contract", "record_verdict", "record_worker_failure", "reject_worker_result", "request_correction", "bind_evidence"} {
		add(actionID)
	}
	sort.Strings(actions)
	return actions
}

// admissionModelStaleError is the staleness boundary's refusal the model
// folds for one staleness class and admission.
func admissionModelStaleError(stale string, ownMarker bool) error {
	switch stale {
	case "law":
		return newFailure(KindStaleLawRevision, "workflow_law_revision", "the model's pinned law revision is stale", false, "supersede the workflow contract")
	case "registry":
		if ownMarker {
			return nil
		}
		failure := newFailure(KindStaleRequiresReview, "workflow_domain_overlap", "the model's Domain registry pin is stale", false, "supersede the workflow contract")
		failure.StaleDomainRegistryPin = &StaleDomainRegistryPin{WorkID: admissionModelWorkID}
		return failure
	}
	return nil
}

// admissionCheckpointStep reports a human checkpoint step.
func admissionCheckpointStep(definition WorkflowDefinition, stepID string) bool {
	step := workflowStep(definition, stepID)
	return step != nil && step.Kind == WorkflowStepHumanCheckpoint
}

// admissionDispatchHold is the one hold rule over the model's attempt: at a
// human checkpoint the latest attempt holds until an accept or a failure
// record dispositions it; on every other step any attempt since the latest
// start holds until an accept dispositions it.
func admissionDispatchHold(definition WorkflowDefinition, state admissionModelState) bool {
	if admissionCheckpointStep(definition, state.step) {
		return state.attempt == "completed" || state.attempt == "failed" || state.attempt == "rejected"
	}
	return state.attempt != ""
}

// admissionWorkerFailureRecovery is the off-step failure record route: the
// step does not declare the record, and either the definition declares it
// nowhere or the step is a human checkpoint.
func admissionWorkerFailureRecovery(definition WorkflowDefinition, state admissionModelState) bool {
	if state.attempt != "failed" || stepDeclaresAction(definition, state.step, "record_worker_failure") {
		return false
	}
	declared := false
	for _, action := range definition.ActionDefinitions {
		if action.ID == "record_worker_failure" {
			declared = true
		}
	}
	return !declared || admissionCheckpointStep(definition, state.step)
}

// admissionContractCorrection folds the ordinary contract correction route.
func admissionContractCorrection(definition WorkflowDefinition, state admissionModelState) bool {
	step := workflowStep(definition, state.step)
	if step == nil {
		return false
	}
	checkpoint := containsString(step.Actions, "complete") ||
		(step.Kind == WorkflowStepHumanCheckpoint && containsString(step.Actions, "confirm_premise") && !admissionTerminalStep(definition, state.step)) ||
		(step.Kind == WorkflowStepExternalEffect && containsString(step.Actions, "dispatch_worker"))
	switch {
	case !checkpoint:
		return false
	case containsString(step.Actions, "complete"):
		return admissionCompleteStepCorrection(definition, state)
	case step.Kind == WorkflowStepHumanCheckpoint:
		return true
	case state.contracts > 1:
		return true
	case state.contracts != 1:
		return false
	}
	return state.attempt == "" || state.attempt == "failed" || state.attempt == "rejected"
}

// admissionCompleteStepCorrection folds the CD-0172 complete-step route.
func admissionCompleteStepCorrection(definition WorkflowDefinition, state admissionModelState) bool {
	return workflowCompleteStepCorrectionStep(definition, state.step) && !state.done && state.contracts >= 1 &&
		(state.attempt == "" || state.attempt == "failure_recorded") && state.observed
}

// admissionLateVerdictRoute folds the late record_verdict recovery: a
// terminal or premise-question step behind a verdict step whose latest
// verdict is not ok.
func admissionLateVerdictRoute(definition WorkflowDefinition, state admissionModelState) bool {
	if !workflowLateVerdictRecoveryStep(definition, state.step) || state.contracts == 0 || state.verdict == "ok" {
		return false
	}
	for _, step := range definition.StepGraph.Steps {
		if step.ID != state.step && containsString(step.Actions, "record_verdict") {
			return true
		}
	}
	return false
}

// admissionWorkflowState lifts the abstract model state into the folded
// WorkflowAdmissionState workflowAdmit decides over, applying the loader's
// rules to the model's dimensions.
func admissionWorkflowState(definition WorkflowDefinition, state admissionModelState) WorkflowAdmissionState {
	correction := workflowCorrectionWorkflow(definition)
	reviewStep := workflowPostRejectionReviewStep(definition, state.step)
	instance, lifecycle := "running", "in_progress"
	if state.done {
		instance, lifecycle = "completed", "completed"
	}
	attemptState, disposition := "", ""
	switch state.attempt {
	case "completed":
		attemptState = "completed"
	case "rejected":
		attemptState, disposition = "completed", "reject_worker_result"
	case "failed":
		attemptState = "failed"
	case "failure_recorded":
		attemptState, disposition = "failed", "record_worker_failure"
	}
	staleErr := admissionModelStaleError(state.stale, false)
	folded := WorkflowAdmissionState{
		Step:                        state.step,
		Lifecycle:                   lifecycle,
		InstanceState:               instance,
		CorrectionWorkflow:          correction,
		ReviewStep:                  reviewStep,
		ActiveContracts:             state.contracts,
		LawPinStale:                 staleErr != nil && workflowContractRecoveryStaleness(staleErr, admissionModelWorkID),
		LawPinStaleError:            staleErr,
		LawPinSelfError:             admissionModelStaleError(state.stale, true),
		DesignStale:                 state.designStale,
		AttemptState:                attemptState,
		LatestResultDisposition:     disposition,
		ReviewDebt:                  ReviewDebtNone,
		LateVerdictRoute:            admissionLateVerdictRoute(definition, state),
		WorkerFailureRecovery:       admissionWorkerFailureRecovery(definition, state),
		CorrectionRecovery:          state.attempt == "completed" && stepDeclaresAction(definition, state.step, "dispatch_worker"),
		SameStepFailedAttempts:      state.failed,
		DispatchHold:                admissionDispatchHold(definition, state),
		PendingOperatorDecision:     state.contracts == 1 && workflowOperatorDecisionPending(definition, state.step) && state.artifact,
		CompleteStepCorrection:      admissionCompleteStepCorrection(definition, state),
		ContractCorrectionAvailable: admissionContractCorrection(definition, state),
		Delivery:                    workflowDeliveryAdmission{Started: state.started, ProofRequired: workflowRefineProofGateActive(definition, state.step)},
	}
	if folded.Delivery.ProofRequired {
		folded.Delivery.ProofReady = state.proof
		if state.started && !state.proof {
			folded.Delivery.ProofDisqualifier = "no verification evidence is bound in the current refine epoch"
		}
	}
	if state.mandate != "" {
		folded.Mandate = workflowMandateAdmission{Present: true, BindingStep: workflowEvidenceBindingStep(definition, state.step)}
		if state.mandate == "missing" {
			folded.Mandate.LawID = "spec:model"
			folded.EvidenceRecoveryRoute = state.contracts == 1 && folded.Mandate.BindingStep != ""
		}
	}
	if state.contracts > 1 {
		folded.Mandate.Failure = newFailure(KindInvariantViolation, "workflow_action", "workflow contract projection has multiple active contracts", false, "use the typed operator recovery for duplicate active contracts")
	}
	// The acceptance gate folds the recorded verdict: an open premise
	// question without one refuses confirm_premise, and record_verdict on the
	// same step stays the exit. Evidence binding is outside the model.
	if action, _ := workflowOperatorQuestionAction(definition, state.step); folded.PendingOperatorDecision && action == "confirm_premise" && state.verdict == "" {
		folded.AcceptanceDeliverablesMissing = newFailure(KindMissingEvidence, "workflow_action", "premise confirmation requires a recorded workflow verdict", false, "record_verdict before confirming the premise")
	}
	if correction && reviewStep {
		folded.ReviewDebt = state.debt
		if state.debt == ReviewDebtOutstanding {
			folded.ReadyReviewAttemptID = admissionReadyAttemptID(state)
			folded.ReadyReviewVerdict = admissionReadyVerdict(state)
			folded.ReadyReviewSettles = state.ready != "" && workflowReviewSettlesDebt(folded.ReadyReviewVerdict)
		}
	}
	gate := workflowStepIsDeliveryGate(workflowStep(definition, state.step))
	if correction && gate {
		folded.CorrectionRequestRecovery = folded.ReviewDebt == ReviewDebtOutstanding
		if !folded.CorrectionRequestRecovery {
			folded.CorrectionRequestMissing = workflowCorrectionMissingGateReview
		}
	}
	return folded
}

// admissionReadyAttemptID names the ready review the model carries, so the
// identity-satisfying accept route counts as admitted.
func admissionReadyAttemptID(state admissionModelState) string {
	if state.ready == "" {
		return ""
	}
	return "attempt:model:ready-" + state.ready
}

// admissionReadyVerdict is the loader's typed verdict for the model's ready
// review: the pre-CD-0197 review carries none.
func admissionReadyVerdict(state admissionModelState) string {
	if state.ready == "absent" {
		return ""
	}
	return state.ready
}

// admissionGuardAllows applies the guard preconditions a payload-blind
// admission cannot see but the folded history decides: an attempt
// disposition needs the attempt it disposes, a verdict needs one active
// contract, the completion gate needs an ok verdict on one fresh contract,
// and the contract approval needs no contract.
func admissionGuardAllows(state admissionModelState, actionID string) bool {
	switch actionID {
	case "accept_worker_result", "accept_worker_evidence", "reject_worker_result":
		return state.attempt == "completed"
	case "record_worker_failure":
		return state.attempt == "failed"
	case "record_verdict":
		return state.contracts == 1
	case "complete":
		return state.contracts == 1 && state.verdict == "ok"
	case "approve_contract":
		return state.contracts == 0
	}
	return true
}

// admissionModelMoves resolves the admitted moves of one state: every action
// of the state's universe whose workflowAdmit decision admits it, or stands
// behind operator approval, and whose guard precondition holds, plus the
// debt family's deferred advance — the fresh-review refusal the review gate
// owns, whose identity-satisfying accept the guard admits.
func admissionModelMoves(definition WorkflowDefinition, state admissionModelState) []string {
	folded := admissionWorkflowState(definition, state)
	var moves []string
	for _, actionID := range admissionStateActions(definition, state) {
		if !admissionGuardAllows(state, actionID) {
			continue
		}
		decision := workflowAdmit(definition, folded, actionID)
		if decision.Admitted || decision.ApprovalRequired || (workflowAdmissionDefersToReviewGate(decision, actionID) && folded.ReadyReviewAttemptID != "") {
			moves = append(moves, actionID)
		}
	}
	return moves
}

// admissionEnterStep moves the model to a new step: the step entry anchors a
// fresh same-step wall and a fresh attempt window.
func admissionEnterStep(definition WorkflowDefinition, state admissionModelState, step string) admissionModelState {
	if step == "" || step == state.step {
		return state
	}
	// A delivery gate reads the incoming delivery-bearing step's start.
	// Other step entries need their own start and a new epoch proof.
	if !workflowStepIsDeliveryGate(workflowStep(definition, step)) {
		state.started = false
	}
	state.proof = false
	state.step, state.attempt, state.failed = step, "", 0
	return state
}

func admissionFailureEdgeTarget(definition WorkflowDefinition, step string) string {
	for _, edge := range definition.StepGraph.Edges {
		if edge.From == step && edge.Kind == WorkflowEdgeFailure {
			return edge.To
		}
	}
	return ""
}

func admissionDeclaresAction(definition WorkflowDefinition, actionID string) bool {
	for _, step := range definition.StepGraph.Steps {
		if containsString(step.Actions, actionID) {
			return true
		}
	}
	return false
}

// admissionSuccessors is the abstract successor set of one admitted action
// over one model state. A worker dispatch completes or fails, and a review
// completes with any typed verdict; a verdict records ok or not; a contract
// supersession carries a design record or not, and runs with or without a
// successor contract. The step move is definition-owned: an advance-mode
// action follows the forward edge, and every other mode keeps the step.
func admissionSuccessors(definition WorkflowDefinition, state admissionModelState, actionID string) []admissionModelState {
	if admissionContinuityAction(actionID) {
		// Continuity records only its typed event and no generic
		// completion, so the fold never moves the step on it, whatever
		// mode a version-1 pin declares (CD-0112 D1).
		return []admissionModelState{state}
	}
	next := state
	advance := true
	switch actionID {
	case "dispatch_worker":
		next.started, next.proof = true, false
		completed := next
		completed.attempt = "completed"
		failed := next
		failed.attempt = "failed"
		if failed.failed < workflowCorrectionAttemptLimit {
			failed.failed++
		}
		if next.debt != ReviewDebtOutstanding {
			return []admissionModelState{completed, failed}
		}
		// A fresh review dispatch supersedes any ready review: the loader
		// names the latest completed review whose acceptance no action has
		// dispositioned, so the new completion replaces the standing one.
		ship, noShip := completed, completed
		ship.ready, noShip.ready = "ship", "no_ship"
		return []admissionModelState{ship, noShip, failed}
	case "accept_worker_result":
		next.attempt, next.failed = "", 0
		if next.ready != "" {
			if next.ready == "no_ship" {
				// The no_ship accept preserves the findings and settles
				// nothing: the debt stays outstanding and the advance waits
				// for a settling review (CD-0201 D3).
				next.ready = ""
				return []admissionModelState{next}
			}
			next.debt, next.ready = ReviewDebtNone, ""
		}
	case "accept_worker_evidence":
		next.attempt = ""
	case "reject_worker_result":
		next.attempt = "rejected"
		if workflowCorrectionWorkflow(definition) && workflowPostRejectionReviewStep(definition, state.step) {
			next.debt, next.ready = ReviewDebtOutstanding, ""
		}
	case "record_worker_failure":
		next.attempt = "failure_recorded"
	case "approve_contract":
		next.contracts = 1
		plain := admissionEnterStep(definition, next, workflowNextStep(definition, state.step))
		mandated := plain
		mandated.mandate = "missing"
		return []admissionModelState{plain, mandated}
	case "bind_evidence":
		if next.mandate == "missing" {
			next.mandate = "bound"
		}
	case "record_verdict":
		ok, bad := next, next
		ok.verdict, bad.verdict = "ok", "bad"
		ok.observed, bad.observed = false, false
		return []admissionModelState{ok, bad}
	case "confirm_premise":
		target := workflowNextStep(definition, state.step)
		if stepDeclaresAction(definition, state.step, "record_verdict") && state.verdict == "bad" {
			if failure := admissionFailureEdgeTarget(definition, state.step); failure != "" {
				target = failure
			}
		}
		return []admissionModelState{admissionEnterStep(definition, next, target)}
	case "request_correction":
		return []admissionModelState{admissionEnterStep(definition, next, workflowCorrectionTargetStep(definition, state.step))}
	case "complete":
		next.done = true
		return []admissionModelState{next}
	case "supersede_contract":
		return admissionSupersedeSuccessors(definition, state)
	default:
		if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionFenced && !admissionContinuityAction(actionID) {
			// A fresh fenced start opens a new attempt window and releases
			// the hold of every attempt before it.
			next.attempt = ""
			next.started, next.proof = true, false
		}
	}
	if advance {
		if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
			next = admissionEnterStep(definition, next, workflowNextStep(definition, state.step))
		}
	}
	return []admissionModelState{next}
}

// admissionSupersedeSuccessors folds the contract supersession: a successor
// contract re-pins the law and starts a fresh verdict record; at the pinned
// complete step it returns the instance to the correction target; without a
// successor the instance returns to the contract step with no contract.
func admissionSupersedeSuccessors(definition WorkflowDefinition, state admissionModelState) []admissionModelState {
	successor := state
	successor.contracts, successor.stale, successor.verdict, successor.observed = 1, "", "", false
	if workflowCompleteStepCorrectionStep(definition, state.step) {
		successor = admissionEnterStep(definition, successor, workflowCorrectionTargetStep(definition, state.step))
		return []admissionModelState{successor}
	}
	successors := []admissionModelState{successor}
	if admissionDeclaresAction(definition, "record_design") {
		withoutDesign := successor
		withoutDesign.designStale = true
		successors[0].designStale = false
		successors = append(successors, withoutDesign)
	}
	if contractStep, err := workflowDefinitionContractStep(definition); err == nil {
		reset := admissionEnterStep(definition, state, contractStep)
		reset.contracts, reset.stale, reset.verdict, reset.observed = 0, "", "", false
		reset.mandate = ""
		successors = append(successors, reset)
	}
	return successors
}

// admissionAgentMoves models observation records and the native verify route
// outside workflow_action. Verification still needs an admitted evidence bind.
func admissionAgentMoves(definition WorkflowDefinition, state admissionModelState) []admissionModelState {
	if state.done {
		return nil
	}
	var successors []admissionModelState
	if !state.artifact || !state.observed {
		observed := state
		observed.artifact, observed.observed = true, true
		successors = append(successors, observed)
	}
	// worktree_verify is outside workflow_action. A green run can prove the
	// epoch only when it follows the start and bind_evidence is admitted.
	if state.started && !state.proof && workflowRefineProofGateActive(definition, state.step) {
		if workflowAdmit(definition, admissionWorkflowState(definition, state), "bind_evidence").Admitted {
			verified := state
			verified.proof = true
			successors = append(successors, verified)
		}
	}
	return successors
}

func admissionAgentMoveName(before, after admissionModelState) string {
	if before.proof != after.proof {
		return "worktree_verify+bind_evidence"
	}
	return "observation_record"
}

// admissionEnvironmentMoves are the moves no agent controls: a law revision
// or a Domain registry change makes the active contract's pin stale.
func admissionEnvironmentMoves(state admissionModelState) []admissionModelState {
	if state.done || state.contracts != 1 || state.stale != "" {
		return nil
	}
	law, registry := state, state
	law.stale, registry.stale = "law", "registry"
	return []admissionModelState{law, registry}
}

func admissionTerminalStep(definition WorkflowDefinition, step string) bool {
	return containsString(definition.StepGraph.TerminalSteps, step)
}

// admissionStepIndex orders the steps along the definition's step list.
func admissionStepIndex(definition WorkflowDefinition, stepID string) int {
	for i, step := range definition.StepGraph.Steps {
		if step.ID == stepID {
			return i
		}
	}
	return -1
}

// wellFormedAdmissionStates enumerates the well-formed nonterminal abstract
// states of one definition version: every step crossed with the dimension
// values the loader can fold there. A step at or before the contract step
// holds no contract; a later step holds one, or the duplicated projection.
// Staleness, design currency, and verdicts exist only under a contract, and
// verdicts only from the first verdict step on. Attempts exist on a step
// that dispatches or starts work. The review debt exists only on a
// correction workflow's review steps, and a ready review implies the debt.
func wellFormedAdmissionStates(definition WorkflowDefinition) []admissionModelState {
	contractIndex := -1
	if contractStep, err := workflowDefinitionContractStep(definition); err == nil {
		contractIndex = admissionStepIndex(definition, contractStep)
	}
	verdictIndex := -1
	for i, step := range definition.StepGraph.Steps {
		if containsString(step.Actions, "record_verdict") {
			verdictIndex = i
			break
		}
	}
	design := admissionDeclaresAction(definition, "record_design")
	correction := workflowCorrectionWorkflow(definition)
	var states []admissionModelState
	for i, step := range definition.StepGraph.Steps {
		contracts := []int64{0}
		if contractIndex < 0 {
			contracts = []int64{0, 1, 2}
		} else if i > contractIndex {
			contracts = []int64{1, 2}
		}
		attempts, walls := []string{""}, []int64{0}
		starts := containsString(step.Actions, "dispatch_worker")
		for _, actionID := range step.Actions {
			if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionFenced {
				starts = true
			}
		}
		if starts {
			attempts = []string{"", "completed", "failed", "failure_recorded", "rejected"}
			walls = []int64{0, workflowCorrectionAttemptLimit}
		}
		type debtShape struct {
			debt  WorkflowReviewDebt
			ready string
		}
		debts := []debtShape{{ReviewDebtNone, ""}}
		if correction && workflowPostRejectionReviewStep(definition, step.ID) {
			debts = append(debts, debtShape{ReviewDebtOutstanding, ""}, debtShape{ReviewDebtOutstanding, "ship"}, debtShape{ReviewDebtOutstanding, "absent"}, debtShape{ReviewDebtOutstanding, "no_ship"})
		}
		for _, count := range contracts {
			stales, designs, verdicts := []string{""}, []bool{false}, []string{""}
			if count > 0 {
				stales = []string{"", "law", "registry"}
				if design {
					designs = []bool{false, true}
				}
				if verdictIndex >= 0 && i >= verdictIndex {
					verdicts = []string{"", "ok", "bad"}
				}
			}
			for _, stale := range stales {
				for _, designStale := range designs {
					for _, verdict := range verdicts {
						for _, attempt := range attempts {
							for _, wall := range walls {
								for _, shape := range debts {
									for _, observation := range [][2]bool{{false, false}, {true, false}, {true, true}} {
										states = append(states, admissionModelState{
											step: step.ID, debt: shape.debt, ready: shape.ready,
											attempt: attempt, failed: wall, contracts: count, stale: stale,
											designStale: designStale, verdict: verdict,
											artifact: observation[0], observed: observation[1],
										})
									}
								}
							}
						}
					}
				}
			}
		}
	}
	var withEvidence []admissionModelState
	for _, state := range states {
		starts := []bool{false}
		if workflowStepIsDeliveryGate(workflowStep(definition, state.step)) || state.attempt != "" {
			// The gate is entered from its delivery-bearing predecessor; a
			// dispatched attempt also records a fenced action start.
			starts = []bool{true}
		} else {
			for _, action := range workflowStep(definition, state.step).Actions {
				if mode, ok := workflowActionExecutionMode(definition, action); ok && mode == ActionFenced {
					starts = []bool{false, true}
					break
				}
			}
		}
		mandates := []string{""}
		if state.contracts == 1 {
			mandates = []string{"", "missing", "bound"}
		}
		for _, started := range starts {
			proofs := []bool{false}
			if started && workflowRefineProofGateActive(definition, state.step) {
				proofs = []bool{false, true}
			}
			for _, proof := range proofs {
				for _, mandate := range mandates {
					next := state
					next.started, next.proof, next.mandate = started, proof, mandate
					withEvidence = append(withEvidence, next)
				}
			}
		}
	}
	return withEvidence
}

// admissionExits returns the admitted actions with a successor that leaves
// the state. A hold action whose every successor equals the state is no
// exit (CD-0201 D2). The agent's own observation record counts: it is a
// route the agent always holds, and it changes the folded state.
func admissionExits(definition WorkflowDefinition, state admissionModelState) []string {
	var exits []string
	for _, successor := range admissionAgentMoves(definition, state) {
		exits = append(exits, admissionAgentMoveName(state, successor))
	}
	for _, actionID := range admissionModelMoves(definition, state) {
		if admissionContinuityAction(actionID) {
			continue
		}
		for _, successor := range admissionSuccessors(definition, state, actionID) {
			if successor != state {
				exits = append(exits, actionID)
				break
			}
		}
	}
	return exits
}

// wellFormedAdmissionExitWitnesses walks one definition version's well-formed
// nonterminal abstract states and returns a witness line for each state that
// admits no exit.
func wellFormedAdmissionExitWitnesses(definition WorkflowDefinition) []string {
	var witnesses []string
	for _, state := range wellFormedAdmissionStates(definition) {
		if len(admissionExits(definition, state)) == 0 {
			witnesses = append(witnesses, fmt.Sprintf("well-formed nonterminal state %s admits no exit; admitted: %s",
				state, strings.Join(admissionModelMoves(definition, state), ", ")))
		}
	}
	return witnesses
}

// TestWellFormedAdmissionStateHasNonContinuityExit proves liveness clause
// (a) over every registered definition version: every well-formed nonterminal
// abstract state admits at least one non-continuity action that leaves it.
func TestWellFormedAdmissionStateHasNonContinuityExit(t *testing.T) {
	definitions := builtinWorkflowDefinitionsWithHistory()
	if len(definitions) == 0 {
		t.Fatal("no built-in workflow definitions are registered")
	}
	for _, definition := range definitions {
		for _, witness := range wellFormedAdmissionExitWitnesses(definition) {
			t.Errorf("%s v%d: %s", definition.Ref, definition.Version, witness)
		}
	}
}

// TestWellFormedExitCheckNamesSeededStrandedState proves the seeded-strand
// half of the exit check: a definition whose step declares only hold actions
// produces a witness that names the stranded state, so the check fails rather
// than assumes.
func TestWellFormedExitCheckNamesSeededStrandedState(t *testing.T) {
	builtins := builtinWorkflowDefinitionsWithHistory()
	if len(builtins) == 0 {
		t.Fatal("no built-in workflow definitions are registered")
	}
	seeded := cloneWorkflowDefinition(builtins[0])
	seeded.Ref = "workflow.seed_missing_exit"
	seeded.Version = 1
	for i := range seeded.StepGraph.Steps {
		if seeded.StepGraph.Steps[i].ID == seeded.StepGraph.StartStep {
			seeded.StepGraph.Steps[i].Actions = []string{"checkpoint_context"}
		}
	}
	witnesses := wellFormedAdmissionExitWitnesses(seeded)
	if len(witnesses) == 0 {
		t.Fatal("the seeded hold-only step produced no stranded witness")
	}
	if !strings.Contains(witnesses[0], seeded.StepGraph.StartStep) {
		t.Fatalf("witness does not name the stranded step: %s", witnesses[0])
	}
}

// TestReachableAdmissionStateReachesTerminal proves liveness clause (b) over
// every registered definition version: from every model-reachable nonterminal
// state, the completed instance is reachable through admitted actions and the
// agent's own observation record. The witness names the definition, the
// stranded state, the path that reached it, and the admitted moves the model
// exhausted.
func TestReachableAdmissionStateReachesTerminal(t *testing.T) {
	definitions := builtinWorkflowDefinitionsWithHistory()
	if len(definitions) == 0 {
		t.Fatal("no built-in workflow definitions are registered")
	}
	for _, definition := range definitions {
		for _, witness := range admissionStrandedWitnesses(definition) {
			t.Errorf("%s v%d: %s", definition.Ref, definition.Version, witness)
		}
	}
}

// TestReachableCheckNamesSeededStrandedState proves the reachable check
// fails: a definition whose terminal step loses its completion action
// strands every reachable state at that step.
func TestReachableCheckNamesSeededStrandedState(t *testing.T) {
	seeded := cloneWorkflowDefinition(mustBuiltinDefinition(t, "workflow.implementation").Definition)
	seeded.Ref = "workflow.seed_missing_completion"
	seeded.Version = 1
	for i := range seeded.StepGraph.Steps {
		if admissionTerminalStep(seeded, seeded.StepGraph.Steps[i].ID) {
			seeded.StepGraph.Steps[i].Actions = []string{"checkpoint_context"}
		}
	}
	witnesses := admissionStrandedWitnesses(seeded)
	if len(witnesses) == 0 {
		t.Fatal("the seeded completion-free terminal step produced no stranded witness")
	}
	// Every state upstream of the seeded terminal step strands with it, so
	// the check must name the terminal step among its witnesses.
	terminal := fmt.Sprintf("state (step %q", seeded.StepGraph.TerminalSteps[0])
	for _, witness := range witnesses {
		if strings.Contains(witness, terminal) {
			return
		}
	}
	t.Fatalf("no witness names the stranded terminal step; first witness: %s", witnesses[0])
}

func mustBuiltinDefinition(t *testing.T, ref string) RegisteredDefinition {
	t.Helper()
	definition, err := BuiltinWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

// admissionStrandedWitnesses walks every reachable state of one definition
// version and returns a witness for each nonterminal state from which no
// path reaches the completed instance.
func admissionStrandedWitnesses(definition WorkflowDefinition) []string {
	reachable, parents := admissionReachableStates(definition, admissionModelStart(definition))
	live := admissionLiveStates(definition, reachable)
	var witnesses []string
	for _, state := range reachable {
		if state.done || live[state] {
			continue
		}
		witnesses = append(witnesses, fmt.Sprintf("reachable nonterminal state %s reaches no completed instance; admitted moves: %s; path: %s",
			state, strings.Join(admissionModelMoves(definition, state), ", "), admissionModelPath(parents, state)))
	}
	return witnesses
}

// admissionModelEdge records how the reachable search first reached a state.
type admissionModelEdge struct {
	from   admissionModelState
	action string
}

// admissionReachableStates is the breadth-first search over admitted moves,
// the agent's observation record, and the environment's pin staleness, from
// the start state. It returns the reachable states in discovery order and
// the first edge into each.
func admissionReachableStates(definition WorkflowDefinition, start admissionModelState) ([]admissionModelState, map[admissionModelState]admissionModelEdge) {
	parents := map[admissionModelState]admissionModelEdge{}
	seen := map[admissionModelState]bool{start: true}
	reachable := []admissionModelState{start}
	for i := 0; i < len(reachable); i++ {
		state := reachable[i]
		visit := func(action string, successor admissionModelState) {
			if !seen[successor] {
				seen[successor] = true
				parents[successor] = admissionModelEdge{from: state, action: action}
				reachable = append(reachable, successor)
			}
		}
		for _, actionID := range admissionModelMoves(definition, state) {
			for _, successor := range admissionSuccessors(definition, state, actionID) {
				visit(actionID, successor)
			}
		}
		for _, successor := range admissionAgentMoves(definition, state) {
			visit(admissionAgentMoveName(state, successor), successor)
		}
		for _, successor := range admissionEnvironmentMoves(state) {
			visit("environment:stale_pin", successor)
		}
	}
	return reachable, parents
}

// admissionLiveStates is the backward fixpoint over the reachable graph: a
// state is live when it is the completed instance, or when an admitted move
// or the agent's observation record has a live successor. Environment moves
// never make a state live.
func admissionLiveStates(definition WorkflowDefinition, reachable []admissionModelState) map[admissionModelState]bool {
	live := map[admissionModelState]bool{}
	for _, state := range reachable {
		if state.done {
			live[state] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, state := range reachable {
			if live[state] {
				continue
			}
			successors := admissionAgentMoves(definition, state)
			for _, actionID := range admissionModelMoves(definition, state) {
				successors = append(successors, admissionSuccessors(definition, state, actionID)...)
			}
			for _, successor := range successors {
				if live[successor] {
					live[state], changed = true, true
					break
				}
			}
		}
	}
	return live
}

// admissionModelPath renders the first path the reachable search took into
// a state.
func admissionModelPath(parents map[admissionModelState]admissionModelEdge, state admissionModelState) string {
	var actions []string
	for {
		edge, ok := parents[state]
		if !ok {
			break
		}
		actions = append(actions, edge.action)
		state = edge.from
	}
	for i, j := 0, len(actions)-1; i < j; i, j = i+1, j-1 {
		actions[i], actions[j] = actions[j], actions[i]
	}
	if len(actions) == 0 {
		return "start"
	}
	return strings.Join(actions, " -> ")
}

// admissionSuccessor is the primary successor of one action: the first
// member of admissionSuccessors, the completed or ship outcome of a
// dispatch, the ok verdict, and the supersession that carries a design
// record. The conformance replay names the outcome it performs.
func admissionSuccessor(definition WorkflowDefinition, state admissionModelState, actionID string) admissionModelState {
	return admissionSuccessors(definition, state, actionID)[0]
}

// TestContractStepAdvancesOnlyThroughItsApproval pins the version-1 research
// strand: frame_research is advance-moded beside approve_contract on the
// frame step, so admitting it with no active contract would move the work
// past the only step that binds one. The engine refuses that advance and
// still admits the approval behind its operator question (CD-0203 D1).
func TestContractStepAdvancesOnlyThroughItsApproval(t *testing.T) {
	t.Parallel()
	var definition WorkflowDefinition
	for _, candidate := range builtinWorkflowDefinitionsWithHistory() {
		if candidate.Ref == "workflow.research" && candidate.Version == 1 {
			definition = candidate
		}
	}
	if definition.Ref == "" {
		t.Fatal("workflow.research v1 is not registered")
	}
	contractStep, err := workflowDefinitionContractStep(definition)
	if err != nil {
		t.Fatal(err)
	}
	if !workflowActionAdvancesStep(definition, "frame_research") {
		t.Fatal("research v1 frame_research is not advance-moded; the regression no longer exercises the strand")
	}
	state := WorkflowAdmissionState{Step: contractStep, Lifecycle: "in_progress", InstanceState: "active"}
	decision := workflowAdmit(definition, state, "frame_research")
	var failure *Failure
	if decision.Admitted || !failureAs(decision.Failure, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("contractless frame_research at %q = %+v, want the illegal lifecycle refusal", contractStep, decision)
	}
	if approval := workflowAdmit(definition, state, "approve_contract"); approval.Failure != nil {
		t.Fatalf("approve_contract at %q refused: %v", contractStep, approval.Failure)
	}
	state.ActiveContracts = 1
	if bound := workflowAdmit(definition, state, "frame_research"); bound.Failure != nil {
		t.Fatalf("frame_research with an active contract refused: %v", bound.Failure)
	}
}
