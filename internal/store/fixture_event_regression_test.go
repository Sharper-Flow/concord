package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// synthExecPath creates corruption through a foreign connection without
// foreign keys, while retaining the canonical projection write guards.
func synthExecPath(t *testing.T, path, guard string, queries ...string) {
	t.Helper()
	db, err := sql.Open(driverName, SQLiteFileURI(path)+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO `+guard+`(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = db.ExecContext(ctx, `DELETE FROM `+guard) }()
	for _, q := range queries {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("path synthetic query: %v\n%s", err, q)
		}
	}
}

func TestFixtureDSNsKeepSubtestStoresSeparate(t *testing.T) {
	root := t.TempDir()
	neighbor, err := Open(t.Context(), filepath.Join(root, "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = neighbor.Close() })
	for _, name := range []string{"#first", "#second"} {
		t.Run(name, func(t *testing.T) {
			s, err := Open(t.Context(), filepath.Join(root, "fixture"+name+".db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			synthExecPath(t, s.Path(), "write_guard", `CREATE TABLE fixture_path_probe (id INTEGER PRIMARY KEY)`)
			var count int
			if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='fixture_path_probe'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Errorf("fixture wrote the truncated neighbor instead of %q", s.Path())
			}
		})
	}
	var count int
	if err := neighbor.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='fixture_path_probe'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("fixture wrote the fragment-truncated neighbor")
	}
}

func TestObsoleteEpicEventsAreNotRegistered(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{EventID: "obsolete-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "obsolete-work", Actor: "test", OccurredAt: time.Unix(4, 3).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: mustJSONValue(map[string]any{"work_kind": "epic", "title": "obsolete"})}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "obsolete-work"): 0}})
	if err == nil {
		t.Fatal("obsolete Epic work kind was accepted by the store")
	}
}
