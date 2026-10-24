package store

import (
	"fmt"
	"reflect"
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
	repopulateWorkflowCurrentFactsForTest(t, s)
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("backfill changed open obligations: got %v, want %v", got, want)
	}
	if _, err := s.db.Exec(`ALTER TABLE domain_events RENAME TO domain_events_hidden`); err != nil {
		t.Fatal(err)
	}
	assertCounts(1, 1)
}

func TestWorkflowActionObligationBackfillPreservesUnknownKeys(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const work = "obligation-unknown-keys"
	continuityTestWorkflow(t, s, work)
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
		if _, err := s.db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item',?,'actor:retained','2026-10-01T00:00:00Z',1,?)`, f.id, f.kind, work, f.payload); err != nil {
			t.Fatal(err)
		}
	}
	repopulateWorkflowCurrentFactsForTest(t, s)
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
}
