package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The acceptance step's transition must refuse while the deliverables
// completion depends on are absent. A workflow that confirms its premise
// without a recorded verdict or without every contract-required evidence
// kind reaches the release step with no declared action able to produce
// them, so completion becomes unreachable. The corpus scenario WF04's setup
// history is replayed with the verdict and premise events removed.
func TestConfirmPremiseRequiresVerdictAndRequiredEvidence(t *testing.T) {
	t.Parallel()
	corpus := readWorkflowScenarioCorpus(t)
	var scenario workflowScenario
	for _, candidate := range corpus.Scenarios {
		if candidate.ID == "WF04-weaker-delivery" {
			scenario = candidate
			break
		}
	}
	if scenario.ID == "" {
		t.Fatal("WF04 is missing from the corpus")
	}

	ctx := context.Background()

	registered, err := BuiltinWorkflowDefinitionForRef(scenario.Request.DefinitionPin.Ref)
	if err != nil {
		t.Fatal(err)
	}
	actorRef := scenarioActorRef(scenario)

	replay := func(drop ...string) (*Store, string) {
		t.Helper()
		dropped := map[string]bool{}
		for _, kind := range drop {
			dropped[kind] = true
		}
		history := make([]workflowCorpusEvent, 0, len(scenario.Setup.EventHistory))
		for _, event := range scenario.Setup.EventHistory {
			if dropped[event.Kind] {
				continue
			}
			history = append(history, event)
		}
		setup := scenario.Setup
		setup.EventHistory = history
		store := openTemp(t)
		if err := replayWorkflowCorpusSetup(ctx, store, setup, registered, actorRef, true); err != nil {
			t.Fatal(err)
		}
		return store, scenario.Setup.FixtureRefs.WorkItem
	}

	atAcceptance := func(store *Store, workID string) int64 {
		t.Helper()
		var step string
		if err := store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
			t.Fatal(err)
		}
		if step != "acceptance" {
			if err := applyProjectionCorruptionFault(ctx, store, projectionCorruptionFaultInput{WorkID: workID, Target: "workflow_instances", Field: "current_step", Value: "acceptance"}); err != nil {
				t.Fatal(err)
			}
		}
		version, err := workflowCurrentVersion(ctx, store, workID)
		if err != nil {
			t.Fatal(err)
		}
		return version
	}

	confirm := func(ctx context.Context, store *Store, workID string, version int64) error {
		t.Helper()
		grant := scenario.Request.Grant
		invoking := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: ActorAgent}
		operator := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: "agent:operator-signed", SessionRef: "session:operator-signed", ActorClass: ActorOperator}
		tx, err := store.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		_, actionErr := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "confirm_premise",
			Actor: invoking, OperatorActor: &operator, OperationID: workID + ":premise-confirm",
			PrincipalRef: operator.PrincipalRef, Tool: "concord_work_transition",
			IdempotencyKey: workID + ":premise-confirm", IdempotencyIdentity: workID + ":premise-confirm",
			RequestID: workID + ":premise-confirm", AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), ContractDigest: testManifestDigest,
			Now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC),
		})
		_ = leaveFold(ctx, tx)
		return actionErr
	}

	// Without the verdict: confirmation must refuse naming the verdict.
	s, workID := replay("workflow.verdict_recorded", "workflow.premise_confirmed")
	version := atAcceptance(s, workID)
	actionErr := confirm(ctx, s, workID, version)
	if actionErr == nil {
		t.Fatal("premise confirmation passed without a recorded verdict")
	}
	var failure *Failure
	if !failureAs(actionErr, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "verdict") {
		t.Fatalf("premise confirmation without a verdict returned %v, want KindMissingEvidence naming the verdict", actionErr)
	}

	// With the verdict and every required evidence kind present, the
	// confirmation proceeds: the gate refuses only the missing deliverables.
	s2, workID2 := replay("workflow.premise_confirmed")
	version2 := atAcceptance(s2, workID2)
	if actionErr2 := confirm(ctx, s2, workID2, version2); actionErr2 != nil {
		t.Fatalf("premise confirmation with full deliverables refused: %v", actionErr2)
	}
}

func scenarioActorRef(scenario workflowScenario) string {
	grant := scenario.Request.Grant
	actor := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: ActorAgent}
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		return ""
	}
	return ref
}

// seedPremiseDeliverablesInvestigation records the investigation artifact the
// operator question requires at the acceptance step, so the question stands
// open and the deliverables gate is the confirmation's only refusal. The
// corpus Product holds one work item, so the artifact needs only name the
// current Domain the corpus replay already registered.
func seedPremiseDeliverablesInvestigation(t *testing.T, s *Store, workID string) {
	t.Helper()
	var domainID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT domain_id FROM domains WHERE product_id='product' AND status='current' ORDER BY domain_id LIMIT 1`).Scan(&domainID); err != nil {
		t.Fatalf("read the corpus Domain: %v", err)
	}
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("d", 16), []string{domainID})
}

// premiseDeliverablesOperator is the operator identity the confirmation's
// boundary requires.
func premiseDeliverablesOperator(grant workflowCorpusGrant) WorkflowActor {
	return WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: "agent:operator-signed", SessionRef: "session:operator-signed", ActorClass: ActorOperator}
}

// premiseDeliverablesConfirmation builds the operator's confirmation request
// against the open question: the selected choice and the question's decision
// context digest, so the deliverables gate is the request's only refusal.
func premiseDeliverablesConfirmation(t *testing.T, s *Store, workID string, operator WorkflowActor) WorkflowActionPreflightRequest {
	t.Helper()
	pin := issue1013Pin(t, s, workID)
	if pin.PendingOperatorDecision == nil || pin.PendingOperatorDecision.ActionID != "confirm_premise" {
		t.Fatalf("acceptance pin question = %+v, withheld = %+v; want the open confirm_premise question", pin.PendingOperatorDecision, pin.WithheldOperatorDecision)
	}
	return WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: pin.Version, ActionID: "confirm_premise", Actor: operator, Payload: json.RawMessage(`{}`),
		SelectedChoice: "confirm", DecisionContextDigest: pin.PendingOperatorDecision.DecisionContextDigest,
	}
}

// replayPremiseQuestionAtAcceptance replays the WF04 corpus setup without the
// named event kinds, places the instance at its acceptance step, and records
// the investigation artifact, so the confirm_premise question stands open and
// the deliverables gate is the confirmation's only refusal.
func replayPremiseQuestionAtAcceptance(t *testing.T, drop ...string) (*Store, string, WorkflowActor) {
	t.Helper()
	ctx := context.Background()
	corpus := readWorkflowScenarioCorpus(t)
	var scenario workflowScenario
	for _, candidate := range corpus.Scenarios {
		if candidate.ID == "WF04-weaker-delivery" {
			scenario = candidate
			break
		}
	}
	if scenario.ID == "" {
		t.Fatal("WF04 is missing from the corpus")
	}
	registered, err := BuiltinWorkflowDefinitionForRef(scenario.Request.DefinitionPin.Ref)
	if err != nil {
		t.Fatal(err)
	}
	dropped := map[string]bool{}
	for _, kind := range drop {
		dropped[kind] = true
	}
	history := make([]workflowCorpusEvent, 0, len(scenario.Setup.EventHistory))
	for _, event := range scenario.Setup.EventHistory {
		if !dropped[event.Kind] {
			history = append(history, event)
		}
	}
	setup := scenario.Setup
	setup.EventHistory = history
	store := openTemp(t)
	if err := replayWorkflowCorpusSetup(ctx, store, setup, registered, scenarioActorRef(scenario), true); err != nil {
		t.Fatal(err)
	}
	workID := scenario.Setup.FixtureRefs.WorkItem
	var step string
	if err := store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "acceptance" {
		if err := applyProjectionCorruptionFault(ctx, store, projectionCorruptionFaultInput{WorkID: workID, Target: "workflow_instances", Field: "current_step", Value: "acceptance"}); err != nil {
			t.Fatal(err)
		}
	}
	seedPremiseDeliverablesInvestigation(t, store, workID)
	return store, workID, premiseDeliverablesOperator(scenario.Request.Grant)
}

// TestPreflightRefusesConfirmationMissingMandatedDeliverables pins the same
// deliverables gate on the read-only preflight: behind an open operator
// question, the preflight refuses the confirmation whose mandated
// deliverables are missing with the same refusal the confirmation's own
// boundary applies, and stops refusing once the deliverables stand, so the
// surface a caller asks before acting agrees with the boundary that enforces.
func TestPreflightRefusesConfirmationMissingMandatedDeliverables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Without the verdict: the preflight refuses the confirmation with the
	// verdict refusal the confirmation's own boundary applies.
	s, workID, operator := replayPremiseQuestionAtAcceptance(t, "workflow.verdict_recorded", "workflow.premise_confirmed")
	preflightErr := InspectWorkflowActionAdmission(ctx, s, premiseDeliverablesConfirmation(t, s, workID, operator))
	var failure *Failure
	if preflightErr == nil || !failureAs(preflightErr, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "verdict") {
		t.Fatalf("preflight confirm_premise without the verdict = %v, want KindMissingEvidence naming the verdict", preflightErr)
	}

	// With the verdict and every required evidence kind bound, the preflight
	// admits the confirmation: the gate refuses only the missing deliverables.
	s2, workID2, operator2 := replayPremiseQuestionAtAcceptance(t, "workflow.premise_confirmed")
	if err := InspectWorkflowActionAdmission(ctx, s2, premiseDeliverablesConfirmation(t, s2, workID2, operator2)); err != nil {
		t.Fatalf("preflight confirm_premise with full deliverables refused: %v", err)
	}
}
