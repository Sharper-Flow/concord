package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file uses only production code. A native control pins it as its
// sole test file, and the producer compiles exactly the pinned files.

// openTemp opens a store backed by a throwaway database file.
func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), copyTestDatabase(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})
	return s
}

func readSchemaManifestVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

// testDatabaseTemplate owns one migrated, empty database image for the package
// run. openTemp copies the closed image before Open performs its normal startup
// validation, avoiding a repeated replay of every migration in the hot path.
//
// The package TestMain sets dir to its run root, which it removes after the
// run. A native control compiles no TestMain, so dir stays empty and the
// template takes a private directory under the producer's TMPDIR, which lies
// outside the controlled snapshot and which the producer removes.
var testDatabaseTemplate struct {
	once sync.Once
	dir  string
	path string
	err  error
}

func copyTestDatabase(t *testing.T) string {
	t.Helper()

	testDatabaseTemplate.once.Do(func() {
		if testDatabaseTemplate.dir == "" {
			dir, err := os.MkdirTemp("", "concord-store-template-")
			if err != nil {
				testDatabaseTemplate.err = err
				return
			}
			testDatabaseTemplate.dir = dir
		}
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
	return copyClosedTestDatabase(t, testDatabaseTemplate.path)
}

// The source must be closed and checkpointed so the copy needs no WAL sidecar.
func copyClosedTestDatabase(t *testing.T, path string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "concord.db")
	source, err := os.Open(path)
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
	t.Parallel()
	first := openTemp(t)
	second := openTemp(t)
	ctx := context.Background()

	event := Event{EventID: "first-only", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "first-project", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("project.created"), Payload: []byte(`{"display_name":"First project"}`)}
	product := Event{EventID: "first-product-created", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "first-product", Actor: event.Actor, OccurredAt: event.OccurredAt, PayloadVersion: CurrentEventPayloadVersion("product.created"), Payload: []byte(`{"display_name":"First product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)}
	membership := Event{EventID: "first-membership", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: product.SubjectID, Actor: event.Actor, OccurredAt: event.OccurredAt, PayloadVersion: CurrentEventPayloadVersion("product_project.added"), Payload: []byte(`{"product_id":"first-product","project_id":"first-project","role":"primary","reason":"fixture isolation","expected_version":1,"resulting_version":2}`)}
	if err := ApplyOperation(ctx, first, Operation{Events: []Event{product, event, membership}}); err != nil {
		t.Fatalf("seed first store: %v", err)
	}
	var displayName string
	if err := first.db.QueryRow(`SELECT display_name FROM projects WHERE id=?`, event.SubjectID).Scan(&displayName); err != nil || displayName != "First project" {
		t.Fatalf("first store project = %q, err = %v", displayName, err)
	}

	var firstEvents, secondEvents int
	if err := first.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&firstEvents); err != nil {
		t.Fatalf("count first store events: %v", err)
	}
	if err := second.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM effect_receipts`).Scan(&secondEvents); err != nil {
		t.Fatalf("count second store events: %v", err)
	}
	if firstEvents != 3 || secondEvents != 0 {
		t.Fatalf("store receipt counts = (%d, %d), want (3, 0)", firstEvents, secondEvents)
	}

	if got, err := readSchemaManifestVersion(ctx, second.DatabaseForTesting()); err != nil {
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

func TestTestDatabaseTemplateCleanupOnFailure(t *testing.T) {
	const childEnv = "TEST_CONCORD_TEMPLATE_CLEANUP_CHILD"
	const marker = "template-root: "
	if os.Getenv(childEnv) == "1" {
		openTemp(t)
		fmt.Println(marker + testDatabaseTemplate.dir)
		t.Fatal("synthetic failure after template creation")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestTestDatabaseTemplateCleanupOnFailure$")
	command.Env = append(os.Environ(), childEnv+"=1")
	output, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
		t.Fatalf("failing test child: %v\n%s", err, output)
	}
	for line := range strings.SplitSeq(string(output), "\n") {
		if root, found := strings.CutPrefix(line, marker); found {
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("template root survived a failing test: %s (%v)", root, err)
			}
			return
		}
	}
	t.Fatalf("test child did not create the template:\n%s", output)
}
