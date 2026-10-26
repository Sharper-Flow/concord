package sqlitedb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// WriteGuard opens depth-counted access to current rows on one caller-owned
// transaction. Enter and Close must pair before commit. Do not copy an active
// guard. Rollback removes its uncommitted row with the other transaction writes.
type WriteGuard struct {
	tx    *sql.Tx
	depth int
}

func NewWriteGuard(tx *sql.Tx) WriteGuard { return WriteGuard{tx: tx} }

func (g *WriteGuard) Active() bool { return g.depth > 0 }

// Enter inserts the guard only at the outermost level. A collision refuses
// instead of absorbing a stranded guard into an ordinary write scope.
func (g *WriteGuard) Enter(ctx context.Context) error {
	if g.depth > 0 {
		g.depth++
		return nil
	}
	if _, err := g.tx.ExecContext(ctx, `INSERT INTO write_guard (active) VALUES (1)`); err != nil {
		if isWriteGuardCollision(err) {
			return failure(InvariantViolation, "fold",
				"a committed write_guard row already guards the current rows, so no write scope owns it", false,
				"stop concord processes and run concord restore from a verified backup into a new path, then swap the restored database into place", nil)
		}
		return failure(Unavailable, "fold", "cannot enable current-row write guard", true,
			"retry once the database is writable", err)
	}
	g.depth = 1
	return nil
}

// Close leaves nested callers' protection in place until the outermost close.
func (g *WriteGuard) Close(ctx context.Context) error {
	if g.depth == 0 {
		return nil
	}
	g.depth--
	if g.depth > 0 {
		return nil
	}
	return LeaveWrite(ctx, g.tx)
}

// EnterWrite enables flat current-row writers. Unlike a scoped Enter, these
// writers share an existing row until LeaveWrite; store open has already refused
// any committed guard.
func EnterWrite(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO write_guard (active) VALUES (1)`); err != nil {
		return failure(Unavailable, "fold", "cannot enable current-row write guard", true,
			"retry once the database is writable", err)
	}
	return nil
}

func LeaveWrite(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM write_guard WHERE active = 1`); err != nil {
		return failure(Unavailable, "fold", "cannot disable current-row write guard", true,
			"retry once the database is writable", err)
	}
	return nil
}

// RefuseStrandedWriteGuard runs before ordinary store use. A committed guard
// means current rows may be inconsistent and require verified-backup recovery.
func RefuseStrandedWriteGuard(ctx context.Context, db *sql.DB) error {
	var stranded int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM write_guard`).Scan(&stranded); err != nil {
		return failure(Unavailable, "open", "cannot inspect the write guard", true,
			"confirm the database is readable", err)
	}
	if stranded == 0 {
		return nil
	}
	return failure(InvariantViolation, "open",
		fmt.Sprintf("%d committed write_guard row(s) guard the current rows, and no write scope owns them", stranded), false,
		"stop concord processes and run concord restore from a verified backup into a new path, then swap the restored database into place", nil)
}

func isWriteGuardCollision(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "UNIQUE constraint failed") && strings.Contains(message, "write_guard.active")
}
