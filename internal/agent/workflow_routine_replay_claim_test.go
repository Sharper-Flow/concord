package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// PURE CLASSIFICATION tests for historicalRoutineClaimConsent. Every case
// classifies one synthetic store.Event by the frozen source-version approval
// rules its writer admitted it under. These tests prove the source rule alone:
// they are not real consumed consent, not upgraded cached dispatch evidence,
// and they fabricate no approval, receipt, or projection row. The real cache
// behavior stays with the existing store source-v1 fold/rebuild tests and the
// live routine cache tests.
//
// The rules under test are the historical writer rules the current folds
// still apply to recorded source-version-1 payloads: the overlap fold required
// a plan-layer approval reference for every resolution kind except depends_on
// (workflow_domain_overlap.go), and the delivery-correction fold required the
// complete approval binding with the recorded operator attribution
// (workflow_delivery_correction.go). The current v2 classes carry no binding,
// so they never prove historical consent.
func TestHistoricalRoutineClaimConsentPureClassification(t *testing.T) {
	t.Parallel()
	const legacyRef = "approval:legacy-consumed-binding"
	const legacyDigest = "sha256:9a7152d67e83438f2a4c9be6193c2f58a4d21ba5f33e94b3a4c1d70fbaf2f21c"
	legacyCorrectionPayload := func(mutations map[string]string) string {
		payload := map[string]string{
			"approval_ref":              legacyRef,
			"approval_operation_digest": legacyDigest,
			"approval_scope_json":       `{"work_ids":["work-1"]}`,
			"approval_versions_json":    `{"work":4}`,
			"approval_consequence":      "relation",
		}
		for field, value := range mutations {
			payload[field] = value
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	for _, testCase := range []struct {
		name           string
		event          store.Event
		resolutionKind string
		wantConsent    bool
	}{
		{
			name: "v1 overlap compatible_with with the required reference proves historical consent",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 1,
				Payload: json.RawMessage(`{"resolution_kind":"compatible_with","approval_ref":"` + legacyRef + `"}`)},
			resolutionKind: store.ResolutionCompatibleWith,
			wantConsent:    true,
		},
		{
			name: "v1 overlap blocks with the required reference proves historical consent",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 1,
				Payload: json.RawMessage(`{"resolution_kind":"blocks","approval_ref":"` + legacyRef + `"}`)},
			resolutionKind: store.ResolutionBlocks,
			wantConsent:    true,
		},
		{
			name: "v1 overlap depends_on carries no historical consent exemption even with a reference",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 1,
				Payload: json.RawMessage(`{"resolution_kind":"depends_on","approval_ref":"` + legacyRef + `"}`)},
			resolutionKind: store.ResolutionDependsOn,
			wantConsent:    false,
		},
		{
			name: "v1 overlap compatible_with without a reference proves no consent",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 1,
				Payload: json.RawMessage(`{"resolution_kind":"compatible_with","approval_ref":""}`)},
			resolutionKind: store.ResolutionCompatibleWith,
			wantConsent:    false,
		},
		{
			name: "current v2 overlap event carries no binding and never proves consent",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 2,
				Payload: json.RawMessage(`{"resolution_kind":"compatible_with","approval_ref":""}`)},
			resolutionKind: store.ResolutionCompatibleWith,
			wantConsent:    false,
		},
		{
			name: "the stored source version decides, so a v2 payload with a reference still proves no consent",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 2,
				Payload: json.RawMessage(`{"resolution_kind":"compatible_with","approval_ref":"` + legacyRef + `"}`)},
			resolutionKind: store.ResolutionCompatibleWith,
			wantConsent:    false,
		},
		{
			name: "an unrelated event kind cannot waive a routine refusal",
			event: store.Event{Kind: store.WorkflowActorRecorded, PayloadVersion: 2,
				Payload: json.RawMessage(`{"actor_ref":"agent:someone"}`)},
			resolutionKind: store.ResolutionCompatibleWith,
			wantConsent:    false,
		},
		{
			name: "an unrelated v1 event with complete approval-looking fields proves no consent",
			event: store.Event{Kind: store.WorkflowActorRecorded, PayloadVersion: 1,
				Actor:   "agent:legacy-coordinator",
				Payload: json.RawMessage(legacyCorrectionPayload(nil))},
			resolutionKind: store.ResolutionCompatibleWith,
			wantConsent:    false,
		},
		{
			name: "v1 delivery correction with the complete binding and actor proves historical consent",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 1,
				Actor:   "agent:legacy-coordinator",
				Payload: json.RawMessage(legacyCorrectionPayload(nil))},
			wantConsent: true,
		},
		{
			name: "v1 delivery correction without the recorded attribution proves no consent",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 1,
				Payload: json.RawMessage(legacyCorrectionPayload(nil))},
			wantConsent: false,
		},
		{
			name: "v1 delivery correction missing its approval reference proves no consent",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 1,
				Actor:   "agent:legacy-coordinator",
				Payload: json.RawMessage(legacyCorrectionPayload(map[string]string{"approval_ref": ""}))},
			wantConsent: false,
		},
		{
			name: "v1 delivery correction missing its operation digest proves no consent",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 1,
				Actor:   "agent:legacy-coordinator",
				Payload: json.RawMessage(legacyCorrectionPayload(map[string]string{"approval_operation_digest": ""}))},
			wantConsent: false,
		},
		{
			name: "v1 delivery correction missing its scope binding proves no consent",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 1,
				Actor:   "agent:legacy-coordinator",
				Payload: json.RawMessage(legacyCorrectionPayload(map[string]string{"approval_scope_json": ""}))},
			wantConsent: false,
		},
		{
			name: "v1 delivery correction missing its version pin proves no consent",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 1,
				Actor:   "agent:legacy-coordinator",
				Payload: json.RawMessage(legacyCorrectionPayload(map[string]string{"approval_versions_json": ""}))},
			wantConsent: false,
		},
		{
			name: "v1 delivery correction missing its consequence proves no consent",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 1,
				Actor:   "agent:legacy-coordinator",
				Payload: json.RawMessage(legacyCorrectionPayload(map[string]string{"approval_consequence": ""}))},
			wantConsent: false,
		},
		{
			name: "current v2 delivery correction carries no binding and never proves consent",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 2,
				Actor:   "agent:current-coordinator",
				Payload: json.RawMessage(legacyCorrectionPayload(nil))},
			wantConsent: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			consent, err := historicalRoutineClaimConsent(testCase.event, testCase.resolutionKind)
			if err != nil {
				t.Fatalf("a well-formed event must classify without a corrupt-event error, got %v", err)
			}
			if consent != testCase.wantConsent {
				t.Fatalf("historical consent = %t, want %t for %s event at payload version %d",
					consent, testCase.wantConsent, testCase.event.Kind, testCase.event.PayloadVersion)
			}
		})
	}
}

// PURE CLASSIFICATION: a stored event whose bytes no fold could have admitted
// is corrupt, and the classifier must report it instead of answering either
// consent question from broken bytes.
func TestHistoricalRoutineClaimConsentCorruptEventRefusesClosed(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name  string
		event store.Event
	}{
		{
			name:  "unreadable overlap payload",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 1, Payload: json.RawMessage(`{"resolution_kind":`)},
		},
		{
			name:  "empty overlap payload",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 1},
		},
		{
			name: "overlap payload that disagrees with the receipt's resolution kind",
			event: store.Event{Kind: store.WorkflowOverlapResolved, PayloadVersion: 1,
				Payload: json.RawMessage(`{"resolution_kind":"blocks","approval_ref":"approval:legacy-consumed-binding"}`)},
		},
		{
			name:  "unreadable delivery correction payload",
			event: store.Event{Kind: store.WorkflowDeliveryCorrected, PayloadVersion: 1, Payload: json.RawMessage(`{"approval_ref":`)},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			consent, err := historicalRoutineClaimConsent(testCase.event, store.ResolutionCompatibleWith)
			if err == nil {
				t.Fatalf("a corrupt receipt event classified consent=%t with no error, want a typed corrupt-event failure", consent)
			}
			if consent {
				t.Fatal("a corrupt receipt event must never classify as consent")
			}
		})
	}
}
