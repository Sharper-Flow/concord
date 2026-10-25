package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestHistoryRejectsLegacyAndUnknownRecordKinds(t *testing.T) {
	s := seedQueryFixture(t)
	for _, kind := range []string{"worker.unknown", "work.created", "workflow.action_completed", "contract"} {
		_, err := s.QueryQ7(context.Background(), Q7Request{Work: "blocked", RecordKinds: []string{kind}})
		assertFailureKind(t, err, KindInvalidFilter)
	}
}

func TestHistoryEventKindRegistryFitsPublishedRefusal(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/agent-tool-envelope.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"$defs", "typedError", "properties", "details", "properties", "admitted_event_kinds"} {
		next, ok := schema[key].(map[string]any)
		if !ok {
			t.Fatalf("published admitted-event-kind contract lacks %s", key)
		}
		schema = next
	}
	maximum, ok := schema["maxItems"].(float64)
	if !ok || maximum < 1 {
		t.Fatalf("published admitted-event-kind maxItems is invalid: %v", schema["maxItems"])
	}
	if len(eventKindRegistry) > int(maximum) {
		t.Fatalf("event-kind registry has %d kinds, exceeding admitted_event_kinds maxItems %d; update the published refusal contract", len(eventKindRegistry), int(maximum))
	}
}
