package agent

import (
	"context"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// Shared test helpers for agent-package tests.

func productVersionForTest(t *testing.T, s *store.Store, productID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT version FROM products WHERE id = ?`, productID).Scan(&version); err != nil {
		t.Fatalf("read product version: %v", err)
	}
	return version
}

func domainEventCountForTest(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var count int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT last FROM store_sequences WHERE name='effect'`).Scan(&count); err != nil {
		t.Fatalf("count domain events: %v", err)
	}
	return count
}
