package store

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
)

func recordFixtureCheckpoint(t *testing.T, f workContextFixture, strategy string) {
	t.Helper()
	if err := runVerdictActionAs(t, f.store, f.workID, "checkpoint_context", mustJSONValue(map[string]any{
		"active_unit": "the bounded unit", "hypothesis": "the bounded hypothesis", "diagnosis": "the bounded diagnosis", "strategy": strategy,
		"touched_refs": []string{"internal/store/work_context.go"}, "evidence_refs": []string{"evidence:checkpoint"}, "pending_questions": []string{}, "pending_decisions": []string{},
	}), 0, f.owner); err != nil {
		t.Fatalf("checkpoint_context refused: %v", err)
	}
}

// The continuity envelope admits one copy of a max-size checkpoint; the work
// pin embeds inside that envelope and must not duplicate the checkpoint.
func TestContinuityPinsLatestCheckpointOnce(t *testing.T) {
	t.Parallel()
	fixture := seedWorkContextFixture(t, "ckpt-pin-view")
	defer fixture.store.Close()
	snapshot, err := ReadWorkflowContinuity(context.Background(), fixture.store, ContinuityRequest{Work: fixture.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestCheckpoint != nil {
		t.Fatal("continuity pinned a checkpoint before any record existed")
	}
	recordFixtureCheckpoint(t, fixture, "follow the recorded strategy")
	snapshot, err = ReadWorkflowContinuity(context.Background(), fixture.store, ContinuityRequest{Work: fixture.workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.LatestCheckpoint == nil || snapshot.LatestCheckpoint.Strategy != "follow the recorded strategy" {
		t.Fatalf("continuity left the latest checkpoint absent or stale: %+v", snapshot.LatestCheckpoint)
	}
	if snapshot.WorkPin == nil {
		t.Fatal("continuity pinned no work pin")
	}
	pinJSON, err := json.Marshal(snapshot.WorkPin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pinJSON, []byte(`"checkpoint"`)) {
		t.Fatalf("the pin duplicates the checkpoint the snapshot already carries:\n%s", pinJSON)
	}
}
