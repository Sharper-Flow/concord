package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// carryForwardFixture pins a work item to a named built-in definition version
// and, when step is not empty, walks it to that step of the pin.
func carryForwardFixture(t *testing.T, s *Store, workID, ref string, version int64, step string) WorkflowActor {
	t.Helper()
	entry, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("%s version %d is not registered", ref, version)
	}
	seedStepWork(t, s, workID)
	initializeStepWorkflow(t, s, workID, entry.Definition)
	if step != "" {
		actorRef, err := WorkflowActorRef(stepFixtureActor())
		if err != nil {
			t.Fatal(err)
		}
		if err := advanceWorkflowTestInstanceToStep(context.Background(), s, workID, step, actorRef); err != nil {
			t.Fatal(err)
		}
	}
	return stepFixtureActor()
}

// An instance stranded behind a promotion carries forward onto the current
// definition while it keeps its position: the pin moves to the current
// version and digest, and the step it holds is preserved rather than reset.
func TestCarryForwardOntoTheCurrentVersionPreservesTheStep(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "carry-forward-step"
	carryForwardFixture(t, s, workID, "workflow.break_fix", 12, "diagnose")
	current, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	if current.Definition.Version != 14 {
		t.Fatalf("current break_fix version = %d, want 14", current.Definition.Version)
	}
	// An in-flight attempt on the held step: the stranded shape the carry
	// forward exists for.
	startStepFixtureAction(t, s, workID, "record_root_cause")
	ctx := context.Background()
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		return RepinWorkflowTx(ctx, transaction, WorkflowRepinRequest{WorkID: workID, EventID: workID + "-carry", Definition: current, Actor: stepFixtureActor(), Now: time.Unix(30, 0).UTC()})
	}); err != nil {
		t.Fatalf("carry forward refused: %v", err)
	}
	ref, version, digest := workflowInstancePin(t, s, workID)
	if ref != "workflow.break_fix" || version != 14 || digest != current.Digest {
		t.Fatalf("pin after carry forward = %s v%d %s, want workflow.break_fix v14 %s", ref, version, digest, current.Digest)
	}
	if got := readInstanceStep(t, s, workID); got != "diagnose" {
		t.Fatalf("step after carry forward = %q, want diagnose preserved", got)
	}
}

// The carry-forward admission is per instance: when the target definition
// does not contain the instance's current step, the fold refuses and the pin
// stays where it was.
func TestCarryForwardRefusesWhenTheStepIsAbsent(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "carry-forward-absent"
	carryForwardFixture(t, s, workID, "workflow.break_fix", 12, "")
	// A step no break_fix definition declares reaches storage only through a
	// second writer; the carry-forward admission is the guard that refuses to
	// promote a pin underneath it. The write runs inside a fold, which is the
	// only window the projection triggers admit.
	if err := s.Transact(context.Background(), func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "carry_forward_fixture")
		if err != nil {
			return err
		}
		if err := enterFold(context.Background(), tx); err != nil {
			return err
		}
		defer func() { _ = leaveFold(context.Background(), tx) }()
		_, err = tx.ExecContext(context.Background(), `UPDATE workflow_instances SET current_step='plan' WHERE work_id=?`, workID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	current, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = s.Transact(ctx, func(transaction *Transaction) error {
		return RepinWorkflowTx(ctx, transaction, WorkflowRepinRequest{WorkID: workID, EventID: workID + "-carry", Definition: current, Actor: stepFixtureActor(), Now: time.Unix(30, 0).UTC()})
	})
	if err == nil {
		t.Fatal("carry forward onto a definition without the instance's step was accepted, want refusal")
	}
	if !strings.Contains(err.Error(), "carry forward refuses a definition without the instance's current step") {
		t.Fatalf("carry forward refusal = %v, want the absent-step refusal", err)
	}
	if got := readInstanceStep(t, s, workID); got != "plan" {
		t.Fatalf("refused carry forward moved the step to %q, want plan", got)
	}
	if _, version, _ := workflowInstancePin(t, s, workID); version != 12 {
		t.Fatalf("refused carry forward moved the pin to v%d, want v12", version)
	}
}

// The mid-flight exemption is bounded: only a newer version of the pinned
// family carries forward. A same-family downgrade after execution starts
// stays refused.
func TestSameFamilyDowngradeStillRefusesAfterAnActionStarts(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "carry-forward-downgrade"
	carryForwardFixture(t, s, workID, "workflow.break_fix", 13, "diagnose")
	startStepFixtureAction(t, s, workID, "record_root_cause")
	older, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 12)
	if !ok {
		t.Fatal("workflow.break_fix version 12 is not registered")
	}
	ctx := context.Background()
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return RepinWorkflowTx(ctx, transaction, WorkflowRepinRequest{WorkID: workID, EventID: workID + "-downgrade", Definition: older, Actor: stepFixtureActor(), Now: time.Unix(30, 0).UTC()})
	})
	if err == nil {
		t.Fatal("same-family downgrade after an action start was accepted, want refusal")
	}
	if !strings.Contains(err.Error(), "definition cannot change after execution starts") {
		t.Fatalf("downgrade refusal = %v, want the execution-started refusal", err)
	}
	if _, version, _ := workflowInstancePin(t, s, workID); version != 13 {
		t.Fatalf("refused downgrade moved the pin to v%d, want v13", version)
	}
}

// A stranded instance holds an approved contract. The contract stays
// authoritative across a carry forward: it binds the work's premise and
// outcome, not the definition version, so the carry forward must not demand
// its supersession.
func TestCarryForwardKeepsAnApprovedContractAuthoritative(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "carry-forward-contract"
	// research is the stranded shape without Product-changing authority: its
	// contract carries no law boundary or architecture binding, which keeps
	// this test on the carry-forward semantics alone.
	carryForwardFixture(t, s, workID, "workflow.research", 8, "")
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	approval := workflowEventWithActor(workID+"-contract", WorkflowContractApproved, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1,
		"premise": "the stranded instance's approved premise",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:carry", "ordinal": 0, "outcome_kind": "outcome",
			"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"resolved", "report_recorded"}}}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	})
	approval.PayloadVersion = 3
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{approval}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	current, err := BuiltinWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		return RepinWorkflowTx(ctx, transaction, WorkflowRepinRequest{WorkID: workID, EventID: workID + "-carry", Definition: current, Actor: stepFixtureActor(), Now: time.Unix(40, 0).UTC()})
	}); err != nil {
		t.Fatalf("carry forward with an approved contract refused: %v", err)
	}
	if _, version, _ := workflowInstancePin(t, s, workID); version != 9 {
		t.Fatalf("pin after carry forward = v%d, want v9", version)
	}
	var active int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active contracts after carry forward = %d, want the approved contract kept authoritative", active)
	}
}
