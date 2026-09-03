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
	payload := []byte(`{"changed_refs":[{"entity_kind":"work_item","id":"work-1","version":3}],"next_valid_intents":[],"operation_id":"workflow-1","worker_packet_digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`)
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", payload, true); err != nil {
		t.Fatalf("dispatch_worker result with worker_packet_digest refused: %v", err)
	}
}

func TestMutationResultRefusesMalformedWorkerPacketDigest(t *testing.T) {
	payload := []byte(`{"changed_refs":[],"next_valid_intents":[],"worker_packet_digest":"not-a-digest"}`)
	err := ValidateOperationPayload("concord_work_transition", "workflow_action", payload, true)
	if err == nil {
		t.Fatal("malformed worker_packet_digest passed validation")
	}
	if !strings.Contains(err.Error(), "worker_packet_digest") {
		t.Fatalf("error %q does not name worker_packet_digest", err)
	}
}
