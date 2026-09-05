package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
)

// A checkpoint the write accepts must read back. The checkpoint_context
// write admits any whitespace-free reference of 2 to 128 bytes, so file
// paths and URLs are legal refs, and the continuity read must carry them.
func TestContinuityReadsCheckpointRefsTheWriteAccepts(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Checkpoint refs research", "research", "workflow.research", "checkpoint-refs-capture")

	version := agentCompositionWorkVersion(t, s, workID)
	raw, err := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "action_id": "checkpoint_context", "idempotency_key": "checkpoint-refs-write",
		"fields": map[string]any{
			"active_unit":       "unit:frame",
			"hypothesis":        "paths and URLs are legal checkpoint refs",
			"diagnosis":         "the read schema typed them as ids",
			"strategy":          "widen the read to the write bound",
			"touched_refs":      []string{"internal/store/workflow_registry.go", "scenarios/workflow-engine.v1.json"},
			"evidence_refs":     []string{"https://github.com/Sharper-Flow/concord/pull/836", "commit:f076cef390c13944b831b9024334b291a435588b"},
			"pending_questions": []string{}, "pending_decisions": []string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:checkpoint-refs-write:" + strconv.FormatInt(version, 10)
	written, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	if written.Outcome != OutcomeOK {
		t.Fatalf("checkpoint_context refused: %+v", written.Error)
	}

	env.RequestID = "request:checkpoint-refs-read"
	read, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "continuity", Input: json.RawMessage(`{"work_id":"` + workID + `","page":{"cursor":null,"limit":10}}`)}, env)
	if err != nil {
		t.Fatal(err)
	}
	if read.Outcome != OutcomeOK {
		t.Fatalf("continuity read refused the checkpoint the write accepted: %+v", read.Error)
	}
	var payload struct {
		LatestCheckpoint *struct {
			TouchedRefs  []string `json:"touched_refs"`
			EvidenceRefs []string `json:"evidence_refs"`
		} `json:"latest_checkpoint"`
	}
	if err := json.Unmarshal(read.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.LatestCheckpoint == nil || len(payload.LatestCheckpoint.TouchedRefs) != 2 || len(payload.LatestCheckpoint.EvidenceRefs) != 2 {
		t.Fatalf("latest_checkpoint=%+v", payload.LatestCheckpoint)
	}
}
