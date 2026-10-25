package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func seedChecksRepairFixture(t *testing.T, workID, step string) workflowReturnRouteFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, step,
		[]string{"verification"}, []string{"verification", "review", "artifact"})
}

func TestChecksRepairHeadsPreserveConsequencePolicy(t *testing.T) {
	for _, composition := range []WorkflowComposition{currentImplementationComposition(), currentBreakFixComposition()} {
		if composition.ApprovalPolicy != WorkflowApprovalPolicyConsequenceV1 {
			t.Fatalf("%s lost its consequence policy", composition.Ref)
		}
	}
}

func TestChecksRepairDispatchPropagatesUnreadableAuthorization(t *testing.T) {
	const workID = "checks-repair-unreadable-authorization"
	const jobID = "job:checks-repair-unreadable"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Check the confirming authorization read failure",
		"stopping_condition": "unreadable authorization returns an unavailable failure",
		"checks":             []string{"go test ./internal/store"}, "ready": true,
		"readiness_evidence": []string{"evidence:checks-repair-authorization"},
	}); err != nil {
		t.Fatal(err)
	}
	binding := checksRepairLatestBinding(t, s, workID, jobID)
	if binding == nil {
		t.Fatal("the job revision was not recorded")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worker_job_revisions SET state='satisfied' WHERE work_id=? AND job_id=?`, workID, jobID); err != nil {
		t.Fatal(err)
	}
	// A retained marker with missing binding members makes Scan fail. That
	// read failure is not evidence that the marker is absent.
	if _, err := tx.ExecContext(ctx, `INSERT INTO worker_authorizations(work_id,attempt_id,seq,payload) VALUES(?,?,1,?)`, workID, "attempt:unreadable", `{"action_id":"dispatch_worker","worker_attempt_id":"attempt:unreadable","checks_repair_review":{"worker_job":{}}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	err = verifyWorkerDispatchedJobBindingTx(ctx, tx, Event{SubjectID: workID}, "attempt:unreadable", binding)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindUnavailable {
		t.Fatalf("unreadable confirming authorization = %v, want unavailable", err)
	}
}

// The checks repair regression set (CD-0205 D4):
// recording, local acceptance, the one confirming review, the void paths,
// and correction obligation satisfaction.

func TestChecksRepairSatisfiesNamedRevisionsAndAdmitsOneConfirmingReview(t *testing.T) {
	const workID = "checks-repair-confirming-review"
	const jobID = "job:checks-repair"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	// The confirming review's subject join needs a real claimed worktree: one
	// git repository whose qualified HEAD anchors the observed editing
	// delivery and the review subject alike.
	reviewSubjectClaim(t, s, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checks-repair-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	acceptor := reviewGateAcceptor(workID)

	jobFields := func(checks string, repairRevisions []int64) map[string]any {
		fields := map[string]any{
			"job_id":               jobID,
			"objective":            "Corrective bounded objective for the checks-repair job",
			"stopping_condition":   "the recorded checks pass with no unresolved reference",
			"path_scope":           []string{"internal/store"},
			"checks":               []string{checks},
			"reserved_integration": "integration evidence binds at the parent effect step",
			"ready":                true,
			"readiness_evidence":   []string{"evidence:checks-repair-ready"},
		}
		if repairRevisions != nil {
			fields["repair_revisions"] = repairRevisions
			fields["repair_reason"] = "the named revisions' checks invoked a command no admitted lane can execute or decide as written"
		}
		return fields
	}
	record := func(fields map[string]any, what string) {
		t.Helper()
		if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, fields); err != nil {
			t.Fatalf("record the %s: %v", what, err)
		}
	}
	latestRevision := func(what string) *WorkerJobBinding {
		t.Helper()
		views, err := readWorkerJobRevisions(ctx, s.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		var latest *WorkerJobBinding
		for i := range views {
			if views[i].Binding.JobID == jobID {
				binding := views[i].Binding
				latest = &binding
			}
		}
		if latest == nil {
			t.Fatalf("no recorded revision of %s after the %s", jobID, what)
		}
		return latest
	}

	// Revision 1 fails on its own checks, so its obligation stays unresolved.
	record(jobFields("concord check --original-checks --exit-code=never", nil), "original revision 1")
	rev1 := latestRevision("original recording")
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, 0, "checks-repair-attempt-1", rev1)
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "the recorded check names a command no admitted lane can execute")

	// Revision 2 rewrites only the checks and fails the same way, so both
	// revisions of the job sit unresolved behind the same obligation shape.
	record(jobFields("concord check --rewritten-checks --exit-code=never", nil), "rewritten revision 2")
	rev2 := latestRevision("rewritten recording")
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt2 := "attempt:" + workID + ":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "checks-repair-attempt-2", rev2)
	s2WorkerFailedAttributedLive(t, s, workID, attempt2, "the rewritten check fails the same unexecutable way")

	// The checks repair records revision 3 of the same job: the authoring
	// supplies the named revisions and the reason together, and the core
	// derives the revision number and digest (CD-0205 D4).
	record(jobFields("go test ./internal/store/ -run TestChecksRepair", []int64{1, 2}), "checks repair revision 3 naming revisions 1 and 2")
	rev3 := latestRevision("repair recording")
	rev2View := checksRepairRevisionView(t, s, workID, *rev2)
	rev3View := checksRepairRevisionView(t, s, workID, *rev3)
	if rev3View.Binding.Revision != 3 || rev3View.Binding.Digest == rev2View.Binding.Digest {
		t.Fatalf("the repairing revision = %s@%d digest %s, want derived revision 3 with a fresh digest", rev3View.Binding.JobID, rev3View.Binding.Revision, rev3View.Binding.Digest)
	}
	// Every other recorded content field stays byte-identical to the named
	// revisions, so only the checks differ.
	if rev3View.Objective != rev2View.Objective || rev3View.StoppingCondition != rev2View.StoppingCondition || rev3View.ProjectScope != rev2View.ProjectScope ||
		!slices.Equal(rev3View.PathScope, rev2View.PathScope) || !slices.Equal(rev3View.PredicateIDs, rev2View.PredicateIDs) ||
		!slices.Equal(rev3View.Prerequisites, rev2View.Prerequisites) || !slices.Equal(rev3View.UnresolvedRefs, rev2View.UnresolvedRefs) ||
		rev3View.ReservedIntegration != rev2View.ReservedIntegration {
		t.Fatalf("the repairing revision changed content beside the checks: %+v against %+v", rev3View, rev2View)
	}
	if slices.Equal(rev3View.Checks, rev2View.Checks) {
		t.Fatalf("the repairing revision kept the named revision's checks %#v", rev3View.Checks)
	}
	// The oracle is frozen: the repairing revision carries byte-identical
	// oracle content to the named revisions, readiness references included.
	rev2Event := checksRepairRecordedEvent(t, s, workID, jobID, 2)
	rev3Event := checksRepairRecordedEvent(t, s, workID, jobID, 3)
	if string(rev2Event["acceptance_oracle"]) != string(rev3Event["acceptance_oracle"]) {
		t.Fatalf("the repairing revision changed the acceptance oracle: %s against %s", rev3Event["acceptance_oracle"], rev2Event["acceptance_oracle"])
	}
	// The core records the old checks of each named revision beside the new
	// checks, read from the exact recorded events, never caller assertions.
	var repair checksRepairRecord
	if raw, ok := rev3Event["checks_repair"]; !ok || json.Unmarshal(raw, &repair) != nil {
		t.Fatalf("recorded revision 3 carries no checks_repair record: %v", rev3Event["checks_repair"])
	}
	if repair.Reason == "" {
		t.Fatal("the recorded checks repair carries no reason")
	}
	if len(repair.Revisions) != 2 {
		t.Fatalf("recorded checks repair names %d revisions, want the two named unresolved revisions", len(repair.Revisions))
	}
	if repair.Revisions[0].WorkerJob.JobID != rev1.JobID || repair.Revisions[0].WorkerJob.Revision != rev1.Revision || !slices.Equal(repair.Revisions[0].Checks, checksRepairRevisionView(t, s, workID, *rev1).Checks) {
		t.Fatalf("checks repair entry 1 = %+v, want revision %s@%d with its old checks", repair.Revisions[0], rev1.JobID, rev1.Revision)
	}
	if repair.Revisions[1].WorkerJob.JobID != rev2.JobID || repair.Revisions[1].WorkerJob.Revision != rev2.Revision || !slices.Equal(repair.Revisions[1].Checks, rev2View.Checks) {
		t.Fatalf("checks repair entry 2 = %+v, want revision %s@%d with its old checks", repair.Revisions[1], rev2.JobID, rev2.Revision)
	}

	// The repairing revision dispatches and its acceptance is local: the step
	// holds, and the correction remains open until confirmation. The completion
	// carries the core-observed clean delivery that anchors the review subject.
	pin = issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt3 := "attempt:" + workID + ":3"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt3, pin.Correction, worker, pin.Version, "checks-repair-attempt-3", rev3)
	completeChecksRepairAttemptObserved(t, s, workID, attempt3, "checks-repair-completed-"+attempt3)
	acceptChecksRepairAttempt(t, s, workID, attempt3, acceptor)
	if pin := issue1013Pin(t, s, workID); pin.Step != "repair" {
		t.Fatalf("step after the repairing acceptance = %q, want repair held", pin.Step)
	}
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("correction anchor after the repairing acceptance = (%#v, %v), want the open correction", anchor, anchorErr)
	}
	// Nothing is dispatch-ready while the unconfirmed repair stands: the
	// named revisions are satisfied for readiness only, and the repairing
	// revision is satisfied.
	if ready, err := readyWorkerJobRevisions(ctx, s.db, workID); err != nil || len(ready) != 0 {
		t.Fatalf("ready revisions behind the unconfirmed repair = %#v, %v, want none", ready, err)
	}
	// A later ordinary revision cannot hide an accepted repair's pending confirming Review.
	later := jobFields("go test ./internal/store/ -run TestChecksRepair", nil)
	later["ready"] = false
	record(later, "later ordinary revision")
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	var delivery workflowDeliveryAdmission
	if _, err := loadWorkflowDeliveryJobAdmissionForAccept(ctx, s.db, workID, definition.Definition, &delivery, ""); err != nil {
		t.Fatal(err)
	}
	if !delivery.ChecksRepairUnconfirmedReview {
		t.Fatal("a later ordinary revision hid the unconfirmed repair's pending confirming Review")
	}
	// The named revisions resolve prerequisite readiness through the
	// accepted repairing result: each records the repairing acceptance as
	// its satisfaction reference (CD-0205 D4), so a later job's prerequisite
	// pinning them — with or without a result_ref pin — resolves.
	var acceptedRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, jobID, rev3View.Binding.Revision).Scan(&acceptedRef); err != nil || acceptedRef == "" {
		t.Fatalf("the repairing revision's satisfaction reference = %q, %v, want the accepting event", acceptedRef, err)
	}
	for _, named := range []*WorkerJobBinding{rev1, rev2} {
		var state, resultRef string
		if err := s.DatabaseForTesting().QueryRow(`SELECT state, COALESCE(satisfied_result_ref,'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, named.JobID, named.Revision).Scan(&state, &resultRef); err != nil {
			t.Fatal(err)
		}
		if state != "satisfied" || resultRef != acceptedRef {
			t.Fatalf("named revision %s@%d = %s via %q, want satisfied through the repairing acceptance %q", named.JobID, named.Revision, state, resultRef, acceptedRef)
		}
	}
	// Delivery refuses while the confirming Review remains pending; the refusal text is
	// implementation-owned.
	if err := runVerdictActionAs(t, s, workID, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:checks-repair-early", "delivery_state": "asserted"}), 0, acceptor); err == nil {
		t.Fatal("delivery advanced while the unconfirmed checks repair's confirming Review remains pending")
	}
	// At most one unconfirmed repair per job: a second repair of the same
	// named revisions refuses while the first stands.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, jobFields("go test ./internal/store/ -run TestChecksRepairSecond", []int64{1, 2})); err == nil {
		t.Fatal("a second unconfirmed checks repair was recorded for the same job while the first stands")
	}

	// The accepted repair admits exactly one independent confirming review.
	reviewAttempt := "attempt:" + workID + ":review-1"
	if err := dispatchConfirmingReview(t, s, workID, reviewAttempt, rev3, fixture.owner); err != nil {
		t.Fatalf("the confirming review dispatch refused: %v", err)
	}
	assertChecksRepairReviewMarker(t, s, workID, reviewAttempt, *rev3)
	if bound, err := workflowDispatchedJobForAttempt(ctx, s.DatabaseForTesting(), workID, reviewAttempt); err != nil || bound == nil || *bound != *rev3 {
		t.Fatalf("confirming review binding = (%#v, %v), want the satisfied repairing revision %s@%d", bound, err, rev3.JobID, rev3.Revision)
	}
	// The confirming admission is consumed once: a second review dispatch for
	// the same repair refuses.
	if err := dispatchConfirmingReview(t, s, workID, "attempt:"+workID+":review-2", rev3, fixture.owner); err == nil {
		t.Fatal("a second confirming review dispatch was admitted for the same repair")
	}
	// The review admission grants no authoring attempt: a dispatch of the
	// satisfied repairing revision refuses.
	authoringPacket := dispatchWorkerPacket(t, s, workID, "repair", "attempt:"+workID+":authoring")
	authoringPacket["schema_version"] = WorkerPacketSchemaVersion
	authoringPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *rev3, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, "attempt:"+workID+":authoring", worker, "checks-repair-authoring", authoringPacket); err == nil {
		t.Fatal("an authoring dispatch of the satisfied repairing revision was admitted")
	}

	// Only acceptance of the confirming Review's readiness confirms the
	// repair and satisfies the named revisions' failed obligations. The
	// Review's own completed report leaves the repair unconfirmed, and
	// nothing dispatches in between: a second review dispatch refuses before
	// the verdict lands.
	completeChecksRepairReviewWithVerdict(t, s, workID, reviewAttempt, "ready_for_final_proof")
	if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(candidates) != 0 {
		t.Fatalf("candidates behind the unaccepted ready report = (%#v, %v), want none: the one confirming review already dispatched", candidates, err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": reviewAttempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept the confirming Review's readiness: %v", err)
	}
	var confirmedRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, jobID, rev3View.Binding.Revision).Scan(&confirmedRef); err != nil || confirmedRef != acceptedRef {
		t.Fatalf("the repairing revision's satisfaction reference after the confirming acceptance = %q, %v, want the repairing acceptance %q", confirmedRef, err, acceptedRef)
	}
	assertIntegrationPopulation := func() {
		t.Helper()
		watermark, err := workerChecksRepairWatermarkTx(ctx, s.DatabaseForTesting(), workID)
		if err != nil {
			t.Fatal(err)
		}
		inputs, unavailable, err := workerFailureIntegrationInputs(ctx, s.DatabaseForTesting(), workID, sql.NullInt64{Int64: watermark, Valid: true})
		if err != nil || unavailable || len(inputs) != 1 || inputs[0].Job == nil || *inputs[0].Job != *rev3 || inputs[0].ResultRef != acceptedRef {
			t.Fatalf("confirmed repair's integration population = (%+v, %v, %v), want only the repairing acceptance %q", inputs, unavailable, err, acceptedRef)
		}
	}
	assertIntegrationPopulation()

	// The confirmed repair closes the correction when no obligation remains.
	if pin := issue1013Pin(t, s, workID); pin.Correction != nil {
		t.Fatalf("correction window after the confirming acceptance = %#v, want it closed", pin.Correction)
	}

}

func dispatchConfirmingReview(t *testing.T, s *Store, workID, attemptID string, job *WorkerJobBinding, actor WorkflowActor) error {
	t.Helper()
	lane := reviewGateLane(t, "review")
	laneDigest := registeredLaneIdentity(t, "review")
	step := currentStep(t, s, workID)
	packet := joinPacketFor(t, s, workID, step, attemptID, "review", laneDigest)
	packet["schema_version"] = WorkerPacketSchemaVersion
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "review")
	packet["inputs"].(map[string]any)["binding"].(map[string]any)["work_version"] = readWorkVersion(t, s, workID)
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(packetBytes)})
	if err != nil {
		t.Fatal(err)
	}
	sessionWorktree := dispatchSessionWorktree(t, s, workID)
	// The open correction window is part of the packet's truth: the
	// confirming review consumes the same current correction context any
	// dispatch at this step consumes.
	if correction, correctionErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, step, attemptID); correctionErr != nil {
		return correctionErr
	} else if correction != nil {
		packet["inputs"].(map[string]any)["correction"] = correction
		rebuilt, marshalErr := json.Marshal(packet)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		payload = mustJSONValue(map[string]any{"attempt_id": attemptID, "worker_packet": json.RawMessage(rebuilt)})
	}
	ctx, prepErr := reviewSubjectPrepareDispatch(s, workID, sessionWorktree)
	if prepErr != nil {
		return prepErr
	}
	if _, err := invokeWorkflowActionForCD0059(ctx, t, s, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "dispatch_worker", Payload: json.RawMessage(payload),
		SessionWorktree: sessionWorktree, Actor: actor,
		AcceptedInputsDigest: cd0059TestDigest(t, attemptID+"-inputs"), IdempotencyIdentity: attemptID + "-op",
		OperationID: "op-" + attemptID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: attemptID + "-key", RequestID: "req-" + attemptID, AcceptedScope: `{}`, ContractDigest: testManifestDigest,
		Now: time.Unix(44, 0).UTC(),
	}); err != nil {
		return err
	}
	var packetDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_packet_digest') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted, attemptID).Scan(&packetDigest); err != nil {
		t.Fatal(err)
	}
	dispatch := Event{EventID: "checks-repair-review-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(45, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest,
		ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, WorkerJob: job,
	})}
	return s.Transact(context.Background(), func(transaction *Transaction) error {
		prepared, err := PrepareLaneActorDispatch(context.Background(), transaction, dispatch, actor.PrincipalRef, actor.ClientRef)
		if err != nil {
			return err
		}
		_, err = AppendLaneActorDispatchTx(context.Background(), transaction, prepared)
		return err
	})
}

func assertChecksRepairReviewMarker(t *testing.T, s *Store, workID, attemptID string, binding WorkerJobBinding) {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, attemptID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["retry_convergence"]; present {
		t.Fatal("confirming dispatch emitted retry_convergence")
	}
	var marker map[string]json.RawMessage
	if err := json.Unmarshal(payload["checks_repair_review"], &marker); err != nil {
		t.Fatalf("decode confirming admission: %v", err)
	}
	if _, present := marker["funded"]; present {
		t.Fatal("confirming admission emitted funded")
	}
	var got WorkerChecksRepairReviewAdmission
	if err := json.Unmarshal(payload["checks_repair_review"], &got); err != nil {
		t.Fatal(err)
	}
	seq, found, err := workerChecksRepairConfirmingAcceptanceSeqTx(context.Background(), s.DatabaseForTesting(), workID, binding)
	if err != nil || !found || seq == 0 {
		t.Fatalf("repairing acceptance = (%d, %v, %v), want the recorded acceptance", seq, found, err)
	}
	if got.WorkerJob != binding || got.RepairingAcceptanceSeq != seq {
		t.Fatalf("confirming admission = %+v, want exact binding %+v and acceptance %d", got, binding, seq)
	}
}

// checksRepairRecordedEvent reads one recorded worker.job_recorded event
// payload by job identity, so the test asserts the exact recorded bytes the
// fold holds, never a projection copy.
func checksRepairRecordedEvent(t *testing.T, s *Store, workID, jobID string, revision int64) map[string]json.RawMessage {
	t.Helper()
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerJobRecorded, jobID, revision).Scan(&raw); err != nil {
		t.Fatalf("read the recorded worker.job_recorded event for %s@%d: %v", jobID, revision, err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("recorded worker.job_recorded payload for %s@%d is not an object: %v", jobID, revision, err)
	}
	return payload
}

// checksRepairRecord is the core-recorded checks repair beside a repairing
// revision: the stated reason and, for every named revision, its binding and
// the old checks the exact recorded events hold.
type checksRepairRecord struct {
	Reason    string `json:"reason"`
	Revisions []struct {
		WorkerJob WorkerJobBinding `json:"worker_job"`
		Checks    []string         `json:"checks"`
	} `json:"revisions"`
}

func checksRepairRevisionView(t *testing.T, s *Store, workID string, binding WorkerJobBinding) WorkerJobRevisionView {
	t.Helper()
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding == binding {
			return view
		}
	}
	t.Fatalf("worker-job revision %s@%d is not recorded", binding.JobID, binding.Revision)
	return WorkerJobRevisionView{}
}

// The fold verifies a recorded repair's metadata against the exact recorded
// events: a raw worker.job_recorded event whose checks_repair asserts old
// checks the named revision never recorded refuses even under a
// self-consistent digest, and the truth the action path derived replays
// unchanged (CD-0205 D4).
func TestWorkerJobChecksRepairFoldVerifiesDerivedMetadata(t *testing.T) {
	const workID = "checks-repair-fold-metadata"
	const jobID = "job:fold-metadata"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/fold-metadata-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, job)
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, 0, "fold-metadata-1", job)
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "the recorded check names a command no admitted lane can execute")

	named := checksRepairRecordedEvent(t, s, workID, jobID, 1)
	var namedPayload WorkerJobRecordedPayload
	if raw, err := json.Marshal(named); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(raw, &namedPayload); err != nil {
		t.Fatal(err)
	}
	forge := func(checks []string, namedChecks []string, digestOver func(WorkerJobRecordedPayload) string) error {
		candidate := namedPayload
		candidate.WorkflowVersionFields = WorkflowVersionFields{}
		candidate.Revision = 2
		candidate.Checks = checks
		candidate.ChecksRepair = &WorkerJobChecksRepair{Reason: "the named revision's checks could not run", Revisions: []WorkerJobChecksRepairEntry{{
			WorkerJob: WorkerJobBinding{JobID: jobID, Revision: 1, Digest: namedPayload.Digest}, Checks: namedChecks,
		}}}
		candidate.Digest = digestOver(candidate)
		return applyWorkerJobEventForTest(t, s, workID, candidate)
	}
	// Forged old checks under a self-consistent digest refuse at the fold.
	forgedChecks := append(slices.Clone(namedPayload.Checks), "an invented extra check")
	if err := forge([]string{"go test ./internal/store/ -run TestChecksRepairFold"}, forgedChecks, DeriveWorkerJobDigest); err == nil || !strings.Contains(err.Error(), "does not match the metadata the exact recorded events derive") {
		t.Fatalf("forged repair metadata error = %v, want the derived-truth refusal", err)
	}
	// The derived truth replays: the same repair beside the named revision's
	// real old checks records and reads back with its metadata.
	if err := forge([]string{"go test ./internal/store/ -run TestChecksRepairFold"}, namedPayload.Checks, DeriveWorkerJobDigest); err != nil {
		t.Fatalf("the truthfully derived repair metadata refused: %v", err)
	}
	repair := checksRepairRecordedEvent(t, s, workID, jobID, 2)
	var metadata WorkerJobChecksRepair
	if err := json.Unmarshal(repair["checks_repair"], &metadata); err != nil {
		t.Fatalf("recorded repair metadata is not one object: %v", err)
	}
	if len(metadata.Revisions) != 1 || metadata.Revisions[0].WorkerJob.Revision != 1 || !slices.Equal(metadata.Revisions[0].Checks, namedPayload.Checks) {
		t.Fatalf("recorded repair metadata = %+v, want revision 1 beside its recorded checks", metadata)
	}
}

// Acceptance of a blocking confirming Review voids the repair: the named
// revisions become unresolved again, the old and new checks stay on record,
// and the voided repair cannot admit another confirming review (CD-0205 D4).
func TestChecksRepairCorrectiveReviewVoidsRepair(t *testing.T) {
	for _, disposition := range []string{"correction_required", "revision_required"} {
		t.Run(disposition, func(t *testing.T) {
			const workID = "checks-repair-corrective-review-void"
			const jobID = "job:checks-repair-void"
			fixture := seedChecksRepairFixture(t, workID, "repair")
			s := fixture.store
			defer s.Close()
			ctx := context.Background()
			reviewSubjectClaim(t, s, workID)
			worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checks-repair-void-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
			acceptor := reviewGateAcceptor(workID)

			job := &WorkerJobBinding{JobID: jobID, Revision: 1}
			if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
				"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-void job",
				"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
				"checks": []string{"concord check --void-original --exit-code=never"}, "reserved_integration": "integration evidence binds at the parent effect step",
				"ready": true, "readiness_evidence": []string{"evidence:checks-repair-void-ready"},
			}); err != nil {
				t.Fatalf("record the original revision: %v", err)
			}
			if latest := checksRepairLatestBinding(t, s, workID, jobID); latest != nil {
				*job = *latest
			}
			attempt1 := "attempt:" + workID + ":1"
			dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, 0, "checks-repair-void-1", job)
			s2WorkerFailedAttributedLive(t, s, workID, attempt1, "the recorded check names a command no admitted lane can execute")

			repairFields := map[string]any{
				"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-void job",
				"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
				"checks": []string{"go test ./internal/store/ -run TestChecksRepairVoid"}, "reserved_integration": "integration evidence binds at the parent effect step",
				"ready": true, "readiness_evidence": []string{"evidence:checks-repair-void-ready"},
				"repair_revisions": []int64{1}, "repair_reason": "the named revision's checks invoked a command no admitted lane can execute or decide as written",
			}
			if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, repairFields); err != nil {
				t.Fatalf("record the repair: %v", err)
			}
			repairing := checksRepairLatestBinding(t, s, workID, jobID)
			if repairing == nil {
				t.Fatal("the repairing revision is not recorded")
			}
			pin := issue1013Pin(t, s, workID)
			issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
			pin = issue1013Pin(t, s, workID)
			attempt2 := "attempt:" + workID + ":2"
			dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "checks-repair-void-2", repairing)
			completeChecksRepairAttemptObserved(t, s, workID, attempt2, "checks-repair-completed-"+attempt2)
			acceptChecksRepairAttempt(t, s, workID, attempt2, acceptor)

			// The confirming Review dispatches once. Main's acceptance of its
			// blocking disposition voids the repair and its provisional readiness.
			reviewAttempt := "attempt:" + workID + ":review-void"
			if err := dispatchConfirmingReview(t, s, workID, reviewAttempt, repairing, fixture.owner); err != nil {
				t.Fatalf("the confirming review dispatch refused: %v", err)
			}
			assertChecksRepairReviewMarker(t, s, workID, reviewAttempt, *repairing)
			completeChecksRepairReviewWithVerdict(t, s, workID, reviewAttempt, disposition)
			if state := checksRepairRevisionView(t, s, workID, *job).State; state != "satisfied" {
				t.Fatalf("unaccepted Review changed provisional satisfaction to %q", state)
			}
			watermark, err := workerChecksRepairWatermarkTx(ctx, s.db, workID)
			if err != nil {
				t.Fatal(err)
			}
			history, err := workerChecksRepairWalkTx(ctx, s.db, workID, watermark)
			if err != nil {
				t.Fatal(err)
			}
			if repair := history.confirmingAttemptOf(reviewAttempt); repair == nil || repair.voided || repair.confirmed {
				t.Fatalf("unaccepted Review decided the repair: %+v", repair)
			}
			if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": reviewAttempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
				t.Fatalf("accept the blocking Review: %v", err)
			}
			if state := checksRepairRevisionView(t, s, workID, *job).State; state != "recorded" {
				t.Fatalf("accepting the voiding review restored the named revision to %q", state)
			}
			if disposition == "revision_required" {
				return
			}
			checksRepairRequestCorrection(t, s, workID, acceptor)

			// The named revision is unresolved again and the window is open: the
			// voided repair admits no replacement review.
			anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
			if anchorErr != nil || anchor == nil {
				t.Fatalf("correction anchor after the voiding review = (%#v, %v), want the open window restored", anchor, anchorErr)
			}
			if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(candidates) != 0 {
				t.Fatalf("candidates after the voiding review = (%#v, %v), want none", candidates, err)
			}
			// The blocking Review's own failure lands on the repairing revision.
			// Re-recording that obligation admits recovery under ordinary dispatch.
			if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, func() map[string]any {
				fields := repairFields
				delete(fields, "repair_revisions")
				delete(fields, "repair_reason")
				return fields
			}()); err != nil {
				t.Fatalf("record the repairing revision's own obligation: %v", err)
			}
			own := checksRepairLatestBinding(t, s, workID, jobID)
			if own == nil {
				t.Fatal("the repairing revision's own obligation is not recorded")
			}
			pin = issue1013Pin(t, s, workID)
			issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
			pin = issue1013Pin(t, s, workID)
			ownAttempt := "attempt:" + workID + ":own"
			ownPacket := dispatchWorkerPacket(t, s, workID, "repair", ownAttempt)
			ownPacket["schema_version"] = WorkerPacketSchemaVersion
			ownPacket["inputs"].(map[string]any)["correction"] = pin.Correction
			ownPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *own, "implement")
			if err := dispatchJobPacketForTest(t, s, workID, ownAttempt, worker, "checks-repair-void-own", ownPacket); err != nil {
				t.Fatalf("the own-obligation recovery dispatch refused: %v", err)
			}
			recordJobBoundWorkerDispatch(t, s, workID, ownAttempt, own)
			s2WorkerFailedAttributedLive(t, s, workID, ownAttempt, "the re-recorded obligation still failed")

			// The operator supersedes the contract. The successor carries the job
			// forward, so recovery re-records the obligation under the active contract
			// and its acceptance discharges the repairing revision's debt.
			checksRepairSupersedeContract(t, s, workID)
			if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, func() map[string]any {
				fields := repairFields
				delete(fields, "repair_revisions")
				delete(fields, "repair_reason")
				return fields
			}()); err != nil {
				t.Fatalf("record the recovery revision under the successor contract: %v", err)
			}
			recovered := checksRepairLatestBinding(t, s, workID, jobID)
			if recovered == nil {
				t.Fatal("the recovery revision is not recorded")
			}
			if got := checksRepairRevisionView(t, s, workID, *recovered).ContractVersion; got != 2 {
				t.Fatalf("recovery revision contract = %d, want the successor's 2", got)
			}
			pin = issue1013Pin(t, s, workID)
			issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
			pin = issue1013Pin(t, s, workID)
			recoveryAttempt := "attempt:" + workID + ":recovery"
			recoveryPacket := dispatchWorkerPacket(t, s, workID, "repair", recoveryAttempt)
			recoveryPacket["schema_version"] = WorkerPacketSchemaVersion
			recoveryPacket["inputs"].(map[string]any)["correction"] = pin.Correction
			recoveryPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *recovered, "implement")
			if err := dispatchJobPacketForTest(t, s, workID, recoveryAttempt, worker, "checks-repair-void-recovery", recoveryPacket); err != nil {
				t.Fatalf("the successor-contract recovery dispatch refused: %v", err)
			}
			recordJobBoundWorkerDispatch(t, s, workID, recoveryAttempt, recovered)
			completeAndAcceptAttempt(t, s, workID, recoveryAttempt, reviewGateLane(t, "implementation"), acceptor)
			// Satisfying the revision admits no second authoring dispatch.
			secondPacket := dispatchWorkerPacket(t, s, workID, "repair", "attempt:"+workID+":recovery-2")
			secondPacket["schema_version"] = WorkerPacketSchemaVersion
			secondPacket["inputs"].(map[string]any)["correction"] = pin.Correction
			secondPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *recovered, "implement")
			if err := dispatchJobPacketForTest(t, s, workID, "attempt:"+workID+":recovery-2", worker, "checks-repair-void-recovery-2", secondPacket); err == nil || !strings.Contains(err.Error(), "selected worker-job revision is not ready") {
				t.Fatalf("second authoring dispatch = %v, want the satisfied revision's readiness refusal", err)
			}

			if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(candidates) != 0 {
				t.Fatalf("candidates after recovery = (%#v, %v), want the same voided state", candidates, err)
			}
			anchor, anchorErr = workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
			if anchorErr != nil || anchor == nil {
				t.Fatalf("correction anchor after recovery = (%#v, %v), want the open window", anchor, anchorErr)
			}
		})
	}
}

// A contract supersession before the confirming Review is accepted voids an
// unconfirmed repair: the named revisions lose their provisional readiness,
// the review is no longer a candidate, and no repair under the active contract
// can name those revisions (CD-0205 D4).
func TestChecksRepairSupersessionVoidsUnconfirmedRepair(t *testing.T) {
	const workID = "checks-repair-supersession-void"
	const jobID = "job:checks-repair-supersede"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	reviewSubjectClaim(t, s, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checks-repair-supersede-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	acceptor := reviewGateAcceptor(workID)

	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-supersede job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{"concord check --supersede-original --exit-code=never"}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-supersede-ready"},
	}); err != nil {
		t.Fatalf("record the original revision: %v", err)
	}
	if latest := checksRepairLatestBinding(t, s, workID, jobID); latest != nil {
		*job = *latest
	}
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, 0, "checks-repair-supersede-1", job)
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "the recorded check names a command no admitted lane can execute")

	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-supersede job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{"go test ./internal/store/ -run TestChecksRepairSupersede"}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-supersede-ready"},
		"repair_revisions": []int64{1}, "repair_reason": "the named revision's checks invoked a command no admitted lane can execute or decide as written",
	}); err != nil {
		t.Fatalf("record the repair: %v", err)
	}
	views, err := readWorkerJobRevisions(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	repairing := views[len(views)-1].Binding
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt2 := "attempt:" + workID + ":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "checks-repair-supersede-2", &repairing)
	completeChecksRepairAttemptObserved(t, s, workID, attempt2, "checks-repair-completed-"+attempt2)
	acceptChecksRepairAttempt(t, s, workID, attempt2, acceptor)
	if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || !slices.Equal(candidates, []WorkerJobBinding{repairing}) {
		t.Fatalf("candidates behind the unconfirmed repair = (%#v, %v), want the exact repairing binding", candidates, err)
	}

	// The operator supersedes the contract before a confirming acceptance: the
	// supersession voids the unconfirmed repair, the named revision loses its
	// provisional readiness, and no confirming review remains available.
	checksRepairSupersedeContract(t, s, workID)
	watermark, err := workerChecksRepairWatermarkTx(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := workerChecksRepairWalkTx(ctx, s.db, workID, watermark)
	if err != nil {
		t.Fatal(err)
	}
	if repair := history.repairOfBinding(repairing.JobID, repairing.Revision); repair == nil || !repair.voided {
		t.Fatal("the correction walk did not void the superseded repair")
	}
	if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(candidates) != 0 {
		t.Fatalf("candidates after the supersession = (%#v, %v), want none", candidates, err)
	}
	if err := dispatchConfirmingReview(t, s, workID, "attempt:"+workID+":review-superseded", &repairing, fixture.owner); err == nil {
		t.Fatal("a review dispatch was admitted after the supersession voided the repair")
	}
	// The named revision's obligation is unresolved again, so the window the
	// failure opened stays open behind the stale authority.
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("correction anchor after the supersession = (%#v, %v), want the open window", anchor, anchorErr)
	}
}

func TestChecksRepairPendingAcceptanceCannotAssertDelivery(t *testing.T) {
	state := workflowDeliveryAdmission{
		JobsRecorded: true, JobsIntegrated: true,
		ChecksRepairPendingKeys: workerJobKey("job:pending-repair", 2),
	}
	if failure := workflowDeliveryJobsFailure(state, nil); failure != nil {
		t.Fatalf("an unaccepted nonlatest repair opened delivery debt: %v", failure)
	}
	if failure := workflowDeliveryJobsFailure(state, &WorkerJobBinding{JobID: "job:other", Revision: 1}); failure != nil {
		t.Fatalf("an unrelated acceptance opened repair debt: %v", failure)
	}
	if failure := workflowDeliveryJobsFailure(state, &WorkerJobBinding{JobID: "job:pending-repair", Revision: 2}); failure == nil {
		t.Fatal("the combined acceptance asserted delivery while opening an unconfirmed repair")
	}
}

// completeChecksRepairAttemptObserved completes an implementation attempt
// with a core-observed clean delivery on the work's claimed worktree, so the
// accepted result anchors the confirming review's subject join (CD-0205 D2).
func completeChecksRepairAttemptObserved(t *testing.T, s *Store, workID, attemptID, eventID string) {
	t.Helper()
	lane := checksRepairAttemptLane(t, s, workID, attemptID)
	dispatched, err := workflowDispatchedJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	completion := Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(42, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		WorkerDirectory: workerFixtureDirectory(t, s, workID), WorkerJob: dispatched, EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane),
	})}
	prepared, prepErr := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attemptID, EventID: eventID})
	if prepErr != nil {
		t.Fatalf("terminal delivery preparation refused: %v", prepErr)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
}

func acceptChecksRepairAttempt(t *testing.T, s *Store, workID, attemptID string, acceptor WorkflowActor) {
	t.Helper()
	acceptChecksRepairAttemptAtStep(t, s, workID, attemptID, "repair", acceptor)
}

func acceptChecksRepairAttemptAtStep(t *testing.T, s *Store, workID, attemptID, stepID string, acceptor WorkflowActor) {
	t.Helper()
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": latestStepStartEpoch(t, s, workID, stepID)}), 0, acceptor); err != nil {
		t.Fatalf("accept %s: %v", attemptID, err)
	}
}

func checksRepairAttemptLane(t *testing.T, s *Store, workID, attemptID string) LaneDefinition {
	t.Helper()
	var laneID string
	var laneDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lane_id,lane_digest FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&laneID, &laneDigest); err != nil {
		t.Fatal(err)
	}
	lane, err := LookupLane(laneID, laneDigest)
	if err != nil {
		t.Fatal(err)
	}
	return lane
}

// completeChecksRepairReviewWithVerdict completes a dispatched review attempt
// with the named disposition. Readiness carries the Review block alone; a
// corrective report binds one classified delivery blocker beside its unavailable
// receipt, the shape an oracle-bound review of the repairing revision owes.
func completeChecksRepairReviewWithVerdict(t *testing.T, s *Store, workID, attemptID, verdict string) {
	t.Helper()
	lane := checksRepairAttemptLane(t, s, workID, attemptID)
	dispatched, err := workflowDispatchedJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	review := currentReviewBlockForTest(t, s, workID, attemptID, verdict)
	project := ""
	if verdict == "correction_required" {
		project = checksRepairRevisionView(t, s, workID, *dispatched).ProjectScope
		review.Findings = []WorkerReviewFinding{{
			FindingID: "finding:checks-repair", InScope: true, Blocking: true, Status: "open",
			EvidenceRefs: []string{"evidence:checks-repair-review"}, ClosureRefs: []string{},
			Severity: "P1", Confidence: "high",
			Detail: "the repaired checks still cannot be executed or decided as written",
			Oracle: &WorkerOracleFinding{
				Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:fixture",
				FailureFamily: "checks-repair", PredicateIDs: fixtureRepairPredicateIDs(t, s, workID),
				ControlIDs: []string{"control:fixture"}, EvidenceRefs: []string{"evidence:checks-repair-review"},
			},
		}}
	}
	event := checksRepairReviewCompletion(t, s, workID, attemptID, lane, dispatched, review, project)
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attemptID, EventID: event.EventID})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(t.Context(), prepared), s, event)
}

func checksRepairReviewCompletion(t *testing.T, s *Store, workID, attemptID string, lane LaneDefinition, dispatched *WorkerJobBinding, review *WorkerReviewBlock, project string) Event {
	t.Helper()
	payload := WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: WorkerReportSchemaVersion,
		WorkerDirectory: workerFixtureDirectory(t, s, workID), WorkerJob: dispatched, EvidenceOrigin: WorkerEvidenceReported,
		Review: review, Evidence: reportedLaneEvidenceForTest(lane, review),
	}
	if review.Disposition == "correction_required" {
		payload.Evidence[0].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:fixture"}, CaseIDs: []string{"case:fixture"},
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: project, Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
			Result:       OracleReceiptResultUnavailable, RunRef: "", EvidenceRefs: []string{"harness could not resolve the repaired check"},
		}
	}
	return Event{EventID: "checks-repair-review-completed-" + attemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(46, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
}

func fixtureRepairPredicateIDs(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil || len(views) == 0 || views[len(views)-1].AcceptanceOracle == nil || len(views[len(views)-1].AcceptanceOracle.Owners) == 0 {
		t.Fatalf("read the repairing revision's oracle for %s: %v", workID, err)
	}
	return views[len(views)-1].AcceptanceOracle.Owners[0].PredicateIDs
}

func checksRepairRequestCorrection(t *testing.T, s *Store, workID string, actor WorkflowActor) {
	t.Helper()
	blocked := issue1013Pin(t, s, workID)
	reportRef := blocked.Review.Accepted.ReportRef
	lineage, err := readWorkerOracleFindingLineageTx(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "review", "evidence_ref": reportRef}), 0, actor); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "request_correction", mustJSONValue(map[string]any{
		"predicate_ids": fixtureRepairPredicateIDs(t, s, workID), "evidence_refs": []string{reportRef}, "open_finding_ids": lineage.openFindingIDs(),
		"diagnosis": "The accepted Review finds the repairing checks cannot run.", "strategy": "Re-record and repair the unresolved obligation.",
	}), 0, actor); err != nil {
		t.Fatal(err)
	}
}

// checksRepairLatestBinding returns the latest recorded binding of one job.
func checksRepairLatestBinding(t *testing.T, s *Store, workID, jobID string) *WorkerJobBinding {
	t.Helper()
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	var latest *WorkerJobBinding
	for i := range views {
		if views[i].Binding.JobID == jobID {
			binding := views[i].Binding
			latest = &binding
		}
	}
	return latest
}

// checksRepairSupersedeContract supersedes the active contract through the
// folded supersession event, the way the operator's typed supersession
// lands on a non-Product-changing break_fix contract: a fully supplied
// successor with no Product authority and the fixture's own root Domain.
func checksRepairSupersedeContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	ownerRef, err := WorkflowActorRef(WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": 1, "new_contract_version": 2,
		"supersede_reason": "the pinned contract moved past the accepted delivery",
		"audit_evidence":   []string{"evidence:checks-repair-supersede"},
		"successor_contract": map[string]any{
			"contract_version": 2, "premise": "deliver the checked change under the second contract", "outcome_kind": "check",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:return-route", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}}},
			"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
			"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"architecture_binding": WorkflowArchitectureBinding{
				DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
				DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{},
			},
		},
	}
	event := workflowEventWithActor("checks-repair-supersede-"+workID, WorkflowContractSuperseded, workID, ownerRef, payload)
	event.PayloadVersion = 1
	event = withPinnedDecisionFact(t, pinnedWorkflowDefinitionForTest(t, s, workID), event)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("supersede contract v1: %v", err)
	}
}

// The confirming acceptance closes the correction only when no other recorded
// revision stays unresolved: an unrelated job's failed obligation keeps it open.
func TestChecksRepairConfirmingAcceptanceKeepsUnrelatedJobDebt(t *testing.T) {
	const workID = "checks-repair-unrelated-debt"
	const jobID = "job:checks-repair-debt"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	reviewSubjectClaim(t, s, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checks-repair-debt-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	acceptor := reviewGateAcceptor(workID)

	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-debt job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{"concord check --debt-original --exit-code=never"}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-debt-ready"},
	}); err != nil {
		t.Fatalf("record the original revision: %v", err)
	}
	if latest := checksRepairLatestBinding(t, s, workID, jobID); latest != nil {
		*job = *latest
	}
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, 0, "checks-repair-debt-1", job)
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "the recorded check names a command no admitted lane can execute")

	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-debt job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{"go test ./internal/store/ -run TestChecksRepairDebt"}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-debt-ready"},
		"repair_revisions": []int64{1}, "repair_reason": "the named revision's checks invoked a command no admitted lane can execute or decide as written",
	}); err != nil {
		t.Fatalf("record the repair: %v", err)
	}
	repairing := checksRepairLatestBinding(t, s, workID, jobID)
	if repairing == nil {
		t.Fatal("the repairing revision is not recorded")
	}
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt2 := "attempt:" + workID + ":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "checks-repair-debt-2", repairing)
	completeChecksRepairAttemptObserved(t, s, workID, attempt2, "checks-repair-completed-"+attempt2)
	acceptChecksRepairAttempt(t, s, workID, attempt2, acceptor)

	// A separate Review assignment can inspect the integrated candidate while
	// the checks repair awaits confirmation. Its failed obligation must not be
	// discharged by the repairing assignment's confirming acceptance.
	unrelated := &WorkerJobBinding{JobID: "job:unrelated-debt", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, unrelated)
	unrelatedAttempt := "attempt:" + workID + ":unrelated"
	if err := dispatchConfirmingReview(t, s, workID, unrelatedAttempt, unrelated, fixture.owner); err != nil {
		t.Fatal(err)
	}
	s2WorkerFailedAttributedLive(t, s, workID, unrelatedAttempt, "the unrelated job's check cannot run")

	reviewAttempt := "attempt:" + workID + ":review-debt"
	if err := dispatchConfirmingReview(t, s, workID, reviewAttempt, repairing, fixture.owner); err != nil {
		t.Fatalf("the confirming review dispatch refused: %v", err)
	}
	completeChecksRepairReviewWithVerdict(t, s, workID, reviewAttempt, "continue")
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": reviewAttempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept the confirming Review's continuation: %v", err)
	}

	// The repair is confirmed, but the unrelated job's obligation stays
	// unresolved and keeps the correction open.
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("correction anchor after the confirming acceptance = (%#v, %v), want the open window on the unrelated job's debt", anchor, anchorErr)
	}
	if state := checksRepairRevisionView(t, s, workID, *unrelated).State; state != "recorded" {
		t.Fatalf("unrelated revision state = %q, want its obligation unresolved", state)
	}
	watermark, err := workerChecksRepairWatermarkTx(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	history, err := workerChecksRepairWalkTx(ctx, s.db, workID, watermark)
	if err != nil {
		t.Fatal(err)
	}
	if repair := history.confirmingAttemptOf(reviewAttempt); repair == nil || !repair.confirmed || repair.voided {
		t.Fatalf("accepted continue did not confirm the exact repair: %+v", repair)
	}
}

// checksRepairRefineProof seeds one green verify run of the work's primary
// Project acquired now and binds its verification evidence, so a
// delivery-asserting accept at refine passes the CD-0192 refine-exit proof
// and the admission's later gates answer for themselves.
func checksRepairRefineProof(t *testing.T, s *Store, workID string) {
	t.Helper()
	head := issue1013Pin(t, s, workID).Review.Candidate.Commit
	git := checksRepairClaimGit(t, s, workID, workerFixtureDirectory(t, s, workID), head)
	req := verifyRequest(git, "checks-repair-refine-proof-"+workID, []string{"go", "vet", "./..."}, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
	req.WorkID, req.ProjectID = workID, "project"
	req.PrincipalRef = "principal/operator"
	run, err := s.VerifyWorktree(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "evidence_ref": run.OperationRef}), 0, reviewGateAcceptor(workID)); err != nil {
		t.Fatal(err)
	}
}

// An abandoned confirming review voids the repair exactly as a failed one
// does (CD-0205 D4): the named revision loses its provisional readiness at
// the abandonment terminal, and the voided repair admits no replacement
// confirming review while the old and new checks stay on
// record.
func TestChecksRepairAbandonedConfirmingReviewVoidsRepair(t *testing.T) {
	const workID = "checks-repair-abandoned-void"
	const jobID = "job:checks-repair-abandoned"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	reviewSubjectClaim(t, s, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checks-repair-abandoned-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	acceptor := reviewGateAcceptor(workID)

	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-abandoned job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{"concord check --abandoned-original --exit-code=never"}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-abandoned-ready"},
	}); err != nil {
		t.Fatalf("record the original revision: %v", err)
	}
	if latest := checksRepairLatestBinding(t, s, workID, jobID); latest != nil {
		*job = *latest
	}
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, 0, "checks-repair-abandoned-1", job)
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "the recorded check names a command no admitted lane can execute")

	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-abandoned job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{"go test ./internal/store/ -run TestChecksRepairAbandoned"}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-abandoned-ready"},
		"repair_revisions": []int64{1}, "repair_reason": "the named revision's checks invoked a command no admitted lane can execute or decide as written",
	}); err != nil {
		t.Fatalf("record the repair: %v", err)
	}
	repairing := checksRepairLatestBinding(t, s, workID, jobID)
	if repairing == nil {
		t.Fatal("the repairing revision is not recorded")
	}
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attempt2 := "attempt:" + workID + ":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, pin.Correction, worker, pin.Version, "checks-repair-abandoned-2", repairing)
	completeChecksRepairAttemptObserved(t, s, workID, attempt2, "checks-repair-completed-"+attempt2)
	acceptChecksRepairAttempt(t, s, workID, attempt2, acceptor)

	// The confirming review dispatches once; its host abandons before
	// any report exists, and the abandonment terminal voids the repair.
	reviewAttempt := "attempt:" + workID + ":review-abandoned"
	if err := dispatchConfirmingReview(t, s, workID, reviewAttempt, repairing, fixture.owner); err != nil {
		t.Fatalf("the confirming review dispatch refused: %v", err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkerEvidenceOperationTx(ctx, tx, s2WorkerFailedAttributedEvent(workID, reviewAttempt, "failed-"+reviewAttempt, "", WorkerFailureAbandoned, "the host ended without a report", time.Unix(47, 0).UTC()))
		return err
	}); err != nil {
		t.Fatalf("the abandonment terminal ingest: %v", err)
	}

	// The void is visible before any coordinator move: the named revision's
	// provisional readiness is gone, no review candidate stands, and the old
	// and new checks stay on record.
	var namedState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, jobID, 1).Scan(&namedState); err != nil {
		t.Fatal(err)
	}
	if namedState != "recorded" {
		t.Fatalf("the named revision's state after the abandonment = %q, want the provisional readiness reverted to recorded", namedState)
	}
	if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(candidates) != 0 {
		t.Fatalf("candidates after the abandonment = (%#v, %v), want none", candidates, err)
	}
	recorded, recordedErr := readWorkerJobChecksRepairsTx(ctx, s.DatabaseForTesting(), workID)
	if recordedErr != nil {
		t.Fatal(recordedErr)
	}
	if _, onRecord := recorded[*repairing]; !onRecord {
		t.Fatal("the voided repair's recorded metadata left the log")
	}
	// The correction the original failure opened stays open on the named revision.
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("correction anchor after the abandonment = (%#v, %v), want the open window", anchor, anchorErr)
	}
}

// The delivery-asserting accept runs the one delivery admission the
// standalone record_delivery runs (CD-0205 D3/D5): behind an unconfirmed
// checks repair's pending confirming Review it refuses on the same shared gate, on the
// combined route exactly as on the standalone route.
func TestChecksRepairUnconfirmedReviewHoldsCombinedAcceptRoute(t *testing.T) {
	const workID = "checks-repair-debt-combined"
	const jobID = "job:checks-repair-combined"
	fixture := seedChecksRepairFixture(t, workID, "refine")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	reviewGateStartStep(t, s, workID, "refine", "start_refine", fixture.owner)
	reviewSubjectClaim(t, s, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checks-repair-combined-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	acceptor := reviewGateAcceptor(workID)
	contextFixture := workContextFixture{store: s, workID: workID, owner: worker}
	// The current candidate remains unchanged while checks are repaired. A
	// real preceding delivery and accepted ready Review establish exit proof.
	anchorJob := &WorkerJobBinding{JobID: "job:checks-repair-anchor", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, anchorJob)
	anchorAttempt := "attempt:" + workID + ":anchor"
	dispatchJobBoundAttempt(t, s, workID, "refine", anchorAttempt, nil, worker, 0, "checks-repair-combined-anchor", anchorJob)
	repo := workerFixtureDirectory(t, s, workID)
	anchorPath := "internal/store/" + anchorJob.JobID + "/anchor.txt"
	if err := os.MkdirAll(repo+"/internal/store/"+anchorJob.JobID, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo+"/"+anchorPath, []byte("integrated candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "add", anchorPath)
	gitRunStore(t, repo, "commit", "-m", "implement the fixture candidate")
	completeChecksRepairAttemptObserved(t, s, workID, anchorAttempt, "checks-repair-anchor-completed")
	acceptChecksRepairAttemptAtStep(t, s, workID, anchorAttempt, "refine", acceptor)
	checksRepairRefineProof(t, s, workID)
	acceptCurrentReviewForTest(t, contextFixture, workerFixtureDirectory(t, s, workID), "ready_for_final_proof")

	job := &WorkerJobBinding{JobID: jobID, Revision: 1}
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-combined job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{"concord check --combined-original --exit-code=never"}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-combined-ready"},
	}); err != nil {
		t.Fatalf("record the original revision: %v", err)
	}
	if latest := checksRepairLatestBinding(t, s, workID, jobID); latest != nil {
		*job = *latest
	}
	attempt1 := "attempt:" + workID + ":1"
	if err := dispatchConfirmingReview(t, s, workID, attempt1, job, worker); err != nil {
		t.Fatal(err)
	}
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "the recorded check names a command no admitted lane can execute")

	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Corrective bounded objective for the checks-repair-combined job",
		"stopping_condition": "the recorded checks pass with no unresolved reference", "path_scope": []string{"internal/store"},
		"checks": []string{"go test ./internal/store/ -run TestChecksRepairCombined"}, "reserved_integration": "integration evidence binds at the parent effect step",
		"ready": true, "readiness_evidence": []string{"evidence:checks-repair-combined-ready"},
		"repair_revisions": []int64{1}, "repair_reason": "the named revision's checks invoked a command no admitted lane can execute or decide as written",
	}); err != nil {
		t.Fatalf("record the repair: %v", err)
	}
	repairing := checksRepairLatestBinding(t, s, workID, jobID)
	if repairing == nil {
		t.Fatal("the repairing revision is not recorded")
	}
	attempt2 := "attempt:" + workID + ":2"
	failedAnchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "refine", "")
	if anchorErr != nil || failedAnchor == nil {
		t.Fatalf("correction anchor after the failed job at refine = (%#v, %v), want the open window", failedAnchor, anchorErr)
	}
	if err := dispatchConfirmingReview(t, s, workID, attempt2, repairing, worker); err != nil {
		t.Fatal(err)
	}
	// The refine-exit proof stays green: the combined accept gets past the
	// proof gate, so the refusal behind the pending confirming Review is its own gate and
	// nothing ahead of it.
	completeChecksRepairReviewWithVerdict(t, s, workID, attempt2, "ready_for_final_proof")

	// The combined accept asserts delivery in the same event that records
	// the job disposition: behind the unconfirmed repair's pending confirming Review it
	// refuses on the shared delivery admission, naming the confirming Review the
	// standalone route names.
	err := runDeliveryAdmissionAction(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attempt2, "attempt_epoch": reviewSubjectWindow(t, s, workID, attempt2).AttemptEpoch,
		"delivery_artifact": "artifact:checks-repair-combined", "delivery_state": "asserted",
	}), refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."}), acceptor)
	if err == nil || !strings.Contains(err.Error(), "requires an accepted confirming Review") {
		t.Fatalf("the combined accept behind the pending confirming Review = %v, want the shared delivery admission's confirming-Review refusal", err)
	}
	// The refusal recorded no disposition: on the combined route the
	// acceptance and the delivery assertion are one event, so nothing
	// opened — the repairing revision stays recorded with nothing consumed,
	// and the plain local accept afterward opens the repair with its confirming
	// review untouched.
	var repairingState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, jobID, repairing.Revision).Scan(&repairingState); err != nil {
		t.Fatal(err)
	}
	if repairingState != "recorded" {
		t.Fatalf("the repairing revision's state after the refused combined accept = %q, want recorded with nothing consumed", repairingState)
	}
	if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(candidates) != 0 {
		t.Fatalf("candidates after the refused combined accept = (%#v, %v), want none: the pending disposition opened nothing", candidates, err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attempt2, "attempt_epoch": reviewSubjectWindow(t, s, workID, attempt2).AttemptEpoch}), 0, acceptor); err != nil {
		t.Fatal(err)
	}
	if candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID); err != nil || !slices.Equal(candidates, []WorkerJobBinding{*repairing}) {
		t.Fatalf("candidates after the plain accepting disposition = (%#v, %v), want the exact repairing binding", candidates, err)
	}
}
