package agent

import (
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// derivedRequestWireMaxBytes pins the derived full-invoke wire maximum of
// the current widest admitted action shape. The test owns the pin, not
// production: any growth in the widest shape fails this exact figure first,
// and the pin moves only by a deliberate re-derivation beside the ceilings'
// own next-power-of-two assertions.
const derivedRequestWireMaxBytes = 33517457

// The shared action ceilings derive from the widest admitted shape, not a
// shortlist: this test enumerates every admitted workflow-action variant the
// generated contract declares, derives each one's whole serialized maximum
// through the same walker and writer bounds the packet derivations use, and
// takes the widest action variant. Composing the full host call envelope
// around that variant is the one request derivation, and its next power of
// two is the one value both ceilings — the store's whole-action fields
// budget and the agent request transport limit, one aliased production
// constant — must equal. The store's split test proves the fields JSON
// figure separately. A future shape growth fails the exact wire pin first
// and requires a new derivation; an unknown unbounded member fails its walk
// with its exact path.
func TestWorkflowActionWidestShapeDerivesSharedCeilings(t *testing.T) {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	// Keep the optional Review input's contribution visible beside the wire
	// pin. The walker includes optional members and uses 2+6*maxLength even
	// for hex patterns; it does not narrow objects by conditional oneOf arms.
	// Candidate: 770+386+386 string bytes + 33 syntax bytes = 1575.
	// Criterion: 770+428 string bytes + 35 syntax bytes = 1233;
	// criteria: 8*1233 + 2 brackets + 7 commas = 9873.
	// Dependencies: 9873+428+5 value bytes + 67 syntax bytes = 10373.
	// Descriptor: 1575+10373 value bytes + 36 syntax bytes = 11984.
	// Its inputs member adds 14 key bytes, a colon, and a comma: 12000.
	dependencies := defs["worker_packet_review_dependencies"].(map[string]any)["properties"].(map[string]any)
	criteria := dependencies["criteria"].(map[string]any)
	for _, row := range []struct {
		name string
		node any
		want int
	}{
		{"candidate", defs["worker_packet_review_candidate"], 1575},
		{"criterion", criteria["items"], 1233},
		{"criteria", criteria, 9873},
		{"dependencies", defs["worker_packet_review_dependencies"], 10373},
		{"descriptor", defs["worker_packet_review_input"], 11984},
	} {
		got, err := schemawire.Max(row.node, row.name, schemawire.Options{Defs: defs})
		if err != nil {
			t.Fatal(err)
		}
		if got != row.want {
			t.Fatalf("Review %s = %d bytes, want the member-derived %d", row.name, got, row.want)
		}
		t.Logf("Review %s = %d bytes", row.name, got)
	}
	input, _ := defs["work_transition_action_input"].(map[string]any)
	anyOf, _ := input["anyOf"].([]any)
	if len(anyOf) == 0 {
		t.Fatal("the generated contract declares no admitted action variants")
	}
	var variants []string
	for _, branch := range anyOf {
		ref, _ := branch.(map[string]any)["$ref"].(string)
		name := ref[strings.LastIndex(ref, "/")+1:]
		if name == "" || defs[name] == nil {
			t.Fatalf("the admitted variant branch %v names no generated definition", branch)
		}
		variants = append(variants, name)
	}
	if store.WorkContextActionFindingsMax != 16 {
		t.Fatalf("WorkContextActionFindingsMax = %d, want 16; the finding-reference derivation rests on it", store.WorkContextActionFindingsMax)
	}
	// The packet identities and writer bounds are the shared ones: the
	// packet figures come from the common derivation the dispatch request
	// proof uses, and the union of writer-owned member bounds carries each
	// variant's prefix, so one options set serves every variant's walk.
	findingRefMax := 2 + len("finding:") + 19 + 1 + 2
	packet := derivedPacketFigure(t, defs, findingRefMax)
	digestWire := 2 + len("sha256:") + 64
	commitOIDWire := 2 + 64
	const recordJob = "work_transition_action_variant_record_worker_job"
	const recordContext = "work_transition_action_variant_record_work_context"
	lawSourceHash := "/direct_caller_allowance/worker_job_direct_caller_allowance/law_bindings/items/worker_oracle_law_binding/source/work_context_reading_source_knowledge/content_hash"
	opt := schemawire.Options{
		Defs: defs,
		RefBounds: map[string]int{
			"worker_acceptance_oracle":   store.OracleCombinedMaxBytes,
			"worker_failure_observation": store.WorkerFailureAttributionMaxBlockBytes,
			"worker_packet":              packet + store.DispatchContextAggregateMaxBytes - 1,
		},
		PathBounds: map[string]int{
			recordJob + "/fields/oneOf" + lawSourceHash: digestWire,
			recordContext + "/fields/required_reading/items/work_context_reading/source/oneOf/work_context_reading_source_repository_file/commit_oid": commitOIDWire,
			recordContext + "/fields/required_reading/items/work_context_reading/source/oneOf/work_context_reading_source_knowledge/content_hash":     digestWire,
		},
		FindingRefMax: findingRefMax,
	}
	widest, widestName := 0, ""
	for _, name := range variants {
		size, err := schemawire.Max(defs[name], name, opt)
		if err != nil {
			t.Fatalf("the %s derivation hit an unbudgeted member: %v", name, err)
		}
		if size > widest {
			widest, widestName = size, name
		}
	}
	if widestName == "" {
		t.Fatal("no admitted variant derived a figure")
	}
	// One derivation: the widest whole action variant rides the invoke
	// envelope, and its next power of two is the one figure both production
	// aliases must equal.
	request := invokeRequestWireBytes(t, defs, opt, widest)
	derived := schemawire.NextPowerOfTwo(request)
	if request != derivedRequestWireMaxBytes {
		t.Fatalf("the derived full-invoke request = %d bytes, want the pinned %d; the widest shape grew and the pin must be re-derived beside the ceilings it rounds to", request, derivedRequestWireMaxBytes)
	}
	if store.MaxWorkflowActionPayloadBytes != derived {
		t.Fatalf("store.MaxWorkflowActionPayloadBytes = %d, want the widest request composition's own rounding %d", store.MaxWorkflowActionPayloadBytes, derived)
	}
	if MaxRequestBytes != derived {
		t.Fatalf("MaxRequestBytes = %d, want the widest request composition's own rounding %d", MaxRequestBytes, derived)
	}
	if MaxRequestBytes != store.MaxWorkflowActionPayloadBytes {
		t.Fatalf("MaxRequestBytes = %d and store.MaxWorkflowActionPayloadBytes = %d; the two ceilings alias one production value and one derivation", MaxRequestBytes, store.MaxWorkflowActionPayloadBytes)
	}
	t.Logf("the widest admitted action %s serializes to %d bytes as a whole variant and %d bytes as a full invoke request; both shared ceilings derive to %d", widestName, widest, request, derived)
}
