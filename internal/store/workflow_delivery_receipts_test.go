package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWorkflowDeliveryReceiptFlagBackfillParity(t *testing.T) {
	const work = "delivery-receipt-backfill"
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
	// workflow admission is exercised by the live journey above.
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
		current, err := workflowDeliveryAssertionRead(t.Context(), s.db, work)
		if err != nil || current == nil || current.EventID != target || current.Seq != targetSeq || current.Artifact != deliveryCorrectionWrongPath+"/1" {
			t.Errorf("current delivery=%+v err=%v", current, err)
		}
		return current, err
	}}
	assertWorkerFactsLogBlind(t, s, checks)
}
