package store

import (
	"context"
	"testing"
)

func TestWorkflowActionStartCurrentFactsWithoutLog(t *testing.T) {
	f := seedCrossAttemptFixture(t, "action-start-current-facts")
	defer f.s.Close()
	f.dispatch(t, f.attemptR, f.researchLane, f.dispatcher)
	ctx := context.Background()
	seq, epoch, found, err := latestWorkflowActionStart(ctx, f.s.db, f.workID, f.step)
	if err != nil || !found || seq <= 0 || epoch <= 0 {
		t.Fatalf("start fixture: seq=%d epoch=%d found=%v err=%v", seq, epoch, found, err)
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, f.s.db, BuiltinWorkflowRegistry(), f.workID)
	if err != nil {
		t.Fatal(err)
	}
	dispatchStart, err := readWorkflowActionStartTx(ctx, f.s.db, f.workID, f.step, true)
	if err != nil || dispatchStart.Seq != seq {
		t.Fatalf("dispatch start fixture: %+v, %v", dispatchStart, err)
	}
	for _, action := range workflowStep(entry.Definition, f.step).Actions {
		if action == "dispatch_worker" {
			continue
		}
		version := readWorkVersion(t, f.s, f.workID)
		start := workflowEventWithActor("non-dispatch-start", WorkflowActionStarted, f.workID, dispatchStart.Actor, map[string]any{
			"work_id": f.workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": f.step, "action_id": action, "attempt_epoch": epoch + 1,
			"actor_ref": dispatchStart.Actor, "accepted_inputs_digest": testManifestDigest, "idempotency_identity": "non-dispatch-start",
		})
		if err := applyWorkflowTestOperation(ctx, f.s, Operation{Events: []Event{start}}); err != nil {
			t.Fatal(err)
		}
		break
	}
	retained, err := readWorkflowActionStartTx(ctx, f.s.db, f.workID, f.step, true)
	if err != nil || retained.Seq != dispatchStart.Seq || retained.EventID != dispatchStart.EventID || string(retained.Payload) != string(dispatchStart.Payload) {
		t.Fatalf("non-dispatch start erased dispatch pairing: %+v, %v", retained, err)
	}
	assertWorkerFactsLogBlind(t, f.s, map[string]func() (any, error){
		"G22-dispatch-start": func() (any, error) { return readWorkflowActionStartTx(ctx, f.s.db, f.workID, f.step, true) },
		"G22-pass-boundary": func() (any, error) {
			seq, active, err := workflowStepPassBoundary(ctx, f.s.db, entry.Definition, f.workID, f.step, "workflow_action")
			return struct {
				Seq    int64
				Active bool
			}{seq, active}, err
		},
		"G22-latest-start": func() (any, error) {
			seq, epoch, found, err := latestWorkflowActionStart(ctx, f.s.db, f.workID, f.step)
			return struct {
				Seq, Epoch int64
				Found      bool
			}{seq, epoch, found}, err
		},
	})
}

func TestWorkflowHeldReviewDoesNotRecordPhaseExit(t *testing.T) {
	j, _ := descendantReviewFixture(t, "held-review-phase-exit")
	attempt, epoch := completeCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	// The candidate fixture includes an imported acceptance descriptor.
	repopulateWorkflowCurrentFactsForTest(t, j.s)
	var before int64
	if err := j.s.db.QueryRow(`SELECT COALESCE(MAX(exit_seq),0) FROM workflow_phase_exits WHERE work_id=? AND action_id='accept_worker_result'`, j.workID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}, j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	assertNoExit := func() {
		t.Helper()
		var n int64
		if err := j.s.db.QueryRow(`SELECT COALESCE(MAX(exit_seq),0) FROM workflow_phase_exits WHERE work_id=? AND action_id='accept_worker_result'`, j.workID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != before {
			t.Fatalf("held Review changed qualifying phase exit from %d to %d", before, n)
		}
	}
	assertNoExit()
	// A retained held Review need not carry a local job disposition. The
	// migration must classify its recorded hold independently of that binding.
	if _, err := j.s.db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('unbound-held-review','workflow.action_completed','work_item',?,'actor:review','2026-10-01T00:00:00Z',7,'{"action_id":"accept_worker_result","review_advance_held":true}')`, j.workID); err != nil {
		t.Fatal(err)
	}
	repopulateWorkflowCurrentFactsForTest(t, j.s)
	assertNoExit()
}
