package payloadschema

import (
	"encoding/json"
	"testing"
)

func outstandingEvidencePinFixture() map[string]any {
	return map[string]any{
		"work_id": "work-outstanding-schema", "title": "Evidence debt", "linear_issue_key": "",
		"project_id": "project-fixture", "project_display_name": "Fixture",
		"version": 3, "lifecycle": "in_progress", "cancelled_instance_closes": 0,
		"workflow_type": "change", "step": "acceptance",
		"driving_sessions": []any{},
		"watermark":        "seq:3", "next_valid_intents": []any{},
		"obligations": []string{"verification"},
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

func TestWorkPinStepFixtures(t *testing.T) {
	for _, test := range []struct {
		name         string
		workflowType string
		step         string
	}{
		{name: "planned", workflowType: "change", step: "acceptance"},
		{name: "intake", workflowType: "", step: "intake"},
		{name: "clarify", workflowType: "", step: "clarify"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pin := outstandingEvidencePinFixture()
			pin["workflow_type"], pin["step"] = test.workflowType, test.step
			if pin["step"] == "" {
				t.Fatal("published pin fixture has an empty step")
			}
			raw, err := json.Marshal(pin)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate("work_pin", raw); err != nil {
				t.Fatalf("work_pin refused fixture: %v", err)
			}
		})
	}
}
