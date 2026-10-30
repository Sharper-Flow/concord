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
			digest := workflowFixtureDigest(t)
			executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/exec")
			setup := []Event{
				workflowEvent("d6-executor", WorkflowActorRecorded, "d6-work", map[string]any{"work_id": "d6-work", "expected_version": 2, "resulting_version": 3, "actor_ref": executor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/exec", "actor_class": "agent"}),
				workflowEvent("d6-definition", WorkflowDefinitionSelected, "d6-work", map[string]any{"work_id": "d6-work", "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 1, "digest": digest, "work_kind": workflowFixtureWorkKind}),
				workflowEventWithActor("d6-start", WorkflowActionStarted, "d6-work", executor, map[string]any{"work_id": "d6-work", "expected_version": 4, "resulting_version": 5, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "d6-operation", "actor_ref": executor, "execution_model": testCase.executionModel}),
			}
			if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "d6-work"): 2}}); err != nil {
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
