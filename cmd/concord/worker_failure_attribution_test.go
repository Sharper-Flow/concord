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

// CLI acceptance witnesses for the signed failure-attribution route.
// Every case drives the actual worker-evidence CLI verbs
// against a synthetic store: the v2 assertion authenticates the canonical
// observation digest of the request's own producer observation, a changed
// observation under an old signature refuses with zero durable effects, and
// every malformed provenance refuses before the nonce is durably committed —
// proven by replaying the original valid request on the same nonce after the
// refusals.

// attributionHostTerminalObservation is one host_adapter host_execution
// observation citing a typed terminal host error.
func attributionHostTerminalObservation(message string) *store.WorkerFailureObservation {
	return &store.WorkerFailureObservation{
		Observer: store.WorkerFailureObserverHostAdapter,
		Phase:    store.WorkerFailurePhaseHostExecution,
		Cause:    store.WorkerFailureCauseHostProvider,
		Basis:    store.WorkerFailureBasisSupported,
		Sources: []store.WorkerFailureSource{{
			Kind:    store.WorkerFailureSourceHostEvent,
			Locator: "evt:cli-witness-1",
			Digest:  "sha256:" + strings.Repeat("a", 64),
			Message: message,
			Host: &store.WorkerFailureHostObservation{
				Tag: store.WorkerFailureHostTagTerminal, EventType: "session.error",
				SessionID: "ses-cli-witness", MessageID: "msg-cli-witness",
				ErrorName: "APIError", ErrorCode: "provider_5xx", StatusCode: 503,
			},
		}},
	}
}

// attributionWorkerReportObservation is one worker_report worker_execution
// observation citing admitted report evidence.
func attributionWorkerReportObservation() *store.WorkerFailureObservation {
	return &store.WorkerFailureObservation{
		Observer: store.WorkerFailureObserverWorkerReport,
		Phase:    store.WorkerFailurePhaseWorkerExecution,
		Cause:    store.WorkerFailureCauseUnknown,
		Basis:    store.WorkerFailureBasisUnknown,
		Sources: []store.WorkerFailureSource{{
			Kind:    store.WorkerFailureSourceAdmittedReport,
			Locator: "report-cli-witness-1",
			Digest:  "sha256:" + strings.Repeat("b", 64),
			Message: "worker reported the failed command",
		}},
	}
}

// attributionSignedV2Assertion returns the worker-fail request's assertion
// signed over the v2 canonical bytes with the given observation digest.
func attributionSignedV2Assertion(t *testing.T, key ed25519.PrivateKey, attemptID string, lane store.LaneDefinition, readback, nonce, observationDigest string, mutate func(*agent.WorkerEvidenceAssertion)) map[string]any {
	t.Helper()
	assertion := agent.WorkerEvidenceAssertion{
		Verb: agent.WorkerEvidenceVerbFail, WorkID: "work-1", AttemptID: attemptID,
		LaneID: lane.ID, LaneDigest: lane.Digest,
		ReadbackModel: readback, FailureKind: store.WorkerFailureWorkerError,
		Version:           agent.WorkerEvidenceAssertionVersion2,
		ObservationDigest: observationDigest,
		Nonce:             nonce,
	}
	if mutate != nil {
		mutate(&assertion)
	}
	return signWorkerEvidence(t, key, assertion)
}

// attributionDispatchJSON builds one ordinary worker-dispatch request for the
// named attempt, signed v1 exactly as the released dispatch evidence is.
func attributionDispatchJSON(t *testing.T, key ed25519.PrivateKey, workID, attemptID string, lane store.LaneDefinition, readback, nonce string) string {
	t.Helper()
	assertion := agent.WorkerEvidenceAssertion{
		Verb: agent.WorkerEvidenceVerbDispatch, WorkID: workID, AttemptID: attemptID,
		LaneID: lane.ID, LaneDigest: lane.Digest,
		ReadbackModel:        readback,
		HostProvenanceDigest: "sha256:" + strings.Repeat("a", 64),
		PacketDigest:         "sha256:" + strings.Repeat("c", 64),
		Nonce:                nonce,
	}
	value := map[string]any{
		"event_id": "dispatch-" + attemptID, "work_id": workID, "attempt_id": attemptID,
		"lane_id": lane.ID, "lane_digest": lane.Digest,
		"packet_schema_version": store.WorkerPacketSchemaVersion,
		"report_schema_version": store.WorkerReportSchemaVersion,
		"packet_digest":         "sha256:" + strings.Repeat("c", 64),
		"readback_model":        readback,
		"host_provenance": map[string]any{
			"digest":  "sha256:" + strings.Repeat("a", 64),
			"sources": []map[string]any{{"kind": "agent_definition", "path": ".opencode/agents/" + lane.ID + ".md", "sha256": "sha256:" + strings.Repeat("b", 64)}},
		},
		"assertion": signWorkerEvidence(t, key, assertion),
	}
	return mustJSON(t, value)
}

// runAttributionCLI runs one CLI verb against the synthetic store and fails
// the test only on an unexpected harness error.
func runAttributionCLI(t *testing.T, command, raw string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runWithInput([]string{command}, strings.NewReader(raw), &out, &errOut)
	return code, errOut.String()
}

// attributionNonceRows counts the durable nonce rows one client nonce has.
func attributionNonceRows(t *testing.T, dbPath, nonce string) int {
	t.Helper()
	s := mustOpenStore(t, dbPath)
	defer s.Close()
	var rows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_nonce_replay WHERE nonce=?`, nonce).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// attributionStoredFailureAttribution reads one stored worker.failed payload's
// attribution block.
func attributionStoredFailureAttribution(t *testing.T, dbPath, attemptID string) store.WorkerFailureAttribution {
	t.Helper()
	s := mustOpenStore(t, dbPath)
	defer s.Close()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(a.terminal_payload,'$.failure_attribution') FROM worker_attempts a JOIN effect_receipts r ON r.event_id=a.terminal_event_id AND r.effect_seq=a.terminal_seq WHERE r.subject_type='work_item' AND r.kind=? AND a.attempt_id=?`, store.WorkerFailed, attemptID).Scan(&raw); err != nil {
		t.Fatalf("read the stored failure attribution: %v", err)
	}
	var block store.WorkerFailureAttribution
	if err := json.Unmarshal([]byte(raw), &block); err != nil {
		t.Fatal(err)
	}
	return block
}

// TestWorkerFailAttributionV2AuthenticatesTheProducerObservation is the
// A10/A12 acceptance witness at the CLI boundary. A v2-signed worker-fail
// whose assertion carries the observation digest records the attribution with
// the store-derived identity; a changed observation under the old signature
// refuses with zero events and a still-usable nonce; wrong work, attempt,
// lane, and malformed observer/source provenance each refuse before the nonce
// is durably committed; and the original valid request lands on the same
// nonce afterwards.
func TestWorkerFailAttributionV2AuthenticatesTheProducerObservation(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	const workID, attemptID = "work-1", "attempt-1"
	seedAuthorizedDispatchWindow(t, dbPath, workID, attemptID)
	if code, errOut := runAttributionCLI(t, "worker-dispatch", attributionDispatchJSON(t, key, workID, attemptID, lane, readback, "nonce-con787-seed-dispatch")); code != 0 {
		t.Fatalf("seed dispatch exit=%d stderr=%q", code, errOut)
	}

	observation := attributionHostTerminalObservation("provider 503 during host execution")
	digest, digestErr := store.WorkerFailureObservationDigest(observation)
	if digestErr != nil {
		t.Fatal(digestErr)
	}
	const nonce = "nonce-con787-v2-fail-00001"
	request := map[string]any{
		"event_id": "failed-con787-cli-witness", "work_id": workID, "attempt_id": attemptID,
		"readback_model": readback, "failure_kind": store.WorkerFailureWorkerError,
		"detail":              "the attempt failed during host execution",
		"assertion":           attributionSignedV2Assertion(t, key, attemptID, lane, readback, nonce, digest, nil),
		"failure_attribution": observation,
	}

	t.Run("a changed observation under the old signature refuses with its nonce intact", func(t *testing.T) {
		altered := map[string]any{}
		if err := json.Unmarshal([]byte(mustJSON(t, request)), &altered); err != nil {
			t.Fatal(err)
		}
		altered["failure_attribution"] = attributionHostTerminalObservation("provider 504 AFTER the signature")
		code, errOut := runAttributionCLI(t, "worker-fail", mustJSON(t, altered))
		if code == 0 || !strings.Contains(errOut, "does not bind this attempt identity") {
			t.Fatalf("altered-observation worker-fail exit=%d stderr=%q, want the binding refusal", code, errOut)
		}
		attributionAssertNoEffects(t, dbPath, nonce)
	})

	for _, testCase := range []struct {
		name          string
		nonce         string
		mutate        func(*agent.WorkerEvidenceAssertion)
		observation   *store.WorkerFailureObservation
		digestRefuses bool
		wantSub       string
	}{
		{
			name: "an assertion claiming another work item", wantSub: "does not bind this attempt identity",
			nonce:  "nonce-con787-refused-workid-01",
			mutate: func(a *agent.WorkerEvidenceAssertion) { a.WorkID = "work-somewhere-else" },
		},
		{
			name: "an assertion claiming another attempt", wantSub: "does not bind this attempt identity",
			nonce:  "nonce-con787-refused-attempt-1",
			mutate: func(a *agent.WorkerEvidenceAssertion) { a.AttemptID = "attempt-somewhere-else" },
		},
		{
			name: "an assertion claiming another lane", wantSub: "does not bind this attempt identity",
			nonce:  "nonce-con787-refused-laneid-01",
			mutate: func(a *agent.WorkerEvidenceAssertion) { a.LaneID = "lane-somewhere-else" },
		},
		{
			name: "a worker report citing a host source", wantSub: "may cite only the source kinds it owns",
			nonce:         "nonce-con787-refused-source-1",
			digestRefuses: true,
			observation: &store.WorkerFailureObservation{
				Observer: store.WorkerFailureObserverWorkerReport, Phase: store.WorkerFailurePhaseWorkerExecution,
				Cause: store.WorkerFailureCauseUnknown, Basis: store.WorkerFailureBasisUnknown,
				Sources: []store.WorkerFailureSource{{Kind: store.WorkerFailureSourceHostEvent, Message: "wrong boundary",
					Host: &store.WorkerFailureHostObservation{Tag: store.WorkerFailureHostTagTerminal, EventType: "session.error"},
				}},
			},
		},
		{
			name: "a coordinator observation on a worker terminal", wantSub: "cannot record a worker terminal",
			nonce: "nonce-con787-refused-coord-1",
			observation: &store.WorkerFailureObservation{
				Observer: store.WorkerFailureObserverCoordinator, Phase: store.WorkerFailurePhaseResultReview,
				Cause: store.WorkerFailureCauseUnknown, Basis: store.WorkerFailureBasisUnknown,
				Sources: []store.WorkerFailureSource{{Kind: store.WorkerFailureSourceResult, Locator: "result:wrong-boundary", Digest: "sha256:" + strings.Repeat("d", 64)}},
			},
		},
	} {
		t.Run("refuses "+testCase.name, func(t *testing.T) {
			observation := testCase.observation
			if observation == nil {
				observation = attributionHostTerminalObservation("provider 503 during host execution")
			}
			caseDigest, digestErr := store.WorkerFailureObservationDigest(observation)
			if testCase.digestRefuses {
				if digestErr == nil {
					t.Fatalf("%s: the production digest accepted provenance the coupling table refuses", testCase.name)
				}
			} else if digestErr != nil {
				t.Fatal(digestErr)
			}
			refused := map[string]any{}
			if err := json.Unmarshal([]byte(mustJSON(t, request)), &refused); err != nil {
				t.Fatal(err)
			}
			refused["failure_attribution"] = observation
			if testCase.digestRefuses {
				// The CLI computes the observation digest before any assertion
				// work, so the malformed provenance refuses with the request's
				// plain released-format assertion and the nonce is untouched.
				refused["assertion"] = signWorkerEvidence(t, key, agent.WorkerEvidenceAssertion{
					Verb: agent.WorkerEvidenceVerbFail, WorkID: workID, AttemptID: attemptID,
					LaneID: lane.ID, LaneDigest: lane.Digest,
					ReadbackModel: readback, FailureKind: store.WorkerFailureWorkerError, Nonce: testCase.nonce,
				})
			} else {
				refused["assertion"] = attributionSignedV2Assertion(t, key, attemptID, lane, readback, testCase.nonce, caseDigest, testCase.mutate)
			}
			code, errOut := runAttributionCLI(t, "worker-fail", mustJSON(t, refused))
			if code == 0 || !strings.Contains(errOut, testCase.wantSub) {
				t.Fatalf("%s exit=%d stderr=%q, want refusal containing %q", testCase.name, code, errOut, testCase.wantSub)
			}
			attributionAssertNoEffects(t, dbPath, testCase.nonce)
		})
	}

	t.Run("the original signed request lands on the same nonce", func(t *testing.T) {
		code, errOut := runAttributionCLI(t, "worker-fail", mustJSON(t, request))
		if code != 0 {
			t.Fatalf("original worker-fail exit=%d stderr=%q, want acceptance", code, errOut)
		}
		if rows := attributionNonceRows(t, dbPath, nonce); rows != 1 {
			t.Fatalf("durable nonce rows = %d, want exactly the consumed assertion nonce", rows)
		}
		block := attributionStoredFailureAttribution(t, dbPath, attemptID)
		if block.Origin != store.WorkerFailureAttributionRecorded || block.Observation == nil {
			t.Fatalf("stored attribution = %+v, want the recorded producer observation", block)
		}
		if block.Identity == nil || block.Identity.Kind != store.WorkerFailureIdentityAttempt || block.Identity.Attempt == nil {
			t.Fatalf("stored identity = %+v, want the core-derived attempt variant", block.Identity)
		}
		identity := block.Identity.Attempt
		if identity.AttemptID != attemptID || identity.LaneID != lane.ID || identity.Epoch != 1 ||
			identity.PacketDigest != "sha256:"+strings.Repeat("c", 64) || !identity.NoJob || identity.Subject != nil {
			t.Fatalf("derived identity = %+v, want the seeded window (epoch 1, packet digest c*64, no job, no subject)", identity)
		}
		storedDigest, err := store.WorkerFailureObservationDigest(block.Observation)
		if err != nil {
			t.Fatal(err)
		}
		if storedDigest != digest {
			t.Fatalf("stored observation digest = %q, want the signed %q", storedDigest, digest)
		}
		s := mustOpenStore(t, dbPath)
		defer s.Close()
		var state, failureKind string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state, failure_kind FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&state, &failureKind); err != nil {
			t.Fatal(err)
		}
		if state != "failed" || failureKind != store.WorkerFailureWorkerError {
			t.Fatalf("attempt = (%q, %q), want the released failed worker_error disposition", state, failureKind)
		}
	})

	t.Run("the consumed nonce refuses a replay", func(t *testing.T) {
		code, errOut := runAttributionCLI(t, "worker-fail", mustJSON(t, request))
		if code == 0 || !strings.Contains(errOut, "nonce replayed") {
			t.Fatalf("replayed worker-fail exit=%d stderr=%q, want nonce replay refusal", code, errOut)
		}
	})
}

// attributionAssertNoEffects proves a refusal left zero durable effects: no
// terminal worker event and no nonce row for the refused assertion.
func attributionAssertNoEffects(t *testing.T, dbPath, nonce string) {
	t.Helper()
	assertNoTerminalWorkerEvent(t, dbPath, "")
	if rows := attributionNonceRows(t, dbPath, nonce); rows != 0 {
		t.Fatalf("the refused assertion durably committed %d nonce row(s), want zero", rows)
	}
}

// TestWorkerFailAttributionHostAndWorkerReportKeepTypedBoundaries pins the
// observer semantics for one failure kind: the host and worker routes record
// differently typed observations for the same worker_error terminal, each
// digest-verified on readback, and both keep the identical failure
// disposition and retry authority.
func TestWorkerFailAttributionHostAndWorkerReportKeepTypedBoundaries(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	routes := []struct {
		name        string
		attemptID   string
		observation *store.WorkerFailureObservation
	}{
		{name: "host", attemptID: "attempt-1", observation: attributionHostTerminalObservation("host observed the provider 503")},
		{name: "worker", attemptID: "attempt-2", observation: attributionWorkerReportObservation()},
	}
	digests := map[string]string{}
	for _, route := range routes {
		seedAuthorizedDispatchWindow(t, dbPath, "work-1", route.attemptID)
		if code, errOut := runAttributionCLI(t, "worker-dispatch", attributionDispatchJSON(t, key, "work-1", route.attemptID, lane, readback, "nonce-con787-typed-seed-"+route.attemptID)); code != 0 {
			t.Fatalf("seed dispatch %s exit=%d stderr=%q", route.attemptID, code, errOut)
		}
		digest, err := store.WorkerFailureObservationDigest(route.observation)
		if err != nil {
			t.Fatal(err)
		}
		digests[route.attemptID] = digest
		request := map[string]any{
			"event_id": "failed-con787-typed-" + route.attemptID, "work_id": "work-1", "attempt_id": route.attemptID,
			"readback_model": readback, "failure_kind": store.WorkerFailureWorkerError,
			"detail":              "the same worker_error terminal on both routes",
			"assertion":           attributionSignedV2Assertion(t, key, route.attemptID, lane, readback, "nonce-con787-typed-"+route.attemptID, digest, nil),
			"failure_attribution": route.observation,
		}
		if code, errOut := runAttributionCLI(t, "worker-fail", mustJSON(t, request)); code != 0 {
			t.Fatalf("%s route worker-fail exit=%d stderr=%q", route.name, code, errOut)
		}
	}
	host := attributionStoredFailureAttribution(t, dbPath, "attempt-1")
	worker := attributionStoredFailureAttribution(t, dbPath, "attempt-2")
	if host.Observation.Observer != store.WorkerFailureObserverHostAdapter || host.Observation.Phase != store.WorkerFailurePhaseHostExecution || host.Observation.Sources[0].Kind != store.WorkerFailureSourceHostEvent {
		t.Fatalf("host observation = %+v, want the host boundary's typed view", host.Observation)
	}
	if worker.Observation.Observer != store.WorkerFailureObserverWorkerReport || worker.Observation.Phase != store.WorkerFailurePhaseWorkerExecution || worker.Observation.Sources[0].Kind != store.WorkerFailureSourceAdmittedReport {
		t.Fatalf("worker observation = %+v, want the worker boundary's typed view", worker.Observation)
	}
	for _, route := range routes {
		block := attributionStoredFailureAttribution(t, dbPath, route.attemptID)
		stored, err := store.WorkerFailureObservationDigest(block.Observation)
		if err != nil {
			t.Fatal(err)
		}
		if stored != digests[route.attemptID] {
			t.Fatalf("%s route stored digest = %q, want the signed %q", route.name, stored, digests[route.attemptID])
		}
	}
	s := mustOpenStore(t, dbPath)
	defer s.Close()
	for _, route := range routes {
		var kind string
		if err := s.DatabaseForTesting().QueryRow(`SELECT failure_kind FROM worker_attempts WHERE attempt_id=?`, route.attemptID).Scan(&kind); err != nil {
			t.Fatal(err)
		}
		if kind != store.WorkerFailureWorkerError {
			t.Fatalf("%s route failure_kind = %q, want the shared worker_error disposition", route.name, kind)
		}
	}
}

// TestWorkerDispatchBornFailedAttributionReadback is the A12 born-failed
// witness at the CLI: the born-failed terminal dispatch carries the producer
// observation, the store derives the attempt identity from the dispatch
// authorization, and the readback names the typed terminal kind on both the
// attempt row and the attribution reader's born-failed arm.
func TestWorkerDispatchBornFailedAttributionReadback(t *testing.T) {
	dbPath := freshMigratedCLIDatabase(t)
	key := seedWorkerEvidenceClient(t)
	lane := store.BuiltinLaneDefinitions()[0]
	readback := preferredLaneModel(lane)
	const workID, attemptID = "work-1", "attempt-born"
	seedAuthorizedDispatchWindow(t, dbPath, workID, attemptID)

	observation := &store.WorkerFailureObservation{
		Observer: store.WorkerFailureObserverHostAdapter,
		Phase:    store.WorkerFailurePhaseDispatch,
		Cause:    store.WorkerFailureCauseIdentityProvenance,
		Basis:    store.WorkerFailureBasisHypothesis,
		Sources: []store.WorkerFailureSource{{
			Kind:    store.WorkerFailureSourceHostEvent,
			Locator: "evt:born-failed-readback",
			Digest:  "sha256:" + strings.Repeat("e", 64),
			Message: "the executing model did not match the declared identity",
			Host: &store.WorkerFailureHostObservation{
				Tag: store.WorkerFailureHostTagTerminal, EventType: "session.error",
				SessionID: "ses-born-readback", MessageID: "msg-born-readback",
				ErrorName: "undeclared_model",
			},
		}},
	}
	digest, err := store.WorkerFailureObservationDigest(observation)
	if err != nil {
		t.Fatal(err)
	}
	assertion := agent.WorkerEvidenceAssertion{
		Verb: agent.WorkerEvidenceVerbDispatch, WorkID: workID, AttemptID: attemptID,
		LaneID: lane.ID, LaneDigest: lane.Digest,
		ReadbackModel:        "provider/executing-model",
		FailureKind:          store.WorkerFailureModelIdentity,
		HostProvenanceDigest: "sha256:" + strings.Repeat("a", 64),
		PacketDigest:         "sha256:" + strings.Repeat("c", 64),
		Version:              agent.WorkerEvidenceAssertionVersion2,
		ObservationDigest:    digest,
		Nonce:                "nonce-con787-born-dispatch1",
	}
	request := map[string]any{
		"event_id": "born-failed-con787-readback", "work_id": workID, "attempt_id": attemptID,
		"lane_id": lane.ID, "lane_digest": lane.Digest,
		"packet_schema_version": store.WorkerPacketSchemaVersion,
		"report_schema_version": store.WorkerReportSchemaVersion,
		"packet_digest":         "sha256:" + strings.Repeat("c", 64),
		"readback_model":        "provider/executing-model",
		"host_provenance": map[string]any{
			"digest":  "sha256:" + strings.Repeat("a", 64),
			"sources": []map[string]any{{"kind": "agent_definition", "path": ".opencode/agents/" + lane.ID + ".md", "sha256": "sha256:" + strings.Repeat("b", 64)}},
		},
		"terminal":              "failed",
		"terminal_failure_kind": store.WorkerFailureModelIdentity,
		"terminal_detail":       "undeclared executing model",
		"failure_attribution":   observation,
		"assertion":             signWorkerEvidence(t, key, assertion),
	}
	if code, errOut := runAttributionCLI(t, "worker-dispatch", mustJSON(t, request)); code != 0 {
		t.Fatalf("born-failed dispatch exit=%d stderr=%q", code, errOut)
	}

	s := mustOpenStore(t, dbPath)
	defer s.Close()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT a.dispatch_payload FROM worker_attempts a JOIN effect_receipts r ON r.event_id=a.dispatch_event_id AND r.effect_seq=a.dispatch_seq WHERE r.subject_type='work_item' AND r.kind=? AND a.attempt_id=?`, store.WorkerDispatched, attemptID).Scan(&raw); err != nil {
		t.Fatalf("read the born-failed dispatch: %v", err)
	}
	var payload store.WorkerDispatchedPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Terminal != "failed" || payload.TerminalFailureKind != store.WorkerFailureModelIdentity {
		t.Fatalf("born-failed terminal = (%q, %q), want the failed model_identity record", payload.Terminal, payload.TerminalFailureKind)
	}
	if payload.FailureAttribution == nil || payload.FailureAttribution.Origin != store.WorkerFailureAttributionRecorded ||
		payload.FailureAttribution.Identity == nil || payload.FailureAttribution.Identity.Kind != store.WorkerFailureIdentityAttempt || payload.FailureAttribution.Identity.Attempt == nil {
		t.Fatalf("born-failed attribution = %+v, want the recorded block with the derived attempt identity", payload.FailureAttribution)
	}
	identity := payload.FailureAttribution.Identity.Attempt
	if identity.AttemptID != attemptID || identity.Epoch != 1 || identity.PacketDigest != "sha256:"+strings.Repeat("c", 64) || !identity.NoJob {
		t.Fatalf("born-failed identity = %+v, want the dispatch authorization's own window", identity)
	}
	var state, kind, model string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state, failure_kind, readback_model FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&state, &kind, &model); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || kind != store.WorkerFailureModelIdentity || model != "provider/executing-model" {
		t.Fatalf("born-failed attempt = (%q, %q, %q), want failed/model_identity_mismatch with the reported readback", state, kind, model)
	}
	record, readerErr := store.WorkerFailureAttributionForAttemptTx(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if readerErr != nil {
		t.Fatalf("the attribution reader refused the born-failed arm: %v", readerErr)
	}
	if record.EventID != "born-failed-con787-readback" || record.Attribution == nil || record.Attribution.Identity == nil {
		t.Fatalf("attribution reader record = %+v, want the born-failed dispatch evidence", record)
	}
	// The born-failed terminal is the attempt's terminal outcome: a later
	// authenticated worker-fail for the same attempt refuses.
	followUp := map[string]any{
		"event_id": "failed-con787-after-born", "work_id": workID, "attempt_id": attemptID,
		"readback_model": readback, "failure_kind": store.WorkerFailureWorkerError,
		"detail":    "a late failure report for a born-failed attempt",
		"assertion": attributionSignedV2Assertion(t, key, attemptID, lane, readback, "nonce-con787-after-born1", digest, nil),
	}
	if code, errOut := runAttributionCLI(t, "worker-fail", mustJSON(t, followUp)); code == 0 || !strings.Contains(errOut, "terminal outcome") {
		t.Fatalf("late worker-fail exit=%d stderr=%q, want the terminal-outcome refusal", code, errOut)
	}
}
