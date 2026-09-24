package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The delivery correction admits one typed, append-only correction of a
// completed delivery assertion behind a consumed core operator approval. The
// original event log stays immutable, the terminal instance stays closed, and
// the read surfaces show the effective assertion beside the unchanged
// original.
const (
	deliveryCorrectionApprovalRef  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	deliveryCorrectionApprovalHold = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	deliveryCorrectionDigest       = "sha256:" + deliveryCorrectionApprovalHold
	deliveryCorrectionConsequence  = "recovery"
	deliveryCorrectionWrongPath    = "file:internal/store/impl.go"
	deliveryCorrectionMergeRef     = "https://github.com/Sharper-Flow/concord/pull/1339"
)

func deliveryCorrectionScopeJSON(workID string) string {
	return `{"work_ids":["` + workID + `"]}`
}

func deliveryCorrectionVersionsJSON(version int64) string {
	return `{"work":` + jsonInt(version) + `}`
}

func jsonInt(value int64) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

// completedDeliveryFixture seeds one work item whose workflow instance is
// completed through the log with a delivery assertion that carries a
// repository file path instead of merge evidence, the defect shape the
// correction exists to repair. It returns the store, the target assertion
// event id, the work item version the correction must expect, the assertion's
// stored payload version, and its sequence.
func completedDeliveryFixture(t *testing.T, workID string) (*Store, string, int64, int, int64) {
	t.Helper()
	s := completedDeliveryFixtureWithAssertions(t, workID, 1)
	targetEventID, seq, payloadVersion := deliveryAssertionEvent(t, s, workID, 0)
	return s, targetEventID, deliveryWorkVersion(t, s, workID), payloadVersion, seq
}

// completedDeliveryFixtureWithAssertions seeds the completed item with a
// chosen number of delivery assertions, so the stale-target refusal can name
// an assertion that is no longer the current one. Only the first assertion is
// folded; later ones are plain event rows because a second record_delivery on
// one step cannot fold twice.
func completedDeliveryFixtureWithAssertions(t *testing.T, workID string, assertions int) *Store {
	t.Helper()
	s := openTemp(t)
	seedWork(t, s, workID)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/reviewer", SessionRef: "session/" + workID + "-reviewer", ActorClass: ActorAgent}
	reviewerRef, reviewerErr := WorkflowActorRef(reviewer)
	if reviewerErr != nil {
		t.Fatal(reviewerErr)
	}
	definition := workflowFixtureDefinition(t, 2)
	events := []Event{
		workflowEvent("owner-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("definition-"+workID, WorkflowDefinitionSelected, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": workflowFixtureWorkKind}),
		workflowActionCompletedFixture("proposal-"+workID, workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("discovery-"+workID, workID, ownerRef, 5, "discovery", "record_discovery"),
		workflowActionCompletedFixture("design-"+workID, workID, ownerRef, 6, "design", "record_design"),
		workflowActionCompletedFixture("approve-"+workID, workID, ownerRef, 7, "planning", "approve_contract"),
		workflowEventWithActor("contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 8, "resulting_version": 9, "contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}, "required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite"}),
		workflowEventWithActor("delivery-"+workID, WorkflowActionCompleted, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": 9, "resulting_version": 10,
			"step_id": "execution", "action_id": "record_delivery", "attempt_epoch": 1,
			"delivery_artifact": deliveryCorrectionWrongPath, "delivery_state": "asserted",
			"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": ownerRef,
		}),
		workflowEvent("reviewer-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 10, "resulting_version": 11, "actor_ref": reviewerRef, "principal_ref": reviewer.PrincipalRef, "client_ref": reviewer.ClientRef, "agent_ref": reviewer.AgentRef, "session_ref": reviewer.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("verdict-"+workID, WorkflowVerdictRecorded, workID, reviewerRef, map[string]any{"work_id": workID, "expected_version": 11, "resulting_version": 12, "contract_version": 1, "predicate_id": "predicate:primary", "verdict_kind": "ok", "verdict_actor_ref": reviewerRef, "verdict_model": "test/model-1", "evaluation_evidence": []string{"evidence:verification"}}),
		workflowCompletedFixtureEvent("completed-"+workID, workID, reviewerRef, reviewerRef, 12),
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	// Later assertions replace the first one in the log without folding on the
	// already-advanced step; only their read shape matters for the stale case.
	for index := 1; index < assertions; index++ {
		payload, err := json.Marshal(map[string]any{
			"work_id": workID, "expected_version": 10, "resulting_version": 11,
			"step_id": "execution", "action_id": "record_delivery", "attempt_epoch": 1,
			"delivery_artifact": deliveryCorrectionWrongPath + "/" + jsonInt(int64(index)), "delivery_state": "asserted",
			"result_evidence_refs": []string{}, "changed_refs": []string{workID}, "actor_ref": ownerRef,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
			"delivery-"+workID+"-"+jsonInt(int64(index+1)), WorkflowActionCompleted, string(SubjectWorkItem), workID, ownerRef, time.Unix(int64(index), 0).UTC().Format(time.RFC3339Nano), 1, payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE work_items SET lifecycle='completed' WHERE id=?;
		DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client/concord-1','active','principal/operator','[]','[]','[]','2026-09-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	insertConsumedApproval(t, s, deliveryCorrectionApprovalRef, workID, deliveryWorkVersion(t, s, workID))
	return s
}

// workflowCompletedFixtureEvent builds the terminal completion event at its
// registered payload version.
func workflowCompletedFixtureEvent(id, workID, actor, verdictRef string, expected int64) Event {
	event := workflowEventWithActor(id, WorkflowCompleted, workID, actor, map[string]any{"work_id": workID, "expected_version": expected, "resulting_version": expected + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": verdictRef, "premise_confirmed": false, "evidence_count": 0, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64), "impact_verdict": "non-breaking"})
	event.PayloadVersion = 2
	return event
}

// insertConsumedApproval inserts an operator approval row in its consumed
// one-use state, mirroring what the approval boundary leaves behind.
func insertConsumedApproval(t *testing.T, s *Store, ref, workID string, version int64) {
	t.Helper()
	insertConsumedApprovalState(t, s, ref, workID, version, 1)
}

func insertConsumedApprovalState(t *testing.T, s *Store, ref, workID string, version int64, usedCount int) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO agent_approvals(approval_ref,operation_digest,scope_json,version_json,consequence,human_principal_ref,client_ref,session_ref,issued_at,expires_at,max_uses,used_count,protected_evidence_ref,protected_evidence_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ref, deliveryCorrectionDigest, deliveryCorrectionScopeJSON(workID), deliveryCorrectionVersionsJSON(version), deliveryCorrectionConsequence, "principal/operator", "client/concord-1", "session/"+workID+"-approval-"+ref[:4], "2026-09-10T00:00:00Z", "2026-09-11T00:00:00Z", 1, usedCount, "approval-evidence", "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
}

func deliveryWorkVersion(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// deliveryAssertionEvent reads the delivery assertion at a chosen offset from
// the latest, returning its event id, sequence, and stored payload version.
func deliveryAssertionEvent(t *testing.T, s *Store, workID string, offsetFromLatest int) (string, int64, int) {
	t.Helper()
	var eventID string
	var seq int64
	var payloadVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id,seq,payload_version FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_delivery' AND COALESCE(json_extract(payload,'$.delivery_artifact'),'')<>'' ORDER BY seq DESC LIMIT 1 OFFSET ?`, workID, WorkflowActionCompleted, offsetFromLatest).Scan(&eventID, &seq, &payloadVersion); err != nil {
		t.Fatal(err)
	}
	return eventID, seq, payloadVersion
}

func deliveryCorrectionRequest(workID string, version int64, targetEventID string, targetSeq int64, targetPayloadVersion int) WorkflowDeliveryCorrectionRequest {
	return WorkflowDeliveryCorrectionRequest{
		WorkID: workID, ExpectedVersion: version,
		TargetEventID: targetEventID, TargetSeq: targetSeq, TargetPayloadVersion: targetPayloadVersion,
		Reason:           "the asserted delivery named repository paths before the pull request merged",
		DeliveryArtifact: deliveryCorrectionMergeRef, DeliveryState: "asserted",
		ApprovalRef:             deliveryCorrectionApprovalRef,
		ApprovalOperationDigest: deliveryCorrectionDigest,
		ApprovalScopeJSON:       deliveryCorrectionScopeJSON(workID),
		ApprovalVersionsJSON:    deliveryCorrectionVersionsJSON(version),
		ApprovalConsequence:     deliveryCorrectionConsequence,
		EventID:                 "correction-" + workID,
		OccurredAt:              time.Unix(20, 0).UTC(),
	}
}

func runDeliveryCorrection(t *testing.T, s *Store, request WorkflowDeliveryCorrectionRequest) error {
	t.Helper()
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := ApplyWorkflowDeliveryCorrectionTx(context.Background(), tx, request)
		return err
	})
	return err
}

func requireDeliveryCorrectionRefusal(t *testing.T, err error, wantKind FailureKind, wantDetail string) {
	t.Helper()
	if err == nil {
		t.Fatalf("delivery correction was admitted, want %s refusal naming %q", wantKind, wantDetail)
	}
	failure := &Failure{}
	if !failureAs(err, &failure) || failure.Kind != wantKind || !strings.Contains(failure.Detail, wantDetail) {
		t.Fatalf("delivery correction error=%v, want %s naming %q", err, wantKind, wantDetail)
	}
}

// TestTerminalDeliveryCorrectionAdmission proves the admission gate: the
// correction is admitted only with the target identity, version, reason, and
// merge evidence, behind a consumed one-use operator approval bound to this
// exact operation, on a completed instance whose assertion is not already
// corrected. proves check:terminal-delivery-correction-admission.
func TestTerminalDeliveryCorrectionAdmission(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-admission"
	s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)

	if err := runDeliveryCorrection(t, s, deliveryCorrectionRequest(workID, version, targetEventID, targetSeq, targetPayloadVersion)); err != nil {
		t.Fatalf("approved delivery correction refused: %v", err)
	}
	var instanceState, currentStep, lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState, &currentStep); err != nil {
		t.Fatal(err)
	}
	if instanceState != "completed" || currentStep != "acceptance" {
		t.Fatalf("correction moved the closed instance to %q at %q", instanceState, currentStep)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("correction reopened the work item lifecycle to %q", lifecycle)
	}
	if got := deliveryWorkVersion(t, s, workID); got != version+2 {
		t.Fatalf("correction version=%d, want the operator actor and the correction event (%d)", got, version+2)
	}

	// A second correction of the same assertion is refused: the approval is
	// one-use per correction and the assertion is already corrected.
	second := deliveryCorrectionRequest(workID, deliveryWorkVersion(t, s, workID), targetEventID, targetSeq, targetPayloadVersion)
	second.EventID = "correction-" + workID + "-second"
	second.ApprovalRef = "a0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	insertConsumedApproval(t, s, second.ApprovalRef, workID, deliveryWorkVersion(t, s, workID))
	second.ApprovalVersionsJSON = deliveryCorrectionVersionsJSON(deliveryWorkVersion(t, s, workID))
	requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, second), KindInvalidOperation, "already corrected")
}

// TestTerminalDeliveryCorrectionAdmissionRefusals holds the named refusals:
// incomplete fields, a mistargeted identity or version, a non-assertion
// target, a live instance, a restated artifact, and an approval that is
// missing, unconsumed, or unbound.
func TestTerminalDeliveryCorrectionAdmissionRefusals(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-refusals"
	s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)
	valid := deliveryCorrectionRequest(workID, version, targetEventID, targetSeq, targetPayloadVersion)

	cases := []struct {
		name      string
		mutate    func(request WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest
		wantKind  FailureKind
		wantPiece string
	}{
		{name: "missing reason", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest { r.Reason = ""; return r }, wantKind: KindInvalidPayload, wantPiece: "incomplete or unbounded fields"},
		{name: "wrong target identity", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.TargetSeq += 40
			return r
		}, wantKind: KindInvalidOperation, wantPiece: "does not match the recorded assertion"},
		{name: "wrong target version", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.TargetPayloadVersion++
			return r
		}, wantKind: KindInvalidOperation, wantPiece: "does not match the recorded assertion"},
		{name: "target is not an assertion", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.TargetEventID = "proposal-" + workID
			return r
		}, wantKind: KindInvalidOperation, wantPiece: "not a recorded delivery assertion"},
		{name: "target event is not recorded", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.TargetEventID = "absent-" + workID
			return r
		}, wantKind: KindProjectionNotFound, wantPiece: "not recorded"},
		{name: "restated artifact", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.DeliveryArtifact = deliveryCorrectionWrongPath
			return r
		}, wantKind: KindInvalidOperation, wantPiece: "restates the asserted artifact"},
		{name: "repository path merge evidence", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.DeliveryArtifact = "file:internal/store/other.go"
			return r
		}, wantKind: KindInvalidPayload, wantPiece: "merge evidence is not an external merge reference"},
		{name: "missing approval", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.ApprovalRef = ""
			return r
		}, wantKind: KindInvalidPayload, wantPiece: "operator approval reference"},
		{name: "unconsumed approval", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.ApprovalRef = deliveryCorrectionApprovalHold
			r.EventID = "correction-" + workID + "-unconsumed"
			return r
		}, wantKind: KindApprovalRequired, wantPiece: "consumed operator approval"},
		{name: "unbound approval", mutate: func(r WorkflowDeliveryCorrectionRequest) WorkflowDeliveryCorrectionRequest {
			r.ApprovalRef = "d0aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			r.EventID = "correction-" + workID + "-unbound"
			insertConsumedApprovalState(t, s, r.ApprovalRef, workID, version, 1)
			r.ApprovalOperationDigest = "sha256:" + strings.Repeat("d", 64)
			return r
		}, wantKind: KindUnauthorized, wantPiece: "not bound to the exact operation"},
	}
	for _, item := range cases {
		request := item.mutate(valid)
		requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, request), item.wantKind, item.wantPiece)
	}

	// A live instance corrects delivery through record_delivery, not a
	// correction event.
	live := valid
	live.EventID = "correction-" + workID + "-live"
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET instance_state='running' WHERE work_id=?;
		DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, live), KindInvalidOperation, "only to a completed workflow instance")
}

// TestTerminalDeliveryCorrectionTargetsCurrentAssertionOnly refuses a
// correction that names an assertion which a later assertion already replaced.
func TestTerminalDeliveryCorrectionTargetsCurrentAssertionOnly(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-stale-target"
	s := completedDeliveryFixtureWithAssertions(t, workID, 2)
	staleEventID, staleSeq, stalePayloadVersion := deliveryAssertionEvent(t, s, workID, 1)
	request := deliveryCorrectionRequest(workID, deliveryWorkVersion(t, s, workID), staleEventID, staleSeq, stalePayloadVersion)
	requireDeliveryCorrectionRefusal(t, runDeliveryCorrection(t, s, request), KindInvalidOperation, "not the current delivery assertion")
}

// TestTerminalDeliveryCorrectionReadReplay proves the read surface shows the
// effective assertion beside the unchanged original, that the original event
// bytes and the closed instance survive the correction, and that a full
// replay from the event log re-derives the same admission and the same read.
// proves check:terminal-delivery-correction-read-replay.
func TestTerminalDeliveryCorrectionReadReplay(t *testing.T) {
	t.Parallel()
	const workID = "delivery-correction-read-replay"
	s, targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryFixture(t, workID)

	before, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if before.DeliveryAssertion == nil || before.DeliveryAssertion.Correction != nil || before.DeliveryAssertion.Artifact != deliveryCorrectionWrongPath {
		t.Fatalf("read before correction = %+v, want the bare wrong assertion", before.DeliveryAssertion)
	}
	// The read supplies the exact target identity the correction admission
	// consumes: event id, sequence, and stored payload version.
	target := before.DeliveryAssertion
	if target.EventID != targetEventID || target.Seq != targetSeq || target.TargetPayloadVersion != targetPayloadVersion {
		t.Fatalf("read target identity = (%q,%d,%d), want (%q,%d,%d)", target.EventID, target.Seq, target.TargetPayloadVersion, targetEventID, targetSeq, targetPayloadVersion)
	}
	var originalPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, targetEventID).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}

	if err := runDeliveryCorrection(t, s, deliveryCorrectionRequest(workID, version, targetEventID, targetSeq, targetPayloadVersion)); err != nil {
		t.Fatalf("approved delivery correction refused: %v", err)
	}

	after, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	assertion := after.DeliveryAssertion
	if assertion == nil || assertion.Correction == nil {
		t.Fatalf("read after correction = %+v, want the assertion with its correction", assertion)
	}
	if assertion.EventID != targetEventID || assertion.Artifact != deliveryCorrectionWrongPath || assertion.State != "asserted" || assertion.TargetPayloadVersion != targetPayloadVersion {
		t.Fatalf("original assertion changed: %+v", assertion)
	}
	correction := assertion.Correction
	if correction.Artifact != deliveryCorrectionMergeRef || correction.EvidenceSource != "coordinator_asserted" || correction.Reason == "" || correction.ApprovalRef != deliveryCorrectionApprovalRef {
		t.Fatalf("correction read = %+v, want the merge evidence under coordinator provenance", correction)
	}
	if assertion.EffectiveArtifact() != deliveryCorrectionMergeRef {
		t.Fatalf("effective artifact = %q, want the corrected merge evidence", assertion.EffectiveArtifact())
	}
	var replayedPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, targetEventID).Scan(&replayedPayload); err != nil {
		t.Fatal(err)
	}
	if originalPayload != replayedPayload {
		t.Fatal("the original assertion event payload was rewritten")
	}
	var eventCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=? AND subject_id=?`, WorkflowDeliveryCorrected, workID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("correction event count=%d, want exactly one appended correction", eventCount)
	}

	// The replay re-derives the admission from the log and durable approval,
	// then reproduces the same read.
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("replay after correction: %v", err)
	}
	replayed, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayed.DeliveryAssertion, after.DeliveryAssertion) {
		t.Fatalf("replayed assertion read %+v diverges from %+v", replayed.DeliveryAssertion, after.DeliveryAssertion)
	}
	var instanceState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&instanceState); err != nil {
		t.Fatal(err)
	}
	if instanceState != "completed" {
		t.Fatalf("replay reopened the instance to %q", instanceState)
	}
}
