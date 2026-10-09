package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func oracleSubjectFromResult(t *testing.T, result WorktreeVerifyResult) string {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	subject, _ := fields["subject_ref"].(string)
	return subject
}

func oracleRealVerifyRequest(lease string, run func(context.Context, string, []string, int) (int, []byte, bool, error)) WorktreeVerifyRequest {
	req := verifyRequest(nil, lease, []string{"true"}, run)
	req.Runner = ExecGitRunner{}
	return req
}

type oracleSubjectProbeRunner struct {
	GitRunner
	probe func(context.Context, string, []string) error
}

func (r oracleSubjectProbeRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if err := r.probe(ctx, path, args); err != nil {
		return nil, err
	}
	return r.GitRunner.Run(ctx, path, args...)
}

func TestOwnerOracleSubjectVerifyCleanCommitAndReplay(t *testing.T) {
	s, path := realGitTiersFixture(t)
	ctx := context.Background()
	head, err := (ExecGitRunner{}).Run(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	want := "commit:" + strings.TrimSpace(string(head))
	runs := 0
	req := oracleRealVerifyRequest("oracle-clean", func(ctx context.Context, _ string, _ []string, _ int) (int, []byte, bool, error) {
		runs++
		// The command can obtain the store's sole connection: no transaction
		// spans the native execution.
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM worktree_verify_leases WHERE state='held'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("held lease=%d err=%v", count, err)
		}
		return 0, []byte(strings.Repeat("x", 17000)), false, nil
	})
	probes := 0
	req.Runner = oracleSubjectProbeRunner{GitRunner: ExecGitRunner{}, probe: func(ctx context.Context, _ string, _ []string) error {
		probeCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var count int
		if err := s.db.QueryRowContext(probeCtx, `SELECT count(*) FROM worktree_verify_leases`).Scan(&count); err != nil {
			return err
		}
		probes++
		return nil
	}}
	result, err := s.VerifyWorktree(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := oracleSubjectFromResult(t, result); got != want {
		t.Fatalf("subject=%q want %q", got, want)
	}
	if probes != 9 {
		t.Fatalf("native probes=%d, want branch plus paired tracked/clean HEAD observations", probes)
	}
	if !result.OutputTruncated || len(result.Output) != 16384 {
		t.Fatalf("output bytes=%d truncated=%v", len(result.Output), result.OutputTruncated)
	}
	var leaseJSON, operationJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT l.result_json,d.result_payload FROM worktree_verify_leases l JOIN durable_operations d ON d.op_id=? WHERE l.lease_id=?`, result.OperationRef, result.LeaseID).Scan(&leaseJSON, &operationJSON); err != nil {
		t.Fatal(err)
	}
	if leaseJSON != operationJSON || !strings.Contains(operationJSON, want) {
		t.Fatal("the lease and durable producer do not retain the same subject")
	}
	// A replay reports the recorded subject, never the later HEAD.
	gitRunStore(t, path, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle Test", "commit", "--allow-empty", "-m", "later")
	replayed, err := s.VerifyWorktree(ctx, req)
	if err != nil || oracleSubjectFromResult(t, replayed) != want || runs != 1 {
		t.Fatalf("replay=%+v runs=%d err=%v", replayed, runs, err)
	}
}

func TestOwnerOracleSubjectVerifyDirtyHasNoCommit(t *testing.T) {
	for _, file := range []string{"tracked.txt", "untracked.txt"} {
		t.Run(file, func(t *testing.T) {
			s, path := realGitTiersFixture(t)
			if err := os.WriteFile(filepath.Join(path, file), []byte("dirty\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := s.VerifyWorktree(context.Background(), oracleRealVerifyRequest("oracle-dirty", nil))
			if err != nil || result.ExitCode != 0 || result.TrackedFilesChanged {
				t.Fatalf("legacy dirty verify=%+v err=%v", result, err)
			}
			if got := oracleSubjectFromResult(t, result); got != "" {
				t.Fatalf("dirty verify emitted %q", got)
			}
		})
	}
}

func TestOwnerOracleSubjectVerifyHeadMovesRefuses(t *testing.T) {
	s, _ := realGitTiersFixture(t)
	result, err := s.VerifyWorktree(context.Background(), oracleRealVerifyRequest("oracle-moved", func(_ context.Context, path string, _ []string, _ int) (int, []byte, bool, error) {
		gitRunStore(t, path, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle Test", "commit", "--allow-empty", "-m", "moved")
		return 0, nil, false, nil
	}))
	if failureKind(err) != KindWorktreeVerifyMutated || !result.TrackedFilesChanged || oracleSubjectFromResult(t, result) != "" {
		t.Fatalf("moved subject=%+v err=%v", result, err)
	}
}

func TestOwnerOracleSubjectVerifyMockClean(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	result, err := s.VerifyWorktree(context.Background(), verifyRequest(git, "oracle-mock", []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) {
		return 0, nil, false, nil
	}))
	if err != nil || oracleSubjectFromResult(t, result) != "commit:"+strings.Repeat("a", 40) {
		t.Fatalf("mock subject=%+v err=%v", result, err)
	}
}

func TestOwnerOracleSubjectCurrentReceiptJoin(t *testing.T) {
	s, path := realGitTiersFixture(t)
	ctx := context.Background()
	if subject, err := readCurrentOracleSubject(ctx, s.db, "work-w"); err != nil || subject != "" {
		t.Fatalf("claim alone qualified %q err=%v", subject, err)
	}
	first, err := s.VerifyWorktree(ctx, oracleRealVerifyRequest("oracle-first", nil))
	if err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, path, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle Test", "commit", "--allow-empty", "-m", "candidate")
	second, err := s.VerifyWorktree(ctx, oracleRealVerifyRequest("oracle-second", nil))
	if err != nil || first.SubjectRef == second.SubjectRef {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	ctx, err = s.EstablishWorkContextNavigationProof(ctx, WorkContextNavigationRequest{WorkIDs: []string{"work-w"}})
	if err != nil {
		t.Fatal(err)
	}
	// WorkContext and the receipt join work with the caller's transaction and
	// no native probes. The claim's cached HEAD is the earlier first subject.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	view, err := readWorkContextView(ctx, tx, "work-w")
	if err != nil || view == nil || view.CandidateSubject != second.SubjectRef {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	for _, check := range []struct{ work, run, subject string }{
		{"work-w", first.OperationRef, first.SubjectRef},
		{"work-w", second.OperationRef, second.SubjectRef},
		{"work-other", second.OperationRef, ""},
		{"work-w", "run:model-claim", ""},
	} {
		receipt, err := readOracleVerifySubjectReceipt(ctx, tx, check.work, check.run)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if receipt != nil {
			got = receipt.SubjectRef
		}
		if got != check.subject {
			t.Fatalf("receipt %s/%s=%q want %q", check.work, check.run, got, check.subject)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// A model report cannot fill the producer join. A newer unjoined or
	// subject-less legacy receipt also cannot displace the qualified run.
	if _, err := s.db.ExecContext(ctx, `UPDATE durable_operations SET result_payload='{}' WHERE op_id=?`, second.OperationRef); err != nil {
		t.Fatal(err)
	}
	if got, err := readCurrentOracleSubject(ctx, s.db, "work-w"); err != nil || got != first.SubjectRef {
		t.Fatalf("unjoined current=%q err=%v", got, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE worktree_verify_leases SET result_json=json_remove(result_json,'$.subject_ref') WHERE lease_id=?`, first.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE durable_operations SET result_payload=(SELECT result_json FROM worktree_verify_leases WHERE lease_id=?) WHERE op_id=?`, first.LeaseID, first.OperationRef); err != nil {
		t.Fatal(err)
	}
	if got, err := readCurrentOracleSubject(ctx, s.db, "work-w"); err != nil || got != "" {
		t.Fatalf("legacy receipt current=%q err=%v", got, err)
	}
}

func TestOwnerOracleSubjectCurrentExcludesFailedDirtyAndReclaimed(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)
	ctx := context.Background()
	run := func(code int) func(context.Context, string, []string, int) (int, []byte, bool, error) {
		return func(context.Context, string, []string, int) (int, []byte, bool, error) { return code, nil, false, nil }
	}
	if _, err := s.VerifyWorktree(ctx, verifyRequest(git, "oracle-failed", []string{"true"}, run(1))); err != nil {
		t.Fatal(err)
	}
	if got, err := readCurrentOracleSubject(ctx, s.db, "work-w"); err != nil || got != "" {
		t.Fatalf("failed current=%q err=%v", got, err)
	}
	git.dirty[entry.Path] = true
	if _, err := s.VerifyWorktree(ctx, verifyRequest(git, "oracle-dirty", []string{"true"}, run(0))); err != nil {
		t.Fatal(err)
	}
	if got, err := readCurrentOracleSubject(ctx, s.db, "work-w"); err != nil || got != "" {
		t.Fatalf("dirty current=%q err=%v", got, err)
	}
	git.dirty[entry.Path] = false
	clean, err := s.VerifyWorktree(ctx, verifyRequest(git, "oracle-clean", []string{"true"}, run(0)))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readCurrentOracleSubject(ctx, s.db, "work-w"); err != nil || got != clean.SubjectRef {
		t.Fatalf("clean current=%q err=%v", got, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE worktree_entries SET state='reclaimed' WHERE path=?`, entry.Path); err != nil {
		t.Fatal(err)
	}
	if got, err := readCurrentOracleSubject(ctx, s.db, "work-w"); err != nil || got != "" {
		t.Fatalf("reclaimed current=%q err=%v", got, err)
	}
}

func TestOwnerOracleSubjectProbeFailureNeverQualifies(t *testing.T) {
	for _, failAfterRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[failAfterRun], func(t *testing.T) {
			s, git, _ := worktreeFixture(t)
			claimFixtureWorktree(t, s, git)
			ran := false
			req := verifyRequest(git, "oracle-probe-failure", []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) {
				ran = true
				return 0, nil, false, nil
			})
			req.Runner = oracleSubjectProbeRunner{GitRunner: git, probe: func(_ context.Context, _ string, args []string) error {
				if strings.Join(args, " ") == "status --porcelain=v1 -z --untracked-files=all" && ran == failAfterRun {
					return errors.New("synthetic clean-status probe failure")
				}
				return nil
			}}
			result, err := s.VerifyWorktree(context.Background(), req)
			if failureKind(err) != KindGitUnreachable || result.SubjectRef != "" || ran != failAfterRun {
				t.Fatalf("result=%+v ran=%v err=%v", result, ran, err)
			}
			var held, producers int
			if err := s.db.QueryRow(`SELECT count(*) FROM worktree_verify_leases WHERE state='held'`).Scan(&held); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE workflow_type_ref='worktree.verify'`).Scan(&producers); err != nil {
				t.Fatal(err)
			}
			if held != 0 || producers != 0 {
				t.Fatalf("probe failure retained held=%d producers=%d", held, producers)
			}
		})
	}
}

func TestOwnerOracleSubjectProductionLeaseReceiptIdentity(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	leaseID := strings.Repeat("b", 64) + ":worktree-verify:work-w"
	result, err := s.VerifyWorktree(context.Background(), verifyRequest(git, leaseID, []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) {
		return 0, nil, false, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := readOracleVerifySubjectReceipt(context.Background(), s.db, "work-w", result.OperationRef)
	if err != nil || receipt == nil || receipt.SubjectRef != result.SubjectRef || receipt.LeaseID != leaseID {
		t.Fatalf("production receipt=%+v err=%v", receipt, err)
	}
}
