package main

// CD-0093 regression coverage. The session command resolves the Project
// directory of the selected work before anything else, and one resolved
// directory governs agent definition resolution, the host registry probe,
// and host execution. These tests drive the production wiring — the real
// directory resolver, the real identity verification, the real registry
// probe, and the real executor — against a fake `opencode` installed on
// PATH, because the anchors that inject a probe cannot observe a
// substituted executor (issue #664 acceptance).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// fakeHostScript is the fake `opencode` the tests install on PATH. Its
// registry branch emulates the directory dependence of the real host: it
// answers `debug config` with an agent map read from the invocation
// directory, so a registry that does not register the asserted handle
// refuses the launch exactly where the real one would substitute the
// operator's default agent. Its session branch records the argument count,
// the leading flags, and its own working directory.
const fakeHostScript = `#!/bin/sh
record="$CONCORD_FAKE_HOST_RECORD"
if [ "$1" = "debug" ] && [ "$2" = "config" ]; then
	pwd > "$record/probe-cwd"
	n=$(cat "$record/probe-count" 2>/dev/null || echo 0)
	echo $((n+1)) > "$record/probe-count"
	if [ -f opencode.registry.json ]; then
		cat opencode.registry.json
	else
		printf '{}'
	fi
	exit 0
fi
printf '%s\n' "$#" "$1" "$2" "$3" > "$record/host-argv"
pwd > "$record/host-cwd"
exit 0
`

// fakeWrapperScript is the fake configured host command the tests install on
// PATH beside the bare fake host. Its probe branch answers `debug config`
// with its own registry file when one is present — so a test can make the
// configured command's document differ from the bare host's — and counts its
// probes separately. Its session branch records the argument count, the
// leading flags, and its own working directory under wrapper-prefixed names.
const fakeWrapperScript = `#!/bin/sh
record="$CONCORD_FAKE_HOST_RECORD"
if [ "$1" = "debug" ] && [ "$2" = "config" ]; then
	pwd > "$record/wrapper-probe-cwd"
	n=$(cat "$record/wrapper-probe-count" 2>/dev/null || echo 0)
	echo $((n+1)) > "$record/wrapper-probe-count"
	if [ -f wrapper.registry.json ]; then
		cat wrapper.registry.json
	elif [ -f opencode.registry.json ]; then
		cat opencode.registry.json
	else
		printf '{}'
	fi
	exit 0
fi
printf '%s\n' "$#" "$1" "$2" "$3" > "$record/wrapper-argv"
pwd > "$record/wrapper-cwd"
exit 0
`

// installFakeHost puts the fake host binary first on PATH and points its
// record directory at recordDir.
func installFakeHost(t *testing.T, recordDir string) {
	t.Helper()
	installFakeCommand(t, recordDir, "opencode", fakeHostScript)
}

// installFakeWrapper puts the fake configured host command first on PATH
// beside the bare fake host.
func installFakeWrapper(t *testing.T, recordDir string) {
	t.Helper()
	installFakeCommand(t, recordDir, "fake-wrapper", fakeWrapperScript)
}

func installFakeCommand(t *testing.T, recordDir, name, script string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONCORD_FAKE_HOST_RECORD", recordDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// hostRecord reads one record the fake host wrote. A missing record fails
// the test with the cause: the fake host never ran that branch.
func hostRecord(t *testing.T, recordDir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(recordDir, name))
	if err != nil {
		t.Fatalf("fake host record %s: %v", name, err)
	}
	return strings.TrimSpace(string(data))
}

// seedSessionProject seeds one Product, one Project, one work item with the
// implementation workflow instance, and — when projectDir is non-empty — a
// canonical_path locator for the Project. It returns the database path and
// closes the seeding store so production wiring can open the file itself.
func seedSessionProject(t *testing.T, projectDir string) string {
	t.Helper()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open session fixture store: %v", err)
	}
	seedSessionProduct(t, s, "product-1", "Session product")
	seedSessionWork(t, s, "work-1", "product-1")
	seedApprovalWorkflow(t, s, "work-1")
	if projectDir != "" {
		sessionFixtureExec(t, s, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES ('locator-session','product-1-project','canonical_path',?,?, 'now','now')`, projectDir, projectDir)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("close session fixture store: %v", err)
	}
	return path
}

func seedSessionProduct(t *testing.T, s *store.Store, id, name string) {
	t.Helper()
	projectID := id + "-project"
	sessionFixtureExec(t, s, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES (?,?,'prototype','operator_only',1,?,?)`, id, name, "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	sessionFixtureExec(t, s, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES (?,?,1,?,?)`, projectID, name+" project", "2026-08-01T00:00:00Z", "2026-08-01T00:00:00Z")
	sessionFixtureExec(t, s, `INSERT INTO product_projects(product_id,project_id,role) VALUES (?,?,'primary')`, id, projectID)
}

func seedSessionWork(t *testing.T, s *store.Store, id, productID string) {
	t.Helper()
	sessionFixtureExec(t, s, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES (?,'task','Session work','needed',1,1,?,?,NULL)`, id, "2026-09-01T00:00:00Z", "2026-09-01T00:00:00Z")
	sessionFixtureExec(t, s, `INSERT INTO work_projects(work_id,project_id,role) VALUES (?,?,'primary')`, id, productID+"-project")
}

func seedApprovalWorkflow(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatalf("load workflow definition: %v", err)
	}
	sessionFixtureExec(t, s, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,started_at) VALUES (?,?,?,?,?,?,?)`, workID, definition.Definition.Ref, definition.Definition.Version, definition.Digest, "planning", "ready", "2026-08-01T00:00:00Z")
}

func sessionFixtureExec(t *testing.T, s *store.Store, statement string, args ...any) {
	t.Helper()
	db := s.DatabaseForTesting()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatalf("enable session fixture fold guard: %v", err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("disable session fixture fold guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, statement, args...); err != nil {
		t.Fatalf("seed session fixture: %v", err)
	}
}

// writeProjectHostArtifacts places the lane and orchestrator definitions
// and the registry document in the Project directory, and nothing anywhere
// else: HOME and the launcher directory carry no definitions, so every
// directory-dependent step must receive the resolved Project directory or
// fail.
func writeProjectHostArtifacts(t *testing.T, projectDir string) {
	t.Helper()
	agents := filepath.Join(projectDir, ".opencode", "agents")
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, agents, laneAgentFileName(lane.ID))
	}
	writeAgentDefinitionBody(t, agents, agentDefinitionFileName("concord-1"), []byte("---\nmode: all\n---\norchestrator\n"))
	registry, err := json.Marshal(hostConfigDocument{Agent: map[string]hostAgentEntry{
		"concord-1": {Mode: "primary"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "opencode.registry.json"), registry, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSessionRunsInTheResolvedProjectDirectory is the issue #664 regression
// test. It drives the real directory resolver, the real identity
// verification, the real registry probe, and the real executor: the session
// must run in the Project directory the selected work resolves to, verify
// the registry that directory resolves, and name the fixed host command.
// A session that verified or executed in the launcher's directory refuses
// here, because that directory registers no agent and supplies no
// definitions; a session that consulted OPENCODE_BIN would run /bin/false.
func TestSessionRunsInTheResolvedProjectDirectory(t *testing.T) {
	projectDir := t.TempDir()
	writeProjectHostArtifacts(t, projectDir)
	t.Setenv(dbOverrideEnv, seedSessionProject(t, projectDir))

	launcherDir := t.TempDir()
	recordDir := t.TempDir()
	installFakeHost(t, recordDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENCODE_BIN", "/bin/false")
	t.Chdir(launcherDir)
	setIdentityLaunchEnv(t, "work-1", "", "concord-1")

	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		hostSessionDirectory, hostSessionHostCommand, DeriveSessionBoot, runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
	if code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if probe := hostRecord(t, recordDir, "probe-cwd"); probe != projectDir {
		t.Fatalf("registry probed %q, want the Project directory %q", probe, projectDir)
	}
	if host := hostRecord(t, recordDir, "host-cwd"); host != projectDir {
		t.Fatalf("host started in %q, want the Project directory %q", host, projectDir)
	}
	argvLines := strings.Split(hostRecord(t, recordDir, "host-argv"), "\n")
	if len(argvLines) != 4 || argvLines[0] != "4" {
		t.Fatalf("host argument vector shape=%q, want the fixed 4-argument vector", argvLines)
	}
	if argvLines[1] != "--agent" || argvLines[2] != "concord-1" || argvLines[3] != "--prompt" {
		t.Fatalf("host argument vector=%q, want --agent %s --prompt", argvLines, "concord-1")
	}
	// No host_command is configured here, so the bootstrap probe is the
	// only probe: one bare `opencode debug config` run, no second probe
	// (CD-0189's behavior without the option is unchanged).
	if count := hostRecord(t, recordDir, "probe-count"); count != "1" {
		t.Fatalf("bare probes=%s, want exactly the one bootstrap probe", count)
	}
}

// TestSessionLaunchesAConfiguredHostCommand covers CD-0189 through the
// production wiring: the bare probe reads the host_command option from the
// Concord plugin tuple, the second probe runs through the configured command
// in the resolved directory, the registration check reads that command's own
// document, and the launch appends Concord's fixed arguments to the
// configured argv. The bare host never starts a session.
func TestSessionLaunchesAConfiguredHostCommand(t *testing.T) {
	projectDir := t.TempDir()
	writeProjectHostArtifacts(t, projectDir)
	// The bare probe's document carries the option beside the agent map;
	// the configured command's own document carries the identical value and
	// registers the handle.
	registry := `{"agent":{"concord-1":{"mode":"primary"}},"plugin":[["file:///tools/concord-plugin.ts",{"host_command":["fake-wrapper"]}]]}`
	if err := os.WriteFile(filepath.Join(projectDir, "opencode.registry.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(dbOverrideEnv, seedSessionProject(t, projectDir))

	launcherDir := t.TempDir()
	recordDir := t.TempDir()
	installFakeHost(t, recordDir)
	installFakeWrapper(t, recordDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENCODE_BIN", "/bin/false")
	t.Chdir(launcherDir)
	setIdentityLaunchEnv(t, "work-1", "", "concord-1")

	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		hostSessionDirectory, hostSessionHostCommand, DeriveSessionBoot, runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
	if code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if count := hostRecord(t, recordDir, "probe-count"); count != "1" {
		t.Fatalf("bare probes=%s, want exactly the one bootstrap probe", count)
	}
	if count := hostRecord(t, recordDir, "wrapper-probe-count"); count != "1" {
		t.Fatalf("configured probes=%s, want exactly the one verification probe", count)
	}
	if probe := hostRecord(t, recordDir, "wrapper-probe-cwd"); probe != projectDir {
		t.Fatalf("configured command probed %q, want the Project directory %q", probe, projectDir)
	}
	if host := hostRecord(t, recordDir, "wrapper-cwd"); host != projectDir {
		t.Fatalf("configured command started in %q, want the Project directory %q", host, projectDir)
	}
	argvLines := strings.Split(hostRecord(t, recordDir, "wrapper-argv"), "\n")
	if len(argvLines) != 4 || argvLines[0] != "4" || argvLines[1] != "--agent" || argvLines[2] != "concord-1" || argvLines[3] != "--prompt" {
		t.Fatalf("configured argument vector=%q, want the fixed 4-argument vector after the wrapper argv", argvLines)
	}
	if _, err := os.Stat(filepath.Join(recordDir, "host-argv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the bare host started a session even though a host command is configured")
	}
}

// TestSessionRefusesWhenTheProjectDirectoryDoesNotResolve covers CD-0093 D3:
// a canonical path that does not resolve on this machine refuses the launch
// before identity verification runs and before any host starts.
func TestSessionRefusesWhenTheProjectDirectoryDoesNotResolve(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	t.Setenv(dbOverrideEnv, seedSessionProject(t, gone))
	setIdentityLaunchEnv(t, "work-1", "", "")

	identityCalls, runs := 0, 0
	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		hostSessionDirectory,
		hostCommandAt(defaultHostResolution()),
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			runs++
			return nil
		},
		func(string) error { identityCalls++; return nil },
		func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
			return "concord-1", nil
		})
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errOut.String())
	}
	if identityCalls != 0 {
		t.Fatalf("identity verified %d time(s); the directory must resolve before identity verification", identityCalls)
	}
	if runs != 0 {
		t.Fatalf("host started %d time(s) on a refused session", runs)
	}
	if !strings.Contains(errOut.String(), "is not a usable directory") || !strings.Contains(errOut.String(), gone) {
		t.Fatalf("diagnostic=%q", errOut.String())
	}
}

// TestSessionRefusesWithoutAResolvableProject covers the remaining CD-0093
// D3 refusals through the production resolver: a selected work with no
// primary Project, and a primary Project with no canonical_path locator.
func TestSessionRefusesWithoutAResolvableProject(t *testing.T) {
	t.Run("no primary project", func(t *testing.T) {
		s, err := storetest.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		path := s.Path()
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		t.Setenv(dbOverrideEnv, path)
		setIdentityLaunchEnv(t, "work-1", "", "")
		identityCalls, runs := 0, 0
		var out, errOut bytes.Buffer
		code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
			hostSessionDirectory,
			hostCommandAt(defaultHostResolution()),
			func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
			func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
				runs++
				return nil
			},
			func(string) error { identityCalls++; return nil },
			func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
				return "concord-1", nil
			})
		if code != 2 || identityCalls != 0 || runs != 0 {
			t.Fatalf("exit=%d identity=%d runs=%d stderr=%q", code, identityCalls, runs, errOut.String())
		}
		if !strings.Contains(errOut.String(), "no primary Project") {
			t.Fatalf("diagnostic=%q", errOut.String())
		}
	})
	t.Run("no canonical path locator", func(t *testing.T) {
		t.Setenv(dbOverrideEnv, seedSessionProject(t, ""))
		setIdentityLaunchEnv(t, "work-1", "", "")
		identityCalls, runs := 0, 0
		var out, errOut bytes.Buffer
		code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
			hostSessionDirectory,
			hostCommandAt(defaultHostResolution()),
			func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
			func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
				runs++
				return nil
			},
			func(string) error { identityCalls++; return nil },
			func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
				return "concord-1", nil
			})
		if code != 2 || identityCalls != 0 || runs != 0 {
			t.Fatalf("exit=%d identity=%d runs=%d stderr=%q", code, identityCalls, runs, errOut.String())
		}
		if !strings.Contains(errOut.String(), "no canonical_path locator") {
			t.Fatalf("diagnostic=%q", errOut.String())
		}
	})
}

// TestProductOnlySessionRemainsIdentityOnly holds the Product-only session
// mode across CD-0093. CD-0093 decides where a session runs when work is
// selected; it does not withdraw the mode that selects none. A Product spans
// Projects, so no work-derived Project exists to resolve, and the session
// keeps the launcher's directory and carries identity without a continuity
// packet. It is the floor anchor for fc1-operator-work-capture.
func TestProductOnlySessionRemainsIdentityOnly(t *testing.T) {
	setIdentityLaunchEnv(t, "", "", "concord-1")
	launcherDir := t.TempDir()
	t.Chdir(launcherDir)
	bootstrapCalls, directoryCalls := 0, 0
	var argv []string
	var ranIn string
	code := runSessionCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true,
		func(_ context.Context, _ string, _ string) (string, error) { directoryCalls++; return "", nil },
		hostCommandAt(defaultHostResolution()),
		func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil },
		func(_ context.Context, dir string, got []string, _ []string, _ io.Reader, _, _ io.Writer) error {
			ranIn = dir
			argv = append([]string(nil), got...)
			return nil
		},
		func(string) error { return nil },
		func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
			return "concord-1", nil
		})
	if code != 0 {
		t.Fatalf("exit=%d, want 0", code)
	}
	// No work means no Project to resolve, so the store resolution never runs.
	if directoryCalls != 0 || bootstrapCalls != 0 {
		t.Fatalf("directory=%d bootstrap=%d; a Product-only session resolves neither", directoryCalls, bootstrapCalls)
	}
	if prompt := hostPrompt(t, argv); prompt != "Concord identity: product_id=product-1" {
		t.Fatalf("prompt=%q", prompt)
	}
	// CD-0093 D2 still binds: the host runs in the one resolved directory.
	if resolved, err := filepath.EvalSymlinks(ranIn); err != nil || resolved != mustEvalSymlinks(t, launcherDir) {
		t.Fatalf("ran in %q (resolved %q, err %v), want the launcher directory", ranIn, resolved, err)
	}
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

// seedWorktreeFixture seeds the Product, Project, and work that
// seedSessionProject seeds, registers projectDir as the Project canonical
// path, and records an active worktree entry for work-1 at worktreePath.
// It returns the database path and closes the seeding store so production
// wiring can open the file itself.
func seedWorktreeFixture(t *testing.T, projectDir, worktreePath string) string {
	t.Helper()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open worktree fixture store: %v", err)
	}
	seedSessionProduct(t, s, "product-1", "Session product")
	seedSessionWork(t, s, "work-1", "product-1")
	seedApprovalWorkflow(t, s, "work-1")
	sessionFixtureExec(t, s, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES ('locator-session','product-1-project','canonical_path',?,?,'now','now')`, projectDir, projectDir)
	base := strings.Repeat("a", 40)
	branch := "work/work-1"
	sessionFixtureExec(t, s, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES ('wt-op-1','work-1','product-1-project',?,'repo-1',?,?,?,'verified','operator','req-1','now','now')`,
		store.WorktreeSetID("work-1"), branch, base, filepath.Clean(worktreePath))
	sessionFixtureExec(t, s, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts) VALUES (?,'product-1-project','wt-op-1',?,?,?,'repo-1','active','now','{}')`,
		store.WorktreeSetID("work-1"), branch, base, filepath.Clean(worktreePath))
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("close worktree fixture store: %v", err)
	}
	return path
}

// TestSessionStartsInTheActiveWorktree covers CD-0176: the selected work's
// active, on-disk worktree is the landing directory, and it governs the
// registry probe and host execution alike (CD-0093 D2). Only the worktree
// carries the host artifacts, so a session that falls back to the Project
// directory cannot start at all.
func TestSessionStartsInTheActiveWorktree(t *testing.T) {
	projectDir := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "wt-on-disk")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProjectHostArtifacts(t, worktree)
	t.Setenv(dbOverrideEnv, seedWorktreeFixture(t, projectDir, worktree))

	launcherDir := t.TempDir()
	recordDir := t.TempDir()
	installFakeHost(t, recordDir)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(launcherDir)
	setIdentityLaunchEnv(t, "work-1", "", "concord-1")

	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		hostSessionDirectory, hostSessionHostCommand, DeriveSessionBoot, runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
	if code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if probe := hostRecord(t, recordDir, "probe-cwd"); probe != worktree {
		t.Fatalf("registry probed %q, want the active worktree %q", probe, worktree)
	}
	if host := hostRecord(t, recordDir, "host-cwd"); host != worktree {
		t.Fatalf("host started in %q, want the active worktree %q", host, worktree)
	}
}

// TestSessionFallsBackToTheProjectPathWithoutTheWorktreeOnDisk covers the
// CD-0176 boundary: an active entry whose path is absent on this machine is
// not a landing site. The session starts in the Project canonical path
// (CD-0093 D1), whose own refusal terms are unchanged (CD-0093 D3).
func TestSessionFallsBackToTheProjectPathWithoutTheWorktreeOnDisk(t *testing.T) {
	projectDir := t.TempDir()
	writeProjectHostArtifacts(t, projectDir)
	gone := filepath.Join(t.TempDir(), "wt-gone")
	t.Setenv(dbOverrideEnv, seedWorktreeFixture(t, projectDir, gone))

	launcherDir := t.TempDir()
	recordDir := t.TempDir()
	installFakeHost(t, recordDir)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(launcherDir)
	setIdentityLaunchEnv(t, "work-1", "", "concord-1")

	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		hostSessionDirectory, hostSessionHostCommand, DeriveSessionBoot, runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
	if code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if probe := hostRecord(t, recordDir, "probe-cwd"); probe != projectDir {
		t.Fatalf("registry probed %q, want the Project directory %q", probe, projectDir)
	}
	if host := hostRecord(t, recordDir, "host-cwd"); host != projectDir {
		t.Fatalf("host started in %q, want the Project directory %q", host, projectDir)
	}
}

// TestSessionProjectSelectionResolvesTheMemberProject covers the explicit
// Project selection (CD-0182): the selection reaches the directory resolver
// with the selected work, the fixed prompt tells the new coordinator to
// resume the work item through concord_work_start with both identities, and
// an unusable selection refuses before identity verification or any host
// starts.
func TestSessionProjectSelectionResolvesTheMemberProject(t *testing.T) {
	t.Run("selection reaches the resolver and the fixed prompt names the resume route", func(t *testing.T) {
		setIdentityLaunchEnv(t, "work-1", "project-two", "concord-1")
		resolvedWork, resolvedProject := "", ""
		var ranIn string
		var argv []string
		code := runSessionCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true,
			func(_ context.Context, workID, projectID string) (string, error) {
				resolvedWork, resolvedProject = workID, projectID
				return "/selected/landing", nil
			},
			hostCommandAt(defaultHostResolution()),
			func(context.Context, string, string, string) ([]byte, error) { return []byte("packet"), nil },
			func(_ context.Context, dir string, got []string, _ []string, _ io.Reader, _, _ io.Writer) error {
				ranIn = dir
				argv = append([]string(nil), got...)
				return nil
			},
			func(string) error { return nil },
			func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
				return "concord-1", nil
			})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if resolvedWork != "work-1" || resolvedProject != "project-two" {
			t.Fatalf("resolver read work=%q project=%q, want work-1 and project-two", resolvedWork, resolvedProject)
		}
		if ranIn != "/selected/landing" {
			t.Fatalf("host started in %q, want the resolved landing", ranIn)
		}
		prompt := hostPrompt(t, argv)
		if !strings.Contains(prompt, "concord_work_start with work_id work-1 and project_id project-two") {
			t.Fatalf("prompt=%q, want the fixed resume route", prompt)
		}
		if !strings.Contains(prompt, "packet") {
			t.Fatalf("prompt=%q, want the continuity packet after the fixed route", prompt)
		}
	})
	// Both refusal shapes share one probe: neither callback may run and the
	// diagnostic names the refused selection.
	refusesSelection := func(work, project, wantDiagnostic string) {
		t.Helper()
		setIdentityLaunchEnv(t, work, project, "")
		identityCalls, runs := 0, 0
		var errOut bytes.Buffer
		code := runSessionCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &errOut, true,
			func(context.Context, string, string) (string, error) { return "/unused", nil },
			hostCommandAt(defaultHostResolution()),
			nil,
			func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
				runs++
				return nil
			},
			func(string) error { identityCalls++; return nil },
			func(context.Context, string, hostCommandResolution, string, string, string) (string, error) {
				return "concord-1", nil
			})
		if code != 2 || identityCalls != 0 || runs != 0 {
			t.Fatalf("exit=%d identity=%d runs=%d stderr=%q", code, identityCalls, runs, errOut.String())
		}
		if !strings.Contains(errOut.String(), wantDiagnostic) {
			t.Fatalf("diagnostic=%q", errOut.String())
		}
	}
	t.Run("selection without a selected work refuses", func(t *testing.T) {
		refusesSelection("", "project-two", "requires a selected work")
	})
	t.Run("invalid selection refuses", func(t *testing.T) {
		refusesSelection("work-1", "../escape", "Project selection is missing or invalid")
	})
}
