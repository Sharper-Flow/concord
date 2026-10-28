package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// CapabilityWorkerEvidence authorizes a registered client to record worker
// attempt evidence. It is a client-policy capability only: no agent-tool
// operation maps to it and no grant assertion may request it, so it is never
// reachable by a bearer token an agent holds. Worker evidence is authorized by
// a per-write signature from the registered client, never by a grant.
const CapabilityWorkerEvidence agentcontract.Capability = "worker_evidence"

// CapabilityWorkerDispatch authorizes a registered client to invoke the
// dispatch_worker registered workflow action (CD-0059 D3). It mirrors
// CapabilityWorkerEvidence: registrable in a client policy but deliberately
// absent from the grant-request vocabulary, so a worker that holds only
// work_transition cannot dispatch a nested worker. The capability lives
// in the policy boundary so CD-0017 D4's nested-worker rule is structural,
// not a host-side convention.
const CapabilityWorkerDispatch agentcontract.Capability = "worker_dispatch"

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

// Worker evidence assertion schema versions. Absent (0) and 1 are the same
// released v1 byte format; 2 signs the observation digest with the v2
// prefix. Any other version is refused before the signature is checked.
const (
	WorkerEvidenceAssertionVersion1 = 1
	WorkerEvidenceAssertionVersion2 = 2
)

// workerEvidenceSchemaVersion normalizes a declared version to its byte
// format: 0 and 1 are v1, 2 is v2, and anything else keeps its value so a
// comparison cannot silently equate an unknown version with a known one.
// Validation refuses an unknown assertion version outright; the normalizer
// only serves the binding comparison, where a binding this build does not
// know must fail the match rather than collapse into v1.
func workerEvidenceSchemaVersion(version int) int {
	switch version {
	case 0, WorkerEvidenceAssertionVersion1:
		return WorkerEvidenceAssertionVersion1
	case WorkerEvidenceAssertionVersion2:
		return WorkerEvidenceAssertionVersion2
	default:
		return version
	}
}

// validWorkerEvidenceObservationDigest pins the digest shape v2 signs: a
// sha256 over the canonical observation content, lowercase hex, exactly 64
// digits. The assertion authenticates this value; its binding must come
// from independently validated observation content.
func validWorkerEvidenceObservationDigest(digest string) bool {
	const prefix = "sha256:"
	if len(digest) != len(prefix)+64 || !strings.HasPrefix(digest, prefix) {
		return false
	}
	for _, r := range digest[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// WorkerEvidenceAssertion is the proof a registered client presents when it
// records worker evidence. It authenticates the caller and binds the exact
// attempt, lane, readback model, and (for dispatch) the canonical lane-packet
// digest the core recorded on the dispatch_worker completion. It carries no
// workflow authority: a valid assertion still cannot transition a step, record
// a verdict, or complete work.
//
// Version selects the signed byte format. Absent and 1 are the released v1
// format, which has no slot for an observation digest and refuses one; 2
// signs the canonical observation-content digest established by the binding,
// so a changed observation cannot pass with a v1-era
// signature. Both members stay omitted on the wire for v1 writes.
type WorkerEvidenceAssertion struct {
	ClientRef            string `json:"client_ref"`
	Verb                 string `json:"verb"`
	WorkID               string `json:"work_id"`
	AttemptID            string `json:"attempt_id"`
	LaneID               string `json:"lane_id"`
	LaneDigest           string `json:"lane_digest"`
	ReadbackModel        string `json:"readback_model"`
	FailureKind          string `json:"failure_kind"`
	HostProvenanceDigest string `json:"host_provenance_digest"`
	PacketDigest         string `json:"packet_digest"`
	Version              int    `json:"version,omitempty"`
	ObservationDigest    string `json:"observation_digest,omitempty"`
	IssuedAt             string `json:"issued_at"`
	Nonce                string `json:"nonce"`
	Signature            []byte `json:"signature"`
}

// CanonicalWorkerEvidenceAssertion is the byte format a trusted client signs
// for a worker-evidence write. The format is deliberately not JSON: the
// prefix is `worker-evidence-v1\0`, followed by fixed, named fields
// encoded as `name=<UTF-8 byte length>:<UTF-8 value>|`. Field order is part of
// the contract and matches the adapter mirror; the shared vector at
// adapter/opencode/worker-evidence-vector.json pins both implementations to one
// byte sequence. Fields that do not apply to a verb are signed as empty —
// complete and fail sign an empty packet_digest because dispatch is the only
// verb that opens a packet-bound window.
//
// Version 2 keeps the same framing under the prefix `worker-evidence-v2\0`
// and inserts observation_digest between packet_digest and issued_at. The v1
// encoding stays byte-exact with the released vectors: a v1 assertion never
// carries an observation digest, so its slot does not exist even as empty.
func CanonicalWorkerEvidenceAssertion(a WorkerEvidenceAssertion) []byte {
	names := []string{
		"client_ref",
		"verb",
		"work_id",
		"attempt_id",
		"lane_id",
		"lane_digest",
		"readback_model",
		"failure_kind",
		"host_provenance_digest",
		"packet_digest",
		"issued_at",
		"nonce",
	}
	values := []string{
		a.ClientRef,
		a.Verb,
		a.WorkID,
		a.AttemptID,
		a.LaneID,
		a.LaneDigest,
		a.ReadbackModel,
		a.FailureKind,
		a.HostProvenanceDigest,
		a.PacketDigest,
		a.IssuedAt,
		a.Nonce,
	}
	prefix := "worker-evidence-v1\x00"
	if a.Version == WorkerEvidenceAssertionVersion2 {
		prefix = "worker-evidence-v2\x00"
		names = insertWorkerEvidenceField(names, 10, "observation_digest")
		values = insertWorkerEvidenceField(values, 10, a.ObservationDigest)
	}
	return canonicalClientAssertion(prefix, names, values)
}

// insertWorkerEvidenceField inserts value into fields at the given position,
// shifting the tail right. The encoder uses it to place the v2 digest slot
// without disturbing the released v1 field order.
func insertWorkerEvidenceField(fields []string, at int, value string) []string {
	fields = append(fields, "")
	copy(fields[at+1:], fields[at:])
	fields[at] = value
	return fields
}

// WorkerEvidenceBinding is the identity the CLI boundary already established
// from the lane registry and stored attempt. The assertion must claim exactly
// this identity, so a signature captured for one attempt cannot authorize
// evidence for another. PacketDigest is only populated for dispatch; terminal
// verbs leave it empty because the dispatch window is the only surface it
// guards. Version and ObservationDigest carry the same schema semantics as
// the assertion: the comparison normalizes absent and 1 to v1 and requires
// byte equality on the digest, so a v2 assertion never passes a v1 binding
// and vice versa.
type WorkerEvidenceBinding struct {
	Verb                 string
	WorkID               string
	AttemptID            string
	LaneID               string
	LaneDigest           string
	ReadbackModel        string
	FailureKind          string
	HostProvenanceDigest string
	PacketDigest         string
	Version              int
	ObservationDigest    string
}

func validWorkerEvidenceAssertion(a WorkerEvidenceAssertion) bool {
	if !slices.Contains(WorkerEvidenceVerbs, a.Verb) {
		return false
	}
	// The shape rules switch on the declared version, not the normalized
	// byte format: a version this build does not know refuses here, before
	// the signature or the binding comparison can vouch for it.
	switch a.Version {
	case 0, WorkerEvidenceAssertionVersion1:
		// v1 is byte-exact with the released vectors and has no signed
		// slot for an observation digest.
		if a.ObservationDigest != "" {
			return false
		}
	case WorkerEvidenceAssertionVersion2:
		if !validWorkerEvidenceObservationDigest(a.ObservationDigest) {
			return false
		}
	default:
		return false
	}
	if !agentcontract.BoundedBytes(a.ClientRef, 1, 128) || !agentcontract.BoundedBytes(a.WorkID, 1, 128) || !agentcontract.BoundedBytes(a.AttemptID, 1, 128) {
		return false
	}
	if !validWorkerNonce(a.Nonce) {
		return false
	}
	if len(a.Signature) != ed25519.SignatureSize {
		return false
	}
	return true
}

// validWorkerNonce uses the code-point bound of agent_nonce_replay.
// NUL is forbidden because SQLite length(TEXT) stops at its first NUL.
func validWorkerNonce(nonce string) bool {
	if !utf8.ValidString(nonce) || strings.ContainsRune(nonce, '\x00') {
		return false
	}
	length := utf8.RuneCountInString(nonce)
	return length >= 16 && length <= 256
}

func workerEvidenceMatchesBinding(a WorkerEvidenceAssertion, binding WorkerEvidenceBinding) bool {
	if workerEvidenceSchemaVersion(a.Version) != workerEvidenceSchemaVersion(binding.Version) {
		return false
	}
	if a.ObservationDigest != binding.ObservationDigest {
		return false
	}
	return a.Verb == binding.Verb &&
		a.WorkID == binding.WorkID &&
		a.AttemptID == binding.AttemptID &&
		a.LaneID == binding.LaneID &&
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
		return "", trustedClientRefusalFor(err, errors.New("trusted client unavailable"))
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
		var failure *store.Failure
		if errors.As(err, &failure) && failure.Kind == store.KindProjectionConflict {
			return "", errors.New("worker evidence assertion nonce replayed")
		}
		return "", err
	}
	return client.PrincipalRef, nil
}
