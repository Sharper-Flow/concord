package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// deliveryGateFixture pins one bug work item's workflow instance to the
// current break_fix definition sitting on the CD-0165 delivery gate, the
// state a session leaves behind when it ends with the change not yet on the
// default branch.
func deliveryGateFixture(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("p-deliv"), locatorProjectEvent("pr-deliv"), locatorMembershipEvent("p-deliv", "pr-deliv")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "p-deliv"): 0, VersionRef(SubjectProject, "pr-deliv"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "deliv-work-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-deliv", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"bug","title":"Delivery","priority":1}`)},
		{EventID: "deliv-work-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-deliv", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"pr-deliv","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}}); err != nil {
		t.Fatal(err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 13)
	if !ok {
		t.Fatal("workflow.break_fix v13 is not registered")
	}
	if !workflowStepIsDeliveryGate(workflowStep(registered.Definition, "delivery")) {
		t.Fatal("workflow.break_fix v13 has no delivery gate step named delivery")
	}
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state) VALUES('work-deliv','workflow.break_fix',?,?,'delivery','running')`, registered.Definition.Version, registered.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	return s
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

func TestWorkflowReadParksDeliveryAtGate(t *testing.T) {
	t.Parallel()
	s := deliveryGateFixture(t)
	out, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: "work-deliv"})
	if err != nil {
		t.Fatal(err)
	}
	if out.ParkedDelivery == nil {
		t.Fatal("delivery-gate step did not project a parked delivery")
	}
	if out.ParkedDelivery.StepID != "delivery" || out.ParkedDelivery.ResumeAction != "record_delivery" {
		t.Fatalf("parked delivery carries %+v", out.ParkedDelivery)
	}
	if out.ParkedDelivery.Unreconciled {
		t.Fatal("a running instance must not read as unreconciled")
	}
	if out.ParkedDelivery.ParkedSeconds < 0 {
		t.Fatalf("parked duration %d is negative", out.ParkedDelivery.ParkedSeconds)
	}
}

func TestWorkflowReadMarksUnreconciledAfterCancellation(t *testing.T) {
	t.Parallel()
	s := deliveryGateFixture(t)
	if err := transitionDeliverWork(t, s, "cancelled", "superseded by operator judgement"); err != nil {
		t.Fatal(err)
	}
	out, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: "work-deliv"})
	if err != nil {
		t.Fatal(err)
	}
	if out.ParkedDelivery == nil || !out.ParkedDelivery.Unreconciled {
		t.Fatalf("cancelled-at-gate item must read as unreconciled, got %+v", out.ParkedDelivery)
	}
}

func TestWorkflowCompletionRefusedAtDeliveryGate(t *testing.T) {
	t.Parallel()
	s := deliveryGateFixture(t)
	err := transitionDeliverWork(t, s, "completed", "done")
	if err == nil {
		t.Fatal("completion at the delivery gate must refuse")
	}
	if !strings.Contains(err.Error(), "unreconciled delivery") {
		t.Fatalf("refusal must name the unreconciled delivery, got %v", err)
	}
}

func TestProductRowsCountParkedDeliveries(t *testing.T) {
	t.Parallel()
	s := deliveryGateFixture(t)
	result, err := s.QueryProductRows(context.Background(), ProductRowRequest{Product: "p-deliv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 {
		t.Fatalf("want one Product row, got %d", len(result.Rows))
	}
	values := result.Rows[0].ActionCounts.Values
	if values == nil {
		t.Fatal("action counts are unavailable")
	}
	if values.ParkedDeliveries != 1 {
		t.Fatalf("want one parked delivery, got %d", values.ParkedDeliveries)
	}
	if err := transitionDeliverWork(t, s, "cancelled", "abandoned"); err != nil {
		t.Fatal(err)
	}
	result, err = s.QueryProductRows(context.Background(), ProductRowRequest{Product: "p-deliv"})
	if err != nil {
		t.Fatal(err)
	}
	if values = result.Rows[0].ActionCounts.Values; values == nil || values.ParkedDeliveries != 0 {
		t.Fatalf("a terminal item must leave the parked count, got %+v", values)
	}
}
