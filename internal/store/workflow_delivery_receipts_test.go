package store

import (
	"encoding/json"
	"testing"
)

func TestWorkflowDeliveryReceiptFlag(t *testing.T) {
	const work = "delivery-receipt-flag"
	f := qCurrentAcceptanceWithDelivery(t, work, deliveryCorrectionWrongPath)
	s := f.store
	defer s.Close()
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.run(ctx, "confirm_premise", "confirm", readWorkVersion(t, s, work), nil, nil); err != nil {
		t.Fatal(err)
	}
	completeWorkflowPlanFixture(t, s, work)
	if err := f.run(ctx, "complete", "complete", readWorkVersion(t, s, work), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
		t.Fatal(err)
	}
	target, _, _ := deliveryAssertionEvent(t, s, work, 0)
	var asserted bool
	if err := s.db.QueryRow(`SELECT delivery_assertion FROM effect_receipts WHERE event_id=?`, target).Scan(&asserted); err != nil || !asserted {
		t.Fatalf("live assertion receipt flag = %v,%v; want set by appendEvent", asserted, err)
	}
}

func TestWorkflowDeliveryAssertionClassification(t *testing.T) {
	for _, test := range []struct {
		name, kind, payload string
		subject             SubjectType
		want                bool
		malformed           bool
	}{
		{name: "direct", subject: SubjectWorkItem, kind: WorkflowActionCompleted, payload: `{"action_id":"record_delivery","delivery_artifact":"artifact:one","delivery_state":"asserted"}`, want: true},
		{name: "combined", subject: SubjectWorkItem, kind: WorkflowActionCompleted, payload: `{"action_id":"accept_worker_result","delivery_artifact":"artifact:one","delivery_state":"asserted"}`, want: true},
		{name: "fields own assertion", subject: SubjectWorkItem, kind: WorkflowActionCompleted, payload: `{"delivery_artifact":"artifact:one","delivery_state":"asserted"}`, want: true},
		{name: "no artifact", subject: SubjectWorkItem, kind: WorkflowActionCompleted, payload: `{"delivery_state":"asserted"}`},
		{name: "no assertion", subject: SubjectWorkItem, kind: WorkflowActionCompleted, payload: `{"delivery_artifact":"artifact:one"}`},
		{name: "pending", subject: SubjectWorkItem, kind: WorkflowActionCompleted, payload: `{"delivery_artifact":"artifact:one","delivery_state":"pending"}`},
		{name: "wrong kind", subject: SubjectWorkItem, kind: WorkflowDeliveryCorrected, payload: `{"delivery_artifact":"artifact:one","delivery_state":"asserted"}`},
		{name: "wrong subject", subject: SubjectSession, kind: WorkflowActionCompleted, payload: `{"delivery_artifact":"artifact:one","delivery_state":"asserted"}`},
		{name: "malformed fields", subject: SubjectWorkItem, kind: WorkflowActionCompleted, payload: `{"delivery_artifact":42,"delivery_state":"asserted"}`, malformed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := workflowEventDeliveryAssertion(Event{Kind: test.kind, SubjectType: test.subject, Payload: json.RawMessage(test.payload)})
			if got != test.want || (err != nil) != test.malformed {
				t.Fatalf("assertion = %v,%v; want %v, malformed %v", got, err, test.want, test.malformed)
			}
		})
	}
}

func TestWorkflowCurrentDeliveryWithoutLog(t *testing.T) {
	const work = "delivery-receipt-target"
	s := completedDeliveryFixtureWithAssertions(t, work, 2)
	defer s.Close()
	target, targetSeq, _ := deliveryAssertionEvent(t, s, work, 0)
	checks := map[string]func() (any, error){"current": func() (any, error) {
		var eventID, artifact string
		var seq int64
		err := s.db.QueryRowContext(t.Context(), `SELECT assertion_id,assertion_seq,json_extract(payload,'$.delivery_artifact') FROM workflow_delivery_state WHERE work_id=?`, work).Scan(&eventID, &seq, &artifact)
		if err != nil || eventID != target || seq != targetSeq || artifact != deliveryCorrectionWrongPath+"/1" {
			t.Errorf("current delivery=%q/%d/%q err=%v", eventID, seq, artifact, err)
		}
		return []any{eventID, seq, artifact}, err
	}}
	assertWorkerFactsLogBlind(t, s, checks)
}
