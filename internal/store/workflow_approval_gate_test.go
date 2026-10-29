package store

import "testing"

// An approval gate only gates when it is the step's only exit. A step that
// holds an approval-required advancing action beside a second advancing action
// lets the first call leave the step before the gate runs, so the gate never
// runs. This holds for every built-in definition and every step.
func TestApprovalGateStepHasNoOtherAdvancingAction(t *testing.T) {
	t.Parallel()
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
