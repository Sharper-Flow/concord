package store

// researchContextSelectionField declares the optional selected-research
// action input on record_work_context: an array of the closed selection
// tuples, sharing the generic finding-reference bound. Absent and empty both
// select nothing.
func researchContextSelectionField() WorkflowPayloadField {
	return actionItemArrayField("research_finding_refs", false, 0, WorkContextFindingRefsMax, "work_context_research_selection")
}

var researchContextSelection = withAppendedPayloadField(researchContextSelectionField)
