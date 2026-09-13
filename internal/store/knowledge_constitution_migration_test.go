package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateV80WidensLawSubjectsAndInvalidatesWatermark proves the
// constitution migration on a pre-v80 database: existing decision and spec
// law subjects and their relations survive the table rebuild, the widened
// vocabulary admits constitution rows and still refuses non-law kinds, and
// the deleted watermark forces the demand-driven knowledge rebuild.
func TestMigrateV80WidensLawSubjectsAndInvalidatesWatermark(t *testing.T) {
	ctx := context.Background()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "concord-v79.db"), len(migrations)-1)

	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('p','P',1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('l','p','canonical_path','/test/l','/test/l','t','t')`); err != nil {
		t.Fatal(err)
	}
	hashA, hashB := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	if _, err := db.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','CD-0001','decision','accepted','docs/decisions/CD-0001.md','A',?,'c')`, hashA); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','spec-9','spec','accepted','docs/specs/spec.md','B',?,'c')`, hashB); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('p','l','CD-0001','refines','spec-9','c')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete) VALUES('p','l','HEAD','c','t',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}

	last := migrations[len(migrations)-1]
	if last.Version != 80 {
		t.Fatalf("last migration version = %d, want 80", last.Version)
	}
	if err := applyMigration(ctx, db, last); err != nil {
		t.Fatalf("apply migration 80: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, last.Version, last.Name, last.checksum(), "2026-09-13T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	var subjects, relations, watermarks int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM law_subjects WHERE home_project_id='p' AND home_locator_id='l'`).Scan(&subjects); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM law_relations WHERE home_project_id='p' AND home_locator_id='l'`).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM knowledge_index_watermark`).Scan(&watermarks); err != nil {
		t.Fatal(err)
	}
	if subjects != 2 || relations != 1 {
		t.Fatalf("post-migration law rows = subjects %d relations %d, want 2 and 1", subjects, relations)
	}
	if watermarks != 0 {
		t.Fatalf("post-migration watermark rows = %d, want 0 (invalidated for demand rebuild)", watermarks)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','proc-1','constitution','accepted','docs/proc.md','C',?,'c')`, "sha256:"+strings.Repeat("c", 64)); err != nil {
		t.Fatalf("constitution law subject insert refused after migration 80: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('p','l','lesson-1','lesson','published','docs/lessons/l.md','D',?,'c')`, "sha256:"+strings.Repeat("d", 64)); err == nil {
		t.Fatal("non-law lesson kind admitted into law_subjects after migration 80")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
}
