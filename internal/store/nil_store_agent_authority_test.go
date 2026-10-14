package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreAgentAuthorityMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"TrustedClient", func(s *Store) error {
			_, err := s.TrustedClient(ctx, "client-1")
			return err
		}},
		{"TrustedClientWithKey", func(s *Store) error {
			_, _, err := s.TrustedClientWithKey(ctx, "client-1")
			return err
		}},
	})
}
