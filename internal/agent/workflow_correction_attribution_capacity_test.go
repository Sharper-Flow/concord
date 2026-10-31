package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// Ordinary correction must fit without increasing the request transport cap.
func TestRequestBudgetHoldsCorrectionVariant(t *testing.T) {
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	if store.WorkContextActionFindingsMax != 16 {
		t.Fatalf("WorkContextActionFindingsMax = %d, want 16; the finding-reference derivation rests on it", store.WorkContextActionFindingsMax)
	}
	findingRefMax := 2 + len("finding:") + 19 + 1 + 2
	opt := schemawire.Options{
		Defs:          defs,
		FindingRefMax: findingRefMax,
	}
	correction := 0
	for _, name := range []string{"work_transition_action_variant_request_correction"} {
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
	request := invokeRequestWireBytes(t, correction)
	if request > MaxRequestBytes {
		t.Fatalf("the correction and host envelope need %d bytes, above the %d-byte request budget", request, MaxRequestBytes)
	}
}
