package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

// CD-0208 D1: an exact acknowledgment compares the caller's request against
// the stored event's ORIGINAL payload version and complete payload. These
// fixtures seed synthetic historical worker events at the raw payload
// versions earlier binaries recorded — worker.dispatched v3 (pre
// lane-actor enrichment), worker.dispatched v4 (enriched), and
// worker.completed v3 (pre worker-job claim) — through the store's lawful
// retained current rows, then acknowledge them by their original identifier
// with fresh signed assertions. The stored row must keep its raw version,
// raw payload bytes, actor, and single-event count, and a request that
// inserts content the original never carried must refuse.

// legacyWorkerEvidenceSeededTime is the fixed occurrence time of every
// seeded historical event. Occurrence time is not payload identity
// (CD-0208 D1), so the acknowledgment's fresh clock reading never compares
// against it.
var legacyWorkerEvidenceSeededTime = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func legacyWorkerDispatchEvent(t *testing.T, eventID string, lane store.LaneDefinition, readback string, rawVersion int) store.Event {
	t.Helper()
	payload := store.WorkerDispatchedPayload{
		AttemptID:           "attempt-1",
		LaneID:              lane.ID,
		LaneDigest:          lane.Digest,
		CapabilityClass:     lane.CapabilityClass,
		PacketSchemaVersion: store.WorkerPacketSchemaVersionLegacy,
		ReportSchemaVersion: store.WorkerReportSchemaVersionLegacy,
		HostProvenance: &store.WorkerHostProvenance{
			Digest:  "sha256:" + strings.Repeat("a", 64),
			Sources: []store.WorkerHostProvenanceSource{{Kind: "agent_definition", Path: ".opencode/agents/" + lane.ID + ".md", SHA256: "sha256:" + strings.Repeat("b", 64)}},
		},
		ReadbackModel: readback,
		PacketDigest:  "sha256:" + strings.Repeat("c", 64),
	}
	return store.Event{
		EventID: eventID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1",
		Actor: "client:" + workerEvidenceClientRef + ":operator-1", OccurredAt: legacyWorkerEvidenceSeededTime,
		PayloadVersion: rawVersion, Payload: []byte(mustJSON(t, payload)),
	}
}

// seedLegacyWorkerDispatchEvent retains the original payload version, released
// schema pair, bytes and actor that exact acknowledgment must compare.
func seedLegacyWorkerDispatchEvent(t *testing.T, dbPath, eventID string, lane store.LaneDefinition, readback string, rawVersion int) {
	t.Helper()
	seedLegacyWorkerEventRows(t, dbPath, legacyWorkerDispatchEvent(t, eventID, lane, readback, rawVersion))
}

// seedLegacyWorkerCompletionEvent records one worker.completed event at the
// raw v3 payload version, beside a legacy v3 dispatch of the same attempt.
func seedLegacyWorkerCompletionEvent(t *testing.T, dbPath, dispatchEventID, completionEventID string, lane store.LaneDefinition, readback string) {
	t.Helper()
	evidence := make([]store.WorkerReportEvidence, 0, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		evidence = append(evidence, store.WorkerReportEvidence{Obligation: obligation, Detail: "discharged " + obligation})
	}
	completion := store.Event{
		EventID: completionEventID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: "work-1",
		Actor: "client:" + workerEvidenceClientRef + ":operator-1", OccurredAt: legacyWorkerEvidenceSeededTime,
		PayloadVersion: 3,
		Payload: []byte(mustJSON(t, store.WorkerCompletedPayload{
			AttemptID:           "attempt-1",
			ReadbackModel:       readback,
			ReportSchemaVersion: store.WorkerReportSchemaVersionLegacy,
			EvidenceOrigin:      store.WorkerEvidenceReported,
			Evidence:            evidence,
		})),
	}
	seedLegacyWorkerEventRows(t, dbPath, legacyWorkerDispatchEvent(t, dispatchEventID, lane, readback, 3), completion)
}

// seedLegacyWorkerEventRows retains original worker payloads beside their
// immutable receipt identities. eventByID joins these exact current rows;
// no live dispatch validation or stored-payload conversion is exercised here.
func seedLegacyWorkerEventRows(t *testing.T, dbPath string, events ...store.Event) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	db := s.DatabaseForTesting()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		seq := seedCLIReceipt(t, tx, event)
		if event.Kind == store.WorkerDispatched {
			var payload store.WorkerDispatchedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO worker_attempts (work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at) VALUES (?,?,?,?,?,?,?,?,'dispatched','','',?,NULL,NULL)`,
				event.SubjectID, payload.AttemptID, payload.LaneID, payload.LaneDigest, payload.CapabilityClass, payload.ReadbackModel, payload.PacketSchemaVersion, payload.ReportSchemaVersion, event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
				t.Fatalf("seed legacy attempt row %s: %v", payload.AttemptID, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET dispatch_event_id=?,dispatch_actor=?,dispatch_payload_version=?,dispatch_payload=?,dispatch_seq=? WHERE work_id=? AND attempt_id=?`, event.EventID, event.Actor, event.PayloadVersion, string(event.Payload), seq, event.SubjectID, payload.AttemptID); err != nil {
				t.Fatal(err)
			}
		} else {
			var payload struct {
				AttemptID string `json:"attempt_id"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE worker_attempts SET terminal_event_id=?,terminal_kind=?,terminal_payload_version=?,terminal_payload=?,terminal_seq=?,terminal_occurred_at=? WHERE work_id=? AND attempt_id=?`, event.EventID, event.Kind, event.PayloadVersion, string(event.Payload), seq, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID, payload.AttemptID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// storedWorkerEventRow reads the durable identity an acknowledgment must
// leave untouched: the raw payload version, the raw payload bytes, and the
// actor.
func storedWorkerEventRow(t *testing.T, dbPath, eventID string) (version int, payload []byte, actor string) {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var digest string
	if err := db.QueryRow(`SELECT r.payload_version,CASE r.kind WHEN 'worker.dispatched' THEN a.dispatch_payload ELSE a.terminal_payload END,r.actor,r.payload_digest
FROM effect_receipts r JOIN worker_attempts a ON a.work_id=r.subject_id AND
((r.kind='worker.dispatched' AND a.dispatch_event_id=r.event_id AND a.dispatch_seq=r.effect_seq AND a.dispatch_payload_version=r.payload_version AND a.dispatch_actor=r.actor) OR
(r.kind IN ('worker.completed','worker.failed') AND a.terminal_event_id=r.event_id AND a.terminal_kind=r.kind AND a.terminal_seq=r.effect_seq AND a.terminal_payload_version=r.payload_version))
WHERE r.event_id=?`, eventID).Scan(&version, &payload, &actor, &digest); err != nil {
		t.Fatalf("read stored event %s: %v", eventID, err)
	}
	if want := cliPayloadDigest(t, payload); digest != want {
		t.Fatalf("stored original payload %q does not match receipt: got %q want %q", eventID, digest, want)
	}
	return version, payload, actor
}

func countWorkerEvents(t *testing.T, dbPath, kind string) int {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id='work-1' AND kind=?`, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestWorkerEvidenceExactReconciliationAcknowledgesLegacyPayloadVersions is
// the CD-0208 D1 regression for historically recorded events: after the
// registry bump (worker.dispatched v5, worker.completed v4), an exact
// acknowledgment of a stored dispatch v3 or v4, or a stored completion v3,
// must still succeed against the ORIGINAL payload version and complete
// payload, twice, with the stored row unchanged, and must refuse a request
// that inserts payload or job metadata the original never carried.
func TestWorkerEvidenceExactReconciliationAcknowledgesLegacyPayloadVersions(t *testing.T) {
	dispatchAck := func(t *testing.T, rawVersion int) {
		dbPath := freshMigratedCLIDatabase(t)
		key := seedWorkerEvidenceClient(t)
		lane := store.BuiltinLaneDefinitions()[0]
		readback := preferredLaneModel(lane)
		seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
		eventID := fmt.Sprintf("event-legacy-dispatch-v%d", rawVersion)
		seedLegacyWorkerDispatchEvent(t, dbPath, eventID, lane, readback, rawVersion)
		rawVersionBefore, rawPayloadBefore, rawActorBefore := storedWorkerEventRow(t, dbPath, eventID)
		request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbDispatch, lane, readback, "nonce-legacy-dispatch-first01")
		request["event_id"] = eventID
		request["packet_schema_version"] = store.WorkerPacketSchemaVersionLegacy
		request["report_schema_version"] = store.WorkerReportSchemaVersionLegacy
		var out, diagnostic bytes.Buffer
		for i := range 2 {
			assertion.Nonce = fmt.Sprintf("nonce-legacy-dispatch-v%d-%03d", rawVersion, i)
			request["assertion"] = signWorkerEvidence(t, key, assertion)
			out.Reset()
			diagnostic.Reset()
			if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
				t.Fatalf("exact acknowledgment of stored dispatch v%d: exit=%d stderr=%s", rawVersion, code, diagnostic.String())
			}
		}
		rawVersionAfter, rawPayloadAfter, rawActorAfter := storedWorkerEventRow(t, dbPath, eventID)
		if rawVersionAfter != rawVersionBefore || rawActorAfter != rawActorBefore || string(rawPayloadAfter) != string(rawPayloadBefore) {
			t.Fatalf("acknowledgment changed the stored dispatch v%d: version %d→%d actor %q→%q payload changed=%v", rawVersion, rawVersionBefore, rawVersionAfter, rawActorBefore, rawActorAfter, string(rawPayloadAfter) != string(rawPayloadBefore))
		}
		if count := countWorkerEvents(t, dbPath, store.WorkerDispatched); count != 1 {
			t.Fatalf("acknowledgment appended worker.dispatched events: count=%d", count)
		}
		// A request that inserts a worker-job binding the original event
		// never carried is a mismatch, not an acknowledgment (CD-0208 D1).
		request["worker_job"] = map[string]any{"job_id": "job-never-recorded", "revision": 1, "digest": "sha256:" + strings.Repeat("1", 64)}
		assertion.Nonce = fmt.Sprintf("nonce-legacy-dispatch-v%d-job01", rawVersion)
		request["assertion"] = signWorkerEvidence(t, key, assertion)
		out.Reset()
		diagnostic.Reset()
		if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code == 0 || !strings.Contains(diagnostic.String(), "identity conflicts") {
			t.Fatalf("inserted worker_job on stored dispatch v%d admitted: exit=%d stderr=%s", rawVersion, code, diagnostic.String())
		}
	}
	for _, rawVersion := range []int{3, 4} {
		t.Run(fmt.Sprintf("dispatch v%d", rawVersion), func(t *testing.T) { dispatchAck(t, rawVersion) })
	}
	t.Run("completion v3", func(t *testing.T) {
		dbPath := freshMigratedCLIDatabase(t)
		key := seedWorkerEvidenceClient(t)
		lane := store.BuiltinLaneDefinitions()[0]
		readback := preferredLaneModel(lane)
		seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
		completionEventID := "event-legacy-complete-v3"
		seedLegacyWorkerCompletionEvent(t, dbPath, "event-legacy-dispatch-for-complete", completionEventID, lane, readback)
		rawVersionBefore, rawPayloadBefore, rawActorBefore := storedWorkerEventRow(t, dbPath, completionEventID)
		request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbComplete, lane, readback, "nonce-legacy-complete-first01")
		request["event_id"] = completionEventID
		request["report_schema_version"] = store.WorkerReportSchemaVersionLegacy
		delete(request, "worker_directory")
		var out, diagnostic bytes.Buffer
		for i := range 2 {
			assertion.Nonce = fmt.Sprintf("nonce-legacy-complete-v3-%03d", i)
			request["assertion"] = signWorkerEvidence(t, key, assertion)
			out.Reset()
			diagnostic.Reset()
			if code := runWithInput([]string{"worker-complete"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code != 0 {
				t.Fatalf("exact acknowledgment of stored completion v3: exit=%d stderr=%s", code, diagnostic.String())
			}
		}
		rawVersionAfter, rawPayloadAfter, rawActorAfter := storedWorkerEventRow(t, dbPath, completionEventID)
		if rawVersionAfter != rawVersionBefore || rawActorAfter != rawActorBefore || string(rawPayloadAfter) != string(rawPayloadBefore) {
			t.Fatalf("acknowledgment changed the stored completion v3: version %d→%d actor %q→%q payload changed=%v", rawVersionBefore, rawVersionAfter, rawActorBefore, rawActorAfter, string(rawPayloadAfter) != string(rawPayloadBefore))
		}
		if count := countWorkerEvents(t, dbPath, store.WorkerCompleted); count != 1 {
			t.Fatalf("acknowledgment appended worker.completed events: count=%d", count)
		}
		// Changed payload content is a mismatch, not an acknowledgment.
		request["evidence"] = []map[string]any{{"obligation": lane.EvidenceObligations[0], "detail": "changed result"}}
		assertion.Nonce = "nonce-legacy-complete-v3-changed01"
		request["assertion"] = signWorkerEvidence(t, key, assertion)
		out.Reset()
		diagnostic.Reset()
		if code := runWithInput([]string{"worker-complete"}, strings.NewReader(mustJSON(t, request)), &out, &diagnostic); code == 0 || !strings.Contains(diagnostic.String(), "identity conflicts") {
			t.Fatalf("changed payload on stored completion v3 admitted: exit=%d stderr=%s", code, diagnostic.String())
		}
	})
}
