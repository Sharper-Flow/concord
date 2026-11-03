package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestWorkflowRegistryBoundary(t *testing.T) {
	t.Parallel()
	t.Run("stored plan determines opaque mutation pin", func(t *testing.T) {
		ctx := context.Background()
		s := openTemp(t)
		const route = "registry-route"
		seedStepWork(t, s, route)
		plan, err := WorkflowStepPlanTemplate(WorkflowStepPlanChange)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Transact(ctx, func(tx *Transaction) error {
			scope := newWriteScope(tx.tx)
			if err := scope.enter(ctx); err != nil {
				return err
			}
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,instance_state,workflow_type,step_plan) VALUES(?,?,?,?)`, route, "running", string(plan.Type), string(raw)); err != nil {
				return err
			}
			return scope.close(ctx)
		}); err != nil {
			t.Fatal(err)
		}
		version := readWorkVersion(t, s, route)
		events := approvalEventCount(t, s, route)
		read := func(reg DefinitionRegistry) WorkPin {
			t.Helper()
			var pin WorkPin
			var available bool
			if err := s.Transact(ctx, func(tx *Transaction) error {
				var err error
				pin, available, err = ReadMutationWorkPinTxForRegistry(ctx, tx, reg, route, nil)
				return err
			}); err != nil || !available {
				t.Fatalf("mutation pin available=%v, err=%v", available, err)
			}
			return pin
		}
		pin := read(nil)
		if pin.WorkflowType != string(plan.Type) || pin.Step != plan.Steps[0].ID || !workPinContainsAction(pin.NextValidIntents, "complete_step") {
			t.Fatalf("plan mutation pin = %+v", pin)
		}
		if other := read(NewWorkflowDefinitionRegistry()); !reflect.DeepEqual(pin, other) {
			t.Fatalf("registry changed plan pin: %+v != %+v", pin, other)
		}
		poolPin, err := ReadWorkPin(ctx, s, route)
		if err != nil || !reflect.DeepEqual(pin, poolPin) {
			t.Fatalf("pool pin = %+v, err = %v, want %+v", poolPin, err, pin)
		}
		if readWorkVersion(t, s, route) != version || approvalEventCount(t, s, route) != events {
			t.Fatal("pin reads moved the work item")
		}
		var guards int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM write_guard`).Scan(&guards); err != nil || guards != 0 {
			t.Fatalf("pin reads left %d write guards: %v", guards, err)
		}
	})
	t.Run("duplicate-active state keeps the pin typed", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		const workID = "registry-duplicate-active"
		s, owner, _ := seedItemAtAcceptance(t, workID)
		defer s.Close()
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		seedDuplicateContractProjection(t, s, workID, ownerRef)
		var activeVersions []int64
		err = s.readSnapshot(ctx, func(q queryer) error {
			var innerErr error
			activeVersions, innerErr = activeWorkflowContractVersions(ctx, q, workID)
			return innerErr
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(activeVersions, []int64{1, 2}) {
			t.Fatalf("active contract versions = %v, want [1 2] with highest 2", activeVersions)
		}
		seededVersion := readWorkVersion(t, s, workID)
		eventsBefore := approvalEventCount(t, s, workID)
		readPin := func() (WorkPin, bool, error) {
			var pin WorkPin
			var available bool
			txErr := s.Transact(ctx, func(tx *Transaction) error {
				var err error
				pin, available, err = ReadMutationWorkPinTxForRegistry(ctx, tx, nil, workID, nil)
				return err
			})
			return pin, available, txErr
		}
		pin, available, err := readPin()
		if err != nil || !available {
			t.Fatalf("duplicate-active mutation pin = available %v, %v, want the readable pin", available, err)
		}
		if len(pin.NextValidIntents) != 0 {
			t.Fatalf("duplicate-active intents = %+v, want no action authority", pin.NextValidIntents)
		}
		poolPin, err := ReadWorkPin(ctx, s, workID)
		if err != nil {
			t.Fatalf("duplicate-active pool pin read refused: %v", err)
		}
		if !reflect.DeepEqual(pin, poolPin) {
			t.Fatalf("pool pin = %+v, want the opaque-Tx pin %+v", poolPin, pin)
		}
		parity, _, err := readPin()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pin, parity) {
			t.Fatalf("second mutation pin = %+v, want %+v", parity, pin)
		}
		if approvalEventCount(t, s, workID) != eventsBefore || readWorkVersion(t, s, workID) != seededVersion {
			t.Fatal("the duplicate-active reads moved the unchanged item")
		}
	})
}
