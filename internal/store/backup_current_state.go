package store

import (
	"context"
	"database/sql"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func backupCurrentStateDigest(ctx context.Context, db *sql.DB) (string, error) {
	digest, err := sqlitedb.CurrentStateDigest(ctx, db)
	return digest, mechanismFailure(err)
}
