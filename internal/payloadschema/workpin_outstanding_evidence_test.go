package payloadschema

import (
	"encoding/json"
	"testing"
)

func workPinStepFixture() map[string]any {
	return map[string]any{
		"work_id": "work-step-schema", "title": "Step fixture", "linear_issue_key": "",
		"project_id": "project-fixture", "project_display_name": "Fixture",
		"version": 3, "lifecycle": "in_progress", "cancelled_instance_closes": 0,
		"workflow_type": "change", "step": "acceptance",
		"driving_sessions": []any{},
		"watermark":        "seq:3", "next_valid_intents": []any{},
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
			pin := workPinStepFixture()
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
