package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestRetryWindowAbsentFromCurrentReadSurfaces(t *testing.T) {
	const workID = "retry-window-absent"
	j := pReturnReviewFixture(t, workID, "workflow.implementation", "correction_required")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	s := j.s
	version := readWorkVersion(t, s, workID)
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil || pin.Correction.SourceEventID == "" || pin.Correction.SourceEventSeq < 1 {
		t.Fatalf("the pin lost the genuine correction source: %+v", pin.Correction)
	}
	projection, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"work_pin": pin, "workflow_read": projection} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if _, exists := fields["retry_window"]; exists {
			t.Errorf("%s still publishes retry_window", name)
		}
	}
	if readWorkVersion(t, s, workID) != version {
		t.Fatal("reading the projections changed the correction's work version")
	}
}

func TestRetryWindowAbsentFromAuthoredReadContract(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/agent-tool-surface-payloads.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workflow_retry_window", "workflow_progress_anchor"} {
		if _, exists := schema.Defs[name]; exists {
			t.Errorf("the published payload schema still declares %s", name)
		}
	}
	for _, name := range []string{"work_pin", "workflow_read"} {
		definition, exists := schema.Defs[name]
		if !exists {
			t.Fatalf("the payload schema is missing %s", name)
		}
		if _, exists := definition.Properties["retry_window"]; exists {
			t.Errorf("%s still admits retry_window", name)
		}
	}
}
