package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

// seedSessionAuthority registers a Product, a Project whose canonical_path is
// the supplied directory, and a work item whose primary Project membership is
// that Project — the store shape `concord session` resolves the session
// directory from (CD-0093 D1).
func seedSessionAuthority(t *testing.T, dbPath, dir string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "sd-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"display_name":"session","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "sd-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-b", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"display_name":"Project B"}`)},
		{EventID: "sd-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"product_id":"product-1","project_id":"project-b","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-b"): 0}}))
	must(store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "sd-work-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-661", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Session directory work","priority":1}`)},
		{EventID: "sd-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-661", Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-b","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-661"): 0}}))
	must(s.AddProjectLocator(ctx, "project-b", store.ProjectLocator{ID: "path-b", Kind: store.LocatorCanonicalPath, Value: dir}, 1))
}

// writeHostDouble installs a fake `opencode` on PATH that behaves like the
// real host in the two ways the session contract depends on: `debug config`
// prints the registry the working directory resolves (its
// .opencode/registry.json), and a session invocation answers an unregistered
// --agent name by silently substituting the default agent and exiting zero
// (CD-0049 D2). Every session invocation appends what actually executed to
// the witness file, which is how a test observes the substituted executor the
// host itself never reports.
func writeHostDouble(t *testing.T, witness string) {
	t.Helper()
	binDir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "debug" ] && [ "$2" = "config" ]; then
  cat "$PWD/.opencode/registry.json"
  exit 0
fi
agent=""
while [ $# -gt 0 ]; do
  case "$1" in
    --agent) agent="$2"; shift 2 ;;
    *) shift ;;
  esac
done
executed="$agent"
if ! grep -q "\"$agent\"" "$PWD/.opencode/registry.json" 2>/dev/null; then
  executed="default"
fi
{
  printf 'cwd=%s\n' "$PWD"
  printf 'agent=%s\n' "$agent"
  printf 'executed=%s\n' "$executed"
} >> '` + witness + `'
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeHostRegistry declares the registry a directory resolves: every named
// handle is an enabled primary agent.
func writeHostRegistry(t *testing.T, dir string, handles ...string) {
	t.Helper()
	agents := make(map[string]hostAgentEntry, len(handles))
	for _, handle := range handles {
		agents[handle] = hostAgentEntry{Mode: "primary"}
	}
	document, err := json.Marshal(hostConfigDocument{Agent: agents})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".opencode", "registry.json"), document, 0o644); err != nil {
		t.Fatal(err)
	}
}

// hostInvocation is what the host double records for one session start: the
// directory the host process ran in, the agent the session selected, and the
// agent that actually executed.
type hostInvocation struct {
	CWD      string
	Agent    string
	Executed string
}

// readHostWitness returns the host invocations the double recorded. An absent
// witness means no host started, which is the fail-closed outcome a refusal
// must leave behind.
func readHostWitness(t *testing.T, witness string) []hostInvocation {
	t.Helper()
	data, err := os.ReadFile(witness)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines)%3 != 0 {
		t.Fatalf("witness is not a sequence of cwd/agent/executed records: %q", string(data))
	}
	invocations := make([]hostInvocation, 0, len(lines)/3)
	for i := 0; i < len(lines); i += 3 {
		cwd, ok := strings.CutPrefix(lines[i], "cwd=")
		if !ok {
			t.Fatalf("witness cwd line malformed: %q", lines[i])
		}
		agent, ok := strings.CutPrefix(lines[i+1], "agent=")
		if !ok {
			t.Fatalf("witness agent line malformed: %q", lines[i+1])
		}
		executed, ok := strings.CutPrefix(lines[i+2], "executed=")
		if !ok {
			t.Fatalf("witness executed line malformed: %q", lines[i+2])
		}
		invocations = append(invocations, hostInvocation{CWD: cwd, Agent: agent, Executed: executed})
	}
	return invocations
}

// TestSessionOpensInTheSelectedWorkProjectDirectory is issue #661's
// reproduction: a launcher started in Project A's directory selects work whose
// Project is B, and the session must open in B's canonical path. The test
// drives the production session wiring — the real identity verification, the
// real registry probe, and the real host runner — against a PATH-installed
// host double, because the property under test is the host process's working
// directory, which no injected callback can observe.
func TestSessionOpensInTheSelectedWorkProjectDirectory(t *testing.T) {
	home := t.TempDir()
	launcherDir, projectDir := t.TempDir(), t.TempDir()
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, filepath.Join(home, ".config", "opencode", "agents"), laneAgentFileName(lane.ID))
	}
	writeAgentDefinition(t, filepath.Join(home, ".config", "opencode", "agents"), orchestratorAgentFileName)
	dbPath := filepath.Join(t.TempDir(), "concord-session-directory.db")
	seedSessionAuthority(t, dbPath, projectDir)
	t.Setenv("CONCORD_DB_PATH", dbPath)
	t.Setenv("HOME", home)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-661")
	witness := filepath.Join(t.TempDir(), "host-witness")
	writeHostDouble(t, witness)
	writeHostRegistry(t, launcherDir, orchestratorAgentName)
	writeHostRegistry(t, projectDir, orchestratorAgentName)
	t.Chdir(launcherDir)
	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		func(context.Context, string, string, string) ([]byte, error) {
			return sessionboot.Build("product-1", store.ContinuitySnapshot{
				WorkID: "work-661", ProductIdentity: []string{"product-1"}, WorkflowStep: "start",
				SpecMandate: []string{}, Boundaries: []store.ContextBoundary{}, Watermark: "seq:1",
				RestartUnavailableReason: "typed restart is deliberately excluded",
			})
		},
		runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
	if code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	invocations := readHostWitness(t, witness)
	if len(invocations) != 1 {
		t.Fatalf("host started %d time(s), want exactly 1", len(invocations))
	}
	if invocations[0].CWD != projectDir {
		t.Fatalf("issue #661: session opened in %q, want the selected work's Project directory %q", invocations[0].CWD, projectDir)
	}
	if invocations[0].Agent != orchestratorAgentName || invocations[0].Executed != orchestratorAgentName {
		t.Fatalf("host ran agent=%q executed=%q, want %q", invocations[0].Agent, invocations[0].Executed, orchestratorAgentName)
	}
}
