package store_test

import (
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/sharper-flow/concord/internal/store"
)

// holderDataSourceName mirrors store.dataSourceName so the lock holder faces
// the same connection settings a second store process would.
func holderDataSourceName(path string) string {
	query := []string{"_txlock=immediate"}
	for _, p := range []string{"busy_timeout(5000)", "journal_mode(wal)", "synchronous(normal)", "foreign_keys(on)"} {
		query = append(query, "_pragma="+url.QueryEscape(p))
	}
	return "file:" + path + "?" + strings.Join(query, "&")
}

// Opening an established database must not need the write lock: another
// process can hold one for longer than the busy timeout while a reader
// opens. The installation key already exists, so the open path must stay
// read-only there. This test fails while finishOpen runs an unconditional
// INSERT OR IGNORE for the installation key.
func TestOpenSucceedsWhileAnotherConnectionHoldsTheWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	established, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("establish open: %v", err)
	}
	t.Cleanup(func() { _ = established.Close() })

	holder, err := sql.Open("sqlite", holderDataSourceName(path))
	if err != nil {
		t.Fatalf("holder open: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("holder begin immediate: %v", err)
	}
	t.Cleanup(func() { _, _ = conn.ExecContext(t.Context(), `ROLLBACK`) })

	started := time.Now()
	second, err := store.Open(t.Context(), path)
	if err != nil {
		if strings.Contains(err.Error(), "cannot persist the installation cursor key") {
			t.Fatalf("open needed the write lock and failed after %s: %v", time.Since(started).Round(time.Millisecond), err)
		}
		t.Fatalf("open under a foreign write lock: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	// pragmaBusyTimeout is 5000 ms; an established-key open that stays
	// read-only returns without ever waiting on the write lock.
	const busyTimeout = 5 * time.Second
	if elapsed := time.Since(started); elapsed > busyTimeout {
		t.Fatalf("open waited %s for the write lock although the installation key exists", elapsed)
	}
}
