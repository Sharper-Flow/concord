package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowActionObligationsKeepExactOpenEpochs(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const work = "action-obligations"
	actor, _ := continuityTestWorkflow(t, s, work)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT json_array(step_id,attempt_epoch,state,actor_ref,effect_seq,payload) FROM workflow_action_obligations ORDER BY work_id,step_id,attempt_epoch,state,actor_ref`,
		`SELECT json_array(step_id,completion_seq) FROM workflow_step_completions ORDER BY work_id,step_id`,
	}
	assertCounts := func(started, failed int) {
		t.Helper()
		for state, want := range map[string]int{"started": started, "failed": failed} {
			var got int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_action_obligations WHERE work_id=? AND state=?`, work, state).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("%s obligations = %d, want %d", state, got, want)
			}
		}
	}
	apply := func(kind string, epoch int64) {
		t.Helper()
		version := readWorkVersion(t, s, work)
		id := fmt.Sprintf("obligation-%s-%d-%d", kind, epoch, version)
		fields := map[string]any{"work_id": work, "expected_version": version, "resulting_version": version + 1, "step_id": "proposal", "attempt_epoch": epoch, "actor_ref": actorRef}
		switch kind {
		case WorkflowActionStarted:
			fields["action_id"], fields["accepted_inputs_digest"], fields["idempotency_identity"] = "record_proposal", testManifestDigest, id
		case WorkflowActionFailed:
			fields["failure_kind"], fields["recoverable"] = "timeout", false
		case WorkflowActionCompleted:
			fields["action_id"] = "checkpoint_context"
		}
		if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{workflowEventWithActor(id, kind, work, actorRef, fields)}}); err != nil {
			t.Fatal(err)
		}
	}
	assertCounts(1, 0)
	apply(WorkflowActionStarted, 2)
	assertCounts(2, 0)
	apply(WorkflowActionFailed, 2)
	assertCounts(1, 1)
	apply(WorkflowActionCompleted, 2)
	assertCounts(1, 0)
	apply(WorkflowActionFailed, 2)
	assertCounts(1, 1)
	apply(WorkflowActionStarted, 3)
	apply(WorkflowActionFailed, 3)
	assertCounts(1, 2)
	apply(WorkflowActionCompleted, 3)
	assertCounts(1, 1)
	// A non-advancing completion without its own start still records the
	// step's completion frontier, without consuming another epoch's debt.
	apply(WorkflowActionCompleted, 77)
	assertCounts(1, 1)
	want := workerFactsQueryRows(t, s, queries)
	assertWorkflowCurrentFactsWithIndependentMigration142(t, s)
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill changed open obligations: got %v, want %v", got, want)
	}
	assertEventLogAbsent(t, s)
	assertCounts(1, 1)
}

func TestWorkflowActionObligationBackfillPreservesUnknownKeys(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const work = "obligation-unknown-keys"
	actor, _ := continuityTestWorkflow(t, s, work)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	base := s
	c := historicalFactCorpus{events: []Event{corpusEvent("obligation-initial-start", WorkflowActionStarted, work, map[string]any{"step_id": "proposal", "attempt_epoch": 1, "actor_ref": actorRef})}, parents: func(t *testing.T, target *Store, guard string) {
		copyFixtureDataToPrefix(t, base, target.db, []string{"products", "projects", "product_projects", "work_items", "work_projects", "workflow_actors", "workflow_instances", "workflow_contracts"}, map[string]map[string]string{"workflow_instances": {"definition_version": "26"}, "workflow_contracts": {"definition_version": "26"}}, guard)
	}}
	fixtures := []struct{ id, kind, payload string }{
		{"missing-epoch-start", WorkflowActionStarted, `{"step_id":"unknown"}`},
		{"missing-step-start", WorkflowActionStarted, `{"attempt_epoch":7}`},
		{"missing-actor-start", WorkflowActionStarted, `{"step_id":"other","attempt_epoch":9}`},
		{"missing-epoch-failure", WorkflowActionFailed, `{"step_id":"unknown","failure_kind":"timeout","recoverable":false}`},
		{"missing-epoch-failure-again", WorkflowActionFailed, `{"step_id":"unknown","failure_kind":"unavailable","recoverable":false}`},
		{"missing-epoch-completion", WorkflowActionCompleted, `{"step_id":"unknown","action_id":"checkpoint_context"}`},
		{"missing-step-completion", WorkflowActionCompleted, `{"attempt_epoch":7,"action_id":"checkpoint_context"}`},
		{"orphan-completion", WorkflowActionCompleted, `{"step_id":"orphan","attempt_epoch":3,"action_id":"checkpoint_context"}`},
	}
	for _, f := range fixtures {
		c.events = append(c.events, corpusEvent(f.id, f.kind, work, json.RawMessage(f.payload)))
	}
	s = c.prefix(t, 141)
	advanceHistoricalPrefix(t, s.db, 142)
	for query, want := range map[string]int{
		`SELECT count(*) FROM workflow_action_obligations WHERE state='started'`:                          4,
		`SELECT count(*) FROM workflow_action_obligations WHERE state='failed'`:                           2,
		`SELECT count(*) FROM workflow_action_obligations WHERE actor_ref=''`:                             5,
		`SELECT count(*) FROM workflow_action_obligations WHERE step_id IS NULL OR attempt_epoch IS NULL`: 4,
		`SELECT count(*) FROM workflow_step_completions WHERE step_id IN ('orphan','unknown')`:            2,
	} {
		var got int
		if err := s.db.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", query, got, want)
		}
	}
	// These nullable-key rows are observable at 142. The later admission
	// migration intentionally refuses the completion with no step class.
	err = Migrate(t.Context(), s.db)
	assertFailureKind(t, err, KindInvariantViolation)
	for _, ref := range []string{work, "missing-step-completion", "sequence"} {
		if !strings.Contains(err.Error(), ref) {
			t.Fatalf("missing completion-class refusal %v does not identify %q", err, ref)
		}
	}
}
