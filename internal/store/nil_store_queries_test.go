package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreQueryMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"QueryQ9", func(s *Store) error {
			_, err := s.QueryQ9(ctx, Q9Request{})
			return err
		}},
		{"QueryQ10", func(s *Store) error {
			_, err := s.QueryQ10(ctx, Q10Request{Work: "work-1"})
			return err
		}},
	})
}
