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

func TestCompletionMainAuthorityDoesNotRequireDifferentModels(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name           string
		executionModel string
		verdictModel   string
		declared       bool
		sameMain       bool
	}{
		{name: "different models", executionModel: "vendor/model-a", verdictModel: "vendor/model-b", declared: true},
		{name: "same model", executionModel: "vendor/model-a", verdictModel: "vendor/model-a", declared: true},
		{name: "absent proof-association model", executionModel: "vendor/model-a", verdictModel: "", declared: true},
		{name: "absent execution model", executionModel: "", verdictModel: "vendor/model-b", declared: true},
		{name: "no declared separation", executionModel: "vendor/model-a", verdictModel: "vendor/model-a", declared: false},
		{name: "Main also executed earlier steps", executionModel: "vendor/model-a", verdictModel: "vendor/model-a", declared: true, sameMain: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			seedWork(t, s, "d6-work")
			seedWorkflowLaw(t, s)
			digest := workflowFixtureDigest(t)
			executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/exec")
			reviewer := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/reviewer", "session/review")
			setup := []Event{
				workflowEvent("d6-executor", WorkflowActorRecorded, "d6-work", map[string]any{"work_id": "d6-work", "expected_version": 2, "resulting_version": 3, "actor_ref": executor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/exec", "actor_class": "agent"}),
				workflowEvent("d6-reviewer", WorkflowActorRecorded, "d6-work", map[string]any{"work_id": "d6-work", "expected_version": 3, "resulting_version": 4, "actor_ref": reviewer, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/reviewer", "session_ref": "session/review", "actor_class": "agent"}),
				workflowEvent("d6-definition", WorkflowDefinitionSelected, "d6-work", map[string]any{"work_id": "d6-work", "expected_version": 4, "resulting_version": 5, "ref": workflowFixtureRef, "version": 1, "digest": digest, "work_kind": workflowFixtureWorkKind}),
				workflowEventWithActor("d6-start", WorkflowActionStarted, "d6-work", executor, map[string]any{"work_id": "d6-work", "expected_version": 5, "resulting_version": 6, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "d6-operation", "actor_ref": executor, "execution_model": testCase.executionModel}),
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

			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if testCase.sameMain {
				reviewer = executor
			}
			if err := workflowCompletionActorDistinct(ctx, tx, "d6-work", reviewer); err != nil {
				t.Fatalf("Main was subjected to a retired evaluator/model requirement: %v", err)
			}
		})
	}
}

// The declared flag must reach the gate from the definition, so no builtin may
// silently opt in: CD-0017 leaves mandatory scope to the R6 section 5 basis.
func TestNoBuiltinDeclaresModelDistinctness(t *testing.T) {
	t.Parallel()
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.EvaluatorIndependence.ModelDistinct {
			t.Fatalf("%s declares model distinctness; CD-0017 leaves that to the R6 measured basis", definition.Ref)
		}
	}
}
