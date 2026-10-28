package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func retainedReviewFixture(t *testing.T, id string) (*candidateJourney, WorkerJobBinding) {
	t.Helper()
	f := seedWorkContextFixture(t, id)
	t.Cleanup(func() { f.store.Close() })
	dir, _ := reviewSubjectClaim(t, f.store, id)
	var branch string
	if err := f.store.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active'`, WorktreeSetID(id)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "branch", "-m", branch)
	j := &candidateJourney{fixture: f, s: f.store, workID: id, repo: dir}
	j.observeRun(t, "baseline")
	job := retainedReviewRevision(t, j, []string{"seed.txt"})
	acceptCurrentReviewForBoundJobForTest(t, f, dir, "continue", job)
	return j, job
}

func retainedReviewRevision(t *testing.T, j *candidateJourney, paths []string) WorkerJobBinding {
	t.Helper()
	const jobID = "job:retained-review"
	if err := recordWorkerJobFieldsForTest(t, j.s, j.workID, j.fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Retain the scoped correction", "stopping_condition": "the scoped correction passes review",
		"checks": []string{"go test ./internal/store/"}, "ready": true, "readiness_evidence": []string{"evidence:coordinator-ready"}, "path_scope": paths,
	}); err != nil {
		t.Fatal(err)
	}
	jobs, err := readyWorkerJobRevisions(t.Context(), j.s.db, j.workID)
	if err != nil || len(jobs) != 1 || jobs[0].Binding.JobID != jobID {
		t.Fatalf("sole ready revision = %+v, err=%v", jobs, err)
	}
	return jobs[0].Binding
}

func retainedReviewFail(t *testing.T, j *candidateJourney, attempt, capability, kind string) {
	t.Helper()
	event := Event{EventID: attempt + ":failed", Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: j.workID, Actor: "worker:test", OccurredAt: time.Now().UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerFailed),
		Payload: mustJSONValue(WorkerFailedPayload{AttemptID: attempt, ReadbackModel: preferredModelForLane(reviewGateLane(t, capability)), FailureKind: kind, Detail: "synthetic retained editing failure", FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: attributionHostObservation()}})}
	workerTerminalEventThroughSeam(t, j.s, event)
}

func retainedReviewEdit(t *testing.T, j *candidateJourney, job WorkerJobBinding, lane, attempt string) string {
	t.Helper()
	if err := descendantDispatch(t, j, lane, attempt, job, nil); err != nil {
		t.Fatalf("editing authorization: %v", err)
	}
	capability := testLaneCapabilityClass(lane)
	j.laneDispatch(t, attempt, capability, &job)
	head := descendantCommit(t, j, attempt)
	retainedReviewFail(t, j, attempt, capability, WorkerFailureWorkerError)
	return head
}

func TestRetainedFailedEditingReviewRecovery(t *testing.T) {
	for _, lane := range []string{"implement", "review"} {
		t.Run(lane, func(t *testing.T) {
			j, job := retainedReviewFixture(t, "retained-positive-"+lane)
			baseline, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
			if err != nil {
				t.Fatal(err)
			}
			head := retainedReviewEdit(t, j, job, lane, "failed-editor")
			var failedBefore string
			if err := j.s.db.QueryRow(`SELECT terminal_payload FROM worker_attempts WHERE work_id=? AND attempt_id='failed-editor'`, j.workID).Scan(&failedBefore); err != nil {
				t.Fatal(err)
			}
			job = retainedReviewRevision(t, j, []string{"seed.txt"})
			attempt, epoch := completeCurrentReviewForBoundJobForTest(t, j.fixture, j.repo, "continue", job)
			auth, result, err := readReviewAttempt(t.Context(), j.s.db, j.workID, attempt)
			if err != nil {
				t.Fatal(err)
			}
			proof := auth.WorkerRetainedReview
			if proof == nil || proof.Input.Commit != head || proof.State.Candidate != *baseline.Candidate || !sameStringSet(proof.ChangedPaths, []string{"seed.txt"}) || len(proof.State.Editors) != 1 || proof.State.Editors[0].Job.Revision != 1 {
				t.Fatalf("authenticated retained proof = %+v", proof)
			}
			current, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
			if err != nil || reviewDigest(current.Candidate) != reviewDigest(baseline.Candidate) || reviewDigest(current.Accepted) != reviewDigest(baseline.Accepted) {
				t.Fatalf("terminal promoted retained input: %+v, err=%v", current, err)
			}
			if err := validateReviewAssignmentAcceptance(t.Context(), j.s.db, j.workID, auth, result.Review); err != nil {
				t.Fatalf("SQL-only retained acceptance: %v", err)
			}
			if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
				t.Fatalf("accept fresh Review: %v", err)
			}
			current, err = readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
			if err != nil || current.Candidate.Commit != head || current.Accepted.AttemptID != attempt {
				t.Fatalf("accepted candidate = %+v, err=%v", current, err)
			}
			var failedAfter string
			if err := j.s.db.QueryRow(`SELECT terminal_payload FROM worker_attempts WHERE work_id=? AND attempt_id='failed-editor'`, j.workID).Scan(&failedAfter); err != nil || failedBefore != failedAfter {
				t.Fatalf("failed bytes changed: %v", err)
			}
		})
	}
}

func TestRetainedFailedEditingReviewCanRepair(t *testing.T) {
	j, job := retainedReviewFixture(t, "retained-repair-output")
	input := retainedReviewEdit(t, j, job, "implement", "failed-editor")
	job = retainedReviewRevision(t, j, []string{"seed.txt"})
	output := ""
	attempt, epoch := completeCurrentReviewForBoundJobForTest(t, j.fixture, j.repo, "continue", job, func(report *WorkerReviewBlock) {
		output = descendantCommit(t, j, "fresh-review-repair")
		report.OutputCandidate = WorkerReviewCandidate{ProjectID: report.InputCandidate.ProjectID, Commit: output, Tree: reviewObservedTree(t, j.repo, output)}
	})
	if input == output {
		t.Fatal("Review did not repair the retained input")
	}
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
		t.Fatalf("accept scoped repairing Review: %v", err)
	}
	current, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil || current.Candidate.Commit != output {
		t.Fatalf("repair output not accepted: %+v, err=%v", current, err)
	}
}

func TestRetainedFailedEditingReviewBounds(t *testing.T) {
	for _, shape := range []string{"dirty", "nonancestor", "review_scope", "failed_scope", "empty_scope", "mixed_rejected", "completed_unaccepted", "in_flight", "abandoned", "multiple_failed"} {
		t.Run(shape, func(t *testing.T) {
			j, job := retainedReviewFixture(t, "retained-bound-"+shape)
			retainedReviewEdit(t, j, job, "implement", "failed-editor")
			paths := []string{"seed.txt"}
			if shape == "review_scope" {
				paths = []string{"other.txt"}
			}
			if shape == "empty_scope" {
				paths = []string{}
			}
			if shape == "failed_scope" {
				if err := os.WriteFile(filepath.Join(j.repo, "other.txt"), []byte("outside failed scope\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				j.commit(t, "worker@example.invalid", "outside failed scope")
				j.observeRun(t, "outside-failed")
				paths = []string{"seed.txt", "other.txt"}
			}
			if shape == "nonancestor" {
				raw, err := (ExecGitRunner{}).Run(t.Context(), j.repo, "commit-tree", reviewObservedTree(t, j.repo, j.pinned(t)), "-m", "unrelated tree")
				if err != nil {
					t.Fatal(err)
				}
				gitRunStore(t, j.repo, "reset", "--hard", strings.TrimSpace(string(raw)))
				j.observeRun(t, "unrelated")
			}
			job = retainedReviewRevision(t, j, paths)
			if shape == "dirty" {
				if err := os.WriteFile(filepath.Join(j.repo, "seed.txt"), []byte("dirty\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if shape == "mixed_rejected" || shape == "completed_unaccepted" || shape == "in_flight" || shape == "abandoned" || shape == "multiple_failed" {
				if err := descendantDispatch(t, j, "review", "second-editor", job, nil); err != nil {
					t.Fatalf("second editor: %v", err)
				}
				j.laneDispatch(t, "second-editor", "review", &job)
				switch shape {
				case "abandoned", "multiple_failed":
					kind := WorkerFailureWorkerError
					if shape == "abandoned" {
						kind = WorkerFailureAbandoned
					}
					retainedReviewFail(t, j, "second-editor", "review", kind)
				case "mixed_rejected", "completed_unaccepted":
					j.completeReview(t, "second-editor", job, currentReviewBlockForTest(t, j.s, j.workID, "second-editor", "continue"))
					if shape == "mixed_rejected" {
						rejector := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/rejector", SessionRef: "session/rejector", ActorClass: ActorAgent}
						if err := j.action(t, "reject_worker_result", map[string]any{"attempt_id": "second-editor", "attempt_epoch": j.dispatchEpoch(t, "second-editor"), "diagnosis": "the result does not discharge the obligation", "strategy": "correct the same obligation", "predicate_ids": []string{"predicate:work-context"}, "evidence_refs": []string{"candidate-completed-second-editor"}}, rejector); err != nil {
							t.Fatal(err)
						}
					}
				}
				job = retainedReviewRevision(t, j, paths)
			}
			before := j.effects(t)
			err := descendantDispatch(t, j, "review", "fresh-review", job, nil)
			if shape == "abandoned" || shape == "multiple_failed" {
				if err != nil {
					t.Fatalf("all-failed chain refused: %v", err)
				}
				return
			}
			want := KindStaleRequiresReview
			if shape == "dirty" {
				want = KindProjectionConflict
			}
			if shape == "in_flight" {
				want = KindOperationConflict
			}
			if failureKind(err) != want {
				t.Fatalf("bounded retained refusal = %v, want %s", err, want)
			}
			var failure *Failure
			if errors.As(err, &failure) && failure.Detail == "Review input differs from Main's current integrated candidate" {
				if !strings.Contains(failure.RecoveryAction, "no admitted retained-input recovery exists") || !strings.Contains(failure.RecoveryAction, "maintainer investigation") || strings.Contains(failure.RecoveryAction, "qualify Main's accepted current candidate") {
					t.Fatalf("refusal prescribes an unavailable recovery: %s", failure.RecoveryAction)
				}
			}
			if j.effects(t) != before {
				t.Fatal("refusal changed durable state")
			}
		})
	}
}

func TestRetainedReviewAcceptanceDrift(t *testing.T) {
	for _, shape := range []string{"new_editor", "new_acceptance", "new_revision", "report_input", "contract"} {
		t.Run(shape, func(t *testing.T) {
			j, job := retainedReviewFixture(t, "retained-accept-drift-"+shape)
			retainedReviewEdit(t, j, job, "implement", "failed-editor")
			job = retainedReviewRevision(t, j, []string{"seed.txt"})
			attempt, _ := completeCurrentReviewForBoundJobForTest(t, j.fixture, j.repo, "continue", job)
			auth, result, err := readReviewAttempt(t.Context(), j.s.db, j.workID, attempt)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateReviewAssignmentAcceptance(t.Context(), j.s.db, j.workID, auth, result.Review); err != nil {
				t.Fatal(err)
			}
			switch shape {
			case "new_revision":
				retainedReviewRevision(t, j, []string{"seed.txt"})
			case "contract":
				reviewSubjectSupersedeContract(t, j.s, j.workID)
			case "report_input":
				result.Review.InputCandidate.Tree = strings.Repeat("f", 40)
			case "new_acceptance":
				if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": j.dispatchEpoch(t, attempt)}, j.fixture.owner); err != nil {
					t.Fatal(err)
				}
			case "new_editor":
				// Exercise acceptance's own population check independently of the
				// dispatcher, which also refuses completed-unaccepted editors.
				later := auth
				later.WorkerRetainedReview = nil
				later.WorkerAttemptID = "later-failed"
				version := readWorkVersion(t, j.s, j.workID)
				next := version + 1
				later.WorkflowVersionFields = WorkflowVersionFields{WorkID: j.workID, ExpectedVersion: &version, ResultingVersion: &next}
				event := Event{EventID: "later-editor-authorization", Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: j.workID, Actor: auth.ActorRef, OccurredAt: time.Now().UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkflowActionCompleted), Payload: mustJSONValue(later)}
				if err := applyWorkflowTestOperation(t.Context(), j.s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, j.workID): version}}); err != nil {
					t.Fatal(err)
				}
				j.laneDispatch(t, "later-failed", "review", &job)
				retainedReviewFail(t, j, "later-failed", "review", WorkerFailureWorkerError)
			}
			if err := validateReviewAssignmentAcceptance(context.Background(), j.s.db, j.workID, auth, result.Review); err == nil {
				raw, _ := json.Marshal(auth.WorkerRetainedReview)
				t.Fatalf("stale proof accepted: %s", raw)
			}
		})
	}
}
