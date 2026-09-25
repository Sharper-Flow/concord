package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// Agent admission of the CD-0166 delivery-gate recovery: a pinned, nonterminal
// instance parked on a released closed-gate delivery step with an outstanding
// post-rejection review reads the evidence-bearing corrective return through
// concord_work_transition, refuses the unreviewed delivery exit, and refuses
// the unauthenticated return. record_delivery stays the only forward exit, and
// the recovery asserts no delivery.

// deliveryRecoveryLane returns the builtin lane for one capability class.
func deliveryRecoveryLane(t *testing.T, capabilityClass string) store.LaneDefinition {
	t.Helper()
	for _, candidate := range store.BuiltinLaneDefinitions() {
		if candidate.CapabilityClass == capabilityClass {
			return candidate
		}
	}
	t.Fatalf("no builtin lane carries capability class %q", capabilityClass)
	return store.LaneDefinition{}
}

// deliveryRecoveryPacket builds the worker packet for one dispatch on the
// given lane, consuming the pin's correction when one is durable.
func deliveryRecoveryPacket(t *testing.T, s *store.Store, workID, stepID, attemptID, capabilityClass string) map[string]any {
	t.Helper()
	lane := deliveryRecoveryLane(t, capabilityClass)
	inputs := map[string]any{"task": "advance the approved gate objective", "constraints": []string{"preserve the approved contract"}}
	pin, err := store.ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction != nil {
		inputs["correction"] = pin.Correction
	}
	return map[string]any{"schema_version": "1.0", "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": workID, "step_id": stepID, "inputs": inputs}
}

// deliveryRecoveryCompletion records the lane dispatch and the completed
// report for one dispatched worker attempt.
func deliveryRecoveryCompletion(t *testing.T, s *store.Store, grant Authority, attemptID, capabilityClass string) {
	t.Helper()
	lane := deliveryRecoveryLane(t, capabilityClass)
	dispatch := store.Event{EventID: "delivery-recovery-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	completion := store.Event{EventID: "delivery-recovery-completed-" + attemptID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: retryJSON(store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		if _, err := store.AppendLaneActorDispatchTx(context.Background(), tx, enriched); err != nil {
			return err
		}
		_, err = store.ApplyOperationTx(context.Background(), tx, store.Operation{Events: []store.Event{completion}})
		return err
	}); err != nil {
		t.Fatalf("seed delivery recovery completion %s: %v", attemptID, err)
	}
}

func TestDeliveryGateRecoveryAdmitsPinnedReturnThroughAgent(t *testing.T) {
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	ctx := context.Background()
	seedCurrentWorkflowDomainFixture(t, s)

	worktree := t.TempDir()
	grant.Worktree = worktree

	// The instance is pinned to the released closed-gate implementation
	// version, whose delivery step carries no corrective return.
	pinned, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.implementation", 15)
	if !ok {
		t.Fatal("workflow.implementation v15 is not registered")
	}
	grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	grantActorRef, err := store.WorkflowActorRef(grantActor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: pinned, Actor: grantActor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved gate return objective','internal_sqlite','["review"]','[]','now',?,'[]','[]',0,'prototype_internal')`, grantActorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:gate-recovery","immutable_subject_ref":"commit:gate-recovery","expected_result":"pass"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?)`, store.WorktreeSetID("work-1"), "project-1", "claim:gate-recovery", "gate-recovery", strings.Repeat("a", 40), worktree, "repo:gate-recovery", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	invoke := func(input map[string]any) Envelope {
		t.Helper()
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	stepOf := func() string {
		t.Helper()
		var step string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
			t.Fatal(err)
		}
		return step
	}
	epochOf := func(actionID string) int64 {
		t.Helper()
		var epoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted, actionID).Scan(&epoch); err != nil {
			t.Fatal(err)
		}
		return epoch
	}
	version := func() int64 { return workVersion(t, s, "work-1") }
	dispatch := func(stepID, attemptID, capabilityClass string) {
		t.Helper()
		response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "dispatch_worker", "fields": map[string]any{"attempt_id": attemptID, "worker_packet": deliveryRecoveryPacket(t, s, "work-1", stepID, attemptID, capabilityClass)}, "idempotency_key": "gate-dispatch-" + attemptID})
		if response.Outcome != OutcomeOK {
			t.Fatalf("gate recovery dispatch %s: %+v", attemptID, response.Error)
		}
		deliveryRecoveryCompletion(t, s, grant, attemptID, capabilityClass)
	}

	// The execution pass, its bound review evidence, and the refinement pass.
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "start_execution", "idempotency_key": "gate-execution-start"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery start_execution: %+v", response.Error)
	}
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "review", "immutable_subject_ref": "evidence:gate-review"}, "idempotency_key": "gate-evidence-bind"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery bind_evidence: %+v", response.Error)
	}
	dispatch("execution", "attempt:work-1:gate-execution", "implementation")
	executionEpoch := epochOf("dispatch_worker")
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "accept_worker_result", "fields": map[string]any{"attempt_id": "attempt:work-1:gate-execution", "attempt_epoch": executionEpoch}, "idempotency_key": "gate-execution-accept"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery execution accept: %+v", response.Error)
	}
	if stepOf() != "refine" {
		t.Fatalf("step after execution accept = %q, want refine", stepOf())
	}
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "start_refine", "idempotency_key": "gate-refine-start"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery start_refine: %+v", response.Error)
	}
	refineEpoch := epochOf("dispatch_worker")

	// A rejected review, then an accepted implement repair: the refinement
	// history carries the post-rejection review debt.
	dispatch("refine", "attempt:work-1:gate-review", "review")
	reviewer := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/gate-reviewer", SessionRef: "session/work-1-gate-reviewer", ActorClass: store.ActorAgent}
	runVerificationStoreAction(t, s, "work-1", "reject_worker_result", map[string]any{
		"attempt_id": "attempt:work-1:gate-review", "attempt_epoch": refineEpoch,
		"diagnosis": "the review rejects the refined result", "strategy": "implement the review findings and resubmit",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:gate-review"},
	}, reviewer, nil, "gate-reject")
	dispatch("refine", "attempt:work-1:gate-repair", "implementation")

	// The historical parked position of a pre-gate database: the instance
	// sits on the delivery gate with the review debt outstanding.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='delivery' WHERE work_id='work-1';
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("seed the parked gate position: %v", err)
	}

	// The unreviewed delivery exit refuses on the pinned shape.
	deliveryRefusal := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:gate-recovery", "delivery_state": "asserted"}, "idempotency_key": "gate-recovery-delivery"})
	if deliveryRefusal.Outcome != OutcomeError || deliveryRefusal.Error == nil || !strings.Contains(deliveryRefusal.Error.Message, "fresh accepted review") {
		t.Fatalf("parked gate delivery exit = %+v, want a fresh-review refusal", deliveryRefusal.Error)
	}

	// The corrective return is admitted up to the approval wall: an
	// unauthenticated request mints the operator challenge and refuses.
	correctionInput := map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "request_correction", "idempotency_key": "gate-recovery-return", "fields": map[string]any{
		"diagnosis": "the repaired result has no fresh accepted review", "strategy": "return to the execution step and dispatch a fresh review",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:gate-review"},
	}}
	challenge := invoke(correctionInput)
	if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("unauthenticated gate return = %+v, want approval_required", challenge.Error)
	}
	details := challenge.Error.Details
	challengeRef, _ := details["approval_ref"].(string)
	if len(challengeRef) != 64 {
		t.Fatalf("gate return challenge carries no approval reference: %+v", details)
	}
	if got, _ := details["action_id"].(string); got != "request_correction" {
		t.Fatalf("gate return challenge action_id = %v", details["action_id"])
	}

	// One operator approval admits the evidence-bearing return.
	approvedInput := cloneWithApproval(t, correctionInput, challengeRef)
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": version(), "contract": int64(1)}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	returned := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, env)
	if returned.Outcome != OutcomeOK {
		t.Fatalf("approved gate return = %+v", returned.Error)
	}
	if got := stepOf(); got != "execution" {
		t.Fatalf("step after gate return = %q, want execution", got)
	}

	// The return asserted no delivery: record_delivery stays the only forward
	// exit and the parked change never claimed one.
	var deliveries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='record_delivery'`, store.WorkflowActionCompleted).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("gate recovery recorded %d delivery assertions, want none", deliveries)
	}
	var pinnedVersion int64
	var pinnedDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_version,definition_digest FROM workflow_instances WHERE work_id='work-1'`).Scan(&pinnedVersion, &pinnedDigest); err != nil {
		t.Fatal(err)
	}
	if pinnedVersion != 15 || pinnedDigest != pinned.Digest {
		t.Fatalf("pin after recovery = v%d %s, want the released v15 %s", pinnedVersion, pinnedDigest, pinned.Digest)
	}
}
