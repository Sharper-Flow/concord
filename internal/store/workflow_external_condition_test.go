package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// An unresolved external condition holds consequential execution without
// changing the work. Only evidence from its resolution authority releases it.
func TestExternalConditionHoldBlocksConsequentialExecution(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "condition-hold-execution"
	s, executorRef, owner := seedOrdinaryExecutorAtExecution(t, workID)
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
	// The execution fence is still live, so its holder restarts; another actor's
	// start would be a takeover (CD-0013 criterion 25).
	applyStartForTest(t, s, workID, workflowActorForRef(t, s, executorRef), readWorkVersion(t, s, workID), "condition-release-start")
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("released start moved the step to %q, want execution", got)
	}
}

// seedWorkflowTestConditionAuthority records the durable operation a
// condition's resolution authority names. It uses the production durable
// claim/completion protocol, so the condition folds verify evidence against a
// real completed operation.
func seedWorkflowTestConditionAuthority(t *testing.T, s *Store, workID, opID, evidenceRef string) {
	t.Helper()
	var workflowTypeRef string
	var workflowTypeDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&workflowTypeRef, &workflowTypeDigest); err != nil {
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
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,1,?,?,?,?,'external_effect',?,?,'completed','{}',?,'principal/condition-authority',?,?,?,?)`,
		opID, workID, workflowTypeRef, workflowTypeDigest, currentStep, "sha256:condition-authority", "{}", string(evidenceJSON), "req-"+opID, at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), testManifestDigest); err != nil {
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
