package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The continuity regression (CD-0205 D4, amended 2026-10-19, CD-0148
// amendment): a failed confirming review voids the first repair and leaves
// its repairing revision unresolved, and that own debt cannot fold into a
// second checks repair — recovery first re-records and accepts the repairing
// revision's own obligation, leaving only the original revision unresolved,
// and only then does the second checks repair name the remaining
// original-check revision. The second repair and its confirming review ride
// ordinary CD-0148 authority behind real store-derived findings convergence:
// the continuity snapshot publishes the unfunded candidate beside the funded
// ones, and the recorded admission marker names the exact repairing
// acceptance with funded=false.

// checksRepairClaimGit rebinds the review-claim fixture's fake git the way
// reviewSubjectClaim built it, so closure verify runs drive the claimed
// repository without advancing its qualified subject.
func checksRepairClaimGit(t *testing.T, s *Store, workID, dir, head string) *fakeWorktreeGit {
	t.Helper()
	var branch string
	if err := s.DatabaseForTesting().QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(dir)
	git.worktrees[dir] = branch
	git.worktreeRepos[dir] = dir
	git.branches[branch] = head
	return git
}

// checksRepairClosureRun runs the fixture control's exact argv once more on
// the current subject and retains the executed ledger, producing the closure
// evidence a resolution claim cites.
func checksRepairClosureRun(t *testing.T, s *Store, workID, name string, git *fakeWorktreeGit, dir string) WorktreeVerifyResult {
	t.Helper()
	command := oracleGoArgv("fixture")
	req := verifyRequest(git, "verify-"+workID+"-"+name, command, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
	req.WorkID, req.ProjectID = workID, "project"
	result, err := s.VerifyWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("closure run %s: %v", name, err)
	}
	seedOracleExecuteLedgerFixture(t, s, &result, oracleFixtureGraphForCommand(t, s, workID, command))
	return result
}

// checksRepairFindingsCompletion applies one job-bound completion whose
// no_ship report carries the classified blockers beside the resolution
// claims, with the closure runs' receipts attached.
func checksRepairFindingsCompletion(t *testing.T, s *Store, workID, attemptID, eventKey, verdict string, blockers []WorkerReviewFinding, resolutions []WorkerOracleResolution, receipt *WorkerOracleReceipt) {
	t.Helper()
	completion := oracleCompletionEvent(t, s, workID, attemptID, eventKey, func(payload *WorkerCompletedPayload) {
		if blockers == nil {
			blockers = []WorkerReviewFinding{}
		}
		payload.Review = &WorkerReviewBlock{Verdict: verdict, Findings: blockers, ResolvedFindings: resolutions}
		if receipt != nil && len(payload.Evidence) > 0 {
			payload.Evidence[0].OracleReceipt = receipt
		}
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completion}}); err != nil {
		t.Fatalf("%s completion refused: %v", attemptID, err)
	}
}

func checksRepairBlocker(t *testing.T, s *Store, workID, detail, family string) WorkerReviewFinding {
	t.Helper()
	return WorkerReviewFinding{Severity: "P1", Confidence: "high", Detail: detail,
		Oracle: &WorkerOracleFinding{
			Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:fixture",
			FailureFamily: family, PredicateIDs: fixtureRepairPredicateIDs(t, s, workID),
			ControlIDs: []string{"control:fixture"}, EvidenceRefs: []string{"evidence:checks-repair-continuity-" + family},
		}}
}

func checksRepairReject(t *testing.T, s *Store, workID, attemptID string, open []string, acceptor WorkflowActor) {
	t.Helper()
	payload := map[string]any{
		"attempt_id": attemptID, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair"),
		"diagnosis": "the repaired checks still cannot be executed or decided as written",
		"strategy":  "repair the failing entry paths", "predicate_ids": []string{"predicate:return-route"},
		"evidence_refs": []string{"evidence:checks-repair-continuity"},
	}
	if open != nil {
		payload["open_finding_ids"] = open
	}
	if err := runVerdictActionAs(t, s, workID, "reject_worker_result", mustJSONValue(payload), 0, acceptor); err != nil {
		t.Fatalf("reject %s with %v: %v", attemptID, open, err)
	}
}

func checksRepairRepairFields(jobID, checks string, revisions []int64) map[string]any {
	fields := map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-continuity job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{checks}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-continuity-ready"},
	}
	if revisions != nil {
		fields["repair_revisions"] = revisions
		fields["repair_reason"] = "the named revisions' checks invoked a command no admitted lane can execute or decide as written"
	}
	return fields
}

func TestChecksRepairSecondRepairReviewRidesContinuityWithoutFunding(t *testing.T) {
	const workID = "checks-repair-continuity"
	const jobID = "job:checks-repair-continuity"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	dir, head := reviewSubjectClaim(t, s, workID)
	git := checksRepairClaimGit(t, s, workID, dir, head)
	acceptor := reviewGateAcceptor(workID)

	// Round one: the original revision's attempt completes no_ship with four
	// classified blockers, and the exact-set rejection records them.
	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields(jobID, "concord check --continuity-original --exit-code=never", nil)); err != nil {
		t.Fatalf("record the original revision: %v", err)
	}
	if latest := checksRepairLatestBinding(t, s, workID, jobID); latest != nil {
		*job = *latest
	}
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, workerForChecksRepair(workID), 0, "checks-repair-continuity-1", job)
	checksRepairFindingsCompletion(t, s, workID, attempt1, "checks-repair-r0", "no_ship", []WorkerReviewFinding{
		checksRepairBlocker(t, s, workID, "first blocker", "family-one"),
		checksRepairBlocker(t, s, workID, "second blocker", "family-two"),
		checksRepairBlocker(t, s, workID, "third blocker", "family-three"),
		checksRepairBlocker(t, s, workID, "fourth blocker", "family-four"),
	}, nil, nil)
	lineage, err := readWorkerOracleFindingLineageTx(ctx, s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	open := lineage.openFindingIDs()
	if len(open) != 4 {
		t.Fatalf("open finding ids = %v, want four blockers", open)
	}
	checksRepairReject(t, s, workID, attempt1, open, acceptor)

	// The first repair names the one unresolved revision; its acceptance
	// opens the repair and funds the confirming review below the wall.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields(jobID, "go test ./internal/store/ -run TestChecksRepairContinuityOne", []int64{1})); err != nil {
		t.Fatalf("record the first repair: %v", err)
	}
	first := checksRepairLatestBinding(t, s, workID, jobID)
	if first == nil {
		t.Fatal("the first repair is not recorded")
	}
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt2 := "attempt:" + workID + ":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, workerForChecksRepair(workID), pin.Version, "checks-repair-continuity-2", first)
	completeChecksRepairAttemptObserved(t, s, workID, attempt2, "checks-repair-completed-"+attempt2)
	acceptChecksRepairAttempt(t, s, workID, attempt2, acceptor)

	// Below the wall the funded candidate publishes; the funded review
	// dispatches and its no_ship verdict voids the first repair.
	continuity, continuityErr := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if continuityErr != nil {
		t.Fatal(continuityErr)
	}
	if len(continuity.ChecksRepairReviewJobs) != 1 || continuity.ChecksRepairReviewJobs[0].JobID != jobID || continuity.ChecksRepairReviewJobs[0].Revision != first.Revision {
		t.Fatalf("funded continuity jobs = %+v, want the first repair's exact binding", continuity.ChecksRepairReviewJobs)
	}
	review1 := "attempt:" + workID + ":review-1"
	if err := dispatchConfirmingReview(t, s, workID, review1, first, fixture.owner); err != nil {
		t.Fatalf("the funded confirming review dispatch refused: %v", err)
	}
	// The voiding report continues three standing blockers and closes the
	// fourth with an evidenced pass, so the exact-set rejection shrinks the
	// open set 4→3 and records the cycle the wall's first basis derives from.
	run4 := checksRepairClosureRun(t, s, workID, "close-family-four", git, dir)
	checksRepairFindingsCompletion(t, s, workID, review1, "checks-repair-review1", "no_ship", []WorkerReviewFinding{
		checksRepairContinuation(t, s, workID, open[0], "first blocker stands"),
		checksRepairContinuation(t, s, workID, open[1], "second blocker stands"),
		checksRepairContinuation(t, s, workID, open[2], "third blocker stands"),
	}, []WorkerOracleResolution{{FindingID: open[3], EvidenceRefs: []string{run4.OperationRef}}}, &WorkerOracleReceipt{
		ControlIDs: []string{"control:fixture"}, CaseIDs: []string{"case:fixture"},
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
		Result:       OracleReceiptResultPass, ExitCode: passExitCode(), RunRef: run4.OperationRef,
		EvidenceRefs: []string{run4.OperationRef},
	})
	checksRepairReject(t, s, workID, review1, open[:3], acceptor)
	if fundings, fundingErr := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); fundingErr != nil || len(fundings) != 0 {
		t.Fatalf("fundings after the void = (%#v, %v), want none", fundings, fundingErr)
	}

	// The failed confirming review's own debt cannot fold into a second
	// checks repair: recovery first re-records and accepts the repairing
	// revision's own obligation (CD-0205 amendment), so a repair naming the
	// voided first repair beside the original refuses.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields(jobID, "go test ./internal/store/ -run TestChecksRepairContinuityTwo", []int64{1, 2})); err == nil {
		t.Fatal("a second checks repair folded the voided first repair's unresolved obligation into its named set")
	} else if !strings.Contains(err.Error(), "re-records and accepts that revision's own obligation") {
		t.Fatalf("the second repair refusal = %v, want the own-obligation recovery rule", err)
	}

	// Own-debt recovery: revision 3 re-records the first repair's exact
	// obligation — its checks beside a plain revision, no checks repair
	// metadata — and dispatches at the wall behind the real store-derived
	// findings convergence the 4→3 shrink cycle recorded.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields(jobID, "go test ./internal/store/ -run TestChecksRepairContinuityOne", nil)); err != nil {
		t.Fatalf("record the repairing revision's own obligation: %v", err)
	}
	recovery := checksRepairLatestBinding(t, s, workID, jobID)
	if recovery == nil || recovery.Revision != 3 {
		t.Fatalf("the own-obligation re-record = %+v, want derived revision 3", recovery)
	}
	var recoveryMetadata string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(payload,'$.checks_repair'),'') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=3`, string(SubjectWorkItem), workID, WorkerJobRecorded, jobID).Scan(&recoveryMetadata); err != nil || recoveryMetadata != "" {
		t.Fatalf("the own-obligation re-record's checks_repair metadata = (%q, %v), want a plain revision", recoveryMetadata, err)
	}
	pin = issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt3 := "attempt:" + workID + ":3"
	correction, correctionErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", attempt3)
	if correctionErr != nil || correction == nil {
		t.Fatalf("correction context for the recovery attempt = (%#v, %v)", correction, correctionErr)
	}
	laneVersion, laneDigest := registeredLaneIdentity(t, "implement")
	attempt3Packet := joinPacketFor(t, s, workID, "repair", attempt3, "implement", laneVersion, laneDigest)
	attempt3Packet["schema_version"] = WorkerPacketSchemaVersion
	attempt3Packet["inputs"].(map[string]any)["correction"] = correction
	attempt3Packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *recovery)
	attempt3Payload, payloadErr := json.Marshal(map[string]any{"attempt_id": attempt3, "worker_packet": attempt3Packet})
	if payloadErr != nil {
		t.Fatal(payloadErr)
	}
	if _, dispatchErr := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker", Payload: attempt3Payload,
		SessionWorktree: dispatchSessionWorktree(t, s, workID), Actor: fixture.owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "checks-repair-continuity-3", OperationID: "checks-repair-continuity-3",
		PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "checks-repair-continuity-3",
		RequestID: "request:checks-repair-continuity-3", ContractDigest: testManifestDigest, Now: time.Unix(60, 0).UTC(),
	}); dispatchErr != nil {
		t.Fatalf("the recovery dispatch refused behind the recorded convergence: %v", dispatchErr)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt3, recovery)

	// The recovery run settles: its own checks pass — the pass receipt proves
	// the run — and its evidenced resolution closes the third blocker, so the
	// open set shrinks 3→2 through the exact-set rejection the coordinator
	// records because the work still holds open blockers. The rejection is
	// the record the wall's second convergence basis derives from.
	run3 := checksRepairClosureRun(t, s, workID, "close-family-three", git, dir)
	checksRepairFindingsCompletion(t, s, workID, attempt3, "checks-repair-recovery", "ship", nil,
		[]WorkerOracleResolution{{FindingID: open[2], EvidenceRefs: []string{run3.OperationRef}}}, &WorkerOracleReceipt{
			ControlIDs: []string{"control:fixture"}, CaseIDs: []string{"case:fixture"},
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
			Result:       OracleReceiptResultPass, ExitCode: passExitCode(), RunRef: run3.OperationRef,
			EvidenceRefs: []string{run3.OperationRef},
		})
	checksRepairReject(t, s, workID, attempt3, open[:2], acceptor)
	// The settling acceptance discharges the failed confirming review's
	// repairing revision, leaving only the original revision unresolved.
	acceptChecksRepairAttempt(t, s, workID, attempt3, acceptor)
	watermark, watermarkErr := workerChecksRepairWatermarkTx(ctx, s.DatabaseForTesting(), workID)
	if watermarkErr != nil {
		t.Fatal(watermarkErr)
	}
	walked, walkErr := workerChecksRepairWalkTx(ctx, s.DatabaseForTesting(), workID, watermark)
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if !walked.unresolved[workerJobKey(jobID, 1)] || walked.unresolved[workerJobKey(jobID, 2)] || walked.unresolved[workerJobKey(jobID, 3)] {
		t.Fatalf("unresolved after the own-obligation acceptance = %v, want only the original revision 1", walked.unresolved)
	}

	// The second repair names the remaining original-check revision alone
	// and dispatches at the wall behind the new 3→2 convergence basis.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields(jobID, "go test ./internal/store/ -run TestChecksRepairContinuityTwo", []int64{1})); err != nil {
		t.Fatalf("record the second repair: %v", err)
	}
	second := checksRepairLatestBinding(t, s, workID, jobID)
	if second == nil || second.Revision != 4 {
		t.Fatalf("the second repair = %+v, want derived revision 4", second)
	}
	pin = issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt4 := "attempt:" + workID + ":4"
	correction4, correction4Err := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", attempt4)
	if correction4Err != nil || correction4 == nil {
		t.Fatalf("correction context for the second repair's attempt = (%#v, %v)", correction4, correction4Err)
	}
	attempt4Packet := joinPacketFor(t, s, workID, "repair", attempt4, "implement", laneVersion, laneDigest)
	attempt4Packet["schema_version"] = WorkerPacketSchemaVersion
	attempt4Packet["inputs"].(map[string]any)["correction"] = correction4
	attempt4Packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *second)
	attempt4Payload, payload4Err := json.Marshal(map[string]any{"attempt_id": attempt4, "worker_packet": attempt4Packet})
	if payload4Err != nil {
		t.Fatal(payload4Err)
	}
	if _, dispatch4Err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker", Payload: attempt4Payload,
		SessionWorktree: dispatchSessionWorktree(t, s, workID), Actor: fixture.owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("d", 64), IdempotencyIdentity: "checks-repair-continuity-4", OperationID: "checks-repair-continuity-4",
		PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "checks-repair-continuity-4",
		RequestID: "request:checks-repair-continuity-4", ContractDigest: testManifestDigest, Now: time.Unix(60, 0).UTC(),
	}); dispatch4Err != nil {
		t.Fatalf("the second repair's dispatch refused behind the new convergence basis: %v", dispatch4Err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt4, second)

	// The second repair's own attempt shrinks the open set 2→1 with an
	// evidenced pass. The completion carries the core-observed clean delivery,
	// so the accepted attempt anchors the confirming review's subject join
	// (the latest accepted implementation delivery must carry the
	// observation).
	run2 := checksRepairClosureRun(t, s, workID, "close-family-two", git, dir)
	attempt4Completion := oracleCompletionEvent(t, s, workID, attempt4, "checks-repair-second-repair", func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{
			checksRepairContinuation(t, s, workID, open[0], "first blocker stands"),
		}, ResolvedFindings: []WorkerOracleResolution{{FindingID: open[1], EvidenceRefs: []string{run2.OperationRef}}}}
		payload.Evidence[0].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:fixture"}, CaseIDs: []string{"case:fixture"},
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
			Result:       OracleReceiptResultPass, ExitCode: passExitCode(), RunRef: run2.OperationRef,
			EvidenceRefs: []string{run2.OperationRef},
		}
	})
	prepared, preparedErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attempt4, EventID: attempt4Completion.EventID})
	if preparedErr != nil {
		t.Fatalf("terminal delivery preparation refused: %v", preparedErr)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, attempt4Completion)
	checksRepairReject(t, s, workID, attempt4, open[:1], acceptor)
	acceptChecksRepairAttempt(t, s, workID, attempt4, acceptor)
	acceptSeq, found, acceptErr := workerChecksRepairConfirmingAcceptanceSeqTx(ctx, s.DatabaseForTesting(), workID, *second)
	if acceptErr != nil || !found || acceptSeq == 0 {
		t.Fatalf("the second repair's walked acceptance = (%d, %v, %v), want the recorded accepting event", acceptSeq, found, acceptErr)
	}

	// The ordinary-authority candidate must publish beside the funded ones:
	// the adapter builds the second review's packet from this view, so a
	// funded-only publication strands the review forever.
	continuity, continuityErr = ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if continuityErr != nil {
		t.Fatal(continuityErr)
	}
	found = false
	for _, job := range continuity.ChecksRepairReviewJobs {
		if job.JobID == jobID && job.Revision == second.Revision && job.Digest == second.Digest {
			found = true
		}
		if job.Revision != second.Revision {
			t.Fatalf("continuity carries stale candidate %s@%d beside the second repair", job.JobID, job.Revision)
		}
	}
	if !found {
		t.Fatalf("continuity checks_repair_review_jobs = %+v, want the exact satisfied second repair %s@%d — the ordinary-authority review packet cannot be built", continuity.ChecksRepairReviewJobs, second.JobID, second.Revision)
	}

	// The review packet the adapter builds from the continuity-produced job
	// dispatches through the third shrink cycle: the recorded marker rides
	// the exact repairing acceptance with funded=false — the ordinary
	// CD-0148 authority, and no third authority appears.
	review2 := "attempt:" + workID + ":review-2"
	if err := dispatchConfirmingReview(t, s, workID, review2, second, fixture.owner); err != nil {
		t.Fatalf("the ordinary-authority confirming review dispatch refused: %v", err)
	}
	var marker struct {
		Funded                 bool   `json:"funded"`
		RepairingAcceptanceSeq int64  `json:"repairing_acceptance_seq"`
		JobID                  string `json:"job_id"`
		Revision               int64  `json:"revision"`
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.checks_repair_review.funded'), json_extract(payload,'$.checks_repair_review.repairing_acceptance_seq'), json_extract(payload,'$.checks_repair_review.worker_job.job_id'), json_extract(payload,'$.checks_repair_review.worker_job.revision') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, review2).Scan(&marker.Funded, &marker.RepairingAcceptanceSeq, &marker.JobID, &marker.Revision); err != nil {
		t.Fatal(err)
	}
	if marker.Funded || marker.RepairingAcceptanceSeq != acceptSeq || marker.JobID != second.JobID || marker.Revision != second.Revision {
		t.Fatalf("the second review's admission marker = %+v, want funded=false beside the exact repairing acceptance %d", marker, acceptSeq)
	}

	// The wall stays basis-gated: a repeated review of the same repair
	// refuses without a fresh basis, and a third repair recording refuses
	// while the second stands unconfirmed.
	if err := dispatchConfirmingReview(t, s, workID, "attempt:"+workID+":review-3", second, fixture.owner); err == nil {
		t.Fatal("a repeated confirming review was admitted after the admission was consumed")
	} else if !strings.Contains(err.Error(), "without a convergence basis") {
		t.Fatalf("repeated review refusal = %v, want the wall's convergence refusal", err)
	}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields(jobID, "go test ./internal/store/ -run TestChecksRepairContinuityThree", []int64{1, 4})); err == nil {
		t.Fatal("a third repair was recorded while the second stands unconfirmed")
	} else if !strings.Contains(err.Error(), "at most one unconfirmed checks repair") {
		t.Fatalf("third repair refusal = %v, want the open-repair refusal", err)
	}
}

func workerForChecksRepair(workID string) WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checks-repair-continuity-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
}

func passExitCode() *int {
	code := 0
	return &code
}

// checksRepairContinuation re-reports one standing blocker as the same
// canonical open finding: the continuation maps onto the earlier id and
// mints nothing, so the derived open set only shrinks through resolutions.
// The continuation retains the earlier finding's failure family, the tie
// the fold checks.
func checksRepairContinuation(t *testing.T, s *Store, workID, findingID, detail string) WorkerReviewFinding {
	t.Helper()
	lineage, err := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	target, recorded := lineage.findings[findingID]
	if !recorded {
		t.Fatalf("continuation names unknown finding %q", findingID)
	}
	finding := checksRepairBlocker(t, s, workID, detail, target.FailureFamily)
	finding.Oracle.ContinuesFindingID = findingID
	return finding
}
