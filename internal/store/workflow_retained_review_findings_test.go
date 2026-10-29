package store

import (
	"strings"
	"testing"
)

func TestRetainedReviewFindingsRemainVisibleAfterAcceptance(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		name := "crucial"
		if blocking {
			name = "blocking"
		}
		t.Run(name, func(t *testing.T) {
			j, job := retainedReviewFixture(t, "retained-finding-"+name)
			retainedReviewEdit(t, j, job, "implement", "failed-editor")
			job = retainedReviewRevision(t, j, []string{"seed.txt"})
			oracle, err := readWorkerJobOracle(t.Context(), j.s.db, j.workID, job)
			if err != nil || oracle == nil {
				t.Fatalf("recorded oracle: %+v, %v", oracle, err)
			}
			finding := WorkerReviewFinding{
				FindingID: "finding:retained", Severity: "P2", Confidence: "high",
				Detail:  "the retained correction still violates its bound obligation",
				InScope: true, Blocking: blocking, Status: "open",
				EvidenceRefs: []string{"evidence:retained"}, ClosureRefs: []string{},
				Oracle: &WorkerOracleFinding{
					Classification: OracleClassificationDeliveryBlocker, OwnerID: oracle.Owners[0].OwnerID,
					PredicateIDs: oracle.Owners[0].PredicateIDs, ControlIDs: []string{oracle.Controls[0].ControlID},
					EvidenceRefs: []string{"evidence:retained"},
				},
			}
			attempt, epoch := completeCurrentReviewForBoundJobForTest(t, j.fixture, j.repo, "ready_for_final_proof", job, func(report *WorkerReviewBlock) {
				report.Findings = []WorkerReviewFinding{finding}
			})
			assertFinding := func() WorkflowReviewState {
				t.Helper()
				state, err := loadWorkflowReviewState(t.Context(), j.s.db, j.workID)
				if err != nil || len(state.Findings) != 1 || reviewDigest(state.Findings[0]) != reviewDigest(finding) {
					t.Fatalf("retained finding disappeared or changed: %+v, %v", state.Findings, err)
				}
				return state
			}
			assertFinding()
			if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
				t.Fatalf("accept retained Review with open findings: %v", err)
			}
			state := assertFinding()
			// Exercise the exit rule using the facts the actual terminal and
			// acceptance writers retained; only the size input is synthetic.
			state.Crucial = !blocking
			if err := workflowAdmitReview(state, ""); !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), finding.FindingID) {
				t.Fatalf("open retained finding did not block exit: %v", err)
			}
		})
	}
}

func TestRetainedReviewFindingObservationKeepsCandidateBoundary(t *testing.T) {
	base := WorkerReviewCandidate{ProjectID: "project", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	input := WorkerReviewCandidate{ProjectID: "project", Commit: strings.Repeat("c", 40), Tree: strings.Repeat("d", 40)}
	for _, shape := range []string{"ordinary", "retained", "replaced base", "different input"} {
		t.Run(shape, func(t *testing.T) {
			state := WorkflowReviewState{Candidate: &base, Findings: []WorkerReviewFinding{}}
			report := reviewBlockFixture()
			report.InputCandidate = input
			auth := workflowActionCompletedPayload{}
			if shape != "ordinary" {
				auth.WorkerRetainedReview = &retainedReviewProof{State: retainedReviewState{Candidate: base}, Input: input}
			}
			if shape == "replaced base" {
				auth.WorkerRetainedReview.State.Candidate.Commit = strings.Repeat("e", 40)
			}
			if shape == "different input" {
				auth.WorkerRetainedReview.Input.Tree = strings.Repeat("f", 40)
			}
			observeWorkflowReviewReport(&state, auth, report)
			want := 0
			if shape == "retained" {
				want = len(report.Findings)
			}
			if len(state.Findings) != want || *state.Candidate != base {
				t.Fatalf("observation has %d findings and candidate %+v; want %d and unchanged base", len(state.Findings), state.Candidate, want)
			}
		})
	}
}

func TestRetainedReviewReadyForFinalProofSatisfiesJob(t *testing.T) {
	j, job := retainedReviewFixture(t, "retained-ready-for-proof")
	head := retainedReviewEdit(t, j, job, "implement", "failed-editor")
	job = retainedReviewRevision(t, j, []string{"seed.txt"})
	attempt, epoch := completeCurrentReviewForBoundJobForTest(t, j.fixture, j.repo, "ready_for_final_proof", job)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
		t.Fatalf("accept retained ready_for_final_proof: %v", err)
	}
	state, err := readWorkflowReviewFacts(t.Context(), j.s.db, j.workID)
	if err != nil || state.Candidate == nil || state.Candidate.Commit != head || state.Accepted == nil || state.Accepted.AttemptID != attempt {
		t.Fatalf("accepted retained Review: %+v, %v", state, err)
	}
	var satisfied, accepted string
	if err := j.s.db.QueryRow(`SELECT j.satisfied_result_ref, d.event_id FROM worker_job_revisions j
 JOIN worker_attempt_dispositions d ON d.work_id=j.work_id AND d.attempt_id=? AND d.action_id='accept_worker_result'
 WHERE j.work_id=? AND j.job_id=? AND j.revision=? AND j.digest=? AND j.state='satisfied'`,
		attempt, j.workID, job.JobID, job.Revision, job.Digest).Scan(&satisfied, &accepted); err != nil || satisfied == "" || satisfied != accepted {
		t.Fatalf("job satisfaction=%q, accepted disposition=%q, err=%v", satisfied, accepted, err)
	}
}

// A satisfied latest revision whose reference no longer names its current
// disposition row (a repeated acceptance moves only the row) is committed
// state, not this acceptance's pending satisfaction.
func TestRetainedReviewAcceptanceIgnoresCommittedSatisfaction(t *testing.T) {
	for _, disposition := range []string{"continue", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			j, job := retainedReviewFixture(t, "retained-committed-satisfaction-"+disposition)
			retainedReviewEdit(t, j, job, "implement", "failed-editor")
			if err := recordWorkerJobFieldsForTest(t, j.s, j.workID, j.fixture.owner, map[string]any{
				"job_id": "job:other", "objective": "Other accepted work", "stopping_condition": "other passes",
				"checks": []string{"go test ./internal/store/"}, "ready": true, "readiness_evidence": []string{"evidence:coordinator-ready"}, "path_scope": []string{"other.txt"},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := j.s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE worker_job_revisions SET state='satisfied',satisfied_result_ref='first-acceptance-event',satisfied_seq=1 WHERE work_id=? AND job_id='job:other'; DELETE FROM write_guard`, j.workID); err != nil {
				t.Fatal(err)
			}
			job = retainedReviewRevision(t, j, []string{"seed.txt"})
			attempt, epoch := completeCurrentReviewForBoundJobForTest(t, j.fixture, j.repo, disposition, job)
			if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
				t.Fatalf("accept retained Review beside committed satisfaction: %v", err)
			}
		})
	}
}
