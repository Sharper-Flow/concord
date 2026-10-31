package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorkflowRegistryBoundary(t *testing.T) {
	t.Parallel()
	t.Run("caller registry reaches initialization, repin, selection, and the opaque work pin", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		definition := approvalRouteDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		higher := cloneWorkflowDefinition(definition)
		higher.StalenessRules = append(higher.StalenessRules, WorkflowStalenessRule{ID: "staleness.repin", InputRef: "input:repin", Severity: "warning"})
		foldGuardRows := func(t *testing.T, s *Store) int {
			t.Helper()
			var rows int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM write_guard`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			return rows
		}
		ownerFor := func(workID string) WorkflowActor {
			return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
		}
		initialize := func(t *testing.T, s *Store, workID string, entry RegisteredDefinition, reg DefinitionRegistry) error {
			t.Helper()
			verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
			if err != nil {
				return err
			}
			return s.Transact(verified, func(tx *Transaction) error {
				return InitializeWorkflowTx(verified, tx, WorkflowInitializationRequest{WorkID: workID, Definition: entry, Actor: ownerFor(workID), Now: time.Unix(5, 0).UTC(), Registry: reg})
			})
		}
		const probe = "registry-probe"
		s := openTemp(t)
		seedWork(t, s, probe)
		seedWorkflowLaw(t, s)
		probeVersion := readWorkVersion(t, s, probe)
		probeEvents := approvalEventCount(t, s, probe)
		expectPinRefusal := func(name string, err error) {
			t.Helper()
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
				t.Fatalf("%s initialization = %v, want the typed pin refusal", name, err)
			}
			if readWorkVersion(t, s, probe) != probeVersion || approvalEventCount(t, s, probe) != probeEvents {
				t.Fatalf("%s refusal moved the probe item", name)
			}
			if rows := foldGuardRows(t, s); rows != 0 {
				t.Fatalf("%s refusal left %d write_guard rows", name, rows)
			}
		}
		expectPinRefusal("missing (builtin)", initialize(t, s, probe, registered, nil))
		expectPinRefusal("wrong (empty)", initialize(t, s, probe, registered, NewWorkflowDefinitionRegistry()))
		drifted := registered
		drifted.Digest = "sha256:" + strings.Repeat("0", 64)
		expectPinRefusal("drifted digest", initialize(t, s, probe, drifted, registry))
		if err := initialize(t, s, probe, registered, registry); err != nil {
			t.Fatalf("caller-registry initialization refused after the fail-closed controls: %v", err)
		}
		var probeRef, probeStep, probeDigest string
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest,current_step FROM workflow_instances WHERE work_id=?`, probe).Scan(&probeRef, &probeDigest, &probeStep); err != nil {
			t.Fatal(err)
		}
		if probeRef != registered.Definition.Ref || probeDigest != registered.Digest {
			t.Fatalf("probe pin = %s %s, want the verified registered entry", probeRef, probeDigest)
		}
		if rows := foldGuardRows(t, s); rows != 0 {
			t.Fatalf("caller-registry initialization left %d write_guard rows", rows)
		}
		higherRegistered, err := registry.Register(higher)
		if err != nil {
			t.Fatalf("the higher local definition refused registration: %v", err)
		}
		if err := s.Transact(ctx, func(tx *Transaction) error {
			return RepinWorkflowTx(ctx, tx, WorkflowRepinRequest{WorkID: probe, EventID: "registry-repin:" + probe, Definition: higherRegistered, Actor: ownerFor(probe), Now: time.Unix(6, 0).UTC(), Registry: registry})
		}); err != nil {
			t.Fatalf("caller-registry repin refused: %v", err)
		}
		var repinVersion int64
		var repinRef, repinStep, repinDigest string
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest,current_step,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, probe).Scan(&repinRef, &repinDigest, &repinStep, &repinVersion); err != nil {
			t.Fatal(err)
		}
		if repinRef != higherRegistered.Definition.Ref || repinDigest != higherRegistered.Digest {
			t.Fatalf("repinned pin = %s %s, want the changed registered entry", repinRef, repinDigest)
		}
		if repinStep != probeStep {
			t.Fatalf("carry-forward repin moved the step from %s to %s", probeStep, repinStep)
		}
		repinVersionBefore := repinVersion
		repinEvents := approvalEventCount(t, s, probe)
		if err := s.Transact(ctx, func(tx *Transaction) error {
			return RepinWorkflowTx(ctx, tx, WorkflowRepinRequest{WorkID: probe, EventID: "registry-repin-missing:" + probe, Definition: higherRegistered, Actor: ownerFor(probe), Now: time.Unix(7, 0).UTC(), Registry: nil})
		}); err == nil {
			t.Fatal("missing-registry repin was accepted")
		} else {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
				t.Fatalf("missing-registry repin = %v, want the typed pin refusal", err)
			}
		}
		if readWorkVersion(t, s, probe) != repinVersionBefore || approvalEventCount(t, s, probe) != repinEvents || foldGuardRows(t, s) != 0 {
			t.Fatal("the refused repin moved the probe item")
		}
		const route = "registry-route"
		registry, registered = newResolutionRegistry(t, definition)
		seedWork(t, s, route)
		if err := initialize(t, s, route, registered, registry); err != nil {
			t.Fatalf("route initialization refused: %v", err)
		}
		recordResolutionActors(t, s, route)
		advanceResolutionToExecution(t, s, registry, route, ownerFor(route), []string{})
		advanceResolutionDelivery(t, s, registry, route, ownerFor(route))
		seedResolutionInvestigation(t, s, route)
		routeVersion := readWorkVersion(t, s, route)
		routeEvents := approvalEventCount(t, s, route)
		if readWorkVersion(t, s, route) != routeVersion || approvalEventCount(t, s, route) != routeEvents || foldGuardRows(t, s) != 0 {
			t.Fatal("the selection reads moved the route item")
		}
		readRoutePin := func(reg DefinitionRegistry) (WorkPin, bool, error) {
			var pin WorkPin
			var available bool
			txErr := s.Transact(ctx, func(tx *Transaction) error {
				var err error
				pin, available, err = ReadMutationWorkPinTxForRegistry(ctx, tx, reg, route, nil)
				return err
			})
			return pin, available, txErr
		}
		pin, available, err := readRoutePin(registry)
		if err != nil || !available {
			t.Fatalf("caller-registry mutation pin = available %v, %v, want the readable pin", available, err)
		}
		if pin.WorkflowType != registered.Definition.Ref || pin.WorkflowDefinitionDigest == nil || *pin.WorkflowDefinitionDigest != registered.Digest {
			t.Fatalf("caller-registry pin identity = %s %v, want the verified registered entry", pin.WorkflowType, pin.WorkflowDefinitionDigest)
		}
		parity, _, err := readRoutePin(registry)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pin, parity) {
			t.Fatalf("second mutation pin = %+v, want %+v", parity, pin)
		}
		var wrongPinRefusal *Failure
		_, _, wrongPinErr := readRoutePin(NewWorkflowDefinitionRegistry())
		if wrongPinErr == nil || !failureAs(wrongPinErr, &wrongPinRefusal) || wrongPinRefusal.Kind != KindInvariantViolation {
			t.Fatalf("wrong-registry mutation pin = %v, want the typed pin refusal", wrongPinErr)
		}
		var builtinConvention *Failure
		builtinConventionErr := s.Transact(ctx, func(tx *Transaction) error {
			_, _, err := ReadMutationWorkPinTxForRegistry(ctx, tx, nil, route, nil)
			return err
		})
		if builtinConventionErr == nil || !failureAs(builtinConventionErr, &builtinConvention) || builtinConvention.Kind != KindInvariantViolation || !strings.Contains(builtinConvention.Detail, "current workflow definition is unavailable") {
			t.Fatalf("builtin-convenience mutation pin = %v, want the typed unregistered refusal", builtinConventionErr)
		}
		const builtin = "registry-builtin"
		seedWork(t, s, builtin)
		builtinEntry, err := LiveWorkflowDefinitionForRef("workflow.implementation")
		if err != nil {
			t.Fatalf("the builtin implementation definition refused resolution: %v", err)
		}
		if err := initialize(t, s, builtin, builtinEntry, nil); err != nil {
			t.Fatalf("nil-registry builtin initialization refused: %v", err)
		}
		builtinPin, available, err := func() (WorkPin, bool, error) {
			var pin WorkPin
			var ready bool
			txErr := s.Transact(ctx, func(tx *Transaction) error {
				var err error
				pin, ready, err = ReadMutationWorkPinTxForRegistry(ctx, tx, nil, builtin, nil)
				return err
			})
			return pin, ready, txErr
		}()
		if err != nil || !available {
			t.Fatalf("builtin mutation pin = available %v, %v, want the readable pin", available, err)
		}
		if builtinPin.WorkflowDefinitionDigest == nil || *builtinPin.WorkflowDefinitionDigest != builtinEntry.Digest {
			t.Fatalf("builtin pin digest = %v, want the builtin entry %s", builtinPin.WorkflowDefinitionDigest, builtinEntry.Digest)
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
		if len(pin.NextValidIntents) != 1 || pin.NextValidIntents[0].ActionID != "supersede_contract" || pin.NextValidIntents[0].ReasonCode != "operator_contract_correction" {
			t.Fatalf("duplicate-active intents = %+v, want only the operator_contract_correction recovery", pin.NextValidIntents)
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
