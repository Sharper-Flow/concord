package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkflowFootprintAgentSourceInputsAndPrecedence(t *testing.T) {
	f := newSpineExecutionFixture(t)
	payload := json.RawMessage(`{"delivery_artifact":"artifact:source-inputs","delivery_state":"asserted"}`)
	for _, action := range []string{"record_delivery", "complete"} {
		tooling, resolved, err := resolveWorkflowActionSources(context.Background(), f.s, store.BuiltinWorkflowRegistry(), f.work, action, payload)
		if err != nil || tooling == nil || resolved == nil || len(tooling.ResolvedCoverage) != 1 || tooling.ResolvedCoverage[0].Anchor != (store.FootprintCheck{Kind: "go_package", Value: "./pkg"}) {
			t.Fatalf("%s source inputs: tooling=%+v resolved=%+v err=%v", action, tooling, resolved, err)
		}
		encoded, err := json.Marshal(store.WorkflowActionPreflightRequest{ProjectTooling: tooling, ResolvedFootprint: resolved})
		if err != nil || strings.Contains(string(encoded), "ResolvedFootprint") || strings.Contains(string(encoded), "ProjectTooling") {
			t.Fatalf("internal source inputs leaked to JSON: %s, %v", encoded, err)
		}
	}
	// Missing candidate scope was captured, but the original accepted Review
	// prerequisite must answer first through the real runtime discovery route.
	response := f.action(t, "record_delivery", map[string]any{"delivery_artifact": "artifact:source-inputs", "delivery_state": "asserted"})
	if response.Outcome != OutcomeError || response.Error == nil || !strings.Contains(response.Error.Message, "Review") || strings.Contains(response.Error.Message, "footprint") {
		t.Fatalf("source resolution displaced Review admission: %+v", response)
	}
}
