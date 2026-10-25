package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func assertWorkPinIntentsAdmitted(t *testing.T, s *Store, workID string) int {
	t.Helper()
	ctx := context.Background()
	count := 0
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "work_pin")
		if err != nil {
			return err
		}
		pin, err := ReadWorkPinTx(ctx, tx, workID)
		if err != nil {
			return err
		}
		// nil registry keeps the builtin convenience verification, matching
		// the builtin-definition intent sweep below.
		registered, _, _, err := workPinReadInstanceForRegistryTx(ctx, tx, workID, &WorkPin{}, nil)
		if err != nil {
			return err
		}
		state, _, _, err := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, registered.Definition, pin.Step, "work_pin", BuiltinWorkflowRegistry())
		if err != nil {
			return err
		}
		for _, intent := range pin.NextValidIntents {
			decision := workflowAdmit(registered.Definition, state, intent.ActionID)
			if !decision.Admitted {
				return fmt.Errorf("%s step %s advertises unadmitted action %s: %+v", pin.WorkflowType, pin.Step, intent.ActionID, decision)
			}
			if intent.Tool != "concord_work_transition" || intent.Operation != "workflow_action" || intent.ExpectedVersion != pin.Version {
				return fmt.Errorf("intent does not name an action at its pin version: %+v", intent)
			}
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkPinIntentsAreAdmittedAtEveryBuiltinStep(t *testing.T) {
	count := 0
	for _, definition := range BuiltinWorkflowDefinitions() {
		t.Run(definition.Ref, func(t *testing.T) {
			s := openTemp(t)
			const workID = "pin-admission"
			seedWorkWithUrgency(t, s, workID, "standard", 10)
			registered, err := BuiltinWorkflowRegistry().Register(definition)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Transact(context.Background(), func(tx *Transaction) error {
				return InitializeWorkflowTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: WorkflowActor{PrincipalRef: "principal/pin", ClientRef: "client/pin", AgentRef: "agent/pin", SessionRef: "session/pin", ActorClass: ActorAgent}, Now: time.Unix(1, 0).UTC()})
			}); err != nil {
				t.Fatal(err)
			}
			for _, step := range definition.StepGraph.Steps {
				t.Run(step.ID, func(t *testing.T) {
					if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET current_step=? WHERE work_id=?; DELETE FROM write_guard`, step.ID, workID); err != nil {
						t.Fatal(err)
					}
					count += assertWorkPinIntentsAdmitted(t, s, workID)
				})
			}
		})
	}
	if count == 0 {
		t.Fatal("no emitted intent was checked")
	}
}

func TestWorkPinRecoveryIntentsAreAdmittedAtTheirWalls(t *testing.T) {
	t.Run("duplicate_contracts", func(t *testing.T) {
		f := newAcceptanceRecoveryFixture(context.Background(), t)
		duplicateActiveWorkflowContract(context.Background(), t, f.store, f.workID)
		if assertWorkPinIntentsAdmitted(t, f.store, f.workID) == 0 {
			t.Fatal("duplicate recovery offered no intent")
		}
		seedTerminalInstanceForTesting(t, f.store, f.workID, "cancelled")
		if assertWorkPinIntentsAdmitted(t, f.store, f.workID) != 0 {
			t.Fatal("terminal work offered an intent")
		}
	})
}
