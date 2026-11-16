package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// openMigrated returns a database at the current schema version.
func openMigrated(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "repair.db")
	db, err := sql.Open("sqlite", SQLiteFileURI(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// The compatibility floor is the highest breaking migration applied, and every
// migration after it is additive by declaration. A binary defining the floor
// must open a database that has run later additive migrations.
func TestManifestAdmitsLaterAdditiveMigrations(t *testing.T) {
	t.Parallel()
	floor := 0
	for _, m := range migrations {
		if m.Breaking && m.Version > floor {
			floor = m.Version
		}
	}
	if floor == 0 {
		t.Fatal("no breaking migration declared; the floor would never rise")
	}

	// A database that ran two migrations this binary does not define: one
	// additive, which must not raise the floor, and nothing breaking.
	applied := map[int]appliedMigration{}
	for _, m := range migrations {
		applied[m.Version] = appliedMigration{Checksum: m.checksum(), Breaking: m.Breaking}
	}
	applied[CurrentSchemaVersion()+1] = appliedMigration{Checksum: "future-additive", Breaking: false}
	applied[CurrentSchemaVersion()+2] = appliedMigration{Checksum: "future-additive-2", Breaking: false}
	if err := checkManifest(applied); err != nil {
		t.Fatalf("later additive migrations must not refuse this binary: %v", err)
	}

	// One breaking migration this binary does not define closes the door, and
	// the refusal names the version the operator needs.
	applied[CurrentSchemaVersion()+3] = appliedMigration{Checksum: "future-breaking", Breaking: true}
	err := checkManifest(applied)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("breaking future migration err=%v, want schema_unsupported", err)
	}
	if !strings.Contains(failure.Detail, strconv.Itoa(CurrentSchemaVersion()+3)) {
		t.Fatalf("refusal %q must name the version the database requires", failure.Detail)
	}
	if !failure.RetrySafe {
		t.Fatal("a refusal that writes nothing must be retry safe")
	}
}

// One database that has not changed must produce one refusal, whichever order
// the range happens to take. checkManifest reads a map, and Go randomizes map
// order, so a refusal built from the first entry reached names a different
// migration between runs and reads as the database advancing under the
// operator. Repetition is the assertion here: a single call cannot tell a
// stable answer from a lucky one.
func TestManifestRefusalNamesTheSameMigrationEveryTime(t *testing.T) {
	t.Parallel()

	baseline := func() map[int]appliedMigration {
		applied := map[int]appliedMigration{}
		for _, m := range migrations {
			applied[m.Version] = appliedMigration{Checksum: m.checksum(), Breaking: m.Breaking}
		}
		return applied
	}
	const attempts = 200

	t.Run("drift names the earliest divergence", func(t *testing.T) {
		t.Parallel()
		applied := baseline()
		// Two historical migrations drift at once. The earliest is the point
		// where the live schema and this binary parted, so it is the one the
		// operator must restore first.
		early, late := migrations[1].Version, migrations[len(migrations)-1].Version
		applied[early] = appliedMigration{Checksum: "forged-early", Breaking: applied[early].Breaking}
		applied[late] = appliedMigration{Checksum: "forged-late", Breaking: applied[late].Breaking}

		want := "migration " + strconv.Itoa(early) + " "
		for i := 0; i < attempts; i++ {
			err := checkManifest(applied)
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindSchemaDrift {
				t.Fatalf("attempt %d: err=%v, want schema_drift", i, err)
			}
			if !strings.Contains(failure.Detail, want) {
				t.Fatalf("attempt %d: refusal %q must name migration %d, the earliest drift", i, failure.Detail, early)
			}
		}
	})

	t.Run("unsupported names the highest breaking version", func(t *testing.T) {
		t.Parallel()
		applied := baseline()
		// Several breaking migrations this binary does not define. The highest
		// is the floor: a binary defining it defines every earlier one too.
		lower := CurrentSchemaVersion() + 1
		highest := CurrentSchemaVersion() + 5
		for version := lower; version <= highest; version++ {
			applied[version] = appliedMigration{Checksum: "future-breaking", Breaking: true}
		}

		want := "schema version " + strconv.Itoa(highest)
		for i := 0; i < attempts; i++ {
			err := checkManifest(applied)
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindSchemaUnsupported {
				t.Fatalf("attempt %d: err=%v, want schema_unsupported", i, err)
			}
			if !strings.Contains(failure.Detail, want) {
				t.Fatalf("attempt %d: refusal %q must name schema version %d, the highest breaking migration", i, failure.Detail, highest)
			}
		}
	})
}

func TestSupportedManifestWithoutCompatibilityColumnFailsClosed(t *testing.T) {
	t.Parallel()
	db := openMigrated(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `ALTER TABLE schema_migrations DROP COLUMN breaking`); err != nil {
		t.Fatalf("stage a pre-column manifest: %v", err)
	}
	err := Migrate(ctx, db)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindReadinessUnknown {
		t.Fatalf("migrate error = %v, want readiness_unknown", err)
	}
	var columns int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('schema_migrations') WHERE name='breaking'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("refused migration restored the missing breaking column")
	}
}
