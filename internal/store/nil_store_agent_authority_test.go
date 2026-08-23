package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreAgentAuthorityMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"TrustedClientForGrant", func(s *Store) error {
			_, _, err := s.TrustedClientForGrant(ctx, "client-1")
			return err
		}},
		{"PersistGrant", func(s *Store) error {
			return s.PersistGrant(ctx, GrantInsert{})
		}},
		{"Grant", func(s *Store) error {
			_, err := s.Grant(ctx, nil)
			return err
		}},
		{"RevokeGrant", func(s *Store) error {
			return s.RevokeGrant(ctx, nil, "grant-1", "now")
		}},
		{"RevokeApproval", func(s *Store) error {
			return s.RevokeApproval(ctx, "approval-1", "now")
		}},
		{"RevokeApprovalChallenge", func(s *Store) error {
			return s.RevokeApprovalChallenge(ctx, "challenge-1")
		}},
	})
}
