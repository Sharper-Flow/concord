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
	// The dispatch variant stays the widest composition the request budget's
	// rounding rests on, so the correction attribution input rides inside the
	// derived budget the dispatch derivation already proves.
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
	// The research-context packet3 identity rides the same
	// composition: its research closure sits inside the excluded work_context
	// member, so the promoted heads' dispatch identity derives identically
	// and the retained packet2 evidence stays.
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
	// The size-capable packet4 identity adds the recorded size members and
	// the caller exclusions inside worker_job; it derives its own part.
	packet4Opt := schemawire.Options{
		Defs: defs,
		PathBounds: map[string]int{
			"worker_packet4/inputs/worker_job/worker_packet4_worker_job/acceptance_oracle":                                                                                                                                                 store.OracleCombinedMaxBytes,
			"worker_packet4/inputs/worker_job/worker_packet4_worker_job/measurement_basis/worker_job_measurement_basis/base_commit":                                                                                                        71,
			"worker_packet4/inputs/worker_job/worker_packet4_worker_job/direct_caller_allowance/worker_job_direct_caller_allowance/law_bindings/items/worker_oracle_law_binding/source/work_context_reading_source_knowledge/content_hash": 71,
			"worker_packet4/inputs/design_record/workflow_design_record/recorded_at":                                                                                                                                                       32,
			"worker_packet4/inputs/proposal_record/workflow_proposal_record/recorded_at":                                                                                                                                                   32,
		},
		Excludes: map[string]bool{
			"worker_packet4/inputs/work_context": true,
			"worker_packet4/inputs/checkpoint":   true,
			"worker_packet4/inputs/correction":   true,
		},
		FindingRefMax: findingRefMax,
	}
	packet4Parts, err := schemawire.Max(defs["worker_packet4"], "worker_packet4", packet4Opt)
	if err != nil {
		t.Fatalf("the packet4 derivation hit an unbudgeted member: %v", err)
	}
	if packet3Parts != packetParts {
		t.Fatalf("the packet3 derivation = %d bytes, want the packet2 derivation %d", packet3Parts, packetParts)
	}
	dispatchOpt := schemawire.Options{
		Defs: defs,
		RefBounds: map[string]int{
			"worker_packet2": packetParts + store.DispatchContextAggregateMaxBytes - 1,
			"worker_packet3": packet3Parts + store.DispatchContextAggregateMaxBytes - 1,
			"worker_packet4": packet4Parts + store.DispatchContextAggregateMaxBytes - 1,
		},
		FindingRefMax: findingRefMax,
	}
	dispatch, err := schemawire.Max(defs["work_transition_action_variant_dispatch_worker"], "work_transition_action_variant_dispatch_worker", dispatchOpt)
	if err != nil {
		t.Fatalf("the dispatch derivation hit an unbudgeted member: %v", err)
	}
	if correction >= dispatch {
		t.Fatalf("the correction attribution composition %d bytes reaches the dispatch composition %d bytes; the dispatch input must stay the widest derivation the request rounding rests on", correction, dispatch)
	}
	if schemawire.NextPowerOfTwo(correction) >= MaxRequestBytes {
		t.Fatalf("the correction composition rounds to %d, at or above the %d-byte request budget; it must stay inside the rounding the dispatch derivation owns", schemawire.NextPowerOfTwo(correction), MaxRequestBytes)
	}
}
