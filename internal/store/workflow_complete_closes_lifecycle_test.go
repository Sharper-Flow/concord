package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// CD-0183 D3: the workflow completion's terminal state closes the work item
// lifecycle in the same transaction, and the instance keeps the completed
// record the completion fold wrote.
func TestWorkflowCompleteClosesLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "complete-closes-lifecycle"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})

	var lifecycleBefore string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycleBefore); err != nil {
		t.Fatal(err)
	}
	if lifecycleBefore != "in_progress" {
		t.Fatalf("lifecycle before the completion = %q, want in_progress", lifecycleBefore)
	}

	if err := CompleteWorkflow(ctx, s, completion); err != nil {
		t.Fatalf("workflow completion refused: %v", err)
	}

	var lifecycle, state string
	var instanceCompletedAt *string
	var itemTerminalTime *string
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.instance_state, i.completed_at, w.terminal_time FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &instanceCompletedAt, &itemTerminalTime); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" {
		t.Fatalf("lifecycle after the completion = %q, want completed", lifecycle)
	}
	if state != "completed" {
		t.Fatalf("instance_state after the completion = %q, want completed", state)
	}
	if itemTerminalTime == nil || *itemTerminalTime == "" {
		t.Fatal("the completion left no work-item terminal time")
	}
	if instanceCompletedAt == nil || *instanceCompletedAt != *itemTerminalTime {
		t.Fatalf("instance stamp %v does not match the terminal close %v", instanceCompletedAt, itemTerminalTime)
	}
}

// CD-0183 D3: the completion gate admits only ok verdicts, so the lifecycle it
// closes is completed. A completion that names another terminal state refuses
// before any effect: supersession stays atomic with its relation, and
// cancellation keeps its own lifecycle route.
func TestWorkflowCompleteRefusesNonCompletedTerminalState(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{"superseded", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			workID := "complete-refuses-" + terminal
			s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true, includePremise: true, verdictKind: "ok"})
			var payload map[string]any
			if err := json.Unmarshal(completion.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			payload["terminal_state"] = terminal
			completion.Payload = mustJSONValue(payload)
			var eventsBefore int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}

			err := CompleteWorkflow(ctx, s, completion)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Fatalf("completion with terminal_state %s = %v, want %s", terminal, err, KindInvalidPayload)
			}

			var lifecycle, state string
			var eventsAfter int
			if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.instance_state, (SELECT count(*) FROM domain_events WHERE subject_id=w.id) FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &state, &eventsAfter); err != nil {
				t.Fatal(err)
			}
			if lifecycle != "in_progress" || state == "completed" || eventsAfter != eventsBefore {
				t.Fatalf("refused completion left lifecycle=%s instance=%s events %d->%d; want no effect", lifecycle, state, eventsBefore, eventsAfter)
			}
		})
	}
}
