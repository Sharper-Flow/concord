package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestWorkflowDeliveryReceiptFlagBackfillParity(t *testing.T) {
	const work = "delivery-receipt-backfill"
	s, target, _, _, _ := completedDeliveryFixture(t, work)
	defer s.Close()
	queries := []string{`SELECT json_array(event_id,delivery_assertion) FROM effect_receipts ORDER BY effect_seq`}
	want := workerFactsQueryRows(t, s, queries)
	var asserted bool
	if err := s.db.QueryRow(`SELECT delivery_assertion FROM effect_receipts WHERE event_id=?`, target).Scan(&asserted); err != nil || !asserted {
		t.Fatalf("live assertion receipt flag = %v,%v; want set by appendEvent", asserted, err)
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE effect_receipts DROP COLUMN delivery_assertion`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version == 147 {
			if err := applyMigration(t.Context(), tx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt flags changed on backfill: got %v, want %v", got, want)
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
		{name: "wrong kind", subject: SubjectWorkItem, kind: WorkflowDeliveryCorrected, payload: `{"delivery_artifact":"artifact:one","delivery_state":"asserted"}`},
		{name: "wrong subject", subject: SubjectSession, kind: WorkflowActionCompleted, payload: `{"delivery_artifact":"artifact:one","delivery_state":"asserted"}`},
		{name: "malformed fields", subject: SubjectWorkItem, kind: WorkflowActionCompleted, payload: `{"delivery_artifact":42,"delivery_state":"asserted"}`, malformed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, got, err := workflowEventDeliveryAssertion(Event{Kind: test.kind, SubjectType: test.subject, Payload: json.RawMessage(test.payload)})
			if got != test.want || (err != nil) != test.malformed {
				t.Fatalf("assertion = %v,%v; want %v, malformed %v", got, err, test.want, test.malformed)
			}
		})
	}
}

func TestWorkflowDeliveryTargetClassificationWithoutLog(t *testing.T) {
	const work = "delivery-receipt-target"
	s := completedDeliveryFixtureWithAssertions(t, work, 2)
	defer s.Close()
	target, targetSeq, targetVersion := deliveryAssertionEvent(t, s, work, 0)
	older, _, _ := deliveryAssertionEvent(t, s, work, 1)
	var other string
	if err := s.db.QueryRow(`SELECT event_id FROM effect_receipts WHERE subject_id=? AND kind='workflow.action_completed' AND delivery_assertion=0 ORDER BY effect_seq DESC LIMIT 1`, work).Scan(&other); err != nil {
		t.Fatal(err)
	}
	checks := map[string]func() (any, error){}
	for name, test := range map[string]struct{ id, work, failure string }{
		"assertion":     {id: target, work: work},
		"not-assertion": {id: other, work: work, failure: "not a recorded delivery assertion"},
		"not-recorded":  {id: "missing-assertion", work: work, failure: "target event is not recorded"},
		"not-current":   {id: older, work: work, failure: "not the current delivery assertion"},
		"other-work":    {id: target, work: "other-work", failure: "not a recorded delivery assertion"},
	} {
		checks[name] = func() (any, error) {
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback()
			seq, version, artifact, readErr := workflowDeliveryAssertionEventTx(t.Context(), tx, test.work, test.id)
			if test.failure == "" {
				if readErr != nil || seq != targetSeq || version != targetVersion || artifact != deliveryCorrectionWrongPath+"/1" {
					t.Errorf("current assertion = %d,%d,%q,%v", seq, version, artifact, readErr)
				}
			} else if readErr == nil || !strings.Contains(readErr.Error(), test.failure) {
				t.Errorf("%s refusal = %v; want %s", name, readErr, test.failure)
			}
			return struct {
				Seq               int64
				Version           int
				Artifact, Failure string
			}{seq, version, artifact, fmt.Sprint(readErr)}, nil
		}
	}
	assertWorkerFactsLogBlind(t, s, checks)
}
