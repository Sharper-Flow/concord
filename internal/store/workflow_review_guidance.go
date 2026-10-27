package store

// Prerequisites precede dispatch; acceptance names the report coverage and
// disposition the refusing gate actually requires.
func workflowReviewRecovery(prerequisites, acceptance string) string {
	next := "use typed dispatch_worker with lane_id=review for the existing scoped job and run a native concord-review Task subagent under Main's own session (finish an already dispatched undispositioned attempt only if it meets this gate's required coverage and disposition; do not dispatch twice), then have Main accept its exact Review report"
	if prerequisites != "" {
		next = prerequisites + "; then " + next
	}
	if acceptance != "" {
		next += " " + acceptance
	}
	return next + "; no separate agent or RTE session is required"
}
