package store

import (
	"fmt"
	"testing"
	"time"
)

// These reader fixtures call the current fact writer directly. The matching
// log inputs establish the old reader's answer without requiring a full job
// journey; completion-fold and migration parity are separate writer tests.
func admissionExitFixture(t *testing.T, s *Store, ref string, p workflowActionCompletedPayload) int64 {
	t.Helper()
	ctx := t.Context()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var next int64
	if err := tx.QueryRow(`SELECT last+1 FROM store_sequences WHERE name='effect'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: fmt.Sprintf("exit-pin-%d", next), Kind: WorkflowDefinitionSelected, SubjectType: SubjectWorkItem, SubjectID: "admission-exits", Actor: frontierActor, OccurredAt: time.Unix(next, 0).UTC(), PayloadVersion: 1}
	if ref != "" {
		event.Payload = mustJSONValue(map[string]any{"ref": ref, "version": 1})
		if _, err := appendPreparedEvent(ctx, tx, event); err != nil {
			t.Fatal(err)
		}
	}
	event.EventID, event.Kind, event.Payload = fmt.Sprintf("exit-%d", next), WorkflowActionCompleted, mustJSONValue(p)
	seq, err := appendPreparedEvent(ctx, tx, event)
	if err != nil {
		t.Fatal(err)
	}
	event.Seq = seq
	if err := writeWorkflowCompletedExitTx(ctx, tx, event, p, ref); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return event.Seq
}

func TestWorkflowAdmissionCompletedExitsLogBlind(t *testing.T) {
	s := openTemp(t)
	seedStepWork(t, s, "admission-exits")
	p := workflowActionCompletedPayload{StepID: "findings", ActionID: "record_report"}
	admissionExitFixture(t, s, "workflow.research", p)
	latest := admissionExitFixture(t, s, "workflow.research", p)
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_completed_exits WHERE work_id='admission-exits'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("repeated completion class count = %d, %v; want 1", count, err)
	}
	otherFamily := admissionExitFixture(t, s, "workflow.implementation", p)
	p.ActionID = "accept_worker_result"
	otherAction := admissionExitFixture(t, s, "workflow.research", p)
	p.StepID = "investigate"
	otherStep := admissionExitFixture(t, s, "workflow.research", p)
	frontierWriteOnlyLog(t, s)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	type completedExit struct {
		seq                             int64
		stepID, actionID, definitionRef string
	}
	rows, err := tx.QueryContext(t.Context(), `SELECT exit_seq,step_id,action_id,definition_ref FROM workflow_completed_exits WHERE work_id='admission-exits' ORDER BY exit_seq DESC`)
	if err != nil {
		t.Fatalf("completed exits without the log: %v", err)
	}
	defer rows.Close()
	var exits []completedExit
	for rows.Next() {
		var exit completedExit
		if err := rows.Scan(&exit.seq, &exit.stepID, &exit.actionID, &exit.definitionRef); err != nil {
			t.Fatal(err)
		}
		exits = append(exits, exit)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []completedExit{
		{seq: otherStep, stepID: "investigate", actionID: "accept_worker_result", definitionRef: "workflow.research"},
		{seq: otherAction, stepID: "findings", actionID: "accept_worker_result", definitionRef: "workflow.research"},
		{seq: otherFamily, stepID: "findings", actionID: "record_report", definitionRef: "workflow.implementation"},
		{seq: latest, stepID: "findings", actionID: "record_report", definitionRef: "workflow.research"},
	}
	if len(exits) != len(want) {
		t.Fatalf("completed exits = %+v; want %d stable classes", exits, len(want))
	}
	for i, exit := range exits {
		if exit != want[i] {
			t.Errorf("exit %d = %+v; want %+v", i, exit, want[i])
		}
	}
	if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM workflow_completed_exits WHERE work_id='other-work'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("other work exit count = %d, %v; want none", count, err)
	}
}
