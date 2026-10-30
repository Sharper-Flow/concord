package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// invokeRequestWireBytes composes the whole invoke request's wire figure
// from one derived action input: the call envelope's host-owned identities
// under their downstream owners' bounds, then the four-member request
// wrapper. Both request derivations share it.
func invokeRequestWireBytes(t *testing.T, input int) int {
	t.Helper()
	// The call envelope's host-owned identities, each with its downstream
	// owner: the reference grammar bounds the request and actor identities
	// at 128 bytes (768 escaped bytes worst case in quotes), the contract's
	// absolute_path def bounds the session paths at 4096 code points, the
	// digest def's ASCII-proven sha256 grammar bounds the three digests at
	// one byte per scalar.
	envelope := map[string]int{
		"schema_version":      len(`"1.0"`),
		"request_id":          schemawire.JSONStringWireBytes(128),
		"client_ref":          schemawire.JSONStringWireBytes(128),
		"principal_ref":       schemawire.JSONStringWireBytes(128),
		"session_ref":         schemawire.JSONStringWireBytes(128),
		"agent_ref":           schemawire.JSONStringWireBytes(128),
		"directory":           schemawire.JSONStringWireBytes(4096),
		"worktree":            schemawire.JSONStringWireBytes(4096),
		"ambient_project_id":  schemawire.JSONStringWireBytes(128),
		"selected_product_id": schemawire.JSONStringWireBytes(128),
		// The three digests are this derivation's only one-byte population:
		// their owner $defs/digest anchors an alphabet of hex digits and
		// colon — fixed-ASCII, fixed-syntax, no escapable scalar — so the
		// encoder's cost is exactly one byte per code point, quotes
		// included. Every other string here keeps the six-byte figure.
		"scope_version":         2 + 128,
		"manifest_digest":       2 + 128,
		"host_assertion_digest": 2 + 128,
	}
	envelopeBytes := 2
	first := true
	for name, bound := range envelope {
		if !first {
			envelopeBytes++
		}
		first = false
		envelopeBytes += len(schemawire.JSONString(name)) + 1 + bound
	}
	request := 2
	first = true
	for _, name := range []string{"call_envelope", "tool", "operation", "input"} {
		if !first {
			request++
		}
		first = false
		request += len(schemawire.JSONString(name)) + 1
		switch name {
		case "call_envelope":
			request += envelopeBytes
		case "tool", "operation":
			request += schemawire.JSONStringWireBytes(128)
		default:
			request += input
		}
	}
	return request
}

// Review's declared maximum composes with the authenticated host envelope.
func TestRequestBudgetDerivesFromDeclaredMaxima(t *testing.T) {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	if store.WorkContextActionFindingsMax != 16 {
		t.Fatalf("WorkContextActionFindingsMax = %d, want 16; the finding-reference derivation rests on it", store.WorkContextActionFindingsMax)
	}
	// Quotes count: the reference serializes inside quotation marks.
	findingRefMax := 2 + len("finding:") + 19 + 1 + 2
	opt := schemawire.Options{
		Defs: defs,
		RefBounds: map[string]int{
			"worker_acceptance_oracle": store.OracleCombinedMaxBytes,
		},
		FindingRefMax: findingRefMax,
	}
	const variant = "work_transition_action_variant_record_review"
	if defs[variant] == nil {
		t.Fatal("record_review is absent from the generated contract; regenerate from the workflow owner")
	}
	input, err := schemawire.Max(defs[variant], variant, opt)
	if err != nil {
		t.Fatalf("the request derivation hit an unbudgeted member: %v", err)
	}
	// The call envelope's host-owned identities wrap the derived Review
	// input; the shared builder keeps this per-shape proof and the
	// widest-shape derivation on one construction.
	request := invokeRequestWireBytes(t, input)
	if request > MaxRequestBytes {
		t.Fatalf("the composed declared maxima need %d bytes, above the %d-byte request budget; derive the budget again", request, MaxRequestBytes)
	}
	if MaxEnvelopeBytes != 65536 || agentcontract.MaxResultEnvelopeBytes != 51200 {
		t.Fatalf("envelope caps moved: MaxEnvelopeBytes=%d MaxResultEnvelopeBytes=%d, want 65536 and 51200 unchanged", MaxEnvelopeBytes, agentcontract.MaxResultEnvelopeBytes)
	}
}

// The request transport refusal stays the whole-request decode refusal on
// its own named limit: past the bound the decode refuses with the exceeding
// diagnostic and no envelope is addressed; dispatch-failure typing
// (invalid_input through shapeInvokeFailure) is untouched by the size path.
func TestDecodeInvokeRequestRefusesTypedAndWhole(t *testing.T) {
	oversize := make([]byte, MaxRequestBytes+1)
	for i := range oversize {
		oversize[i] = ' '
	}
	_, _, err := DecodeInvokeRequest(oversize)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("request past the bound error = %v, want the whole-request size refusal", err)
	}
}
