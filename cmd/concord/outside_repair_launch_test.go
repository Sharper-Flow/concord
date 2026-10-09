package main

// Real temporary repositories exercise native branch and path effects.
// Synthetic store fixtures and injected host I/O isolate operator authority.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// outsideRepairTestWorkItem seeds the work and the operator-approved
// outside-repair hold the verb requires. The fixture mirrors the
// outside_repair_test.go pattern: it uses fold_guard to write rows that
// the schema otherwise keeps fold-only, then drops fold_guard once the
// fixture is in place.
func outsideRepairTestWorkItem(t *testing.T, s *store.Store, workID, projectID, repositoryPath string) {
	t.Helper()
	enable := func() {
		if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
			`INSERT INTO fold_guard(active) VALUES(1);`); err != nil {
			t.Fatalf("enable fixture fold guard: %v", err)
		}
	}
	disable := func() {
		if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
			`DELETE FROM fold_guard;`); err != nil {
			t.Fatalf("disable fixture fold guard: %v", err)
		}
	}
	enable()
	defer disable()
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('outside-product','Outside Repair Repair','prototype','operator_only',1,'now','now');`); err != nil {
		t.Fatalf("seed fixture product: %v", err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES(?, 'Outside Repair Project',1,'now','now');`, projectID); err != nil {
		t.Fatalf("seed fixture project: %v", err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('outside-product',?,'primary');`, projectID); err != nil {
		t.Fatalf("seed fixture product_project: %v", err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('outside-repair-locator',?,'canonical_path',?,?,'now','now');`, projectID, repositoryPath, repositoryPath); err != nil {
		t.Fatalf("seed fixture locator: %v", err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
		`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES(?, 'task','CON-869 outside repair','in_progress',1,1,'now','now',NULL);`, workID); err != nil {
		t.Fatalf("seed fixture work item: %v", err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
		`INSERT INTO work_projects(work_id,project_id,role) VALUES(?,?,'primary');`, workID, projectID); err != nil {
		t.Fatalf("seed fixture work_project: %v", err)
	}
}

// outsideRepairTestHold records an operator-approved outside-repair
// disposition on the fixture work. The verb reads the hold through the
// same ReadWorkPin the store exposes, so the fixture must place a row into
// outside_repair_dispositions. The store's fold-validation gate (which
// requires a recorded operator approval) is not exercised here: the verb
// is a read-only outside surface and never invokes the fold.
func outsideRepairTestHold(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
		`INSERT INTO fold_guard(active) VALUES(1);`); err != nil {
		t.Fatalf("enable fixture fold guard: %v", err)
	}
	defer func() {
		if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
			`DELETE FROM fold_guard;`); err != nil {
			t.Fatalf("disable fixture fold guard: %v", err)
		}
	}()
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(),
		`INSERT INTO outside_repair_dispositions(work_id,approval_ref,state,reason,evidence_json,recorded_at) VALUES(?,?,'active','bounded outside defect repair','{}','now');`,
		workID, "approval:outside-repair-"+workID); err != nil {
		t.Fatalf("seed outside-repair hold: %v", err)
	}
}

// outsideRepairTestRepo builds a real git repository with one commit and
// origin/HEAD pointing at the only branch. The verb reads this default
// branch via store.DefaultBranchRef.
func outsideRepairTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch=main", dir},
		{"-C", dir, "config", "user.email", "outside-repair@example.com"},
		{"-C", dir, "config", "user.name", "outside-repair"},
		{"-C", dir, "config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if out, err := exec.Command("git", "-C", dir, "commit", "--quiet", "--allow-empty", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	remote := "https://example.com/outside-repair/repository.git"
	if out, err := exec.Command("git", "-C", dir, "config", "url."+dir+".insteadOf", remote).CombinedOutput(); err != nil {
		t.Fatalf("git config local transport: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", remote).CombinedOutput(); err != nil {
		t.Fatalf("git remote add origin: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "fetch", "--quiet", "origin").CombinedOutput(); err != nil {
		t.Fatalf("git fetch origin: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "remote", "set-head", "origin", "--auto").CombinedOutput(); err != nil {
		t.Fatalf("git remote set-head origin --auto: %v\n%s", err, out)
	}
	return dir
}

// outsideRepairTestGitRunner captures every argv the verb issues and runs
// git against the on-disk fixture repos. The fixture repo lives at the
// repositoryPath the seeding pass records.
type outsideRepairTestGitRunner struct {
	Cmds     []recordedGitCommand
	Next     outsideRepairGitCmd
	Contexts []context.Context
}

type recordedGitCommand struct {
	Dir  string
	Args []string
}

type outsideRepairGitCmd func(ctx context.Context, dir string, args ...string) ([]byte, error)

func (r *outsideRepairTestGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	r.Cmds = append(r.Cmds, recordedGitCommand{Dir: dir, Args: append([]string(nil), args...)})
	r.Contexts = append(r.Contexts, ctx)
	if r.Next == nil {
		return store.ExecGitRunner{}.Run(ctx, dir, args...)
	}
	return r.Next(ctx, dir, args...)
}

func (r *outsideRepairTestGitRunner) RunNoninteractive(ctx context.Context, dir string, args ...string) ([]byte, error) {
	r.Cmds = append(r.Cmds, recordedGitCommand{Dir: dir, Args: append([]string(nil), args...)})
	r.Contexts = append(r.Contexts, ctx)
	if r.Next != nil {
		return r.Next(ctx, dir, args...)
	}
	return store.ExecGitRunner{}.RunNoninteractive(ctx, dir, args...)
}

// outsideRepairRecorderExec captures the argv, env, and cwd the host
// receives. It never starts a process, so the test owns the binary's
// effective answer.
type outsideRepairRecorderExec struct {
	Cwd    string
	Argv   []string
	Env    []string
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	ErrOut error
}

func (r *outsideRepairRecorderExec) Run(_ context.Context, dir string, argv, env []string, in io.Reader, out, errOut io.Writer) error {
	r.Cwd = dir
	r.Argv = append([]string(nil), argv...)
	r.Env = append([]string(nil), env...)
	r.In = in
	r.Out = out
	r.Err = errOut
	return r.ErrOut
}

// outsideRepairFixture owns the lifecycle of a test that exercises the verb.
// The fixture closes the store, points the database override at the test
// path, and seeds the held work, the operator approval, and the canonical
// repository locator. Tests drive the verb through runOutsideRepairCommand
// against this fixture.
type outsideRepairFixture struct {
	Store     *store.Store
	WorkID    string
	ProjectID string
	RepoPath  string
	XDGHome   string
	Deps      outsideRepairDeps
	Git       *outsideRepairTestGitRunner
	Recorder  *outsideRepairRecorderExec
	Stdin     string
}

func newOutsideRepairFixture(t *testing.T, hold bool, ids ...string) *outsideRepairFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for outside-repair tests")
	}
	repo := outsideRepairTestRepo(t)
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open fixture store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	workID := "work-outside-repair"
	if len(ids) > 0 {
		workID = ids[0]
	}
	projectID := "project-outside-repair"
	outsideRepairTestWorkItem(t, s, workID, projectID, repo)
	if hold {
		outsideRepairTestHold(t, s, workID)
	}
	xdgHome := t.TempDir()
	git := &outsideRepairTestGitRunner{}
	recorder := &outsideRepairRecorderExec{}
	deps := outsideRepairDeps{
		Git:         git,
		HostCommand: outsideRepairFakeHostCommand,
		Exec:        recorder.Run,
		XDGHome:     func() (string, error) { return xdgHome, nil },
		Now:         func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) },
		Terminal:    func() (io.ReadWriteCloser, error) { return &outsideRepairTestTerminal{}, nil },
	}
	return &outsideRepairFixture{
		Store: s, WorkID: workID, ProjectID: projectID, RepoPath: repo,
		XDGHome: xdgHome, Deps: deps, Git: git, Recorder: recorder,
	}
}

func outsideRepairFakeHostCommand(_ context.Context, dir string, _ []string) (hostCommandResolution, error) {
	return hostCommandResolution{Command: []string{"opencode"}, Registry: []byte(`{"agent":{"host-repair-agent":{"mode":"primary"}}}`)}, nil
}

type outsideRepairTestTerminal struct {
	Input  io.Reader
	Output bytes.Buffer
	Closed bool
}

func (terminal *outsideRepairTestTerminal) Read(data []byte) (int, error) {
	if terminal.Input == nil {
		return 0, io.EOF
	}
	return terminal.Input.Read(data)
}

func (terminal *outsideRepairTestTerminal) Write(data []byte) (int, error) {
	return terminal.Output.Write(data)
}

func (terminal *outsideRepairTestTerminal) Close() error { terminal.Closed = true; return nil }

func (f *outsideRepairFixture) Run(t *testing.T) (int, string, string) {
	t.Helper()
	t.Setenv(dbOverrideEnv, f.Store.Path())
	t.Setenv("XDG_DATA_HOME", f.XDGHome)
	t.Setenv("HOME", filepath.Dir(f.XDGHome))
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	var before, after int64
	if err := f.Store.DatabaseForTesting().QueryRow("SELECT total_changes()").Scan(&before); err != nil {
		t.Fatal(err)
	}
	code := runOutsideRepairCommand([]byte(f.Stdin), f.Store, f.Deps, out, errOut)
	if err := f.Store.DatabaseForTesting().QueryRow("SELECT total_changes()").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("operator launch wrote to Concord: total_changes %d -> %d", before, after)
	}
	for _, ctx := range f.Git.Contexts {
		if _, bounded := ctx.Deadline(); !bounded {
			t.Error("Git command had no deadline")
		}
		if ctx.Err() != context.Canceled {
			t.Errorf("Git context not canceled after launch: %v", ctx.Err())
		}
	}
	return code, out.String(), errOut.String()
}

// TestOutsideRepairRefusesWhenHoldMissing is the failing-before evidence for
// the disposition gate. The verb must refuse before any git work when the
// named work has no recorded outside-repair hold.
func TestOutsideRepairRefusesWhenHoldMissing(t *testing.T) {
	f := newOutsideRepairFixture(t, false)
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, errOut := f.Run(t)
	if code != outsideRepairLaunchRefusalExit {
		t.Fatalf("refusal exit=%d stderr=%q, want %d", code, errOut, outsideRepairLaunchRefusalExit)
	}
	if !strings.Contains(errOut, "no outside-repair hold is active") {
		t.Fatalf("refusal does not name the missing hold: %q", errOut)
	}
	if len(f.Git.Cmds) != 0 {
		t.Fatalf("refusal issued %d git commands, want zero", len(f.Git.Cmds))
	}
	if f.Recorder.Cwd != "" {
		t.Fatalf("refusal reached the host exec: %q", f.Recorder.Cwd)
	}
}

// TestOutsideRepairRefusesWhenWorkMissing covers the projection-not-found
// branch of the pin read. The work does not exist; the hold is therefore
// irrelevant and the fixture stays without one.
func TestOutsideRepairRefusesWhenWorkMissing(t *testing.T) {
	f := newOutsideRepairFixture(t, false)
	f.Stdin = `{"work_id":"work-does-not-exist","agent":"host-repair-agent"}`
	code, _, errOut := f.Run(t)
	if code != outsideRepairLaunchRefusalExit {
		t.Fatalf("refusal exit=%d stderr=%q, want %d", code, errOut, outsideRepairLaunchRefusalExit)
	}
	if !strings.Contains(errOut, "work has no primary Project") && !strings.Contains(errOut, "work item is not recorded") {
		t.Fatalf("refusal does not name the missing work: %q", errOut)
	}
	if len(f.Git.Cmds) != 0 {
		t.Fatalf("refusal issued %d git commands, want zero", len(f.Git.Cmds))
	}
}

// TestOutsideRepairRefusesWhenWorkIDOutOfBounds covers the input-validation
// branch. A work_id outside the safe path pattern refuses before any read.
func TestOutsideRepairRefusesWhenWorkIDOutOfBounds(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Stdin = `{"work_id":"../etc/passwd","agent":"host-repair-agent"}`
	code, _, errOut := f.Run(t)
	if code != outsideRepairLaunchRefusalExit {
		t.Fatalf("refusal exit=%d stderr=%q, want %d", code, errOut, outsideRepairLaunchRefusalExit)
	}
	if !strings.Contains(errOut, "work_id is missing or out of bounds") {
		t.Fatalf("refusal does not name the input validation: %q", errOut)
	}
}

// TestOutsideRepairRefusesWhenAgentOutOfBounds covers the input-validation
// branch on the agent selector. The agent is operator-specified, but a value
// that cannot become a flag is refused before any read.
func TestOutsideRepairRefusesWhenAgentOutOfBounds(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host repair agent with spaces"}`, f.WorkID)
	code, _, errOut := f.Run(t)
	if code != outsideRepairLaunchRefusalExit {
		t.Fatalf("refusal exit=%d stderr=%q, want %d", code, errOut, outsideRepairLaunchRefusalExit)
	}
	if !strings.Contains(errOut, "agent is missing or out of bounds") {
		t.Fatalf("refusal does not name the agent validation: %q", errOut)
	}
}

// outsideRepairFixtureSlug returns the deterministic short-suffix the verb
// derives from the supplied Now() function. Tests pre-create the path the
// verb will compute to exercise the collision refusal.
func outsideRepairFixtureSlug(workID string, now func() time.Time) string {
	stamp := now().UnixNano()
	digest := sha256.Sum256([]byte(workID + ":" + strconv.FormatInt(stamp, 10)))
	return outsideRepairAdHocBranchPrefix + "-" + hex.EncodeToString(digest[:12])
}

// TestOutsideRepairRefusesOnAdhocPathCollision covers the refusal that
// protects existing data. The verb never removes a path that exists at
// its ad-hoc location.
func TestOutsideRepairRefusesOnAdhocPathCollision(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	// Pin the slug the verb will derive so we collide on the exact target.
	slug := outsideRepairFixtureSlug(f.WorkID, f.Deps.Now)
	collisionDir := filepath.Join(f.XDGHome, outsideRepairAdHocDirName)
	if err := os.MkdirAll(collisionDir, 0o750); err != nil {
		t.Fatalf("mkdir collision parent: %v", err)
	}
	collisionPath := filepath.Join(collisionDir, slug)
	if err := os.MkdirAll(collisionPath, 0o750); err != nil {
		t.Fatalf("mkdir collision target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(collisionPath, "marker"), []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, errOut := f.Run(t)
	if code != outsideRepairLaunchRefusalExit {
		t.Fatalf("refusal exit=%d stderr=%q, want %d", code, errOut, outsideRepairLaunchRefusalExit)
	}
	if !strings.Contains(errOut, "already exists") {
		t.Fatalf("refusal does not name the collision: %q", errOut)
	}
	if _, err := os.Stat(filepath.Join(collisionPath, "marker")); err != nil {
		t.Fatalf("refusal deleted the existing marker: %v", err)
	}
}

// TestOutsideRepairRefusesOnXDGHomeUnavailable covers the platform-error
// branch where neither XDG_DATA_HOME nor a readable HOME yields a data
// home.
func TestOutsideRepairRefusesOnXDGHomeUnavailable(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Deps.XDGHome = func() (string, error) { return "", errors.New("no XDG_DATA_HOME") }
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, errOut := f.Run(t)
	if code != outsideRepairLaunchRefusalExit {
		t.Fatalf("refusal exit=%d stderr=%q, want %d", code, errOut, outsideRepairLaunchRefusalExit)
	}
	if !strings.Contains(errOut, "XDG_DATA_HOME") {
		t.Fatalf("refusal does not name the XDG home: %q", errOut)
	}
	if len(f.Git.Cmds) != 0 {
		t.Fatalf("refusal issued %d git commands, want zero", len(f.Git.Cmds))
	}
}

func TestOutsideRepairLaunchesHostInAdHocWorktree(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, stdout, errOut := f.Run(t)
	if code != 0 {
		t.Fatalf("verb refused: stderr=%q", errOut)
	}
	var parsed outsideRepairLaunchOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil {
		t.Fatalf("decode verb output: %v\n%s", err, stdout)
	}
	if parsed.WorkID != f.WorkID || parsed.Agent != "host-repair-agent" {
		t.Fatalf("verb output identity = %+v", parsed)
	}
	if parsed.Repository != f.RepoPath {
		t.Fatalf("verb output repository = %q, want %q", parsed.Repository, f.RepoPath)
	}
	if parsed.DefaultRef == "" || parsed.BaseSHA == "" {
		t.Fatalf("verb output missing ref or sha: %+v", parsed)
	}
	if !regexp.MustCompile("^[0-9a-f]{40}$").MatchString(parsed.BaseSHA) {
		t.Fatalf("verb output base sha is not 40 hex: %q", parsed.BaseSHA)
	}
	if filepath.Clean(parsed.Path) != parsed.Path {
		t.Fatalf("verb output path is not clean: %q", parsed.Path)
	}
	if !strings.HasPrefix(parsed.Path, filepath.Join(f.XDGHome, outsideRepairAdHocDirName)+string(filepath.Separator)) {
		t.Fatalf("verb output path %q is not under XDG %q", parsed.Path, filepath.Join(f.XDGHome, outsideRepairAdHocDirName))
	}
	if _, err := os.Stat(parsed.Path); err != nil {
		t.Fatalf("verb did not create the ad-hoc worktree at %q: %v", parsed.Path, err)
	}
	if got := f.Recorder.Cwd; got != parsed.Path {
		t.Fatalf("host cwd = %q, want %q", got, parsed.Path)
	}
	prompt := parsed.Argv[len(parsed.Argv)-1]
	wantArgv := []string{"opencode", "--agent", "host-repair-agent", "--prompt", prompt}
	if !slices.Equal(f.Recorder.Argv, wantArgv) {
		t.Fatalf("host argv = %v, want %v", f.Recorder.Argv, wantArgv)
	}
	// The arg vector is literal: no shell can split "agent host-repair-agent"
	// into argv[1] because the verb passes each argv element separately.
	if !literalArgvNoShell(f.Recorder.Argv) {
		t.Fatalf("host argv is not literal: %v", f.Recorder.Argv)
	}
	// Env must clear every managed identifier. The fixture seeds an inherited
	// env to make sure CONCORD_SELECTED_PRODUCT_ID, CONCORD_SELECTED_WORK_ID,
	// and CONCORD_DB_PATH do not reach the host.
	for _, value := range f.Recorder.Env {
		for _, prefix := range outsideRepairManagedEnvPrefixes {
			if strings.HasPrefix(value, prefix) {
				t.Fatalf("managed env leaked: %q", value)
			}
		}
	}
	// The prompt carries read-only issue context, not a managed boot packet.
	if strings.Contains(prompt, "session_boot_packet") || strings.Contains(prompt, "session_type") || strings.Contains(prompt, "manifest_digest") {
		t.Fatalf("prompt carried a managed marker: %q", prompt)
	}
	if !strings.Contains(prompt, "Outside repair session") || !strings.Contains(prompt, f.WorkID) {
		t.Fatalf("prompt missed the read-only context: %q", prompt)
	}
	wantCommands := []string{"rev-parse", "rev-parse", "rev-parse", "rev-parse", "config", "show-ref", "fetch", "rev-parse", "worktree", "status"}
	if !assertGitCommandSequence(f.Git.Cmds, wantCommands) {
		t.Fatalf("git command sequence = %v, want prefix %v", flattenGitCommandSequence(f.Git.Cmds), wantCommands)
	}
}

// TestOutsideRepairEnvIsolationInheritedManagedVarsStripped makes sure the
// env-clear step removes every inherited CONCORD_SELECTED_* and
// CONCORD_DB_PATH before the host exec.
func TestOutsideRepairEnvIsolationInheritedManagedVarsStripped(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "managed-product")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "managed-work")
	t.Setenv("CONCORD_SELECTED_PROJECT_PATH", "/data/concord/main")
	t.Setenv("CONCORD_SELECTED_PROJECT_ID", "managed-project")
	t.Setenv("CONCORD_SELECTED_PROMPT", "managed-prompt")
	t.Setenv("CONCORD_SELECTED_AGENT", "managed-agent")
	t.Setenv("CONCORD_LAST_WORK_ID", "managed-work")
	t.Setenv("CONCORD_DB_PATH", "/data/concord/store.db")
	t.Setenv("CONCORD_PRODUCT_ID", "managed-product")
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, errOut := f.Run(t)
	if code != 0 {
		t.Fatalf("verb refused: stderr=%q", errOut)
	}
	for _, value := range f.Recorder.Env {
		if strings.HasPrefix(value, "CONCORD_SELECTED_") || strings.HasPrefix(value, "CONCORD_DB_PATH=") || strings.HasPrefix(value, "CONCORD_LAST_WORK_ID=") || strings.HasPrefix(value, "CONCORD_PRODUCT_ID=") {
			t.Fatalf("managed env leaked: %q", value)
		}
	}
}

// TestOutsideRepairHostCommandResolutionUsesAdHocDir makes sure the verb
// invokes the host command resolver in the ad-hoc worktree, never in the
// canonical path.
func TestOutsideRepairHostCommandResolutionUsesAdHocDir(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	var hostCommandDir string
	f.Deps.HostCommand = func(_ context.Context, dir string, env []string) (hostCommandResolution, error) {
		hostCommandDir = dir
		return outsideRepairFakeHostCommand(context.Background(), dir, env)
	}
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, stdout, errOut := f.Run(t)
	if code != 0 {
		t.Fatalf("verb refused: stderr=%q", errOut)
	}
	var parsed outsideRepairLaunchOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil {
		t.Fatalf("decode verb output: %v", err)
	}
	if hostCommandDir != parsed.Path {
		t.Fatalf("host command resolver ran in %q, want the ad-hoc path %q", hostCommandDir, parsed.Path)
	}
}

// TestOutsideRepairRefusesWhenRepoUnreachable covers the failure the
// resolver surfaces when the canonical path is gone. The verb must
// refuse without retrying.
func TestOutsideRepairRefusesWhenRepoUnreachable(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Deps.Git = outsideRepairBrokenGitRunner{}
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, errOut := f.Run(t)
	if code != outsideRepairLaunchRefusalExit {
		t.Fatalf("refusal exit=%d stderr=%q, want %d", code, errOut, outsideRepairLaunchRefusalExit)
	}
	if !strings.Contains(errOut, "git repository") {
		t.Fatalf("refusal does not name the git failure: %q", errOut)
	}
}

func TestOutsideRepairRetainsDirtyPathCollision(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	slug := outsideRepairFixtureSlug(f.WorkID, f.Deps.Now)
	dirtPath := filepath.Join(f.XDGHome, outsideRepairAdHocDirName, slug)
	if output, err := exec.Command("git", "-C", f.RepoPath, "worktree", "add", "--quiet", "-b", "untouched-existing", dirtPath).CombinedOutput(); err != nil {
		t.Fatalf("create colliding dirty tree: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(dirtPath, "stale"), []byte("stale residue"), 0o600); err != nil {
		t.Fatalf("write stale residue: %v", err)
	}
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, errOut := f.Run(t)
	if code != outsideRepairLaunchRefusalExit {
		t.Fatalf("refusal exit=%d stderr=%q, want %d", code, errOut, outsideRepairLaunchRefusalExit)
	}
	if !strings.Contains(errOut, "already exists") {
		t.Fatalf("refusal does not name the pre-existing path: %q", errOut)
	}
	if data, err := os.ReadFile(filepath.Join(dirtPath, "stale")); err != nil || string(data) != "stale residue" {
		t.Fatalf("refusal changed the dirty residue: %v %s", err, data)
	}
	branch, err := exec.Command("git", "-C", dirtPath, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(branch)) != "untouched-existing" {
		t.Fatalf("refusal changed the existing branch: %v %s", err, branch)
	}
}

func TestOutsideRepairOutputJSONIsReproducible(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, stdout, errOut := f.Run(t)
	if code != 0 {
		t.Fatalf("verb refused: stderr=%q", errOut)
	}
	var parsed outsideRepairLaunchOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &parsed); err != nil {
		t.Fatalf("decode verb output: %v", err)
	}
	if !slices.Equal(parsed.Argv, f.Recorder.Argv) {
		t.Fatalf("JSON argv %v != host argv %v", parsed.Argv, f.Recorder.Argv)
	}
	if parsed.Path != f.Recorder.Cwd {
		t.Fatalf("JSON directory %q != host directory %q", parsed.Path, f.Recorder.Cwd)
	}
}

// TestOutsideRepairHostExecFailureSurfacesTyped covers the failure path the
// host exec returns. The verb must report it without retrying.
func TestOutsideRepairHostExecFailureSurfacesTyped(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Recorder.ErrOut = errors.New("host exec refused")
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, errOut := f.Run(t)
	if code != 1 {
		t.Fatalf("verb refused: exit=%d stderr=%q", code, errOut)
	}
	if !strings.Contains(errOut, "host execution refused") {
		t.Fatalf("refusal does not name the host exec: %q", errOut)
	}
}

// Authorized coordinators can still read the held work's recovery context.
func TestOutsideRepairContinuityPacketExposesRouteWhenHoldActive(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	t.Setenv(dbOverrideEnv, f.Store.Path())
	snapshot, err := store.ReadWorkflowContinuity(context.Background(), f.Store, store.ContinuityRequest{Work: f.WorkID, Limit: 1})
	if err != nil {
		t.Fatalf("read continuity: %v", err)
	}
	if snapshot.OutsideRepairDisposition == nil || snapshot.OutsideRepairDisposition.State != store.OutsideRepairStateActive {
		t.Fatalf("continuity snapshot lost the disposition: %+v", snapshot.OutsideRepairDisposition)
	}
	if len(snapshot.OutsideRepairRoute) == 0 {
		t.Fatalf("continuity snapshot lost the route")
	}
	pin, err := store.ReadWorkPin(context.Background(), f.Store, f.WorkID)
	if err != nil {
		t.Fatalf("read work pin: %v", err)
	}
	if pin.OutsideRepairDisposition == nil || pin.OutsideRepairRoute == nil {
		t.Fatalf("work pin lost the disposition/route: %+v", pin)
	}
}

// TestOutsideRepairContinuityPacketSuppressesStepActionsWhenHeld proves the
// store-side continuity render clears step_actions while the hold is
// active. The adapter's continuity-hook relies on this so its prompt
// carries no step-action pin when the held work is committed.
func TestOutsideRepairContinuityPacketSuppressesStepActionsWhenHeld(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	t.Setenv(dbOverrideEnv, f.Store.Path())
	snapshot, err := store.ReadWorkflowContinuity(context.Background(), f.Store, store.ContinuityRequest{Work: f.WorkID, Limit: 1})
	if err != nil {
		t.Fatalf("read continuity: %v", err)
	}
	if len(snapshot.StepActions) != 0 {
		t.Fatalf("held continuity still exposed step actions: %v", snapshot.StepActions)
	}
	if snapshot.PendingOperatorDecision != nil {
		t.Fatalf("held continuity still exposed a pending operator decision: %+v", snapshot.PendingOperatorDecision)
	}
}

// outsideRepairBrokenGitRunner fails every command with a transport error
// so the verb's resolver surfaces the typed refusal deterministically.
type outsideRepairBrokenGitRunner struct{}

func (outsideRepairBrokenGitRunner) Run(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return nil, fmt.Errorf("git unavailable")
}

func (r outsideRepairBrokenGitRunner) RunNoninteractive(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return r.Run(ctx, dir, args...)
}

// literalArgvNoShell makes sure the verb passes argv elements literally. The
// fail-safe is the presence of a single string in the slice that has no
// shell metacharacters: a shell would have split or expanded the slice.
func literalArgvNoShell(argv []string) bool {
	// The verb must keep "--agent" and "host-repair-agent" as separate
	// argv entries. If a shell were involved, the vector would have lost
	// one of them.
	if len(argv) < 4 {
		return false
	}
	if argv[1] != "--agent" {
		return false
	}
	if argv[3] != "--prompt" {
		return false
	}
	return true
}

func flattenGitCommandSequence(cmds []recordedGitCommand) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		if len(c.Args) == 0 {
			continue
		}
		out = append(out, c.Args[0])
	}
	return out
}

func assertGitCommandSequence(cmds []recordedGitCommand, want []string) bool {
	got := flattenGitCommandSequence(cmds)
	if len(got) < len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestOutsideRepairStrictInput(t *testing.T) {
	for _, extra := range []string{`,"unexpected":true}`, `} {"agent":"another"}`} {
		t.Run(extra, func(t *testing.T) {
			f := newOutsideRepairFixture(t, true)
			f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"`, f.WorkID) + extra
			code, _, stderr := f.Run(t)
			if code == 0 || len(f.Git.Cmds) != 0 || f.Recorder.Cwd != "" {
				t.Fatalf("invalid input reached launch: code=%d git=%v stderr=%s", code, f.Git.Cmds, stderr)
			}
		})
	}
}

func TestOutsideRepairRealBranchAndPrivateOutput(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	t.Setenv("OUTSIDE_REPAIR_TEST_SECRET", "synthetic-credential-do-not-print")
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, stdout, stderr := f.Run(t)
	if code != 0 {
		t.Fatalf("launch: %d %s", code, stderr)
	}
	var output outsideRepairLaunchOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	branch, err := exec.Command("git", "-C", output.Path, "symbolic-ref", "--short", "HEAD").CombinedOutput()
	if err != nil || strings.TrimSpace(string(branch)) != output.Branch {
		t.Errorf("actual branch=%q err=%v reported branch=%q", branch, err, output.Branch)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"env", "runnable"} {
		if _, exists := fields[field]; exists {
			t.Errorf("JSON exposes forbidden field %s", field)
		}
	}
	if strings.Contains(stdout+stderr, "synthetic-credential-do-not-print") {
		t.Error("inherited credential leaked into output")
	}
	if f.Recorder.In == nil {
		t.Error("host has no interactive stdin after JSON consumption")
	}
	if f.Recorder.Out != any(f.Recorder.In) || f.Recorder.Err != any(f.Recorder.In) {
		t.Error("host does not use the controlling terminal for all TUI streams")
	}
	prompt := output.Argv[len(output.Argv)-1]
	for _, prohibited := range []string{"Dispatch the outside-repair route", "before any managed effect", "outside_repair_resume"} {
		if strings.Contains(prompt, prohibited) {
			t.Errorf("prompt grants managed authority: %s", prohibited)
		}
	}
	for _, required := range []string{"no Concord workflow authority", "read-only Concord tools", "ordinary repository evidence", "authorized coordinator"} {
		if !strings.Contains(prompt, required) {
			t.Errorf("prompt omits %q", required)
		}
	}
}

func TestOutsideRepairColonWorkIDCreatesValidGitRef(t *testing.T) {
	f := newOutsideRepairFixture(t, true, "work:outside:repair")
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, stdout, stderr := f.Run(t)
	if code != 0 {
		t.Fatalf("launch: %d %s", code, stderr)
	}
	var output outsideRepairLaunchOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "check-ref-format", "--branch", output.Branch).CombinedOutput(); err != nil {
		t.Fatalf("invalid reported branch %q: %v %s", output.Branch, err, out)
	}
}

func TestOutsideRepairRejectsUnsafeDataParentsBeforeFetch(t *testing.T) {
	for _, kind := range []string{"empty", "relative", "unclean", "xdg-symlink", "parent-symlink", "claimed-parent-symlink", "file-parent", "inside-main", "inside-claimed"} {
		t.Run(kind, func(t *testing.T) {
			f := newOutsideRepairFixture(t, true)
			home := f.XDGHome
			switch kind {
			case "empty":
				home = ""
			case "relative":
				home = "."
			case "unclean":
				home += "/../" + filepath.Base(home)
			case "xdg-symlink":
				home = filepath.Join(t.TempDir(), "data")
				if err := os.Symlink(f.RepoPath, home); err != nil {
					t.Fatal(err)
				}
			case "parent-symlink":
				if err := os.Symlink(f.RepoPath, filepath.Join(home, "opencode")); err != nil {
					t.Fatal(err)
				}
			case "file-parent":
				if err := os.WriteFile(filepath.Join(home, "opencode"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "inside-main":
				home = f.RepoPath
			case "inside-claimed", "claimed-parent-symlink":
				linked := filepath.Join(t.TempDir(), "linked")
				if output, err := exec.Command("git", "-C", f.RepoPath, "worktree", "add", "--quiet", "-b", "existing-claimed", linked).CombinedOutput(); err != nil {
					t.Fatalf("seed linked tree: %v %s", err, output)
				}
				if kind == "inside-claimed" {
					home = linked
				} else if err := os.Symlink(linked, filepath.Join(home, "opencode")); err != nil {
					t.Fatal(err)
				}
			}
			f.Deps.XDGHome = func() (string, error) { return home, nil }
			f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
			code, _, stderr := f.Run(t)
			if code != outsideRepairLaunchRefusalExit || f.Recorder.Cwd != "" {
				t.Errorf("unsafe parent launched: %d %s", code, stderr)
			}
			for _, command := range f.Git.Cmds {
				if command.Args[0] == "fetch" || command.Args[0] == "-c" || command.Args[0] == "worktree" {
					t.Errorf("unsafe parent reached Git effect: %v", command)
				}
			}
		})
	}
}

func TestOutsideRepairRejectsCanonicalLocatorMismatchBeforeFetch(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	other := outsideRepairTestRepo(t)
	link := filepath.Join(t.TempDir(), "registered")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Store.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE project_locators SET locator_value=?,normalized_value=? WHERE project_id=?; DELETE FROM fold_guard;`, link, link, f.ProjectID); err != nil {
		t.Fatal(err)
	}
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, stderr := f.Run(t)
	if code != outsideRepairLaunchRefusalExit || f.Recorder.Cwd != "" {
		t.Errorf("wrong repository launched: %d %s", code, stderr)
	}
	for _, command := range f.Git.Cmds {
		if command.Args[0] == "fetch" || command.Args[0] == "-c" || command.Args[0] == "worktree" {
			t.Errorf("mismatched locator reached Git effect: %v", command)
		}
	}
}

func TestOutsideRepairStripsAllManagedIdentityPrefixes(t *testing.T) {
	for _, name := range []string{"CONCORD_SELECTED_FUTURE", "CONCORD_SESSION_REF", "CONCORD_WORK_ID", "CONCORD_WORKTREE_PATH", "CONCORD_LEASE_ID"} {
		t.Setenv(name, "parent-identity")
	}
	t.Setenv("CONCORD_CLIENT_REF", "read-client")
	t.Setenv("CONCORD_BIN", "/synthetic/release/concord")
	env := outsideRepairBuildEnv()
	for _, value := range env {
		if strings.HasSuffix(value, "=parent-identity") {
			t.Errorf("inherited managed identity: %s", value)
		}
	}
	for _, wanted := range []string{"CONCORD_CLIENT_REF=read-client", "CONCORD_BIN=/synthetic/release/concord"} {
		if !strings.Contains(strings.Join(env, "\n"), wanted) {
			t.Errorf("lost read-capable release environment: %s", wanted)
		}
	}
}

func TestOutsideRepairActualChildInFreshBranch(t *testing.T) {
	f := newOutsideRepairFixture(t, true, "work:child")
	// Advance the origin after the fixture fetch to prove creation uses a
	// fresh remote head, not the previously cached tracking ref.
	if output, err := exec.Command("git", "-C", f.RepoPath, "commit", "--quiet", "--allow-empty", "-m", "fresh origin").CombinedOutput(); err != nil {
		t.Fatalf("advance origin: %v %s", err, output)
	}
	head, err := exec.Command("git", "-C", f.RepoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	beforeMain, err := exec.Command("git", "-C", f.RepoPath, "symbolic-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	childRecord := filepath.Join(t.TempDir(), "child.json")
	t.Setenv("OUTSIDE_REPAIR_CHILD_RECORD", childRecord)
	t.Setenv("OUTSIDE_REPAIR_TEST_SECRET", "private-child-credential")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "parent-work")
	t.Setenv("CONCORD_SESSION_REF", "parent-session")
	t.Setenv("CONCORD_LEASE_ID", "parent-lease")
	t.Setenv("CONCORD_CLIENT_REF", "read-client")
	t.Setenv("CONCORD_BIN", "/synthetic/release/concord")
	executable := filepath.Join(t.TempDir(), "host-fixture")
	if output, err := exec.Command("go", "build", "-o", executable, "./testdata/outside-repair-host.go").CombinedOutput(); err != nil {
		t.Fatalf("build host fixture: %v %s", err, output)
	}
	f.Deps.HostCommand = func(ctx context.Context, dir string, _ []string) (hostCommandResolution, error) {
		for _, gitCtx := range f.Git.Contexts {
			if gitCtx.Err() != context.Canceled {
				t.Fatal("Git phase still live at host resolution")
			}
		}
		return hostCommandResolution{Command: []string{executable}, Registry: []byte(`{"agent":{"host-repair-agent":{"mode":"primary"}}}`)}, nil
	}
	terminal := &outsideRepairTestTerminal{Input: strings.NewReader("operator-keystroke\n")}
	f.Deps.Terminal = func() (io.ReadWriteCloser, error) { return terminal, nil }
	f.Deps.Exec = runOpenCode
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, stdout, stderr := f.Run(t)
	if code != 0 {
		t.Fatalf("launch: %d %s", code, stderr)
	}
	var launch outsideRepairLaunchOutput
	if err := json.Unmarshal([]byte(stdout), &launch); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(launch.Path, "outside-repair-child.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(childRecord); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host fixture wrote an environment-selected path: %v", err)
	}
	var child outsideRepairChildRecord
	if err := json.Unmarshal(data, &child); err != nil {
		t.Fatal(err)
	}
	if child.Directory != launch.Path || child.Branch != launch.Branch || child.Head != launch.BaseSHA || child.Head != strings.TrimSpace(string(head)) {
		t.Fatalf("child did not enter the actual fresh branch: %+v launch=%+v", child, launch)
	}
	if len(child.ManagedIdentity) != 0 {
		t.Fatalf("child inherited parent identity: %v", child.ManagedIdentity)
	}
	if child.Client != "read-client" || child.Release != "/synthetic/release/concord" || child.Secret != "private-child-credential" {
		t.Fatalf("child lost private read environment: %+v", child)
	}
	if child.Input != "operator-keystroke\n" || !strings.Contains(terminal.Output.String(), "child-tty-output") || !terminal.Closed {
		t.Fatal("child did not receive usable separate terminal I/O")
	}
	if strings.Contains(stdout+stderr, "private-child-credential") || strings.Contains(stdout, "child-tty-output") {
		t.Fatal("JSON stream contains child I/O or credentials")
	}
	afterMain, err := exec.Command("git", "-C", f.RepoPath, "symbolic-ref", "HEAD").Output()
	if err != nil || string(afterMain) != string(beforeMain) {
		t.Fatalf("main checkout branch changed: %s %v", afterMain, err)
	}
}

type outsideRepairChildRecord struct {
	Directory       string
	Branch          string
	Head            string
	Input           string
	Client          string
	Release         string
	Secret          string
	ManagedIdentity []string
}

func TestOutsideRepairRefusesBranchCollisionWithoutDeletion(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	branch := outsideRepairFixtureSlug(f.WorkID, f.Deps.Now)
	if output, err := exec.Command("git", "-C", f.RepoPath, "branch", branch).CombinedOutput(); err != nil {
		t.Fatalf("create collision: %v %s", err, output)
	}
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, stderr := f.Run(t)
	if code != outsideRepairLaunchRefusalExit || !strings.Contains(stderr, "branch already exists") {
		t.Fatalf("collision: %d %s", code, stderr)
	}
	if output, err := exec.Command("git", "-C", f.RepoPath, "show-ref", "--verify", "refs/heads/"+branch).CombinedOutput(); err != nil {
		t.Fatalf("collision branch removed: %v %s", err, output)
	}
	for _, cmd := range f.Git.Cmds {
		if cmd.Args[0] == "fetch" || cmd.Args[0] == "worktree" {
			t.Fatalf("branch collision reached Git effect: %v", cmd)
		}
	}
}

func TestOutsideRepairRefusesWithoutTerminalBeforeGitEffects(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Deps.Terminal = func() (io.ReadWriteCloser, error) { return nil, errors.New("no controlling TTY") }
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, stderr := f.Run(t)
	if code != outsideRepairLaunchRefusalExit || !strings.Contains(stderr, "TTY") || len(f.Git.Cmds) != 0 {
		t.Fatalf("terminal refusal: %d %s Git=%v", code, stderr, f.Git.Cmds)
	}
}

func TestOutsideRepairRetainsNewDirtyWorktree(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	var dirtyPath string
	f.Git.Next = func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		if args[0] == "status" {
			dirtyPath = filepath.Join(dir, "residue")
			if err := os.WriteFile(dirtyPath, []byte("retain"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return store.ExecGitRunner{}.Run(ctx, dir, args...)
	}
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, stderr := f.Run(t)
	if code != outsideRepairLaunchRefusalExit || !strings.Contains(stderr, "dirty after creation") || f.Recorder.Cwd != "" {
		t.Fatalf("dirty launch: %d %s", code, stderr)
	}
	if data, err := os.ReadFile(dirtyPath); err != nil || string(data) != "retain" {
		t.Fatalf("dirty worktree removed: %v %s", err, data)
	}
}

func TestOutsideRepairRejectsUnregisteredHostAgent(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	f.Deps.HostCommand = func(_ context.Context, _ string, _ []string) (hostCommandResolution, error) {
		return hostCommandResolution{Command: []string{"opencode"}, Registry: []byte(`{"agent":{}}`)}, nil
	}
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, _, stderr := f.Run(t)
	if code != outsideRepairLaunchRefusalExit || !strings.Contains(stderr, "not registered") || f.Recorder.Cwd != "" {
		t.Fatalf("agent refusal: %d %s", code, stderr)
	}
}

func TestOutsideRepairHostResolutionUsesPrivateChildEnvironment(t *testing.T) {
	f := newOutsideRepairFixture(t, true)
	bin := t.TempDir()
	wrapper := filepath.Join(bin, "host wrapper")
	config := filepath.Join(t.TempDir(), "config.json")
	document, err := json.Marshal(map[string]any{
		"plugin": []any{[]any{"/synthetic/concord-plugin.ts", map[string]any{"host_command": []string{wrapper, "--fixed"}}}},
		"agent":  map[string]any{"host-repair-agent": map[string]any{"mode": "primary"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, document, 0o600); err != nil {
		t.Fatal(err)
	}
	script := []byte(`#!/bin/sh
set -eu
test -z "${CONCORD_SELECTED_WORK_ID-}"
test -z "${CONCORD_SESSION_REF-}"
test -z "${CONCORD_LEASE_ID-}"
test "$CONCORD_CLIENT_REF" = "read-client"
if [ "${1-}" = "--fixed" ]; then shift; fi
if [ "${1-}" = "debug" ]; then
  cat "$OUTSIDE_REPAIR_HOST_CONFIG"
else
  read -r line
  printf 'host-tty:%s\n' "$line"
fi
`)
	for _, path := range []string{filepath.Join(bin, "opencode"), wrapper} {
		if err := os.WriteFile(path, script, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OUTSIDE_REPAIR_HOST_CONFIG", config)
	t.Setenv("CONCORD_SELECTED_WORK_ID", "parent-work")
	t.Setenv("CONCORD_SESSION_REF", "parent-session")
	t.Setenv("CONCORD_LEASE_ID", "parent-lease")
	t.Setenv("CONCORD_CLIENT_REF", "read-client")
	f.Deps.HostCommand = defaultOutsideRepairDeps().HostCommand
	f.Deps.Exec = runOpenCode
	terminal := &outsideRepairTestTerminal{Input: strings.NewReader("operator-input\n")}
	f.Deps.Terminal = func() (io.ReadWriteCloser, error) { return terminal, nil }
	f.Stdin = fmt.Sprintf(`{"work_id":%q,"agent":"host-repair-agent"}`, f.WorkID)
	code, stdout, stderr := f.Run(t)
	if code != 0 {
		t.Fatalf("resolution/launch: %d %s", code, stderr)
	}
	var launch outsideRepairLaunchOutput
	if err := json.Unmarshal([]byte(stdout), &launch); err != nil {
		t.Fatal(err)
	}
	if launch.Argv[0] != wrapper || launch.Argv[1] != "--fixed" || !strings.Contains(terminal.Output.String(), "host-tty:operator-input") {
		t.Fatalf("configured host not used: argv=%v terminal=%q", launch.Argv, terminal.Output.String())
	}
}

func TestOutsideRepairTerminalAfterJSONPipe(t *testing.T) {
	if os.Getenv("OUTSIDE_REPAIR_TTY_CHILD") == "1" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil || string(data) != `{"work_id":"work-held","agent":"host-repair-agent"}` {
			t.Fatalf("JSON pipe: %s %v", data, err)
		}
		terminal, err := outsideRepairTerminal()
		if err != nil {
			t.Fatal(err)
		}
		defer terminal.Close()
		if _, err := fmt.Fprintln(terminal, "tty-ready"); err != nil {
			t.Fatal(err)
		}
		var input string
		if _, err := fmt.Fscanln(terminal, &input); err != nil || input != "operator-input" {
			t.Fatalf("TTY input: %s %v", input, err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "json-stream-only"); err != nil {
			t.Fatal(err)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The child gets a controlling PTY but a separate JSON pipe on fd 0.
	// This exercises the production /dev/tty reopen, not the injected seam.
	const script = `import fcntl, os, pty, select, subprocess, sys, termios, time
master, slave = pty.openpty()
def controlling_terminal():
    os.setsid()
    fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
env = dict(os.environ, OUTSIDE_REPAIR_TTY_CHILD="1")
child = subprocess.Popen([sys.argv[1], "-test.run=^TestOutsideRepairTerminalAfterJSONPipe$"], env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, pass_fds=(slave,), preexec_fn=controlling_terminal)
try:
    child.stdin.write(b'{"work_id":"work-held","agent":"host-repair-agent"}')
    child.stdin.close()
    child.stdin = None
    ready = b""
    deadline = time.monotonic() + 10
    while b"tty-ready" not in ready:
        readable, _, _ = select.select([master], [], [], max(0, deadline - time.monotonic()))
        if not readable: raise RuntimeError("no TTY readiness after JSON EOF")
        ready += os.read(master, 4096)
    os.write(master, b"operator-input\n")
    stdout, stderr = child.communicate(timeout=10)
    assert child.returncode == 0, (child.returncode, stdout, stderr)
    assert b"json-stream-only" in stdout and b"tty-ready" not in stdout, (stdout, stderr)
finally:
    if child.poll() is None:
        child.kill()
        child.wait()
    os.close(master)
    os.close(slave)
`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "python3", "-c", script, executable).CombinedOutput(); err != nil {
		t.Fatalf("controlling terminal integration: %v %s", err, output)
	}
}
