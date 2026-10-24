package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

func TestContinuityBlockPrintsDeterministicPacketForDirectory(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	directory := "/session/worktree"
	bootstrap := func(_ context.Context, _, dir string) ([]byte, error) {
		if dir != directory {
			t.Fatalf("continuity-block resolved %q, want the supplied directory %q", dir, directory)
		}
		return sessionboot.Build("product-1", store.ContinuitySnapshot{
			WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "planning",
			SpecMandate: []string{}, Boundaries: []store.ContextBoundary{}, Watermark: "seq:42",
			RestartUnavailableReason: "typed restart is deliberately excluded", PendingMessages: 3,
		})
	}
	var packets []string
	for range 2 {
		var out, errOut bytes.Buffer
		if code := runContinuityBlockCommandWithDirectory([]string{directory}, &out, &errOut, bootstrap); code != 0 {
			t.Fatalf("continuity-block exit=%d stderr=%q", code, errOut.String())
		}
		packets = append(packets, out.String())
	}
	if packets[0] == "" || packets[0] != packets[1] {
		t.Fatalf("continuity-block packets differ: %q vs %q", packets[0], packets[1])
	}
	if !strings.Contains(packets[0], `"pending_messages":3`) {
		t.Fatalf("continuity-block omitted pending messages: %q", packets[0])
	}
	if err := sessionboot.Validate([]byte(packets[0])); err != nil {
		t.Fatalf("continuity-block packet invalid: %v", err)
	}
}

func TestContinuityBlockWithoutResolvedClaimPrintsNothing(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	bootstrap := func(context.Context, string, string) ([]byte, error) {
		return nil, nil
	}
	var out, errOut bytes.Buffer
	if code := runContinuityBlockCommandWithDirectory([]string{"/session/main-checkout"}, &out, &errOut, bootstrap); code != 0 {
		t.Fatalf("continuity-block exit=%d stderr=%q", code, errOut.String())
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("continuity-block unresolved output=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestContinuityBlockRefusesArgumentCountsOtherThanOne(t *testing.T) {
	bootstrap := func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("bootstrap must not run")
	}
	for _, args := range [][]string{nil, {"one", "two"}} {
		var out, errOut bytes.Buffer
		if code := runContinuityBlockCommandWithDirectory(args, &out, &errOut, bootstrap); code != 2 {
			t.Fatalf("continuity-block %v exit=%d stderr=%q", args, code, errOut.String())
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "supply exactly one session directory") {
			t.Fatalf("continuity-block %v output=%q stderr=%q", args, out.String(), errOut.String())
		}
	}
}

func TestContinuityBlockReadFailureIsSilentOnStdout(t *testing.T) {
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	bootstrap := func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("continuity read failed")
	}
	var out, errOut bytes.Buffer
	if code := runContinuityBlockCommandWithDirectory([]string{"/session/worktree"}, &out, &errOut, bootstrap); code == 0 {
		t.Fatal("continuity-block succeeded after continuity read failure")
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "continuity read failed") {
		t.Fatalf("continuity-block failure output=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestContinuityBlockRecoversWhenReviewCandidateGitIsUnavailable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "session.db")
	t.Setenv(dbOverrideEnv, dbPath)
	s := mustOpenStore(t, dbPath)
	seedLocatorAuthority(t, s, initLocatorRepo(t))
	started, err := s.BootstrapWorktree(t.Context(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	execUnderFoldGuard(t, s.DatabaseForTesting(),
		fmt.Sprintf(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
		VALUES('%s',1,'session recovery fixture','internal_sqlite','[]','[]','now',(SELECT actor_ref FROM workflow_actors LIMIT 1),'[]','[]',1,'prototype_internal')`, started.WorkID),
		fmt.Sprintf(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
		VALUES('%s',1,'predicate:session',0,'exists','{"kind":"exists","subjects":["README.md"],"surface":"repository"}')`, started.WorkID))
	if _, err := s.VerifyWorktree(t.Context(), store.WorktreeVerifyRequest{
		WorkID: started.WorkID, ProjectID: "project-wl", Command: []string{"true"},
		Owner:   store.SessionWorktreeOwner{ClientRef: "client/session", AgentRef: "agent/session", SessionRef: "session/session"},
		LeaseID: "lease:session", PrincipalRef: "principal/session", RequestID: "request:session",
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ReadWorkflowContinuity(t.Context(), s, store.ContinuityRequest{Work: started.WorkID})
	if err != nil || snapshot.ReviewInput == nil {
		t.Fatalf("reachable review candidate: %+v, %v", snapshot.ReviewInput, err)
	}
	t.Cleanup(hideGitDir(t, started.Entry.Path))
	var out, errOut bytes.Buffer
	if code := run([]string{"continuity-block", started.Entry.Path}, &out, &errOut); code != 0 {
		t.Fatalf("session recovery exit=%d stderr=%q", code, errOut.String())
	}
	if err := sessionboot.Validate(out.Bytes()); err != nil {
		t.Fatalf("invalid session boot: %v", err)
	}
	var boot sessionboot.Packet
	if err := json.Unmarshal(out.Bytes(), &boot); err != nil || boot.WorkID != started.WorkID {
		t.Fatalf("session boot lost work identity: %s, %v", out.Bytes(), err)
	}
}

// hostCommandAt returns a sessionHostCommandFunc that reports the supplied
// resolution, standing in for what hostSessionPrepareRegistry resolves in
// production.
func hostCommandAt(host hostCommandResolution) sessionHostCommandFunc {
	return func(context.Context, string) (hostCommandResolution, error) { return host, nil }
}

// defaultHostResolution is the resolution tests inject when the test is not
// about the registry: the bare probe command and no registry, which only a
// stubbed orchestrator may observe.
func defaultHostResolution() hostCommandResolution {
	return hostCommandResolution{Command: append([]string(nil), defaultHostCommand...)}
}

// registryResolutionFor builds the registry resolution whose document
// declares each supplied handle as an enabled primary agent. Tests that are
// not about registration state use it to say "the host registers what the
// definition derives", so a registration refusal never masquerades as the
// failure under test.
func registryResolutionFor(handles ...string) hostCommandResolution {
	agents := make(map[string]hostAgentEntry, len(handles))
	for _, handle := range handles {
		agents[handle] = hostAgentEntry{Mode: "primary"}
	}
	document, err := json.Marshal(hostConfigDocument{Agent: agents})
	if err != nil {
		panic(err)
	}
	return hostCommandResolution{Command: append([]string(nil), defaultHostCommand...), Registry: document}
}

// recordedAssertion is the shape the identity tests read back from the store;
// the event is evidence, so a test that wants to recompute the digest needs
// only the digest and the source list.
type recordedAssertion struct {
	Type          string                             `json:"type"`
	Version       string                             `json:"version"`
	RulesetDigest string                             `json:"ruleset_digest"`
	Sources       []store.OrchestratorArtifactSource `json:"sources"`
}

func readRecordedAssertion(t *testing.T, dbPath string) (recordedAssertion, error) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		return recordedAssertion{}, err
	}
	defer s.Close()
	var payload []byte
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(),
		`SELECT payload FROM domain_events WHERE kind = ? ORDER BY seq DESC LIMIT 1`,
		store.EventSessionOrchestratorIdentityAsserted,
	).Scan(&payload); err != nil {
		return recordedAssertion{}, err
	}
	var ra recordedAssertion
	if err := json.Unmarshal(payload, &ra); err != nil {
		return recordedAssertion{}, err
	}
	return ra, nil
}

// TestSessionRecordsExactlyOneOrchestratorIdentityEvent covers CD-0061 D4:
// a prepared session with a resolvable active-agent definition records
// exactly one subject_type='session' domain event carrying the asserted
// type, version, and ruleset digest.
func TestSessionRecordsExactlyOneOrchestratorIdentityEvent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
	}
	writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), agentDefinitionFileName("concord-1"))
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "concord-assertion.db")
	t.Setenv(dbOverrideEnv, dbPath)
	if err := verifyLaneAgentIdentity(home, cwd, store.BuiltinLaneDefinitions()); err != nil {
		t.Fatalf("lane identity: %v", err)
	}
	if _, err := recordOrchestratorIdentity(context.Background(), home, registryResolutionFor("concord-1"), cwd, "product-1", "work-1", "concord-1"); err != nil {
		t.Fatalf("record assertion: %v", err)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	rows, err := s.DatabaseForTesting().QueryContext(context.Background(),
		`SELECT subject_type, payload FROM domain_events WHERE kind = ?`, store.EventSessionOrchestratorIdentityAsserted)
	if err != nil {
		t.Fatalf("read assertion events: %v", err)
	}
	defer rows.Close()
	type recordedEvent struct {
		SubjectType string
		Payload     []byte
	}
	var events []recordedEvent
	for rows.Next() {
		var evt recordedEvent
		if err := rows.Scan(&evt.SubjectType, &evt.Payload); err != nil {
			t.Fatalf("scan: %v", err)
		}
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("recorded %d session orchestrator events, want exactly 1", len(events))
	}
	if events[0].SubjectType != string(store.SubjectSession) {
		t.Fatalf("subject_type = %q, want %q", events[0].SubjectType, store.SubjectSession)
	}
	var payload struct {
		Type          string `json:"type"`
		Version       string `json:"version"`
		RulesetDigest string `json:"ruleset_digest"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Type != OrchestratorIdentityType {
		t.Fatalf("type = %q, want %q", payload.Type, OrchestratorIdentityType)
	}
	if payload.Version != OrchestratorIdentityVersion {
		t.Fatalf("version = %q, want %q", payload.Version, OrchestratorIdentityVersion)
	}
	if !strings.HasPrefix(payload.RulesetDigest, "sha256:") {
		t.Fatalf("ruleset_digest = %q, want sha256:<hex>", payload.RulesetDigest)
	}
}

// TestOrchestratorIdentityRefusalRecordsNothing covers the CD-0061 D4
// ordering property CD-0049 D4 admits: when the active agent's definition
// cannot resolve, or the registry does not register the derived handle, the
// assertion records nothing — verification fails before any store
// interaction, so the database file the environment names never exists.
func TestOrchestratorIdentityRefusalRecordsNothing(t *testing.T) {
	t.Run("definition absent", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		// Lane definitions resolve, but the active agent's definition does not.
		for _, lane := range store.BuiltinLaneDefinitions() {
			writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
		}
		t.Setenv("HOME", home)
		dbPath := filepath.Join(t.TempDir(), "concord-absent.db")
		t.Setenv(dbOverrideEnv, dbPath)
		_, err := recordOrchestratorIdentity(context.Background(), home, registryResolutionFor("concord-1"), cwd, "product-1", "work-1", "concord-1")
		if err == nil {
			t.Fatal("absent definition recorded an assertion")
		}
		stderr := err.Error()
		for _, fragment := range []string{
			"required agent identity is absent",
			agentDefinitionFileName("concord-1"),
			filepath.Join(cwd, ".opencode", "agents"),
			filepath.Join(home, ".config", "opencode", "agents"),
		} {
			if !strings.Contains(stderr, fragment) {
				t.Fatalf("diagnostic missing %q: %q", fragment, stderr)
			}
		}
		if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database was created at %q even though verification failed; the absent path must not touch the store", dbPath)
		}
	})
	t.Run("registry does not register the handle", func(t *testing.T) {
		home, cwd := t.TempDir(), t.TempDir()
		for _, lane := range store.BuiltinLaneDefinitions() {
			writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
		}
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), agentDefinitionFileName("concord-1"))
		t.Setenv("HOME", home)
		dbPath := filepath.Join(t.TempDir(), "concord-unregistered.db")
		t.Setenv(dbOverrideEnv, dbPath)
		_, err := recordOrchestratorIdentity(context.Background(), home, registryResolutionFor("some-other-agent"), cwd, "product-1", "work-1", "concord-1")
		if err == nil {
			t.Fatal("an unregistered handle recorded an assertion")
		}
		for _, fragment := range []string{"concord-1", "not registered"} {
			if !strings.Contains(err.Error(), fragment) {
				t.Fatalf("diagnostic %q omits %q", err.Error(), fragment)
			}
		}
		if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database created at %q despite a refused registration check", dbPath)
		}
	})
}

// TestOrchestratorIdentityDigestRecomputesAndChangesWithArtifact covers
// CD-0061 D5 / Invariant 4: the recorded digest equals a digest recomputed
// from the resolved artifacts, and changes when any resolved artifact
// changes.
func TestOrchestratorIdentityDigestRecomputesAndChangesWithArtifact(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
	}
	writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), agentDefinitionFileName("concord-1"))
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# instructions v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	dbPath := filepath.Join(t.TempDir(), "concord-digest.db")
	t.Setenv(dbOverrideEnv, dbPath)
	record := func() {
		t.Helper()
		if err := verifyLaneAgentIdentity(home, cwd, store.BuiltinLaneDefinitions()); err != nil {
			t.Fatalf("lane identity: %v", err)
		}
		if _, err := recordOrchestratorIdentity(context.Background(), home, registryResolutionFor("concord-1"), cwd, "product-1", "work-1", "concord-1"); err != nil {
			t.Fatalf("record assertion: %v", err)
		}
	}

	// First assertion — recorded against the current files.
	record()
	first, err := readRecordedAssertion(t, dbPath)
	if err != nil {
		t.Fatalf("read first assertion: %v", err)
	}
	// Recompute the digest from the same recorded source list and assert
	// equality. This is the property CD-0061 D5 / Invariant 4 requires:
	// the digest derives only from what was resolved.
	recomputed := computeOrchestratorRulesetDigest(first.Sources)
	if recomputed != first.RulesetDigest {
		t.Fatalf("recomputed digest %q != recorded %q", recomputed, first.RulesetDigest)
	}
	// Mutate AGENTS.md and re-record. The new digest must differ from the
	// first, both for the recomputed value and the recorded one.
	if err := os.WriteFile(agentsPath, []byte("# instructions v2 — silently changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Recompute the digest from the *current* filesystem (the verification
	// step re-reads and re-hashes every resolved artifact). The current
	// digest must differ from the recorded first one.
	currentAssertion, _, err := verifyOrchestratorIdentity(home, cwd, "concord-1")
	if err != nil {
		t.Fatalf("verify after mutation: %v", err)
	}
	recomputedFromCurrent := currentAssertion.RulesetDigest
	if recomputedFromCurrent == first.RulesetDigest {
		t.Fatalf("digest unchanged after AGENTS.md mutation: %q", recomputedFromCurrent)
	}
	record()
	second, err := readRecordedAssertion(t, dbPath)
	if err != nil {
		t.Fatalf("read second assertion: %v", err)
	}
	if second.RulesetDigest == first.RulesetDigest {
		t.Fatalf("recorded digest unchanged across artifact mutation")
	}
	if second.RulesetDigest != recomputedFromCurrent {
		t.Fatalf("recorded second digest %q != recomputed %q", second.RulesetDigest, recomputedFromCurrent)
	}
}

// The recorded agent reference is the handle the host runs the session as.
//
// The active agent below is concord-1, so the host registers it under that
// name and the assertion must record it. Recording the file name, or any
// other string, would attribute every session to an agent the host does not
// have — and under a fail-closed agent scope, to one no client can present.
func TestOrchestratorAssertionRecordsTheRegisteredHandle(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeAgentDefinitionBody(t, filepath.Join(cwd, ".opencode", "agents"), agentDefinitionFileName("concord-1"),
		[]byte("---\nmode: primary\n---\n"))
	dbPath := filepath.Join(t.TempDir(), "concord-handle.db")
	t.Setenv("CONCORD_DB_PATH", dbPath)

	handle, err := recordOrchestratorIdentity(context.Background(), home, registryResolutionFor("concord-1"), cwd, "product-1", "", "concord-1")
	if err != nil {
		t.Fatalf("record assertion: %v", err)
	}
	if handle != "concord-1" {
		t.Fatalf("handle=%q, want the frontmatter name", handle)
	}

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var actor string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(),
		`SELECT actor FROM domain_events WHERE kind = ?`, store.EventSessionOrchestratorIdentityAsserted).Scan(&actor); err != nil {
		t.Fatalf("read assertion event: %v", err)
	}
	// The agent reference is hashed into the actor rather than stored beside
	// it, so the derived reference is what proves which agent was recorded.
	want := store.DeriveWorkflowActorRef("principal/orchestrator", "client/concord-session", "agent/concord-1", "session/product-1")
	stem := store.DeriveWorkflowActorRef("principal/orchestrator", "client/concord-session", "agent/concord-1.md", "session/product-1")
	if actor == stem {
		t.Fatal("the assertion recorded the definition file name, not the handle the host runs")
	}
	if actor != want {
		t.Fatalf("recorded actor=%q, want the actor derived from agent/concord-1", actor)
	}
}
