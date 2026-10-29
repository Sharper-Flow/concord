package agent

import (
	"context"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func domainEventCountForTest(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var count int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT last FROM store_sequences WHERE name='effect'`).Scan(&count); err != nil {
		t.Fatalf("count domain events: %v", err)
	}
	return count
}
