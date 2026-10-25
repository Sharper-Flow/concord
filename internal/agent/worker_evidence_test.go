package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/sharper-flow/concord/internal/store"
)

// workerEvidenceVectorCase is one verb's entry in the shared vector. The
// assertion and its canonical encoding pin the byte format; bound_fields
// declares the field set the CLI binding populates for that verb, which the
// adapter must sign and the CLI must enforce.
type workerEvidenceVectorCase struct {
	Verb        string   `json:"verb"`
	BoundFields []string `json:"bound_fields"`
	Assertion   struct {
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
		Version              int    `json:"version"`
		ObservationDigest    string `json:"observation_digest"`
		IssuedAt             string `json:"issued_at"`
		Nonce                string `json:"nonce"`
	} `json:"assertion"`
	CanonicalBase64 string `json:"canonical_base64"`
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
	return vector.Cases
}

// workerEvidenceVectorV2Cases reads the v2 section of the shared vector. The
// v1 cases stay in their own key byte for byte; the v2 cases pin the
// observation-digest format next to them.
func workerEvidenceVectorV2Cases(t *testing.T) []workerEvidenceVectorCase {
	t.Helper()
	raw, err := os.ReadFile("../../adapter/opencode/worker-evidence-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		V2Cases []workerEvidenceVectorCase `json:"v2_cases"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.V2Cases) == 0 {
		t.Fatal("shared worker evidence vector declares no v2_cases")
	}
	return vector.V2Cases
}

// TestCanonicalWorkerEvidenceVector pins the signed byte sequence. The adapter
// mirrors this encoder in TypeScript; both read this vector, so a divergence
// that would let one side accept bytes the other never produced fails here
// rather than silently weakening the boundary.
func TestCanonicalWorkerEvidenceVector(t *testing.T) {
	t.Parallel()
	cases := workerEvidenceVectorCases(t)
	if len(cases) == 0 {
		t.Fatal("shared worker evidence vector declares no cases")
	}
	for _, testCase := range cases {
		t.Run(testCase.Verb, func(t *testing.T) {
			value := testCase.Assertion
			assertion := WorkerEvidenceAssertion{
				ClientRef: value.ClientRef, Verb: value.Verb, WorkID: value.WorkID, AttemptID: value.AttemptID,
				LaneID: value.LaneID, LaneDigest: value.LaneDigest,
				ReadbackModel: value.ReadbackModel, FailureKind: value.FailureKind,
				HostProvenanceDigest: value.HostProvenanceDigest, PacketDigest: value.PacketDigest,
				IssuedAt: value.IssuedAt, Nonce: value.Nonce,
			}
			if value.Verb != testCase.Verb {
				t.Fatalf("case verb = %q but its assertion claims %q", testCase.Verb, value.Verb)
			}
			if got := base64.StdEncoding.EncodeToString(CanonicalWorkerEvidenceAssertion(assertion)); got != testCase.CanonicalBase64 {
				t.Fatalf("canonical worker evidence mismatch:\n got %s\nwant %s", got, testCase.CanonicalBase64)
			}
		})
	}
}

// TestCanonicalWorkerEvidenceVectorV2 pins the production v2 byte sequence:
// the worker-evidence-v2 prefix, the released v1 field order with
// observation_digest inserted before issued_at, and the digest itself as the
// signed value. A vector-structure witness alone does not prove signer parity.
func TestCanonicalWorkerEvidenceVectorV2(t *testing.T) {
	t.Parallel()
	for _, testCase := range workerEvidenceVectorV2Cases(t) {
		t.Run(testCase.Verb, func(t *testing.T) {
			value := testCase.Assertion
			if value.Version != WorkerEvidenceAssertionVersion2 {
				t.Fatalf("v2 case version = %d, want %d", value.Version, WorkerEvidenceAssertionVersion2)
			}
			if value.Verb != testCase.Verb {
				t.Fatalf("case verb = %q but its assertion claims %q", testCase.Verb, value.Verb)
			}
			if !validWorkerEvidenceObservationDigest(value.ObservationDigest) {
				t.Fatalf("v2 case observation_digest %q is not sha256:<64 lowercase hex>", value.ObservationDigest)
			}
			assertion := WorkerEvidenceAssertion{
				ClientRef: value.ClientRef, Verb: value.Verb, WorkID: value.WorkID, AttemptID: value.AttemptID,
				LaneID: value.LaneID, LaneDigest: value.LaneDigest,
				ReadbackModel: value.ReadbackModel, FailureKind: value.FailureKind,
				HostProvenanceDigest: value.HostProvenanceDigest, PacketDigest: value.PacketDigest,
				Version: value.Version, ObservationDigest: value.ObservationDigest,
				IssuedAt: value.IssuedAt, Nonce: value.Nonce,
			}
			canonical := CanonicalWorkerEvidenceAssertion(assertion)
			if !strings.HasPrefix(string(canonical), "worker-evidence-v2\x00") {
				t.Fatalf("v2 canonical bytes begin %q, want the worker-evidence-v2 prefix", canonical[:20])
			}
			if got := base64.StdEncoding.EncodeToString(canonical); got != testCase.CanonicalBase64 {
				t.Fatalf("canonical worker evidence v2 mismatch:\n got %s\nwant %s", got, testCase.CanonicalBase64)
			}
		})
	}
}

// TestWorkerFailureObservationDigestMatchesTheSharedVector pins the
// observation-digest half of the boundary through the production helper: every
// observation_vectors sample in the shared vector — exact HTML-escaped,
// U+2028/U+2029, and multibyte content, and samples whose optional members are
// omitted — hashes under store.WorkerFailureObservationDigest to exactly the
// digest the fixture records. The digests were computed by the Go production
// helper when the fixture was written, so agreement here proves the decode
// path, not a self-consistent synthetic value.
func TestWorkerFailureObservationDigestMatchesTheSharedVector(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../adapter/opencode/worker-evidence-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		ObservationVectors []struct {
			Name              string                         `json:"name"`
			Observation       store.WorkerFailureObservation `json:"observation"`
			ObservationDigest string                         `json:"observation_digest"`
		} `json:"observation_vectors"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	if len(vector.ObservationVectors) == 0 {
		t.Fatal("shared worker evidence vector declares no observation_vectors")
	}
	for _, vectorCase := range vector.ObservationVectors {
		t.Run(vectorCase.Name, func(t *testing.T) {
			observation := vectorCase.Observation
			if observation.Observer == "" || observation.Phase == "" || len(observation.Sources) == 0 {
				t.Fatalf("sample %q decoded no observation content: %+v", vectorCase.Name, observation)
			}
			digest, err := store.WorkerFailureObservationDigest(&observation)
			if err != nil {
				t.Fatalf("the production digest refused the vector observation %q: %v", vectorCase.Name, err)
			}
			if digest != vectorCase.ObservationDigest {
				t.Fatalf("observation digest mismatch for %q:\n got %s\nwant %s", vectorCase.Name, digest, vectorCase.ObservationDigest)
			}
		})
	}
}

// TestWorkerEvidenceVectorCoversEveryVerbAndBindingField keeps the shared
// bound_fields declaration honest against this package. Every acceptable verb
// needs a case, every declared field must exist on the binding, and every
// binding field must be claimed by some verb — so a fourth verb or a tenth
// binding field cannot land on one side of the boundary alone.
func TestWorkerEvidenceVectorCoversEveryVerbAndBindingField(t *testing.T) {
	t.Parallel()
	cases := workerEvidenceVectorCases(t)
	covered := make([]string, 0, len(cases))
	for _, testCase := range cases {
		covered = append(covered, testCase.Verb)
	}
	sort.Strings(covered)
	verbs := append([]string(nil), WorkerEvidenceVerbs...)
	sort.Strings(verbs)
	if !reflect.DeepEqual(covered, verbs) {
		t.Fatalf("vector covers verbs %v, want %v", covered, verbs)
	}

	// The v2 section must cover the same verb vocabulary, without diluting
	// the v1 exactness check above: .cases stays the v1-only verb set.
	v2Cases := workerEvidenceVectorV2Cases(t)
	v2Verbs := map[string]bool{}
	v1BoundByVerb := map[string][]string{}
	for _, testCase := range cases {
		v1BoundByVerb[testCase.Verb] = testCase.BoundFields
	}
	for _, testCase := range v2Cases {
		if !oneOf(testCase.Verb, WorkerEvidenceVerbs...) {
			t.Fatalf("v2 case claims verb %q, which is not a worker evidence verb", testCase.Verb)
		}
		v2Verbs[testCase.Verb] = true
		bound := map[string]bool{}
		for _, field := range testCase.BoundFields {
			bound[field] = true
		}
		for _, field := range v1BoundByVerb[testCase.Verb] {
			if !bound[field] {
				t.Fatalf("v2 %s drops bound field %q that v1 binds", testCase.Verb, field)
			}
		}
		for _, field := range []string{"version", "observation_digest"} {
			if !bound[field] {
				t.Fatalf("v2 %s must bind %q: the schema version and the signed digest are binding identity", testCase.Verb, field)
			}
		}
	}
	for _, verb := range WorkerEvidenceVerbs {
		if !v2Verbs[verb] {
			t.Fatalf("vector v2_cases has no case for verb %q", verb)
		}
	}

	// Every binding field must be claimed by some case across the schema
	// versions, so the new Version and ObservationDigest members cannot land
	// on the binding alone.
	bindingFields := map[string]bool{}
	bindingType := reflect.TypeOf(WorkerEvidenceBinding{})
	for i := range bindingType.NumField() {
		bindingFields[snakeCase(bindingType.Field(i).Name)] = false
	}
	for _, testCase := range append(append([]workerEvidenceVectorCase{}, cases...), v2Cases...) {
		for _, field := range testCase.BoundFields {
			claimed, known := bindingFields[field]
			if !known {
				t.Fatalf("%s binds %q, which is not a WorkerEvidenceBinding field", testCase.Verb, field)
			}
			if !claimed {
				bindingFields[field] = true
			}
		}
	}
	for field, claimed := range bindingFields {
		if !claimed {
			t.Fatalf("binding field %q is bound by no verb in the shared vector", field)
		}
	}
}

// snakeCase renders a Go field name as its canonical assertion field name.
func snakeCase(name string) string {
	var out strings.Builder
	runes := []rune(name)
	for i, r := range runes {
		upper := unicode.IsUpper(r)
		boundary := i > 0 && upper && (!unicode.IsUpper(runes[i-1]) || (i+1 < len(runes) && !unicode.IsUpper(runes[i+1])))
		if boundary {
			out.WriteByte('_')
		}
		out.WriteRune(unicode.ToLower(r))
	}
	return out.String()
}

func TestWorkerEvidenceCapabilityIsNotRequestable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, service, authority, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	invocation := Invocation{
		ClientRef: authority.ClientRef, PrincipalRef: authority.PrincipalRef, SessionRef: authority.SessionRef,
		AgentRef: authority.AgentRef, Directory: authority.Directory, Worktree: authority.Worktree,
		ManifestDigest: authority.ManifestDigest, RequiredCapability: CapabilityWorkerEvidence, ProjectID: "project-1",
	}
	if _, err := service.Authorize(ctx, invocation); err == nil {
		t.Fatal("worker_evidence obtained without a client policy entry")
	}
	if err := service.RegisterTrustedClient(ctx, testClientRegistration("worker-evidence-client", "worker-evidence-principal", []Capability{CapabilityWorkerEvidence}, []string{"product-1"}, []string{"project-1"})); err != nil {
		t.Fatal(err)
	}
	invocation.ClientRef = "worker-evidence-client"
	invocation.PrincipalRef = "worker-evidence-principal"
	if _, err := service.Authorize(ctx, invocation); err != nil {
		t.Fatalf("worker_evidence client policy was refused: %v", err)
	}
}

// TestWorkerEvidenceBindingMismatchOnPacketDigest pins CD-0067 D6 at the
// agent layer: the binding carries PacketDigest for dispatch, and an
// assertion that quotes a different value must refuse before the signature
// is even checked. The mismatch style mirrors the existing field-by-field
// comparison so a future field that lands on the binding follows the same
// shape.
func TestWorkerEvidenceBindingMismatchOnPacketDigest(t *testing.T) {
	t.Parallel()
	cases := workerEvidenceVectorCases(t)
	var dispatchCase *workerEvidenceVectorCase
	for i := range cases {
		if cases[i].Verb == WorkerEvidenceVerbDispatch {
			dispatchCase = &cases[i]
			break
		}
	}
	if dispatchCase == nil {
		t.Fatal("shared worker evidence vector declares no dispatch case")
	}
	matched := WorkerEvidenceAssertion{
		ClientRef: dispatchCase.Assertion.ClientRef, Verb: dispatchCase.Assertion.Verb,
		WorkID: dispatchCase.Assertion.WorkID, AttemptID: dispatchCase.Assertion.AttemptID,
		LaneID:     dispatchCase.Assertion.LaneID,
		LaneDigest: dispatchCase.Assertion.LaneDigest, ReadbackModel: dispatchCase.Assertion.ReadbackModel,
		HostProvenanceDigest: dispatchCase.Assertion.HostProvenanceDigest,
		PacketDigest:         dispatchCase.Assertion.PacketDigest,
	}
	binding := WorkerEvidenceBinding{
		Verb: matched.Verb, WorkID: matched.WorkID, AttemptID: matched.AttemptID,
		LaneID: matched.LaneID, LaneDigest: matched.LaneDigest,
		ReadbackModel: matched.ReadbackModel, HostProvenanceDigest: matched.HostProvenanceDigest,
		PacketDigest: matched.PacketDigest,
	}
	if !workerEvidenceMatchesBinding(matched, binding) {
		t.Fatal("matching binding refused the assertion")
	}
	divergent := matched
	divergent.PacketDigest = "sha256:" + strings.Repeat("e", 64)
	if workerEvidenceMatchesBinding(divergent, binding) {
		t.Fatal("binding accepted an assertion with a divergent packet digest")
	}
	// Sanity: complete / fail also bind empty packet_digest, so the
	// comparison's empty-on-both-sides path stays open for those verbs.
	var completeCase *workerEvidenceVectorCase
	for i := range cases {
		if cases[i].Verb == WorkerEvidenceVerbComplete {
			completeCase = &cases[i]
			break
		}
	}
	if completeCase == nil {
		t.Fatal("shared worker evidence vector declares no complete case")
	}
	complete := WorkerEvidenceAssertion{
		ClientRef: completeCase.Assertion.ClientRef, Verb: completeCase.Assertion.Verb,
		WorkID: completeCase.Assertion.WorkID, AttemptID: completeCase.Assertion.AttemptID,
		LaneID:     completeCase.Assertion.LaneID,
		LaneDigest: completeCase.Assertion.LaneDigest, ReadbackModel: completeCase.Assertion.ReadbackModel,
		PacketDigest: completeCase.Assertion.PacketDigest,
	}
	completeBinding := WorkerEvidenceBinding{
		Verb: complete.Verb, WorkID: complete.WorkID, AttemptID: complete.AttemptID,
		LaneID: complete.LaneID, LaneDigest: complete.LaneDigest,
		ReadbackModel: complete.ReadbackModel, PacketDigest: complete.PacketDigest,
	}
	if !workerEvidenceMatchesBinding(complete, completeBinding) {
		t.Fatal("complete binding refused an empty-packet-digest assertion")
	}
}

// workerEvidenceAuthClientRef is the trusted client the v2 authentication
// fixtures register. Its private key stays in the test, unlike
// testClientRegistration, which discards it.
const workerEvidenceAuthClientRef = "worker-evidence-test-client"

// workerEvidenceAssertionEnvelope is the assertion wire shape a CLI request
// carries. The tests build and decode it
// as JSON so every authentication behavior is exercised through the same
// decode path the CLI uses.
type workerEvidenceAssertionEnvelope struct {
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
	Signature            string `json:"signature"`
}

// workerEvidenceV2Envelope bases a fixture assertion on the shared vector's
// dispatch case — the verb whose binding populates the full field set — and
// restamps it for the fixture clock.
func workerEvidenceV2Envelope(t *testing.T, nonce string) workerEvidenceAssertionEnvelope {
	t.Helper()
	cases := workerEvidenceVectorCases(t)
	var dispatch *workerEvidenceVectorCase
	for i := range cases {
		if cases[i].Verb == WorkerEvidenceVerbDispatch {
			dispatch = &cases[i]
			break
		}
	}
	if dispatch == nil {
		t.Fatal("shared worker evidence vector declares no dispatch case")
	}
	value := dispatch.Assertion
	return workerEvidenceAssertionEnvelope{
		ClientRef:            workerEvidenceAuthClientRef,
		Verb:                 value.Verb,
		WorkID:               value.WorkID,
		AttemptID:            value.AttemptID,
		LaneID:               value.LaneID,
		LaneDigest:           value.LaneDigest,
		ReadbackModel:        value.ReadbackModel,
		FailureKind:          value.FailureKind,
		HostProvenanceDigest: value.HostProvenanceDigest,
		PacketDigest:         value.PacketDigest,
		IssuedAt:             fixedTime().Format(time.RFC3339Nano),
		Nonce:                nonce,
	}
}

// decodeWorkerEvidenceEnvelope decodes the envelope into the production
// assertion type, the same decode the CLI performs: unknown v2 members are
// ignored until production grows them.
func decodeWorkerEvidenceEnvelope(t *testing.T, env workerEvidenceAssertionEnvelope, assertion *WorkerEvidenceAssertion) {
	t.Helper()
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, assertion); err != nil {
		t.Fatal(err)
	}
}

// workerEvidenceBindingEnvelope restates the envelope identity as the
// binding the caller passes, with the version semantics phase 1 adds: a
// binding is v1 when Version is absent and v2 when it carries 2 plus the
// observation digest.
func workerEvidenceBindingEnvelope(env workerEvidenceAssertionEnvelope, version int, observationDigest string) map[string]any {
	return map[string]any{
		"Verb": env.Verb, "WorkID": env.WorkID, "AttemptID": env.AttemptID,
		"LaneID": env.LaneID, "LaneDigest": env.LaneDigest,
		"ReadbackModel": env.ReadbackModel, "FailureKind": env.FailureKind,
		"HostProvenanceDigest": env.HostProvenanceDigest, "PacketDigest": env.PacketDigest,
		"Version": version, "ObservationDigest": observationDigest,
	}
}

func decodeWorkerEvidenceBinding(t *testing.T, value map[string]any) WorkerEvidenceBinding {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var binding WorkerEvidenceBinding
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	return binding
}

// signDecodedCanonicalBytes signs whatever production canonicalizes for the
// decoded envelope: the v2 format for a version 2 envelope and the released
// v1 bytes otherwise.
// The refusal tests under it refuse at validation, before the signature is
// checked, so the signer only has to produce a well-formed assertion.
func signDecodedCanonicalBytes(t *testing.T, key ed25519.PrivateKey, env workerEvidenceAssertionEnvelope) string {
	t.Helper()
	var assertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, env, &assertion)
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, CanonicalWorkerEvidenceAssertion(assertion)))
}

// signWorkerEvidenceV2CanonicalBytes signs the approved phase-1 v2 byte
// format: prefix worker-evidence-v2\0, the v1 named fields in v1 order with
// observation_digest inserted before issued_at, each field encoded as
// name=<UTF-8 byte length>:<value>|. The acceptance test's signature
// agreement is what pins production to these exact bytes.
func signWorkerEvidenceV2CanonicalBytes(t *testing.T, key ed25519.PrivateKey, env workerEvidenceAssertionEnvelope) string {
	t.Helper()
	fields := []struct{ name, value string }{
		{"client_ref", env.ClientRef},
		{"verb", env.Verb},
		{"work_id", env.WorkID},
		{"attempt_id", env.AttemptID},
		{"lane_id", env.LaneID},
		{"lane_digest", env.LaneDigest},
		{"readback_model", env.ReadbackModel},
		{"failure_kind", env.FailureKind},
		{"host_provenance_digest", env.HostProvenanceDigest},
		{"packet_digest", env.PacketDigest},
		{"observation_digest", env.ObservationDigest},
		{"issued_at", env.IssuedAt},
		{"nonce", env.Nonce},
	}
	var b strings.Builder
	b.WriteString("worker-evidence-v2\x00")
	for _, field := range fields {
		b.WriteString(field.name)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len(field.value)))
		b.WriteByte(':')
		b.WriteString(field.value)
		b.WriteByte('|')
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(b.String())))
}

// registerHeldKeyWorkerEvidenceClient registers a trusted worker-evidence
// client whose private key the test holds.
func registerHeldKeyWorkerEvidenceClient(t *testing.T, service *Service) ed25519.PrivateKey {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterTrustedClient(context.Background(), ClientRegistration{
		ClientRef: workerEvidenceAuthClientRef,
		KeyID:     "key-con787-evidence",
		PublicKey: publicKey,
		Policy: TrustedClientPolicy{
			PrincipalRef: "con787-evidence-principal",
			Capabilities: []Capability{CapabilityWorkerEvidence},
			ProductScope: []string{"product-1"},
			ProjectScope: []string{"project-1"},
			AgentScope:   testFixtureAgents,
		},
	}); err != nil {
		t.Fatal(err)
	}
	return privateKey
}

// validateWorkerEvidenceAuthTx runs one authentication attempt in its own
// transaction and reports the validator's outcome.
func validateWorkerEvidenceAuthTx(t *testing.T, s *store.Store, service *Service, assertion WorkerEvidenceAssertion, binding WorkerEvidenceBinding) (string, error) {
	t.Helper()
	var principal string
	var validateErr error
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		principal, validateErr = service.ValidateWorkerEvidenceAssertionTx(context.Background(), tx, assertion, binding)
		return nil
	}); err != nil {
		t.Fatalf("worker evidence transaction failed: %v", err)
	}
	return principal, validateErr
}

// TestWorkerEvidenceV2AuthenticatesTheSignedObservationDigest is the
// phase-1 acceptance test. A version 2 assertion signed over the v2
// canonical bytes authenticates only when the caller's binding carries
// version 2 and the same digest, refuses when the digest is restated after
// signing, and refuses against a v1 binding: the signature covers the
// digest, and the binding comparison carries the version semantics.
func TestWorkerEvidenceV2AuthenticatesTheSignedObservationDigest(t *testing.T) {
	t.Parallel()
	s, service, _, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	key := registerHeldKeyWorkerEvidenceClient(t, service)
	digest := "sha256:" + strings.Repeat("a", 64)

	env := workerEvidenceV2Envelope(t, "nonce-con787-v2-accept-0001")
	env.Version = 2
	env.ObservationDigest = digest
	env.Signature = signWorkerEvidenceV2CanonicalBytes(t, key, env)
	var assertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, env, &assertion)
	principal, err := validateWorkerEvidenceAuthTx(t, s, service, assertion, decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(env, 2, digest)))
	if err != nil {
		t.Fatalf("v2 assertion with a bound observation digest refused: %v", err)
	}
	if principal != "con787-evidence-principal" {
		t.Fatalf("principal = %q, want the verified worker-evidence client", principal)
	}

	restated := workerEvidenceV2Envelope(t, "nonce-con787-v2-mutated-01")
	restated.Version = 2
	restated.ObservationDigest = digest
	restated.Signature = signWorkerEvidenceV2CanonicalBytes(t, key, restated)
	restated.ObservationDigest = "sha256:" + strings.Repeat("b", 64)
	var restatedAssertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, restated, &restatedAssertion)
	if _, err := validateWorkerEvidenceAuthTx(t, s, service, restatedAssertion, decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(restated, 2, restated.ObservationDigest))); err == nil {
		t.Fatal("v2 assertion accepted with an observation digest restated after signing")
	}

	unbound := workerEvidenceV2Envelope(t, "nonce-con787-v2-v1bind-01")
	unbound.Version = 2
	unbound.ObservationDigest = digest
	unbound.Signature = signWorkerEvidenceV2CanonicalBytes(t, key, unbound)
	var unboundAssertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, unbound, &unboundAssertion)
	if _, err := validateWorkerEvidenceAuthTx(t, s, service, unboundAssertion, decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(unbound, 0, ""))); err == nil {
		t.Fatal("v2 assertion accepted against a binding without version semantics")
	}
}

// TestWorkerEvidenceV2RefusesAMissingObservationDigest pins the v2 shape
// rule: version 2 without a signed observation digest is malformed and
// refuses at validation, before the signature or the binding is consulted.
func TestWorkerEvidenceV2RefusesAMissingObservationDigest(t *testing.T) {
	t.Parallel()
	s, service, _, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	key := registerHeldKeyWorkerEvidenceClient(t, service)

	env := workerEvidenceV2Envelope(t, "nonce-con787-v2-nodigest1")
	env.Version = 2
	env.Signature = signDecodedCanonicalBytes(t, key, env)
	var assertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, env, &assertion)
	if _, err := validateWorkerEvidenceAuthTx(t, s, service, assertion, decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(env, 0, ""))); err == nil {
		t.Fatal("version 2 without an observation digest was accepted")
	}
}

// TestWorkerEvidenceV1RefusesAnObservationDigest pins the v1 byte-exactness
// rule from the other side: a versionless assertion carrying an observation
// digest has no signed v1 slot for it and must refuse rather than silently
// ignoring the member.
func TestWorkerEvidenceV1RefusesAnObservationDigest(t *testing.T) {
	t.Parallel()
	s, service, _, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	key := registerHeldKeyWorkerEvidenceClient(t, service)

	env := workerEvidenceV2Envelope(t, "nonce-con787-v1-digest-01")
	env.ObservationDigest = "sha256:" + strings.Repeat("c", 64)
	env.Signature = signDecodedCanonicalBytes(t, key, env)
	var assertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, env, &assertion)
	if _, err := validateWorkerEvidenceAuthTx(t, s, service, assertion, decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(env, 0, ""))); err == nil {
		t.Fatal("a versionless assertion carrying an observation digest was accepted")
	}
}

// TestWorkerEvidenceRefusesAnUnknownAssertionVersion pins forward safety: a
// version this build does not know refuses even with a signature over the
// v1 bytes, so a newer producer cannot downgrade itself past a stricter
// core.
func TestWorkerEvidenceRefusesAnUnknownAssertionVersion(t *testing.T) {
	t.Parallel()
	s, service, _, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	key := registerHeldKeyWorkerEvidenceClient(t, service)

	env := workerEvidenceV2Envelope(t, "nonce-con787-v3-unknown1")
	env.Version = 3
	env.Signature = signDecodedCanonicalBytes(t, key, env)
	var assertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, env, &assertion)
	if _, err := validateWorkerEvidenceAuthTx(t, s, service, assertion, decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(env, 0, ""))); err == nil {
		t.Fatal("unknown assertion version 3 was accepted")
	}
}

// TestWorkerEvidenceV1PathStaysOpenForVersionlessAssertions is the phase-1
// guard, green on baseline by construction: a versionless, digest-free
// assertion authenticates exactly as before, so adding v2 cannot disturb
// the released v1 writes the shared vector pins byte for byte.
func TestWorkerEvidenceV1PathStaysOpenForVersionlessAssertions(t *testing.T) {
	t.Parallel()
	s, service, _, _ := mutationDispatchFixture(t, []Capability{"product_read"})
	key := registerHeldKeyWorkerEvidenceClient(t, service)

	env := workerEvidenceV2Envelope(t, "nonce-con787-v1-open-0001")
	env.Signature = signDecodedCanonicalBytes(t, key, env)
	var assertion WorkerEvidenceAssertion
	decodeWorkerEvidenceEnvelope(t, env, &assertion)
	principal, err := validateWorkerEvidenceAuthTx(t, s, service, assertion, decodeWorkerEvidenceBinding(t, workerEvidenceBindingEnvelope(env, 0, "")))
	if err != nil {
		t.Fatalf("versionless v1 assertion refused: %v", err)
	}
	if principal != "con787-evidence-principal" {
		t.Fatalf("principal = %q, want the verified worker-evidence client", principal)
	}
}
