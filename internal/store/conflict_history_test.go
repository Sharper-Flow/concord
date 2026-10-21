package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// The recorded work.conflict_applied event must surface through the public
// PM1.Q7 history as an optional typed conflict_application payload with the
// recorded trio (rule_id, size, outcome) and its frozen source hash, catalog
// generation, assessment sequence, model version, and settlement reference.
// Every assertion reads the serialized public event; the recorder tests pin
// the stored record, and these own only the public read.

// conflictHistoryAllowedEventKeys is the closed serialized TimelineEvent key
// set: the ordinary public fields plus the one optional conflict_application
// payload. Anything else is an arbitrary payload leak.
var conflictHistoryAllowedEventKeys = map[string]struct{}{
	"event_id": {}, "seq": {}, "global_seq": {}, "kind": {}, "actor": {},
	"occurred_at": {}, "from": {}, "to": {}, "reason": {}, "evidence_refs": {},
	"contract_version": {}, "previous_contract_version": {}, "conflict_application": {},
}

func conflictHistoryEventJSON(t *testing.T, event TimelineEvent) map[string]any {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("serialize the public history event: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("public history event is not a JSON object: %v", err)
	}
	for key := range fields {
		if _, ok := conflictHistoryAllowedEventKeys[key]; !ok {
			t.Fatalf("public history event leaks key %q: %s", key, raw)
		}
	}
	return fields
}

// conflictHistoryPayload returns the published conflict_application payload
// of one serialized history event, failing while the trio is hidden.
func conflictHistoryPayload(t *testing.T, event map[string]any) map[string]any {
	t.Helper()
	payload, ok := event["conflict_application"].(map[string]any)
	if !ok {
		t.Fatalf("public history event %v carries no conflict_application payload; the persisted trio is hidden from readers", event)
	}
	return payload
}

func conflictHistoryString(t *testing.T, payload map[string]any, field string) string {
	t.Helper()
	value, ok := payload[field].(string)
	if !ok || value == "" {
		t.Fatalf("conflict_application payload %v carries no %q string", payload, field)
	}
	return value
}

func conflictHistoryNumber(t *testing.T, payload map[string]any, field string) float64 {
	t.Helper()
	value, ok := payload[field].(float64)
	if !ok {
		t.Fatalf("conflict_application payload %v carries no %q number", payload, field)
	}
	return value
}

// conflictHistoryAssertShape checks one published payload against the closed
// v1 event shape and one recorded settlement.
func conflictHistoryAssertShape(t *testing.T, payload map[string]any, ruleID, size, outcome string, assessmentSequence, modelVersion int) {
	t.Helper()
	if len(payload) != 8 {
		t.Fatalf("conflict_application payload carries %d fields; the closed v1 shape has exactly 8: %v", len(payload), payload)
	}
	for _, field := range []string{"rule_id", "rule_source_hash", "catalog_generation", "size_assessment_sequence", "model_version", "size", "outcome", "settlement_ref"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("conflict_application payload is missing %q: %v", field, payload)
		}
	}
	if got := conflictHistoryString(t, payload, "rule_id"); got != ruleID {
		t.Fatalf("published rule_id %q, want %q", got, ruleID)
	}
	if got := conflictHistoryString(t, payload, "size"); got != size {
		t.Fatalf("published size %q, want the recorded %q", got, size)
	}
	if got := conflictHistoryString(t, payload, "outcome"); got != outcome {
		t.Fatalf("published outcome %q, want the recorded %q", got, outcome)
	}
	if got := conflictHistoryString(t, payload, "settlement_ref"); len(got) > conflictSettlementRefMaxBytes {
		t.Fatalf("published settlement reference %q exceeds its %d-byte bound", got, conflictSettlementRefMaxBytes)
	}
	hash := conflictHistoryString(t, payload, "rule_source_hash")
	if len(hash) != len("sha256:")+64 || hash[:len("sha256:")] != "sha256:" {
		t.Fatalf("published rule source hash %q is not a sha256 digest", hash)
	}
	if got := conflictHistoryNumber(t, payload, "size_assessment_sequence"); got != float64(assessmentSequence) {
		t.Fatalf("published size_assessment_sequence %v, want %d", got, assessmentSequence)
	}
	if got := conflictHistoryNumber(t, payload, "model_version"); got != float64(modelVersion) {
		t.Fatalf("published model_version %v, want %d", got, modelVersion)
	}
	if got := conflictHistoryNumber(t, payload, "catalog_generation"); got < 1 {
		t.Fatalf("published catalog_generation %v, want the live table generation", got)
	}
}

// conflictHistorySingle queries one work's conflict-filtered public history
// and returns the published payload of its single conflict event.
func conflictHistorySingle(t *testing.T, s *Store, workID string) map[string]any {
	t.Helper()
	result, err := s.QueryQ7(context.Background(), Q7Request{Work: workID, EventKinds: []string{WorkConflictApplied}})
	if err != nil {
		t.Fatalf("conflict-filtered history for %s: %v", workID, err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("conflict-filtered history for %s returned %d events, want 1", workID, len(result.Events))
	}
	return conflictHistoryPayload(t, conflictHistoryEventJSON(t, result.Events[0]))
}

func TestConflictHistoryPublishesTheRecordedTrio(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "public")
	if _, err := recordConflictApplication(t, s, conflictApplicationRequest("public", "public-1", nil)); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	result, err := s.QueryQ7(context.Background(), Q7Request{Work: "public", EventKinds: []string{WorkConflictApplied}})
	if err != nil {
		t.Fatalf("conflict-filtered history: %v", err)
	}
	if len(result.Events) != 1 || result.Events[0].Kind != WorkConflictApplied {
		t.Fatalf("filtered history returned %+v, want exactly the recorded work.conflict_applied event", result.Events)
	}
	event := conflictHistoryEventJSON(t, result.Events[0])
	if got := event["actor"]; got != "operator" {
		t.Fatalf("published actor %v, want the authenticated settlement actor", got)
	}
	conflictHistoryAssertShape(t, conflictHistoryPayload(t, event), "R1", string(WorkSizeSmall), "settled: rule R1 applied through the recorder", 1, WorkSizeModelVersion)
}

// The synthetic later work.sized event is a fold fixture, not a public resize
// route: the published copy must stay the assessment the event references,
// never the current size.
func TestConflictHistoryKeepsTheFrozenAssessmentAfterALaterFold(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "frozen")
	if _, err := recordConflictApplication(t, s, conflictApplicationRequest("frozen", "frozen-1", nil)); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	later := workSizeInputsFixture()
	later.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("frozen-later", "frozen", WorkSizeModelVersion, WorkSizeLarger, later), nil); err != nil {
		t.Fatalf("record the later assessment: %v", err)
	}
	conflictHistoryAssertShape(t, conflictHistorySingle(t, s, "frozen"), "R1", string(WorkSizeSmall), "settled: rule R1 applied through the recorder", 1, WorkSizeModelVersion)
}

func TestConflictHistoryLeavesOrdinaryHistoryUnchanged(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "ordinary")
	if _, err := recordConflictApplication(t, s, conflictApplicationRequest("ordinary", "ordinary-1", nil)); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	result, err := s.QueryQ7(context.Background(), Q7Request{Work: "ordinary"})
	if err != nil {
		t.Fatalf("default history: %v", err)
	}
	if len(result.Events) == 0 {
		t.Fatal("default history returned no events")
	}
	for _, event := range result.Events {
		if event.Kind == WorkConflictApplied {
			t.Fatalf("default history includes the conflict event %s; the published defaults must stay unchanged", event.EventID)
		}
		fields := conflictHistoryEventJSON(t, event)
		if _, ok := fields["conflict_application"]; ok {
			t.Fatalf("ordinary event %s carries a conflict_application payload: %v", event.EventID, fields)
		}
	}
}

func TestConflictHistoryPagesTheFilteredTrioWithUnchangedCursors(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "paged")
	firstIDs, err := recordConflictApplication(t, s, conflictApplicationRequest("paged", "paged-1", nil))
	if err != nil {
		t.Fatalf("record the first application: %v", err)
	}
	secondIDs, err := recordConflictApplication(t, s, conflictApplicationRequest("paged", "paged-2", func(r *ConflictApplicationRequest) {
		r.RuleID = "A2-3"
		r.Outcome = "settled: A2-3 recorded"
	}))
	if err != nil {
		t.Fatalf("record the second application: %v", err)
	}
	ruleByEvent := map[string]string{firstIDs[0]: "R1", secondIDs[0]: "A2-3"}
	first, err := s.QueryQ7(context.Background(), Q7Request{Work: "paged", EventKinds: []string{WorkConflictApplied}, Direction: "oldest_first", Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Events) != 1 || first.NextCursor == nil {
		t.Fatalf("first page = %+v cursor %v, want one event and a continuation", first.Events, first.NextCursor)
	}
	seen := map[string]bool{first.Events[0].EventID: true}
	if got := conflictHistoryString(t, conflictHistoryPayload(t, conflictHistoryEventJSON(t, first.Events[0])), "rule_id"); got != ruleByEvent[first.Events[0].EventID] {
		t.Fatalf("page event %s publishes rule %q", first.Events[0].EventID, got)
	}
	second, err := s.QueryQ7(context.Background(), Q7Request{Work: "paged", EventKinds: []string{WorkConflictApplied}, Direction: "oldest_first", Limit: 1, Cursor: *first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Events) != 1 || second.NextCursor != nil {
		t.Fatalf("second page = %+v cursor %v, want the last event and no continuation", second.Events, second.NextCursor)
	}
	if got := conflictHistoryString(t, conflictHistoryPayload(t, conflictHistoryEventJSON(t, second.Events[0])), "rule_id"); got != ruleByEvent[second.Events[0].EventID] {
		t.Fatalf("page event %s publishes rule %q", second.Events[0].EventID, got)
	}
	seen[second.Events[0].EventID] = true
	if len(seen) != 2 {
		t.Fatalf("cursor walk visited %v, want both recorded events", seen)
	}
}

// The published trio must survive rebuild and reopen against the frozen
// assessment, not the later one.
func TestConflictHistorySurvivesRebuildAndReopen(t *testing.T) {
	ctx := context.Background()
	path := copyTestDatabase(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	seedSizedWork(t, s, "durable-read")
	if _, err := recordConflictApplication(t, s, conflictApplicationRequest("durable-read", "durable-read-1", nil)); err != nil {
		t.Fatalf("record the conflict application: %v", err)
	}
	before := conflictHistorySingle(t, s, "durable-read")
	later := workSizeInputsFixture()
	later.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("durable-read-later", "durable-read", WorkSizeModelVersion, WorkSizeLarger, later), nil); err != nil {
		t.Fatalf("record the later assessment: %v", err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	after := conflictHistorySingle(t, s, "durable-read")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rebuild changed the published payload: %+v, want %+v", after, before)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	again := conflictHistorySingle(t, reopened, "durable-read")
	if !reflect.DeepEqual(before, again) {
		t.Fatalf("reopened store publishes %+v, want %+v", again, before)
	}
	conflictHistoryAssertShape(t, again, "R1", string(WorkSizeSmall), "settled: rule R1 applied through the recorder", 1, WorkSizeModelVersion)
}
