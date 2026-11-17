package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestWorkflowTypeSwitchFrontierTypedActions(t *testing.T) {
	for _, action := range []string{"record_removal_scope", "complete_step"} {
		t.Run(action, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			const work = "switch-frontier"
			seedWork(t, s, work)
			seedWorkflowLaw(t, s)
			owner := stepFixtureActor()
			if err := s.Transact(t.Context(), func(tx *Transaction) error {
				return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: work, Actor: owner, Now: time.Unix(1, 0).UTC()})
			}); err != nil {
				t.Fatal(err)
			}
			seedWorkflowPlanFrontier(t, s, work, WorkflowStepPlanChange, "clarify")
			seq := 0
			act := func(id string, fields map[string]any) error {
				seq++
				_, err := applyTestAction(t, s, work, readWorkVersion(t, s, work), id, fmt.Sprintf("switch-op-%d", seq), fields, owner)
				return err
			}
			for _, step := range []struct {
				id     string
				fields map[string]any
			}{
				{"record_clarify", map[string]any{"resolved_questions": []string{}, "note": "No questions remain."}},
				{"complete_step", map[string]any{"step_id": "clarify", "note": "Clarification complete."}},
				{"pick_type", map[string]any{"type": "removal", "reason": "The work is a removal."}},
			} {
				if err := act(step.id, step.fields); err != nil {
					t.Fatalf("%s: %v", step.id, err)
				}
			}
			before, err := readWorkflowPlanTx(t.Context(), s.db, work)
			if err != nil {
				t.Fatal(err)
			}
			pin, err := ReadWorkPin(t.Context(), s, work)
			if err != nil {
				t.Fatal(err)
			}
			advertised := false
			for _, intent := range pin.NextValidIntents {
				if intent.ActionID == action {
					advertised = true
				}
			}
			if !advertised {
				t.Fatalf("pin does not advertise %s", action)
			}
			if err := act("complete_step", map[string]any{"step_id": "implement", "note": "Not yet."}); err == nil {
				t.Fatal("non-frontier step accepted")
			}
			var oldDocument map[string]any
			if err := json.Unmarshal([]byte(testWorkflowPlanDocument), &oldDocument); err != nil {
				t.Fatal(err)
			}
			if err := act("record_plan", oldDocument); err == nil {
				t.Fatal("retained step admitted a document not declared by the current type")
			}
			if action == "record_removal_scope" {
				err = act(action, map[string]any{"removed": []string{"file:one"}, "retiring_clauses": []string{}, "note": "Retire this."})
			} else {
				err = act(action, map[string]any{"step_id": "plan", "note": "Retained step complete."})
			}
			if err != nil {
				t.Fatalf("%s on switched frontier: %v", action, err)
			}
			if action == "record_removal_scope" {
				var count int
				if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM workflow_removal_scopes WHERE work_id=?`, work).Scan(&count); err != nil || count != 1 {
					t.Fatalf("removal scope count=%d err=%v", count, err)
				}
			}
			after, err := readWorkflowPlanTx(t.Context(), s.db, work)
			if err != nil {
				t.Fatal(err)
			}
			for _, prior := range before.Steps {
				got, ok := stepByID(after.Steps, prior.ID)
				if !ok || !reflect.DeepEqual(got.After, prior.After) {
					t.Fatalf("lost step or edges: %s", prior.ID)
				}
				if prior.ID != "plan" && !reflect.DeepEqual(got.Attempts, prior.Attempts) {
					t.Fatalf("changed prior attempts: %s", prior.ID)
				}
				if prior.ID == "plan" && action == "complete_step" && len(got.Attempts) != len(prior.Attempts)+1 {
					t.Fatal("missing retained step attempt")
				}
			}
		})
	}
}
