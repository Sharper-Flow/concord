package store

import (
	"context"
	"strings"
	"testing"
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
// dispatch evidence.
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
	directory := workerFixtureDirectory(t, s, "worker-completion-revision")
	retired := workerCompleteEventV2("worker-completion-revision", "complete-completion-revision", "dispatch-completion-revision", preferredModelForLane(lane), WorkerEvidenceReported, laneCoveringEvidence(lane), directory)
	retired.Payload = mustJSONValue(WorkerCompletedPayload{AttemptID: "dispatch-completion-revision", ReadbackModel: preferredModelForLane(lane), ReportSchemaVersion: "1.1", WorkerDirectory: directory, EvidenceOrigin: WorkerEvidenceReported, Evidence: laneCoveringEvidence(lane)})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{retired}}); !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "report_schema_version does not match the schema identity the attempt was dispatched with") {
		t.Fatalf("retired 1.1 completion = %v, want %s", err, KindInvalidPayload)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE attempt_id=?`, "dispatch-completion-revision").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "dispatched" {
		t.Fatalf("refused retired report changed the attempt state to %s", state)
	}
	current := workerCompleteEventV2("worker-completion-revision", "complete-completion-revision-current", "dispatch-completion-revision", preferredModelForLane(lane), WorkerEvidenceReported, laneCoveringEvidence(lane), directory)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{current}}); err != nil {
		t.Fatalf("current completion refused: %v", err)
	}
}

// A live job-bound dispatch records the current packet/report pair.
func TestWorkerJobBindingRecordsCurrentSchemaPair(t *testing.T) {
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
	recordWorkerJobRevisionForTest(t, s, workID, worker, job)
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, worker, issue1013Pin(t, s, workID).Version, "job-revision-live", job)
	var livePacket, liveReport string
	if err := s.DatabaseForTesting().QueryRow(`SELECT packet_schema_version,report_schema_version FROM worker_attempts WHERE attempt_id=?`, attempt).Scan(&livePacket, &liveReport); err != nil {
		t.Fatal(err)
	}
	if livePacket != WorkerPacketSchemaVersion || liveReport != WorkerReportSchemaVersion {
		t.Fatalf("live job-bound dispatch pair = %s/%s, want the current pair %s/%s", livePacket, liveReport, WorkerPacketSchemaVersion, WorkerReportSchemaVersion)
	}
}
