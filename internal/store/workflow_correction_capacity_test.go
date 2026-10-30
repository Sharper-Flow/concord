package store

import (
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

func TestWorkflowCorrectionFitsDerivedActionBudget(t *testing.T) {
	t.Parallel()
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	const name = "work_transition_action_variant_request_correction"
	composed, err := schemawire.Max(defs[name], name, schemawire.Options{
		Defs: defs, FindingRefMax: 2 + len("finding:") + 19 + 1 + 2,
	})
	if err != nil {
		t.Fatalf("the %s derivation hit an unbudgeted member: %v", name, err)
	}
	if composed > maxWorkflowActionPayloadBytes {
		t.Fatalf("the %s declared maxima need %d bytes, above the %d-byte whole-action budget", name, composed, maxWorkflowActionPayloadBytes)
	}
}
