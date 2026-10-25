package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

type claimFenceGit struct {
	before, after func(context.Context, string, []string) error
}

func (g claimFenceGit) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if g.before != nil {
		if err := g.before(ctx, dir, args); err != nil {
			return nil, err
		}
	}
	out, err := (ExecGitRunner{}).Run(ctx, dir, args...)
	if err == nil && g.after != nil {
		err = g.after(ctx, dir, args)
	}
	return out, err
}

func TestClaimNativeFenceProcess(t *testing.T) {
	stage := os.Getenv("TEST_CONCORD_NATIVE_FENCE_STAGE")
	if stage == "" {
		t.Skip("owned compensation subprocess")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	s, err := Open(ctx, os.Getenv("TEST_CONCORD_NATIVE_FENCE_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var req WorktreeClaimRequest
	if err := json.Unmarshal([]byte(os.Getenv("TEST_CONCORD_NATIVE_FENCE_REQUEST")), &req); err != nil {
		t.Fatal(err)
	}
	_, created, err := s.PrepareWorktreeClaimNative(ctx, req)
	if err != nil || created == nil {
		t.Fatalf("native creation = %+v error=%v", created, err)
	}
	barrier := func(_ context.Context, _ string, args []string) error {
		if len(args) < 2 || args[0] != "worktree" || args[1] != "remove" {
			return nil
		}
		if _, err := fmt.Fprintln(os.Stdout, "NATIVE_COMPENSATION_RESERVED"); err != nil {
			return err
		}
		_, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return err
	}
	runner := claimFenceGit{}
	switch stage {
	case "before-remove":
		runner.before = barrier
	case "after-remove":
		runner.after = barrier
	default:
		t.Fatalf("unknown crash stage %q", stage)
	}
	cause := errors.New("original claim failed")
	t.Fatal(s.CompensateWorktreeClaimCreation(ctx, runner, *created, cause))
}

func TestClaimNativeFenceCrashRecovery(t *testing.T) {
	for _, stage := range []string{"before-remove", "after-remove"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			s, repo, req := overlapClaimFixture(t)
			raw, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClaimNativeFenceProcess$")
			cmd.Env = append(os.Environ(), "TEST_CONCORD_NATIVE_FENCE_STAGE="+stage, "TEST_CONCORD_NATIVE_FENCE_DB="+s.Path(), "TEST_CONCORD_NATIVE_FENCE_REQUEST="+string(raw))
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				_ = input.Close()
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				_ = input.Close()
				t.Fatal(err)
			}
			done := make(chan error, 1)
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(output)
				if scanner.Scan() {
					ready <- scanner.Text()
				} else {
					ready <- ""
				}
				for scanner.Scan() {
				}
				done <- cmd.Wait()
				close(done)
			}()
			t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); <-done })
			select {
			case line := <-ready:
				if line != "NATIVE_COMPENSATION_RESERVED" {
					t.Fatalf("child barrier = %q", line)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var pending int
			if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims WHERE state='pending'`).Scan(&pending); err != nil || pending != 1 {
				t.Fatalf("compensation reservation count=%d error=%v", pending, err)
			}
			// Same-operation recovery cannot enter a live executor, and a
			// foreign operation cannot adopt its reserved tree or branch.
			for _, same := range []bool{true, false} {
				probe := req
				if !same {
					probe.OpID = "foreign-recovery"
				}
				_, _, err := s.PrepareWorktreeClaimNative(ctx, probe)
				assertFailureKind(t, err, KindProjectionConflict)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatalf("compensation child exit=%v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var deletes int
			req.Runner = claimFenceGit{before: func(_ context.Context, _ string, args []string) error {
				if len(args) > 0 && args[0] == "update-ref" {
					deletes++
				}
				return nil
			}}
			_, created, err := s.PrepareWorktreeClaimNative(ctx, req)
			if err != nil || created == nil || created.CreatedBranch || deletes != 0 {
				t.Fatalf("exact recovery invented branch deletion/creation authority: created=%+v deletes=%d error=%v", created, deletes, err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("recovery stranded reservation count=%d error=%v", pending, err)
			}
			if got := overlapClaimGit(t, repo, "rev-parse", "refs/heads/"+created.Branch); got != req.BaseSHA {
				t.Fatalf("recovery changed preserved branch tip=%s", got)
			}
			winner, err := s.ClaimWorktree(ctx, req)
			if err != nil || winner.Created != nil {
				t.Fatalf("claim after exact recovery: %+v error=%v", winner, err)
			}
			assertOverlapClaimWinner(t, s, repo, winner.Entry)
		})
	}
}

// The reservation row cannot fence Git already running in a live executor: a
// same-operation recovery that removed and released the reservation would let
// a winner commit while the stale executor's removal is still pending.
func TestClaimNativeFenceLiveExecutorPreservesLaterWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	s, repo, req := overlapClaimFixture(t)
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestClaimNativeFenceProcess$")
	cmd.Env = append(os.Environ(), "TEST_CONCORD_NATIVE_FENCE_STAGE=before-remove", "TEST_CONCORD_NATIVE_FENCE_DB="+s.Path(), "TEST_CONCORD_NATIVE_FENCE_REQUEST="+string(raw))
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "NATIVE_COMPENSATION_RESERVED" {
		t.Fatalf("child barrier = %q error=%v", scanner.Text(), scanner.Err())
	}
	winner, during := s.ClaimWorktree(ctx, req)
	if during != nil {
		assertFailureKind(t, during, KindProjectionConflict)
	}
	if _, err := fmt.Fprintln(input, "RESUME"); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
	}
	_ = cmd.Wait()
	if during != nil {
		if winner, err = s.ClaimWorktree(ctx, req); err != nil {
			t.Fatalf("claim after the live executor finished: %v", err)
		}
	}
	assertOverlapClaimWinner(t, s, repo, winner.Entry)
}

func TestClaimNativeFenceEachGeneration(t *testing.T) {
	for _, resource := range []string{"path", "branch"} {
		t.Run(resource, func(t *testing.T) {
			s, _, req := overlapClaimFixture(t)
			ctx := t.Context()
			native, created, err := s.PrepareWorktreeClaimNative(ctx, req)
			if err != nil || created == nil {
				t.Fatalf("prepare=%+v error=%v", created, err)
			}
			cause := errors.New("failed claim")
			if err := s.CompensateWorktreeClaimCreation(ctx, nil, *created, cause); err != cause {
				t.Fatal(err)
			}
			current, err := readClaimNativeGeneration(ctx, s.db, native.Location)
			if err != nil || current.path != 1 || current.branch != 1 {
				t.Fatalf("compensation generations=%+v error=%v", current, err)
			}
			// Keep just one leg stale to exercise each identity independently.
			if resource == "path" {
				native.generation.branch = current.branch
			} else {
				native.generation.path = current.path
			}
			before := overlapClaimSnapshot(t, s)
			err = s.TransactDurable(ctx, func(tx *Transaction) error {
				_, err := ClaimWorktreeTx(ctx, tx, req, native)
				return err
			})
			assertFailureKind(t, err, KindStaleContext)
			if after := overlapClaimSnapshot(t, s); !reflect.DeepEqual(before, after) {
				t.Fatalf("stale %s generation published owner state", resource)
			}
			if _, err := s.ClaimWorktree(ctx, req); err != nil {
				t.Fatalf("fresh preparation cannot retry: %v", err)
			}
		})
	}
}

func TestClaimNativeFenceExecutorIsPerReservation(t *testing.T) {
	s, _, req := overlapClaimFixture(t)
	ctx := t.Context()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "other-work-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-other", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: jsonRaw(`{"work_kind":"task","title":"Other Work","priority":1}`)},
		{EventID: "other-work-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-other", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-w","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-other"): 0}}); err != nil {
		t.Fatal(err)
	}
	_, first, err := s.PrepareWorktreeClaimNative(ctx, req)
	if err != nil || first == nil {
		t.Fatalf("first prepare=%+v error=%v", first, err)
	}
	other := req
	other.WorkID, other.OpID, other.RequestID = "work-other", "other-op", "other-request"
	_, second, err := s.PrepareWorktreeClaimNative(ctx, other)
	if err != nil || second == nil {
		t.Fatalf("second prepare=%+v error=%v", second, err)
	}
	var visited bool
	runner := claimFenceGit{before: func(_ context.Context, _ string, args []string) error {
		if len(args) < 2 || args[0] != "worktree" || args[1] != "remove" {
			return nil
		}
		visited = true
		var pid int
		var start uint64
		if err := s.db.QueryRow(`SELECT executor_pid,executor_pid_start FROM worktree_claims WHERE op_id=?`, compensationClaimID(*first)).Scan(&pid, &start); err != nil {
			t.Fatal(err)
		}
		wantStart, err := hostlease.ProcessStart(os.Getpid())
		if err != nil || pid != os.Getpid() || start != wantStart {
			t.Fatalf("executor identity=%d/%d want=%d/%d error=%v", pid, start, os.Getpid(), wantStart, err)
		}
		// Another goroutine in this process must not be treated as recovery.
		cause := newFailure(KindVersionConflict, "worktree_claim", "concurrent same-process retry", false, "retry")
		if err := s.CompensateWorktreeClaimCreation(ctx, nil, *first, cause); err != cause || !cause.EffectPossible || !strings.Contains(cause.Detail, "still executing") {
			t.Fatalf("same-process executor entered live reservation: %v", err)
		}
		cause = newFailure(KindVersionConflict, "worktree_claim", "unrelated compensation", false, "retry")
		if err := s.CompensateWorktreeClaimCreation(ctx, nil, *second, cause); err != cause || cause.EffectPossible {
			t.Fatalf("unrelated reservation was blocked: %v", err)
		}
		if _, err := os.Stat(second.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unrelated compensation did not remove its tree: %v", err)
		}
		return nil
	}}
	cause := errors.New("first claim failed")
	if err := s.CompensateWorktreeClaimCreation(ctx, runner, *first, cause); err != cause || !visited {
		t.Fatalf("first compensation=%v visited=%t", err, visited)
	}
}

func TestClaimNativeFenceFailuresPreserveCreation(t *testing.T) {
	for _, failure := range []string{"ownership-read", "generation-read", "generation-write"} {
		t.Run(failure, func(t *testing.T) {
			s, repo, req := overlapClaimFixture(t)
			_, created, err := s.PrepareWorktreeClaimNative(t.Context(), req)
			if err != nil || created == nil {
				t.Fatalf("prepare=%+v error=%v", created, err)
			}
			query := map[string]string{
				"ownership-read":   `DROP TABLE worktree_entries`,
				"generation-read":  `DROP TABLE worktree_claim_native_fences`,
				"generation-write": `CREATE TRIGGER native_fence_refuse BEFORE INSERT ON worktree_claim_native_fences BEGIN SELECT RAISE(ABORT,'synthetic native fence write failure'); END`,
			}[failure]
			if _, err := s.db.Exec(query); err != nil {
				t.Fatal(err)
			}
			cause := newFailure(KindVersionConflict, "worktree_claim", "original failure", false, "retry")
			if err := s.CompensateWorktreeClaimCreation(t.Context(), nil, *created, cause); err != cause || !cause.EffectPossible {
				t.Fatalf("unreadable ownership/fence reports no effect: error=%v cause=%+v", err, cause)
			}
			if got := overlapClaimGit(t, created.Path, "rev-parse", "HEAD"); got != req.BaseSHA {
				t.Fatalf("failed fence removed native tree: %s", got)
			}
			if got := overlapClaimGit(t, repo, "rev-parse", "refs/heads/"+created.Branch); got != req.BaseSHA {
				t.Fatalf("failed fence removed native branch: %s", got)
			}
			var reservations int
			if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&reservations); err != nil || reservations != 0 {
				t.Fatalf("failed reservation partially published rows=%d error=%v", reservations, err)
			}
		})
	}
}

func TestClaimNativeFenceIncompleteCleanupRetriesExactly(t *testing.T) {
	s, repo, req := overlapClaimFixture(t)
	_, created, err := s.PrepareWorktreeClaimNative(t.Context(), req)
	if err != nil || created == nil {
		t.Fatalf("prepare=%+v error=%v", created, err)
	}
	runner := claimFenceGit{after: func(_ context.Context, _ string, args []string) error {
		if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
			return errors.New("synthetic response loss after actual removal")
		}
		return nil
	}}
	cause := newFailure(KindVersionConflict, "worktree_claim", "original failure", false, "retry")
	if err := s.CompensateWorktreeClaimCreation(t.Context(), runner, *created, cause); err != cause || !cause.EffectPossible {
		t.Fatalf("incomplete removal=%v cause=%+v", err, cause)
	}
	var reserved int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims WHERE state='pending' AND executor_pid=0 AND executor_pid_start=0`).Scan(&reserved); err != nil || reserved != 1 {
		t.Fatalf("incomplete cleanup lost reservation=%d error=%v", reserved, err)
	}
	if err := s.CompensateWorktreeClaimCreation(t.Context(), nil, *created, errors.New("exact cleanup retry")); err == nil || err.Error() != "exact cleanup retry" {
		t.Fatalf("exact live-fact cleanup retry=%v", err)
	}
	if _, exists, err := worktreeBranchHead(t.Context(), ExecGitRunner{}, repo, created.Branch); err != nil || exists {
		t.Fatalf("exact creation facts did not settle owned branch: exists=%t error=%v", exists, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("exact retry stranded reservation=%d error=%v", reserved, err)
	}
}

func TestClaimNativeFenceStaleCleanupCannotRemoveFreshCreation(t *testing.T) {
	s, _, req := overlapClaimFixture(t)
	_, old, err := s.PrepareWorktreeClaimNative(t.Context(), req)
	if err != nil || old == nil {
		t.Fatalf("original prepare=%+v error=%v", old, err)
	}
	cause := errors.New("original failure")
	if err := s.CompensateWorktreeClaimCreation(t.Context(), nil, *old, cause); err != cause {
		t.Fatal(err)
	}
	_, fresh, err := s.PrepareWorktreeClaimNative(t.Context(), req)
	if err != nil || fresh == nil {
		t.Fatalf("fresh prepare=%+v error=%v", fresh, err)
	}
	stale := newFailure(KindVersionConflict, "worktree_claim", "delayed original cleanup", false, "retry")
	if err := s.CompensateWorktreeClaimCreation(t.Context(), nil, *old, stale); err != stale || !stale.EffectPossible || !strings.Contains(stale.Detail, "creation predates a native compensation generation") {
		t.Fatalf("stale cleanup lost its generation refusal: error=%v cause=%+v", err, stale)
	}
	if got := overlapClaimGit(t, fresh.Path, "rev-parse", "HEAD"); got != req.BaseSHA {
		t.Fatalf("delayed cleanup removed the fresh native creation: %s", got)
	}
	cause = errors.New("fresh cleanup")
	if err := s.CompensateWorktreeClaimCreation(t.Context(), nil, *fresh, cause); err != cause {
		t.Fatalf("fresh creation cannot clean up its own generation: %v", err)
	}
}

func TestClaimNativeFenceCleanupLifetimes(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained-defect=%t", retained), func(t *testing.T) {
			s, _ := terminalCleanupFixture(t)
			terminalCleanupSQL(t, s, `INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/native','wts:work-done',1)`)
			if retained {
				terminalCleanupSQL(t, s, `UPDATE work_items SET kind='bug',intent_json='{"defect":{"failure_shape":"native-fence-test","sibling_ids":[]}}' WHERE id='work-done'`)
			}
			if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claim_native_fences`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("terminal cleanup retained native fences=%d error=%v", count, err)
			}
			if retained {
				if err := s.db.QueryRow(`SELECT count(*) FROM work_items WHERE id='work-done'`).Scan(&count); err != nil || count != 1 {
					t.Fatalf("defect classification lost: count=%d error=%v", count, err)
				}
			}
		})
	}
	t.Run("removal", func(t *testing.T) {
		s := openTemp(t)
		seedWork(t, s, "remove-work-1")
		terminalCleanupSQL(t, s, `INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/native','wts:remove-work-1',1)`)
		if _, err := s.RemoveWork(t.Context(), removalTestRequest()); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claim_native_fences`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("removal retained native fences=%d error=%v", count, err)
		}
	})
}

func TestClaimNativeFencePendingReservationBlocksCleanup(t *testing.T) {
	s, _ := terminalCleanupFixture(t)
	terminalCleanupSQL(t, s,
		`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/native','wts:work-done',1)`,
		`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES('claim-compensation:fixture','work-done','proj-web','wts:work-done','/synthetic/repo','work/closed','1111111111111111111111111111111111111111','/synthetic/native','pending','worktree_claim_compensation','original','now','now')`)
	assertFailureKind(t, s.CleanupPublishedTerminalWork(t.Context(), "work-done"), KindResourceBusy)
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM worktree_claim_native_fences`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pending native owner lost fence: count=%d error=%v", count, err)
	}
}

func TestClaimNativeFencePreparationReadFailureMakesNoNativeCreation(t *testing.T) {
	s, repo, req := overlapClaimFixture(t)
	if _, err := s.db.Exec(`DROP TABLE worktree_claim_native_fences`); err != nil {
		t.Fatal(err)
	}
	_, created, err := s.PrepareWorktreeClaimNative(t.Context(), req)
	if failureKind(err) != KindUnavailable || created != nil {
		t.Fatalf("unreadable preparation fence: created=%+v error=%v", created, err)
	}
	if listing := overlapClaimGit(t, repo, "worktree", "list", "--porcelain"); strings.Count(listing, "worktree ") != 1 {
		t.Fatalf("preparation fence failure created native state: %s", listing)
	}
}

func TestClaimNativeFenceNoTransactionAcrossRemoval(t *testing.T) {
	s, _, req := overlapClaimFixture(t)
	_, created, err := s.PrepareWorktreeClaimNative(t.Context(), req)
	if err != nil || created == nil {
		t.Fatalf("prepare=%+v error=%v", created, err)
	}
	checked := false
	rollback := errors.New("observation transaction rolls back")
	runner := claimFenceGit{before: func(ctx context.Context, _ string, args []string) error {
		if len(args) < 2 || args[0] != "worktree" || args[1] != "remove" {
			return nil
		}
		checked = true
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		// The same single-connection pool must be free, and a second
		// immediate writer must be admissible while native Git is paused.
		if err := s.TransactDurable(ctx, func(*Transaction) error { return rollback }); err != rollback {
			return fmt.Errorf("a SQL transaction spans native removal: %w", err)
		}
		return nil
	}}
	cause := errors.New("original failure")
	if err := s.CompensateWorktreeClaimCreation(t.Context(), runner, *created, cause); err != cause || !checked {
		t.Fatalf("native removal transaction observation=%t error=%v", checked, err)
	}
}

func TestClaimNativeFenceMigration154(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "native-fence.db"), 153)
	assertFailureKind(t, migrateAtOpen(t.Context(), db), KindUpgradeRequired)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var floor int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations WHERE breaking=1 AND version<=154`).Scan(&floor); err != nil || floor != 154 {
		t.Fatalf("native fence compatibility floor=%d error=%v", floor, err)
	}
	for _, query := range []string{
		`INSERT INTO worktree_claim_native_fences VALUES('other','','/synthetic/path','wts:fixture',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('path','/synthetic/repo','/synthetic/path','wts:fixture',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('branch','','work/fixture','wts:fixture',1)`,
		`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/path','wts:fixture',0)`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("migration accepted an invalid fence identity/generation: %s", query)
		}
	}
	if _, err := db.Exec(`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/path','wts:fixture',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worktree_claim_native_fences VALUES('path','','/synthetic/path','wts:other',2)`); err == nil {
		t.Fatal("migration admitted two generation owners for one native path")
	}
}
