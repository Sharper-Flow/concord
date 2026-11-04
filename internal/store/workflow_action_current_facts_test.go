package store

import (
	"context"
	"testing"
)

func TestWorkflowActionStartCurrentFactsWithoutLog(t *testing.T) {
	const workID = "action-start-current-facts"
	const step = "implement"
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	seedWork(t, s, workID)
	owner := stepFixtureActor()
	actor, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Actor: owner}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, WorkflowStepPlanChange, step)
	version := readWorkVersion(t, s, workID)
	initial := workflowEventWithActor("ordinary-start", WorkflowActionStarted, workID, actor, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "step_id": step, "action_id": "start_execution", "attempt_epoch": 1, "actor_ref": actor, "accepted_inputs_digest": testManifestDigest, "idempotency_identity": "ordinary-start"})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{initial}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	seq, epoch, found, err := latestWorkflowActionStart(ctx, s.db, workID, step)
	if err != nil || !found || seq <= 0 || epoch <= 0 {
		t.Fatalf("start fixture: seq=%d epoch=%d found=%v err=%v", seq, epoch, found, err)
	}
	first, err := readWorkflowActionStartTx(ctx, s.db, workID, step)
	if err != nil || first.Seq != seq {
		t.Fatalf("ordinary start fixture: %+v, %v", first, err)
	}
	version = readWorkVersion(t, s, workID)
	start := workflowEventWithActor("newer-ordinary-start", WorkflowActionStarted, workID, first.Actor, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"step_id": step, "action_id": "start_execution", "attempt_epoch": epoch + 1,
		"actor_ref": first.Actor, "accepted_inputs_digest": testManifestDigest, "idempotency_identity": "newer-ordinary-start",
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{start}}); err != nil {
		t.Fatal(err)
	}
	retained, err := readWorkflowActionStartTx(ctx, s.db, workID, step)
	if err != nil || retained.Seq <= seq || retained.EventID != start.EventID || retained.Actor != first.Actor || string(retained.Payload) != string(start.Payload) {
		t.Fatalf("newest start lost exact actor/payload/identity: %+v, %v", retained, err)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"G22-prewrite-start": func() (any, error) {
			seq, epoch, found, err := latestWorkflowActionStartAt(ctx, s.db, workID, step, int64(1)<<62)
			return struct {
				Seq, Epoch int64
				Found      bool
			}{seq, epoch, found}, err
		},
		"G22-fenced-start": func() (any, error) { return readWorkflowActionStartTx(ctx, s.db, workID, step) },
		"G22-latest-start": func() (any, error) {
			seq, epoch, found, err := latestWorkflowActionStart(ctx, s.db, workID, step)
			return struct {
				Seq, Epoch int64
				Found      bool
			}{seq, epoch, found}, err
		},
	})
}

func TestWorkflowHeldReviewDoesNotRecordPhaseExit(t *testing.T) {
	f := seedWorkflowReturnRouteFixtureWithPlan(t, "held-review-phase-exit", WorkflowStepPlanChange, "review", []string{}, []string{})
	var before int64
	if err := f.store.db.QueryRow(`SELECT COALESCE(MAX(exit_seq),0) FROM workflow_phase_exits WHERE work_id=? AND action_id='record_review'`, f.workID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, f.store, f.workID, "record_review", mustJSONValue(map[string]any{"step_id": "review", "note": "review corrected the change", "reviewer": "concord-review", "result": "fixed", "commit": "commit:fixture", "link": "https://example.com/review"}), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	assertNoExit := func() {
		t.Helper()
		var n int64
		if err := f.store.db.QueryRow(`SELECT COALESCE(MAX(exit_seq),0) FROM workflow_phase_exits WHERE work_id=? AND action_id='record_review'`, f.workID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != before {
			t.Fatalf("held Review changed qualifying phase exit from %d to %d", before, n)
		}
	}
	assertNoExit()
	assertWorkflowCurrentFactsWithIndependentMigration142(t, f.store)
	assertNoExit()
}
