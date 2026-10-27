package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// acceptCurrentReviewForTest enters through the dispatch, host terminal, and
// Main acceptance owners. Candidate identities come from the real checkout;
// this helper never installs a claimed output observation or acceptance row.
func acceptCurrentReviewForTest(t *testing.T, f workContextFixture, repo, disposition string) {
	t.Helper()
	acceptCurrentReviewForBoundJobForTest(t, f, repo, disposition, reviewSubjectReadyRevision(t, f))
}

func acceptCurrentReviewForBoundJobForTest(t *testing.T, f workContextFixture, repo, disposition string, job WorkerJobBinding) {
	t.Helper()
	attempt, epoch := completeCurrentReviewForBoundJobForTest(t, f, repo, disposition, job)
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, f.owner); err != nil {
		t.Fatalf("Main acceptance of fixture Review: %v", err)
	}
}

func completeCurrentReviewForTest(t *testing.T, f workContextFixture, repo, disposition string, mutations ...func(*WorkerReviewBlock)) (string, int64) {
	t.Helper()
	return completeCurrentReviewForBoundJobForTest(t, f, repo, disposition, reviewSubjectReadyRevision(t, f), mutations...)
}

func completeCurrentReviewForBoundJobForTest(t *testing.T, f workContextFixture, repo, disposition string, job WorkerJobBinding, mutations ...func(*WorkerReviewBlock)) (string, int64) {
	t.Helper()
	j := &candidateJourney{fixture: f, s: f.store, workID: f.workID, repo: repo}
	attempt := fmt.Sprintf("review:fixture:%d", readWorkVersion(t, f.store, f.workID))
	if err := descendantDispatch(t, j, "review", attempt, job, nil); err != nil {
		t.Fatalf("dispatch fixture Review: %v", err)
	}
	j.laneDispatch(t, attempt, "review", &job)
	window := reviewSubjectWindow(t, f.store, f.workID, attempt)
	report := currentReviewBlockForTest(t, f.store, f.workID, attempt, disposition)
	for _, mutate := range mutations {
		mutate(report)
	}
	lane := reviewGateLane(t, "review")
	evidence := []WorkerReportEvidence{}
	for _, obligation := range lane.EvidenceObligations {
		if obligation != "severity" {
			evidence = append(evidence, WorkerReportEvidence{Obligation: obligation, Detail: "Reviewed the fixture's current candidate."})
		}
	}
	event := Event{EventID: attempt + ":completed", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: f.workID, Actor: "worker:test", OccurredAt: time.Now().UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted),
		Payload: mustJSONValue(WorkerCompletedPayload{AttemptID: attempt, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion, WorkerDirectory: repo, Evidence: evidence, EvidenceOrigin: WorkerEvidenceReported, WorkerJob: &job, Review: report})}
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), f.store, PrepareWorkerTerminalDeliveryRequest{WorkID: f.workID, AttemptID: attempt, EventID: event.EventID})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), f.store, event)
	return attempt, window.AttemptEpoch
}

func currentReviewBlockForTest(t *testing.T, s *Store, workID, attempt, disposition string) *WorkerReviewBlock {
	t.Helper()
	repo := workerFixtureDirectory(t, s, workID)
	window := reviewSubjectWindow(t, s, workID, attempt)
	commit := window.SubjectCommit
	if commit == "" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		raw, err := (ExecGitRunner{}).Run(ctx, repo, "rev-parse", "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		commit = strings.TrimSpace(string(raw))
	}
	var project string
	if err := s.db.QueryRowContext(t.Context(), `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		t.Fatal(err)
	}
	candidate := WorkerReviewCandidate{ProjectID: project, Commit: commit, Tree: reviewObservedTree(t, repo, commit)}
	dependencies, err := workflowReviewDependencies(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	return &WorkerReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies,
		Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}
}
