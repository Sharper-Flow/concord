package store

// The matched packet/report schema revision becomes 2.0 and drops
// the retired domain_cards member. The released 1.0/1.0 (no-job) and 1.1/1.1
// (job-capable) pairs stay admitted as recorded history: the matched-pair
// fault admits all three released pairs, job presence binds the recorded
// job-capable version instead of today's current, the live dispatch and
// completion folds refuse every retired pair, and log-ordered replay folds
// recorded 1.0/1.1 history unchanged.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestWorkerSchemaVersionFaultAdmitsReleasedRevisionPairs(t *testing.T) {
	t.Parallel()
	const current = "2.0"
	if WorkerPacketSchemaVersion != current {
		t.Fatalf("current worker packet schema = %s, want the card-free %s revision", WorkerPacketSchemaVersion, current)
	}
	if WorkerReportSchemaVersion != current {
		t.Fatalf("current worker report schema = %s, want the matched %s revision", WorkerReportSchemaVersion, current)
	}
	cases := []struct {
		packet, report string
		jobBound       bool
		wantFault      bool
	}{
		{"1.0", "1.0", false, false},
		{"1.1", "1.1", false, false},
		{"2.0", "2.0", false, false},
		{"1.1", "1.1", true, false},
		{"2.0", "2.0", true, false},
		{"1.0", "1.0", true, true},
		{"1.0", "1.1", false, true},
		{"2.0", "1.1", false, true},
		{"1.1", "2.0", false, true},
		{"3.0", "3.0", false, true},
		{"2.0", "1.0", true, true},
	}
	for _, tc := range cases {
		fault := workerSchemaVersionFault(tc.packet, tc.report, tc.jobBound)
		if (fault != "") != tc.wantFault {
			t.Fatalf("workerSchemaVersionFault(%q, %q, jobBound=%v) = %q, wantFault=%v", tc.packet, tc.report, tc.jobBound, fault, tc.wantFault)
		}
		if tc.wantFault && !strings.Contains(fault, tc.packet) && !strings.Contains(fault, "matched pair") && !strings.Contains(fault, "job-capable") {
			t.Fatalf("fault %q does not name the refused pair %s/%s", fault, tc.packet, tc.report)
		}
	}
}

func TestWorkerCompletedPayloadBindsJobToRecordedJobCapableVersion(t *testing.T) {
	t.Parallel()
	base := WorkerCompletedPayload{AttemptID: "attempt-schema-revision", ReadbackModel: "openai/test-model", EvidenceOrigin: WorkerEvidenceLegacyUnavailable}
	cases := []struct {
		report      string
		jobBound    bool
		wantRefusal bool
	}{
		{report: "1.0", jobBound: false},
		{report: "1.1", jobBound: false},
		{report: "2.0", jobBound: false},
		{report: "1.1", jobBound: true},
		{report: "2.0", jobBound: true},
		{report: "1.0", jobBound: true, wantRefusal: true},
		{report: "3.0", jobBound: false, wantRefusal: true},
	}
	for _, tc := range cases {
		payload := base
		payload.ReportSchemaVersion = tc.report
		if tc.jobBound {
			payload.WorkerJob = &WorkerJobBinding{JobID: "job:revision", Revision: 1, Digest: "sha256:" + strings.Repeat("1", 64)}
		}
		err := validateWorkerCompletedPayload(Event{}, payload)
		if (err != nil) != tc.wantRefusal {
			t.Fatalf("validateWorkerCompletedPayload(report %q, jobBound %v) = %v, wantRefusal %v", tc.report, tc.jobBound, err, tc.wantRefusal)
		}
	}
}

// The live dispatch boundary refuses every retired packet/report pair: an old
// host that records the released card-bearing identities cannot land new
// dispatch evidence, while the same recorded pair folds unchanged on
// log-ordered replay.
func TestWorkerDispatchedRefusesRetiredSchemaPairLive(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	retired := workerDispatchEvent("worker-revision-retired", "dispatch-revision-retired", lane, map[string]any{
		"packet_schema_version": "1.1", "report_schema_version": "1.1",
	})
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{retired}})
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("retired 1.1 dispatch = %v, want %s", err, KindInvalidPayload)
	}
	retiredLegacy := workerDispatchEvent("worker-revision-retired-legacy", "dispatch-revision-retired-legacy", lane, map[string]any{
		"packet_schema_version": "1.0", "report_schema_version": "1.0",
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{retiredLegacy}}); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("retired 1.0 dispatch = %v, want %s", err, KindInvalidPayload)
	}
	if got := countRows(t, s, "worker_attempts"); got != 0 {
		t.Fatalf("retired dispatch rows = %d, want 0", got)
	}
	// The same recorded pair folds unchanged under the replay context, so
	// recorded 1.0/1.1 history keeps rebuilding after the cutover. The seed
	// transaction commits before any pooled-connection read: while it is
	// open, it holds the store's one connection.
	db := s.DatabaseForTesting()
	replayTx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyOperationTx(workflowReplayContext(context.Background()), replayTx, Operation{Events: []Event{retired}}, newFoldScope(replayTx), false); err != nil {
		t.Fatalf("recorded 1.1 history replay refused: %v", err)
	}
	if err := replayTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var pair [2]string
	if err := s.DatabaseForTesting().QueryRow(`SELECT packet_schema_version,report_schema_version FROM worker_attempts WHERE attempt_id=?`, "dispatch-revision-retired").Scan(&pair[0], &pair[1]); err != nil {
		t.Fatal(err)
	}
	if pair != [2]string{"1.1", "1.1"} {
		t.Fatalf("recorded attempt pair = %v, want 1.1/1.1 preserved", pair)
	}
	before := workerProjectionSnapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("rebuild changed the worker projection with recorded retired-pair history:\n%s\nwant\n%s", after, before)
	}
}

// Completion must match the schema identity recorded by its own attempt.
// A retired report cannot complete a current attempt; retained attempts
// keep their recorded identity when their original results arrive.
func TestWorkerCompletedRefusesRetiredReportVersionLive(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	ctx := context.Background()
	// The attempt row only needs one admitted dispatch; the current pair
	// admits through the live boundary both before and after the cutover.
	authorizeWorkerFixtureDispatch(t, s, "worker-completion-revision", "dispatch-completion-revision")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workerDispatchEvent("worker-completion-revision", "dispatch-completion-revision", lane, nil)}}); err != nil {
		t.Fatal(err)
	}
	// The completion fold owns the report identity through the attempt row:
	// a retired 1.1 claim against an attempt recorded at the current pair
	// refuses closed, so no global current-revision gate is needed here.
	retired := workerCompleteEvent("worker-completion-revision", "complete-completion-revision", "dispatch-completion-revision", preferredModelForLane(lane), workerFixtureDirectory(t, s, "worker-completion-revision"))
	retiredPayload := mustJSONValue(map[string]any{"attempt_id": "dispatch-completion-revision", "readback_model": preferredModelForLane(lane), "report_schema_version": "1.1", "evidence_origin": WorkerEvidenceLegacyUnavailable})
	retired.Payload = retiredPayload
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{retired}}); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("retired 1.1 completion = %v, want %s", err, KindInvalidPayload)
	}
	current := workerCompleteEvent("worker-completion-revision", "complete-completion-revision-current", "dispatch-completion-revision", preferredModelForLane(lane), workerFixtureDirectory(t, s, "worker-completion-revision"))
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{current}}); err != nil {
		t.Fatalf("current completion refused: %v", err)
	}
}

// Job capability binds the recorded job-capable version, not today's
// current: a recorded pre-cutover dispatch whose evidence carried the
// released 1.1 pair and the authorized job binding folds and rebuilds
// unchanged, while the live producer dispatches the same authorized job
// under the current 2.0 pair through the real authorization route.
func TestWorkerJobBindingBindsRecordedJobCapableVersion(t *testing.T) {
	const workID = "worker-job-revision-history"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	workerVersion := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("revision-worker-"+workID, WorkflowActorRecorded, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": workerVersion, "resulting_version": workerVersion + 1,
		"actor_ref": workerRef, "principal_ref": worker.PrincipalRef, "client_ref": worker.ClientRef,
		"agent_ref": worker.AgentRef, "session_ref": worker.SessionRef, "actor_class": "agent",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): workerVersion}}); err != nil {
		t.Fatal(err)
	}
	job := &WorkerJobBinding{JobID: "job:revision-history", Revision: 1}
	// Authorize the historic attempt through the real dispatch_worker route
	// (the record+authorize half of the job-bound harness), so its evidence
	// can bind the same recorded revision.
	historicAttempt := "attempt:" + workID + ":1"
	recordWorkerJobRevisionForTest(t, s, workID, worker, job)
	historicPacket := dispatchWorkerPacket(t, s, workID, "repair", historicAttempt)
	historicPacket["schema_version"] = WorkerPacketSchemaVersion
	historicPacket["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, *job, "implement")
	if err := dispatchJobPacketForTest(t, s, workID, historicAttempt, worker, "job-revision-historic", historicPacket); err != nil {
		t.Fatalf("authorize the historic attempt: %v", err)
	}
	// The recorded pre-cutover shape: the authorized job binding on an
	// evidence event that carried the released 1.1 pair folds under the
	// replay context and keeps the pair on the attempt row across rebuild.
	// It resolves the historic authorization's launch before the live
	// dispatch, which admits one unresolved authorization at a time.
	var jobDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.worker_job.digest') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, historicAttempt).Scan(&jobDigest); err != nil {
		t.Fatal(err)
	}
	lane := reviewGateLane(t, "implementation")
	historic := Event{EventID: "dispatch-revision-history-1", Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(15, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerDispatched), Payload: mustJSONValue(map[string]any{
		"attempt_id": historicAttempt, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "capability_class": lane.CapabilityClass,
		"readback_model":        "openai/test-model",
		"packet_schema_version": "1.1", "report_schema_version": "1.1",
		"packet_digest": "sha256:" + strings.Repeat("d", 64),
		"worker_job":    map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": jobDigest},
	})}
	db := s.DatabaseForTesting()
	replayTx, txErr := db.BeginTx(context.Background(), nil)
	if txErr != nil {
		t.Fatal(txErr)
	}
	defer replayTx.Rollback()
	if _, err := applyOperationTx(workflowReplayContext(context.Background()), replayTx, Operation{Events: []Event{historic}}, newFoldScope(replayTx), false); err != nil {
		t.Fatalf("recorded 1.1 job-bound dispatch replay refused: %v", err)
	}
	if err := replayTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var historicRowPacket, historicRowReport string
	if err := db.QueryRow(`SELECT packet_schema_version,report_schema_version FROM worker_attempts WHERE attempt_id=?`, historicAttempt).Scan(&historicRowPacket, &historicRowReport); err != nil {
		t.Fatal(err)
	}
	if historicRowPacket != "1.1" || historicRowReport != "1.1" {
		t.Fatalf("recorded job-bound attempt pair = %s/%s, want 1.1/1.1", historicRowPacket, historicRowReport)
	}
	// The live producer path: the real dispatch_worker authorization at the
	// current pair, then the evidence event the current host records.
	attempt2 := "attempt:" + workID + ":2"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt2, nil, worker, issue1013Pin(t, s, workID).Version, "job-revision-live", job)
	var livePacket, liveReport string
	if err := s.DatabaseForTesting().QueryRow(`SELECT packet_schema_version,report_schema_version FROM worker_attempts WHERE attempt_id=?`, attempt2).Scan(&livePacket, &liveReport); err != nil {
		t.Fatal(err)
	}
	if livePacket != WorkerPacketSchemaVersion || liveReport != WorkerReportSchemaVersion {
		t.Fatalf("live job-bound dispatch pair = %s/%s, want the current pair %s/%s", livePacket, liveReport, WorkerPacketSchemaVersion, WorkerReportSchemaVersion)
	}
	before := workerProjectionSnapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := workerProjectionSnapshot(t, s); after != before {
		t.Fatalf("rebuild changed the worker projection with recorded job-bound history:\n%s\nwant\n%s", after, before)
	}
}

// The completion fold refuses a completion whose report identity disagrees
// with the matched pair the attempt recorded, live and on replay, so no
// mixed-era report reaches the projection.
func TestWorkerCompletedFoldRefusesCrossPairReportVersionOnReplay(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	ctx := context.Background()
	seed := []Event{
		workerDispatchEvent("worker-cross-pair", "dispatch-cross-pair", lane, map[string]any{
			"packet_schema_version": "1.1", "report_schema_version": "1.1",
		}),
		Event{EventID: "complete-cross-pair", Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: "worker-cross-pair", Actor: "worker:test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(map[string]any{"attempt_id": "dispatch-cross-pair", "readback_model": preferredModelForLane(lane), "report_schema_version": "1.0", "evidence_origin": WorkerEvidenceLegacyUnavailable})},
	}
	db := s.DatabaseForTesting()
	replayTx, txErr := db.BeginTx(ctx, nil)
	if txErr != nil {
		t.Fatal(txErr)
	}
	defer replayTx.Rollback()
	_, err := applyOperationTx(workflowReplayContext(ctx), replayTx, Operation{Events: seed}, newFoldScope(replayTx), false)
	if !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("cross-pair recorded completion = %v, want %s", err, KindInvalidPayload)
	}
}
