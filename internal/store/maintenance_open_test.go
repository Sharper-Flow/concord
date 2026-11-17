package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMaintenanceRefusesUninitializedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	if s, err := OpenMaintenance(t.Context(), path); err == nil {
		_ = s.Close()
		t.Fatal("opened absent store")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("absent store created: %v", err)
	}
}

func TestOpenMaintenanceDoesNotCreateMissingKey(t *testing.T) {
	s := openTemp(t)
	path := s.Path()
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM agent_installation_keys; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if maintenance, err := OpenMaintenance(t.Context(), path); err == nil {
		_ = maintenance.Close()
		t.Fatal("opened store without installation key")
	}
	db, err := openReadonlyStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM agent_installation_keys`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("key count = %d: %v", count, err)
	}
}
