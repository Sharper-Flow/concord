package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode"
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
		LaneVersion          int64  `json:"lane_version"`
		LaneDigest           string `json:"lane_digest"`
		ReadbackModel        string `json:"readback_model"`
		FailureKind          string `json:"failure_kind"`
		HostProvenanceDigest string `json:"host_provenance_digest"`
		PacketDigest         string `json:"packet_digest"`
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

// TestCanonicalWorkerEvidenceVector pins the signed byte sequence. The adapter
// mirrors this encoder in TypeScript; both read this vector, so a divergence
// that would let one side accept bytes the other never produced fails here
// rather than silently weakening the boundary.
func TestCanonicalWorkerEvidenceVector(t *testing.T) {
	cases := workerEvidenceVectorCases(t)
	if len(cases) == 0 {
		t.Fatal("shared worker evidence vector declares no cases")
	}
	for _, testCase := range cases {
		t.Run(testCase.Verb, func(t *testing.T) {
			value := testCase.Assertion
			assertion := WorkerEvidenceAssertion{
				ClientRef: value.ClientRef, Verb: value.Verb, WorkID: value.WorkID, AttemptID: value.AttemptID,
				LaneID: value.LaneID, LaneVersion: value.LaneVersion, LaneDigest: value.LaneDigest,
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

// TestWorkerEvidenceVectorCoversEveryVerbAndBindingField keeps the shared
// bound_fields declaration honest against this package. Every acceptable verb
// needs a case, every declared field must exist on the binding, and every
// binding field must be claimed by some verb — so a fourth verb or a tenth
// binding field cannot land on one side of the boundary alone.
func TestWorkerEvidenceVectorCoversEveryVerbAndBindingField(t *testing.T) {
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

	bindingFields := map[string]bool{}
	bindingType := reflect.TypeOf(WorkerEvidenceBinding{})
	for i := range bindingType.NumField() {
		bindingFields[snakeCase(bindingType.Field(i).Name)] = false
	}
	for _, testCase := range cases {
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
		LaneID: dispatchCase.Assertion.LaneID, LaneVersion: dispatchCase.Assertion.LaneVersion,
		LaneDigest: dispatchCase.Assertion.LaneDigest, ReadbackModel: dispatchCase.Assertion.ReadbackModel,
		HostProvenanceDigest: dispatchCase.Assertion.HostProvenanceDigest,
		PacketDigest:         dispatchCase.Assertion.PacketDigest,
	}
	binding := WorkerEvidenceBinding{
		Verb: matched.Verb, WorkID: matched.WorkID, AttemptID: matched.AttemptID,
		LaneID: matched.LaneID, LaneVersion: matched.LaneVersion, LaneDigest: matched.LaneDigest,
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
		LaneID: completeCase.Assertion.LaneID, LaneVersion: completeCase.Assertion.LaneVersion,
		LaneDigest: completeCase.Assertion.LaneDigest, ReadbackModel: completeCase.Assertion.ReadbackModel,
		PacketDigest: completeCase.Assertion.PacketDigest,
	}
	completeBinding := WorkerEvidenceBinding{
		Verb: complete.Verb, WorkID: complete.WorkID, AttemptID: complete.AttemptID,
		LaneID: complete.LaneID, LaneVersion: complete.LaneVersion, LaneDigest: complete.LaneDigest,
		ReadbackModel: complete.ReadbackModel, PacketDigest: complete.PacketDigest,
	}
	if !workerEvidenceMatchesBinding(complete, completeBinding) {
		t.Fatal("complete binding refused an empty-packet-digest assertion")
	}
}
