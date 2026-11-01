package store

import (
	"database/sql"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// maskedError hides the driver's message text while preserving the error
// chain, proving classification cannot depend on message wording.
type maskedError struct{ err error }

func (m maskedError) Error() string { return "driver detail withheld" }
func (m maskedError) Unwrap() error { return m.err }

func constraintProbeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE probe_unique(a TEXT UNIQUE); INSERT INTO probe_unique VALUES('x')`); err != nil {
		t.Fatal(err)
	}
	return db
}

// Constraint classification must derive from the driver's typed result
// code, not from message text: a wording change in the driver would
// otherwise silently reclassify constraint failures as retryable
// availability failures.
func TestUniqueViolationClassificationIsMessageIndependent(t *testing.T) {
	t.Parallel()
	db := constraintProbeDB(t)
	_, err := db.Exec(`INSERT INTO probe_unique VALUES('x')`)
	if err == nil {
		t.Fatal("expected a unique violation from the driver")
	}
	if !sqlitedb.IsUniqueViolation(err) {
		t.Fatalf("stock driver error must classify as unique violation: %v", err)
	}
	if !sqlitedb.IsUniqueViolation(maskedError{err: err}) {
		t.Fatal("classification must survive message masking (typed code, not text)")
	}
	if sqlitedb.IsUniqueViolation(sql.ErrConnDone) {
		t.Fatal("unrelated errors must not classify as unique violations")
	}
}

func foreignKeyProbeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE probe_parent(id TEXT PRIMARY KEY);
CREATE TABLE probe_child(parent_id TEXT REFERENCES probe_parent(id));
CREATE TABLE probe_check(value INTEGER CHECK(value > 0));`); err != nil {
		t.Fatal(err)
	}
	return db
}

// Foreign-key, check, and any-constraint classification must equally
// derive from typed codes, and constraint kinds must not cross-classify.
func TestConstraintClassificationByKindIsTyped(t *testing.T) {
	t.Parallel()
	db := foreignKeyProbeDB(t)

	_, fkErr := db.Exec(`INSERT INTO probe_child VALUES('missing')`)
	if fkErr == nil {
		t.Fatal("expected a foreign key violation from the driver")
	}
	if !sqlitedb.IsForeignKeyViolation(fkErr) || !sqlitedb.IsForeignKeyViolation(maskedError{err: fkErr}) {
		t.Fatalf("foreign key violation must classify typed: %v", fkErr)
	}
	if sqlitedb.IsUniqueViolation(fkErr) || sqlitedb.IsCheckViolation(fkErr) {
		t.Fatalf("foreign key violation must not cross-classify: %v", fkErr)
	}

	_, checkErr := db.Exec(`INSERT INTO probe_check VALUES(-1)`)
	if checkErr == nil {
		t.Fatal("expected a check violation from the driver")
	}
	if !sqlitedb.IsCheckViolation(checkErr) || !sqlitedb.IsCheckViolation(maskedError{err: checkErr}) {
		t.Fatalf("check violation must classify typed: %v", checkErr)
	}
	if sqlitedb.IsUniqueViolation(checkErr) || sqlitedb.IsForeignKeyViolation(checkErr) {
		t.Fatalf("check violation must not cross-classify: %v", checkErr)
	}

	if !sqlitedb.IsConstraintViolation(fkErr) || !sqlitedb.IsConstraintViolation(checkErr) {
		t.Fatal("every constraint violation must satisfy IsConstraintViolation")
	}
	if sqlitedb.IsConstraintViolation(sql.ErrConnDone) || sqlitedb.IsConstraintViolation(nil) {
		t.Fatal("unrelated errors must not classify as constraint violations")
	}
}
