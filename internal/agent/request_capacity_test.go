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
// from one derived action input: the call envelope over the writer
// population and first-run admissions below state, then the four-member
// request wrapper. All three request derivations share it.
func invokeRequestWireBytes(t *testing.T, input int) int {
	t.Helper()
	// The adapter writer is the population this derivation scopes: it
	// copies both path values from the host session readback — its only
	// writer-side check is non-empty — composes request_id from the host
	// session and message IDs, sends client_ref from the client
	// environment (default "opencode") and principal_ref empty, names
	// agent_ref as the host agent, echoes the scope watermark
	// project-resolve returned (checked non-empty), pins manifest_digest
	// to the release constant, and omits host_assertion_digest, which a
	// mutation defaults to a digest of the same 71-scalar shape.
	//
	// First-run admissions bound that population further: client_ref must
	// resolve to a trusted client record (registration's Go guard admits
	// any non-empty ref; the column CHECK bounds only the pre-NUL
	// prefix) and principal_ref must be empty or equal the record's
	// principal, which registration bounds to 1..128 bytes in Go;
	// agent_ref, ambient_project_id, and selected_product_id must sit
	// inside the client's agent, Project, and Product scopes, whose
	// members registration holds to 1..128 bytes; manifest_digest must
	// equal the agentcontract.ManifestDigest constant; a workflow
	// action's actor write guards all four actor references at 128 bytes
	// (workflowString) and new references at 2..128 (ValidReference); and
	// workflow actions refuse a scope_version that mismatches the minted
	// scope watermark, while a read refreshes a stale one with a warning.
	// A replayed mutation still re-runs identity authorization and
	// re-authorizes its recorded scope, but skips the actor write; a
	// replay answered before the scope check also skips the scope_version
	// check.
	//
	// The figures below therefore state this derivation's own envelope
	// population, not declared maxima: 4096 code points per path value —
	// the release platform's PATH_MAX order — and 128 per
	// identity-bearing member, at the six-byte worst case per scalar.
	// DecodeInvokeRequest enforces no per-field envelope bound, and the
	// output definition that last declared a 4096-code-point session-path
	// figure is retired. A member past this scope stays
	// transport-admitted; the one transport bound it faces is the
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
		// The two the writer carries — the pinned manifest constant, the
		// echoed scope watermark — have the "sha256:" plus 64 lowercase
		// hex shape, 71 ASCII scalars with nothing the encoder escapes;
		// host_assertion_digest, which the writer omits, rides the same
		// one-byte scope on the strength of the mutation default's shape.
		// The cost is exactly one byte per code point, quotes included,
		// and the 130-byte figure keeps headroom. Every other string here
		// keeps the six-byte figure.
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
