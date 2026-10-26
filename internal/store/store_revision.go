package store

import (
	"context"
	"database/sql"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// Revision returns the publication revision used by read metadata and cursors.
// Write admission may advance it even when a transaction changes no other row:
// equal revisions mean unchanged state; unequal revisions mean possibly changed.
func (s *Store) Revision(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "store_revision", "database is not open", true, "open the authority database")
	}
	return storeRevision(ctx, s.db)
}

func storeRevision(ctx context.Context, q queryer) (int64, error) {
	revision, err := sqlitedb.Revision(ctx, q)
	return revision, mechanismFailure(err)
}

func advanceStoreRevision(ctx context.Context, tx *sql.Tx) error {
	return mechanismFailure(sqlitedb.AdvanceRevision(ctx, tx))
}
