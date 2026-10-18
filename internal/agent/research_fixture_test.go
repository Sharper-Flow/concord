package agent

import (
	"context"

	"github.com/sharper-flow/concord/internal/store"
)

// The research fixtures write through the transaction-scoped cores that
// production reaches; each call runs in its own store transaction.

func createResearchPack(ctx context.Context, s *store.Store, req store.CreateResearchPackRequest) (out store.ResearchPack, err error) {
	err = s.Transact(ctx, func(tx *store.Transaction) error {
		out, err = store.CreateResearchPackWithinTx(ctx, tx, req)
		return err
	})
	return out, err
}

func appendResearchRevision(ctx context.Context, s *store.Store, req store.AppendResearchRevisionRequest) error {
	return s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.AppendResearchRevisionWithinTx(ctx, tx, req)
		return err
	})
}

func recordResearchFinding(ctx context.Context, s *store.Store, req store.ResearchFindingRequest) error {
	return s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.RecordResearchFindingWithinTx(ctx, tx, req)
		return err
	})
}

func recordResearchSource(ctx context.Context, s *store.Store, req store.ResearchSourceRequest) error {
	return s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.RecordResearchSourceWithinTx(ctx, tx, req)
		return err
	})
}

func setResearchFreshness(ctx context.Context, s *store.Store, req store.SetResearchFreshnessRequest) error {
	return s.Transact(ctx, func(tx *store.Transaction) error {
		return store.SetResearchFreshnessWithinTx(ctx, tx, req)
	})
}
