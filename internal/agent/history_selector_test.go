package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestTraceHistoryRejectsLegacySelectors(t *testing.T) {
	for _, input := range []string{
		`{"work_id":"work-1","event_kinds":["work.created"]}`,
		`{"work_id":"work-1","record_kinds":["work.created"]}`,
		`{"work_id":"work-1","record_kinds":["worker.unknown"]}`,
	} {
		if err := ValidateOperationPayload("concord_work_trace", "history", []byte(input), false); err == nil {
			t.Fatalf("legacy or unknown selector accepted: %s", input)
		}
	}
	for _, kind := range []string{"lifecycle", "relation", "phase_exit", "evidence_binding"} {
		input := `{"work_id":"work-1","record_kinds":["` + kind + `"]}`
		if err := ValidateOperationPayload("concord_work_trace", "history", []byte(input), false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryEntriesAreClosedVariants(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		fields  map[string]any
		foreign string
	}{
		{"lifecycle", map[string]any{"lifecycle": "needed"}, "action_id"},
		{"relation", map[string]any{"work_id_from": "work-1", "work_id_to": "work-2", "relation_kind": "depends_on", "created_at": "2026-10-01T00:00:00Z"}, "lifecycle"},
		{"phase_exit", map[string]any{"action_id": "record_report", "step_id": "report"}, "binding_id"},
		{"evidence_binding", map[string]any{"binding_id": "binding-1", "evidence_kind": "verification", "immutable_subject_ref": "proof:one", "producer_id": "producer:one", "producer_run_ref": "run:one", "producer_watermark": "seq:1", "observed_at": "2026-10-01T00:00:00Z"}, "step_id"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			entry := map[string]any{"entry_id": tc.kind + ":one", "entry_kind": tc.kind, "position": map[string]any{"effect_seq": 1, "basis": "effect"}, "version": 1, "version_work_id": "work-1", "evidence_refs": []string{}}
			for key, value := range tc.fields {
				entry[key] = value
			}
			page := map[string]any{"entries": []any{entry}}
			raw, _ := json.Marshal(page)
			if err := ValidateOperationPayload("concord_work_trace", "history", raw, true); err != nil {
				t.Fatalf("valid %s: %v", tc.kind, err)
			}
			entry[tc.foreign] = "foreign"
			if tc.foreign == "lifecycle" {
				entry[tc.foreign] = "needed"
			}
			raw, _ = json.Marshal(page)
			if err := ValidateOperationPayload("concord_work_trace", "history", raw, true); err == nil {
				t.Fatalf("%s admitted %s from another variant", tc.kind, tc.foreign)
			}
		})
	}
}

func TestHistoryAdmittedKindsDetailsBounds(t *testing.T) {
	maximum := make([]string, 128)
	for i := range maximum {
		maximum[i] = "event." + strconv.Itoa(i)
	}
	for _, tc := range []struct {
		name    string
		details map[string]any
		valid   bool
	}{
		{name: "declared maximum", details: map[string]any{"admitted_event_kinds": maximum}, valid: true},
		{name: "over declared maximum", details: map[string]any{"admitted_event_kinds": append(append([]string{}, maximum...), "event.extra")}},
		{name: "empty", details: map[string]any{"admitted_event_kinds": []string{}}},
		{name: "duplicate", details: map[string]any{"admitted_event_kinds": []string{"work.created", "work.created"}}},
		{name: "empty kind", details: map[string]any{"admitted_event_kinds": []string{""}}},
		{name: "long kind", details: map[string]any{"admitted_event_kinds": []string{strings.Repeat("a", 257)}}},
		{name: "wrong type", details: map[string]any{"admitted_event_kinds": "work.created"}},
		{name: "generic array bound unchanged", details: map[string]any{"other": maximum[:21]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envelope := coreError(NewBase("history-details", "concord_work_trace", "history"), "invalid_input", "invalid filter", "reread_entities", false)
			envelope.Error.Details = tc.details
			_, err := json.Marshal(envelope)
			if (err == nil) != tc.valid {
				t.Fatalf("error details validity = %v, want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
