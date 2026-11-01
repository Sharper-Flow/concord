package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
)

func staleVerificationRetirementFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 168)
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	for i, lifecycle := range []string{"completed", "completed", "completed", "cancelled", "cancelled", "cancelled"} {
		id := fmt.Sprintf("work-stale-%d", i)
		missing := filepath.Join(root, id)
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatalf("expected removed worktree: %v", err)
		}
		pid, started := 0, ""
		if i >= 4 {
			pid, started = dead.Process.Pid, "1"
		}
		retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?,'task','Stale verification witness',?,0,1,?,?)`, id, lifecycle, retirementUpgradeTime, retirementUpgradeTime)
		retirementUpgradeExec(t, db, `INSERT INTO work_projects VALUES(?,'project-upgrade','primary')`, id)
		retirementUpgradeExec(t, db, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome,owner_pid,owner_started) VALUES(?,?,'project-upgrade',?,'held','client-upgrade','agent-upgrade','session-upgrade','operator-upgrade','["go","test"]',?,'running',?,?)`, "lease:"+id, id, missing, retirementUpgradeTime, pid, started)
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	return db, path
}

func TestVerificationRetirementUpgradesStaleTerminalLeasesFrom168(t *testing.T) {
	useStampedBuild(t)
	db, path := staleVerificationRetirementFixture(t)
	before := retirementUpgradeSnapshot(t, db)
	snapshot := filepath.Join(t.TempDir(), "pre-stale-verification.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil || manifest.SchemaVersion != 168 {
		t.Fatalf("schema-168 snapshot = %+v, %v", manifest, err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"work_items", "effect_receipts", "workflow_evidence_bindings", "durable_operations"} {
		if got := retirementUpgradeRows(t, db, `SELECT * FROM `+table+` ORDER BY rowid`); !reflect.DeepEqual(got, before[table]) {
			t.Errorf("stale lease retirement changed %s", table)
		}
	}
	if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name='worktree_verify_leases'`); len(got) != 0 {
		t.Fatal("verification lease table survives")
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("upgrade damaged predecessor snapshot: %+v, %v", verified, err)
	}
	retirementUpgradeHealthy(t, db)
}

func TestVerificationRetirementRefusesUnsafeLeasesFrom168(t *testing.T) {
	useStampedBuild(t)
	start, err := hostlease.ProcessStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, update, recovery string
		args                   []any
	}{
		{"live-terminal-owner", `UPDATE worktree_verify_leases SET owner_pid=?,owner_started=? WHERE work_id='work-stale-5'`, "wait for verification owner pid", []any{os.Getpid(), fmt.Sprint(start)}},
		{"live-running-released", `UPDATE worktree_verify_leases SET state='released',owner_pid=?,owner_started=? WHERE work_id='work-stale-5'`, "wait for verification owner pid", []any{os.Getpid(), fmt.Sprint(start)}},
		{"live-held-completed", `UPDATE worktree_verify_leases SET outcome='completed',owner_pid=?,owner_started=? WHERE work_id='work-stale-5'`, "wait for verification owner pid", []any{os.Getpid(), fmt.Sprint(start)}},
		{"nonterminal-unknown-owner", `UPDATE work_items SET lifecycle='needed' WHERE id='work-stale-0'`, "complete or cancel", nil},
		{"nonterminal-dead-owner", `UPDATE work_items SET lifecycle='in_progress' WHERE id='work-stale-5'`, "complete or cancel", nil},
		{"legacy-existing-path", `UPDATE worktree_verify_leases SET path=? WHERE work_id='work-stale-0'`, "preserve changes", []any{t.TempDir()}},
		{"partial-owner", `UPDATE worktree_verify_leases SET owner_pid=1 WHERE work_id='work-stale-0'`, "maintainer", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := staleVerificationRetirementFixture(t)
			retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
			retirementUpgradeExec(t, db, tc.update, tc.args...)
			retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
			before := retirementUpgradeSnapshot(t, db)
			_, err := Upgrade(t.Context(), path, nil)
			assertFailureKind(t, err, KindResourceBusy)
			var failure *Failure
			if !errors.As(err, &failure) || !strings.Contains(failure.RecoveryAction, tc.recovery) {
				t.Fatalf("upgrade recovery = %v, want %q", err, tc.recovery)
			}
			if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("refused schema-168 upgrade changed predecessor rows or migrations")
			}
		})
	}
}

func TestVerificationRetirementStaleLeaseRollbackFrom168(t *testing.T) {
	useStampedBuild(t)
	db, path := staleVerificationRetirementFixture(t)
	retirementUpgradeExec(t, db, `CREATE TRIGGER verification_retirement_failure BEFORE DELETE ON write_guard WHEN NOT EXISTS(SELECT 1 FROM sqlite_schema WHERE name='worktree_verify_leases') BEGIN SELECT RAISE(ABORT,'stale verification rollback witness'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	assertFailureKind(t, err, KindUnavailable)
	if !strings.Contains(err.Error(), "stale verification rollback witness") {
		t.Fatalf("upgrade missed the post-drop failure: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed upgrade changed stale leases or schema-168 manifest")
	}
	retirementUpgradeExec(t, db, `DROP TRIGGER verification_retirement_failure`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
}

func TestVerificationRetirementOwnerObservation(t *testing.T) {
	for _, tc := range []struct {
		name, lifecycle, path, started string
		pid                            int
		observed                       uint64
		processErr, statErr            error
		selfErr                        error
		// recovery is a substring of the refusal's recovery; empty means
		// the lease is retirable.
		recovery string
	}{
		{name: "dead-owner", lifecycle: "completed", pid: 123, started: "42", processErr: fmt.Errorf("wrapped: %w", os.ErrNotExist)},
		{name: "reused-pid", lifecycle: "cancelled", pid: 123, started: "42", observed: 43},
		{name: "live-owner", lifecycle: "superseded", pid: 123, started: "42", observed: 42, recovery: "wait for verification owner pid 123"},
		{name: "live-nonterminal-owner", lifecycle: "in_progress", pid: 123, started: "42", observed: 42, recovery: "wait for verification owner pid 123"},
		{name: "dead-nonterminal-owner", lifecycle: "in_progress", pid: 123, started: "42", processErr: os.ErrNotExist, recovery: "complete or cancel"},
		{name: "legacy-nonterminal", lifecycle: "needed", path: "/synthetic/removed", statErr: os.ErrNotExist, recovery: "complete or cancel"},
		{name: "unreadable-owner", lifecycle: "completed", pid: 123, started: "42", processErr: os.ErrPermission, recovery: "/proc entry readable"},
		{name: "malformed-proc", lifecycle: "completed", pid: 123, started: "42", processErr: errors.New("malformed stat"), recovery: "/proc entry readable"},
		{name: "missing-proc-mount", lifecycle: "completed", pid: 123, started: "42", processErr: os.ErrNotExist, selfErr: os.ErrNotExist, recovery: "with /proc readable"},
		{name: "legacy-missing", lifecycle: "superseded", path: "/synthetic/removed", statErr: os.ErrNotExist},
		{name: "legacy-parent-file", lifecycle: "completed", path: "/synthetic/parent-file/removed", statErr: syscall.ENOTDIR},
		{name: "legacy-unreadable", lifecycle: "completed", path: "/synthetic/removed", statErr: os.ErrPermission, recovery: "worktree path readable"},
		{name: "legacy-relative", lifecycle: "completed", path: "removed", statErr: os.ErrNotExist, recovery: "unused worktree"},
		{name: "legacy-existing", lifecycle: "completed", path: "/synthetic/existing", recovery: "unused worktree"},
		{name: "partial-start", lifecycle: "completed", started: "42", recovery: "maintainer"},
		{name: "partial-nonterminal", lifecycle: "in_progress", pid: 123, recovery: "maintainer"},
		{name: "malformed-start", lifecycle: "completed", pid: 123, started: "not-a-number", recovery: "maintainer"},
		{name: "zero-start", lifecycle: "completed", pid: 123, started: "0", recovery: "maintainer"},
		{name: "missing-work", pid: 123, started: "42", processErr: os.ErrNotExist, recovery: "complete or cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := func(pid int) (uint64, error) {
				if pid == os.Getpid() {
					return 1, tc.selfErr
				}
				return tc.observed, tc.processErr
			}
			stat := func(string) (os.FileInfo, error) { return nil, tc.statErr }
			got := verificationRetirementRecovery(tc.lifecycle, tc.path, tc.pid, tc.started, probe, stat)
			if (got == "") != (tc.recovery == "") || !strings.Contains(got, tc.recovery) {
				t.Fatalf("recovery = %q, want %q", got, tc.recovery)
			}
		})
	}
}

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
	assertWorktreeTablesAbsent(t, db)
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
