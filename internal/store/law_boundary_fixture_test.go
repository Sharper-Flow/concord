package store

import "context"

func establishKnowledgeSourceSetProofForProject(ctx context.Context, s *Store, projectID string) (context.Context, error) {
	project, locator, err := workflowLawHomeForProject(ctx, s.db, projectID)
	if err != nil {
		return ctx, nil
	}
	return s.establishKnowledgeSourceSetProofAtHomes(ctx, project, locator, nil)
}

// checkMandatedLawsAtHome runs the production law boundary for the fixture
// home (Project "p", locator "l"): the pre-transaction source-set proof, then
// the transaction-scoped check.
func checkMandatedLawsAtHome(ctx context.Context, s *Store, mandated, modified []string, allowAmendment bool) error {
	const homeProjectID, homeLocatorID = "p", "l"
	ctx, err := s.establishKnowledgeSourceSetProofAtHomes(ctx, homeProjectID, homeLocatorID, nil)
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
