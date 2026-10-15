package store

// Recorded-admission regressions over synthetic journals restored verbatim
// and folded through the production rebuild. Positives require a recorded
// historical admission to fold; negatives pin the refusal the recorded
// alternative must not widen, each attributed to its owning guard kind and
// detail. Replay tolerances (digest drift, off-step actions) apply to every
// journal here, so each negative names the exact refusal it holds, repeats
// deterministically, and requires the refused rebuild to leave no workflow
// projection committed. Every identity is invented; the only real digests
// are the registry's own recorded definition pins, and lane identities come
// from BuiltinLaneDefinitions at runtime.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	recordedAdmissionOccurredAt = "2026-10-15T12:00:00Z"
	recordedAdmissionObservedAt = "2026-10-15T12:30:00Z"

	recordedAdmissionPrincipal = "principal/operator"
	recordedAdmissionClient    = "client/synthetic-c5"
	recordedAdmissionProduct   = "recorded-admission-product"
	recordedAdmissionProject   = "recorded-admission-project"
	recordedAdmissionDomainID  = "domain/recorded-admission-main"

	recordedAdmissionExecutorAgent = "agent/executor"
	recordedAdmissionOwnerAgent    = "agent/owner"
	recordedAdmissionLaneAgent     = "agent/lane"
)

// Archived definition pins are the registry's own recorded entries; see
// recordedCorrectionRoutes and workflowDefinitionVersionPins. The unknown-pin
// controls use registered versions outside the mapped populations.
const (
	recordedPinImplementationV12 = "sha256:26340249dcc709a625db60711bc980acb2c8bdfb8be1588b1b1b80839b8d6f33"
	recordedPinBreakFixV12       = "sha256:ad0d90e5e531160fea75094a38dcbac00b4ca33bafdc346ea02ccfe2fe44f7d2"
	recordedPinImplementationV16 = "sha256:ac13f85d1e460c3f54be49e3550aeac564e510ed66db524ee10f619bd2ea05cc"
	recordedPinBreakFixV14       = "sha256:c89bc73749f03062931776ffb4ba4cbdcbaae023cf0b9d8be3dd841885dbef36"
	recordedPinStaticAnalysisV15 = "sha256:5a561e9d53d26fc0ddec0f84aa72ca5cd2a0018e92dfa5dd0732efd0a8b32ade"

	recordedPinImplementationV10 = "sha256:9335bd4830c5592240e578528582a26b51702f3f6d0b4f20916c47267fd160ac"
	recordedPinImplementationV21 = "sha256:0a6ed9d4f03954f8d8802b4c953cbec7338860f3552aa83bac4b97fcf469c901"
	recordedPinBreakFixV11       = "sha256:2c13955de3099ab12072d1cc450f2fe37a99027903facc11690ad1ea42d2d7a8"
	recordedPinBreakFixV13       = "sha256:c11469369d6aa689997e0f3c801938fd05ee47d7b1eac37e46de91e26eba1573"
	recordedPinBreakFixV15       = "sha256:dc4b081253f694fdd4383792b762b0b03517fa84256f04219c0a6fa15156162c"
	recordedPinBreakFixV18       = "sha256:9b355f4af052d0f48c71d0616177f8b0c2b0cd56bfdfda57a92ea8af46cfaad1"

	recordedUnknownPinImplementationV13 = "sha256:a17d5afc3ee430aeaf406a004e93489daafea7138eb178c8e0ae63506496d341"
	recordedUnknownPinImplementationV17 = "sha256:a50776df39e5a2a6c961b2a580bf320675eacf15d2d1fa0a9d9723d3911cde80"

	recordedAdmissionBlankDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

// Synthetic fixture digests. These carry no identity; they only satisfy the
// digest shape the folds validate. recordedAdmissionWrongDigest matches no
// registered definition and exercises replay digest drift.
var (
	recordedAdmissionInputsDigest     = "sha256:" + strings.Repeat("b", 64)
	recordedAdmissionPacketDigest     = "sha256:" + strings.Repeat("e", 64)
	recordedAdmissionChangedRefDigest = "sha256:" + strings.Repeat("a", 64)
	recordedAdmissionWrongDigest      = "sha256:" + strings.Repeat("f", 64)
)

type recordedAdmissionEvent struct {
	seq            int64
	eventID        string
	kind           string
	subjectType    string
	subjectID      string
	actor          string
	occurredAt     string
	payloadVersion int
	payload        []byte
}

type recordedAdmissionJournal struct {
	workID string
	events []recordedAdmissionEvent
}

func (j *recordedAdmissionJournal) append(kind, subjectType, subjectID, actor string, payloadVersion int, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(fmt.Sprintf("synthetic recorded-admission payload for %s cannot marshal: %v", kind, err))
	}
	position := int64(len(j.events) + 1)
	j.events = append(j.events, recordedAdmissionEvent{
		seq:            position,
		eventID:        fmt.Sprintf("%s-%s-%03d", subjectID, kind, position),
		kind:           kind,
		subjectType:    subjectType,
		subjectID:      subjectID,
		actor:          actor,
		occurredAt:     recordedAdmissionOccurredAt,
		payloadVersion: payloadVersion,
		payload:        raw,
	})
}

// recordedAdmissionWork builds one synthetic work item's journal with its
// version chain tracked exactly as the folds consume it.
type recordedAdmissionWork struct {
	journal  *recordedAdmissionJournal
	workID   string
	version  int64
	actorRef map[string]string
}

func recordedAdmissionStartWork(t *testing.T, workID string) *recordedAdmissionWork {
	t.Helper()
	j := &recordedAdmissionJournal{workID: workID}
	j.append("product.created", "product", recordedAdmissionProduct, "operator", 1, map[string]any{
		"display_name": "Synthetic", "stage_audience_commitment": "operator_only", "stage_maturity": "prototype",
	})
	j.append("project.created", "project", recordedAdmissionProject, "operator", 1, map[string]any{
		"display_name": "Synthetic",
	})
	j.append("product_project.added", "product", recordedAdmissionProduct, "operator", 1, map[string]any{
		"expected_version": int64(1), "product_id": recordedAdmissionProduct, "project_id": recordedAdmissionProject,
		"reason": "synthetic recorded-admission fixture", "resulting_version": int64(2), "role": "primary",
	})
	j.append("work.created", "work_item", workID, "operator", 2, map[string]any{
		"priority": int64(10), "title": workID, "work_kind": "task",
	})
	j.append("work_project.added", "work_item", workID, "operator", 1, map[string]any{
		"expected_version": int64(1), "project_id": recordedAdmissionProject,
		"reason": "synthetic recorded-admission fixture", "resulting_version": int64(2), "role": "primary", "work_id": workID,
	})
	return &recordedAdmissionWork{journal: j, workID: workID, version: 2, actorRef: map[string]string{}}
}

func (w *recordedAdmissionWork) recordActor(t *testing.T, class, agentRef string) string {
	t.Helper()
	ref := DeriveWorkflowActorRef(recordedAdmissionPrincipal, recordedAdmissionClient, agentRef, "session/"+w.workID)
	w.journal.append("workflow.actor_recorded", "work_item", w.workID, ref, 1, map[string]any{
		"actor_class": class, "actor_ref": ref, "agent_ref": agentRef, "client_ref": recordedAdmissionClient,
		"expected_version": w.version, "principal_ref": recordedAdmissionPrincipal,
		"resulting_version": w.version + 1, "session_ref": "session/" + w.workID, "work_id": w.workID,
	})
	w.version++
	w.actorRef[agentRef] = ref
	return ref
}

func (w *recordedAdmissionWork) selectDefinition(t *testing.T, workKind, ref string, version int64, digest string) {
	t.Helper()
	w.journal.append("workflow.definition_selected", "work_item", w.workID, w.actorRef[recordedAdmissionExecutorAgent], 1, map[string]any{
		"digest": digest, "expected_version": w.version, "ref": ref,
		"resulting_version": w.version + 1, "version": version, "work_id": w.workID, "work_kind": workKind,
	})
	w.version++
}

func (w *recordedAdmissionWork) approveContract(t *testing.T, productTruthChanging bool) {
	t.Helper()
	payloadVersion := 1
	payload := map[string]any{
		"consequence_class": "internal_sqlite", "contract_version": int64(1), "expected_version": w.version,
		"law_modifies": []string{}, "outcome_kind": "check",
		"outcome_payload": map[string]any{
			"check_ref": "check:" + w.workID, "expected_result": "pass", "immutable_subject_ref": "commit:" + w.workID, "kind": "check",
		},
		"premise":           "Synthetic recorded-admission contract for " + w.workID,
		"required_evidence": []string{"verification"}, "resulting_version": w.version + 1,
		"rigor_class": "prototype_internal", "route_conventions": []string{}, "spec_mandate": []string{},
		"work_id": w.workID,
	}
	if productTruthChanging {
		// A Product-changing approval carries its architecture authority on
		// the v2 contract shape: the predicate list plus the complete
		// binding, boundary, and law-pin fields.
		payloadVersion = 2
		delete(payload, "outcome_kind")
		delete(payload, "outcome_payload")
		payload["outcome_predicates"] = []any{map[string]any{
			"ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{
				"check_ref": "check:" + w.workID, "expected_result": "pass", "immutable_subject_ref": "commit:" + w.workID, "kind": "check",
			},
			"predicate_id": "predicate:primary",
		}}
		payload["law_boundary_version"] = int(1)
		payload["law_revisions"] = []any{}
		payload["architecture_binding"] = map[string]any{
			"domain_registry_content_hash": recordedAdmissionBlankDigest, "home_domain_id": recordedAdmissionDomainID,
			"affected_domain_ids": []string{recordedAdmissionDomainID}, "domain_modifies": []string{},
			"domain_relation_modifies": []any{}, "law_additions": []any{}, "verification_obligations": []any{},
		}
	}
	w.journal.append("workflow.contract_approved", "work_item", w.workID, w.actorRef[recordedAdmissionExecutorAgent], payloadVersion, payload)
	w.version++
}

func (w *recordedAdmissionWork) completeAction(t *testing.T, actionID, stepID, actor string, payloadVersion int, extra map[string]any) {
	t.Helper()
	payload := map[string]any{
		"action_id": actionID, "actor_ref": actor, "attempt_epoch": int64(1), "changed_refs": []string{},
		"expected_version": w.version, "result_evidence_refs": []string{}, "resulting_version": w.version + 1,
		"step_id": stepID, "work_id": w.workID,
	}
	for key, value := range extra {
		payload[key] = value
	}
	w.journal.append("workflow.action_completed", "work_item", w.workID, actor, payloadVersion, payload)
	w.version++
}

func (w *recordedAdmissionWork) startAction(t *testing.T, actionID, stepID, actor string, epoch int64, identity string) {
	t.Helper()
	w.journal.append("workflow.action_started", "work_item", w.workID, actor, 1, map[string]any{
		"accepted_inputs_digest": recordedAdmissionInputsDigest, "action_id": actionID, "actor_ref": actor,
		"attempt_epoch": epoch, "expected_version": w.version, "idempotency_identity": identity,
		"resulting_version": w.version + 1, "step_id": stepID, "work_id": w.workID,
	})
	w.version++
}

// checkpoint records one recorded checkpoint at the fenced external-effect
// step every scenario in this file checkpoints; the step kind is the payload's
// own, not a caller choice.
func (w *recordedAdmissionWork) checkpoint(t *testing.T, stepID, actor string, epoch int64, payloadVersion int) {
	t.Helper()
	w.journal.append("workflow.action_checkpointed", "work_item", w.workID, actor, payloadVersion, map[string]any{
		"attempt_epoch": epoch, "checkpoint_payload": map[string]any{"note": "synthetic recorded checkpoint"},
		"expected_version": w.version, "resume_cursor": "", "resulting_version": w.version + 1,
		"step_id": stepID, "step_kind": string(WorkflowStepExternalEffect), "work_id": w.workID, "actor_ref": actor,
		"request_id": "request/" + w.workID + "/checkpoint",
	})
	w.version++
}

func (w *recordedAdmissionWork) dispatchCompletion(t *testing.T, actionStep, actor string, epoch int64, attemptID string, laneVersion int64, laneDigest string) {
	t.Helper()
	w.completeAction(t, "dispatch_worker", actionStep, actor, 4, map[string]any{
		"attempt_epoch": epoch, "worker_attempt_id": attemptID, "worker_capability_class": "research",
		"worker_lane_digest": laneDigest, "worker_lane_id": "research", "worker_lane_version": laneVersion,
		"worker_packet_digest": recordedAdmissionPacketDigest, "worker_packet_predicate_ids": []string{},
	})
}

func (w *recordedAdmissionWork) workerDispatched(t *testing.T, attemptID, laneActor string, laneVersion int64, laneDigest string) {
	t.Helper()
	w.journal.append("worker.dispatched", "work_item", w.workID, "worker:test", 5, map[string]any{
		"attempt_id": attemptID, "lane_id": "research", "lane_version": laneVersion, "lane_digest": laneDigest,
		"capability_class": "research", "packet_schema_version": "1.1", "report_schema_version": "1.1",
		"packet_digest": recordedAdmissionPacketDigest, "readback_model": "synthetic/model-a", "lane_actor_ref": laneActor,
	})
}

func (w *recordedAdmissionWork) workerFailed(t *testing.T, attemptID string) {
	t.Helper()
	w.journal.append("worker.failed", "work_item", w.workID, "worker:test", 2, map[string]any{
		"attempt_id": attemptID, "readback_model": "synthetic/model-a", "failure_kind": "worker_error",
		"detail": "synthetic recorded-admission fixture: the disposed attempt ended in a bounded error",
	})
}

func (w *recordedAdmissionWork) evidenceBound(t *testing.T, kind, subjectRef, actor, opID, producerID, requestID string) {
	t.Helper()
	w.journal.append("workflow.evidence_bound", "work_item", w.workID, actor, 1, map[string]any{
		"evidence_kind": kind, "expected_version": w.version, "immutable_subject_ref": subjectRef,
		"observed_at": recordedAdmissionObservedAt, "producer_id": producerID, "producer_run_ref": opID,
		"producer_watermark": requestID, "resulting_version": w.version + 1, "work_id": w.workID,
	})
	w.version++
}

func (w *recordedAdmissionWork) verdict(t *testing.T, actor, evidenceRef string) {
	t.Helper()
	w.journal.append("workflow.verdict_recorded", "work_item", w.workID, actor, 1, map[string]any{
		"contract_version": int64(1), "evaluation_evidence": []string{evidenceRef}, "expected_version": w.version,
		"incomparable_with_approved": false, "predicate_id": "predicate:primary", "resulting_version": w.version + 1,
		"verdict_actor_ref": actor, "verdict_kind": "ok", "work_id": w.workID,
	})
	w.version++
}

func (w *recordedAdmissionWork) confirmPremise(t *testing.T, actor string) {
	t.Helper()
	w.journal.append("workflow.premise_confirmed", "work_item", w.workID, actor, 1, map[string]any{
		"confirming_actor_ref": actor, "contract_version": int64(1), "expected_version": w.version,
		"resulting_version": w.version + 1, "work_id": w.workID,
	})
	w.version++
}

func (w *recordedAdmissionWork) completeWorkflow(t *testing.T, actor string) {
	t.Helper()
	w.journal.append("workflow.completed", "work_item", w.workID, actor, 2, map[string]any{
		"changed_refs_digest": recordedAdmissionChangedRefDigest, "evidence_count": int64(3), "expected_version": w.version,
		"final_verdict_kind": "ok", "impact_verdict": "non-breaking", "premise_confirmed": true,
		"resulting_version": w.version + 1, "terminal_state": "completed", "verdict_actor_ref": actor, "work_id": w.workID,
	})
	w.version++
}

type recordedAdmissionReceipt struct {
	opID       string
	typeRef    string
	typeVer    int64
	stepID     string
	stepKind   string
	refs       []string
	producerID string
	requestID  string
}

func restoreRecordedAdmissionJournal(t *testing.T, journal *recordedAdmissionJournal, receipts []recordedAdmissionReceipt) *Store {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, event := range journal.events {
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`,
			event.seq, event.eventID, event.kind, event.subjectType, event.subjectID, event.actor, event.occurredAt, event.payloadVersion, string(event.payload)); err != nil {
			t.Fatalf("restore synthetic event %s: %v", event.eventID, err)
		}
	}
	for _, receipt := range receipts {
		refs, err := json.Marshal(receipt.refs)
		if err != nil {
			t.Fatalf("synthetic receipt %s refs: %v", receipt.opID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			receipt.opID, int64(1), journal.workID, receipt.typeRef, receipt.typeVer, receipt.stepID, receipt.stepKind,
			recordedAdmissionInputsDigest, "{}", "completed", "", string(refs), "[]", receipt.producerID, receipt.requestID,
			recordedAdmissionObservedAt, recordedAdmissionObservedAt, recordedAdmissionBlankDigest); err != nil {
			t.Fatalf("restore synthetic receipt %s: %v", receipt.opID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func recordedAdmissionInstanceState(t *testing.T, s *Store, workID string) (string, string, string) {
	t.Helper()
	var state, step, actor string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT instance_state,current_step,COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, workID).Scan(&state, &step, &actor); err != nil {
		t.Fatalf("synthetic workflow instance %s is missing: %v", workID, err)
	}
	return state, step, actor
}

// recordedAdmissionAssertLogIntact requires the rebuild to leave every
// restored event header and payload byte exactly as stored. The fixture store
// carries only this journal, so the whole domain_events table is compared,
// including the subject headers.
func recordedAdmissionAssertLogIntact(t *testing.T, s *Store, journal *recordedAdmissionJournal) {
	t.Helper()
	ctx := context.Background()
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	index := 0
	for rows.Next() {
		if index >= len(journal.events) {
			t.Fatalf("rebuild appended events beyond the restored journal")
		}
		want := journal.events[index]
		var gotSubjectType, gotSubjectID, payload string
		var gotSeq int64
		var gotEventID, gotKind, gotActor, gotOccurredAt string
		var gotPayloadVersion int
		if err := rows.Scan(&gotSeq, &gotEventID, &gotKind, &gotSubjectType, &gotSubjectID, &gotActor, &gotOccurredAt, &gotPayloadVersion, &payload); err != nil {
			t.Fatal(err)
		}
		if gotSeq != want.seq || gotEventID != want.eventID || gotKind != want.kind || gotSubjectType != want.subjectType || gotSubjectID != want.subjectID || gotActor != want.actor || gotOccurredAt != want.occurredAt || gotPayloadVersion != want.payloadVersion || payload != string(want.payload) {
			t.Fatalf("restored event %s moved across rebuild: got %s/%s/%s/%s/%d, want seq %d subject %s/%s actor %s payload version %d with its exact bytes", want.eventID, gotEventID, gotKind, gotSubjectType, gotSubjectID, gotPayloadVersion, want.seq, want.subjectType, want.subjectID, want.actor, want.payloadVersion)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(journal.events) {
		t.Fatalf("rebuild retained %d of %d restored events", index, len(journal.events))
	}
}

func recordedAdmissionAssertNoWorkflowProjections(t *testing.T, s *Store) {
	t.Helper()
	var instances int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM workflow_instances`).Scan(&instances); err != nil {
		t.Fatal(err)
	}
	if instances != 0 {
		t.Fatalf("refused rebuild committed %d workflow instance projections; rollback must restore the pre-rebuild state", instances)
	}
}

// recordedAdmissionRequireRefusal restores the journal, requires the rebuild
// to refuse with the named guard, repeats the rebuild deterministically, and
// requires the refusal to commit no projection and retain the log bytes.
func recordedAdmissionRequireRefusal(t *testing.T, w *recordedAdmissionWork, receipts []recordedAdmissionReceipt, wantKind FailureKind, wantDetail string) {
	t.Helper()
	s := restoreRecordedAdmissionJournal(t, w.journal, receipts)
	defer s.Close()
	err := RebuildFromLog(context.Background(), s)
	if err == nil {
		t.Fatalf("expected the rebuild to refuse with %s (%q); it admitted the journal", wantKind, wantDetail)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("rebuild refusal is not a typed failure: %v", err)
	}
	if failure.Kind != wantKind || !strings.Contains(failure.Detail, wantDetail) {
		t.Fatalf("refusal %s/%q does not name the expected guard %s/%q", failure.Kind, failure.Detail, wantKind, wantDetail)
	}
	if again := RebuildFromLog(context.Background(), s); again == nil {
		t.Fatal("repeat rebuild admitted what the first rebuild refused")
	}
	recordedAdmissionAssertNoWorkflowProjections(t, s)
	recordedAdmissionAssertLogIntact(t, s, w.journal)
}

// recordedAdmissionRequireFold restores the journal and requires the rebuild
// to fold it; the caller asserts the folded effects.
func recordedAdmissionRequireFold(t *testing.T, w *recordedAdmissionWork, receipts []recordedAdmissionReceipt) *Store {
	t.Helper()
	s := restoreRecordedAdmissionJournal(t, w.journal, receipts)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the recorded admission must fold: %v", err)
	}
	recordedAdmissionAssertLogIntact(t, s, w.journal)
	return s
}

// recordedAdmissionRebuildStable rebuilds a second time and requires the
// projection hash, the instance row, and the log bytes to reproduce.
func recordedAdmissionRebuildStable(t *testing.T, s *Store, journal *recordedAdmissionJournal) {
	t.Helper()
	ctx := context.Background()
	before, err := WorkflowProjectionHash(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	state, step, actor := recordedAdmissionInstanceState(t, s, journal.workID)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("second rebuild of the synthetic journal refused: %v", err)
	}
	after, err := WorkflowProjectionHash(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("projection hash moved across rebuild of the synthetic journal: %s != %s", before, after)
	}
	if replayed, replayedStep, replayedActor := recordedAdmissionInstanceState(t, s, journal.workID); replayed != state || replayedStep != step || replayedActor != actor {
		t.Fatalf("instance row moved across rebuild: %q/%q/%q != %q/%q/%q", replayed, replayedStep, replayedActor, state, step, actor)
	}
	recordedAdmissionAssertLogIntact(t, s, journal)
}

// recordedAdmissionBackupRoundtrip snapshots the fixture store and requires
// the restored copy to reproduce the projection hash and the instance row.
func recordedAdmissionBackupRoundtrip(t *testing.T, s *Store, journal *recordedAdmissionJournal) {
	t.Helper()
	ctx := context.Background()
	backup := filepath.Join(t.TempDir(), "concord-backup.db")
	if _, err := Backup(ctx, s, backup); err != nil {
		t.Fatalf("backup the synthetic fixture store: %v", err)
	}
	restored := filepath.Join(t.TempDir(), "concord-restored.db")
	if _, err := RestoreBackup(ctx, backup, restored); err != nil {
		t.Fatalf("restore the synthetic fixture backup: %v", err)
	}
	reopened := openTempAtPath(t, restored)
	defer reopened.Close()
	before, err := WorkflowProjectionHash(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	after, err := WorkflowProjectionHash(ctx, reopened)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("restored backup projection hash %s does not reproduce the source %s", after, before)
	}
	wantState, wantStep, wantActor := recordedAdmissionInstanceState(t, s, journal.workID)
	if state, step, actor := recordedAdmissionInstanceState(t, reopened, journal.workID); state != wantState || step != wantStep || actor != wantActor {
		t.Fatalf("restored backup instance = %q/%q/%q, want %q/%q/%q", state, step, actor, wantState, wantStep, wantActor)
	}
}

// Candidate A scenario: walk a pinned graph to its fenced step, run one
// dispatched attempt, and optionally dispose it and add the newer fenced
// explicit start. The tests own every variant beyond this prefix.
type recordedAdmissionFencedPlan struct {
	workID       string
	family       string
	ref          string
	version      int64
	storedDigest string
	walk         []recordedAdmissionStep
	startAction  string
	fencedStep   string
}

type recordedAdmissionStep struct{ stepID, actionID string }

var (
	recordedAdmissionImplementationWalk = []recordedAdmissionStep{
		{"proposal", "record_proposal"}, {"discovery", "record_discovery"},
		{"design", "record_design"}, {"planning", "approve_contract"},
	}
	recordedAdmissionBreakFixWalk = []recordedAdmissionStep{
		{"reproduce", "record_reproduction"}, {"diagnose", "record_root_cause"}, {"planning", "approve_contract"},
	}
)

type recordedAdmissionLaneRun struct {
	work        *recordedAdmissionWork
	executor    string
	owner       string
	lane        string
	attempt1    string
	attempt2    string
	laneVersion int64
	laneDigest  string
	plan        recordedAdmissionFencedPlan
}

func recordedAdmissionRunDisposedLane(t *testing.T, plan recordedAdmissionFencedPlan, withStart1, withDispatch, withDisposal bool) *recordedAdmissionLaneRun {
	t.Helper()
	w := recordedAdmissionStartWork(t, plan.workID)
	executor := w.recordActor(t, "agent", recordedAdmissionExecutorAgent)
	owner := w.recordActor(t, "agent", recordedAdmissionOwnerAgent)
	lane := w.recordActor(t, "agent", recordedAdmissionLaneAgent)
	w.selectDefinition(t, plan.family, plan.ref, plan.version, plan.storedDigest)
	w.approveContract(t, true)
	for _, step := range plan.walk {
		w.completeAction(t, step.actionID, step.stepID, executor, 1, nil)
	}
	laneVersion, laneDigest := registeredLaneIdentity(t, "research")
	run := &recordedAdmissionLaneRun{
		work: w, executor: executor, owner: owner, lane: lane,
		attempt1: "attempt/" + plan.workID + "-one", attempt2: "attempt/" + plan.workID + "-two",
		laneVersion: laneVersion, laneDigest: laneDigest, plan: plan,
	}
	if withStart1 {
		w.startAction(t, plan.startAction, plan.fencedStep, executor, 1, "synthetic:"+plan.workID+":1")
	}
	if withDispatch {
		w.dispatchCompletion(t, plan.fencedStep, owner, 1, run.attempt1, laneVersion, laneDigest)
		w.workerDispatched(t, run.attempt1, lane, laneVersion, laneDigest)
	}
	if withDisposal {
		w.workerFailed(t, run.attempt1)
		w.completeAction(t, "record_worker_failure", plan.fencedStep, owner, 4, map[string]any{
			"attempt_epoch": int64(1), "worker_attempt_id": run.attempt1,
		})
	}
	return run
}

// recordedAdmissionStartNewAttempt appends the newer fenced explicit start
// owned by the executor and its completing completion.
func (r *recordedAdmissionLaneRun) startNewAttempt(t *testing.T, actionID, stepID string, epoch int64) {
	t.Helper()
	r.work.startAction(t, actionID, stepID, r.executor, epoch, "synthetic:"+r.plan.workID+":"+stepID+":"+fmt.Sprint(epoch))
	r.work.completeAction(t, actionID, stepID, r.executor, 1, map[string]any{"attempt_epoch": epoch})
}

// recordedAdmissionRequireStoredDigestRefusal exercises the real stored
// tuple: the journal's final event is withheld so the prefix rebuild leaves a
// canonical instance row, then the stored digest is corrupted under the
// fold's own transaction guard, the final event row is recorded at its real
// sequence, and the recorded event is folded directly under the replay
// context. The typed refusal must name the expected guard, and the deferred
// rollback restores the rebuilt state. The registry entry stays canonical
// throughout, so only a stored-tuple comparison can refuse.
func recordedAdmissionRequireStoredDigestRefusal(t *testing.T, w *recordedAdmissionWork, receipts []recordedAdmissionReceipt, wrongDigest string, wantKind FailureKind, wantDetail string) {
	t.Helper()
	if len(w.journal.events) < 2 {
		t.Fatal("stored-digest scenario needs a prefix and a target event")
	}
	prefix := &recordedAdmissionJournal{workID: w.workID, events: w.journal.events[:len(w.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, receipts)
	defer s.Close()
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the digest-mismatch prefix must fold before the stored tuple is corrupted: %v", err)
	}
	target := w.journal.events[len(w.journal.events)-1]
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	scope, err := beginFold(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scope.close(context.Background()) }()
	if _, err := tx.ExecContext(context.Background(), `UPDATE workflow_instances SET definition_digest=? WHERE work_id=?`, wrongDigest, w.workID); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := tx.QueryRowContext(context.Background(), `SELECT definition_digest FROM workflow_instances WHERE work_id=?`, w.workID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != wrongDigest {
		t.Fatalf("stored tuple digest = %q, want the corrupted %q", stored, wrongDigest)
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`,
		target.seq, target.eventID, target.kind, target.subjectType, target.subjectID, target.actor, target.occurredAt, target.payloadVersion, string(target.payload)); err != nil {
		t.Fatalf("record the target event %s: %v", target.eventID, err)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, target.occurredAt)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		Seq: target.seq, EventID: target.eventID, Kind: target.kind, SubjectType: SubjectType(target.subjectType),
		SubjectID: target.subjectID, Actor: target.actor, OccurredAt: occurredAt,
		PayloadVersion: target.payloadVersion, Payload: json.RawMessage(target.payload),
	}
	foldErr := foldRecordedEvent(workflowReplayContext(context.Background()), tx, event, scope)
	var failure *Failure
	if !errors.As(foldErr, &failure) {
		t.Fatalf("stored-digest fold error is not a typed failure: %v", foldErr)
	}
	if failure.Kind != wantKind || !strings.Contains(failure.Detail, wantDetail) {
		t.Fatalf("stored-digest refusal %s/%q does not name the expected guard %s/%q", failure.Kind, failure.Detail, wantKind, wantDetail)
	}
}

func implementationPlan(workID string, version int64, digest string) recordedAdmissionFencedPlan {
	return recordedAdmissionFencedPlan{
		workID: workID, family: "implementation", ref: "workflow.implementation", version: version,
		storedDigest: digest, walk: recordedAdmissionImplementationWalk,
		startAction: "start_execution", fencedStep: "execution",
	}
}

func breakFixPlan(workID string, version int64, digest string) recordedAdmissionFencedPlan {
	return recordedAdmissionFencedPlan{
		workID: workID, family: "break_fix", ref: "workflow.break_fix", version: version,
		storedDigest: digest, walk: recordedAdmissionBreakFixWalk,
		startAction: "start_repair", fencedStep: "repair",
	}
}

// Candidate A, implementation pin: the executor's recorded original-v1
// checkpoint after the latest disposed attempt must fold when the newer
// fenced explicit start owns the executing actor.
func TestRecordedWorkflowAdmissionCheckpointNewExecutorAfterDisposedLaneImplementationV12(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-impl", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 2, 1)

	s := recordedAdmissionRequireFold(t, run.work, nil)
	defer s.Close()
	if state, step, actor := recordedAdmissionInstanceState(t, s, run.plan.workID); state != "running" || step != run.plan.fencedStep || actor != run.executor {
		t.Fatalf("replayed instance = %q/%q/%q, want running/%s/%q", state, step, actor, run.plan.fencedStep, run.executor)
	}
	var checkpoints int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM workflow_checkpoints WHERE work_id=? AND step_id=? AND attempt_epoch=2 AND actor_ref=?`, run.plan.workID, run.plan.fencedStep, run.executor).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 1 {
		t.Fatalf("recorded checkpoint rows = %d, want 1", checkpoints)
	}
	recordedAdmissionRebuildStable(t, s, run.work.journal)
	recordedAdmissionBackupRoundtrip(t, s, run.work.journal)
}

// Candidate A, break-fix pin: the same disposed-lane shape on the archived
// break_fix v12 graph must admit the executor's recorded checkpoint.
func TestRecordedWorkflowAdmissionCheckpointNewExecutorAfterDisposedLaneBreakFixV12(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, breakFixPlan("recorded-admission-ckpt-bfix", 12, recordedPinBreakFixV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 2, 1)

	s := recordedAdmissionRequireFold(t, run.work, nil)
	defer s.Close()
	if state, step, actor := recordedAdmissionInstanceState(t, s, run.plan.workID); state != "running" || step != run.plan.fencedStep || actor != run.executor {
		t.Fatalf("replayed instance = %q/%q/%q, want running/%s/%q", state, step, actor, run.plan.fencedStep, run.executor)
	}
	var checkpoints int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM workflow_checkpoints WHERE work_id=? AND step_id=? AND attempt_epoch=2 AND actor_ref=?`, run.plan.workID, run.plan.fencedStep, run.executor).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 1 {
		t.Fatalf("recorded checkpoint rows = %d, want 1", checkpoints)
	}
	recordedAdmissionRebuildStable(t, s, run.work.journal)
}

// The refine variant: the disposed attempt belongs to execution and the
// newer fenced explicit start is start_refine, whose fold moves the instance
// to refine itself; the checkpoint lands at refine with the newer start
// strictly after the older disposal. No delivery advance is involved.
func TestRecordedWorkflowAdmissionCheckpointNewExecutorAtRefineImplementationV12(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-refine", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, "start_refine", "refine", 1)
	run.work.checkpoint(t, "refine", run.executor, 1, 1)

	s := recordedAdmissionRequireFold(t, run.work, nil)
	defer s.Close()
	if state, step, actor := recordedAdmissionInstanceState(t, s, run.plan.workID); state != "running" || step != "refine" || actor != run.executor {
		t.Fatalf("replayed instance = %q/%q/%q, want running/refine/%q", state, step, actor, run.executor)
	}
	var checkpoints int
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT count(*) FROM workflow_checkpoints WHERE work_id=? AND step_id='refine' AND attempt_epoch=1 AND actor_ref=?`, run.plan.workID, run.executor).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	if checkpoints != 1 {
		t.Fatalf("recorded checkpoint rows = %d, want 1", checkpoints)
	}
	recordedAdmissionRebuildStable(t, s, run.work.journal)
}

// A negative: a live (non-replay) append of the same executor checkpoint
// keeps the existing refusal; the recorded alternative is replay-only.
func TestRecordedWorkflowAdmissionCheckpointLiveAppendKeepsExistingRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-live", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)

	s := recordedAdmissionRequireFold(t, run.work, nil)
	defer s.Close()
	version := run.work.version
	event := workflowEventWithActor(run.plan.workID+"-live-checkpoint", WorkflowActionCheckpointed, run.plan.workID, run.executor, map[string]any{
		"attempt_epoch": int64(2), "checkpoint_payload": map[string]any{"note": "synthetic live checkpoint"},
		"expected_version": version, "resume_cursor": "", "resulting_version": version + 1,
		"step_id": run.plan.fencedStep, "step_kind": string(WorkflowStepExternalEffect), "work_id": run.plan.workID,
		"actor_ref": run.executor, "request_id": "request/" + run.plan.workID + "/live-checkpoint",
	})
	err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, run.plan.workID): version},
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "checkpoint actor does not hold the workflow writing authority") {
		t.Fatalf("live checkpoint refusal = %v, want the unchanged writing-authority guard", err)
	}
}

// A negative: a recorded checkpoint stored at source payload version 2 is no
// recorded original-v1 event; the decode boundary keeps refusing it.
func TestRecordedWorkflowAdmissionCheckpointSource2PayloadKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-src2", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 2, 2)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindUnsupportedPayloadVersion, "uses payload version 2")
}

// A negative: a registered version outside the mapped pin population keeps
// the existing refusal even though its journal is otherwise identical.
func TestRecordedWorkflowAdmissionCheckpointUnknownPinKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-unknown", 13, recordedUnknownPinImplementationV13), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 2, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindUnauthorized, "checkpoint actor does not hold the workflow writing authority")
}

// A negative: the stored pin digest must match the registry exactly. The
// definition fold canonicalizes the stored digest, so the wrong digest is
// written into the stored tuple directly and the target checkpoint is folded
// from its recorded row; the replay drift tolerance must not become an
// admission path.
func TestRecordedWorkflowAdmissionCheckpointStoredDigestMismatchKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-digest", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 2, 1)
	recordedAdmissionRequireStoredDigestRefusal(t, run.work, nil, recordedAdmissionWrongDigest, KindUnauthorized, "checkpoint actor does not hold the workflow writing authority")
}

// A negative: a fenced-step checkpoint with no explicit start is an attempt
// that never ran; the epoch guard refuses before any writer rule.
func TestRecordedWorkflowAdmissionCheckpointWithoutExplicitStartRefuses(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-nostart", 12, recordedPinImplementationV12), false, true, true)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 1, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindIllegalLifecycleTransition, "checkpoint does not match the latest workflow action start epoch")
}

// A negative: the checkpoint must name the latest started attempt.
func TestRecordedWorkflowAdmissionCheckpointStaleEpochRefuses(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-stale", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 1, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindIllegalLifecycleTransition, "checkpoint does not match the latest workflow action start epoch")
}

// A negative: a checkpoint naming a step other than the pinned current step
// never gains the pen; the fold reads epochs and authority at the instance's
// step, and the recorded alternative requires exact step agreement.
func TestRecordedWorkflowAdmissionCheckpointForeignStepKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-step", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, "refine", run.executor, 2, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindUnauthorized, "checkpoint actor does not hold the workflow writing authority")
}

// A negative: a third recorded actor is neither the newer start's executor
// nor the disposed attempt's disposer; the writing rule keeps refusing.
func TestRecordedWorkflowAdmissionCheckpointForeignActorKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-actor", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.lane, 2, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindUnauthorized, "checkpoint actor does not hold the workflow writing authority")
}

// A negative: while the lane attempt is live (no matching disposal), the pen
// belongs to the lane alone and no recorded alternative rescues the owner.
func TestRecordedWorkflowAdmissionCheckpointLiveLaneKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-inflight", 12, recordedPinImplementationV12), true, true, false)
	run.work.checkpoint(t, run.plan.fencedStep, run.owner, 1, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindUnauthorized, "checkpoint actor does not hold the workflow writing authority")
}

// A negative: a newer dispatch authorization after the executor's start
// supersedes the proof; the executor's checkpoint keeps refusing.
func TestRecordedWorkflowAdmissionCheckpointNewerDispatchAuthorizationKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-redispatch", 12, recordedPinImplementationV12), true, true, true)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.dispatchCompletion(t, run.plan.fencedStep, run.owner, 2, run.attempt2, run.laneVersion, run.laneDigest)
	run.work.workerDispatched(t, run.attempt2, run.lane, run.laneVersion, run.laneDigest)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 2, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindUnauthorized, "checkpoint actor does not hold the workflow writing authority")
}

// A counterexample: an in-flight authorization recorded before the newer
// start, with no worker.dispatched row at all, must not hand the pen to the
// recorded owner. With no dispatch row the ordinary writer rule keeps the pen
// with the execution actor alone, so the owner's checkpoint at the newer
// start's epoch reaches the denial alternative; the authorization's in-flight
// worker_attempts binding and the missing dispatch row are the incomplete
// historical proof, and the fold keeps its refusal.
func TestRecordedWorkflowAdmissionCheckpointInFlightAuthorizationWithoutDispatchRowKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-authonly", 12, recordedPinImplementationV12), true, false, false)
	run.work.dispatchCompletion(t, run.plan.fencedStep, run.owner, 1, run.attempt1, run.laneVersion, run.laneDigest)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.owner, 2, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindUnauthorized, "checkpoint actor does not hold the workflow writing authority")
}

// A counterexample: the disposed window is only half recorded — the attempt
// failed but no disposition names it. While the latest dispatch has no
// disposal the ordinary writer rule keeps the pen with the execution actor
// alone, so the recorded owner's checkpoint at the newer start's epoch reaches
// the denial alternative; the missing disposal is the incomplete historical
// proof, and the fold keeps its refusal.
func TestRecordedWorkflowAdmissionCheckpointMissingDisposalKeepsRefusal(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-nodisposal", 12, recordedPinImplementationV12), true, true, false)
	run.work.workerFailed(t, run.attempt1)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.owner, 2, 1)
	recordedAdmissionRequireRefusal(t, run.work, nil, KindUnauthorized, "checkpoint actor does not hold the workflow writing authority")
}

// recordedAdmissionRequireRecordedCheckpointRefusal isolates the historical
// recorded-checkpoint helper on an incomplete proof. The prefix — the journal
// without its final checkpoint — rebuilds into the canonical pinned instance;
// the recorded executor checkpoint is then judged by the helper alone, under
// the private replay context, a real transaction over the rebuilt state, and
// the registry's own archived pin. The checkpoint's writer is the execution
// actor, so the actor gate stays out of the way and only the missing proof
// can refuse. The deferred rollback discards the probe transaction.
func recordedAdmissionRequireRecordedCheckpointRefusal(t *testing.T, run *recordedAdmissionLaneRun, scenario string) {
	t.Helper()
	prefix := &recordedAdmissionJournal{workID: run.work.workID, events: run.work.journal.events[:len(run.work.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, nil)
	defer s.Close()
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the %s prefix must fold before the recorded checkpoint helper is judged: %v", scenario, err)
	}
	target := run.work.journal.events[len(run.work.journal.events)-1]
	if target.kind != string(WorkflowActionCheckpointed) {
		t.Fatalf("withheld target %s is %s, want the recorded checkpoint", target.eventID, target.kind)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	entry, err := VerifyWorkflowInstanceDefinitionTx(context.Background(), tx, BuiltinWorkflowRegistry(), run.work.workID)
	if err != nil {
		t.Fatalf("the rebuilt prefix must carry the registered archived pin: %v", err)
	}
	var currentStep, executionActor string
	if err := tx.QueryRowContext(context.Background(), `SELECT current_step,COALESCE(execution_actor_ref,'') FROM workflow_instances WHERE work_id=?`, run.work.workID).Scan(&currentStep, &executionActor); err != nil {
		t.Fatal(err)
	}
	var p workflowActionCheckpointedPayload
	if err := json.Unmarshal(target.payload, &p); err != nil {
		t.Fatal(err)
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, target.occurredAt)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{
		Seq: target.seq, EventID: target.eventID, Kind: target.kind, SubjectType: SubjectType(target.subjectType),
		SubjectID: target.subjectID, Actor: target.actor, OccurredAt: occurredAt,
		PayloadVersion: target.payloadVersion, Payload: json.RawMessage(target.payload),
	}
	admitted, err := workflowRecordedCheckpointAdmission(workflowReplayContext(context.Background()), tx, event, p, entry.Definition, currentStep, executionActor)
	if err != nil {
		t.Fatalf("the recorded checkpoint helper must refuse the incomplete %s proof without erroring: %v", scenario, err)
	}
	if admitted {
		t.Fatalf("the recorded checkpoint helper admitted the incomplete %s proof", scenario)
	}
}

// A direct negative of the historical helper: with the executor equality in
// place, the dispatch authorization whose in-flight binding never became a
// worker.dispatched row is the only missing proof, and the helper refuses it.
func TestRecordedWorkflowAdmissionRecordedCheckpointHelperWithoutDispatchRowRefuses(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-helper-authonly", 12, recordedPinImplementationV12), true, false, false)
	run.work.dispatchCompletion(t, run.plan.fencedStep, run.owner, 1, run.attempt1, run.laneVersion, run.laneDigest)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 2, 1)
	recordedAdmissionRequireRecordedCheckpointRefusal(t, run, "no-dispatch-row")
}

// A direct negative of the historical helper: with the executor equality in
// place, the latest dispatch whose failed terminal names no disposal action is
// the only missing proof, and the helper refuses it.
func TestRecordedWorkflowAdmissionRecordedCheckpointHelperMissingDisposalRefuses(t *testing.T) {
	run := recordedAdmissionRunDisposedLane(t, implementationPlan("recorded-admission-ckpt-helper-nodisposal", 12, recordedPinImplementationV12), true, true, false)
	run.work.workerFailed(t, run.attempt1)
	run.startNewAttempt(t, run.plan.startAction, run.plan.fencedStep, 2)
	run.work.checkpoint(t, run.plan.fencedStep, run.executor, 2, 1)
	recordedAdmissionRequireRecordedCheckpointRefusal(t, run, "missing-disposal")
}

// Candidate B: the completion fold's verdict input must stop at the
// completion's own sequence, so a later different evaluator's verdict on the
// intact log cannot own a completion that names the authenticated prior
// evaluator.
func TestRecordedWorkflowAdmissionCompletionVerdictInputStopsAtOwnSequence(t *testing.T) {
	w := recordedAdmissionCompletionScenario(t, "recorded-admission-verdict")
	receipts := recordedAdmissionCompletionReceipts(w.workID)
	recordedAdmissionBindCompletionEvidence(t, w, receipts)
	w.verdict(t, w.actorRef["agent/reviewer-one"], "evidence:"+w.workID+"-verify")
	w.confirmPremise(t, w.actorRef["agent/reviewer-one"])
	w.completeWorkflow(t, w.actorRef["agent/reviewer-one"])
	w.verdict(t, w.actorRef["agent/reviewer-two"], "evidence:"+w.workID+"-verify")

	s := recordedAdmissionRequireFold(t, w, receipts)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "completed" || step != "complete" {
		t.Fatalf("replayed instance = %q/%q, want completed/complete", state, step)
	}
	recordedAdmissionRebuildStable(t, s, w.journal)
}

// recordedAdmissionCompletionScenario walks the static_analysis graph to the
// complete step with the executor and two operator reviewers; the caller
// appends the evidence, verdict, and completion arrangement under test.
func recordedAdmissionCompletionScenario(t *testing.T, workID string) *recordedAdmissionWork {
	t.Helper()
	w := recordedAdmissionStartWork(t, workID)
	executor := w.recordActor(t, "agent", recordedAdmissionExecutorAgent)
	w.recordActor(t, "operator", "agent/reviewer-one")
	w.recordActor(t, "operator", "agent/reviewer-two")
	w.selectDefinition(t, "static_analysis", "workflow.static_analysis", 15, recordedPinStaticAnalysisV15)
	w.approveContract(t, false)
	w.completeAction(t, "approve_contract", "scope", executor, 1, nil)
	w.completeAction(t, "record_delivery", "analyze", executor, 1, nil)
	w.completeAction(t, "record_report", "report", executor, 1, nil)
	w.completeAction(t, "confirm_premise", "review", executor, 1, nil)
	return w
}

func recordedAdmissionBindCompletionEvidence(t *testing.T, w *recordedAdmissionWork, receipts []recordedAdmissionReceipt) {
	t.Helper()
	for index, receipt := range receipts {
		w.evidenceBound(t, receiptKind(index), receipt.refs[0], w.actorRef[recordedAdmissionExecutorAgent], receipt.opID, receipt.producerID, receipt.requestID)
	}
}

func receiptKind(index int) string {
	return []string{"verification", "review", "artifact"}[index]
}

func recordedAdmissionCompletionReceipts(workID string) []recordedAdmissionReceipt {
	return []recordedAdmissionReceipt{
		{opID: "op/" + workID + "-verify", typeRef: "workflow.static_analysis", typeVer: 15, stepID: "complete", stepKind: "internal_sqlite", refs: []string{"evidence:" + workID + "-verify"}, producerID: "principal/synthetic-verification", requestID: "request/" + workID + "-verify"},
		{opID: "op/" + workID + "-review", typeRef: "workflow.static_analysis", typeVer: 15, stepID: "complete", stepKind: "internal_sqlite", refs: []string{"evidence:" + workID + "-review"}, producerID: "principal/synthetic-review", requestID: "request/" + workID + "-review"},
		{opID: "op/" + workID + "-artifact", typeRef: "workflow.static_analysis", typeVer: 15, stepID: "complete", stepKind: "internal_sqlite", refs: []string{"evidence:" + workID + "-artifact"}, producerID: "principal/synthetic-artifact", requestID: "request/" + workID + "-artifact"},
	}
}

// B negative: with no prior verdict at all, a later verdict by the very actor
// the completion names must not satisfy it; the prefix read, not the actor
// comparison alone, owns the input. On the unchanged engine the unbounded
// read admits this journal, so this negative is red until the fix lands.
func TestRecordedWorkflowAdmissionCompletionWithoutPriorVerdictRefusesDespiteMatchingFuture(t *testing.T) {
	w := recordedAdmissionCompletionScenario(t, "recorded-admission-verdict-future")
	receipts := recordedAdmissionCompletionReceipts(w.workID)
	recordedAdmissionBindCompletionEvidence(t, w, receipts)
	w.confirmPremise(t, w.actorRef["agent/reviewer-one"])
	w.completeWorkflow(t, w.actorRef["agent/reviewer-one"])
	w.verdict(t, w.actorRef["agent/reviewer-one"], "evidence:"+w.workID+"-later")
	recordedAdmissionRequireRefusal(t, w, receipts, KindUnauthorized, "completed verdict actor is not the authenticated recorded evaluator")
}

// B negative: a completion naming anyone but the authenticated prior
// evaluator refuses even though a matching prior verdict exists.
func TestRecordedWorkflowAdmissionCompletionForeignPriorEvaluatorRefuses(t *testing.T) {
	w := recordedAdmissionCompletionScenario(t, "recorded-admission-verdict-foreign")
	receipts := recordedAdmissionCompletionReceipts(w.workID)
	recordedAdmissionBindCompletionEvidence(t, w, receipts)
	w.verdict(t, w.actorRef["agent/reviewer-one"], "evidence:"+w.workID+"-verify")
	w.confirmPremise(t, w.actorRef["agent/reviewer-one"])
	w.completeWorkflow(t, w.actorRef["agent/reviewer-two"])
	recordedAdmissionRequireRefusal(t, w, receipts, KindUnauthorized, "completed verdict actor is not the authenticated recorded evaluator")
}

// Candidate C scenario: walk a pinned graph past its binding step, bind an
// earlier verification evidence through an authoritative durable producer,
// then bind the own evidence and complete bind_evidence off-step.
type recordedAdmissionBindingPlan struct {
	workID        string
	family        string
	ref           string
	version       int64
	storedDigest  string
	walk          []recordedAdmissionStep
	deliveries    []string
	finalStep     string
	receiptStep   string
	sourceVersion int
}

type recordedAdmissionBindingOptions struct {
	withContract        bool
	withEarlierBinding  bool
	omitOwnBinding      bool   // skip the own binding event and its receipt entirely
	ownBindingActor     string // recordedAdmissionExecutorAgent (default) or the owner
	ownBindingKind      string // default "verification"
	omitOwnRefInResults bool   // when true the completion names only the earlier binding
	completionVersion   int    // overrides the plan's source version when > 0
}

func recordedAdmissionBuildBindingScenario(t *testing.T, plan recordedAdmissionBindingPlan, options recordedAdmissionBindingOptions) (*recordedAdmissionWork, []recordedAdmissionReceipt) {
	t.Helper()
	w := recordedAdmissionStartWork(t, plan.workID)
	executor := w.recordActor(t, "agent", recordedAdmissionExecutorAgent)
	owner := w.recordActor(t, "agent", recordedAdmissionOwnerAgent)
	w.selectDefinition(t, plan.family, plan.ref, plan.version, plan.storedDigest)
	if options.withContract {
		w.approveContract(t, true)
	}
	for _, step := range plan.walk {
		if step.actionID == "record_alignment" {
			// The alignment action is a typed action: the search rides its
			// own backlog_alignment_recorded event and the completion stays
			// generic, exactly as the live constructor emits the pair.
			w.journal.append("workflow.backlog_alignment_recorded", "work_item", w.workID, executor, 1, map[string]any{
				"expected_version": w.version, "outcome": "none_found",
				"resulting_version": w.version + 1,
				"searched":          "synthetic recorded-admission backlog alignment search", "work_id": w.workID,
			})
			w.version++
		}
		w.completeAction(t, step.actionID, step.stepID, executor, 1, nil)
	}
	for _, step := range plan.deliveries {
		w.completeAction(t, "record_delivery", step, executor, 1, nil)
	}
	ownActor := executor
	if options.ownBindingActor == recordedAdmissionOwnerAgent {
		ownActor = owner
	}
	ownKind := options.ownBindingKind
	if ownKind == "" {
		ownKind = "verification"
	}
	var receipts []recordedAdmissionReceipt
	earlierRef := "evidence:" + plan.workID + "-earlier"
	ownRef := "evidence:" + plan.workID + "-own"
	if options.withEarlierBinding {
		receipt := recordedAdmissionReceipt{
			opID: "op/" + plan.workID + "-earlier", typeRef: plan.ref, typeVer: plan.version, stepID: plan.receiptStep,
			stepKind: "internal_sqlite", refs: []string{earlierRef}, producerID: "principal/synthetic-verification", requestID: "request/" + plan.workID + "-earlier",
		}
		w.evidenceBound(t, "verification", earlierRef, executor, receipt.opID, receipt.producerID, receipt.requestID)
		receipts = append(receipts, receipt)
	}
	if !options.omitOwnBinding {
		ownReceipt := recordedAdmissionReceipt{
			opID: "op/" + plan.workID + "-own", typeRef: plan.ref, typeVer: plan.version, stepID: plan.receiptStep,
			stepKind: "internal_sqlite", refs: []string{ownRef}, producerID: "principal/synthetic-verification", requestID: "request/" + plan.workID + "-own",
		}
		w.evidenceBound(t, ownKind, ownRef, ownActor, ownReceipt.opID, ownReceipt.producerID, ownReceipt.requestID)
		receipts = append(receipts, ownReceipt)
	}
	results := []string{ownRef}
	if options.omitOwnRefInResults {
		results = []string{earlierRef}
	}
	completionVersion := plan.sourceVersion
	if options.completionVersion > 0 {
		completionVersion = options.completionVersion
	}
	w.completeAction(t, "bind_evidence", plan.finalStep, executor, completionVersion, map[string]any{
		"result_evidence_refs": results,
	})
	return w, receipts
}

// recordedAdmissionWalkWithAlignment splices the mandatory alignment step
// into the pinned walk. withAlignmentStep carries it from implementation v13
// and break_fix v11 (the retained v11 shares the v12 alignment graph), and
// record_alignment is the step's only advancing exit, so the synthetic walk
// must record it to cross every edge the pinned graph holds between the
// contract approval and the fenced work.
func recordedAdmissionWalkWithAlignment(walk []recordedAdmissionStep, family string, version int64) []recordedAdmissionStep {
	aligned := (family == "implementation" && version >= 13) || (family == "break_fix" && version >= 11)
	if !aligned {
		return walk
	}
	after := "proposal"
	if family == "break_fix" {
		after = "reproduce"
	}
	spliced := make([]recordedAdmissionStep, 0, len(walk)+1)
	for _, step := range walk {
		spliced = append(spliced, step)
		if step.stepID == after {
			spliced = append(spliced, recordedAdmissionStep{stepID: "alignment", actionID: "record_alignment"})
		}
	}
	return spliced
}

func recordedAdmissionBindingPinPlan(workID, family, ref, digest string, version int64, deliveries []string, finalStep, receiptStep string, sourceVersion int) recordedAdmissionBindingPlan {
	walk := recordedAdmissionImplementationWalk
	if family == "break_fix" {
		walk = recordedAdmissionBreakFixWalk
	}
	walk = recordedAdmissionWalkWithAlignment(walk, family, version)
	return recordedAdmissionBindingPlan{
		workID: workID, family: family, ref: ref, version: version, storedDigest: digest, walk: walk,
		deliveries: deliveries, finalStep: finalStep, receiptStep: receiptStep, sourceVersion: sourceVersion,
	}
}

// Candidate C across every remaining mapped pin/source pair: the recorded
// bind_evidence completion whose own binding's declared verification kind an
// earlier authoritative durable-producer binding already satisfies folds on
// the declared requirement set. The implementation v16 source-2 and break_fix
// v14 source-3 independent seeds carry their own dedicated tests.
func TestRecordedWorkflowAdmissionRecoveryBindingAllRemainingMappedPins(t *testing.T) {
	pins := []struct {
		name string
		plan recordedAdmissionBindingPlan
	}{
		{"implementation-v10-source2", recordedAdmissionBindingPinPlan("recorded-admission-bind-impl10", "implementation", "workflow.implementation", recordedPinImplementationV10, 10, []string{"execution", "refine"}, "acceptance", "execution", 2)},
		{"implementation-v21-source3", recordedAdmissionBindingPinPlan("recorded-admission-bind-impl21", "implementation", "workflow.implementation", recordedPinImplementationV21, 21, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 3)},
		{"break-fix-v11-source2", recordedAdmissionBindingPinPlan("recorded-admission-bind-bfix11", "break_fix", "workflow.break_fix", recordedPinBreakFixV11, 11, []string{"repair", "refine"}, "verify", "repair", 2)},
		{"break-fix-v13-source2", recordedAdmissionBindingPinPlan("recorded-admission-bind-bfix13", "break_fix", "workflow.break_fix", recordedPinBreakFixV13, 13, []string{"repair", "refine", "delivery"}, "verify", "repair", 2)},
		{"break-fix-v15-source2", recordedAdmissionBindingPinPlan("recorded-admission-bind-bfix15", "break_fix", "workflow.break_fix", recordedPinBreakFixV15, 15, []string{"repair", "refine", "delivery"}, "verify", "repair", 2)},
		{"break-fix-v18-source3", recordedAdmissionBindingPinPlan("recorded-admission-bind-bfix18", "break_fix", "workflow.break_fix", recordedPinBreakFixV18, 18, []string{"repair", "refine", "delivery"}, "verify", "repair", 3)},
	}
	for _, pin := range pins {
		pin := pin
		t.Run(pin.name, func(t *testing.T) {
			w, receipts := recordedAdmissionBuildBindingScenario(t, pin.plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
			s := recordedAdmissionRequireFold(t, w, receipts)
			defer s.Close()
			if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "running" || step != pin.plan.finalStep {
				t.Fatalf("replayed instance = %q/%q, want running/%s", state, step, pin.plan.finalStep)
			}
			recordedAdmissionRebuildStable(t, s, w.journal)
		})
	}
}

// Candidate C, source-2 seed pin: the recorded recovery binding on the
// implementation v16 graph folds on its declared requirement.
func TestRecordedWorkflowAdmissionRecoveryBindingDeclaredKindImplementationSource2(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-impl", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
	s := recordedAdmissionRequireFold(t, w, receipts)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "running" || step != "acceptance" {
		t.Fatalf("replayed instance = %q/%q, want running/acceptance", state, step)
	}
	recordedAdmissionRebuildStable(t, s, w.journal)
	recordedAdmissionBackupRoundtrip(t, s, w.journal)
}

// Candidate C, source-3 seed pin: the same declared-versus-outstanding shape
// on the archived break_fix v14 graph at source payload version 3.
func TestRecordedWorkflowAdmissionRecoveryBindingDeclaredKindBreakFixSource3(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-bfix", "break_fix", "workflow.break_fix", recordedPinBreakFixV14, 14, []string{"repair", "refine", "delivery"}, "verify", "repair", 3)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
	s := recordedAdmissionRequireFold(t, w, receipts)
	defer s.Close()
	if state, step, _ := recordedAdmissionInstanceState(t, s, w.workID); state != "running" || step != "verify" {
		t.Fatalf("replayed instance = %q/%q, want running/verify", state, step)
	}
	recordedAdmissionRebuildStable(t, s, w.journal)
}

// C negative: a mapped implementation pin completed at the break-fix source
// payload version is the wrong source pair and keeps refusing.
func TestRecordedWorkflowAdmissionRecoveryBindingWrongSourcePairKeepsRefusal(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-pair", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 3)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
	recordedAdmissionRequireRefusal(t, w, receipts, KindMissingEvidence, "recovery evidence binding does not bind eligible recovery evidence")
}

// C negative: a completion stored at the current source version keeps the
// existing rule; the recorded alternative recognizes only source 2 and 3.
func TestRecordedWorkflowAdmissionRecoveryBindingSource4KeepsRefusal(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-src4", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true, completionVersion: 4})
	recordedAdmissionRequireRefusal(t, w, receipts, KindMissingEvidence, "recovery evidence binding does not bind eligible recovery evidence")
}

// C negative: a registered version outside the mapped population keeps
// refusing the same declared-kind shape.
func TestRecordedWorkflowAdmissionRecoveryBindingUnknownPinKeepsRefusal(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-unknown", "implementation", "workflow.implementation", recordedUnknownPinImplementationV17, 17, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
	recordedAdmissionRequireRefusal(t, w, receipts, KindMissingEvidence, "recovery evidence binding does not bind eligible recovery evidence")
}

// C negative: the stored pin digest must match the registry exactly. The
// prefix rebuild leaves a canonical instance row; the stored digest is then
// corrupted and the recorded completion folded directly, so a refusal can
// come only from the actual stored mismatch and not from an empty
// requirement mask.
func TestRecordedWorkflowAdmissionRecoveryBindingStoredDigestMismatchKeepsRefusal(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-digest", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
	recordedAdmissionRequireStoredDigestRefusal(t, w, receipts, recordedAdmissionWrongDigest, KindMissingEvidence, "recovery evidence binding does not bind eligible recovery evidence")
}

// C negative: a bind_evidence completion with no preceding binding of its own
// refuses at the own-binding join. No workflow.evidence_bound event precedes
// the completion at all — the earlier same-actor binding is absent together
// with the own binding — so the join cannot mistake another binding for its
// own.
func TestRecordedWorkflowAdmissionRecoveryBindingWithoutOwnBindingRefuses(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-absent", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: false, omitOwnBinding: true})
	recordedAdmissionRequireRefusal(t, w, receipts, KindMissingEvidence, "recovery evidence binding requires its own bound subject")
}

// C negative: a preceding binding by another actor does not join as the own
// binding.
func TestRecordedWorkflowAdmissionRecoveryBindingForeignOwnBindingRefuses(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-foreign", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true, ownBindingActor: recordedAdmissionOwnerAgent})
	recordedAdmissionRequireRefusal(t, w, receipts, KindMissingEvidence, "recovery evidence binding requires its own bound subject")
}

// C negative: the completion must name its own binding's immutable subject in
// its results; naming only the earlier binding keeps refusing.
func TestRecordedWorkflowAdmissionRecoveryBindingOwnReferenceOutsideResultsRefuses(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-refs", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true, omitOwnRefInResults: true})
	recordedAdmissionRequireRefusal(t, w, receipts, KindMissingEvidence, "recovery evidence binding does not bind eligible recovery evidence")
}

// C negative: with no approved contract the declared requirement set is
// empty and the completion keeps refusing.
func TestRecordedWorkflowAdmissionRecoveryBindingWithoutContractRefuses(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-nocontract", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: false, withEarlierBinding: true})
	recordedAdmissionRequireRefusal(t, w, receipts, KindMissingEvidence, "recovery evidence binding does not bind eligible recovery evidence")
}

// C negative: a binding whose kind the contract and the pinned definition do
// not declare keeps refusing even though the declared verification kind is
// already satisfied. "approval" is a closed evidence kind the implementation
// family never requires.
func TestRecordedWorkflowAdmissionRecoveryBindingUndeclaredKindRefuses(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-undeclared", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true, ownBindingKind: "approval"})
	recordedAdmissionRequireRefusal(t, w, receipts, KindMissingEvidence, "recovery evidence binding does not bind eligible recovery evidence")
}

// C negative: a live (non-replay) append of the same bind_evidence completion
// keeps the existing refusal through the observable live route; no replay
// metadata is forged and the recorded alternative is replay-only.
func TestRecordedWorkflowAdmissionRecoveryBindingLiveAppendKeepsExistingRefusal(t *testing.T) {
	plan := recordedAdmissionBindingPinPlan("recorded-admission-bind-live", "implementation", "workflow.implementation", recordedPinImplementationV16, 16, []string{"execution", "refine", "delivery"}, "acceptance", "execution", 2)
	w, receipts := recordedAdmissionBuildBindingScenario(t, plan, recordedAdmissionBindingOptions{withContract: true, withEarlierBinding: true})
	// Withhold the completion event: the prefix restores and rebuilds, then
	// the completion is applied live against the rebuilt projections. Both
	// bindings stay in the prefix with their receipts. The expected version is
	// the prefix's own: the withheld completion's version bump sits above it.
	prefix := &recordedAdmissionJournal{workID: w.workID, events: w.journal.events[:len(w.journal.events)-1]}
	s := restoreRecordedAdmissionJournal(t, prefix, receipts)
	defer s.Close()
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("the live-append prefix must fold: %v", err)
	}
	version := w.version - 1
	event := workflowEventWithActor(w.workID+"-live-bind", WorkflowActionCompleted, w.workID, w.actorRef[recordedAdmissionExecutorAgent], map[string]any{
		"action_id": "bind_evidence", "actor_ref": w.actorRef[recordedAdmissionExecutorAgent], "attempt_epoch": int64(1),
		"changed_refs": []string{}, "expected_version": version, "result_evidence_refs": []string{"evidence:" + w.workID + "-own"},
		"resulting_version": version + 1, "step_id": plan.finalStep, "work_id": w.workID,
	})
	event.PayloadVersion = plan.sourceVersion
	err := applyWorkflowTestOperationDirect(context.Background(), s, Operation{
		Events:           []Event{event},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, w.workID): version},
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "recovery evidence binding does not bind eligible recovery evidence") {
		t.Fatalf("live bind_evidence refusal = %v, want the unchanged recovery-eligibility guard", err)
	}
}
