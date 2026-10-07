package store

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// The child exits successfully after writing its complete output, while a
// descendant retains the output descriptors. Command success must not depend
// on that descendant closing a pipe or on a Go copy goroutine's schedule.
func TestExecGitRunnerSuccessfulExitWithInheritedOutput(t *testing.T) {
	bin := t.TempDir()
	shim := "#!/bin/sh\nif [ \"$3\" = stdin ]; then cat; else printf 'stdout\\n'; fi\nprintf 'stderr\\n' >&2\nsleep 30 &\nprintf '%s\\n' \"$!\" > \"$GIT_RUNNER_TEST_PID\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, mode := range gitOutputRunnerModes() {
		t.Run(mode.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "descendant.pid")
			t.Setenv("GIT_RUNNER_TEST_PID", pidPath)
			t.Cleanup(func() {
				data, err := os.ReadFile(pidPath)
				if err != nil {
					t.Error(err)
					return
				}
				pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil || pid <= 0 {
					t.Errorf("invalid descendant pid %q: %v", data, err)
					return
				}
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			start := time.Now()
			output, err := mode.run(ctx, t.TempDir())
			if err != nil {
				t.Fatalf("successful Git child returned %v", err)
			}
			if elapsed := time.Since(start); elapsed >= 2*time.Second {
				t.Fatalf("successful child waited %s for descendant output", elapsed)
			}
			want := "stdout\n"
			if mode.name == "stdin" {
				want = strings.Repeat("patch line\n", 65536)
			}
			if string(output) != want {
				t.Fatalf("stdout length=%d, want complete %d-byte output", len(output), len(want))
			}
		})
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

func TestExecGitRunnerCancellationKillsDescendants(t *testing.T) {
	bin := t.TempDir()
	shim := "#!/bin/sh\nsleep 30 &\nprintf '%s\\n' \"$!\" > \"$GIT_RUNNER_TEST_PID\"\nwait\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, mode := range gitOutputRunnerModes() {
		t.Run(mode.name, func(t *testing.T) {
			pidPath := filepath.Join(t.TempDir(), "descendant.pid")
			t.Setenv("GIT_RUNNER_TEST_PID", pidPath)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			dir := t.TempDir()
			done := make(chan error, 1)
			go func() {
				_, err := mode.run(ctx, dir)
				done <- err
			}()
			var pid int
			deadline := time.Now().Add(2 * time.Second)
			for pid == 0 {
				data, err := os.ReadFile(pidPath)
				if err == nil && len(data) > 0 {
					pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
					if err != nil || pid <= 0 {
						t.Fatalf("invalid descendant pid %q: %v", data, err)
					}
				} else if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				if time.Now().After(deadline) {
					t.Fatal("Git descendant did not become ready")
				}
				time.Sleep(time.Millisecond)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancelled Git command reported success")
				}
			case <-time.After(150 * time.Millisecond):
				t.Fatal("Git cancellation exceeded the existing shutdown bound")
			}
			deadline = time.Now().Add(2 * time.Second)
			for {
				stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if errors.Is(err, os.ErrNotExist) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				end := strings.LastIndex(string(stat), ") ")
				if end < 0 || len(stat) <= end+2 {
					t.Fatalf("invalid descendant stat %q", stat)
				}
				if stat[end+2] == 'Z' || stat[end+2] == 'X' {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("descendant %d survived group cancellation", pid)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestGitCaptureFilePrivateUnlinkedAndOffsetIndependent(t *testing.T) {
	file, err := newGitCaptureFile()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("capture permissions: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(file.Name()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture pathname remains visible: %v", err)
	}
	if _, err := file.WriteString("first\n"); err != nil {
		t.Fatal(err)
	}
	output, err := readGitCaptureFile(file)
	if err != nil || string(output) != "first\n" {
		t.Fatalf("first snapshot=%q err=%v", output, err)
	}
	if _, err := file.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	output, err = readGitCaptureFile(file)
	if err != nil || string(output) != "first\nsecond\n" {
		t.Fatalf("snapshot changed the inherited write offset: output=%q err=%v", output, err)
	}
}

func TestExecGitRunnerCaptureSetupFailureDoesNotLaunch(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(bin, "started")
	shim := "#!/bin/sh\nprintf started > \"$GIT_RUNNER_TEST_MARKER\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_RUNNER_TEST_MARKER", marker)
	t.Setenv("TMPDIR", filepath.Join(bin, "absent"))
	for _, mode := range gitOutputRunnerModes() {
		t.Run(mode.name, func(t *testing.T) {
			_, err := mode.run(context.Background(), bin)
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("capture setup error=%v", err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Git launched before capture setup completed: %v", err)
			}
		})
	}
}

func TestRunBoundedGitOutputClosesCaptureFiles(t *testing.T) {
	for _, outcome := range []string{"success", "exit_error", "start_error", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "printf output; printf error >&2")
			if outcome == "exit_error" {
				cmd = exec.CommandContext(ctx, "/bin/sh", "-c", "printf error >&2; exit 7")
			} else if outcome == "start_error" {
				cmd = exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing"))
			} else if outcome == "cancelled" {
				cancel()
			}
			_, _, err := runBoundedGitOutput(cmd)
			if (err == nil) != (outcome == "success") {
				t.Fatalf("outcome=%s err=%v", outcome, err)
			}
			for _, stream := range []io.Writer{cmd.Stdout, cmd.Stderr} {
				file, ok := stream.(*os.File)
				if !ok {
					t.Fatalf("capture stream type %T", stream)
				}
				if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("capture descriptor remains open: %v", err)
				}
				if _, err := os.Stat(file.Name()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("capture pathname remains after %s: %v", outcome, err)
				}
			}
		})
	}
}
