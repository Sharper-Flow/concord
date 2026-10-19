package store

import "context"

// The research fixtures write through the transaction-scoped cores that
// production reaches; each call runs in its own store transaction.

func createResearchPack(ctx context.Context, s *Store, req CreateResearchPackRequest) (out ResearchPack, err error) {
	err = s.Transact(ctx, func(tx *Transaction) error {
		out, err = CreateResearchPackWithinTx(ctx, tx, req)
		return err
	})
	return out, err
}

func appendResearchRevision(ctx context.Context, s *Store, req AppendResearchRevisionRequest) (out ResearchRevision, err error) {
	err = s.Transact(ctx, func(tx *Transaction) error {
		out, err = AppendResearchRevisionWithinTx(ctx, tx, req)
		return err
	})
	return out, err
}

func recordResearchFinding(ctx context.Context, s *Store, req ResearchFindingRequest) (out ResearchFinding, err error) {
	err = s.Transact(ctx, func(tx *Transaction) error {
		out, err = RecordResearchFindingWithinTx(ctx, tx, req)
		return err
	})
	return out, err
}

func recordResearchSource(ctx context.Context, s *Store, req ResearchSourceRequest) (out ResearchSource, err error) {
	err = s.Transact(ctx, func(tx *Transaction) error {
		out, err = RecordResearchSourceWithinTx(ctx, tx, req)
		return err
	})
	return out, err
}

func setResearchFreshness(ctx context.Context, s *Store, req SetResearchFreshnessRequest) error {
	return s.Transact(ctx, func(tx *Transaction) error {
		return SetResearchFreshnessWithinTx(ctx, tx, req)
	})
}
