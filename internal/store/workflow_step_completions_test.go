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
	actor, _ := continuityTestWorkflow(t, s, work)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	var previous int64
	for _, epoch := range []int64{2, 3, 77} {
		version := readWorkVersion(t, s, work)
		fields := map[string]any{"work_id": work, "expected_version": version, "resulting_version": version + 1, "step_id": "proposal", "attempt_epoch": epoch, "actor_ref": actorRef, "action_id": "checkpoint_context"}
		id := fmt.Sprintf("completion-%d", epoch)
		if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{workflowEventWithActor(id, WorkflowActionCompleted, work, actorRef, fields)}}); err != nil {
			t.Fatal(err)
		}
		var current, receipt int64
		if err := s.db.QueryRow(`SELECT completion_seq FROM workflow_step_completions WHERE work_id=? AND step_id='proposal'`, work).Scan(&current); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow(`SELECT effect_seq FROM effect_receipts WHERE event_id=?`, id).Scan(&receipt); err != nil {
			t.Fatal(err)
		}
		if current != receipt || current <= previous {
			t.Fatalf("completion frontier=%d receipt=%d previous=%d", current, receipt, previous)
		}
		previous = current
	}
	queries := []string{`SELECT json_array(step_id,completion_seq) FROM workflow_step_completions ORDER BY work_id,step_id`}
	want := workerFactsQueryRows(t, s, queries)
	assertWorkflowCurrentFactsWithIndependentMigration142(t, s)
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill changed completion frontier: got %v, want %v", got, want)
	}
	assertEventLogAbsent(t, s)
}

func TestWorkflowCompletionBackfillPreservesUnknownKeys(t *testing.T) {
	base := openTemp(t)
	const work = "completion-unknown-keys"
	continuityTestWorkflow(t, base, work)
	c := historicalFactCorpus{parents: func(t *testing.T, target *Store, guard string) {
		copyFixtureDataToPrefix(t, base, target.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts"}, map[string]map[string]string{"workflow_instances": {"definition_version": "26", "result_disposition": "''", "latest_non_dispatch_start_seq": "0"}, "workflow_contracts": {"definition_version": "26"}}, guard)
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
	err := Migrate(t.Context(), s.db)
	assertFailureKind(t, err, KindInvariantViolation)
	for _, ref := range []string{work, "missing-step-completion", "sequence"} {
		if !strings.Contains(err.Error(), ref) {
			t.Fatalf("missing completion-class refusal %v does not identify %q", err, ref)
		}
	}
}
