package store

// Automatic conflict records preserve normalized contained UTF-8 paths.
// Resolution edits retain the architecture's stricter path vocabulary.

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// integrationBracketedRoute is the common Next.js dynamic-route spelling
// the narrower conflict-record policy must accept: contained, normalized,
// valid UTF-8, and glob-shaped only in spelling.
const integrationBracketedRoute = "app/[slug]/page.tsx"

// integrationNativeEmptyResolution is the empty T->tree(M) resolution:
// no supplied edits, only the project identity and the declared limit.
func integrationNativeEmptyResolution() sliceDiffInput {
	return sliceDiffInput{ProjectID: sliceDiffProjectID, Limit: designArchitectureMaxChangedLines}
}

// TestIntegrationNativeSeams proves the approved narrower path policy for
// automatic conflict records: Git-reported paths accept every normalized
// contained UTF-8 spelling, including spaces, tabs, and glob spellings,
// while containment, normalization, and encoding refusals stay exact.
func TestIntegrationNativeSeams(t *testing.T) {
	t.Run("bracketed route conflict records accept while T->M is empty", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)
		records := []string{
			integrationMergeTreeRecord("100644", oid, "1", integrationBracketedRoute),
			integrationMergeTreeRecord("100644", oid, "2", integrationBracketedRoute),
			integrationMergeTreeRecord("100644", oid, "3", integrationBracketedRoute),
		}
		want := []integrationConflictStage{
			{Path: integrationBracketedRoute, Mode: "100644", OID: oid, Stage: 1},
			{Path: integrationBracketedRoute, Mode: "100644", OID: oid, Stage: 2},
			{Path: integrationBracketedRoute, Mode: "100644", OID: oid, Stage: 3},
		}

		t.Run("parse accepts complete NUL records on the bracketed route", func(t *testing.T) {
			got, err := parseIntegrationMergeTree(1, integrationMergeTreeOutput(integrationTreeOID40, records...))
			assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID40, true, want)
		})

		t.Run("measurement accepts the conflicted tree with an empty resolution", func(t *testing.T) {
			in := integrationResolutionDiffInput{
				MergeTreeExit:   1,
				MergeTreeOutput: integrationMergeTreeOutput(integrationTreeOID40, records...),
				Resolution:      integrationNativeEmptyResolution(),
			}
			got, err := measureIntegrationResolutionDiff(in)
			assertIntegrationMergeTreeValid(t, got.AutomaticTree, err, integrationTreeOID40, true, want)
			if len(got.Resolution.Paths) != 0 || got.Resolution.TotalLines != 0 {
				t.Fatalf("Resolution = %+v, want zero paths and a zero total", got.Resolution)
			}
		})
	})

	t.Run("conflict records accept normalized contained native path spellings", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)
		cases := []struct {
			name string
			path string
		}{
			{"space", "dir with space/doc file.go"},
			{"tab", "dir\tfile.go"},
			{"star glob", "internal/store/generated/*.go"},
			{"question glob", "internal/store/config?.go"},
			{"multibyte utf-8", "internal/store/国際化.go"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				out := integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord("100644", oid, "2", tc.path))
				got, err := parseIntegrationMergeTree(1, out)
				assertIntegrationMergeTreeValid(t, got, err, integrationTreeOID40, true, []integrationConflictStage{{Path: tc.path, Mode: "100644", OID: oid, Stage: 2}})
			})
		}
	})

	t.Run("conflict records refuse absolute traversal unnormalized and invalid-encoding paths", func(t *testing.T) {
		oid := integrationRecordOIDFor(integrationTreeOID40)
		cases := []struct {
			name    string
			path    string
			markers []string
		}{
			{"absolute path", "/etc/passwd", []string{"normalized"}},
			{"parent traversal", "../escape.go", []string{"normalized"}},
			{"self segment", "internal/./store.go", []string{"normalized"}},
			{"empty segment", "internal//store.go", []string{"normalized"}},
			{"trailing separator", "internal/store/", []string{"normalized"}},
			{"invalid encoding", "internal/store/bad\xff\xfename.go", []string{"UTF-8"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				out := integrationMergeTreeOutput(integrationTreeOID40, integrationMergeTreeRecord("100644", oid, "1", tc.path))
				got, err := parseIntegrationMergeTree(1, out)
				assertIntegrationMergeTreeRefusal(t, got, err, tc.markers...)
			})
		}
	})
}

// TestIntegrationSharedVocabularyUnchanged proves the narrower policy
// stays scoped to automatic conflict records: ordinary slice diffs and
// resolution edits keep the existing shared vocabulary and its glob
// refusal for bracketed paths.
func TestIntegrationSharedVocabularyUnchanged(t *testing.T) {
	t.Run("ordinary slice edit refuses a bracketed path", func(t *testing.T) {
		in := sliceDiffInput{
			ProjectID: sliceDiffProjectID,
			RawDiff:   []byte(sliceDiffRawRecord("100644", "100644", "M", integrationBracketedRoute)),
			Numstat:   []byte(sliceDiffNumstatRecord("3", "2", integrationBracketedRoute)),
			Limit:     designArchitectureMaxChangedLines,
		}
		got, err := measureSliceDiff(in)
		if err == nil {
			t.Fatalf("ordinary slice edit on %q measured %+v; the shared vocabulary must keep refusing bracketed paths", integrationBracketedRoute, got)
		}
		assertFailureKind(t, err, KindInvalidPayload)
		if !strings.Contains(err.Error(), "glob") {
			t.Fatalf("refusal %v does not name the glob policy", err)
		}
	})

	t.Run("resolution edit refuses a bracketed path", func(t *testing.T) {
		got, err := integrationResolutionVariant(t, 1, func(in *integrationResolutionDiffInput) {
			in.Resolution.RawDiff = []byte(sliceDiffRawRecord("100644", "100644", "M", integrationBracketedRoute))
			in.Resolution.Numstat = []byte(sliceDiffNumstatRecord("3", "2", integrationBracketedRoute))
		})
		assertIntegrationResolutionRefusal(t, got, err, "glob")
	})
}

// assertSliceDiffReason classifies one measured-slice refusal through the
// typed reason and the standard Failure envelope, never through detail text.
func assertSliceDiffReason(t *testing.T, err error, want sliceDiffReason) {
	t.Helper()
	if err == nil {
		t.Fatal("measureSliceDiff accepted input it must refuse")
	}
	if !errors.Is(err, want) {
		t.Fatalf("refusal %v does not carry reason %q", err, want)
	}
	other := sliceDiffReasonScope
	if want == sliceDiffReasonScope {
		other = sliceDiffReasonBudget
	}
	if errors.Is(err, other) {
		t.Fatalf("refusal %v carries both scope and budget reasons", err)
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("refusal %v is not a *Failure", err)
	}
	if failure.Kind != KindInvalidPayload {
		t.Fatalf("Failure.Kind = %q, want %q", failure.Kind, KindInvalidPayload)
	}
}

// TestIntegrationSliceDiffReason proves the internal typed reason rides the
// standard Failure: the three scope sites and the total-budget site classify
// with errors.Is and errors.As, while every malformed input keeps its plain
// refusal with no reason attached.
func TestIntegrationSliceDiffReason(t *testing.T) {
	const countedPath = "internal/store/kept.go"
	scoped := func(change func(in *sliceDiffInput)) sliceDiffInput {
		in := sliceDiffInput{
			ProjectID: sliceDiffProjectID,
			RawDiff:   []byte(sliceDiffRawRecord("100644", "100644", "M", countedPath)),
			Numstat:   []byte(sliceDiffNumstatRecord("3", "2", countedPath)),
			OldBlobs:  map[string][]byte{countedPath: sliceDiffTextBlob(2)},
			NewBlobs:  map[string][]byte{countedPath: sliceDiffTextBlob(3)},
			Files: []WorkflowDesignArchitectureFile{
				{ProjectID: sliceDiffProjectID, Path: countedPath, Role: DesignArchitectureFileRoleSource},
			},
			SliceFiles: []string{countedPath},
			Limit:      designArchitectureMaxChangedLines,
		}
		if change != nil {
			change(&in)
		}
		return in
	}

	t.Run("path outside the declared slice files carries the scope reason", func(t *testing.T) {
		_, err := measureSliceDiff(scoped(func(in *sliceDiffInput) {
			in.SliceFiles = nil
		}))
		assertSliceDiffReason(t, err, sliceDiffReasonScope)
	})

	t.Run("changed path declared unchanged carries the scope reason", func(t *testing.T) {
		_, err := measureSliceDiff(scoped(func(in *sliceDiffInput) {
			in.Unchanged = []string{countedPath}
		}))
		assertSliceDiffReason(t, err, sliceDiffReasonScope)
	})

	t.Run("changed path without an approved role carries the scope reason", func(t *testing.T) {
		_, err := measureSliceDiff(scoped(func(in *sliceDiffInput) {
			in.Files = nil
		}))
		assertSliceDiffReason(t, err, sliceDiffReasonScope)
	})

	t.Run("measured cost above the declared limit carries the budget reason", func(t *testing.T) {
		_, err := measureSliceDiff(scoped(func(in *sliceDiffInput) {
			in.Numstat = []byte(sliceDiffNumstatRecord("251", "250", countedPath))
		}))
		assertSliceDiffReason(t, err, sliceDiffReasonBudget)
	})

	t.Run("malformed input keeps the plain failure with no reason", func(t *testing.T) {
		in := scoped(nil)
		in.RawDiff = in.RawDiff[:len(in.RawDiff)-1]
		_, err := measureSliceDiff(in)
		assertFailureKind(t, err, KindInvalidPayload)
		if errors.Is(err, sliceDiffReasonScope) || errors.Is(err, sliceDiffReasonBudget) {
			t.Fatalf("malformed refusal %v carries a policy reason", err)
		}
	})
}

// TestProjectCanonicalPathCoreServesCallerQueryer proves the extracted
// locator core reads through the supplied queryer alone: called inside an
// open write transaction it serves the read without parking on the pooled
// connection, and its missing-locator refusal stays typed.
func TestProjectCanonicalPathCoreServesCallerQueryer(t *testing.T) {
	s := locatorReplayStore(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := s.AddProjectLocator(ctx, "project-a", ProjectLocator{ID: "path-core", Kind: LocatorCanonicalPath, Value: root}, 1); err != nil {
		t.Fatal(err)
	}
	public, err := s.ProjectCanonicalPath(ctx, "project-a")
	if err != nil || public == "" {
		t.Fatalf("public read = %q, err = %v", public, err)
	}

	wtx, err := beginWriteTx(ctx, s.db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wtx.Rollback() }()
	pending, err := projectCanonicalPathCore(ctx, wtx, "project-a")
	if err != nil {
		t.Fatalf("core refused inside an open write transaction: %v", err)
	}
	if pending != public {
		t.Fatalf("core read %q through the caller queryer, want the public %q", pending, public)
	}
	if _, err := projectCanonicalPathCore(ctx, wtx, "project-missing"); err == nil {
		t.Fatal("core accepted a Project with no canonical_path locator")
	} else {
		assertFailureKind(t, err, KindUnknownScope)
	}
}

// ---------------------------------------------------------------------------
// Native runner conformance.
//
// No shell is executed and no unbounded buffer owner is reused. The tests
// derive only shorter parent contexts; they add no production timeout knob.

// assertIntegrationProbeRefusal pins the one refusal shape: a typed error
// carrying the expected reason and the zero result, never a truncated
// partial success.
func assertIntegrationProbeRefusal(t *testing.T, got integrationProbeResult, err error, want integrationProbeRefusalReason) {
	t.Helper()
	if err == nil {
		t.Fatalf("probe accepted a command it must refuse; result %+v", got)
	}
	if !errors.Is(err, want) {
		t.Fatalf("refusal %v does not carry reason %q", err, want)
	}
	if got.ExitCode != 0 || len(got.Stdout) != 0 || len(got.Stderr) != 0 {
		t.Fatalf("refusal %v carried a partial result %+v; the zero result is the only refusal shape", err, got)
	}
}

// integrationRunnerRepo initializes the deterministic subject repository.
func integrationRunnerRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "subject")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "init", "-q", "-b", "main")
	return repo
}

// integrationRunnerCommitAll stages every path and writes one commit.
func integrationRunnerCommitAll(t *testing.T, repo, message string) {
	t.Helper()
	gitRunStore(t, repo, "add", "-A")
	gitRunStore(t, repo, "-c", "user.email=integration@example.invalid", "-c", "user.name=Integration Fixture", "commit", "-q", "-m", message)
}

// integrationRunnerBlob stores content in the subject repository and returns
// its object name.
func integrationRunnerBlob(t *testing.T, repo string, content []byte) string {
	t.Helper()
	out, err := ExecGitRunner{}.RunStdin(context.Background(), repo, content, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// integrationRunnerStdinPipe returns a read end the command blocks on until
// closeAfter elapses; zero means the test never closes it. git
// hash-object --stdin reads until the pipe closes, so the pipe proves
// deadline behavior and bounded cleanup without a sleep command.
func integrationRunnerStdinPipe(t *testing.T, closeAfter time.Duration) *os.File {
	t.Helper()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	var timer *time.Timer
	if closeAfter > 0 {
		timer = time.AfterFunc(closeAfter, func() { _ = pw.Close() })
	}
	t.Cleanup(func() {
		if timer != nil {
			timer.Stop()
		}
		_ = pw.Close()
		_ = pr.Close()
	})
	return pr
}

// TestIntegrationNativeRunner drives the native runner contract with
// deterministic real-Git fixtures.
func TestIntegrationNativeRunner(t *testing.T) {
	t.Run("owns the five second aggregate deadline", func(t *testing.T) {
		if integrationProbeTimeout != 5*time.Second {
			t.Fatalf("integrationProbeTimeout = %v, want the owned five-second aggregate deadline", integrationProbeTimeout)
		}
	})

	t.Run("preserves the conflicted exit one and its complete stdout", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		integrationRunnerCommitAll(t, repo, "base")
		gitRunStore(t, repo, "checkout", "-q", "-b", "theirs")
		if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("theirs\n"), 0600); err != nil {
			t.Fatal(err)
		}
		integrationRunnerCommitAll(t, repo, "theirs side")
		gitRunStore(t, repo, "checkout", "-q", "main")
		gitRunStore(t, repo, "checkout", "-q", "-b", "mine")
		if err := os.WriteFile(filepath.Join(repo, "shared.txt"), []byte("mine\n"), 0600); err != nil {
			t.Fatal(err)
		}
		integrationRunnerCommitAll(t, repo, "mine side")

		probe := newIntegrationGitProbe(context.Background())
		got, err := probe.output(repo, nil, nil, "merge-tree", "--write-tree", "-z", "--no-messages", "mine", "theirs")
		if err != nil {
			t.Fatalf("conflicted merge-tree became a transport refusal: %v", err)
		}
		if got.ExitCode != 1 {
			t.Fatalf("conflicted merge exit = %d, want 1", got.ExitCode)
		}
		parsed, err := parseIntegrationMergeTree(got.ExitCode, got.Stdout)
		if err != nil {
			t.Fatalf("exit-one stdout is not the supported pinned bytes: %v", err)
		}
		if !parsed.Conflicted {
			t.Fatalf("parse = %+v, want the conflicted flag derived from the exit status", parsed)
		}
		shared := false
		for _, stage := range parsed.Conflicts {
			if stage.Path == "shared.txt" {
				shared = true
			}
		}
		if !shared {
			t.Fatalf("conflict records %+v lost the conflicted path", parsed.Conflicts)
		}
	})

	t.Run("keeps one aggregate output budget across commands", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		big := bytes.Repeat([]byte("a"), 3<<20)
		more := bytes.Repeat([]byte("b"), 2<<20)
		oidBig := integrationRunnerBlob(t, repo, big)
		oidMore := integrationRunnerBlob(t, repo, more)

		probe := newIntegrationGitProbe(context.Background())
		first, err := probe.output(repo, nil, nil, "cat-file", "-p", oidBig)
		if err != nil || first.ExitCode != 0 {
			t.Fatalf("first command under the budget refused: %+v %v", first, err)
		}
		if !bytes.Equal(first.Stdout, big) {
			t.Fatalf("first command carried %d bytes, want the complete %d-byte stream", len(first.Stdout), len(big))
		}

		second, err := probe.output(repo, nil, nil, "cat-file", "-p", oidMore)
		assertIntegrationProbeRefusal(t, second, err, integrationProbeRefusalOverflow)

		_, err = probe.output(repo, nil, nil, "rev-parse", "--show-toplevel")
		assertIntegrationProbeRefusal(t, integrationProbeResult{}, err, integrationProbeRefusalOverflow)
	})

	t.Run("refuses single-command output above the budget", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		huge := bytes.Repeat([]byte("c"), 5<<20)
		oid := integrationRunnerBlob(t, repo, huge)

		probe := newIntegrationGitProbe(context.Background())
		got, err := probe.output(repo, nil, nil, "cat-file", "-p", oid)
		assertIntegrationProbeRefusal(t, got, err, integrationProbeRefusalOverflow)
	})

	t.Run("captures stderr separately from stdout", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		probe := newIntegrationGitProbe(context.Background())

		// A failed verify writes only the fatal message to stderr, so any
		// stream mixing or swapping flips one of the two assertions.
		got, err := probe.output(repo, nil, nil, "rev-parse", "--verify", "definitely-missing-ref")
		if err != nil {
			t.Fatalf("ordinary nonzero exit became a transport refusal: %v", err)
		}
		if got.ExitCode == 0 {
			t.Fatal("rev-parse verified a missing reference")
		}
		if len(got.Stdout) != 0 {
			t.Fatalf("stdout captured %q for a failed verify, want the failure only on stderr", got.Stdout)
		}
		if len(got.Stderr) == 0 {
			t.Fatal("stderr lost the failure output; the streams are not separately captured")
		}
	})

	t.Run("shares one locked budget across both streams", func(t *testing.T) {
		repo := integrationRunnerRepo(t)

		// Two stream writers draw one counter: 3 MiB on stdout plus 2 MiB
		// on stderr overflows the 4-MiB budget though neither stream alone
		// would, and the poisoned probe refuses the next real command.
		probe := newIntegrationGitProbe(context.Background())
		stdoutStream, stderrStream := &integrationProbeStream{probe: probe}, &integrationProbeStream{probe: probe}
		if _, err := stdoutStream.Write(bytes.Repeat([]byte("a"), 3<<20)); err != nil {
			t.Fatalf("stdout stream write failed: %v", err)
		}
		if _, err := stderrStream.Write(bytes.Repeat([]byte("b"), 2<<20)); err != nil {
			t.Fatalf("stderr stream write failed: %v", err)
		}
		if probe.remaining != 0 || !probe.overflow {
			t.Fatalf("budget after both streams: remaining %d overflow %v, want the exhausted shared budget", probe.remaining, probe.overflow)
		}
		_, err := probe.output(repo, nil, nil, "rev-parse", "--show-toplevel")
		assertIntegrationProbeRefusal(t, integrationProbeResult{}, err, integrationProbeRefusalOverflow)

		// Concurrent writers must not lose updates under the one lock.
		concurrent := newIntegrationGitProbe(context.Background())
		left, right := &integrationProbeStream{probe: concurrent}, &integrationProbeStream{probe: concurrent}
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			_, _ = left.Write(bytes.Repeat([]byte("a"), 1<<20))
		}()
		go func() {
			defer group.Done()
			_, _ = right.Write(bytes.Repeat([]byte("b"), 1<<20))
		}()
		group.Wait()
		if concurrent.remaining != maxGitOutput-2<<20 {
			t.Fatalf("concurrent stream writes left remaining %d, want %d", concurrent.remaining, maxGitOutput-2<<20)
		}
	})

	t.Run("keeps the aggregate deadline across commands", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		parent, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		probe := newIntegrationGitProbe(parent)

		first, err := probe.output(repo, integrationRunnerStdinPipe(t, 100*time.Millisecond), nil, "hash-object", "--stdin")
		if err != nil || first.ExitCode != 0 {
			t.Fatalf("first command inside the aggregate deadline refused: %+v %v", first, err)
		}
		second, err := probe.output(repo, integrationRunnerStdinPipe(t, 400*time.Millisecond), nil, "hash-object", "--stdin")
		assertIntegrationProbeRefusal(t, second, err, integrationProbeRefusalDeadline)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline refusal %v does not carry the context deadline", err)
		}
	})

	t.Run("kills a pipe-blocked child on the derived deadline", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		parent, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		probe := newIntegrationGitProbe(parent)

		got, err := probe.output(repo, integrationRunnerStdinPipe(t, 0), nil, "hash-object", "--stdin")
		assertIntegrationProbeRefusal(t, got, err, integrationProbeRefusalDeadline)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline refusal %v does not carry the context deadline", err)
		}
		if _, err := probe.output(repo, nil, nil, "rev-parse", "--show-toplevel"); !errors.Is(err, integrationProbeRefusalDeadline) {
			t.Fatalf("later command after the deadline = %v, want the same terminal refusal", err)
		}
	})

	t.Run("refuses a pre-canceled parent before any command", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		parent, cancel := context.WithCancel(context.Background())
		probe := newIntegrationGitProbe(parent)
		cancel()

		got, err := probe.output(repo, nil, nil, "rev-parse", "--show-toplevel")
		assertIntegrationProbeRefusal(t, got, err, integrationProbeRefusalCanceled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled refusal %v does not carry the context cause", err)
		}
	})

	t.Run("drops poisoned inherited Git keys", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("tracked\n"), 0600); err != nil {
			t.Fatal(err)
		}
		integrationRunnerCommitAll(t, repo, "tracked file")
		content := bytes.Repeat([]byte("object"), 64)
		oid := integrationRunnerBlob(t, repo, content)

		poison := t.TempDir()
		poisonIndex := filepath.Join(poison, "index")
		if err := os.WriteFile(poisonIndex, nil, 0600); err != nil {
			t.Fatal(err)
		}
		poisonGlobal := filepath.Join(poison, "global.config")
		if err := os.WriteFile(poisonGlobal, []byte("[core]\n\tabbrev = 4\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_DIR", filepath.Join(poison, "wrong.git"))
		t.Setenv("GIT_WORK_TREE", poison)
		t.Setenv("GIT_INDEX_FILE", poisonIndex)
		t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(poison, "objects"))
		t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", poison)
		t.Setenv("GIT_COMMON_DIR", poison)
		t.Setenv("GIT_CONFIG_GLOBAL", poisonGlobal)
		t.Setenv("GIT_CONFIG_COUNT", "4")
		t.Setenv("GIT_CONFIG_KEY_0", "core.abbrev")
		t.Setenv("GIT_CONFIG_VALUE_0", "4")
		t.Setenv("GIT_CONFIG_KEY_1", "extensions.partialclone")
		t.Setenv("GIT_CONFIG_VALUE_1", "origin")
		t.Setenv("GIT_CONFIG_KEY_2", "remote.origin.promisor")
		t.Setenv("GIT_CONFIG_VALUE_2", "true")
		t.Setenv("GIT_CONFIG_KEY_3", "remote.origin.url")
		t.Setenv("GIT_CONFIG_VALUE_3", "/nonexistent-promisor.example.invalid")
		t.Setenv("GIT_NO_LAZY_FETCH", "0")

		probe := newIntegrationGitProbe(context.Background())

		top, err := probe.output(repo, nil, nil, "rev-parse", "--show-toplevel")
		if err != nil || top.ExitCode != 0 {
			t.Fatalf("repository identity followed the poisoned GIT_DIR: %+v %v", top, err)
		}
		gotTop := strings.TrimSpace(string(top.Stdout))
		if resolved, err := filepath.EvalSymlinks(gotTop); err == nil {
			gotTop = resolved
		}
		if wantTop, err := filepath.EvalSymlinks(repo); err == nil {
			repo = wantTop
		}
		if gotTop != repo {
			t.Fatalf("repository identity = %q, want the fixture %q", gotTop, repo)
		}

		status, err := probe.output(repo, nil, nil, "status", "--porcelain")
		if err != nil || status.ExitCode != 0 {
			t.Fatalf("index identity followed the poisoned GIT_INDEX_FILE: %+v %v", status, err)
		}
		if strings.TrimSpace(string(status.Stdout)) != "" {
			t.Fatalf("clean subject read through the poisoned index: %q", status.Stdout)
		}

		object, err := probe.output(repo, nil, nil, "cat-file", "-p", oid)
		if err != nil || object.ExitCode != 0 {
			t.Fatalf("object read followed the poisoned GIT_OBJECT_DIRECTORY: %+v %v", object, err)
		}
		if !bytes.Equal(object.Stdout, content) {
			t.Fatalf("object read carried %d bytes, want the committed content", len(object.Stdout))
		}

		abbrev, err := probe.output(repo, nil, nil, "rev-parse", "--short", "HEAD")
		if err != nil || abbrev.ExitCode != 0 {
			t.Fatalf("config identity followed the poisoned config family: %+v %v", abbrev, err)
		}
		if got := strings.TrimSpace(string(abbrev.Stdout)); len(got) < 7 {
			t.Fatalf("abbreviated HEAD %q carries the injected core.abbrev", got)
		}
	})

	t.Run("carries the fresh allowlist environment", func(t *testing.T) {
		t.Setenv("GIT_DIR", t.TempDir())
		t.Setenv("GIT_CONFIG_COUNT", "1")
		t.Setenv("GIT_CONFIG_KEY_0", "core.abbrev")
		t.Setenv("GIT_CONFIG_VALUE_0", "4")
		t.Setenv("GIT_NO_LAZY_FETCH", "0")

		probe := newIntegrationGitProbe(context.Background())
		want := []string{
			"LC_ALL=C",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_ATTR_NOSYSTEM=1",
			"GIT_NO_REPLACE_OBJECTS=1",
			"GIT_NO_LAZY_FETCH=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_OPTIONAL_LOCKS=0",
		}
		if path, ok := os.LookupEnv("PATH"); ok {
			want = append([]string{"PATH=" + path}, want...)
		}
		if got := probe.environment(); !reflect.DeepEqual(got, want) {
			t.Fatalf("environment = %q, want the exact fresh allowlist %q", got, want)
		}
	})

	t.Run("exposes the fixed merge configuration", func(t *testing.T) {
		probe := newIntegrationGitProbe(context.Background())
		want := []string{
			"merge.default=text",
			"merge.renames=true",
			"merge.directoryRenames=conflict",
			"merge.renormalize=false",
			"merge.conflictStyle=merge",
		}
		if got := probe.mergeConfigArgs(); !reflect.DeepEqual(got, want) {
			t.Fatalf("mergeConfigArgs = %q, want the exact fixed built-in merge settings %q", got, want)
		}
	})

	t.Run("applies the fixed merge configuration over repository config", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		gitRunStore(t, repo, "config", "merge.renames", "false")
		// Ten stable lines keep the rename similarity high; each side
		// changes only the one conflicting line, so detection sees a real
		// rename with a content conflict and not a delete-plus-add.
		stable := strings.Repeat("stable line for rename similarity\n", 10)
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte(stable+"conflict line: base\n"), 0600); err != nil {
			t.Fatal(err)
		}
		integrationRunnerCommitAll(t, repo, "base")
		gitRunStore(t, repo, "checkout", "-q", "-b", "theirs")
		gitRunStore(t, repo, "mv", "a.txt", "moved.txt")
		if err := os.WriteFile(filepath.Join(repo, "moved.txt"), []byte(stable+"conflict line: theirs\n"), 0600); err != nil {
			t.Fatal(err)
		}
		integrationRunnerCommitAll(t, repo, "rename and edit one line")
		gitRunStore(t, repo, "checkout", "-q", "main")
		gitRunStore(t, repo, "checkout", "-q", "-b", "mine")
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte(stable+"conflict line: mine\n"), 0600); err != nil {
			t.Fatal(err)
		}
		integrationRunnerCommitAll(t, repo, "edit the same line in place")

		probe := newIntegrationGitProbe(context.Background())
		got, err := probe.output(repo, nil, nil, "merge-tree", "--write-tree", "-z", "--no-messages", "mine", "theirs")
		if err != nil {
			t.Fatalf("rename conflict probe refused: %v", err)
		}
		pinned, err := parseIntegrationMergeTree(got.ExitCode, got.Stdout)
		if err != nil {
			t.Fatalf("pinned merge-tree output did not parse: %v", err)
		}
		renamed, original := false, false
		for _, stage := range pinned.Conflicts {
			switch stage.Path {
			case "moved.txt":
				renamed = true
			case "a.txt":
				original = true
			}
		}
		if !renamed || original {
			t.Fatalf("rename detection did not override the repository merge.renames=false: %+v", pinned.Conflicts)
		}

		// Negative control: the same conflicted invocation without the
		// probe's fixed -c prefix follows the repository
		// merge.renames=false, so the rename collapses to a delete/modify
		// conflict on the original path. cmd.Output keeps the exit-one
		// stdout the existing runner seam would drop. The pair proves the
		// pinned invocation, not a Git default, decides the shape.
		controlCmd := exec.Command("git", "-C", repo, "merge-tree", "--write-tree", "-z", "--no-messages", "mine", "theirs")
		controlRaw, controlErr := controlCmd.Output()
		var controlExit *exec.ExitError
		if !errors.As(controlErr, &controlExit) || controlExit.ExitCode() != 1 {
			t.Fatalf("negative-control merge-tree did not exit one: %v", controlErr)
		}
		control, err := parseIntegrationMergeTree(controlExit.ExitCode(), controlRaw)
		if err != nil {
			t.Fatalf("negative-control output did not parse: %v", err)
		}
		controlRenamed, controlOriginal := false, false
		for _, stage := range control.Conflicts {
			switch stage.Path {
			case "moved.txt":
				controlRenamed = true
			case "a.txt":
				controlOriginal = true
			}
		}
		if controlRenamed || !controlOriginal {
			t.Fatalf("the unpinned invocation did not follow the repository merge.renames=false: %+v", control.Conflicts)
		}
	})

	t.Run("never executes a shell", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		marker := filepath.Join(t.TempDir(), "shell-marker")
		probe := newIntegrationGitProbe(context.Background())

		for _, poisoned := range []string{
			"HEAD; touch " + marker,
			"HEAD$(touch " + marker + ")",
		} {
			got, err := probe.output(repo, nil, nil, "rev-parse", "--verify", "--quiet", poisoned)
			if err != nil {
				t.Fatalf("metacharacter argv %q became a transport refusal: %v", poisoned, err)
			}
			if got.ExitCode == 0 {
				t.Fatalf("git accepted the poisoned rev %q with exit zero", poisoned)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("the probe executed a shell for %q: marker stat = %v", poisoned, err)
			}
		}
	})

	t.Run("requires exit zero on the helper adapter", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("tracked\n"), 0600); err != nil {
			t.Fatal(err)
		}
		integrationRunnerCommitAll(t, repo, "tracked file")

		probe := newIntegrationGitProbe(context.Background())
		head, err := probe.Run(context.Background(), repo, "rev-parse", "--verify", "HEAD")
		if err != nil {
			t.Fatalf("default-ref helper refused a clean HEAD: %v", err)
		}
		if got := strings.TrimSpace(string(head)); len(got) != 40 {
			t.Fatalf("default-ref helper read %q, want a full object name", got)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := probe.Run(canceled, repo, "rev-parse", "--verify", "HEAD"); !errors.Is(err, context.Canceled) {
			t.Fatalf("adapter with a dead caller context = %v, want the context cause", err)
		}
		if _, err := probe.runZero(repo, nil, "rev-parse", "--verify", "--quiet", "definitely-missing-ref"); !errors.Is(err, errIntegrationProbeExitStatus) {
			t.Fatalf("nonzero exit surfaced as %v, want the exit-status sentinel", err)
		}
	})

	t.Run("refuses output whose pipe outlives the exit status", func(t *testing.T) {
		// The descendant holds the pipes past every exit status, so a
		// zero exit must refuse exactly like the conflicted exits: no
		// status turns an undrained pipe into a native success.
		for _, status := range []struct {
			name string
			code string
		}{
			{"exit zero", "0"},
			{"exit one", "1"},
			{"exit three", "3"},
		} {
			t.Run(status.name, func(t *testing.T) {
				repo := integrationRunnerRepo(t)
				gitRunStore(t, repo, "config", "alias.escape", "!printf partial; (sleep 1 &); exit "+status.code)
				got, err := newIntegrationGitProbe(context.Background()).output(repo, nil, nil, "escape")
				assertIntegrationProbeRefusal(t, got, err, integrationProbeRefusalPipe)
			})
		}
	})

	t.Run("kills a live group descendant when cancellation follows the child exit", func(t *testing.T) {
		repo := integrationRunnerRepo(t)
		// The alias writes the Git parent PID and the descendant PID to
		// the test-owned file, then blocks on the test-owned release
		// file until the test releases it. The descendant stays in the
		// Git child's process group, so group cancellation must reach it
		// even though the direct child has already exited and os/exec's
		// context watcher has already finished.
		gitRunStore(t, repo, "config", "alias.escape",
			"!sleep 30 & printf '%s %s\\n' \"$PPID\" \"$!\" > \"$INTEGRATION_PROBE_TEST_PIDS\"; while [ ! -f \"$INTEGRATION_PROBE_RELEASE\" ]; do sleep 0.01; done; printf 'held stdout\\n'; exit 0")
		pidPath := filepath.Join(t.TempDir(), "pids")
		releasePath := filepath.Join(t.TempDir(), "release")

		parent, cancel := context.WithCancel(context.Background())
		defer cancel()
		probe := newIntegrationGitProbe(parent)

		type probeOutcome struct {
			got integrationProbeResult
			err error
		}
		results := make(chan probeOutcome, 1)
		go func() {
			got, err := probe.output(repo, nil, []string{
				"INTEGRATION_PROBE_TEST_PIDS=" + pidPath,
				"INTEGRATION_PROBE_RELEASE=" + releasePath,
			}, "escape")
			results <- probeOutcome{got, err}
		}()

		var gitParent, descendant int
		awaitGitTestCondition(t, func() bool {
			data, err := os.ReadFile(pidPath)
			fields := strings.Fields(string(data))
			if err != nil || len(fields) != 2 {
				return false
			}
			gitParent, _ = strconv.Atoi(fields[0])
			descendant, _ = strconv.Atoi(fields[1])
			return gitParent > 0 && descendant > 0
		})
		t.Cleanup(func() {
			_ = os.WriteFile(releasePath, nil, 0600)
			if descendant > 0 {
				_ = syscall.Kill(descendant, syscall.SIGKILL)
			}
		})

		// The probe starts Git with Setpgid, so the direct child leads
		// its own group and the fixture descendant must share it.
		if group, err := syscall.Getpgid(descendant); err != nil || group != gitParent {
			t.Fatalf("descendant group = %d, %v; want the direct Git child's group %d", group, err, gitParent)
		}

		// Hold the probe's budget lock before releasing the alias: the
		// post-release stdout parks the pipe copy inside Write on this
		// mutex, so Cmd.Wait cannot finish drainage even after WaitDelay
		// closes the pipes. The probe therefore cannot report anything
		// until the scoped release, and the cancellation below races
		// nothing. The scoped defer releases the mutex on every failure
		// path.
		probe.mu.Lock()
		func() {
			defer probe.mu.Unlock()
			if err := os.WriteFile(releasePath, nil, 0600); err != nil {
				t.Fatal(err)
			}
			// Wait for the direct Git child to exit while the
			// descendant still holds the pipes open.
			awaitGitTestCondition(t, func() bool {
				return errors.Is(syscall.Kill(gitParent, 0), syscall.ESRCH)
			})
			if err := syscall.Kill(descendant, 0); err != nil {
				t.Fatalf("descendant %d died before cancellation: %v", descendant, err)
			}
			cancel()
		}()

		select {
		case outcome := <-results:
			assertIntegrationProbeRefusal(t, outcome.got, outcome.err, integrationProbeRefusalCanceled)
			if !errors.Is(outcome.err, context.Canceled) {
				t.Fatalf("canceled refusal %v does not carry the context cause", outcome.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancellation did not bound the probe's post-exit cleanup")
		}
		awaitGitTestCondition(t, func() bool {
			data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(descendant), "stat"))
			return errors.Is(err, os.ErrNotExist) || strings.Contains(string(data), ") Z ")
		})
	})
}

// ---------------------------------------------------------------------------
// Private object-view conformance.

// integrationViewTempRoot sandboxes the effective temporary directory for
// one subtest, so every view directory lands inside a test-owned root where
// leaks are enumerable and failure cleanup is automatic.
func integrationViewTempRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "views")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", root)
	return root
}

// integrationViewSource creates the fixture source repository under name,
// which may carry separator and quote characters, and commits one tracked
// file so the source holds a real index, refs, and objects.
func integrationViewSource(t *testing.T, name string, initArgs ...string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, append([]string{"init", "-q", "-b", "main"}, initArgs...)...)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("source fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	integrationRunnerCommitAll(t, repo, "source fixture base")
	return repo
}

// integrationViewWriteAll writes files at their repository-relative paths,
// creating parent directories.
func integrationViewWriteAll(t *testing.T, repo string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(repo, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// integrationViewResolve resolves one revision to its full object name.
func integrationViewResolve(t *testing.T, repo, rev string) string {
	t.Helper()
	out, err := ExecGitRunner{}.Run(context.Background(), repo, "rev-parse", "--verify", rev)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// integrationViewObjectInventory lists every entry under the source objects
// directory, sorted, as the object-store identity the view must not change.
func integrationViewObjectInventory(t *testing.T, repo string) []string {
	t.Helper()
	objects := filepath.Join(repo, ".git", "objects")
	var found []string
	err := filepath.WalkDir(objects, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(objects, path)
		if relErr != nil {
			return relErr
		}
		found = append(found, relative)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(found)
	return found
}

// integrationViewState is the source identity snapshot: refs, config file
// bytes, index file bytes, and the object inventory.
type integrationViewState struct {
	refs    string
	config  []byte
	index   []byte
	objects []string
}

// integrationViewSnapshot takes one source identity snapshot.
func integrationViewSnapshot(t *testing.T, repo string) integrationViewState {
	t.Helper()
	refs, err := ExecGitRunner{}.Run(context.Background(), repo, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	return integrationViewState{
		refs:    string(refs),
		config:  config,
		index:   index,
		objects: integrationViewObjectInventory(t, repo),
	}
}

// integrationViewAssertUntouched proves the source repository still holds
// exactly the snapshot it was taken from.
func integrationViewAssertUntouched(t *testing.T, repo string, before integrationViewState) {
	t.Helper()
	after := integrationViewSnapshot(t, repo)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("source repository changed: refs %q -> %q, config %d -> %d bytes, index %d -> %d bytes, objects %d -> %d entries",
			before.refs, after.refs, len(before.config), len(after.config), len(before.index), len(after.index), len(before.objects), len(after.objects))
	}
}

// integrationViewRun runs one command inside the view through the probe and
// requires a clean exit.
func integrationViewRun(t *testing.T, probe *integrationGitProbe, view *integrationObjectView, args ...string) integrationProbeResult {
	t.Helper()
	got, err := probe.output(view.dir, nil, view.env, args...)
	if err != nil {
		t.Fatalf("view command git %v refused: %v", args, err)
	}
	if got.ExitCode != 0 {
		t.Fatalf("view command git %v exited %d: %s", args, got.ExitCode, got.Stderr)
	}
	return got
}

// integrationViewGitRefused proves a direct source-side git command fails,
// such as reading an object only the view holds.
func integrationViewGitRefused(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := (ExecGitRunner{}).Run(context.Background(), dir, args...); err == nil {
		t.Fatalf("git %v succeeded in %s, want refusal", args, dir)
	}
}

// integrationViewLeakCheck proves the temporary root holds no leftover view
// directory after a close or a refused construction.
func integrationViewLeakCheck(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("temporary root holds leftover view directories %q", names)
	}
}

// integrationViewAttrFiles are the tracked paths the attribute fixtures
// share: one path per merge spelling plus a nested directory path.
var integrationViewAttrFiles = map[string]string{
	"union.txt":       "union content\n",
	"bin.data":        "binary content\n",
	"src.code":        "text content\n",
	"keep.md":         "kept content\n",
	"no.txt":          "unset content\n",
	"plain.dat":       "plain content\n",
	"README.md":       "source fixture\n",
	"docs/nested.txt": "nested content\n",
}

// integrationViewCleanAttrs are the attribute rules of commit A. The unset
// rule follows the glob so the last match decides for no.txt.
const integrationViewCleanAttrs = `*.txt merge=union
bin.data merge=binary
*.code merge=text
keep.md merge
no.txt -merge
`

// integrationViewAttrValues are the exact cached merge values every commit A
// with the clean rules must report, including the tracked rules file itself.
var integrationViewAttrValues = map[string]string{
	".gitattributes":  "unspecified",
	"README.md":       "unspecified",
	"union.txt":       "union",
	"bin.data":        "binary",
	"src.code":        "text",
	"keep.md":         "set",
	"no.txt":          "unset",
	"plain.dat":       "unspecified",
	"docs/nested.txt": "union",
}

// integrationViewUnspecifiedValues are the exact cached merge values for the
// attribute-free base commit: every shared path, all unspecified.
func integrationViewUnspecifiedValues() map[string]string {
	values := make(map[string]string, len(integrationViewAttrFiles))
	for path := range integrationViewAttrFiles {
		values[path] = "unspecified"
	}
	return values
}

// integrationViewAttrRepo builds the attribute fixture: a base commit with
// every shared path and no attribute file, then commit A adding the supplied
// .gitattributes content, the extra files, and a diverged nested path so A
// and the base hold distinct trees everywhere attributes could differ.
func integrationViewAttrRepo(t *testing.T, attrs string, extra map[string]string) string {
	t.Helper()
	repo := integrationViewSource(t, "subject")
	integrationViewWriteAll(t, repo, integrationViewAttrFiles)
	integrationRunnerCommitAll(t, repo, "attribute base")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte(attrs), 0600); err != nil {
		t.Fatal(err)
	}
	nested, err := os.ReadFile(filepath.Join(repo, "docs", "nested.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "docs", "nested.txt"), append(nested, "a-side\n"...), 0600); err != nil {
		t.Fatal(err)
	}
	integrationViewWriteAll(t, repo, extra)
	integrationRunnerCommitAll(t, repo, "attribute rules")
	return repo
}

// TestIntegrationNativeObjectView drives the private object view
// contract with deterministic real-Git fixtures.
func TestIntegrationNativeObjectView(t *testing.T) {
	t.Run("initializes a bare private view in the derived source format", func(t *testing.T) {
		root := integrationViewTempRoot(t)
		repo := integrationViewSource(t, "subject")
		before := integrationViewSnapshot(t, repo)

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatalf("view construction refused for a supported source: %v", err)
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil || len(entries) != 1 {
			t.Fatalf("view construction left %d entries under the temporary root (err %v), want exactly the one view", len(entries), readErr)
		}
		info, err := os.Stat(view.dir)
		if err != nil {
			t.Fatal(err)
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			t.Fatalf("view directory mode = %v, want a 0700 directory", info.Mode())
		}
		if bare := integrationViewRun(t, probe, view, "rev-parse", "--is-bare-repository"); strings.TrimSpace(string(bare.Stdout)) != "true" {
			t.Fatalf("view repository is not bare: %q", bare.Stdout)
		}
		if format := integrationViewRun(t, probe, view, "rev-parse", "--show-object-format"); strings.TrimSpace(string(format.Stdout)) != "sha1" {
			t.Fatalf("view object format = %q, want the derived sha1", format.Stdout)
		}
		if refs := integrationViewRun(t, probe, view, "for-each-ref"); len(refs.Stdout) != 0 {
			t.Fatalf("fresh view carries refs: %q", refs.Stdout)
		}
		wantEnv := []string{
			"GIT_INDEX_FILE=" + filepath.Join(view.dir, "index"),
			"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strconv.Quote(filepath.Join(repo, ".git", "objects")),
		}
		if !reflect.DeepEqual(view.env, wantEnv) {
			t.Fatalf("view env = %q, want exactly the private index and the quoted original objects %q", view.env, wantEnv)
		}
		integrationViewAssertUntouched(t, repo, before)
	})

	t.Run("derives the sha256 source format into the view", func(t *testing.T) {
		integrationViewTempRoot(t)
		repo := integrationViewSource(t, "hashed", "--object-format=sha256")
		before := integrationViewSnapshot(t, repo)

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatalf("view construction refused for a sha256 source: %v", err)
		}
		if got := integrationViewRun(t, probe, view, "rev-parse", "--show-object-format"); strings.TrimSpace(string(got.Stdout)) != "sha256" {
			t.Fatalf("view object format = %q, want the derived sha256", got.Stdout)
		}
		tree := integrationViewResolve(t, repo, "HEAD^{tree}")
		integrationViewRun(t, probe, view, "cat-file", "-e", tree)
		integrationViewAssertUntouched(t, repo, before)
	})

	t.Run("reads original objects read-only through quoted alternates", func(t *testing.T) {
		integrationViewTempRoot(t)
		repo := integrationViewSource(t, `we:ird sour"ce`)
		before := integrationViewSnapshot(t, repo)

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatalf("view construction refused for a separator and quote source path: %v", err)
		}
		wantEnv := []string{
			"GIT_INDEX_FILE=" + filepath.Join(view.dir, "index"),
			"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strconv.Quote(filepath.Join(repo, ".git", "objects")),
		}
		if !reflect.DeepEqual(view.env, wantEnv) {
			t.Fatalf("view env = %q, want the quoted original objects spelling %q", view.env, wantEnv)
		}

		tree := integrationViewResolve(t, repo, "HEAD^{tree}")
		integrationViewRun(t, probe, view, "cat-file", "-e", tree)

		created, err := probe.output(view.dir, bytes.NewReader([]byte("view-only object\n")), view.env, "hash-object", "-w", "--stdin")
		if err != nil || created.ExitCode != 0 {
			t.Fatalf("view object write refused: %+v %v", created, err)
		}
		oid := strings.TrimSpace(string(created.Stdout))
		integrationViewRun(t, probe, view, "cat-file", "-e", oid)
		integrationViewGitRefused(t, repo, "cat-file", "-e", oid)
		integrationViewAssertUntouched(t, repo, before)
	})

	t.Run("keeps the private index and the derived tree out of the source", func(t *testing.T) {
		integrationViewTempRoot(t)
		repo := integrationViewSource(t, "subject")
		before := integrationViewSnapshot(t, repo)

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatal(err)
		}
		integrationViewRun(t, probe, view, "read-tree", integrationViewResolve(t, repo, "HEAD"))
		if _, err := os.Stat(filepath.Join(view.dir, "index")); err != nil {
			t.Fatalf("private index missing after read-tree: %v", err)
		}
		paths := integrationViewRun(t, probe, view, "ls-files", "-z")
		if got := strings.Split(strings.TrimRight(string(paths.Stdout), "\x00"), "\x00"); len(got) != 1 || got[0] != "README.md" {
			t.Fatalf("view index enumerated %q, want exactly the source tracked path", paths.Stdout)
		}

		blob := integrationViewResolve(t, repo, "HEAD:README.md")
		treeInput := "100644 blob " + blob + "\tview-only.tree\n"
		wrote, err := probe.output(view.dir, strings.NewReader(treeInput), view.env, "mktree")
		if err != nil || wrote.ExitCode != 0 {
			t.Fatalf("view tree write refused: %+v %v", wrote, err)
		}
		treeOID := strings.TrimSpace(string(wrote.Stdout))
		if kind := integrationViewRun(t, probe, view, "cat-file", "-t", treeOID); strings.TrimSpace(string(kind.Stdout)) != "tree" {
			t.Fatalf("view object %q is not a tree", treeOID)
		}
		integrationViewGitRefused(t, repo, "cat-file", "-e", treeOID)
		integrationViewAssertUntouched(t, repo, before)
	})

	t.Run("leaves the source repository untouched across the whole view lifetime", func(t *testing.T) {
		integrationViewTempRoot(t)
		repo := integrationViewAttrRepo(t, integrationViewCleanAttrs, nil)
		before := integrationViewSnapshot(t, repo)

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := probe.checkIntegrationMergeAttributes(view, integrationViewResolve(t, repo, "HEAD")); err != nil {
			t.Fatalf("attribute check refused a clean source: %v", err)
		}
		wrote, err := probe.output(view.dir, bytes.NewReader(nil), view.env, "hash-object", "-w", "--stdin")
		if err != nil || wrote.ExitCode != 0 {
			t.Fatalf("view object write refused: %+v %v", wrote, err)
		}
		integrationViewAssertUntouched(t, repo, before)
		if err := view.close(); err != nil {
			t.Fatalf("close refused: %v", err)
		}
		integrationViewAssertUntouched(t, repo, before)
	})

	t.Run("returns exact cached merge values for every tracked path on each ref", func(t *testing.T) {
		integrationViewTempRoot(t)
		repo := integrationViewAttrRepo(t, integrationViewCleanAttrs, nil)
		before := integrationViewSnapshot(t, repo)

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatal(err)
		}
		values, err := probe.checkIntegrationMergeAttributes(view, integrationViewResolve(t, repo, "HEAD"), integrationViewResolve(t, repo, "HEAD~1"))
		if err != nil {
			t.Fatalf("cached attribute check refused the clean parents: %v", err)
		}
		want := []map[string]string{integrationViewAttrValues, integrationViewUnspecifiedValues()}
		if !reflect.DeepEqual(values, want) {
			t.Fatalf("cached merge values = %v, want the exact per-ref maps %v", values, want)
		}
		integrationViewAssertUntouched(t, repo, before)
	})

	t.Run("rejects named external drivers on any ref without executing them", func(t *testing.T) {
		integrationViewTempRoot(t)
		repo := integrationViewAttrRepo(t, integrationViewCleanAttrs+"*.evil merge=evil-driver\n", map[string]string{"evil.evil": "driver content\n"})
		marker := filepath.Join(t.TempDir(), "driver-marker")
		gitRunStore(t, repo, "config", "merge.evil-driver.driver", "touch "+marker)
		if configured, err := (ExecGitRunner{}).Run(context.Background(), repo, "config", "--get", "merge.evil-driver.driver"); err != nil || strings.TrimSpace(string(configured)) != "touch "+marker {
			t.Fatalf("driver poison config missing: %q %v", configured, err)
		}
		before := integrationViewSnapshot(t, repo)

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatal(err)
		}
		base := integrationViewResolve(t, repo, "HEAD~1")
		head := integrationViewResolve(t, repo, "HEAD")

		if values, err := probe.checkIntegrationMergeAttributes(view, head); err == nil {
			t.Fatalf("driver-valued path measured %+v; the named external driver must refuse", values)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("driver marker stat = %v; the source driver command must never execute", err)
		}
		if _, err := probe.checkIntegrationMergeAttributes(view, head, base); err == nil {
			t.Fatal("a driver-valued parent must refuse the whole multi-ref check")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("driver marker stat = %v; the source driver command must never execute", err)
		}
		values, err := probe.checkIntegrationMergeAttributes(view, base)
		if err != nil {
			t.Fatalf("the clean merge-base ref refused: %v", err)
		}
		if want := []map[string]string{integrationViewUnspecifiedValues()}; !reflect.DeepEqual(values, want) {
			t.Fatalf("clean base values = %v, want the unspecified map %v", values, want)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("driver marker stat = %v; the source driver command must never execute", err)
		}
		integrationViewAssertUntouched(t, repo, before)
	})

	t.Run("refuses missing original objects without a fetch attempt", func(t *testing.T) {
		integrationViewTempRoot(t)
		repo := integrationViewAttrRepo(t, integrationViewCleanAttrs, nil)
		marker := filepath.Join(t.TempDir(), "fetch-marker")
		helper := filepath.Join(t.TempDir(), "fakebin")
		if err := os.MkdirAll(helper, 0700); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\nprintf 'fetch-attempt\\n' > " + marker + "\nexit 1\n"
		if err := os.WriteFile(filepath.Join(helper, "git-remote-evil"), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		gitRunStore(t, repo, "config", "remote.origin.promisor", "true")
		gitRunStore(t, repo, "config", "remote.origin.partialclonefilter", "blob:none")
		gitRunStore(t, repo, "config", "extensions.partialclone", "origin")
		gitRunStore(t, repo, "config", "remote.origin.url", "evil::promisor-fetch")

		// Remove commit A's docs subtree from the original objects: the
		// view must refuse rather than fetch or borrow the source's
		// promisor configuration. The base commit holds its own intact
		// docs subtree, so only the A side loses an object.
		docs := integrationViewResolve(t, repo, "HEAD:docs")
		if err := os.Remove(filepath.Join(repo, ".git", "objects", docs[:2], docs[2:])); err != nil {
			t.Fatal(err)
		}
		before := integrationViewSnapshot(t, repo)
		t.Setenv("PATH", helper+string(os.PathListSeparator)+os.Getenv("PATH"))

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatalf("view construction refused with a missing original tree: %v", err)
		}
		base := integrationViewResolve(t, repo, "HEAD~1")
		head := integrationViewResolve(t, repo, "HEAD")

		if values, err := probe.checkIntegrationMergeAttributes(view, head); err == nil {
			t.Fatalf("attribute check measured %+v across a missing original tree; it must refuse", values)
		}
		values, err := probe.checkIntegrationMergeAttributes(view, base)
		if err != nil {
			t.Fatalf("the intact merge-base ref refused: %v", err)
		}
		if want := []map[string]string{integrationViewUnspecifiedValues()}; !reflect.DeepEqual(values, want) {
			t.Fatalf("base values = %v, want the unspecified map %v", values, want)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("fetch marker stat = %v; the probe must never attempt a promisor fetch", err)
		}
		integrationViewAssertUntouched(t, repo, before)
	})

	t.Run("closes the view and reports cleanup errors", func(t *testing.T) {
		root := integrationViewTempRoot(t)
		repo := integrationViewSource(t, "subject")
		before := integrationViewSnapshot(t, repo)

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(view.dir, root+string(os.PathSeparator)) {
			t.Fatalf("view directory %q escaped the temporary root %q", view.dir, root)
		}
		if err := view.close(); err != nil {
			t.Fatalf("close refused a clean view: %v", err)
		}
		if _, err := os.Stat(view.dir); !os.IsNotExist(err) {
			t.Fatalf("closed view directory stat = %v, want removal", err)
		}
		integrationViewLeakCheck(t, root)
		integrationViewAssertUntouched(t, repo, before)

		view2, err := probe.newIntegrationObjectView(repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(root, 0500); err != nil {
			t.Fatal(err)
		}
		if err := view2.close(); err == nil {
			t.Fatal("close swallowed the removal failure")
		}
		if _, err := os.Stat(view2.dir); err != nil {
			t.Fatalf("view directory vanished despite the reported cleanup failure: %v", err)
		}
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := view2.close(); err != nil {
			t.Fatalf("close refused after restoring permissions: %v", err)
		}
		integrationViewLeakCheck(t, root)
	})

	t.Run("refuses construction from an underivable source and leaves no view", func(t *testing.T) {
		root := integrationViewTempRoot(t)
		source := filepath.Join(t.TempDir(), "not-a-repository")
		if err := os.MkdirAll(source, 0700); err != nil {
			t.Fatal(err)
		}

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(source)
		if err == nil {
			t.Fatalf("construction accepted a non-repository source; view %q", view.dir)
		}
		integrationViewLeakCheck(t, root)
	})

	t.Run("removes a created view when initialization fails", func(t *testing.T) {
		root := integrationViewTempRoot(t)
		repo := integrationViewSource(t, "subject")

		// Fault injection for the failure-cleanup path, not native
		// acceptance proof: the shim delegates every metadata command to
		// the real Git and makes only the view's init fail with an
		// invalid object-format argument, after the constructor already
		// created its temporary directory. The probe prefixes its fixed
		// merge settings, so the scan cannot match on argv position.
		realGit, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		shim := "#!/bin/sh\n" +
			"for a in \"$@\"; do\n" +
			"  if [ \"$a\" = init ]; then\n" +
			"    exec " + realGit + " init -q --bare --template= --object-format=definitely-not-an-object-format\n" +
			"  fi\n" +
			"done\n" +
			"exec " + realGit + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(shim), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		probe := newIntegrationGitProbe(context.Background())
		view, err := probe.newIntegrationObjectView(repo)
		if err == nil {
			t.Fatalf("fault-injected init succeeded; view %q", view.dir)
		}
		if view != nil {
			t.Fatalf("failed construction returned a view at %q", view.dir)
		}
		if !errors.Is(err, errIntegrationProbeExitStatus) {
			t.Fatalf("init failure surfaced as %v, want the nonzero-exit refusal", err)
		}
		integrationViewLeakCheck(t, root)
	})
}

// TestIntegrationNativeObjectViewFraming pins the NUL framing contract of
// parseIntegrationMergeAttrs directly: the attribute output must be
// NUL-terminated and carry exactly one complete path, merge, value triple
// per enumerated path, in the enumerated order. Unterminated, malformed,
// empty, misaligned, and incomplete frames refuse through the typed store
// Failure; the terminated frame and the empty tree return exact maps.
// Termination and empty-record refusals come from the shared
// sliceDiffTokens recognizer both NUL streams reuse.
func TestIntegrationNativeObjectViewFraming(t *testing.T) {
	paths := []string{"union.txt", "docs/nested.txt"}
	terminated := []byte("union.txt\x00merge\x00union\x00docs/nested.txt\x00merge\x00union\x00")
	want := map[string]string{"union.txt": "union", "docs/nested.txt": "union"}

	t.Run("accepts the complete terminated frame", func(t *testing.T) {
		values, err := parseIntegrationMergeAttrs(paths, terminated)
		if err != nil {
			t.Fatalf("terminated frame refused: %v", err)
		}
		if !reflect.DeepEqual(values, want) {
			t.Fatalf("values = %v, want %v", values, want)
		}
	})

	t.Run("refuses output missing the final NUL", func(t *testing.T) {
		_, err := parseIntegrationMergeAttrs(paths, bytes.TrimSuffix(terminated, []byte{0}))
		assertFailureKind(t, err, KindInvalidPayload)
	})

	t.Run("refuses a frame whose attribute is not merge", func(t *testing.T) {
		_, err := parseIntegrationMergeAttrs(paths, []byte("union.txt\x00attr\x00union\x00docs/nested.txt\x00merge\x00union\x00"))
		assertFailureKind(t, err, KindInvalidPayload)
	})

	t.Run("refuses a misaligned frame", func(t *testing.T) {
		_, err := parseIntegrationMergeAttrs(paths, []byte("docs/nested.txt\x00merge\x00union\x00union.txt\x00merge\x00union\x00"))
		assertFailureKind(t, err, KindInvalidPayload)
	})

	t.Run("refuses an empty frame for tracked paths", func(t *testing.T) {
		_, err := parseIntegrationMergeAttrs(paths, nil)
		assertFailureKind(t, err, KindInvalidPayload)
	})

	t.Run("refuses an incomplete final triple", func(t *testing.T) {
		_, err := parseIntegrationMergeAttrs(paths, []byte("union.txt\x00merge\x00union\x00docs/nested.txt\x00merge\x00"))
		assertFailureKind(t, err, KindInvalidPayload)
	})

	t.Run("accepts an empty frame for an empty tree", func(t *testing.T) {
		values, err := parseIntegrationMergeAttrs(nil, nil)
		if err != nil {
			t.Fatalf("empty tree frame refused: %v", err)
		}
		if len(values) != 0 {
			t.Fatalf("values = %v, want an empty map", values)
		}
	})
}
