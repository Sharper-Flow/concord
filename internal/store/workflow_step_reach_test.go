package store

import (
	"slices"
	"testing"
)

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
			reachable := false
			for _, id := range step.Actions {
				if _, ok := effect[id]; !ok || !workflowActionAdvancesStep(definition, id) {
					continue
				}
				reachable = true
				break
			}
			if !reachable {
				t.Errorf("%s step %q declares no advancing action it admits; work that reaches this step cannot leave it", definition.Ref, step.ID)
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
			t.Errorf("%s requires evidence kinds %v its reachable actions cannot produce; those actions produce %v", definition.Ref, missing, producible)
		}
	}
}
