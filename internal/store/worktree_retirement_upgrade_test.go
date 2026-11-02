package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var retiredWorktreeTables = []string{
	"bootstrap_operations", "worktree_claim_native_fences", "worktree_claims",
	"worktree_entries", "worktree_occupancy", "worktree_ref_outcomes",
	"session_vacate_requests", "session_claim_landings", "session_relocation_counts",
}

func worktreeRetirementFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 172)
	digest := "sha256:" + strings.Repeat("d", 64)
	commit := strings.Repeat("a", 40)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES('claim:retire','work-upgrade-from','project-upgrade','set:retire','topic/retire',?,'/synthetic/retire','verified','operator-upgrade','request:retire',?,?)`, commit, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO worktree_entries VALUES('set:retire','project-upgrade','claim:retire','topic/retire',?,'/synthetic/retire','repo:retire','active',?,NULL,'{ "preserve": true }')`, commit, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/retire','set:retire',1)`)
	retirementUpgradeExec(t, db, `INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,has_process_identity) VALUES('set:retire:project-upgrade:claim:retire','session-upgrade',?,0)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO worktree_ref_outcomes(set_id,project_id,claim_op_id,branch,phase,reason,path,recorded_at) VALUES('set:retire','project-upgrade','claim:retire','topic/retire','retained_unproven','preserve host branch','/synthetic/retire',?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO session_vacate_requests VALUES('work-upgrade-from','session-upgrade','project-upgrade','/synthetic/source','/synthetic/retire',23,0)`)
	retirementUpgradeExec(t, db, `INSERT INTO session_claim_landings VALUES('work-upgrade-from','session-upgrade','project-upgrade','claim:retire')`)
	retirementUpgradeExec(t, db, `INSERT INTO session_relocation_counts VALUES('work-upgrade-from','session-upgrade',2,1,3)`)
	retirementUpgradeExec(t, db, `INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at) VALUES('bootstrap:retire','bootstrap:retire',?,'{}','product-upgrade','project-upgrade','work-upgrade-from','/synthetic/source',1,'pending',?,?)`, digest, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO project_handoffs(handoff_id,work_id,contract_version,source_project_id,target_project_id,source_session_ref,bounded_job,changes,verification,artifact_refs,blockers,next_action,state,recorded_at,recorded_seq) VALUES('handoff:retained','work-upgrade-from',1,'project-upgrade','project-target','session-upgrade','Retain authenticated handoff','[]','[]','["artifact:retained"]','[]','Continue in target','recorded',?,23)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,resume_cursor,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES('operation:retained',1,'work-upgrade-from','workflow.break_fix',?,'repair','internal_sqlite',?,'{}','completed','{ "retained": true }','[]','[]','','operator-upgrade','request:retained',?,?,?)`, digest, digest, retirementUpgradeTime, retirementUpgradeTime, digest)
	retirementUpgradeExec(t, db, `INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,first_observed_at,last_observed_at) VALUES('operator-upgrade','concord_work_transition','workflow_action','key:retained',?,'operation:retained','[]',?,?)`, digest, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	retirementUpgradeHealthy(t, db)
	return db, path
}

func assertWorktreeTablesAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range retiredWorktreeTables {
		if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name=? OR tbl_name=?`, table, table); len(got) != 0 {
			t.Errorf("retired ownership schema for %s survives: %v", table, got)
		}
	}
}

func TestWorktreeRetirementVerifiedSnapshotUpgrade(t *testing.T) {
	useStampedBuild(t)
	db, path := worktreeRetirementFixture(t)
	before := retirementUpgradeSnapshot(t, db)
	retainedBindings := retirementUpgradeRows(t, db, `SELECT work_id,contract_version,product_id,home_domain_id,projection_hash FROM workflow_architecture_bindings ORDER BY rowid`)
	if s, err := Open(t.Context(), path); err == nil {
		s.Close()
		t.Fatal("ordinary open crossed the breaking ownership retirement")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("ordinary open changed the predecessor")
	}
	snapshot := filepath.Join(t.TempDir(), "ownership-predecessor.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 172 {
		t.Fatalf("verified predecessor = %+v, %v", verified, err)
	}
	report, err := Upgrade(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The upgrade runs from the v172 predecessor to the current head, so the
	// applied list is the complete pending tail starting at the worktree
	// retirement.
	var wantApplied []int
	for _, m := range migrations {
		if m.Version > 172 {
			wantApplied = append(wantApplied, m.Version)
		}
	}
	if !reflect.DeepEqual(report.Applied, wantApplied) {
		t.Fatalf("applied migrations = %v, want %v", report.Applied, wantApplied)
	}
	assertWorktreeTablesAbsent(t, db)
	assertLawIndexRetired(t, db)
	if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name='workflow_contract_law_revisions' OR tbl_name='workflow_contract_law_revisions'`); len(got) != 0 {
		t.Fatalf("retired law pin schema survives: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT name FROM pragma_table_info('workflow_architecture_bindings') WHERE name='domain_registry_content_hash'`); len(got) != 0 {
		t.Fatalf("retired registry hash column survives: %v", got)
	}
	for table, want := range before {
		switch table {
		case "schema", "schema_migrations", "store_revision", "workflow_contract_law_revisions":
			continue
		case "workflow_architecture_bindings":
			want = retainedBindings
		}
		retired := false
		for _, name := range append(append([]string(nil), retiredWorktreeTables...), lawIndexRetiredPrefixes...) {
			retired = retired || table == name
		}
		if !retired {
			if got := retirementUpgradeRows(t, db, `SELECT * FROM "`+table+`" ORDER BY rowid`); !reflect.DeepEqual(got, want) {
				t.Errorf("unrelated table %s changed: got %v, want %v", table, got, want)
			}
		}
	}
	backup, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if got := retirementUpgradeSnapshot(t, backup); !reflect.DeepEqual(got, before) {
		t.Fatal("predecessor snapshot lost schema or rows")
	}
	for _, table := range retiredWorktreeTables {
		if len(before[table]) == 0 {
			t.Errorf("snapshot did not exercise populated %s", table)
		}
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("upgrade damaged predecessor snapshot: %+v, %v", verified, err)
	}
	retirementUpgradeHealthy(t, db)
	if _, err := PlanUpgradeReadiness(t.Context(), path); err != nil {
		t.Fatalf("post-retirement readiness: %v", err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("idempotent upgrade: %v", err)
	}
}

func TestWorktreeRetirementAtomicRollback(t *testing.T) {
	useStampedBuild(t)
	db, path := worktreeRetirementFixture(t)
	retirementUpgradeExec(t, db, `CREATE TRIGGER worktree_retirement_failure BEFORE INSERT ON schema_migrations WHEN NEW.version=173 AND NOT EXISTS(SELECT 1 FROM sqlite_schema WHERE name='worktree_claims') BEGIN SELECT RAISE(ABORT,'ownership retirement rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "ownership retirement rollback witness") {
		t.Fatalf("upgrade missed post-drop failure: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed upgrade changed schema, ownership rows, ordinary state or manifest")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER worktree_retirement_failure`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	assertWorktreeTablesAbsent(t, db)
}

func TestWorktreeRetirementFreshAbsence(t *testing.T) {
	s := openTemp(t)
	assertWorktreeTablesAbsent(t, s.db)
	retirementUpgradeHealthy(t, s.db)
}

func TestWorktreeRetirementHeldReleaseRefusesWithoutChanges(t *testing.T) {
	useStampedBuild(t)
	db, path := worktreeRetirementFixture(t)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, []HeldSchema{{PID: 123, ReleaseRoot: "/synthetic/release", SchemaVersion: 172}})
	assertFailureKind(t, err, KindUpgradeBlocked)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("held release refusal changed predecessor state")
	}
}
