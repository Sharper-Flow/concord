package store

import (
	"errors"
	"strings"
	"testing"
)

func TestWorkflowPlanMandateGuardNamesLawAndPreservesBinding(t *testing.T) {
	for _, planType := range []WorkflowStepPlanType{WorkflowStepPlanChange, WorkflowStepPlanFix, WorkflowStepPlanResearch, WorkflowStepPlanRemoval} {
		t.Run(string(planType), func(t *testing.T) {
			plan := currentWorkflowPlanFixture(t, planType)
			step := ""
			for _, candidate := range plan.Steps {
				if workflowPlanAllowsAction(&plan, candidate.ID, "bind_evidence") {
					step = candidate.ID
					break
				}
			}
			if step == "" {
				t.Fatal("plan has no evidence binding step")
			}
			workID := "mandate-" + string(planType)
			f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, planType, step, []string{}, nil)
			ctx := t.Context()
			tx, err := f.store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE workflow_contracts SET spec_mandate='["law:required"]' WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
				t.Fatal(err)
			}
			bind := currentActionDefinition("bind_evidence", false)
			if err := guardPlanMandate(ctx, tx, workID, &plan, step, bind); err != nil {
				t.Fatalf("binding refused: %v", err)
			}
			err = guardPlanMandate(ctx, tx, workID, &plan, step, currentActionDefinition("record_review", false))
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "law:required") || !strings.Contains(failure.RecoveryAction, "bind_evidence") {
				t.Fatalf("unbound mandate refusal=%v", err)
			}
		})
	}
}
