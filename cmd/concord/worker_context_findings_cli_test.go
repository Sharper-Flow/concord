package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

// CON-887 typed terminal-report retention at the CLI ingress. The adapter
// sends the optional context_findings array of agent-lane-report.v1 on both
// terminal verbs; the CLI must carry it into the terminal payload at the
// registry's current version through the existing evidence authentication,
// keep nonce/idempotency/acknowledgment semantics, and never write a second
// post-terminal event.

// cliContextFindings is one fully-admissible finding on the CLI wire shape.
func cliContextFindings() []map[string]any {
	return []map[string]any{
		{
			"kind":                   "observation",
			"statement":              "the bounded read is the only admission route",
			"subject_ref":            "internal/store/worker_lanes.go",
			"evidence_refs":          []string{"internal/store/worker_lanes_test.go"},
			"domain_id":              cliContextRootDomain,
			"product_wide_rationale": "the admission route binds every Domain of the Product",
		},
		{
			"kind":                   "direction",
			"statement":              "the reader joins declarations without parsing narrative",
			"subject_ref":            "internal/store/fold.go",
			"evidence_refs":          []string{},
			"domain_id":              cliContextRootDomain,
			"product_wide_rationale": "the fold binds every Domain of the Product",
		},
	}
}

// cliContextRootDomain is the root Domain seedCLIWorkDomain installs. The
// work carries no architecture binding, so the root Domain with a
// product-wide rationale is the admissible finding Domain.
const cliContextRootDomain = "cli-root"

// seedCLIWorkDomain installs a current Domain registry for the primary
// Product of work-1, the work seedWorkerEvidenceAttempt dispatches, so the
// live terminal fold can validate finding Domains.
func seedCLIWorkDomain(t *testing.T, dbPath string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var productID string
	err = s.DatabaseForTesting().QueryRow(`SELECT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? AND wp.role='primary'`, "work-1").Scan(&productID)
	s.Close()
	if err != nil {
		t.Fatalf("resolve the work's primary Product: %v", err)
	}
	seedCLIDomain(t, dbPath, productID, cliContextRootDomain)
}

// runWorkerCLI runs one worker evidence verb's request JSON and returns the
// exit code with both output buffers.
func runWorkerCLI(t *testing.T, verb string, request string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runWithInput([]string{verb}, strings.NewReader(request), &out, &errOut)
	return code, out.String(), errOut.String()
}

// storedTerminalEvent reads one stored worker event's raw payload version and
// payload bytes, the durable identity a repeat admission must not alter.
func storedTerminalEvent(t *testing.T, dbPath, eventID string) (int, []byte) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	var payload []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version,payload FROM domain_events WHERE event_id=?`, eventID).Scan(&version, &payload); err != nil {
		t.Fatalf("read stored event %s: %v", eventID, err)
	}
	return version, payload
}

// TestWorkerCompleteCLIRetainsTypedContextFindings proves the complete verb
// admits the optional typed findings, records the completion at the current
// payload version with the findings bytes retained, and refuses an
// over-bound array whole.
func TestWorkerCompleteCLIRetainsTypedContextFindings(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, readback)
	seedCLIWorkDomain(t, dbPath)

	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbComplete, lane, readback, "nonce-complete-findings01")
	request["event_id"] = "complete-findings"
	request["context_findings"] = cliContextFindings()
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	code, _, stderr := runWorkerCLI(t, "worker-complete", mustJSON(t, request))
	if code != 0 {
		t.Fatalf("worker-complete with context_findings exit=%d stderr=%q", code, stderr)
	}
	version, payload := storedTerminalEvent(t, dbPath, "complete-findings")
	if want := store.WorkerEvidenceEventPayloadVersion(store.WorkerCompleted); version != want {
		t.Fatalf("stored completion payload_version = %d, want current registry version %d", version, want)
	}
	var stored struct {
		ContextFindings []map[string]any `json:"context_findings"`
	}
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.ContextFindings) != 2 || stored.ContextFindings[0]["statement"] != "the bounded read is the only admission route" {
		t.Fatalf("stored completion findings = %v, want both retained entries", stored.ContextFindings)
	}

	var state string
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, "attempt-1").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("lifecycle_state = %q, want completed", state)
	}
}

// TestWorkerCompleteCLIReturnsTheAggregateBoundRefusal proves an over-bound
// findings array is refused whole through the typed CLI result, with no
// terminal event recorded.
func TestWorkerCompleteCLIReturnsTheAggregateBoundRefusal(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, readback)
	seedCLIWorkDomain(t, dbPath)

	oversized := make([]map[string]any, 16)
	for i := range oversized {
		oversized[i] = map[string]any{
			"kind":                   "observation",
			"statement":              strings.Repeat("x", 1024),
			"subject_ref":            strings.Repeat("s", 128),
			"evidence_refs":          []string{},
			"domain_id":              cliContextRootDomain,
			"product_wide_rationale": "the bound applies to every Domain of the Product",
		}
	}
	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbComplete, lane, readback, "nonce-complete-overbound1")
	request["event_id"] = "complete-overbound"
	request["context_findings"] = oversized
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	code, out, stderr := runWorkerCLI(t, "worker-complete", mustJSON(t, request))
	if code == 0 || !strings.Contains(out+stderr, "16384-byte aggregate bound") {
		t.Fatalf("over-bound completion exit=%d out=%q stderr=%q, want the context_findings aggregate bound refusal", code, out, stderr)
	}
	assertNoTerminalWorkerEvent(t, dbPath, stderr)
}

// TestWorkerFailCLIRetainsTypedContextFindingsOnWorkerErrorOnly proves the
// fail verb retains findings on the worker-reported failure kind alone, at
// the current payload version, and refuses them on the diagnostic kinds.
func TestWorkerFailCLIRetainsTypedContextFindingsOnWorkerErrorOnly(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, readback)
	seedCLIWorkDomain(t, dbPath)

	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbFail, lane, readback, "nonce-fail-findings001")
	request["event_id"] = "fail-findings"
	request["context_findings"] = cliContextFindings()
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	code, _, stderr := runWorkerCLI(t, "worker-fail", mustJSON(t, request))
	if code != 0 {
		t.Fatalf("worker-fail worker_error with context_findings exit=%d stderr=%q", code, stderr)
	}
	version, payload := storedTerminalEvent(t, dbPath, "fail-findings")
	if want := store.WorkerEvidenceEventPayloadVersion(store.WorkerFailed); version != want {
		t.Fatalf("stored failure payload_version = %d, want %d", version, want)
	}
	var stored struct {
		ContextFindings []map[string]any `json:"context_findings"`
	}
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.ContextFindings) != 2 {
		t.Fatalf("stored failure findings = %v, want both retained entries", stored.ContextFindings)
	}

	// The diagnostic invalid_report kind refuses the same findings on a
	// fresh dispatched attempt, and no second terminal event lands.
	seedWorkerFindingsAttempt(t, key, lane, dbPath, readback, "attempt-2", "nonce-seed-dispatch000002")
	diagnostic, diagnosticAssertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbFail, lane, readback, "nonce-fail-diagnostic01")
	diagnostic["event_id"] = "fail-diagnostic-findings"
	diagnostic["attempt_id"] = "attempt-2"
	diagnosticAssertion.AttemptID = "attempt-2"
	diagnostic["failure_kind"] = string(store.WorkerFailureInvalidReport)
	diagnosticAssertion.FailureKind = string(store.WorkerFailureInvalidReport)
	diagnostic["context_findings"] = cliContextFindings()
	diagnostic["assertion"] = signWorkerEvidence(t, key, diagnosticAssertion)
	code, _, stderr = runWorkerCLI(t, "worker-fail", mustJSON(t, diagnostic))
	if code == 0 || !strings.Contains(stderr, "worker_error") {
		t.Fatalf("invalid_report with findings exit=%d stderr=%q, want the worker_error-only refusal", code, stderr)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=? AND json_extract(payload,'$.attempt_id')=?`, store.WorkerFailed, "attempt-2").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("refused diagnostic failure still appended %d event(s)", events)
	}
}

// seedWorkerFindingsAttempt opens a fresh authorized window and dispatches
// one named attempt, so a test can take a second attempt terminal after the
// seeded attempt-1 reached its own terminal outcome.
func seedWorkerFindingsAttempt(t *testing.T, key ed25519.PrivateKey, lane store.LaneDefinition, dbPath, readback, attemptID, nonce string) {
	t.Helper()
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", attemptID)
	assertion := agent.WorkerEvidenceAssertion{
		Verb: agent.WorkerEvidenceVerbDispatch, WorkID: "work-1", AttemptID: attemptID,
		LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		ReadbackModel: readback, Nonce: nonce,
	}
	provenanceDigest := "sha256:" + strings.Repeat("a", 64)
	packetDigest := "sha256:" + strings.Repeat("c", 64)
	assertion.HostProvenanceDigest = provenanceDigest
	assertion.PacketDigest = packetDigest
	request := map[string]any{
		"event_id": "dispatch-" + attemptID, "work_id": "work-1", "attempt_id": attemptID,
		"readback_model": readback, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest,
		"packet_schema_version": store.WorkerPacketSchemaVersion, "report_schema_version": store.WorkerReportSchemaVersion,
		"packet_digest": packetDigest,
		"host_provenance": map[string]any{
			"digest":  provenanceDigest,
			"sources": []map[string]any{{"kind": "agent_definition", "path": ".opencode/agents/" + lane.ID + ".md", "sha256": "sha256:" + strings.Repeat("b", 64)}},
		},
		"assertion": signWorkerEvidence(t, key, assertion),
	}
	if code, _, stderr := runWorkerCLI(t, "worker-dispatch", mustJSON(t, request)); code != 0 {
		t.Fatalf("seed worker-dispatch %s exit=%d stderr=%q", attemptID, code, stderr)
	}
}

// TestWorkerEvidenceRepeatAdmissionKeepsTheOriginalFindingsBytes proves the
// CD-0208 dedupe at the findings boundary: an exact repeat of a terminal
// request with findings — same event identity and payload, fresh nonce —
// acknowledges the stored event, appends nothing, and leaves the original
// payload version and bytes unchanged.
func TestWorkerEvidenceRepeatAdmissionKeepsTheOriginalFindingsBytes(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, readback)
	seedCLIWorkDomain(t, dbPath)

	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbComplete, lane, readback, "nonce-complete-repeat001")
	request["event_id"] = "complete-findings-repeat"
	request["context_findings"] = cliContextFindings()
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	if code, _, stderr := runWorkerCLI(t, "worker-complete", mustJSON(t, request)); code != 0 {
		t.Fatalf("first completion exit=%d stderr=%q", code, stderr)
	}
	versionBefore, payloadBefore := storedTerminalEvent(t, dbPath, "complete-findings-repeat")

	assertion.Nonce = "nonce-complete-repeat002"
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	if code, _, stderr := runWorkerCLI(t, "worker-complete", mustJSON(t, request)); code != 0 {
		t.Fatalf("repeat completion exit=%d stderr=%q, want an acknowledgment", code, stderr)
	}
	versionAfter, payloadAfter := storedTerminalEvent(t, dbPath, "complete-findings-repeat")
	if versionAfter != versionBefore || string(payloadAfter) != string(payloadBefore) {
		t.Fatalf("repeat admission changed the stored completion: version %d→%d payload changed=%v", versionBefore, versionAfter, string(payloadAfter) != string(payloadBefore))
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=? AND json_extract(payload,'$.attempt_id')=?`, store.WorkerCompleted, "attempt-1").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("repeat admission appended %d completion events, want 1", events)
	}
}
