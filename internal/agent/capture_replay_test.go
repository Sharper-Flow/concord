package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestPublicCaptureReplayRecordsNoDuplicate(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := captureTaskFixture(t)
	input := retryJSON(map[string]any{"title": "Checksum mismatch", "value_statement": "Uploads lose their checksum", "kind": "bug", "project_ids": []string{"project-1"}, "idempotency_key": "capture-replay"})
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "capture-replay-1"
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("first capture response=%+v", first)
	}
	env.RequestID = "capture-replay-2"
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if replay.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("replayed capture response=%+v", replay)
	}
	if !replay.Replayed {
		t.Fatal("identical capture did not replay")
	}
	if (*replay.ChangedRefs)[0].ID != (*first.ChangedRefs)[0].ID {
		t.Fatalf("replay work ID=%s, want %s", (*replay.ChangedRefs)[0].ID, (*first.ChangedRefs)[0].ID)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("replay left %d work items, want 1", count)
	}
}
