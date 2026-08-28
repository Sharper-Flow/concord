package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// CapabilityWorkerEvidence authorizes a registered client to record worker
// attempt evidence. It is a client-policy capability only: no agent-tool
// operation maps to it and no grant assertion may request it, so it is never
// reachable by a bearer token an agent holds. Worker evidence is authorized by
// a per-write signature from the registered client, never by a grant.
const CapabilityWorkerEvidence Capability = "worker_evidence"

// CapabilityWorkerDispatch authorizes a registered client to invoke the
// dispatch_worker registered workflow action (CD-0059 D3). It mirrors
// CapabilityWorkerEvidence: registrable in a client policy but deliberately
// absent from the grant-request vocabulary, so a worker that holds only
// work_transition cannot dispatch a nested worker. The capability lives
// in the policy boundary so CD-0017 D4's nested-worker rule is structural,
// not a host-side convention.
const CapabilityWorkerDispatch Capability = "worker_dispatch"

// The three evidence-recording verbs a trusted client may sign for. The verb is
// part of the signed bytes so a dispatch assertion cannot be presented as a
// completion.
const (
	WorkerEvidenceVerbDispatch = "worker-dispatch"
	WorkerEvidenceVerbComplete = "worker-complete"
	WorkerEvidenceVerbFail     = "worker-fail"
)

// WorkerEvidenceVerbs enumerates every verb an assertion may claim. Validation
// reads this list, and so does the shared vector at
// adapter/opencode/worker-evidence-vector.json, so a verb cannot become
// acceptable without a case that pins the field set its binding populates.
var WorkerEvidenceVerbs = []string{WorkerEvidenceVerbDispatch, WorkerEvidenceVerbComplete, WorkerEvidenceVerbFail}

// WorkerEvidenceAssertion is the proof a registered client presents when it
// records worker evidence. It authenticates the caller and binds the exact
// attempt, lane, readback model, and (for dispatch) the canonical lane-packet
// digest the core recorded on the dispatch_worker completion. It carries no
// workflow authority: a valid assertion still cannot transition a step, record
// a verdict, or complete work.
type WorkerEvidenceAssertion struct {
	ClientRef            string `json:"client_ref"`
	Verb                 string `json:"verb"`
	WorkID               string `json:"work_id"`
	AttemptID            string `json:"attempt_id"`
	LaneID               string `json:"lane_id"`
	LaneVersion          int64  `json:"lane_version"`
	LaneDigest           string `json:"lane_digest"`
	ReadbackModel        string `json:"readback_model"`
	FailureKind          string `json:"failure_kind"`
	HostProvenanceDigest string `json:"host_provenance_digest"`
	PacketDigest         string `json:"packet_digest"`
	IssuedAt             string `json:"issued_at"`
	Nonce                string `json:"nonce"`
	Signature            []byte `json:"signature"`
}

// CanonicalWorkerEvidenceAssertion is the byte format a trusted client signs
// for a worker-evidence write. The format is deliberately not JSON: the prefix
// is `worker-evidence-v1\0`, followed by fixed, named fields
// encoded as `name=<UTF-8 byte length>:<UTF-8 value>|`. Field order is part of
// the contract and matches the adapter mirror; the shared vector at
// adapter/opencode/worker-evidence-vector.json pins both implementations to one
// byte sequence. Fields that do not apply to a verb are signed as empty —
// complete and fail sign an empty packet_digest because dispatch is the only
// verb that opens a packet-bound window.
func CanonicalWorkerEvidenceAssertion(a WorkerEvidenceAssertion) []byte {
	values := []string{
		a.ClientRef,
		a.Verb,
		a.WorkID,
		a.AttemptID,
		a.LaneID,
		strconv.FormatInt(a.LaneVersion, 10),
		a.LaneDigest,
		a.ReadbackModel,
		a.FailureKind,
		a.HostProvenanceDigest,
		a.PacketDigest,
		a.IssuedAt,
		a.Nonce,
	}
	names := []string{
		"client_ref",
		"verb",
		"work_id",
		"attempt_id",
		"lane_id",
		"lane_version",
		"lane_digest",
		"readback_model",
		"failure_kind",
		"host_provenance_digest",
		"packet_digest",
		"issued_at",
		"nonce",
	}
	var b strings.Builder
	b.WriteString("worker-evidence-v1\x00")
	for i, name := range names {
		value := values[i]
		b.WriteString(name)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len([]byte(value))))
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte('|')
	}
	return []byte(b.String())
}

// WorkerEvidenceBinding is the identity the CLI boundary already established
// from the lane registry and stored attempt. The assertion must claim exactly
// this identity, so a signature captured for one attempt cannot authorize
// evidence for another. PacketDigest is only populated for dispatch; terminal
// verbs leave it empty because the dispatch window is the only surface it
// guards.
type WorkerEvidenceBinding struct {
	Verb                 string
	WorkID               string
	AttemptID            string
	LaneID               string
	LaneVersion          int64
	LaneDigest           string
	ReadbackModel        string
	FailureKind          string
	HostProvenanceDigest string
	PacketDigest         string
}

func validWorkerEvidenceAssertion(a WorkerEvidenceAssertion) bool {
	if !oneOf(a.Verb, WorkerEvidenceVerbs...) {
		return false
	}
	if !bounded(a.ClientRef, 1, 128) || !bounded(a.WorkID, 1, 128) || !bounded(a.AttemptID, 1, 128) {
		return false
	}
	if len(a.Nonce) < 16 || len(a.Nonce) > 256 {
		return false
	}
	if len(a.Signature) != ed25519.SignatureSize {
		return false
	}
	return true
}

func workerEvidenceMatchesBinding(a WorkerEvidenceAssertion, binding WorkerEvidenceBinding) bool {
	return a.Verb == binding.Verb &&
		a.WorkID == binding.WorkID &&
		a.AttemptID == binding.AttemptID &&
		a.LaneID == binding.LaneID &&
		a.LaneVersion == binding.LaneVersion &&
		a.LaneDigest == binding.LaneDigest &&
		a.ReadbackModel == binding.ReadbackModel &&
		a.FailureKind == binding.FailureKind &&
		a.HostProvenanceDigest == binding.HostProvenanceDigest &&
		a.PacketDigest == binding.PacketDigest
}

func clientHoldsWorkerEvidenceCapability(capabilitiesJSON string) bool {
	var capabilities []string
	if err := json.Unmarshal([]byte(capabilitiesJSON), &capabilities); err != nil {
		return false
	}
	for _, capability := range capabilities {
		if capability == string(CapabilityWorkerEvidence) {
			return true
		}
	}
	return false
}

// ValidateWorkerEvidenceAssertionTx authenticates a worker-evidence write and
// consumes its nonce in the caller's transaction, so authorization and the
// durable evidence share one commit. It returns the verified principal so the
// event actor is a proven client identity rather than a literal string.
//
// It authenticates the caller; it does not widen what a worker may do. CD-0017
// D4's boundary is unchanged: this path records attempt evidence only, and the
// owner still accepts a result through the workflow dispatcher.
func (s *Service) ValidateWorkerEvidenceAssertionTx(ctx context.Context, tx *store.Transaction, assertion WorkerEvidenceAssertion, binding WorkerEvidenceBinding) (string, error) {
	if err := s.authorityReady("agent_validate_worker_evidence"); err != nil {
		return "", err
	}
	if tx == nil {
		return "", transactionInvalid("agent_validate_worker_evidence")
	}
	if !validWorkerEvidenceAssertion(assertion) {
		return "", errors.New("worker evidence assertion is malformed")
	}
	if !workerEvidenceMatchesBinding(assertion, binding) {
		return "", errors.New("worker evidence assertion does not bind this attempt identity")
	}
	issued, err := time.Parse(time.RFC3339Nano, assertion.IssuedAt)
	if err != nil || issued.Before(s.now().Add(-s.skew())) || issued.After(s.now().Add(s.skew())) {
		return "", errors.New("worker evidence assertion timestamp invalid")
	}
	client, key, err := store.TrustedClientWithKeyTx(ctx, tx, assertion.ClientRef)
	if err != nil {
		return "", errors.New("trusted client unavailable")
	}
	if client.Status != "active" || key.Status != "active" || key.KeyID == "" {
		return "", errors.New("trusted client is not active")
	}
	if !clientHoldsWorkerEvidenceCapability(client.CapabilitiesJSON) {
		return "", errors.New("trusted client policy does not carry the worker_evidence capability")
	}
	if !ed25519.Verify(ed25519.PublicKey(key.PublicKey), CanonicalWorkerEvidenceAssertion(assertion), assertion.Signature) {
		return "", errors.New("worker evidence assertion signature invalid")
	}
	now := s.now()
	if err := store.PruneAndRecordNonceTx(ctx, tx, assertion.ClientRef, assertion.Nonce, now.Format(time.RFC3339Nano), now.Add(s.skew()).Format(time.RFC3339Nano), now.Add(-s.skew()).Format(time.RFC3339Nano)); err != nil {
		return "", errors.New("worker evidence assertion nonce replayed")
	}
	return client.PrincipalRef, nil
}
