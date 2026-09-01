package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

// seedSessionAuthorityState registers a Product, a Project, and work-661 with
// the D3 failure authorities removable: secondaryOnly leaves the work with no
// primary Project membership, and withLocator controls the canonical_path
// locator.
func seedSessionAuthorityState(t *testing.T, dbPath, dir string, secondaryOnly, withLocator bool) {
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
	// The fold requires work.created to carry its membership in the same
	// operation, so the no-primary case is a work whose only membership is
	// secondary rather than a work with none.
	membership := store.Event{EventID: "sd-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-661", Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1}
	if secondaryOnly {
		membership.Payload = jsonRaw(`{"memberships":[{"project_id":"project-b","role":"secondary"}],"expected_version":1,"resulting_version":2}`)
	} else {
		membership.Payload = jsonRaw(`{"memberships":[{"project_id":"project-b","role":"primary"}],"expected_version":1,"resulting_version":2}`)
	}
	must(store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "sd-work-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-661", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Session directory work","priority":1}`)},
		membership,
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-661"): 0}}))
	if withLocator {
		must(s.AddProjectLocator(ctx, "project-b", store.ProjectLocator{ID: "path-b", Kind: store.LocatorCanonicalPath, Value: dir}, 1))
	}
}

// seedSessionAuthority registers the complete session-directory authority:
// primary membership plus canonical_path locator.
func seedSessionAuthority(t *testing.T, dbPath, dir string) {
	t.Helper()
	seedSessionAuthorityState(t, dbPath, dir, false, true)
}

// refuseSessionWithAuthority runs `concord session` for work-661 against the
// production directory resolver with counting callbacks, and returns the exit
// code, stderr, and the call counts.
func refuseSessionWithAuthority(t *testing.T, dbPath string) (int, string, int, int, int) {
	t.Helper()
	t.Setenv("CONCORD_DB_PATH", dbPath)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-661")
	identityCalls, runs, bootstrapCalls := 0, 0, 0
	var out, errOut bytes.Buffer
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		resolveSessionDirectory,
		func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil },
		func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			runs++
			return nil
		},
		func() error { identityCalls++; return nil },
		func(context.Context, string, string) (string, error) { return orchestratorAgentName, nil })
	return code, errOut.String(), identityCalls, runs, bootstrapCalls
}

// The three CD-0093 D3 refusal cases: a work whose only membership is
// secondary has no owning primary Project; a primary Project without a
// canonical_path locator names the missing registration; a canonical path
// that does not resolve on this machine refuses rather than starting a
// session in a fallback directory. Every case refuses before the identity
// callbacks, the packet, or the host.
func TestSessionRefusesWhenDirectoryAuthorityIsMissing(t *testing.T) {
	t.Run("no primary Project", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "concord-no-primary.db")
		seedSessionAuthorityState(t, dbPath, t.TempDir(), true, true)
		code, stderr, identityCalls, runs, bootstrapCalls := refuseSessionWithAuthority(t, dbPath)
		if code != 2 || identityCalls != 0 || runs != 0 || bootstrapCalls != 0 {
			t.Fatalf("code=%d identity=%d runs=%d bootstrap=%d stderr=%q", code, identityCalls, runs, bootstrapCalls, stderr)
		}
		if !strings.Contains(stderr, "no primary Project") {
			t.Fatalf("diagnostic omits the missing primary Project: %q", stderr)
		}
	})
	t.Run("no canonical_path locator", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "concord-no-locator.db")
		seedSessionAuthorityState(t, dbPath, t.TempDir(), false, false)
		code, stderr, identityCalls, runs, bootstrapCalls := refuseSessionWithAuthority(t, dbPath)
		if code != 2 || identityCalls != 0 || runs != 0 || bootstrapCalls != 0 {
			t.Fatalf("code=%d identity=%d runs=%d bootstrap=%d stderr=%q", code, identityCalls, runs, bootstrapCalls, stderr)
		}
		if !strings.Contains(stderr, "no canonical_path locator") {
			t.Fatalf("diagnostic omits the missing canonical_path locator: %q", stderr)
		}
	})
	t.Run("canonical path does not resolve", func(t *testing.T) {
		stale := t.TempDir()
		dbPath := filepath.Join(t.TempDir(), "concord-stale-path.db")
		seedSessionAuthorityState(t, dbPath, stale, false, true)
		if err := os.RemoveAll(stale); err != nil {
			t.Fatal(err)
		}
		code, stderr, identityCalls, runs, bootstrapCalls := refuseSessionWithAuthority(t, dbPath)
		if code != 2 || identityCalls != 0 || runs != 0 || bootstrapCalls != 0 {
			t.Fatalf("code=%d identity=%d runs=%d bootstrap=%d stderr=%q", code, identityCalls, runs, bootstrapCalls, stderr)
		}
		if !strings.Contains(stderr, "session directory does not resolve") {
			t.Fatalf("diagnostic omits the unresolved path: %q", stderr)
		}
	})
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
		resolveSessionDirectory,
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
