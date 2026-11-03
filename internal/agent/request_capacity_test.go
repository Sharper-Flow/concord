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
// from one derived action input: the call envelope under the bounds its
// current owners enforce plus the derivation's population scope for the
// members no owner bounds, then the four-member request wrapper. All
// three request derivations share it.
func invokeRequestWireBytes(t *testing.T, input int) int {
	t.Helper()
	// The call envelope's members, each under the bound a current owner
	// enforces: client_ref must resolve to a trusted client record and
	// principal_ref must be empty or equal that record's principal (the
	// agent_clients CHECKs cap both at 128 characters); agent_ref,
	// ambient_project_id, and selected_product_id must sit inside the
	// client's agent, Project, and Product scopes, whose members
	// registration holds to 1..128 bytes; session_ref lands in the
	// workflow_actors admission on mutations (CHECK 2..128). Two digests
	// are owner-fixed at "sha256:" plus 64 lowercase hex: manifest_digest
	// must equal the agentcontract.ManifestDigest constant, and
	// scope_version is the store-minted scope watermark.
	//
	// Population scope, not declared maxima: nothing bounds the length of
	// directory, worktree, request_id, host_assertion_digest, or
	// session_ref on reads. The adapter copies both path values from the
	// host session readback — its only writer-side check is non-empty —
	// composes request_id from the host session and message IDs, and
	// omits host_assertion_digest, which a mutation defaults to a digest
	// of the same 71-scalar shape only when empty. DecodeInvokeRequest
	// enforces no per-field envelope bound, and the output definition
	// that last declared a 4096-code-point session-path figure is
	// retired. This derivation instead scopes its envelope population at
	// 4096 code points per path value — the release platform's PATH_MAX
	// order — and 128 per identity-bearing member, at the six-byte worst
	// case per scalar the figures below carry. A member past this scope
	// stays transport-admitted; the one bound it faces is the
	// whole-request refusal TestDecodeInvokeRequestRefusesTypedAndWhole
	// pins, unchanged.
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
		// The three digests are this derivation's only one-byte population.
		// Two are owner-fixed at "sha256:" plus 64 lowercase hex; the
		// adapter omits host_assertion_digest and a mutation defaults an
		// empty one to that same 71-scalar shape, so the population scope
		// takes it at one byte per scalar too. Fixed-ASCII, no escapable
		// scalar: the encoder's cost is exactly one byte per code point,
		// quotes included, and the 130-byte figure keeps headroom. Every
		// other string here keeps the six-byte figure.
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
	// The call envelope wraps the derived Review input; the shared
	// builder keeps this per-shape proof and the widest-shape derivation
	// on one construction.
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
