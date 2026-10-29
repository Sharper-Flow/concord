package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestTraceHistoryRejectsLegacySelectors(t *testing.T) {
	for _, input := range []string{
		`{"work_id":"work-1","event_kinds":["work.created"]}`,
		`{"work_id":"work-1","record_kinds":["work.created"]}`,
		`{"work_id":"work-1","record_kinds":["worker.unknown"]}`,
	} {
		if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(input), false); err == nil {
			t.Fatalf("legacy or unknown selector accepted: %s", input)
		}
	}
	for _, kind := range []string{"lifecycle", "relation", "phase_exit", "evidence_binding"} {
		input := `{"work_id":"work-1","record_kinds":["` + kind + `"]}`
		if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(input), false); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{
		`{"work_id":"work-1","record_kinds":["lifecycle","relation","phase_exit","evidence_binding"]}`,
		`{"work_id":"work-1","record_kinds":["evidence_binding","evidence_binding"]}`,
	} {
		err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(input), false)
		duplicate := strings.Contains(input, `"evidence_binding","evidence_binding"`)
		if (err != nil) != duplicate {
			t.Fatalf("four-kind or duplicate selector: %s: %v", input, err)
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
			if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", raw, true); err != nil {
				t.Fatalf("valid %s: %v", tc.kind, err)
			}
			entry[tc.foreign] = "foreign"
			if tc.foreign == "lifecycle" {
				entry[tc.foreign] = "needed"
			}
			raw, _ = json.Marshal(page)
			if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", raw, true); err == nil {
				t.Fatalf("%s admitted %s from another variant", tc.kind, tc.foreign)
			}
		})
	}
}

func TestHistoryDetailsGenericArrayBound(t *testing.T) {
	for _, size := range []int{20, 21} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			envelope := coreError(agentcontract.NewBase("history-details", "concord_work_trace", "history"), "invalid_input", "invalid filter", "reread_entities", false)
			envelope.Error.Details = map[string]any{"other": make([]string, size)}
			_, err := json.Marshal(envelope)
			if (err == nil) != (size == 20) {
				t.Fatalf("generic details array with %d items: %v", size, err)
			}
		})
	}
}
