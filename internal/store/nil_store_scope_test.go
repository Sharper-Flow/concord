package store

import (
	"context"
	"testing"
	"time"
)

func TestUnopenedStoreScopeMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"ResourceClaims", func(s *Store) error {
			_, err := s.ResourceClaims(ctx, "key-1", "prod-1", 10)
			return err
		}},
		{"BlockedSessions", func(s *Store) error {
			_, err := s.BlockedSessions(ctx, time.Time{}, nil, 10)
			return err
		}},
	})
}
