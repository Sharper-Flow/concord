package store

import (
	"encoding/json"
	"os"
	"testing"
)

// An approval gate only gates when the framing action beside it holds the
// step. A framing action that advances lets the first call leave the step
// with a null contract, so approve_contract never runs.
func TestApprovalGateStepHasNoOtherAdvancingAction(t *testing.T) {
	gated := map[string]map[string][]string{
		"workflow.research":           {"frame": {"frame_research"}, "conclude": {"record_conclusion"}},
		"workflow.architecture_spike": {"frame": {"frame_question"}},
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		steps, ok := gated[definition.Ref]
		if !ok {
			continue
		}
		modes := make(map[string]ActionExecutionMode, len(definition.ActionDefinitions))
		for _, action := range definition.ActionDefinitions {
			modes[action.ID] = action.ExecutionMode
		}
		for stepID, held := range steps {
			for _, action := range held {
				if !definitionStepAllows(definition, stepID, action) {
					t.Fatalf("%s step %s does not declare %s", definition.Ref, stepID, action)
				}
				if modes[action] != ActionHold {
					t.Fatalf("%s step %s action %s mode=%s, want %s", definition.Ref, stepID, action, modes[action], ActionHold)
				}
			}
		}
	}
}

// The conformance corpus must walk the gate, not around it. A research or
// spike frame walk without approve_contract records the defect this fix
// removes.
func TestWorkflowConformanceCorpusWalksApproveContract(t *testing.T) {
	raw, err := os.ReadFile("../../scenarios/workflow-engine.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Scenarios []struct {
			ID    string `json:"id"`
			Setup struct {
				EventHistory []struct {
					Kind    string `json:"kind"`
					WorkID  string `json:"work_id"`
					Payload struct {
						StepID   string `json:"step_id"`
						ActionID string `json:"action_id"`
					} `json:"payload"`
				} `json:"event_history"`
			} `json:"setup"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	framing := map[string]bool{"frame_research": true, "frame_question": true}
	walked := 0
	for _, scenario := range corpus.Scenarios {
		type stepKey struct{ work, step string }
		framed := make(map[stepKey]string)
		approved := make(map[stepKey]bool)
		for _, event := range scenario.Setup.EventHistory {
			if event.Kind != "workflow.action_completed" {
				continue
			}
			key := stepKey{work: event.WorkID, step: event.Payload.StepID}
			switch {
			case framing[event.Payload.ActionID]:
				framed[key] = event.Payload.ActionID
			case event.Payload.ActionID == "approve_contract":
				approved[key] = true
			}
		}
		for key, action := range framed {
			if !approved[key] {
				t.Fatalf("scenario %s walks %s at %s/%s without approve_contract", scenario.ID, action, key.work, key.step)
			}
			walked++
		}
	}
	if walked == 0 {
		t.Fatal("corpus contains no framing walk to check")
	}
}
