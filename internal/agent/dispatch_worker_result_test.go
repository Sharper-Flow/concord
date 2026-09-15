package agent

import (
	"strings"
	"testing"
)

// The store places worker_packet_digest on the dispatch_worker result (CD-0067
// D6) and the adapter refuses a dispatch response without it. The closed
// mutation_result schema must therefore admit the field, or every lane
// dispatch fails before a worker starts (issue #771).
func TestDispatchWorkerResultAdmitsWorkerPacketDigest(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"changed_refs":[{"entity_kind":"work_item","id":"work-1","version":3}],"next_valid_intents":[],"operation_id":"workflow-1","worker_packet_digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`)
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", payload, true); err != nil {
		t.Fatalf("dispatch_worker result with worker_packet_digest refused: %v", err)
	}
}

func TestMutationResultRefusesMalformedWorkerPacketDigest(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"changed_refs":[],"next_valid_intents":[],"worker_packet_digest":"not-a-digest"}`)
	err := ValidateOperationPayload("concord_work_transition", "workflow_action", payload, true)
	if err == nil {
		t.Fatal("malformed worker_packet_digest passed validation")
	}
	if !strings.Contains(err.Error(), "worker_packet_digest") {
		t.Fatalf("error %q does not name worker_packet_digest", err)
	}
}

func TestDispatchWorkerPacketAdmitsCorrectionContext(t *testing.T) {
	payload := []byte(`{"work_id":"work-1","expected_version":2,"action_id":"dispatch_worker","idempotency_key":"dispatch-1","fields":{"attempt_id":"attempt-1","worker_packet":{"schema_version":"1.0","attempt_id":"attempt-1","lane_id":"verify","lane_version":1,"lane_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","work_id":"work-1","step_id":"repair","inputs":{"task":"verify the change","correction":{"disposition":"rejected","attempt_count":1,"attempt_limit":3,"escalated":false,"predicate_ids":["predicate:primary"],"evidence_refs":["evidence:review"],"diagnosis":"the result misses the boundary case","strategy":"change the helper and add a test"}}}}}`)
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", payload, false); err != nil {
		t.Fatalf("dispatch_worker worker_packet with correction refused: %v", err)
	}
}
