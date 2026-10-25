package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const checksRepairContinuityWorkID = "checks-repair-continuity"
const checksRepairContinuityJobID = "job:checks-repair-continuity"

// The continuity regression (CD-0205 D4): a failed confirming review voids
// the first repair and leaves
// its repairing revision unresolved, and that own debt cannot fold into a
// second checks repair — recovery first re-records and accepts the repairing
// revision's own obligation, leaving only the original revision unresolved,
// and only then does the second checks repair name the remaining
// original-check revision. Continuity publishes each exact repairing binding,
// and each confirming dispatch names its repairing acceptance. Classified
// findings and evidenced resolutions retain their exact-set rejection checks.

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
func checksRepairClosureRun(t *testing.T, s *Store, name string, git *fakeWorktreeGit) WorktreeVerifyResult {
	t.Helper()
	const workID = checksRepairContinuityWorkID
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
// corrective report carries the classified blockers beside the resolution
// claims, with the closure runs' receipts attached.
func checksRepairFindingsCompletion(t *testing.T, s *Store, workID, attemptID, eventKey, verdict string, blockers []WorkerReviewFinding, resolutions []WorkerOracleResolution, receipt *WorkerOracleReceipt) {
	t.Helper()
	completion := oracleCompletionEvent(t, s, workID, attemptID, eventKey, func(payload *WorkerCompletedPayload) {
		if blockers == nil {
			blockers = []WorkerReviewFinding{}
		}
		payload.Review = currentReviewBlockForTest(t, s, workID, attemptID, verdict)
		payload.Review.Findings, payload.Review.ResolvedFindings = blockers, resolutions
		if receipt != nil && len(payload.Evidence) > 0 {
			payload.Evidence[0].OracleReceipt = receipt
		}
	})
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(completion.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	// The oracle builder uses a synthetic tree. This journey seals a real
	// checkout, so its Review must report that checkout's candidate instead.
	candidate := currentReviewBlockForTest(t, s, workID, attemptID, verdict)
	payload.Review.InputCandidate, payload.Review.OutputCandidate = candidate.InputCandidate, candidate.OutputCandidate
	completion.Payload = mustJSONValue(payload)
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attemptID, EventID: completion.EventID})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(t.Context(), prepared), s, checksRepairSealedCompletion(t, completion))
}

func checksRepairBlocker(t *testing.T, s *Store, detail, family string) WorkerReviewFinding {
	t.Helper()
	const workID = checksRepairContinuityWorkID
	return WorkerReviewFinding{FindingID: "finding:" + family, Severity: "P1", Confidence: "high", Detail: detail,
		InScope: true, Blocking: true, Status: "open", EvidenceRefs: []string{"evidence:checks-repair-continuity-" + family}, ClosureRefs: []string{},
		Oracle: &WorkerOracleFinding{
			Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:fixture",
			FailureFamily: family, PredicateIDs: fixtureRepairPredicateIDs(t, s, workID),
			ControlIDs: []string{"control:fixture"}, EvidenceRefs: []string{"evidence:checks-repair-continuity-" + family},
		}}
}

func checksRepairRejection(t *testing.T, s *Store, attemptID string, open []string, acceptor WorkflowActor, apply bool) {
	t.Helper()
	const workID = checksRepairContinuityWorkID
	// The rejection binds the attempt's own dispatch epoch: an intermediate
	// dispatch at the step moves the step's latest start, never the
	// attempt's authorized window.
	window := attributionDispatchWindow(t, s, workID, attemptID)
	payload := map[string]any{
		"attempt_id": attemptID, "attempt_epoch": window.AttemptEpoch,
		"diagnosis": "the repaired checks still cannot be executed or decided as written",
		"strategy":  "repair the failing entry paths", "predicate_ids": []string{"predicate:return-route"},
		"evidence_refs": []string{"evidence:checks-repair-continuity"},
	}
	if open != nil {
		payload["open_finding_ids"] = open
	}
	if !apply {
		err := WorkflowActionPreflightWithRegistry(t.Context(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "reject_worker_result", Payload: mustJSONValue(payload), Actor: acceptor,
		})
		if err != nil {
			t.Fatalf("preflight rejection of %s with %v: %v", attemptID, open, err)
		}
		return
	}
	if err := runVerdictActionAs(t, s, workID, "reject_worker_result", mustJSONValue(payload), 0, acceptor); err != nil {
		t.Fatalf("reject %s with %v: %v", attemptID, open, err)
	}
}

func checksRepairRepairFields(checks string, revisions []int64) map[string]any {
	fields := map[string]any{
		"job_id": checksRepairContinuityJobID, "objective": "Corrective bounded objective for the checks-repair-continuity job",
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

// checksRepairSealedCompletion sends the report-owned completion the sealed
// terminal seam admits: observed_delivery is core-derived identity the
// production ingest refuses from any caller, and the sealed proof owns the
// delivery observation and its report size. The two continuity callsites
// drop the direct-fold builder's synthetic members only here, so the already
// prepared native seam derives the observation and the seam's report-size
// helper binds the production_size the sealed proof proves.
func checksRepairSealedCompletion(t *testing.T, event Event) Event {
	t.Helper()
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode the sealed completion %s: %v", event.EventID, err)
	}
	payload.ObservedDelivery = nil
	payload.ProductionSize = nil
	event.Payload = mustJSONValue(payload)
	return event
}

func TestChecksRepairSecondRepairReviewRetainsContinuityBinding(t *testing.T) {
	const workID = checksRepairContinuityWorkID
	const jobID = checksRepairContinuityJobID
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	dir, head := reviewSubjectClaim(t, s, workID)
	git := checksRepairClaimGit(t, s, workID, dir, head)
	acceptor := reviewGateAcceptor(workID)

	// Round one: the original revision's attempt reports correction with five
	// classified blockers, and the exact-set rejection records them.
	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields("concord check --continuity-original --exit-code=never", nil)); err != nil {
		t.Fatalf("record the original revision: %v", err)
	}
	if latest := checksRepairLatestBinding(t, s, workID, jobID); latest != nil {
		*job = *latest
	}
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, workerForChecksRepair(workID), 0, "checks-repair-continuity-1", job)
	checksRepairFindingsCompletion(t, s, workID, attempt1, "checks-repair-r0", "correction_required", []WorkerReviewFinding{
		checksRepairBlocker(t, s, "first blocker", "family-one"),
		checksRepairBlocker(t, s, "second blocker", "family-two"),
		checksRepairBlocker(t, s, "third blocker", "family-three"),
		checksRepairBlocker(t, s, "fourth blocker", "family-four"),
		checksRepairBlocker(t, s, "fifth blocker", "family-five"),
	}, nil, nil)
	lineage, err := readWorkerOracleFindingLineageTx(ctx, s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	open := lineage.openFindingIDs()
	if len(open) != 5 {
		t.Fatalf("open finding ids = %v, want five blockers", open)
	}
	checksRepairRejection(t, s, attempt1, open, acceptor, true)

	// The first repair names the one unresolved revision; its acceptance
	// opens the repair and admits its confirming review.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields("go test ./internal/store/ -run TestChecksRepairContinuityOne", []int64{1})); err != nil {
		t.Fatalf("record the first repair: %v", err)
	}
	first := checksRepairLatestBinding(t, s, workID, jobID)
	if first == nil {
		t.Fatal("the first repair is not recorded")
	}
	// A failed attempt of the same assignment remains beside the repair. A
	// different ordinary assignment would require a new accepted Review.
	plain := "attempt:" + workID + ":plain"
	plainCorrection, plainCorrectionErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", plain)
	if plainCorrectionErr != nil {
		t.Fatal(plainCorrectionErr)
	}
	dispatchJobBoundAttempt(t, s, workID, "repair", plain, plainCorrection, workerForChecksRepair(workID), 0, "checks-repair-continuity-plain", first)
	plainFailure := Event{EventID: "checks-repair-plain-failed-" + workID, Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(31, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerFailed), Payload: mustJSONValue(WorkerFailedPayload{
		AttemptID: plain, ReadbackModel: preferredModelForLane(reviewGateLane(t, "implementation")), FailureKind: WorkerFailureWorkerError, Detail: "the unrelated attempt failed before a judgeable result",
		FailureAttribution: &WorkerFailureAttribution{Origin: WorkerFailureAttributionRecorded, Observation: workerFailureObservationFixture()},
	})}
	if err := s2WorkerFailedLive(t, s, plainFailure); err != nil {
		t.Fatalf("the unrelated attempt's failure ingest refused: %v", err)
	}
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt2 := "attempt:" + workID + ":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, workerForChecksRepair(workID), pin.Version, "checks-repair-continuity-2", first)
	// The first repairing result resolves the fifth blocker with a real
	// oracle receipt, and the core observes its delivery, so the accepted
	// attempt anchors the confirming review's subject join.
	run5 := checksRepairClosureRun(t, s, "close-family-five", git)
	attempt2Completion := checksRepairSealedCompletion(t, oracleCompletionEvent(t, s, workID, attempt2, "checks-repair-completed-"+attempt2, func(payload *WorkerCompletedPayload) {
		payload.Review = currentReviewBlockForTest(t, s, workID, attempt2, "correction_required")
		payload.Review.Findings = []WorkerReviewFinding{
			checksRepairContinuation(t, s, open[0], "first blocker stands"),
			checksRepairContinuation(t, s, open[1], "second blocker stands"),
			checksRepairContinuation(t, s, open[2], "third blocker stands"),
			checksRepairContinuation(t, s, open[3], "fourth blocker stands"),
		}
		payload.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: open[4], EvidenceRefs: []string{run5.OperationRef}}}
		payload.Evidence[0].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:fixture"}, CaseIDs: []string{"case:fixture"},
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
			Result:       OracleReceiptResultPass, ExitCode: passExitCode(), RunRef: run5.OperationRef,
			EvidenceRefs: []string{run5.OperationRef},
		}
	}))
	attempt2Prepared, attempt2PreparedErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attempt2, EventID: attempt2Completion.EventID})
	if attempt2PreparedErr != nil {
		t.Fatalf("the first repair's terminal delivery preparation refused: %v", attempt2PreparedErr)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), attempt2Prepared), s, attempt2Completion)
	// Exact-set rejection preflight retains the four remaining blockers.
	// Main chooses acceptance once; it cannot also reject the same result.
	checksRepairRejection(t, s, attempt2, open[:4], acceptor, false)
	// The acceptance binds the attempt's own dispatch epoch: the plain
	// attempt's dispatch moved the step's latest start, never attempt2's
	// authorized window.
	attempt2Window := attributionDispatchWindow(t, s, workID, attempt2)
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attempt2, "attempt_epoch": attempt2Window.AttemptEpoch}), 0, acceptor); err != nil {
		t.Fatalf("accept %s: %v", attempt2, err)
	}

	// Continuity publishes the first repair's exact candidate. Its confirming
	// Review's accepted blocking disposition voids the repair.
	continuity, continuityErr := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if continuityErr != nil {
		t.Fatal(continuityErr)
	}
	if len(continuity.ChecksRepairReviewJobs) != 1 || continuity.ChecksRepairReviewJobs[0].JobID != first.JobID || continuity.ChecksRepairReviewJobs[0].Revision != first.Revision || continuity.ChecksRepairReviewJobs[0].Digest != first.Digest {
		t.Fatalf("continuity jobs = %+v, want the first repair's exact binding", continuity.ChecksRepairReviewJobs)
	}
	review1 := "attempt:" + workID + ":review-1"
	correction1, correction1Err := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", review1)
	if correction1Err != nil || correction1 == nil {
		t.Fatalf("correction at the first review = (%#v, %v), want the open correction", correction1, correction1Err)
	}
	if err := dispatchConfirmingReview(t, s, workID, review1, first, fixture.owner); err != nil {
		t.Fatalf("the first confirming review dispatch refused: %v", err)
	}
	assertChecksRepairReviewMarker(t, s, workID, review1, *first)
	// The voiding report continues three standing blockers and closes the
	// fourth with an evidenced pass, so the derived finding lineage shrinks the
	// open set 4→3.
	run4 := checksRepairClosureRun(t, s, "close-family-four", git)
	checksRepairFindingsCompletion(t, s, workID, review1, "checks-repair-review1", "correction_required", []WorkerReviewFinding{
		checksRepairContinuation(t, s, open[0], "first blocker stands"),
		checksRepairContinuation(t, s, open[1], "second blocker stands"),
		checksRepairContinuation(t, s, open[2], "third blocker stands"),
	}, []WorkerOracleResolution{{FindingID: open[3], EvidenceRefs: []string{run4.OperationRef}}}, &WorkerOracleReceipt{
		ControlIDs: []string{"control:fixture"}, CaseIDs: []string{"case:fixture"},
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
		Result:       OracleReceiptResultPass, ExitCode: passExitCode(), RunRef: run4.OperationRef,
		EvidenceRefs: []string{run4.OperationRef},
	})
	checksRepairRejection(t, s, review1, open[:3], acceptor, false)
	acceptChecksRepairAttempt(t, s, workID, review1, acceptor)
	checksRepairRequestCorrection(t, s, workID, acceptor)
	if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(candidates) != 0 {
		t.Fatalf("candidates after the void = (%#v, %v), want none", candidates, err)
	}

	// The failed confirming review's own debt cannot fold into a second
	// checks repair: recovery first re-records and accepts the repairing
	// revision's own obligation (CD-0205 amendment), so a repair naming the
	// voided first repair beside the original refuses.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields("go test ./internal/store/ -run TestChecksRepairContinuityTwo", []int64{1, 2})); err == nil {
		t.Fatal("a second checks repair folded the voided first repair's unresolved obligation into its named set")
	} else if !strings.Contains(err.Error(), "re-records and accepts that revision's own obligation") {
		t.Fatalf("the second repair refusal = %v, want the own-obligation recovery rule", err)
	}

	// Own-debt recovery: revision 3 re-records the first repair's exact
	// obligation — its checks beside a plain revision, no checks repair
	// metadata — and dispatches under the current correction.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields("go test ./internal/store/ -run TestChecksRepairContinuityOne", nil)); err != nil {
		t.Fatalf("record the repairing revision's own obligation: %v", err)
	}
	recovery := checksRepairLatestBinding(t, s, workID, jobID)
	if recovery == nil || recovery.Revision != 3 {
		t.Fatalf("the own-obligation re-record = %+v, want derived revision 3", recovery)
	}
	var recoveryMetadata string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(recorded_payload,'$.checks_repair'),'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=3`, workID, jobID).Scan(&recoveryMetadata); err != nil || recoveryMetadata != "" {
		t.Fatalf("the own-obligation re-record's checks_repair metadata = (%q, %v), want a plain revision", recoveryMetadata, err)
	}
	pin = issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	attempt3 := "attempt:" + workID + ":3"
	correction, correctionErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", attempt3)
	if correctionErr != nil || correction == nil {
		t.Fatalf("correction context for the recovery attempt = (%#v, %v)", correction, correctionErr)
	}
	laneDigest := registeredLaneIdentity(t, "implement")
	attempt3Packet := joinPacketFor(t, s, workID, "repair", attempt3, "implement", laneDigest)
	attempt3Packet["schema_version"] = WorkerPacketSchemaVersion
	attempt3Packet["inputs"].(map[string]any)["correction"] = correction
	attempt3Packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *recovery, "implement")
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
		t.Fatalf("the own-obligation recovery dispatch refused: %v", dispatchErr)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt3, recovery)

	// The recovery run settles: its own checks pass — the pass receipt proves
	// the run — and its evidenced resolution closes the third blocker, so the
	// open set shrinks 3→2. Rejection preflight validates the exact remaining
	// set, then Main accepts the result without a second disposition.
	run3 := checksRepairClosureRun(t, s, "close-family-three", git)
	checksRepairFindingsCompletion(t, s, workID, attempt3, "checks-repair-recovery", "continue", nil,
		[]WorkerOracleResolution{{FindingID: open[2], EvidenceRefs: []string{run3.OperationRef}}}, &WorkerOracleReceipt{
			ControlIDs: []string{"control:fixture"}, CaseIDs: []string{"case:fixture"},
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
			Result:       OracleReceiptResultPass, ExitCode: passExitCode(), RunRef: run3.OperationRef,
			EvidenceRefs: []string{run3.OperationRef},
		})
	checksRepairRejection(t, s, attempt3, open[:2], acceptor, false)
	// The confirming acceptance discharges the failed confirming review's
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
	// and dispatches under the current correction.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields("go test ./internal/store/ -run TestChecksRepairContinuityTwo", []int64{1})); err != nil {
		t.Fatalf("record the second repair: %v", err)
	}
	second := checksRepairLatestBinding(t, s, workID, jobID)
	if second == nil || second.Revision != 4 {
		t.Fatalf("the second repair = %+v, want derived revision 4", second)
	}
	reviewState, err := loadWorkflowReviewState(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewState.Accepted == nil || reviewState.Candidate == nil || reviewState.Accepted.Report.OutputCandidate != *reviewState.Candidate || !reviewState.DependenciesCurrent || !reviewState.StaleJobs[jobID] {
		t.Fatalf("the second repair must revalidate its changed job on the unchanged candidate: %+v", reviewState)
	}
	// The candidate is unchanged, but the new checks-repair revision is not
	// covered by the voiding Review. Revalidate that job before its dispatch.
	reviewFixture := workContextFixture{store: s, workID: workID, owner: fixture.owner, operator: fixture.operator}
	acceptCurrentReviewForTest(t, reviewFixture, workerFixtureDirectory(t, s, workID), "correction_required")
	checksRepairRequestCorrection(t, s, workID, acceptor)
	pin = issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	attempt4 := "attempt:" + workID + ":4"
	correction4, correction4Err := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", attempt4)
	if correction4Err != nil || correction4 == nil {
		t.Fatalf("correction context for the second repair's attempt = (%#v, %v)", correction4, correction4Err)
	}
	attempt4Packet := joinPacketFor(t, s, workID, "repair", attempt4, "implement", laneDigest)
	attempt4Packet["schema_version"] = WorkerPacketSchemaVersion
	attempt4Packet["inputs"].(map[string]any)["correction"] = correction4
	attempt4Packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *second, "implement")
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
		t.Fatalf("the second repair's dispatch refused: %v", dispatch4Err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt4, second)

	// The second repair's own attempt shrinks the open set 2→1 with an
	// evidenced pass. The completion carries the core-observed clean delivery,
	// so the accepted attempt anchors the confirming review's subject join
	// (the latest accepted implementation delivery must carry the
	// observation).
	run2 := checksRepairClosureRun(t, s, "close-family-two", git)
	attempt4Completion := checksRepairSealedCompletion(t, oracleCompletionEvent(t, s, workID, attempt4, "checks-repair-second-repair", func(payload *WorkerCompletedPayload) {
		payload.Review = currentReviewBlockForTest(t, s, workID, attempt4, "correction_required")
		payload.Review.Findings = []WorkerReviewFinding{
			checksRepairContinuation(t, s, open[0], "first blocker stands"),
		}
		payload.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: open[1], EvidenceRefs: []string{run2.OperationRef}}}
		payload.Evidence[0].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:fixture"}, CaseIDs: []string{"case:fixture"},
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
			Result:       OracleReceiptResultPass, ExitCode: passExitCode(), RunRef: run2.OperationRef,
			EvidenceRefs: []string{run2.OperationRef},
		}
	}))
	prepared, preparedErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attempt4, EventID: attempt4Completion.EventID})
	if preparedErr != nil {
		t.Fatalf("terminal delivery preparation refused: %v", preparedErr)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, attempt4Completion)
	checksRepairRejection(t, s, attempt4, open[:1], acceptor, false)
	acceptChecksRepairAttempt(t, s, workID, attempt4, acceptor)
	acceptSeq, found, acceptErr := workerChecksRepairConfirmingAcceptanceSeqTx(ctx, s.DatabaseForTesting(), workID, *second)
	if acceptErr != nil || !found || acceptSeq == 0 {
		t.Fatalf("the second repair's walked acceptance = (%d, %v, %v), want the recorded accepting event", acceptSeq, found, acceptErr)
	}

	// The adapter builds the second review's packet from continuity, which
	// must publish the new candidate without the voided first repair.
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
		t.Fatalf("continuity checks_repair_review_jobs = %+v, want the exact satisfied second repair %s@%d", continuity.ChecksRepairReviewJobs, second.JobID, second.Revision)
	}

	// The review packet the adapter builds from the continuity-produced job
	// dispatches with the exact repairing acceptance and binding.
	review2 := "attempt:" + workID + ":review-2"
	if err := dispatchConfirmingReview(t, s, workID, review2, second, fixture.owner); err != nil {
		t.Fatalf("the second confirming review dispatch refused: %v", err)
	}
	assertChecksRepairReviewMarker(t, s, workID, review2, *second)

	// A repeated review of the same repair refuses after consumption,
	// and a third repair recording refuses
	// while the second stands unconfirmed.
	if err := dispatchConfirmingReview(t, s, workID, "attempt:"+workID+":review-3", second, fixture.owner); err == nil {
		t.Fatal("a repeated confirming review was admitted after the admission was consumed")
	} else if !strings.Contains(err.Error(), "selected worker-job revision is not ready") {
		t.Fatalf("repeated review refusal = %v, want the consumed candidate's readiness refusal", err)
	}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields("go test ./internal/store/ -run TestChecksRepairContinuityThree", []int64{1, 4})); err == nil {
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
func checksRepairContinuation(t *testing.T, s *Store, findingID, detail string) WorkerReviewFinding {
	t.Helper()
	const workID = checksRepairContinuityWorkID
	lineage, err := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	target, recorded := lineage.findings[findingID]
	if !recorded {
		t.Fatalf("continuation names unknown finding %q", findingID)
	}
	finding := checksRepairBlocker(t, s, detail, target.FailureFamily)
	finding.Oracle.ContinuesFindingID = findingID
	return finding
}
