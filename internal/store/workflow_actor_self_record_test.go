package store

import (
	"context"
	"errors"
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
	ref, err := DefaultWorkflowRefForKind(kind)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := BuiltinWorkflowDefinitionForRef(ref)
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
	t.Parallel()
	for _, testCase := range []struct {
		kind     string
		step     string
		actionID string
	}{
		{kind: "task", step: "proposal", actionID: "record_proposal"},
		// The bug head opens at the merged diagnosis step whose only advance
		// exit is record_diagnosis.
		{kind: "bug", step: "diagnosis", actionID: "record_diagnosis"},
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
			payload := mustJSONValue(map[string]any{})
			if testCase.actionID == "record_proposal" {
				payload = mustJSONValue(map[string]any{"problem": "The bounded problem statement.", "affected": []string{"The affected system."}, "stakes": "The bounded stakes statement.", "user_outcomes": []string{"The expected user outcome."}})
			}
			if testCase.actionID == "record_diagnosis" {
				payload = mustJSONValue(map[string]any{"reproduction": "The bounded reproduction steps.", "searched": "The bounded searched area.", "outcome": "none_found", "root_cause": "The bounded root cause."})
			}

			request := WorkflowActionExecutionRequest{
				WorkID: workID, ExpectedVersion: version, ActionID: testCase.actionID,
				Payload: payload, Actor: agent,
				AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64),
				IdempotencyIdentity:  "issue740-" + testCase.actionID,
				OperationID:          "issue740-" + testCase.actionID,
				PrincipalRef:         agent.PrincipalRef, Tool: "concord_work_transition",
				IdempotencyKey: "issue740-" + testCase.actionID, RequestID: "request:issue740-" + testCase.actionID,
				ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC),
			}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), request)
				return err
			}); err != nil {
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

// countActorRecordedEvents counts accepted actor-record effects.
func countActorRecordedEvents(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=?`, WorkflowActorRecorded).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// The actor row owns its tuple and first-seen time. The receipt binds that tuple
// to the prefix identity and the request's exact version cursor.
func readActorRecordedPrefixEvent(t *testing.T, s *Store, eventID string, expectedVersion int64) (Event, workflowActorRecordedPayload) {
	t.Helper()
	var event Event
	var occurredAt, digest string
	var payload workflowActorRecordedPayload
	err := s.DatabaseForTesting().QueryRow(`SELECT r.kind,r.subject_type,r.subject_id,r.actor,a.first_seen_at,r.payload_version,r.payload_digest,a.actor_ref,a.principal_ref,a.client_ref,a.agent_ref,a.session_ref,a.actor_class FROM effect_receipts r JOIN workflow_actors a ON a.actor_ref=r.actor WHERE r.event_id=? AND r.kind=?`, eventID, WorkflowActorRecorded).Scan(&event.Kind, &event.SubjectType, &event.SubjectID, &event.Actor, &occurredAt, &event.PayloadVersion, &digest, &payload.ActorRef, &payload.PrincipalRef, &payload.ClientRef, &payload.AgentRef, &payload.SessionRef, &payload.ActorClass)
	if err != nil {
		t.Fatalf("read actor prefix receipt and current tuple %s: %v", eventID, err)
	}
	parsed, parseErr := time.Parse(time.RFC3339Nano, occurredAt)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	event.OccurredAt = parsed
	payload.WorkID = event.SubjectID
	payload.ExpectedVersion = &expectedVersion
	resultingVersion := expectedVersion + 1
	payload.ResultingVersion = &resultingVersion
	want, digestErr := effectPayloadDigest(mustJSONValue(payload))
	if digestErr != nil || digest != want {
		t.Fatalf("actor prefix payload digest=%q, want exact tuple/version payload %q: %v", digest, want, digestErr)
	}
	return event, payload
}

func readActorPrefixActionTime(t *testing.T, s *Store, eventID string) time.Time {
	t.Helper()
	var occurredAt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT i.last_checkpoint_at FROM workflow_instances i JOIN workflow_phase_exits x ON x.work_id=i.work_id JOIN effect_receipts r ON r.effect_seq=x.exit_seq WHERE r.event_id=? AND r.kind=?`, eventID, WorkflowActionCompleted).Scan(&occurredAt); err != nil {
		t.Fatalf("read current action time for %s: %v", eventID, err)
	}
	parsed, parseErr := time.Parse(time.RFC3339Nano, occurredAt)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	return parsed
}

// TestWorkflowActorRecordPrefixCharacterization pins the shared actor-prefix
// owner's outcomes through the real dispatch path: one exact prefix at the
// request's version cursor for a missing tuple, no prefix and an unchanged
// actor-event count for an already-recorded tuple, the mismatch refusal for a
// recorded row that disagrees with the authenticated actor, and the normalized
// default time for a zero request Now.
func TestWorkflowActorRecordPrefixCharacterization(t *testing.T) {
	t.Parallel()
	buildAction := func(workID, actionID, operationID string, actor WorkflowActor, expectedVersion int64, payload map[string]any) WorkflowActionExecutionRequest {
		return WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: expectedVersion, ActionID: actionID,
			Payload: mustJSONValue(payload), Actor: actor,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64),
			IdempotencyIdentity:  operationID,
			OperationID:          operationID,
			PrincipalRef:         actor.PrincipalRef, Tool: "concord_work_transition",
			IdempotencyKey: operationID, RequestID: "request:" + operationID,
			ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC),
		}
	}
	proposalPayload := map[string]any{"problem": "The bounded problem statement.", "affected": []string{"The affected system."}, "stakes": "The bounded stakes statement.", "user_outcomes": []string{"The expected user outcome."}}
	ctx := context.Background()

	t.Run("missing tuple records one exact prefix", func(t *testing.T) {
		workID := "issue740-prefix-missing"
		s, version := seedBootstrapCapturedWork(t, workID, "task")
		agent := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/agent-" + workID, ActorClass: ActorAgent}
		agentRef, err := WorkflowActorRef(agent)
		if err != nil {
			t.Fatal(err)
		}
		request := buildAction(workID, "record_proposal", workID, agent, version, proposalPayload)
		if err := s.Transact(ctx, func(tx *Transaction) error {
			_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), request)
			return err
		}); err != nil {
			t.Fatalf("first workflow action by an unrecorded session: %v", err)
		}
		if got := countWorkflowActor(t, s, agentRef); got != 1 {
			t.Fatalf("acting session actor rows=%d, want 1", got)
		}
		event, payload := readActorRecordedPrefixEvent(t, s, request.OperationID+":actor", version)
		if event.Kind != WorkflowActorRecorded || event.SubjectType != SubjectWorkItem || event.SubjectID != workID || event.Actor != agentRef {
			t.Fatalf("actor prefix event identity=%+v, want the canonical actor record for %s", event, agentRef)
		}
		if !event.OccurredAt.Equal(request.Now) {
			t.Fatalf("actor prefix occurred_at=%v, want the request time %v", event.OccurredAt, request.Now)
		}
		if event.PayloadVersion != 1 {
			t.Fatalf("actor prefix payload_version=%d, want 1", event.PayloadVersion)
		}
		if payload.WorkID != workID || payload.ExpectedVersion == nil || *payload.ExpectedVersion != version || payload.ResultingVersion == nil || *payload.ResultingVersion != version+1 {
			t.Fatalf("actor prefix version cursor=%+v, want expected %d resulting %d", payload.WorkflowVersionFields, version, version+1)
		}
		if payload.ActorRef != agentRef || payload.PrincipalRef != agent.PrincipalRef || payload.ClientRef != agent.ClientRef || payload.AgentRef != agent.AgentRef || payload.SessionRef != agent.SessionRef || payload.ActorClass != string(ActorAgent) {
			t.Fatalf("actor prefix payload=%+v, want the authenticated tuple", payload)
		}
	})

	t.Run("recorded tuple appends no prefix", func(t *testing.T) {
		workID := "issue740-prefix-recorded"
		s, version := seedBootstrapCapturedWork(t, workID, "task")
		agent := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/agent-" + workID, ActorClass: ActorAgent}
		agentRef, err := WorkflowActorRef(agent)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Transact(ctx, func(tx *Transaction) error {
			_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), buildAction(workID, "record_proposal", workID, agent, version, proposalPayload))
			return err
		}); err != nil {
			t.Fatalf("first workflow action by an unrecorded session: %v", err)
		}
		// Alignment is the actual next step after proposal on this
		// definition, and record_alignment is its only advance exit. From
		// source: a none_found alignment emits exactly one typed event, and
		// the generic completion follows it, so the recorded session's
		// action adds exactly two versions and no actor prefix.
		recordedVersion := readWorkVersion(t, s, workID)
		actorEventsBefore := countActorRecordedEvents(t, s)
		alignmentPayload := map[string]any{"searched": "The bounded searched area.", "outcome": "none_found"}
		if err := s.Transact(ctx, func(tx *Transaction) error {
			_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), buildAction(workID, "record_alignment", workID+"-second", agent, recordedVersion, alignmentPayload))
			return err
		}); err != nil {
			t.Fatalf("second workflow action by the recorded session: %v", err)
		}
		if got := readWorkVersion(t, s, workID); got != recordedVersion+2 {
			t.Fatalf("already-recorded action moved version to %d, want the alignment semantic and completion pair at %d", got, recordedVersion+2)
		}
		if got := countActorRecordedEvents(t, s); got != actorEventsBefore {
			t.Fatalf("matched tuple added %d actor events, want 0", got-actorEventsBefore)
		}
		if got := countWorkflowActor(t, s, agentRef); got != 1 {
			t.Fatalf("acting session actor rows=%d, want 1", got)
		}
	})

	t.Run("mismatched recorded tuple refuses", func(t *testing.T) {
		actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/agent-corrupt", ActorClass: ActorAgent}
		tx := seedMismatchedActorRowTx(t, ctx, actor)
		prefix, prefixErr := workflowActorRecordPrefixTx(ctx, tx, actor)
		var failure *Failure
		if !errors.As(prefixErr, &failure) || failure.Kind != KindInvariantViolation || failure.Detail != "recorded workflow actor tuple does not match the authenticated actor" {
			t.Fatalf("mismatched tuple failure=%v, want the recorded-tuple invariant refusal", prefixErr)
		}
		if prefix != nil {
			t.Fatalf("mismatched tuple returned a recording draft %+v, want no effect", prefix)
		}
	})

	t.Run("zero request time records the normalized action time", func(t *testing.T) {
		workID := "issue740-prefix-zero-now"
		s, version := seedBootstrapCapturedWork(t, workID, "task")
		agent := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/agent-" + workID, ActorClass: ActorAgent}
		agentRef, err := WorkflowActorRef(agent)
		if err != nil {
			t.Fatal(err)
		}
		request := buildAction(workID, "record_proposal", workID, agent, version, proposalPayload)
		request.Now = time.Time{}
		if err := s.Transact(ctx, func(tx *Transaction) error {
			_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), request)
			return err
		}); err != nil {
			t.Fatalf("first workflow action with zero request time: %v", err)
		}
		if got := countWorkflowActor(t, s, agentRef); got != 1 {
			t.Fatalf("acting session actor rows=%d, want 1", got)
		}
		event, payload := readActorRecordedPrefixEvent(t, s, request.OperationID+":actor", version)
		if event.OccurredAt.IsZero() {
			t.Fatalf("actor prefix occurred_at is zero, want the normalized action time")
		}
		completedAt := readActorPrefixActionTime(t, s, request.OperationID+":completed")
		if !event.OccurredAt.Equal(completedAt) {
			t.Fatalf("actor prefix occurred_at=%v, want the action's normalized event time %v", event.OccurredAt, completedAt)
		}
		if payload.WorkID != workID || payload.ExpectedVersion == nil || *payload.ExpectedVersion != version || payload.ResultingVersion == nil || *payload.ResultingVersion != version+1 {
			t.Fatalf("actor prefix version cursor=%+v, want expected %d resulting %d", payload.WorkflowVersionFields, version, version+1)
		}
		if payload.ActorRef != agentRef || payload.PrincipalRef != agent.PrincipalRef || payload.ClientRef != agent.ClientRef || payload.AgentRef != agent.AgentRef || payload.SessionRef != agent.SessionRef || payload.ActorClass != string(ActorAgent) {
			t.Fatalf("actor prefix payload=%+v, want the authenticated tuple", payload)
		}
	})
}
