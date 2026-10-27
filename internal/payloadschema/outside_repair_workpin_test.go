package payloadschema

import (
	"encoding/json"
	"testing"
)

func TestOutsideRepairWorkPinTypedAbsence(t *testing.T) {
	pin := map[string]any{
		"work_id": "work-fixture", "title": "repair", "linear_issue_key": "",
		"project_id": "project-fixture", "project_display_name": "fixture",
		"version": 1, "lifecycle": "needed", "cancelled_instance_closes": 0,
		"workflow_type": "", "step": "", "attempt": nil,
		"pending_operator_decision": nil, "driving_sessions": []any{},
		"watermark": "seq:1", "next_valid_intents": []any{},
	}
	validate := func() error {
		raw, err := json.Marshal(pin)
		if err != nil {
			t.Fatal(err)
		}
		return Validate("work_pin", raw)
	}
	if err := validate(); err == nil {
		t.Fatal("ordinary managed pin admitted empty workflow identity")
	}
	disposition := map[string]any{"work_id": "work-fixture", "reason": "repair", "approval_ref": "approval-fixture", "state": "active", "recorded_at": "2026-10-07T00:00:00Z"}
	pin["outside_repair_disposition"] = disposition
	for _, claim := range []string{"approval-fixture", ""} {
		disposition["approval_ref"] = claim
		for _, state := range []string{"active", "completed"} {
			disposition["state"] = state
			if err := validate(); err != nil {
				t.Fatalf("outside %s pin with approval_ref %q rejected typed absence: %v", state, claim, err)
			}
		}
	}
	disposition["state"] = "resumed"
	if err := validate(); err == nil {
		t.Fatal("resumed managed pin admitted empty workflow identity")
	}
	disposition["state"] = "active"
	pin["workflow_type"], pin["step"] = "workflow.break_fix", "repair"
	pin["next_valid_intents"] = []any{map[string]any{"tool": "concord_work_transition", "operation": "workflow_action", "reason_code": "managed"}}
	if err := validate(); err == nil {
		t.Fatal("held pin admitted a managed action prompt")
	}
}

func TestOutsideRepairDispositionOrdinaryApprovalShape(t *testing.T) {
	for _, state := range []string{"active", "completed", "resumed"} {
		for _, claim := range []any{"", "approval:historical", nil, 1, "not a reference"} {
			disposition := map[string]any{"work_id": "work-fixture", "reason": "repair", "approval_ref": claim, "state": state, "recorded_at": "2026-10-07T00:00:00Z"}
			raw, err := json.Marshal(disposition)
			if err != nil {
				t.Fatal(err)
			}
			valid := claim == "" || claim == "approval:historical"
			if err := Validate("outside_repair_disposition", raw); (err == nil) != valid {
				t.Errorf("state %s claim %v: valid=%v, want %v: %v", state, claim, err == nil, valid, err)
			}
		}
	}
}
