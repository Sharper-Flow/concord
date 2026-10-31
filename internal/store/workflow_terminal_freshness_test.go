package store

import "context"

// prepareTerminalTestContext mirrors the production registry-proof
// preparation: the actions whose owning boundary always reads the verified
// knowledge sources carry the pre-transaction proof, and every other action
// carries it on the store's own demand rule — an active contract that binds
// law or Domains verifies before its consequential transaction opens
// (prepareContractLawProof).
func prepareTerminalTestContext(ctx context.Context, s *Store, workID, action string) (context.Context, error) {
	switch action {
	case "approve_contract", "supersede_contract", "complete", "record_work_context", "confirm_premise":
		return s.EstablishKnowledgeSourceSetProof(ctx, workID)
	}
	return s.prepareContractLawProof(ctx, workID)
}
