package store

import (
	"context"
	"testing"
)

func TestUnopenedStoreKnowledgeMethods(t *testing.T) {
	ctx := context.Background()
	assertUnopenedStoreTypedFailure(t, []nilStoreCase{
		{"KnowledgeIndexWatermark", func(s *Store) error {
			_, err := s.KnowledgeIndexWatermark(ctx, "prod-1", "loc-1", "refs/heads/main")
			return err
		}},
		{"QueryLawConflictsAtHome", func(s *Store) error {
			_, err := s.QueryLawConflictsAtHome(ctx, "proj-1", "loc-1", []string{"law-1"})
			return err
		}},
		{"CheckMandatedLawsAtHome", func(s *Store) error {
			return s.CheckMandatedLawsAtHome(ctx, "proj-1", "loc-1", []string{"law-1"}, nil, false)
		}},
		{"ResolveKnowledgeQueryHome", func(s *Store) error {
			_, err := s.ResolveKnowledgeQueryHome(ctx, "", "", KnowledgeHome{}, "knowledge_home")
			return err
		}},
		{"RebuildKnowledgeIndex", func(s *Store) error {
			return s.RebuildKnowledgeIndex(ctx, KnowledgeHome{})
		}},
	})
}
