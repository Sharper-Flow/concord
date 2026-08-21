package store

import (
	"context"
	"fmt"
)

// SyncDurable makes every transaction committed on this store before the call
// durable across power loss and operating-system crash, by running a TRUNCATE
// checkpoint of the write-ahead log.
//
// The store commits ordinary writes under synchronous=NORMAL, which does not
// sync the WAL per commit; a committed transaction may roll back after a power
// failure. A checkpoint is the one operation that issues syncs even under
// NORMAL: sqlite.org/wal.html requires the WAL to be synced before its content
// moves into the database file, and the database file to be synced before the
// WAL resets. Because fsync flushes the whole dirty tail of the append-only
// WAL, one checkpoint syncs every prior commit, not only the most recent one.
//
// The durability contract is therefore: acknowledged consequential operations
// are durable across power loss, together with everything committed before
// them. Ordinary writes between consequential boundaries may roll back after
// an OS crash or power loss; the store remains consistent in every case.
//
// TRUNCATE runs to completion before returning and blocks concurrent readers
// of the WAL while it works. The store's pool holds one connection, so
// in-process readers cannot be blocked by it; a separate process holding a
// read transaction during the checkpoint reports busy and this call fails
// retry-safe rather than claiming durability it did not establish.
//
// Call this only after the caller's transaction has committed, never inside
// one. The call is not a transaction and holds no connection open afterwards.
func (s *Store) SyncDurable(ctx context.Context) error {
	const op = "sync_durable"
	if s == nil {
		return newFailure(KindUnavailable, op, "store is nil", false,
			"open the store before syncing")
	}
	var busy, log, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &checkpointed); err != nil {
		return wrapFailure(KindUnavailable, op, "cannot run the durability checkpoint", true,
			"retry once the database is writable; nothing is lost by retrying", err)
	}
	if busy != 0 {
		return newFailure(KindUnavailable, op,
			fmt.Sprintf("durability checkpoint did not complete: busy=%d log=%d checkpointed=%d", busy, log, checkpointed),
			true,
			"retry once concurrent readers finish; the checkpoint resumes where it stopped")
	}
	return nil
}
