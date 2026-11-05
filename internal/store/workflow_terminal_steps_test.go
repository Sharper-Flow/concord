package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestReportedCompletionDiscardsStepsAndPreservesDelivery(t *testing.T) {
	const workID = "reported-completion"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{
		emptyMandate: true, includePremise: true, omitClosure: true,
	})
	if _, err := s.db.Exec(`INSERT INTO write_guard VALUES(1); INSERT OR REPLACE INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES(?,1,'reported-delivery','principal/operator','2026-11-01T00:00:00Z',1,'{"delivery_artifact":"https://example.com/change/1","delivery_state":"asserted"}'); DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	before := retirementUpgradeRows(t, s.db, `SELECT * FROM workflow_delivery_state`)
	if len(before) != 1 {
		t.Fatalf("reported delivery = %+v", before)
	}
	bindings := retirementUpgradeRows(t, s.db, `SELECT * FROM workflow_evidence_bindings ORDER BY rowid`)
	actor := workflowActorForRef(t, s, completion.Actor)
	initialVersion := readWorkVersion(t, s, workID)
	completeReviewedPlanForTerminalTest(t, s, workID, actor)
	completedPlan, err := readWorkflowPlanTx(t.Context(), s.db, workID)
	if err != nil || completedPlan == nil {
		t.Fatalf("read completed plan: %v", err)
	}
	completion = shiftWorkflowTestEventVersions(completion, readWorkVersion(t, s, workID)-initialVersion)
	if err := CompleteWorkflow(t.Context(), s, completion); err != nil {
		t.Fatal(err)
	}
	after := retirementUpgradeRows(t, s.db, `SELECT * FROM workflow_delivery_state`)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("completion changed reported delivery: before=%+v after=%+v", before, after)
	}
	if afterBindings := retirementUpgradeRows(t, s.db, `SELECT * FROM workflow_evidence_bindings ORDER BY rowid`); !reflect.DeepEqual(bindings, afterBindings) || len(bindings) == 0 {
		t.Fatalf("completion changed ordinary evidence bindings: before=%v after=%v", bindings, afterBindings)
	}
	plan, err := readWorkflowPlanTx(t.Context(), s.db, workID)
	if err != nil || plan == nil || len(WorkflowStepPlanFrontier(*plan)) != 0 || !reflect.DeepEqual(plan, completedPlan) {
		t.Fatalf("completed plan = %+v, %v", plan, err)
	}
	for _, table := range []string{"workflow_step_epochs", "workflow_phase_exits", "workflow_completed_exits", "workflow_checkpoints", "workflow_coordinator_actions"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE work_id=?", workID).Scan(&count); err != nil || count != 0 {
			t.Errorf("retained %s rows=%d err=%v", table, count, err)
		}
	}
	if rows := retirementUpgradeRows(t, s.db, `SELECT name FROM sqlite_schema WHERE type='table' AND name='workflow_artifact_actions'`); len(rows) != 0 {
		t.Errorf("retired workflow_artifact_actions table remains: %v", rows)
	}
	var state, lifecycle string
	var receipts, producers int
	if err := s.db.QueryRow(`SELECT i.instance_state,w.lifecycle,(SELECT count(*) FROM effect_receipts WHERE subject_id=w.id),(SELECT count(*) FROM durable_operations WHERE work_id=w.id AND result_kind='completed') FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE w.id=?`, workID).Scan(&state, &lifecycle, &receipts, &producers); err != nil || state != "completed" || lifecycle != "completed" || receipts == 0 || producers == 0 {
		t.Fatalf("terminal identity state=%q lifecycle=%q receipts=%d producers=%d err=%v", state, lifecycle, receipts, producers, err)
	}
}

func completeReviewedPlanForTerminalTest(t *testing.T, s *Store, workID string, actor WorkflowActor) {
	t.Helper()
	for {
		plan, err := readWorkflowPlanTx(t.Context(), s.db, workID)
		if err != nil || plan == nil {
			t.Fatalf("read plan: %v", err)
		}
		frontier := WorkflowStepPlanFrontier(*plan)
		if len(frontier) == 0 {
			break
		}
		step := frontier[0]
		payload := map[string]any{"step_id": step.ID, "note": "completed for delivery"}
		action := "complete_step"
		if step.Reviewed {
			action = "record_review"
			payload["reviewer"], payload["result"], payload["commit"], payload["link"] = "concord-review", "pass", "commit:fixture", "https://example.com/review"
		}
		if err := runVerdictActionAs(t, s, workID, action, mustJSONValue(payload), 0, actor); err != nil {
			t.Fatalf("complete step %s: %v", step.ID, err)
		}
	}
}

func TestReportedCompleteActionDiscardsStepsAtomically(t *testing.T) {
	const workID = "reported-action"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{
		emptyMandate: true, includePremise: true, omitClosure: true,
	})
	completeReviewedPlanForTerminalTest(t, s, workID, workflowActorForRef(t, s, completion.Actor))
	if err := runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)); err != nil {
		t.Fatal(err)
	}
	var steps, operations int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_step_epochs WHERE work_id=?1),(SELECT count(*) FROM durable_operations WHERE work_id=?1 AND result_kind='completed' AND completed_at IS NOT NULL)`, workID).Scan(&steps, &operations); err != nil || steps != 0 || operations == 0 {
		t.Fatalf("completion steps=%d operations=%d err=%v", steps, operations, err)
	}
}

func TestLifecycleCompletionWithoutWorkflowStillAllowed(t *testing.T) {
	for _, from := range []string{"needed", "in_progress"} {
		t.Run(from, func(t *testing.T) {
			const workID = "legacy-lifecycle-completion"
			s := openTemp(t)
			seedWork(t, s, workID)
			version := readWorkVersion(t, s, workID)
			if from == "in_progress" {
				if err := applyWorkEvent(t, s, workTransitionEvent("legacy-start", workID, "needed", from, version, version+1), workVersion(workID, version)); err != nil {
					t.Fatal(err)
				}
				version++
			}
			if err := applyWorkEvent(t, s, workTransitionEvent("legacy-complete", workID, from, "completed", version, version+1), workVersion(workID, version)); err != nil {
				t.Fatal(err)
			}
			var lifecycle string
			if err := s.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil || lifecycle != "completed" || readWorkVersion(t, s, workID) != version+1 {
				t.Fatalf("legacy completion lifecycle=%q err=%v", lifecycle, err)
			}
		})
	}
}

func TestTerminalLifecycleDiscardsStepExecution(t *testing.T) {
	for _, target := range []string{"completed", "cancelled"} {
		t.Run(target, func(t *testing.T) {
			const workID = "terminal-lifecycle"
			s := openTemp(t)
			seedWork(t, s, workID)
			if _, err := s.db.Exec(`INSERT INTO write_guard VALUES(1); INSERT INTO workflow_step_epochs(work_id,step_id,attempt_epoch) VALUES(?,'execute',1); DELETE FROM write_guard`, workID); err != nil {
				t.Fatal(err)
			}
			version := readWorkVersion(t, s, workID)
			if err := applyWorkEvent(t, s, workTransitionEvent("terminal-step", workID, "needed", target, version, version+1), workVersion(workID, version)); err != nil {
				t.Fatal(err)
			}
			var steps int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_step_epochs WHERE work_id=?`, workID).Scan(&steps); err != nil || steps != 0 {
				t.Fatalf("terminal step rows=%d err=%v", steps, err)
			}
		})
	}
}

func TestReopenedWorkflowRequiresWorkflowCompletion(t *testing.T) {
	const workID = "completed-lifecycle-repair"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{
		emptyMandate: true, includePremise: true, omitClosure: true,
	})
	initialVersion := readWorkVersion(t, s, workID)
	completeReviewedPlanForTerminalTest(t, s, workID, workflowActorForRef(t, s, completion.Actor))
	completion = shiftWorkflowTestEventVersions(completion, readWorkVersion(t, s, workID)-initialVersion)
	if err := CompleteWorkflow(t.Context(), s, completion); err != nil {
		t.Fatal(err)
	}
	completedPlan, err := readWorkflowPlanTx(t.Context(), s.db, workID)
	if err != nil || completedPlan == nil {
		t.Fatalf("completed plan = %+v, err=%v", completedPlan, err)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkEvent(t, s, workReopenedEvent("reopen-completed-workflow", workID, "completed", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	version++
	var lifecycle, state string
	var completedAt, completionPayload, completionOccurredAt sql.NullString
	if err := s.db.QueryRow(`SELECT w.lifecycle,i.instance_state,i.completed_at,i.completion_payload,i.completion_occurred_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &completedAt, &completionPayload, &completionOccurredAt); err != nil {
		t.Fatal(err)
	}
	plan, err := readWorkflowPlanTx(t.Context(), s.db, workID)
	if lifecycle != "needed" || state != "running" || completedAt.Valid || completionPayload.Valid || completionOccurredAt.Valid || err != nil || !reflect.DeepEqual(plan, completedPlan) {
		t.Fatalf("reopened lifecycle=%q state=%q completion metadata=%+v/%+v/%+v plan=%+v err=%v", lifecycle, state, completedAt, completionPayload, completionOccurredAt, plan, err)
	}
	workBefore := retirementUpgradeRows(t, s.db, `SELECT * FROM work_items`)
	instanceBefore := retirementUpgradeRows(t, s.db, `SELECT * FROM workflow_instances`)
	receiptsBefore := retirementUpgradeRows(t, s.db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	err = applyWorkEvent(t, s, workTransitionEvent("repair-lifecycle", workID, "needed", "completed", version, version+1), workVersion(workID, version))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindNotTerminal {
		t.Fatalf("direct completion error = %v, want not_terminal", err)
	}
	if readWorkVersion(t, s, workID) != version || !reflect.DeepEqual(workBefore, retirementUpgradeRows(t, s.db, `SELECT * FROM work_items`)) || !reflect.DeepEqual(instanceBefore, retirementUpgradeRows(t, s.db, `SELECT * FROM workflow_instances`)) || !reflect.DeepEqual(receiptsBefore, retirementUpgradeRows(t, s.db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)) {
		t.Fatal("refused direct completion changed work, workflow, version, or receipts")
	}
	// The reopened work continues as an inserted step on the retained plan.
	last := completedPlan.Steps[len(completedPlan.Steps)-1].ID
	if err := runVerdictAction(t, s, workID, "insert_step", json.RawMessage(`{"id":"follow_up","after":["`+last+`"],"reason":"reopened work"}`)); err != nil {
		t.Fatalf("insert step after reopen: %v", err)
	}
	completeReviewedPlanForTerminalTest(t, s, workID, workflowActorForRef(t, s, completion.Actor))
	if err := runVerdictAction(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`)); err != nil {
		t.Fatalf("workflow completion: %v", err)
	}
	if err := s.db.QueryRow(`SELECT w.lifecycle,i.instance_state,i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &completedAt); err != nil || lifecycle != "completed" || state != "completed" || !completedAt.Valid {
		t.Fatalf("workflow completion lifecycle=%q state=%q completed_at=%+v err=%v", lifecycle, state, completedAt, err)
	}
}
