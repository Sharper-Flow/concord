package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreLauncherMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"TransactDurable", func(s *Store) error {
			return s.TransactDurable(ctx, func(*Transaction) error { return nil })
		}},
	})
}
