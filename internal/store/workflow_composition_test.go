package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWorkflowSuccessorUsesDefinitionFamilyForComposition(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	sourceID := "composition-source"
	successorID := "composition-successor"
	actor := WorkflowActor{PrincipalRef: "principal:composition", ClientRef: "client:composition", AgentRef: "agent:composition", SessionRef: "session:composition", ActorClass: ActorAgent}
	seedWork(t, s, sourceID)
	seedWork(t, s, successorID)
	initializeCompositionWorkflow(t, s, sourceID, "workflow.implementation", actor)
	initializeCompositionWorkflow(t, s, successorID, "workflow.break_fix", actor)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, sourceID, "execution", actorRef); err != nil {
		t.Fatal(err)
	}
	var sourceKind, successorKind string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, sourceID).Scan(&sourceKind); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, successorID).Scan(&successorKind); err != nil {
		t.Fatal(err)
	}
	if sourceKind != "task" || successorKind != "task" {
		t.Fatalf("work item kinds = (%q, %q), want both operator kind task", sourceKind, successorKind)
	}

	version := readWorkflowWorkVersion(t, s, sourceID)
	result, err := executeCompositionLink(ctx, s, sourceID, successorID, version, actor)
	if err != nil {
		t.Fatalf("forward link with an allowed successor family was refused: %v", err)
	}
	if result.ResultingVersion != version+2 {
		t.Fatalf("link resulting version = %d, want %d", result.ResultingVersion, version+2)
	}
	var eventKind, payloadKind, payloadDefinition string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT kind,json_extract(payload,'$.successor_kind'),json_extract(payload,'$.definition_ref') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, sourceID, WorkflowSuccessorLinked).Scan(&eventKind, &payloadKind, &payloadDefinition); err != nil {
		t.Fatal(err)
	}
	if eventKind != WorkflowSuccessorLinked || payloadKind != "task" || payloadDefinition != "workflow.break_fix" {
		t.Fatalf("link event = (%q, %q, %q), want successor_kind task and workflow.break_fix", eventKind, payloadKind, payloadDefinition)
	}
}

func TestWorkflowSuccessorWithoutWorkflowInstanceIsRejected(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	sourceID := "missing-instance-source"
	successorID := "missing-instance-successor"
	actor := WorkflowActor{PrincipalRef: "principal:missing-instance", ClientRef: "client:missing-instance", AgentRef: "agent:missing-instance", SessionRef: "session:missing-instance", ActorClass: ActorAgent}
	seedWork(t, s, sourceID)
	seedWork(t, s, successorID)
	initializeCompositionWorkflow(t, s, sourceID, "workflow.implementation", actor)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, sourceID, "execution", actorRef); err != nil {
		t.Fatal(err)
	}

	version := readWorkflowWorkVersion(t, s, sourceID)
	_, err = executeCompositionLink(ctx, s, sourceID, successorID, version, actor)
	assertUndeterminedSuccessorFamily(t, err, "workflow_action")

	event := workflowEventWithActor("missing-instance-fold", WorkflowSuccessorLinked, sourceID, actorRef, map[string]any{
		"work_id": sourceID, "expected_version": version, "resulting_version": version + 1,
		"successor_work_id": successorID, "relation_kind": "forward_link", "successor_kind": "task",
	})
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	foldErr := foldWorkflowSuccessorLinked(ctx, tx, event)
	_ = leaveFold(ctx, tx)
	_ = tx.Rollback()
	assertUndeterminedSuccessorFamily(t, foldErr, "fold_event")
}

func TestWorkflowSuccessorLinkedV1RebuildPreservesUnpinnedResearchSuccessor(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	sourceID := "legacy-composition-source"
	successorID := "legacy-composition-successor"
	actor := WorkflowActor{PrincipalRef: "principal:legacy-composition", ClientRef: "client:legacy-composition", AgentRef: "agent:legacy-composition", SessionRef: "session:legacy-composition", ActorClass: ActorAgent}
	seedWork(t, s, sourceID)
	created := operationEvent("create-"+successorID, "work.created", SubjectWorkItem, successorID, map[string]any{
		"work_kind": "research", "title": successorID, "priority": 10,
	})
	created.PayloadVersion = 2
	if err := ApplyOperation(ctx, s, Operation{
		Events: []Event{
			created,
			operationEvent("membership-"+successorID, "work_project.added", SubjectWorkItem, successorID, map[string]any{
				"work_id": successorID, "project_id": "project", "role": "secondary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, successorID): 0},
	}); err != nil {
		t.Fatal(err)
	}
	initializeCompositionWorkflow(t, s, sourceID, "workflow.implementation", actor)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkflowWorkVersion(t, s, sourceID)
	payload, err := json.Marshal(map[string]any{
		"work_id": sourceID, "expected_version": version, "resulting_version": version + 1,
		"successor_work_id": successorID, "relation_kind": "forward_link", "successor_kind": "research",
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyEvent := Event{
		EventID: "legacy-successor-link", Kind: WorkflowSuccessorLinked, SubjectType: SubjectWorkItem,
		SubjectID: sourceID, Actor: actorRef, OccurredAt: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC),
		PayloadVersion: 1, Payload: payload,
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	liveErr := foldRegisteredEvent(ctx, tx, legacyEvent)
	_ = leaveFold(ctx, tx)
	_ = tx.Rollback()
	if liveErr == nil {
		t.Fatal("live workflow.successor_linked v1 bypassed the definition_ref requirement")
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		legacyEvent.EventID, legacyEvent.Kind, legacyEvent.SubjectType, legacyEvent.SubjectID, legacyEvent.Actor,
		legacyEvent.OccurredAt.Format(time.RFC3339Nano), legacyEvent.PayloadVersion, legacyEvent.Payload); err != nil {
		t.Fatal(err)
	}

	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild refused historically valid workflow.successor_linked v1: %v", err)
	}
	var relationCount, storedVersion int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE work_id_from=? AND work_id_to=? AND kind='forward_link'`, sourceID, successorID).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT payload_version FROM domain_events WHERE event_id='legacy-successor-link'`).Scan(&storedVersion); err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 || storedVersion != 1 {
		t.Fatalf("legacy replay relation count=%d stored payload version=%d, want 1 and 1", relationCount, storedVersion)
	}
}

func TestWorkflowSuccessorLinkedCurrentVersionRequiresDefinitionRef(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	sourceID := "current-composition-source"
	successorID := "current-composition-successor"
	actor := WorkflowActor{PrincipalRef: "principal:current-composition", ClientRef: "client:current-composition", AgentRef: "agent:current-composition", SessionRef: "session:current-composition", ActorClass: ActorAgent}
	seedWork(t, s, sourceID)
	seedWork(t, s, successorID)
	initializeCompositionWorkflow(t, s, sourceID, "workflow.implementation", actor)
	initializeCompositionWorkflow(t, s, successorID, "workflow.research", actor)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkflowWorkVersion(t, s, sourceID)
	event := workflowEventWithActor("current-successor-link", WorkflowSuccessorLinked, sourceID, actorRef, map[string]any{
		"work_id": sourceID, "expected_version": version, "resulting_version": version + 1,
		"successor_work_id": successorID, "relation_kind": "forward_link", "successor_kind": "task",
	})
	event.PayloadVersion = eventKindRegistry[WorkflowSuccessorLinked].CurrentVersion
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	foldErr := foldRegisteredEvent(ctx, tx, event)
	_ = leaveFold(ctx, tx)
	_ = tx.Rollback()
	if foldErr == nil {
		t.Fatal("current workflow.successor_linked accepted an empty definition_ref")
	}
	var failure *Failure
	if !errors.As(foldErr, &failure) || failure.Kind != KindInvalidRelation {
		t.Fatalf("current empty definition_ref failure=%v", foldErr)
	}
}

func initializeCompositionWorkflow(t *testing.T, s *Store, workID, definitionRef string, actor WorkflowActor) {
	t.Helper()
	definition, err := BuiltinWorkflowDefinitionForRef(definitionRef)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{
		WorkID: workID, Definition: definition, Actor: actor,
		Now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func executeCompositionLink(ctx context.Context, s *Store, sourceID, successorID string, version int64, actor WorkflowActor) (WorkflowActionExecutionResult, error) {
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return WorkflowActionExecutionResult{}, err
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return WorkflowActionExecutionResult{}, err
	}
	result, err := applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: sourceID, ExpectedVersion: version, ActionID: "link_successor",
		Payload: json.RawMessage(`{"successor_work_id":"` + successorID + `"}`), Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "composition-link",
		OperationID: "composition-link", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: "composition-link", RequestID: "request:composition-link", ContractDigest: testManifestDigest,
		Now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	})
	_ = leaveFold(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		return WorkflowActionExecutionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowActionExecutionResult{}, err
	}
	return result, nil
}

func readWorkflowWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func assertUndeterminedSuccessorFamily(t *testing.T, err error, stage string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s accepted a successor without a workflow instance", stage)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("%s returned %T, want typed failure: %v", stage, err, err)
	}
	if failure.Kind != KindInvalidRelation {
		t.Fatalf("%s failure kind = %s, want %s", stage, failure.Kind, KindInvalidRelation)
	}
	if failure.Detail != "successor has no workflow instance, so its family is undetermined" {
		t.Fatalf("%s failure detail = %q", stage, failure.Detail)
	}
	if failure.RecoveryAction != "select a workflow definition for the successor before linking it" {
		t.Fatalf("%s failure recovery = %q", stage, failure.RecoveryAction)
	}
}
