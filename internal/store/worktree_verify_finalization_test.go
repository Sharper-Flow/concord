package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Cancellation ends command authority, not the obligation to compare the
// subject and retain the failed run's output under its already acquired lease.
func TestVerifyWorktreeCancellationPreservesRunAndMutation(t *testing.T) {
	for _, mutated := range []bool{false, true} {
		for _, commandError := range []bool{false, true} {
			t.Run(fmt.Sprintf("mutated=%t/command_error=%t", mutated, commandError), func(t *testing.T) {
				s, git, _ := worktreeFixture(t)
				claimFixtureWorktree(t, s, git)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result, err := s.VerifyWorktree(ctx, verifyRequest(git, "lease-finalize-cancel", []string{"go", "test"}, func(runCtx context.Context, dir string, _ []string, _ int) (int, []byte, bool, error) {
					git.dirty[dir] = mutated
					cancel()
					if commandError {
						return -1, []byte("retained diagnostics"), false, runCtx.Err()
					}
					// A nil runner error or zero exit cannot promote a cancelled run.
					return 0, []byte("retained diagnostics"), false, nil
				}))
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err=%v, want cancellation", err)
				}
				if result.LeaseID != "lease-finalize-cancel" || result.TrackedFilesChanged != mutated || !strings.Contains(result.Output, "retained diagnostics") {
					t.Fatalf("cancelled result lost the run or comparison: %+v", result)
				}
				var state, outcome, payload string
				if err := s.db.QueryRow(`SELECT state,outcome,coalesce(result_json,'') FROM worktree_verify_leases WHERE lease_id='lease-finalize-cancel'`).Scan(&state, &outcome, &payload); err != nil {
					t.Fatal(err)
				}
				var recorded WorktreeVerifyResult
				if err := json.Unmarshal([]byte(payload), &recorded); err != nil {
					t.Fatalf("cancelled run has no readable result: %v", err)
				}
				if state != "released" || outcome != "aborted" || recorded.TrackedFilesChanged != mutated || recorded.Output != result.Output {
					t.Fatalf("lease=%s/%s recorded=%+v", state, outcome, recorded)
				}
				var producers int
				if err := s.db.QueryRow(`SELECT count(*) FROM durable_operations WHERE workflow_type_ref='worktree.verify'`).Scan(&producers); err != nil {
					t.Fatal(err)
				}
				if producers != 0 {
					t.Fatalf("cancelled run recorded %d passing producers", producers)
				}
				ran := false
				_, replayErr := s.VerifyWorktree(context.Background(), verifyRequest(git, "lease-finalize-cancel", []string{"go", "test"}, func(context.Context, string, []string, int) (int, []byte, bool, error) {
					ran = true
					return 0, nil, false, nil
				}))
				if failureKind(replayErr) != KindInvalidOperation || ran {
					t.Fatalf("aborted lease replay: ran=%t err=%v", ran, replayErr)
				}
			})
		}
	}
}

// A wrapper owns groups it creates itself. TERM must reach its cleanup trap;
// killing just the wrapper leaves its output-holding child alive after expiry.
func TestRunWorktreeVerifyCommandCancellationLetsWrapperCleanUp(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	marker := filepath.Join(dir, "terminated")
	script := filepath.Join(dir, "wrapper")
	body := "#!/bin/sh\ntrap 'kill -TERM \"$child\"; wait \"$child\"; printf cleaned > \"$2\"; exit 0' TERM\nsetsid sleep 30 &\nchild=$!\nprintf 'before cancellation\\n'\nprintf '%s\\n' \"$child\" > \"$1\"\nwait \"$child\"\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type runResult struct {
		code int
		out  []byte
		err  error
	}
	done := make(chan runResult, 1)
	go func() {
		code, out, _, err := RunWorktreeVerifyCommand(ctx, dir, []string{script, pidPath, marker}, 1024)
		done <- runResult{code, out, err}
	}()
	pid := waitVerifyFixturePID(t, pidPath)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	cancel()
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || result.code == 0 || !strings.Contains(string(result.out), "before cancellation") {
			t.Fatalf("cancelled wrapper result=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("verification cancellation left a wrapper child holding output")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "cleaned" {
		t.Fatalf("wrapper's termination cleanup did not run: data=%q err=%v", data, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("wrapper child %d survived cleanup: %v", pid, err)
	}
}

func waitVerifyFixturePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || pid <= 0 {
				t.Fatalf("invalid fixture pid %q: %v", data, err)
			}
			return pid
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("verification child did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
}
