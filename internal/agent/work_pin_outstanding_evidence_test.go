package agent

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
)

func TestWorkPinOutstandingEvidenceKindsTypedEnvelope(t *testing.T) {
	for _, present := range []bool{false, true} {
		pin := map[string]any{
			"work_id": "work-1", "title": "Evidence debt", "linear_issue_key": "",
			"project_id": "project-fixture", "project_display_name": "Fixture",
			"version": 3, "lifecycle": "in_progress", "cancelled_instance_closes": 0,
			"workflow_type": "workflow.implementation", "step": "acceptance", "attempt": nil,
			"pending_operator_decision": nil, "driving_sessions": []any{},
			"watermark": "seq:3", "next_valid_intents": []any{},
			"workflow_definition_digest": "sha256:" + strings.Repeat("0", 64),
			"obligations":                []string{"verification"},
		}
		if present {
			pin["outstanding_evidence_kinds"] = []string{"approval", "artifact", "commit", "durable_note", "native_run", "review", "verification"}
		}
		payload, err := json.Marshal(map[string]any{
			"changed_refs":       []any{map[string]any{"entity_kind": "work_item", "id": "work-1", "version": 3}},
			"next_valid_intents": []any{}, "operation_id": "workflow-1", "work_pins": []any{pin},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", payload, true); err != nil {
			t.Fatalf("typed envelope with debt present=%t: %v", present, err)
		}
		response := NewOKMutation(NewBase("debt-envelope", "concord_work_transition", "workflow_action"), payload,
			[]ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "3"}}, nil)
		encoded, err := response.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > MaxResultEnvelopeBytes {
			t.Fatal("seven-kind metadata exceeded the unchanged envelope budget")
		}
		decoded, err := decodeEnvelopeForTest(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded.Result, payload) {
			t.Fatal("envelope round-trip changed or truncated evidence-debt metadata")
		}
		replayed, _, pinless, err := (runtime{}).replayMutationPayload(payload,
			[]ChangedRef{{EntityKind: "work_item", ID: "work-1", Version: "3"}})
		if err != nil || len(pinless) != 0 || !bytes.Equal(replayed, payload) {
			t.Fatalf("retained snapshot changed, backfilled, or lost its pin: err=%v pinless=%v", err, pinless)
		}
	}
}
