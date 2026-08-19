package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func issue31WorkflowAction(t *testing.T, s *Store, workID string, version int64, actionID, operationID string, actor WorkflowActor) int64 {
	return issue31WorkflowActionWithPayload(t, s, workID, version, actionID, operationID, actor, json.RawMessage(`{}`))
}

func issue31WorkflowActionWithPayload(t *testing.T, s *Store, workID string, version int64, actionID, operationID string, actor WorkflowActor, payload json.RawMessage) int64 {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(context.Background(), tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	result, err := applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:issue31", IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractVersion: "4.0.0", Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
	})
	_ = leaveFold(context.Background(), tx)
	if err != nil {
		tx.Rollback()
		t.Fatalf("action %s: %v", actionID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result.ResultingVersion
}

func TestGenericApplyOperationRejectsEveryReservedWorkflowEvent(t *testing.T) {
	s := openTemp(t)
	var reserved []string
	for kind, registration := range eventKindRegistry {
		if registration.Authority == EventAppendAuthorityWorkflow {
			reserved = append(reserved, kind)
		}
	}
	for _, kind := range reserved {
		err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
			EventID: "reserved-" + kind, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: "work-issue31",
			Actor: "actor:issue31", OccurredAt: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: json.RawMessage(`{}`),
		}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-issue31"): 0}})
		if err == nil {
			t.Fatalf("ApplyOperation accepted reserved event %q", kind)
		}
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind LIKE 'workflow.%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("reserved event append left %d rows", count)
	}
}

func TestWorkflowActionSemanticEventsAreFollowedByUniversalCompletion(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "workflow-issue31-actions")
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:issue31", AgentRef: "agent:issue31", SessionRef: "session:issue31", ActorClass: ActorAgent}
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: "workflow-issue31-actions", Definition: registered, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := int64(4)
	for _, action := range []string{"record_proposal", "record_discovery", "record_design"} {
		version = issue31WorkflowAction(t, s, "workflow-issue31-actions", version, action, "issue31-"+action, actor)
	}
	approval := json.RawMessage(`{"spec_mandate":[],"law_modifies":[],"architecture_binding":{"domain_registry_content_hash":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}}`)
	version = issue31WorkflowActionWithPayload(t, s, "workflow-issue31-actions", version, "approve_contract", "issue31-approve", actor, approval)
	if version != 9 {
		t.Fatalf("approve_contract resulting version=%d, want 9", version)
	}
	version = issue31WorkflowAction(t, s, "workflow-issue31-actions", version, "bind_evidence", "issue31-evidence", actor)
	if version != 11 {
		t.Fatalf("bind_evidence resulting version=%d, want 11", version)
	}
	rows, err := s.DatabaseForTesting().Query(`SELECT kind FROM domain_events WHERE subject_id=? AND (kind=? OR kind=? OR kind=?) ORDER BY seq`, "workflow-issue31-actions", WorkflowContractApproved, WorkflowEvidenceBound, WorkflowActionCompleted)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
	}
	want := []string{WorkflowActionCompleted, WorkflowActionCompleted, WorkflowActionCompleted, WorkflowContractApproved, WorkflowActionCompleted, WorkflowEvidenceBound, WorkflowActionCompleted}
	if len(kinds) != len(want) {
		t.Fatalf("semantic event order=%v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("event[%d]=%q, want %q", i, kinds[i], want[i])
		}
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild workflow event log: %v", err)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, "workflow-issue31-actions").Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "execution" {
		t.Fatalf("rebuilt workflow current_step=%q, want execution", step)
	}
}

func seedIssue31DomainRegistry(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("b", 64)
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator','product','root','1.0',?,'test')`, hash); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','root','Root','Product law','current',?,'test')`, hash); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowContractApprovalPinsLawBoundaryForLegacySurface(t *testing.T) {
	s := openTemp(t)
	workID := "workflow-law-pin-legacy-surface"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:law-pin", ClientRef: "client:law-pin", AgentRef: "agent:law-pin", SessionRef: "session:law-pin", ActorClass: ActorAgent}
	registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 3)
	if !ok {
		t.Fatal("historical v3 implementation definition is not registered")
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	version := int64(4)
	for _, action := range []string{"record_proposal", "record_discovery", "record_design"} {
		version = issue31WorkflowAction(t, s, workID, version, action, "law-pin-"+action, actor)
	}
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "approve_contract", "law-pin-approve", actor, json.RawMessage(`{"spec_mandate":["spec:one"]}`))

	var boundaryVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT law_boundary_version FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&boundaryVersion); err != nil {
		t.Fatal(err)
	}
	if boundaryVersion != 1 {
		t.Fatalf("law boundary version=%d, want 1 for a new contract submitted through the legacy surface", boundaryVersion)
	}
	var pinnedHash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id=? AND contract_version=1 AND law_id='spec:one'`, workID).Scan(&pinnedHash); err != nil || pinnedHash != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("legacy-surface approval law pin=%q err=%v", pinnedHash, err)
	}
	if version == 0 {
		t.Fatal("approval route did not produce a resulting version")
	}
}

func issue31ActorSetup(t *testing.T, workID string) (*Store, WorkflowActor, WorkflowActor, int64) {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	executor := WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:issue31", AgentRef: "agent:executor", SessionRef: "session:executor", ActorClass: ActorAgent}
	evaluator := WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:issue31", AgentRef: "agent:evaluator", SessionRef: "session:evaluator", ActorClass: ActorOperator}
	definition, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("legacy implementation definition is not registered")
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: executor, Now: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	evaluatorRef, err := WorkflowActorRef(evaluator)
	if err != nil {
		t.Fatal(err)
	}
	actorPayload := map[string]any{"work_id": workID, "expected_version": 4, "resulting_version": 5, "actor_ref": evaluatorRef, "principal_ref": evaluator.PrincipalRef, "client_ref": evaluator.ClientRef, "agent_ref": evaluator.AgentRef, "session_ref": evaluator.SessionRef, "actor_class": string(evaluator.ActorClass)}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor(workID+":evaluator", WorkflowActorRecorded, workID, evaluatorRef, actorPayload)}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatal(err)
	}
	return s, executor, evaluator, 5
}

func TestWorkflowAuthenticatedActorBindingRejectsSpoofedVerdictAndPremise(t *testing.T) {
	spoofed := []struct {
		name  string
		actor WorkflowActor
	}{
		{name: "operator row", actor: WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:issue31", AgentRef: "agent:evaluator", SessionRef: "session:evaluator", ActorClass: ActorOperator}},
		{name: "same principal different process", actor: WorkflowActor{PrincipalRef: "principal:issue31", ClientRef: "client:other", AgentRef: "agent:other", SessionRef: "session:other", ActorClass: ActorAgent}},
		{name: "unknown actor", actor: WorkflowActor{PrincipalRef: "principal:unknown", ClientRef: "client:unknown", AgentRef: "agent:unknown", SessionRef: "session:unknown", ActorClass: ActorAgent}},
	}
	for _, testCase := range spoofed {
		t.Run("verdict "+testCase.name, func(t *testing.T) {
			s, executor, evaluator, version := issue31ActorSetup(t, "spoof-verdict-"+strings.ReplaceAll(testCase.name, " ", "-"))
			spoofRef, err := WorkflowActorRef(testCase.actor)
			if err != nil {
				t.Fatal(err)
			}
			event := workflowEventWithActor("spoof-verdict", WorkflowVerdictRecorded, "spoof-verdict-"+strings.ReplaceAll(testCase.name, " ", "-"), executorRefForIssue31(t, executor), map[string]any{"work_id": "spoof-verdict-" + strings.ReplaceAll(testCase.name, " ", "-"), "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "predicate_id": "predicate:spoof", "verdict_kind": "ok", "verdict_actor_ref": spoofRef, "evaluation_evidence": []string{"evidence:spoof"}, "incomparable_with_approved": false})
			if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): version}}); err == nil {
				t.Fatal("spoofed verdict was accepted")
			}
			assertIssue31VersionAndNoVerdict(t, s, event.SubjectID, version)
			_ = evaluator
		})
	}

	s, executor, evaluator, version := issue31ActorSetup(t, "valid-evaluator")
	evaluatorRef, _ := WorkflowActorRef(evaluator)
	valid := workflowEventWithActor("valid-verdict", WorkflowVerdictRecorded, "valid-evaluator", evaluatorRef, map[string]any{"work_id": "valid-evaluator", "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "predicate_id": "predicate:valid", "verdict_kind": "ok", "verdict_actor_ref": evaluatorRef, "evaluation_evidence": []string{"evidence:valid"}, "incomparable_with_approved": false})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{valid}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "valid-evaluator"): version}}); err != nil {
		t.Fatalf("valid independent evaluator rejected: %v", err)
	}
	assertIssue31VersionAndVerdict(t, s, "valid-evaluator", version+1)
	_ = executor

	s, executor, evaluator, version = issue31ActorSetup(t, "valid-operator-premise")
	evaluatorRef, _ = WorkflowActorRef(evaluator)
	contract := workflowEventWithActor("valid-contract", WorkflowContractApproved, "valid-operator-premise", evaluatorRef, map[string]any{"work_id": "valid-operator-premise", "expected_version": version, "resulting_version": version + 1, "contract_version": 1, "premise": "operator premise", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + strings.Repeat("a", 64), "expected_result": "pass"}, "required_evidence": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{contract}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "valid-operator-premise"): version}}); err != nil {
		t.Fatal(err)
	}
	premise := workflowEventWithActor("valid-premise", WorkflowPremiseConfirmed, "valid-operator-premise", evaluatorRef, map[string]any{"work_id": "valid-operator-premise", "expected_version": version + 1, "resulting_version": version + 2, "contract_version": 1, "confirming_actor_ref": evaluatorRef})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{premise}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "valid-operator-premise"): version + 1}}); err != nil {
		t.Fatalf("valid authenticated operator rejected: %v", err)
	}
	_ = executor
}

func executorRefForIssue31(t *testing.T, actor WorkflowActor) string {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func assertIssue31VersionAndNoVerdict(t *testing.T, s *Store, workID string, want int64) {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var verdicts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&verdicts); err != nil {
		t.Fatal(err)
	}
	if version != want || verdicts != 0 {
		t.Fatalf("spoof changed state: version=%d verdict_projection=%d, want version=%d and no verdict projection", version, verdicts, want)
	}
}

func assertIssue31VersionAndVerdict(t *testing.T, s *Store, workID string, want int64) {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if version != want || events != 1 {
		t.Fatalf("valid evaluator state: version=%d verdict_events=%d", version, events)
	}
}
