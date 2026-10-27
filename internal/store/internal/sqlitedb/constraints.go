package sqlitedb

import (
	"errors"

	sqlite "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// ResultCode returns the typed SQLite result code carried by err, or
// 0 when err is not a driver error. Driver message text is not a stability
// contract; classification at this boundary uses the typed code.
func ResultCode(err error) int {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return 0
	}
	return sqliteErr.Code()
}

// IsUniqueViolation reports whether err is a uniqueness conflict.
func IsUniqueViolation(err error) bool {
	return ResultCode(err) == sqlitelib.SQLITE_CONSTRAINT_UNIQUE
}

// IsIdentityConflict reports whether err is a row-identity collision, which
// SQLite raises under a different code depending on whether the colliding
// column is a UNIQUE index or the table's primary key. A fold that inserts a
// row keyed by a caller-supplied id has to treat both as "this id already
// exists"; matching only the UNIQUE code would misreport a primary-key
// collision as an unrelated storage failure.
func IsIdentityConflict(err error) bool {
	code := ResultCode(err)
	return code == sqlitelib.SQLITE_CONSTRAINT_UNIQUE || code == sqlitelib.SQLITE_CONSTRAINT_PRIMARYKEY
}

// IsForeignKeyViolation reports whether err is a foreign-key conflict.
func IsForeignKeyViolation(err error) bool {
	return ResultCode(err) == sqlitelib.SQLITE_CONSTRAINT_FOREIGNKEY
}

// IsCheckViolation reports whether err is a CHECK constraint conflict.
func IsCheckViolation(err error) bool {
	return ResultCode(err) == sqlitelib.SQLITE_CONSTRAINT_CHECK
}

// IsConstraintViolation reports whether err is any SQLite constraint
// conflict (unique, primary key, foreign key, check, not-null).
func IsConstraintViolation(err error) bool {
	code := ResultCode(err)
	return code != 0 && code&0xff == sqlitelib.SQLITE_CONSTRAINT
}
