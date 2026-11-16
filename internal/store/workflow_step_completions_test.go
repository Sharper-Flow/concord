package store

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestWorkflowStepCompletionFrontier(t *testing.T) {
	s := openTemp(t)
	const work = "step-completions"
	actor, version := seedTestWorkflow(t, s, work)
	seedWorkflowLaw(t, s)
	version = issue31WorkflowActionWithPayload(t, s, work, version, "record_plan", "frontier-plan", actor, json.RawMessage(testWorkflowPlanDocument))
	// approve first: CD-0203 D1 forbids closing the contract step without a contract
	version = issue31WorkflowActionWithPayload(t, s, work, version, "approve_contract", "frontier-approve", actor, json.RawMessage(contractStepApprovalPayload))
	if got := workflowPlanFixtureFrontier(t, s, work); got != "plan" {
		t.Fatalf("record_plan advanced frontier to %s", got)
	}
	var previous int64
	var earlier []WorkflowPlanAttempt
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("completion-%d", i)
		version = issue31WorkflowActionWithPayload(t, s, work, version, "complete_step", id, actor, json.RawMessage(fmt.Sprintf(`{"step_id":"plan","note":"attempt %d"}`, i)))
		plan, err := readWorkflowPlanTx(t.Context(), s.db, work)
		if err != nil || plan == nil {
			t.Fatalf("read plan: %v", err)
		}
		step, ok := stepByID(plan.Steps, "plan")
		if !ok || len(step.Attempts) != i {
			t.Fatalf("attempts=%+v, want %d", step.Attempts, i)
		}
		for j := range earlier {
			if step.Attempts[j] != earlier[j] {
				t.Fatalf("attempt %d changed: got %+v want %+v", j, step.Attempts[j], earlier[j])
			}
		}
		var receipt int64
		if err := s.db.QueryRow(`SELECT effect_seq FROM effect_receipts WHERE event_id=?`, id+":semantic").Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		current := step.Attempts[i-1].CompletionSeq
		if current != receipt || current <= previous {
			t.Fatalf("completion frontier=%d receipt=%d previous=%d", current, receipt, previous)
		}
		previous = current
		earlier = append([]WorkflowPlanAttempt(nil), step.Attempts...)
		if got := workflowPlanFixtureFrontier(t, s, work); got != "implement" {
			t.Fatalf("frontier after attempt %d = %s, want implement", i, got)
		}
	}
	assertEventLogAbsent(t, s)
}
