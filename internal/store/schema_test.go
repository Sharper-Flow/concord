package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sharper-flow/concord/internal/version"
)

func TestOpenAppliesSchemaManifest(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()

	var applied int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Fatalf("applied migrations = %d, want %d", applied, len(migrations))
	}

	got, err := readSchemaManifestVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if want := migrations[len(migrations)-1].Version; got != want {
		t.Errorf("schema manifest version = %d, want %d", got, want)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()

	before, err := readSchemaManifestVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if err := Migrate(ctx, s.DatabaseForTesting()); err != nil {
		t.Fatalf("second Migrate() error = %v", err)
	}
	after, err := readSchemaManifestVersion(ctx, s.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if before != after {
		t.Errorf("schema version moved on re-migration: %d -> %d", before, after)
	}

	var applied int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("applied migrations = %d, want %d", applied, len(migrations))
	}
}

func TestUpgradeConcurrentFirstOpen(t *testing.T) {
	previous := version.Value
	version.Value = "v1.0.0"
	t.Cleanup(func() { version.Value = previous })
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord.db")
	hits := 0
	loserCtx := context.WithValue(ctx, schemaAbsentHookKey{}, func(probe string) {
		if probe != "manifestVersions" {
			return
		}
		hits++
		winner, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("winner Open() error = %v", err)
		}
		t.Cleanup(func() { _ = winner.Close() })
	})
	report, err := Upgrade(loserCtx, path, nil)
	if hits != 1 {
		t.Fatalf("manifestVersions probe reached %d times, want 1; Upgrade() error = %v", hits, err)
	}
	if err != nil {
		t.Fatalf("Upgrade() after winner committed error = %v", err)
	}
	if report.SchemaVersion != CurrentSchemaVersion() {
		t.Errorf("schema version = %d, want %d", report.SchemaVersion, CurrentSchemaVersion())
	}
	if len(report.Applied) != 0 {
		t.Errorf("Upgrade() applied = %v, want empty; winner applied all migrations", report.Applied)
	}
}

func TestOpenAndMigrateRefuseForeignSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "foreign.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `CREATE TABLE foreign_data (value TEXT)`); err != nil {
		t.Fatal(err)
	}
	for name, open := range map[string]func() error{
		"open": func() error {
			s, err := Open(ctx, path)
			if s != nil {
				_ = s.Close()
			}
			return err
		},
		"migrate": func() error { return Migrate(ctx, db) },
	} {
		t.Run(name, func(t *testing.T) {
			err := open()
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindReadinessUnknown {
				t.Fatalf("error = %v, want readiness_unknown", err)
			}
			var objects int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='foreign_data'`).Scan(&objects); err != nil || objects != 1 {
				t.Fatalf("foreign table count = %d, error = %v", objects, err)
			}
		})
	}
}

// Serial: the concurrent-open busy budget assumes an idle machine;
// parallel siblings exceed it and surface SQLITE_BUSY.
func TestOpenConcurrentlyInitializesOneDatabase(t *testing.T) {
	for _, probe := range []string{"unstampedStoreFresh", "migrationManifestCurrent"} {
		t.Run(probe, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "concord.db")
			ctx := context.Background()
			var winner *Store
			hits := 0
			loserCtx := context.WithValue(ctx, schemaAbsentHookKey{}, func(current string) {
				if current != probe {
					return
				}
				hits++
				var err error
				winner, err = Open(ctx, path)
				if err != nil {
					t.Fatalf("winner Open() error = %v", err)
				}
				t.Cleanup(func() { _ = winner.Close() })
			})
			loser, err := Open(loserCtx, path)
			if hits != 1 {
				t.Fatalf("target probe reached %d times, want 1; Open() error = %v", hits, err)
			}
			if err != nil {
				t.Fatalf("Open() after winner committed error = %v", err)
			}
			t.Cleanup(func() { _ = loser.Close() })
			version, err := readSchemaManifestVersion(ctx, loser.DatabaseForTesting())
			if err != nil || version != CurrentSchemaVersion() {
				t.Errorf("schema version = %d, error = %v; want %d", version, err, CurrentSchemaVersion())
			}
		})
	}

	const openers = 8

	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()
	// Create the file with its connection settings and no schema objects, so
	// every concurrent Open takes the fresh-store path and contends for the
	// one write transaction that installs the baseline and retained migrations.
	seed, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("seed sql.Open() error = %v", err)
	}
	if _, err := seed.ExecContext(ctx, `CREATE TABLE concurrent_open_seed(x INTEGER); DROP TABLE concurrent_open_seed`); err != nil {
		_ = seed.Close()
		t.Fatalf("seed empty database file: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("seed Close() error = %v", err)
	}
	var ready sync.WaitGroup
	var start sync.WaitGroup
	var done sync.WaitGroup
	ready.Add(openers)
	start.Add(1)
	done.Add(openers)

	stores := make([]*Store, openers)
	errs := make([]error, openers)
	for i := range openers {
		go func(i int) {
			defer done.Done()
			ready.Done()
			start.Wait()
			stores[i], errs[i] = Open(ctx, path)
		}(i)
	}
	ready.Wait()
	start.Done()
	done.Wait()

	for i, s := range stores {
		if s == nil {
			continue
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Errorf("Open(%d) Close() error = %v", i, err)
			}
		})
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("Open(%d) error = %v", i, err)
		}
	}

	var verifier *Store
	for _, s := range stores {
		if s != nil {
			verifier = s
			break
		}
	}
	if verifier == nil {
		t.Fatal("all concurrent Open calls failed")
	}

	got, err := readSchemaManifestVersion(ctx, verifier.DatabaseForTesting())
	if err != nil {
		t.Fatalf("schema manifest query error = %v", err)
	}
	if want := migrations[len(migrations)-1].Version; got != want {
		t.Errorf("schema manifest version = %d, want %d", got, want)
	}

	var applied int
	if err := verifier.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(migrations) {
		t.Errorf("applied migrations = %d, want %d", applied, len(migrations))
	}
}

// The manifest records a checksum per migration so an edited historical
// migration is detected instead of silently diverging from the live schema.
func TestMigrateDetectsEditedHistory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = 'tampered' WHERE version = ?`, migrations[0].Version); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(ctx, path)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("Open() on tampered history error = %v, want *Failure", err)
	}
	if f.Kind != KindSchemaDrift {
		t.Errorf("Kind = %q, want %q", f.Kind, KindSchemaDrift)
	}
}

// A database written by a newer binary must fail closed rather than be operated
// on by an older schema definition.
func TestMigrateRejectsNewerSchema(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	future := migrations[len(migrations)-1].Version + 1
	if _, err := s.DatabaseForTesting().ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, 'from-the-future', 'x', '2026-01-01T00:00:00Z')`,
		future); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_, err = Open(ctx, path)
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("Open() on newer schema error = %v, want *Failure", err)
	}
	if f.Kind != KindSchemaUnsupported {
		t.Errorf("Kind = %q, want %q", f.Kind, KindSchemaUnsupported)
	}
	if !f.RetrySafe {
		t.Error("RetrySafe = false; upgrading the binary and retrying is the documented recovery")
	}
}

func TestMigrationsAreOrderedAndUnique(t *testing.T) {
	t.Parallel()
	seen := make(map[int]bool, len(migrations))
	for i, m := range migrations {
		if m.Version <= 0 {
			t.Errorf("migration %d has non-positive version %d", i, m.Version)
		}
		if seen[m.Version] {
			t.Errorf("duplicate migration version %d", m.Version)
		}
		seen[m.Version] = true
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Errorf("migration %d version %d is not greater than previous %d", i, m.Version, migrations[i-1].Version)
		}
		if m.Name == "" {
			t.Errorf("migration %d has no name", i)
		}
		if m.SQL == "" {
			t.Errorf("migration %d has no statements", i)
		}
	}
}

// work_items carries no last_activity_at column. A fold-advanced stamp drifts
// behind the log under a rolling upgrade with no convergence (CD-0111), so the
// fresh baseline must not reintroduce one.
func TestWorkItemsCarryNoFoldMaintainedLastActivity(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	var columns int
	if err := s.DatabaseForTesting().QueryRow(
		`SELECT count(*) FROM pragma_table_info('work_items') WHERE name='last_activity_at'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("work_items still carries last_activity_at; the stored marker must stay gone so no fold-maintained copy can drift")
	}
}

func TestFreshStoreSeedsVocabularyRegistriesAndGuardsNativePairs(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	db := s.DatabaseForTesting()
	ctx := context.Background()
	var workKinds, nativeStatuses int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM work_kinds`).Scan(&workKinds); err != nil || workKinds != 7 {
		t.Fatalf("work kind rows=%d err=%v", workKinds, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_native_run_statuses`).Scan(&nativeStatuses); err != nil || nativeStatuses != 10 {
		t.Fatalf("native status rows=%d err=%v", nativeStatuses, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at,terminal_time) VALUES('native-registry-work','task','Native registry','needed',1,'now','now',NULL)`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	pairs := []struct{ phase, status string }{
		{"start", "started"}, {"start", "failed_to_start"},
		{"health", "healthy"}, {"health", "degraded"}, {"health", "failed"},
		{"rollback", "rolled_back"}, {"rollback", "partially_rolled_back"}, {"rollback", "rollback_failed"},
		{"cleanup", "cleaned"}, {"cleanup", "cleanup_failed"},
	}
	for i, pair := range pairs {
		args := []any{"native-registry-work", "run-" + pair.phase, pair.phase, pair.status, "event-" + pair.phase, "client", "actor", "native://run", "sha256:" + strings.Repeat("a", 64), "evidence://run", "sha256:" + strings.Repeat("b", 64), "2026-08-25T00:00:00Z", "2026-08-25T00:00:00Z", "trusted_client_report", "{}", "policy", "policy"}
		if i > 0 {
			args[1] = "run-" + pair.phase + "-" + pair.status
		}
		if _, err := db.ExecContext(ctx, insert, args...); err != nil {
			t.Fatalf("declared native pair %s/%s rejected: %v", pair.phase, pair.status, err)
		}
	}
	for _, pair := range []struct{ phase, status string }{{"start", "healthy"}, {"health", "unknown"}} {
		args := []any{"native-registry-work", "bad-" + pair.phase + pair.status, pair.phase, pair.status, "event-bad", "client", "actor", "native://run", "sha256:" + strings.Repeat("a", 64), "evidence://run", "sha256:" + strings.Repeat("b", 64), "2026-08-25T00:00:00Z", "2026-08-25T00:00:00Z", "trusted_client_report", "{}", "policy", "policy"}
		if _, err := db.ExecContext(ctx, insert, args...); err == nil || !strings.Contains(err.Error(), "workflow native run phase and status are not a declared pair") {
			t.Fatalf("invalid native pair %s/%s error=%v", pair.phase, pair.status, err)
		}
	}
}

func TestArchivedWorkKindVocabulary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	db := s.DatabaseForTesting()
	seedArchivedWorkKindHome(t, db)
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()

	for _, kind := range []string{"work_note", "constitution", "decision", "spec", "lesson", "reference", "research"} {
		if err := insertArchivedWorkKind(ctx, db, "archived-"+kind, kind); err != nil {
			t.Fatalf("declared kind %s rejected: %v", kind, err)
		}
	}
	if err := insertArchivedWorkKind(ctx, db, "archived-bogus", "bogus"); err == nil || !strings.Contains(err.Error(), "archived work type is not a declared knowledge kind") {
		t.Fatalf("undeclared kind insert error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE archived_work SET type='bogus' WHERE id='archived-work_note'`); err == nil || !strings.Contains(err.Error(), "archived work type is not a declared knowledge kind") {
		t.Fatalf("undeclared kind update error = %v", err)
	}
}

func seedArchivedWorkKindHome(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('archived-kind-project','Archived kind project',1,'now','now')`,
		`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('archived-kind-product','Archived kind product','prototype','operator_only',1,'now','now')`,
		`INSERT INTO product_projects(product_id,project_id,role) VALUES('archived-kind-product','archived-kind-project','secondary')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('archived-kind-locator','archived-kind-project','canonical_path','/test/archived-kind','/test/archived-kind','now','now')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

func insertArchivedWorkKind(ctx context.Context, db *sql.DB, id, kind string) error {
	_, err := db.ExecContext(ctx, `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, kind, "Archived work", "2026-08-27T00:00:00Z", "completed", "[]", "completed", 0, "summary", "archived-kind-project", "archived-kind-locator", ".concord/docs/work/archived.md", strings.Repeat("a", 40), "sha256:"+strings.Repeat("b", 64))
	return err
}

// The read surface returns a work item's narrative whole, so the stored
// column holds at most 16384 characters and defaults to empty.
func TestFreshStoreBoundsWorkItemNarrative(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openTemp(t).DatabaseForTesting()
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('narrative-work','task','Narrative','needed',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	var narrative string
	if err := db.QueryRowContext(ctx, `SELECT narrative FROM work_items WHERE id='narrative-work'`).Scan(&narrative); err != nil || narrative != "" {
		t.Fatalf("narrative=%q err=%v, want empty default", narrative, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE work_items SET narrative=? WHERE id='narrative-work'`, strings.Repeat("n", 16385)); err == nil {
		t.Fatal("oversize narrative bypassed bounded CHECK")
	}
	if _, err := db.ExecContext(ctx, `UPDATE work_items SET narrative=? WHERE id='narrative-work'`, strings.Repeat("n", 16384)); err != nil {
		t.Fatalf("bounded narrative rejected: %v", err)
	}
}
