package store

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func workPinOutstandingKindsWire(t *testing.T, pin *WorkPin) ([]string, bool) {
	t.Helper()
	raw, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	value, present := fields["outstanding_evidence_kinds"]
	if !present {
		return nil, false
	}
	var kinds []string
	if err := json.Unmarshal(value, &kinds); err != nil {
		t.Fatal(err)
	}
	if kinds == nil {
		t.Fatal("outstanding_evidence_kinds is null, not a known array")
	}
	return kinds, true
}

func requireWorkPinOutstandingKinds(t *testing.T, pin *WorkPin, want []string) {
	t.Helper()
	kinds, present := workPinOutstandingKindsWire(t, pin)
	if !present || !reflect.DeepEqual(kinds, want) {
		t.Fatalf("outstanding_evidence_kinds = %v (present=%t), want %v", kinds, present, want)
	}
}

func TestWorkPinOutstandingEvidenceKindsShrinkAfterBinding(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t, "durable_note", "commit")
	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &pin, []string{"commit", "durable_note"})
	for _, binding := range []struct {
		kind string
		ref  string
		want []string
	}{
		{kind: "commit", ref: wf04RecoveryRef, want: []string{"durable_note"}},
		{kind: "durable_note", ref: "note:outstanding-kind", want: []string{}},
	} {
		before := pin
		if err := f.bind(ctx, "bind-"+binding.kind, before.Version, binding.kind, binding.ref); err != nil {
			t.Fatalf("bind %s: %v", binding.kind, err)
		}
		pin, err = ReadWorkPin(ctx, f.store, f.workID)
		if err != nil {
			t.Fatal(err)
		}
		requireWorkPinOutstandingKinds(t, &pin, binding.want)
		if pin.Version <= before.Version || pin.Version != f.version(ctx, t) || pin.Step != before.Step {
			t.Fatalf("binding snapshot = version %d step %q, before %d %q", pin.Version, pin.Step, before.Version, before.Step)
		}
	}
}

func TestWorkPinOutstandingEvidenceKindsKnownEmpty(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)
	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &pin, []string{})
}

func TestWorkPinOutstandingEvidenceKindsRequireProducerAuthority(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t, "commit")
	if err := f.bind(ctx, "producer-authority", f.version(ctx, t), "commit", wf04RecoveryRef); err != nil {
		t.Fatal(err)
	}
	before, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &before, []string{})
	result, err := f.store.DatabaseForTesting().Exec(`DELETE FROM durable_operations WHERE op_id=(SELECT json_extract(payload,'$.producer_run_ref') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='commit' AND json_extract(payload,'$.immutable_subject_ref')=?)`, f.workID, WorkflowEvidenceBound, wf04RecoveryRef)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("removed producer operations = %d, err=%v, want exactly one", rows, err)
	}
	after, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &after, []string{"commit"})
	if after.Version != before.Version || after.Step != before.Step {
		t.Fatal("reading missing producer authority changed the workflow")
	}
}

func TestWorkPinOutstandingEvidenceKindsContinuity(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	const workID = "workpin-outstanding-continuity"
	seedLawContextFixture(t, s, workID)
	binding := WorkflowArchitectureBinding{
		DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
		DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
		VerificationObligations: []WorkflowVerificationObligation{{LawID: "spec:one", ObligationID: "verification"}},
	}
	approveLawContextContract(t, s, workID, []string{"spec:one"}, []string{}, binding)
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &pin, []string{"artifact", "review", "verification"})
	snapshot, err := ReadWorkflowContinuity(ctx, s, ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkPin == nil || snapshot.WorkPin.Version != pin.Version || snapshot.WorkPin.Step != pin.Step {
		t.Fatalf("continuity pin = %+v, direct pin = %+v", snapshot.WorkPin, pin)
	}
	requireWorkPinOutstandingKinds(t, snapshot.WorkPin, []string{"artifact", "review", "verification"})
}

func TestWorkPinOutstandingEvidenceKindsIncludeRecoveryCheck(t *testing.T) {
	for _, missingProducer := range []bool{false, true} {
		name := "predicate-subject"
		if missingProducer {
			name = "kind-and-predicate-subject"
		}
		t.Run(name, func(t *testing.T) {
			workID := "workpin-debt-" + name
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
			if missingProducer {
				if _, err := fixture.store.DatabaseForTesting().Exec(`DELETE FROM durable_operations WHERE op_id=?`, "return-authority-verification-"+workID); err != nil {
					t.Fatal(err)
				}
			}
			pin, err := ReadWorkPin(context.Background(), fixture.store, workID)
			if err != nil {
				t.Fatal(err)
			}
			requireWorkPinOutstandingKinds(t, &pin, []string{"verification"})
		})
	}
}

func TestWorkPinOutstandingEvidenceKindsUseCallerTransaction(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)
	before, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &before, []string{})
	tx, err := f.store.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_contracts SET required_evidence='["commit"]' WHERE work_id=? AND superseded_by IS NULL`, f.workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	uncommitted, err := ReadWorkPinTx(ctx, tx, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &uncommitted, []string{"commit"})
	if uncommitted.Version != before.Version || uncommitted.Step != before.Step {
		t.Fatal("debt read changed the pinned version or step")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	requireWorkPinOutstandingKinds(t, &after, []string{})
}

func TestWorkPinOutstandingEvidenceKindsOmittedWithoutContractAuthority(t *testing.T) {
	ctx := context.Background()
	t.Run("no-contract", func(t *testing.T) {
		s := openTemp(t)
		continuityTestWorkflow(t, s, "workpin-debt-no-contract")
		pin, err := ReadWorkPin(ctx, s, "workpin-debt-no-contract")
		if err != nil {
			t.Fatal(err)
		}
		if _, present := workPinOutstandingKindsWire(t, &pin); present {
			t.Fatal("no-contract pin claims known evidence debt")
		}
	})
	t.Run("ambiguous-contract", func(t *testing.T) {
		f := newAcceptanceRecoveryFixture(ctx, t, "commit")
		duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
		pin, err := ReadWorkPin(ctx, f.store, f.workID)
		if err != nil {
			t.Fatal(err)
		}
		if _, present := workPinOutstandingKindsWire(t, &pin); present {
			t.Fatal("ambiguous-contract pin claims known evidence debt")
		}
	})
	t.Run("outside-repair", func(t *testing.T) {
		workID := "workpin-debt-outside-repair"
		f := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
		req := outsideRepairTestRequest(t, f.store, workID, "hold")
		if err := outsideRepairApply(t, f.store, req, WorkflowOutsideRepairDispositionSet, OutsideRepairEvidence{}); err != nil {
			t.Fatal(err)
		}
		pin, err := ReadWorkPin(ctx, f.store, workID)
		if err != nil {
			t.Fatal(err)
		}
		if _, present := workPinOutstandingKindsWire(t, &pin); present {
			t.Fatal("outside-repair partial pin claims managed evidence debt")
		}
	})
}

func TestWorkPinOutstandingEvidenceKindsRecoveryIntentAcrossDefinitions(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
		current, err := BuiltinWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := BuiltinWorkflowRegistry().Lookup(ref, current.Definition.Version-1); ok {
			t.Fatalf("retired definition %s@%d remains executable", ref, current.Definition.Version-1)
		}
		for _, version := range []int64{current.Definition.Version} {
			name := ref + "@" + strconv.FormatInt(version, 10)
			t.Run(name, func(t *testing.T) {
				registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
				if !ok {
					t.Fatalf("definition %s is not registered", name)
				}
				workID := "workpin-debt-" + ref + "-" + strconv.FormatInt(version, 10)
				var completionStep string
				for _, step := range registered.Definition.StepGraph.Steps {
					if slices.Contains(step.Actions, "complete") {
						completionStep = step.ID
					}
				}
				if completionStep == "" {
					t.Fatalf("definition %s has no completion step", name)
				}
				f := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, registered, completionStep, []string{"commit"}, []string{"verification", "review", "artifact"})
				pin, err := ReadWorkPin(context.Background(), f.store, workID)
				if err != nil {
					t.Fatal(err)
				}
				want := []string{"commit"}
				for _, kind := range registered.Definition.OutcomeSchema.AllowedKinds {
					if kind == PredicateCheck {
						want = append(want, "verification")
					}
				}
				requireWorkPinOutstandingKinds(t, &pin, want)
				var requiredFields []string
				for _, action := range registered.Definition.ActionDefinitions {
					if action.ID == "bind_evidence" {
						for _, field := range publicWorkflowActionPayload(action).Fields {
							if field.Required {
								requiredFields = append(requiredFields, field.Name)
							}
						}
					}
				}
				count := 0
				for _, intent := range pin.NextValidIntents {
					if intent.ActionID != "bind_evidence" {
						continue
					}
					count++
					if intent.ExpectedVersion != pin.Version || !slices.Equal(intent.RequiredFields, requiredFields) {
						t.Fatalf("bind intent = %+v, want version %d fields %v", intent, pin.Version, requiredFields)
					}
				}
				if count != 1 {
					t.Fatalf("bind intents = %d, want exactly one", count)
				}
			})
		}
	}
}
