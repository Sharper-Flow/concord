package store

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/sharper-flow/concord/internal/hostlease"
)

func TestClaimLandingRefreshesRestartedHostIdentity(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	if _, err := s.ClaimWorktree(context.Background(), baseClaim(git)); err != nil {
		t.Fatal(err)
	}
	oldHost := exec.Command("sleep", "60")
	if err := oldHost.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = oldHost.Process.Kill(); _ = oldHost.Wait() })
	request := WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-resume", LandedDirectory: claimPath(s), HostPID: oldHost.Process.Pid}
	if _, err := s.RecordWorktreeClaimLanding(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_ = oldHost.Process.Kill()
	_ = oldHost.Wait()
	request.HostPID = os.Getpid()
	landed, err := s.RecordWorktreeClaimLanding(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if landed.AlreadyRecorded {
		t.Fatal("replacement host landing replayed the ended host identity")
	}
	var events int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id='work-w' AND kind='work.session_claim_landed'`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("landing events = %d, error = %v; want two recorded identities", events, err)
	}
	check := func() {
		t.Helper()
		var pid int
		var start uint64
		if err := s.db.QueryRow(`SELECT host_pid,host_pid_start FROM worktree_occupancy WHERE session_ref=?`, request.SessionRef).Scan(&pid, &start); err != nil {
			t.Fatal(err)
		}
		wantStart, err := hostlease.ProcessStart(os.Getpid())
		if err != nil || pid != os.Getpid() || start != wantStart {
			t.Fatalf("occupancy pid/start = %d/%d, want %d/%d; error = %v", pid, start, os.Getpid(), wantStart, err)
		}
	}
	check()
	replay, err := s.RecordWorktreeClaimLanding(context.Background(), request)
	if err != nil || !replay.AlreadyRecorded {
		t.Fatalf("same-process replay = %+v, error = %v", replay, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id='work-w' AND kind='work.session_claim_landed'`).Scan(&events); err != nil || events != 2 {
		t.Fatalf("same-process replay recorded another event: count=%d error=%v", events, err)
	}
}
