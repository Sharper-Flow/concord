package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func nativeTaskRetirementDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = db.Exec(`
CREATE TABLE worktree_entries(set_id TEXT,project_id TEXT,claim_op_id TEXT,branch TEXT,base_sha TEXT,path TEXT,repository_id TEXT,state TEXT,verified_at TEXT,reclaimed_at TEXT,git_facts TEXT,member_id TEXT,PRIMARY KEY(set_id,project_id,member_id));
CREATE TABLE worktree_claims(work_id TEXT,project_id TEXT,set_id TEXT,member_id TEXT,state TEXT);
CREATE UNIQUE INDEX worktree_claims_one_active ON worktree_claims(work_id,project_id,member_id) WHERE state IN ('pending','verified');
CREATE TABLE worktree_occupancy(worktree_id TEXT);
CREATE TABLE worktree_verify_leases(work_id TEXT,project_id TEXT,path TEXT,state TEXT,outcome TEXT);
CREATE TABLE worktree_ref_outcomes(set_id TEXT,path TEXT,branch TEXT,phase TEXT);
CREATE TABLE workflow_review_state(work_id TEXT PRIMARY KEY,state TEXT);
CREATE TABLE work_context_findings(work_id TEXT,finding TEXT,source_seq INTEGER,ordinal INTEGER,finding_class TEXT);
CREATE TABLE effect_receipts(effect_seq INTEGER,event_id TEXT,kind TEXT,subject_type TEXT,subject_id TEXT);
CREATE TABLE worker_oracle_finding_facts(work_id TEXT,findings TEXT);
CREATE TABLE write_guard(active INTEGER PRIMARY KEY);
CREATE TABLE workflow_group_members(id TEXT);
CREATE TABLE workflow_member_groups(id TEXT);
CREATE TABLE workflow_member_callers(id TEXT);
CREATE TABLE workflow_worker_actors(id TEXT);
CREATE TABLE worker_attempt_dispositions(id TEXT);
CREATE TABLE worker_authorizations(work_id TEXT,attempt_id TEXT,seq INTEGER,payload TEXT);
CREATE TABLE worker_attempts(work_id TEXT,attempt_id TEXT,dispatch_payload TEXT);
CREATE TABLE worker_job_revisions(work_id TEXT,job_id TEXT,revision INTEGER,digest TEXT,recorded_payload TEXT);
CREATE TABLE workflow_correction_state(work_id TEXT PRIMARY KEY,state TEXT NOT NULL CHECK(json_valid(state)));
CREATE TRIGGER workflow_correction_state_guard_update BEFORE UPDATE ON workflow_correction_state BEGIN SELECT RAISE(ABORT, 'workflow_correction_state is write-seam-only') WHERE NOT EXISTS(SELECT 1 FROM write_guard WHERE active=1); END;
CREATE TABLE workflow_instances(work_id TEXT PRIMARY KEY,execution_actor_ref TEXT,latest_non_dispatch_start_seq INTEGER,result_disposition TEXT);
CREATE TABLE workflow_step_epochs(work_id TEXT,step_id TEXT,attempt_epoch INTEGER,latest_start_seq INTEGER,latest_start_event_id TEXT,latest_start_payload TEXT,latest_dispatch_start_seq INTEGER,latest_dispatch_start_event_id TEXT,latest_dispatch_start_payload TEXT);
INSERT INTO worktree_entries VALUES('set:one','project:one','claim:one','topic/one','base','/synthetic/one','repo:one','active','stamp',NULL,'{}','');
INSERT INTO worktree_entries VALUES('set:one','project:one','claim:member','topic/member','base','/synthetic/member','repo:one','reclaimed','stamp','stamp','{}','member:one');
INSERT INTO worktree_claims VALUES('work:one','project:one','set:one','','verified');
INSERT INTO workflow_review_state VALUES('work:one','{"candidate":{"project_id":"project:one","commit":"retained","tree":"retained"},"accepted":null,"findings":[]}');
INSERT INTO workflow_instances VALUES('work:one','actor:one',8,'');
INSERT INTO workflow_step_epochs VALUES('work:one','step:one',2,8,'start:one','{}',7,'dispatch:one','{}');
`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func nativeTaskRetirementMigration(t *testing.T) migration {
	t.Helper()
	for _, m := range migrations {
		if m.Name == "native_task_cutover" {
			return m
		}
	}
	t.Fatal("native Task retirement migration is absent")
	return migration{}
}

func runNativeTaskRetirement(t *testing.T, db *sql.DB, retirementSQL string) error {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	m := nativeTaskRetirementMigration(t)
	m.SQL = retirementSQL
	if err := applyMigration(t.Context(), tx, m); err != nil {
		return err
	}
	return tx.Commit()
}

func TestNativeTaskRetirementRefusesUnsettledMemberTrees(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"active-tree", `UPDATE worktree_entries SET state='active' WHERE member_id<>''`},
		{"pending-claim", `INSERT INTO worktree_claims VALUES('work:one','project:one','set:one','member:one','pending')`},
		{"verified-claim", `INSERT INTO worktree_claims VALUES('work:one','project:one','set:one','member:one','verified')`},
		{"occupancy", `INSERT INTO worktree_occupancy VALUES('set:one:project:one:claim:member')`},
		{"verify-lease", `INSERT INTO worktree_verify_leases VALUES('work:one','project:one','/synthetic/member','held','running')`},
		{"reference-effect", `INSERT INTO worktree_ref_outcomes VALUES('set:one','/synthetic/member','topic/member','retained_unproven')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := nativeTaskRetirementDB(t)
			original := `{"boundary":0,"records":[{"seq":10,"source_id":"correction:one","fields":{"action_id":"request_correction"}}]}`
			if _, err := db.Exec(`INSERT INTO workflow_correction_state VALUES('work:one',?)`, original); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			err := runNativeTaskRetirement(t, db, nativeTaskRetirementMigration(t).SQL)
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindResourceBusy {
				t.Fatalf("cutover=%v, want resource_busy", err)
			}
			var tables int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name IN ('worker_attempts','workflow_member_callers')`).Scan(&tables); err != nil || tables != 2 {
				t.Fatalf("refused cutover changed tables: count=%d err=%v", tables, err)
			}
			var rows int
			if err := db.QueryRow(`SELECT count(*) FROM worktree_entries`).Scan(&rows); err != nil || rows != 2 {
				t.Fatalf("refused cutover changed native rows: count=%d err=%v", rows, err)
			}
			var retained string
			if err := db.QueryRow(`SELECT state FROM workflow_correction_state`).Scan(&retained); err != nil || retained != original {
				t.Fatalf("refused cutover changed correction state: %s err=%v", retained, err)
			}
		})
	}
}

func TestNativeTaskRetirementPreservesFindingSourceInventory(t *testing.T) {
	db := nativeTaskRetirementDB(t)
	oracle := AcceptanceOracle{Owners: []OracleOwner{{OwnerID: "owner:one", DomainID: "domain:one"}}, Cases: []OracleCase{{CaseID: "case:one", OwnerID: "owner:one", EntryPoint: "entry:one", Judge: OracleJudgeReview}}, Controls: []OracleControl{}}
	finding := ReviewFinding{FindingID: "local:one", Status: "open", InScope: true, Blocking: true, Oracle: &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:one", CaseIDs: []string{"case:one"}}}
	state, err := json.Marshal(workflowReviewFacts{Findings: []ReviewFinding{finding}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_review_state SET state=? WHERE work_id='work:one'`, string(state)); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(finding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO work_context_findings VALUES('work:one',?,7,0,'ranked')`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO effect_receipts VALUES(7,'report:one','worker.completed','work_item','work:one');INSERT INTO worker_oracle_finding_facts VALUES('work:one','[{"FindingID":"finding:7:0","DomainID":"domain:one","SourceEventSeq":7,"SourceOrdinal":0,"source_job":{"job_id":"job:one","revision":1,"digest":"digest:one"}}]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worker_job_revisions VALUES('work:one','job:one',1,'digest:one',?)`, workflowJSON(map[string]any{"acceptance_oracle": oracle})); err != nil {
		t.Fatal(err)
	}
	if err := runNativeTaskRetirement(t, db, nativeTaskRetirementMigration(t).SQL); err != nil {
		t.Fatal(err)
	}
	retained, err := readWorkflowReviewFacts(t.Context(), db, "work:one")
	if err != nil {
		t.Fatal(err)
	}
	source := retained.findingSources["local:one"]
	if source.CanonicalID != "finding:7:0" || source.DomainID != "domain:one" || source.EventID != "report:one" || source.Kind != "worker.completed" || source.Seq != 7 || source.Ordinal != 0 || !oracleEntryIdentical(source.Oracle, &oracle) {
		t.Fatalf("finding source lost its exact bound inventory: %+v", source)
	}
	if retained.Accepted != nil || len(retained.Findings) != 1 || retained.Findings[0].Status != "open" {
		t.Fatalf("migration fabricated acceptance or closure: %+v", retained)
	}
}

func TestNativeTaskRetirementPreservesOrdinaryState(t *testing.T) {
	db := nativeTaskRetirementDB(t)
	m := nativeTaskRetirementMigration(t)
	if !m.Breaking {
		t.Fatal("destructive cutover is not breaking")
	}
	if err := runNativeTaskRetirement(t, db, m.SQL); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND (name LIKE 'worker_%' OR name IN ('workflow_member_groups','workflow_group_members','workflow_worker_actors','workflow_member_callers'))`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired tables=%d err=%v", count, err)
	}
	var path, branch, state string
	if err := db.QueryRow(`SELECT path,branch,state FROM worktree_entries`).Scan(&path, &branch, &state); err != nil || path != "/synthetic/one" || branch != "topic/one" || state != "active" {
		t.Fatalf("ordinary entry=%q %q %q err=%v", path, branch, state, err)
	}
	if err := db.QueryRow(`SELECT state FROM workflow_review_state WHERE work_id='work:one'`).Scan(&state); err != nil || !strings.Contains(state, `"commit":"retained"`) || !strings.Contains(state, `"accepted":null`) {
		t.Fatalf("Review fabricated or lost: %s err=%v", state, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('worktree_claims') WHERE name='member_id'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("member claim column=%d err=%v", count, err)
	}
	if _, err := db.Exec(`INSERT INTO worktree_claims(work_id,project_id,set_id,state) VALUES('work:one','project:one','other','verified')`); err == nil {
		t.Fatal("duplicate ordinary active claim admitted")
	}
	var actor, start string
	var epoch, seq int
	if err := db.QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id='work:one'`).Scan(&actor); err != nil || actor != "actor:one" {
		t.Fatalf("ordinary execution actor=%q err=%v", actor, err)
	}
	if err := db.QueryRow(`SELECT attempt_epoch,latest_start_seq,latest_start_event_id FROM workflow_step_epochs WHERE work_id='work:one'`).Scan(&epoch, &seq, &start); err != nil || epoch != 2 || seq != 8 || start != "start:one" {
		t.Fatalf("ordinary fenced start=%d %d %q err=%v", epoch, seq, start, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('workflow_step_epochs') WHERE name LIKE 'latest_dispatch_%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retired dispatch columns=%d err=%v", count, err)
	}
}

func TestNativeTaskRetirementPreservesOnlyUnconsumedCorrectionDirections(t *testing.T) {
	for _, tc := range []struct {
		name             string
		authorizationSeq int
		materialized     bool
		sameWork         bool
		sameAttempt      bool
		boundary         int
		wantOpen         bool
	}{
		{"no-authorization", 0, false, true, true, 0, true},
		{"consumed", 20, true, true, true, 0, false},
		{"authorization-only", 20, false, true, true, 0, true},
		{"earlier-authorization-later-dispatch", 5, true, true, true, 0, true},
		{"acceptance-boundary-does-not-consume-request", 5, true, true, true, 30, true},
		{"foreign-work-dispatch", 20, true, false, true, 0, true},
		{"another-attempt-dispatch", 20, true, true, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := nativeTaskRetirementDB(t)
			raw := workflowJSON(map[string]any{"boundary": tc.boundary, "unresolved": map[string]bool{"job:old|1": false}, "repairs": map[string]any{}, "records": []map[string]any{{"seq": 10, "source_id": "correction:original", "job_key": "", "fields": workflowCorrectionCompletionFields{ActionID: "request_correction", CorrectionDiagnosis: "reported failure", CorrectionStrategy: "correct the output", CorrectionPredicates: []string{"predicate:one"}, CorrectionEvidence: []string{"review:original"}}}}})
			if _, err := db.Exec(`INSERT INTO workflow_correction_state VALUES('work:one',?)`, raw); err != nil {
				t.Fatal(err)
			}
			if tc.authorizationSeq != 0 {
				if _, err := db.Exec(`INSERT INTO worker_authorizations VALUES('work:one','attempt:one',?,'{"action_id":"dispatch_worker","worker_attempt_id":"attempt:one"}')`, tc.authorizationSeq); err != nil {
					t.Fatal(err)
				}
				work, attempt := "work:one", "attempt:one"
				if !tc.sameWork {
					work = "work:other"
				}
				if !tc.sameAttempt {
					attempt = "attempt:other"
				}
				var dispatch any
				if tc.materialized {
					dispatch = workflowJSON(map[string]any{"attempt_id": attempt})
				}
				if _, err := db.Exec(`INSERT INTO worker_attempts VALUES(?,?,?)`, work, attempt, dispatch); err != nil {
					t.Fatal(err)
				}
			}
			if err := runNativeTaskRetirement(t, db, nativeTaskRetirementMigration(t).SQL); err != nil {
				t.Fatal(err)
			}
			context, err := readWorkflowCorrectionContext(t.Context(), db, "work:one")
			if err != nil || (context != nil) != tc.wantOpen {
				t.Fatalf("retained direction=%+v err=%v, open=%v", context, err, tc.wantOpen)
			}
			if context != nil && (context.SourceEventID != "correction:original" || context.SourceEventSeq != 10 || context.Diagnosis != "reported failure" || context.Strategy != "correct the output" || len(context.PredicateIDs) != 1 || context.PredicateIDs[0] != "predicate:one" || len(context.EvidenceRefs) != 1 || context.EvidenceRefs[0] != "review:original") {
				t.Fatalf("migration changed genuine direction facts: %+v", context)
			}
			var normalized string
			if err := db.QueryRow(`SELECT state FROM workflow_correction_state`).Scan(&normalized); err != nil || strings.Contains(normalized, "unresolved") || strings.Contains(normalized, "job_key") || strings.Contains(normalized, "repairs") {
				t.Fatalf("retired worker debt remains in correction owner: %s err=%v", normalized, err)
			}
		})
	}
}

func TestNativeTaskRetirementRollsBackNormalizationAndDDL(t *testing.T) {
	db := nativeTaskRetirementDB(t)
	original := `{"boundary":0,"unresolved":{},"records":[{"seq":10,"source_id":"correction:one","fields":{"action_id":"request_correction"}}]}`
	if _, err := db.Exec(`INSERT INTO workflow_correction_state VALUES('work:one',?)`, original); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worker_authorizations VALUES('work:one','attempt:one',20,'{}'); INSERT INTO worker_attempts VALUES('work:one','attempt:one','{}')`); err != nil {
		t.Fatal(err)
	}
	err := runNativeTaskRetirement(t, db, nativeTaskRetirementMigration(t).SQL+`; INSERT INTO absent_cutover_table VALUES(1);`)
	if err == nil {
		t.Fatal("injected retirement failure succeeded")
	}
	var actual string
	if err := db.QueryRow(`SELECT state FROM workflow_correction_state`).Scan(&actual); err != nil || actual != original {
		t.Fatalf("failed cutover changed correction state: %s err=%v", actual, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM worker_authorizations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed cutover dropped worker source: count=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM worktree_entries WHERE member_id<>''`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed cutover changed member source: count=%d err=%v", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM write_guard`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed cutover leaked write guard: count=%d err=%v", count, err)
	}
}

func TestNativeTaskRetirementCorrectionNormalizationRejectsInvalidSource(t *testing.T) {
	db := nativeTaskRetirementDB(t)
	if _, err := db.Exec(`INSERT INTO workflow_correction_state VALUES('work:one','{"records":[{"seq":10,"fields":{"action_id":"request_correction"}}]}')`); err != nil {
		t.Fatal(err)
	}
	var failure *Failure
	if err := runNativeTaskRetirement(t, db, nativeTaskRetirementMigration(t).SQL); !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
		t.Fatalf("missing historical correction source=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='worker_attempts'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid source began destructive retirement: count=%d err=%v", count, err)
	}
}

func TestNativeTaskRetirementCorrectionNormalizationSelectsGenuineLatestRequest(t *testing.T) {
	state, err := normalizeNativeTaskCorrection(`{"boundary":30,"records":[{"seq":20,"source_id":"correction:latest","fields":{"action_id":"request_correction"}},{"seq":40,"source_id":"failure:retired","job_key":"job:one|1","fields":{"action_id":"record_worker_failure"}},{"seq":10,"source_id":"correction:older","fields":{"action_id":"request_correction"}}]}`, 15)
	if err != nil || state.Boundary != 0 || len(state.Records) != 1 || state.Records[0].SourceID != "correction:latest" {
		t.Fatalf("latest unconsumed request=%+v err=%v", state, err)
	}
	state, err = normalizeNativeTaskCorrection(`{"boundary":30,"records":[{"seq":40,"source_id":"rejection:retired","fields":{"action_id":"reject_worker_result"}}]}`, 15)
	if err != nil || state.Boundary != 30 || len(state.Records) != 0 {
		t.Fatalf("retired worker rejection became a current request: %+v err=%v", state, err)
	}
	var failure *Failure
	if _, err := normalizeNativeTaskCorrection(`malformed`, 0); !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
		t.Fatalf("malformed legacy state=%v", err)
	}
}

func TestNativeTaskRetirementPreservesCallerWriteGuard(t *testing.T) {
	db := nativeTaskRetirementDB(t)
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := runNativeTaskRetirement(t, db, nativeTaskRetirementMigration(t).SQL); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM write_guard WHERE active=1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cutover removed caller-owned write guard: count=%d err=%v", count, err)
	}
}
