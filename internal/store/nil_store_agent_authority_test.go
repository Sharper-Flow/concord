package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreAgentAuthorityMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"TrustedClientWithKey", func(s *Store) error {
			_, _, err := s.TrustedClientWithKey(ctx, "client-1")
			return err
		}},
	})
}
