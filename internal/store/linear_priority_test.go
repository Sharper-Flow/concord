package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLinearCreatePayloadSeedsPriorityFromUrgency(t *testing.T) {
	cases := []struct {
		name         string
		urgency      string
		wantPriority int
	}{
		{name: "expedite seeds urgent", urgency: "expedite", wantPriority: 1},
		{name: "standard seeds medium", urgency: "standard", wantPriority: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			ctx := context.Background()
			setupLinearProduct(t, s, "priority-product")
			setupLinearConnectionResource(t, s, "priority-product", map[string]any{"linear": map[string]any{
				"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
			}})
			if _, err := s.SetProductPlanningMode(ctx, "priority-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
				t.Fatal(err)
			}
			seedLinearWorkItem(t, s, "priority-work", "priority-product-project", "Priority title", "Priority value")
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_items SET urgency=? WHERE id='priority-work'; DELETE FROM fold_guard`, tc.urgency); err != nil {
				t.Fatal(err)
			}
			op, err := s.EnqueueLinearIssueForWork(ctx, "priority-work", LinearOpIssueCreate)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Priority int `json:"priority"`
			}
			var raw string
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT payload FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Priority != tc.wantPriority {
				t.Fatalf("create payload priority = %d, want %d", payload.Priority, tc.wantPriority)
			}
		})
	}
}

func TestLinearUpdatePayloadOmitsPriority(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	setupLinearProduct(t, s, "priority-product")
	setupLinearConnectionResource(t, s, "priority-product", map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1", "auth_mode": "personal_api_key",
	}})
	if _, err := s.SetProductPlanningMode(ctx, "priority-product", PlanningModeLinear, "pilot", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedLinearWorkItem(t, s, "priority-work", "priority-product-project", "Priority title", "Priority value")
	if _, err := s.EnqueueLinearIssueForWork(ctx, "priority-work", LinearOpIssueCreate); err != nil {
		t.Fatal(err)
	}
	update, err := s.EnqueueLinearIssueForWork(ctx, "priority-work", LinearOpIssueUpdate)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT payload FROM linear_outbox WHERE operation_id=?`, update.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, `"priority"`) {
		t.Fatalf("update payload %s must never carry a priority", raw)
	}
}
