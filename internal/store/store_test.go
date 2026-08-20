package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

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

// CD-0002 fixes the connection settings; PM3 relies on foreign keys for typed
// edges. Each is per-connection in SQLite, so read them back from the live
// connection rather than trusting the data source name.
func TestOpenAppliesRequiredPragmas(t *testing.T) {
	s := openTemp(t)

	for _, tc := range []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"synchronous", "2"},
		{"busy_timeout", "5000"},
		{"foreign_keys", "1"},
	} {
		var got string
		if err := s.DatabaseForTesting().QueryRowContext(context.Background(), "PRAGMA "+tc.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s error = %v", tc.pragma, err)
		}
		if got != tc.want {
			t.Errorf("PRAGMA %s = %q, want %q", tc.pragma, got, tc.want)
		}
	}
}

// A pragma readback proves the declaration, not the behavior. The slice requires
// evidence that foreign keys are actually enforced.
func TestOpenEnforcesForeignKeysBehaviorally(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	if _, err := s.DatabaseForTesting().ExecContext(ctx, `
		CREATE TABLE fk_parent (id TEXT PRIMARY KEY);
		CREATE TABLE fk_child (id TEXT PRIMARY KEY, parent_id TEXT NOT NULL REFERENCES fk_parent(id));
	`); err != nil {
		t.Fatalf("create fixture tables: %v", err)
	}

	_, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fk_child (id, parent_id) VALUES ('c', 'missing')`)
	if err == nil {
		t.Fatal("insert violating a foreign key succeeded; enforcement is not active")
	}
}

func TestOpenIsIdempotentAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concord.db")
	ctx := context.Background()

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestDefaultPathHonorsDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg-data")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	if want := filepath.Join("/xdg-data", "concord", "concord.db"); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestDefaultPathFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/fallback-home")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath() error = %v", err)
	}
	if want := filepath.Join("/fallback-home", ".local", "share", "concord", "concord.db"); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestOpenRejectsUnknownDataHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("DefaultPath() with no resolvable home returned no error")
	}
}

// A failure must classify itself so callers do not parse message strings.
func TestFailureCarriesTypedClassification(t *testing.T) {
	var f *Failure
	err := newFailure(KindInvalidSubject, "append", "unknown subject type", false, "use a registered subject type")
	if !errors.As(err, &f) {
		t.Fatalf("newFailure() did not produce a *Failure: %T", err)
	}
	if f.Kind != KindInvalidSubject {
		t.Errorf("Kind = %q, want %q", f.Kind, KindInvalidSubject)
	}
	if f.RetrySafe {
		t.Error("RetrySafe = true, want false")
	}
	if f.RecoveryAction == "" {
		t.Error("RecoveryAction is empty; callers cannot act on the failure")
	}
	if f.Error() == "" {
		t.Error("Error() is empty")
	}
}
