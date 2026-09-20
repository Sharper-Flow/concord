package store

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// CON-317 recovery coverage: a verify lease outlives no run. A window whose
// context cancels releases its lease as 'aborted' on a fresh bounded
// context, an aborted run binds no verification authority, and a held lease
// whose recorded owner process is provably gone reclaims while a live owner
// keeps the typed refusal.

func TestVerifyLeaseReleasedWhenContextCancels(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := s.VerifyWorktree(ctx, verifyRequest(git, "lease-cancel", []string{"go", "test", "./..."}, func(runCtx context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		cancel()
		<-runCtx.Done()
		return 0, nil, false, runCtx.Err()
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want the caller's cancellation", err)
	}
	var state, outcome string
	if err := s.db.QueryRow(`SELECT state,outcome FROM worktree_verify_leases WHERE lease_id='lease-cancel'`).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "released" || outcome != "aborted" {
		t.Fatalf("lease state=%q outcome=%q, want released/aborted after cancellation", state, outcome)
	}
	// The released-aborted lease blocks nothing: the worktree verifies again
	// under a new lease id.
	result, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-after", []string{"go", "test", "./..."}, func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		return 0, []byte("ok"), false, nil
	}))
	if err != nil {
		t.Fatalf("verify after cancellation: %v", err)
	}
	if result.LeaseID != "lease-after" || result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestAbortedVerifyRecordsNoAuthority(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)

	ctx, cancel := context.WithCancel(context.Background())
	_, err := s.VerifyWorktree(ctx, verifyRequest(git, "lease-abort", []string{"go", "test", "./..."}, func(runCtx context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		cancel()
		<-runCtx.Done()
		return 0, nil, false, runCtx.Err()
	}))
	if err == nil {
		t.Fatal("cancelled run returned no error")
	}
	// An aborted run never stood as verification authority: the release
	// path never reached recordWorktreeVerifyAuthorityTx.
	var authority int
	if err := s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE workflow_type_ref='worktree.verify'`).Scan(&authority); err != nil {
		t.Fatal(err)
	}
	if authority != 0 {
		t.Fatalf("durable_operations carries %d worktree.verify rows after an aborted run, want none", authority)
	}
	// The spent lease id cannot replay as a pass: a retry refuses and runs
	// nothing.
	ran := false
	if _, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-abort", []string{"go", "test", "./..."}, func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		ran = true
		return 0, nil, false, nil
	})); failureKind(err) != KindInvalidOperation {
		t.Fatalf("err=%v, want the abandoned-lease refusal", err)
	}
	if ran {
		t.Fatal("the abandoned lease replayed the command")
	}
}

func TestVerifyLeaseReclaimedWhenOwnerProcessGone(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	// The recorded owner comes from a real child that is started and reaped,
	// so its pid is gone and cannot come back during the test. Even a pid
	// reuse cannot resurrect the lease: the recorded start time '17' matches
	// no live process's procfs start time.
	dead := exec.Command("true")
	if err := dead.Start(); err != nil {
		t.Fatal(err)
	}
	deadPID := dead.Process.Pid
	_ = dead.Wait()

	stamp := time.Unix(1, 0).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome,owner_pid,owner_started) VALUES('lease-dead','work-w','project-w',?, 'held','client-0','agent-0','session-0','principal-0','["go","test","./..."]',?, 'running', ?, '17')`, entry.Path, stamp, deadPID); err != nil {
		t.Fatal(err)
	}

	ran := false
	result, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-fresh", []string{"go", "test", "./..."}, func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		ran = true
		return 0, []byte("ok"), false, nil
	}))
	if err != nil {
		t.Fatalf("verify against a dead holder: %v", err)
	}
	if !ran || result.ExitCode != 0 {
		t.Fatalf("reclaimed run did not execute: ran=%v result=%+v", ran, result)
	}
	var state, outcome string
	if err := s.db.QueryRow(`SELECT state,outcome FROM worktree_verify_leases WHERE lease_id='lease-dead'`).Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != "released" || outcome != "aborted" {
		t.Fatalf("reclaimed lease state=%q outcome=%q, want released/aborted", state, outcome)
	}
}

func TestVerifyLeaseRefusesWhileOwnerProcessLives(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)

	// This process is alive, so its observed identity proves the recorded
	// holder lives: the acquire keeps the typed refusal and runs nothing.
	live := currentProcessIdentity()
	if live.started == "" {
		t.Skip("procfs start time unobservable on this host")
	}
	stamp := time.Unix(1, 0).UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome,owner_pid,owner_started) VALUES('lease-live','work-w','project-w',?, 'held','client-0','agent-0','session-0','principal-0','["go","test","./..."]',?, 'running', ?, ?)`, entry.Path, stamp, live.pid, live.started); err != nil {
		t.Fatal(err)
	}

	ran := false
	_, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-other", []string{"go", "test", "./..."}, func(_ context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		ran = true
		return 0, nil, false, nil
	}))
	if failureKind(err) != KindWorktreeLeaseHeld {
		t.Fatalf("err=%v, want worktree_lease_held while the holder process lives", err)
	}
	if ran {
		t.Fatal("the contender ran under a live holder's lease")
	}
	var held int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE lease_id='lease-live' AND state='held'`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Fatalf("live holder's lease rows held=%d, want the lease untouched", held)
	}
}

func TestMigration95PreservesVerifyLeasesAndTheirRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-v94.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:len(migrations)-1] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-09-20T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, path: path}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-w"), locatorProjectEvent("project-w"), locatorMembershipEvent("product-w", "project-w")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-w"): 0, VersionRef(SubjectProject, "project-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "migration95-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Migration","priority":1}`)},
		{EventID: "migration95-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-w"): 0}}); err != nil {
		t.Fatal(err)
	}
	// Rows written at the v94 vocabulary: a held lease with no recorded
	// outcome and a finished one with its result.
	stamp := time.Unix(5, 0).UTC().Format(time.RFC3339Nano)
	if _, err := db.ExecContext(ctx, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES('lease-old-held','work-w','project-w','/repo/wt','held','client-9','agent-9','session-9','principal-9','["old"]',?, 'running')`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,exit_code,outcome,result_json) VALUES('lease-old-done','work-w','project-w','/repo/wt','released','client-9','agent-9','session-9','principal-9','["old"]',?,?,-1, 'completed','{"exit_code":-1}')`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var heldState, heldOutcome string
	var heldPID, donePID int
	var heldStarted string
	if err := db.QueryRow(`SELECT state,outcome,owner_pid,owner_started FROM worktree_verify_leases WHERE lease_id='lease-old-held'`).Scan(&heldState, &heldOutcome, &heldPID, &heldStarted); err != nil {
		t.Fatal(err)
	}
	if heldState != "held" || heldOutcome != "running" {
		t.Fatalf("held row state=%q outcome=%q, want held/running through the rebuild", heldState, heldOutcome)
	}
	var doneState, doneOutcome string
	if err := db.QueryRow(`SELECT state,outcome,owner_pid FROM worktree_verify_leases WHERE lease_id='lease-old-done'`).Scan(&doneState, &doneOutcome, &donePID); err != nil {
		t.Fatal(err)
	}
	if doneState != "released" || doneOutcome != "completed" {
		t.Fatalf("done row state=%q outcome=%q, want released/completed through the rebuild", doneState, doneOutcome)
	}
	// Migrated rows carry no recorded identity, and no identity reads as
	// alive: every refusal the database carried into the migration stays in
	// force until its owner process is provably gone.
	if heldPID != 0 || heldStarted != "" || donePID != 0 {
		t.Fatalf("migrated owner identity pid=%d/%d started=%q, want the zero identity", heldPID, donePID, heldStarted)
	}
	if verifyOwnerProcessGone(verifyLeaseOwnerProcess{pid: heldPID, started: heldStarted}) {
		t.Fatal("the zero identity read as dead; pre-migration refusals would not stay in force")
	}
}
