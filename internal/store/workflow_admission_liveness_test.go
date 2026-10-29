package store

// CD-0201 D2 liveness over every registered definition version,
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
// The model folds current Review, contracts, staleness, design, verdicts,
// artifacts, delivery, and mandates. No state is skipped.

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

func TestAdmissionWorkflowStateKeepsArtifactStaleBit(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		for _, step := range definition.StepGraph.Steps {
			for _, stale := range []bool{false, true} {
				node := admissionModelState{step: step.ID, contracts: 1, artifactStale: stale}
				if got := admissionWorkflowState(definition, node).ArtifactStale; got != stale {
					t.Fatalf("%s step %s changed artifact stale %v to %v", definition.Ref, step.ID, stale, got)
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

func workflowStepJudgesStaleArtifact(definition WorkflowDefinition, stepID string) bool {
	return len(workflowStalenessSpanTargets(definition, stepID)) != 0
}

// admissionModelState is one node of the abstract admission state space.
type admissionModelState struct {
	step   string
	review admissionModelReview
	// contracts is the active contract count: 2 is the duplicated
	// projection the supersede recovery owns.
	contracts int64
	// stale is the pin staleness: "" or "law" (a deleted-law pin every
	// action refuses on).
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
	return fmt.Sprintf("(step %q contracts %d stale %q designStale %v artifactStale %v verdict %q artifact %v observed %v started %v proof %v mandate %q sizeStop %v engineering %v done %v review %+v)",
		s.step, s.contracts, s.stale, s.designStale, s.artifactStale, s.verdict, s.artifact, s.observed, s.started, s.proof, s.mandate, s.sizeStop, s.engineering, s.done, s.review)
}

func admissionModelStart(definition WorkflowDefinition) admissionModelState {
	return admissionModelState{step: definition.StepGraph.StartStep}
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
var admissionRecoveryActions = []string{"supersede_contract", "record_verdict", "request_correction", "bind_evidence"}

// admissionModelStaleError is the staleness boundary's refusal the model
// folds for one staleness class.
func admissionModelStaleError(stale string) error {
	if stale == "law" {
		return newFailure(KindStaleLawRevision, "workflow_law_revision", "the model's pinned law revision is stale", false, "supersede the workflow contract")
	}
	return nil
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
		(step.Kind != WorkflowStepHumanCheckpoint && slices.Contains(step.Actions, "record_review") && !admissionTerminalStep(definition, state.step))
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
	return true
}

// admissionCompleteStepCorrection folds the CD-0172 complete-step route.
func admissionCompleteStepCorrection(definition WorkflowDefinition, state admissionModelState) bool {
	return workflowCompleteStepCorrectionStep(definition, state.step) && !state.done && state.contracts >= 1 && state.observed
}

// admissionWorkflowState lifts the abstract model state into the folded
// WorkflowAdmissionState workflowAdmit decides over, applying the loader's
// rules to the model's dimensions.
func admissionWorkflowState(definition WorkflowDefinition, state admissionModelState) WorkflowAdmissionState {
	refinement := workflowRefinementStepID(definition) != ""
	instance, lifecycle := "running", "in_progress"
	if state.done {
		instance, lifecycle = "completed", "completed"
	}
	staleErr := admissionModelStaleError(state.stale)
	delivery := workflowDeliveryAdmission{Started: state.started, ProofRequired: workflowRefineProofGateActive(definition, state.step)}
	folded := WorkflowAdmissionState{
		Step:                        state.step,
		Lifecycle:                   lifecycle,
		InstanceState:               instance,
		RefinementWorkflow:          refinement,
		ActiveContracts:             state.contracts,
		LateCriterionAssociation:    admissionLateCriterionAssociation(definition, state),
		LawPinStale:                 staleErr != nil && workflowContractRecoveryStaleness(staleErr),
		LawPinStaleError:            staleErr,
		DesignStale:                 state.designStale,
		ArtifactStale:               state.artifactStale,
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
	gate := workflowStepIsDeliveryGate(workflowStep(definition, state.step))
	if gate {
		// The gate's corrective return carries the accepted-but-inapplicable
		// Review: a successor contract changed the candidate or its
		// depended-on revisions, the delivery
		// assertion refuses, and the declared route obtains a fresh
		// accepted Review at the producer step.
		folded.CorrectionRequestRecovery = state.review.accepted != "" && state.review.candidate && (!state.review.current || state.review.changed)
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

// admissionGuardAllows applies payload-blind guard prerequisites: a verdict
// needs one active contract, completion needs an ok verdict, and contract
// approval needs no standing contract.
func admissionGuardAllows(state admissionModelState, actionID string) bool {
	switch actionID {
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
// behind operator approval, and whose guard precondition holds.
func admissionModelMoves(definition WorkflowDefinition, state admissionModelState) []string {
	folded := admissionWorkflowState(definition, state)
	var moves []string
	for _, actionID := range admissionStateActions(definition, state) {
		if !admissionGuardAllows(state, actionID) {
			continue
		}
		decision := workflowAdmit(definition, folded, actionID)
		if decision.Admitted {
			moves = append(moves, actionID)
		}
	}
	return moves
}

// admissionEnterStep preserves the incoming start at a delivery gate. Other
// steps require their own start and fresh proof.
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
	state.step = step
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
// over one model state. A Review records any typed disposition; a verdict
// records ok or not; a contract
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
	switch actionID {
	case "record_review":
		var successors []admissionModelState
		for _, disposition := range admissionReviewDispositions[1:] {
			recorded := next
			recorded.review.accepted = disposition
			recorded.review.candidate, recorded.review.current, recorded.review.changed = true, true, false
			if disposition == "ready_for_final_proof" {
				recorded.review.blocked = false
			}
			successors = append(successors, recorded)
		}
		return successors
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
			return []admissionModelState{bad}
		}
		ok, bad := next, next
		ok.verdict, bad.verdict = "ok", "bad"
		ok.observed, bad.observed = false, false
		bad.artifactStale = true
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
			next.started, next.proof = true, false
		}
	}
	// Direct artifact actions prove production by their own event authority,
	// independently of advancement.
	if admissionProducesAtRouteTarget(definition, state.step, actionID) {
		next.artifactStale = false
	}
	if mode, ok := workflowActionExecutionMode(definition, actionID); ok && mode == ActionAdvance {
		next = admissionEnterStep(definition, next, workflowNextStep(definition, state.step))
	}
	return []admissionModelState{next}
}

// admissionProducesAtRouteTarget reports whether completing actionID at
// stepID is actual production of the artifact the declared routes' evaluator
// judges: the action positively produces (workflowStepArtifactActions), and the
// step is
// a declared unhealthy_verdict route target — the producer the staleness
// causal frontier reads. Every registered definition's unhealthy routes name
// one shared target, so the single staleness bit stays exact. Evidence-only
// acceptance, starts, checkpoints, bindings, verdicts, and producing-shaped
// actions at non-target steps never produce.
func admissionProducesAtRouteTarget(definition WorkflowDefinition, stepID, actionID string) bool {
	if !slices.Contains(workflowStepArtifactActions(definition, stepID), actionID) {
		return false
	}
	for _, route := range workflowRecoveryRoutes(definition) {
		if route.Trigger == WorkflowRecoveryTriggerUnhealthyVerdict && route.Target == stepID {
			return true
		}
	}
	return false
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
		// A supersession changes no artifact staleness (CD-0242 D7).
		reset.mandate = ""
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
		unchanged.verdict = previous.verdict
		if !successor.designStale {
			unchanged.review.current = previous.review.current
		}
		changed = append(changed, unchanged)
	}
	return changed
}

func TestCriterionAssociationModelKeepsUnchangedSupersession(t *testing.T) {
	d := mustBuiltinDefinition(t, "workflow.research").Definition
	before := admissionModelState{step: "complete", contracts: 1, verdict: "ok", stale: "law"}
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
// admission supplied, so the reachable search can reuse its admitted moves.
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
	if !proves || !bindEvidenceAdmitted() {
		return successors
	}
	verified := state
	verified.proof = true
	successors = append(successors, verified)
	return successors
}

func admissionAgentMoveName(before, after admissionModelState) string {
	if before.sizeStop != after.sizeStop {
		return "revise_intent:size_rerun"
	}
	if before.proof != after.proof {
		return "worktree_verify+bind_evidence"
	}
	return "observation_record"
}

// admissionEnvironmentMoves are the moves no agent controls: a breaking law
// cutover makes the active contract's pin stale (CD-0036), and a
// review's findings can prove a larger change bigger (CD-0229 D5), which
// sets the size stop.
func admissionEnvironmentMoves(state admissionModelState) []admissionModelState {
	if state.done || state.contracts != 1 {
		return nil
	}
	var moves []admissionModelState
	if state.stale == "" {
		law := state
		law.stale = "law"
		moves = append(moves, law)
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
// verdict step on. A bad verdict makes an artifact stale; changed contract
// content invalidates its Review and proof dependencies separately.
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
	return func(yield func(admissionModelState) bool) {
		// Each base state expands in place into its evidence, size-stop, and
		// Review variants, in that nesting order, so a walk never
		// holds the whole state space in memory.
		withReview := func(state admissionModelState) bool {
			for _, variant := range admissionReviewVariants(definition, state) {
				if !yield(variant) {
					return false
				}
			}
			return true
		}
		// The size stop exists only under one active contract: its proof needs
		// two accepted reviews of the contracted work.
		withStops := func(state admissionModelState) bool {
			if !withReview(state) {
				return false
			}
			if state.contracts != 1 {
				return true
			}
			stopped := state
			stopped.sizeStop = true
			return withReview(stopped)
		}
		withEvidence := func(state admissionModelState) bool {
			starts := []bool{false}
			if workflowStepIsDeliveryGate(workflowStep(definition, state.step)) {
				// The gate is entered from its delivery-bearing predecessor.
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
						if !withStops(next) {
							return false
						}
					}
				}
			}
			return true
		}
		for i, step := range definition.StepGraph.Steps {
			contracts := []int64{0}
			if contractIndex < 0 {
				contracts = []int64{0, 1, 2}
			} else if i > contractIndex {
				contracts = []int64{1, 2}
			}
			for _, count := range contracts {
				stales, designs, verdicts, artifacts := []string{""}, []bool{false}, []string{""}, []bool{false}
				if count > 0 {
					stales = []string{"", "law"}
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
								for _, observation := range [][2]bool{{false, false}, {true, false}, {true, true}} {
									if !withEvidence(admissionModelState{
										step: step.ID, contracts: count, stale: stale,
										designStale: designStale, verdict: verdict, artifactStale: artifactStale,
										artifact: observation[0], observed: observation[1],
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
		t.Run(definition.Ref, func(t *testing.T) {
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
		t.Run(definition.Ref, func(t *testing.T) {
			t.Parallel()
			if workflowFamilyArchived(definition.Ref) {
				for witness := range archivedRefusalWitnesses(definition) {
					t.Errorf("%s: %s", definition.Ref, witness)
				}
				return
			}
			for witness := range admissionStrandedWitnesses(definition) {
				t.Errorf("%s: %s", definition.Ref, witness)
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
	for i := range seeded.StepGraph.Steps {
		if admissionTerminalStep(seeded, seeded.StepGraph.Steps[i].ID) {
			seeded.StepGraph.Steps[i].Actions = []string{"checkpoint_context"}
		}
	}
	strand := fmt.Sprintf("state (step %q", seeded.StepGraph.TerminalSteps[0])
	var first string
	for witness := range admissionStrandedWitnesses(seeded) {
		if first == "" {
			first = witness
		}
		if strings.Contains(witness, strand) {
			return
		}
	}
	if first == "" {
		t.Fatal("the seeded completion-free terminal step produced no stranded witness")
	}
	t.Fatalf("no witness names the stranded terminal step; first witness: %s", first)
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
// version and yields a witness for each nonterminal state from which no
// path reaches the completed instance. Search and reachability remain exhaustive;
// a consumer that has its witness can stop rendering the remaining diagnostics.
func admissionStrandedWitnesses(definition WorkflowDefinition) iter.Seq[string] {
	return func(yield func(string) bool) {
		graph := admissionReachableGraph(definition, admissionModelStart(definition))
		live := admissionLiveGraphStates(graph)
		for i := range graph.keys {
			state := graph.state(int32(i))
			if state.done || live[i] {
				continue
			}
			if !yield(fmt.Sprintf("reachable nonterminal state %s reaches no terminal outcome; admitted moves: %s; path: %s",
				state, strings.Join(admissionModelMoves(definition, state), ", "), admissionModelPath(graph, int32(i)))) {
				return
			}
		}
	}
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
	admissionKeyStales   = []string{"", "law"}
	admissionKeyVerdicts = []string{"", "ok", "bad"}
	admissionKeyMandates = []string{"", "missing", "bound"}
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
	p.put(admissionKeyCode(state.stale, admissionKeyStales), 2)
	p.put(admissionKeyCode(state.verdict, admissionKeyVerdicts), 2)
	p.put(admissionKeyCode(state.mandate, admissionKeyMandates), 2)
	p.put(admissionKeyCount(state.contracts), 8)
	for _, set := range []bool{state.designStale, state.artifactStale, state.artifact, state.observed, state.started, state.proof, state.sizeStop, state.done, state.engineering} {
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
	state.stale = admissionKeyStales[p.get(2)]
	state.verdict = admissionKeyVerdicts[p.get(2)]
	state.mandate = admissionKeyMandates[p.get(2)]
	state.contracts = int64(p.get(8))
	for _, flag := range []*bool{&state.designStale, &state.artifactStale, &state.artifact, &state.observed, &state.started, &state.proof, &state.sizeStop, &state.done, &state.engineering} {
		*flag = p.get(1) == 1
	}
	state.review = admissionUnpackReview(&p)
	return state
}

// admissionModelKeyFields is the number of admissionModelState fields
// admissionModelKeyOf encodes.
const admissionModelKeyFields = 15

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
				step: pick(steps), stale: pick(admissionKeyStales), verdict: pick(admissionKeyVerdicts),
				mandate: pick(admissionKeyMandates), contracts: random.Int64N(256),
				designStale: flip(), artifactStale: flip(),
				artifact: flip(), observed: flip(), started: flip(), proof: flip(), sizeStop: flip(), done: flip(), engineering: flip(),
				review: admissionModelReview{accepted: pick(admissionReviewDispositions), candidate: flip(), changed: flip(), current: flip(), blocked: flip()},
			})
		}
	}
}

func TestAdmissionModelKeyKeepsEngineeringClassification(t *testing.T) {
	steps := []string{""}
	plain := admissionModelKeyOf(steps, admissionModelState{})
	engineering := admissionModelKeyOf(steps, admissionModelState{engineering: true})
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
		moves := admissionModelMoves(definition, state)
		for _, actionID := range moves {
			for _, successor := range admissionSuccessors(definition, state, actionID) {
				link(int32(i), actionID, successor)
			}
		}
		for _, successor := range admissionAgentMovesWith(definition, state, func() bool { return slices.Contains(moves, "bind_evidence") }) {
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
// member of admissionSuccessors, the completed outcome of a
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
		next.started, next.proof = true, false
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
			t.Errorf("%s: %s", definition.Ref, witness)
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
