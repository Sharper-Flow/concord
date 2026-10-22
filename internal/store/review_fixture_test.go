package store

import (
	"context"
	"testing"
	"time"
)

// acceptCurrentReviewForTest enters through the dispatch, host terminal, and
// Main acceptance owners. Candidate identities come from the real checkout;
// this helper never installs a claimed output observation or acceptance row.
func acceptCurrentReviewForTest(t *testing.T, f workContextFixture, repo, disposition string) {
	t.Helper()
	attempt, epoch := completeCurrentReviewForTest(t, f, repo, disposition)
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, f.owner); err != nil {
		t.Fatalf("Main acceptance of fixture Review: %v", err)
	}
}

func completeCurrentReviewForTest(t *testing.T, f workContextFixture, repo, disposition string) (string, int64) {
	t.Helper()
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	job := reviewSubjectReadyRevision(t, f)
	attempt := "review:fixture:" + job.JobID
	if err := descendantDispatch(t, j, "review", attempt, job, nil); err != nil {
		t.Fatalf("dispatch fixture Review: %v", err)
	}
	j.laneDispatch(t, attempt, "review", &job)
	window := reviewSubjectWindow(t, f.store, f.workID, attempt)
	candidate := WorkerReviewCandidate{ProjectID: "project", Commit: window.SubjectCommit, Tree: reviewObservedTree(t, repo, window.SubjectCommit)}
	dependencies, err := workflowReviewDependencies(t.Context(), f.store.db, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	report := &WorkerReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies,
		Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}
	lane := reviewGateLane(t, "review")
	evidence := []WorkerReportEvidence{}
	for _, obligation := range lane.EvidenceObligations {
		if obligation != "severity" {
			evidence = append(evidence, WorkerReportEvidence{Obligation: obligation, Detail: "Reviewed the fixture's current candidate."})
		}
	}
	event := Event{EventID: attempt + ":completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "worker:test", OccurredAt: time.Now().UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted),
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: repo, Evidence: evidence, EvidenceOrigin: WorkerEvidenceReported, WorkerJob: &job, Review: report})}
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), f.store, PrepareWorkerTerminalDeliveryRequest{WorkID: f.workID, AttemptID: attempt, EventID: event.EventID})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), f.store, event)
	return attempt, window.AttemptEpoch
}
