package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Deterministic witnesses of the approved worker-job obligations (CD-0205),
// each derived from an independent review probe: one bounded objective per
// dispatch, one disposition per local acceptance, and an unresolved failed
// job whose correction source and failed-attempt identity survive unrelated
// success. They stay as repository tests so the obligations they prove
// cannot regress silently.

// A rejected completed result leaves its dispatched job unresolved exactly as
// a recorded failure does (CD-0205): a later unrelated acceptance cannot
// discharge the failed job's obligation.
func TestReviewRejectedJobWindow(t *testing.T) {
	const id = "review-rejected-job"
	f := oSeedReviewRoute(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	lane := BuiltinLaneDefinitions()[0]
	acceptor := reviewGateAcceptor(id)
	job := &WorkerJobBinding{JobID: "job:corrective-rejected", Revision: 1}
	attempt := "attempt:" + id + ":1"
	dispatchJobBoundAttempt(t, s, id, "repair", attempt, nil, f.owner, 0, "review-reject-1", job)
	if err := completeJobBoundAttemptForTest(s, id, attempt, lane, job); err != nil {
		t.Fatal(err)
	}
	epoch := latestStepStartEpoch(t, s, id, "repair")
	if err := runVerdictActionAs(t, s, id, "reject_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attempt, "attempt_epoch": epoch, "diagnosis": "the corrective job remains unsatisfied", "strategy": "repair this job",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:review-reject"},
	}), 0, acceptor); err != nil {
		t.Fatal(err)
	}
	pin := issue1013Pin(t, s, id)
	unrelated := &WorkerJobBinding{JobID: "job:unrelated-success", Revision: 1}
	issue1013StartRepair(t, s, id, f.owner, pin.Version, epoch+1)
	pin = issue1013Pin(t, s, id)
	oDispatchJobBoundAttempt(t, s, id, "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "review-reject-2", unrelated)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", lane, acceptor)
	if boundary := jobWindowAcceptanceBoundary(t, s, id); boundary != 0 {
		t.Fatalf("unrelated acceptance closed rejected job obligation at %d", boundary)
	}
}

// A recorded worker-job revision admits the same predicate-id shape the lane
// report schema admits: one-or-more characters after the predicate: prefix,
// not an eleven-character minimum.
func TestReviewWorkerJobPredicateShape(t *testing.T) {
	id := "predicate:primary"
	if !regexp.MustCompile(`^predicate:[A-Za-z0-9][A-Za-z0-9._:-]*$`).MatchString(id) {
		t.Fatal("probe ID is invalid")
	}
	if !workerJobPredicatePattern.MatchString(id) {
		t.Fatalf("valid contract/report predicate %s is refused by worker-job predicate validator", id)
	}
}

// A delivery assertion may not exit a phase while a recorded required job is
// unsatisfied and no evidence binds the jobs' integration (CD-0205 D3):
// record_delivery refuses instead of asserting whole-work delivery.
func TestReviewUnsatisfiedJobPhaseExit(t *testing.T) {
	const id = "review-unsatisfied-job-exit"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	reviewGateStartStep(t, s, id, "repair", "start_repair", f.owner)
	job := &WorkerJobBinding{JobID: "job:required-but-unfinished", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, id, f.owner, job)
	err := runVerdictActionAs(t, s, id, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:partial", "delivery_state": "asserted"}), 0, f.owner)
	if err == nil {
		t.Fatalf("record_delivery advanced to %s with an unsatisfied recorded job and no integration binding", issue1013Pin(t, s, id).Step)
	}
}

// CD-0242 D7 preserves applicable proof when its dependencies do not change.
// Superseding a contract counter alone cannot revoke an unchanged job.
func TestReviewJobAuthoritySurvivesSupersession(t *testing.T) {
	const id = "review-stale-job-authority"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:old-authority", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, id, f.owner, job)
	driftRegistryFixture(t, s)
	if err := runCoordinatorActionTx(t, s, BuiltinWorkflowRegistry(), id, "supersede_contract", f.owner, "", string(registryRepinSuccessorPayload(t, 2, id, 1))); err != nil {
		t.Fatal(err)
	}
	ready, err := readyWorkerJobRevisions(context.Background(), s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].Binding != *job {
		t.Fatalf("unchanged job lost authority after counter-only supersession: %+v", ready)
	}
	// Dispatch consumes that same recorded binding without relabelling its
	// native preparations or recording a replacement revision.
	packet := dispatchWorkerPacket(t, s, id, "repair", "attempt:"+id+":stale")
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, id, *job, "implement")
	if err := dispatchJobPacketForTest(t, s, id, "attempt:"+id+":stale", f.owner, "review-stale-dispatch", packet); err != nil {
		t.Fatalf("unchanged job dispatch refused after contract supersession: %v", err)
	}
}

// An acceptance discharges a failed job's corrective obligation only when the
// accepted revision carries the same recorded obligation (CD-0205 D4): a
// rewritten revision under the same job identity with an unrelated objective
// cannot close the original corrective window.
func TestReviewRewrittenRevisionResetsFailedJob(t *testing.T) {
	const id = "review-rewritten-correction"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	acceptor := reviewGateAcceptor(id)
	job := &WorkerJobBinding{JobID: "job:original-corrective", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":1", nil, f.owner, 0, "review-rewritten-1", job)
	// The live ingest carries the recorded observation the promoted pin
	// requires and derives the failure record in the same commit, replacing
	// the explicit record disposition.
	s2WorkerFailedAttributedLive(t, s, id, "attempt:"+id+":1", "corrective objective remains unsatisfied")
	pin := issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	// The rewritten revision keeps its unrelated objective; the oracle the
	// pin requires travels with it as fixture bookkeeping, not as the
	// obligation the acceptance discharges.
	if err := recordWorkerJobFieldsForTest(t, s, id, f.owner, map[string]any{
		"job_id": job.JobID, "objective": "Perform an unrelated documentation task, not the failed repair",
		"stopping_condition": "Report the document title", "checks": []string{}, "ready": true, "readiness_evidence": []string{"evidence:new-document-task"},
	}); err != nil {
		t.Fatal(err)
	}
	revisions, err := readWorkerJobRevisions(context.Background(), s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	replacement := revisions[len(revisions)-1].Binding
	pin = issue1013Pin(t, s, id)
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "review-rewritten-2", &replacement)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", BuiltinLaneDefinitions()[0], acceptor)
	if boundary := jobWindowAcceptanceBoundary(t, s, id); boundary != 0 {
		t.Fatalf("acceptance of unrelated objective closed failed corrective obligation at %d", boundary)
	}
}

// A held local acceptance of an unrelated job leaves the failed obligation unresolved.
func TestWitnessUnrelatedLocalAcceptKeepsObligation(t *testing.T) {
	const id = "witness-unrelated-local-accept-wall"
	f := oSeedReviewRoute(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:witness-failed", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":1", nil, f.owner, 0, "witness-wall-1", job)
	s2WorkerFailedAttributedLive(t, s, id, "attempt:"+id+":1", "fallback blocked")
	before := jobWindowAcceptanceBoundary(t, s, id)
	if before != 0 {
		t.Fatalf("failed obligation boundary = %d, want unresolved", before)
	}
	pin := issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	pin = issue1013Pin(t, s, id)
	unrelated := &WorkerJobBinding{JobID: "job:witness-unrelated", Revision: 1}
	oDispatchJobBoundAttempt(t, s, id, "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "witness-wall-2", unrelated)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", BuiltinLaneDefinitions()[0], reviewGateAcceptor(id))
	after := jobWindowAcceptanceBoundary(t, s, id)
	if after != before {
		t.Fatalf("unrelated local accept changed the obligation cutoff: before=%d after=%d", before, after)
	}
}

// A held local acceptance closes the correction only when it discharges the
// last unresolved recorded obligation (CD-0205 D4).
func TestWitnessSatisfyingLocalAcceptClosesObligation(t *testing.T) {
	const id = "witness-satisfying-local-accept-wall"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:witness-satisfying", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":1", nil, f.owner, 0, "witness-satisfy-1", job)
	s2WorkerFailedAttributedLive(t, s, id, "attempt:"+id+":1", "fallback blocked")
	before := jobWindowAcceptanceBoundary(t, s, id)
	if before != 0 {
		t.Fatalf("failed obligation boundary = %d, want unresolved", before)
	}
	pin := issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	pin = issue1013Pin(t, s, id)
	// A later revision of the same job identity carries the same recorded
	// obligation, so its acceptance discharges the unresolved one.
	satisfying := &WorkerJobBinding{JobID: job.JobID, Revision: 2}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "witness-satisfy-2", satisfying)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", BuiltinLaneDefinitions()[0], reviewGateAcceptor(id))
	if pin := issue1013Pin(t, s, id); pin.Correction != nil {
		t.Fatalf("satisfying acceptance left the correction window open: %#v", pin.Correction)
	}
	after := jobWindowAcceptanceBoundary(t, s, id)
	if after <= before {
		t.Fatalf("satisfying local accept did not advance the obligation cutoff: before=%d after=%d", before, after)
	}
}

// A rewritten revision of a failed job's identity consumes no correction
// authority (CD-0205 D4): the acceptance of an unrelated obligation leaves
// the window open and the correction record bound to the exact failed
// attempt.
func TestWitnessRewrittenJobKeepsFailedAttemptIdentity(t *testing.T) {
	const id = "witness-rewritten-job-binding"
	f := seedWorkflowReturnRouteFixture(t, id, "workflow.break_fix", "repair")
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:witness-corrective", Revision: 1}
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":1", nil, f.owner, 0, "witness-binding-1", job)
	s2WorkerFailedAttributedLive(t, s, id, "attempt:"+id+":1", "fallback blocked")
	pin := issue1013Pin(t, s, id)
	issue1013StartRepair(t, s, id, f.owner, pin.Version, latestStepStartEpoch(t, s, id, "repair")+1)
	// The rewritten revision keeps its unrelated objective; the oracle the
	// pin requires travels with it as fixture bookkeeping, not as the
	// obligation the acceptance discharges.
	if err := recordWorkerJobFieldsForTest(t, s, id, f.owner, map[string]any{
		"job_id": job.JobID, "objective": "An unrelated rewritten task", "stopping_condition": "An unrelated outcome",
		"ready": true, "readiness_evidence": []string{"evidence:witness-ready"},
	}); err != nil {
		t.Fatal(err)
	}
	views, err := readWorkerJobRevisions(context.Background(), s.db, id)
	if err != nil {
		t.Fatal(err)
	}
	replacement := views[len(views)-1].Binding
	pin = issue1013Pin(t, s, id)
	dispatchJobBoundAttempt(t, s, id, "repair", "attempt:"+id+":2", pin.Correction, f.owner, pin.Version, "witness-binding-2", &replacement)
	completeAndAcceptAttempt(t, s, id, "attempt:"+id+":2", BuiltinLaneDefinitions()[0], reviewGateAcceptor(id))
	if boundary := jobWindowAcceptanceBoundary(t, s, id); boundary != 0 {
		t.Fatalf("rewritten job closed the original obligation at %d", boundary)
	}
	correction, err := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), id, "repair", "")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.FailedAttemptID != "attempt:"+id+":1" {
		t.Fatalf("rewritten job acceptance lost the exact failed-attempt binding: %#v", correction)
	}
}

// Applicable producer proof follows the candidate an earlier Review integrates.
func TestWitnessCombinedAcceptAfterEarlierReview(t *testing.T) {
	for _, changed := range []bool{true, false} {
		name := "unchanged-candidate"
		if changed {
			name = "changed-candidate"
		}
		t.Run(name, func(t *testing.T) {
			const id = "witness-candidate-integration"
			c := acceptDeliveryCandidateAt(t, id, "workflow.break_fix", "refine", false)
			s := c.f.store
			defer s.Close()
			c.j.bindVerification(t, c.early)
			proofSubject := c.early.SubjectRef
			if changed {
				workerDiffCommit(t, c.j, "final-result", "the final job changes the candidate\n")
				c.early = c.j.observeRun(t, "integrated-final-result")
				// The integrated candidate moved past the ready job's recorded
				// preparation (CD-0205 D1). A second ready job leaves the
				// Review helper to record and bind a fresh job prepared on
				// the moved candidate; the stale job is not re-prepared.
				workerDiffJob(t, c.j, "integrate-final-result")
				qAcceptCriterionReview(t, c.f, c.j.repo, c.early.OperationRef)
			}
			candidate := "commit:" + refineProofCandidate(t, s, id)
			if (candidate != proofSubject) != changed {
				t.Fatalf("integrated candidate = %s, earlier proof subject = %s, want changed=%t", candidate, proofSubject, changed)
			}
			c.attempt = "attempt:" + id + ":final"
			c.epoch = acceptDeliveryCompleteReview(t, c, c.attempt, "ready_for_final_proof")
			err := runDeliveryAdmissionAction(t, s, id, "accept_worker_result", acceptDeliveryCombinedPayload(c, "artifact:witness-final"),
				refineProofTooling(ProjectDeclaredTool{ID: "head", Invocation: "git rev-parse --verify HEAD^{commit}"}), reviewGateAcceptor(id))
			if err != nil {
				t.Fatalf("combined acceptance refused producer proof on the integrated candidate: %v", err)
			}
			var accepted int
			if err := s.db.QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='accept_worker_result' AND attempt_id=?`, id, c.attempt).Scan(&accepted); err != nil || accepted != 1 {
				t.Fatalf("combined acceptance count = %d, %v; want exactly one", accepted, err)
			}
			reviewGateRequireStep(t, s, id, "delivery")
			if seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), s, id); err != nil || seq == 0 {
				t.Fatalf("combined acceptance delivery assertion = %d, %v; want the asserted delivery", seq, err)
			}
		})
	}
}

func witnessUnprovedCorrectiveReview(t *testing.T, j *candidateJourney, attempt string, job WorkerJobBinding, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(j.repo, path)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(j.repo, path), []byte("corrective output awaiting verification\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := j.commit(t, "reviewer@example.invalid", "unproved corrective output")
	report := currentReviewBlockForTest(t, j.s, j.workID, attempt, "ready_for_final_proof")
	report.OutputCandidate = WorkerReviewCandidate{ProjectID: report.InputCandidate.ProjectID, Commit: output, Tree: reviewObservedTree(t, j.repo, output)}
	j.reviewReceipts = nil
	j.completeReview(t, attempt, job, report)
	return output
}

// Integration proof follows the candidate the final pending result produces
// (CD-0205 D3), not the integrated candidate before its acceptance.
func TestWitnessCombinedAcceptBindsIntegrationToTheIntegratedCandidate(t *testing.T) {
	for _, changed := range []bool{true, false} {
		name := "unchanged-candidate"
		if changed {
			name = "changed-candidate"
		}
		t.Run(name, func(t *testing.T) {
			const id = "witness-candidate-integration"
			j := journeyToRefineA(t, id)
			s := j.s
			proof := j.observeRun(t, "before-final-result")
			j.bindVerification(t, proof)
			attempt := "attempt:" + id + ":final"
			job := workerDiffJob(t, j.candidateJourney, "witness-final")
			if err := descendantDispatch(t, j.candidateJourney, "review", attempt, job, nil); err != nil {
				t.Fatal(err)
			}
			j.laneDispatch(t, attempt, "review", &job)
			output := j.head(t)
			if changed {
				output = witnessUnprovedCorrectiveReview(t, j.candidateJourney, attempt, job, "internal/store/final.txt")
			} else {
				j.completeReview(t, attempt, job, currentReviewBlockForTest(t, s, id, attempt, "ready_for_final_proof"))
			}
			if ("commit:"+output != proof.SubjectRef) != changed {
				t.Fatalf("final job output = %s, earlier proof subject = %s, want changed=%t", output, proof.SubjectRef, changed)
			}
			before := j.effects(t)
			integratedBefore := refineProofCandidate(t, s, id)
			// The journey delivered repair before entering refine. Refusing the
			// pending acceptance must leave that assertion untouched, not add one.
			deliveryBefore, err := workflowLatestDeliveryAssertionSeq(t.Context(), s, id)
			if err != nil {
				t.Fatal(err)
			}
			payload := mustJSONValue(map[string]any{"attempt_id": attempt, "attempt_epoch": j.dispatchEpoch(t, attempt), "delivery_artifact": "artifact:witness-final", "delivery_state": "asserted"})
			err = runDeliveryAdmissionAction(t, s, id, "accept_worker_result", payload,
				refineProofTooling(ProjectDeclaredTool{ID: "head", Invocation: "git rev-parse --verify HEAD^{commit}"}), reviewGateAcceptor(id))
			if changed {
				t.Logf("proof=%s pending_output=commit:%s integrated_before=commit:%s integrated_after=commit:%s acceptance_error=%v before=%s after=%s", proof.SubjectRef, output, integratedBefore, refineProofCandidate(t, s, id), err, before, j.effects(t))
				var failure *Failure
				if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "verification does not cover the current integrated candidate") {
					t.Fatalf("combined acceptance refusal = %v, want missing_evidence naming the changed integrated candidate", err)
				}
				if after := j.effects(t); after != before {
					t.Fatalf("refused combined acceptance changed durable state: before=%s after=%s", before, after)
				}
				if after := refineProofCandidate(t, s, id); after != integratedBefore {
					t.Fatalf("refused combined acceptance integrated candidate = %s, want %s", after, integratedBefore)
				}
				definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
				for _, pending := range []string{"", attempt} {
					admission, _, _, err := loadWorkflowDeliveryAdmission(t.Context(), s.db, id, definition, "refine", pending)
					if err != nil {
						t.Fatal(err)
					}
					wantCovered := pending == ""
					if admission.ProofReady != wantCovered || admission.JobsIntegrated != wantCovered {
						t.Fatalf("accepting %q: refine proof ready=%t, jobs integrated=%t, want both %t", pending, admission.ProofReady, admission.JobsIntegrated, wantCovered)
					}
				}
				reviewGateRequireStep(t, s, id, "refine")
				if seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), s, id); err != nil || seq != deliveryBefore {
					t.Fatalf("refused combined acceptance delivery assertion = %d, %v; want unchanged %d", seq, err, deliveryBefore)
				}
				// The same pending result admits once its output is actually proven.
				j.bindVerification(t, j.observeRun(t, "final-result"))
				err = runDeliveryAdmissionAction(t, s, id, "accept_worker_result", payload,
					refineProofTooling(ProjectDeclaredTool{ID: "head", Invocation: "git rev-parse --verify HEAD^{commit}"}), reviewGateAcceptor(id))
			}
			if err != nil {
				t.Fatalf("combined acceptance refused proof on its output candidate: %v", err)
			}
			if candidate := refineProofCandidate(t, s, id); candidate != output {
				t.Fatalf("combined acceptance integrated candidate = %s, want output %s", candidate, output)
			}
			var accepted int
			if err := s.db.QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND action_id='accept_worker_result' AND attempt_id=?`, id, attempt).Scan(&accepted); err != nil || accepted != 1 {
				t.Fatalf("combined acceptance count = %d, %v; want exactly one", accepted, err)
			}
			reviewGateRequireStep(t, s, id, "delivery")
			if seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), s, id); err != nil || seq <= deliveryBefore {
				t.Fatalf("combined acceptance delivery assertion = %d, %v; want the asserted delivery", seq, err)
			}
		})
	}
}

// Tool declarations qualify the pending output's producer commands, not the
// cached commands that proved the candidate before combined acceptance.
func TestWitnessCombinedAcceptChecksPendingCandidateTooling(t *testing.T) {
	for _, pendingDeclared := range []bool{false, true} {
		name := "only-pre-acceptance-command-declared"
		if pendingDeclared {
			name = "only-pending-output-command-declared"
		}
		t.Run(name, func(t *testing.T) {
			const id = "witness-pending-tooling"
			j := journeyToRefineA(t, id)
			proofA := j.observeRun(t, "tooling-a")
			j.bindVerification(t, proofA)
			attempt := "attempt:" + id + ":final"
			job := workerDiffJob(t, j.candidateJourney, "tooling-final")
			if err := descendantDispatch(t, j.candidateJourney, "review", attempt, job, nil); err != nil {
				t.Fatal(err)
			}
			j.laneDispatch(t, attempt, "review", &job)
			output := witnessUnprovedCorrectiveReview(t, j.candidateJourney, attempt, job, "internal/store/tooling.txt")
			request := j.request("tooling-b", nil)
			request.Command = []string{"git", "rev-parse", "HEAD"}
			proofB, err := j.s.VerifyWorktree(t.Context(), request)
			if err != nil || proofB.SubjectRef != "commit:"+output || proofB.SubjectRef == proofA.SubjectRef {
				t.Fatalf("pending producer proof = %+v, %v; want changed output %s", proofB, err, output)
			}
			j.bindVerification(t, proofB)
			invocation := "git rev-parse --verify HEAD^{commit}"
			if pendingDeclared {
				invocation = "git rev-parse HEAD"
			}
			tooling := refineProofTooling(ProjectDeclaredTool{ID: "head", Invocation: invocation})
			before := j.effects(t)
			candidateBefore := refineProofCandidate(t, j.s, id)
			deliveryBefore, err := workflowLatestDeliveryAssertionSeq(t.Context(), j.s, id)
			if err != nil {
				t.Fatal(err)
			}
			payload := mustJSONValue(map[string]any{"attempt_id": attempt, "attempt_epoch": j.dispatchEpoch(t, attempt), "delivery_artifact": "artifact:pending-tooling", "delivery_state": "asserted"})
			err = runDeliveryAdmissionAction(t, j.s, id, "accept_worker_result", payload, tooling, reviewGateAcceptor(id))
			if !pendingDeclared {
				var failure *Failure
				if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "declared checks: head ("+invocation+")") {
					t.Fatalf("combined acceptance with only A's command declared = %v; want missing_evidence for B's undeclared command", err)
				}
				if after := j.effects(t); after != before {
					t.Fatalf("undeclared pending command changed durable state: before=%s after=%s", before, after)
				}
				if candidate := refineProofCandidate(t, j.s, id); candidate != candidateBefore {
					t.Fatalf("undeclared pending command integrated %s, want unchanged %s", candidate, candidateBefore)
				}
				reviewGateRequireStep(t, j.s, id, "refine")
				if seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), j.s, id); err != nil || seq != deliveryBefore {
					t.Fatalf("undeclared pending command delivery assertion = %d, %v; want unchanged %d", seq, err, deliveryBefore)
				}
				return
			}
			if err != nil {
				t.Fatalf("combined acceptance with only B's command declared: %v", err)
			}
			if candidate := refineProofCandidate(t, j.s, id); candidate != output {
				t.Fatalf("combined acceptance integrated %s, want pending output %s", candidate, output)
			}
			reviewGateRequireStep(t, j.s, id, "delivery")
			if seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), j.s, id); err != nil || seq <= deliveryBefore {
				t.Fatalf("combined acceptance delivery assertion = %d, %v; want a new assertion", seq, err)
			}
		})
	}
}

func registryRepinSuccessorPayload(t *testing.T, contractVersion int64, workID string, predecessor int64) json.RawMessage {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(returnRouteSuccessorPayload(contractVersion, workID, predecessor), &fields); err != nil {
		t.Fatal(err)
	}
	fields["premise"] = "recheck the approved contract against the rescanned Domain registry"
	fields["supersede_reason"] = "the Domain registry changed"
	fields["audit_evidence"] = []string{"evidence:registry-rescan"}
	return mustJSONValue(fields)
}
