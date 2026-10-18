package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestOpsRunbookConditionResolutionAcrossHealthDispatch holds the retired
// ops-runbook condition flow on its frozen pin. The recorded route still
// folds: the approval chain reaches the execute step, the added condition
// holds the step projection open, the delivery record is the exit, the
// resolution is a versioned write, and the health record exits health. Every
// public action on the same nonterminal instance refuses — the archived
// family admits no new public workflow actions (read the recorded history, or
// start retained-family work). The live consequential-hold boundary the flow
// used to prove is held by TestExternalConditionHoldBlocksConsequentialExecution
// on the retained fixture family.
func TestOpsRunbookConditionResolutionAcrossHealthDispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID := "ops-runbook-condition-dispatch"
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:ops-test", ClientRef: "client:ops-test", AgentRef: "agent:ops-test", SessionRef: "session:ops-test", ActorClass: ActorAgent}
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.ops_runbook")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range definition.Definition.StepGraph.Steps {
		if !slices.Contains(step.Actions, "resolve_condition") || !slices.Contains(step.Actions, "cancel_condition") {
			t.Fatalf("step %q lacks universal condition actions: %v", step.ID, step.Actions)
		}
	}
	initTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, initTx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: now}); err != nil {
		_ = initTx.Rollback()
		t.Fatal(err)
	}
	if err := initTx.Commit(); err != nil {
		t.Fatal(err)
	}
	ownerRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}

	// The recorded route walks the approval chain to the execute step.
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "execute", ownerRef); err != nil {
		t.Fatalf("recorded walk to execute: %v", err)
	}

	// Adding a condition holds the step: the projection records the open
	// wait and no action moves the instance.
	authorityOp := "op-ops-runbook-condition"
	authorityEvidence := "evidence:ops-condition-authority"
	seedWorkflowTestConditionAuthority(t, s, workID, authorityOp, authorityEvidence)
	applyWorkflowTestConditionAdded(t, s, workID, ownerRef, "condition:health", authorityOp)
	assertOpsRunbookStep(t, s, "execute")
	assertWorkflowTestConditionState(t, s, workID, "condition:health", "open")
	assertArchivedOpsRunbookRefusal(t, s, workID, "execute", "record_delivery", actor)

	// The delivery record is the exit the recorded route still folds.
	applyWorkflowTestActionCompleted(t, s, workID, ownerRef, "execute", "record_delivery")
	assertOpsRunbookStep(t, s, "health")

	// Resolving the condition is a versioned write on the open projection;
	// the health record is the exit.
	beforeResolve := readWorkVersion(t, s, workID)
	applyWorkflowTestConditionResolved(t, s, workID, ownerRef, "condition:health", authorityEvidence)
	if afterResolve := readWorkVersion(t, s, workID); afterResolve <= beforeResolve {
		t.Fatalf("resolve_condition resulting version=%d, want an advanced version", afterResolve)
	}
	assertOpsRunbookStep(t, s, "health")
	applyWorkflowTestActionCompleted(t, s, workID, ownerRef, "health", "record_health")
	assertOpsRunbookStep(t, s, "rollback_optional")
	assertWorkflowTestConditionState(t, s, workID, "condition:health", "resolved")
	assertArchivedOpsRunbookRefusal(t, s, workID, "rollback_optional", "record_health", actor)
}

// TestExternalConditionHoldBlocksConsequentialExecution proves the live
// boundary behind the retired ops-runbook flow: an open external condition
// refuses the consequential action at the admission boundary, the refusal
// mutates nothing, and resolving the condition releases the action. Conditions
// reach a retained-family instance only through recorded events, exactly the
// shape a pinned item replays.
func TestExternalConditionHoldBlocksConsequentialExecution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "condition-hold-execution"
	s, workerRef, owner, _ := seedWorkerAtExecution(t, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	authorityOp := "op-condition-hold"
	authorityEvidence := "evidence:condition-hold-authority"
	seedWorkflowTestConditionAuthority(t, s, workID, authorityOp, authorityEvidence)
	applyWorkflowTestConditionAdded(t, s, workID, ownerRef, "condition:hold", authorityOp)
	heldVersion := readWorkVersion(t, s, workID)

	preflightErr := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: heldVersion, StepID: "execution",
		ActionID: "start_execution", Payload: json.RawMessage(`{}`), Actor: owner,
	})
	if preflightErr == nil || !strings.Contains(preflightErr.Error(), "consequential action has unresolved external conditions") {
		t.Fatalf("start_execution with an open condition error=%v, want boundary refusal", preflightErr)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("refused consequential action moved the step to %q", got)
	}
	if got := readWorkVersion(t, s, workID); got != heldVersion {
		t.Fatalf("refused consequential action moved the version to %d, want %d", got, heldVersion)
	}
	assertWorkflowTestConditionState(t, s, workID, "condition:hold", "open")

	applyWorkflowTestConditionResolved(t, s, workID, ownerRef, "condition:hold", authorityEvidence)
	assertWorkflowTestConditionState(t, s, workID, "condition:hold", "resolved")
	// The lane attempt is still live, so its holder restarts; another actor's
	// start would be a takeover (CD-0013 criterion 25).
	applyStartForTest(t, s, workID, workflowActorForRef(t, s, workerRef), readWorkVersion(t, s, workID), "condition-release-start")
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("released start moved the step to %q, want execution", got)
	}
}

// assertArchivedOpsRunbookRefusal proves the public route on the archived pin
// refuses before any write: the named action answers the archived-family
// invalid_operation and the instance stays put.
func assertArchivedOpsRunbookRefusal(t *testing.T, s *Store, workID, stepID, actionID string, actor WorkflowActor) {
	t.Helper()
	before := readWorkVersion(t, s, workID)
	err := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: before, StepID: stepID, ActionID: actionID, Payload: json.RawMessage(`{}`), Actor: actor,
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "workflow.ops_runbook is archived: new public workflow actions are refused") {
		t.Fatalf("%s preflight = %v, want the archived-family invalid_operation refusal", actionID, err)
	}
	if got := readWorkVersion(t, s, workID); got != before {
		t.Fatalf("archived preflight moved the version to %d, want %d", got, before)
	}
}

// seedWorkflowTestConditionAuthority records the durable operation a
// condition's resolution authority names. It uses the production durable
// claim/completion protocol, so the condition folds verify evidence against a
// real completed operation.
func seedWorkflowTestConditionAuthority(t *testing.T, s *Store, workID, opID, evidenceRef string) {
	t.Helper()
	var workflowTypeRef string
	var workflowTypeVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&workflowTypeRef, &workflowTypeVersion); err != nil {
		t.Fatal(err)
	}
	var currentStep string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	evidenceJSON, err := json.Marshal([]string{evidenceRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,1,?,?,?,?,'external_effect',?,?,'completed','{}',?,'principal/condition-authority',?,?,?,?)`,
		opID, workID, workflowTypeRef, workflowTypeVersion, currentStep, "sha256:condition-authority", "{}", string(evidenceJSON), "req-"+opID, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), testManifestDigest); err != nil {
		t.Fatalf("seed the condition authority operation: %v", err)
	}
}

// applyWorkflowTestConditionAdded records the typed condition-added event on
// the recorded route.
func applyWorkflowTestConditionAdded(t *testing.T, s *Store, workID, actorRef, conditionID, authorityOp string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	payload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"condition_id": conditionID, "await_type": "ci_result", "await_ref": "await:" + conditionID,
		"resolution_authority": "durable_operation:" + authorityOp,
	}
	event := workflowEventWithActor("condition-added-"+conditionID, WorkflowConditionAdded, workID, actorRef, payload)
	event.OccurredAt = time.Date(2026, 8, 24, 12, 1, 0, 0, time.UTC)
	if err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the condition: %v", err)
	}
}

// applyWorkflowTestConditionResolved records the typed condition-resolved
// event on the recorded route. The evidence must be bound to the stored
// authority operation, the same rule the public resolve_condition satisfies.
func applyWorkflowTestConditionResolved(t *testing.T, s *Store, workID, actorRef, conditionID, authorityEvidence string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	payload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"condition_id": conditionID, "resolution_evidence": []string{authorityEvidence},
		"resolved_by_event": "condition-resolved-" + conditionID,
	}
	event := workflowEventWithActor("condition-resolved-"+conditionID, WorkflowConditionResolved, workID, actorRef, payload)
	event.OccurredAt = time.Date(2026, 8, 24, 12, 2, 0, 0, time.UTC)
	if err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("resolve the condition: %v", err)
	}
}

// applyWorkflowTestActionCompleted records one step action's completed event
// on the recorded route.
func applyWorkflowTestActionCompleted(t *testing.T, s *Store, workID, actorRef, stepID, actionID string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	payload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": stepID, "action_id": actionID, "attempt_epoch": 1, "result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": actorRef}
	event := workflowEventWithActor(workID+":recorded:"+actionID, WorkflowActionCompleted, workID, actorRef, payload)
	if err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record %s: %v", actionID, err)
	}
}

func assertWorkflowTestConditionState(t *testing.T, s *Store, workID, conditionID, want string) {
	t.Helper()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id=? AND condition_id=?`, workID, conditionID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != want {
		t.Fatalf("condition state=%q, want %q", state, want)
	}
}

func assertOpsRunbookStep(t *testing.T, s *Store, want string) {
	t.Helper()
	var got string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, "ops-runbook-condition-dispatch").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("current step=%q, want %q", got, want)
	}
}
