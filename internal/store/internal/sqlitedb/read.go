package sqlitedb

import (
	"context"
	"database/sql"
)

// BeginTxSource admits a pool or a held connection without borrowing another
// connection for an existing snapshot.
type BeginTxSource interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// BeginRead opens a deferred snapshot even on an immediate-write DSN.
func BeginRead(ctx context.Context, db BeginTxSource) (*sql.Tx, error) {
	return db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}
