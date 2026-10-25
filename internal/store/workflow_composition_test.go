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
	t.Parallel()
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

	version := readWorkVersion(t, s, sourceID)
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
	t.Parallel()
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

	version := readWorkVersion(t, s, sourceID)
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
	foldErr := foldWorkflowSuccessorLinked(ctx, tx, event)
	_ = tx.Rollback()
	assertUndeterminedSuccessorFamily(t, foldErr, "fold_event")
}

func TestWorkflowSuccessorLinkedCurrentVersionRequiresDefinitionRef(t *testing.T) {
	t.Parallel()
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
	version := readWorkVersion(t, s, sourceID)
	event := workflowEventWithActor("current-successor-link", WorkflowSuccessorLinked, sourceID, actorRef, map[string]any{
		"work_id": sourceID, "expected_version": version, "resulting_version": version + 1,
		"successor_work_id": successorID, "relation_kind": "forward_link", "successor_kind": "task",
	})
	event.PayloadVersion = eventKindRegistry[WorkflowSuccessorLinked].CurrentVersion
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	foldErr := applyEffect(ctx, tx, event)
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
	result, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: sourceID, ExpectedVersion: version, ActionID: "link_successor",
		Payload: json.RawMessage(`{"successor_work_id":"` + successorID + `"}`), Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "composition-link",
		OperationID: "composition-link", PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: "composition-link", RequestID: "request:composition-link", ContractDigest: testManifestDigest,
		Now: time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		_ = tx.Rollback()
		return WorkflowActionExecutionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkflowActionExecutionResult{}, err
	}
	return result, nil
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
