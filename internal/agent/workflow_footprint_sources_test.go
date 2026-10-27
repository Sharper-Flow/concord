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
	declared, err := store.ResolveWorkProjectTooling(context.Background(), f.s, f.work)
	if err != nil || declared == nil || len(declared.ResolvedCoverage) != 1 || declared.ResolvedCoverage[0].Anchor != (store.FootprintCheck{Kind: "go_package", Value: "./pkg"}) {
		t.Fatalf("declared source inputs: tooling=%+v err=%v", declared, err)
	}
	for _, action := range []string{"record_delivery", "complete"} {
		tooling, resolved, err := resolveWorkflowActionSources(context.Background(), f.s, store.BuiltinWorkflowRegistry(), f.work, action, payload)
		if err != nil || tooling != nil || resolved == nil || resolved.SourcesResolved() {
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

func TestWorkflowFootprintFailedSourcesDeferToolingErrors(t *testing.T) {
	f := newSpineExecutionFixture(t)
	if _, err := f.s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE project_locators SET normalized_value='/unreachable-fixture-repository' WHERE project_id='project-1' AND kind='canonical_path';
		DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveWorkProjectTooling(t.Context(), f.s, f.work); err == nil {
		t.Fatal("broken repository did not produce the competing tooling error")
	}
	tooling, resolved, err := resolveWorkflowActionSources(t.Context(), f.s, store.BuiltinWorkflowRegistry(), f.work, "complete", nil)
	if err != nil || tooling != nil || resolved == nil || resolved.SourcesResolved() {
		t.Fatalf("tooling preempted the sealed source failure: tooling=%+v resolved=%+v err=%v", tooling, resolved, err)
	}
}
