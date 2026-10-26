// Package sqlitedb owns SQLite connection, snapshot, revision and durable write
// mechanics. It has no dependency on the store's domain or migration policy.
package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	// Registers the pure-Go SQLite driver.
	_ "modernc.org/sqlite"
)

const (
	DriverName  = "sqlite"
	BusyTimeout = "5000"
	journalMode = "WAL"
	synchronous = "NORMAL"
	foreignKeys = "ON"
)

// Open prepares a one-connection pool without reaching the database. Callers
// retain their own validation, Ping and PRAGMA readback ordering.
func Open(dsn string) (*sql.DB, error) {
	db, err := sql.Open(DriverName, dsn)
	if err != nil {
		return nil, err
	}
	// Serialize local writers; busy_timeout absorbs cross-process contention.
	db.SetMaxOpenConns(1)
	return db, nil
}

// DSN applies settings per connection. The timeout precedes the WAL change.
// Immediate writes acquire their lock before reading; BeginRead opts out.
func DSN(path string) string {
	pragmas := []string{
		"busy_timeout(" + BusyTimeout + ")",
		"journal_mode(" + journalMode + ")",
		"synchronous(" + synchronous + ")",
		"foreign_keys(" + foreignKeys + ")",
	}
	query := []string{"_txlock=immediate"}
	for _, p := range pragmas {
		query = append(query, "_pragma="+url.QueryEscape(p))
	}
	return FileURI(path) + "?" + strings.Join(query, "&")
}

// FileURI escapes SQLite filename delimiters and leaves other bytes unchanged.
func FileURI(path string) string {
	return "file:" + strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
}

// ReadOnlyDataSource is the backup reader's foreign-key-enabled connection.
func ReadOnlyDataSource(path string) string {
	return FileURI(path) + "?mode=ro&_pragma=foreign_keys(1)"
}

// ReadOnlyReadinessDSN keeps readiness independent of write-side settings.
func ReadOnlyReadinessDSN(path string) string {
	return FileURI(path) + "?mode=ro&_pragma=busy_timeout(" + BusyTimeout + ")"
}

// VerifyPragmas confirms the driver's applied settings, not just accepted DSN.
func VerifyPragmas(ctx context.Context, q Queryer) error {
	for _, want := range []struct {
		pragma string
		expect string
	}{
		{"journal_mode", strings.ToLower(journalMode)},
		{"synchronous", "1"}, // NORMAL
		{"busy_timeout", BusyTimeout},
		{"foreign_keys", "1"}, // ON
	} {
		var got string
		if err := q.QueryRowContext(ctx, "PRAGMA "+want.pragma).Scan(&got); err != nil {
			return failure(Unavailable, "open", fmt.Sprintf("cannot read PRAGMA %s", want.pragma), true,
				"confirm the database file is a readable SQLite database", err)
		}
		if !strings.EqualFold(got, want.expect) {
			return failure(Unavailable, "open", fmt.Sprintf("PRAGMA %s is %q, want %q", want.pragma, got, want.expect), false,
				"confirm the driver applies connection settings from the data source name", nil)
		}
	}
	return nil
}
