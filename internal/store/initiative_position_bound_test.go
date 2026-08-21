package store

import (
	"context"
	"testing"
	"time"
)

// CD-0042 raw-event surface: the agent payload schema bounds entry positions
// at 1000, but the event fold is the authority boundary, so the bound must be
// enforced where the event folds — otherwise a hand-authored event stores a
// position inside the reorder staging band (position+1000000) and a later
// reorder collides on the unique index, misclassified as retryable unavailability.
func TestInitiativeEntryPositionIsBoundedAtFoldTime(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedInitiativeForNarrative(t, s)

	atBound, err := InitiativeEntryEvent("pos-1000", "initiative_entry.added", "initiative", InitiativeEntry{InitiativeWorkID: "initiative", ChildWorkID: "task", Position: 1000, Required: true}, "operator", time.Unix(4, 0).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{atBound}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err != nil {
		t.Fatalf("position 1000 is the accepted surface bound and must fold: %v", err)
	}

	overSecond := openTemp(t)
	seedInitiativeForNarrative(t, overSecond)
	over, err := InitiativeEntryEvent("pos-1001", "initiative_entry.added", "initiative", InitiativeEntry{InitiativeWorkID: "initiative", ChildWorkID: "task", Position: 1001, Required: true}, "operator", time.Unix(4, 0).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	err = ApplyOperation(ctx, overSecond, Operation{Events: []Event{over}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}})
	assertFailureKind(t, err, KindInvalidPayload)

	reordered, err := InitiativeEntryEvent("pos-reorder-over", "initiative_entry.reordered", "initiative", InitiativeEntry{InitiativeWorkID: "initiative", ChildWorkID: "task", Position: 1001}, "operator", time.Unix(4, 1).UTC(), 2)
	if err != nil {
		t.Fatal(err)
	}
	err = ApplyOperation(ctx, overSecond, Operation{Events: []Event{reordered}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}})
	assertFailureKind(t, err, KindInvalidPayload)
}
