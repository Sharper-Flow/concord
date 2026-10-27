package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func oSeedReviewRoute(t *testing.T, workID, definitionRef, step string) workflowReturnRouteFixture {
	t.Helper()
	f := seedWorkflowReturnRouteFixture(t, workID, definitionRef, step)
	dir, _ := reviewSubjectClaim(t, f.store, workID)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "branch", "-m", branch)
	return f
}

func oDispatchJobBoundAttempt(t *testing.T, s *Store, workID, attemptID string, correction *WorkflowCorrectionContext, actor WorkflowActor, version int64, key string, job *WorkerJobBinding) {
	t.Helper()
	const stepID = "repair"
	if job.Digest == "" {
		recordWorkerJobRevisionForTest(t, s, workID, actor, job)
	}
	review, err := loadWorkflowReviewState(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if workflowAdmitReview(review, job.JobID) != nil {
		acceptCurrentReviewForBoundJobForTest(t, workContextFixture{store: s, workID: workID, owner: actor}, workerFixtureDirectory(t, s, workID), "continue", *job)
	}
	dispatchJobBoundAttempt(t, s, workID, stepID, attemptID, correction, actor, version, key, job)
}

// oAcceptOracleFixtureReview uses the oracle fixture's host Git runner for
// dispatch and the same observed commit in its stub terminal runner.
func oAcceptOracleFixtureReview(t *testing.T, f oracleRepairFixture, workID, disposition string) {
	t.Helper()
	attempt := oCompleteOracleFixtureReview(t, f, workID, disposition)
	j := &candidateJourney{fixture: workContextFixture{store: f.store, workID: workID, owner: f.owner}, s: f.store, workID: workID, repo: f.entry.Path}
	if err := j.acceptEvidence(t, attempt); err != nil {
		t.Fatal(err)
	}
}

func oCompleteOracleFixtureReview(t *testing.T, f oracleRepairFixture, workID, disposition string) string {
	t.Helper()
	s := f.store
	w := workContextFixture{store: s, workID: workID, owner: f.owner}
	job := reviewSubjectReviewJob(t, w)
	attempt := fmt.Sprintf("review:%s:%d", job.JobID, readWorkVersion(t, s, workID))
	laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, currentStep(t, s, workID), attempt, "review", laneDigest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, "review")
	correction, err := workflowCorrectionContextForDispatch(t.Context(), s.db, workID, currentStep(t, s, workID), attempt)
	if err != nil {
		t.Fatal(err)
	}
	if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
	}
	checkpoint, err := readLatestContextCheckpointTx(t.Context(), s.db, workID, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		packet["inputs"].(map[string]any)["checkpoint"] = checkpoint
	}
	prepared, err := PrepareWorkerDispatchFreshness(t.Context(), s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: f.entry.Path, Runner: f.git})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkerDispatchPreparation(context.Background(), prepared)
	if _, err := dispatchJoinAttempt(ctx, t, s, workID, readWorkVersion(t, s, workID), f.owner, packet); err != nil {
		t.Fatal(err)
	}
	j := &candidateJourney{fixture: w, s: s, workID: workID, repo: f.entry.Path}
	j.laneDispatch(t, attempt, "review", &job)
	if prepared.proof.observation == nil {
		t.Fatal("fixture Review has no host input observation")
	}
	eventID := attempt + ":completed"
	terminal, err := PrepareWorkerTerminalDelivery(t.Context(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attempt, EventID: eventID, Runner: observedDeltaRunner{head: prepared.proof.observation.Commit}})
	if err != nil {
		t.Fatal(err)
	}
	observation := terminal.proof.observation
	if observation == nil || observation.Commit != prepared.proof.observation.Commit {
		t.Fatal("fixture Review changed its input candidate")
	}
	candidate := WorkerReviewCandidate{ProjectID: "project", Commit: observation.Commit, Tree: observation.Tree}
	dependencies, err := workflowReviewDependencies(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	event := jobBoundCompletionEvent(workID, eventID, attempt, reviewGateLane(t, "review"), time.Now().UTC(), &job, f.entry.Path)
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Review = &WorkerReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies, Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}
	event.Payload = mustJSONValue(payload)
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), terminal), s, event)
	return attempt
}

func oProveReadyDelivery(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	f := oracleRepairFixtureFrom(t, workID, fixture)
	oAcceptOracleFixtureReview(t, f, workID, "ready_for_final_proof")
	req := verifyRequest(f.git, "ready-delivery-"+workID, []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
	req.WorkID, req.ProjectID, req.PrincipalRef, req.Now = workID, "project", f.owner.PrincipalRef, time.Now().UTC()
	run, err := f.store.VerifyWorktree(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": run.OperationRef, "producer_id": req.PrincipalRef, "producer_run_ref": run.OperationRef, "producer_watermark": req.RequestID}), 0, f.owner); err != nil {
		t.Fatal(err)
	}
}
