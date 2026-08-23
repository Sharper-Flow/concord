// CD-0059: tests proving the registered dispatch_worker action, the capability
// boundary that guards it, and the dispatch-window integrity check that
// closes the integrity hole a Task-tool spawn used to leave open.

package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestDispatchWorkerIsRegisteredWithTheD2Policy proves the brief's D2
// triple (ActionExternalEffect, ActionApprovalNone, ActionFenced,
// ActionEventGeneric) is the action policy in the built-in registry.
func TestDispatchWorkerIsRegisteredWithTheD2Policy(t *testing.T) {
	policy, ok := builtinActionPolicies["dispatch_worker"]
	if !ok {
		t.Fatal("dispatch_worker is not registered in builtinActionPolicies")
	}
	if policy.Consequence != ActionExternalEffect {
		t.Fatalf("dispatch_worker consequence = %s, want external_effect", policy.Consequence)
	}
	if policy.Approval != ActionApprovalNone {
		t.Fatalf("dispatch_worker approval = %s, want none", policy.Approval)
	}
	if policy.ExecutionMode != ActionFenced {
		t.Fatalf("dispatch_worker execution_mode = %s, want fenced", policy.ExecutionMode)
	}
	if policy.EventShape != ActionEventGeneric {
		t.Fatalf("dispatch_worker event_shape = %s, want generic", policy.EventShape)
	}
}

// TestDispatchWorkerCapabilityMatchesTheRegistryEntry proves the per-action
// capability is read from the action's RequiredCapability field. The
// mutation layer must not hard-code work_transition for every action; the
// dispatch action specifically requires worker_dispatch.
func TestDispatchWorkerCapabilityMatchesTheRegistryEntry(t *testing.T) {
	registry := BuiltinWorkflowRegistry()
	cases := []struct {
		actionID string
		want     string
	}{
		{actionID: "dispatch_worker", want: "worker_dispatch"},
		{actionID: "accept_worker_result", want: "work_transition"},
	}
	for _, c := range cases {
		var got string
		var seen bool
		for _, def := range []int64{2, 3, 4} {
			entry, ok := registry.Lookup("workflow.implementation", def)
			if !ok {
				continue
			}
			for _, a := range entry.Definition.ActionDefinitions {
				if a.ID == c.actionID {
					got = a.RequiredCapability
					seen = true
				}
			}
		}
		if !seen {
			t.Fatalf("action %q is not declared on workflow.implementation at v2/v3/v4", c.actionID)
		}
		if got != c.want {
			t.Fatalf("action %q required_capability = %q, want %q", c.actionID, got, c.want)
		}
	}
}

// TestDispatchFoldOpensAFencedWindowAgainstTheStepEpoch proves the brief's
// window mechanism: invoking dispatch_worker emits a WorkflowActionStarted
// event whose attempt_epoch is the next step epoch, so the worker-dispatch
// evidence boundary can later bind to the same step. The fold is the
// existing ActionFenced branch; this test pins its dispatch behavior so a
// future change cannot silently drop the fence.
func TestDispatchFoldOpensAFencedWindowAgainstTheStepEpoch(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-dispatch-fence")
	actor := seed.ownerActor
	result, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: json.RawMessage(`{"attempt_id":"attempt-fence"}`),
		Actor:   actor, AcceptedInputsDigest: cd0059TestDigest(t, "fence-inputs"),
		IdempotencyIdentity: "fence-open-op", OperationID: "op-fence-open", PrincipalRef: actor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "fence-open-key", RequestID: "req-fence-open",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	if err != nil {
		t.Fatalf("dispatch_worker invocation failed: %v", err)
	}
	if len(result.EventIDs) < 2 {
		t.Fatalf("dispatch_worker invocation emitted %d events, want >= 2 (started + completed)", len(result.EventIDs))
	}
	var actionID string
	var attemptEpoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.action_id'), json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE kind=? AND subject_id=? AND json_extract(payload,'$.step_id')=? ORDER BY seq DESC LIMIT 1`,
		"workflow.action_started", seed.workID, "execution").Scan(&actionID, &attemptEpoch); err != nil {
		t.Fatalf("cannot read workflow.action_started: %v", err)
	}
	if actionID != "dispatch_worker" {
		t.Fatalf("latest action_started action_id = %q, want dispatch_worker", actionID)
	}
	if attemptEpoch < 1 {
		t.Fatalf("attempt_epoch = %d, want >=1", attemptEpoch)
	}
}

// TestDispatchWorkerAppearsOnExternalEffectWorkflows proves the brief's
// instruction to append dispatch_worker to AvailableActions on workflows
// whose external_effect step is where workers run. Research workflows have
// no external_effect step (intentional per the early-return in
// builtinWorkflowV2), so dispatch_worker is intentionally absent there.
func TestDispatchWorkerAppearsOnExternalEffectWorkflows(t *testing.T) {
	registry := BuiltinWorkflowRegistry()
	wantHasDispatch := []string{
		"workflow.implementation",
		"workflow.break_fix",
		"workflow.architecture_spike",
		"workflow.ops_runbook",
		"workflow.static_analysis",
		"workflow.generic_one_off",
	}
	wantLacksDispatch := []string{"workflow.research"}
	for _, ref := range wantHasDispatch {
		entry, ok := registry.Lookup(ref, 4)
		if !ok {
			t.Fatalf("%s v4 is not registered", ref)
		}
		hasDispatch := false
		for _, action := range entry.Definition.AvailableActions {
			if action == "dispatch_worker" {
				hasDispatch = true
				break
			}
		}
		if !hasDispatch {
			t.Fatalf("%s v4 AvailableActions = %v, want dispatch_worker present", ref, entry.Definition.AvailableActions)
		}
	}
	for _, ref := range wantLacksDispatch {
		entry, ok := registry.Lookup(ref, 2)
		if !ok {
			t.Fatalf("%s v2 is not registered", ref)
		}
		for _, action := range entry.Definition.AvailableActions {
			if action == "dispatch_worker" {
				t.Fatalf("%s unexpectedly carries dispatch_worker (research has no external_effect step)", ref)
			}
		}
	}
}

// TestWorkerDispatchRefusesWithoutAnAuthorizedWindow proves the integrity
// fix: a worker-dispatch written for a work item that has a workflow
// instance but no dispatch_worker authorization at the current step is
// refused with KindUnauthorizedDispatch, and no worker_attempts row is
// written.
func TestWorkerDispatchRefusesWithoutAnAuthorizedWindow(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-no-window")
	if got := currentStep(t, s, seed.workID); got != "execution" {
		t.Fatalf("fixture step = %q, want execution", got)
	}
	version := readWorkVersion(t, s, seed.workID)
	if version != 4 {
		t.Fatalf("fixture version = %d, want 4", version)
	}
	err := s.Transact(ctx, func(tx *Transaction) error {
		return ValidateWorkerDispatchWindow(ctx, tx, seed.workID, "execution", "attempt-illegal")
	})
	if err == nil {
		t.Fatal("dispatch without an authorized window was accepted")
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed failure, got %v", err)
	}
	if failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("failure kind = %s, want unauthorized_dispatch", failure.Kind)
	}
	// No worker_attempts row was created.
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=?`, seed.workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("worker_attempts row count = %d, want 0", count)
	}
}

// TestWorkerDispatchRefusesAWorkItemWithNoWorkflowInstance proves the
// integrity invariant CD-0059 D5 relies on: a worker attempt belongs to a
// work item a workflow is executing, and a work item without a workflow
// instance is not in a state where dispatch is legal. The gate refuses with
// KindUnauthorizedDispatch and names the missing instance so an operator
// can tell it apart from a missing authorization, and no worker_attempts
// row is written.
func TestWorkerDispatchRefusesAWorkItemWithNoWorkflowInstance(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	// A work item with no workflow instance: seedWork folds the work item
	// and its primary membership, no DefinitionSelected event follows.
	seedWork(t, s, "work-no-instance")
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id=?`, "work-no-instance").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("seedWork pre-seeded %d workflow_instances row(s), want 0", count)
	}
	// stepID="" forces the gate to resolve current_step from the
	// workflow_instances row; that lookup returns sql.ErrNoRows because
	// the row does not exist, which is the hatch CD-0059 D5 closes.
	err := s.Transact(ctx, func(tx *Transaction) error {
		return ValidateWorkerDispatchWindow(ctx, tx, "work-no-instance", "", "attempt-illegal")
	})
	if err == nil {
		t.Fatal("dispatch against a work item with no workflow instance was accepted")
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed failure, got %v", err)
	}
	if failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("failure kind = %s, want unauthorized_dispatch", failure.Kind)
	}
	// The message must name the missing instance so an operator can tell
	// it apart from a missing authorization at an existing step.
	if !strings.Contains(failure.Detail, "workflow instance") {
		t.Fatalf("failure detail = %q, want it to name the missing workflow instance", failure.Detail)
	}
	// No worker_attempts row was created.
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=?`, "work-no-instance").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("worker_attempts row count = %d, want 0", count)
	}
}

// TestWorkerDispatchRejectsReuseOfAConsumedWindow proves the brief's
// single-use rule: one authorization admits exactly one attempt, and a
// second worker-dispatch against the same window is refused.
func TestWorkerDispatchRejectsReuseOfAConsumedWindow(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-reuse")
	// Open the dispatch window by invoking dispatch_worker.
	actor := seed.ownerActor
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: json.RawMessage(`{"attempt_id":"attempt-reuse"}`),
		Actor:   actor, AcceptedInputsDigest: cd0059TestDigest(t, "dispatch-inputs"),
		IdempotencyIdentity: "reuse-open-op", OperationID: "op-reuse-open", PrincipalRef: actor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "reuse-open-key", RequestID: "req-reuse-open",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}); err != nil {
		t.Fatalf("dispatch_worker failed: %v", err)
	}
	// First worker-dispatch check: the window is open.
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return ValidateWorkerDispatchWindow(ctx, tx, seed.workID, "execution", "attempt-reuse")
	}); err != nil {
		t.Fatalf("first dispatch_window check refused: %v", err)
	}
	// Consume the window by appending a worker.dispatched event for the
	// authorized attempt.
	if err := seedWorkerDispatchedForAttempt(t, s, seed.workID, "attempt-reuse"); err != nil {
		t.Fatal(err)
	}
	// Reuse must be refused.
	reuseErr := s.Transact(ctx, func(tx *Transaction) error {
		return ValidateWorkerDispatchWindow(ctx, tx, seed.workID, "execution", "attempt-reuse")
	})
	if reuseErr == nil {
		t.Fatal("second dispatch_window check was accepted; single-use violated")
	}
	var failure *Failure
	if !errors.As(reuseErr, &failure) {
		t.Fatalf("expected typed failure, got %v", reuseErr)
	}
	if failure.Kind != KindUnauthorizedDispatch {
		t.Fatalf("failure kind = %s, want unauthorized_dispatch", failure.Kind)
	}
}

// ---- helpers ----

type cd0059DispatchSeed struct {
	workID     string
	ownerActor WorkflowActor
}

// seedDispatchFixture seeds a workflow instance for workflow.implementation
// at the execution step, where dispatch_worker is a registered action. The
// caller drives workflow actions and worker-dispatch CLI commands against
// this fixture.
func seedDispatchFixture(t *testing.T, s *Store, workID string) cd0059DispatchSeed {
	t.Helper()
	ctx := context.Background()
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	seedWork(t, s, workID)
	digest := cd0059ImplementationDigest(t)
	events := []Event{
		workflowEvent("cd0059-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": "agent"}),
		workflowEvent("cd0059-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 4, "digest": digest, "work_kind": "implementation"}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	// CD-0059 fixture: pin the workflow instance to the execution step
	// inside one fold transaction, since the workflow_instances table is
	// fold-only. The dispatch action lives on the external_effect step
	// "execution"; advancing via the action itself would force the test
	// to drive the workflow through contract approval first.
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET current_step=? WHERE work_id=?`, "execution", workID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return cd0059DispatchSeed{workID: workID, ownerActor: actor}
}

// invokeWorkflowActionForCD0059 invokes a workflow action with a default
// idempotency identity and contract digest, isolating the test from the
// preflight machinery so the dispatch path is the only thing under test.
func invokeWorkflowActionForCD0059(ctx context.Context, t *testing.T, s *Store, request WorkflowActionExecutionRequest) (WorkflowActionExecutionResult, error) {
	t.Helper()
	preflight := WorkflowActionPreflightRequest{
		WorkID: request.WorkID, ExpectedVersion: request.ExpectedVersion, ActionID: request.ActionID,
		SelectedChoice: request.SelectedChoice, DecisionContextDigest: request.DecisionContextDigest,
		Payload: request.Payload, Actor: request.Actor,
	}
	var result WorkflowActionExecutionResult
	err := AuthorizeWorkflowActionAtBoundaryTx(ctx, s, BuiltinWorkflowRegistry(), preflight, nil, time.Time{}, nil, func(tx *Transaction) error {
		var inner error
		result, inner = ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), request)
		return inner
	})
	return result, err
}

// cd0059ImplementationDigest computes the v4 implementation digest, which
// includes the registered dispatch_worker action and RequiredCapability
// fields introduced by CD-0059.
func cd0059ImplementationDigest(t *testing.T) string {
	t.Helper()
	registry := BuiltinWorkflowRegistry()
	entry, ok := registry.Lookup("workflow.implementation", 4)
	if !ok {
		t.Fatal("workflow.implementation v4 is not registered")
	}
	return entry.Digest
}

// cd0059TestDigest returns a stable 64-char hex digest of the given label.
func cd0059TestDigest(t *testing.T, label string) string {
	t.Helper()
	pad := 64 - len(label)
	if pad < 0 {
		label = label[:64]
		pad = 0
	}
	return "sha256:" + label + strings.Repeat("a", pad)
}

// seedWorkerDispatchedForAttempt appends a worker.dispatched event directly
// so the dispatch window is consumed. The event bypasses the signed
// assertion path because the test fixture is asserting the window state,
// not the signature authority (which is exercised separately).
func seedWorkerDispatchedForAttempt(t *testing.T, s *Store, workID, attemptID string) error {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	payload := map[string]any{
		"attempt_id": attemptID, "lane_id": "research", "lane_version": int64(1),
		"lane_digest":      "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		"capability_class": "research", "readback_model": "openai/gpt-5.6-luna",
		"packet_schema_version": "1.0", "report_schema_version": "1.0",
	}
	raw, _ := json.Marshal(payload)
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES(?,?,?,?,?,?,?,?)`,
		"cd0059-consume-"+attemptID, "worker.dispatched", "work_item", workID,
		"client:cd0059:principal/operator", "2026-08-22T00:00:00Z", 3, string(raw)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
