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
	var argv []string
	bootstrapCalls := 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) {
		bootstrapCalls++
		return sessionboot.Build("product-1", store.ContinuitySnapshot{
			WorkID: "work-1", ProductIdentity: []string{"product-1"}, WorkflowStep: "planning",
			SpecMandate: []string{}, Boundaries: []store.ContextBoundary{}, Watermark: "seq:4",
			RestartUnavailableReason: "typed restart is deliberately excluded", PendingMessages: 2,
		})
	}
	runner := func(_ context.Context, got []string, _ []string, _ io.Reader, _, _ io.Writer) error {
		argv = append([]string(nil), got...)
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, func() error { return nil }, func(context.Context, string, string) error { return nil }); code != 0 {
		t.Fatalf("session exit=%d stderr=%q", code, errOut.String())
	}
	if bootstrapCalls != 1 {
		t.Fatalf("bootstrap calls=%d", bootstrapCalls)
	}
	if len(argv) != 3 || argv[0] != "opencode" || argv[1] != "--prompt" {
		t.Fatalf("argv=%q", argv)
	}
	start := strings.IndexByte(argv[2], '{')
	if start < 0 || !strings.Contains(argv[2][:start], "core-derived authority") {
		t.Fatalf("prompt omitted boot header: %q", argv[2])
	}
	if err := sessionboot.Validate([]byte(argv[2][start:])); err != nil {
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
	runner := func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error { runs++; return nil }
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, func() error { return nil }, func(context.Context, string, string) error { return nil }); code == 0 {
		t.Fatal("packet failure started session")
	}
	if runs != 0 || !strings.Contains(errOut.String(), "manifest digest mismatch") {
		t.Fatalf("runs=%d stderr=%q", runs, errOut.String())
	}
}

func TestProductOnlySessionRemainsIdentityOnly(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "")
	bootstrapCalls := 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	var prompt string
	runner := func(_ context.Context, argv []string, _ []string, _ io.Reader, _, _ io.Writer) error {
		prompt = argv[2]
		return nil
	}
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, func() error { return nil }, func(context.Context, string, string) error { return nil }); code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, errOut.String())
	}
	if bootstrapCalls != 0 || prompt != "Concord identity: product_id=product-1" {
		t.Fatalf("calls=%d prompt=%q", bootstrapCalls, prompt)
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

func TestSessionRefusesToStartWhenRequiredAgentIdentityIsAbsent(t *testing.T) {
	t.Setenv("CONCORD_SELECTED_PRODUCT_ID", "product-1")
	t.Setenv("CONCORD_SELECTED_WORK_ID", "work-1")
	bootstrapCalls, runs := 0, 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	runner := func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error { runs++; return nil }
	identity := func() error { return verifyLaneAgentIdentity("", "", store.BuiltinLaneDefinitions()) }
	var out, errOut bytes.Buffer

	code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, identity, func(context.Context, string, string) error { return nil })
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
	identity := func() error { return verifyLaneAgentIdentity(home, cwd, store.BuiltinLaneDefinitions()) }
	// The orchestrator callback runs the real verification + recording
	// path against temp dirs. With no concord-orchestrator.md on disk, the
	// verification fails before the store is opened, so the recorded
	// database file must not exist.
	orchestrator := recordOrchestratorIdentityAt(home, cwd)
	bootstrapCalls, runs := 0, 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	runner := func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error { runs++; return nil }
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, identity, orchestrator); code != 2 {
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
	identity := func() error { return verifyLaneAgentIdentity(home, cwd, store.BuiltinLaneDefinitions()) }
	orchestrator := recordOrchestratorIdentityAt(home, cwd)
	bootstrapCalls, runs := 0, 0
	bootstrap := func(context.Context, string, string, string) ([]byte, error) { bootstrapCalls++; return nil, nil }
	runner := func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error { runs++; return nil }
	var out, errOut bytes.Buffer
	if code := runSessionCommand(nil, strings.NewReader(""), &out, &errOut, true, bootstrap, runner, identity, orchestrator); code != 0 {
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
	identity := func() error { return verifyLaneAgentIdentity(home, cwd, store.BuiltinLaneDefinitions()) }
	orchestrator := recordOrchestratorIdentityAt(home, cwd)

	// First session — record an assertion against the current files.
	if code := runSessionCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true,
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error { return nil },
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
	currentAssertion, err := verifyOrchestratorIdentity(home, cwd)
	if err != nil {
		t.Fatalf("verify after mutation: %v", err)
	}
	recomputedFromCurrent := currentAssertion.RulesetDigest
	if recomputedFromCurrent == first.RulesetDigest {
		t.Fatalf("digest unchanged after AGENTS.md mutation: %q", recomputedFromCurrent)
	}
	if code := runSessionCommand(nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, true,
		func(context.Context, string, string, string) ([]byte, error) { return nil, nil },
		func(context.Context, []string, []string, io.Reader, io.Writer, io.Writer) error { return nil },
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

// recordOrchestratorIdentityAt mirrors hostOrchestratorIdentity but takes
// explicit home/cwd so tests can place agent definitions in temp dirs and
// verify the assertion end-to-end without touching the operator's real
// installation. Production wiring in cmd/concord/session.go uses
// hostOrchestratorIdentity, which reads os.Getenv/os.Getwd; the shape is
// otherwise identical.
func recordOrchestratorIdentityAt(home, cwd string) sessionOrchestratorFunc {
	return func(ctx context.Context, productID, workID string) error {
		assertion, err := verifyOrchestratorIdentity(home, cwd)
		if err != nil {
			return err
		}
		assertion.ProductID = productID
		assertion.WorkID = workID
		assertion.PrincipalRef = "principal/orchestrator"
		assertion.ClientRef = "client/concord-session"
		assertion.AgentRef = "agent/" + orchestratorAgentFileName
		assertion.SessionRef = "session/" + productID
		path, err := databasePath()
		if err != nil {
			return err
		}
		s, err := store.Open(ctx, path)
		if err != nil {
			return err
		}
		defer s.Close()
		eventID := orchestratorAssertionEventID(productID, workID)
		if _, err := s.RecordOrchestratorIdentityAssertion(ctx, eventID, time.Now().UTC(), assertion); err != nil {
			return err
		}
		return nil
	}
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

func convertSources(src []store.OrchestratorArtifactSource) []store.OrchestratorArtifactSource {
	out := make([]store.OrchestratorArtifactSource, len(src))
	copy(out, src)
	return out
}

func convertSourcesFromPaths(t *testing.T, home, cwd, definition string) []store.OrchestratorArtifactSource {
	t.Helper()
	assertion, err := verifyOrchestratorIdentity(home, cwd)
	if err != nil {
		t.Fatalf("recompute sources: %v", err)
	}
	// Override definition path: verifyOrchestratorIdentity only looks at
	// searched directories. When definition sits inside cwd, its path is
	// already in the resolved list; this is just a safety net.
	if len(assertion.Sources) > 0 {
		return assertion.Sources
	}
	_ = definition
	return nil
}

// _ ensures time is referenced even when a future refactor drops every
// direct time mention in this file; it keeps the import set honest.
var _ = time.RFC3339Nano
