package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const frontierActor = "actor:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// Current appends and readers must work with the retired log genuinely absent.
func frontierWriteOnlyLog(t *testing.T, s *Store) {
	t.Helper()
	hideEffectLog(t, s)
}

// Derived effects have no readable log prefix or tail either.
func frontierHideNewEffects(t *testing.T, s *Store) {
	t.Helper()
	hideEffectLog(t, s)
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
				result, err = applyOperationTx(t.Context(), tx, Operation{Events: []Event{e}}, newWriteScope(tx), false)
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
INSERT INTO work_version_actions VALUES('work',3,'second','session',20),('work',2,'first','session',10),('work',4,'first','session',30);`)
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
INSERT INTO work_size_assessments VALUES('size-one','work',8,'["proof:a","proof:b"]'),('size-two','work',12,'["proof:b"]');`)
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

// CD-0242 D2 retires raw-log writes from the current schema, not migration
// 146's historical trigger or the live sequence allocation guarantee.
func TestFrontierHistoricalRawLogInsertAdvancesEffectSequence(t *testing.T) {
	s := &Store{db: openMigratedTo(t, filepath.Join(t.TempDir(), "raw-log-146.db"), 146)}
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
	if seq, err := allocateStoreSequence(t.Context(), tx, "effect"); err != nil || seq != 41 {
		t.Fatalf("allocation after historical raw inserts = %d, %v; want 41", seq, err)
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
	hideEffectLog(t, s)
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
	var rolledBack int64
	if err := s.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("rollback sequence = %d, %v; want 0", rolledBack, err)
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
	for want := int64(2); want <= 40; want++ {
		if seq, err := allocateStoreSequence(t.Context(), tx, "effect"); err != nil || seq != want {
			t.Fatalf("live allocation = %d, %v; want %d", seq, err, want)
		}
	}
	e := testEvent()
	e.EventID = "after-live-high-water"
	if seq, err := AppendEvent(t.Context(), tx, e); err != nil || seq != 41 {
		t.Fatalf("append after live allocation = %d, %v; want 41", seq, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&last); err != nil || last != 41 {
		t.Fatalf("committed high water = %d, %v; want 41", last, err)
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
	if _, err := tx.Exec(`INSERT INTO write_guard VALUES(1);
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
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "frontiers-144.db"), 144)
	if _, err := db.Exec(`INSERT INTO fold_guard VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES
 ('a','task','Frontier A','needed',0,300,'now','now'),('b','task','Frontier B','needed',0,10,'now','now');
INSERT INTO workflow_actors VALUES(?,'principal','client','agent','session','agent','now');
DELETE FROM fold_guard`, frontierActor); err != nil {
		t.Fatal(err)
	}
	// Every relation-selection arm, each action join/filter boundary, and
	// each size basis shape has its own source row and explicit expectation.
	type source struct{ id, kind, subject, work, actor, payload string }
	inputs := []source{
		{"relation", "relation.added", "work_item", "a", "operator", `{}`},
		{"supersede", "work.superseded", "work_item", "a", "operator", `{}`},
		{"reopen", "work.reopened_from_superseded", "work_item", "b", "operator", `{}`},
		{"overlap", "workflow.overlap_resolved", "work_item", "a", "operator", `{}`},
		{"successor", "workflow.successor_linked", "work_item", "a", "operator", `{}`},
		{"raised", "work.created", "work_item", "a", "operator", `{"raised_from_work_id":"b"}`},
		{"unraised", "work.created", "work_item", "a", "operator", `{}`},
		{"empty-raised", "work.created", "work_item", "a", "operator", `{"raised_from_work_id":""}`},
		{"null-raised", "work.created", "work_item", "a", "operator", `{"raised_from_work_id":null}`},
		{"other-kind-raised", "work.observed", "work_item", "a", "operator", `{"raised_from_work_id":"b"}`},
		{"below-floor", "workflow.action_started", "work_item", "a", frontierActor, `{"action_id":"below","resulting_version":43}`},
		{"at-floor", "workflow.action_started", "work_item", "a", frontierActor, `{"action_id":"floor","resulting_version":44}`},
		{"new-action", "workflow.action_completed", "work_item", "a", frontierActor, `{"step_id":"execution","action_id":"new","resulting_version":45}`},
		{"started-action", "workflow.action_started", "work_item", "a", frontierActor, `{"action_id":"started","resulting_version":46}`},
		{"unrecorded", "workflow.action_completed", "work_item", "a", "missing", `{"step_id":"execution","action_id":"missing","resulting_version":47}`},
		{"removed-action", "workflow.action_completed", "work_item", "removed", frontierActor, `{"step_id":"execution","action_id":"removed","resulting_version":48}`},
		{"wrong-subject", "workflow.action_completed", "project", "a", frontierActor, `{"step_id":"execution","action_id":"subject","resulting_version":49}`},
		{"wrong-kind", "work.observed", "work_item", "a", frontierActor, `{"action_id":"kind","resulting_version":50}`},
		{"empty-action", "workflow.action_started", "work_item", "a", frontierActor, `{"action_id":"","resulting_version":51}`},
		{"missing-action", "workflow.action_started", "work_item", "a", frontierActor, `{"resulting_version":52}`},
		{"missing-version", "workflow.action_completed", "work_item", "a", frontierActor, `{"step_id":"execution","action_id":"no-version"}`},
		{"other-work", "workflow.action_completed", "work_item", "b", frontierActor, `{"step_id":"execution","action_id":"other","resulting_version":2}`},
		{"size-default", "work.sized", "work_item", "a", "operator", `{}`},
		{"size-empty", "work.sized", "work_item", "a", "operator", `{"review_basis":[]}`},
		{"basis-size", "work.sized", "work_item", "a", "operator", `{"review_basis":["proof:one","proof:two"]}`},
		{"other-size", "work.sized", "work_item", "b", "operator", `{"review_basis":["proof:other"]}`},
		{"null-action", "workflow.action_started", "work_item", "a", frontierActor, `{"action_id":null,"resulting_version":53}`},
		{"unassessed-size", "work.sized", "work_item", "a", "operator", `{"review_basis":["proof:unassessed"]}`},
	}
	var corpus []histEventRow
	for i, input := range inputs {
		row := histEventRow{Seq: int64(i + 1), EventID: input.id, Kind: input.kind, SubjectType: input.subject, SubjectID: input.work, Actor: input.actor, OccurredAt: "1970-01-01T00:00:01Z", Version: 1, Payload: input.payload}
		corpus = append(corpus, row)
		if _, err := db.Exec(`INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?,?)`, row.Seq, row.EventID, row.Kind, row.SubjectType, row.SubjectID, row.Actor, row.OccurredAt, row.Version, row.Payload); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO fold_guard VALUES(1);
INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size_class,inputs_json,footprint_json,recorded_at) VALUES
 ('size-default','a',1,2,'small','{}','{}','now'),('size-empty','a',2,2,'small','{}','{}','now'),
 ('basis-size','a',3,2,'larger','{}','{}','now'),('other-size','b',1,2,'small','{}','{}','now');
UPDATE sqlite_sequence SET seq=1000 WHERE name='domain_events';
DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	advanceHistoricalPrefix(t, db, 145)
	if os.Getenv("TEST_CONCORD_MIGRATION_SELECTION_RED") == "146" {
		m := migrations[145]
		m.SQL = strings.Replace(m.SQL, "'relation.added','work.reopened_from_superseded'", "'relation.omitted','work.reopened_from_superseded'", 1)
		if err := applyMigration(t.Context(), db, m); err != nil {
			t.Fatal(err)
		}
	} else {
		advanceHistoricalPrefix(t, db, 146)
	}
	assertParity := func() {
		t.Helper()
		assertHistoricalReceipts(t, db, corpus)
		var effect, relation, floorA, floorB, actions int64
		if err := db.QueryRow(`SELECT (SELECT last FROM store_sequences WHERE name='effect'),(SELECT last FROM store_sequences WHERE name='relation'),(SELECT version_actions_floor FROM work_items WHERE id='a'),(SELECT version_actions_floor FROM work_items WHERE id='b'),(SELECT count(*) FROM work_version_actions)`).Scan(&effect, &relation, &floorA, &floorB, &actions); err != nil || effect != 1000 || relation != 6 || floorA != 44 || floorB != 0 || actions != 3 {
			t.Fatalf("146 selection: effect/relation/floors/actions=%d/%d/%d/%d/%d, %v; want 1000/6/44/0/3", effect, relation, floorA, floorB, actions, err)
		}
		for i, row := range corpus {
			var got sql.NullInt64
			if err := db.QueryRow(`SELECT relation_seq FROM effect_receipts WHERE event_id=?`, row.EventID).Scan(&got); err != nil || got.Valid != (i < 6) || (got.Valid && got.Int64 != int64(i+1)) {
				t.Fatalf("146 relation %s = %v, %v", row.EventID, got, err)
			}
		}
		for _, want := range []struct {
			id, action, work string
			version, seq     int64
		}{{"new-action", "new", "a", 45, 13}, {"started-action", "started", "a", 46, 14}, {"other-work", "other", "b", 2, 22}} {
			var session string
			var seq int64
			if err := db.QueryRow(`SELECT session_ref,effect_seq FROM work_version_actions WHERE work_id=? AND resulting_version=? AND action_id=?`, want.work, want.version, want.action).Scan(&session, &seq); err != nil || session != "session" || seq != want.seq {
				t.Fatalf("146 action %s = %s/%d, %v", want.id, session, seq, err)
			}
		}
		for _, want := range []struct {
			id, basis string
			seq       int64
		}{{"size-default", "[]", 23}, {"size-empty", "[]", 24}, {"basis-size", `["proof:one","proof:two"]`, 25}, {"other-size", `["proof:other"]`, 26}} {
			var seq int64
			var basis string
			if err := db.QueryRow(`SELECT effect_seq,review_basis FROM work_size_assessments WHERE event_id=?`, want.id).Scan(&seq, &basis); err != nil || seq != want.seq || basis != want.basis {
				t.Fatalf("146 size %s = %d/%s, %v", want.id, seq, basis, err)
			}
		}
		got, err := interveningWorkflowActions(t.Context(), db, SubjectWorkItem, "a", 44, 300)
		if err != nil || !reflect.DeepEqual(got, []InterveningAction{{ActionID: "new", SessionRef: "session"}, {ActionID: "started", SessionRef: "session"}}) {
			t.Fatalf("action reader = %v, %v", got, err)
		}
		_, err = interveningWorkflowActions(t.Context(), db, SubjectWorkItem, "a", 43, 300)
		assertFailureKind(t, err, KindLimitExceeded)
		if seq, err := workSizeAssessmentFrontierTx(t.Context(), db, "a"); err != nil || seq != 25 {
			t.Fatalf("size reader frontier = %d, %v", seq, err)
		}
		if basis, err := workSizeRecordedBasisTx(t.Context(), db, "a"); err != nil || !reflect.DeepEqual(basis, []string{"proof:one", "proof:two"}) {
			t.Fatalf("size reader basis = %v, %v", basis, err)
		}
	}
	assertParity()
	if got := histEventRows(t, &Store{db: db}); !reflect.DeepEqual(got, corpus) {
		t.Fatalf("146 changed source envelopes: %+v", got)
	}
	for range 2 {
		if err := Migrate(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		assertParity()
		hideEffectLog(t, &Store{db: db})
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if position, err := relationIdentity(t.Context(), tx, Event{EventID: "reopen"}); err != nil || position != 3 {
		t.Fatalf("backfilled relation reader = %d, %v", position, err)
	}
	for _, id := range []string{"basis-size", "missing"} {
		_, err := relationIdentity(t.Context(), tx, Event{EventID: id})
		assertFailureKind(t, err, KindInvariantViolation)
	}
	if seq, err := AppendEvent(t.Context(), tx, testEvent()); err != nil || seq != 1001 {
		t.Fatalf("append after historical high water = %d, %v", seq, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertParity()
	// The same explicit corpus also exercises live relation allocation. It
	// cannot derive expected positions from the implementation under test.
	fresh := openTemp(t)
	for _, id := range []string{"a", "b"} {
		seedWork(t, fresh, id)
	}
	if _, err := fresh.db.Exec(`INSERT INTO write_guard VALUES(1);
UPDATE work_items SET version=CASE id WHEN 'a' THEN 300 ELSE 10 END WHERE id IN ('a','b');
INSERT INTO workflow_actors VALUES(?,'principal','client','agent','session','agent','now');
DELETE FROM write_guard`, frontierActor); err != nil {
		t.Fatal(err)
	}
	var livePrefix int64
	if err := fresh.db.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&livePrefix); err != nil {
		t.Fatal(err)
	}
	tx, err = fresh.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	for i, row := range corpus {
		e := Event{EventID: row.EventID, Kind: row.Kind, SubjectType: SubjectType(row.SubjectType), SubjectID: row.SubjectID, Actor: row.Actor, PayloadVersion: row.Version, Payload: []byte(row.Payload)}
		if seq, err := appendPreparedEvent(t.Context(), tx, e); err != nil || seq != livePrefix+row.Seq {
			t.Fatalf("live effect %s = %d, %v", row.EventID, seq, err)
		}
		var got sql.NullInt64
		if err := tx.QueryRow(`SELECT relation_seq FROM effect_receipts WHERE event_id=?`, row.EventID).Scan(&got); err != nil || got.Valid != (i < 6) || (got.Valid && got.Int64 != int64(i+1)) {
			t.Fatalf("live relation %s = %v, %v", row.EventID, got, err)
		}
	}
	// Exercise the live action writer, including pruned and unrecorded actors,
	// then compare the exact surviving rows with the historical migration.
	for _, action := range []struct {
		id, work, actor, name string
		version, seq          int64
	}{
		{"below-floor", "a", frontierActor, "below", 43, 11},
		{"at-floor", "a", frontierActor, "floor", 44, 12},
		{"new-action", "a", frontierActor, "new", 45, 13},
		{"started-action", "a", frontierActor, "started", 46, 14},
		{"unrecorded", "a", "missing", "missing", 47, 15},
		{"empty-action", "a", frontierActor, "", 51, 19},
		{"other-work", "b", frontierActor, "other", 2, 22},
	} {
		if err := recordWorkVersionActionTx(t.Context(), tx, Event{EventID: action.id, SubjectID: action.work, Actor: action.actor, Seq: livePrefix + action.seq}, action.name, action.version); err != nil {
			t.Fatal(err)
		}
	}
	if err := leaveWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Parent fixture effects precede these corpus effects on the fresh store.
	// Compare the action records rather than unrelated parent receipts.
	liveCorpus := append([]histEventRow(nil), corpus...)
	for i := range liveCorpus {
		liveCorpus[i].Seq += livePrefix
	}
	assertHistoricalReceipts(t, fresh.db, liveCorpus)
	var liveActions int
	if err := fresh.db.QueryRow(`SELECT count(*) FROM work_version_actions`).Scan(&liveActions); err != nil || liveActions != 3 {
		t.Fatalf("live action count = %d, %v; want 3", liveActions, err)
	}
	for _, want := range []struct {
		work, action string
		version, seq int64
	}{{"a", "new", 45, 13}, {"a", "started", 46, 14}, {"b", "other", 2, 22}} {
		var session string
		var seq int64
		if err := fresh.db.QueryRow(`SELECT session_ref,effect_seq FROM work_version_actions WHERE work_id=? AND resulting_version=? AND action_id=?`, want.work, want.version, want.action).Scan(&session, &seq); err != nil || session != "session" || seq != livePrefix+want.seq {
			t.Fatalf("live action %s/%s = %s/%d, %v", want.work, want.action, session, seq, err)
		}
	}
	for _, work := range []string{"a", "b"} {
		for _, database := range []*sql.DB{db, fresh.db} {
			got, err := interveningWorkflowActions(t.Context(), database, SubjectWorkItem, work, map[string]int64{"a": 44, "b": 0}[work], 300)
			want := []InterveningAction{{ActionID: "other", SessionRef: "session"}}
			if work == "a" {
				want = []InterveningAction{{ActionID: "new", SessionRef: "session"}, {ActionID: "started", SessionRef: "session"}}
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("live/backfill actions for %s = %v, %v; want %v", work, got, err, want)
			}
		}
	}
	sized := workSizedEvent("live-sized", "b", WorkSizeModelVersion, WorkSizeSmall, workSizeInputsFixture())
	if err := applyWorkEvent(t, fresh, sized, nil); err != nil {
		t.Fatal(err)
	}
	var sizeReceiptSeq int64
	if err := fresh.db.QueryRow(`SELECT effect_seq FROM effect_receipts WHERE event_id='live-sized'`).Scan(&sizeReceiptSeq); err != nil {
		t.Fatal(err)
	}
	if seq, err := workSizeAssessmentFrontierTx(t.Context(), fresh.db, "b"); err != nil || seq != sizeReceiptSeq {
		t.Fatalf("live size frontier = %d, %v; want receipt %d", seq, err, sizeReceiptSeq)
	}
	if basis, err := workSizeRecordedBasisTx(t.Context(), fresh.db, "b"); err != nil || len(basis) != 0 {
		t.Fatalf("live default basis = %v, %v; want the historical empty-basis result", basis, err)
	}
	hideEffectLog(t, fresh)
}
