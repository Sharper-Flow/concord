package store

import "testing"

func TestAcceptedCriteriaPrecedeArchitecture(t *testing.T) {
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	for _, edge := range definition.StepGraph.Edges {
		if edge.From == "planning" && edge.Kind == WorkflowEdgeForward {
			if edge.To != "design" {
				t.Fatalf("accepted criteria advance to %q, want architecture before execution", edge.To)
			}
			return
		}
	}
	t.Fatal("accepted criteria have no forward architecture stage")
}

func TestArchitectureReviewGuardsFirstAndContinuingDispatch(t *testing.T) {
	report := reviewBlockFixture()
	report.Disposition = "continue"
	report.Findings = []WorkerReviewFinding{}
	report.Dependencies.ArchitectureNotApplicable = false
	report.Dependencies.ArchitectureDigest = testManifestDigest
	state := WorkflowReviewState{Dependencies: &report.Dependencies}
	if err := workflowAdmitReview(state, "job:first"); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("unreviewed architecture admitted first job: %v", err)
	}
	state.Candidate = &report.OutputCandidate
	state.Accepted = &WorkflowAcceptedReview{ReportRef: "review:accepted", Report: *report}
	state.DependenciesCurrent = true
	if err := workflowAdmitReview(state, "job:first"); err != nil {
		t.Fatalf("accepted current architecture Review refused: %v", err)
	}
	state.ExecutionProgressed = true
	state.Groups = map[string]workflowAssignmentGroup{"job:first": {ReviewRef: "review:accepted", Permission: "continue"}}
	state.DependenciesCurrent = false
	if err := workflowAdmitReview(state, "job:first"); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("existing group bypassed stale architecture Review: %v", err)
	}
	definition := mustBuiltinDefinition(t, "workflow.implementation").Definition
	admission := WorkflowAdmissionState{Step: "execution", InstanceState: "running", Lifecycle: "in_progress", ActiveContracts: 1,
		Review: &state, ReviewLane: "implementation", ReviewGroup: "job:first", ReviewCorrective: true}
	if decision := workflowAdmit(definition, admission, "dispatch_worker"); decision.Admitted || decision.Failure == nil || decision.Failure.Kind != KindMissingEvidence {
		t.Fatalf("corrective implementation bypassed stale architecture: %+v", decision)
	}
	if err := workflowAdmitReview(state, ""); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("execution exit bypassed stale architecture: %v", err)
	}
}

func TestReviewChangedJobRevisionWithdrawsContinuation(t *testing.T) {
	f := seedWorkContextFixture(t, "review-job-revision")
	t.Cleanup(func() { _ = f.store.Close() })
	repo, _ := reviewSubjectClaim(t, f.store, f.workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(f.workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	job := reviewSubjectReadyRevision(t, f)
	other := reviewSubjectReadyRevision(t, f)
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, f.workID)
	design := workflowEventWithActor("design:review-gate", WorkflowDesignRecorded, f.workID, actor, map[string]any{
		"work_id": f.workID, "expected_version": version, "resulting_version": version + 1,
		"approach":     "Keep the accepted value behind the existing owner boundary.",
		"decisions":    []map[string]any{{"id": "decision:boundary", "question": "Which owner handles the value?", "choice": "Keep its existing owner.", "rationale": "No new boundary is needed.", "rejected": []string{}}},
		"touched_refs": []string{"seed.txt"},
	})
	if err := applyWorkflowTestOperation(t.Context(), f.store, Operation{Events: []Event{design}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, f.workID): version}}); err != nil {
		t.Fatal(err)
	}
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	assertRecoveryRefusalWithoutEffect(t, f.store, f.workID, KindMissingEvidence, func() error {
		return descendantDispatch(t, j, "implement", "attempt:unreviewed-architecture", job, nil)
	})
	acceptCurrentReviewForTest(t, f, repo, "continue")
	before, err := loadWorkflowReviewState(t.Context(), f.store.db, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := workflowAdmitReview(before, job.JobID); err != nil {
		t.Fatalf("unchanged job lost Review authority: %v", err)
	}
	recordWorkerJobRevisionForTest(t, f.store, f.workID, f.owner, &job)
	if job.Revision != 2 {
		t.Fatalf("replacement job revision=%d, want 2", job.Revision)
	}
	assertRecoveryRefusalWithoutEffect(t, f.store, f.workID, KindMissingEvidence, func() error {
		return descendantDispatch(t, j, "implement", "attempt:revised-job", job, nil)
	})
	after, err := loadWorkflowReviewState(t.Context(), f.store.db, f.workID)
	if err != nil || !after.DependenciesCurrent || *before.Candidate != *after.Candidate {
		t.Fatalf("job revision invalidated unrelated architecture or candidate: %+v, %v", after, err)
	}
	if err := workflowAdmitReview(after, other.JobID); err != nil {
		t.Fatalf("job revision withdrew an unchanged assignment's authority: %v", err)
	}
	acceptCurrentReviewForTest(t, f, repo, "continue")
	if err := descendantDispatch(t, j, "implement", "attempt:reviewed-revised-job", job, nil); err != nil {
		t.Fatalf("fresh Review did not restore revised job authority: %v", err)
	}
}
