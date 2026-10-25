package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The read fixture deliberately has no projection guards or unrelated schema.
// Each probe must answer from current facts with no log or replacement view.
func effectReceiptReadFixture(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
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
	if _, err := s.db.Exec(`INSERT INTO effect_receipts VALUES(?,?,?,?,?,?,?,?)`, event.EventID, seq, event.Kind, event.SubjectType, event.SubjectID, event.Actor, event.PayloadVersion, "sha256:"+hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
}

func hideEffectLog(t *testing.T, s *Store) {
	t.Helper()
	for _, schema := range []string{"main", "temp"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM ` + schema + `.sqlite_schema WHERE name IN ('domain_events','hidden_domain_events','domain_events_advance_effect_sequence','domain_events_insert')`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s retained a log object: count=%d, err=%v", schema, count, err)
		}
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
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "receipts-144.db"), 144)
	if _, err := db.Exec(`INSERT INTO fold_guard VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work','task','Receipts','needed',0,1,'now','now');
DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	stale := fmt.Sprintf(`{"rule_id":"rule","severity":"warning","drifted":true,"accepted_inputs_digest":%q,"observed_at":"1970-01-01T00:00:01Z"}`, testManifestDigest)
	corpus := []histEventRow{
		{1, "receipt-product", "product.created", "product", "product", "creator", "1970-01-01T00:00:01Z", 1, `{ "display_name": "Product", "extension": {"z":2,"a":1}, "number":9007199254740993 }`},
		{2, "receipt-stale", "workflow.staleness_observed", "work_item", "work", "staleness", "1970-01-01T00:00:02Z", 1, stale},
		{3, "receipt-block", "workflow.staleness_observed", "work_item", "work", "staleness", "1970-01-01T00:00:03Z", 2, strings.ReplaceAll(strings.ReplaceAll(stale, "warning", "block"), "true", "false")},
		{4, "receipt-wrong-subject", "workflow.staleness_observed", "project", "work", "other", "1970-01-01T00:00:04Z", 1, stale},
		{5, "receipt-removed", "workflow.staleness_observed", "work_item", "removed", "other", "1970-01-01T00:00:05Z", 1, stale},
		{6, "receipt-wrong-kind", "work.observed", "work_item", "work", "observer", "1970-01-01T00:00:06Z", 3, stale},
	}
	for _, row := range corpus {
		if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`, row.Seq, row.EventID, row.Kind, row.SubjectType, row.SubjectID, row.Actor, row.OccurredAt, row.Version, row.Payload); err != nil {
			t.Fatal(err)
		}
	}
	// The negative run changes one selection in a local value, never the
	// immutable migration or its on-disk manifest/checksum.
	if os.Getenv("TEST_CONCORD_MIGRATION_SELECTION_RED") == "145" {
		m := migrations[144]
		m.SQL = strings.Replace(m.SQL, "AND subject_type='work_item'", "AND subject_type='project'", 1)
		if err := applyMigration(t.Context(), db, m); err != nil {
			t.Fatal(err)
		}
	} else {
		advanceHistoricalPrefix(t, db, 145)
	}
	assertParity := func() {
		t.Helper()
		assertHistoricalReceipts(t, db, corpus)
		var receipts, observations int
		if err := db.QueryRow(`SELECT (SELECT count(*) FROM effect_receipts),(SELECT count(*) FROM workflow_staleness_observations)`).Scan(&receipts, &observations); err != nil || receipts != len(corpus) || observations != 2 {
			t.Fatalf("145 selection: receipts=%d observations=%d, err=%v; want %d/2", receipts, observations, err, len(corpus))
		}
		for i, row := range corpus[1:3] {
			var work, rule, severity, digest, observed string
			var drifted bool
			wantSeverity := []string{"warning", "block"}[i]
			if err := db.QueryRow(`SELECT work_id,rule_id,severity,drifted,accepted_inputs_digest,observed_at FROM workflow_staleness_observations WHERE event_id=?`, row.EventID).Scan(&work, &rule, &severity, &drifted, &digest, &observed); err != nil || work != "work" || rule != "rule" || digest != testManifestDigest || observed != "1970-01-01T00:00:01Z" || severity != wantSeverity || drifted != (i == 0) {
				t.Fatalf("145 staleness %s = %s/%s/%s/%t/%s/%s, %v", row.EventID, work, rule, severity, drifted, digest, observed, err)
			}
		}
	}
	assertParity()
	if got := histEventRows(t, &Store{db: db}); !reflect.DeepEqual(got, corpus) {
		t.Fatalf("145 changed source envelopes: %+v", got)
	}
	for range 2 {
		if err := Migrate(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		assertParity()
		hideEffectLog(t, &Store{db: db})
	}
	// Compare the retained headers/digests with the live receipt writer on a
	// fresh current schema. The corpus is explicit data, not a log reconstruction.
	fresh := openTemp(t)
	tx, err := fresh.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, row := range corpus {
		e := Event{EventID: row.EventID, Kind: row.Kind, SubjectType: SubjectType(row.SubjectType), SubjectID: row.SubjectID, Actor: row.Actor, PayloadVersion: row.Version, Payload: []byte(row.Payload)}
		seq, err := appendPreparedEvent(t.Context(), tx, e)
		if err != nil || seq != row.Seq {
			t.Fatalf("live receipt %s = %d, %v", row.EventID, seq, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertHistoricalReceipts(t, fresh.db, corpus)
	hideEffectLog(t, fresh)
	seedWork(t, fresh, "work")
	if err := AppendWorkflowStalenessObservation(t.Context(), fresh, "live-stale", "work", "staleness", testManifestDigest, []byte(`{"staleness_rule_id":"rule","observed_drift":{"severity":"warning","drifted":true}}`), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	for _, database := range []*sql.DB{db, fresh.db} {
		id := "receipt-stale"
		if database == fresh.db {
			id = "live-stale"
		}
		var work, rule, severity, digest, observed string
		var drifted bool
		if err := database.QueryRow(`SELECT work_id,rule_id,severity,drifted,accepted_inputs_digest,observed_at FROM workflow_staleness_observations WHERE event_id=?`, id).Scan(&work, &rule, &severity, &drifted, &digest, &observed); err != nil || work != "work" || rule != "rule" || severity != "warning" || !drifted || digest != testManifestDigest || observed != "1970-01-01T00:00:01Z" {
			t.Fatalf("live/backfilled staleness %s = %s/%s/%s/%t/%s/%s, %v", id, work, rule, severity, drifted, digest, observed, err)
		}
	}
}

func TestEffectReceiptBackfillRefusesMissingDigest(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "missing-digest-145.db"), 145)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version) VALUES('orphan',1,'product.created','product','product','operator',1)`); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, backfillEffectReceiptDigests(t.Context(), tx), KindInvariantViolation)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed digest backfill escaped rollback: %d, %v", count, err)
	}
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
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback left %d rows: %v", count, err)
	}
	hideEffectLog(t, s)
}
