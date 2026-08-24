// CD-0059: tests proving the registered dispatch_worker action, the capability
// boundary that guards it, and the dispatch-window integrity check that
// closes the integrity hole a Task-tool spawn used to leave open.

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		for _, def := range []int64{1} {
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
			t.Fatalf("action %q is not declared on workflow.implementation", c.actionID)
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
	packetPayload, err := json.Marshal(dispatchWorkerPacket(seed.workID, "execution", "attempt-fence"))
	if err != nil {
		t.Fatalf("marshal dispatch packet: %v", err)
	}
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt-fence", "worker_packet": json.RawMessage(packetPayload)})
	if err != nil {
		t.Fatalf("marshal dispatch fields: %v", err)
	}
	result, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload,
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
		entry, ok := registry.Lookup(ref, 1)
		if !ok {
			t.Fatalf("%s is not registered", ref)
		}
		hasDispatch := false
		for _, action := range entry.Definition.AvailableActions {
			if action == "dispatch_worker" {
				hasDispatch = true
				break
			}
		}
		if !hasDispatch {
			t.Fatalf("%s AvailableActions = %v, want dispatch_worker present", ref, entry.Definition.AvailableActions)
		}
	}
	for _, ref := range wantLacksDispatch {
		entry, ok := registry.Lookup(ref, 1)
		if !ok {
			t.Fatalf("%s is not registered", ref)
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
	packetPayload, err := json.Marshal(dispatchWorkerPacket(seed.workID, "execution", "attempt-reuse"))
	if err != nil {
		t.Fatalf("marshal dispatch packet: %v", err)
	}
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt-reuse", "worker_packet": json.RawMessage(packetPayload)})
	if err != nil {
		t.Fatalf("marshal dispatch fields: %v", err)
	}
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload,
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

// TestDispatchFoldRecordsTheCanonicalPacketDigest proves CD-0067 D2: a
// successful dispatch_worker fold writes worker_packet_digest on the
// WorkflowActionCompleted event, computed as sha256 over canonicalJSON of the
// packet the caller supplied. The digest is what later worker-evidence
// boundaries compare against the worker's reported packet.
func TestDispatchFoldRecordsTheCanonicalPacketDigest(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-digest")
	actor := seed.ownerActor
	packet := dispatchWorkerPacket(seed.workID, "execution", "attempt-digest")
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	canonical, err := canonicalJSON(packetBytes)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	wantSum := sha256.Sum256(canonical)
	wantDigest := "sha256:" + hex.EncodeToString(wantSum[:])
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt-digest", "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload,
		Actor:   actor, AcceptedInputsDigest: cd0059TestDigest(t, "digest-inputs"),
		IdempotencyIdentity: "digest-open-op", OperationID: "op-digest-open", PrincipalRef: actor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "digest-open-key", RequestID: "req-digest-open",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}); err != nil {
		t.Fatalf("dispatch_worker failed: %v", err)
	}
	var storedDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(payload,'$.worker_packet_digest'),'') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=? AND json_extract(payload,'$.step_id')=?`,
		string(SubjectWorkItem), seed.workID, WorkflowActionCompleted, "dispatch_worker", "execution").Scan(&storedDigest); err != nil {
		t.Fatalf("cannot read workflow.action_completed: %v", err)
	}
	if storedDigest != wantDigest {
		t.Fatalf("worker_packet_digest = %q, want %q", storedDigest, wantDigest)
	}
}

// TestDispatchFoldRefusesPacketWorkIDMismatch proves CD-0067 D2 identity guard:
// a dispatch_worker whose worker_packet.work_id does not match the action's
// work_id is refused with KindInvalidPayload and writes no events.
func TestDispatchFoldRefusesPacketWorkIDMismatch(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-mismatch")
	packet := dispatchWorkerPacket("work-other", "execution", "attempt-mismatch")
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt-mismatch", "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	actor := seed.ownerActor
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload,
		Actor:   actor, AcceptedInputsDigest: cd0059TestDigest(t, "mismatch-inputs"),
		IdempotencyIdentity: "mismatch-op", OperationID: "op-mismatch", PrincipalRef: actor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "mismatch-key", RequestID: "req-mismatch",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	if err == nil {
		t.Fatal("dispatch_worker with mismatched packet.work_id was accepted")
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed failure, got %v", err)
	}
	if failure.Kind != KindInvalidPayload {
		t.Fatalf("failure kind = %s, want invalid_payload", failure.Kind)
	}
	var startedCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=?`,
		string(SubjectWorkItem), seed.workID, WorkflowActionStarted, "dispatch_worker").Scan(&startedCount); err != nil {
		t.Fatal(err)
	}
	if startedCount != 0 {
		t.Fatalf("dispatch_worker.started event count = %d, want 0 after identity refusal", startedCount)
	}
}

// TestDispatchFoldRefusesPacketAttemptIDMismatch proves CD-0067 D2 identity
// guard: a dispatch_worker whose worker_packet.attempt_id does not match
// fields.attempt_id is refused with KindInvalidPayload and writes no events.
func TestDispatchFoldRefusesPacketAttemptIDMismatch(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-attempt-mismatch")
	packet := dispatchWorkerPacket(seed.workID, "execution", "attempt-other")
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt-fields", "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	actor := seed.ownerActor
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload,
		Actor:   actor, AcceptedInputsDigest: cd0059TestDigest(t, "attempt-mismatch-inputs"),
		IdempotencyIdentity: "attempt-mismatch-op", OperationID: "op-attempt-mismatch", PrincipalRef: actor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "attempt-mismatch-key", RequestID: "req-attempt-mismatch",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	if err == nil {
		t.Fatal("dispatch_worker with mismatched packet.attempt_id was accepted")
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed failure, got %v", err)
	}
	if failure.Kind != KindInvalidPayload {
		t.Fatalf("failure kind = %s, want invalid_payload", failure.Kind)
	}
	var startedCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')=?`,
		string(SubjectWorkItem), seed.workID, WorkflowActionStarted, "dispatch_worker").Scan(&startedCount); err != nil {
		t.Fatal(err)
	}
	if startedCount != 0 {
		t.Fatalf("dispatch_worker.started event count = %d, want 0 after identity refusal", startedCount)
	}
}

// TestDispatchPreflightRejectsNonObjectWorkerPacket proves CD-0067 D3: the
// preflight enforces the PayloadObject value type, so a non-object
// worker_packet is refused with the existing "wrong registered type or
// bounds" failure detail before the fold runs. The owning preflight wraps
// payload errors in its own typed kind; the test pins the detail string so
// a future change cannot quietly let a string-shaped worker_packet reach
// the fold.
func TestDispatchPreflightRejectsNonObjectWorkerPacket(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-non-object")
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": "attempt-non-object", "worker_packet": "not-an-object"})
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	actor := seed.ownerActor
	_, err = invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload,
		Actor:   actor, AcceptedInputsDigest: cd0059TestDigest(t, "non-object-inputs"),
		IdempotencyIdentity: "non-object-op", OperationID: "op-non-object", PrincipalRef: actor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "non-object-key", RequestID: "req-non-object",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	if err == nil {
		t.Fatal("dispatch_worker with non-object worker_packet was accepted")
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("expected typed failure, got %v", err)
	}
	if !strings.Contains(failure.Detail, "wrong registered type") {
		t.Fatalf("failure detail = %q, want it to name the wrong registered type", failure.Detail)
	}
}

// ---- helpers ----

type cd0059DispatchSeed struct {
	workID     string
	ownerActor WorkflowActor
}

// dispatchWorkerPacket builds the closed lane packet the dispatch_worker fold
// expects under fields.worker_packet. CD-0067 D1/D2 require the packet's
// work_id, attempt_id, and step_id to match the action's work_id, the
// fields.attempt_id, and the seeded execution step; the helper enforces those
// equalities from its arguments so callers cannot ship a packet the fold
// refuses on identity grounds.
func dispatchWorkerPacket(workID, stepID, attemptID string) map[string]any {
	return map[string]any{
		"schema_version": "1.0",
		"attempt_id":     attemptID,
		"lane_id":        "implement",
		"lane_version":   int64(1),
		"lane_digest":    "sha256:" + strings.Repeat("a", 64),
		"work_id":        workID,
		"step_id":        stepID,
		"inputs": map[string]any{
			"task":        "cd0065 dispatch packet",
			"constraints": []string{"do-not-modify-product-truth"},
		},
	}
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
		workflowEvent("cd0059-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 1, "digest": digest, "work_kind": "implementation"}),
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
	entry, ok := registry.Lookup("workflow.implementation", 1)
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

// TestFindAuthorizedDispatchWindowSurfacesThePacketDigest proves the read
// round-trip the post-merge audit of #446 found missing: the worker packet
// digest the dispatch_worker fold records onto the completing
// WorkflowActionCompleted event (TestDispatchFoldRecordsTheCanonicalPacketDigest,
// the write-side pin) is the same value FindAuthorizedDispatchWindowTx reads
// off that row into WorkerDispatchWindow.PacketDigest (the read side). The
// test runs a real dispatch_worker invocation end-to-end, then opens a
// store transaction and calls FindAuthorizedDispatchWindowTx directly, so
// any drift in the SELECT column expression, the payload key, or the struct
// field becomes a hard failure rather than a silent gap.
func TestFindAuthorizedDispatchWindowSurfacesThePacketDigest(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-window-digest")
	actor := seed.ownerActor
	attemptID := "attempt-window-digest"
	packet := dispatchWorkerPacket(seed.workID, "execution", attemptID)
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	canonical, err := canonicalJSON(packetBytes)
	if err != nil {
		t.Fatalf("canonicalJSON: %v", err)
	}
	wantSum := sha256.Sum256(canonical)
	wantDigest := "sha256:" + hex.EncodeToString(wantSum[:])
	fieldsPayload, err := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	// Open the dispatch window through the real fold so the digest is
	// recorded by production code (not seeded into a domain_events row).
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload,
		Actor:   actor, AcceptedInputsDigest: cd0059TestDigest(t, "window-digest-inputs"),
		IdempotencyIdentity: "window-digest-op", OperationID: "op-window-digest", PrincipalRef: actor.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "window-digest-key", RequestID: "req-window-digest",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}); err != nil {
		t.Fatalf("dispatch_worker fold: %v", err)
	}
	// Read the window back through the production queryer that runs
	// inside a worker-dispatch CLI transaction. cd0059 dispatch tests
	// open transactions via s.Transact and rely on transactionSQL to
	// expose the underlying *sql.Tx to Tx-scoped helpers; follow that
	// pattern rather than reaching for the *sql.Tx directly.
	var (
		window    WorkerDispatchWindow
		windowErr error
	)
	transactErr := s.Transact(ctx, func(tx *Transaction) error {
		sqlTx, err := transactionSQL(tx, "test_window_digest")
		if err != nil {
			return err
		}
		window, windowErr = FindAuthorizedDispatchWindowTx(ctx, sqlTx, seed.workID, "execution")
		return windowErr
	})
	if transactErr != nil {
		t.Fatalf("FindAuthorizedDispatchWindowTx: %v", transactErr)
	}
	if window.AttemptID != attemptID {
		t.Fatalf("window.AttemptID = %q, want %q", window.AttemptID, attemptID)
	}
	if window.AttemptEpoch < 1 {
		t.Fatalf("window.AttemptEpoch = %d, want >=1 (the step epoch recorded by the fold)", window.AttemptEpoch)
	}
	if window.PacketDigest != wantDigest {
		t.Fatalf("window.PacketDigest = %q, want %q (sha256 of canonicalJSON over the supplied packet)", window.PacketDigest, wantDigest)
	}
}

// TestDispatchFoldDefensivelyRefusesAbsentWorkerPacket pins the first
// defensive refusal in appendGenericWorkflowCompletion's dispatch_worker
// branch (workflow_action_guards.go line 388): a payload whose fields map
// has no worker_packet key is refused with KindInvalidPayload and appends no
// WorkflowActionCompleted event. Preflight normally catches this first;
// reaching the fold directly tests the defensive depth the dispatch
// refactor of #446 retained.
func TestDispatchFoldDefensivelyRefusesAbsentWorkerPacket(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"attempt_id": "attempt-absent"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	assertDispatchFoldRefuses(t, "absent worker_packet", payload,
		"dispatch_worker worker_packet is absent from the action payload")
}

// TestDispatchFoldDefensivelyRefusesNonObjectWorkerPacket pins the second
// defensive refusal in appendGenericWorkflowCompletion's dispatch_worker
// branch (workflow_action_guards.go line 392): a worker_packet that decodes
// to JSON other than a single object (here a JSON string) is refused with
// KindInvalidPayload and appends no WorkflowActionCompleted event. Preflight
// normally catches this first; reaching the fold directly tests the
// defensive depth.
func TestDispatchFoldDefensivelyRefusesNonObjectWorkerPacket(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"attempt_id": "attempt-not-object", "worker_packet": "not-an-object"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	assertDispatchFoldRefuses(t, "string worker_packet", payload,
		"dispatch_worker worker_packet is not a JSON object")
}

// TestDispatchFoldDefensivelyRefusesWorkerPacketMissingIdentity pins the
// third defensive refusal in appendGenericWorkflowCompletion's
// dispatch_worker branch (workflow_action_guards.go line 397): a
// worker_packet that decodes to an object but is missing work_id or
// attempt_id is refused with KindInvalidPayload and appends no
// WorkflowActionCompleted event. Preflight normally catches this first;
// reaching the fold directly tests the defensive depth.
func TestDispatchFoldDefensivelyRefusesWorkerPacketMissingIdentity(t *testing.T) {
	packetBytes, err := json.Marshal(map[string]any{"schema_version": "1.0", "lane_id": "implement"})
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"attempt_id": "attempt-missing-ident", "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	assertDispatchFoldRefuses(t, "worker_packet missing identity", payload,
		"dispatch_worker worker_packet is missing work_id or attempt_id")
}

// TestDispatchFoldDefensivelyReachesTheCanonicalJSONCall confirms the
// reachable boundary the dispatch_worker fold sets up before canonicalJSON:
// by the time the fold calls canonicalJSON (workflow_action_guards.go line
// 406), the same bytes have already passed json.Unmarshal into
// map[string]json.RawMessage (line 392), so the bytes are exactly one JSON
// object, and the only difference between that decode and canonicalJSON is
// the use of UseNumber. canonicalJSON's documented contract
// (research_operations.go line 114) requires exactly one JSON value and
// uses the standard marshaller; by construction, neither precondition can
// fail on these bytes, so the canonicalJSON error branch (line 407) is
// unreachable defensive depth retained for the contract and is not faked
// here.
func TestDispatchFoldDefensivelyReachesTheCanonicalJSONCall(t *testing.T) {
	attemptID := "attempt-reaches-canonical"
	packetBytes, err := json.Marshal(dispatchWorkerPacket("work-fold-defensive", "execution", attemptID))
	if err != nil {
		t.Fatalf("marshal packet: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	in := dispatchFoldAssemblyInput(payload)
	events, err := appendGenericWorkflowCompletion(in, 1, []Event{})
	if err != nil {
		t.Fatalf("appendGenericWorkflowCompletion returned %v, want nil (the happy path proves the canonicalJSON call is reached)", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1 (one WorkflowActionCompleted appended on success)", len(events))
	}
	if events[0].Kind != WorkflowActionCompleted {
		t.Fatalf("events[0].Kind = %q, want %q", events[0].Kind, WorkflowActionCompleted)
	}
}

// dispatchFoldAssemblyInput builds the minimal workflowActionAssemblyInput
// appendGenericWorkflowCompletion needs to reach the dispatch_worker branch
// and exercise its defensive refusals. The function does not touch the
// store — it returns events or a typed failure — so no transaction or
// fixture is required; the only field that varies between the refusal
// cases is the payload JSON.
func dispatchFoldAssemblyInput(payload json.RawMessage) workflowActionAssemblyInput {
	return workflowActionAssemblyInput{
		request: WorkflowActionExecutionRequest{
			WorkID:          "work-fold-defensive",
			ExpectedVersion: 100,
			ActionID:        "dispatch_worker",
			OperationID:     "op-fold-defensive",
			Now:             time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
		},
		currentStep:  "execution",
		payload:      payload,
		evidenceRefs: []string{"evidence:op-fold-defensive"},
		eventActor:   "client:cd0059:principal/operator",
	}
}

// assertDispatchFoldRefuses drives one defensive-refusal case: it calls
// appendGenericWorkflowCompletion with the supplied payload on a fresh
// empty event slice, asserts the returned failure is the typed
// KindInvalidPayload refusal with the expected detail text, and asserts no
// WorkflowActionCompleted event was appended.
func assertDispatchFoldRefuses(t *testing.T, label string, payload json.RawMessage, wantDetail string) {
	t.Helper()
	in := dispatchFoldAssemblyInput(payload)
	events, err := appendGenericWorkflowCompletion(in, 1, []Event{})
	if err == nil {
		t.Fatalf("%s: appendGenericWorkflowCompletion returned nil error, want refusal %q", label, wantDetail)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("%s: expected typed *Failure, got %T: %v", label, err, err)
	}
	if failure.Kind != KindInvalidPayload {
		t.Fatalf("%s: failure kind = %s, want invalid_payload", label, failure.Kind)
	}
	if failure.Detail != wantDetail {
		t.Fatalf("%s: failure detail = %q, want %q", label, failure.Detail, wantDetail)
	}
	if len(events) != 0 {
		t.Fatalf("%s: events len = %d, want 0 (no WorkflowActionCompleted appended on refusal)", label, len(events))
	}
}
