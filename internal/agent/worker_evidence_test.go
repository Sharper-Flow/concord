package agent

import (
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
				HostProvenanceDigest: value.HostProvenanceDigest, IssuedAt: value.IssuedAt, Nonce: value.Nonce,
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
