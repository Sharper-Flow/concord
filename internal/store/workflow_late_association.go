package store

import "slices"

func workflowLateCriterionAssociationStep(definition WorkflowDefinition, stepID string) bool {
	return !stepDeclaresAction(definition, stepID, "record_verdict") && workflowLateVerdictServesVerdictStep(definition, stepID)
}

func workflowLateVerdictServesVerdictStep(definition WorkflowDefinition, stepID string) bool {
	terminal := slices.Contains(definition.StepGraph.TerminalSteps, stepID)
	if !terminal && !(stepDeclaresAction(definition, stepID, "confirm_premise") && !stepDeclaresAction(definition, stepID, "record_verdict")) {
		return false
	}
	for _, candidate := range definition.StepGraph.Steps {
		if slices.Contains(candidate.Actions, "record_verdict") && workflowStepFollows(definition, candidate.ID, stepID) {
			return true
		}
	}
	return false
}
