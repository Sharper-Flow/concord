package agent

import (
	"encoding/json"
	"testing"
)

// The store answers dispatch_worker with worker_packet_digest on the result
// (CD-0067 D6), and the adapter refuses a dispatch response without it. The
// mutation-result producer validates the store's bytes against the closed
// mutation_result schema, so that schema must name the field or every
// dispatch refuses before a worker starts (issue #771).
func TestDispatchWorkerResultCarriesThePacketDigestThroughTheClosedSchema(t *testing.T) {
	base := NewBase("dispatch-result", "concord_work_transition", "workflow_action")
	changed := []ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "9"}}
	payload := json.RawMessage(`{"changed_refs":[{"entity_kind":"work_item","id":"work-1","version":9}],"next_valid_intents":[],"operation_id":"workflow-0123456789abcdef01234567","worker_packet_digest":"sha256:` + repeatHex(64) + `"}`)
	response := (runtime{Tool: base.Tool, Operation: base.Operation}).mutationResult(base, payload, changed, nil)
	if response.Outcome != OutcomeOK {
		t.Fatalf("dispatch_worker result refused: %+v", response.Error)
	}
	if _, err := response.Encode(); err != nil {
		t.Fatalf("dispatch_worker result envelope does not encode: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result["worker_packet_digest"].(string); !ok {
		t.Fatalf("result lost worker_packet_digest: %s", response.Result)
	}
}

// The field is bound to a digest shape; an arbitrary string is still refused.
func TestDispatchWorkerResultRefusesANonDigestPacketDigest(t *testing.T) {
	base := NewBase("dispatch-result-bad", "concord_work_transition", "workflow_action")
	payload := json.RawMessage(`{"changed_refs":[],"next_valid_intents":[],"operation_id":"workflow-0123456789abcdef01234567","worker_packet_digest":"not-a-digest"}`)
	response := (runtime{Tool: base.Tool, Operation: base.Operation}).mutationResult(base, payload, nil, nil)
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "malformed_response" {
		t.Fatalf("non-digest packet digest accepted: %+v", response)
	}
}
