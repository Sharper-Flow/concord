package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
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
	for _, kind := range []string{"lifecycle", "relation", "phase_exit", "evidence_binding", "conflict_application"} {
		input := `{"work_id":"work-1","record_kinds":["` + kind + `"]}`
		if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(input), false); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{
		`{"work_id":"work-1","record_kinds":["lifecycle","relation","phase_exit","evidence_binding","conflict_application"]}`,
		`{"work_id":"work-1","record_kinds":["conflict_application","conflict_application"]}`,
	} {
		err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(input), false)
		duplicate := strings.Contains(input, `"conflict_application","conflict_application"`)
		if (err != nil) != duplicate {
			t.Fatalf("five-kind or duplicate selector: %s: %v", input, err)
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
		{"conflict_application", map[string]any{"conflict_application": historyConflictApplication()}, "lifecycle"},
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
			delete(entry, tc.foreign)
			if tc.kind != "conflict_application" {
				entry["conflict_application"] = historyConflictApplication()
			} else {
				delete(entry, "conflict_application")
			}
			raw, _ = json.Marshal(page)
			if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", raw, true); err == nil {
				t.Fatalf("%s admitted a foreign or missing conflict payload", tc.kind)
			}
		})
	}
}

func historyConflictApplication() store.ConflictApplication {
	return store.ConflictApplication{RuleID: "R1", RuleSourceHash: "sha256:" + strings.Repeat("a", 64), CatalogGeneration: 1, SizeAssessmentSequence: 1, ModelVersion: 1, Size: store.WorkSizeSmall, Outcome: "settled", SettlementRef: "settlement:one"}
}

func TestHistoryConflictApplicationPayloadContract(t *testing.T) {
	valid, err := json.Marshal(historyConflictApplication())
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(valid, &payload); err != nil {
		t.Fatal(err)
	}
	validate := func(t *testing.T, wantValid bool) {
		t.Helper()
		page := map[string]any{"entries": []any{map[string]any{"entry_id": "conflict:one", "entry_kind": "conflict_application", "position": map[string]any{"effect_seq": 1, "basis": "effect"}, "version": 1, "version_work_id": "work-1", "evidence_refs": []string{}, "conflict_application": payload}}}
		raw, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", raw, true); (err == nil) != wantValid {
			t.Fatalf("valid=%t: %v: %s", wantValid, err, raw)
		}
	}
	validate(t, true)
	for _, key := range []string{"rule_id", "rule_source_hash", "catalog_generation", "size_assessment_sequence", "model_version", "size", "outcome", "settlement_ref"} {
		t.Run("missing_"+key, func(t *testing.T) {
			value := payload[key]
			delete(payload, key)
			validate(t, false)
			payload[key] = value
		})
	}
	for _, tc := range []struct {
		key   string
		value any
		valid bool
	}{
		{"unknown", "foreign", false},
		{"rule_id", 1, false},
		{"rule_source_hash", "sha256:invalid", false},
		{"catalog_generation", 0, false},
		{"size_assessment_sequence", 0, false},
		{"model_version", 0, false},
		{"model_version", 1.5, false},
		{"size", "unknown", false},
		{"size", "medium", true},
		{"size", "larger", true},
		{"outcome", "", false},
		{"outcome", strings.Repeat("x", 4096), true},
		{"outcome", strings.Repeat("x", 4097), false},
		{"settlement_ref", "", false},
		{"settlement_ref", strings.Repeat("x", 256), true},
		{"settlement_ref", strings.Repeat("x", 257), false},
		{"catalog_generation", int64(1) << 32, true},
		{"size_assessment_sequence", int64(1) << 32, true},
		{"model_version", int64(1) << 32, true},
	} {
		t.Run(tc.key+"_"+strconv.FormatBool(tc.valid), func(t *testing.T) {
			original, present := payload[tc.key]
			payload[tc.key] = tc.value
			validate(t, tc.valid)
			if present {
				payload[tc.key] = original
			} else {
				delete(payload, tc.key)
			}
		})
	}
}

func TestConflictObservationHistoryIntent(t *testing.T) {
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "history-intent", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled"}})
	if err != nil || response.Error != nil || response.Outcome != OutcomeOK {
		t.Fatalf("record = %+v, %v", response, err)
	}
	want := NextIntent{Tool: "concord_work_trace", Operation: "history", QueryID: "PM1.Q7", ReasonCode: "verify_settlement_recorded", RequiredFields: []string{"work_id"}}
	if response.NextValidIntents == nil || !reflect.DeepEqual(*response.NextValidIntents, []NextIntent{want}) {
		t.Fatalf("envelope intents = %+v, want %+v", response.NextValidIntents, want)
	}
	var result struct {
		Intents []mutationResultIntent `json:"next_valid_intents"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Intents, mutationResultIntents([]NextIntent{want})) {
		t.Fatalf("result intents = %+v, want history intent", result.Intents)
	}
	raw := json.RawMessage(`{"work_id":"work-sized"}`)
	read, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: want.Tool, Operation: want.Operation, Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil || read.Error != nil || read.Outcome != OutcomeOK {
		t.Fatalf("follow history intent = %+v, %v", read, err)
	}
	var page struct {
		Entries []store.HistoryEntry `json:"entries"`
	}
	if err := json.Unmarshal(read.Result, &page); err != nil {
		t.Fatal(err)
	}
	for _, entry := range page.Entries {
		if entry.ConflictApplication != nil && entry.ConflictApplication.Outcome == "settled" {
			return
		}
	}
	t.Fatalf("history intent did not reach settlement: %+v", page.Entries)
}

func TestHistoryDetailsGenericArrayBound(t *testing.T) {
	for _, size := range []int{20, 21} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			envelope := coreError(NewBase("history-details", "concord_work_trace", "history"), "invalid_input", "invalid filter", "reread_entities", false)
			envelope.Error.Details = map[string]any{"other": make([]string, size)}
			_, err := json.Marshal(envelope)
			if (err == nil) != (size == 20) {
				t.Fatalf("generic details array with %d items: %v", size, err)
			}
		})
	}
}
