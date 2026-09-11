package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAppendWorkflowStalenessObservationReplaysSemanticIdentity(t *testing.T) {
	s, _ := seedCompletionGateCase(t, "staleness-replay", completionGateCase{requiredEvidence: []string{"verification", "review"}})
	payload := json.RawMessage(`{"staleness_rule_id":"staleness:warning","observed_drift":{"severity":"warning","drifted":true}}`)
	firstObservedAt := time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)
	if err := AppendWorkflowStalenessObservation(context.Background(), s, "staleness-replay:observation", "staleness-replay", "actor:staleness", testManifestDigest, payload, firstObservedAt); err != nil {
		t.Fatal(err)
	}
	if err := AppendWorkflowStalenessObservation(context.Background(), s, "staleness-replay:observation", "staleness-replay", "actor:staleness", testManifestDigest, payload, firstObservedAt.Add(time.Minute)); err != nil {
		t.Fatalf("semantic replay was refused: %v", err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE event_id='staleness-replay:observation'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("staleness observation count = %d, want 1", count)
	}
	differentDigest := "sha256:" + strings.Repeat("1", 64)
	err := AppendWorkflowStalenessObservation(context.Background(), s, "staleness-replay:observation", "staleness-replay", "actor:staleness", differentDigest, payload, firstObservedAt.Add(2*time.Minute))
	assertFailureKind(t, err, KindOperationConflict)

	changed := json.RawMessage(`{"staleness_rule_id":"staleness:warning","observed_drift":{"severity":"warning","drifted":false}}`)
	err = AppendWorkflowStalenessObservation(context.Background(), s, "staleness-replay:observation", "staleness-replay", "actor:staleness", testManifestDigest, changed, firstObservedAt.Add(3*time.Minute))
	assertFailureKind(t, err, KindOperationConflict)
}
