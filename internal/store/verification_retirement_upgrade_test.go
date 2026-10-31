package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func verificationRetirementFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 170)
	digest := "sha256:" + strings.Repeat("d", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,resume_cursor,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES('verify:retire',1,'work-upgrade-from','worktree.verify',?,'verify','internal_sqlite',?,'{}','completed','{}','["verify:retire"]','[]','','operator-upgrade','request:retire',?,?,?)`, digest, digest, retirementUpgradeTime, retirementUpgradeTime, digest)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_evidence_bindings VALUES('work-upgrade-from',11,'event-upgrade-0',?,'{"evidence_kind":"verification","immutable_subject_ref":"verify:retire","producer_run_ref":"verify:retire"}')`, "actor:"+strings.Repeat("a", 64))
	retirementUpgradeExec(t, db, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,exit_code,outcome,result_json,stdout_blob,stderr_blob) VALUES('lease:retire','work-upgrade-from','project-upgrade','/synthetic/retirement','released','client-upgrade','agent-upgrade','session-upgrade','operator-upgrade','["go","test"]',?,?,0,'completed','{}',X'616263',X'646566')`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	return db, path
}

func TestVerificationRetirementVerifiedSnapshotUpgrade(t *testing.T) {
	useStampedBuild(t)
	db, path := verificationRetirementFixture(t)
	before := retirementUpgradeSnapshot(t, db)
	if s, err := Open(t.Context(), path); err == nil {
		s.Close()
		t.Fatal("ordinary open crossed the breaking verification retirement")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	snapshot := filepath.Join(t.TempDir(), "verification-predecessor.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 170 {
		t.Fatalf("predecessor snapshot = %+v, %v", verified, err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`SELECT name FROM sqlite_schema WHERE name='worktree_verify_leases' OR tbl_name='worktree_verify_leases'`,
		`SELECT op_id FROM durable_operations WHERE workflow_type_ref='worktree.verify'`,
		`SELECT work_id FROM workflow_evidence_bindings WHERE json_extract(payload,'$.producer_run_ref')='verify:retire'`,
	} {
		if rows := retirementUpgradeRows(t, db, query); len(rows) != 0 {
			t.Fatalf("retired verification state survives: %s: %v", query, rows)
		}
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM work_items ORDER BY rowid`); !reflect.DeepEqual(got, before["work_items"]) {
		t.Fatal("verification retirement changed work items")
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY rowid`); !reflect.DeepEqual(got, before["effect_receipts"]) {
		t.Fatal("verification retirement changed durable effect receipts")
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("upgrade damaged predecessor snapshot: %+v, %v", verified, err)
	}
	retirementUpgradeHealthy(t, db)
}

func TestVerificationRetirementRefusesActiveRun(t *testing.T) {
	useStampedBuild(t)
	db, path := verificationRetirementFixture(t)
	retirementUpgradeExec(t, db, `UPDATE worktree_verify_leases SET state='held',outcome='running'`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindResourceBusy)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused upgrade changed active verification state")
	}
}

func TestVerificationRetirementRollsBackAfterTableDrop(t *testing.T) {
	useStampedBuild(t)
	db, path := verificationRetirementFixture(t)
	retirementUpgradeExec(t, db, `CREATE TRIGGER verification_retirement_failure BEFORE DELETE ON write_guard WHEN NOT EXISTS(SELECT 1 FROM sqlite_schema WHERE name='worktree_verify_leases') BEGIN SELECT RAISE(ABORT,'verification retirement rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "verification retirement rollback witness") {
		t.Fatalf("upgrade missed the post-drop failure: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed upgrade changed verification table, streams, bindings, operations or manifest")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER verification_retirement_failure`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("retry after failure removal: %v", err)
	}
}
