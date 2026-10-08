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

func TestAdmissionWorkflowStateKeepsArtifactStaleBit(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitionsWithHistory() {
		for _, step := range definition.StepGraph.Steps {
			for _, stale := range []bool{false, true} {
				node := admissionModelState{step: step.ID, contracts: 1, artifactStale: stale}
				if got := admissionWorkflowState(definition, node).ArtifactStale; got != stale {
					t.Fatalf("%s v%d step %s changed artifact stale %v to %v", definition.Ref, definition.Version, step.ID, stale, got)
				}
			}
		}
	}
}

func workflowCapabilityClassProduces(class string) bool {
	return containsString(workflowProducingCapabilityClasses, class)
}

func workflowStepJudgesStaleArtifact(definition WorkflowDefinition, stepID string) bool {
	return len(workflowStalenessSpanTargets(definition, stepID)) != 0
}

// admissionModelState is one node of the abstract admission state space.
type admissionModelState struct {
	step string
	// debt and ready are the post-rejection review debt and the typed
	// verdict of the ready review: "", "ship", "absent", or "no_ship".
	debt  WorkflowReviewDebt
	ready string
	// attempt is the latest worker attempt since the step's latest start:
	// "", "completed", "failed", "failure_recorded", or "rejected".
	// dispatched reports whether a dispatch happened since the step's latest
	// pass boundary — the dimension the contract-correction route reads —
	// because a held local acceptance clears the attempt but not the pass's
	// dispatch (CD-0205 D3). A completed attempt refreshes the artifact only
	// when its capability produces artifacts (attemptProduces) and its
	// dispatch follows the latest stale cause (attemptFresh, CD-0209 D4).
	attempt         string
	dispatched      bool
	attemptProduces bool
	attemptFresh    bool
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
	// artifactStale mirrors the artifact-staleness fold (CD-0201 D5): a
	// bad verdict or a successor contract made the artifact the evaluator
	// judges stale, and only fresh production at the declared route target
	// clears it. While set, record_verdict admits only a non-ok verdict and
	// confirm_premise refuses.
	artifactStale bool
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
	// jobs is the worker-job facet (CD-0205): "" no recorded revision;
	// "required" a recorded latest revision unsatisfied; "satisfied" every
	// recorded latest revision satisfied. integration is the qualifying
	// verify coverage of every required Project after the required job
	// acceptances and the phase start — the facet the delivery admission
	// reads beside the population. jobsUnsatisfied and jobsScope are the
	// deterministic keys the folded delivery state carries, so the
	// conformance lift compares them verbatim.
	jobs            string
	integration     bool
	jobsUnsatisfied string
	jobsScope       string
	// done is the completed instance with the closed work lifecycle.
	done bool
}

func (s admissionModelState) String() string {
	return fmt.Sprintf("(step %q debt %q ready %q attempt %q dispatched %v producer %v freshOrigin %v failed %d contracts %d stale %q designStale %v artifactStale %v verdict %q artifact %v observed %v started %v proof %v mandate %q jobs %q integration %v done %v)",
		s.step, s.debt, s.ready, s.attempt, s.dispatched, s.attemptProduces, s.attemptFresh, s.failed, s.contracts, s.stale, s.designStale, s.artifactStale, s.verdict, s.artifact, s.observed, s.started, s.proof, s.mandate, s.jobs, s.integration, s.done)
}

func admissionModelStart(definition WorkflowDefinition) admissionModelState {
	return admissionModelState{step: definition.StepGraph.StartStep, debt: ReviewDebtNone}
}

// admissionContinuityAction names the continuity actions: they record session
// context and never move the work, whatever mode a historical pin declares.
func admissionContinuityAction(actionID string) bool {
	return actionID == "checkpoint_context" || actionID == "cross_context_boundary"
}

// admissionLocalJobAcceptStep reports whether an accept at the step may be
// local acceptance of one worker job (CD-0205): the pin carries the worker-job
// lifecycle and the step declares record_worker_job.
func admissionLocalJobAcceptStep(definition WorkflowDefinition, step string) bool {
	return workflowWorkerJobsActive(definition) && stepDeclaresAction(definition, step, "record_worker_job")
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
		(workflowUnhealthyVerdictRouteTarget(definition, state.step) != "" && !admissionTerminalStep(definition, state.step)) ||
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
	return (state.attempt == "" && !state.dispatched) || state.attempt == "failed" || state.attempt == "rejected"
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
	if stepDeclaresAction(definition, state.step, "record_verdict") || workflowUnhealthyVerdictRouteTarget(definition, state.step) == "" || state.contracts == 0 || (state.verdict == "ok" && !state.artifactStale) {
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
	refinement := workflowRefinementStepID(definition) != ""
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
	delivery := workflowDeliveryAdmission{Started: state.started, ProofRequired: workflowRefineProofGateActive(definition, state.step)}
	if workflowWorkerJobsActive(definition) && state.jobs != "" {
		// The CD-0205 job facet the one delivery admission reads: the
		// required revisions' satisfaction and the qualifying integration
		// coverage, folded from the same dimensions the model carries.
		delivery.JobsRecorded = true
		delivery.JobsUnsatisfiedKeys = state.jobsUnsatisfied
		delivery.JobsScopeKeys = state.jobsScope
		delivery.JobsIntegrated = state.jobs == "satisfied" && state.integration
	}
	folded := WorkflowAdmissionState{
		Step:                        state.step,
		Lifecycle:                   lifecycle,
		InstanceState:               instance,
		RefinementWorkflow:          refinement,
		ReviewStep:                  reviewStep,
		ActiveContracts:             state.contracts,
		LawPinStale:                 staleErr != nil && workflowContractRecoveryStaleness(staleErr, admissionModelWorkID),
		LawPinStaleError:            staleErr,
		LawPinSelfError:             admissionModelStaleError(state.stale, true),
		DesignStale:                 state.designStale,
		ArtifactStale:               state.artifactStale,
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
		Delivery:                    delivery,
	}
	if (state.attempt == "failed" || state.attempt == "failure_recorded") && stepDeclaresAction(definition, state.step, "dispatch_worker") {
		folded.FailedWorkerRetry = &WorkflowRetryApprovalBinding{FailedAttemptID: "attempt:model", FailedAttemptEpoch: 1}
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
	if refinement && reviewStep {
		folded.ReviewDebt = state.debt
		if state.debt == ReviewDebtOutstanding {
			folded.ReadyReviewAttemptID = admissionReadyAttemptID(state)
			folded.ReadyReviewVerdict = admissionReadyVerdict(state)
			folded.ReadyReviewSettles = state.ready != "" && workflowReviewSettlesDebt(folded.ReadyReviewVerdict)
		}
	}
	gate := workflowStepIsDeliveryGate(workflowStep(definition, state.step))
	if gate {
		folded.CorrectionRequestRecovery = folded.ReviewDebt == ReviewDebtOutstanding
		if !folded.CorrectionRequestRecovery {
			folded.CorrectionRequestMissing = workflowCorrectionMissingGateReview
		}
	} else if workflowUnhealthyVerdictRouteTarget(definition, state.step) != "" {
		// The evaluator-step correction request mirrors the fold's verdict
		// route: a declared unhealthy_verdict route — the step's own,
		// which every admitted late evaluator context, terminal or
		// premise-question, carries (CD-0204) — an
		// active contract, and a current non-ok verdict, where an ok
		// verdict the staleness frontier predates counts as non-ok
		// (CD-0201 D5/D6), so the declared route carries the continuation
		// after a successor contract. The accepted-delivery prerequisite
		// is history the model abstracts: a well-formed history that
		// recorded the verdict delivered and accepted the result first.
		folded.CorrectionRequestRecovery = state.contracts == 1 && state.verdict == "bad"
		if !folded.CorrectionRequestRecovery && state.contracts == 1 {
			folded.CorrectionRequestMissing = workflowCorrectionMissingVerdict
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
	// Integration persists across the same-pass advance the loader still
	// reads from the workflow's bound evidence, so the model's
	// fold-equivalent must carry it forward.
	if !workflowStepIsDeliveryGate(workflowStep(definition, step)) {
		state.started = false
	}
	state.proof = false
	state.step, state.attempt, state.failed, state.dispatched = step, "", 0, false
	state.attemptProduces, state.attemptFresh = false, false
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
		next.started, next.proof, next.integration = true, false, false
		next.dispatched = true
		completed := next
		completed.attempt = "completed"
		completed.attemptFresh = true
		failed := next
		failed.attempt = "failed"
		failed.attemptProduces, failed.attemptFresh = false, false
		if failed.failed < workflowCorrectionAttemptLimit {
			failed.failed++
		}
		if next.debt != ReviewDebtOutstanding {
			successors := []admissionModelState{}
			for _, produces := range admissionDispatchProductionClasses(definition, state.step) {
				candidate := completed
				candidate.attemptProduces = produces
				successors = append(successors, candidate)
			}
			return append(successors, failed)
		}
		// A fresh review dispatch supersedes any ready review: the loader
		// names the latest completed review whose acceptance no action has
		// dispositioned, so the new completion replaces the standing one.
		ship, noShip := completed, completed
		ship.attemptProduces, noShip.attemptProduces = false, false
		ship.ready, noShip.ready = "ship", "no_ship"
		return []admissionModelState{ship, noShip, failed}
	case "record_worker_job":
		// CD-0205 D1/D2: the delivery fold reads each job's latest
		// revision only, so the recording always leaves one unsatisfied
		// required revision — a re-recorded job reopens the required
		// population an earlier acceptance satisfied, and the verify
		// coverage that predates the reopened population's acceptance no
		// longer qualifies. The recording holds the step and satisfies
		// nothing.
		next.jobs = "required"
		next.jobsUnsatisfied = admissionModelJobKeys("required")
		next.jobsScope = admissionModelJobScope("required")
		next.integration = false
		return []admissionModelState{next}
	case "accept_worker_result":
		next.attempt, next.failed = "", 0
		if state.attempt == "completed" && state.attemptProduces && state.attemptFresh && admissionProducesAtRouteTarget(definition, state.step, "accept_worker_result") {
			// Dispatch admission includes evaluator capabilities. Acceptance
			// therefore needs the producing class and fresh causal origin,
			// not merely a completed attempt at the route target.
			next.artifactStale = false
		}
		// An acceptance does not invalidate the qualifying integration
		// coverage in the model's fold-equivalent: the loader reads every
		// verification binding after coverageSeq and counts each qualifying
		// run, so the model's single bool must keep the bound evidence in
		// scope across the same-pass accept (CD-0205 D3).
		next.attemptProduces, next.attemptFresh = false, false
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
		next.attemptProduces, next.attemptFresh = false, false
	case "reject_worker_result":
		next.attempt = "rejected"
		if workflowPostRejectionReviewStep(definition, state.step) {
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
		if next.artifactStale {
			// The artifact-staleness guard: only a non-ok verdict records
			// while the artifact a declared route's span holds is stale,
			// so the ok successor is not admitted (CD-0201 D5). The bad
			// successor keeps the declared route open. A step that
			// resolves no route evaluates nothing, so its verdicts fold
			// unstale exactly as the loader does.
			bad := next
			bad.verdict, bad.observed = "bad", false
			bad.attemptFresh = false
			return []admissionModelState{bad}
		}
		ok, bad := next, next
		ok.verdict, bad.verdict = "ok", "bad"
		ok.observed, bad.observed = false, false
		bad.artifactStale = true
		bad.attemptFresh = false
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
		return []admissionModelState{admissionEnterStep(definition, next, workflowCorrectionReturnTarget(definition, state.step))}
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
			next.dispatched = false
			next.attemptProduces, next.attemptFresh = false, false
			next.started, next.proof = true, false
			next.integration = false
		}
	}
	// Direct artifact actions prove production by their own event authority,
	// independently of advancement. Worker acceptance uses its origin above.
	if containsString(workflowStepArtifactActions(definition, state.step), actionID) && admissionProducesAtRouteTarget(definition, state.step, actionID) {
		next.artifactStale = false
	}
	if advance {
		if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
			advanced := admissionEnterStep(definition, next, workflowNextStep(definition, state.step))
			if actionID == "accept_worker_result" && admissionLocalJobAcceptStep(definition, state.step) {
				// CD-0205: on a job-capable pin the accept of a job-bound
				// attempt is local acceptance and holds the step. The held
				// successor keeps the same-step failed wall the failed job
				// opened (CD-0205 D4 refining CD-0164 D2) and moves the job
				// facet: the accepted revision is satisfied, while another
				// required revision may remain unsatisfied. The accept
				// advances only for an attempt without a job, or — where the
				// step exits through delivery — only when the delivery
				// derivation admits the assertion it carries.
				held := next
				held.failed = state.failed
				if state.jobs == "required" {
					heldStillRequired := held
					heldStillRequired.jobs = "required"
					held.jobs = "satisfied"
					held.jobsUnsatisfied = ""
					if !workflowAcceptDeliveryAdmissionActive(definition, state.step) || workflowAdmitDelivery(admissionWorkflowState(definition, state), WorkflowAdmissionDecision{}).Failure == nil {
						return []admissionModelState{heldStillRequired, held, advanced}
					}
					return []admissionModelState{heldStillRequired, held}
				}
				if !workflowAcceptDeliveryAdmissionActive(definition, state.step) || workflowAdmitDelivery(admissionWorkflowState(definition, state), WorkflowAdmissionDecision{}).Failure == nil {
					successors := []admissionModelState{held}
					// CD-0205 D5: when the delivery derivation admits the assertion
					// the accept carries, the model's fold-equivalent must lift
					// both the held-satisfied and the advancing successor, the
					// way the loader reads the integrated delivery on either path.
					successors = append(successors, advanced)
					return successors
				}
				return []admissionModelState{held}
			}
			next = advanced
		}
	}
	return []admissionModelState{next}
}

// The authored dispatch join admits both producing and evaluator classes.
// Classes with the same production behavior share one abstract successor.
func admissionDispatchProductionClasses(definition WorkflowDefinition, stepID string) []bool {
	classes := map[bool]bool{}
	for capability := range laneStepDispatchKinds {
		if LaneStepDispatchAllowed(capability, workflowStep(definition, stepID).Kind) {
			classes[workflowCapabilityClassProduces(capability)] = true
		}
	}
	var result []bool
	for _, produces := range []bool{true, false} {
		if classes[produces] {
			result = append(result, produces)
		}
	}
	if len(result) == 0 {
		// An unidentified historical completion carries no production proof.
		return []bool{false}
	}
	return result
}

// admissionProducesAtRouteTarget reports whether completing actionID at
// stepID is actual production of the artifact the declared routes' evaluator
// judges: the action positively produces (workflowStepArtifactActions, or the
// accepted result delivery at a step that dispatches workers) AND the step is
// a declared unhealthy_verdict route target — the producer the staleness
// causal frontier reads. Every registered definition's unhealthy routes name
// one shared target, so the single staleness bit stays exact. Evidence-only
// acceptance, starts, checkpoints, bindings, verdicts, and producing-shaped
// actions at non-target steps never produce.
func admissionProducesAtRouteTarget(definition WorkflowDefinition, stepID, actionID string) bool {
	if !admissionStepProducesArtifact(definition, stepID, actionID) {
		return false
	}
	for _, route := range workflowRecoveryRoutes(definition) {
		if route.Trigger == WorkflowRecoveryTriggerUnhealthyVerdict && route.Target == stepID {
			return true
		}
	}
	return false
}

// admissionStepProducesArtifact reports whether completing actionID at stepID
// is a producing completion of the step's own mechanism: one of the step's
// positively classified artifact actions (workflowStepArtifactActions), or
// the accepted result delivery at a step that dispatches workers. Starts,
// checkpoints, evidence bindings, verdicts, unrelated holds, and
// evidence-only acceptance never produce.
func admissionStepProducesArtifact(definition WorkflowDefinition, stepID, actionID string) bool {
	if containsString(workflowStepArtifactActions(definition, stepID), actionID) {
		return true
	}
	return actionID == "accept_worker_result" && stepDeclaresAction(definition, stepID, "dispatch_worker")
}

// admissionSupersedeSuccessors folds the contract supersession: a successor
// contract re-pins the law, starts a fresh verdict record, and makes the
// artifact the evaluator judges stale until it is re-produced (CD-0201 D6);
// at the pinned complete step it returns the instance to the correction
// target; without a successor the instance returns to the contract step with
// no contract.
func admissionSupersedeSuccessors(definition WorkflowDefinition, state admissionModelState) []admissionModelState {
	successor := state
	successor.contracts, successor.stale, successor.verdict, successor.observed = 1, "", "", false
	successor.artifactStale = true
	successor.attemptFresh = false
	if target := workflowDisprovedPremiseAtCompleteRouteTarget(definition, state.step); target != "" {
		successor = admissionEnterStep(definition, successor, target)
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
		// The supersession stands as a staleness cause with or without a
		// successor (CD-0201 D6): the fold reads the event sequence, not
		// the projection, so only fresh production at the route target —
		// which every walk back to an evaluator step passes — clears it.
		reset.mandate, reset.artifactStale = "", true
		reset.attemptFresh = false
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
	// The same route supplies the integration coverage the job facet reads
	// (CD-0205 D3): a green verify run after the required job acceptances and
	// the phase start, bound through an admitted evidence bind, integrates
	// the required Projects.
	if workflowWorkerJobsActive(definition) && state.started && state.jobs == "satisfied" && !state.integration {
		if workflowAdmit(definition, admissionWorkflowState(definition, state), "bind_evidence").Admitted {
			integrated := state
			integrated.integration = true
			successors = append(successors, integrated)
		}
	}
	return successors
}

func admissionAgentMoveName(before, after admissionModelState) string {
	if before.proof != after.proof || before.integration != after.integration {
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
// Staleness, design currency, verdicts, and artifact staleness exist only
// under a contract, verdicts and artifact staleness only from the first
// verdict step on — a staleness cause is a bad verdict or a successor
// contract, and no evaluator sits before that step. Attempts exist on a step
// that dispatches or starts work. The review debt exists only on a
// definition that declares the refinement shape's review steps, and a ready
// review implies the debt.
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
	// CD-0209 D3 replaces the family whitelist with the typed route table:
	// every family's refinement step (the step declaring start_refine) is the
	// source that decides whether post-rejection review debt can exist. The
	// correction family filter (workflowCorrectionWorkflow) is gone.
	refinement := workflowRefinementStepID(definition) != ""
	// The worker-job facet exists only where a revision can be recorded
	// (CD-0205 D2), so the well-formed enumeration carries it exactly there:
	// no recorded revision, an unsatisfied required revision, and the fully
	// satisfied population the delivery admission reads.
	jobShapes := []string{""}
	if workflowWorkerJobsActive(definition) {
		jobShapes = append(jobShapes, "required", "satisfied")
	}
	var states []admissionModelState
	for i, step := range definition.StepGraph.Steps {
		stepJobShapes := jobShapes
		if !containsString(step.Actions, "record_worker_job") {
			stepJobShapes = []string{""}
		}
		contracts := []int64{0}
		if contractIndex < 0 {
			contracts = []int64{0, 1, 2}
		} else if i > contractIndex {
			contracts = []int64{1, 2}
		}
		walls := []int64{0}
		starts := containsString(step.Actions, "dispatch_worker")
		for _, actionID := range step.Actions {
			if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionFenced {
				starts = true
			}
		}
		if starts {
			walls = []int64{0, workflowCorrectionAttemptLimit}
		}
		// attemptDispatched pairs each attempt value with whether a dispatch
		// happened since the step's latest pass boundary. Only the empty
		// attempt splits: a live attempt implies its own dispatch, and a
		// held local acceptance clears the attempt while the pass keeps its
		// dispatch (CD-0205 D3).
		type attemptDispatched struct {
			attempt    string
			dispatched bool
		}
		attemptDispatches := []attemptDispatched{{"", false}}
		if starts {
			attemptDispatches = []attemptDispatched{{"", false}, {"", true}, {"completed", true}, {"failed", true}, {"failure_recorded", true}, {"rejected", true}}
		}
		type debtShape struct {
			debt  WorkflowReviewDebt
			ready string
		}
		debts := []debtShape{{ReviewDebtNone, ""}}
		if refinement && workflowPostRejectionReviewStep(definition, step.ID) {
			debts = append(debts, debtShape{ReviewDebtOutstanding, ""}, debtShape{ReviewDebtOutstanding, "ship"}, debtShape{ReviewDebtOutstanding, "absent"}, debtShape{ReviewDebtOutstanding, "no_ship"})
		}
		for _, count := range contracts {
			stales, designs, verdicts, artifacts := []string{""}, []bool{false}, []string{""}, []bool{false}
			if count > 0 {
				stales = []string{"", "law", "registry"}
				if design {
					designs = []bool{false, true}
				}
				if verdictIndex >= 0 && i >= verdictIndex {
					verdicts = []string{"", "ok", "bad"}
				}
				if workflowStepJudgesStaleArtifact(definition, step.ID) {
					artifacts = []bool{false, true}
				}
			}
			for _, stale := range stales {
				for _, designStale := range designs {
					for _, verdict := range verdicts {
						for _, artifactStale := range artifacts {
							for _, attemptDispatch := range attemptDispatches {
								for _, wall := range walls {
									for _, shape := range debts {
										for _, jobs := range stepJobShapes {
											for _, observation := range [][2]bool{{false, false}, {true, false}, {true, true}} {
												states = append(states, admissionModelState{
													step: step.ID, debt: shape.debt, ready: shape.ready,
													attempt: attemptDispatch.attempt, dispatched: attemptDispatch.dispatched,
													failed: wall, contracts: count, stale: stale,
													designStale: designStale, verdict: verdict, artifactStale: artifactStale,
													artifact: observation[0], observed: observation[1],
													jobs: jobs, jobsUnsatisfied: admissionModelJobKeys(jobs), jobsScope: admissionModelJobScope(jobs),
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
			integrations := []bool{false}
			if started && state.jobs == "satisfied" {
				integrations = []bool{false, true}
			}
			for _, proof := range proofs {
				for _, integration := range integrations {
					for _, mandate := range mandates {
						next := state
						next.started, next.proof, next.mandate, next.integration = started, proof, mandate, integration
						withEvidence = append(withEvidence, next)
					}
				}
			}
		}
	}
	var withOrigins []admissionModelState
	for _, state := range withEvidence {
		if state.attempt != "completed" {
			withOrigins = append(withOrigins, state)
			continue
		}
		for _, produces := range admissionDispatchProductionClasses(definition, state.step) {
			for _, fresh := range []bool{false, true} {
				next := state
				next.attemptProduces, next.attemptFresh = produces, fresh
				withOrigins = append(withOrigins, next)
			}
		}
	}
	return withOrigins
}

// admissionModelJobKeys and admissionModelJobScope derive the canonical
// deterministic keys the folded delivery state carries for one model job
// shape. Only emptiness decides, so a single synthetic revision and Project
// stand for the whole required population.
func admissionModelJobKeys(jobs string) string {
	if jobs == "required" {
		return "job:model|1"
	}
	return ""
}

func admissionModelJobScope(jobs string) string {
	if jobs != "" {
		return "p"
	}
	return ""
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

// admissionBadToOkWitnesses walks every well-formed state whose artifact is
// stale and returns a witness for each admitted path that records an ok
// verdict or confirms the premise without re-production at the declared route
// target — the bad-to-ok transition CD-0201 D5 forbids. With enforce false
// the walk seeds the unmirrored fold the trap ran under: the staleness bit is
// dropped before the model decides, so the check names exactly the witnesses
// the enforcement removes and a regression that loses the mirror cannot pass
// silently.
func admissionBadToOkWitnesses(definition WorkflowDefinition, enforce bool) []string {
	var witnesses []string
	for _, state := range wellFormedAdmissionStates(definition) {
		if !state.artifactStale || state.contracts == 0 || !workflowStepJudgesStaleArtifact(definition, state.step) {
			// Staleness bites only inside a declared route's
			// producer-to-evaluator span; elsewhere the fold reads it
			// unstale.
			continue
		}
		probe := state
		if !enforce {
			probe.artifactStale = false
		}
		if workflowAdmit(definition, admissionWorkflowState(definition, probe), "record_verdict").Admitted {
			for _, successor := range admissionSuccessors(definition, probe, "record_verdict") {
				if successor.verdict == "ok" && !successor.artifactStale && successor.step == state.step {
					witnesses = append(witnesses, fmt.Sprintf("stale state %s records an ok verdict without re-production", state))
				}
			}
		}
		if workflowAdmit(definition, admissionWorkflowState(definition, probe), "confirm_premise").Admitted && stepDeclaresAction(definition, state.step, "confirm_premise") {
			witnesses = append(witnesses, fmt.Sprintf("stale state %s confirms its premise past the verdict without re-production", state))
		}
	}
	return witnesses
}

// TestStaleArtifactStateAdmitsNoBadToOkWithoutReproduction proves the
// artifact-staleness safety property over every registered definition
// version: from no well-formed stale state does an admitted move record an ok
// verdict or step the premise past the verdict before the artifact is
// re-produced at the declared route target.
func TestStaleArtifactStateAdmitsNoBadToOkWithoutReproduction(t *testing.T) {
	definitions := builtinWorkflowDefinitionsWithHistory()
	if len(definitions) == 0 {
		t.Fatal("no built-in workflow definitions are registered")
	}
	for _, definition := range definitions {
		for _, witness := range admissionBadToOkWitnesses(definition, true) {
			t.Errorf("%s v%d: %s", definition.Ref, definition.Version, witness)
		}
	}
}

// TestStaleArtifactCheckNamesSeededWitness proves the safety check fails
// rather than assumes: with the staleness mirror dropped — the fold the
// CON-846 trap ran under — the same walk names a bad-to-ok witness, so the
// check detects a model or admission that loses the enforcement.
func TestStaleArtifactCheckNamesSeededWitness(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	witnesses := admissionBadToOkWitnesses(definition, false)
	if len(witnesses) == 0 {
		t.Fatal("the seeded unmirrored staleness produced no bad-to-ok witness")
	}
	for _, witness := range witnesses {
		if strings.Contains(witness, "ok verdict") {
			return
		}
	}
	t.Fatalf("no witness names the ok verdict; first witness: %s", witnesses[0])
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

func TestAdmissionModelProductionRequiresCapabilityAndFreshOrigin(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	for _, test := range []struct {
		name            string
		produces, fresh bool
		wantStale       bool
	}{
		{"unidentified-completion", false, false, true},
		{"fresh-evaluator-result", false, true, true},
		{"old-producing-dispatch", true, false, true},
		{"fresh-producing-result", true, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := admissionModelState{
				step: "execute", contracts: 1, artifactStale: true,
				attempt: "completed", attemptProduces: test.produces,
				attemptFresh: test.fresh, started: true, proof: true,
				debt: ReviewDebtNone,
			}
			if decision := workflowAdmit(definition, admissionWorkflowState(definition, state), "accept_worker_result"); !decision.Admitted {
				t.Fatalf("acceptance refused: %v", decision.Failure)
			}
			for _, next := range admissionSuccessors(definition, state, "accept_worker_result") {
				if next.artifactStale != test.wantStale {
					t.Fatalf("accepted result stale=%v, want %v: %s", next.artifactStale, test.wantStale, next)
				}
			}
		})
	}
}

func TestAdmissionModelDispatchKeepsEvaluatorAndProducerAlternatives(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.generic_one_off").Definition
	state := admissionModelState{step: "execute", contracts: 1, artifactStale: true, debt: ReviewDebtNone}
	producing, evaluating := false, false
	for _, next := range admissionSuccessors(definition, state, "dispatch_worker") {
		if next.attempt != "completed" {
			continue
		}
		if !next.attemptFresh || !next.artifactStale {
			t.Fatalf("dispatch changed staleness or omitted its fresh origin: %s", next)
		}
		producing = producing || next.attemptProduces
		evaluating = evaluating || !next.attemptProduces
	}
	if !producing || !evaluating {
		t.Fatal("external-effect dispatch lost an admitted capability alternative")
	}
	completed := state
	completed.attempt, completed.attemptProduces, completed.attemptFresh = "completed", true, true
	for _, revised := range admissionSupersedeSuccessors(definition, completed) {
		if revised.attemptFresh {
			t.Fatalf("contract revision preserved a pre-revision producing origin: %s", revised)
		}
	}
}
