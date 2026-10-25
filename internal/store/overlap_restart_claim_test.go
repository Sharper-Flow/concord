package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const overlapClaimChildEnv = "TEST_CONCORD_OVERLAP_CLAIM_CHILD"

type overlapClaimMessage struct {
	Stage  string
	PID    int
	Result WorktreeClaimResult
	Kind   FailureKind
	Error  string
}

// The child uses the same native preparation and transaction composition as
// the agent claim envelope. The barrier follows the actual claim writes, not
// an unrelated operation or a simulated SQL outcome.
func TestOverlapRestartClaimProcess(t *testing.T) {
	mode := os.Getenv(overlapClaimChildEnv)
	if mode == "" {
		t.Skip("owned subprocess entry point")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Open(ctx, os.Getenv("TEST_CONCORD_OVERLAP_CLAIM_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var req WorktreeClaimRequest
	if err := json.Unmarshal([]byte(os.Getenv("TEST_CONCORD_OVERLAP_CLAIM_REQUEST")), &req); err != nil {
		t.Fatal(err)
	}
	req.HostPID = os.Getpid()
	emit := func(stage string, result WorktreeClaimResult, err error) {
		t.Helper()
		message := overlapClaimMessage{Stage: stage, PID: os.Getpid(), Result: result, Kind: failureKind(err)}
		if err != nil {
			message.Error = err.Error()
		}
		if err := json.NewEncoder(os.Stdout).Encode(message); err != nil {
			t.Fatal(err)
		}
	}
	gate := func() error {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		if line != "RUN\n" {
			return fmt.Errorf("unexpected claim barrier input %q", line)
		}
		return nil
	}
	if mode == "claim" {
		result, err := s.ClaimWorktree(ctx, req)
		emit("result", result, err)
		return
	}
	native, created, err := s.PrepareWorktreeClaimNative(ctx, req)
	if err != nil || created == nil {
		t.Fatalf("native creation = %+v, error = %v", created, err)
	}
	if mode == "race" {
		emit("native-ready", WorktreeClaimResult{Created: created}, nil)
		if err := gate(); err != nil {
			t.Fatal(err)
		}
	} else if mode != "crash" {
		t.Fatalf("unknown subprocess mode %q", mode)
	}
	var result WorktreeClaimResult
	err = s.TransactDurable(ctx, func(tx *Transaction) error {
		var err error
		result, err = ClaimWorktreeTx(ctx, tx, req, native)
		if err != nil || mode != "crash" {
			return err
		}
		// Inspect through the transaction's private read seam. Calling the
		// pooled Store here would deadlock on its single connection.
		var claims, entries, occupants int
		for query, target := range map[string]*int{
			`SELECT count(*) FROM worktree_claims WHERE op_id=? AND state='verified'`:                                                                   &claims,
			`SELECT count(*) FROM worktree_entries WHERE claim_op_id=? AND state='active'`:                                                              &entries,
			`SELECT count(*) FROM worktree_occupancy WHERE worktree_id=(SELECT set_id||':'||project_id||':'||op_id FROM worktree_claims WHERE op_id=?)`: &occupants,
		} {
			if err := tx.tx.QueryRowContext(ctx, query, req.OpID).Scan(target); err != nil {
				return err
			}
		}
		var version int64
		if err := tx.tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, req.WorkID).Scan(&version); err != nil {
			return err
		}
		if claims != 1 || entries != 1 || occupants != 1 || version != req.ExpectedVersion+1 {
			return fmt.Errorf("precommit claim/entry/occupancy/version = %d/%d/%d/%d", claims, entries, occupants, version)
		}
		emit("precommit", result, nil)
		return gate()
	})
	if err != nil {
		err = s.CompensateWorktreeClaimCreation(ctx, nil, *created, err)
	}
	emit("result", result, err)
}

type overlapClaimProcess struct {
	cmd   *exec.Cmd
	input *os.File
	lines chan string
	done  chan struct{}
	err   error
}

func startOverlapClaimProcess(t *testing.T, ctx context.Context, s *Store, req WorktreeClaimRequest, mode string) *overlapClaimProcess {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOverlapRestartClaimProcess$", "-test.v=false")
	cmd.Env = append(os.Environ(), overlapClaimChildEnv+"="+mode, "TEST_CONCORD_OVERLAP_CLAIM_DB="+s.Path(), "TEST_CONCORD_OVERLAP_CLAIM_REQUEST="+string(raw))
	input, childInput, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin, cmd.Stderr = input, os.Stderr
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		_ = childInput.Close()
		t.Fatal(err)
	}
	p := &overlapClaimProcess{cmd: cmd, input: childInput, lines: make(chan string, 8), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = childInput.Close()
		t.Fatal(err)
	}
	_ = input.Close()
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			select {
			case p.lines <- scanner.Text():
			default:
				// Drain excess failure output so cleanup can always reap the
				// child, even when it panics before the expected barrier.
			}
		}
		close(p.lines)
		p.err = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() {
		_ = childInput.Close()
		_ = cmd.Process.Kill()
		<-p.done
	})
	return p
}

func (p *overlapClaimProcess) next(t *testing.T, ctx context.Context, stage string) overlapClaimMessage {
	t.Helper()
	select {
	case line, ok := <-p.lines:
		var message overlapClaimMessage
		if !ok || json.Unmarshal([]byte(line), &message) != nil || message.Stage != stage || message.PID != p.cmd.Process.Pid {
			t.Fatalf("claim child expected %s, got %q (stream open=%t)", stage, line, ok)
		}
		return message
	case <-ctx.Done():
		t.Fatalf("claim child waiting for %s: %v", stage, ctx.Err())
		return overlapClaimMessage{}
	}
}

func (p *overlapClaimProcess) wait(t *testing.T, ctx context.Context, killed bool) {
	t.Helper()
	select {
	case <-p.done:
		var exit *exec.ExitError
		if killed {
			if !errors.As(p.err, &exit) {
				t.Fatalf("killed claim child exit = %v", p.err)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
				t.Fatalf("claim child did not exit through SIGKILL: %v", p.err)
			}
		} else if p.err != nil {
			t.Fatalf("claim child exit = %v", p.err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func overlapClaimFixture(t *testing.T) (*Store, string, WorktreeClaimRequest) {
	t.Helper()
	s, git, repo := worktreeFixture(t)
	gitRunStore(t, repo, "init", "-b", "main")
	gitRunStore(t, repo, "config", "user.email", "concord@example.invalid")
	gitRunStore(t, repo, "config", "user.name", "Concord Claim Test")
	gitRunStore(t, repo, "commit", "--allow-empty", "-m", "base")
	seedRealGitOrigin(t, repo)
	req := baseClaim(git)
	req.Runner = nil
	req.BaseSHA = overlapClaimGit(t, repo, "rev-parse", "HEAD")
	req.SessionRef = "session-claim-a"
	return s, repo, req
}

func overlapClaimGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := (ExecGitRunner{}).Run(ctx, repo, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// Snapshot only current owner facts, not an event count or reconstructed log.
func overlapClaimSnapshot(t *testing.T, s *Store) []string {
	t.Helper()
	var result []string
	for _, query := range []string{
		`SELECT json_array(id,version) FROM work_items ORDER BY id`,
		`SELECT json_array(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) FROM worktree_claims ORDER BY op_id`,
		`SELECT json_array(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) FROM worktree_entries ORDER BY claim_op_id`,
		`SELECT json_array(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity) FROM worktree_occupancy ORDER BY worktree_id,session_ref`,
	} {
		rows, err := s.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, query)
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			result = append(result, row)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func assertOverlapClaimWinner(t *testing.T, s *Store, repo string, winner WorktreeEntry) {
	t.Helper()
	entries, err := s.WorktreeEntries(context.Background(), "work-w")
	if err != nil || len(entries) != 1 || !reflect.DeepEqual(entries[0], winner) {
		t.Fatalf("winning entry changed: entries=%+v error=%v, want %+v", entries, err, winner)
	}
	if _, err := os.Stat(winner.Path); err != nil {
		t.Errorf("winning native worktree disappeared while its durable entry remains active: %v", err)
		_, exists, branchErr := worktreeBranchHead(context.Background(), ExecGitRunner{}, repo, winner.Branch)
		if branchErr != nil || !exists {
			t.Errorf("winning native branch exists=%t, error=%v", exists, branchErr)
		}
		t.Fatalf("native worktree inventory after losing compensation: %s", overlapClaimGit(t, repo, "worktree", "list", "--porcelain"))
	}
	if got := overlapClaimGit(t, winner.Path, "rev-parse", "HEAD"); got != winner.BaseSHA {
		t.Fatalf("winning worktree HEAD = %s, want %s", got, winner.BaseSHA)
	}
	if got := overlapClaimGit(t, winner.Path, "symbolic-ref", "--short", "HEAD"); got != winner.Branch {
		t.Fatalf("winning worktree branch = %s, want %s", got, winner.Branch)
	}
	if got := overlapClaimGit(t, repo, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 2 || !strings.Contains(got, "worktree "+winner.Path+"\n") {
		t.Fatalf("native slots = %q, want primary plus the winning worktree", got)
	}
}

func TestOverlapRestartClaimPinnedIntent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	s, repo, req := overlapClaimFixture(t)
	a := startOverlapClaimProcess(t, ctx, s, req, "claim")
	winner := a.next(t, ctx, "result")
	a.wait(t, ctx, false)
	if winner.Error != "" || winner.Result.Created == nil {
		t.Fatalf("first claim = %+v", winner)
	}
	before := overlapClaimSnapshot(t, s)
	a2 := startOverlapClaimProcess(t, ctx, s, req, "claim")
	replay := a2.next(t, ctx, "result")
	a2.wait(t, ctx, false)
	if replay.Error != "" || !replay.Result.Reconciled || replay.Result.Created != nil || a2.cmd.Process.Pid == a.cmd.Process.Pid {
		t.Fatalf("fresh-process exact replay = %+v", replay)
	}
	if after := overlapClaimSnapshot(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("exact replay changed owner facts: before=%v after=%v", before, after)
	}
	competing := req
	competing.OpID, competing.RequestID, competing.SessionRef = "competing-op", "competing-request", "session-claim-b"
	b := startOverlapClaimProcess(t, ctx, s, competing, "claim")
	conflict := b.next(t, ctx, "result")
	b.wait(t, ctx, false)
	if conflict.Kind != KindProjectionConflict {
		t.Fatalf("competing identity = %+v, want projection conflict", conflict)
	}
	// Prepare actual Git facts, then change the requested pin before the SQL
	// half. This reaches pinned-intent admission rather than the native probe.
	native, created, err := s.PrepareWorktreeClaimNative(ctx, req)
	if err != nil || created != nil {
		t.Fatalf("prepare winning tree = %+v, error=%v", created, err)
	}
	gitRunStore(t, repo, "commit", "--allow-empty", "-m", "other base")
	changed := req
	changed.BaseSHA = overlapClaimGit(t, repo, "rev-parse", "HEAD")
	err = s.TransactDurable(ctx, func(tx *Transaction) error {
		_, err := ClaimWorktreeTx(ctx, tx, changed, native)
		return err
	})
	if failureKind(err) != KindInvalidOperation || !strings.Contains(err.Error(), "retry does not match the derived pinned intent") {
		t.Fatalf("changed pinned base = %v, want pinned-intent refusal", err)
	}
	if after := overlapClaimSnapshot(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("refusal changed winning owner facts: before=%v after=%v", before, after)
	}
	assertOverlapClaimWinner(t, s, repo, winner.Result.Entry)
}

func TestOverlapRestartClaimPrecommitCrash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	s, repo, req := overlapClaimFixture(t)
	before := overlapClaimSnapshot(t, s)
	var beforeRevision int64
	if err := s.db.QueryRowContext(ctx, `SELECT revision FROM store_revision`).Scan(&beforeRevision); err != nil {
		t.Fatal(err)
	}
	a := startOverlapClaimProcess(t, ctx, s, req, "crash")
	held := a.next(t, ctx, "precommit")
	if err := a.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	a.wait(t, ctx, true)
	fresh, err := Open(ctx, s.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if after := overlapClaimSnapshot(t, fresh); !reflect.DeepEqual(before, after) {
		t.Fatalf("precommit crash published claim facts: before=%v after=%v", before, after)
	}
	var revision int64
	if err := fresh.db.QueryRowContext(ctx, `SELECT revision FROM store_revision`).Scan(&revision); err != nil || revision != beforeRevision {
		t.Fatalf("precommit crash advanced store revision: before=%d after=%d error=%v", beforeRevision, revision, err)
	}
	if got := overlapClaimGit(t, held.Result.Entry.Path, "rev-parse", "HEAD"); got != req.BaseSHA {
		t.Fatalf("crash changed native base: %s", got)
	}
	a2 := startOverlapClaimProcess(t, ctx, fresh, req, "claim")
	retried := a2.next(t, ctx, "result")
	a2.wait(t, ctx, false)
	if retried.Error != "" || retried.Result.Created != nil || retried.Result.Entry.ClaimOpID != req.OpID {
		t.Fatalf("retry did not adopt the interrupted native creation: %+v", retried)
	}
	assertOverlapClaimWinner(t, fresh, repo, retried.Result.Entry)
	var version, claims, occupants int
	if err := fresh.db.QueryRow(`SELECT version,(SELECT count(*) FROM worktree_claims),(SELECT count(*) FROM worktree_occupancy) FROM work_items WHERE id='work-w'`).Scan(&version, &claims, &occupants); err != nil || version != 3 || claims != 1 || occupants != 1 {
		t.Fatalf("retry version/claims/occupants=%d/%d/%d error=%v, want 3/1/1", version, claims, occupants, err)
	}
}

func TestOverlapRestartClaimLostRacePreservesWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	s, repo, req := overlapClaimFixture(t)
	a := startOverlapClaimProcess(t, ctx, s, req, "race")
	a.next(t, ctx, "native-ready")
	competing := req
	competing.OpID, competing.RequestID, competing.SessionRef = "winning-op", "winning-request", "session-claim-b"
	b := startOverlapClaimProcess(t, ctx, s, competing, "claim")
	winner := b.next(t, ctx, "result")
	b.wait(t, ctx, false)
	if winner.Error != "" || winner.Result.Created != nil {
		t.Fatalf("competing adoption = %+v", winner)
	}
	before := overlapClaimSnapshot(t, s)
	if _, err := fmt.Fprintln(a.input, "RUN"); err != nil {
		t.Fatal(err)
	}
	loser := a.next(t, ctx, "result")
	a.wait(t, ctx, false)
	if loser.Kind != KindProjectionConflict {
		t.Fatalf("lost claim race = %+v, want projection conflict", loser)
	}
	if after := overlapClaimSnapshot(t, s); !reflect.DeepEqual(before, after) {
		t.Fatalf("losing claim changed winning owner facts: before=%v after=%v", before, after)
	}
	assertOverlapClaimWinner(t, s, repo, winner.Result.Entry)
}

type overlapClaimRemovalGate struct {
	beforeRemove func()
}

func (r overlapClaimRemovalGate) Run(ctx context.Context, directory string, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" {
		r.beforeRemove()
	}
	return (ExecGitRunner{}).Run(ctx, directory, args...)
}

func TestOverlapRestartClaimCompensationReservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	s, repo, req := overlapClaimFixture(t)
	_, created, err := s.PrepareWorktreeClaimNative(ctx, req)
	if err != nil || created == nil {
		t.Fatalf("prepare losing creation = %+v, error=%v", created, err)
	}
	competing := req
	competing.OpID, competing.RequestID, competing.SessionRef = "reservation-contender", "reservation-request", "session-claim-b"
	paused := false
	runner := overlapClaimRemovalGate{beforeRemove: func() {
		paused = true
		b := startOverlapClaimProcess(t, ctx, s, competing, "claim")
		contender := b.next(t, ctx, "result")
		b.wait(t, ctx, false)
		if contender.Kind != KindProjectionConflict {
			t.Errorf("claim during reserved native removal = %+v, want projection_conflict", contender)
		}
	}}
	cause := newFailure(KindVersionConflict, "worktree_claim", "claim lost its expected version", false, "reread the work item")
	if err := s.CompensateWorktreeClaimCreation(ctx, runner, *created, cause); err != cause {
		t.Fatalf("compensation = %v, want original failure %v", err, cause)
	}
	if !paused {
		t.Fatal("compensation never reached the actual Git removal boundary")
	}
	if _, err := os.Stat(created.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unowned failed creation survived complete compensation: %v", err)
	}
	if _, exists, err := worktreeBranchHead(ctx, ExecGitRunner{}, repo, created.Branch); err != nil || exists {
		t.Fatalf("unowned created branch survived: exists=%t, error=%v", exists, err)
	}
}

func TestOverlapRestartClaimRejectsPreparationBeforeCompensation(t *testing.T) {
	for _, sameOperation := range []bool{false, true} {
		name := "foreign-operation"
		if sameOperation {
			name = "same-operation-retry"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			s, _, req := overlapClaimFixture(t)
			_, created, err := s.PrepareWorktreeClaimNative(ctx, req)
			if err != nil || created == nil {
				t.Fatalf("prepare original creation = %+v, error=%v", created, err)
			}
			contender := req
			if !sameOperation {
				contender.OpID, contender.RequestID, contender.SessionRef = "prepared-contender", "prepared-request", "session-claim-b"
			}
			native, adoptedCreation, err := s.PrepareWorktreeClaimNative(ctx, contender)
			if err != nil || adoptedCreation != nil {
				t.Fatalf("prepare adoption before compensation = %+v, error=%v", adoptedCreation, err)
			}
			cause := newFailure(KindVersionConflict, "worktree_claim", "original claim failed", false, "reread the work item")
			if err := s.CompensateWorktreeClaimCreation(ctx, nil, *created, cause); err != cause {
				t.Fatalf("compensate original creation: %v", err)
			}
			before := overlapClaimSnapshot(t, s)
			err = s.TransactDurable(ctx, func(tx *Transaction) error {
				_, err := ClaimWorktreeTx(ctx, tx, contender, native)
				return err
			})
			if failureKind(err) != KindStaleContext {
				t.Errorf("claim with pre-compensation native facts = %v, want stale_context", err)
			}
			if after := overlapClaimSnapshot(t, s); !reflect.DeepEqual(before, after) {
				t.Fatalf("stale preparation published owner facts: before=%v after=%v", before, after)
			}
		})
	}
}
