package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
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
	for _, pair := range []struct{ predecessor, successor WorkflowComposition }{
		{implementationConsequenceV34(), implementationCompositionV35()},
		{breakFixConsequenceV31(), breakFixCompositionV32()},
	} {
		got, want := pair.successor, pair.predecessor
		if got.Version != want.Version+1 || got.ActionCatalogVersion != 2 || got.ApprovalPolicy != WorkflowApprovalPolicyConsequenceV1 {
			t.Fatalf("%s successor v%d catalog %d policy %q does not preserve the consequence head", got.Ref, got.Version, got.ActionCatalogVersion, got.ApprovalPolicy)
		}
		got.Version, got.ActionCatalogVersion = want.Version, want.ActionCatalogVersion
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s checks repair changed composition beyond version and action catalog", got.Ref)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,subject_type,subject_id,kind,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,'2026-10-19T00:00:00Z',1,?)`, "checks-repair-unreadable-marker", SubjectWorkItem, workID, WorkflowActionCompleted, "operator", `{"action_id":"dispatch_worker","worker_attempt_id":"attempt:unreadable","checks_repair_review":{"worker_job":{}}}`); err != nil {
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
// recording, local acceptance, the one funded confirming review at the wall,
// the void paths, and the productive reset.

func TestChecksRepairSatisfiesNamedRevisionsAndFundsOneConfirmingReview(t *testing.T) {
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
	if count := jobWindowCorrectionCount(t, s, workID); count != 2 {
		t.Fatalf("correction count after two failed job-bound attempts = %d, want 2", count)
	}

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
	// holds, and the acceptance opens no correction window and resets no
	// attempt count. The completion carries the core-observed clean delivery,
	// so the accepted result anchors the confirming review's subject.
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
	if count := jobWindowCorrectionCount(t, s, workID); count != 3 {
		t.Fatalf("correction count after the repairing acceptance = %d, want all 3 dispatches preserved with no reset", count)
	}
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil || !anchor.Escalated || anchor.AttemptCount != 3 {
		t.Fatalf("correction anchor after the repairing acceptance = (%#v, %v), want the escalated open window with 3 counted dispatches", anchor, anchorErr)
	}
	if nonProgress, err := workflowNonProgressAttemptCountForRegistry(ctx, s.DatabaseForTesting(), workID, "checks-repair", BuiltinWorkflowRegistry()); err != nil || nonProgress != 2 {
		t.Fatalf("non-progress attempts after the repairing acceptance = %d, %v, want the 2 failed attempts preserved", nonProgress, err)
	}
	// Nothing is dispatch-ready while the unconfirmed repair stands: the
	// named revisions are satisfied for readiness only, and the repairing
	// revision is satisfied.
	if ready, err := readyWorkerJobRevisions(ctx, s.db, workID); err != nil || len(ready) != 0 {
		t.Fatalf("ready revisions behind the unconfirmed repair = %#v, %v, want none", ready, err)
	}
	// A later ordinary revision cannot hide an accepted repair's review debt.
	later := jobFields("go test ./internal/store/ -run TestChecksRepair", nil)
	later["ready"] = false
	record(later, "later ordinary revision")
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	var delivery workflowDeliveryAdmission
	if _, err := loadWorkflowDeliveryJobAdmission(ctx, s.db, workID, definition.Definition, &delivery); err != nil {
		t.Fatal(err)
	}
	if !delivery.ChecksRepairReviewDebt {
		t.Fatal("a later ordinary revision hid the unconfirmed repair's review debt")
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
	// Delivery refuses while the review debt stands; the refusal text is
	// implementation-owned.
	if err := runVerdictActionAs(t, s, workID, "record_delivery", mustJSONValue(map[string]any{"delivery_artifact": "artifact:checks-repair-early", "delivery_state": "asserted"}), 0, acceptor); err == nil {
		t.Fatal("delivery advanced while the unconfirmed checks repair's review debt stands")
	}
	// At most one unconfirmed repair per job: a second repair of the same
	// named revisions refuses while the first stands.
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, jobFields("go test ./internal/store/ -run TestChecksRepairSecond", []int64{1, 2})); err == nil {
		t.Fatal("a second unconfirmed checks repair was recorded for the same job while the first stands")
	}

	// The accepted repair funds exactly one independent confirming review for
	// this job, admitted even at the escalation wall.
	reviewAttempt := "attempt:" + workID + ":review-1"
	if err := dispatchConfirmingReview(t, s, workID, reviewAttempt, rev3, fixture.owner); err != nil {
		t.Fatalf("the funded confirming review dispatch refused at the wall: %v", err)
	}
	if bound, err := workflowDispatchedJobForAttempt(ctx, s.DatabaseForTesting(), workID, reviewAttempt); err != nil || bound == nil || *bound != *rev3 {
		t.Fatalf("confirming review binding = (%#v, %v), want the satisfied repairing revision %s@%d", bound, err, rev3.JobID, rev3.Revision)
	}
	// The funding is consumed once: a second confirming review dispatch for
	// the same repair refuses.
	if err := dispatchConfirmingReview(t, s, workID, "attempt:"+workID+":review-2", rev3, fixture.owner); err == nil {
		t.Fatal("a second confirming review dispatch was admitted for the same repair")
	}
	// No authoring attempt is funded: an implementation dispatch of the
	// satisfied repairing revision refuses.
	authoringPacket := dispatchWorkerPacket(t, s, workID, "repair", "attempt:"+workID+":authoring")
	authoringPacket["schema_version"] = WorkerPacketSchemaVersion
	authoringPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *rev3, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, "attempt:"+workID+":authoring", worker, "checks-repair-authoring", authoringPacket); err == nil {
		t.Fatal("an authoring dispatch of the satisfied repairing revision was admitted")
	}

	// Only acceptance of the confirming review's ship verdict confirms the
	// repair and satisfies the named revisions' failed obligations. The
	// review's own completed ship report leaves the repair unconfirmed, and
	// nothing dispatches in between: a second review dispatch refuses before
	// the verdict lands.
	completeChecksRepairReviewWithVerdict(t, s, workID, reviewAttempt, "ship")
	if fundings, err := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(fundings) != 0 {
		t.Fatalf("fundings behind the unaccepted ship report = (%#v, %v), want none: the one confirming review already dispatched", fundings, err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": reviewAttempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept the confirming review's ship verdict: %v", err)
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

	// The confirmed repair opens the productive window: no other recorded
	// revision stays unresolved, so the acceptance resets the count and the
	// non-progress budget.
	if count := jobWindowCorrectionCount(t, s, workID); count != 0 {
		t.Fatalf("correction count after the confirming acceptance = %d, want the productive reset to 0", count)
	}
	if pin := issue1013Pin(t, s, workID); pin.Correction != nil {
		t.Fatalf("correction window after the confirming acceptance = %#v, want it closed", pin.Correction)
	}
	if nonProgress, err := workflowNonProgressAttemptCountForRegistry(ctx, s.DatabaseForTesting(), workID, "checks-repair", BuiltinWorkflowRegistry()); err != nil || nonProgress != 0 {
		t.Fatalf("non-progress attempts after the confirming acceptance = %d, %v, want the productive reset to 0", nonProgress, err)
	}

	// Rebuild parity: the recorded repair metadata, the consumed funding, and
	// the closed window fold identically from the log.
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild from the log: %v", err)
	}
	if rebuilt := checksRepairRecordedEvent(t, s, workID, jobID, 3); string(rebuilt["checks_repair"]) == "" {
		t.Fatal("the rebuilt log lost the recorded checks repair metadata")
	}
	if count := jobWindowCorrectionCount(t, s, workID); count != 0 {
		t.Fatalf("correction count after the rebuild = %d, want the same closed window", count)
	}
	if pin := issue1013Pin(t, s, workID); pin.Correction != nil {
		t.Fatalf("rebuilt correction window = %#v, want it closed", pin.Correction)
	}
	assertIntegrationPopulation()
}

func dispatchConfirmingReview(t *testing.T, s *Store, workID, attemptID string, job *WorkerJobBinding, actor WorkflowActor) error {
	t.Helper()
	lane := reviewGateLane(t, "review")
	laneVersion, laneDigest := registeredLaneIdentity(t, "review")
	packet := joinPacketFor(t, s, workID, "repair", attemptID, "review", laneVersion, laneDigest)
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
	// The wall's open correction window is part of the packet's truth: the
	// confirming review consumes the same current correction context any
	// dispatch at this step consumes.
	if correction, correctionErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", attemptID); correctionErr != nil {
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
	dispatch := Event{EventID: "checks-repair-review-dispatch-" + attemptID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(45, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: packetDigest,
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

// A confirming review whose verdict is no_ship voids the repair: the named
// revisions become unresolved again, the review counts as non-progress, the
// old and new checks stay on record, and neither the voided repair nor a new
// repair of the same revisions funds any dispatch (CD-0205 D4).
func TestChecksRepairNoShipReviewVoidsRepair(t *testing.T) {
	const workID = "checks-repair-no-ship-void"
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

	// The funded confirming review dispatches once; its completed no_ship
	// report voids the repair at the verdict, and its acceptance only binds
	// the findings. The named revision loses its provisional readiness.
	reviewAttempt := "attempt:" + workID + ":review-void"
	if err := dispatchConfirmingReview(t, s, workID, reviewAttempt, repairing, fixture.owner); err != nil {
		t.Fatalf("the funded confirming review dispatch refused: %v", err)
	}
	var markerFunded float64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.checks_repair_review.funded') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, reviewAttempt).Scan(&markerFunded); err != nil {
		t.Fatal(err)
	}
	if markerFunded != 1 {
		t.Fatalf("the funded confirming review recorded funded=%v, want 1", markerFunded)
	}
	completeChecksRepairReviewWithVerdict(t, s, workID, reviewAttempt, "no_ship")
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": reviewAttempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept the no_ship review: %v", err)
	}
	if state := checksRepairRevisionView(t, s, workID, *job).State; state != "recorded" {
		t.Fatalf("accepting the voiding review restored the named revision to %q", state)
	}

	// The named revision is unresolved again and the window is open: the
	// voided repair reset nothing and funds no replacement. Rebuild folds the
	// same voided state.
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("correction anchor after the voiding review = (%#v, %v), want the open window restored", anchor, anchorErr)
	}
	if fundings, err := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(fundings) != 0 {
		t.Fatalf("fundings after the voiding review = (%#v, %v), want none", fundings, err)
	}
	// The no_ship review's own failure lands on the repairing revision, and
	// the wall the three dispatches armed holds every recovery dispatch
	// until a store-derived convergence basis exists: the recovery attempt
	// on the repairing revision's own re-recorded obligation refuses without
	// one, exactly as the wall requires.
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
	if err := dispatchJobPacketForTest(t, s, workID, ownAttempt, worker, "checks-repair-void-own", ownPacket); err == nil || !strings.Contains(err.Error(), "without a convergence basis") {
		t.Fatalf("the recovery dispatch without a basis = %v, want the wall's convergence refusal", err)
	}

	// The operator supersedes the contract: a changed approach is the store
	// derived basis that admits exactly one fresh fenced dispatch at the
	// wall. The successor contract carries the job forward, so the recovery
	// re-records the obligation under the active contract and its acceptance
	// discharges the repairing revision's debt.
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
		t.Fatalf("the basis-admitted recovery dispatch refused: %v", err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, recoveryAttempt, recovered)
	var basis string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.retry_convergence.basis') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, recoveryAttempt).Scan(&basis); err != nil {
		t.Fatal(err)
	}
	if basis != "approach_changed" {
		t.Fatalf("recovery dispatch basis = %q, want approach_changed", basis)
	}
	completeAndAcceptAttempt(t, s, workID, recoveryAttempt, reviewGateLane(t, "implementation"), acceptor)
	// The consumed basis admits no second dispatch at the wall.
	secondPacket := dispatchWorkerPacket(t, s, workID, "repair", "attempt:"+workID+":recovery-2")
	secondPacket["schema_version"] = WorkerPacketSchemaVersion
	secondPacket["inputs"].(map[string]any)["correction"] = pin.Correction
	secondPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *recovered, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, "attempt:"+workID+":recovery-2", worker, "checks-repair-void-recovery-2", secondPacket); err == nil || !strings.Contains(err.Error(), "without a convergence basis") {
		t.Fatalf("the dispatch behind the consumed basis = %v, want the wall's convergence refusal", err)
	}

	// Rebuild folds the same voided state and the same open window from the
	// log alone.
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild after the void: %v", err)
	}
	if fundings, err := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(fundings) != 0 {
		t.Fatalf("fundings after the rebuild = (%#v, %v), want the same voided state", fundings, err)
	}
	anchor, anchorErr = workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("rebuilt correction anchor = (%#v, %v), want the open window", anchor, anchorErr)
	}
}

// A contract supersession before the ship verdict is accepted voids an
// unconfirmed repair: the named revisions lose their provisional readiness,
// the review stays unfunded, and no repair under the active contract can name
// those revisions (CD-0205 D4).
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
	if fundings, err := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(fundings) != 1 {
		t.Fatalf("fundings behind the unconfirmed repair = (%#v, %v), want the one funded review", fundings, err)
	}

	// The operator supersedes the contract before any ship verdict: the
	// supersession voids the unconfirmed repair, the named revision loses its
	// provisional readiness, and the review stays unfunded.
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
	if fundings, err := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(fundings) != 0 {
		t.Fatalf("fundings after the supersession = (%#v, %v), want none", fundings, err)
	}
	if err := dispatchConfirmingReview(t, s, workID, "attempt:"+workID+":review-superseded", &repairing, fixture.owner); err == nil {
		t.Fatal("a review dispatch was admitted after the supersession voided the repair")
	}
	// The named revision's obligation is unresolved again, so the window the
	// failure opened stays open behind the stale authority, and the rebuild
	// folds the same voided state.
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("correction anchor after the supersession = (%#v, %v), want the open window", anchor, anchorErr)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild after the supersession: %v", err)
	}
	if fundings, err := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); err != nil || len(fundings) != 0 {
		t.Fatalf("fundings after the rebuild = (%#v, %v), want the same voided state", fundings, err)
	}
	if anchor, anchorErr = workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", ""); anchorErr != nil || anchor == nil {
		t.Fatalf("rebuilt correction anchor = (%#v, %v), want the open window", anchor, anchorErr)
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
	completion := Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(42, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(WorkerCompletedPayload{
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
	var laneVersion int64
	var laneDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lane_id,lane_version,lane_digest FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&laneID, &laneVersion, &laneDigest); err != nil {
		t.Fatal(err)
	}
	lane, err := LookupLane(laneID, laneVersion, laneDigest)
	if err != nil {
		t.Fatal(err)
	}
	return lane
}

// completeChecksRepairReviewWithVerdict completes a dispatched review attempt
// with the named verdict. A ship report carries the review block alone; a
// no_ship report binds one classified delivery blocker beside its unavailable
// receipt, the shape an oracle-bound review of the repairing revision owes.
func completeChecksRepairReviewWithVerdict(t *testing.T, s *Store, workID, attemptID, verdict string) {
	t.Helper()
	lane := checksRepairAttemptLane(t, s, workID, attemptID)
	dispatched, err := workflowDispatchedJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	review := &WorkerReviewBlock{Disposition: verdict, Findings: []WorkerReviewFinding{}}
	project := ""
	if verdict == "no_ship" {
		project = checksRepairRevisionView(t, s, workID, *dispatched).ProjectScope
		review.Findings = []WorkerReviewFinding{{
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
	ctx := prepareWorkerTerminalDelivery(t, s, workID, attemptID, event.EventID)
	workerTerminalEventThroughSeamCtx(t, ctx, s, event)
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
		Actor: "worker:test", OccurredAt: time.Unix(46, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
}

func fixtureRepairPredicateIDs(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil || len(views) == 0 || views[len(views)-1].AcceptanceOracle == nil || len(views[len(views)-1].AcceptanceOracle.Owners) == 0 {
		t.Fatalf("read the repairing revision's oracle for %s: %v", workID, err)
	}
	return views[len(views)-1].AcceptanceOracle.Owners[0].PredicateIDs
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

// The wall's one named admission is pure: an escalated correction without a
// convergence basis refuses dispatch_worker, and the same state with one
// funded confirming review admits the dispatch so the payload guard can bind
// it to the exact funded repairing revision (CD-0148 amendment). The state
// stays comparable over the added field.
func TestWorkflowAdmitWallAdmitsOnlyTheFundedConfirmingReview(t *testing.T) {
	definition := WorkflowDefinition{Ref: "workflow.break_fix", Version: 1,
		ActionDefinitions: []WorkflowActionDefinition{
			{ID: "dispatch_worker", ExecutionMode: ActionFenced},
			{ID: "record_worker_job", ExecutionMode: ActionHold},
		},
		StepGraph: WorkflowStepGraph{Steps: []WorkflowStep{{ID: "repair", Actions: []string{"dispatch_worker", "record_worker_job"}}}}}
	escalated := WorkflowAdmissionState{Step: "repair", CorrectionEscalated: true, NonProgressAttempts: 3}
	if decision := workflowAdmit(definition, escalated, "dispatch_worker"); decision.Admitted || decision.Failure == nil || decision.Failure.Kind != KindMissingEvidence {
		t.Fatalf("escalated dispatch without a basis = %+v, want the convergence refusal", decision)
	}
	funded := escalated
	funded.FundedChecksRepairReviews = 1
	if decision := workflowAdmit(definition, funded, "dispatch_worker"); !decision.Admitted || decision.Failure != nil {
		t.Fatalf("escalated dispatch with the funded confirming review = %+v, want admission", decision)
	}
	if decision := workflowAdmit(definition, funded, "record_worker_job"); !decision.Admitted {
		t.Fatalf("a hold action beside the funded review = %+v, want admission", decision)
	}
	_ = make(map[WorkflowAdmissionState]struct{})
}

// A released pin without the repair declaration refuses the authoring fields
// before any effect (CD-0115, CD-0205 D6): the repair travels only with the
// definition versions that declare it.
func TestChecksRepairAuthoringRefusedOnReleasedPin(t *testing.T) {
	const workID = "checks-repair-released-pin"
	fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 30, "repair")
	s := fixture.store
	defer s.Close()
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": "job:released-pin", "objective": "bounded objective", "stopping_condition": "the recorded checks pass",
		"checks": []string{"go test ./internal/store/"}, "ready": true, "readiness_evidence": []string{"evidence:released-pin"},
		"repair_revisions": []int64{1}, "repair_reason": "the named revision's checks cannot run",
	}); err == nil || !strings.Contains(err.Error(), "is not declared for action") {
		t.Fatalf("repair authoring on a released pin = %v, want the undeclared-field refusal", err)
	}
}

// The void coverage rule is pure over the walked repair state: a new repair
// naming revisions a voided repair already named is admitted without
// funding, and one naming untouched revisions keeps the funded admission.
func TestChecksRepairVoidCoverageUnit(t *testing.T) {
	history := &workflowCorrectionHistory{
		checksRepairs: map[string]*workflowChecksRepairState{
			workerJobKey("job:covered", 2): {binding: WorkerJobBinding{JobID: "job:covered", Revision: 2, Digest: "sha256:" + strings.Repeat("a", 64)}, namedKeys: []string{workerJobKey("job:covered", 1)}, voided: true},
		},
	}
	if !workerChecksRepairCoveredByVoid(history, []string{workerJobKey("job:covered", 1)}) {
		t.Fatal("a revision a voided repair named is not covered")
	}
	if workerChecksRepairCoveredByVoid(history, []string{workerJobKey("job:other", 1)}) {
		t.Fatal("an untouched revision is covered")
	}
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

// The confirming acceptance opens the productive window only when no other
// recorded revision stays unresolved (CD-0205 D4): an unrelated job's failed
// obligation keeps the correction window and the counted dispatches exactly
// as it kept them before the repair.
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

	// The unrelated job fails and stays unresolved beside the whole flow.
	unrelated := &WorkerJobBinding{JobID: "job:unrelated-debt", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, unrelated)
	unrelatedAttempt := "attempt:" + workID + ":unrelated"
	dispatchJobBoundAttempt(t, s, workID, "repair", unrelatedAttempt, nil, worker, 0, "checks-repair-debt-unrelated", unrelated)
	s2WorkerFailedAttributedLive(t, s, workID, unrelatedAttempt, "the unrelated job's check cannot run")

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
	unrelatedPin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, unrelatedPin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	unrelatedPin = issue1013Pin(t, s, workID)
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, unrelatedPin.Correction, worker, unrelatedPin.Version, "checks-repair-debt-1", job)
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

	reviewAttempt := "attempt:" + workID + ":review-debt"
	if err := dispatchConfirmingReview(t, s, workID, reviewAttempt, repairing, fixture.owner); err != nil {
		t.Fatalf("the funded confirming review dispatch refused: %v", err)
	}
	completeChecksRepairReviewWithVerdict(t, s, workID, reviewAttempt, "ship")
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": reviewAttempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor); err != nil {
		t.Fatalf("accept the confirming review's ship verdict: %v", err)
	}

	// The repair is confirmed, but the unrelated job's obligation stays
	// unresolved: no productive reset, the window keeps its counted
	// dispatches, and the non-progress budget keeps its failures.
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("correction anchor after the confirming acceptance = (%#v, %v), want the open window on the unrelated job's debt", anchor, anchorErr)
	}
	if count := jobWindowCorrectionCount(t, s, workID); count != 4 {
		t.Fatalf("correction count after the confirming acceptance = %d, want the 4 dispatches the preserved window holds", count)
	}
	if nonProgress, err := workflowNonProgressAttemptCountForRegistry(ctx, s.DatabaseForTesting(), workID, "checks-repair", BuiltinWorkflowRegistry()); err != nil || nonProgress != 2 {
		t.Fatalf("non-progress attempts after the confirming acceptance = %d, %v, want the unrelated failure and the repaired job's failure preserved", nonProgress, err)
	}
}

// checksRepairRefineProof seeds one green verify run of the work's primary
// Project acquired now and binds its verification evidence, so a
// delivery-asserting accept at refine passes the CD-0192 refine-exit proof
// and the admission's later gates answer for themselves.
func checksRepairRefineProof(t *testing.T, s *Store, workID string) {
	t.Helper()
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	ref := refineProofSeedVerifyRunForProject(t, s, workID, strings.Repeat("c", 64), []string{"go", "vet", "./..."}, time.Now().UTC().Add(time.Second), views[0].ProjectScope)
	refineProofBindVerification(t, s, workID, ref, ref)
}

// An abandoned confirming review voids the repair exactly as a failed one
// does (CD-0205 D4): the named revision loses its provisional readiness at
// the abandonment terminal, the review counts as non-progress, and the
// voided repair funds no replacement — while the old and new checks stay on
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

	// The funded confirming review dispatches once; its host abandons before
	// any report exists, and the abandonment terminal voids the repair.
	reviewAttempt := "attempt:" + workID + ":review-abandoned"
	if err := dispatchConfirmingReview(t, s, workID, reviewAttempt, repairing, fixture.owner); err != nil {
		t.Fatalf("the funded confirming review dispatch refused: %v", err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkerEvidenceOperationTx(ctx, tx, s2WorkerFailedAttributedEvent(workID, reviewAttempt, "failed-"+reviewAttempt, "", WorkerFailureAbandoned, "the host ended without a report", time.Unix(47, 0).UTC()))
		return err
	}); err != nil {
		t.Fatalf("the abandonment terminal ingest: %v", err)
	}

	// The void is visible before any coordinator move: the named revision's
	// provisional readiness is gone, no funding stands, and the old and new
	// checks stay on record.
	var namedState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, jobID, 1).Scan(&namedState); err != nil {
		t.Fatal(err)
	}
	if namedState != "recorded" {
		t.Fatalf("the named revision's state after the abandonment = %q, want the provisional readiness reverted to recorded", namedState)
	}
	if fundings, fundingErr := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); fundingErr != nil || len(fundings) != 0 {
		t.Fatalf("fundings after the abandonment = (%#v, %v), want none", fundings, fundingErr)
	}
	recorded, recordedErr := readWorkerJobChecksRepairsTx(ctx, s.DatabaseForTesting(), workID)
	if recordedErr != nil {
		t.Fatal(recordedErr)
	}
	if _, onRecord := recorded[*repairing]; !onRecord {
		t.Fatal("the voided repair's recorded metadata left the log")
	}
	// The window the original failure opened stays open on the named
	// revision, and the abandonment counts as non-progress beside it.
	anchor, anchorErr := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, "repair", "")
	if anchorErr != nil || anchor == nil {
		t.Fatalf("correction anchor after the abandonment = (%#v, %v), want the open window", anchor, anchorErr)
	}
	if nonProgress, npErr := workflowNonProgressAttemptCountForRegistry(ctx, s.DatabaseForTesting(), workID, "checks-repair", BuiltinWorkflowRegistry()); npErr != nil || nonProgress != 2 {
		t.Fatalf("non-progress attempts after the abandonment = %d, %v, want the original failure and the abandoned review preserved", nonProgress, npErr)
	}
}

// The delivery-asserting accept runs the one delivery admission the
// standalone record_delivery runs (CD-0205 D3/D5): behind an unconfirmed
// checks repair's review debt it refuses on the same shared gate, on the
// combined route exactly as on the standalone route.
func TestChecksRepairReviewDebtHoldsCombinedAcceptRoute(t *testing.T) {
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
	dispatchJobBoundAttempt(t, s, workID, "refine", attempt1, nil, worker, 0, "checks-repair-combined-1", job)
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
	dispatchJobBoundAttempt(t, s, workID, "refine", attempt2, failedAnchor, worker, 0, "checks-repair-combined-2", repairing)
	// The refine-exit proof stays green: the combined accept gets past the
	// proof gate, so the refusal behind the review debt is the debt gate and
	// nothing ahead of it.
	checksRepairRefineProof(t, s, workID)
	completeChecksRepairAttemptObserved(t, s, workID, attempt2, "checks-repair-completed-"+attempt2)

	// The combined accept asserts delivery in the same event that records
	// the job disposition: behind the unconfirmed repair's review debt it
	// refuses on the shared delivery admission, naming the review debt the
	// standalone route names.
	err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attempt2, "attempt_epoch": latestStepStartEpoch(t, s, workID, "refine"),
		"delivery_artifact": "artifact:checks-repair-combined", "delivery_state": "asserted",
	}), 0, acceptor)
	if err == nil || !strings.Contains(err.Error(), "review debt") {
		t.Fatalf("the combined accept behind the review debt = %v, want the shared delivery admission's review-debt refusal", err)
	}
	// The refusal recorded no disposition: on the combined route the
	// acceptance and the delivery assertion are one event, so nothing
	// opened — the repairing revision stays recorded with nothing consumed,
	// and the plain local accept afterward opens the repair with its funded
	// review untouched.
	var repairingState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, jobID, repairing.Revision).Scan(&repairingState); err != nil {
		t.Fatal(err)
	}
	if repairingState != "recorded" {
		t.Fatalf("the repairing revision's state after the refused combined accept = %q, want recorded with nothing consumed", repairingState)
	}
	if fundings, fundingErr := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); fundingErr != nil || len(fundings) != 0 {
		t.Fatalf("fundings after the refused combined accept = (%#v, %v), want none: the pending disposition opened nothing", fundings, fundingErr)
	}
	acceptChecksRepairAttemptAtStep(t, s, workID, attempt2, "refine", acceptor)
	if fundings, fundingErr := workerChecksRepairReviewFundingsTx(ctx, s.DatabaseForTesting(), workID); fundingErr != nil || len(fundings) != 1 {
		t.Fatalf("fundings after the plain accepting disposition = (%#v, %v), want the one funded confirming review", fundings, fundingErr)
	}
}
