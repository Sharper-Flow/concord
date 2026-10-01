package store

import (
	"context"
	"database/sql"
)

// recordLanding runs one landing verb's transaction shape: the store must be
// open, the landing must carry the work, session, landed directory, and host
// pid, and the verb's core fold plus its commit own every other refusal. The
// op string names the verb in every failure this wrapper writes, so a
// refusal names the route that refused.
func recordLanding[T any](s *Store, ctx context.Context, op, workID, sessionRef, landedDirectory string, hostPID int, core func(context.Context, *sql.Tx) (T, error)) (T, error) {
	var zero T
	if s == nil || s.db == nil {
		return zero, newFailure(KindUnavailable, op, "store is not open", false, "open the authority database")
	}
	if workID == "" || sessionRef == "" || landedDirectory == "" {
		return zero, newFailure(KindInvalidOperation, op, "landing is missing the work, session, or landed directory", false, "supply the work id, session ref, and verified landed path")
	}
	if hostPID <= 0 {
		return zero, newFailure(KindInvalidOperation, op, "landing requires the host process pid", false, "supply the adapter's process.pid with the landing request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, wrapFailure(KindUnavailable, op, "cannot begin landing", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	out, err := core(ctx, tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, wrapFailure(KindUnavailable, op, "cannot commit landing", true, "retry the same landing", err)
	}
	return out, nil
}
