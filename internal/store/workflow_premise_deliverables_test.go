package store

import (
	"context"
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
		if err := enterFold(ctx, tx); err != nil {
			return err
		}
		_, actionErr := applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
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
