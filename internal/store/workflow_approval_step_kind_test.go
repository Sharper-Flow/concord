package store

import "testing"

// TestShippedStepsAdmitTheirApprovalRequiredActions holds the invariant that
// stranded workflow.ops_runbook at its cleanup step: a step may declare an
// approval-required action only when its kind can serve the operator question
// that approval consumes. workflowOperatorQuestionTx refuses the question on
// any step that is not a human checkpoint, so a step of another kind declaring
// such an action offers a route no caller can take.
//
// The scope is the shipped set. Frozen prior versions stay byte-identical by
// law, and three of them violate this rule, so the registry constructor cannot
// carry the check without rejecting history it must keep.
func TestShippedStepsAdmitTheirApprovalRequiredActions(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		approvalRequired := map[string]bool{}
		for _, action := range definition.ActionDefinitions {
			if action.Approval == ActionApprovalRequired {
				approvalRequired[action.ID] = true
			}
		}
		for _, step := range definition.StepGraph.Steps {
			if step.Kind == WorkflowStepHumanCheckpoint {
				continue
			}
			for _, id := range step.Actions {
				if approvalRequired[id] {
					t.Errorf("%s v%d step %q has kind %q and declares approval-required action %q; the operator question that approval consumes is served only from a human checkpoint, so the action can never run", definition.Ref, definition.Version, step.ID, step.Kind, id)
				}
			}
		}
	}
}

// TestShippedNonTerminalStepsCanAdvance proves the consequence the invariant
// above protects. A non-terminal step whose every advancing action is refused
// by its kind is a dead end, and every work item that reaches it stops.
func TestShippedNonTerminalStepsCanAdvance(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		terminal := map[string]bool{}
		for _, id := range definition.StepGraph.TerminalSteps {
			terminal[id] = true
		}
		effect := map[string]WorkflowActionDefinition{}
		for _, action := range definition.ActionDefinitions {
			effect[action.ID] = action
		}
		for _, step := range definition.StepGraph.Steps {
			if terminal[step.ID] {
				continue
			}
			reachable := false
			for _, id := range step.Actions {
				action, ok := effect[id]
				if !ok || action.ExecutionMode != ActionAdvance {
					continue
				}
				if action.Approval == ActionApprovalRequired && step.Kind != WorkflowStepHumanCheckpoint {
					continue
				}
				reachable = true
				break
			}
			if !reachable {
				t.Errorf("%s v%d step %q declares no advancing action its kind %q admits; work that reaches this step cannot leave it", definition.Ref, definition.Version, step.ID, step.Kind)
			}
		}
	}
}
