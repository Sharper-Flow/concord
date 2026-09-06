package store

import (
	"encoding/json"
	"os"
	"testing"
)

// An approval gate only gates when it is the step's only exit. A step that
// holds an approval-required advancing action beside a second advancing action
// lets the first call leave the step before the gate runs, so the gate never
// runs. This holds for every built-in definition and every step.
func TestApprovalGateStepHasNoOtherAdvancingAction(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		modes := make(map[string]ActionExecutionMode, len(definition.ActionDefinitions))
		approvals := make(map[string]ActionApproval, len(definition.ActionDefinitions))
		for _, action := range definition.ActionDefinitions {
			modes[action.ID] = action.ExecutionMode
			approvals[action.ID] = action.Approval
		}
		for _, step := range definition.StepGraph.Steps {
			var gates, advancing []string
			for _, action := range step.Actions {
				if modes[action] != ActionAdvance {
					continue
				}
				advancing = append(advancing, action)
				if approvals[action] == ActionApprovalRequired {
					gates = append(gates, action)
				}
			}
			if len(gates) != 0 && len(advancing) > len(gates) {
				t.Errorf("%s step %s gates on %v but also advances on %v", definition.Ref, step.ID, gates, advancing)
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
