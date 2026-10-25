package store

import (
	"database/sql"
	"fmt"
	"strings"
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

func admissionExitPinFixture(t *testing.T, s *Store, ref, step string) {
	t.Helper()
	entry, err := BuiltinWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := pinWorkflowInstanceToStepTx(t.Context(), tx, "admission-exits", entry, step, ""); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
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
	exits, err := readWorkflowExits(t.Context(), tx, "admission-exits")
	if err != nil {
		t.Fatalf("completed exits without the log: %v", err)
	}
	want := []workflowExitRecord{
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
	if exits, err := readWorkflowExits(t.Context(), tx, "other-work"); err != nil || len(exits) != 0 {
		t.Fatalf("other work exits = %+v, %v; want none", exits, err)
	}
}

func TestWorkflowAdmissionCarriedExitLogBlind(t *testing.T) {
	for _, tc := range []struct {
		name, ref, step, action, verdict string
		want                             bool
	}{
		{"own-family", "workflow.research", "findings", "record_report", "conclude", true},
		{"wrong-family", "workflow.implementation", "findings", "record_report", "conclude", false},
		{"unknown-family", "workflow.unknown", "findings", "record_report", "conclude", false},
		{"no-prior-pin", "", "findings", "record_report", "conclude", false},
		{"nonadvancing-action", "workflow.research", "findings", "checkpoint_context", "conclude", false},
		{"not-direct-predecessor", "workflow.research", "investigate", "record_finding", "conclude", false},
		{"wrong-verdict-step", "workflow.research", "findings", "record_report", "acceptance", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			seedStepWork(t, s, "admission-exits")
			admissionExitFixture(t, s, tc.ref, workflowActionCompletedPayload{StepID: tc.step, ActionID: tc.action})
			// The current instance family is deliberately not the exit's family.
			admissionExitPinFixture(t, s, "workflow.implementation", "acceptance")
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			exits, err := readWorkflowExits(t.Context(), tx, "admission-exits")
			if err != nil || len(exits) != 1 {
				t.Fatalf("fixture exits = %+v, %v; want one", exits, err)
			}
			got := carriedExitSatisfiedItsPinnedDefinition(tc.verdict, exits)
			if got != tc.want {
				t.Fatalf("carried exit before hiding log = %t; want %t", got, tc.want)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			frontierWriteOnlyLog(t, s)
			tx, err = s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			exits, err = readWorkflowExits(t.Context(), tx, "admission-exits")
			if err != nil || len(exits) != 1 {
				t.Fatalf("carried exit facts without log = %+v, %v; want one", exits, err)
			}
			got = carriedExitSatisfiedItsPinnedDefinition(tc.verdict, exits)
			if got != tc.want {
				t.Fatalf("carried exit without log = %t; want %t", got, tc.want)
			}
		})
	}
}

func TestWorkflowAdmissionHeldAndLocalAcceptLogBlind(t *testing.T) {
	for _, held := range []bool{true, false} {
		t.Run(fmt.Sprintf("review-held-%t", held), func(t *testing.T) {
			s := openTemp(t)
			seedStepWork(t, s, "admission-exits")
			p := workflowActionCompletedPayload{StepID: "findings", ActionID: "accept_worker_result", ReviewAdvanceHeld: held}
			if !held {
				p.WorkerJob = &WorkerJobBinding{JobID: "local-job", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64)}
			}
			admissionExitFixture(t, s, "workflow.research", p)
			admissionExitPinFixture(t, s, "workflow.research", "conclude")
			var phaseExits int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_phase_exits WHERE work_id='admission-exits'`).Scan(&phaseExits); err != nil || phaseExits != 0 {
				t.Fatalf("phase exits = %d, %v; fixture must have none", phaseExits, err)
			}
			frontierWriteOnlyLog(t, s)
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := requireOperatorVerdictExit(t.Context(), tx, "admission-exits"); err != nil {
				t.Fatalf("held/local completed acceptance must count without a phase exit or log: %v", err)
			}
		})
	}
}

func TestWorkflowAdmissionDefinitionFamilyLogBlind(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		started      bool
	}{
		{"no-start-change-family", "workflow.research", false},
		{"started-change-family", "workflow.research", true},
		{"started-same-family", "workflow.implementation", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			const work = "admission-exits"
			seedStepWork(t, s, work)
			initial, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
			if err != nil {
				t.Fatal(err)
			}
			initializeStepWorkflow(t, s, work, initial.Definition)
			if tc.started {
				startStepFixtureAction(t, s, work, "record_proposal")
			}
			var epochs int
			var executionStarted sql.NullString
			if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_step_epochs WHERE work_id=?),execution_started_at FROM workflow_instances WHERE work_id=?`, work, work).Scan(&epochs, &executionStarted); err != nil {
				t.Fatal(err)
			}
			if (epochs != 0) != tc.started || executionStarted.Valid {
				t.Fatalf("proposal start facts = %d epochs, external execution %v; want started=%t without external execution", epochs, executionStarted, tc.started)
			}
			var version int64
			if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, work).Scan(&version); err != nil {
				t.Fatal(err)
			}
			target, err := BuiltinWorkflowDefinitionForRef(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			frontierWriteOnlyLog(t, s)
			err = s.Transact(t.Context(), func(tx *Transaction) error {
				return RepinWorkflowTx(t.Context(), tx, WorkflowRepinRequest{WorkID: work, EventID: "admission-repin", Definition: target, Actor: stepFixtureActor(), Now: time.Unix(11, 0).UTC()})
			})
			wantRef, wantStep := tc.target, "proposal"
			if tc.started && tc.target != initial.Definition.Ref {
				assertFailureKind(t, err, KindInvalidOperation)
				if !strings.Contains(err.Error(), "definition cannot change after execution starts") {
					t.Fatalf("family change refusal = %v; want execution-start refusal", err)
				}
				wantRef = initial.Definition.Ref
			} else {
				if err != nil {
					t.Fatalf("permitted family selection without log: %v", err)
				}
				version++
				if tc.target == "workflow.research" {
					wantStep = "frame"
				}
			}
			var ref, step string
			var gotVersion int64
			if err := s.db.QueryRow(`SELECT i.definition_ref,i.current_step,w.version FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE i.work_id=?`, work).Scan(&ref, &step, &gotVersion); err != nil {
				t.Fatal(err)
			}
			if ref != wantRef || step != wantStep || gotVersion != version {
				t.Fatalf("repin state = %s/%s v%d; want %s/%s v%d", ref, step, gotVersion, wantRef, wantStep, version)
			}
		})
	}
}
