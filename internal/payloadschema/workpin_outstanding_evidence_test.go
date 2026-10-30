package payloadschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func outstandingEvidencePinFixture() map[string]any {
	return map[string]any{
		"work_id": "work-outstanding-schema", "title": "Evidence debt", "linear_issue_key": "",
		"project_id": "project-fixture", "project_display_name": "Fixture",
		"version": 3, "lifecycle": "in_progress", "cancelled_instance_closes": 0,
		"workflow_type": "workflow.implementation", "step": "acceptance",
		"pending_operator_decision": nil, "driving_sessions": []any{},
		"watermark": "seq:3", "next_valid_intents": []any{},
		"workflow_definition_digest": "sha256:" + strings.Repeat("0", 64),
		"obligations":                []string{"verification"},
	}
}

func TestWorkPinOutstandingEvidenceKindsSchema(t *testing.T) {
	for _, test := range []struct {
		name    string
		present bool
		value   any
		valid   bool
	}{
		{name: "legacy-absence", valid: true},
		{name: "known-empty", present: true, value: []string{}, valid: true},
		{name: "all-seven-kinds", present: true, value: []string{"approval", "artifact", "commit", "durable_note", "native_run", "review", "verification"}, valid: true},
		{name: "unknown-kind", present: true, value: []string{"unregistered"}},
		{name: "duplicate-kind", present: true, value: []string{"commit", "commit"}},
		{name: "null-is-not-known-empty", present: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pin := outstandingEvidencePinFixture()
			if test.present {
				pin["outstanding_evidence_kinds"] = test.value
			}
			raw, err := json.Marshal(pin)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate("work_pin", raw); (err == nil) != test.valid {
				t.Fatalf("work_pin validation = %v, want valid=%t", err, test.valid)
			}
		})
	}
}

func TestOutsideRepairWorkPinOmitsOutstandingEvidenceKinds(t *testing.T) {
	for _, state := range []string{"active", "completed"} {
		t.Run(state, func(t *testing.T) {
			pin := outstandingEvidencePinFixture()
			delete(pin, "workflow_definition_digest")
			delete(pin, "obligations")
			pin["outside_repair_disposition"] = map[string]any{
				"work_id": "work-outstanding-schema", "reason": "repair",
				"state": state, "recorded_at": "2026-10-17T00:00:00Z",
			}
			raw, err := json.Marshal(pin)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate("work_pin", raw); err != nil {
				t.Fatalf("held pin without managed debt: %v", err)
			}
			pin["outstanding_evidence_kinds"] = []string{}
			raw, err = json.Marshal(pin)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate("work_pin", raw); err == nil {
				t.Fatal("held pin admitted managed evidence-debt metadata")
			}
		})
	}
}
