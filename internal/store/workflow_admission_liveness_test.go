package store

// CD-0201 D2 liveness over every registered definition version (CD-0115 D2),
// split by the CD-0218 D1 family boundary into two properties:
//
// Retained families satisfy the liveness property: every
// well-formed nonterminal state has an exit, and every reachable state has a
// completion path. Seeded strands detect missing exits and completion actions.
//
// Archived families cannot lawfully satisfy completion liveness: archived
// public authoring is forbidden, so an archived state admits no public
// action. Their assertion is the positive refusal totality — every candidate
// action at every well-formed nonterminal state returns the archived-family
// KindInvalidOperation refusal — and a seeded non-archived clone proves the
// detector names an admitted action rather than passing vacuously.
//
// No state is skipped on either side. The model folds review debt, attempts,
// contracts, staleness, design, verdicts, artifacts, delivery, mandates, and
// jobs. Breaking notices and external conditions stay zero.

import (
	"fmt"
	"iter"
	"math/rand/v2"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

const admissionModelWorkID = "admission-model"

func TestAdmissionWorkflowStateKeepsArtifactStaleBit(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
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

func TestAdmissionConfirmationAuthorityRequiresOneContract(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.research").Definition
	for _, contracts := range []int64{0, 1, 2} {
		for _, engineering := range []bool{false, true} {
			state := admissionModelState{step: "conclude", contracts: contracts, engineering: engineering}
			want := WorkflowConfirmationAuthority("")
			if contracts == 1 {
				want = WorkflowConfirmationOperator
				if engineering {
					want = WorkflowConfirmationCoordinator
				}
			}
			if got := admissionWorkflowState(definition, state).ConfirmationAuthority; got != want {
				t.Errorf("%d active contracts, engineering=%v: confirmation authority=%q, want %q", contracts, engineering, got, want)
			}
		}
	}
}

func workflowCapabilityClassProduces(class string) bool {
	return slices.Contains(workflowProducingCapabilityClasses, class)
}

func workflowStepJudgesStaleArtifact(definition WorkflowDefinition, stepID string) bool {
	return len(workflowStalenessSpanTargets(definition, stepID)) != 0
}

// admissionModelState is one node of the abstract admission state space.
type admissionModelState struct {
	step   string
	review admissionModelReview
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
	// jobDebt reports an unresolved recorded job obligation: some failed
	// attempt was bound to a required revision and no acceptance since
	// discharged it (CD-0205 D4).
	jobDebt bool
	// unboundRejectionStep is the step whose unbound rejection stands
	// unconsumed. The model's reject_worker_result is the unbound rejection —
	// it opens no job debt — and only a materialized dispatch consumes it, so
	// hold acceptance and step entry keep the memory. The lift exposes it
	// only at the remembered step while that step is non-checkpoint.
	unboundRejectionStep string
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
	// sizeStop is the CD-0229 D6 stop: a review proved the larger change
	// bigger after its latest size record. Only the size rerun, a
	// revise_intent outside workflow_action, releases it.
	sizeStop bool
	// engineering is the explicit classification dimension for a
	// consequence_v1 contract whose declared decision fact is the empty set:
	// the model folds no pending operator question for engineering work,
	// whatever the step shape declares, and the conformance lift compares
	// that answer with the real fold. Absence of the dimension (false on
	// every legacy enumeration state) keeps the registered legacy
	// enumeration byte-for-byte in its prior behavior.
	engineering bool
	// done is the completed instance with the closed work lifecycle.
	done bool
}

func (s admissionModelState) String() string {
	return fmt.Sprintf("(step %q debt %q ready %q attempt %q dispatched %v producer %v freshOrigin %v jobDebt %v unboundRejectAt %q contracts %d stale %q designStale %v artifactStale %v verdict %q artifact %v observed %v started %v proof %v mandate %q jobs %q unsatisfied %q scope %q integration %v sizeStop %v engineering %v done %v review %+v)",
		s.step, s.debt, s.ready, s.attempt, s.dispatched, s.attemptProduces, s.attemptFresh, s.jobDebt, s.unboundRejectionStep, s.contracts, s.stale, s.designStale, s.artifactStale, s.verdict, s.artifact, s.observed, s.started, s.proof, s.mandate, s.jobs, s.jobsUnsatisfied, s.jobsScope, s.integration, s.sizeStop, s.engineering, s.done, s.review)
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
	var actions []string
	if step := workflowStep(definition, state.step); step != nil {
		actions = make([]string, 0, len(step.Actions)+len(admissionRecoveryActions))
		for _, actionID := range step.Actions {
			if !slices.Contains(actions, actionID) {
				actions = append(actions, actionID)
			}
		}
	}
	for _, actionID := range admissionRecoveryActions {
		if !slices.Contains(actions, actionID) {
			actions = append(actions, actionID)
		}
	}
	sort.Strings(actions)
	return actions
}

// admissionRecoveryActions are the off-step recovery actions every state's
// action universe carries.
var admissionRecoveryActions = []string{"supersede_contract", "record_verdict", "record_worker_failure", "reject_worker_result", "request_correction", "bind_evidence"}

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

func admissionLateCriterionAssociation(definition WorkflowDefinition, state admissionModelState) bool {
	// This dimension is derivable from the existing criterion state. Artifact
	// staleness neither opens the route nor prevents supported reassessment.
	return state.contracts == 1 && state.verdict != "ok" && workflowLateCriterionAssociationStep(definition, state.step)
}

// admissionContractCorrection folds the ordinary contract correction route.
func admissionContractCorrection(definition WorkflowDefinition, state admissionModelState) bool {
	step := workflowStep(definition, state.step)
	if step == nil {
		return false
	}
	checkpoint := slices.Contains(step.Actions, "complete") ||
		(workflowUnhealthyVerdictRouteTarget(definition, state.step) != "" && !admissionTerminalStep(definition, state.step)) ||
		(step.Kind == WorkflowStepHumanCheckpoint && slices.Contains(step.Actions, "confirm_premise") && !admissionTerminalStep(definition, state.step)) ||
		(step.Kind != WorkflowStepHumanCheckpoint && slices.Contains(step.Actions, "dispatch_worker") && !admissionTerminalStep(definition, state.step))
	switch {
	case !checkpoint:
		return false
	case slices.Contains(step.Actions, "complete"):
		return admissionCompleteStepCorrection(definition, state)
	case step.Kind == WorkflowStepHumanCheckpoint:
		return true
	case state.contracts > 1:
		return true
	case state.contracts != 1:
		return false
	}
	return !state.dispatched || state.attempt == "failure_recorded" || state.attempt == "rejected"
}

// admissionCompleteStepCorrection folds the CD-0172 complete-step route.
func admissionCompleteStepCorrection(definition WorkflowDefinition, state admissionModelState) bool {
	return workflowCompleteStepCorrectionStep(definition, state.step) && !state.done && state.contracts >= 1 &&
		(state.attempt == "" || state.attempt == "failure_recorded") && state.observed
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
		LateCriterionAssociation:    admissionLateCriterionAssociation(definition, state),
		LawPinStale:                 staleErr != nil && workflowContractRecoveryStaleness(staleErr, admissionModelWorkID),
		LawPinStaleError:            staleErr,
		LawPinSelfError:             admissionModelStaleError(state.stale, true),
		DesignStale:                 state.designStale,
		ArtifactStale:               state.artifactStale,
		AttemptState:                attemptState,
		LatestResultDisposition:     disposition,
		ReviewDebt:                  ReviewDebtNone,
		WorkerFailureRecovery:       admissionWorkerFailureRecovery(definition, state),
		CorrectionRecovery:          state.attempt == "completed" && stepDeclaresAction(definition, state.step, "dispatch_worker"),
		UnconsumedUnboundRejection:  workflowStepCarriesRejectionHold(definition, state.step) && state.unboundRejectionStep == state.step,
		DispatchHold:                admissionDispatchHold(definition, state),
		PendingOperatorDecision:     state.contracts == 1 && !state.engineering && workflowOperatorDecisionPending(definition, state.step) && state.artifact,
		ApprovalPolicy:              definition.ApprovalPolicy,
		CompleteStepCorrection:      admissionCompleteStepCorrection(definition, state),
		ContractCorrectionAvailable: admissionContractCorrection(definition, state),
		Delivery:                    delivery,
		Review:                      admissionReviewState(state),
	}
	// The classification dimension folds the confirmation authority beside
	// the question: an engineering contract records the coordinator's own
	// confirmation, a decision contract keeps the verified operator, and a
	// legacy pin keeps the empty authority the question machinery owns.
	if state.contracts == 1 && definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1 {
		folded.ConfirmationAuthority = WorkflowConfirmationOperator
		if state.engineering {
			folded.ConfirmationAuthority = WorkflowConfirmationCoordinator
		}
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
	// same step stays the exit. Engineering work keeps the same deliverable
	// gate — its confirmation replaces the question, not the prerequisites —
	// and folds the recorded-investigation prerequisite beside it: without
	// the observation the confirmation refuses, never admits behind an
	// absent prerequisite. An earlier folded failure keeps its place, exactly
	// as the loader preserves it. Evidence binding is outside the model.
	action, _ := workflowOperatorQuestionAction(definition, state.step)
	if (folded.PendingOperatorDecision || state.engineering) && action == "confirm_premise" && state.verdict == "" {
		folded.AcceptanceDeliverablesMissing = newFailure(KindMissingEvidence, "workflow_action", "premise confirmation requires a recorded workflow verdict", false, "record_verdict before confirming the premise")
	}
	// The engineering confirmation folds the investigation prerequisite at
	// every step that hosts the confirmation action — a human checkpoint's
	// record_verdict beside confirm_premise — exactly as the loader's
	// prerequisite fold does.
	if state.engineering && action != "" && folded.AcceptanceDeliverablesMissing == nil && !state.artifact {
		folded.AcceptanceDeliverablesMissing = newFailure(KindMissingEvidence, "workflow_action", "premise confirmation requires the recorded investigation artifact", false, "record the investigation observation before confirming")
	}
	if refinement && reviewStep {
		folded.ReviewDebt = state.debt
		if state.debt == ReviewDebtOutstanding {
			folded.ReadyReviewAttemptID = admissionReadyAttemptID(state)
			folded.ReadyReviewVerdict = admissionReadyVerdict(state)
		}
	}
	gate := workflowStepIsDeliveryGate(workflowStep(definition, state.step))
	if gate {
		// The gate's corrective return carries the post-rejection debt and
		// the accepted-but-inapplicable Review: a successor contract changed
		// the candidate or its depended-on revisions, the delivery
		// assertion refuses, and the declared route obtains a fresh
		// accepted Review at the producer step.
		folded.CorrectionRequestRecovery = folded.ReviewDebt == ReviewDebtOutstanding ||
			state.review.accepted != "" && state.review.candidate && (!state.review.current || state.review.changed)
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
	if workflowCorrectionReturnTarget(definition, state.step) != "" && state.contracts == 1 && state.review.accepted == "correction_required" && state.review.candidate && !state.review.changed && state.review.current {
		folded.CorrectionRequestRecovery = true
		folded.CorrectionRequestMissing = workflowCorrectionMissingNone
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
		if decision.Admitted || (workflowAdmissionDefersToReviewGate(decision, actionID) && folded.ReadyReviewAttemptID != "") {
			moves = append(moves, actionID)
		}
	}
	return moves
}

// admissionMoveFoldKey reduces one model state to the dimensions the lift
// folds and the guards read: the recorded job debt and producing origin of
// the completed attempt never reach workflowAdmit, so states equal under this key admit the
// same moves. The memo the reachable search keeps on it is a harness cost
// repair only — the decision stays the fold's own.
func admissionMoveFoldKey(state admissionModelState) admissionModelState {
	state.jobDebt = false
	state.attemptProduces, state.attemptFresh = false, false
	return state
}

// admissionEnterStep moves the model to a new step: the step entry anchors a
// fresh attempt window while preserving recorded job debt.
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
	state.step, state.attempt, state.dispatched = step, "", false
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
		if slices.Contains(step.Actions, actionID) {
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
		next.review.report = ""
		next.started, next.proof, next.integration = true, false, false
		next.dispatched = true
		// The materialized dispatch is the one consumption of a standing
		// unbound rejection, whatever outcome the successors carry; hold
		// acceptance and step entry never close it.
		next.unboundRejectionStep = ""
		completed := next
		completed.attempt = "completed"
		completed.attemptFresh = true
		failed := next
		failed.attempt = "failed"
		failed.attemptProduces, failed.attemptFresh = false, false
		if admissionLocalJobAcceptStep(definition, state.step) && state.jobs == "required" {
			// The dispatched packet may bind the recorded required
			// revision or run unbound (the payload decides), and only a
			// bound failure opens the recorded obligation's debt
			// (CD-0205 D4).
			bound := failed
			bound.jobDebt = true
			failureSuccessors := []admissionModelState{bound, failed}
			if next.debt != ReviewDebtOutstanding {
				successors := []admissionModelState{}
				for _, produces := range admissionDispatchProductionClasses(definition, state.step) {
					candidate := completed
					candidate.attemptProduces = produces
					if produces {
						successors = append(successors, admissionProducingDispatch(definition, state, candidate)...)
					} else {
						successors = append(successors, candidate)
					}
				}
				successors = append(successors, admissionReviewCompletions(definition, state, completed)...)
				return append(successors, failureSuccessors...)
			}
			ship, noShip := completed, completed
			ship.attemptProduces, noShip.attemptProduces = false, false
			ship.ready, noShip.ready = "ship", "no_ship"
			return append(failureSuccessors, ship, noShip)
		}
		if next.debt != ReviewDebtOutstanding {
			successors := []admissionModelState{}
			for _, produces := range admissionDispatchProductionClasses(definition, state.step) {
				candidate := completed
				candidate.attemptProduces = produces
				if produces {
					successors = append(successors, admissionProducingDispatch(definition, state, candidate)...)
				} else {
					successors = append(successors, candidate)
				}
			}
			successors = append(successors, admissionReviewCompletions(definition, state, completed)...)
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
		next.attempt = ""
		if state.review.report != "" {
			next.review.accepted, next.review.report = state.review.report, ""
			next.review.candidate, next.review.current, next.review.changed = true, true, false
			if state.review.report == "ready_for_final_proof" {
				next.review.blocked = false
			}
			next.attemptProduces, next.attemptFresh = false, false
			return admissionAcceptedJobVariants(next)
		}
		if state.attemptProduces {
			next.review.candidate = true
			if next.review.accepted != "" {
				next.review.changed = true
			}
		}
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
		if next.ready == "no_ship" {
			// The no_ship accept preserves the findings and settles
			// nothing: the debt stays outstanding and the advance waits
			// for a settling review (CD-0201 D3).
			next.ready = ""
			return []admissionModelState{next}
		}
		if next.ready != "" {
			next.debt, next.ready = ReviewDebtNone, ""
		}
		// Main's local intake is not the delivery assertion. Only the
		// separately admitted record_delivery edge leaves execution.
		if workflowDefinitionProducesDelivery(definition) {
			variants := admissionAcceptedJobVariants(next)
			if state.attemptProduces && state.review.accepted != "" {
				for _, changed := range variants {
					unchanged := changed
					unchanged.review.changed = state.review.changed
					variants = append(variants, unchanged)
				}
			}
			return variants
		}
	case "accept_worker_evidence":
		next.attempt = ""
		next.attemptProduces, next.attemptFresh = false, false
	case "reject_worker_result":
		next.attempt = "rejected"
		next.review.report = ""
		// The model's rejection is the unbound one — no job debt opens — so
		// the record stands at this step until a materialized dispatch
		// consumes it.
		next.unboundRejectionStep = state.step
		if workflowPostRejectionReviewStep(definition, state.step) {
			next.debt, next.ready = ReviewDebtOutstanding, ""
		}
		return []admissionModelState{next}
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
		if workflowLateCriterionAssociationStep(definition, state.step) {
			next.verdict, next.observed = "ok", false
			return []admissionModelState{next}
		}
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
		if state.review.accepted == "correction_required" && state.review.current {
			next.review.group = "corrective"
		}
		return []admissionModelState{admissionEnterStep(definition, next, workflowCorrectionReturnTarget(definition, state.step))}
	case "complete":
		next.done = true
		return []admissionModelState{next}
	case "supersede_contract":
		return admissionSupersedeSuccessors(definition, state)
	default:
		mode, modeOK := workflowActionExecutionMode(definition, actionID)
		// CD-0198 D3: on a derived-start pin the step's qualifying
		// checkpoint answers the loader's start window: a fresh entry's
		// first checkpoint derives the start, so the pass reads started
		// with no proof; a re-entry's checkpoint reuses the standing start
		// and resets nothing.
		if workflowDerivedStepStartActive(definition) && workflowDerivedStartCheckpoints[actionID] == state.step {
			next.started, next.proof = true, false
		}
		if modeOK && mode == ActionFenced && !admissionContinuityAction(actionID) {
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
	if slices.Contains(workflowStepArtifactActions(definition, state.step), actionID) && admissionProducesAtRouteTarget(definition, state.step, actionID) {
		next.artifactStale = false
	}
	if advance {
		if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
			advanced := admissionEnterStep(definition, next, workflowNextStep(definition, state.step))
			if actionID == "accept_worker_result" && admissionLocalJobAcceptStep(definition, state.step) {
				// CD-0205: on a job-capable pin the accept of a job-bound
				// attempt is local acceptance and holds the step. The held
				// successor moves the job facet: the accepted
				// revision is satisfied, while another required revision
				// may remain unsatisfied. The accept advances only for an
				// attempt without a job, or — where the step exits through
				// delivery — only when the delivery derivation admits the
				// assertion it carries. The held local acceptance of the
				// recorded obligation discharges the recorded debt; a still-required
				// sibling revision's debt is not discharged by it.
				held := next
				if state.jobs == "required" {
					heldStillRequired := held
					heldStillRequired.jobs = "required"
					held.jobs = "satisfied"
					held.jobsUnsatisfied = ""
					held.jobDebt = false
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
	if slices.Contains(workflowStepArtifactActions(definition, stepID), actionID) {
		return true
	}
	return actionID == "accept_worker_result" && stepDeclaresAction(definition, stepID, "dispatch_worker")
}

// admissionSupersedeSuccessors folds the contract supersession: a successor
// contract re-pins the law. Changed criterion content needs new proof;
// unchanged content keeps its applicable association (CD-0242 D7).
// at the pinned complete step it returns the instance to the correction
// target; without a successor the instance returns to the contract step with
// no contract.
func admissionSupersedeSuccessors(definition WorkflowDefinition, state admissionModelState) []admissionModelState {
	successor := state
	successor.contracts, successor.stale, successor.verdict, successor.observed = 1, "", "", false
	successor.artifactStale = true
	successor.attemptFresh = false
	if workflowDerivedStepStartActive(definition) {
		// The typed supersession opens the successor contract's pass. The
		// old attempt still holds advances until disposition, but its
		// dispatch precedes the boundary correction admission reads.
		successor.dispatched = false
	}
	if target := workflowDisprovedPremiseAtCompleteRouteTarget(definition, state.step); target != "" {
		successor = admissionEnterStep(definition, successor, target)
		return admissionSupersedeCriterionVariants(state, []admissionModelState{successor})
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
	return admissionSupersedeCriterionVariants(state, successors)
}

func admissionSupersedeCriterionVariants(previous admissionModelState, changed []admissionModelState) []admissionModelState {
	for i := range changed {
		changed[i].review.current = false
	}
	if previous.contracts != 1 || previous.verdict != "ok" && previous.review.accepted == "" {
		return changed
	}
	for _, successor := range changed {
		if successor.contracts != 1 {
			continue
		}
		unchanged := successor
		unchanged.verdict, unchanged.artifactStale = previous.verdict, previous.artifactStale
		if !successor.designStale {
			unchanged.review.current = previous.review.current
		}
		changed = append(changed, unchanged)
	}
	return changed
}

func TestCriterionAssociationModelKeepsUnchangedSupersession(t *testing.T) {
	d := mustBuiltinDefinition(t, "workflow.research").Definition
	before := admissionModelState{step: "complete", contracts: 1, verdict: "ok", stale: "law", debt: ReviewDebtNone}
	changed, unchanged := false, false
	for _, successor := range admissionSupersedeSuccessors(d, before) {
		if successor.contracts != 1 {
			continue
		}
		if successor.verdict == "" {
			changed = true
		}
		if successor.verdict == "ok" && !successor.artifactStale {
			unchanged = true
		}
	}
	if !changed || !unchanged {
		t.Fatalf("supersession lost a criterion-content class: changed=%v unchanged=%v", changed, unchanged)
	}
}

// admissionAgentMoves models the size rerun, observation records, and the native verify route
// outside workflow_action. Verification still needs an admitted evidence bind.
func admissionAgentMoves(definition WorkflowDefinition, state admissionModelState) []admissionModelState {
	return admissionAgentMovesWith(definition, state, func() bool {
		return workflowAdmit(definition, admissionWorkflowState(definition, state), "bind_evidence").Admitted
	})
}

// admissionAgentMovesWith is admissionAgentMoves with the bind_evidence
// admission supplied, so the reachable search can answer it from its memo.
func admissionAgentMovesWith(definition WorkflowDefinition, state admissionModelState, bindEvidenceAdmitted func() bool) []admissionModelState {
	if state.done {
		return nil
	}
	var successors []admissionModelState
	if state.sizeStop {
		rerun := state
		rerun.sizeStop = false
		successors = append(successors, rerun)
	}
	if !state.artifact || !state.observed {
		observed := state
		observed.artifact, observed.observed = true, true
		successors = append(successors, observed)
	}
	// worktree_verify is outside workflow_action. A green run can prove the
	// epoch only when it follows the start and bind_evidence is admitted.
	proves := state.started && !state.proof && workflowRefineProofGateActive(definition, state.step)
	// The same route supplies the integration coverage the job facet reads
	// (CD-0205 D3): a green verify run after the required job acceptances and
	// the phase start, bound through an admitted evidence bind, integrates
	// the required Projects.
	integrates := state.started && state.jobs == "satisfied" && !state.integration && workflowWorkerJobsActive(definition)
	if (!proves && !integrates) || !bindEvidenceAdmitted() {
		return successors
	}
	if proves {
		verified := state
		verified.proof = true
		successors = append(successors, verified)
	}
	if integrates {
		integrated := state
		integrated.integration = true
		successors = append(successors, integrated)
	}
	return successors
}

func admissionAgentMoveName(before, after admissionModelState) string {
	if before.sizeStop != after.sizeStop {
		return "revise_intent:size_rerun"
	}
	if before.proof != after.proof || before.integration != after.integration {
		return "worktree_verify+bind_evidence"
	}
	return "observation_record"
}

// admissionEnvironmentMoves are the moves no agent controls: a law revision
// or a Domain registry change makes the active contract's pin stale, and a
// review's findings can prove a larger change bigger (CD-0229 D5), which
// sets the size stop.
func admissionEnvironmentMoves(state admissionModelState) []admissionModelState {
	if state.done || state.contracts != 1 {
		return nil
	}
	var moves []admissionModelState
	if state.stale == "" {
		law, registry := state, state
		law.stale, registry.stale = "law", "registry"
		moves = append(moves, law, registry)
	}
	if !state.sizeStop {
		proved := state
		proved.sizeStop = true
		moves = append(moves, proved)
	}
	return moves
}

func admissionTerminalStep(definition WorkflowDefinition, step string) bool {
	return slices.Contains(definition.StepGraph.TerminalSteps, step)
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
// that dispatches or starts work. Recorded job debt stays out of the enumeration:
// no fold reads them, they shape only the successor model's reachable walk.
// The review debt exists only on a definition that declares the refinement
// shape's review steps, and a ready review implies the debt.
func wellFormedAdmissionStates(definition WorkflowDefinition) iter.Seq[admissionModelState] {
	contractIndex := -1
	if contractStep, err := workflowDefinitionContractStep(definition); err == nil {
		contractIndex = admissionStepIndex(definition, contractStep)
	}
	verdictIndex := -1
	for i, step := range definition.StepGraph.Steps {
		if slices.Contains(step.Actions, "record_verdict") {
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
	return func(yield func(admissionModelState) bool) {
		// Each base state expands in place into its evidence, size-stop, and
		// attempt-origin variants, in that nesting order, so a walk never
		// holds the whole state space in memory.
		withReview := func(state admissionModelState) bool {
			for _, variant := range admissionReviewVariants(definition, state) {
				if !yield(variant) {
					return false
				}
			}
			return true
		}
		withOrigins := func(state admissionModelState) bool {
			if state.attempt != "completed" {
				return withReview(state)
			}
			for _, produces := range admissionDispatchProductionClasses(definition, state.step) {
				for _, fresh := range []bool{false, true} {
					next := state
					next.attemptProduces, next.attemptFresh = produces, fresh
					if !withReview(next) {
						return false
					}
				}
			}
			return true
		}
		// The size stop exists only under one active contract: its proof needs
		// two accepted reviews of the contracted work.
		withStops := func(state admissionModelState) bool {
			if !withOrigins(state) {
				return false
			}
			if state.contracts != 1 {
				return true
			}
			stopped := state
			stopped.sizeStop = true
			return withOrigins(stopped)
		}
		withEvidence := func(state admissionModelState) bool {
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
							if !withStops(next) {
								return false
							}
						}
					}
				}
			}
			return true
		}
		for i, step := range definition.StepGraph.Steps {
			stepJobShapes := jobShapes
			if !slices.Contains(step.Actions, "record_worker_job") {
				stepJobShapes = []string{""}
			}
			contracts := []int64{0}
			if contractIndex < 0 {
				contracts = []int64{0, 1, 2}
			} else if i > contractIndex {
				contracts = []int64{1, 2}
			}
			starts := slices.Contains(step.Actions, "dispatch_worker")
			for _, actionID := range step.Actions {
				if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionFenced {
					starts = true
				}
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
									for _, shape := range debts {
										for _, jobs := range stepJobShapes {
											for _, observation := range [][2]bool{{false, false}, {true, false}, {true, true}} {
												if !withEvidence(admissionModelState{
													step: step.ID, debt: shape.debt, ready: shape.ready,
													attempt: attemptDispatch.attempt, dispatched: attemptDispatch.dispatched,
													contracts: count, stale: stale,
													designStale: designStale, verdict: verdict, artifactStale: artifactStale,
													artifact: observation[0], observed: observation[1],
													jobs: jobs, jobsUnsatisfied: admissionModelJobKeys(jobs), jobsScope: admissionModelJobScope(jobs),
												}) {
													return
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
	}
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

// admissionHasExit reports whether some admitted action has a successor that
// leaves the state, stopping at the first exit. A hold action whose every
// successor equals the state is no exit (CD-0201 D2). The agent's own
// observation record counts: it is a route the agent always holds, and it
// changes the folded state.
func admissionHasExit(definition WorkflowDefinition, state admissionModelState) bool {
	if len(admissionAgentMoves(definition, state)) != 0 {
		return true
	}
	for _, actionID := range admissionModelMoves(definition, state) {
		if admissionContinuityAction(actionID) {
			continue
		}
		for _, successor := range admissionSuccessors(definition, state, actionID) {
			if successor != state {
				return true
			}
		}
	}
	return false
}

// wellFormedAdmissionExitWitnesses walks one definition version's well-formed
// nonterminal abstract states and returns a witness line for each state that
// admits no exit.
func wellFormedAdmissionExitWitnesses(definition WorkflowDefinition) []string {
	var witnesses []string
	for state := range wellFormedAdmissionStates(definition) {
		if !admissionHasExit(definition, state) {
			witnesses = append(witnesses, fmt.Sprintf("well-formed nonterminal state %s admits no exit; admitted: %s",
				state, strings.Join(admissionModelMoves(definition, state), ", ")))
		}
	}
	return witnesses
}

// archivedFamilyRefusalHolds reports whether the admission of one candidate
// action over one folded well-formed state is exactly the archived-family
// refusal: no admission, no approval route, and a KindInvalidOperation
// failure whose message names the archived family.
func archivedFamilyRefusalHolds(definition WorkflowDefinition, folded WorkflowAdmissionState, actionID string) bool {
	decision := workflowAdmit(definition, folded, actionID)
	var failure *Failure
	return !decision.Admitted && failureAs(decision.Failure, &failure) &&
		failure.Kind == KindInvalidOperation &&
		strings.Contains(failure.Detail, definition.Ref) && strings.Contains(failure.Detail, "archived")
}

// archivedRefusalWitnesses walks one archived definition version's well-formed
// nonterminal states and returns a witness for every candidate action whose
// admission is not the archived-family refusal. Archived public authoring is
// forbidden, so workflowAdmit's answer must be total on this side: every
// candidate action refuses with KindInvalidOperation naming the family, and
// a state or action that admits — or mis-classifies its refusal — is the
// witness the seeded detector test proves this walk can name.
func archivedRefusalWitnesses(definition WorkflowDefinition) iter.Seq[string] {
	return func(yield func(string) bool) {
		for state := range wellFormedAdmissionStates(definition) {
			folded := admissionWorkflowState(definition, state)
			for _, actionID := range admissionStateActions(definition, state) {
				if archivedFamilyRefusalHolds(definition, folded, actionID) {
					continue
				}
				witness := fmt.Sprintf("archived nonterminal state %s admits or mis-classifies %q", state, actionID)
				if !yield(witness) {
					return
				}
			}
		}
	}
}

// admissionSeededArchivedProbe returns the first well-formed state and
// candidate action whose admission a definition admits.
func admissionSeededArchivedProbe(definition WorkflowDefinition) (admissionModelState, string, bool) {
	for state := range wellFormedAdmissionStates(definition) {
		for _, actionID := range admissionStateActions(definition, state) {
			if decision := workflowAdmit(definition, admissionWorkflowState(definition, state), actionID); decision.Admitted {
				return state, actionID, true
			}
		}
	}
	return admissionModelState{}, "", false
}

// TestWellFormedAdmissionStateHasNonContinuityExit proves liveness clause
// (a) over every registered definition version: every well-formed nonterminal
// abstract state of a retained family admits at least one non-continuity
// action that leaves it, and every well-formed nonterminal state of an
// archived family refuses every candidate action with the archived-family
// refusal. Archived rows permit reads, not public execution.
func TestWellFormedAdmissionStateHasNonContinuityExit(t *testing.T) {
	t.Parallel()
	definitions := BuiltinWorkflowDefinitions()
	if len(definitions) == 0 {
		t.Fatal("no built-in workflow definitions are registered")
	}
	for _, definition := range definitions {
		t.Run(fmt.Sprintf("%s/v%d", definition.Ref, definition.Version), func(t *testing.T) {
			t.Parallel()
			if workflowFamilyArchived(definition.Ref) {
				for witness := range archivedRefusalWitnesses(definition) {
					t.Error(witness)
				}
				return
			}
			for _, witness := range wellFormedAdmissionExitWitnesses(definition) {
				t.Error(witness)
			}
		})
	}
}

// TestArchivedRefusalCheckNamesSeededAdmission proves the refusal-totality
// detector is not vacuous: a clone whose ref is not archived admits candidate
// actions and the walk names one of them, so a regression that drops or
// mis-aims the archive guard turns an archived row into the same witnesses.
func TestArchivedRefusalCheckNamesSeededAdmission(t *testing.T) {
	seeded := cloneWorkflowDefinition(mustBuiltinDefinition(t, "workflow.implementation").Definition)
	seeded.Ref = "workflow.seed_archived_refusal_detector"
	probeState, probeAction, admitted := admissionSeededArchivedProbe(seeded)
	if !admitted {
		t.Fatalf("the non-archived clone admits no candidate action; the detector has nothing to detect: %s", probeState)
	}
	for witness := range archivedRefusalWitnesses(seeded) {
		if strings.Contains(witness, fmt.Sprintf("%q", probeAction)) {
			return
		}
	}
	t.Fatalf("no witness names the admitted action %q; the refusal-totality walk is vacuous", probeAction)
}

// TestWellFormedExitCheckNamesSeededStrandedState proves the seeded-strand
// half of the exit check: a definition whose step declares only hold actions
// produces a witness that names the stranded state, so the check fails rather
// than assumes.
func TestWellFormedExitCheckNamesSeededStrandedState(t *testing.T) {
	builtins := BuiltinWorkflowDefinitions()
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

// TestReachableAdmissionStateReachesTerminal checks completion reachability
// for executable states and the declared stop at a missing-basis refusal.
// Reaching a future stop by wasting attempts cannot satisfy completion.
// Archived versions cannot lawfully satisfy completion liveness; their
// assertion is the same positive refusal totality the well-formed exit check
// carries.
func TestReachableAdmissionStateReachesTerminal(t *testing.T) {
	t.Parallel()
	definitions := BuiltinWorkflowDefinitions()
	if len(definitions) == 0 {
		t.Fatal("no built-in workflow definitions are registered")
	}
	for _, definition := range definitions {
		t.Run(fmt.Sprintf("%s/v%d", definition.Ref, definition.Version), func(t *testing.T) {
			t.Parallel()
			if workflowFamilyArchived(definition.Ref) {
				for witness := range archivedRefusalWitnesses(definition) {
					t.Errorf("%s v%d: %s", definition.Ref, definition.Version, witness)
				}
				return
			}
			for _, witness := range admissionStrandedWitnesses(definition) {
				t.Errorf("%s v%d: %s", definition.Ref, definition.Version, witness)
			}
		})
	}
}

// TestReachableCheckNamesSeededStrandedState proves a missing completion
// still strands the terminal step, even if earlier failures could reach an
// operator stop. That stop never substitutes for the missing completion.
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
	strand := fmt.Sprintf("state (step %q", seeded.StepGraph.TerminalSteps[0])
	for _, witness := range witnesses {
		if strings.Contains(witness, strand) {
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
	graph := admissionReachableGraph(definition, admissionModelStart(definition))
	live := admissionLiveGraphStates(graph)
	var witnesses []string
	for i := range graph.keys {
		state := graph.state(int32(i))
		if state.done || live[i] {
			continue
		}
		witnesses = append(witnesses, fmt.Sprintf("reachable nonterminal state %s reaches no terminal outcome; admitted moves: %s; path: %s",
			state, strings.Join(admissionModelMoves(definition, state), ", "), admissionModelPath(graph, int32(i))))
	}
	return witnesses
}

// admissionModelKey is a pointer-free encoding of one model state in two
// machine words: every string dimension maps to its index in a closed
// vocabulary, every count to a byte, and every flag to a bit, each in its own
// bit field, so two states share a key exactly when they are equal. A value
// outside its vocabulary or its field width panics rather than collide. The
// reachable search keys its maps on it rather than the state's strings,
// and the collector does not scan a pointer-free map.
// TestAdmissionModelKeyEncodesEveryField fails when the
// state gains a field the key does not encode.
type admissionModelKey [2]uint64

// admissionModelKeyStepBits is the width of each step field: a definition
// may declare at most 30 steps beside the empty step.
const admissionModelKeyStepBits = 5

// admissionModelKeyPacker appends bit fields to a key from the low end.
type admissionModelKeyPacker struct {
	key   admissionModelKey
	shift uint
}

func (p *admissionModelKeyPacker) put(value uint8, bits uint) {
	if bits == 0 || bits > 8 {
		panic(fmt.Sprintf("admission model key field width %d is outside 1..8 bits", bits))
	}
	if uint(value) >= 1<<bits {
		panic(fmt.Sprintf("admission model key field value %d does not fit %d bits", value, bits))
	}
	if p.shift+bits > 128 {
		panic(fmt.Sprintf("admission model key needs more than 128 bits at field offset %d", p.shift))
	}
	word, shift := p.shift/64, p.shift%64
	p.key[word] |= uint64(value) << shift
	if shift+bits > 64 {
		p.key[word+1] |= uint64(value) >> (64 - shift)
	}
	p.shift += bits
}

// get reads the next bit field in the order put wrote it.
func (p *admissionModelKeyPacker) get(bits uint) uint8 {
	if bits == 0 || bits > 8 || p.shift+bits > 128 {
		panic(fmt.Sprintf("admission model key cannot read %d bits at field offset %d", bits, p.shift))
	}
	word, shift := p.shift/64, p.shift%64
	value := p.key[word] >> shift
	if shift+bits > 64 {
		value |= p.key[word+1] << (64 - shift)
	}
	p.shift += bits
	return uint8(value & (1<<bits - 1))
}

func TestAdmissionModelKeyPacksAcrossWordBoundary(t *testing.T) {
	for offset := uint(57); offset <= 64; offset++ {
		for value := 0; value < 256; value++ {
			packed := admissionModelKeyPacker{shift: offset}
			packed.put(uint8(value), 8)
			packed.put(1, 1)
			decoded := admissionModelKeyPacker{key: packed.key, shift: offset}
			if got := decoded.get(8); got != uint8(value) || decoded.get(1) != 1 {
				t.Fatalf("offset %d: key %#v did not retain byte %d and its following flag", offset, packed.key, value)
			}
			changed := admissionModelKeyPacker{shift: offset}
			changed.put(uint8(value), 8)
			changed.put(0, 1)
			if changed.key == packed.key {
				t.Fatal("keys differing in the second word collided")
			}
		}
	}

	packed := admissionModelKeyPacker{}
	for range 16 {
		packed.put(255, 8)
	}
	decoded := admissionModelKeyPacker{key: packed.key}
	for range 16 {
		if got := decoded.get(8); got != 255 {
			t.Fatalf("full-width key lost byte %d", got)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("key capacity overrun did not panic")
		}
	}()
	packed.put(1, 1)
}

func TestAdmissionModelKeyRejectsInvalidFields(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func()
	}{
		{"value overflow", func() { new(admissionModelKeyPacker).put(2, 1) }},
		{"zero write width", func() { new(admissionModelKeyPacker).put(0, 0) }},
		{"wide write", func() { new(admissionModelKeyPacker).put(0, 9) }},
		{"zero read width", func() { new(admissionModelKeyPacker).get(0) }},
		{"wide read", func() { new(admissionModelKeyPacker).get(9) }},
		{"read overrun", func() { (&admissionModelKeyPacker{shift: 128}).get(1) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid key field did not panic")
				}
			}()
			test.run()
		})
	}
}

var (
	admissionKeyDebts     = []string{string(ReviewDebtNone), string(ReviewDebtOutstanding)}
	admissionKeyReadies   = []string{"", "ship", "absent", "no_ship"}
	admissionKeyAttempts  = []string{"", "completed", "failed", "failure_recorded", "rejected"}
	admissionKeyStales    = []string{"", "law", "registry"}
	admissionKeyVerdicts  = []string{"", "ok", "bad"}
	admissionKeyMandates  = []string{"", "missing", "bound"}
	admissionKeyJobs      = []string{"", "required", "satisfied"}
	admissionKeyJobKeys   = []string{"", admissionModelJobKeys("required")}
	admissionKeyJobScopes = []string{"", admissionModelJobScope("required")}
)

func admissionKeyCode(value string, vocabulary []string) uint8 {
	for i, candidate := range vocabulary {
		if candidate == value {
			return uint8(i)
		}
	}
	panic(fmt.Sprintf("admission model value %q is outside the key vocabulary %q", value, vocabulary))
}

func admissionKeyCount(value int64) uint8 {
	if value < 0 || value > 255 {
		panic(fmt.Sprintf("admission model count %d does not fit the key", value))
	}
	return uint8(value)
}

// admissionModelKeyOf encodes one state; steps is "" followed by the
// definition's step IDs.
func admissionModelKeyOf(steps []string, state admissionModelState) admissionModelKey {
	var p admissionModelKeyPacker
	p.put(admissionKeyCode(state.step, steps), admissionModelKeyStepBits)
	p.put(admissionKeyCode(state.unboundRejectionStep, steps), admissionModelKeyStepBits)
	p.put(admissionKeyCode(string(state.debt), admissionKeyDebts), 1)
	p.put(admissionKeyCode(state.ready, admissionKeyReadies), 2)
	p.put(admissionKeyCode(state.attempt, admissionKeyAttempts), 3)
	p.put(admissionKeyCode(state.stale, admissionKeyStales), 2)
	p.put(admissionKeyCode(state.verdict, admissionKeyVerdicts), 2)
	p.put(admissionKeyCode(state.mandate, admissionKeyMandates), 2)
	p.put(admissionKeyCode(state.jobs, admissionKeyJobs), 2)
	p.put(admissionKeyCode(state.jobsUnsatisfied, admissionKeyJobKeys), 1)
	p.put(admissionKeyCode(state.jobsScope, admissionKeyJobScopes), 1)
	p.put(admissionKeyCount(state.contracts), 8)
	for _, set := range []bool{state.dispatched, state.attemptProduces, state.attemptFresh, state.jobDebt, state.designStale,
		state.artifactStale, state.artifact, state.observed, state.started, state.proof, state.integration, state.sizeStop, state.done, state.engineering} {
		var bit uint8
		if set {
			bit = 1
		}
		p.put(bit, 1)
	}
	admissionPackReview(&p, state.review)
	return p.key
}

// admissionModelStateOf decodes a key admissionModelKeyOf encoded with the
// same steps, field for field in the same order.
func admissionModelStateOf(steps []string, key admissionModelKey) admissionModelState {
	p := admissionModelKeyPacker{key: key}
	var state admissionModelState
	state.step = steps[p.get(admissionModelKeyStepBits)]
	state.unboundRejectionStep = steps[p.get(admissionModelKeyStepBits)]
	state.debt = WorkflowReviewDebt(admissionKeyDebts[p.get(1)])
	state.ready = admissionKeyReadies[p.get(2)]
	state.attempt = admissionKeyAttempts[p.get(3)]
	state.stale = admissionKeyStales[p.get(2)]
	state.verdict = admissionKeyVerdicts[p.get(2)]
	state.mandate = admissionKeyMandates[p.get(2)]
	state.jobs = admissionKeyJobs[p.get(2)]
	state.jobsUnsatisfied = admissionKeyJobKeys[p.get(1)]
	state.jobsScope = admissionKeyJobScopes[p.get(1)]
	state.contracts = int64(p.get(8))
	for _, flag := range []*bool{&state.dispatched, &state.attemptProduces, &state.attemptFresh, &state.jobDebt, &state.designStale,
		&state.artifactStale, &state.artifact, &state.observed, &state.started, &state.proof, &state.integration, &state.sizeStop, &state.done, &state.engineering} {
		*flag = p.get(1) == 1
	}
	state.review = admissionUnpackReview(&p)
	return state
}

// admissionModelKeyFields is the number of admissionModelState fields
// admissionModelKeyOf encodes.
const admissionModelKeyFields = 27

func TestAdmissionModelKeyEncodesEveryField(t *testing.T) {
	if got := reflect.TypeOf(admissionModelState{}).NumField(); got != admissionModelKeyFields {
		t.Fatalf("admissionModelState has %d fields but admissionModelKeyOf encodes %d; encode the new field so distinct states keep distinct keys", got, admissionModelKeyFields)
	}
}

// TestAdmissionModelKeyRoundTrips proves the reachable graph, which holds
// states only as keys, decodes every state it encoded: each well-formed state
// of the current implementation and break-fix definitions, and seeded random
// states that draw every field independently from its whole range, so a
// decoder that swaps two fields of one width fails.
func TestAdmissionModelKeyRoundTrips(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewPCG(1, 2))
	pick := func(vocabulary []string) string { return vocabulary[random.IntN(len(vocabulary))] }
	flip := func() bool { return random.IntN(2) == 1 }
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		definition := mustBuiltinDefinition(t, ref).Definition
		steps := []string{""}
		for _, step := range definition.StepGraph.Steps {
			steps = append(steps, step.ID)
		}
		check := func(state admissionModelState) {
			if decoded := admissionModelStateOf(steps, admissionModelKeyOf(steps, state)); decoded != state {
				t.Fatalf("%s: key round trip changed %#v into %#v", ref, state, decoded)
			}
		}
		for state := range wellFormedAdmissionStates(definition) {
			check(state)
		}
		for range 2000 {
			check(admissionModelState{
				step: pick(steps), unboundRejectionStep: pick(steps),
				debt: WorkflowReviewDebt(pick(admissionKeyDebts)), ready: pick(admissionKeyReadies), attempt: pick(admissionKeyAttempts),
				stale: pick(admissionKeyStales), verdict: pick(admissionKeyVerdicts),
				mandate: pick(admissionKeyMandates), jobs: pick(admissionKeyJobs), jobsUnsatisfied: pick(admissionKeyJobKeys),
				jobsScope: pick(admissionKeyJobScopes), contracts: random.Int64N(256),
				dispatched: flip(), attemptProduces: flip(), attemptFresh: flip(), jobDebt: flip(), designStale: flip(), artifactStale: flip(),
				artifact: flip(), observed: flip(), started: flip(), proof: flip(), integration: flip(), sizeStop: flip(), done: flip(), engineering: flip(),
				review: admissionModelReview{report: pick(admissionReviewDispositions), accepted: pick(admissionReviewDispositions), group: pick(admissionReviewGroups), candidate: flip(), changed: flip(), current: flip(), progressed: flip(), blocked: flip()},
			})
		}
	}
}

func TestAdmissionModelKeyKeepsEngineeringClassification(t *testing.T) {
	steps := []string{""}
	plain := admissionModelKeyOf(steps, admissionModelState{debt: ReviewDebtNone})
	engineering := admissionModelKeyOf(steps, admissionModelState{debt: ReviewDebtNone, engineering: true})
	if plain == engineering {
		t.Fatalf("admissionModelKeyOf encodes %d fields but states differing only in engineering share key %#v; encode the engineering field so distinct states keep distinct keys", admissionModelKeyFields, plain)
	}
}

// admissionModelGraph is the reachable search's whole edge set: for every
// reachable state, the successors of its admitted moves and the agent's
// observation record, held as integer indices into the discovery order.
// States are held as their keys: a definition reaches about a million, and
// a pointer-free slice of words keeps the collector from rescanning them.
// Environment edges stay out: the live propagation reads this graph, and
// an environment move never makes a state live. Edge k runs from
// edgeFrom[k] to edgeTo[k]; parent and via record the first edge into each
// state by index; via indexes viaNames.
type admissionModelGraph struct {
	steps            []string
	keys             []admissionModelKey
	parent           []int32
	via              []uint16
	viaNames         []string
	edgeFrom, edgeTo []int32
}

// admissionReachableGraph is the breadth-first search over admitted moves,
// the agent's own moves, and the environment's moves, from
// the start state, in discovery order.
func admissionReachableGraph(definition WorkflowDefinition, start admissionModelState) admissionModelGraph {
	steps := []string{""}
	for _, step := range definition.StepGraph.Steps {
		steps = append(steps, step.ID)
	}
	graph := admissionModelGraph{
		steps:    steps,
		keys:     []admissionModelKey{admissionModelKeyOf(steps, start)},
		parent:   []int32{-1},
		via:      []uint16{0},
		viaNames: []string{""},
	}
	viaIndex := map[string]uint16{"": 0}
	viaOf := func(action string) uint16 {
		name, ok := viaIndex[action]
		if !ok {
			name = uint16(len(graph.viaNames))
			viaIndex[action] = name
			graph.viaNames = append(graph.viaNames, action)
		}
		return name
	}
	index := map[admissionModelKey]int32{graph.keys[0]: 0}
	// The memo holds what workflowAdmit decides over one fold key: the
	// admitted moves and the bind_evidence admission the agent's verify
	// route needs.
	type foldDecisions struct {
		moves        []string
		bindEvidence bool
	}
	memo := map[admissionModelKey]foldDecisions{}
	var decisions foldDecisions
	bindEvidenceAdmitted := func() bool { return decisions.bindEvidence }
	link := func(from int32, action string, successor admissionModelState) {
		key := admissionModelKeyOf(steps, successor)
		to, ok := index[key]
		if !ok {
			to = int32(len(graph.keys))
			index[key] = to
			graph.keys = append(graph.keys, key)
			graph.parent = append(graph.parent, from)
			graph.via = append(graph.via, viaOf(action))
		}
		if to != from && action != admissionEnvironmentMoveName {
			graph.edgeFrom = append(graph.edgeFrom, from)
			graph.edgeTo = append(graph.edgeTo, to)
		}
	}
	for i := 0; i < len(graph.keys); i++ {
		state := graph.state(int32(i))
		moveKey := admissionModelKeyOf(steps, admissionMoveFoldKey(state))
		var ok bool
		decisions, ok = memo[moveKey]
		if !ok {
			decisions = foldDecisions{
				moves:        admissionModelMoves(definition, state),
				bindEvidence: workflowAdmit(definition, admissionWorkflowState(definition, state), "bind_evidence").Admitted,
			}
			memo[moveKey] = decisions
		}
		for _, actionID := range decisions.moves {
			for _, successor := range admissionSuccessors(definition, state, actionID) {
				link(int32(i), actionID, successor)
			}
		}
		for _, successor := range admissionAgentMovesWith(definition, state, bindEvidenceAdmitted) {
			link(int32(i), admissionAgentMoveName(state, successor), successor)
		}
		for _, successor := range admissionEnvironmentMoves(state) {
			link(int32(i), admissionEnvironmentMoveName, successor)
		}
	}
	return graph
}

// state decodes the reachable state at index i.
func (graph admissionModelGraph) state(i int32) admissionModelState {
	return admissionModelStateOf(graph.steps, graph.keys[i])
}

// admissionEnvironmentMoveName labels the environment's edges: pin
// staleness and a size proof.
const admissionEnvironmentMoveName = "environment"

// admissionLiveGraphStates computes completion reachability from the
// recorded edges. Neither an environment move nor an operator stop can
// replace a missing completion path from another state.
func admissionLiveGraphStates(graph admissionModelGraph) []bool {
	live := make([]bool, len(graph.keys))
	// The reverse adjacency in compressed form: the predecessors of state
	// i are reverse[start[i]:start[i+1]].
	start := make([]int32, len(graph.keys)+1)
	for _, to := range graph.edgeTo {
		start[to+1]++
	}
	for i := 1; i < len(start); i++ {
		start[i] += start[i-1]
	}
	reverse := make([]int32, len(graph.edgeTo))
	fill := append([]int32(nil), start[:len(graph.keys)]...)
	for k, to := range graph.edgeTo {
		reverse[fill[to]] = graph.edgeFrom[k]
		fill[to]++
	}
	queue := make([]int32, 0, len(graph.keys))
	mark := func(i int32) {
		if !live[i] {
			live[i] = true
			queue = append(queue, i)
		}
	}
	for i := range graph.keys {
		if graph.state(int32(i)).done {
			mark(int32(i))
		}
	}
	for len(queue) != 0 {
		to := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, from := range reverse[start[to]:start[to+1]] {
			mark(from)
		}
	}
	return live
}

// admissionModelPath renders the first path the reachable search took into
// the state at index i.
func admissionModelPath(graph admissionModelGraph, i int32) string {
	var actions []string
	for ; graph.parent[i] >= 0; i = graph.parent[i] {
		actions = append(actions, graph.viaNames[graph.via[i]])
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

// admissionDerivedCheckpointSuccessor is the CD-0198 D3 successor for the
// step's qualifying checkpoint. derives reports whether the checkpoint
// actually derives the step start — the store's answer, which depends on the
// step's log, not the abstract state — so a deriving checkpoint re-anchors
// the pass the way the loader re-anchors it, and a re-entry checkpoint
// changes nothing.
func admissionDerivedCheckpointSuccessor(definition WorkflowDefinition, state admissionModelState, actionID string, derives bool) admissionModelState {
	next := admissionSuccessors(definition, state, actionID)[0]
	if workflowDerivedStepStartActive(definition) && workflowDerivedStartCheckpoints[actionID] == state.step && derives {
		next.attempt = ""
		next.dispatched = false
		next.attemptProduces, next.attemptFresh = false, false
		next.started, next.proof = true, false
		next.integration = false
	}
	return next
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
	judges := map[string]bool{}
	for _, step := range definition.StepGraph.Steps {
		judges[step.ID] = workflowStepJudgesStaleArtifact(definition, step.ID)
	}
	for state := range wellFormedAdmissionStates(definition) {
		if !state.artifactStale || state.contracts == 0 || !judges[state.step] {
			// Staleness bites only inside a declared route's
			// producer-to-evaluator span; elsewhere the fold reads it
			// unstale.
			continue
		}
		probe := state
		if !enforce {
			probe.artifactStale = false
		}
		folded := admissionWorkflowState(definition, probe)
		if workflowAdmit(definition, folded, "record_verdict").Admitted {
			for _, successor := range admissionSuccessors(definition, probe, "record_verdict") {
				if successor.verdict == "ok" && !successor.artifactStale && successor.step == state.step {
					witnesses = append(witnesses, fmt.Sprintf("stale state %s records an ok verdict without re-production", state))
				}
			}
		}
		if workflowAdmit(definition, folded, "confirm_premise").Admitted && stepDeclaresAction(definition, state.step, "confirm_premise") {
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
	definitions := BuiltinWorkflowDefinitions()
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
// rather than assumes: with the staleness mirror disabled, the same walk
// names a bad-to-ok witness, so the check detects a model or admission that
// loses the enforcement.
func TestStaleArtifactCheckNamesSeededWitness(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
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

// An extra advancing action cannot strand work past its contract step.
// The seeded action exercises the admission guard independently of the
// current compositions, which expose only contract approval as that exit.
func TestContractStepAdvancesOnlyThroughItsApproval(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
		t.Run(ref, func(t *testing.T) {
			definition := mustBuiltinDefinition(t, ref).Definition
			contractStep, err := workflowDefinitionContractStep(definition)
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range workflowStep(definition, contractStep).Actions {
				if action != "approve_contract" && workflowActionAdvancesStep(definition, action) {
					t.Fatalf("%s declares another contract-step advance: %s", ref, action)
				}
			}
			state := WorkflowAdmissionState{Step: contractStep, Lifecycle: "in_progress", InstanceState: "running"}
			if approval := workflowAdmit(definition, state, "approve_contract"); approval.Failure != nil {
				t.Fatalf("current contract approval refused: %v", approval.Failure)
			}
			// Every other advancing action the definition declares is seeded
			// onto the contract step: each must refuse while the step holds
			// no active contract. A seeded action's further preconditions may
			// also refuse; the guarantee is only that no contractless advance
			// is admitted.
			for _, action := range definition.ActionDefinitions {
				if action.ID == "approve_contract" || !workflowActionAdvancesStep(definition, action.ID) {
					continue
				}
				probe := definition
				probe.StepGraph.Steps = slices.Clone(definition.StepGraph.Steps)
				for i := range probe.StepGraph.Steps {
					step := &probe.StepGraph.Steps[i]
					if step.ID == contractStep {
						step.Actions = append(slices.Clone(step.Actions), action.ID)
					}
				}
				if decision := workflowAdmit(probe, state, action.ID); decision.Admitted {
					t.Fatalf("contractless %s advance at %s admitted: %+v", action.ID, contractStep, decision)
				}
			}
		})
	}
}

func TestAdmissionModelProductionRequiresCapabilityAndFreshOrigin(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
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
				step: "execution", contracts: 1, artifactStale: true,
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
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	state := admissionModelState{step: "execution", contracts: 1, artifactStale: true, debt: ReviewDebtNone}
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

// TestAdmissionModelUnboundRejectionHoldsUntilMaterializedDispatch pins the
// unconsumed-unbound-rejection memory over the break-fix pin: the unbound
// rejection stands at its step and lifts only there, a materialized dispatch
// consumes it in every successor, hold acceptance and step entry never
// close it, and a human checkpoint carries no hold at all.
func TestAdmissionModelUnboundRejectionHoldsUntilMaterializedDispatch(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	lifts := func(state admissionModelState) bool {
		return admissionWorkflowState(definition, state).UnconsumedUnboundRejection
	}
	if admissionCheckpointStep(definition, "refine") || !admissionCheckpointStep(definition, "verify") {
		t.Fatalf("pinned step kinds changed: refine checkpoint %v, verify checkpoint %v", admissionCheckpointStep(definition, "refine"), admissionCheckpointStep(definition, "verify"))
	}
	if !admissionLocalJobAcceptStep(definition, "repair") {
		t.Fatal("repair lost the worker-job lifecycle; the hold-acceptance leg no longer exercises the pin")
	}

	// The unbound rejection records its step, and the lift exposes it only
	// at that step.
	rejected := admissionSuccessor(definition, admissionModelState{step: "refine", contracts: 1, debt: ReviewDebtNone}, "reject_worker_result")
	if rejected.unboundRejectionStep != "refine" {
		t.Fatalf("unbound rejection remembered step %q, want refine: %s", rejected.unboundRejectionStep, rejected)
	}
	if !lifts(rejected) {
		t.Fatalf("the unbound rejection at refine lifts no hold: %s", rejected)
	}

	// The materialized dispatch consumes the record whatever outcome the
	// successors carry.
	for _, successor := range admissionSuccessors(definition, rejected, "dispatch_worker") {
		if successor.unboundRejectionStep != "" || lifts(successor) {
			t.Fatalf("dispatch successor kept the unconsumed rejection: %s", successor)
		}
	}

	// The held local job acceptance keeps the memory and its lift: the
	// CD-0205 hold dispositions no rejection.
	jobAccept := admissionModelState{
		step: "repair", contracts: 1, debt: ReviewDebtNone, attempt: "completed", jobs: "required",
		jobsUnsatisfied: admissionModelJobKeys("required"), jobsScope: admissionModelJobScope("required"),
		unboundRejectionStep: "repair",
	}
	heldAccepts := 0
	for _, successor := range admissionSuccessors(definition, jobAccept, "accept_worker_result") {
		if successor.step != "repair" {
			continue
		}
		heldAccepts++
		if successor.unboundRejectionStep != "repair" || !lifts(successor) {
			t.Fatalf("held local acceptance consumed the unconsumed rejection: %s", successor)
		}
	}
	if heldAccepts == 0 {
		t.Fatal("the job-capable accept produced no held successor; the hold-acceptance leg is vacuous")
	}

	// A step entry keeps the memory: the lift is false away from the
	// remembered step and true again on re-entry.
	moved := admissionEnterStep(definition, rejected, "delivery")
	if moved.unboundRejectionStep != "refine" || lifts(moved) {
		t.Fatalf("step entry moved or exposed the remembered rejection: %s", moved)
	}
	if returned := admissionEnterStep(definition, moved, "refine"); !lifts(returned) {
		t.Fatalf("re-entry at the remembered step lifts no hold: %s", returned)
	}

	// A human-checkpoint step carries no hold, and a rejection remembered at
	// another step lifts none.
	if lifts(admissionModelState{step: "verify", contracts: 1, unboundRejectionStep: "verify"}) {
		t.Fatal("a human checkpoint lifted the unconsumed-rejection hold")
	}
	if lifts(admissionModelState{step: "refine", contracts: 1, unboundRejectionStep: "verify"}) {
		t.Fatal("a rejection remembered at another step lifted a hold at refine")
	}
}
