package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// migration is one ordered, immutable step of the schema manifest. A recorded
// migration is never edited: its checksum is compared on every open, so an
// edited step is reported instead of silently diverging from the live schema.
type migration struct {
	Version int
	Name    string
	SQL     string
}

// migrations is the ordered manifest. Append new steps; never rewrite applied
// ones.
var migrations = []migration{
	{
		Version: 1,
		Name:    "domain_events",
		SQL: `
CREATE TABLE domain_events (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        TEXT    NOT NULL UNIQUE,
    kind            TEXT    NOT NULL,
    subject_type    TEXT    NOT NULL,
    subject_id      TEXT    NOT NULL,
    actor           TEXT    NOT NULL,
    occurred_at     TEXT    NOT NULL,
    payload_version INTEGER NOT NULL,
    payload         TEXT    NOT NULL,

    CHECK (length(event_id) > 0),
    CHECK (length(kind) > 0),
    CHECK (length(subject_type) > 0),
    CHECK (length(subject_id) > 0),
    CHECK (length(actor) > 0),
    CHECK (length(occurred_at) > 0),
    CHECK (payload_version > 0),
    CHECK (json_valid(payload)),
    CHECK (json_type(payload) = 'object')
);

CREATE INDEX domain_events_subject ON domain_events (subject_type, subject_id, seq);
CREATE INDEX domain_events_kind ON domain_events (kind, seq);

-- The log is the sole authority for live state, so rewriting it is refused by
-- the database rather than by convention.
CREATE TRIGGER domain_events_no_update
BEFORE UPDATE ON domain_events
BEGIN
    SELECT RAISE(ABORT, 'domain_events is append-only');
END;

CREATE TRIGGER domain_events_no_delete
BEFORE DELETE ON domain_events
BEGIN
    SELECT RAISE(ABORT, 'domain_events is append-only');
END;
`,
	},
}

// schemaManifestDDL creates the manifest itself. It is applied before any
// migration and is not part of the versioned sequence.
const schemaManifestDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
);`

func (m migration) checksum() string {
	sum := sha256.Sum256([]byte(m.SQL))
	return hex.EncodeToString(sum[:])
}

// SchemaVersion reports the highest applied migration version, or zero when the
// manifest is empty.
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, wrapFailure(KindUnavailable, "schema_version", "cannot read the schema manifest", true,
			"confirm the database is initialized", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// Migrate brings the database up to this binary's schema version. It is
// idempotent, applies the manifest and all pending steps in one transaction,
// and fails closed on drift or on a database written by a newer binary.
func Migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "migrate", "cannot begin schema migration", true,
			"retry once the database is writable", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}

	if _, err := tx.ExecContext(ctx, schemaManifestDDL); err != nil {
		return rollback(wrapFailure(KindUnavailable, "migrate", "cannot create the schema manifest", true,
			"check database permissions", err))
	}

	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if err := checkManifest(applied); err != nil {
		return rollback(err)
	}

	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return rollback(wrapFailure(KindUnavailable, "migrate",
				fmt.Sprintf("migration %d (%s) failed", m.Version, m.Name), false,
				"correct the migration definition", err))
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			m.Version, m.Name, m.checksum(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return rollback(wrapFailure(KindUnavailable, "migrate",
				fmt.Sprintf("cannot record migration %d (%s)", m.Version, m.Name), true,
				"retry once the database is writable", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return rollback(wrapFailure(KindUnavailable, "migrate", "cannot commit schema migration", true,
			"retry once the database is writable", err))
	}
	return nil
}

func appliedMigrations(ctx context.Context, tx *sql.Tx) (map[int]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "migrate", "cannot read the schema manifest", true,
			"confirm the database is readable", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, wrapFailure(KindUnavailable, "migrate", "cannot read a manifest row", true,
				"confirm the database is readable", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "migrate", "cannot read the schema manifest", true,
			"confirm the database is readable", err)
	}
	return applied, nil
}

// checkManifest compares the recorded manifest against this binary's
// definition. Both directions matter: an edited historical step means the live
// schema no longer matches the code, and an unknown newer step means the
// database was written by a binary that knows more than this one.
func checkManifest(applied map[int]string) error {
	known := make(map[int]migration, len(migrations))
	for _, m := range migrations {
		known[m.Version] = m
	}

	for version, checksum := range applied {
		m, ok := known[version]
		if !ok {
			return newFailure(KindSchemaUnsupported, "migrate",
				fmt.Sprintf("the database records migration %d, which this binary does not define", version),
				true, "upgrade to a binary that defines this schema version and retry")
		}
		if m.checksum() != checksum {
			return newFailure(KindSchemaDrift, "migrate",
				fmt.Sprintf("migration %d (%s) no longer matches its recorded checksum", version, m.Name),
				false, "restore the original migration definition; applied migrations are immutable")
		}
	}
	return nil
}
