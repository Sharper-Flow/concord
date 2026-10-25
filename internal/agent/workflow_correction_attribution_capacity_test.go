package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// TestRequestBudgetHoldsCorrectionAttributionVariant proves the
// optional failure_attribution correction input fits the derived request
// budget without a limit increase: the reject correction variant — every
// existing member at its declared maximum beside the observation block at
// the store's own 8192-byte bound — composes strictly under the dispatch
// variant whose composition the request rounding already holds. A missing
// generated definition names the regeneration instead of failing silently.
func TestRequestBudgetHoldsCorrectionAttributionVariant(t *testing.T) {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	if store.WorkContextActionFindingsMax != 16 {
		t.Fatalf("WorkContextActionFindingsMax = %d, want 16; the finding-reference derivation rests on it", store.WorkContextActionFindingsMax)
	}
	findingRefMax := 2 + len("finding:") + 19 + 1 + 2
	opt := schemawire.Options{
		Defs: defs,
		RefBounds: map[string]int{
			// The observation block is bounded by the store's own core
			// bound, not by the generated schema — the same division the
			// acceptance oracle's writer bound takes.
			"worker_failure_observation": store.WorkerFailureAttributionMaxBlockBytes,
		},
		FindingRefMax: findingRefMax,
	}
	correction := 0
	for _, name := range []string{"work_transition_action_variant_reject_worker_result", "work_transition_action_variant_request_correction"} {
		composed, err := schemawire.Max(defs[name], name, opt)
		if err != nil {
			t.Fatalf("the %s derivation hit an unbudgeted member: %v", name, err)
		}
		if composed > MaxRequestBytes {
			t.Fatalf("the %s composed declared maxima need %d bytes, above the %d-byte request budget", name, composed, MaxRequestBytes)
		}
		if composed > correction {
			correction = composed
		}
	}
	// The dispatch variant stays narrower than the widest admitted action
	// shape the shared ceilings' rounding derives from; this structural
	// check keeps the correction attribution input inside the dispatch
	// composition that the per-shape request proof carries. Every packet
	// member of the current packet derives from the same writer-bound
	// inventory (CD-0242 D14); the checks-repair metadata is
	// writer-bounded by WorkerJobChecksRepairMaxBytes exactly as the
	// acceptance oracle is.
	packet := derivedPacketFigure(t, defs, findingRefMax)
	dispatchOpt := schemawire.Options{
		Defs: defs,
		RefBounds: map[string]int{
			"worker_packet": packet + store.DispatchContextAggregateMaxBytes - 1,
		},
		FindingRefMax: findingRefMax,
	}
	dispatch, err := schemawire.Max(defs["work_transition_action_variant_dispatch_worker"], "work_transition_action_variant_dispatch_worker", dispatchOpt)
	if err != nil {
		t.Fatalf("the dispatch derivation hit an unbudgeted member: %v", err)
	}
	if correction >= dispatch {
		t.Fatalf("the correction attribution composition %d bytes reaches the dispatch composition %d bytes; the correction attribution input must stay narrower than the dispatch composition this check pins", correction, dispatch)
	}
	if schemawire.NextPowerOfTwo(correction) >= MaxRequestBytes {
		t.Fatalf("the correction composition rounds to %d, at or above the %d-byte request budget; it must stay inside the rounding the dispatch derivation owns", schemawire.NextPowerOfTwo(correction), MaxRequestBytes)
	}
}
