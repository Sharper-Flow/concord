package main

import (
	"bytes"
	"context"
	"database/sql"
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
// append routes, then acknowledge them by their original event identifier
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
		LaneVersion:         lane.Version,
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

// seedLegacyWorkerDispatchEvent appends one worker.dispatched event at the
// raw payload version a binary of that era recorded. The v4 shape carries
// the lane-actor enrichment that era's boundary derived: the pair is built
// by the same store helper the live boundary uses, then recorded at the raw
// version, so the fold projects it while the stored row keeps its
// historical identity. The v3 shape predates the enrichment and appends
// through the generic operation route.
func seedLegacyWorkerDispatchEvent(t *testing.T, dbPath, eventID string, lane store.LaneDefinition, readback string, rawVersion int) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	event := legacyWorkerDispatchEvent(t, eventID, lane, readback, rawVersion)
	err = s.Transact(ctx, func(tx *store.Transaction) error {
		if rawVersion < 4 {
			_, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{event}})
			return err
		}
		prepared, err := store.PrepareLaneActorDispatch(ctx, tx, event, "principal:operator-1", "client:"+workerEvidenceClientRef)
		if err != nil {
			return err
		}
		// The enriched dispatch records at the raw version the v4-era
		// binary stamped; the prepended actor event records unchanged.
		prepared[len(prepared)-1].PayloadVersion = rawVersion
		_, err = store.AppendLaneActorDispatchTx(ctx, tx, prepared)
		return err
	})
	if err != nil {
		t.Fatalf("seed legacy worker.dispatched v%d: %v", rawVersion, err)
	}
}

// seedLegacyWorkerCompletionEvent appends one worker.completed event at the
// raw v3 payload version, behind a legacy v3 dispatch of the same attempt so
// the completion folds against the schema identity that era recorded.
func seedLegacyWorkerCompletionEvent(t *testing.T, dbPath, dispatchEventID, completionEventID string, lane store.LaneDefinition, readback string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
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
	err = s.Transact(ctx, func(tx *store.Transaction) error {
		if _, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{legacyWorkerDispatchEvent(t, dispatchEventID, lane, readback, 3)}}); err != nil {
			return err
		}
		_, err := store.ApplyOperationTx(ctx, tx, store.Operation{Events: []store.Event{completion}})
		return err
	})
	if err != nil {
		t.Fatalf("seed legacy worker.completed v3: %v", err)
	}
}

// storedWorkerEventRow reads the durable identity an acknowledgment must
// leave untouched: the raw payload version, the raw payload bytes, and the
// actor.
func storedWorkerEventRow(t *testing.T, dbPath, eventID string) (version int, payload []byte, actor string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT payload_version,payload,actor FROM domain_events WHERE event_id=?`, eventID).Scan(&version, &payload, &actor); err != nil {
		t.Fatalf("read stored event %s: %v", eventID, err)
	}
	return version, payload, actor
}

func countWorkerEvents(t *testing.T, dbPath, kind string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id='work-1' AND kind=?`, kind).Scan(&count); err != nil {
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
