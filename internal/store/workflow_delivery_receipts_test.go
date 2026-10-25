package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorkflowDeliveryReceiptFlagBackfillParity(t *testing.T) {
	const work = "delivery-receipt-backfill"
	s, target, _, _, _ := completedDeliveryFixture(t, work)
	defer s.Close()
	var asserted bool
	if err := s.db.QueryRow(`SELECT delivery_assertion FROM effect_receipts WHERE event_id=?`, target).Scan(&asserted); err != nil || !asserted {
		t.Fatalf("live assertion receipt flag = %v,%v; want set by appendEvent", asserted, err)
	}
	// Keep the complete live delivery journey above. This independent corpus
	// also pins negative populations, rather than trusting two uses of the
	// delivery classifier to agree with each other.
	corpus := []struct {
		id, kind, payload string
		subject           SubjectType
		asserted          bool
	}{
		{"direct", WorkflowActionCompleted, `{"action_id":"record_delivery","delivery_artifact":"artifact:one","delivery_state":"asserted"}`, SubjectWorkItem, true},
		{"combined", WorkflowActionCompleted, `{"action_id":"accept_worker_result","delivery_artifact":"artifact:two","delivery_state":"asserted"}`, SubjectWorkItem, true},
		{"fields-only", WorkflowActionCompleted, `{"delivery_artifact":"artifact:three","delivery_state":"asserted"}`, SubjectWorkItem, true},
		{"no-artifact", WorkflowActionCompleted, `{"delivery_state":"asserted"}`, SubjectWorkItem, false},
		{"no-state", WorkflowActionCompleted, `{"delivery_artifact":"artifact:one"}`, SubjectWorkItem, false},
		{"not-asserted", WorkflowActionCompleted, `{"delivery_artifact":"artifact:one","delivery_state":"pending"}`, SubjectWorkItem, false},
		{"wrong-kind", WorkflowDeliveryCorrected, `{"delivery_artifact":"artifact:one","delivery_state":"asserted"}`, SubjectWorkItem, false},
		{"wrong-subject", WorkflowActionCompleted, `{"delivery_artifact":"artifact:one","delivery_state":"asserted"}`, SubjectSession, false},
	}
	var events []Event
	for i, row := range corpus {
		events = append(events, Event{EventID: "delivery-flag:" + row.id, Kind: row.kind, SubjectType: row.subject, SubjectID: work, Actor: "actor:delivery-corpus", OccurredAt: time.Unix(int64(100-i), 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(row.payload)})
	}
	fixture := historicalFactCorpus{
		events:  events,
		parents: func(*testing.T, *Store, string) {},
		// Only append-owned receipt classification is under test here. Full
		// workflow admission is exercised by completedDeliveryFixture above.
		write: func(context.Context, *sql.Tx, Event) error { return nil },
	}
	current := fixture.current(t)
	upgraded := fixture.prefix(t, 144)
	db := upgraded.db
	before := histEventRows(t, upgraded)
	for _, row := range before {
		t.Logf("migration147 source seq=%d id=%s kind=%s actor=%s time=%s digest=%s", row.Seq, row.EventID, row.Kind, row.Actor, row.OccurredAt, historicalFixturePayloadDigest(t, row.Payload))
	}
	advanceHistoricalPrefix(t, db, 147)
	if got := histEventRows(t, &Store{db: db}); !reflect.DeepEqual(got, before) {
		t.Fatal("delivery flag migration changed original source bytes or headers")
	}
	assertFlags := func(label string, store *Store) {
		t.Helper()
		if got := countRows(t, store, "effect_receipts"); got != len(corpus) {
			t.Fatalf("%s receipt population = %d, want exactly %d corpus effects", label, got, len(corpus))
		}
		for i, event := range events {
			var got bool
			if err := store.db.QueryRow(`SELECT delivery_assertion FROM effect_receipts WHERE event_id=?`, event.EventID).Scan(&got); err != nil || got != corpus[i].asserted {
				t.Fatalf("%s delivery selection %s = %v,%v; want %v", label, event.EventID, got, err, corpus[i].asserted)
			}
		}
	}
	assertFlags("fresh append", current)
	assertFlags("migration147", upgraded)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	assertHistoricalReceipts(t, db, before)
	assertEventLogAbsent(t, upgraded)
	assertFlags("upgraded", upgraded)
	queries := []string{`SELECT json_array(event_id,delivery_assertion) FROM effect_receipts ORDER BY effect_seq`}
	want := workerFactsQueryRows(t, current, queries)
	if got := workerFactsQueryRows(t, upgraded, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("receipt flags changed on backfill: got %v, want %v", got, want)
	}
	mutant := fixture.prefix(t, 146)
	if err := applyMigration(t.Context(), deliveryAssertionSelectionMutant{mutant.db}, migrations[146]); err != nil {
		t.Fatalf("delivery selection mutant failed before comparison: %v", err)
	}
	if got := workerFactsQueryRows(t, mutant, queries); reflect.DeepEqual(got, want) {
		t.Fatal("migration147 action-name restriction escaped the delivery flag oracle")
	} else {
		t.Logf("RED migration147 receipt flags: got %v, want %v", got, want)
	}
}

// Only the fixture-local migration query changes. Schema, manifest and the
// production classifier stay untouched; the fields-only rule must still bite.
type deliveryAssertionSelectionMutant struct{ migrationRunner }

func (q deliveryAssertionSelectionMutant) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.HasPrefix(query, "SELECT event_id,subject_type,kind,payload FROM domain_events WHERE") {
		query = strings.Replace(query, "ORDER BY seq", "AND json_extract(payload,'$.action_id')='record_delivery' ORDER BY seq", 1)
	}
	return q.migrationRunner.QueryContext(ctx, query, args...)
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
