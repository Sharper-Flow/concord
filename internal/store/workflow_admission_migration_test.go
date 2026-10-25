package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type admissionMigrationExitKey struct{ work, family, step, action string }
type admissionMigrationActorKey struct{ work, actor string }
type admissionMigrationDisplay struct {
	seq        int64
	action, at string
}
type admissionMigrationFacts struct {
	exits  map[admissionMigrationExitKey]int64
	actors map[admissionMigrationActorKey]admissionMigrationDisplay
}

func admissionMigrationCurrentFacts(t *testing.T, s *Store) admissionMigrationFacts {
	t.Helper()
	got := admissionMigrationFacts{map[admissionMigrationExitKey]int64{}, map[admissionMigrationActorKey]admissionMigrationDisplay{}}
	rows, err := s.db.Query(`SELECT work_id,definition_ref,step_id,action_id,exit_seq FROM workflow_completed_exits`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k admissionMigrationExitKey
		var seq int64
		if err := rows.Scan(&k.work, &k.family, &k.step, &k.action, &seq); err != nil {
			t.Fatal(err)
		}
		got.exits[k] = seq
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	rows, err = s.db.Query(`SELECT work_id,actor_ref,last_effect_seq,last_action_id,last_acted_at FROM workflow_coordinator_actions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k admissionMigrationActorKey
		var v admissionMigrationDisplay
		if err := rows.Scan(&k.work, &k.actor, &v.seq, &v.action, &v.at); err != nil {
			t.Fatal(err)
		}
		got.actors[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func admissionMigrationRequireFacts(t *testing.T, s *Store, want admissionMigrationFacts) {
	t.Helper()
	if got := admissionMigrationCurrentFacts(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("admission facts=%#v; corpus selections=%#v", got, want)
	}
}

func admissionMigration149(t *testing.T) migration {
	t.Helper()
	for _, m := range migrations {
		if m.Version == 149 {
			if m.Name != "workflow_admission_facts" {
				t.Fatal("wrong migration149")
			}
			return m
		}
	}
	t.Fatal("missing migration149")
	return migration{}
}

func TestWorkflowAdmissionMigration149Parity(t *testing.T) {
	const work = "admission-migration-journey"
	fixture := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	s := fixture.store
	want := admissionMigrationCurrentFacts(t, s)
	if len(want.exits) == 0 || len(want.actors) == 0 {
		t.Fatal("pinned journey must produce real completed actions and coordinator facts")
	}
	actor, err := WorkflowActorRef(fixture.owner)
	if err != nil {
		t.Fatal(err)
	}
	step := workflowStep(pinnedWorkflowDefinitionForTest(t, s, work), "repair")
	for i, tc := range []struct {
		kind   string
		fields map[string]any
	}{
		{WorkflowActionStarted, map[string]any{"action_id": "dispatch_worker", "accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "admission-migration-start"}},
		{WorkflowActionCheckpointed, map[string]any{"step_kind": string(step.Kind), "checkpoint_payload": map[string]any{}, "request_id": "admission-migration-checkpoint"}},
		{WorkflowActionFailed, map[string]any{"failure_kind": "timeout", "recoverable": true}},
	} {
		version := readWorkVersion(t, s, work)
		p := tc.fields
		p["work_id"], p["expected_version"], p["resulting_version"] = work, version, version+1
		p["step_id"], p["attempt_epoch"], p["actor_ref"] = "repair", 1, actor
		event := workflowEventWithActor(fmt.Sprintf("admission-migration-real-%d", i), tc.kind, work, actor, p)
		event.OccurredAt = time.Date(2027, 1, 1, 0, 0, i, 0, time.UTC)
		if err := applyWorkflowTestOperationDirect(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(work, version)}); err != nil {
			t.Fatalf("real %s fold: %v", tc.kind, err)
		}
		var seq int64
		if err := s.db.QueryRow(`SELECT effect_seq FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		if got := admissionMigrationCurrentFacts(t, s).actors[admissionMigrationActorKey{work, actor}]; got != (admissionMigrationDisplay{seq, fmt.Sprint(p["action_id"]), event.OccurredAt.Format(time.RFC3339Nano)}) {
			// Checkpoints and failures inherit the action display when the member is absent.
			action, _ := p["action_id"].(string)
			if action == "" {
				action = tc.kind
			}
			if got != (admissionMigrationDisplay{seq, action, event.OccurredAt.Format(time.RFC3339Nano)}) {
				t.Fatalf("real %s coordinator writer=%+v", tc.kind, got)
			}
		}
	}
	assertAdmissionExplicitCorpus(t)
}

func admissionMigrationGuards(t *testing.T, s *Store, work, refusal string) {
	t.Helper()
	before := admissionMigrationCurrentFacts(t, s)
	for _, query := range []string{
		`INSERT INTO workflow_completed_exits VALUES(?, 'family', 'step', 'action', 1)`,
		`UPDATE workflow_completed_exits SET exit_seq=exit_seq+1 WHERE work_id=?`,
		`DELETE FROM workflow_completed_exits WHERE work_id=?`,
		`INSERT INTO workflow_coordinator_actions VALUES(?, 'actor/guard', 1, 'action', '2026-01-01T00:00:00Z')`,
		`UPDATE workflow_coordinator_actions SET last_effect_seq=last_effect_seq+1 WHERE work_id=?`,
		`DELETE FROM workflow_coordinator_actions WHERE work_id=?`,
	} {
		if _, err := s.db.Exec(query, work); err == nil || !strings.Contains(err.Error(), "is "+refusal) {
			t.Fatalf("unguarded %s: %v; want %s refusal", query, err, refusal)
		}
	}
	admissionMigrationRequireFacts(t, s, before)
	assertFoldGuardEmpty(t, s)
}

type admissionMigrationWriteProbe struct {
	migrationRunner
	writes int
}

func (q *admissionMigrationWriteProbe) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	q.writes++
	return q.migrationRunner.ExecContext(ctx, query, args...)
}

func TestWorkflowAdmissionMigration149RejectsMissingCompletedClass(t *testing.T) {
	for _, payload := range []string{`{"step_id":"findings"}`, `{"step_id":"findings","action_id":null}`, `{"action_id":"record_report"}`, `{"step_id":null,"action_id":"record_report"}`} {
		t.Run(payload, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "admission-invalid-148.db")
			s := &Store{db: openMigratedTo(t, path, 148), path: path}
			corpusWorkParents(t, s, "fold_guard", "admission-invalid-source")
			insertHistoricalEvent(t, s, Event{EventID: "admission-invalid-completion", Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: "admission-invalid-source", Actor: "actor/legacy", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(payload)})
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			probe := &admissionMigrationWriteProbe{migrationRunner: tx}
			err = applyMigration(t.Context(), probe, admissionMigration149(t))
			assertFailureKind(t, err, KindInvariantViolation)
			for _, id := range []string{"admission-invalid-source", "admission-invalid-completion", "sequence"} {
				if !strings.Contains(err.Error(), id) {
					t.Fatalf("corrupt completion refusal=%v missing %s", err, id)
				}
			}
			if probe.writes != 0 {
				t.Fatalf("migration wrote %d times before refusing corrupt source", probe.writes)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			var tables, manifest, source int
			if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('workflow_completed_exits','workflow_coordinator_actions')),(SELECT count(*) FROM schema_migrations WHERE version=149),(SELECT count(*) FROM domain_events WHERE event_id='admission-invalid-completion')`).Scan(&tables, &manifest, &source); err != nil {
				t.Fatal(err)
			}
			if tables != 0 || manifest != 0 || source != 1 {
				t.Fatalf("failed migration left tables=%d manifest=%d source=%d; want 0,0,1", tables, manifest, source)
			}
			var guards int
			if err := s.db.QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&guards); err != nil || guards != 0 {
				t.Fatalf("refusal leaked historical guard: %d, %v", guards, err)
			}
		})
	}
}
