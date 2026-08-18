package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// testDatabaseTemplate owns one migrated, empty database image for the package
// run. openTemp copies the closed image before Open performs its normal startup
// validation, avoiding a repeated replay of every migration in the hot path.
var testDatabaseTemplate struct {
	once sync.Once
	dir  string
	path string
	err  error
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "concord-store-test-template-")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create store test template directory: %v\n", err)
		os.Exit(1)
	}
	testDatabaseTemplate.dir = dir

	code := m.Run()
	if err := os.RemoveAll(dir); err != nil && code == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "remove store test template directory: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func copyTestDatabase(t *testing.T) string {
	t.Helper()

	testDatabaseTemplate.once.Do(func() {
		testDatabaseTemplate.path = filepath.Join(testDatabaseTemplate.dir, "template.db")
		db, err := sql.Open(driverName, dataSourceName(testDatabaseTemplate.path))
		if err != nil {
			testDatabaseTemplate.err = err
			return
		}
		db.SetMaxOpenConns(1)

		ctx := context.Background()
		if err := Migrate(ctx, db); err != nil {
			testDatabaseTemplate.err = err
			_ = db.Close()
			return
		}

		// Make the source self-contained before it is copied. In particular, do
		// not leave a WAL sidecar whose contents could race with a destination
		// opened by a test.
		var busy, logPages, checkpointedPages int
		if err := db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointedPages); err != nil {
			testDatabaseTemplate.err = err
			_ = db.Close()
			return
		}
		if busy != 0 {
			testDatabaseTemplate.err = fmt.Errorf("template WAL checkpoint is busy: %d", busy)
			_ = db.Close()
			return
		}
		if err := db.Close(); err != nil {
			testDatabaseTemplate.err = err
		}
	})
	if testDatabaseTemplate.err != nil {
		t.Fatalf("prepare test database template: %v", testDatabaseTemplate.err)
	}

	destination := filepath.Join(t.TempDir(), "concord.db")
	source, err := os.Open(testDatabaseTemplate.path)
	if err != nil {
		t.Fatalf("open test database template: %v", err)
	}
	defer source.Close()

	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create test database copy: %v", err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		t.Fatalf("copy test database template: %v", err)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("close test database copy: %v", err)
	}
	return destination
}

func TestOpenTempCopiesIsolatedLatestSchema(t *testing.T) {
	first := openTemp(t)
	second := openTemp(t)
	ctx := context.Background()

	if _, err := first.DatabaseForTesting().ExecContext(ctx, `INSERT INTO domain_events
		(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload)
		VALUES ('first-only', 'test.event', 'product', 'product-1', 'test', '2026-01-01T00:00:00Z', 1, '{}')`); err != nil {
		t.Fatalf("insert first store event: %v", err)
	}

	var firstEvents, secondEvents int
	if err := first.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events`).Scan(&firstEvents); err != nil {
		t.Fatalf("count first store events: %v", err)
	}
	if err := second.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM domain_events`).Scan(&secondEvents); err != nil {
		t.Fatalf("count second store events: %v", err)
	}
	if firstEvents != 1 || secondEvents != 0 {
		t.Fatalf("store event counts = (%d, %d), want (1, 0)", firstEvents, secondEvents)
	}

	if got, err := SchemaVersion(ctx, second.DatabaseForTesting()); err != nil {
		t.Fatalf("schema version: %v", err)
	} else if got != CurrentSchemaVersion() {
		t.Fatalf("second store schema version = %d, want %d", got, CurrentSchemaVersion())
	}
	for _, table := range []string{"products", "projects", "work_items", "relations"} {
		var count int
		if err := second.DatabaseForTesting().QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("count empty %s table: %v", table, err)
		}
		if count != 0 {
			t.Errorf("second store %s rows = %d, want 0", table, count)
		}
	}
}
