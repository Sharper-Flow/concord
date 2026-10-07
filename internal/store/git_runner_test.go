package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func gitOutputRunnerModes() []struct {
	name string
	run  func(context.Context, string) ([]byte, error)
} {
	return []struct {
		name string
		run  func(context.Context, string) ([]byte, error)
	}{
		{"ordinary", func(ctx context.Context, dir string) ([]byte, error) {
			return (ExecGitRunner{}).Run(ctx, dir, "output")
		}},
		{"stdin", func(ctx context.Context, dir string) ([]byte, error) {
			return (ExecGitRunner{}).RunStdin(ctx, dir, []byte(strings.Repeat("patch line\n", 65536)), "stdin")
		}},
		{"noninteractive", func(ctx context.Context, dir string) ([]byte, error) {
			return (ExecGitRunner{}).RunNoninteractive(ctx, dir, "output")
		}},
	}
}

// The descendant waits until the direct child is reaped before delaying its
// final output. This makes normal drainage exceed the cancellation delay.
func delayedGitOutputShim(output string) string {
	return "#!/bin/sh\nparent=$$\nif [ \"$3\" = stdin ]; then cat; fi\n(while kill -0 \"$parent\" 2>/dev/null; do sleep 0.01; done\nsleep 0.3\nprintf '" + output + "\\n'\nprintf 'late stderr\\n' >&2) &\n"
}

func TestExecGitRunnerNormalOutputDrainage(t *testing.T) {
	bin := t.TempDir()
	shim := delayedGitOutputShim("late stdout")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, mode := range gitOutputRunnerModes() {
		t.Run(mode.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			output, err := mode.run(ctx, t.TempDir())
			if err != nil {
				t.Fatalf("successful Git child returned %v", err)
			}
			if elapsed := time.Since(start); elapsed < 2*boundedGitWaitDelay {
				t.Fatalf("normal drainage took only %s", elapsed)
			}
			want := "late stdout\n"
			if mode.name == "stdin" {
				want = strings.Repeat("patch line\n", 65536) + want
			}
			if string(output) != want {
				t.Fatalf("stdout length=%d, want complete %d-byte output", len(output), len(want))
			}
		})
	}
}

func TestResolveCommitSHANormalDrainageDoesNotRefuseUnreachable(t *testing.T) {
	bin := t.TempDir()
	want := strings.Repeat("a", 40)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(delayedGitOutputShim(want)), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sha, err := resolveCommitSHARunner(ctx, ExecGitRunner{}, t.TempDir(), "HEAD")
	if err != nil || sha != want {
		t.Fatalf("reachable commit refused: sha=%q error=%v", sha, err)
	}
}

func TestGitOutputNormalDrainagePreservesStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.Command("/bin/sh", "-c", strings.TrimPrefix(delayedGitOutputShim("late stdout"), "#!/bin/sh\n"))
	stdout, stderr, err := runBoundedGitOutput(ctx, cmd)
	if err != nil || string(stdout) != "late stdout\n" || string(stderr) != "late stderr\n" {
		t.Fatalf("stdout=%q stderr=%q error=%v", stdout, stderr, err)
	}
}

func TestExecGitRunnerCancellationOwnsEntireDrainage(t *testing.T) {
	for _, state := range []string{"running_group", "exited_escaped"} {
		t.Run(state, func(t *testing.T) {
			bin := t.TempDir()
			child, finish := "sleep 30", "wait"
			if state == "exited_escaped" {
				child, finish = "setsid sleep 30", "exit 0"
			}
			shim := "#!/bin/sh\n" + child + " &\nprintf 'before cancellation\\n'\nprintf '%s %s\\n' \"$$\" \"$!\" > \"$GIT_RUNNER_TEST_PIDS\"\n" + finish + "\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			for _, mode := range gitOutputRunnerModes() {
				t.Run(mode.name, func(t *testing.T) {
					pidPath := filepath.Join(t.TempDir(), "pids")
					t.Setenv("GIT_RUNNER_TEST_PIDS", pidPath)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					type result struct {
						output []byte
						err    error
					}
					results := make(chan result, 1)
					dir := t.TempDir()
					go func() {
						out, err := mode.run(ctx, dir)
						results <- result{out, err}
					}()
					var parent, descendant int
					awaitGitTestCondition(t, func() bool {
						data, err := os.ReadFile(pidPath)
						fields := strings.Fields(string(data))
						if err != nil || len(fields) != 2 {
							return false
						}
						parent, _ = strconv.Atoi(fields[0])
						descendant, _ = strconv.Atoi(fields[1])
						return parent > 0 && descendant > 0
					})
					t.Cleanup(func() { _ = syscall.Kill(descendant, syscall.SIGKILL) })
					if state == "exited_escaped" {
						awaitGitTestCondition(t, func() bool {
							group, err := syscall.Getpgid(descendant)
							return err == nil && group == descendant && errors.Is(syscall.Kill(parent, 0), syscall.ESRCH)
						})
					}
					start := time.Now()
					cancel()
					select {
					case got := <-results:
						if got.err == nil || string(got.output) != "before cancellation\n" {
							t.Fatalf("stdout=%q error=%v", got.output, got.err)
						}
						if state == "exited_escaped" && !errors.Is(got.err, context.Canceled) {
							t.Fatalf("post-exit cancellation error=%v", got.err)
						}
					case <-time.After(time.Second):
						t.Fatal("cancellation did not bound pipe cleanup")
					}
					if state == "exited_escaped" && time.Since(start) < boundedGitWaitDelay {
						t.Fatal("escaped output descriptors did not exercise bounded cleanup")
					}
					if state == "running_group" {
						awaitGitTestCondition(t, func() bool {
							data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(descendant), "stat"))
							return errors.Is(err, os.ErrNotExist) || strings.Contains(string(data), ") Z ")
						})
					}
				})
			}
		})
	}
}

func awaitGitTestCondition(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatal("Git fixture did not reach the expected process state")
		case <-tick.C:
		}
	}
}

func TestExecGitRunnerExitErrorPreservesOutput(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf 'partial stdout\\n'\nprintf 'failure stderr\\n' >&2\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, mode := range gitOutputRunnerModes() {
		t.Run(mode.name, func(t *testing.T) {
			output, err := mode.run(context.Background(), t.TempDir())
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
				t.Fatalf("error=%v, want exit status 7", err)
			}
			if string(output) != "partial stdout\n" || string(exitErr.Stderr) != "failure stderr\n" {
				t.Fatalf("stdout=%q stderr=%q", output, exitErr.Stderr)
			}
		})
	}
}

func TestExecGitRunnerSuccessfulExitWithoutReadingStdin(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf 'complete\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := (ExecGitRunner{}).RunStdin(context.Background(), t.TempDir(), []byte(strings.Repeat("patch line\n", 65536)), "stdin")
	if err != nil || string(out) != "complete\n" {
		t.Fatalf("stdout=%q error=%v", out, err)
	}
}

func TestExecGitRunnerAlreadyCanceledDoesNotStart(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "started")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\ntouch \"$GIT_RUNNER_TEST_MARKER\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_RUNNER_TEST_MARKER", marker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, mode := range gitOutputRunnerModes() {
		out, err := mode.run(ctx, t.TempDir())
		if !errors.Is(err, context.Canceled) || len(out) != 0 {
			t.Fatalf("%s: stdout=%q error=%v", mode.name, out, err)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("already-canceled command started: %v", err)
	}
}
