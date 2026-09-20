package store

import (
	"context"
	"testing"
)

func TestAcknowledgeFailedLinearOperationsKeepsFailureState(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "disposition-product")
	setupLinearConnectionResource(t, s, "disposition-product", map[string]any{
		"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key"},
	})
	if _, err := s.SetProductPlanningMode(ctx, "disposition-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "disposition-work", "disposition-product-project", "Title", "Value")
	op, err := s.EnqueueLinearIssueForWork(ctx, "disposition-work", LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.FailLinearOperation(ctx, op.OperationID, "permanent", "credential rejected"); err != nil {
		t.Fatal(err)
	}

	dispositions, err := s.AcknowledgeFailedLinearOperations(ctx, "disposition-product", nil, "operator accepted the historical failure")
	if err != nil {
		t.Fatal(err)
	}
	if len(dispositions) != 1 || dispositions[0].OperationID != op.OperationID || dispositions[0].Disposition != "acknowledged" {
		t.Fatalf("dispositions = %+v", dispositions)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != LinearOutboxFailed {
		t.Fatalf("outbox state = %s, want failed", state)
	}
	dispositions, err = s.AcknowledgeFailedLinearOperations(ctx, "disposition-product", nil, "operator accepted the historical failure")
	if err != nil {
		t.Fatal(err)
	}
	if len(dispositions) != 0 {
		t.Fatalf("repeat disposition = %+v, want no undisposed rows", dispositions)
	}
}
