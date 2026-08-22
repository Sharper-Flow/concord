package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

const workerEvidenceClientRef = "worker-evidence-client"

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
		"product_scope": []string{"product-1"}, "project_scope": []string{"project-1"},
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
			dbPath := filepath.Join(t.TempDir(), "concord.db")
			t.Setenv(dbOverrideEnv, dbPath)
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
// the same transaction as the evidence: a byte-identical second call fails.
func TestWorkerEvidenceAssertionCannotBeReplayed(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	dispatch := workerDispatchJSON(t, key, "dispatch-1", "work-1", "attempt-1", lane, preferredLaneModel(lane), store.WorkerPacketSchemaVersion, "nonce-dispatch-replay0001")

	var out, errOut bytes.Buffer
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(dispatch), &out, &errOut); code != 0 {
		t.Fatalf("first worker-dispatch exit=%d stderr=%q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"worker-dispatch"}, strings.NewReader(dispatch), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "replayed") {
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
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	runCLIJSON(t, []string{"client", "register"}, map[string]any{
		"client_ref": workerEvidenceClientRef, "key_id": "worker-key-1", "principal_ref": "operator-1",
		"public_key": base64.StdEncoding.EncodeToString(publicKey), "capabilities": []string{"product_read"},
		"product_scope": []string{"product-1"}, "project_scope": []string{"project-1"},
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

// TestWorkerEvidenceRecordsTheVerifiedClientAsActor proves the literal
// "worker:cli" actor string is gone: the recorded actor is a proven identity.
func TestWorkerEvidenceRecordsTheVerifiedClientAsActor(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	t.Setenv(dbOverrideEnv, dbPath)
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
