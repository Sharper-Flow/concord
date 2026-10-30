package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// initFreshnessOriginRepo builds a working repository whose origin is a real
// local bare remote, so publication observation exercises real Git. The
// returned advance function commits and pushes one new default-branch commit
// from a throwaway clone and returns its SHA, leaving the working
// repository's remote-tracking cache stale until a fetch runs.
func initFreshnessOriginRepo(t *testing.T) (string, func(string) string) {
	t.Helper()
	parent := t.TempDir()
	origin := filepath.Join(parent, "origin.git")
	repo := filepath.Join(parent, "repo")
	runTestGit(t, parent, "init", "-q", "--bare", "-b", "main", "origin.git")
	runTestGit(t, parent, "init", "-q", "-b", "main", "repo")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("freshness\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "README.md")
	runTestGit(t, repo, "commit", "-q", "-m", "base")
	runTestGit(t, repo, "remote", "add", "origin", "https://freshness.invalid/repository.git")
	runTestGit(t, repo, "config", "url."+origin+".insteadOf", "https://freshness.invalid/repository.git")
	runTestGit(t, repo, "push", "-q", "origin", "main")
	runTestGit(t, repo, "fetch", "-q", "origin")
	runTestGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	advances := 0
	advance := func(message string) string {
		advances++
		seed := filepath.Join(parent, "seed-"+strconv.Itoa(advances))
		runTestGit(t, parent, "clone", "-q", origin, "seed-"+strconv.Itoa(advances))
		if err := os.WriteFile(filepath.Join(seed, "commit-"+strconv.Itoa(advances)), []byte(message+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runTestGit(t, seed, "add", ".")
		runTestGit(t, seed, "commit", "-q", "-m", message)
		runTestGit(t, seed, "push", "-q", "origin", "main")
		head := runTestGit(t, seed, "rev-parse", "HEAD")
		if err := os.RemoveAll(seed); err != nil {
			t.Fatal(err)
		}
		return head
	}
	return repo, advance
}

// goOfflineOrigin swaps the insteadOf mapping onto a removed local path, so
// every fetch fails the same way an unreachable remote does, with no network
// dependency.
func goOfflineOrigin(t *testing.T, repo string) {
	t.Helper()
	mapped := runTestGit(t, repo, "config", "--get-regexp", `^url\..*\.insteadOf$`)
	fields := strings.Fields(mapped)
	if len(fields) != 2 {
		t.Fatalf("fixture origin mapping missing: %q", mapped)
	}
	runTestGit(t, repo, "config", "--unset", fields[0])
	runTestGit(t, repo, "config", "url."+filepath.Join(t.TempDir(), "gone.git")+".insteadOf", fields[1])
}

func observeTransportPublication(t *testing.T, repo string) KnowledgePublication {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), knowledgePublicationTimeout)
	defer cancel()
	result, _ := observePublicationGit(ctx, publicationLocalRunner{ExecGitRunner{}}, KnowledgeHome{RepoPath: repo, HeadRef: "HEAD"})
	return result
}

// A configured local destination must not widen the publication fetch.
func TestPublicationTransportLeavesConfiguredMappingsUnapplied(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	parked := runTestGit(t, repo, "rev-parse", "HEAD")
	runTestGit(t, repo, "branch", "parked", parked)
	runTestGit(t, repo, "config", "--add", "remote.origin.fetch", "+refs/heads/main:refs/heads/parked")
	advanceDefault("advance origin")
	if result := observeTransportPublication(t, repo); result.Comparison != "lagging" {
		t.Fatalf("publication=%+v want lagging", result)
	}
	if moved := runTestGit(t, repo, "rev-parse", "refs/heads/parked"); moved != parked {
		t.Fatalf("publication moved local branch parked from %s to %s", parked, moved)
	}
}

// Publication must not depend on a submodule origin being reachable.
func TestPublicationTransportIgnoresSubmoduleOrigins(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	subSource, _ := initFreshnessOriginRepo(t)
	runTestGit(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", subSource, "sub")
	runTestGit(t, repo, "commit", "-qm", "add submodule")
	runTestGit(t, repo, "push", "-q", "origin", "main")
	runTestGit(t, filepath.Join(repo, "sub"), "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing-sub-origin.git"))
	runTestGit(t, repo, "config", "fetch.recurseSubmodules", "true")
	if result := observeTransportPublication(t, repo); result.Comparison != "match" {
		t.Fatalf("publication contacted an unreachable submodule origin: %+v", result)
	}
}

// Publication must not apply configured tag pruning.
func TestPublicationTransportKeepsLocalTagsUnderConfiguredPrune(t *testing.T) {
	repo, _ := initFreshnessOriginRepo(t)
	runTestGit(t, repo, "tag", "local-only")
	runTestGit(t, repo, "tag", "remote-only")
	runTestGit(t, repo, "push", "-q", "origin", "refs/tags/remote-only")
	runTestGit(t, repo, "tag", "-d", "remote-only")
	runTestGit(t, repo, "config", "fetch.prune", "true")
	runTestGit(t, repo, "config", "fetch.pruneTags", "true")
	if result := observeTransportPublication(t, repo); result.Comparison != "match" {
		t.Fatalf("publication=%+v want match", result)
	}
	if tags := runTestGit(t, repo, "tag", "--list"); tags != "local-only" {
		t.Fatalf("publication applied the configured prune: tags=%q want local-only", tags)
	}
}

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

// A symbolic tracking ref must not redirect publication onto a local branch.
func TestPublicationTransportNeverMovesLocalTipThroughSymref(t *testing.T) {
	repo, advanceDefault := initFreshnessOriginRepo(t)
	before := runTestGit(t, repo, "rev-parse", "HEAD")
	runTestGit(t, repo, "branch", "parked", before)
	runTestGit(t, repo, "symbolic-ref", "refs/remotes/origin/main", "refs/heads/parked")
	advanceDefault("remote advanced")
	result := observeTransportPublication(t, repo)
	if result.Comparison != "unknown" || !strings.Contains(result.Cause, "symbolic") {
		t.Fatalf("symbolic tracking ref was not refused: %+v", result)
	}
	after := runTestGit(t, repo, "rev-parse", "refs/heads/parked")
	if after != before {
		t.Fatalf("publication moved local branch tip: before=%s after=%s", before, after)
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
