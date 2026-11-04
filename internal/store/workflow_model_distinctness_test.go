package store

import (
	"context"
	"strings"
	"testing"
)

func distinctnessActor(agent, session string, class ActorClass, model string) WorkflowActor {
	return WorkflowActor{
		ActorRef:     DeriveWorkflowActorRef("principal/operator", "client/concord-1", agent, session),
		PrincipalRef: "principal/operator",
		ClientRef:    "client/concord-1",
		AgentRef:     agent,
		SessionRef:   session,
		ActorClass:   class,
		Model:        model,
	}
}

func TestWorkflowActorModelIsBoundedAndOutsideTheIdentityTuple(t *testing.T) {
	t.Parallel()
	if err := ValidateWorkflowActorModel(strings.Repeat("m", 129)); err == nil {
		t.Fatal("oversized readback model accepted")
	}
	if err := ValidateWorkflowActorModel(" vendor/model-a"); err == nil {
		t.Fatal("unbounded readback model accepted")
	}
	if err := ValidateWorkflowActorModel(""); err != nil {
		t.Fatalf("absent readback model rejected: %v", err)
	}
	// The model must never move actor identity: workflow_actors is keyed by its
	// authenticated four-tuple, and one identity may act on different models.
	plain := distinctnessActor("agent/executor", "session/exec", ActorAgent, "")
	withModel := distinctnessActor("agent/executor", "session/exec", ActorAgent, "vendor/model-a")
	plainRef, err := WorkflowActorRef(plain)
	if err != nil {
		t.Fatal(err)
	}
	modelRef, err := WorkflowActorRef(withModel)
	if err != nil {
		t.Fatal(err)
	}
	if plainRef != modelRef {
		t.Fatalf("readback model changed actor identity: %q != %q", plainRef, modelRef)
	}
}

func TestWorkflowExecutionModelProjectionPreservesReadback(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name           string
		executionModel string
	}{
		{name: "present model", executionModel: "vendor/model-a"},
		{name: "another model", executionModel: "vendor/model-b"},
		{name: "absent model", executionModel: ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			seedWork(t, s, "d6-work")
			seedWorkflowLaw(t, s)
			actor := distinctnessActor("agent/executor", "session/exec", ActorAgent, testCase.executionModel)
			executor, err := WorkflowActorRef(actor)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: "d6-work", Actor: actor}); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			seedWorkflowPlanFrontier(t, s, "d6-work", WorkflowStepPlanChange, "implement")
			version := readWorkVersion(t, s, "d6-work")
			start := workflowEventWithActor("d6-start", WorkflowActionStarted, "d6-work", executor, map[string]any{"work_id": "d6-work", "expected_version": version, "resulting_version": version + 1, "step_id": "implement", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "d6-operation", "actor_ref": executor, "execution_model": testCase.executionModel})
			if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "d6-work"): version}}); err != nil {
				t.Fatal(err)
			}

			var stored string
			if err := s.DatabaseForTesting().QueryRow(`SELECT execution_model FROM workflow_instances WHERE work_id='d6-work'`).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != testCase.executionModel {
				t.Fatalf("projected execution_model = %q, want %q", stored, testCase.executionModel)
			}
		})
	}
}
