package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A work item with no research pack is an ordinary state, not a fault: most
// work carries no research. The read that reports it must therefore be
// deliverable, because a caller asking "is there research here?" receives this
// answer on the common path.
//
// TestEveryDeclaredReadMarshalsItsEnvelope drives this read with no work_id
// and reaches a different refusal, so the absent-pack branch had no coverage.

// TestResearchReadWithoutAPackIsDeliverable is the reproduction. The branch
// minted kind "not_found", which the envelope contract does not declare, so
// validation refused the refusal and the caller received a transport fault in
// place of the answer. The owner read is versioned now: the version 1 owner
// read is refused explicitly instead of silently discarding every pack after
// the first, and that refusal must still be deliverable. The version 2 empty
// owner returns an empty page; TestResearchReadOwnerVersion2EmptyOwnerIsEmptyPage
// pins that shape.
func TestResearchReadWithoutAPackIsDeliverable(t *testing.T) {
	s, service, grant, _ := agentJobsPM1Fixture(t)
	env := agentJobsEnvelope(grant, "proj-web", "prod-alpha")

	input := json.RawMessage(`{"product_id":"prod-alpha","work_id":"work-done"}`)
	resp := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "research", Input: input}, env)

	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("the version 1 owner read must refuse; got outcome %q error %+v", resp.Outcome, resp.Error)
	}
	if _, err := resp.Encode(); err != nil {
		t.Fatalf("the owner-read refusal cannot be delivered: %v", err)
	}
	if !store.TypedErrorKindAllowed(resp.Error.Kind) {
		t.Fatalf("the refusal names kind %q, which the envelope contract does not declare", resp.Error.Kind)
	}
	if resp.Error.Kind != "invalid_input" {
		t.Errorf("the version 1 owner read is refused as invalid input; got kind %q, want invalid_input", resp.Error.Kind)
	}
	if !strings.Contains(resp.Error.Message, "result_version") {
		t.Errorf("the refusal must direct the caller to result_version 2; got %q", resp.Error.Message)
	}
	if resp.Error.EffectState != EffectNone {
		t.Errorf("a read commits nothing; got effect_state %q, want %q", resp.Error.EffectState, EffectNone)
	}
}
