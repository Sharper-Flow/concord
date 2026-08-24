package store

import (
	"context"
	"testing"
	"time"
)

func TestStoreClockControlsDurableEventTimestamp(t *testing.T) {
	s := openTemp(t)
	want := time.Date(2042, 12, 31, 23, 59, 59, 123456789, time.FixedZone("test", 2*60*60))
	s.Clock = func() time.Time { return want }

	result, err := s.CreateProductWithProject(context.Background(), ProductCreation{
		ProductID: "clock-product", DisplayName: "Clock Product", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "clock-project", ProjectDisplayName: "Clock Project", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	var occurredAt string
	if err := s.db.QueryRow(`SELECT occurred_at FROM domain_events WHERE event_id=?`, result.EventIDs[0]).Scan(&occurredAt); err != nil {
		t.Fatal(err)
	}
	if occurredAt != want.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("occurred_at = %q, want %q", occurredAt, want.UTC().Format(time.RFC3339Nano))
	}
}
