package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// This fixture isolates the SQL contract from migrations and action admission.
// The work FK is intentional; actor identity may be registered after an action.
func coordinatorFactsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
		PRAGMA foreign_keys=ON;
		CREATE TABLE work_items(id TEXT PRIMARY KEY);
		CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,execution_actor_ref TEXT);
		CREATE TABLE workflow_actors(actor_ref TEXT PRIMARY KEY,session_ref TEXT,actor_class TEXT,first_seen_at TEXT);
		CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,actor TEXT,kind TEXT,payload TEXT,occurred_at TEXT);
		CREATE TABLE workflow_coordinator_actions(
			work_id TEXT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
			actor_ref TEXT NOT NULL,last_effect_seq INTEGER NOT NULL,
			last_action_id TEXT NOT NULL,last_acted_at TEXT NOT NULL,
			PRIMARY KEY(work_id,actor_ref));
		INSERT INTO work_items VALUES('work'),('other');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func coordinatorFactsTx(t *testing.T, db *sql.DB) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func coordinatorFactsExec(t *testing.T, tx *sql.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func coordinatorFactsActor(t *testing.T, tx *sql.Tx, actor, class, firstSeen string) {
	t.Helper()
	coordinatorFactsExec(t, tx, `INSERT INTO workflow_actors VALUES(?,?,?,?)`, actor, "session-"+actor, class, firstSeen)
}

func coordinatorFactsEvent(seq int64, work, actor, kind, payload, at string) Event {
	stamp, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		panic(err)
	}
	return Event{Seq: seq, SubjectType: SubjectWorkItem, SubjectID: work, Actor: actor, Kind: kind, Payload: []byte(payload), OccurredAt: stamp}
}

func coordinatorFactsApply(t *testing.T, tx *sql.Tx, event Event) {
	t.Helper()
	coordinatorFactsExec(t, tx, `INSERT INTO domain_events VALUES(?,?,?,?,?,?,?)`, event.Seq, event.SubjectType, event.SubjectID, event.Actor, event.Kind, string(event.Payload), event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err := writeWorkflowCoordinatorActionTx(context.Background(), tx, event); err != nil {
		t.Fatal(err)
	}
}

// The independent log query is the selection oracle, including NULL versus
// empty action ids and lexical RFC3339Nano ordering.
const coordinatorFactsLogSQL = `
	SELECT a.session_ref,
	       COALESCE((SELECT COALESCE(json_extract(e.payload,'$.action_id'),e.kind)
	                    FROM domain_events e
	                   WHERE e.subject_type=? AND e.subject_id=?
	                     AND e.actor=a.actor_ref AND e.kind IN (?,?,?,?)
	                   ORDER BY e.occurred_at DESC,e.seq DESC LIMIT 1),
	                'workflow.actor_recorded'),
	       COALESCE((SELECT e.occurred_at FROM domain_events e
	                   WHERE e.subject_type=? AND e.subject_id=?
	                     AND e.actor=a.actor_ref AND e.kind IN (?,?,?,?)
	                   ORDER BY e.occurred_at DESC,e.seq DESC LIMIT 1),a.first_seen_at) AS last_acted_at
	FROM workflow_actors a
	WHERE a.actor_class=? AND (
	  a.actor_ref=(SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?)
	  OR a.actor_ref IN (SELECT e.actor FROM domain_events e
	    WHERE e.subject_type=? AND e.subject_id=? AND e.kind IN (?,?,?,?)))
	ORDER BY last_acted_at DESC,a.session_ref LIMIT 17`

func coordinatorFactsLogArgs(work string) []any {
	return []any{string(SubjectWorkItem), work, WorkflowActionStarted, WorkflowActionCompleted, WorkflowActionCheckpointed, WorkflowActionFailed,
		string(SubjectWorkItem), work, WorkflowActionStarted, WorkflowActionCompleted, WorkflowActionCheckpointed, WorkflowActionFailed,
		string(ActorAgent), work, string(SubjectWorkItem), work, WorkflowActionStarted, WorkflowActionCompleted, WorkflowActionCheckpointed, WorkflowActionFailed}
}

func coordinatorFactsLogRead(t *testing.T, tx *sql.Tx, work string) ([]WorkPinDrivingSession, error) {
	t.Helper()
	rows, err := tx.QueryContext(context.Background(), coordinatorFactsLogSQL, coordinatorFactsLogArgs(work)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	return scanWorkPinDrivingSessions(rows)
}

func TestWorkPinDrivingSessionsFactsLogBlind(t *testing.T) {
	tx := coordinatorFactsTx(t, coordinatorFactsDB(t))
	const earlier = "2026-01-01T00:00:00Z"
	const later = "2026-01-02T00:00:00Z"
	for _, actor := range []string{"initial", "alpha", "beta", "empty", "null", "missing", "other-only", "unrelated"} {
		coordinatorFactsActor(t, tx, actor, "agent", earlier)
	}
	coordinatorFactsActor(t, tx, "human", "operator", earlier)
	coordinatorFactsExec(t, tx, `INSERT INTO workflow_instances VALUES('work','initial')`)
	events := []Event{
		coordinatorFactsEvent(1, "work", "alpha", WorkflowActionStarted, `{"action_id":"start"}`, earlier),
		coordinatorFactsEvent(2, "work", "alpha", WorkflowActionCompleted, `{"action_id":"complete"}`, later),
		coordinatorFactsEvent(3, "work", "alpha", WorkflowActionCheckpointed, `{"action_id":"checkpoint"}`, later),
		coordinatorFactsEvent(4, "work", "alpha", WorkflowActionFailed, `{"action_id":"regressing"}`, earlier),
		coordinatorFactsEvent(5, "work", "beta", WorkflowActionFailed, `{"action_id":"failed"}`, later),
		coordinatorFactsEvent(6, "work", "empty", WorkflowActionCompleted, `{"action_id":""}`, earlier),
		coordinatorFactsEvent(7, "work", "null", WorkflowActionCheckpointed, `{"action_id":null}`, earlier),
		coordinatorFactsEvent(8, "work", "missing", WorkflowActionFailed, `{}`, earlier),
		coordinatorFactsEvent(9, "other", "other-only", WorkflowActionStarted, `{"action_id":"other"}`, later),
		coordinatorFactsEvent(10, "other", "alpha", WorkflowActionStarted, `{"action_id":"other-alpha"}`, "2026-01-03T00:00:00Z"),
		coordinatorFactsEvent(11, "work", "human", WorkflowActionStarted, `{"action_id":"human"}`, later),
		coordinatorFactsEvent(12, "work", "unrelated", WorkflowActorRecorded, `{}`, later),
		coordinatorFactsEvent(13, "work", "late-identity", WorkflowActionCompleted, `{}`, earlier),
		coordinatorFactsEvent(14, "work", "unknown-identity", WorkflowActionStarted, `{}`, later),
	}
	wrongSubject := coordinatorFactsEvent(15, "work", "unrelated", WorkflowActionStarted, `{}`, later)
	wrongSubject.SubjectType = SubjectProject
	events = append(events, wrongSubject)
	for _, event := range events {
		coordinatorFactsApply(t, tx, event)
	}
	coordinatorFactsActor(t, tx, "late-identity", "agent", later)
	want := []WorkPinDrivingSession{
		{SessionRef: "session-alpha", LastActionID: "checkpoint", LastActedAt: later},
		{SessionRef: "session-beta", LastActionID: "failed", LastActedAt: later},
		{SessionRef: "session-empty", LastActionID: "", LastActedAt: earlier},
		{SessionRef: "session-initial", LastActionID: WorkflowActorRecorded, LastActedAt: earlier},
		{SessionRef: "session-late-identity", LastActionID: WorkflowActionCompleted, LastActedAt: earlier},
		{SessionRef: "session-missing", LastActionID: WorkflowActionFailed, LastActedAt: earlier},
		{SessionRef: "session-null", LastActionID: WorkflowActionCheckpointed, LastActedAt: earlier},
	}
	old, err := coordinatorFactsLogRead(t, tx, "work")
	if err != nil || !reflect.DeepEqual(old, want) {
		t.Fatalf("log result = %+v, %v; want %+v", old, err, want)
	}
	before, err := workPinDrivingSessionsTx(context.Background(), tx, "work")
	if err != nil || !reflect.DeepEqual(before, want) {
		t.Fatalf("before rename = %+v, %v; want %+v", before, err, want)
	}
	coordinatorFactsExec(t, tx, `ALTER TABLE domain_events RENAME TO unavailable_log`)
	after, err := workPinDrivingSessionsTx(context.Background(), tx, "work")
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("after rename = %+v, %v; before %+v", after, err, before)
	}
}

func TestWorkPinDrivingSessionsFactsBound(t *testing.T) {
	for _, count := range []int{0, 16, 17, 19} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			tx := coordinatorFactsTx(t, coordinatorFactsDB(t))
			for i := range count {
				actor := fmt.Sprintf("actor-%02d", i)
				coordinatorFactsActor(t, tx, actor, "agent", "2026-01-01T00:00:00Z")
				coordinatorFactsApply(t, tx, coordinatorFactsEvent(int64(i+1), "work", actor, WorkflowActionStarted, `{}`, "2026-01-02T00:00:00Z"))
			}
			old, oldErr := coordinatorFactsLogRead(t, tx, "work")
			rows, err := tx.QueryContext(context.Background(), workPinDrivingSessionsSQL, workPinDrivingSessionsArgs("work")...)
			if err != nil {
				t.Fatal(err)
			}
			var rawCount int
			for rows.Next() {
				rawCount++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = rows.Close()
			if rawCount != min(count, 17) {
				t.Fatalf("raw count = %d, want %d", rawCount, min(count, 17))
			}
			coordinatorFactsExec(t, tx, `ALTER TABLE domain_events RENAME TO unavailable_log`)
			got, err := workPinDrivingSessionsTx(context.Background(), tx, "work")
			if count > 16 {
				for _, failure := range []error{oldErr, err} {
					var typed *Failure
					if !errors.As(failure, &typed) || typed.Kind != KindLimitExceeded {
						t.Fatalf("error = %v, want limit_exceeded", failure)
					}
				}
			} else if oldErr != nil || err != nil || !reflect.DeepEqual(got, old) || len(got) != count {
				t.Fatalf("facts = %+v, %v; log = %+v, %v", got, err, old, oldErr)
			}
		})
	}
}

func TestWorkflowCoordinatorActionFactsOrdering(t *testing.T) {
	tx := coordinatorFactsTx(t, coordinatorFactsDB(t))
	const earlier = "2026-01-01T00:00:00Z"
	const later = "2026-01-02T00:00:00Z"
	events := []Event{
		coordinatorFactsEvent(1, "work", "actor", WorkflowActionStarted, `{"action_id":"start"}`, earlier),
		coordinatorFactsEvent(2, "work", "actor", WorkflowActionCompleted, `{"action_id":"complete"}`, later),
		coordinatorFactsEvent(3, "work", "actor", WorkflowActionFailed, `{"action_id":"older"}`, earlier),
		coordinatorFactsEvent(5, "work", "actor", WorkflowActionCheckpointed, `{"action_id":""}`, later),
		coordinatorFactsEvent(4, "work", "actor", WorkflowActionStarted, `{"action_id":"lower-tie"}`, later),
		coordinatorFactsEvent(6, "work", "actor", WorkflowActorRecorded, `{"action_id":"ignored"}`, "2026-01-03T00:00:00Z"),
		// Canonical whole seconds sort above fractional text in the same second.
		coordinatorFactsEvent(7, "work", "actor", WorkflowActionFailed, `{}`, "2026-01-02T00:00:00.1Z"),
		coordinatorFactsEvent(8, "work", "actor", WorkflowActionCompleted, `{"action_id":null}`, "2026-01-02T01:00:00+01:00"),
	}
	wantSeq := []int64{1, 2, 2, 5, 5, 5, 5, 8}
	coordinatorFactsActor(t, tx, "actor", "agent", earlier)
	for i, event := range events {
		coordinatorFactsApply(t, tx, event)
		var seq int64
		if err := tx.QueryRow(`SELECT last_effect_seq FROM workflow_coordinator_actions WHERE work_id='work' AND actor_ref='actor'`).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		if seq != wantSeq[i] {
			t.Fatalf("after seq %d, retained %d; want %d", event.Seq, seq, wantSeq[i])
		}
		var action, stamp string
		if err := tx.QueryRow(`SELECT last_action_id,last_acted_at FROM workflow_coordinator_actions WHERE work_id='work' AND actor_ref='actor'`).Scan(&action, &stamp); err != nil {
			t.Fatal(err)
		}
		old, err := coordinatorFactsLogRead(t, tx, "work")
		if err != nil || len(old) != 1 || old[0].LastActionID != action || old[0].LastActedAt != stamp {
			t.Fatalf("after seq %d, fact = %q/%q; log = %+v, %v", event.Seq, action, stamp, old, err)
		}
	}
	coordinatorFactsExec(t, tx, `DELETE FROM work_items WHERE id='work'`)
	var remaining int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM workflow_coordinator_actions`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("after work deletion: %d rows, %v", remaining, err)
	}
}
