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

// installFakeHost puts the fake host binary first on PATH and points its
// record directory at recordDir.
func installFakeHost(t *testing.T, recordDir string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(fakeHostScript), 0o755); err != nil {
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
	seedLauncherCorpusProduct(t, s, "product-1", "Session product")
	seedLauncherCorpusWork(t, s, "work-1", "product-1", "task", "Session work", "needed", 1, "2026-09-01T00:00:00Z", "2026-09-01T00:00:00Z")
	seedApprovalWorkflow(t, s, "work-1")
	if projectDir != "" {
		corpusExec(t, s, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES ('locator-session','product-1-project','canonical_path',?,?, 'now','now')`, projectDir, projectDir)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatalf("close session fixture store: %v", err)
	}
	return path
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
	writeAgentDefinitionBody(t, agents, orchestratorAgentFileName, []byte("---\nmode: all\n---\norchestrator\n"))
	registry, err := json.Marshal(hostConfigDocument{Agent: map[string]hostAgentEntry{
		orchestratorAgentName: {Mode: "primary"},
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
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")

	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		hostSessionDirectory, DeriveSessionBoot, runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
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
	if argvLines[1] != "--agent" || argvLines[2] != orchestratorAgentName || argvLines[3] != "--prompt" {
		t.Fatalf("host argument vector=%q, want --agent %s --prompt", argvLines, orchestratorAgentName)
	}
}

// TestSessionRefusesWhenTheProjectDirectoryDoesNotResolve covers CD-0093 D3:
// a canonical path that does not resolve on this machine refuses the launch
// before identity verification runs and before any host starts.
func TestSessionRefusesWhenTheProjectDirectoryDoesNotResolve(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	t.Setenv(dbOverrideEnv, seedSessionProject(t, gone))
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")

	identityCalls, runs := 0, 0
	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		hostSessionDirectory,
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			runs++
			return nil
		},
		func(string) error { identityCalls++; return nil },
		func(context.Context, string, string, string) (string, error) { return orchestratorAgentName, nil })
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
		t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
		t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
		identityCalls, runs := 0, 0
		var out, errOut bytes.Buffer
		code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
			hostSessionDirectory,
			func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
			func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
				runs++
				return nil
			},
			func(string) error { identityCalls++; return nil },
			func(context.Context, string, string, string) (string, error) { return orchestratorAgentName, nil })
		if code != 2 || identityCalls != 0 || runs != 0 {
			t.Fatalf("exit=%d identity=%d runs=%d stderr=%q", code, identityCalls, runs, errOut.String())
		}
		if !strings.Contains(errOut.String(), "no primary Project") {
			t.Fatalf("diagnostic=%q", errOut.String())
		}
	})
	t.Run("no canonical path locator", func(t *testing.T) {
		t.Setenv(dbOverrideEnv, seedSessionProject(t, ""))
		t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
		t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
		identityCalls, runs := 0, 0
		var out, errOut bytes.Buffer
		code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
			hostSessionDirectory,
			func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
			func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
				runs++
				return nil
			},
			func(string) error { identityCalls++; return nil },
			func(context.Context, string, string, string) (string, error) { return orchestratorAgentName, nil })
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
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "")
	launcherDir := t.TempDir()
	t.Chdir(launcherDir)
	bootstrapCalls, directoryCalls := 0, 0
	var argv []string
	var ranIn string
	code := runSessionCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true,
		func(context.Context, string) (string, error) { directoryCalls++; return "", nil },
		func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil },
		func(_ context.Context, dir string, got []string, _ []string, _ io.Reader, _, _ io.Writer) error {
			ranIn = dir
			argv = append([]string(nil), got...)
			return nil
		},
		func(string) error { return nil },
		func(context.Context, string, string, string) (string, error) { return orchestratorAgentName, nil })
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
