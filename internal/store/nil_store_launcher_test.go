package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreLauncherMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"SyncDurable", func(s *Store) error {
			return s.SyncDurable(ctx)
		}},
	})
}
