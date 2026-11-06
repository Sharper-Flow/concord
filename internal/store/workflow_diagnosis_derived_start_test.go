package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func diagnosisS1Actor(workID, name string) WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/" + name, SessionRef: "session/" + workID + "-" + name, ActorClass: ActorAgent}
}

func diagnosisPayload(reproduction, searched, outcome string, relatedIDs []string, rootCause string) json.RawMessage {
	payload := map[string]any{"reproduction": reproduction, "searched": searched, "outcome": outcome, "root_cause": rootCause}
	if relatedIDs != nil {
		payload["related_ids"] = relatedIDs
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encoded
}

type workflowStartObservation struct {
	Seq           int64
	EventID       string
	ActionID      string
	Epoch         int64
	ActorRef      string
	PayloadDigest string
}

func workflowCurrentStartAtStep(t *testing.T, s *Store, workID, stepID string) *workflowStartObservation {
	t.Helper()
	var start workflowStartObservation
	var raw []byte
	err := s.db.QueryRow(`SELECT latest_start_seq,latest_start_event_id,latest_start_payload,attempt_epoch FROM workflow_step_epochs WHERE work_id=? AND step_id=? AND latest_start_seq>0`, workID, stepID).Scan(&start.Seq, &start.EventID, &raw, &start.Epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the current step start: %v", err)
	}
	var payload workflowActionStartedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WorkID != workID || payload.StepID != stepID || payload.AttemptEpoch != start.Epoch {
		t.Fatalf("current start identity/epoch differs from its retained payload: %+v, %+v", start, payload)
	}
	if err := s.db.QueryRow(`SELECT actor,payload_digest FROM effect_receipts WHERE event_id=? AND effect_seq=? AND subject_type='work_item' AND subject_id=? AND kind=?`, start.EventID, start.Seq, workID, WorkflowActionStarted).Scan(&start.ActorRef, &start.PayloadDigest); err != nil {
		t.Fatalf("read the exact current-start receipt: %v", err)
	}
	wantDigest, err := effectPayloadDigest(raw)
	if err != nil || wantDigest != start.PayloadDigest || start.ActorRef != payload.ActorRef {
		t.Fatalf("current start differs from its receipt: %+v, payload=%+v, err=%v", start, payload, err)
	}
	start.ActionID = payload.ActionID
	return &start
}

func workflowStartReceiptIDs(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT event_id FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY effect_seq`, workID, WorkflowActionStarted)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func instanceExecutionStart(t *testing.T, s *Store, workID string) (string, string) {
	t.Helper()
	var startedAt, actorRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(execution_started_at,''),COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, workID).Scan(&startedAt, &actorRef); err != nil {
		t.Fatal(err)
	}
	return startedAt, actorRef
}

func diagnosisAlignmentRows(t *testing.T, s *Store, workID string) []struct {
	Related  string
	Searched string
	Outcome  string
} {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT COALESCE(related_work_id,''),searched,outcome FROM workflow_backlog_alignment WHERE work_id=? ORDER BY related_work_id`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []struct {
		Related  string
		Searched string
		Outcome  string
	}
	for rows.Next() {
		var row struct {
			Related  string
			Searched string
			Outcome  string
		}
		if err := rows.Scan(&row.Related, &row.Searched, &row.Outcome); err != nil {
			t.Fatal(err)
		}
		found = append(found, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

func TestRecordDiagnosisRecordsAllSectionsAtomically(t *testing.T) {
	t.Parallel()
	const workID = "diagnosis-recorded-atomic"
	const relatedID = "diagnosis-related-work"
	fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanFix, "diagnose")
	s := fixture.store
	seedWork(t, s, relatedID)
	before := readWorkVersion(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_diagnosis", diagnosisPayload("the defect reproduces from a clean store", "the backlog search covered the tracker", "related_found", []string{relatedID}, "the defect is the fold's pool"), 0, fixture.owner); err != nil {
		t.Fatalf("record diagnosis: %v", err)
	}
	if got := currentStep(t, s, workID); got != "diagnose" {
		t.Fatalf("record_diagnosis changed frontier to %q", got)
	}
	rows := diagnosisAlignmentRows(t, s, workID)
	if len(rows) != 2 || rows[0].Related != "" || rows[1].Related != relatedID {
		t.Fatalf("diagnosis alignment rows = %+v", rows)
	}
	for _, row := range rows {
		if row.Searched != "the backlog search covered the tracker" || row.Outcome != "related_found" {
			t.Fatalf("diagnosis alignment row = %+v", row)
		}
	}
	var events, relations int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowDiagnosisRecorded).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM relations WHERE work_id_from=? OR work_id_to=?`, workID, workID).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if events != 1 || relations != 0 || readWorkVersion(t, s, workID) <= before {
		t.Fatalf("diagnosis effects: events=%d relations=%d version=%d", events, relations, readWorkVersion(t, s, workID))
	}
	if err := runVerdictActionAs(t, s, workID, "complete_step", json.RawMessage(`{"step_id":"diagnose","note":"diagnosis recorded"}`), 0, fixture.owner); err != nil {
		t.Fatalf("complete diagnosis: %v", err)
	}
	if got := currentStep(t, s, workID); got != "clarify" {
		t.Fatalf("frontier after complete_step = %q, want clarify", got)
	}
}

// TestRecordDiagnosisRefusalsLeaveZeroWrites proves every malformed section
// refuses the whole call before any write lands: no event, no projection
// row, no step advance.
func TestRecordDiagnosisRefusalsLeaveZeroWrites(t *testing.T) {
	t.Parallel()
	const workID = "diagnosis-refusal-zero-writes"
	fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanFix, "diagnose")
	s := fixture.store
	version := readWorkVersion(t, s, workID)
	cases := []struct {
		name    string
		payload json.RawMessage
	}{
		{"missing reproduction", json.RawMessage(`{"searched":"the search","outcome":"none_found","root_cause":"the cause"}`)},
		{"missing searched", json.RawMessage(`{"reproduction":"the reproduction","outcome":"none_found","root_cause":"the cause"}`)},
		{"missing outcome", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","root_cause":"the cause"}`)},
		{"missing root cause", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"none_found"}`)},
		{"related_found without ids", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"related_found","root_cause":"the cause"}`)},
		{"none_found with ids", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"none_found","related_ids":["work:diagnosis-refusal-zero-writes"],"root_cause":"the cause"}`)},
		{"unknown related id", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"related_found","related_ids":["work:not-a-real-work-item"],"root_cause":"the cause"}`)},
		{"unknown field", json.RawMessage(`{"reproduction":"the reproduction","searched":"the search","outcome":"none_found","root_cause":"the cause","severity":"high"}`)},
	}
	for _, tc := range cases {
		if err := runVerdictActionAs(t, s, workID, "record_diagnosis", tc.payload, 0, fixture.owner); err == nil {
			t.Fatalf("%s: malformed diagnosis admitted", tc.name)
		}
	}
	var events int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, workID, WorkflowDiagnosisRecorded).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if got := readWorkVersion(t, s, workID); got != version || events != 0 || len(diagnosisAlignmentRows(t, s, workID)) != 0 || currentStep(t, s, workID) != "diagnose" {
		t.Fatalf("refused diagnosis changed state: version=%d events=%d", got, events)
	}
	if err := runVerdictActionAs(t, s, workID, "record_diagnosis", diagnosisPayload("the reproduction", "the search", "none_found", nil, "the cause"), 0, fixture.owner); err != nil {
		t.Fatalf("valid diagnosis after refusals: %v", err)
	}
	if got := currentStep(t, s, workID); got != "diagnose" {
		t.Fatalf("record_diagnosis changed frontier to %q", got)
	}
}

func TestNativeExecutionStartFenceAndAtomicRefusals(t *testing.T) {
	t.Parallel()
	const workID = "native-execution-start"
	fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanFix, "implement")
	s := fixture.store
	first := fixture.owner
	second := diagnosisS1Actor(workID, "second")
	bindHold := func(id string) {
		t.Helper()
		ref, producer := "artifact:"+id, "producer-"+id
		seedWorkflowAuthority(t, s, producer, workID, "principal:hold", "request:"+id, []string{ref})
		payload := mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": ref, "producer_id": "principal:hold", "producer_run_ref": producer, "producer_watermark": "request:" + id})
		if err := runWorkflowActionWithIdentity(t, s, workID, "bind_evidence", payload, 0, first, id); err != nil {
			t.Fatalf("bind evidence %s: %v", id, err)
		}
	}
	bindHold("before-start")
	if start := workflowCurrentStartAtStep(t, s, workID, "implement"); start != nil || len(workflowStartReceiptIDs(t, s, workID)) != 0 {
		t.Fatalf("evidence binding created a start: %+v", start)
	}
	version := readWorkVersion(t, s, workID)
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:not-yet","delivery_state":"asserted"}`), 0, first); err == nil {
		t.Fatal("delivery admitted at implement")
	}
	if err := runVerdictActionAs(t, s, workID, "start_execution", json.RawMessage(`{"unknown_field":true}`), 0, first); err == nil {
		t.Fatal("start_execution admitted an unknown field")
	}
	if got := readWorkVersion(t, s, workID); got != version {
		t.Fatalf("refused start changed version %d -> %d", version, got)
	}
	if start := workflowCurrentStartAtStep(t, s, workID, "implement"); start != nil || len(workflowStartReceiptIDs(t, s, workID)) != 0 {
		t.Fatalf("refused start left a start or receipt: %+v", start)
	}
	if startedAt, _ := instanceExecutionStart(t, s, workID); startedAt != "" {
		t.Fatalf("refused start set execution_started_at = %q", startedAt)
	}
	if err := runVerdictActionAtVersion(t, s, workID, "start_execution", json.RawMessage(`{}`), version, first); err != nil {
		t.Fatalf("start execution: %v", err)
	}
	start := workflowCurrentStartAtStep(t, s, workID, "implement")
	firstRef, err := WorkflowActorRef(first)
	if err != nil {
		t.Fatal(err)
	}
	if start == nil || start.ActionID != "start_execution" || start.Epoch != 1 || start.ActorRef != firstRef || len(workflowStartReceiptIDs(t, s, workID)) != 1 {
		t.Fatalf("native start = %+v", start)
	}
	startedAt, actorRef := instanceExecutionStart(t, s, workID)
	if startedAt == "" || actorRef != firstRef {
		t.Fatalf("instance execution fence = %q %q", startedAt, actorRef)
	}
	receipts := workflowStartReceiptIDs(t, s, workID)
	bindHold("after-start")
	if got := workflowCurrentStartAtStep(t, s, workID, "implement"); got == nil || *got != *start || !slices.Equal(receipts, workflowStartReceiptIDs(t, s, workID)) {
		t.Fatalf("evidence binding changed the start watermark: %+v -> %+v", start, got)
	}
	if gotTime, gotActor := instanceExecutionStart(t, s, workID); gotTime != startedAt || gotActor != actorRef {
		t.Fatalf("evidence binding changed execution fence: %q %q", gotTime, gotActor)
	}
	if err := runVerdictActionAtVersion(t, s, workID, "start_execution", json.RawMessage(`{}`), version, second); err == nil {
		t.Fatal("stale competing start was admitted")
	}
	if err := runVerdictActionAs(t, s, workID, "start_execution", json.RawMessage(`{}`), 0, second); err == nil {
		t.Fatal("another actor took over a held start without a snapshot")
	}
	if got := workflowCurrentStartAtStep(t, s, workID, "implement"); got == nil || *got != *start || len(workflowStartReceiptIDs(t, s, workID)) != 1 {
		t.Fatalf("refused starts changed fence: %+v -> %+v", start, got)
	}
}

// A revised contract remains live through a native execution start, current
// evidence, independent verdict and completion.
func TestRevisedContractLivenessThroughProducerWork(t *testing.T) {
	t.Parallel()
	const workID = "corrected-contract-liveness"
	fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanFix, "implement")
	s := fixture.store
	current := workContextFixture{store: s, workID: workID, owner: fixture.owner, operator: fixture.operator}
	if err := reviseContractRecord(t, s, workID, fixture.owner, pContractSuccessor(t, s, workID), readWorkVersion(t, s, workID)); err != nil {
		t.Fatalf("revise the contract at the producer step: %v", err)
	}
	if step := currentStep(t, s, workID); step != "implement" {
		t.Fatalf("instance after the contract revision = %q, want implement", step)
	}

	// Binding evidence holds the frontier after the contract revision.
	seedWorkflowAuthority(t, s, "revised-contract-producer", workID, "principal:revision", "request:revision", []string{"artifact:revision"})
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "artifact:revision", "producer_id": "principal:revision", "producer_run_ref": "revised-contract-producer", "producer_watermark": "request:revision"}), 0, fixture.owner); err != nil {
		t.Fatalf("bind evidence after contract revision: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "start_execution", json.RawMessage(`{}`), 0, fixture.owner); err != nil {
		t.Fatalf("start execution after contract revision: %v", err)
	}
	sourceCommit := strings.Repeat("a1", 20)
	bindKind := func(kind, ref string) {
		t.Helper()
		authority := "liveness-authority-" + kind + "-" + workID
		seedWorkflowAuthority(t, s, authority, workID, "principal:liveness-"+kind, "request/liveness-"+kind, []string{ref})
		payload := json.RawMessage(`{"evidence_ref":"evidence:liveness-` + kind + `-` + workID + `","evidence_kind":"` + kind + `","immutable_subject_ref":"` + ref + `","producer_id":"principal:liveness-` + kind + `","producer_run_ref":"` + authority + `","producer_watermark":"request/liveness-` + kind + `"}`)
		if err := runVerdictActionAs(t, s, workID, "bind_evidence", payload, 0, fixture.owner); err != nil {
			t.Fatalf("bind fresh %s evidence: %v", kind, err)
		}
	}
	bindKind("review", "review:liveness-"+workID)
	bindKind("artifact", "artifact:liveness-"+workID)
	completeReviewedStep := func(step string) {
		t.Helper()
		payload := mustJSONValue(map[string]any{"step_id": step, "note": "review completed", "reviewer": "concord-review", "result": "pass", "commit": sourceCommit, "link": "https://example.com/review"})
		if err := runVerdictActionAs(t, s, workID, "record_review", payload, 0, fixture.owner); err != nil {
			t.Fatalf("complete %s: %v", step, err)
		}
	}
	completeReviewedStep("implement")
	if step := currentStep(t, s, workID); step != "review" {
		t.Fatalf("frontier after implement = %q, want review", step)
	}
	if err := runVerdictActionAs(t, s, workID, "record_delivery", json.RawMessage(`{"delivery_artifact":"artifact:liveness-gate-`+workID+`","delivery_state":"asserted"}`), 0, fixture.owner); err != nil {
		t.Fatalf("record delivery on the corrected contract: %v", err)
	}
	if err := pConfirmReceiptPremise(t, current); err != nil {
		t.Fatalf("confirm the corrected premise: %v", err)
	}
	if step := currentStep(t, s, workID); step != "review" {
		t.Fatalf("frontier after the corrected premise = %q, want review", step)
	}
	completeReviewedStep("review")
	completer := diagnosisS1Actor(workID, "completer")
	completePayload := json.RawMessage(`{"impact_verdict":"non-breaking"}`)
	if err := runVerdictActionAs(t, s, workID, "complete", completePayload, 0, completer); err != nil {
		t.Fatalf("complete the corrected contract: %v", err)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("lifecycle after completing the corrected contract = %q, want completed", lifecycle)
	}
}

func TestDiagnosisRecordDoesNotStartExecution(t *testing.T) {
	t.Parallel()
	const workID = "diagnosis-lane-cannot-strand"
	fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanFix, "diagnose")
	s := fixture.store
	actor := fixture.owner
	if start := workflowCurrentStartAtStep(t, s, workID, "diagnose"); start != nil || len(workflowStartReceiptIDs(t, s, workID)) != 0 {
		t.Fatalf("diagnose has an execution start or receipt: %+v", start)
	}
	if err := runVerdictActionAs(t, s, workID, "record_diagnosis", diagnosisPayload("the reproduction", "the search", "none_found", nil, "the cause"), 0, actor); err != nil {
		t.Fatalf("record diagnosis without a start: %v", err)
	}
	if step := currentStep(t, s, workID); step != "diagnose" {
		t.Fatalf("frontier after diagnosis = %q, want diagnose", step)
	}
	if start := workflowCurrentStartAtStep(t, s, workID, "diagnose"); start != nil || len(workflowStartReceiptIDs(t, s, workID)) != 0 {
		t.Fatalf("diagnosis created an execution start: %+v", start)
	}
}

func runVerdictActionAtVersion(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64, owner WorkflowActor) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(context.Background(), s, workID, action)
	if err != nil {
		return err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64) + fmt.Sprint(version), IdempotencyIdentity: action + "-" + workID + "-" + fmt.Sprint(version), OperationID: action + "-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: action + "-" + workID + "-" + fmt.Sprint(version), RequestID: "request:" + action + "-" + workID + "-" + fmt.Sprint(version), ContractDigest: testManifestDigest, Now: time.Unix(11, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// runWorkflowActionWithIdentity runs one action under a distinct operation
// identity, so a repeated action on one version mints its own durable
// operation instead of colliding with the first call's identity.
func runWorkflowActionWithIdentity(t *testing.T, s *Store, workID, action string, payload json.RawMessage, version int64, owner WorkflowActor, suffix string) error {
	t.Helper()
	if version == 0 {
		version = readWorkVersion(t, s, workID)
	}
	operation := action + "-" + suffix + "-" + workID + "-" + fmt.Sprint(version)
	ctx, err := prepareTerminalTestContext(context.Background(), s, workID, action)
	if err != nil {
		return err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs(action, payload), WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64) + operation, IdempotencyIdentity: operation, OperationID: operation,
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operation, RequestID: "request:" + operation, ContractDigest: testManifestDigest, Now: time.Unix(13, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// CD-0198 D2 keeps the search beside the analysis: a Fix item's diagnose
// step completes only after its typed diagnosis record exists.
func TestDiagnoseStepCompletionRequiresRecordedDiagnosis(t *testing.T) {
	const workID = "diagnose-document-gate"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanFix, "diagnose", nil)
	before := readWorkVersion(t, f.store, workID)
	err := runVerdictActionAs(t, f.store, workID, "complete_step", mustJSONValue(map[string]any{"step_id": "diagnose", "note": "no diagnosis recorded"}), 0, f.owner)
	if !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("diagnose completion without a record = %v, want missing_evidence", err)
	}
	if readWorkVersion(t, f.store, workID) != before {
		t.Fatal("refused diagnose completion changed the work version")
	}
	if err := runVerdictActionAs(t, f.store, workID, "record_diagnosis", mustJSONValue(map[string]any{"reproduction": "Repro", "searched": "Search", "outcome": "none_found", "root_cause": "Cause"}), 0, f.owner); err != nil {
		t.Fatalf("record diagnosis: %v", err)
	}
	if err := runVerdictActionAs(t, f.store, workID, "complete_step", mustJSONValue(map[string]any{"step_id": "diagnose", "note": "diagnosis recorded"}), 0, f.owner); err != nil {
		t.Fatalf("diagnose completion with a record: %v", err)
	}
}
