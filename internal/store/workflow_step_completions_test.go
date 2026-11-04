package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowStepCompletionFrontier(t *testing.T) {
	s := openTemp(t)
	const work = "step-completions"
	actor, version := continuityTestWorkflow(t, s, work)
	version = issue31WorkflowActionWithPayload(t, s, work, version, "record_plan", "frontier-plan", actor, json.RawMessage(testWorkflowPlanDocument))
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

func TestWorkflowCompletionBackfillPreservesUnknownKeys(t *testing.T) {
	base := openTemp(t)
	const work = "completion-unknown-keys"
	continuityTestWorkflow(t, base, work)
	c := historicalFactCorpus{parents: func(t *testing.T, target *Store, guard string) {
		copyFixtureDataToPrefix(t, base, target.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts"}, map[string]map[string]string{"workflow_instances": {"outside_repair_state": "NULL", "definition_version": "26", "definition_ref": "'workflow.implementation'", "definition_digest": "'sha256:" + strings.Repeat("0", 64) + "'", "current_step": "'plan'", "result_disposition": "''", "latest_non_dispatch_start_seq": "0", "unhealthy_verdict_seq": "0"}, "workflow_contracts": {"definition_version": "26", "definition_ref": "'workflow.implementation'", "definition_digest": "'sha256:" + strings.Repeat("0", 64) + "'", "law_change": "NULL"}}, guard)
	}}
	for _, f := range []struct{ id, payload string }{
		{"missing-epoch-completion", `{"step_id":"unknown","action_id":"checkpoint_context"}`},
		{"missing-step-completion", `{"attempt_epoch":7,"action_id":"checkpoint_context"}`},
		{"orphan-completion", `{"step_id":"orphan","attempt_epoch":3,"action_id":"checkpoint_context"}`},
	} {
		c.events = append(c.events, corpusEvent(f.id, WorkflowActionCompleted, work, json.RawMessage(f.payload)))
	}
	s := c.prefix(t, 141)
	advanceHistoricalPrefix(t, s.db, 142)
	var got int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_step_completions WHERE step_id IN ('orphan','unknown')`).Scan(&got); err != nil || got != 2 {
		t.Fatalf("completion frontier rows=%d err=%v, want 2", got, err)
	}
	before := retirementUpgradeSnapshot(t, s.db)
	err := Migrate(outcomeSnapshotContext(t, t.Context(), s.db), s.db)
	assertFailureKind(t, err, KindInvariantViolation)
	for _, ref := range []string{work, "missing-step-completion", "sequence"} {
		if !strings.Contains(err.Error(), ref) {
			t.Fatalf("missing completion-class refusal %v does not identify %q", err, ref)
		}
	}
	if after := retirementUpgradeSnapshot(t, s.db); !reflect.DeepEqual(after, before) {
		t.Fatal("failed migration changed predecessor")
	}
	var version, rows int
	if err := s.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_step_completions WHERE step_id IN ('orphan','unknown')`).Scan(&rows); err != nil || version != 142 || rows != got {
		t.Fatalf("failed migration changed predecessor: version=%d rows=%d err=%v", version, rows, err)
	}
}
