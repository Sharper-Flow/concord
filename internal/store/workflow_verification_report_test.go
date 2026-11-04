package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEvidenceBindingAttributesSubmittingOperation(t *testing.T) {
	ref := "commit:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fields := map[string]json.RawMessage{
		"immutable_subject_ref": json.RawMessage(`"` + ref + `"`),
		"producer_watermark":    json.RawMessage(`"watermark:one"`),
	}
	request := WorkflowActionExecutionRequest{WorkID: "work:one", ActionID: "bind_evidence", EvidenceRefs: []string{ref}, PrincipalRef: "principal:one", OperationID: "operation:one", RequestID: "request:one", Now: time.Unix(1, 0)}
	events, err := workflowEvidenceBindingEvents(request, "actor:one", fields, "event:one", 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("binding events = %+v, %v", events, err)
	}
	var payload workflowEvidenceBoundPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ProducerID != request.PrincipalRef || payload.ProducerRunRef != request.OperationID || payload.ProducerWatermark != "watermark:one" {
		t.Fatalf("binding attribution = %+v", payload)
	}
}
