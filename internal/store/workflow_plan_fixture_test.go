package store

import (
	"context"
	"fmt"
	"testing"
)

// currentWorkflowPlanFixture returns a fresh copy of the real type template.
func currentWorkflowPlanFixture(t testing.TB, planType WorkflowStepPlanType) WorkflowStepPlan {
	t.Helper()
	plan, err := WorkflowStepPlanTemplate(planType)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func workflowPlanFixtureFrontier(t testing.TB, s *Store, workID string) string {
	t.Helper()
	plan, err := readWorkflowPlanTx(context.Background(), s.db, workID)
	if err != nil || plan == nil {
		t.Fatalf("read plan: plan=%v err=%v", plan, err)
	}
	frontier := WorkflowStepPlanFrontier(*plan)
	if len(frontier) != 1 {
		t.Fatalf("expected one plan frontier: %v", frontier)
	}
	return frontier[0].ID
}

// seedWorkflowPlanFrontier builds a consistent synthetic prefix, without
// claiming the runtime review or evidence receipts that tests must supply.
func seedWorkflowPlanFrontier(t testing.TB, s *Store, workID string, planType WorkflowStepPlanType, target string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	plan := currentWorkflowPlanFixture(t, planType)
	if !stepIDExists(plan.Steps, target) {
		t.Fatalf("workflow type %s has no step %q", planType, target)
	}
	if err := writeWorkflowPreludeTx(ctx, tx, workID, workflowPreludeState{Intake: workflowIntakeRecord{Outcome: WorkflowIntakeProceed}}); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkflowPlanTx(ctx, tx, workID, plan); err != nil {
		t.Fatal(err)
	}
	for _, step := range plan.Steps {
		if step.ID == target {
			break
		}
		current, err := readWorkflowPlanTx(ctx, tx, workID)
		if err != nil || current == nil {
			t.Fatalf("read synthetic plan: %v", err)
		}
		index := -1
		for i := range current.Steps {
			if current.Steps[i].ID == step.ID {
				index = i
				break
			}
		}
		attempt := WorkflowPlanAttempt{Note: "synthetic completed prerequisite", CompletedAt: "2026-09-12T00:00:00Z"}
		if step.Reviewed {
			attempt.Reviewer, attempt.Result = "concord-review", "pass"
			attempt.Commit, attempt.Link = "commit:fixture", "https://example.com/review"
		}
		current.Steps[index].Attempts = append(current.Steps[index].Attempts, attempt)
		if err := writeWorkflowPlanTx(ctx, tx, workID, *current); err != nil {
			t.Fatalf("complete synthetic prerequisite %s: %v", step.ID, err)
		}
	}
	current, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil || current == nil {
		t.Fatalf("read synthetic frontier: %v", err)
	}
	frontier := WorkflowStepPlanFrontier(*current)
	if len(frontier) != 1 || frontier[0].ID != target {
		t.Fatal(fmt.Errorf("synthetic frontier %v does not contain only %s", frontier, target))
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// insertWorkflowPlanFixtureStep uses the same validated plan write as runtime.
func insertWorkflowPlanFixtureStep(t testing.TB, s *Store, workID string, step WorkflowPlanStep) {
	t.Helper()
	ctx := context.Background()
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
		t.Fatalf("read plan for insertion: plan=%v err=%v", plan, err)
	}
	plan.Steps = append(plan.Steps, step)
	if err := writeWorkflowPlanTx(ctx, tx, workID, *plan); err != nil {
		t.Fatal(err)
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// moveWorkflowPlanFixtureFrontier completes synthetic prerequisites on a
// linear current frontier. It never supplies the separate Review receipts.
func moveWorkflowPlanFixtureFrontier(t testing.TB, s *Store, workID, target string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	for {
		plan, err := readWorkflowPlanTx(ctx, tx, workID)
		if err != nil || plan == nil {
			t.Fatalf("read plan: plan=%v err=%v", plan, err)
		}
		if !stepIDExists(plan.Steps, target) {
			t.Fatalf("plan type %s has no step %q", plan.Type, target)
		}
		frontier := WorkflowStepPlanFrontier(*plan)
		if len(frontier) != 1 {
			t.Fatalf("expected one open frontier toward %q, got %v", target, frontier)
		}
		step := frontier[0]
		if step.ID == target {
			break
		}
		attempt := WorkflowPlanAttempt{Note: "synthetic completed prerequisite", CompletedAt: "2026-09-12T00:00:00Z"}
		if step.Reviewed {
			attempt.Reviewer, attempt.Result = "concord-review", "pass"
			attempt.Commit, attempt.Link = "commit:fixture", "https://example.com/review"
		}
		for i := range plan.Steps {
			if plan.Steps[i].ID == step.ID {
				plan.Steps[i].Attempts = append(plan.Steps[i].Attempts, attempt)
				break
			}
		}
		if err := writeWorkflowPlanTx(ctx, tx, workID, *plan); err != nil {
			t.Fatalf("complete synthetic step %s: %v", step.ID, err)
		}
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// completeWorkflowPlanFixture records synthetic plan attempts and terminal
// premise and delivery facts; it does not create review, verdict, or evidence receipts.
func completeWorkflowPlanFixture(t testing.TB, s *Store, workID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	for {
		plan, err := readWorkflowPlanTx(ctx, tx, workID)
		if err != nil || plan == nil {
			t.Fatalf("read plan for completion: plan=%v err=%v", plan, err)
		}
		frontier := WorkflowStepPlanFrontier(*plan)
		if len(frontier) == 0 {
			break
		}
		step := frontier[0]
		attempt := WorkflowPlanAttempt{Note: "synthetic completed prerequisite", CompletedAt: "2026-09-12T00:00:00Z"}
		if step.Reviewed {
			attempt.Reviewer, attempt.Result = "concord-review", "pass"
			attempt.Commit, attempt.Link = "commit:fixture", "https://example.com/review"
		}
		for i := range plan.Steps {
			if plan.Steps[i].ID == step.ID {
				plan.Steps[i].Attempts = append(plan.Steps[i].Attempts, attempt)
				break
			}
		}
		if err := writeWorkflowPlanTx(ctx, tx, workID, *plan); err != nil {
			t.Fatalf("complete synthetic step %s: %v", step.ID, err)
		}
	}
	plan, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil || plan == nil {
		t.Fatalf("read completed plan: plan=%v err=%v", plan, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at)
		SELECT work_id,contract_version,approved_by,approved_at FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
		t.Fatal(err)
	}
	if plan.Type != WorkflowStepPlanResearch {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload)
			SELECT work_id,1,'delivery:fixture:' || work_id,approved_by,approved_at,1,'{"delivery_artifact":"https://example.com/delivery","delivery_state":"asserted"}' FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
			t.Fatal(err)
		}
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
