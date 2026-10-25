package store

import (
	"context"
	"encoding/json"
	"strings"
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
		"G22-prewrite-start": func() (any, error) {
			seq, epoch, found, err := latestWorkflowActionStartAt(ctx, f.s.db, f.workID, f.step, int64(1)<<62)
			return struct {
				Seq, Epoch int64
				Found      bool
			}{seq, epoch, found}, err
		},
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

func TestWorkflowDerivedStartPairCurrentFactsWithoutLog(t *testing.T) {
	const work = "derived-pair-current-facts"
	f := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	defer f.store.Close()
	actor := diagnosisS1Actor(work, "checkpointer")
	if err := runVerdictActionAs(t, f.store, work, "checkpoint_repair", json.RawMessage(`{}`), 0, actor); err != nil {
		t.Fatal(err)
	}
	start, err := readWorkflowActionStartTx(t.Context(), f.store.db, work, "repair", false)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := VerifyWorkflowInstanceDefinition(t.Context(), f.store, BuiltinWorkflowRegistry(), work)
	if err != nil {
		t.Fatal(err)
	}
	read := func() (any, error) {
		tx, err := f.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		return workflowDerivedStartPairAuthored(t.Context(), tx, entry.Definition, work, "repair", "checkpoint_repair", 1, start.Seq+1, start.Actor, strings.TrimSuffix(start.EventID, ":started")+":checkpoint")
	}
	want, err := read()
	if err != nil || want != true {
		t.Fatalf("derived pair fixture: %v, %v", want, err)
	}
	assertWorkerFactsLogBlind(t, f.store, map[string]func() (any, error){"G20-derived-start-pair": read})
}

func TestWorkflowHeldReviewDoesNotRecordPhaseExit(t *testing.T) {
	j, _ := descendantReviewFixture(t, "held-review-phase-exit")
	attempt, epoch := completeCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
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
	// The independent prefix includes an unbound held Review beside the
	// current producer's held Review; neither may qualify as a phase exit.
	assertWorkflowCurrentFactsWithIndependentMigration142(t, j.s)
	assertNoExit()
}
