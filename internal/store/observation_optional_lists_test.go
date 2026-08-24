package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// refs and tags are optional on both observation anchors. A nil slice must
// reach the projection as an empty JSON array: every column that receives one
// declares json_type(...)='array', so rendering nil as null failed the CHECK.
// On the work anchor the fold reported that violation as a duplicate
// observation id, which named neither the real cause nor a usable recovery.

func TestObservationAcceptsAbsentRefsAndTags(t *testing.T) {
	ctx := context.Background()

	work := observationFixture(t)
	workEvent := Event{
		EventID: "obs-no-lists", Kind: "work.observation_recorded",
		SubjectType: SubjectWorkItem, SubjectID: "work-99", Actor: "agent",
		OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"observation_id":"obs:00000000000000aa","statement":"no lists supplied"}`),
	}
	if err := recordObservation(t, work, workEvent); err != nil {
		t.Fatalf("work observation without refs or tags: %v", err)
	}
	rows, err := work.ObservationsForWork(ctx, "work-99", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Refs) != 0 || len(rows[0].Tags) != 0 {
		t.Fatalf("work observation = %+v", rows)
	}

	domain := domainObservationFixture(t)
	domainEvent := Event{
		EventID: "domain-obs-no-lists", Kind: "domain.observation_recorded",
		SubjectType: SubjectProduct, SubjectID: "obs-product", Actor: "agent",
		OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"observation_id":"dob:00000000000000aa","product_id":"obs-product","domain_id":"obs-domain","statement":"no lists supplied"}`),
	}
	if err := foldDomainEvent(t, domain, domainEvent); err != nil {
		t.Fatalf("Domain observation without refs or tags: %v", err)
	}
	domainRows, err := domain.ObservationsForDomain(ctx, "obs-product", "obs-domain", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(domainRows) != 1 || len(domainRows[0].Refs) != 0 || len(domainRows[0].Tags) != 0 {
		t.Fatalf("Domain observation = %+v", domainRows)
	}
}

func TestMarshalStringsRendersNilAsEmptyArray(t *testing.T) {
	if got := marshalStrings(nil); got != "[]" {
		t.Fatalf("marshalStrings(nil) = %q, want []", got)
	}
	if got := marshalStrings([]string{}); got != "[]" {
		t.Fatalf("marshalStrings(empty) = %q, want []", got)
	}
	if got := marshalStrings([]string{"a"}); got != `["a"]` {
		t.Fatalf("marshalStrings([a]) = %q", got)
	}
}
