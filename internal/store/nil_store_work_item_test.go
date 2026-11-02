package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreWorkItemMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"ReadWorkItemSummary", func(s *Store) error {
			_, err := s.ReadWorkItemSummary(ctx, "work-1")
			return err
		}},
	})
}
