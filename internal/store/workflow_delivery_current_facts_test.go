package store

import (
	"context"
	"reflect"
	"testing"
)

func TestWorkflowDeliveryCurrentFactsWithoutLog(t *testing.T) {
	const work = "delivery-current-facts"
	s, id, version, payloadVersion, seq := completedDeliveryFixture(t, work)
	defer s.Close()
	if err := runDeliveryCorrection(t, s, unsignedDeliveryCorrectionRequest(t, s, work, version, id, seq, payloadVersion)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"P44-delivery-assertion":  func() (any, error) { return workflowDeliveryAssertionRead(ctx, s.db, work) },
		"P45-delivery-correction": func() (any, error) { return workflowDeliveryCorrectionRead(ctx, s.db, work, id) },
		"P47-completion-warnings": func() (any, error) { return ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: work}) },
		"P51-completion-verdict":  func() (any, error) { return ReadWorkflowVerdict(ctx, s, work) },
	})
}

func TestWorkflowDeliveryCurrentFactsMigrationBackfill(t *testing.T) {
	const work = "delivery-current-backfill"
	s := completedDeliveryFixtureWithAssertions(t, work, 2)
	defer s.Close()
	id, seq, payloadVersion := deliveryAssertionEvent(t, s, work, 0)
	// This migration fixture's later assertion exists only in the legacy log.
	// Backfill must select it even though its wall-clock time is earlier.
	repopulateWorkflowCurrentFactsForTest(t, s)
	assertion, err := workflowDeliveryAssertionRead(context.Background(), s.db, work)
	if err != nil || assertion == nil || assertion.EventID != id || assertion.Seq != seq {
		t.Fatalf("backfill selected the wrong assertion: %+v, %v", assertion, err)
	}
	if err := runDeliveryCorrection(t, s, unsignedDeliveryCorrectionRequest(t, s, work, readWorkVersion(t, s, work), id, seq, payloadVersion)); err != nil {
		t.Fatal(err)
	}
	queries := []string{
		`SELECT json_array(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,CAST(payload AS TEXT),correction_id,correction_occurred_at,CAST(correction_payload AS TEXT)) FROM workflow_delivery_state ORDER BY work_id`,
		`SELECT json_array(work_id,CAST(completion_payload AS TEXT),completion_occurred_at) FROM workflow_instances ORDER BY work_id`,
	}
	want := workerFactsQueryRows(t, s, queries)
	repopulateWorkflowCurrentFactsForTest(t, s)
	if _, err := s.db.Exec(`ALTER TABLE domain_events RENAME TO domain_events_hidden`); err != nil {
		t.Fatal(err)
	}
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration changed current delivery facts:\ngot %v\nwant %v", got, want)
	}
}
