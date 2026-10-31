package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// TestLawRelationsDeclarerMigrationIsAdditive pins the declaration bits: a
// nullable column an older binary never names is non-breaking, and the
// knowledge rebuild writes it once per row (the origin class).
func TestLawRelationsDeclarerMigrationIsAdditive(t *testing.T) {
	t.Parallel()
	found := false
	for _, m := range migrations {
		if m.Name != "law_relations_carry_declarer" {
			continue
		}
		found = true
		if m.Breaking || m.FoldMaintained != "origin" {
			t.Fatalf("declarer migration must stay additive with an origin declaration: breaking=%v fold=%q", m.Breaking, m.FoldMaintained)
		}
	}
	if !found {
		t.Fatal("law_relations_carry_declarer migration is missing")
	}
}

// TestLawRelationsDeclarerMigrationKeepsExistingRowsNull proves the forward
// migration from the previous version: a relation projected before the column
// keeps a NULL declarer, a writer that never names the column still inserts,
// and the column admits only one of the row's two endpoints.
func TestLawRelationsDeclarerMigrationKeepsExistingRowsNull(t *testing.T) {
	t.Parallel()
	index := -1
	for i, m := range migrations {
		if m.Name == "law_relations_carry_declarer" {
			index = i
			break
		}
	}
	if index < 0 {
		t.Fatal("law_relations_carry_declarer migration is missing")
	}
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "declarer.db"), index)
	ctx := context.Background()
	if present, err := columnPresent(ctx, db, "law_relations", "declarer_law_id"); err != nil || present {
		t.Fatalf("fixture already has the declarer before its forward migration: present=%v err=%v", present, err)
	}
	const hash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	seed := []string{
		`INSERT INTO fold_guard(active) VALUES(1)`,
		`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('p','p',1,'t','t')`,
		`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('l','p','canonical_path','/test/l','/test/l','t','t')`,
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','a','spec','accepted','.concord/docs/a.md','A','` + hash + `','c'),('p','l','b','spec','accepted','.concord/docs/b.md','B','` + hash + `','c'),('p','l','x','spec','accepted','.concord/docs/x.md','X','` + hash + `','c')`,
		`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('p','l','a','conflicts_with','b','c')`,
	}
	for _, statement := range seed {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	// The declarer migration is this test's forward step. Later breaking
	// migrations retire law_relations itself (schema 174), so the proof runs
	// at the pinned declarer prefix instead of the moving head. The guarded
	// tables at this schema are fold-only.
	advanceHistoricalPrefix(t, db, migrations[index].Version)

	var declarer sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT declarer_law_id FROM law_relations WHERE source_law_id='a'`).Scan(&declarer); err != nil {
		t.Fatal(err)
	}
	if declarer.Valid {
		t.Fatalf("a pre-migration relation carries declarer %q, want NULL", declarer.String)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('p','l','a','refines','x','c')`); err != nil {
		t.Fatalf("an older writer's insert, which never names the declarer, refused: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid,declarer_law_id) VALUES('p','l','b','refines','x','c','a')`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Fatalf("law_relations accepted a declarer that is neither endpoint, or refused it for another reason: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}
