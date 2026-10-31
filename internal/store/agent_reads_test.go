package store

import (
	"context"
	"strings"
	"testing"
)

func TestStoreCursorRejectsTamperingAndBindingMismatch(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()

	want := SignedCursor{Version: 1, Tool: "concord_work_browse", Operation: "list", Scope: "product|project", Filter: "filters", Detail: "summary", Order: "priority", Source: "7", Last: "work-a", Inner: "inner-cursor"}
	token, err := s.EncodeCursor(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.DecodeCursor(context.Background(), token, want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cursor=%+v want=%+v", got, want)
	}

	_, err = s.DecodeCursor(context.Background(), token, SignedCursor{Tool: "concord_work_trace", Operation: "history", Scope: want.Scope, Filter: want.Filter, Detail: want.Detail, Order: want.Order})
	assertFailureKind(t, err, KindInvalidCursor)

	tampered := "B" + token[1:]
	if strings.HasPrefix(token, "B") {
		tampered = "A" + token[1:]
	}
	_, err = s.DecodeCursor(context.Background(), tampered, want)
	assertFailureKind(t, err, KindInvalidCursor)
}

func TestReadWorkItemSummaryNotFoundIsTyped(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	defer s.Close()

	got, err := s.ReadWorkItemSummary(context.Background(), "blocker")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "task" {
		t.Fatalf("kind=%q", got.Kind)
	}
	if got.Title != "blocker" {
		t.Fatalf("title=%q", got.Title)
	}
	_, err = s.ReadWorkItemSummary(context.Background(), "missing-initiative")
	assertFailureKind(t, err, KindProjectionNotFound)
}
