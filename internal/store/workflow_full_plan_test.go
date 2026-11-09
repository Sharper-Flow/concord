package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func seedFullWorkflowPlan(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx := t.Context()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil || plan == nil {
		t.Fatalf("read plan: plan=%v err=%v", plan, err)
	}
	for len(plan.Steps) < 64 {
		plan.Steps = append(plan.Steps, WorkflowPlanStep{ID: fmt.Sprintf("extra-%02d", len(plan.Steps)), After: []string{"review"}, Reason: "additional work"})
	}
	if err := writeWorkflowPlanTx(ctx, tx, workID, *plan); err != nil {
		t.Fatalf("seed full plan: %v", err)
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowFullPlanInsertRefusalNamesHandoff(t *testing.T) {
	const workID = "full-plan-insert"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", nil)
	defer f.store.Close()
	seedFullWorkflowPlan(t, f.store, workID)
	before := readWorkVersion(t, f.store, workID)
	err := runVerdictActionAs(t, f.store, workID, "insert_step", json.RawMessage(`{"id":"overflow","after":["review"],"reason":"additional work"}`), 0, f.owner)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidOperation {
		t.Fatalf("insert at limit = %v, want invalid operation", err)
	}
	if got := readWorkVersion(t, f.store, workID); got != before {
		t.Errorf("refused insertion changed version from %d to %d", before, got)
	}
	message := failure.Detail + " " + failure.RecoveryAction
	if !strings.Contains(message, "64") || !strings.Contains(message, "link_successor") {
		t.Errorf("insert at limit detail=%q recovery=%q; want 64-step limit and link_successor handoff", failure.Detail, failure.RecoveryAction)
	}
}

func TestWorkflowFullCompletedPlanLinksSuccessor(t *testing.T) {
	const source, target = "full-plan-source", "full-plan-successor"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, source, WorkflowStepPlanChange, "review", nil)
	defer f.store.Close()
	seedFullWorkflowPlan(t, f.store, source)
	completeWorkflowPlanFixture(t, f.store, source)
	plan, err := readWorkflowPlanTx(t.Context(), f.store.db, source)
	if err != nil || plan == nil || len(plan.Steps) != 64 || !WorkflowStepPlanComplete(*plan) || len(WorkflowStepPlanFrontier(*plan)) != 0 {
		t.Fatalf("expected completed 64-step plan with empty frontier: plan=%v err=%v", plan, err)
	}
	seedWork(t, f.store, target)
	if err := f.store.Transact(t.Context(), func(tx *Transaction) error {
		return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: target, Actor: f.owner, Now: time.Unix(1, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, f.store, target, WorkflowStepPlanFix, "implement")
	if err := runVerdictActionAs(t, f.store, source, "link_successor", mustJSONValue(map[string]any{"successor_work_id": target}), 0, f.owner); err != nil {
		t.Fatalf("link successor on completed full plan: %v", err)
	}
	var count int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_impact_edges WHERE work_id=? AND target_work_id=? AND edge_kind='forward_link'`, source, target).Scan(&count); err != nil || count != 1 {
		t.Fatalf("forward links=%d err=%v", count, err)
	}
}

func TestCompletedPlanSuccessorPinAndRepeatedWrites(t *testing.T) {
	const source = "completed-successor-source"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, source, WorkflowStepPlanChange, "review", nil)
	defer f.store.Close()
	completeWorkflowPlanFixture(t, f.store, source)
	for _, target := range []string{"completed-successor-one", "completed-successor-two"} {
		pin, err := ReadWorkPin(t.Context(), f.store, source)
		if err != nil || !workPinContainsAction(pin.NextValidIntents, "link_successor") {
			t.Fatalf("completed plan pin=%+v err=%v; want link_successor", pin, err)
		}
		seedWork(t, f.store, target)
		if err := f.store.Transact(t.Context(), func(tx *Transaction) error {
			return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: target, Actor: f.owner, Now: time.Unix(1, 0).UTC()})
		}); err != nil {
			t.Fatal(err)
		}
		seedWorkflowPlanFrontier(t, f.store, target, WorkflowStepPlanFix, "implement")
		if err := runVerdictActionAs(t, f.store, source, "link_successor", mustJSONValue(map[string]any{"successor_work_id": target}), 0, f.owner); err != nil {
			t.Fatalf("link %s: %v", target, err)
		}
	}
	var count int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_impact_edges WHERE work_id=? AND edge_kind='forward_link'`, source).Scan(&count); err != nil || count != 2 {
		t.Fatalf("successor links=%d err=%v, want 2", count, err)
	}
	version := readWorkVersion(t, f.store, source)
	if err := applyWorkEvent(t, f.store, workTransitionEvent("end-successor-source", source, "in_progress", "cancelled", version, version+1), workVersion(source, version)); err != nil {
		t.Fatal(err)
	}
	pin, err := ReadWorkPin(t.Context(), f.store, source)
	if err != nil || workPinContainsAction(pin.NextValidIntents, "link_successor") {
		t.Fatalf("terminal pin=%+v err=%v; must not offer link_successor", pin, err)
	}
	err = runVerdictActionAs(t, f.store, source, "link_successor", json.RawMessage(`{"successor_work_id":"completed-successor-one"}`), 0, f.owner)
	if !hasFailureKind(err, KindInvalidOperation) {
		t.Fatalf("terminal successor link=%v, want invalid operation", err)
	}
}

func TestCompletedPlanSuccessorWithAmbiguousContracts(t *testing.T) {
	const source, target = "ambiguous-successor-source", "ambiguous-successor-target"
	f := seedWorkflowReturnRouteFixture(t, source, WorkflowStepPlanChange, "review")
	defer f.store.Close()
	completeWorkflowPlanFixture(t, f.store, source)
	duplicateActiveWorkflowContract(t.Context(), t, f.store, source)
	seedWork(t, f.store, target)
	if err := f.store.Transact(t.Context(), func(tx *Transaction) error {
		return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: target, Actor: f.owner, Now: time.Unix(1, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, f.store, target, WorkflowStepPlanFix, "implement")
	pin, err := ReadWorkPin(t.Context(), f.store, source)
	if err != nil || !workPinContainsAction(pin.NextValidIntents, "link_successor") {
		t.Fatalf("ambiguous-contract pin=%+v err=%v; want link_successor", pin, err)
	}
	if err := runVerdictActionAs(t, f.store, source, "link_successor", mustJSONValue(map[string]any{"successor_work_id": target}), 0, f.owner); err != nil {
		t.Fatalf("link under ambiguous contracts: %v", err)
	}
}

func TestCompletedPlanSuccessorWithoutContract(t *testing.T) {
	const source, target = "contractless-successor-source", "contractless-successor-target"
	f := contractlessStepFixture(t, source, WorkflowStepPlanChange, "review")
	defer f.store.Close()
	completeWorkflowPlanFixture(t, f.store, source)
	seedWork(t, f.store, target)
	if err := f.store.Transact(t.Context(), func(tx *Transaction) error {
		return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: target, Actor: f.owner, Now: time.Unix(1, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, f.store, target, WorkflowStepPlanFix, "implement")
	pin, err := ReadWorkPin(t.Context(), f.store, source)
	if err != nil || !workPinContainsAction(pin.NextValidIntents, "link_successor") {
		t.Fatalf("contractless pin=%+v err=%v; want link_successor", pin, err)
	}
	if err := runVerdictActionAs(t, f.store, source, "link_successor", mustJSONValue(map[string]any{"successor_work_id": target}), 0, f.owner); err != nil {
		t.Fatalf("link without contract: %v", err)
	}
}

func TestSuccessorRemainsFrontierBoundOnIncompletePlan(t *testing.T) {
	const source = "incomplete-successor-source"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, source, WorkflowStepPlanChange, "plan", nil)
	defer f.store.Close()
	before := readWorkVersion(t, f.store, source)
	err := runVerdictActionAs(t, f.store, source, "link_successor", json.RawMessage(`{"successor_work_id":"unused-target"}`), 0, f.owner)
	if !hasFailureKind(err, KindIllegalLifecycleTransition) {
		t.Fatalf("incomplete plan link: %v, want frontier refusal", err)
	}
	if got := readWorkVersion(t, f.store, source); got != before {
		t.Fatalf("refusal changed version: %d to %d", before, got)
	}
}
