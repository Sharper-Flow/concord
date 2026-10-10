package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// sessionChildArg is the fixed argument sessionCommand re-executes this test
// binary with. TestMain intercepts it before the suite runs.
const sessionChildArg = "session"

// runSessionSubprocess is the subprocess helper behind the end-to-end zl
// witness. It runs the production session command with the production
// wiring — the real directory resolver, registry probe, orchestrator
// assertion, continuity derivation, and executor — against the environment
// the parent staged: the isolated store and the fake host on PATH. The only
// injected fact is the TTY: terminalStreams answers a real terminal in
// production, and a test child has none.
func runSessionSubprocess() int {
	return runSessionCommand(nil, os.Stdin, os.Stdout, os.Stderr, true,
		hostSessionDirectory, hostSessionHostCommand, DeriveSessionBoot, runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
}

// TestDefaultSessionLauncherHandsOnlyIdentityToCoreBootstrap proves CD-0078
// D3: the session argument vector is exactly the Concord binary and
// `session`, with identity carried in the environment. No session
// identifier can be forwarded.
func TestDefaultSessionLauncherHandsOnlyIdentityToCoreBootstrap(t *testing.T) {
	cmd, err := sessionCommand(sessionHandoff{ProductID: "product-1", WorkID: "work-1", Agent: defaultSessionAgent})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Args) != 2 || cmd.Args[0] != cmd.Path || cmd.Args[1] != "session" {
		t.Fatalf("session argv=%q path=%q", cmd.Args, cmd.Path)
	}
	selected := map[string]string{}
	for _, value := range cmd.Env {
		if strings.HasPrefix(value, selectedAgentEnv+"=") {
			selected["agent"] = strings.TrimPrefix(value, selectedAgentEnv+"=")
		}
		if strings.HasPrefix(value, selectedProductEnv+"=") {
			selected["product"] = strings.TrimPrefix(value, selectedProductEnv+"=")
		}
		if strings.HasPrefix(value, selectedWorkEnv+"=") {
			selected["work"] = strings.TrimPrefix(value, selectedWorkEnv+"=")
		}
	}
	if selected["product"] != "product-1" || selected["work"] != "work-1" {
		t.Fatalf("session env identity=%v", selected)
	}
	if selected["agent"] != "concord-1" {
		t.Fatalf("session env agent=%q, want concord-1", selected["agent"])
	}
}

func TestSessionCommandPassesPromptThroughEnvironment(t *testing.T) {
	cmd, err := sessionCommand(sessionHandoff{ProductID: "product-1", WorkID: "work-1", Prompt: "inspect the failing test", Agent: defaultSessionAgent})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range cmd.Env {
		if value == selectedPromptEnv+"=inspect the failing test" {
			return
		}
	}
	t.Fatalf("prompt was not passed through session environment: %v", cmd.Env)
}

// TestSessionCommandPassesProjectSelectionThroughEnvironment covers the
// CD-0182 selector: an explicit member Project reaches the session bootstrap
// through CONCORD_SELECTED_PROJECT_ID, and an inherited value is replaced
// rather than doubled.
func TestSessionCommandPassesProjectSelectionThroughEnvironment(t *testing.T) {
	t.Setenv(selectedProjectIDEnv, "stale-project")
	cmd, err := sessionCommand(sessionHandoff{ProductID: "product-1", WorkID: "work-1", ProjectID: "project-two", Agent: defaultSessionAgent})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, value := range cmd.Env {
		if value == selectedProjectIDEnv+"=project-two" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("Project selection reached the session env %d time(s), want exactly the fresh value: %v", seen, cmd.Env)
	}
	if cmd, err := sessionCommand(sessionHandoff{ProductID: "product-1", WorkID: "work-1", Agent: defaultSessionAgent}); err != nil {
		t.Fatal(err)
	} else {
		for _, value := range cmd.Env {
			if strings.HasPrefix(value, selectedProjectIDEnv+"=") {
				t.Fatalf("empty selection leaked into the session env: %q", value)
			}
		}
	}
}

func TestSessionCommandPreservesInheritedAgentOverride(t *testing.T) {
	t.Setenv(selectedAgentEnv, "operator-agent")
	cmd, err := sessionCommand(sessionHandoff{ProductID: "product-1", Agent: defaultSessionAgent})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range cmd.Env {
		if value == selectedAgentEnv+"=operator-agent" {
			return
		}
	}
	t.Fatalf("inherited agent override was not preserved: %v", cmd.Env)
}

func TestSessionLauncherFailsClosedWithoutRunningBinaryIdentity(t *testing.T) {
	original := executablePath
	executablePath = func() (string, error) { return "", errors.New("unavailable") }
	defer func() { executablePath = original }()
	if cmd, err := sessionCommand(sessionHandoff{ProductID: "product-1"}); err == nil || cmd != nil {
		t.Fatalf("session process=%v err=%v", cmd, err)
	}
}

// TestArgv0ZLForwardsToTheZLRoute proves the argv[0] `zl` entry routes into
// zl forwarding rather than the bare-concord usage exit: the diagnostic
// prefix is the zl parser's, and the refusal is the missing work ID.
func TestArgv0ZLForwardsToTheZLRoute(t *testing.T) {
	original := os.Args[0]
	defer func() { os.Args[0] = original }()
	os.Args[0] = filepath.Join(t.TempDir(), "zl")
	var out, errOut bytes.Buffer
	if code := runWithInput(nil, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("argv0 zl exit=%d, want 2; stderr=%q", code, errOut.String())
	}
	if !strings.HasPrefix(errOut.String(), "concord zl: work ID is required") {
		t.Fatalf("argv0 zl stderr=%q, want the zl forwarding refusal", errOut.String())
	}
}

// recordingHostScript is the fake host the end-to-end witness installs on
// PATH. Its registry branch answers `debug config` with the registry
// document the session directory carries. Its session branch records the
// argument count, the leading flags, the full prompt, and its own working
// directory: the boundary evidence the witness asserts on.
const recordingHostScript = `#!/bin/sh
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
printf '%s\n' "$#" > "$record/host-argc"
printf '%s\n' "$1" "$2" "$3" > "$record/host-flags"
printf '%s' "$4" > "$record/host-prompt"
pwd > "$record/host-cwd"
exit 0
`

// hostPromptRecord reads the full prompt the fake host received. Unlike
// hostRecord it does not trim: the recorded bytes are the prompt the session
// command assembled.
func hostPromptRecord(t *testing.T, recordDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(recordDir, "host-prompt"))
	if err != nil {
		t.Fatalf("fake host prompt record: %v", err)
	}
	return string(data)
}

func sessionDurableCounts(t *testing.T, dbPath string) map[string]int {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	return durableCounts(t, s)
}

// The session child records its identity, with no other durable effects.
func requireOrchestratorAssertion(t *testing.T, dbPath string, before map[string]int) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var recorded int
	if err := s.DatabaseForTesting().QueryRow(
		`SELECT count(*) FROM domain_events WHERE kind = ? AND subject_id = 'work-1'`,
		store.EventSessionOrchestratorIdentityAsserted,
	).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded < 1 {
		t.Fatalf("orchestrator identity assertions = %d, want the session child's recorded event", recorded)
	}
	for table, count := range durableCounts(t, s) {
		want := before[table]
		if table == "domain_events" {
			want++ // The session child records one identity event, not a parent write.
		}
		if count != want {
			t.Fatalf("%s count=%d, want %d; forwarding added an unexpected durable effect", table, count, want)
		}
	}
}

// stageWitnessFixture places the host artifacts in projectDir and seeds one
// Product, one Project, one work item, and the implementation workflow
// instance with a canonical_path locator at projectDir. It returns the
// isolated database path.
func stageWitnessFixture(t *testing.T, projectDir string) string {
	t.Helper()
	writeProjectHostArtifacts(t, projectDir)
	return seedSessionProject(t, projectDir)
}

// The child owns session effects. This test isolates the forwarding read from
// that process boundary and proves CD-0108 D4 before any child can write.
func TestZLForwardingReadsLeaveAuthorityUnchanged(t *testing.T) {
	dbPath := seedSessionProject(t, t.TempDir())
	t.Setenv(dbOverrideEnv, dbPath)
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before := durableCounts(t, s)
	beforeLog := eventLogState(t, s)
	original := forwardSession
	defer func() { forwardSession = original }()
	starts := 0
	forwardSession = func(product, work, prompt, project string, _ io.Reader, _, _ io.Writer) int {
		starts++
		if product != "product-1" || work != "work-1" || prompt != "" {
			t.Fatalf("forwarded identity=%q/%q prompt=%q", product, work, prompt)
		}
		if after := durableCounts(t, s); !reflect.DeepEqual(before, after) || eventLogState(t, s) != beforeLog {
			t.Fatalf("forwarding wrote authority before child start: before=%v after=%v", before, after)
		}
		return 0
	}
	for _, args := range [][]string{{"zl", "work-1"}, {"zl", "work-1", "--project", "product-1-project"}} {
		var out, diagnostic bytes.Buffer
		if code := runWithInput(args, strings.NewReader(""), &out, &diagnostic); code != 0 {
			t.Fatalf("%v exit=%d stderr=%q", args, code, diagnostic.String())
		}
	}
	if starts != 2 {
		t.Fatalf("forwarded child starts=%d, want 2", starts)
	}
	if after := durableCounts(t, s); !reflect.DeepEqual(before, after) || eventLogState(t, s) != beforeLog {
		t.Fatalf("forwarding wrote authority after child start: before=%v after=%v", before, after)
	}
}

// TestZLForwardingStartsASessionThroughTheRealSessionChild is the
// end-to-end witness for the zl entry route (CD-0108 as amended by
// CD-0219). Nothing on the path is stubbed: runZLForwarding resolves the
// Product through the real store resolver, forwardSession is the real
// launch, sessionCommand re-executes this test binary with the fixed
// `session` argument, and the child runs the production session command —
// real store landing, real identity verification, real registry probe, real
// continuity derivation, real executor — against a fake host on PATH and an
// isolated store. The only injected fact is the TTY the child cannot have.
func TestZLForwardingStartsASessionThroughTheRealSessionChild(t *testing.T) {
	projectDir := t.TempDir()
	dbPath := stageWitnessFixture(t, projectDir)
	t.Setenv(dbOverrideEnv, dbPath)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	t.Run("work selection lands and boots through the real child", func(t *testing.T) {
		before := sessionDurableCounts(t, dbPath)
		recordDir := t.TempDir()
		installFakeCommand(t, recordDir, "opencode", recordingHostScript)
		var out, errOut bytes.Buffer
		code := runWithInput([]string{"zl", "work-1", "--", "inspect the failing handoff witness"}, strings.NewReader(""), &out, &errOut)
		if code != 0 {
			t.Fatalf("zl exit=%d stderr=%q stdout=%q", code, errOut.String(), out.String())
		}
		if probe := hostRecord(t, recordDir, "probe-cwd"); probe != projectDir {
			t.Fatalf("registry probed %q, want the resolved Project directory %q", probe, projectDir)
		}
		if host := hostRecord(t, recordDir, "host-cwd"); host != projectDir {
			t.Fatalf("host started in %q, want the resolved Project directory %q", host, projectDir)
		}
		if argc := hostRecord(t, recordDir, "host-argc"); argc != "4" {
			t.Fatalf("host argc=%s, want the fixed 4-argument vector", argc)
		}
		flags := strings.Split(hostRecord(t, recordDir, "host-flags"), "\n")
		if len(flags) != 3 || flags[0] != "--agent" || flags[1] != defaultSessionAgent || flags[2] != "--prompt" {
			t.Fatalf("host flags=%q, want --agent %s --prompt", flags, defaultSessionAgent)
		}
		prompt := hostPromptRecord(t, recordDir)
		if !strings.HasPrefix(prompt, "inspect the failing handoff witness\n\nConcord session boot packet") {
			t.Fatalf("host prompt %q lacks the operator prompt and the boot packet preamble", prompt)
		}
		if !strings.Contains(prompt, `"product_id":"product-1","work_id":"work-1"`) {
			t.Fatalf("host prompt %q lacks the core-derived continuity identity", prompt)
		}
		requireOrchestratorAssertion(t, dbPath, before)
	})

	t.Run("project selection lands the member project", func(t *testing.T) {
		before := sessionDurableCounts(t, dbPath)
		recordDir := t.TempDir()
		installFakeCommand(t, recordDir, "opencode", recordingHostScript)
		var out, errOut bytes.Buffer
		code := runWithInput([]string{"zl", "--project", "product-1-project", "work-1"}, strings.NewReader(""), &out, &errOut)
		if code != 0 {
			t.Fatalf("zl --project exit=%d stderr=%q stdout=%q", code, errOut.String(), out.String())
		}
		if host := hostRecord(t, recordDir, "host-cwd"); host != projectDir {
			t.Fatalf("host started in %q, want the member Project directory %q", host, projectDir)
		}
		prompt := hostPromptRecord(t, recordDir)
		want := "You are the coordinator session for work work-1 in this repository. Resume the work item now: call concord_work_start with work_id work-1 and project_id product-1-project."
		if !strings.HasPrefix(prompt, want) {
			t.Fatalf("host prompt %q lacks the fixed coordinator prompt %q", prompt, want)
		}
		if !strings.Contains(prompt, `"product_id":"product-1","work_id":"work-1"`) {
			t.Fatalf("host prompt %q lacks the core-derived continuity identity", prompt)
		}
		requireOrchestratorAssertion(t, dbPath, before)
	})

	t.Run("resume last relaunches the recorded work", func(t *testing.T) {
		before := sessionDurableCounts(t, dbPath)
		recordDir := t.TempDir()
		installFakeCommand(t, recordDir, "opencode", recordingHostScript)
		t.Setenv("CONCORD_LAST_WORK_ID", "work-1")
		t.Setenv(selectedProductEnv, "product-1")
		var out, errOut bytes.Buffer
		code := runWithInput([]string{"zl", "--resume-last"}, strings.NewReader(""), &out, &errOut)
		if code != 0 {
			t.Fatalf("zl --resume-last exit=%d stderr=%q stdout=%q", code, errOut.String(), out.String())
		}
		if host := hostRecord(t, recordDir, "host-cwd"); host != projectDir {
			t.Fatalf("host started in %q, want the resolved Project directory %q", host, projectDir)
		}
		prompt := hostPromptRecord(t, recordDir)
		if !strings.HasPrefix(prompt, "Concord identity: product_id=product-1\n\nConcord session boot packet") {
			t.Fatalf("host prompt %q lacks the default identity prompt and the boot packet preamble", prompt)
		}
		if !strings.Contains(prompt, `"product_id":"product-1","work_id":"work-1"`) {
			t.Fatalf("host prompt %q lacks the core-derived continuity identity", prompt)
		}
		requireOrchestratorAssertion(t, dbPath, before)
	})
}
