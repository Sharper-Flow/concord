package store

import "context"

func prepareTerminalTestContext(ctx context.Context, s *Store, workID, action string) (context.Context, error) {
	if action == "approve_contract" || action == "supersede_contract" || action == "complete" {
		return s.EstablishKnowledgeSourceSetProof(ctx, workID)
	}
	return ctx, nil
}
