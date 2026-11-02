package store

import (
	"context"
	"testing"
	"time"
)

func TestUnopenedStoreWorkItemMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"ReadWorkItemSummary", func(s *Store) error {
			_, err := s.ReadWorkItemSummary(ctx, "work-1")
			return err
		}},
		{"ExternalObservationsForWork", func(s *Store) error {
			_, err := s.ExternalObservationsForWork(ctx, "work-1", time.Time{}, 10)
			return err
		}},
	})
}
