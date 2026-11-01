package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeDeadlineProbeShim installs a fake git on PATH whose descendant sleep
// inherits the command's output pipes and outlives every budget the tests
// set: the shape a wedged probe takes on a host.
func writeDeadlineProbeShim(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 0.5 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Local probes retain the runner's process-group and pipe-drain deadline.
func TestExecGitRunnerDeadlineBoundsProbeDescendants(t *testing.T) {
	writeDeadlineProbeShim(t)
	for _, tc := range []struct {
		name string
		run  func(context.Context, string, ...string) ([]byte, error)
	}{
		{"Run", ExecGitRunner{}.Run},
		{"RunNoninteractive", ExecGitRunner{}.RunNoninteractive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			start := time.Now()
			_, err := tc.run(ctx, t.TempDir(), "rev-parse", "--git-dir")
			elapsed := time.Since(start)
			if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("probe error=%v context=%v want a failed probe at the deadline", err, ctx.Err())
			}
			if elapsed > 150*time.Millisecond {
				t.Fatalf("20ms deadline returned after %s: a probe descendant held the output pipes", elapsed)
			}
		})
	}
}

// RunNoninteractive must end inside the caller's deadline even when a
// descendant of the git process holds the output pipes open: the runner
// kills the process group and bounds the pipe drainage, so a wedged child
// cannot stretch a 20ms budget to the descendant's own lifetime.
func TestRunNoninteractiveBoundsDescendantHeldPipes(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 0.5 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (ExecGitRunner{}).RunNoninteractive(ctx, t.TempDir(), "fetch", "origin", "main")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the deadline to cancel the fetch")
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("20ms deadline returned after %s: the runner waited for a descendant-held output pipe: %v", elapsed, err)
	}
}

// RunNoninteractive refuses every credential prompt the supported transports
// can open: terminal prompts off, and askpass programs that answer empty so
// authentication fails instead of blocking.
func TestExecGitRunnerNoninteractiveEnv(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$GIT_TERMINAL_PROMPT\" \"$SSH_ASKPASS_REQUIRE\" \"$SSH_ASKPASS\" \"$GIT_ASKPASS\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := ExecGitRunner{}.RunNoninteractive(context.Background(), t.TempDir(), "fetch", "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "0|force|/bin/true|/bin/true" {
		t.Fatalf("noninteractive env=%q want refused prompts with empty-output askpass", got)
	}
}
