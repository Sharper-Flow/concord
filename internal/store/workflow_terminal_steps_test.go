package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestReportedCompletionDiscardsStepsAndPreservesDelivery(t *testing.T) {
	const workID = "reported-completion"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, emptyMandate: true,
		includeVerdict: true, includePremise: true, verdictKind: "ok", omitClosure: true,
	})
	if _, err := s.db.Exec(`INSERT INTO write_guard VALUES(1); INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES(?,1,'reported-delivery','principal/operator','2026-11-01T00:00:00Z',1,'{"delivery_artifact":"https://example.com/change/1","delivery_state":"asserted"}'); DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	before, err := workflowDeliveryAssertionRead(t.Context(), s.db, workID)
	if err != nil || before == nil || before.Artifact == "" {
		t.Fatalf("reported delivery = %+v, %v", before, err)
	}
	bindings := retirementUpgradeRows(t, s.db, `SELECT * FROM workflow_evidence_bindings ORDER BY rowid`)
	if err := CompleteWorkflow(t.Context(), s, completion); err != nil {
		t.Fatal(err)
	}
	after, err := workflowDeliveryAssertionRead(t.Context(), s.db, workID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("completion changed reported delivery: before=%+v after=%+v err=%v", before, after, err)
	}
	if afterBindings := retirementUpgradeRows(t, s.db, `SELECT * FROM workflow_evidence_bindings ORDER BY rowid`); !reflect.DeepEqual(bindings, afterBindings) || len(bindings) == 0 {
		t.Fatalf("completion changed ordinary evidence bindings: before=%v after=%v", bindings, afterBindings)
	}
	projection, err := ReadWorkflowProjection(t.Context(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil || !reflect.DeepEqual(before, projection.DeliveryAssertion) {
		t.Fatalf("terminal projection lost reported delivery: %+v, %v", projection.DeliveryAssertion, err)
	}
	for _, table := range []string{"workflow_step_epochs", "workflow_step_completions", "workflow_phase_exits", "workflow_completed_exits", "workflow_checkpoints", "workflow_context_checkpoints", "workflow_context_boundaries", "workflow_artifact_actions", "workflow_coordinator_actions"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE work_id=?", workID).Scan(&count); err != nil || count != 0 {
			t.Errorf("retained %s rows=%d err=%v", table, count, err)
		}
	}
	var state, lifecycle string
	var receipts, producers int
	if err := s.db.QueryRow(`SELECT i.instance_state,w.lifecycle,(SELECT count(*) FROM effect_receipts WHERE subject_id=w.id),(SELECT count(*) FROM durable_operations WHERE work_id=w.id AND result_kind='completed') FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE w.id=?`, workID).Scan(&state, &lifecycle, &receipts, &producers); err != nil || state != "completed" || lifecycle != "completed" || receipts == 0 || producers == 0 {
		t.Fatalf("terminal identity state=%q lifecycle=%q receipts=%d producers=%d err=%v", state, lifecycle, receipts, producers, err)
	}
}

func TestReportedCompleteActionDiscardsStepsAtomically(t *testing.T) {
	const workID = "reported-action"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, emptyMandate: true,
		includeVerdict: true, includePremise: true, verdictKind: "ok", omitClosure: true,
	})
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

func TestLifecycleRepairPreservesCompletedWorkflow(t *testing.T) {
	const workID = "completed-lifecycle-repair"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, emptyMandate: true,
		includeVerdict: true, includePremise: true, verdictKind: "ok", omitClosure: true,
	})
	if err := CompleteWorkflow(t.Context(), s, completion); err != nil {
		t.Fatal(err)
	}
	var completedAt string
	if err := s.db.QueryRow(`SELECT completed_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&completedAt); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkEvent(t, s, workReopenedEvent("reopen-completed-workflow", workID, "completed", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	version++
	if err := applyWorkEvent(t, s, workTransitionEvent("repair-lifecycle", workID, "needed", "completed", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	var lifecycle, state, afterCompletedAt string
	if err := s.db.QueryRow(`SELECT w.lifecycle,i.instance_state,i.completed_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &afterCompletedAt); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" || state != "completed" || afterCompletedAt != completedAt || readWorkVersion(t, s, workID) != version+1 {
		t.Fatalf("lifecycle repair lifecycle=%q state=%q stamp=%q, original stamp=%q", lifecycle, state, afterCompletedAt, completedAt)
	}
}
