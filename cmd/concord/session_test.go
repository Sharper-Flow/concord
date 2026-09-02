package main

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
	"time"

	"github.com/sharper-flow/concord/internal/sessionboot"
	"github.com/sharper-flow/concord/internal/store"
)

func TestSessionBootPassesCorePacketToOpenCodeBeforeSessionStarts(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	const sessionDir = "/resolved/project-directory"
	var argv []string
	var runnerDir string
	bootstrapCalls := 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) {
		bootstrapCalls++
		return sessionboot.Build("product-1", store.ContinuitySnapshot{
			WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "planning",
			SpecMandate: []string{}, Boundaries: []store.ContextBoundary{}, Watermark: "seq:4",
			RestartUnavailableReason: "typed restart is deliberately excluded", PendingMessages: 2,
		})
	}
	runner := func(_ context.Context, dir string, got []string, _ []string, _ io.Reader, _, _ io.Writer) error {
		runnerDir = dir
		argv = append([]string(nil), got...)
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, directoryAt(sessionDir), bootstrap, runner, func(string) error { return nil }, func(context.Context, string, string, string) (string, error) { return orchestratorAgentName, nil }); code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if bootstrapCalls != 1 {
		t.Fatalf("bootstrap calls=%d", bootstrapCalls)
	}
	if runnerDir != sessionDir {
		t.Fatalf("host ran in %q, want the resolved session directory %q", runnerDir, sessionDir)
	}
	if len(argv) != 5 || argv[0] != "opencode" {
		t.Fatalf("argv=%q", argv)
	}
	if selected := selectedAgentName(t, argv); selected != orchestratorAgentName {
		t.Fatalf("session selected agent %q, want %q", selected, orchestratorAgentName)
	}
	prompt := hostPrompt(t, argv)
	start := strings.IndexByte(prompt, '{')
	if start < 0 || !strings.Contains(prompt[:start], "core-derived authority") {
		t.Fatalf("prompt omitted boot header: %q", prompt)
	}
	if err := sessionboot.Validate([]byte(prompt[start:])); err != nil {
		t.Fatalf("prompt packet invalid: %v", err)
	}
}

func TestSessionBootFailsClosedBeforeOpenCodeOnPacketFailure(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	runs := 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) {
		return nil, errors.New("manifest digest mismatch")
	}
	runner := func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		runs++
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, directoryAt("/resolved/project-directory"), bootstrap, runner, func(string) error { return nil }, func(context.Context, string, string, string) (string, error) { return orchestratorAgentName, nil }); code == 0 {
		t.Fatal("packet failure started session")
	}
	if runs != 0 || !strings.Contains(errOut.String(), "manifest digest mismatch") {
		t.Fatalf("runs=%d stderr=%q", runs, errOut.String())
	}
}

func TestSessionRoutesBeforeJSONAndRejectsNonTTY(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"session"}, strings.NewReader("not json"), &out, &errOut); code != 2 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "interactive TTY") || strings.Contains(errOut.String(), "JSON") {
		t.Fatalf("session route diagnostic=%q", errOut.String())
	}
}

func TestContinuityBlockPrintsDeterministicPacket(t *testing.T) {
	t.Setenv(selectedProductEnv, "product-1")
	t.Setenv(selectedWorkEnv, "work-1")
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	bootstrap := func(context.Context, string, string, string) ([]byte, error) {
		return sessionboot.Build("product-1", store.ContinuitySnapshot{
			WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "planning",
			SpecMandate: []string{}, Boundaries: []store.ContextBoundary{}, Watermark: "seq:42",
			RestartUnavailableReason: "typed restart is deliberately excluded", PendingMessages: 3,
		})
	}
	var packets []string
	for range 2 {
		var out, errOut bytes.Buffer
		if code := runContinuityBlockCommandWithBootstrap(nil, &out, &errOut, bootstrap); code != 0 {
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

func TestContinuityBlockWithEmptyWorkIDDoesNothing(t *testing.T) {
	t.Setenv(selectedProductEnv, "product-1")
	t.Setenv(selectedWorkEnv, "")
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	called := false
	bootstrap := func(context.Context, string, string, string) ([]byte, error) {
		called = true
		return nil, errors.New("bootstrap must not run")
	}
	var out, errOut bytes.Buffer
	if code := runContinuityBlockCommandWithBootstrap(nil, &out, &errOut, bootstrap); code != 0 {
		t.Fatalf("continuity-block exit=%d stderr=%q", code, errOut.String())
	}
	if called || out.Len() != 0 || errOut.Len() != 0 {
		t.Fatalf("continuity-block empty-work output=%q stderr=%q called=%t", out.String(), errOut.String(), called)
	}
}

func TestContinuityBlockReadFailureIsSilentOnStdout(t *testing.T) {
	t.Setenv(selectedProductEnv, "product-1")
	t.Setenv(selectedWorkEnv, "work-1")
	t.Setenv(dbOverrideEnv, filepath.Join(t.TempDir(), "fixture.db"))
	bootstrap := func(context.Context, string, string, string) ([]byte, error) {
		return nil, errors.New("continuity read failed")
	}
	var out, errOut bytes.Buffer
	if code := runContinuityBlockCommandWithBootstrap(nil, &out, &errOut, bootstrap); code == 0 {
		t.Fatal("continuity-block succeeded after continuity read failure")
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "continuity read failed") {
		t.Fatalf("continuity-block failure output=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestSessionRefusesToStartWhenRequiredAgentIdentityIsAbsent(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	bootstrapCalls, runs := 0, 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	runner := func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		runs++
		return nil
	}
	identity := func(string) error { return verifyLaneAgentIdentity("", "", store.BuiltinLaneDefinitions()) }
	var out, errOut bytes.Buffer

	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, directoryAt(t.TempDir()), bootstrap, runner, identity, func(context.Context, string, string, string) (string, error) { return orchestratorAgentName, nil })
	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	if runs != 0 {
		t.Fatalf("opencode started %d time(s) despite absent agent identity", runs)
	}
	if bootstrapCalls != 0 {
		t.Fatalf("bootstrap ran %d time(s); identity must be asserted before packet derivation", bootstrapCalls)
	}
	if !strings.Contains(errOut.String(), "required agent identity is absent") {
		t.Fatalf("diagnostic did not name the absence: %q", errOut.String())
	}
	for _, lane := range store.BuiltinLaneDefinitions() {
		if !strings.Contains(errOut.String(), laneAgentFileName(lane.ID)) {
			t.Fatalf("diagnostic omitted lane %q: %q", lane.ID, errOut.String())
		}
	}
}

// TestSessionRefusesWhenOrchestratorIdentityIsAbsent covers CD-0061 D4 and
// the typed-absence contract CD-0049 D4 admits: a launcher-started session
// that cannot resolve concord-orchestrator.md exits with a diagnostic
// naming required identity, observed absence, and searched paths, and
// writes NO event — verification fails before any store interaction.
func TestSessionRefusesWhenOrchestratorIdentityIsAbsent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	// Lane definitions resolve, but the orchestrator definition does not.
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
	}
	t.Setenv("HOME", home)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	dbPath := filepath.Join(t.TempDir(), "concord-absent.db")
	t.Setenv("CONCORD_DB_PATH", dbPath)
	identity := func(dir string) error { return verifyLaneAgentIdentity(home, dir, store.BuiltinLaneDefinitions()) }
	// The orchestrator callback runs the real verification + recording
	// path against temp dirs. With no concord-orchestrator.md on disk, the
	// verification fails before the store is opened, so the recorded
	// database file must not exist.
	orchestrator := recordOrchestratorIdentityAt(home, registryProbeFor(orchestratorAgentName))
	bootstrapCalls, runs := 0, 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	runner := func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		runs++
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, directoryAt(cwd), bootstrap, runner, identity, orchestrator); code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%q", code, errOut.String())
	}
	if runs != 0 || bootstrapCalls != 0 {
		t.Fatalf("runs=%d bootstrapCalls=%d; identity must be asserted before any later step", runs, bootstrapCalls)
	}
	stderr := errOut.String()
	for _, fragment := range []string{
		"required agent identity is absent",
		orchestratorAgentFileName,
		filepath.Join(cwd, ".opencode", "agents"),
		filepath.Join(home, ".config", "opencode", "agents"),
	} {
		if !strings.Contains(stderr, fragment) {
			t.Fatalf("diagnostic missing %q: %q", fragment, stderr)
		}
	}
	// The store must not have been opened. The path the env var names was
	// never created, so an os.Stat must report os.ErrNotExist.
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database was created at %q even though orchestrator verification failed; the absent path must not touch the store", dbPath)
	}
}

// TestSessionRecordsExactlyOneOrchestratorIdentityEvent covers CD-0061 D4:
// a launcher-started session with a resolvable concord-orchestrator.md
// records exactly one subject_type='session' domain event carrying the
// asserted type, version, and ruleset digest.
func TestSessionRecordsExactlyOneOrchestratorIdentityEvent(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
	}
	writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), orchestratorAgentFileName)
	t.Setenv("HOME", home)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	dbPath := filepath.Join(t.TempDir(), "concord-assertion.db")
	t.Setenv("CONCORD_DB_PATH", dbPath)
	identity := func(dir string) error { return verifyLaneAgentIdentity(home, dir, store.BuiltinLaneDefinitions()) }
	orchestrator := recordOrchestratorIdentityAt(home, registryProbeFor(orchestratorAgentName))
	bootstrapCalls, runs := 0, 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	runner := func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
		runs++
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, directoryAt(cwd), bootstrap, runner, identity, orchestrator); code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	// bootstrap may run because workID is set; the durable-write check below
	// is the property CD-0061 D4 requires.
	_ = bootstrapCalls
	_ = runs
	// Open the temp store and assert exactly one session-orchestrator event
	// was recorded, carrying type, version, and digest.
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

// TestSessionStartsTheOrchestratorAgentItAsserted covers CD-0061 Invariant 3:
// a session that proceeds has recorded the orchestrator identity it asserted.
// Verifying a definition on disk and then starting the host without selecting
// it records evidence for an agent that never ran, because the host answers an
// unselected name with the operator's default agent and exits zero (CD-0049
// D2). The assertion's resolved definition path and the started agent name are
// therefore checked against each other, not against a literal.
func TestSessionStartsTheOrchestratorAgentItAsserted(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
	}
	writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), orchestratorAgentFileName)
	t.Setenv("HOME", home)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	dbPath := filepath.Join(t.TempDir(), "concord-selection.db")
	t.Setenv("CONCORD_DB_PATH", dbPath)
	var argv []string
	var runnerDir string
	runner := func(_ context.Context, dir string, got []string, _ []string, _ io.Reader, _, _ io.Writer) error {
		runnerDir = dir
		argv = append([]string(nil), got...)
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		directoryAt(cwd),
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		runner,
		func(dir string) error { return verifyLaneAgentIdentity(home, dir, store.BuiltinLaneDefinitions()) },
		recordOrchestratorIdentityAt(home, registryProbeFor(orchestratorAgentName)),
	); code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if runnerDir != cwd {
		t.Fatalf("host ran in %q, want the resolved session directory %q", runnerDir, cwd)
	}
	selected := selectedAgentName(t, argv)
	recorded, err := readRecordedAssertion(t, dbPath)
	if err != nil {
		t.Fatalf("read assertion: %v", err)
	}
	definition := ""
	for _, src := range recorded.Sources {
		if src.Kind == "orchestrator_definition" {
			definition = src.Path
		}
	}
	if definition == "" {
		t.Fatal("assertion recorded no orchestrator_definition source")
	}
	if want := strings.TrimSuffix(filepath.Base(definition), ".md"); selected != want {
		t.Fatalf("session started agent %q but asserted definition %q", selected, definition)
	}
}

// selectedAgentName returns the value the session passed to the host's --agent
// flag. It fails the test when the session started the host without selecting
// an agent, which is the substitution defect these tests exist to catch.
func selectedAgentName(t *testing.T, argv []string) string {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--agent" {
			return argv[i+1]
		}
	}
	t.Fatalf("session started the host without selecting an agent: argv=%q", argv)
	return ""
}

// hostPrompt returns the value the session passed to the host's --prompt flag.
// Reading it by flag rather than by index keeps these tests honest about what
// the session sends when the argv shape changes.
func hostPrompt(t *testing.T, argv []string) string {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--prompt" {
			return argv[i+1]
		}
	}
	t.Fatalf("session started the host without a prompt: argv=%q", argv)
	return ""
}

// directoryAt returns a session directory resolver that reports dir,
// standing in for what hostSessionDirectory derives in production.
func directoryAt(dir string) sessionDirectoryFunc {
	return func(context.Context, string) (string, error) { return dir, nil }
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
	writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), orchestratorAgentFileName)
	agentsPath := filepath.Join(cwd, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("# instructions v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	dbPath := filepath.Join(t.TempDir(), "concord-digest.db")
	t.Setenv("CONCORD_DB_PATH", dbPath)
	identity := func(dir string) error { return verifyLaneAgentIdentity(home, dir, store.BuiltinLaneDefinitions()) }
	orchestrator := recordOrchestratorIdentityAt(home, registryProbeFor(orchestratorAgentName))

	// First session — record an assertion against the current files.
	if code := runSessionCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true,
		directoryAt(cwd),
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error { return nil },
		identity,
		orchestrator,
	); code != 0 {
		t.Fatalf("first session exit=%d", code)
	}
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
	currentAssertion, _, err := verifyOrchestratorIdentity(home, cwd)
	if err != nil {
		t.Fatalf("verify after mutation: %v", err)
	}
	recomputedFromCurrent := currentAssertion.RulesetDigest
	if recomputedFromCurrent == first.RulesetDigest {
		t.Fatalf("digest unchanged after AGENTS.md mutation: %q", recomputedFromCurrent)
	}
	if code := runSessionCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true,
		directoryAt(cwd),
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error { return nil },
		identity,
		orchestrator,
	); code != 0 {
		t.Fatalf("second session exit=%d", code)
	}
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

// recordOrchestratorIdentityAt binds the real assertion path to a temporary
// installation, so a test verifies production behavior rather than a copy of
// it.
func recordOrchestratorIdentityAt(home string, probe hostRegistryProbeFunc) sessionOrchestratorFunc {
	return func(ctx context.Context, dir, productID, workID string) (string, error) {
		return recordOrchestratorIdentity(ctx, home, probe, dir, productID, workID)
	}
}

// TestSessionRefusesWhenTheHostDoesNotRegisterTheHandle covers issue #430: a
// definition that resolves on disk is not proof the host will start it. The
// registry is checked before the store is touched and before the host starts,
// so a session that cannot run as the agent it asserts records no evidence and
// starts nothing (CD-0049 D4).
func TestSessionRefusesWhenTheHostDoesNotRegisterTheHandle(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
	}
	writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), orchestratorAgentFileName)
	t.Setenv("HOME", home)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	dbPath := filepath.Join(t.TempDir(), "concord-unregistered.db")
	t.Setenv("CONCORD_DB_PATH", dbPath)
	runs := 0
	var out, errOut bytes.Buffer
	// The host resolves the definition file but registers no such agent —
	// the state a `disable: true` or a configuration-layer override leaves
	// behind, which no on-disk check can see.
	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		directoryAt(cwd),
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		func(context.Context, string, []string, []string, io.Reader, io.Writer, io.Writer) error {
			runs++
			return nil
		},
		func(dir string) error { return verifyLaneAgentIdentity(home, dir, store.BuiltinLaneDefinitions()) },
		recordOrchestratorIdentityAt(home, registryProbeFor("some-other-agent")),
	)
	if code == 0 {
		t.Fatal("session started as an agent the host does not register")
	}
	if runs != 0 {
		t.Fatalf("host started %d times on a refused session", runs)
	}
	for _, fragment := range []string{orchestratorAgentName, "not registered"} {
		if !strings.Contains(errOut.String(), fragment) {
			t.Fatalf("diagnostic %q omits %q", errOut.String(), fragment)
		}
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database created at %q despite a refused registration check", dbPath)
	}
}

// registryProbeFor builds a probe whose registry declares each supplied
// handle as an enabled primary agent. Tests that are not about registration
// state use it to say "the host registers what the definition derives", so a
// registration refusal never masquerades as the failure under test.
func registryProbeFor(handles ...string) hostRegistryProbeFunc {
	agents := make(map[string]hostAgentEntry, len(handles))
	for _, handle := range handles {
		agents[handle] = hostAgentEntry{Mode: "primary"}
	}
	document, err := json.Marshal(hostConfigDocument{Agent: agents})
	if err != nil {
		panic(err)
	}
	return func(context.Context, string) ([]byte, error) { return document, nil }
}

// recordedAssertion is the shape TestOrchestratorIdentityDigestRecomputesAndChangesWithArtifact
// reads back from the store; the event is evidence, so a test that wants
// to recompute the digest needs only the digest and the source list.
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

// _ ensures time is referenced even when a future refactor drops every
// direct time mention in this file; it keeps the import set honest.
var _ = time.RFC3339Nano

// A definition that renames itself via frontmatter `name:` registers under
// that name, not its file stem. The session must select the registered name:
// selecting the stem would start the operator's default agent while the
// assertion described the renamed definition (issue #428's probe).
func TestSessionSelectsTheFrontmatterNameARenamedDefinitionRegisters(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	for _, lane := range store.BuiltinLaneDefinitions() {
		writeAgentDefinition(t, filepath.Join(cwd, ".opencode", "agents"), laneAgentFileName(lane.ID))
	}
	writeAgentDefinitionBody(t, filepath.Join(cwd, ".opencode", "agents"), orchestratorAgentFileName,
		[]byte("---\nname: op-session-renamed\nmode: all\n---\norchestrator body\n"))
	t.Setenv("HOME", home)
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	t.Setenv("CONCORD_DB_PATH", filepath.Join(t.TempDir(), "concord-renamed.db"))
	var argv []string
	var runnerDir string
	runner := func(_ context.Context, dir string, got []string, _ []string, _ io.Reader, _, _ io.Writer) error {
		runnerDir = dir
		argv = append([]string(nil), got...)
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true,
		directoryAt(cwd),
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		runner,
		func(dir string) error { return verifyLaneAgentIdentity(home, dir, store.BuiltinLaneDefinitions()) },
		recordOrchestratorIdentityAt(home, registryProbeFor("op-session-renamed")),
	); code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if runnerDir != cwd {
		t.Fatalf("host ran in %q, want the resolved session directory %q", runnerDir, cwd)
	}
	if selected := selectedAgentName(t, argv); selected != "op-session-renamed" {
		t.Fatalf("session selected agent %q, want the frontmatter name %q", selected, "op-session-renamed")
	}
}

// The recorded agent reference is the handle the host runs the session as.
//
// The definition below carries `name: concord-1`, so the host registers it
// under that name and the file stem stops resolving. Recording the stem, or
// the file name, would attribute every session to an agent the host does not
// have — and under a fail-closed agent scope, to one no client can present.
func TestOrchestratorAssertionRecordsTheRegisteredHandle(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeAgentDefinitionBody(t, filepath.Join(cwd, ".opencode", "agents"), orchestratorAgentFileName,
		[]byte("---\nname: concord-1\nmode: primary\n---\n"))
	dbPath := filepath.Join(t.TempDir(), "concord-handle.db")
	t.Setenv("CONCORD_DB_PATH", dbPath)

	handle, err := recordOrchestratorIdentity(context.Background(), home, registryProbeFor("concord-1"), cwd, "product-1", "")
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
	stem := store.DeriveWorkflowActorRef("principal/orchestrator", "client/concord-session", "agent/"+orchestratorAgentFileName, "session/product-1")
	if actor == stem {
		t.Fatal("the assertion recorded the definition file name, not the handle the host runs")
	}
	if actor != want {
		t.Fatalf("recorded actor=%q, want the actor derived from agent/concord-1", actor)
	}
}
