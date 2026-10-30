package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestWorkflowFailedActionSettlementFrontier(t *testing.T) {
	for _, recoverable := range []bool{false, true} {
		t.Run(fmt.Sprintf("recoverable=%t", recoverable), func(t *testing.T) {
			s := openTemp(t)
			const work = "failed-settlement"
			actor, version := continuityTestWorkflow(t, s, work)
			ref, err := WorkflowActorRef(actor)
			if err != nil {
				t.Fatal(err)
			}
			start, err := readWorkflowActionStartTx(t.Context(), s.db, work, "proposal")
			if err != nil {
				t.Fatal(err)
			}
			outputs := []string{
				`SELECT json_array(work_id,definition_ref,step_id,action_id,exit_seq) FROM workflow_completed_exits ORDER BY work_id,action_id`,
				`SELECT json_array(work_id,action_id,exit_seq) FROM workflow_phase_exits ORDER BY work_id,action_id`,
				`SELECT json_array(work_id,binding_seq,payload) FROM workflow_evidence_bindings ORDER BY work_id,binding_seq`,
			}
			before := workerFactsQueryRows(t, s, outputs)
			event := workflowEventWithActor("failed-settlement", WorkflowActionFailed, work, ref, map[string]any{
				"work_id": work, "expected_version": version, "resulting_version": version + 1,
				"step_id": "proposal", "attempt_epoch": 1, "actor_ref": ref,
				"failure_kind": "timeout", "recoverable": recoverable,
			})
			op := Operation{Events: []Event{event}, ExpectedVersions: workVersion(work, version)}
			if err := applyWorkflowTestOperation(t.Context(), s, op); err != nil {
				t.Fatal(err)
			}
			stopped, err := workExecutionStoppedTx(t.Context(), s.db, work, actor.SessionRef)
			if err != nil || !stopped {
				t.Fatalf("failed ordinary attempt remains outstanding: stopped=%t err=%v", stopped, err)
			}
			var settlement, receipt int64
			if err := s.db.QueryRow(`SELECT completion_seq FROM workflow_step_completions WHERE work_id=? AND step_id='proposal'`, work).Scan(&settlement); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT effect_seq FROM effect_receipts WHERE event_id=? AND kind=?`, event.EventID, WorkflowActionFailed).Scan(&receipt); err != nil {
				t.Fatal(err)
			}
			if settlement != receipt || settlement <= start.Seq {
				t.Fatalf("settlement=%d failure receipt=%d start=%d", settlement, receipt, start.Seq)
			}
			afterStart, err := readWorkflowActionStartTx(t.Context(), s.db, work, "proposal")
			if err != nil || !reflect.DeepEqual(afterStart, start) {
				t.Fatalf("failure changed immutable start: got %+v err=%v want %+v", afterStart, err, start)
			}
			if got := workerFactsQueryRows(t, s, outputs); !reflect.DeepEqual(got, before) || countWorkflowActionCompleted(t, s, work) != 0 {
				t.Fatal("failure manufactured successful action outputs")
			}
			snapshot := outsideRepairDatabaseSnapshot(t, s)
			assertFailureKind(t, applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}}), KindDuplicateEvent)
			if outsideRepairDatabaseSnapshot(t, s) != snapshot {
				t.Fatal("exact failed-operation replay changed the database")
			}
			var fields map[string]any
			if err := json.Unmarshal(event.Payload, &fields); err != nil {
				t.Fatal(err)
			}
			fields["failure_kind"] = "unavailable"
			event.Payload, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			assertFailureKind(t, applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}}), KindIdempotencyConflict)
			if outsideRepairDatabaseSnapshot(t, s) != snapshot {
				t.Fatal("changed failure replay changed the database")
			}
		})
	}
}

func TestWorkflowFailedActionCannotSettleLaterAttempt(t *testing.T) {
	s := openTemp(t)
	const work = "failed-settlement-retry"
	actor, _ := continuityTestWorkflow(t, s, work)
	outsideRepairSettleAction(t, s, work, actor, WorkflowActionFailed)
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, work)
	start := workflowEventWithActor("settlement-retry", WorkflowActionStarted, work, ref, map[string]any{
		"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"step_id": "proposal", "action_id": "record_proposal", "attempt_epoch": 2,
		"accepted_inputs_digest": "sha256:settlement-retry", "idempotency_identity": "settlement-retry", "actor_ref": ref,
	})
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{start}, ExpectedVersions: workVersion(work, version)}); err != nil {
		t.Fatal(err)
	}
	stopped, err := workExecutionStoppedTx(t.Context(), s.db, work, "")
	if err != nil || stopped {
		t.Fatalf("earlier failure settled a later start: stopped=%t err=%v", stopped, err)
	}
	version++
	event := workflowEventWithActor("settlement-stale-failure", WorkflowActionFailed, work, ref, map[string]any{
		"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"step_id": "proposal", "attempt_epoch": 1, "actor_ref": ref, "failure_kind": "timeout", "recoverable": false,
	})
	before := outsideRepairDatabaseSnapshot(t, s)
	err = applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(work, version)})
	assertFailureKind(t, err, KindIllegalLifecycleTransition)
	if outsideRepairDatabaseSnapshot(t, s) != before {
		t.Fatal("refused stale failure changed the database")
	}
}

func TestWorkflowFailedActionLeavesUnrelatedStartOutstanding(t *testing.T) {
	s := openTemp(t)
	const work = "failed-settlement-unrelated"
	actor, _ := continuityTestWorkflow(t, s, work)
	// A prior step can still have an unmatched start in current state. Failure
	// of the current step must not settle it, even when its sequence is older.
	if _, err := s.db.Exec(`INSERT INTO write_guard VALUES(1);
		INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload)
		SELECT work_id,'execution',attempt_epoch,latest_start_seq,latest_start_event_id||':unrelated',json_set(latest_start_payload,'$.step_id','execution')
		FROM workflow_step_epochs WHERE work_id=? AND step_id='proposal'; DELETE FROM write_guard`, work); err != nil {
		t.Fatal(err)
	}
	outsideRepairSettleAction(t, s, work, actor, WorkflowActionFailed)
	for _, session := range []string{"", actor.SessionRef} {
		stopped, err := workExecutionStoppedTx(t.Context(), s.db, work, session)
		if err != nil || stopped {
			t.Fatalf("failure settled unrelated start for session %q: stopped=%t err=%v", session, stopped, err)
		}
	}
}
