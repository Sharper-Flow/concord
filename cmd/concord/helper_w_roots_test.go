package main

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

// wFailureObservation binds the producer observation required by the current
// family's failed-terminal contract to the authenticated CLI assertion.
func wFailureObservation(t *testing.T, request map[string]any, assertion agent.WorkerEvidenceAssertion, observation *store.WorkerFailureObservation) agent.WorkerEvidenceAssertion {
	t.Helper()
	digest, err := store.WorkerFailureObservationDigest(observation)
	if err != nil {
		t.Fatal(err)
	}
	request["failure_attribution"] = observation
	assertion.Version = agent.WorkerEvidenceAssertionVersion2
	assertion.ObservationDigest = digest
	return assertion
}

func wWorkerFailJSON(t *testing.T, key ed25519.PrivateKey, eventID, workID, attemptID string, lane store.LaneDefinition, readback, failureKind, detail, nonce string) string {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal([]byte(workerFailJSON(t, key, eventID, workID, attemptID, lane, readback, failureKind, detail, nonce)), &request); err != nil {
		t.Fatal(err)
	}
	assertion := agent.WorkerEvidenceAssertion{
		Verb: agent.WorkerEvidenceVerbFail, WorkID: workID, AttemptID: attemptID,
		LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		ReadbackModel: readback, FailureKind: failureKind, Nonce: nonce,
	}
	assertion = wFailureObservation(t, request, assertion, attributionHostTerminalObservation(detail))
	request["assertion"] = signWorkerEvidence(t, key, assertion)
	return mustJSON(t, request)
}
