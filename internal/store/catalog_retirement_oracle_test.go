package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCatalogRetirementSchema(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, table := range []string{"managed_resources", "resource_products", "domain_resource_attachment_sets", "domain_resource_attachment_edges", "domains", "domain_registries"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("retired catalog object remains: %s", table)
		}
	}
	for _, table := range []string{"projects", "project_locators", "products", "product_projects", "work_items", "work_projects"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("independent current object missing: %s", table)
		}
	}
}

func TestCatalogRetirementSnapshotSafety(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, filepath.Join(root, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snapshot := filepath.Join(root, "snapshot.db")
	manifest, err := Backup(ctx, s, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, snapshot, manifest.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored.db")
	if _, err := RestoreBackup(ctx, snapshot, destination); err != nil {
		t.Fatal(err)
	}
	checksum, err := fileSHA256(snapshot)
	if err != nil || checksum != manifest.DBChecksum {
		t.Fatalf("restore changed source snapshot: checksum=%s, error=%v", checksum, err)
	}
	if _, err := RestoreBackup(ctx, snapshot, destination); err == nil {
		t.Fatal("restore overwrote an existing destination")
	}
}
