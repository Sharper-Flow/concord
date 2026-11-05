package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// deliveryGateFixture places a fix plan at its delivery-capable review frontier.
func deliveryGateFixture(t *testing.T) *Store {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithPlan(t, "work-deliv", WorkflowStepPlanFix, "review", []string{"review", "artifact"}).store
}

func transitionDeliverWork(t *testing.T, s *Store, to, reason string) error {
	t.Helper()
	ctx := context.Background()
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-deliv'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"from": "needed", "to": to, "reason": reason, "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	err = ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "deliv-transition-" + to, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "work-deliv", Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: payload},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-deliv"): version}})
	return err
}

func TestWorkflowCompletionRefusedAtDeliveryGate(t *testing.T) {
	t.Parallel()
	s := deliveryGateFixture(t)
	err := transitionDeliverWork(t, s, "completed", "done")
	if err == nil {
		t.Fatal("completion at the delivery gate must refuse")
	}
	if !strings.Contains(err.Error(), "workflow instance") {
		t.Fatalf("refusal must name the live workflow instance, got %v", err)
	}
	// CD-0183 D4: the remedy names the workflow completion route, not the lifecycle.
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindNotTerminal || !strings.Contains(failure.RecoveryAction, "workflow_action complete") {
		t.Fatalf("refusal remedy = %+v, want not_terminal naming workflow_action complete", failure)
	}
}
