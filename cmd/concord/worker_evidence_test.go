package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

const workerEvidenceClientRef = "worker-evidence-client"

// workerEvidenceVectorCase mirrors one case of the shared vector at
// adapter/opencode/worker-evidence-vector.json. Its bound_fields list is the
// contract this package proves against the CLI and the adapter signs against.
type workerEvidenceVectorCase struct {
	Verb        string   `json:"verb"`
	BoundFields []string `json:"bound_fields"`
}

func workerEvidenceVectorCases(t *testing.T) []workerEvidenceVectorCase {
	t.Helper()
	raw, err := os.ReadFile("../../adapter/opencode/worker-evidence-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Cases []workerEvidenceVectorCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.Cases) == 0 {
		t.Fatal("shared worker evidence vector declares no cases")
	}
	return vector.Cases
}

// seedWorkerEvidenceClient registers the trusted client whose signature
// authorizes worker evidence writes and returns its signing key.
func seedWorkerEvidenceClient(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	runCLIJSON(t, []string{"client", "register"}, map[string]any{
		"client_ref": workerEvidenceClientRef, "key_id": "worker-key-1", "principal_ref": "operator-1",
		"public_key": base64.StdEncoding.EncodeToString(publicKey), "capabilities": []string{"worker_evidence"},
		"product_scope": []string{"product-1"}, "project_scope": []string{"project-1"}, "agent_scope": []string{"agent-worker"},
	})
	return privateKey
}

func signWorkerEvidence(t *testing.T, key ed25519.PrivateKey, assertion agent.WorkerEvidenceAssertion) map[string]any {
	t.Helper()
	if assertion.ClientRef == "" {
		assertion.ClientRef = workerEvidenceClientRef
	}
	if assertion.IssuedAt == "" {
		assertion.IssuedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	assertion.Signature = ed25519.Sign(key, agent.CanonicalWorkerEvidenceAssertion(assertion))
	raw, err := json.Marshal(assertion)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func workerCompleteJSON(t *testing.T, key ed25519.PrivateKey, eventID, workID, attemptID, readback, nonce string, lane *store.LaneDefinition) string {
	t.Helper()
	resolved := store.BuiltinLaneDefinitions()[0]
	if lane != nil {
		resolved = *lane
	}
	assertion := agent.WorkerEvidenceAssertion{
		Verb: agent.WorkerEvidenceVerbComplete, WorkID: workID, AttemptID: attemptID,
		LaneID: resolved.ID, LaneVersion: resolved.Version, LaneDigest: resolved.Digest,
		ReadbackModel: readback, Nonce: nonce,
	}
	evidence := make([]map[string]any, 0, len(resolved.EvidenceObligations))
	for _, obligation := range resolved.EvidenceObligations {
		evidence = append(evidence, map[string]any{"obligation": obligation, "detail": "discharged " + obligation})
	}
	value := map[string]any{
		"event_id": eventID, "work_id": workID, "attempt_id": attemptID,
		"readback_model": readback, "report_schema_version": store.WorkerReportSchemaVersion,
		"evidence_origin": store.WorkerEvidenceReported, "evidence": evidence,
		"assertion": signWorkerEvidence(t, key, assertion),
	}
	return mustJSON(t, value)
}

func workerFailJSON(t *testing.T, key ed25519.PrivateKey, eventID, workID, attemptID string, lane store.LaneDefinition, readback, failureKind, detail, nonce string) string {
	t.Helper()
	assertion := agent.WorkerEvidenceAssertion{
		Verb: agent.WorkerEvidenceVerbFail, WorkID: workID, AttemptID: attemptID,
		LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		ReadbackModel: readback, FailureKind: failureKind, Nonce: nonce,
	}
	value := map[string]any{
		"event_id": eventID, "work_id": workID, "attempt_id": attemptID,
		"readback_model": readback, "failure_kind": failureKind, "detail": detail,
		"assertion": signWorkerEvidence(t, key, assertion),
	}
	return mustJSON(t, value)
}

// workerEvidenceRequest builds one verb's CLI request together with the
// assertion identity that request establishes, so a caller can perturb a single
// bound field before signing.
func workerEvidenceRequest(t *testing.T, verb string, lane store.LaneDefinition, workID, attemptID, readback, nonce string) (map[string]any, agent.WorkerEvidenceAssertion) {
	t.Helper()
	assertion := agent.WorkerEvidenceAssertion{
		Verb: verb, WorkID: workID, AttemptID: attemptID,
		LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		ReadbackModel: readback, Nonce: nonce,
	}
	request := map[string]any{
		"event_id": "event-" + nonce, "work_id": workID, "attempt_id": attemptID,
		"readback_model": readback,
	}
	switch verb {
	case agent.WorkerEvidenceVerbDispatch:
		provenanceDigest := "sha256:" + strings.Repeat("a", 64)
		// CD-0067 D6: dispatch is the only verb that signs a packet
		// digest; the seed keeps it in lockstep with the digest the
		// seeded dispatch window records so the gate accepts it.
		packetDigest := "sha256:" + strings.Repeat("c", 64)
		assertion.HostProvenanceDigest = provenanceDigest
		assertion.PacketDigest = packetDigest
		request["lane_id"] = lane.ID
		request["lane_version"] = lane.Version
		request["lane_digest"] = lane.Digest
		request["packet_schema_version"] = store.WorkerPacketSchemaVersion
		request["report_schema_version"] = store.WorkerReportSchemaVersion
		request["packet_digest"] = packetDigest
		request["host_provenance"] = map[string]any{
			"digest":  provenanceDigest,
			"sources": []map[string]any{{"kind": "agent_definition", "path": ".opencode/agents/" + lane.ID + ".md", "sha256": "sha256:" + strings.Repeat("b", 64)}},
		}
	case agent.WorkerEvidenceVerbComplete:
		evidence := make([]map[string]any, 0, len(lane.EvidenceObligations))
		for _, obligation := range lane.EvidenceObligations {
			evidence = append(evidence, map[string]any{"obligation": obligation, "detail": "discharged " + obligation})
		}
		request["report_schema_version"] = store.WorkerReportSchemaVersion
		request["evidence_origin"] = store.WorkerEvidenceReported
		request["evidence"] = evidence
	case agent.WorkerEvidenceVerbFail:
		assertion.FailureKind = string(store.WorkerFailureWorkerError)
		request["failure_kind"] = string(store.WorkerFailureWorkerError)
		request["detail"] = "the worker reported its own failure"
	default:
		t.Fatalf("unsupported worker evidence verb %q", verb)
	}
	return request, assertion
}

// restrictWorkerEvidenceAssertion keeps only the declared fields, so the
// assertion under test claims exactly what the shared vector says the binding
// carries and nothing else.
func restrictWorkerEvidenceAssertion(t *testing.T, assertion agent.WorkerEvidenceAssertion, boundFields []string) agent.WorkerEvidenceAssertion {
	t.Helper()
	declared := map[string]bool{}
	for _, field := range boundFields {
		declared[field] = true
	}
	restricted := agent.WorkerEvidenceAssertion{ClientRef: assertion.ClientRef, IssuedAt: assertion.IssuedAt, Nonce: assertion.Nonce}
	for field := range declared {
		switch field {
		case "verb":
			restricted.Verb = assertion.Verb
		case "work_id":
			restricted.WorkID = assertion.WorkID
		case "attempt_id":
			restricted.AttemptID = assertion.AttemptID
		case "lane_id":
			restricted.LaneID = assertion.LaneID
		case "lane_version":
			restricted.LaneVersion = assertion.LaneVersion
		case "lane_digest":
			restricted.LaneDigest = assertion.LaneDigest
		case "readback_model":
			restricted.ReadbackModel = assertion.ReadbackModel
		case "failure_kind":
			restricted.FailureKind = assertion.FailureKind
		case "host_provenance_digest":
			restricted.HostProvenanceDigest = assertion.HostProvenanceDigest
		case "packet_digest":
			restricted.PacketDigest = assertion.PacketDigest
		default:
			t.Fatalf("no assertion field for bound field %q", field)
		}
	}
	// The verb is what the CLI routes on; an assertion that could not name it
	// would fail validation for a reason unrelated to the bound field set.
	if !declared["verb"] {
		t.Fatalf("%s declares no bound verb", assertion.Verb)
	}
	return restricted
}

// mutateWorkerEvidenceField restates one bound field as a different, still
// well-formed value. The CLI must refuse the result, which is what proves the
// field is bound rather than merely present.
func mutateWorkerEvidenceField(t *testing.T, assertion agent.WorkerEvidenceAssertion, field string) agent.WorkerEvidenceAssertion {
	t.Helper()
	switch field {
	case "verb":
		if assertion.Verb == agent.WorkerEvidenceVerbDispatch {
			assertion.Verb = agent.WorkerEvidenceVerbComplete
		} else {
			assertion.Verb = agent.WorkerEvidenceVerbDispatch
		}
	case "work_id":
		assertion.WorkID = "work-somewhere-else"
	case "attempt_id":
		assertion.AttemptID = "attempt-somewhere-else"
	case "lane_id":
		assertion.LaneID = "lane-somewhere-else"
	case "lane_version":
		assertion.LaneVersion++
	case "lane_digest":
		assertion.LaneDigest = "sha256:" + strings.Repeat("d", 64)
	case "readback_model":
		assertion.ReadbackModel = "vendor/some-other-model"
	case "failure_kind":
		assertion.FailureKind = string(store.WorkerFailureFallbackBlocked)
	case "host_provenance_digest":
		assertion.HostProvenanceDigest = "sha256:" + strings.Repeat("e", 64)
	case "packet_digest":
		assertion.PacketDigest = "sha256:" + strings.Repeat("f", 64)
	default:
		t.Fatalf("no mutation for bound field %q", field)
	}
	return assertion
}

// seedWorkerEvidenceAttempt records the dispatch a terminal verb binds to.
// seedWorkerEvidenceAttempt records a dispatch attempt through the CLI. A
// worker attempt only exists behind an authorized dispatch window, so the
// fixture opens that window before claiming the attempt.
func seedWorkerEvidenceAttempt(t *testing.T, key ed25519.PrivateKey, lane store.LaneDefinition, dbPath, workID, attemptID, readback string) {
	t.Helper()
	seedAuthorizedDispatchWindow(t, dbPath, workID, attemptID)
	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbDispatch, lane, workID, attemptID, readback, "nonce-seed-dispatch000001")
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(mustJSON(t, request)), &out, &errOut); code != 0 {
		t.Fatalf("seed worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}
}

// TestWorkerEvidenceBindsExactlyTheDeclaredFieldSet proves, per verb, that the
// CLI binding populates exactly the field set the shared vector declares. An
// assertion claiming that set is accepted, so the CLI binds nothing beyond it;
// restating any one of those fields is refused, so it binds every one of them.
// The adapter signs the same declared set (adapter/opencode/worker_evidence.test.ts),
// which is what keeps the two sides from drifting apart per verb.
func TestWorkerEvidenceBindsExactlyTheDeclaredFieldSet(t *testing.T) {
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	for _, vectorCase := range workerEvidenceVectorCases(t) {
		verb := vectorCase.Verb
		run := func(t *testing.T, mutate func(agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion) (int, string, string) {
			t.Helper()
			dbPath := freshMigratedCLIDatabase(t)
			key := seedWorkerEvidenceClient(t)
			if verb == agent.WorkerEvidenceVerbDispatch {
				seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
			} else {
				seedWorkerEvidenceAttempt(t, key, lane, dbPath, "work-1", "attempt-1", readback)
			}
			request, assertion := workerEvidenceRequest(t, verb, lane, "work-1", "attempt-1", readback, "nonce-bound-fieldset00001")
			request["assertion"] = signWorkerEvidence(t, key, mutate(restrictWorkerEvidenceAssertion(t, assertion, vectorCase.BoundFields)))
			var out, errOut bytes.Buffer
			code := runWithInput([]string{verb}, strings.NewReader(mustJSON(t, request)), &out, &errOut)
			return code, errOut.String(), dbPath
		}
		t.Run(verb+"/accepts the declared field set", func(t *testing.T) {
			code, stderr, _ := run(t, func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion { return a })
			if code != 0 {
				t.Fatalf("%s with the declared field set exit=%d stderr=%q, want acceptance", verb, code, stderr)
			}
		})
		for _, field := range vectorCase.BoundFields {
			t.Run(verb+"/refuses a restated "+field, func(t *testing.T) {
				code, stderr, dbPath := run(t, func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion {
					return mutateWorkerEvidenceField(t, a, field)
				})
				if code == 0 {
					t.Fatalf("%s accepted an assertion restating %s", verb, field)
				}
				assertNoTerminalWorkerEvent(t, dbPath, stderr)
			})
		}
	}
}

// TestWorkerFailRefusesAnAssertionWithoutLaneIdentity is the issue #343
// regression at the CLI. A failure assertion that omits lane identity does not
// claim what the binding carries, and the CLI must refuse it before mutation
// while the complete assertion still lands its event.
func TestWorkerFailRefusesAnAssertionWithoutLaneIdentity(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	seedWorkerEvidenceAttempt(t, key, lane, dbPath, "work-1", "attempt-1", readback)

	request, assertion := workerEvidenceRequest(t, agent.WorkerEvidenceVerbFail, lane, "work-1", "attempt-1", readback, "nonce-fail-nolaneidentity")
	unbound := assertion
	unbound.LaneID = ""
	unbound.LaneVersion = 0
	unbound.LaneDigest = ""
	request["assertion"] = signWorkerEvidence(t, key, unbound)
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-fail"}, strings.NewReader(mustJSON(t, request)), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "does not bind this attempt identity") {
		t.Fatalf("worker-fail without lane identity exit=%d stderr=%q, want an attempt-identity refusal", code, errOut.String())
	}
	assertNoTerminalWorkerEvent(t, dbPath, errOut.String())

	request["assertion"] = signWorkerEvidence(t, key, assertion)
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"worker-fail"}, strings.NewReader(mustJSON(t, request)), &out, &errOut); code != 0 {
		t.Fatalf("worker-fail with lane identity exit=%d stderr=%q, want the event to land", code, errOut.String())
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, "attempt-1").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Fatalf("lifecycle_state = %q, want failed", state)
	}
}

// assertNoTerminalWorkerEvent proves a refusal happened before mutation: the
// seeded dispatch may exist, but no completion or failure was appended.
func assertNoTerminalWorkerEvent(t *testing.T, dbPath, stderr string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind IN (?,?)`, store.WorkerCompleted, store.WorkerFailed).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("refused worker evidence still appended %d terminal event(s); stderr=%q", events, stderr)
	}
}

// TestWorkerEvidenceRefusesUnauthenticatedAndForgedCallers is the core issue
// #185 boundary: every worker-evidence verb must fail before mutation when the
// caller cannot prove it is the registered client bound to this exact attempt.
func TestWorkerEvidenceRefusesUnauthenticatedAndForgedCallers(t *testing.T) {
	lane := store.BuiltinLaneDefinitions()[0]
	tests := []struct {
		name    string
		command string
		build   func(t *testing.T, key ed25519.PrivateKey) string
		want    string
	}{
		{
			name: "dispatch without an assertion", command: "worker-dispatch",
			build: func(t *testing.T, key ed25519.PrivateKey) string {
				value := map[string]any{}
				if err := json.Unmarshal([]byte(workerDispatchJSON(t, key, "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-unsigned01")), &value); err != nil {
					t.Fatal(err)
				}
				delete(value, "assertion")
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			},
			want: "malformed",
		},
		{
			name: "dispatch signed by an unregistered key", command: "worker-dispatch",
			build: func(t *testing.T, _ ed25519.PrivateKey) string {
				_, forged, err := ed25519.GenerateKey(nil)
				if err != nil {
					t.Fatal(err)
				}
				return workerDispatchJSON(t, forged, "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-forgedkey1")
			},
			want: "signature invalid",
		},
		{
			name: "dispatch naming an unregistered client", command: "worker-dispatch",
			build: func(t *testing.T, key ed25519.PrivateKey) string {
				return workerDispatchJSONWith(t, key, "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), "nonce-dispatch-unknownref", func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion {
					a.ClientRef = "never-registered"
					return a
				})
			},
			want: "trusted client unavailable",
		},
		{
			name: "dispatch whose assertion claims a different attempt", command: "worker-dispatch",
			build: func(t *testing.T, key ed25519.PrivateKey) string {
				return workerDispatchJSONWith(t, key, "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), "nonce-dispatch-wrongattem", func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion {
					a.AttemptID = "attempt-other"
					return a
				})
			},
			want: "does not bind this attempt identity",
		},
		{
			name: "dispatch whose assertion claims a different work item", command: "worker-dispatch",
			build: func(t *testing.T, key ed25519.PrivateKey) string {
				return workerDispatchJSONWith(t, key, "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), "nonce-dispatch-crosswork1", func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion {
					a.WorkID = "work-somewhere-else"
					return a
				})
			},
			want: "does not bind this attempt identity",
		},
		{
			name: "dispatch whose assertion was signed for another verb", command: "worker-dispatch",
			build: func(t *testing.T, key ed25519.PrivateKey) string {
				return workerDispatchJSONWith(t, key, "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), "nonce-dispatch-verbswap01", func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion {
					a.Verb = agent.WorkerEvidenceVerbComplete
					return a
				})
			},
			want: "does not bind this attempt identity",
		},
		{
			name: "dispatch carrying a stale assertion", command: "worker-dispatch",
			build: func(t *testing.T, key ed25519.PrivateKey) string {
				return workerDispatchJSONWith(t, key, "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), "nonce-dispatch-stalestamp", func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion {
					a.IssuedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
					return a
				})
			},
			want: "timestamp invalid",
		},
		{
			name: "dispatch whose assertion understates the provenance digest", command: "worker-dispatch",
			build: func(t *testing.T, key ed25519.PrivateKey) string {
				return workerDispatchJSONWith(t, key, "event-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), "nonce-dispatch-provdigest", func(a agent.WorkerEvidenceAssertion) agent.WorkerEvidenceAssertion {
					a.HostProvenanceDigest = "sha256:" + strings.Repeat("c", 64)
					return a
				})
			},
			want: "does not bind this attempt identity",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			dbPath := freshMigratedCLIDatabase(t)
			seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
			key := seedWorkerEvidenceClient(t)
			var out, errOut bytes.Buffer
			code := runWithInput([]string{testCase.command}, strings.NewReader(testCase.build(t, key)), &out, &errOut)
			if code == 0 || !strings.Contains(errOut.String(), testCase.want) {
				t.Fatalf("exit=%d stderr=%q, want refusal containing %q", code, errOut.String(), testCase.want)
			}
			assertNoWorkerEvents(t, dbPath)
		})
	}
}

// TestWorkerEvidenceAssertionCannotBeReplayed proves the nonce is consumed in
// the same transaction as the evidence: a byte-identical nonce in a fresh
// window refuses on replay, not on the window gate.
func TestWorkerEvidenceAssertionCannotBeReplayed(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	dispatch1 := workerDispatchJSON(t, key, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-replay0001")

	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(dispatch1), &out, &errOut); code != 0 {
		t.Fatalf("first worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}
	// A second window for a fresh attempt reopens the window gate. The
	// nonce from the first dispatch is what the replay defense refuses;
	// without a fresh window, the dispatch_window gate (CD-0059 D5) would
	// refuse on consumed-window and the nonce check would never run.
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-2")
	dispatch2 := workerDispatchJSON(t, key, "dispatch-2", "work-1", "attempt-2", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-replay0001")
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(dispatch2), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "replayed") {
		t.Fatalf("replayed worker-dispatch exit=%d stderr=%q, want replay refusal", code, errOut.String())
	}
}

// TestWorkerCompleteCLIRefusesAnOmittedEvidenceOriginAndUndischargedObligations
// covers the CD-0056 operator boundary: the CLI names the missing origin
// itself, and the store refuses a report that leaves a declared obligation
// undischarged.
func TestWorkerCompleteCLIRefusesAnOmittedEvidenceOriginAndUndischargedObligations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	seedAuthorizedDispatchWindow(t, dbPath, "work-origin", "attempt-origin")
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(workerDispatchJSON(t, key, "dispatch-origin", "work-origin", "attempt-origin", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-origin001")), &out, &errOut); code != 0 {
		t.Fatalf("worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}

	value := map[string]any{}
	if err := json.Unmarshal([]byte(workerCompleteJSON(t, key, "complete-origin", "work-origin", "attempt-origin", preferredLaneModel(lane), "nonce-complete-origin001", &lane)), &value); err != nil {
		t.Fatal(err)
	}
	delete(value, "evidence_origin")
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"worker-complete"}, bytes.NewReader([]byte(mustJSON(t, value))), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "evidence_origin") {
		t.Fatalf("omitted evidence_origin exit=%d stderr=%q, want an evidence_origin diagnostic", code, errOut.String())
	}

	value["evidence_origin"] = store.WorkerEvidenceReported
	value["evidence"] = []map[string]any{{"obligation": lane.EvidenceObligations[0], "detail": "only one obligation discharged"}}
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"worker-complete"}, bytes.NewReader([]byte(mustJSON(t, value))), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), lane.EvidenceObligations[1]) {
		t.Fatalf("undischarged completion exit=%d stderr=%q, want the missing obligation named", code, errOut.String())
	}

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, "attempt-origin").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "dispatched" {
		t.Fatalf("lifecycle_state = %q, want dispatched", state)
	}
}

// TestWorkerEvidenceCannotChangeATerminalResult covers the acceptance clause a
// signature alone does not deliver: once an attempt has an outcome, further
// authenticated evidence is refused.
func TestWorkerEvidenceCannotChangeATerminalResult(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]

	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(workerDispatchJSON(t, key, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-terminal01")), &out, &errOut); code != 0 {
		t.Fatalf("worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}
	complete := workerCompleteJSON(t, key, "complete-1", "work-1", "attempt-1", preferredLaneModel(lane), "nonce-complete-terminal01", &lane)
	if code := runWithInput([]string{"worker-complete"}, strings.NewReader(complete), &out, &errOut); code != 0 {
		t.Fatalf("worker-complete exit=%d stderr=%q", code, errOut.String())
	}

	overwrite := fmt.Sprintf(`{"event_id":"fail-after-complete","work_id":"work-1","attempt_id":"attempt-1","readback_model":%q,"failure_kind":%q,"detail":"late failure","assertion":%s}`,
		preferredLaneModel(lane), store.WorkerFailureFallbackBlocked, mustJSON(t, signWorkerEvidence(t, key, agent.WorkerEvidenceAssertion{
			Verb: agent.WorkerEvidenceVerbFail, WorkID: "work-1", AttemptID: "attempt-1",
			LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
			ReadbackModel: preferredLaneModel(lane), FailureKind: string(store.WorkerFailureFallbackBlocked),
			Nonce: "nonce-fail-afterterminal",
		})))
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"worker-fail"}, strings.NewReader(overwrite), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "terminal outcome") {
		t.Fatalf("post-terminal worker-fail exit=%d stderr=%q, want terminal refusal", code, errOut.String())
	}

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, "attempt-1").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("lifecycle_state = %q after refused overwrite, want completed", state)
	}
}

// TestWorkerEvidenceRequiresTheWorkerEvidenceCapability proves the authority is
// policy-bound: a registered, active, correctly-signing client without the
// capability is still refused.
func TestWorkerEvidenceRequiresTheWorkerEvidenceCapability(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	runCLIJSON(t, []string{"client", "register"}, map[string]any{
		"client_ref": workerEvidenceClientRef, "key_id": "worker-key-1", "principal_ref": "operator-1",
		"public_key": base64.StdEncoding.EncodeToString(publicKey), "capabilities": []string{"product_read"},
		"product_scope": []string{"product-1"}, "project_scope": []string{"project-1"}, "agent_scope": []string{"agent-worker"},
	})
	lane := store.BuiltinLaneDefinitions()[0]
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(workerDispatchJSON(t, privateKey, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-nocapabil1")), &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "worker_evidence capability") {
		t.Fatalf("exit=%d stderr=%q, want capability refusal", code, errOut.String())
	}
	assertNoWorkerEvents(t, dbPath)
}

// TestWorkerEvidenceRefusesARevokedClient proves revocation reaches this path:
// evidence writing stops when the operator revokes the client.
func TestWorkerEvidenceRefusesARevokedClient(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	key := seedWorkerEvidenceClient(t)
	runCLIJSON(t, []string{"client", "revoke"}, map[string]any{"client_ref": workerEvidenceClientRef})
	lane := store.BuiltinLaneDefinitions()[0]
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(workerDispatchJSON(t, key, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-revoked001")), &out, &errOut)
	if code == 0 {
		t.Fatalf("revoked client recorded worker evidence; stderr=%q", errOut.String())
	}
	assertNoWorkerEvents(t, dbPath)
}

// TestWorkerDispatchRefusesMissingPacketDigest pins the CLI seam of CD-0067
// D6: a dispatch request that omits packet_digest is refused before the
// gate or the signature ever runs, so a worker that did not see the
// core's recorded digest cannot smuggle an unsigned envelope through the
// boundary. The test exercises three refusals — absent key, empty string,
// and a non-sha256 value — so the regex, not a coincidental schema match,
// is what catches the omission.
func TestWorkerDispatchRefusesMissingPacketDigest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	raw := workerDispatchJSON(t, key, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-missingdigest")
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		wantSub string
	}{
		{name: "absent key", mutate: func(value map[string]any) { delete(value, "packet_digest") }, wantSub: "packet_digest"},
		{name: "empty string", mutate: func(value map[string]any) { value["packet_digest"] = "" }, wantSub: "packet_digest"},
		{name: "not a sha256", mutate: func(value map[string]any) { value["packet_digest"] = "not-a-digest" }, wantSub: "packet_digest"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			value := map[string]any{}
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(value)
			mutated, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var out, errOut bytes.Buffer
			code := runWithInput([]string{"worker-dispatch"}, bytes.NewReader(mutated), &out, &errOut)
			if code == 0 {
				t.Fatalf("dispatch accepted the %s request; stderr=%q", testCase.name, errOut.String())
			}
			if !strings.Contains(errOut.String(), testCase.wantSub) {
				t.Fatalf("dispatch %s stderr=%q, want substring %q", testCase.name, errOut.String(), testCase.wantSub)
			}
			assertNoWorkerEvents(t, dbPath)
		})
	}
}

// TestWorkerDispatchRefusesMismatchedPacketDigest pins the store-side half
// of CD-0067 D6: the CLI accepts the request shape (packet_digest is
// present and well-formed), but the gate refuses because the recorded
// digest is different. The error names the packet digest so an operator
// can tell it apart from a missing-window refusal.
func TestWorkerDispatchRefusesMismatchedPacketDigest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	raw := workerDispatchJSON(t, key, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-mismatch001")
	value := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	value["packet_digest"] = "sha256:" + strings.Repeat("0", 64)
	if assertionValue, ok := value["assertion"].(map[string]any); ok {
		assertion := agent.WorkerEvidenceAssertion{
			ClientRef: workerEvidenceClientRef, Verb: agent.WorkerEvidenceVerbDispatch,
			WorkID: "work-1", AttemptID: "attempt-1", LaneID: lane.ID,
			LaneVersion: lane.Version, LaneDigest: lane.Digest,
			ReadbackModel:        preferredLaneModel(lane),
			HostProvenanceDigest: "sha256:" + strings.Repeat("a", 64),
			PacketDigest:         "sha256:" + strings.Repeat("0", 64),
			IssuedAt:             time.Now().UTC().Format(time.RFC3339Nano),
			Nonce:                "nonce-dispatch-mismatch001",
		}
		assertion.Signature = ed25519.Sign(key, agent.CanonicalWorkerEvidenceAssertion(assertion))
		assertionValue["packet_digest"] = "sha256:" + strings.Repeat("0", 64)
		value["assertion"] = assertionValue
	}
	mutated, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWithInput([]string{"worker-dispatch"}, bytes.NewReader(mutated), &out, &errOut)
	if code == 0 {
		t.Fatalf("mismatched-digest dispatch accepted; stderr=%q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "packet digest") {
		t.Fatalf("dispatch stderr=%q, want substring 'packet digest'", errOut.String())
	}
	assertNoWorkerEvents(t, dbPath)
}

// TestWorkerEvidenceRecordsTheVerifiedClientAsActor proves the literal
// "worker:cli" actor string is gone: the recorded actor is a proven identity.
func TestWorkerEvidenceRecordsTheVerifiedClientAsActor(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	seedAuthorizedDispatchWindow(t, dbPath, "work-1", "attempt-1")
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(workerDispatchJSON(t, key, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-actorcheck")), &out, &errOut); code != 0 {
		t.Fatalf("worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var actor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT actor FROM domain_events WHERE kind=?`, store.WorkerDispatched).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	if actor == "worker:cli" || !strings.Contains(actor, workerEvidenceClientRef) {
		t.Fatalf("actor = %q, want the verified client identity", actor)
	}
	// Issue #800 / CD-0017 D4: the dispatch records the lane as a workflow
	// actor and pins it as the instance's executing actor, so the owner's
	// later accept_worker_result is distinct from the party that executed.
	var laneActorRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT actor_ref FROM workflow_actors WHERE agent_ref=?`, "agent/lane:"+lane.ID).Scan(&laneActorRef); err != nil {
		t.Fatalf("lane actor row missing: %v", err)
	}
	var executing string
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?`, "work-1").Scan(&executing); err != nil {
		t.Fatal(err)
	}
	if executing != laneActorRef {
		t.Fatalf("execution_actor_ref = %q, want the dispatched lane actor %q", executing, laneActorRef)
	}
}

func assertNoWorkerEvents(t *testing.T, dbPath string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind IN (?,?,?)`, store.WorkerDispatched, store.WorkerCompleted, store.WorkerFailed).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("refused worker evidence still appended %d event(s)", events)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// seedAuthorizedDispatchWindow sets up the on-disk state the
// worker-dispatch CLI requires to admit a dispatch: a work item, its
// workflow instance pinned to the execution step, and an authorized,
// unconsumed dispatch_worker window bound to attemptID.
//
// CD-0059 D5 makes the dispatch-window integrity check structural, so the
// pre-existing capability/signature/nonce tests in this file cannot exercise
// the layers under the window unless the window exists. The helper folds
// the rows directly through fold_guard so the helper is a one-call fixture
// and the tests keep testing what they are named for.
func seedAuthorizedDispatchWindow(t *testing.T, dbPath, workID, attemptID string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	actor := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: store.ActorAgent}
	actorRef, err := store.WorkflowActorRef(actor)
	if err != nil {
		t.Fatalf("compute actor_ref: %v", err)
	}
	entry, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("workflow.implementation v4 is not registered")
	}
	expected := int64(2)
	resulting := expected + 1
	startPayload := mustJSON(t, map[string]any{
		"work_id":                workID,
		"expected_version":       expected,
		"resulting_version":      expected + 1,
		"step_id":                "execution",
		"action_id":              "dispatch_worker",
		"attempt_epoch":          1,
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64),
		"idempotency_identity":   "dispatch-open:" + attemptID,
		"actor_ref":              actorRef,
	})
	completionPayload := mustJSON(t, map[string]any{
		"work_id":              workID,
		"expected_version":     resulting,
		"resulting_version":    resulting + 1,
		"step_id":              "execution",
		"action_id":            "dispatch_worker",
		"attempt_epoch":        1,
		"result_evidence_refs": []string{},
		"changed_refs":         []string{workID},
		"actor_ref":            actorRef,
		"worker_attempt_id":    attemptID,
		// CD-0067 D6: the seeded dispatch_window must carry the packet
		// digest the worker-evidence assertion claims, so the gate
		// accepts the dispatched record rather than refusing it for
		// the empty-digest cutover case.
		"worker_packet_digest": "sha256:" + strings.Repeat("c", 64),
	})
	// work_items, workflow_actors, workflow_instances, products, projects,
	// product_projects, work_projects, and the workflow event families are
	// fold-only, so the rows are admitted through one fold_guard session.
	// The modernc.org/sqlite driver inconsistently binds parameters across
	// a multi-statement exec, so each insert is run as its own prepared
	// statement inside a single transaction.
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product-1', 'Concord', 'prototype', 'operator_only', 1, '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')`); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES('project-1', 'Repository', 1, '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')`); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES('product-1', 'project-1', 'primary')`); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?, 'task', ?, 'in_progress', 1, 2, '2026-08-22T00:00:00Z', '2026-08-22T00:00:00Z')`, workID, "cd0059 "+workID); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_projects(work_id,project_id,role) VALUES(?, 'project-1', 'primary')`, workID); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?, ?, ?, ?, ?, 'agent', '2026-08-22T00:00:00Z')`, actorRef, actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES(?, 'workflow.implementation', 4, ?, 'execution', 'running', ?, '2026-08-22T00:00:00Z')`, workID, entry.Digest, actorRef); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_started', 'work_item', ?, ?, '2026-08-22T00:00:00Z', 1, ?)`, "dispatch-start-"+attemptID, workID, actorRef, []byte(startPayload)); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_completed', 'work_item', ?, ?, '2026-08-22T00:00:00Z', 2, ?)`, "dispatch-completed-"+attemptID, workID, actorRef, []byte(completionPayload)); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatalf("seed dispatch window: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("seed dispatch window commit: %v", err)
	}
}
