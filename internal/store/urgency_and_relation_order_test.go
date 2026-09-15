package store

import (
	"context"
	"testing"
)

// workCreatedEventWithUrgency creates a work.created v2 event with a declared
// urgency and priority. Used by CD-0018 urgency tests.
func workCreatedEventWithUrgency(id, eventID, urgency string, priority int64) Event {
	event := operationEvent(eventID, "work.created", SubjectWorkItem, id, map[string]any{
		"work_kind": "task", "title": id, "priority": priority, "urgency": urgency,
	})
	event.PayloadVersion = 2
	return event
}

// seedWorkWithUrgency seeds a work item at creation time with a declared urgency
// and priority, plus project membership, leaving it at version 2 in 'needed'.
func seedWorkWithUrgency(t *testing.T, s *Store, id, urgency string, priority int64) {
	t.Helper()
	seedProductAndProject(t, s)
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			workCreatedEventWithUrgency(id, "create-"+id, urgency, priority),
			operationEvent("membership-"+id, "work_project.added", SubjectWorkItem, id, map[string]any{
				"work_id": id, "project_id": "project", "role": "secondary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0},
	}); err != nil {
		t.Fatalf("seed work %s: %v", id, err)
	}
}

// seedProductAndProject ensures the test product and project exist once.
func seedProductAndProject(t *testing.T, s *Store) {
	t.Helper()
	var projectCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM projects`).Scan(&projectCount); err != nil {
		t.Fatal(err)
	}
	if projectCount > 0 {
		return
	}
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("product", "create-product"),
			projectCreatedEvent("project", "create-project"),
			operationEvent("product-project", "product_project.added", SubjectProduct, "product", map[string]any{
				"product_id": "product", "project_id": "project", "role": "primary", "reason": "test",
				"expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{
			VersionRef(SubjectProduct, "product"): 0,
			VersionRef(SubjectProject, "project"): 0,
		},
	}); err != nil {
		t.Fatalf("create test product+project: %v", err)
	}
}

func TestCD0018UrgencyRoundTripPersistence(t *testing.T) {
	s := openTemp(t)
	seedWorkWithUrgency(t, s, "expedite-item", "expedite", 10)

	var urgency string
	if err := s.DatabaseForTesting().QueryRow(`SELECT urgency FROM work_items WHERE id=?`, "expedite-item").Scan(&urgency); err != nil {
		t.Fatalf("select urgency: %v", err)
	}
	if urgency != "expedite" {
		t.Fatalf("urgency = %q, want %q", urgency, "expedite")
	}
}

func TestCD0018UrgencyDefaultsToStandard(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "default-item")

	var urgency string
	if err := s.DatabaseForTesting().QueryRow(`SELECT urgency FROM work_items WHERE id=?`, "default-item").Scan(&urgency); err != nil {
		t.Fatalf("select urgency: %v", err)
	}
	if urgency != "standard" {
		t.Fatalf("default urgency = %q, want %q", urgency, "standard")
	}
}

func TestCD0018UrgencyInvalidValueRejected(t *testing.T) {
	s := openTemp(t)
	event := operationEvent("bad-urgency", "work.created", SubjectWorkItem, "bad", map[string]any{
		"work_kind": "task", "title": "bad", "priority": 10, "urgency": "critical",
	})
	event.PayloadVersion = 2
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
	assertFailureKind(t, err, KindInvalidPayload)
}

func TestCD0018UrgencyOrderingExpediteAboveStandard(t *testing.T) {
	// An expedite item with priority=50 must sort above a standard item with
	// priority=1. Expedite wins regardless of priority.
	s := openTemp(t)
	seedWorkWithUrgency(t, s, "std-high-prio", "standard", 1)
	seedWorkWithUrgency(t, s, "exp-low-prio", "expedite", 50)

	result, err := s.QueryQ5(context.Background(), Q5Request{Product: "product", Limit: 10})
	if err != nil {
		t.Fatalf("Q5: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 ready items, got %d", len(result.Items))
	}
	if result.Items[0].ID != "exp-low-prio" {
		t.Fatalf("expected expedite item first despite worse priority; got %s (urgency=%s, priority=%d)",
			result.Items[0].ID, result.Items[0].Urgency, result.Items[0].Priority)
	}
	if result.Items[0].Urgency != "expedite" {
		t.Fatalf("first item urgency = %q, want expedite", result.Items[0].Urgency)
	}
	if result.Items[1].ID != "std-high-prio" {
		t.Fatalf("expected standard item second; got %s", result.Items[1].ID)
	}
}

func TestCD0018UrgencyWithinBandPriorityOrders(t *testing.T) {
	// Three expedite items with priorities 30, 10, 20: within the band, lower
	// priority still sorts first.
	s := openTemp(t)
	seedWorkWithUrgency(t, s, "exp-30", "expedite", 30)
	seedWorkWithUrgency(t, s, "exp-10", "expedite", 10)
	seedWorkWithUrgency(t, s, "exp-20", "expedite", 20)

	result, err := s.QueryQ5(context.Background(), Q5Request{Product: "product", Limit: 10})
	if err != nil {
		t.Fatalf("Q5: %v", err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("expected 3 ready items, got %d", len(result.Items))
	}
	expected := []string{"exp-10", "exp-20", "exp-30"}
	for i, want := range expected {
		if result.Items[i].ID != want {
			t.Fatalf("item %d = %s, want %s (within-band priority ordering)", i, result.Items[i].ID, want)
		}
	}
}

func TestCD0018RaisedFromIsAcyclic(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"a", "b", "c"} {
		seedWork(t, s, id)
	}
	// a raised_from b, b raised_from c — acyclic chain is fine
	if err := applyWorkEvent(t, s, relationAddedEvent("ab", "raised_from", "a", "b", 2, 3), nil); err != nil {
		t.Fatalf("add a→b raised_from: %v", err)
	}
	if err := applyWorkEvent(t, s, relationAddedEvent("bc", "raised_from", "b", "c", 2, 3), nil); err != nil {
		t.Fatalf("add b→c raised_from: %v", err)
	}
	// c raised_from a would create a cycle — must be rejected
	assertFailureKind(t, applyWorkEvent(t, s, relationAddedEvent("cycle", "raised_from", "c", "a", 2, 3), nil), KindCycleDetected)
}

func TestCD0018RaisedFromDoesNotExcludeFromReady(t *testing.T) {
	// Unlike blocks, a raised_from edge must NOT remove the target from Q5.
	s := openTemp(t)
	seedWork(t, s, "parent-work")
	seedWork(t, s, "child-work")

	if err := applyWorkEvent(t, s, relationAddedEvent("rf", "raised_from", "child-work", "parent-work", 2, 3), nil); err != nil {
		t.Fatalf("add raised_from: %v", err)
	}

	result, err := s.QueryQ5(context.Background(), Q5Request{Product: "product", Limit: 10})
	if err != nil {
		t.Fatalf("Q5: %v", err)
	}
	ids := make(map[string]bool)
	for _, item := range result.Items {
		ids[item.ID] = true
	}
	if !ids["parent-work"] {
		t.Error("parent-work missing from ready: raised_from must not exclude the target")
	}
	if !ids["child-work"] {
		t.Error("child-work missing from ready: raised_from source must remain ready")
	}
}

func TestCD0018RaisedFromIsOrdinaryRelation(t *testing.T) {
	// raised_from is created and removed by the standard relation operations,
	// just like parent or blocks. No special-casing.
	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")

	if err := applyWorkEvent(t, s, relationAddedEvent("add", "raised_from", "a", "b", 2, 3), nil); err != nil {
		t.Fatalf("add raised_from: %v", err)
	}
	assertTableCount(t, s, "relations", 1)

	if err := applyWorkEvent(t, s, relationRemovedEvent("remove", "raised_from", "a", "b", 3, 4), nil); err != nil {
		t.Fatalf("remove raised_from: %v", err)
	}
	assertTableCount(t, s, "relations", 0)
}
