package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreAgentMutationMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"LookupMutationIdempotency", func(s *Store) error {
			_, _, err := s.LookupMutationIdempotency(ctx, MutationIdempotencyKey{})
			return err
		}},
		{"AcceptedInputsDigest", func(s *Store) error {
			_, err := s.AcceptedInputsDigest(ctx, "op-1")
			return err
		}},
	})
}
