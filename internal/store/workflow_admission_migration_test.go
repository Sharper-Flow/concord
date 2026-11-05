package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	useStampedBuild(t)
	c := admissionExplicitCorpus()
	s := c.prefix(t, 148)
	want := admissionCorpusOracle(c.events)
	advanceHistoricalPrefix(t, s.db, 149)
	admissionMigrationRequireFacts(t, s, want)
	if len(want.exits) == 0 || len(want.actors) == 0 {
		t.Fatal("historical corpus must witness exits and coordinator actions")
	}
	advanceHistoricalPrefix(t, s.db, 182)
	retirementUpgradeExec(t, s.db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, s.db, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('admission-product','Admission product','prototype','operator_only',1,'t','t')`)
	retirementUpgradeExec(t, s.db, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('admission-project','Admission project',1,'t','t')`)
	retirementUpgradeExec(t, s.db, `INSERT INTO product_projects(product_id,project_id,role) VALUES('admission-product','admission-project','primary')`)
	retirementUpgradeExec(t, s.db, `INSERT INTO work_projects(work_id,project_id,role) VALUES('admission-corpus','admission-project','primary')`)
	retirementUpgradeExec(t, s.db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state) VALUES('admission-corpus','workflow.research',?,'findings','running')`, "sha256:"+strings.Repeat("a", 64))
	retirementUpgradeExec(t, s.db, `DELETE FROM write_guard`)
	receipts := retirementUpgradeRows(t, s.db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	if _, err := Upgrade(t.Context(), s.path, nil, outcomeSnapshotOption(t, t.Context(), s.path)); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, s.db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, receipts) {
		t.Fatalf("upgrade changed receipts: before=%v after=%v", receipts, got)
	}
	if got := admissionMigrationCurrentFacts(t, s); len(got.exits) != 0 || len(got.actors) != 0 {
		t.Fatalf("parked workflow retained step admission state: %+v", got)
	}
	if got := retirementUpgradeRows(t, s.db, `SELECT instance_state,workflow_type,step_plan FROM workflow_instances WHERE work_id='admission-corpus'`); !reflect.DeepEqual(got, [][]any{{"intake", nil, nil}}) {
		t.Fatalf("historical workflow did not park at intake: %v", got)
	}
	retirementUpgradeExec(t, s.db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, s.db, `INSERT INTO workflow_completed_exits VALUES('admission-corpus','family','step','action',1)`)
	retirementUpgradeExec(t, s.db, `INSERT INTO workflow_coordinator_actions VALUES('admission-corpus','actor/guard',1,'action','2026-01-01T00:00:00Z')`)
	retirementUpgradeExec(t, s.db, `DELETE FROM write_guard`)
	admissionMigrationGuards(t, s, "admission-corpus", "write-seam-only")
	retirementUpgradeHealthy(t, s.db)
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

func TestWorkflowAdmissionCutoverPreservesWorkAndLinearIdentity(t *testing.T) {
	useStampedBuild(t)
	ctx := t.Context()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 182)
	actor := "actor:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `UPDATE work_items SET lifecycle='in_progress' WHERE id='work-upgrade-from'`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES('work-upgrade-from','workflow.implementation',?,'release','running',?,?)`, "sha256:"+strings.Repeat("b", 64), actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES('work-upgrade-to','workflow.implementation',?,'release','running',?,?)`, "sha256:"+strings.Repeat("b", 64), actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES('work-upgrade-to',23,'event-upgrade-4','operator',?,1,'{"delivery_artifact":"https://example.com/change/1"}')`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_predicate_verdicts(work_id,contract_version,predicate_id,verdict_seq,actor,payload) VALUES('work-upgrade-from',1,'predicate:retained',23,?,'{ "verdict": "pass", "evidence": ["ref:proof"] }')`, actor)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch) VALUES('work-upgrade-from','release',1)`)
	retirementUpgradeExec(t, db, `INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,created_at,updated_at) VALUES('work-upgrade-from','uuid-confirmed','CON-1','https://linear.app/x/issue/CON-1',?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	work := retirementUpgradeRows(t, db, `SELECT * FROM work_items WHERE id='work-upgrade-from'`)
	links := retirementUpgradeRows(t, db, `SELECT * FROM linear_issue_links ORDER BY work_id`)
	receipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	actors := retirementUpgradeRows(t, db, `SELECT * FROM workflow_actors ORDER BY actor_ref`)
	delivery := retirementUpgradeRows(t, db, `SELECT * FROM workflow_delivery_state ORDER BY work_id`)
	snapshot := filepath.Join(t.TempDir(), "before-cutover.db")
	manifest, err := BackupDatabase(ctx, path, snapshot)
	if err != nil {
		t.Fatalf("snapshot predecessor: %v", err)
	}
	if _, err := Upgrade(ctx, path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatalf("upgrade predecessor: %v", err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM work_items WHERE id='work-upgrade-from'`); !reflect.DeepEqual(got, work) {
		t.Fatalf("work identity changed: before=%v after=%v", work, got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM linear_issue_links ORDER BY work_id`); !reflect.DeepEqual(got, links) {
		t.Fatalf("Linear identity changed: before=%v after=%v", links, got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, receipts) {
		t.Fatalf("effect receipts changed: before=%v after=%v", receipts, got)
	}
	for query, want := range map[string][][]any{
		`SELECT * FROM workflow_actors ORDER BY actor_ref`:       actors,
		`SELECT * FROM workflow_delivery_state ORDER BY work_id`: delivery,
	} {
		if got := retirementUpgradeRows(t, db, query); !reflect.DeepEqual(got, want) {
			t.Errorf("retained facts changed for %s: before=%v after=%v", query, want, got)
		}
	}
	var state string
	var planType, plan, owner sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT instance_state,workflow_type,step_plan,execution_actor_ref FROM workflow_instances WHERE work_id='work-upgrade-from'`).Scan(&state, &planType, &plan, &owner); err != nil {
		t.Fatal(err)
	}
	if state != "intake" || planType.Valid || plan.Valid || owner.Valid {
		t.Fatalf("undelivered workflow did not return to intake: state=%q type=%v plan=%v owner=%v", state, planType, plan, owner)
	}
	if got := retirementUpgradeRows(t, db, `SELECT w.lifecycle,i.instance_state,i.workflow_type,i.step_plan,i.execution_actor_ref FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id='work-upgrade-to'`); !reflect.DeepEqual(got, [][]any{{"completed", "completed", nil, nil, actor}}) {
		t.Fatalf("delivered workflow did not complete: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_step_epochs`); len(got) != 0 {
		t.Fatalf("retired step epochs survived: %v", got)
	}
	verified, err := VerifyBackup(ctx, snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 182 {
		t.Fatalf("predecessor snapshot changed: %+v %v", verified, err)
	}
}

func TestWorkflowAdmissionCutoverRollback(t *testing.T) {
	useStampedBuild(t)
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 182)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch) VALUES('work-upgrade-from','release',1)`)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	retirementUpgradeExec(t, db, `CREATE TRIGGER cutover_rollback_witness BEFORE DELETE ON workflow_step_epochs BEGIN SELECT RAISE(ABORT,'cutover rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "cutover rollback witness") {
		t.Fatalf("upgrade did not reach rollback witness: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed cutover changed predecessor schema or rows")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER cutover_rollback_witness`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeHealthy(t, db)
}
