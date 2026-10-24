package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkflowActorReferenceUsesDocumentedCanonicalNULEncoding(t *testing.T) {
	t.Parallel()
	parts := []string{"principal/operator", "client/concord-1", "agent/reviewer", "session/42"}
	canonical := "actor-v1\x00" + fmt.Sprintf("principal_ref=%d:%s|", len([]byte(parts[0])), parts[0]) + fmt.Sprintf("client_ref=%d:%s|", len([]byte(parts[1])), parts[1]) + fmt.Sprintf("agent_ref=%d:%s|", len([]byte(parts[2])), parts[2]) + fmt.Sprintf("session_ref=%d:%s|", len([]byte(parts[3])), parts[3])
	digest := sha256.Sum256([]byte(canonical))
	want := "actor:" + hex.EncodeToString(digest[:])
	if got := DeriveWorkflowActorRef(parts[0], parts[1], parts[2], parts[3]); got != want {
		t.Fatalf("actor_ref = %q, want %q", got, want)
	}
	if !strings.Contains(canonical, "actor-v1\x00") || strings.Contains(canonical, `actor-v1\\0`) {
		t.Fatal("canonical actor encoding did not retain exactly one NUL separator")
	}
}

func TestWorkflowEventsFoldAtomically(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "workflow-work")
	seedWorkflowAuthority(t, s, "condition-authority", "workflow-work", "principal/resolver", "request:condition", []string{"approval:one"})
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/1")
	implementationDigest := workflowFixtureDigest(t)
	base := int64(2)
	events := []Event{
		workflowEvent("actor", WorkflowActorRecorded, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": base, "resulting_version": base + 1,
			"actor_ref": actor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/1", "actor_class": "agent",
		}),
		workflowEvent("definition", WorkflowDefinitionSelected, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": 3, "resulting_version": 4,
			"ref": workflowFixtureRef, "version": 1, "digest": implementationDigest, "work_kind": workflowFixtureWorkKind,
		}),
		workflowEventWithActor("contract", WorkflowContractApproved, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 4, "resulting_version": 5,
			"contract_version": 1, "premise": "delivery is required", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:abc", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{"test"}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		}),
		workflowEventWithActor("start", WorkflowActionStarted, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 5, "resulting_version": 6, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "operation-1",
			"actor_ref": actor,
		}),
		workflowEventWithActor("checkpoint", WorkflowActionCheckpointed, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 6, "resulting_version": 7, "step_id": "execution", "step_kind": "external_effect", "attempt_epoch": 1, "checkpoint_payload": map[string]any{"action_id": "checkpoint_execution", "cursor": "one"}, "resume_cursor": "cursor:one", "actor_ref": actor, "request_id": "request:one", "checkpoint_id": "checkpoint:one", "accepted_inputs_digest": "sha256:" + strings.Repeat("c", 64), "idempotency_identity": "operation-checkpoint", "result_evidence_refs": []string{"evidence:one"},
		}),
		workflowEventWithActor("condition", WorkflowConditionAdded, "workflow-work", "authority:one", map[string]any{
			"work_id": "workflow-work", "expected_version": 7, "resulting_version": 8, "condition_id": "condition:one", "await_type": "ci_result", "await_ref": "run:one", "resolution_authority": "durable_operation:condition-authority",
		}),
		workflowEvent("operator", WorkflowActorRecorded, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": 8, "resulting_version": 9, "actor_ref": DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator"), "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/operator", "session_ref": "session/operator", "actor_class": "operator",
		}),
		workflowEventWithActor("cancel-condition", WorkflowConditionCancelled, "workflow-work", DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator"), map[string]any{
			"work_id": "workflow-work", "expected_version": 9, "resulting_version": 10, "condition_id": "condition:one", "cancellation_authority": "operator", "cancellation_evidence": []string{"approval:one"}, "cancelled_by_event": "event:cancelled",
		}),
		workflowEvent("impact", WorkflowImpactDeclared, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": 10, "resulting_version": 11, "edge_id": "edge:one", "edge_kind": "modifies", "edge_class": "none", "target_work_id": "workflow-work", "target_kind": "work_item", "severity": "informational",
		}),
		workflowEvent("notice", WorkflowImpactNoticeRecorded, "workflow-work", map[string]any{
			"work_id": "workflow-work", "expected_version": 11, "resulting_version": 12, "notice_id": WorkflowNoticeID("workflow-work", 1, "spec", "spec:one", "workflow-work", "informational"), "source_contract_version": 1, "entity_kind": "spec", "entity_ref": "spec:one", "target_work_id": "workflow-work", "edge_owner_work_id": "workflow-work", "edge_id": "edge:one", "old_hash": nil, "new_hash": nil, "severity": "informational",
		}),
		workflowEventWithActor("premise", WorkflowPremiseConfirmed, "workflow-work", DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator"), map[string]any{
			"work_id": "workflow-work", "expected_version": 12, "resulting_version": 13, "contract_version": 1, "confirming_actor_ref": DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator"), "confirmation_authority": "operator",
		}),
		workflowEventWithActor("candidate", WorkflowCandidateSetRevised, "workflow-work", actor, map[string]any{
			"work_id": "workflow-work", "expected_version": 13, "resulting_version": 14, "contract_version": 1, "candidate_kind": "work_item", "candidate_ref": "candidate:one", "added": []string{"candidate:one"}, "removed": []string{},
		}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "workflow-work"): base}}); err != nil {
		t.Fatalf("workflow operation: %v", err)
	}
	var advances int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='workflow-work' AND kind=? AND event_id LIKE 'workflow-work:fixture-walk:%'`, WorkflowActionCompleted).Scan(&advances); err != nil {
		t.Fatal(err)
	}
	if advances != 4 {
		t.Fatalf("workflow fixture step advances=%d, want 4", advances)
	}
	if got := readWorkVersion(t, s, "workflow-work"); got != 18 {
		t.Fatalf("workflow operation version=%d, want 18", got)
	}
}

func TestWorkflowPremiseConfirmationReentryUpdatesProjectionRow(t *testing.T) {
	s := openTemp(t)
	workID := "workflow-premise-reentry"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	ctx := context.Background()
	owner := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/1")
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/operator")
	digest := workflowFixtureDigest(t)
	confirmation := func(id string, expected int64, occurredAt time.Time) Event {
		event := workflowEventWithActor(id, WorkflowPremiseConfirmed, workID, operator, map[string]any{
			"work_id": workID, "expected_version": expected, "resulting_version": expected + 1,
			"contract_version": 1, "confirming_actor_ref": operator, "confirmation_authority": "operator",
		})
		event.OccurredAt = occurredAt
		return event
	}
	events := []Event{
		workflowEvent("reentry-actor", WorkflowActorRecorded, workID, map[string]any{
			"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": owner,
			"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/1", "actor_class": "agent",
		}),
		workflowEvent("reentry-operator", WorkflowActorRecorded, workID, map[string]any{
			"work_id": workID, "expected_version": 3, "resulting_version": 4, "actor_ref": operator,
			"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/operator", "session_ref": "session/operator", "actor_class": "operator",
		}),
		workflowEventWithActor("reentry-definition", WorkflowDefinitionSelected, workID, owner, map[string]any{
			"work_id": workID, "expected_version": 4, "resulting_version": 5,
			"ref": workflowFixtureRef, "version": 1, "digest": digest, "work_kind": workflowFixtureWorkKind,
		}),
		workflowEventWithActor("reentry-contract", WorkflowContractApproved, workID, owner, map[string]any{
			"work_id": workID, "expected_version": 5, "resulting_version": 6,
			"contract_version": 1, "premise": "delivery is required", "outcome_kind": "check",
			"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:reentry", "expected_result": "pass"},
			"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		}),
		confirmation("reentry-confirm-first", 6, time.Unix(10, 0).UTC()),
		confirmation("reentry-confirm-second", 7, time.Unix(20, 0).UTC()),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatalf("re-entered premise confirmation: %v", err)
	}

	var rows int
	var confirmedBy, confirmedAt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*), max(confirmed_by), max(confirmed_at) FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=1`, workID).Scan(&rows, &confirmedBy, &confirmedAt); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || confirmedBy != operator || confirmedAt != "1970-01-01T00:00:20Z" {
		t.Fatalf("premise projection rows=%d confirmed_by=%q confirmed_at=%q", rows, confirmedBy, confirmedAt)
	}
}

func TestWorkflowActorRowsRejectMutationAndDifferentTuple(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "actor-work")
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/1")
	event := workflowEvent("actor", WorkflowActorRecorded, "actor-work", map[string]any{
		"work_id": "actor-work", "expected_version": 2, "resulting_version": 3, "actor_ref": actor,
		"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/1", "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "actor-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE workflow_actors SET client_ref='client/other' WHERE actor_ref=?`, actor); err == nil {
		t.Fatal("direct actor update succeeded")
	}
	conflict := workflowEvent("actor-again", WorkflowActorRecorded, "actor-work", map[string]any{
		"work_id": "actor-work", "expected_version": 3, "resulting_version": 4, "actor_ref": actor,
		"principal_ref": "principal/operator", "client_ref": "client/other", "agent_ref": "agent/runner", "session_ref": "session/1", "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{conflict}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "actor-work"): 3}}); err == nil {
		t.Fatal("different actor tuple was accepted for an existing actor_ref")
	}
}

func TestWorkflowCompletedCannotBeAppendedThroughGenericEventAPI(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	event := workflowEvent("direct-completed", WorkflowCompleted, "work-not-created", map[string]any{
		"work_id": "work-not-created", "expected_version": 1, "resulting_version": 2,
		"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": "actor:" + strings.Repeat("a", 64),
		"premise_confirmed": true, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("b", 64),
	})
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
	assertFailureKind(t, err, KindWorkflowCompletionRequired)
	assertTableCount(t, s, "domain_events", 0)
}

func TestWorkflowCompletionGateUsesFirstRefusalAndRollsBackAllTerminalWrites(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "ordered-gate-work")
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/executor")
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/reviewer", "session/reviewer")
	digest := workflowFixtureDigest(t)
	events := []Event{
		workflowEvent("ordered-actor", WorkflowActorRecorded, "ordered-gate-work", map[string]any{
			"work_id": "ordered-gate-work", "expected_version": 2, "resulting_version": 3, "actor_ref": actor,
			"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/executor", "session_ref": "session/executor", "actor_class": "agent",
		}),
		workflowEvent("ordered-operator", WorkflowActorRecorded, "ordered-gate-work", map[string]any{
			"work_id": "ordered-gate-work", "expected_version": 3, "resulting_version": 4, "actor_ref": operator,
			"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/reviewer", "session_ref": "session/reviewer", "actor_class": "operator",
		}),
		workflowEvent("ordered-definition", WorkflowDefinitionSelected, "ordered-gate-work", map[string]any{
			"work_id": "ordered-gate-work", "expected_version": 4, "resulting_version": 5, "ref": workflowFixtureRef, "version": 1, "digest": digest, "work_kind": workflowFixtureWorkKind,
		}),
		workflowEventWithActor("ordered-contract", WorkflowContractApproved, "ordered-gate-work", actor, map[string]any{
			"work_id": "ordered-gate-work", "expected_version": 5, "resulting_version": 6, "contract_version": 1, "premise": "deliver the checked change",
			"outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:ordered", "expected_result": "pass"},
			"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		}),
		workflowEventWithActor("ordered-start", WorkflowActionStarted, "ordered-gate-work", actor, map[string]any{
			"work_id": "ordered-gate-work", "expected_version": 6, "resulting_version": 7, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "ordered-operation", "actor_ref": actor,
		}),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "ordered-gate-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	completionVersion := readWorkVersion(t, s, "ordered-gate-work")
	completion := workflowEventWithActor("ordered-completion", WorkflowCompleted, "ordered-gate-work", operator, map[string]any{
		"work_id": "ordered-gate-work", "expected_version": completionVersion, "resulting_version": completionVersion + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": actor,
		"premise_confirmed": true, "evidence_count": 1, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking",
	})
	completion.PayloadVersion = CurrentEventPayloadVersion(WorkflowCompleted)
	err := CompleteWorkflow(context.Background(), s, completion)
	assertFailureKind(t, err, KindMissingEvidence)
	assertTableCount(t, s, "domain_events", eventsBefore)
	assertTableCount(t, s, "workflow_impact_notices", 0)
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='ordered-gate-work'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state == "completed" {
		t.Fatal("first completion refusal changed the terminal projection")
	}
}

func TestWorkflowCompletionGateCommitsTerminalEventOnlyAfterAllClauses(t *testing.T) {
	t.Parallel()
	s, completion := seedCompletionGateCase(t, "complete-work", completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
	finalVersion := readWorkVersion(t, s, "complete-work")
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='complete-work'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("workflow state = %q, want completed", state)
	}
	// A replay that the action boundary's pre-preflight hook reports as handled
	// commits through the durable commit owner and never runs the complete
	// action again. The hook here detects the committed completion event.
	beforeReplay := s.DurableCommits()
	replayRan := false
	replay := WorkflowActionPreflightRequest{WorkID: "complete-work", ExpectedVersion: finalVersion, ActionID: "complete"}
	if err := AuthorizeWorkflowActionAtBoundaryWithPreflightTx(context.Background(), s, BuiltinWorkflowRegistry(), replay, func(transaction *Transaction) (bool, error) {
		tx, err := transactionSQL(transaction, "completion-replay")
		if err != nil {
			return false, err
		}
		var completed int
		if err := tx.QueryRowContext(context.Background(), `SELECT count(*) FROM domain_events WHERE event_id=? AND kind=?`, completion.EventID, WorkflowCompleted).Scan(&completed); err != nil {
			return false, err
		}
		return completed == 1, nil
	}, nil, func(*Transaction) error {
		replayRan = true
		return nil
	}); err != nil {
		t.Fatalf("completion replay: %v", err)
	}
	if replayRan {
		t.Fatal("completion replay ran the complete action a second time")
	}
	if after := s.DurableCommits(); after != beforeReplay+1 {
		t.Fatalf("completion replay bypassed the durable commit owner: %d -> %d", beforeReplay, after)
	}
	assertTableCount(t, s, "workflow_impact_notices", 0)
}

func TestWorkflowCompletionRequiresCurrentExplicitImpactVerdict(t *testing.T) {
	t.Parallel()
	t.Run("direct completion rejects v1", func(t *testing.T) {
		s, completion := seedCompletionGateCase(t, "completion-v1-refused", completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
		completion.PayloadVersion = 1
		var payload map[string]any
		if err := json.Unmarshal(completion.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		delete(payload, "impact_verdict")
		completion.Payload, _ = json.Marshal(payload)
		assertFailureKind(t, CompleteWorkflow(context.Background(), s, completion), KindUnsupportedPayloadVersion)
	})

	t.Run("complete action rejects omitted verdict", func(t *testing.T) {
		s, _ := seedCompletionGateCase(t, "completion-verdict-omitted", completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='completion-verdict-omitted'`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		actorRef := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/completion-verdict-omitted")
		_, err = workflowCompletionEvent(context.Background(), tx, WorkflowActionExecutionRequest{WorkID: "completion-verdict-omitted", ExpectedVersion: version, OperationID: "completion-verdict-omitted", Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}, actorRef, json.RawMessage(`{}`))
		assertFailureKind(t, err, KindInvalidPayload)
	})
}

func workflowCompletionEvent(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage) (Event, error) {
	registered, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, BuiltinWorkflowRegistry(), request.WorkID)
	if err != nil {
		return Event{}, err
	}
	return workflowCompletionEventForDefinition(ctx, tx, BuiltinWorkflowRegistry(), registered.Definition, request, actor, raw)
}

func TestWorkflowConditionResolutionMustUseStoredAuthority(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "condition-work")
	seedWorkflowAuthority(t, s, "condition-authority", "condition-work", "principal/resolver", "request:condition", []string{"evidence:valid"})
	added := workflowEventWithActor("condition-added", WorkflowConditionAdded, "condition-work", "principal/resolver", map[string]any{
		"work_id": "condition-work", "expected_version": 2, "resulting_version": 3, "condition_id": "condition-1", "await_type": "ci_result", "await_ref": "run:one", "resolution_authority": "durable_operation:condition-authority",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{added}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "condition-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	resolved := workflowEventWithActor("condition-resolved", WorkflowConditionResolved, "condition-work", "principal/resolver", map[string]any{
		"work_id": "condition-work", "expected_version": 3, "resulting_version": 4, "condition_id": "condition-1", "resolution_evidence": []string{"evidence:forged"}, "resolved_by_event": "provider-event",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{resolved}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "condition-work"): 3}}); err == nil {
		t.Fatal("condition resolved through an authority different from the stored resolver")
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id='condition-work' AND condition_id='condition-1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("condition state = %q, want open", state)
	}
}

type completionGateCase struct {
	name             string
	requiredEvidence []string
	includeSpec      bool
	emptyMandate     bool
	omitImpact       bool
	includeVerdict   bool
	includePremise   bool
	verdictKind      string
	openCondition    bool
	conflictNotice   bool
	wantKind         FailureKind
	wantClause       int
}

func TestWorkflowCompletionGateAdjacentClausePrecedenceAndRollback(t *testing.T) {
	t.Parallel()
	cases := []completionGateCase{
		{name: "clause-1-before-2", requiredEvidence: []string{"verification", "review", "native_run"}, openCondition: true, wantKind: KindMissingEvidence, wantClause: 1},
		{name: "clause-2-before-3", requiredEvidence: []string{"verification", "review"}, openCondition: true, wantKind: KindNotTerminal, wantClause: 2},
		{name: "clause-3-before-4", requiredEvidence: []string{"verification", "review"}, wantKind: KindInvariantViolation, wantClause: 3},
		{name: "clause-4-before-5", requiredEvidence: []string{"verification", "review"}, includeSpec: true, wantKind: KindMissingEvidence, wantClause: 4},
		{name: "clause-5-before-7", requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, verdictKind: "ok", conflictNotice: true, wantKind: KindMissingEvidence, wantClause: 5},
		{name: "clause-4-before-7", requiredEvidence: []string{"verification", "review"}, includeSpec: true, conflictNotice: true, wantKind: KindMissingEvidence, wantClause: 4},
		{name: "clause-7-rollback", requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok", conflictNotice: true, wantKind: KindOperationConflict, wantClause: 7},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			workID := "gate-" + strings.ReplaceAll(testCase.name, "-", "_")
			s, completion := seedCompletionGateCase(t, workID, testCase)
			var eventsBefore int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}
			err := CompleteWorkflow(context.Background(), s, completion)
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != testCase.wantKind || failure.Clause != testCase.wantClause {
				t.Fatalf("failure=%+v err=%v, want kind=%s clause=%d", failure, err, testCase.wantKind, testCase.wantClause)
			}
			assertTableCount(t, s, "domain_events", eventsBefore)
			assertTableCount(t, s, "workflow_impact_notices", 0)
		})
	}
}

// TestWorkflowActionBoundaryRefusesOpenConditionThroughAdmission holds CD-0234
// D2 at the owning boundary: the boundary takes no resolver, and a
// consequential action on work with an open condition answers the single
// admission refusal before authorization or mutation runs, writing nothing.
func TestWorkflowActionBoundaryRefusesOpenConditionThroughAdmission(t *testing.T) {
	t.Parallel()
	s, request := seedBoundaryActionRequest(t, "action-boundary-open-condition")
	var eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	called := false
	err := AuthorizeWorkflowActionAtBoundaryTx(context.Background(), s, BuiltinWorkflowRegistry(), request, func(*Transaction) error {
		called = true
		return nil
	}, func(*Transaction) error {
		called = true
		return nil
	})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindNotTerminal || failure.Detail != "consequential action has unresolved external conditions" || failure.Op != "workflow_action_preflight" {
		t.Fatalf("boundary error=%v failure=%+v, want the admission not_terminal refusal", err, failure)
	}
	if called {
		t.Fatal("boundary ran authorization or mutation on work with an open condition")
	}
	assertConditionState(t, s, "action-boundary-open-condition", "condition:gate", "open")
	assertTableCount(t, s, "domain_events", eventsBefore)
}

func TestWorkflowReadyReportsUnreadableConditionWithoutRewrite(t *testing.T) {
	t.Parallel()
	s, _ := seedCompletionGateCase(t, "ready-unreadable", completionGateCase{requiredEvidence: []string{"verification", "review"}, openCondition: true})
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM durable_operations WHERE op_id=?`, "gate-condition-ready-unreadable"); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	result, err := deriveWorkflowReady(context.Background(), s.DatabaseForTesting(), "ready-unreadable")
	if err != nil || result.Ready || len(result.UnknownConditions) != 1 || result.UnknownConditions[0] != "condition:gate" {
		t.Fatalf("ready result=%+v err=%v", result, err)
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("readiness derivation rewrote event history: before=%d after=%d", before, after)
	}
}

func TestWorkflowBlockingStalenessRefusesCompletionSemantics(t *testing.T) {
	t.Parallel()
	s, _ := seedCompletionGateCase(t, "staleness-block", completionGateCase{requiredEvidence: []string{"verification", "review"}})
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	definition := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	definition.StalenessRules = []WorkflowStalenessRule{{ID: "staleness:block", InputRef: "input:drifted", Severity: "block"}}
	err = verifyBlockingStaleness(context.Background(), tx, "staleness-block", definition, nil)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindStaleRequiresReview {
		t.Fatalf("staleness block failure=%+v err=%v", failure, err)
	}
}

func TestWorkflowWarningStalenessIsRecordedForNextRead(t *testing.T) {
	t.Parallel()
	s, _ := seedCompletionGateCase(t, "staleness-warning", completionGateCase{requiredEvidence: []string{"verification", "review"}})
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	definition := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	definition.StalenessRules = []WorkflowStalenessRule{{ID: "staleness:warning", InputRef: "input:drifted", Severity: "warning"}}
	warnings, err := workflowStalenessWarnings(context.Background(), tx, "staleness-warning", definition, nil)
	if err != nil || !reflect.DeepEqual(warnings, []string{"staleness:warning"}) {
		t.Fatalf("staleness warnings=%v err=%v", warnings, err)
	}
}

func TestWorkflowStalenessWarningsReadRecordedObservation(t *testing.T) {
	s, _ := seedCompletionGateCase(t, "staleness-rebuild", completionGateCase{requiredEvidence: []string{"verification", "review"}})
	fields := json.RawMessage(`{"staleness_rule_id":"staleness:warning","observed_drift":{"severity":"warning","drifted":true}}`)
	if err := AppendWorkflowStalenessObservation(context.Background(), s, "staleness-rebuild:observation", "staleness-rebuild", "actor:staleness", testManifestDigest, fields, time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	definition := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	definition.StalenessRules = []WorkflowStalenessRule{{ID: "staleness:warning", InputRef: "input:drifted", Severity: "warning"}}
	var before []string
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		before, err = workflowStalenessWarnings(context.Background(), tx.tx, "staleness-rebuild", definition, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, []string{"staleness:warning"}) {
		t.Fatalf("staleness warnings=%v", before)
	}
	var retired int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='workflow_staleness_warnings'`).Scan(&retired); err != nil {
		t.Fatal(err)
	}
	if retired != 0 {
		t.Fatal("retired staleness warning table remains")
	}
}

func TestWorkflowContractRevisionEmitsBreakingNoticeForConsumedActiveDependent(t *testing.T) {
	t.Parallel()
	source, actor := qContractNoticeFixture(t, "revision-source")
	seedWork(t, source, "revision-dependent")
	dependentActor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/dependent", "session/revision-dependent")
	digest := workflowFixtureDigest(t)
	dependentEvents := []Event{
		workflowEvent("revision-dependent-actor", WorkflowActorRecorded, "revision-dependent", map[string]any{"work_id": "revision-dependent", "expected_version": 2, "resulting_version": 3, "actor_ref": dependentActor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/dependent", "session_ref": "session/revision-dependent", "actor_class": "agent"}),
		workflowEvent("revision-dependent-definition", WorkflowDefinitionSelected, "revision-dependent", map[string]any{"work_id": "revision-dependent", "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 1, "digest": digest, "work_kind": workflowFixtureWorkKind}),
		workflowEventWithActor("revision-dependent-contract", WorkflowContractApproved, "revision-dependent", dependentActor, map[string]any{"work_id": "revision-dependent", "expected_version": 4, "resulting_version": 5, "contract_version": 1, "premise": "consume the source contract", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:dependent", "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("revision-dependent-start", WorkflowActionStarted, "revision-dependent", dependentActor, map[string]any{"work_id": "revision-dependent", "expected_version": 5, "resulting_version": 6, "step_id": "execution", "action_id": "start_execution", "attempt_epoch": 1, "accepted_inputs_digest": "sha256:" + strings.Repeat("b", 64), "idempotency_identity": "dependent-operation", "actor_ref": dependentActor}),
	}
	if err := applyWorkflowTestOperation(context.Background(), source, Operation{Events: dependentEvents, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "revision-dependent"): 2}}); err != nil {
		t.Fatal(err)
	}
	dependentVersion := readWorkVersion(t, source, "revision-dependent")
	var sourceVersion int64
	if err := source.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='revision-source'`).Scan(&sourceVersion); err != nil {
		t.Fatal(err)
	}
	edge := workflowEventWithActor("revision-edge", WorkflowImpactDeclared, "revision-dependent", dependentActor, map[string]any{"work_id": "revision-dependent", "expected_version": dependentVersion, "resulting_version": dependentVersion + 1, "edge_id": "edge:revision", "edge_kind": "depends_on", "edge_class": "hard", "target_work_id": "revision-source", "target_kind": "work_item", "severity": "informational"})
	if err := applyWorkflowTestOperation(context.Background(), source, Operation{Events: []Event{edge}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "revision-dependent"): dependentVersion}}); err != nil {
		t.Fatal(err)
	}
	supersede := workflowEventWithActor("revision-supersede", WorkflowContractSuperseded, "revision-source", actor, map[string]any{"work_id": "revision-source", "expected_version": sourceVersion, "resulting_version": sourceVersion + 1, "previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "refresh approved contract", "audit_evidence": []string{"evidence:audit"}})
	tx, err := source.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := applyWorkflowOperationTx(context.Background(), tx, Operation{Events: []Event{supersede}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "revision-source"): sourceVersion}}, newFoldScope(tx))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(result.EventIDs) != 2 {
		t.Fatalf("supersession event IDs = %v, want the contract event and dependent impact notice", result.EventIDs)
	}
	var resultingVersion int64
	if err := source.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='revision-source'`).Scan(&resultingVersion); err != nil {
		t.Fatal(err)
	}
	if resultingVersion != sourceVersion+2 {
		t.Fatalf("supersession committed version=%d, want %d", resultingVersion, sourceVersion+2)
	}
	var severity, entityKind string
	if err := source.DatabaseForTesting().QueryRow(`SELECT severity,entity_kind FROM workflow_impact_notices WHERE source_work_id='revision-source' AND target_work_id='revision-dependent'`).Scan(&severity, &entityKind); err != nil {
		t.Fatal(err)
	}
	if severity != "breaking" || entityKind != "workflow_contract" {
		t.Fatalf("revision notice=%s/%s, want breaking/workflow_contract", severity, entityKind)
	}
	var frontier, latest int64
	if err := source.DatabaseForTesting().QueryRow(`SELECT effect_seq,(SELECT MAX(seq) FROM domain_events WHERE subject_type='work_item' AND subject_id='revision-source') FROM work_items WHERE id='revision-source'`).Scan(&frontier, &latest); err != nil {
		t.Fatal(err)
	}
	if frontier != latest {
		t.Fatalf("effect frontier = %d, want the nested impact notice's sequence %d", frontier, latest)
	}
	var receiptSeq int64
	if err := source.DatabaseForTesting().QueryRow(`SELECT effect_seq FROM effect_receipts WHERE event_id=? AND kind=? AND subject_id='revision-source'`, result.EventIDs[1], WorkflowImpactNoticeRecorded).Scan(&receiptSeq); err != nil {
		t.Fatal(err)
	}
	if receiptSeq != latest {
		t.Fatalf("notice receipt sequence = %d, want %d", receiptSeq, latest)
	}
}

func TestWorkflowContractRevisionEmitsAdvisoryNoticesForOtherDependents(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name            string
		edgeClass       string
		includeContract bool
		terminal        bool
	}{
		{name: "soft active consumed", edgeClass: "soft", includeContract: true},
		{name: "hard active unconsumed", edgeClass: "hard"},
		{name: "hard inactive consumed", edgeClass: "hard", includeContract: true, terminal: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			sourceID := "revision-advisory-source-" + strings.ReplaceAll(testCase.name, " ", "-")
			dependentID := "revision-advisory-dependent-" + strings.ReplaceAll(testCase.name, " ", "-")
			s, actor := qContractNoticeFixture(t, sourceID)
			seedWork(t, s, dependentID)
			dependentActor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/dependent", "session/"+dependentID)
			digest := workflowFixtureDigest(t)
			version := int64(2)
			events := []Event{
				workflowEvent("actor-"+dependentID, WorkflowActorRecorded, dependentID, map[string]any{"work_id": dependentID, "expected_version": version, "resulting_version": version + 1, "actor_ref": dependentActor, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/dependent", "session_ref": "session/" + dependentID, "actor_class": "agent"}),
				workflowEvent("definition-"+dependentID, WorkflowDefinitionSelected, dependentID, map[string]any{"work_id": dependentID, "expected_version": version + 1, "resulting_version": version + 2, "ref": workflowFixtureRef, "version": 1, "digest": digest, "work_kind": workflowFixtureWorkKind}),
			}
			version += 2
			if testCase.includeContract {
				events = append(events, workflowEventWithActor("contract-"+dependentID, WorkflowContractApproved, dependentID, dependentActor, map[string]any{"work_id": dependentID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "premise": "consume source", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:dependent", "immutable_subject_ref": "commit:" + dependentID, "expected_result": "pass"}, "required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}))
				version++
			}
			events = append(events, workflowEventWithActor("edge-"+dependentID, WorkflowImpactDeclared, dependentID, dependentActor, map[string]any{"work_id": dependentID, "expected_version": version, "resulting_version": version + 1, "edge_id": "edge:" + dependentID, "edge_kind": "depends_on", "edge_class": testCase.edgeClass, "target_work_id": sourceID, "target_kind": "work_item", "severity": "informational"}))
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, dependentID): 2}}); err != nil {
				t.Fatal(err)
			}
			if testCase.terminal {
				if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='completed' WHERE work_id=?; DELETE FROM fold_guard`, dependentID); err != nil {
					t.Fatal(err)
				}
			}
			var sourceVersion int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, sourceID).Scan(&sourceVersion); err != nil {
				t.Fatal(err)
			}
			supersede := workflowEventWithActor("supersede-"+sourceID, WorkflowContractSuperseded, sourceID, actor, map[string]any{"work_id": sourceID, "expected_version": sourceVersion, "resulting_version": sourceVersion + 1, "previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "refresh", "audit_evidence": []string{"evidence:audit"}})
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{supersede}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, sourceID): sourceVersion}}); err != nil {
				t.Fatal(err)
			}
			var severity string
			if err := s.DatabaseForTesting().QueryRow(`SELECT severity FROM workflow_impact_notices WHERE source_work_id=? AND target_work_id=?`, sourceID, dependentID).Scan(&severity); err != nil {
				t.Fatal(err)
			}
			if severity != "non-breaking" {
				t.Fatalf("revision advisory severity = %q", severity)
			}
		})
	}
}

func seedBoundaryActionRequest(t *testing.T, workID string) (*Store, WorkflowActionPreflightRequest) {
	t.Helper()
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeVerdict: true, includePremise: true, emptyMandate: true, openCondition: true})
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	var version int64
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.version, i.current_step FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&version, &step); err != nil {
		t.Fatal(err)
	}
	return s, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, StepID: step, ActionID: "complete", Actor: actor}
}

func assertConditionState(t *testing.T, s *Store, workID, conditionID, want string) {
	t.Helper()
	var got string
	if err := s.DatabaseForTesting().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id=? AND condition_id=?`, workID, conditionID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("condition %s state=%q, want %q", conditionID, got, want)
	}
}

func seedWorkflowLaw(t *testing.T, s *Store) {
	t.Helper()
	var projectVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id='project'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	locatorPath := t.TempDir()
	normalized, err := NormalizeProjectLocator(LocatorCanonicalPath, locatorPath)
	if err != nil {
		t.Fatal(err)
	}
	// The synthetic knowledge home carries the canonical shard layout, so the
	// law context's registry locator resolves against a real file, and every
	// projected law document opens inside the home checkout.
	docsDir := filepath.Join(locatorPath, ".concord", "docs")
	if err := os.MkdirAll(filepath.Join(docsDir, "knowledge"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{"schema_version":"1.0","product_key":"product","root_domain_id":"root","domains":[]}`
	if err := os.WriteFile(filepath.Join(docsDir, "knowledge", "domain-registry.json"), []byte(registry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "knowledge", "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsDir, "spec.md"), []byte("# Synthetic test law\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{operationEvent("workflow-law-locator-added", "project.locator_added", SubjectProject, "project", map[string]any{
			"project_id": "project", "locator_id": "workflow-law-locator", "kind": string(LocatorCanonicalPath), "value": locatorPath, "normalized_value": normalized,
			"expected_version": projectVersion, "resulting_version": projectVersion + 1,
		})},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "project"): projectVersion},
	}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowLawProjection(t, s)
}

// seedWorkflowLawProjection writes the synthetic law home and law subject
// rows the fixture keeps outside the log. A test that rebuilds from the log
// calls it again, because the rebuild cannot refold rows no event recorded.
func seedWorkflowLawProjection(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT OR IGNORE INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product','project','workflow-law-locator'); INSERT OR IGNORE INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:one','spec','accepted','.concord/docs/spec.md','Synthetic test law','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','test'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func seedCompletionGateCase(t *testing.T, workID string, testCase completionGateCase) (*Store, Event) {
	t.Helper()
	extra := []string{}
	for _, kind := range testCase.requiredEvidence {
		if kind != "verification" {
			extra = append(extra, kind)
		}
	}
	f := qBuildAcceptance(t, workID, "", testCase.includeVerdict, extra...)
	s := f.store
	executor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	if testCase.includePremise {
		if err := f.run(t.Context(), "confirm_premise", "confirm", readWorkVersion(t, s, workID), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Fault the mutable gate inputs only after the real current Review and
	// proof path has run. Captured receipts and immutable events stay intact.
	appendEvent := func(event Event) {
		t.Helper()
		if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(workID, readWorkVersion(t, s, workID))}); err != nil {
			t.Fatal(err)
		}
	}
	if !testCase.omitImpact {
		version := readWorkVersion(t, s, workID)
		appendEvent(workflowEvent("gate-impact-"+workID, WorkflowImpactDeclared, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "edge_id": "edge:" + workID, "edge_kind": "modifies", "edge_class": "none", "target_work_id": workID, "target_kind": "work_item", "severity": "breaking"}))
	}
	if testCase.openCondition {
		seedWorkflowAuthority(t, s, "gate-condition-"+workID, workID, "principal/condition", "request/condition", []string{"evidence:condition"})
		version := readWorkVersion(t, s, workID)
		appendEvent(workflowEvent("gate-condition-"+workID, WorkflowConditionAdded, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "condition_id": "condition:gate", "await_type": "ci_result", "await_ref": "run:gate", "resolution_authority": "durable_operation:gate-condition-" + workID}))
	}
	if testCase.includeSpec {
		seedWorkflowAuthority(t, s, "gate-spec-"+workID, workID, "principal/spec", "request/spec", []string{"spec:one"})
		version := readWorkVersion(t, s, workID)
		appendEvent(workflowEvent("gate-spec-"+workID, WorkflowEvidenceBound, workID, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "artifact", "immutable_subject_ref": "spec:one", "producer_id": "principal/spec", "producer_run_ref": "gate-spec-" + workID, "producer_watermark": "request/spec", "observed_at": "2026-08-09T00:00:00Z"}))
	}
	if !testCase.emptyMandate {
		if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND superseded_by IS NULL; DELETE FROM fold_guard`, workID); err != nil {
			t.Fatal(err)
		}
	}
	if testCase.conflictNotice {
		dependentID := workID + "-dependent"
		seedImpactDependent(t, s, dependentID, workID, "hard")
		noticeID := WorkflowNoticeID(workID, 1, "work_item", workID, dependentID, "non-breaking")
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, "notice-event:"+noticeID, "work.intent_revised", string(SubjectWorkItem), workID, executor, "2026-08-09T00:00:00Z", 1, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	version := readWorkVersion(t, s, workID)
	completion := workflowEventWithActor("gate-completion-"+workID, WorkflowCompleted, workID, executor, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": executor, "premise_confirmed": testCase.includePremise, "evidence_count": 3, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking"})
	completion.PayloadVersion = CurrentEventPayloadVersion(WorkflowCompleted)
	return s, completion
}

func TestWorkflowEvidenceBindingRejectsCrossWorkAndProducerIdentityLaundering(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "evidence-work-a")
	seedWork(t, s, "evidence-work-b")
	seedWorkflowAuthority(t, s, "evidence-cross-work", "evidence-work-b", "principal/actual", "request/actual", []string{"evidence:shared"})
	crossWork := workflowEvent("evidence-cross-work-event", WorkflowEvidenceBound, "evidence-work-a", map[string]any{
		"work_id": "evidence-work-a", "expected_version": 2, "resulting_version": 3, "evidence_kind": "verification", "immutable_subject_ref": "evidence:shared", "producer_id": "principal/actual", "producer_run_ref": "evidence-cross-work", "producer_watermark": "request/actual", "observed_at": "2026-08-09T00:00:00Z",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{crossWork}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "evidence-work-a"): 2}}); err == nil {
		t.Fatal("evidence from another work item was accepted")
	}
	seedWorkflowAuthority(t, s, "evidence-identity", "evidence-work-a", "principal/actual", "request/actual", []string{"evidence:identity"})
	mismatchedIdentity := workflowEvent("evidence-identity-event", WorkflowEvidenceBound, "evidence-work-a", map[string]any{
		"work_id": "evidence-work-a", "expected_version": 2, "resulting_version": 3, "evidence_kind": "verification", "immutable_subject_ref": "evidence:identity", "producer_id": "principal/forged", "producer_run_ref": "evidence-identity", "producer_watermark": "request/actual", "observed_at": "2026-08-09T00:00:00Z",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{mismatchedIdentity}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "evidence-work-a"): 2}}); err == nil {
		t.Fatal("evidence with mismatched producer identity was accepted")
	}
}

func TestWorkflowCancellationRejectsForgedEvidenceFromOperator(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "cancel-evidence-work")
	seedWorkflowAuthority(t, s, "cancel-authority", "cancel-evidence-work", "principal/provider", "request/provider", []string{"evidence:real"})
	added := workflowEvent("cancel-condition-added", WorkflowConditionAdded, "cancel-evidence-work", map[string]any{
		"work_id": "cancel-evidence-work", "expected_version": 2, "resulting_version": 3, "condition_id": "condition:cancel", "await_type": "human_approval", "await_ref": "approval:pending", "resolution_authority": "durable_operation:cancel-authority",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{added}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cancel-evidence-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/operator", "session/cancel")
	actor := workflowEvent("cancel-operator", WorkflowActorRecorded, "cancel-evidence-work", map[string]any{
		"work_id": "cancel-evidence-work", "expected_version": 3, "resulting_version": 4, "actor_ref": operator, "principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/operator", "session_ref": "session/cancel", "actor_class": "operator",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{actor}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cancel-evidence-work"): 3}}); err != nil {
		t.Fatal(err)
	}
	cancel := workflowEventWithActor("cancel-forged", WorkflowConditionCancelled, "cancel-evidence-work", operator, map[string]any{
		"work_id": "cancel-evidence-work", "expected_version": 4, "resulting_version": 5, "condition_id": "condition:cancel", "cancellation_authority": "operator", "cancellation_evidence": []string{"evidence:forged"}, "cancelled_by_event": "event:forged",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{cancel}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "cancel-evidence-work"): 4}}); err == nil {
		t.Fatal("operator forged cancellation evidence was accepted")
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id='cancel-evidence-work' AND condition_id='condition:cancel'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("condition state = %q, want open", state)
	}
}

func seedWorkflowAuthority(t *testing.T, s *Store, opID, workID, principal, requestID string, evidence []string) {
	t.Helper()
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,1,?,'workflow.test',1,'evidence','internal_sqlite','digest','{}','completed','{}',?,'[]',?,?,?,? ,?)`, opID, workID, workflowJSON(evidence), principal, requestID, "2026-08-09T00:00:00Z", "2026-08-09T00:00:01Z", testManifestDigest)
	if err != nil {
		t.Fatalf("seed durable workflow authority: %v", err)
	}
}

func TestMigrateV14ToV15PreservesExistingData(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "v14.db")
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:14] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-08T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('v14-product','V14','prototype','operator_only',1,'created','updated');
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('v14-project','V14 project',1,'created','updated');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('v14-work','task','V14 work','needed',1,2,'created','updated'),('v14-work-2','task','V14 work 2','needed',1,1,'created','updated');
INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('v14-work','v14-work-2','blocks','created');
INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('v14-event','work.created','work_item','v14-work','operator','2026-08-08T00:00:00Z',2,'{"work_kind":"task","title":"V14 work","priority":1}');
DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRowContext(ctx, `SELECT display_name FROM products WHERE id='v14-product'`).Scan(&name); err != nil || name != "V14" {
		t.Fatalf("preserved v14 product = %q, error=%v", name, err)
	}
	var workTitle, relationKind, eventPayload string
	if err := db.QueryRowContext(ctx, `SELECT title FROM work_items WHERE id='v14-work'`).Scan(&workTitle); err != nil || workTitle != "V14 work" {
		t.Fatalf("preserved v14 work = %q, error=%v", workTitle, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT kind FROM relations WHERE work_id_from='v14-work' AND work_id_to='v14-work-2'`).Scan(&relationKind); err != nil || relationKind != "blocks" {
		t.Fatalf("preserved v14 relation = %q, error=%v", relationKind, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE event_id='v14-event'`).Scan(&eventPayload); err != nil || eventPayload != `{"work_kind":"task","title":"V14 work","priority":1}` {
		t.Fatalf("preserved v14 event = %q, error=%v", eventPayload, err)
	}
	if got, err := readSchemaManifestVersion(ctx, db); err != nil || got != CurrentSchemaVersion() {
		t.Fatalf("schema version = %d, error=%v, want %d", got, err, CurrentSchemaVersion())
	}
}

func TestWorkflowProjectionSchemaHasClosedChecksForeignKeysAndFoldGuards(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	expectedColumns := map[string][]string{
		"workflow_instances":             {"work_id", "definition_ref", "definition_version", "definition_digest", "current_step", "instance_state", "execution_actor_ref", "execution_model", "started_at", "completed_at", "last_checkpoint_at", "execution_started_at", "outside_repair_state", "result_disposition", "completion_payload", "completion_occurred_at", "unhealthy_verdict_seq", "latest_non_dispatch_start_seq"},
		"workflow_contracts":             {"work_id", "contract_version", "premise", "consequence_class", "required_evidence", "route_conventions", "approved_at", "approved_by", "superseded_by", "spec_mandate", "rigor_class", "law_modifies", "law_boundary_version", "self_repair_json", "definition_ref", "definition_version", "definition_digest", "decision_domains", "law_change", "supersession_seq", "last_healthy_seq"},
		"workflow_contract_predicates":   {"work_id", "contract_version", "predicate_id", "ordinal", "outcome_kind", "outcome_payload"},
		"workflow_candidate_sets":        {"work_id", "contract_version", "candidate_kind", "candidate_ref", "candidate_role", "candidate_scope", "recorded_at", "recorded_by"},
		"workflow_actors":                {"actor_ref", "principal_ref", "client_ref", "agent_ref", "session_ref", "actor_class", "first_seen_at"},
		"workflow_checkpoints":           {"work_id", "checkpoint_id", "step_id", "step_kind", "attempt_epoch", "accepted_inputs_digest", "result_evidence_refs", "resume_cursor", "idempotency_identity", "actor_ref", "request_id", "recorded_at"},
		"workflow_external_conditions":   {"work_id", "condition_id", "await_type", "await_ref", "resolution_authority", "condition_state", "resolution_evidence", "resolved_by_event", "cancellation_authority", "cancellation_evidence", "cancelled_by_event", "recorded_at", "resolved_at", "cancelled_at", "expected_within_seconds"},
		"workflow_impact_edges":          {"work_id", "edge_id", "edge_kind", "edge_class", "target_work_id", "target_kind", "severity", "recorded_at"},
		"workflow_impact_notices":        {"notice_id", "source_work_id", "source_contract_version", "entity_kind", "entity_ref", "target_work_id", "edge_owner_work_id", "edge_id", "old_hash", "new_hash", "severity", "recorded_at"},
		"workflow_decision_records":      {"work_id", "question", "options_considered", "decision", "rationale", "consequences", "inputs", "poc_findings", "supersedes", "superseded_by", "recorded_at"},
		"workflow_design_records":        {"work_id", "work_version", "approach", "decisions", "touched_refs", "recorded_at"},
		"workflow_premise_confirmations": {"work_id", "contract_version", "confirmed_by", "confirmed_at"},
	}
	expectedForeignKeys := map[string][]string{
		"workflow_instances": {"work_items", "workflow_actors"}, "workflow_contracts": {"work_items", "workflow_actors", "workflow_contracts", "workflow_contracts"},
		"workflow_candidate_sets": {"workflow_contracts", "workflow_contracts", "workflow_actors"}, "workflow_contract_predicates": {"workflow_contracts", "workflow_contracts"}, "workflow_actors": {}, "workflow_checkpoints": {"work_items", "workflow_actors"},
		"workflow_external_conditions": {"work_items"}, "workflow_impact_edges": {"work_items", "work_items"}, "workflow_impact_notices": {"work_items", "work_items", "work_items", "workflow_impact_edges", "workflow_impact_edges"},
		"workflow_decision_records": {"work_items"}, "workflow_design_records": {"work_items"}, "workflow_premise_confirmations": {"workflow_contracts", "workflow_contracts", "workflow_actors"},
	}
	expectedUniqueKeys := map[string][][]string{
		"workflow_instances": {{"work_id"}}, "workflow_contracts": {{"work_id", "contract_version"}}, "workflow_contract_predicates": {{"work_id", "contract_version", "predicate_id"}, {"work_id", "contract_version", "ordinal"}}, "workflow_candidate_sets": {{"work_id", "contract_version", "candidate_kind", "candidate_ref"}},
		"workflow_actors": {{"actor_ref"}, {"principal_ref", "client_ref", "agent_ref", "session_ref"}}, "workflow_checkpoints": {{"work_id", "checkpoint_id"}, {"work_id", "step_id", "attempt_epoch"}, {"work_id", "idempotency_identity"}},
		"workflow_external_conditions": {{"work_id", "condition_id"}}, "workflow_impact_edges": {{"work_id", "edge_id"}}, "workflow_impact_notices": {{"notice_id"}, {"source_work_id", "source_contract_version", "entity_kind", "entity_ref", "target_work_id", "severity"}},
		"workflow_decision_records": {{"work_id", "question"}}, "workflow_design_records": {{"work_id", "work_version"}}, "workflow_premise_confirmations": {{"work_id", "contract_version"}},
	}
	enumFragments := map[string][]string{
		"workflow_instances": {"'planned'", "'superseded'", "'outside_repair'", "'active'", "'resumed'"}, "workflow_contracts": {}, "workflow_contract_predicates": {"'exists'", "'check'"},
		"workflow_candidate_sets": {"'work_item'", "candidate_role IN ('include')"}, "workflow_actors": {"'agent'", "'operator'"},
		"workflow_checkpoints": {"'human_checkpoint'"}, "workflow_external_conditions": {"'pr_merge'", "'cancelled'"},
		"workflow_impact_edges": {"'depends_on'", "'work_item'"}, "workflow_impact_notices": {"'breaking'"},
		"workflow_decision_records": {"'accepted_decision'", "'insufficient_evidence'"}, "workflow_design_records": {}, "workflow_premise_confirmations": {},
	}
	for table, columns := range expectedColumns {
		t.Run(table, func(t *testing.T) {
			assertWorkflowColumns(t, s, table, columns)
			assertWorkflowForeignKeys(t, s, table, expectedForeignKeys[table])
			for _, key := range expectedUniqueKeys[table] {
				assertWorkflowUniqueKey(t, s, table, key)
			}
			var ddl string
			if err := s.DatabaseForTesting().QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(ddl, "CHECK") && table != "workflow_actors" {
				t.Fatalf("%s lacks CHECK constraints", table)
			}
			for _, fragment := range enumFragments[table] {
				if !strings.Contains(ddl, fragment) {
					t.Errorf("%s missing exact enum fragment %q", table, fragment)
				}
			}
			for _, action := range []string{"insert", "update", "delete"} {
				var count int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name=?`, table+"_guard_"+action).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Errorf("missing %s guard trigger", action)
				}
			}
			if table == "workflow_contracts" {
				// decision_domains arrived with ALTER TABLE alone, so the
				// table-level fold-only guards keep governing the column:
				// an outside-fold UPDATE of it must abort.
				seedWork(t, s, "fold-guard-decision-domains")
				if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES('actor:' || hex(zeroblob(32)),'principal:fold-guard-dd','client:fold-guard-dd','agent:fold-guard-dd','session:fold-guard-dd','agent','now');
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('fold-guard-decision-domains',1,'fold guard decision domains','internal_sqlite','[]','[]','now','actor:' || hex(zeroblob(32)),'[]','[]',0,'prototype_internal');
DELETE FROM fold_guard`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.DatabaseForTesting().Exec(`UPDATE workflow_contracts SET decision_domains='[]' WHERE work_id='fold-guard-decision-domains' AND contract_version=1`); err == nil {
					t.Fatal("an outside-fold UPDATE of decision_domains was accepted")
				} else if !strings.Contains(err.Error(), "fold-only") {
					t.Fatalf("an outside-fold UPDATE of decision_domains failed with %v, want the fold-only guard", err)
				}
			}
		})
	}
}

func TestWorkflowImpactTargetKindRejectsProductAndProject(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "target-kind-work")
	for _, targetKind := range []string{"product", "project"} {
		t.Run(targetKind, func(t *testing.T) {
			tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := enterFold(context.Background(), tx); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			_, err = tx.Exec(`INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at) VALUES('target-kind-work',?,?,?,'target-kind-work',?,'informational','now')`, "edge:"+targetKind, "modifies", "none", targetKind)
			if err == nil {
				tx.Rollback()
				t.Fatalf("target_kind=%s was accepted", targetKind)
			}
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
		})
	}
}

func assertWorkflowUniqueKey(t *testing.T, s *Store, table string, want []string) {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`PRAGMA index_list(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	var indexes []string
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if unique == 1 {
			indexes = append(indexes, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	for _, name := range indexes {
		info, err := s.DatabaseForTesting().Query(`PRAGMA index_info(` + name + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for info.Next() {
			var seqno, cid int
			var column string
			if err := info.Scan(&seqno, &cid, &column); err != nil {
				info.Close()
				t.Fatal(err)
			}
			got = append(got, column)
		}
		if err := info.Err(); err != nil {
			info.Close()
			t.Fatal(err)
		}
		info.Close()
		if strings.Join(got, "|") == strings.Join(want, "|") {
			return
		}
	}
	t.Fatalf("%s lacks unique key %v", table, want)
}

func assertWorkflowColumns(t *testing.T, s *Store, table string, want []string) {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("columns = %v, want %v", got, want)
	}
}

func assertWorkflowForeignKeys(t *testing.T, s *Store, table string, want []string) {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`PRAGMA foreign_key_list(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, seq int
		var ref, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &seq, &ref, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		got = append(got, ref)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("foreign keys = %v, want %v", got, want)
	}
}

func TestConcurrentWorkflowActorAppendsUseTheSingleWriter(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	for i := 0; i < 8; i++ {
		seedWork(t, s, fmt.Sprintf("concurrent-work-%d", i))
	}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for i := 0; i < 8; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			workID := fmt.Sprintf("concurrent-work-%d", i)
			actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", fmt.Sprintf("agent/%d", i), fmt.Sprintf("session/%d", i))
			event := workflowEvent("concurrent-actor-"+fmt.Sprint(i), WorkflowActorRecorded, workID, map[string]any{
				"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actor,
				"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": fmt.Sprintf("agent/%d", i), "session_ref": fmt.Sprintf("session/%d", i), "actor_class": "agent",
			})
			errors <- applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}})
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent workflow append: %v", err)
		}
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 8 {
		t.Fatalf("workflow actor rows = %d, want 8", count)
	}
}

func TestWorkflowGoverningCycleCombinesDependsOnAndForwardLink(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "mixed-a")
	seedWork(t, s, "mixed-b")
	first := workflowEvent("mixed-depends", WorkflowImpactDeclared, "mixed-a", map[string]any{
		"work_id": "mixed-a", "expected_version": 2, "resulting_version": 3, "edge_id": "edge-depends", "edge_kind": "depends_on", "edge_class": "hard", "target_work_id": "mixed-b", "target_kind": "work_item", "severity": "non-breaking",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{first}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "mixed-a"): 2}}); err != nil {
		t.Fatal(err)
	}
	second := workflowEvent("mixed-forward", WorkflowImpactDeclared, "mixed-b", map[string]any{
		"work_id": "mixed-b", "expected_version": 2, "resulting_version": 3, "edge_id": "edge-forward", "edge_kind": "forward_link", "edge_class": "hard", "target_work_id": "mixed-a", "target_kind": "work_item", "severity": "informational",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{second}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "mixed-b"): 2}}); err == nil {
		t.Fatal("mixed depends_on/forward_link cycle was accepted")
	} else {
		assertFailureKind(t, err, KindCycleDetected)
	}
	assertTableCount(t, s, "workflow_impact_edges", 1)
}

func TestWorkflowPoisonEventLeavesAllProjectionsAtomic(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "poison-work")
	actor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/runner", "session/poison")
	event := workflowEvent("poison-actor", WorkflowActorRecorded, "poison-work", map[string]any{
		"work_id": "poison-work", "expected_version": 2, "resulting_version": 3, "actor_ref": actor,
		"principal_ref": "principal/operator", "client_ref": "client/concord-1", "agent_ref": "agent/runner", "session_ref": "session/poison", "actor_class": "agent",
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "poison-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	before := fullWorkflowProjectionSnapshot(t, s)
	poison := workflowEvent("poison-v2", WorkflowActorRecorded, "poison-work", map[string]any{"work_id": "poison-work", "expected_version": 3, "resulting_version": 4})
	poison.PayloadVersion = 2
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{poison}, ExpectedVersions: workVersion("poison-work", 3)}); err == nil {
		t.Fatal("poison workflow event was admitted")
	}
	after := fullWorkflowProjectionSnapshot(t, s)
	if after != before {
		t.Fatalf("refused poison event changed projections:\nbefore=%s\nafter=%s", before, after)
	}
}

func fullWorkflowProjectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	tables := []string{
		"work_items", "relations", "workflow_instances", "workflow_contracts", "workflow_candidate_sets", "workflow_actors",
		"workflow_checkpoints", "workflow_external_conditions", "workflow_impact_edges", "workflow_impact_notices",
		"workflow_decision_records", "workflow_design_records", "workflow_premise_confirmations",
	}
	var snapshot strings.Builder
	for _, table := range tables {
		var columnCount int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM pragma_table_info(?)`, table).Scan(&columnCount); err != nil {
			t.Fatalf("columns for %s: %v", table, err)
		}
		order := make([]string, columnCount)
		for i := range order {
			order[i] = fmt.Sprintf("%d", i+1)
		}
		rows, err := s.DatabaseForTesting().Query(`SELECT * FROM ` + table + ` ORDER BY ` + strings.Join(order, ","))
		if err != nil {
			t.Fatalf("snapshot %s: %v", table, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("columns for %s: %v", table, err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", table, err)
			}
			snapshot.WriteString(table)
			for _, value := range values {
				snapshot.WriteByte('|')
				switch typed := value.(type) {
				case nil:
					snapshot.WriteString("NULL")
				case []byte:
					snapshot.WriteString(fmt.Sprintf("bytes:%x", typed))
				default:
					snapshot.WriteString(fmt.Sprintf("%T:%v", typed, typed))
				}
			}
			snapshot.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s: %v", table, err)
		}
		rows.Close()
	}
	return snapshot.String()
}

func workflowEvent(id, kind, workID string, payload map[string]any) Event {
	event := operationEvent(id, kind, SubjectWorkItem, workID, payload)
	event.Actor = "actor:test"
	return event
}

func workflowEventWithActor(id, kind, workID, actor string, payload map[string]any) Event {
	event := workflowEvent(id, kind, workID, payload)
	event.Actor = actor
	return event
}

// Consent fixtures resolve the current family, just like live work.
func legacyApprovalPolicyDefinitionForRef(t testing.TB, ref string) RegisteredDefinition {
	t.Helper()
	entry, ok := BuiltinWorkflowRegistry().Current(ref)
	if !ok {
		t.Fatalf("%s has no current definition", ref)
	}
	return entry
}

// withPinnedDecisionFact declares the explicit engineering decision-domain
// fact on a seeded contract approval, or on a seeded supersession's successor
// contract, when the pinned definition answers consent under consequence_v1,
// at the payload version that carries the fact. A legacy pin keeps the seeded
// bytes.
func withPinnedDecisionFact(t *testing.T, definition WorkflowDefinition, event Event) Event {
	t.Helper()
	if !workflowApprovalPolicyRequiresDecisionFact(definition) {
		return event
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	contract, factVersion := payload, eventKindRegistry[WorkflowContractApproved].CurrentVersion
	if event.Kind == WorkflowContractSuperseded {
		successor, ok := payload["successor_contract"].(map[string]any)
		if !ok {
			t.Fatal("seeded supersession carries no successor contract")
		}
		contract, factVersion = successor, eventKindRegistry[WorkflowContractSuperseded].CurrentVersion
	}
	if _, declared := contract["decision_domains"]; !declared {
		contract["decision_domains"] = []string{}
	}
	event.Payload = mustJSONValue(payload)
	event.PayloadVersion = max(event.PayloadVersion, factVersion)
	return event
}
