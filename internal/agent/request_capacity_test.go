package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// The request transport budget is derived, not guessed: this test composes
// the declared maxima of the whole invoke request through the shared
// test-support witness — the closed dispatch action input with its worker
// packet, the call envelope's host-owned identities under their downstream
// owners' bounds — and proves MaxRequestBytes holds the composition. A later
// packet or envelope increase fails here instead of at the transport, and
// any field without a boundable owner fails the derivation with its name.

// TestRequestBudgetDerivesFromDeclaredMaxima pins the request transport
// limit to the composed declared maxima of one dispatch invoke request.
// Call-envelope field bounds cite their downstream owners: the four
// authenticated actor references and the request id ride the shared bounded
// reference grammar (workflow_registry.go ValidReference, 2-128 bytes, no
// whitespace), the session directory and worktree ride the generated
// contract's absolute_path maximum, and the approval rides
// $defs/approval.
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
	packetOpt := schemawire.Options{
		Defs: defs,
		PathBounds: map[string]int{
			"worker_packet2/inputs/worker_job/worker_packet_worker_job/acceptance_oracle": store.OracleCombinedMaxBytes,
			"worker_packet2/inputs/design_record/workflow_design_record/recorded_at":      32,
			"worker_packet2/inputs/proposal_record/workflow_proposal_record/recorded_at":  32,
		},
		Excludes: map[string]bool{
			"worker_packet2/inputs/work_context": true,
			"worker_packet2/inputs/checkpoint":   true,
			"worker_packet2/inputs/correction":   true,
		},
		FindingRefMax: findingRefMax,
	}
	packetParts, err := schemawire.Max(defs["worker_packet2"], "worker_packet2", packetOpt)
	if err != nil {
		t.Fatalf("the packet derivation hit an unbudgeted member: %v", err)
	}
	// The research-context packet3 identity (CON-911) rides the same
	// composition: its research closure sits inside the excluded work_context
	// member, so the promoted heads' dispatch identity derives identically
	// and the request bound is unchanged. The packet2 evidence stays.
	packet3Opt := schemawire.Options{
		Defs: defs,
		PathBounds: map[string]int{
			"worker_packet3/inputs/worker_job/worker_packet_worker_job/acceptance_oracle": store.OracleCombinedMaxBytes,
			"worker_packet3/inputs/design_record/workflow_design_record/recorded_at":      32,
			"worker_packet3/inputs/proposal_record/workflow_proposal_record/recorded_at":  32,
		},
		Excludes: map[string]bool{
			"worker_packet3/inputs/work_context": true,
			"worker_packet3/inputs/checkpoint":   true,
			"worker_packet3/inputs/correction":   true,
		},
		FindingRefMax: findingRefMax,
	}
	packet3Parts, err := schemawire.Max(defs["worker_packet3"], "worker_packet3", packet3Opt)
	if err != nil {
		t.Fatalf("the packet3 derivation hit an unbudgeted member: %v", err)
	}
	if packet3Parts != packetParts {
		t.Fatalf("the packet3 derivation = %d bytes, want the packet2 derivation %d", packet3Parts, packetParts)
	}
	packetForRequest := packetParts + store.DispatchContextAggregateMaxBytes - 1
	opt := schemawire.Options{
		Defs: defs,
		RefBounds: map[string]int{
			"worker_packet2": packetForRequest,
			"worker_packet3": packet3Parts + store.DispatchContextAggregateMaxBytes - 1,
		},
		FindingRefMax: findingRefMax,
	}
	input, err := schemawire.Max(defs["work_transition_action_variant_dispatch_worker"], "work_transition_action_variant_dispatch_worker", opt)
	if err != nil {
		t.Fatalf("the request derivation hit an unbudgeted member: %v", err)
	}
	// The call envelope's host-owned identities, each with its downstream
	// owner: the reference grammar bounds the request and actor identities
	// at 128 bytes (768 escaped bytes worst case in quotes), the contract's
	// absolute_path def bounds the session paths at 4096 code points, the
	// digest def's ASCII-proven sha256 grammar bounds the three digests at
	// one byte per scalar, and the approval rides $defs/approval.
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
		"host_approval_assertion": func() int {
			approval, approvalErr := schemawire.Max(defs["approval"], "approval", opt)
			if approvalErr != nil {
				t.Fatal(approvalErr)
			}
			return approval
		}(),
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
	if request > MaxRequestBytes {
		t.Fatalf("the composed declared maxima need %d bytes, above the %d-byte request budget; derive the budget again", request, MaxRequestBytes)
	}
	if schemawire.NextPowerOfTwo(request) != MaxRequestBytes {
		t.Fatalf("composed declared maxima = %d bytes, whose next power of two is %d; the request budget constant is %d and must stay the derivation's own rounding", request, schemawire.NextPowerOfTwo(request), MaxRequestBytes)
	}
	if MaxEnvelopeBytes != 65536 || MaxResultEnvelopeBytes != 51200 {
		t.Fatalf("envelope caps moved: MaxEnvelopeBytes=%d MaxResultEnvelopeBytes=%d, want 65536 and 51200 unchanged", MaxEnvelopeBytes, MaxResultEnvelopeBytes)
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
