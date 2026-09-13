package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// CD-0013 D5 fences the executing identity against the evaluating identity.
// For a dispatched lane the executing identity is the lane: it ran in its own
// session and reported its own model. The orchestrator dispatched and then
// evaluates, which is exactly the separation D5 wants. The check compared the
// evaluator against workflow_instances.execution_actor_ref, which every
// action start overwrites with whoever started it, so at acceptance time it
// held the orchestrator, the comparison was orchestrator against
// orchestrator, and no dispatched result could ever be accepted (#800).
//
// This runs the whole route against a real store: dispatch_worker opens the
// window, worker.dispatched records the lane's identity, worker.completed
// closes the attempt, and accept_worker_result by the dispatching session is
// admitted. The same session accepting a result it did not dispatch through
// a lane is still refused, so the fence holds where it should.
func TestAcceptWorkerResultComparesAgainstTheLaneThatExecuted(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-accept-lane")
	orchestrator := seed.ownerActor
	const attemptID = "attempt-accept-lane"

	packet := dispatchWorkerPacket(seed.workID, "execution", attemptID)
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalJSON(packetBytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	packetDigest := "sha256:" + hex.EncodeToString(sum[:])
	fieldsPayload, _ := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload, Actor: orchestrator, AcceptedInputsDigest: cd0059TestDigest(t, "accept-lane-inputs"),
		IdempotencyIdentity: "accept-lane-dispatch", OperationID: "op-accept-lane-dispatch", PrincipalRef: orchestrator.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "accept-lane-dispatch-key", RequestID: "req-accept-lane-dispatch",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}); err != nil {
		t.Fatalf("dispatch_worker: %v", err)
	}
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE kind='workflow.action_started' AND subject_id=? ORDER BY seq DESC LIMIT 1`, seed.workID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}

	// The lane executes in its own host session and reports its model. The
	// dispatch evidence carries that identity so D5 has the right pair.
	lane := BuiltinLaneDefinitions()[0]
	const laneSession = "ses_lane_verify_01"
	dispatched := workerDispatchEvent(seed.workID, attemptID, lane, map[string]any{
		"packet_digest":      packetDigest,
		"worker_session_ref": laneSession,
	})
	dispatched.PayloadVersion = 3
	dispatched.Payload = withHostProvenance(t, dispatched.Payload)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatched}}); err != nil {
		t.Fatalf("worker.dispatched: %v", err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerCompleteEvent(seed.workID, "complete-"+attemptID, attemptID, "openai/gpt-5.6-luna")}}); err != nil {
		t.Fatalf("worker.completed: %v", err)
	}

	// The orchestrator that dispatched accepts. It is not the lane, so D5 admits it.
	acceptFields, _ := json.Marshal(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch})
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "accept_worker_result",
		Payload: acceptFields, Actor: orchestrator, AcceptedInputsDigest: cd0059TestDigest(t, "accept-lane-accept"),
		IdempotencyIdentity: "accept-lane-accept", OperationID: "op-accept-lane-accept", PrincipalRef: orchestrator.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "accept-lane-accept-key", RequestID: "req-accept-lane-accept",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}); err != nil {
		t.Fatalf("the dispatching orchestrator must be able to accept the lane's result: %v", err)
	}
	var laneActor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM workflow_actors WHERE session_ref=?`, laneSession).Scan(&laneActor); err != nil {
		t.Fatal(err)
	}
	if laneActor != "1" {
		t.Fatalf("the lane's identity must be recorded as a workflow actor, found %s rows", laneActor)
	}
}

// The fence still refuses the one case it exists for: an evaluator that is the
// lane itself. A worker.dispatched whose session is the orchestrator's own
// session means the orchestrator executed, and its acceptance is self-grading.
func TestAcceptWorkerResultRefusesTheLaneGradingItself(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seed := seedDispatchFixture(t, s, "work-accept-self")
	orchestrator := seed.ownerActor
	const attemptID = "attempt-accept-self"
	packet := dispatchWorkerPacket(seed.workID, "execution", attemptID)
	packetBytes, _ := json.Marshal(packet)
	canonical, _ := canonicalJSON(packetBytes)
	sum := sha256.Sum256(canonical)
	fieldsPayload, _ := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "dispatch_worker",
		Payload: fieldsPayload, Actor: orchestrator, AcceptedInputsDigest: cd0059TestDigest(t, "accept-self-inputs"),
		IdempotencyIdentity: "accept-self-dispatch", OperationID: "op-accept-self-dispatch", PrincipalRef: orchestrator.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "accept-self-dispatch-key", RequestID: "req-accept-self-dispatch",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	}); err != nil {
		t.Fatal(err)
	}
	var epoch int64
	_ = s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE kind='workflow.action_started' AND subject_id=? ORDER BY seq DESC LIMIT 1`, seed.workID).Scan(&epoch)
	lane := BuiltinLaneDefinitions()[0]
	dispatched := workerDispatchEvent(seed.workID, attemptID, lane, map[string]any{
		"packet_digest":      "sha256:" + hex.EncodeToString(sum[:]),
		"worker_session_ref": orchestrator.SessionRef,
	})
	dispatched.PayloadVersion = 3
	dispatched.Payload = withHostProvenance(t, dispatched.Payload)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatched}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerCompleteEvent(seed.workID, "complete-"+attemptID, attemptID, "openai/gpt-5.6-luna")}}); err != nil {
		t.Fatal(err)
	}
	acceptFields, _ := json.Marshal(map[string]any{"attempt_id": attemptID, "attempt_epoch": epoch})
	_, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: seed.workID, ExpectedVersion: readWorkVersion(t, s, seed.workID), ActionID: "accept_worker_result",
		Payload: acceptFields, Actor: orchestrator, AcceptedInputsDigest: cd0059TestDigest(t, "accept-self-accept"),
		IdempotencyIdentity: "accept-self-accept", OperationID: "op-accept-self-accept", PrincipalRef: orchestrator.PrincipalRef,
		Tool: "concord_work_transition", IdempotencyKey: "accept-self-accept-key", RequestID: "req-accept-self-accept",
		AcceptedScope: `{}`, ContractDigest: testManifestDigest,
	})
	if err == nil || !strings.Contains(err.Error(), "own verdict") {
		t.Fatalf("a lane that ran in the evaluator's own session must be refused as self-grading, got %v", err)
	}
}

func withHostProvenance(t *testing.T, payload json.RawMessage) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatal(err)
	}
	m["host_provenance"] = map[string]any{
		"digest":  "sha256:" + strings.Repeat("d", 64),
		"sources": []map[string]any{{"kind": "unenumerated", "path": "provider_behavioral_hints"}},
	}
	out, _ := json.Marshal(m)
	return out
}

var _ = time.Now
