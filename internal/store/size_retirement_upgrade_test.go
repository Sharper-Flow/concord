package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSizeRetirementUpgradePreservesVerifiedSnapshot(t *testing.T) {
	useStampedBuild(t)
	path := filepath.Join(t.TempDir(), "predecessor.db")
	db := openMigratedTo(t, path, migrationPrefixBefore(t, "retire_work_size"))
	_, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product','Product','prototype','operator_only',1,'now','now');
INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project','Project',1,'now','now');
INSERT INTO product_projects(product_id,project_id,role) VALUES('product','project','primary');
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('work','task','Work','needed',0,1,'now','now');
INSERT INTO work_projects(work_id,project_id,role) VALUES('work','project','primary');
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES
 ('size',1,'work.sized','work_item','work','operator',1,?),('other',2,'work.created','work_item','work','operator',1,?);
INSERT INTO work_size_assessments(event_id,work_id,sequence,model_version,size_class,inputs_json,footprint_json,recorded_at) VALUES('size','work',1,1,'small','{}','{}','now');
INSERT INTO work_size_proof_facts(work_id,state) VALUES('work','{}');
DELETE FROM write_guard;`, testManifestDigest, testManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := Open(t.Context(), path); err == nil {
		s.Close()
		t.Fatal("open applied a breaking migration")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	snapshot := filepath.Join(t.TempDir(), "verified.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 168 {
		t.Fatalf("predecessor snapshot: %+v %v", verified, err)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"work_size_assessments", "work_size_proof_facts"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retired table %s remains: %d %v", table, count, err)
		}
	}
	var sizes, others, guards int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM effect_receipts WHERE kind='work.sized'),(SELECT count(*) FROM effect_receipts WHERE event_id='other'),(SELECT count(*) FROM write_guard)`).Scan(&sizes, &others, &guards); err != nil || sizes != 0 || others != 1 || guards != 0 {
		t.Fatalf("receipts/guard = %d/%d/%d: %v", sizes, others, guards, err)
	}
	backup, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var assessments, proofs int
	if err := backup.QueryRow(`SELECT (SELECT count(*) FROM work_size_assessments),(SELECT count(*) FROM work_size_proof_facts)`).Scan(&assessments, &proofs); err != nil || assessments != 1 || proofs != 1 {
		t.Fatalf("snapshot facts = %d/%d: %v", assessments, proofs, err)
	}
}

func TestSizeEventAppendIsRetired(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "retired-size")
	err := applyWorkEvent(t, s, operationEvent("retired-size-event", "work.sized", SubjectWorkItem, "retired-size", map[string]any{}), nil)
	assertFailureKind(t, err, KindInvalidOperation)
}

func TestSizeRetirementRollsBackWhenWriteGuardIsOccupied(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "occupied.db"), migrationPrefixBefore(t, "retire_work_size"))
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), db); err == nil {
		t.Fatal("migration accepted an occupied write guard")
	}
	var tables, applied, guard int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM sqlite_schema WHERE type='table' AND name IN ('work_size_assessments','work_size_proof_facts')),(SELECT count(*) FROM schema_migrations WHERE version=169),(SELECT count(*) FROM write_guard)`).Scan(&tables, &applied, &guard); err != nil || tables != 2 || applied != 0 || guard != 1 {
		t.Fatalf("rollback tables/migration/guard = %d/%d/%d: %v", tables, applied, guard, err)
	}
}
