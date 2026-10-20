package store

// RetryWallV2AdmissionFixture exercises the production dispatch diagnostic
// gate over a folded state. Recorded-history derivation and basis consumption
// remain separate integration obligations, not claims this fixture makes.
func RetryWallV2AdmissionFixture(step string, nonProgress int64) WorkflowAdmissionDecision {
	state := WorkflowAdmissionState{
		Step: step, NonProgressAttempts: nonProgress, CorrectionEscalated: true,
		LatestResultDisposition: "accept_worker_result", ActiveContracts: 1,
	}
	var decision WorkflowAdmissionDecision
	workflowAdmitDispatchRefused(WorkflowDefinition{}, state, "dispatch_worker", &decision)
	return decision
}
