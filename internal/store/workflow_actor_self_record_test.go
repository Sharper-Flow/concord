package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// seedBootstrapCapturedWork reproduces what concord_work_start leaves behind: a
// work item whose workflow instance was initialized by the host's
// operator-class bootstrap actor. The agent session that then works the item is
// always new to workflow_actors, because the host cannot know that session at
// capture time.
func seedBootstrapCapturedWork(t *testing.T, workID, kind string) (*Store, int64) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	definition, err := BuiltinWorkflowDefinitionForRef(DefaultWorkflowRefForKind(kind))
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/bootstrap-" + workID, ActorClass: ActorOperator}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: bootstrap, Now: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)})
	}); err != nil {
		t.Fatal(err)
	}
	return s, readWorkVersion(t, s, workID)
}

// countWorkflowActor reports how many workflow_actors rows carry actorRef.
func countWorkflowActor(t *testing.T, s *Store, actorRef string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, actorRef).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestFirstWorkflowActionRecordsTheActingActor covers issue #740. The actor
// self-record must not be the private property of one action ID: any action a
// session may legally take on the current step is that session's possible first
// action, and every fold that calls requireActor refuses until the row exists.
func TestFirstWorkflowActionRecordsTheActingActor(t *testing.T) {
	for _, testCase := range []struct {
		kind     string
		step     string
		actionID string
	}{
		{kind: "task", step: "proposal", actionID: "record_proposal"},
		{kind: "bug", step: "reproduce", actionID: "record_reproduction"},
	} {
		t.Run(testCase.actionID, func(t *testing.T) {
			ctx := context.Background()
			workID := "issue740-" + testCase.actionID
			s, version := seedBootstrapCapturedWork(t, workID, testCase.kind)

			if got := currentStep(t, s, workID); got != testCase.step {
				t.Fatalf("captured work opened at step=%q, want %q", got, testCase.step)
			}

			agent := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/agent-" + workID, ActorClass: ActorAgent}
			agentRef, err := WorkflowActorRef(agent)
			if err != nil {
				t.Fatal(err)
			}
			if got := countWorkflowActor(t, s, agentRef); got != 0 {
				t.Fatalf("fixture already recorded the acting session: rows=%d, want 0", got)
			}

			request := WorkflowActionExecutionRequest{
				WorkID: workID, ExpectedVersion: version, ActionID: testCase.actionID,
				Payload: mustJSONValue(map[string]any{}), Actor: agent,
				AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64),
				IdempotencyIdentity:  "issue740-" + testCase.actionID,
				OperationID:          "issue740-" + testCase.actionID,
				PrincipalRef:         agent.PrincipalRef, Tool: "concord_work_transition",
				IdempotencyKey: "issue740-" + testCase.actionID, RequestID: "request:issue740-" + testCase.actionID,
				ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC),
			}
			if _, err := invokeWorkflowActionForCD0059(ctx, t, s, request); err != nil {
				t.Fatalf("first workflow action by an unrecorded session: %v", err)
			}

			if got := countWorkflowActor(t, s, agentRef); got != 1 {
				t.Fatalf("acting session actor rows=%d, want 1", got)
			}
			var principal, client, agentField, session, class string
			if err := s.DatabaseForTesting().QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, agentRef).Scan(&principal, &client, &agentField, &session, &class); err != nil {
				t.Fatal(err)
			}
			if principal != agent.PrincipalRef || client != agent.ClientRef || agentField != agent.AgentRef || session != agent.SessionRef || class != string(ActorAgent) {
				t.Fatalf("recorded tuple=%q/%q/%q/%q class=%q, want the acting session's tuple", principal, client, agentField, session, class)
			}
			if got := currentStep(t, s, workID); got == testCase.step {
				t.Fatalf("action did not advance the workflow: step=%q", got)
			}
		})
	}
}
