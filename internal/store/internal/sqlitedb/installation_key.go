package sqlitedb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"time"
)

// EnsureInstallationKey creates the one authority-owned cursor signing key.
// Read first: even an ignored INSERT opens a write transaction, so established
// installations must avoid it when another writer holds the lock. Idempotent
// insertion lets concurrent first opens converge on one key.
func EnsureInstallationKey(ctx context.Context, db *sql.DB, createdAt time.Time) error {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_installation_keys WHERE key_name='cursor')`).Scan(&exists); err != nil {
		return failure(Unavailable, "open", "cannot inspect the installation cursor key", true,
			"confirm the database is readable", err)
	}
	if exists == 1 {
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return failure(Unavailable, "open", "cannot create the installation cursor key", true,
			"retry once the operating system random source is available", err)
	}
	tx, err := BeginWrite(ctx, db, nil, StateWriteAdmission)
	if err != nil {
		return failure(Unavailable, "open", "cannot begin installation cursor key creation", true,
			"retry once the database is writable", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_installation_keys(key_name,key_bytes,created_at) VALUES('cursor',?,?)`, key, createdAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return failure(Unavailable, "open", "cannot persist the installation cursor key", true,
			"retry once the database is writable", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return failure(Unavailable, "open", "cannot confirm installation cursor key creation", true,
			"retry once the database is readable", err)
	}
	if inserted == 0 {
		if err := tx.Rollback(); err != nil {
			return failure(Unavailable, "open", "cannot roll back ignored installation cursor key creation", true,
				"retry once the database is writable", err)
		}
		return nil
	}
	if err := tx.Commit(); err != nil {
		return failure(Unavailable, "open", "cannot commit the installation cursor key", true,
			"retry once the database is writable", err)
	}
	return nil
}

// InstallationKey reads the authority-owned key through the caller's live
// handle. Key bytes must never be serialized into an agent response.
func InstallationKey(ctx context.Context, q Queryer) ([]byte, error) {
	var key []byte
	if err := q.QueryRowContext(ctx, `SELECT key_bytes FROM agent_installation_keys WHERE key_name='cursor'`).Scan(&key); err != nil {
		return nil, failure(Unavailable, "cursor_key", "cannot read the installation cursor key", true,
			"open a migrated authority database", err)
	}
	if len(key) != 32 {
		return nil, failure(InvariantViolation, "cursor_key", "installation cursor key has an invalid length", false,
			"rebuild the authority from its migration", nil)
	}
	return append([]byte(nil), key...), nil
}
