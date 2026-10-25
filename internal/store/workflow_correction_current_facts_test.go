package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkflowCorrectionCurrentFactsWithoutLog(t *testing.T) {
	f := seedCrossAttemptFixture(t, "correction-current-facts")
	defer f.s.Close()
	f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
	f.completeReport(t, f.attemptR, f.researchLane, 21)
	if _, err := f.invokeReject(t, readWorkVersion(t, f.s, f.workID), f.attemptR, 1, "current-facts", 22); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var frontier int64
	read := func() (any, error) {
		h, err := workflowCorrectionWalkWorkerSatisfaction(ctx, f.s.db, f.workID, frontier, "workflow_action")
		if err != nil {
			return nil, err
		}
		return h.openRecord(""), nil
	}
	if err := f.s.db.QueryRow(`SELECT MAX(effect_seq) FROM effect_receipts WHERE subject_id=?`, f.workID).Scan(&frontier); err != nil {
		t.Fatal(err)
	}
	h, err := workflowCorrectionWalkWorkerSatisfaction(ctx, f.s.db, f.workID, frontier, "workflow_action")
	if err != nil || h.openRecord("") == nil {
		t.Fatalf("open correction fixture: %+v, %v", h, err)
	}
	want, err := workflowCorrectionContextForDispatch(ctx, f.s.db, f.workID, "investigate", "")
	if err != nil || want == nil {
		t.Fatalf("dispatch correction fixture: %+v, %v", want, err)
	}
	repopulateWorkflowCurrentFactsForTest(t, f.s)
	got, err := workflowCorrectionContextForDispatch(ctx, f.s.db, f.workID, "investigate", "")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("migration must preserve correction identity and directions: got %+v, want %+v, err %v", got, want, err)
	}
	assertWorkerFactsLogBlind(t, f.s, map[string]func() (any, error){
		"G28-open-obligation": read,
		"G28-dispatch-correction": func() (any, error) {
			return workflowCorrectionContextForDispatch(ctx, f.s.db, f.workID, "investigate", "")
		},
	})
}

func TestWorkflowCorrectionDuplicateRepairRecordingPreservesBackfill(t *testing.T) {
	const workID = "correction-duplicate-repair"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	ctx := t.Context()
	reviewSubjectClaim(t, s, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/repair-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	record := func(checks string, revisions []int64) *WorkerJobBinding {
		t.Helper()
		if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, checksRepairRepairFields(checks, revisions)); err != nil {
			t.Fatal(err)
		}
		views, err := readWorkerJobRevisions(ctx, s.db, workID)
		if err != nil || len(views) == 0 {
			t.Fatalf("job revisions: %+v, %v", views, err)
		}
		binding := views[len(views)-1].Binding
		return &binding
	}
	first := record("concord check --original-checks --exit-code=never", nil)
	dispatchJobBoundAttempt(t, s, workID, "repair", "attempt:duplicate-failed", nil, worker, 0, "duplicate-failed", first)
	s2WorkerFailedAttributedLive(t, s, workID, "attempt:duplicate-failed", "the declared checks cannot execute")
	repair := record("go test ./internal/store/ -run TestChecksRepair", []int64{1})
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	dispatchJobBoundAttempt(t, s, workID, "repair", "attempt:duplicate-repair", pin.Correction, worker, pin.Version, "duplicate-repair", repair)
	completeChecksRepairAttemptObserved(t, s, workID, "attempt:duplicate-repair", "duplicate-repair-completed")
	acceptChecksRepairAttempt(t, s, workID, "attempt:duplicate-repair", reviewGateAcceptor(workID))
	if err := dispatchConfirmingReview(t, s, workID, "attempt:duplicate-confirm", repair, fixture.owner); err != nil {
		t.Fatal(err)
	}
	h, err := readWorkflowCorrectionFacts(ctx, s.db, workID, "workflow_action")
	key := workerJobKey(repair.JobID, repair.Revision)
	if err != nil || h.checksRepairs[key].confirmAttempt == "" {
		t.Fatalf("repair confirmation fixture: %+v, %v", h, err)
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT recorded_payload FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, repair.JobID, repair.Revision).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload WorkerJobRecordedPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	resulting := version + 1
	payload.ExpectedVersion, payload.ResultingVersion = &version, &resulting
	event := workflowEventWithActor("duplicate-repair-recording", WorkerJobRecorded, workID, fixture.owner.AgentRef, nil)
	event.Payload = mustJSONValue(payload)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}}); err != nil {
		t.Fatal(err)
	}
	h, err = readWorkflowCorrectionFacts(ctx, s.db, workID, "workflow_action")
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := readWorkflowCorrectionStateForMigration(ctx, s.db, workID, int64(1)<<62, "migrate")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.checksRepairs[key], legacy.checksRepairs[key]) {
		t.Fatalf("duplicate repair recording must retain legacy authority semantics: current %+v, migration %+v", h.checksRepairs[key], legacy.checksRepairs[key])
	}
	repopulateWorkflowCurrentFactsForTest(t, s)
	after, err := readWorkflowCorrectionFacts(ctx, s.db, workID, "workflow_action")
	if err != nil || !reflect.DeepEqual(h.checksRepairs[key], after.checksRepairs[key]) {
		t.Fatalf("duplicate repair recording backfill changed authority: before %+v, after %+v, err %v", h.checksRepairs[key], after, err)
	}
}
