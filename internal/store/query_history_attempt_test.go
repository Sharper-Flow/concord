package store

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestHistoryInvalidFilterCarriesCompleteRegistry(t *testing.T) {
	s := seedQueryFixture(t)
	_, err := s.QueryQ7(context.Background(), Q7Request{Work: "blocked", EventKinds: []string{"worker.unknown"}})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidFilter {
		t.Fatalf("unknown-kind refusal = %v", err)
	}
	want := make([]string, 0, len(eventKindRegistry))
	for kind := range eventKindRegistry {
		want = append(want, kind)
	}
	sort.Strings(want)
	if !slices.Equal(failure.AdmittedEventKinds, want) {
		t.Fatalf("refusal kinds = %v, want the complete sorted registry %v", failure.AdmittedEventKinds, want)
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

func TestTimelineWorkerAttemptIdentity(t *testing.T) {
	for _, kind := range []string{WorkerDispatched, WorkerCompleted, WorkerFailed} {
		for _, attemptID := range []string{"attempt/one", "attempt:two", strings.Repeat("a", 256)} {
			raw, err := json.Marshal(map[string]string{"attempt_id": attemptID})
			if err != nil {
				t.Fatal(err)
			}
			event, err := decodeTimelineEvent("event-1", 1, kind, "actor-1", "2026-10-17T00:00:00Z", 2, raw)
			if err != nil || event.AttemptID != attemptID {
				t.Fatalf("%s attempt identity = %q, error %v; want %q", kind, event.AttemptID, err, attemptID)
			}
		}
		for _, raw := range []string{`{"attempt_id":12}`, `{"attempt_id":null}`, `{"attempt_id":""}`} {
			_, err := decodeTimelineEvent("event-1", 1, kind, "actor-1", "2026-10-17T00:00:00Z", 2, []byte(raw))
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
				t.Fatalf("%s malformed attempt identity %s returned %v", kind, raw, err)
			}
		}
	}
	for _, raw := range []string{`{}`, `{"attempt_id":"unrelated"}`} {
		event, err := decodeTimelineEvent("event-1", 1, "work.created", "actor-1", "2026-10-17T00:00:00Z", 2, []byte(raw))
		if err != nil || event.AttemptID != "" {
			t.Fatalf("non-worker event acquired attempt identity %q: %v", event.AttemptID, err)
		}
	}
}
