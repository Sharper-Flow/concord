package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// openV49 builds a database at the migration state immediately before the
// display-name bound, so a test can seed rows the bound would refuse.
func openV49(t *testing.T, name string) *sql.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), name)))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:49] {
		if err := applyMigration(ctx, db, migration); err != nil {
			t.Fatalf("migration %d: %v", migration.Version, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, migration.Version, migration.Name, migration.checksum(), "2026-08-10T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestMigrateV49ToV50BoundsProductAndProjectDisplayNames(t *testing.T) {
	ctx := context.Background()
	db := openV49(t, "concord-v49.db")
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != CurrentSchemaVersion() {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
			t.Errorf("remove fold guard: %v", err)
		}
	}()

	overlong := strings.Repeat("a", 257)
	if _, err := db.ExecContext(ctx, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('p',?,'prototype','operator_only',1,'now','now')`, overlong); err == nil {
		t.Fatal("over-length Product display name bypassed the bound")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('j',?,1,'now','now')`, overlong); err == nil {
		t.Fatal("over-length Project display name bypassed the bound")
	}

	// SQLite length() counts characters, so a 256-rune multi-byte name is
	// inside the bound even though it is 768 bytes.
	multibyte := strings.Repeat("界", 256)
	if _, err := db.ExecContext(ctx, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('p',?,'prototype','operator_only',1,'now','now')`, multibyte); err != nil {
		t.Fatalf("256-rune multi-byte Product name refused: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('j',?,1,'now','now')`, multibyte); err != nil {
		t.Fatalf("256-rune multi-byte Project name refused: %v", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE products SET display_name=? WHERE id='p'`, overlong); err == nil {
		t.Fatal("over-length Product rename bypassed the bound")
	}
	if _, err := db.ExecContext(ctx, `UPDATE projects SET display_name=? WHERE id='j'`, overlong); err == nil {
		t.Fatal("over-length Project rename bypassed the bound")
	}
	if _, err := db.ExecContext(ctx, `UPDATE products SET display_name='' WHERE id='p'`); err == nil {
		t.Fatal("empty Product display name bypassed the bound")
	}
}

func TestMigrateV50RefusesAStoredOverlongDisplayName(t *testing.T) {
	ctx := context.Background()
	db := openV49(t, "concord-v49-dirty.db")
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('p',?,'prototype','operator_only',1,'now','now')`, strings.Repeat("a", 257)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil {
		t.Fatal("migration admitted a stored Product name the read surface cannot return")
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if seeded := migrations[48].Version; version != seeded {
		t.Fatalf("schema version=%d after refused migration, want %d", version, seeded)
	}
}

func TestCreateProductRefusesADisplayNameTheReadSurfaceCannotReturn(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	overlong := strings.Repeat("a", 257)

	_, err := s.CreateProductWithProject(ctx, ProductCreation{
		ProductID: "bound-product", DisplayName: overlong, StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "bound-project", ProjectDisplayName: "Bound Project", Role: "primary",
	})
	assertFailureKind(t, err, KindInvalidOperation)

	_, err = s.CreateProductWithProject(ctx, ProductCreation{
		ProductID: "bound-product", DisplayName: "Bound Product", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "bound-project", ProjectDisplayName: overlong, Role: "primary",
	})
	assertFailureKind(t, err, KindInvalidOperation)

	var stored int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM products`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("products rows=%d after refused creations, want 0", stored)
	}

	if _, err := s.CreateProductWithProject(ctx, ProductCreation{
		ProductID: "bound-product", DisplayName: strings.Repeat("界", 256), StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "bound-project", ProjectDisplayName: strings.Repeat("界", 256), Role: "primary",
	}); err != nil {
		t.Fatalf("256-rune multi-byte display names refused: %v", err)
	}
}

func TestFoldRefusesAnOverlongDisplayNameOnEveryNamingEvent(t *testing.T) {
	ctx := context.Background()
	overlong := strings.Repeat("a", 257)

	for _, testCase := range []struct {
		name    string
		kind    string
		subject SubjectType
		id      string
		payload map[string]any
		version int64
	}{
		{
			name: "product.created", kind: "product.created", subject: SubjectProduct, id: "fold-product",
			payload: map[string]any{"display_name": overlong, "stage_maturity": "prototype", "stage_audience_commitment": "operator_only"},
		},
		{
			name: "project.created", kind: "project.created", subject: SubjectProject, id: "fold-project",
			payload: map[string]any{"display_name": overlong},
		},
		{
			name: "product.renamed", kind: "product.renamed", subject: SubjectProduct, id: "named-product",
			payload: map[string]any{"display_name": overlong}, version: 2,
		},
		{
			// The Product absorbs the membership bump, so it is at version 2
			// while its first Project is still at version 1.
			name: "project.renamed", kind: "project.renamed", subject: SubjectProject, id: "named-project",
			payload: map[string]any{"display_name": overlong}, version: 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTemp(t)
			if _, err := s.CreateProductWithProject(ctx, ProductCreation{
				ProductID: "named-product", DisplayName: "Named Product", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
				ProjectID: "named-project", ProjectDisplayName: "Named Project", Role: "primary",
			}); err != nil {
				t.Fatal(err)
			}
			event := operationEvent("bound-"+testCase.kind, testCase.kind, testCase.subject, testCase.id, testCase.payload)
			expected := map[SubjectRef]int64{}
			if testCase.version > 0 {
				expected[VersionRef(testCase.subject, testCase.id)] = testCase.version
			}
			err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: expected})
			assertFailureKind(t, err, KindInvalidPayload)
		})
	}
}
