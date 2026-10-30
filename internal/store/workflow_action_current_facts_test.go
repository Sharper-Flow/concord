package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowActionStartCurrentFactsWithoutLog(t *testing.T) {
	const workID = "action-start-current-facts"
	const step = "execution"
	s, _, _ := seedOrdinaryExecutorAtExecution(t, workID)
	defer s.Close()
	ctx := context.Background()
	seq, epoch, found, err := latestWorkflowActionStart(ctx, s.db, workID, step)
	if err != nil || !found || seq <= 0 || epoch <= 0 {
		t.Fatalf("start fixture: seq=%d epoch=%d found=%v err=%v", seq, epoch, found, err)
	}
	entry, err := VerifyWorkflowInstanceDefinitionTx(ctx, s.db, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := readWorkflowActionStartTx(ctx, s.db, workID, step)
	if err != nil || first.Seq != seq {
		t.Fatalf("ordinary start fixture: %+v, %v", first, err)
	}
	version := readWorkVersion(t, s, workID)
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
		"G22-pass-boundary": func() (any, error) {
			seq, active, err := workflowStepPassBoundary(ctx, s.db, entry.Definition, workID, step, "workflow_action")
			return struct {
				Seq    int64
				Active bool
			}{seq, active}, err
		},
		"G22-latest-start": func() (any, error) {
			seq, epoch, found, err := latestWorkflowActionStart(ctx, s.db, workID, step)
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
	start, err := readWorkflowActionStartTx(t.Context(), f.store.db, work, "repair")
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
	j := pReturnReviewFixture(t, "held-review-phase-exit", "workflow.implementation", "")
	var before int64
	if err := j.s.db.QueryRow(`SELECT COALESCE(MAX(exit_seq),0) FROM workflow_phase_exits WHERE work_id=? AND action_id='record_review'`, j.workID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	completeCurrentReviewForTest(t, j.fixture, j.repo, "correction_required")
	assertNoExit := func() {
		t.Helper()
		var n int64
		if err := j.s.db.QueryRow(`SELECT COALESCE(MAX(exit_seq),0) FROM workflow_phase_exits WHERE work_id=? AND action_id='record_review'`, j.workID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != before {
			t.Fatalf("held Review changed qualifying phase exit from %d to %d", before, n)
		}
	}
	assertNoExit()
	assertWorkflowCurrentFactsWithIndependentMigration142(t, j.s)
	assertNoExit()
}
