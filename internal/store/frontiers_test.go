package store

import (
	"database/sql"
	"fmt"
	"reflect"
	"testing"
)

const reverseFrontiersMigration = `
INSERT OR IGNORE INTO fold_guard(active) VALUES(1);
DROP TRIGGER domain_events_advance_effect_sequence;
DROP TABLE work_version_actions;
DROP TABLE store_sequences;
DROP INDEX IF EXISTS effect_receipts_relation_seq;
ALTER TABLE effect_receipts DROP COLUMN relation_seq;
ALTER TABLE work_items DROP COLUMN version_actions_floor;
ALTER TABLE work_size_assessments DROP COLUMN effect_seq;
ALTER TABLE work_size_assessments DROP COLUMN review_basis;
DELETE FROM schema_migrations WHERE version=146;
DELETE FROM fold_guard;`

const frontierActor = "actor:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// The append seam still writes the log until cutover. This view permits those
// writes but makes a log read fail, including an otherwise empty tail scan.
func frontierWriteOnlyLog(t *testing.T, s *Store) {
	t.Helper()
	hideEffectLog(t, s)
	_, err := s.db.Exec(`CREATE VIEW domain_events AS SELECT * FROM hidden_domain_events WHERE json_extract('unreadable','$');
CREATE TRIGGER domain_events_insert INSTEAD OF INSERT ON domain_events BEGIN
 INSERT INTO hidden_domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload)
 VALUES(NEW.seq,NEW.event_id,NEW.kind,NEW.subject_type,NEW.subject_id,NEW.actor,NEW.occurred_at,NEW.payload_version,NEW.payload);
END;`)
	if err != nil {
		t.Fatal(err)
	}
}

// Existing correction-window readers still need their historical prefix.
// Newly appended effects are invisible, so operation output cannot read them
// back from the log, including effects derived inside a fold.
func frontierHideNewEffects(t *testing.T, s *Store) {
	t.Helper()
	var prefix int64
	if err := s.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&prefix); err != nil {
		t.Fatal(err)
	}
	hideEffectLog(t, s)
	_, err := s.db.Exec(fmt.Sprintf(`CREATE VIEW domain_events AS SELECT * FROM hidden_domain_events WHERE seq<=%d;
CREATE TRIGGER domain_events_insert INSTEAD OF INSERT ON domain_events BEGIN
 INSERT INTO hidden_domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload)
 VALUES(NEW.seq,NEW.event_id,NEW.kind,NEW.subject_type,NEW.subject_id,NEW.actor,NEW.occurred_at,NEW.payload_version,NEW.payload);
END;`, prefix))
	if err != nil {
		t.Fatal(err)
	}
}

func TestFrontierOperationLogBlind(t *testing.T) {
	for _, transactional := range []bool{false, true} {
		t.Run(fmt.Sprint(transactional), func(t *testing.T) {
			s := openTemp(t)
			seedWork(t, s, "work")
			frontierWriteOnlyLog(t, s)
			e := testEvent()
			e.EventID, e.SubjectID, e.Kind = "operation-own", "product", "product.renamed"
			e.Payload = []byte(`{"display_name":"Renamed"}`)
			var result ApplyOperationResult
			var err error
			if transactional {
				tx, beginErr := s.db.BeginTx(t.Context(), nil)
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				defer tx.Rollback()
				result, err = applyOperationTx(t.Context(), tx, Operation{Events: []Event{e}}, newFoldScope(tx), false)
			} else {
				result, err = ApplyOperationWithResult(t.Context(), s, Operation{Events: []Event{e}})
			}
			if err != nil || !reflect.DeepEqual(result.EventIDs, []string{e.EventID}) {
				t.Fatalf("own receipts = %v, %v", result.EventIDs, err)
			}
		})
	}
}

func TestFrontierRelationReopenGapLogBlind(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		seedWork(t, s, id)
	}
	for _, e := range []Event{
		workTransitionEvent("start", "b", "needed", "in_progress", 2, 3),
		workSupersededEvent("supersede", "a", "b", 3, 4),
		workReopenedFromSupersededEvent("reopen", "b", "", 4, 5),
		relationAddedEvent("after-reopen", "implements", "c", "d", 2, 3),
	} {
		if err := applyWorkEvent(t, s, e, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := relationProjectionIDs(t, s); got != "[3]" {
		t.Fatalf("relation IDs = %s, want [3]", got)
	}
	hideEffectLog(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := relationIdentity(t.Context(), tx, Event{EventID: "after-reopen"})
	if err != nil || id != 3 {
		t.Fatalf("relation identity = %d, %v; want 3", id, err)
	}
}

func TestFrontierActionsLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	_, err := s.db.Exec(`CREATE TABLE work_items(id TEXT PRIMARY KEY, version INTEGER, version_actions_floor INTEGER);
CREATE TABLE workflow_actors(actor_ref TEXT,session_ref TEXT);
CREATE TABLE work_version_actions(work_id TEXT,resulting_version INTEGER,action_id TEXT,session_ref TEXT,effect_seq INTEGER);
INSERT INTO work_items VALUES('work',10,0);
INSERT INTO workflow_actors VALUES('actor','session');
INSERT INTO work_version_actions VALUES('work',3,'second','session',20),('work',2,'first','session',10),('work',4,'first','session',30);
INSERT INTO domain_events VALUES(10,'one','workflow.action_started','work_item','work','actor','now',1,'{"action_id":"first","resulting_version":2}'),
(20,'two','workflow.action_completed','work_item','work','actor','now',1,'{"action_id":"second","resulting_version":3}'),
(30,'three','workflow.action_completed','work_item','work','actor','now',1,'{"action_id":"first","resulting_version":4}');`)
	if err != nil {
		t.Fatal(err)
	}
	hideEffectLog(t, s)
	got, err := interveningWorkflowActions(t.Context(), s.db, SubjectWorkItem, "work", 1, 4)
	want := []InterveningAction{{ActionID: "first", SessionRef: "session"}, {ActionID: "second", SessionRef: "session"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %v, %v; want %v", got, err, want)
	}
}

func TestFrontierSizeFactsLogBlind(t *testing.T) {
	s := effectReceiptReadFixture(t)
	_, err := s.db.Exec(`CREATE TABLE work_size_assessments(event_id TEXT,work_id TEXT,effect_seq INTEGER,review_basis TEXT);
INSERT INTO work_size_assessments VALUES('size-one','work',8,'["proof:a","proof:b"]'),('size-two','work',12,'["proof:b"]');
INSERT INTO domain_events VALUES(8,'size-one','work.sized','work_item','work','actor','now',1,'{"review_basis":["proof:a","proof:b"]}'),
(12,'size-two','work.sized','work_item','work','actor','now',1,'{"review_basis":["proof:b"]}');`)
	if err != nil {
		t.Fatal(err)
	}
	hideEffectLog(t, s)
	t.Run("frontier", func(t *testing.T) {
		seq, err := workSizeAssessmentFrontierTx(t.Context(), s.db, "work")
		if err != nil || seq != 12 {
			t.Fatalf("size frontier = %d, %v; want 12", seq, err)
		}
	})
	t.Run("review-basis", func(t *testing.T) {
		basis, err := workSizeRecordedBasisTx(t.Context(), s.db, "work")
		if err != nil || !reflect.DeepEqual(basis, []string{"proof:a", "proof:b"}) {
			t.Fatalf("basis = %v, %v", basis, err)
		}
	})
}

func TestFrontierRawLogInsertAdvancesEffectSequence(t *testing.T) {
	s := openTemp(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES
 (40,'raw-high','product.renamed','product','product','operator','now',1,'{}'),
 (10,'raw-lower','product.renamed','product','product','operator','now',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	var last int64
	if err := tx.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&last); err != nil || last != 40 {
		t.Fatalf("raw inserts left effect sequence at %d, %v; want 40", last, err)
	}
	if seq, err := AppendEvent(t.Context(), tx, testEvent()); err != nil || seq != 41 {
		t.Fatalf("append after raw inserts = %d, %v; want 41", seq, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&last); err != nil || last != 0 {
		t.Fatalf("rollback retained sequence %d, %v; want 0", last, err)
	}
	if _, err := s.db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload)
 VALUES('raw-auto','product.renamed','product','product','operator','now',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&last); err != nil || last != 1 {
		t.Fatalf("automatic raw insert left sequence at %d, %v; want 1", last, err)
	}
}

func TestFrontierSequenceRollbackAndDuplicate(t *testing.T) {
	s := openTemp(t)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if seq, err := AppendEvent(t.Context(), tx, testEvent()); err != nil || seq != 1 {
		t.Fatalf("first seq = %d, %v", seq, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(new(int64)); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seq, err := AppendEvent(t.Context(), tx, testEvent())
	if err != nil || seq != 1 {
		t.Fatalf("rolled-back seq was consumed: %d, %v", seq, err)
	}
	_, err = AppendEvent(t.Context(), tx, testEvent())
	assertFailureKind(t, err, KindDuplicateEvent)
	var last int64
	if err := tx.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&last); err != nil || last != 1 {
		t.Fatalf("duplicate advanced sequence: %d, %v", last, err)
	}
}

func TestFrontierActionsRetentionAndBound(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "work")
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard VALUES(1);
INSERT INTO workflow_actors VALUES(?,'principal','client','agent','session','agent','now')`, frontierActor); err != nil {
		t.Fatal(err)
	}
	for version := int64(2); version <= 300; version++ {
		if _, err := tx.Exec(`UPDATE work_items SET version=? WHERE id='work'`, version); err != nil {
			t.Fatal(err)
		}
		if err := recordWorkVersionActionTx(t.Context(), tx, Event{SubjectID: "work", Actor: frontierActor, Seq: version * 2}, fmt.Sprintf("action:%d", version), version); err != nil {
			t.Fatal(err)
		}
	}
	var count, floor, minimum int64
	if err := tx.QueryRow(`SELECT count(*),min(resulting_version),(SELECT version_actions_floor FROM work_items WHERE id='work') FROM work_version_actions`).Scan(&count, &minimum, &floor); err != nil || count != 256 || minimum != 45 || floor != 44 {
		t.Fatalf("retention = %d,%d,%d; %v", count, minimum, floor, err)
	}
	_, err = interveningWorkflowActions(t.Context(), tx, SubjectWorkItem, "work", 43, 300)
	assertFailureKind(t, err, KindLimitExceeded)
	got, err := interveningWorkflowActions(t.Context(), tx, SubjectWorkItem, "work", 44, 64)
	if err != nil || len(got) != 20 || got[0].ActionID != "action:45" || got[19].ActionID != "action:64" {
		t.Fatalf("20 actions = %v, %v", got, err)
	}
	_, err = interveningWorkflowActions(t.Context(), tx, SubjectWorkItem, "work", 44, 65)
	assertFailureKind(t, err, KindLimitExceeded)
	if err := recordWorkVersionActionTx(t.Context(), tx, Event{SubjectID: "work", Actor: "unrecorded", Seq: 1000}, "not-recorded", 301); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM work_version_actions WHERE action_id='not-recorded'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unrecorded actor contributed: %d, %v", count, err)
	}
	if _, err := tx.Exec(`DELETE FROM work_projects WHERE work_id='work'; DELETE FROM work_items WHERE id='work'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM work_version_actions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed work retained actions: %d, %v", count, err)
	}
}

func TestFrontierBackfillParity(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"a", "b"} {
		seedWork(t, s, id)
	}
	for _, event := range []Event{workSupersededEvent("supersede", "a", "b", 2, 3), workReopenedFromSupersededEvent("reopen", "b", "", 3, 4)} {
		if err := applyWorkEvent(t, s, event, nil); err != nil {
			t.Fatal(err)
		}
	}
	sized := workSizedEvent("sized", "a", 1, WorkSizeSmall, workSizeInputsFixture())
	if err := applyWorkEvent(t, s, sized, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(reverseFrontiersMigration); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO fold_guard VALUES(1);
UPDATE work_items SET version=300 WHERE id='a';
INSERT INTO workflow_actors VALUES(?,'principal','client','agent','session','agent','now');
INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES
 ('old-action','workflow.action_started','work_item','a',?,'now',1,'{"action_id":"old","resulting_version":44}'),
 ('new-action','workflow.action_completed','work_item','a',?,'now',1,'{"action_id":"new","resulting_version":45}'),
 ('unrecorded','workflow.action_completed','work_item','a','missing','now',1,'{"action_id":"missing","resulting_version":46}'),
 ('basis-size','work.sized','work_item','a','operator','now',2,'{"review_basis":["proof:one"]}');
INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size_class,inputs_json,footprint_json,recorded_at)
 VALUES('basis-size','a',2,2,'larger','{}','{}','now');
UPDATE sqlite_sequence SET seq=1000 WHERE name='domain_events';
DELETE FROM fold_guard;`, frontierActor, frontierActor, frontierActor); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	var effect, relation int64
	if err := s.db.QueryRow(`SELECT (SELECT last FROM store_sequences WHERE name='effect'),(SELECT last FROM store_sequences WHERE name='relation')`).Scan(&effect, &relation); err != nil || effect != 1000 || relation != 2 {
		t.Fatalf("sequence backfill = %d,%d; %v", effect, relation, err)
	}
	for index, id := range []string{"supersede", "reopen"} {
		var seq int64
		if err := s.db.QueryRow(`SELECT relation_seq FROM effect_receipts WHERE event_id=?`, id).Scan(&seq); err != nil || seq != int64(index+1) {
			t.Fatalf("relation position %s = %d, %v", id, seq, err)
		}
	}
	got, err := interveningWorkflowActions(t.Context(), s.db, SubjectWorkItem, "a", 44, 300)
	if err != nil || !reflect.DeepEqual(got, []InterveningAction{{ActionID: "new", SessionRef: "session"}}) {
		t.Fatalf("action backfill = %v, %v", got, err)
	}
	var seq, expectedSeq int64
	var basis string
	if err := s.db.QueryRow(`SELECT s.effect_seq,e.seq,s.review_basis FROM work_size_assessments s JOIN domain_events e ON e.event_id=s.event_id WHERE s.event_id='basis-size'`).Scan(&seq, &expectedSeq, &basis); err != nil || seq != expectedSeq || basis != `["proof:one"]` {
		t.Fatalf("size backfill = %d,%d,%s; %v", seq, expectedSeq, basis, err)
	}
	hideEffectLog(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	position, err := relationIdentity(t.Context(), tx, Event{EventID: "reopen"})
	if err != nil || position != 2 {
		t.Fatalf("backfilled log-blind position = %d, %v", position, err)
	}
	if _, err := relationIdentity(t.Context(), tx, Event{EventID: "sized"}); err == nil {
		t.Fatal("non-counted effect fabricated a relation position")
	} else {
		assertFailureKind(t, err, KindInvariantViolation)
	}
	if _, err := relationIdentity(t.Context(), tx, Event{EventID: "missing"}); err == nil {
		t.Fatal("missing effect fabricated a relation position")
	} else {
		assertFailureKind(t, err, KindInvariantViolation)
	}
	var nullPosition sql.NullInt64
	if err := tx.QueryRow(`SELECT relation_seq FROM effect_receipts WHERE event_id='sized'`).Scan(&nullPosition); err != nil || nullPosition.Valid {
		t.Fatalf("non-counted backfill = %v, %v", nullPosition, err)
	}
}
