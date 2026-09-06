package store

// historicalWorkflowDefinitions returns the version-1 definitions shipped
// before CD-0112. It derives the unchanged definition data and reverses only
// the versioned policy changes, so old pins retain their original vocabulary.
func historicalWorkflowDefinitions() []WorkflowDefinition {
	definitions := BuiltinWorkflowDefinitions()
	for i := range definitions {
		definition := &definitions[i]
		definition.Version = 1
		definition.AvailableActions = withoutWorkflowAction(definition.AvailableActions, "record_delivery")
		definition.ActionDefinitions = withoutWorkflowActionDefinitions(definition.ActionDefinitions, "record_delivery")
		for stepIndex := range definition.StepGraph.Steps {
			step := &definition.StepGraph.Steps[stepIndex]
			step.Actions = withoutWorkflowAction(step.Actions, "record_delivery")
		}
		for actionIndex := range definition.ActionDefinitions {
			action := &definition.ActionDefinitions[actionIndex]
			if action.ID == "cross_context_boundary" {
				action.ExecutionMode = ActionAdvance
			}
			if definition.Ref == "workflow.research" && (action.ID == "frame_research" || action.ID == "record_conclusion") {
				action.ExecutionMode = ActionAdvance
			}
		}
		if definition.Ref == "workflow.architecture_spike" {
			restoreArchitectureSpikeHistory(definition)
		}
	}
	return definitions
}

func withoutWorkflowAction(actions []string, unwanted string) []string {
	kept := make([]string, 0, len(actions))
	for _, action := range actions {
		if action != unwanted {
			kept = append(kept, action)
		}
	}
	return kept
}

func withoutWorkflowActionDefinitions(actions []WorkflowActionDefinition, unwanted string) []WorkflowActionDefinition {
	kept := make([]WorkflowActionDefinition, 0, len(actions))
	for _, action := range actions {
		if action.ID != unwanted {
			kept = append(kept, action)
		}
	}
	return kept
}

func restoreArchitectureSpikeHistory(definition *WorkflowDefinition) {
	for stepIndex := range definition.StepGraph.Steps {
		step := &definition.StepGraph.Steps[stepIndex]
		if step.ID == "review" {
			step.Actions = withoutWorkflowAction(step.Actions, "accept_decision")
		}
		if step.ID == "acceptance" && !containsString(step.Actions, "accept_decision") {
			step.Actions = append([]string{"accept_decision"}, step.Actions...)
		}
	}
	for actionIndex := range definition.ActionDefinitions {
		action := &definition.ActionDefinitions[actionIndex]
		switch action.ID {
		case "record_decision":
			action.ExecutionMode = ActionCheckpoint
		case "accept_decision":
			action.ExecutionMode = ActionHold
		}
	}
}
