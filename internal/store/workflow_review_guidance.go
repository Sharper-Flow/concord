package store

// Recovery names the exact report coverage and disposition the gate requires.
func workflowReviewRecovery(prerequisites, acceptance string) string {
	next := "obtain independent Review through native Task, then have the coordinator record_review with the exact candidate, current dependencies, findings and disposition"
	if prerequisites != "" {
		next = prerequisites + "; then " + next
	}
	if acceptance != "" {
		next += " " + acceptance
	}
	return next + "; no separate agent or RTE session is required"
}
