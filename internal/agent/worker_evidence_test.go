package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
)

// TestCanonicalWorkerEvidenceVector pins the signed byte sequence. The adapter
// mirrors this encoder in TypeScript; both read this vector, so a divergence
// that would let one side accept bytes the other never produced fails here
// rather than silently weakening the boundary.
func TestCanonicalWorkerEvidenceVector(t *testing.T) {
	raw, err := os.ReadFile("../../adapter/opencode/worker-evidence-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		ClientRef            string `json:"client_ref"`
		Verb                 string `json:"verb"`
		WorkID               string `json:"work_id"`
		AttemptID            string `json:"attempt_id"`
		LaneID               string `json:"lane_id"`
		LaneVersion          int64  `json:"lane_version"`
		LaneDigest           string `json:"lane_digest"`
		RoutingPolicyVersion string `json:"routing_policy_version"`
		RoutingPolicyDigest  string `json:"routing_policy_digest"`
		ResolvedModel        string `json:"resolved_model"`
		ReadbackModel        string `json:"readback_model"`
		FailureKind          string `json:"failure_kind"`
		HostProvenanceDigest string `json:"host_provenance_digest"`
		IssuedAt             string `json:"issued_at"`
		Nonce                string `json:"nonce"`
		CanonicalBase64      string `json:"canonical_base64"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	assertion := WorkerEvidenceAssertion{
		ClientRef: vector.ClientRef, Verb: vector.Verb, WorkID: vector.WorkID, AttemptID: vector.AttemptID,
		LaneID: vector.LaneID, LaneVersion: vector.LaneVersion, LaneDigest: vector.LaneDigest,
		RoutingPolicyVersion: vector.RoutingPolicyVersion, RoutingPolicyDigest: vector.RoutingPolicyDigest,
		ResolvedModel: vector.ResolvedModel, ReadbackModel: vector.ReadbackModel, FailureKind: vector.FailureKind,
		HostProvenanceDigest: vector.HostProvenanceDigest, IssuedAt: vector.IssuedAt, Nonce: vector.Nonce,
	}
	if got := base64.StdEncoding.EncodeToString(CanonicalWorkerEvidenceAssertion(assertion)); got != vector.CanonicalBase64 {
		t.Fatalf("canonical worker evidence mismatch:\n got %s\nwant %s", got, vector.CanonicalBase64)
	}
}

// TestWorkerEvidenceCapabilityIsNotGrantRequestable proves the capability is
// client-policy authority only. A grant assertion that requests it must be
// refused, so no bearer token an agent holds can ever carry it.
func TestWorkerEvidenceCapabilityIsNotGrantRequestable(t *testing.T) {
	if validSignedRequests(SignedAssertion{RequestedCapabilities: []Capability{CapabilityWorkerEvidence}}) {
		t.Fatal("grant assertion accepted a requested worker_evidence capability")
	}
	if !validTrustedPolicy(TrustedClientPolicy{PrincipalRef: "operator-1", Capabilities: []Capability{CapabilityWorkerEvidence}}) {
		t.Fatal("client policy refused the worker_evidence capability")
	}
}
