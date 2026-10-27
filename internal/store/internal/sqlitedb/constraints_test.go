package sqlitedb_test

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
	sqlitelib "modernc.org/sqlite/lib"
)

type opaqueError struct{ err error }

func (e opaqueError) Error() string { return "driver detail withheld" }
func (e opaqueError) Unwrap() error { return e.err }

func TestConstraintClassification(t *testing.T) {
	t.Parallel()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE parent(id INTEGER PRIMARY KEY);
CREATE TABLE probe(id INTEGER PRIMARY KEY, identity TEXT UNIQUE, parent_id INTEGER REFERENCES parent(id), positive INTEGER CHECK(positive > 0), required TEXT NOT NULL);
INSERT INTO probe VALUES(1, 'known', NULL, 1, 'present');`); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		query string
		code  int
		want  [5]bool // unique, identity, foreign key, check, any constraint
	}{
		{"unique", `INSERT INTO probe VALUES(2, 'known', NULL, 1, 'present')`, sqlitelib.SQLITE_CONSTRAINT_UNIQUE, [5]bool{true, true, false, false, true}},
		{"primary key", `INSERT INTO probe VALUES(1, 'other', NULL, 1, 'present')`, sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY, [5]bool{false, true, false, false, true}},
		{"foreign key", `INSERT INTO probe VALUES(2, 'other', 99, 1, 'present')`, sqlitelib.SQLITE_CONSTRAINT_FOREIGNKEY, [5]bool{false, false, true, false, true}},
		{"check", `INSERT INTO probe VALUES(2, 'other', NULL, -1, 'present')`, sqlitelib.SQLITE_CONSTRAINT_CHECK, [5]bool{false, false, false, true, true}},
		{"not null", `INSERT INTO probe VALUES(2, 'other', NULL, 1, NULL)`, sqlitelib.SQLITE_CONSTRAINT_NOTNULL, [5]bool{false, false, false, false, true}},
		{"non-constraint driver error", `INSERT INTO absent VALUES(1)`, sqlitelib.SQLITE_ERROR, [5]bool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(tc.query)
			if err == nil {
				t.Fatal("expected the driver to refuse the statement")
			}
			for _, input := range []error{err, opaqueError{err}} {
				if got := sqlitedb.ResultCode(input); got != tc.code {
					t.Errorf("ResultCode = %d, want %d", got, tc.code)
				}
				if got := classify(input); got != tc.want {
					t.Errorf("classifiers = %v, want %v", got, tc.want)
				}
			}
		})
	}
	for _, input := range []error{nil, sql.ErrConnDone, errors.New("UNIQUE constraint failed: probe.identity")} {
		if got := sqlitedb.ResultCode(input); got != 0 {
			t.Errorf("non-driver ResultCode = %d, want 0", got)
		}
		if got := classify(input); got != [5]bool{} {
			t.Errorf("non-driver classifiers = %v, want all false", got)
		}
	}
}

func classify(err error) [5]bool {
	return [5]bool{
		sqlitedb.IsUniqueViolation(err), sqlitedb.IsIdentityConflict(err),
		sqlitedb.IsForeignKeyViolation(err), sqlitedb.IsCheckViolation(err),
		sqlitedb.IsConstraintViolation(err),
	}
}
