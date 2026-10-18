package store

import "context"

// checkMandatedLawsAtHome runs the production law boundary for one resolved
// home: the pre-transaction source-set proof, then the transaction-scoped
// check.
func checkMandatedLawsAtHome(ctx context.Context, s *Store, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	ctx, err := s.establishKnowledgeSourceSetProofAtHome(ctx, homeProjectID, homeLocatorID)
	if err != nil {
		return err
	}
	return s.Transact(ctx, func(tx *Transaction) error {
		raw, err := transactionSQL(tx, "check_mandated_laws")
		if err != nil {
			return err
		}
		return checkMandatedLawsTxAtHome(ctx, raw, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
	})
}
