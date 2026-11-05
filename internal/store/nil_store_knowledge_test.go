package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreKnowledgeMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"EstablishKnowledgeSourceSetProof", func(s *Store) error {
			_, err := s.EstablishKnowledgeSourceSetProof(ctx, "work-1")
			return err
		}},
	})
}
