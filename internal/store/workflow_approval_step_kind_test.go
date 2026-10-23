package store

import (
	"slices"
	"testing"
)

// A step may declare an approval-required action only when its operator
// question serves that approval. Correction requests instead use the folded
// recovery route under consequence_v1.
func TestRegisteredStepsAdmitTheirApprovalRequiredActions(t *testing.T) {
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
			served, _ := workflowOperatorQuestionAction(definition, step.ID)
			for _, id := range step.Actions {
				if id == "request_correction" && (workflowStepIsDeliveryGate(&step) || workflowUnhealthyVerdictRouteTarget(definition, step.ID) != "") {
					continue
				}
				if approvalRequired[id] && id != served {
					t.Errorf("%s v%d step %q has kind %q and declares approval-required action %q that its operator question does not serve, so the action can never run", definition.Ref, definition.Version, step.ID, step.Kind, id)
				}
			}
		}
	}
}

// A non-terminal step whose every advancing action is refused is a dead end.
func TestRegisteredNonTerminalStepsCanAdvance(t *testing.T) {
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
			served, _ := workflowOperatorQuestionAction(definition, step.ID)
			reachable := false
			for _, id := range step.Actions {
				action, ok := effect[id]
				if !ok || !workflowActionAdvancesStep(definition, id) {
					continue
				}
				if action.Approval == ActionApprovalRequired && step.Kind != WorkflowStepHumanCheckpoint && id != served {
					continue
				}
				reachable = true
				break
			}
			if !reachable {
				t.Errorf("%s v%d step %q declares no advancing action it admits; work that reaches this step cannot leave it", definition.Ref, definition.Version, step.ID)
			}
		}
	}
}

// workflowRequiredEvidenceOutsideReach returns required evidence kinds that
// no action reachable through forward and optional edges can produce.
func workflowRequiredEvidenceOutsideReach(definition WorkflowDefinition) []EvidenceKind {
	producible := evidenceStrings(workflowReachableEvidenceKinds(definition))
	var missing []EvidenceKind
	for _, kind := range definition.RequiredEvidenceKinds {
		if !slices.Contains(producible, string(kind)) {
			missing = append(missing, kind)
		}
	}
	for _, step := range definition.StepGraph.Steps {
		for _, kind := range step.RequiredEvidenceKinds {
			if !slices.Contains(producible, string(kind)) {
				missing = append(missing, kind)
			}
		}
	}
	for _, rule := range definition.RigorRules {
		for _, kind := range rule.RequiredEvidenceKinds {
			if !slices.Contains(producible, string(kind)) {
				missing = append(missing, kind)
			}
		}
	}
	return missing
}

func TestShippedDefinitionsCanProduceRequiredEvidence(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		if missing := workflowRequiredEvidenceOutsideReach(definition); len(missing) != 0 {
			producible := evidenceStrings(workflowReachableEvidenceKinds(definition))
			t.Errorf("%s v%d requires evidence kinds %v its reachable actions cannot produce; those actions produce %v", definition.Ref, definition.Version, missing, producible)
		}
	}
}
