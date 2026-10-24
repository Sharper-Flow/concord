package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// The read fixture deliberately has no projection guards or unrelated schema.
// Each probe must answer from current facts after the log is renamed.
func effectReceiptReadFixture(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,event_id TEXT,kind TEXT,subject_type TEXT,subject_id TEXT,actor TEXT,occurred_at TEXT,payload_version INTEGER,payload TEXT);
CREATE TABLE effect_receipts(event_id TEXT PRIMARY KEY,effect_seq INTEGER UNIQUE,kind TEXT,subject_type TEXT,subject_id TEXT,actor TEXT,payload_version INTEGER,payload_digest TEXT);
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,dispatch_event_id TEXT,dispatch_payload TEXT,terminal_event_id TEXT,terminal_kind TEXT,terminal_payload TEXT);
CREATE TABLE worker_job_revisions(work_id TEXT,recorded_payload TEXT);
CREATE TABLE workflow_staleness_observations(event_id TEXT PRIMARY KEY,work_id TEXT,rule_id TEXT,severity TEXT,drifted INTEGER,accepted_inputs_digest TEXT,observed_at TEXT);
`)
	if err != nil {
		t.Fatal(err)
	}
	return &Store{db: db}
}

func seedEffectReceiptRead(t *testing.T, s *Store, seq int64, event Event) {
	t.Helper()
	canonical, err := canonicalJSON(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	if _, err := s.db.Exec(`INSERT INTO domain_events VALUES(?,?,?,?,?,?,?,?,?)`, seq, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO effect_receipts VALUES(?,?,?,?,?,?,?,?)`, event.EventID, seq, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.PayloadVersion, "sha256:"+hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
}

func hideEffectLog(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
}

func TestEffectReceiptBootstrapProbeLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	e.EventID = "bootstrap:worktree-created"
	seedEffectReceiptRead(t, s, 7, e)
	hideEffectLog(t, s)
	for _, tc := range []struct{ op, want string }{{"bootstrap", "bootstrap:worktree-created:9"}, {"new", "new:worktree-created"}} {
		got, err := bootstrapCreationEventID(t.Context(), s.db, tc.op, 9)
		if err != nil || got != tc.want {
			t.Fatalf("creation identity = %q, %v; want %q", got, err, tc.want)
		}
	}
}

func TestEffectReceiptConflictLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	seedEffectReceiptRead(t, s, 7, e)
	hideEffectLog(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	assertFailureKind(t, classifyEventIDConflict(t.Context(), tx, e, fmt.Errorf("unique collision")), KindDuplicateEvent)
	e.Payload = []byte(`{"display_name":"different"}`)
	assertFailureKind(t, classifyEventIDConflict(t.Context(), tx, e, fmt.Errorf("unique collision")), KindIdempotencyConflict)
}

func TestEffectReceiptPredecessorProbesLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	e.SubjectType, e.SubjectID, e.Actor = SubjectWorkItem, "work", "importer"
	seedEffectReceiptRead(t, s, 7, e)
	e.EventID, e.Actor = "foreign", "other"
	seedEffectReceiptRead(t, s, 8, e)
	hideEffectLog(t, s)
	found, err := s.EventIDExists(t.Context(), "foreign")
	if err != nil || !found {
		t.Fatalf("exists = %v, %v", found, err)
	}
	found, err = s.EventIDExists(t.Context(), "missing")
	if err != nil || found {
		t.Fatalf("missing = %v, %v", found, err)
	}
	kind, actor, found, err := s.FirstWorkEventByOtherActor(t.Context(), "work", "importer")
	if err != nil || !found || kind != e.Kind || actor != "other" {
		t.Fatalf("foreign = %q, %q, %v, %v", kind, actor, found, err)
	}
}

func TestEffectReceiptTerminalSampleLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	seedEffectReceiptRead(t, s, 7, e)
	hideEffectLog(t, s)
	got, err := sampleWorkerTerminalDelivery(t.Context(), s.db, "work", "attempt", e.EventID)
	if err != nil || !got.eventFound {
		t.Fatalf("sample = %+v, %v", got, err)
	}
}

func TestEffectReceiptStalenessProbeLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e, _, err := workflowStalenessObservationEvent("stale", "work", "actor", testManifestDigest, []byte(`{"staleness_rule_id":"rule","observed_drift":{"severity":"warning","drifted":true}}`), time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	seedEffectReceiptRead(t, s, 7, e)
	if _, err := s.db.Exec(`INSERT INTO workflow_staleness_observations VALUES('stale','work','rule','warning',1,?,'1970-01-01T00:00:01Z')`, testManifestDigest); err != nil {
		t.Fatal(err)
	}
	hideEffectLog(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e.Payload = []byte(fmt.Sprintf(`{"rule_id":"rule","severity":"warning","drifted":true,"accepted_inputs_digest":%q,"observed_at":"1970-01-01T00:01:01Z"}`, testManifestDigest))
	found, err := workflowStalenessObservationRecordedTx(t.Context(), tx, e)
	if err != nil || !found {
		t.Fatalf("retry = %v, %v", found, err)
	}
	e.Payload = []byte(fmt.Sprintf(`{"rule_id":"rule","severity":"warning","drifted":false,"accepted_inputs_digest":%q,"observed_at":"1970-01-01T00:01:01Z"}`, testManifestDigest))
	_, err = workflowStalenessObservationRecordedTx(t.Context(), tx, e)
	assertFailureKind(t, err, KindOperationConflict)
}

func TestCorrectionRecordSourceLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	if _, err := s.db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY); INSERT INTO work_items VALUES('work');
CREATE TABLE workflow_correction_state(work_id TEXT PRIMARY KEY,state TEXT);
CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,payload TEXT);`); err != nil {
		t.Fatal(err)
	}
	h := newWorkflowCorrectionState()
	h.records = []workflowCorrectionRecordState{{seq: 7, sourceID: e.EventID, fields: workflowCorrectionCompletionFields{ActionID: "request_correction"}}}
	if err := writeWorkflowCorrectionFacts(t.Context(), s.db, "work", h); err != nil {
		t.Fatal(err)
	}
	hideEffectLog(t, s)
	got, err := workflowCorrectionContextForDispatch(t.Context(), s.db, "work", "", "")
	if err != nil || got == nil || got.SourceEventID != e.EventID || got.SourceEventSeq != 7 {
		t.Fatalf("source = %+v, %v", got, err)
	}
}

func TestEffectReceiptEvaluationPrefixLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	e.Kind, e.SubjectType, e.SubjectID, e.Actor = WorkerDispatched, SubjectWorkItem, "other-work", "coordinator"
	e.Payload = []byte(`{"attempt_id":"attempt","lane_actor_ref":"worker"}`)
	seedEffectReceiptRead(t, s, 7, e)
	if _, err := s.db.Exec(`INSERT INTO worker_attempts(work_id,attempt_id,dispatch_event_id,dispatch_payload) VALUES('other-work','attempt',?,?)`, e.EventID, string(e.Payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE workflow_worker_actors(actor_ref TEXT PRIMARY KEY,first_dispatch_seq INTEGER); INSERT INTO workflow_worker_actors VALUES('worker',7)`); err != nil {
		t.Fatal(err)
	}
	hideEffectLog(t, s)
	for _, tc := range []struct {
		actor  string
		prefix int64
		want   bool
	}{{"worker", 6, false}, {"worker", 7, true}, {"worker", 8, true}, {"coordinator", 8, false}} {
		got, err := workflowRecordedEvaluationFacts(t.Context(), s.db, "work", tc.actor, tc.prefix)
		if err != nil || got != tc.want {
			t.Fatalf("%+v = %v, %v", tc, got, err)
		}
	}
}

func TestEffectReceiptWorkerAcknowledgmentLogBlind(t *testing.T) {
	for _, kind := range []string{WorkerDispatched, WorkerCompleted, WorkerFailed} {
		t.Run(kind, func(t *testing.T) {
			s := effectReceiptReadFixture(t)
			e := testEvent()
			e.Kind, e.SubjectType, e.SubjectID, e.PayloadVersion = kind, SubjectWorkItem, "work", 3
			e.Payload = []byte("{ \"attempt_id\": \"attempt\", \"extension\": {\"unknown\": true} }")
			seedEffectReceiptRead(t, s, 7, e)
			if kind == WorkerDispatched {
				_, err := s.db.Exec(`INSERT INTO worker_attempts(work_id,attempt_id,dispatch_event_id,dispatch_payload) VALUES('work','attempt',?,?)`, e.EventID, string(e.Payload))
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := s.db.Exec(`INSERT INTO worker_attempts(work_id,attempt_id,terminal_event_id,terminal_kind,terminal_payload) VALUES('work','attempt',?,?,?)`, e.EventID, kind, string(e.Payload))
				if err != nil {
					t.Fatal(err)
				}
			}
			hideEffectLog(t, s)
			got, found, err := eventByID(context.Background(), s.db, e.EventID)
			want := e
			want.Seq, want.OccurredAt = 7, time.Time{}
			if err != nil || !found || !reflect.DeepEqual(got, want) {
				t.Fatalf("ack = %+v, %v, %v", got, found, err)
			}
		})
	}
}

func TestEffectReceiptAppendAndBackfillParity(t *testing.T) {
	s, _ := seedCompletionGateCase(t, "receipt-backfill", completionGateCase{requiredEvidence: []string{"verification", "review"}})
	if err := AppendWorkflowStalenessObservation(t.Context(), s, "receipt-stale", "receipt-backfill", "actor:staleness", testManifestDigest, []byte(`{"staleness_rule_id":"rule","observed_drift":{"severity":"warning","drifted":true}}`), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`SELECT seq,event_id,kind,subject_type,subject_id,actor,payload_version,payload FROM domain_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.EventID, &e.Kind, &e.SubjectType, &e.SubjectID, &e.Actor, &e.PayloadVersion, &e.Payload); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	assertParity := func() {
		t.Helper()
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&count); err != nil || count != len(events) {
			t.Fatalf("count = %d, %v; want %d", count, err, len(events))
		}
		for _, event := range events {
			r, found, err := readEffectReceipt(t.Context(), s.db, event.EventID)
			if err != nil || !found || r.event.Seq != event.Seq || len(r.identityDifferences(event)) != 0 {
				t.Fatalf("receipt %s = %+v, %v, %v", event.EventID, r, found, err)
			}
		}
	}
	assertParity()
	if _, err := s.db.Exec(`DROP TABLE workflow_staleness_observations; DROP TABLE effect_receipts; DELETE FROM schema_migrations WHERE version=145`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	assertParity()
	var rule, severity, digest, observed string
	var drifted bool
	if err := s.db.QueryRow(`SELECT rule_id,severity,drifted,accepted_inputs_digest,observed_at FROM workflow_staleness_observations WHERE event_id='receipt-stale'`).Scan(&rule, &severity, &drifted, &digest, &observed); err != nil {
		t.Fatal(err)
	}
	if rule != "rule" || severity != "warning" || !drifted || digest != testManifestDigest || observed != "1970-01-01T00:00:01Z" {
		t.Fatalf("staleness backfill = %q,%q,%v,%q,%q", rule, severity, drifted, digest, observed)
	}
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	assertParity()
}

func TestEffectReceiptBackfillRefusesMissingDigest(t *testing.T) {
	s := effectReceiptReadFixture(t)
	if _, err := s.db.Exec(`INSERT INTO effect_receipts(event_id,effect_seq) VALUES('orphan',1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	assertFailureKind(t, backfillEffectReceiptDigests(t.Context(), tx), KindInvariantViolation)
}

func TestEffectReceiptPayloadMismatchRefusesAcknowledgment(t *testing.T) {
	s := effectReceiptReadFixture(t)
	e := testEvent()
	e.Kind, e.SubjectType, e.SubjectID, e.Payload = WorkerFailed, SubjectWorkItem, "work", []byte(`{"attempt_id":"attempt","detail":"original"}`)
	seedEffectReceiptRead(t, s, 1, e)
	if _, err := s.db.Exec(`INSERT INTO worker_attempts(work_id,terminal_event_id,terminal_kind,terminal_payload) VALUES('work',?,?,'{"attempt_id":"attempt","detail":"changed"}')`, e.EventID, e.Kind); err != nil {
		t.Fatal(err)
	}
	hideEffectLog(t, s)
	_, _, err := eventByID(t.Context(), s.db, e.EventID)
	assertFailureKind(t, err, KindInvariantViolation)
}

func TestEffectReceiptRollsBackWithAppend(t *testing.T) {
	s := openTemp(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AppendEvent(t.Context(), tx, testEvent()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM domain_events)+(SELECT count(*) FROM effect_receipts)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback left %d rows: %v", count, err)
	}
}
