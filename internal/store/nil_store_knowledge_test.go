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
		{"EstablishKnowledgeSourceSetProofForProject", func(s *Store) error {
			_, err := s.EstablishKnowledgeSourceSetProofForProject(ctx, "project-1")
			return err
		}},
		{"WorkflowInstanceDefinition", func(s *Store) error {
			_, _, err := s.WorkflowInstanceDefinition(ctx, "work-1")
			return err
		}},
		{"ResolveKnowledgeQueryHome", func(s *Store) error {
			_, err := s.ResolveKnowledgeQueryHome(ctx, "", "", KnowledgeHome{}, "knowledge_home")
			return err
		}},
	})
}
