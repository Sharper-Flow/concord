package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

type CompactPages struct {
	Pages         int64  `json:"pages"`
	FreePages     int64  `json:"free_pages"`
	PageSize      int64  `json:"page_size"`
	Bytes         int64  `json:"bytes"`
	ApplicationID int64  `json:"application_id"`
	UserVersion   int64  `json:"user_version"`
	AutoVacuum    int64  `json:"auto_vacuum"`
	Encoding      string `json:"encoding"`
	JournalMode   string `json:"journal_mode"`
}

type CompactReport struct {
	Before               CompactPages `json:"before"`
	After                CompactPages `json:"after"`
	AvailableBytes       uint64       `json:"available_bytes"`
	RequiredBytes        uint64       `json:"required_bytes"`
	VerifyDurationMS     int64        `json:"verify_duration_ms"`
	LockDurationMS       int64        `json:"lock_duration_ms"`
	WriteDurationMS      int64        `json:"write_duration_ms"`
	CheckpointDurationMS int64        `json:"checkpoint_duration_ms"`
	WriteBackStarted     bool         `json:"write_back_started"`
}

// walFrameHeaderBytes and walHeaderBytes size the WAL that the write-back
// fills with every snapshot page before its checkpoint.
const (
	walFrameHeaderBytes = 24
	walHeaderBytes      = 32
)

// Compact requires the caller to hold the stopped-session maintenance boundary.
// It verifies the supplied snapshot and a scratch restore before any write.
// One connection holds SQLite's exclusive lock on the store from the row
// comparison through the verified result, so no other connection can write
// between the comparison and the write-back. WriteBackStarted tells the
// boundary owner to retain exclusion on any error.
func Compact(ctx context.Context, path, snapshot string) (report CompactReport, err error) {
	defer func() {
		if err != nil {
			recovery := "retain the snapshot and retry after resolving the refusal"
			if report.WriteBackStarted {
				recovery = "keep admission fenced and recover using the held verified snapshot"
			}
			failure := wrapFailure(KindUnavailable, "compact", "store compaction failed", !report.WriteBackStarted, recovery, err)
			failure.EffectPossible = report.WriteBackStarted
			err = failure
		}
	}()
	if err := compactAdmitFiles(path, snapshot); err != nil {
		return report, err
	}
	start := time.Now()
	manifest, err := VerifyBackup(ctx, snapshot)
	if err != nil {
		return report, fmt.Errorf("verify compact snapshot: %w", err)
	}
	if err := verifyBackupRestore(ctx, snapshot, manifest); err != nil {
		return report, fmt.Errorf("prove compact snapshot restore: %w", err)
	}
	lockStart := time.Now()
	live, err := sqlitedb.OpenExclusive(ctx, path)
	if err != nil {
		return report, fmt.Errorf("lock the store for compaction: %w", err)
	}
	defer func() {
		closeErr := live.Close()
		report.LockDurationMS = time.Since(lockStart).Milliseconds()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("release the compacted store: %w", closeErr)
		}
	}()
	before, image, err := compactInspectPair(ctx, live.Conn, path, snapshot)
	if err != nil {
		return report, fmt.Errorf("compare compact snapshot before write-back: %w", err)
	}
	report.Before = before
	if !compactSameHeader(before, image) || before.JournalMode != "wal" || image.FreePages != 0 {
		return report, fmt.Errorf("snapshot header or free-page count does not match maintenance requirements")
	}
	if report.AvailableBytes, report.RequiredBytes, err = compactHeadroom(path, image); err != nil {
		return report, err
	}
	report.VerifyDurationMS = time.Since(start).Milliseconds()
	if before.FreePages == 0 {
		report.After = before
		return report, nil
	}
	if report.AvailableBytes < report.RequiredBytes {
		return report, fmt.Errorf("write-back needs %d bytes of WAL space beside the store; %d are available", report.RequiredBytes, report.AvailableBytes)
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	report.WriteBackStarted = true
	start = time.Now()
	if err := live.RestoreFrom(ctx, snapshot); err != nil {
		return report, fmt.Errorf("compact write-back: %w", err)
	}
	report.WriteDurationMS = time.Since(start).Milliseconds()
	start = time.Now()
	if err := compactCheckpoint(ctx, live.Conn); err != nil {
		return report, fmt.Errorf("compact checkpoint: %w", err)
	}
	report.CheckpointDurationMS = time.Since(start).Milliseconds()
	after, compacted, err := compactInspectPair(ctx, live.Conn, path, snapshot)
	if err != nil {
		return report, fmt.Errorf("verify compact write-back: %w", err)
	}
	report.After = after
	if after.FreePages != 0 || after.Pages != compacted.Pages || !compactSameHeader(after, before) || after.Bytes != after.Pages*after.PageSize {
		return report, fmt.Errorf("compact write-back page or header verification failed")
	}
	return report, nil
}

func compactAdmitFiles(path, snapshot string) error {
	if err := AdmitStorePath(path); err != nil {
		return err
	}
	if err := validateBackupDestination(path, snapshot); err != nil {
		return err
	}
	targetInfo, err := os.Stat(path)
	if err != nil {
		return err
	}
	snapshotInfo, err := os.Stat(snapshot)
	if err != nil {
		return err
	}
	if !targetInfo.Mode().IsRegular() || !snapshotInfo.Mode().IsRegular() || os.SameFile(targetInfo, snapshotInfo) {
		return fmt.Errorf("target and snapshot must be distinct regular files")
	}
	if stat, ok := targetInfo.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		return fmt.Errorf("target must have exactly one filesystem link")
	}
	return nil
}

// compactSameHeader compares the header settings a write-back must keep.
func compactSameHeader(a, b CompactPages) bool {
	return a.PageSize == b.PageSize && a.ApplicationID == b.ApplicationID && a.UserVersion == b.UserVersion &&
		a.AutoVacuum == b.AutoVacuum && a.Encoding == b.Encoding && a.JournalMode == b.JournalMode
}

// compactHeadroom reports the space available beside the store and the WAL
// space the write-back needs to hold every snapshot page.
func compactHeadroom(path string, image CompactPages) (available, required uint64, err error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &fs); err != nil {
		return 0, 0, fmt.Errorf("read disk headroom: %w", err)
	}
	if fs.Bsize <= 0 {
		return 0, 0, fmt.Errorf("invalid filesystem block size")
	}
	if image.Pages < 1 || image.PageSize < 1 {
		return 0, 0, fmt.Errorf("snapshot reports %d pages of %d bytes", image.Pages, image.PageSize)
	}
	available = fs.Bavail * uint64(fs.Bsize)
	required = uint64(image.Pages)*uint64(image.PageSize+walFrameHeaderBytes) + walHeaderBytes //nolint:gosec // Both counts are checked positive above.
	return available, required, nil
}

// compactStore is the locked store connection's read surface.
type compactStore interface {
	sqlitedb.Queryer
	sqlitedb.BeginTxSource
}

func compactInspectPair(ctx context.Context, left compactStore, path, snapshot string) (CompactPages, CompactPages, error) {
	var a, b CompactPages
	right, err := sqlitedb.Open(sqlitedb.ReadOnlyDataSource(snapshot))
	if err != nil {
		return a, b, err
	}
	defer func() { _ = right.Close() }()
	if err := compareCompactState(ctx, left, right); err != nil {
		return a, b, err
	}
	for _, db := range []sqlitedb.Queryer{left, right} {
		integrity, quick, foreign, err := sqlitedb.VerifySQLiteTriple(ctx, db)
		if err != nil || integrity != "ok" || quick != "ok" || len(foreign) != 0 {
			return a, b, fmt.Errorf("SQLite verification failed: %s / %s / %v: %v", integrity, quick, foreign, err)
		}
	}
	ld, err := sqlitedb.CurrentStateDigest(ctx, left)
	if err != nil {
		return a, b, err
	}
	rd, err := sqlitedb.CurrentStateDigest(ctx, right)
	if err != nil {
		return a, b, err
	}
	if ld != rd {
		return a, b, fmt.Errorf("logical state digest differs")
	}
	if a, err = compactReadPages(ctx, left, path); err != nil {
		return a, b, err
	}
	if b, err = compactReadPages(ctx, right, snapshot); err != nil {
		return a, b, err
	}
	return a, b, nil
}

func compactReadPages(ctx context.Context, db sqlitedb.Queryer, path string) (p CompactPages, err error) {
	for _, q := range []struct {
		name string
		dest any
	}{
		{"page_count", &p.Pages}, {"freelist_count", &p.FreePages}, {"page_size", &p.PageSize}, {"application_id", &p.ApplicationID}, {"user_version", &p.UserVersion}, {"auto_vacuum", &p.AutoVacuum}, {"encoding", &p.Encoding}, {"journal_mode", &p.JournalMode},
	} {
		if err := db.QueryRowContext(ctx, "PRAGMA "+q.name).Scan(q.dest); err != nil {
			return p, err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return p, err
	}
	p.Bytes = info.Size()
	return p, nil
}

// compactCheckpoint runs on the lock holder; no other connection can read
// the WAL, so a complete checkpoint truncates it and the database file.
func compactCheckpoint(ctx context.Context, live sqlitedb.Queryer) error {
	var busy, log, checkpointed int
	if err := live.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil {
		return err
	}
	if busy != 0 || log != 0 || checkpointed != 0 {
		return fmt.Errorf("checkpoint incomplete: %d/%d/%d", busy, log, checkpointed)
	}
	return nil
}

func compactQuote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// compareCompactState compares the complete schema and every stored row,
// including physical rowids where SQLite exposes them.
func compareCompactState(ctx context.Context, left, right sqlitedb.BeginTxSource) error {
	l, err := sqlitedb.BeginRead(ctx, left)
	if err != nil {
		return err
	}
	defer l.Rollback()
	r, err := sqlitedb.BeginRead(ctx, right)
	if err != nil {
		return err
	}
	defer r.Rollback()
	if err := compactCompareRows(ctx, l, r, `SELECT type,name,tbl_name,sql FROM main.sqlite_schema ORDER BY type COLLATE BINARY,name COLLATE BINARY`); err != nil {
		return fmt.Errorf("schema differs: %w", err)
	}
	inventory, err := compactTables(ctx, l)
	if err != nil {
		return err
	}
	for _, table := range inventory {
		query, err := compactTableQuery(ctx, l, table)
		if err != nil {
			return err
		}
		if err := compactCompareRows(ctx, l, r, query); err != nil {
			return fmt.Errorf("table %q differs: %w", table.name, err)
		}
	}
	return nil
}

type compactTable struct {
	name         string
	withoutRowid bool
	kind         string
}

func compactTables(ctx context.Context, tx *sql.Tx) ([]compactTable, error) {
	tables, err := tx.QueryContext(ctx, `SELECT name,wr,type FROM pragma_table_list WHERE schema='main' ORDER BY name COLLATE BINARY`)
	if err != nil {
		return nil, err
	}
	var inventory []compactTable
	for tables.Next() {
		var t compactTable
		if err := tables.Scan(&t.name, &t.withoutRowid, &t.kind); err != nil {
			_ = tables.Close()
			return nil, err
		}
		if t.name != "sqlite_schema" && t.name != "sqlite_temp_schema" {
			inventory = append(inventory, t)
		}
	}
	if err := tables.Err(); err != nil {
		_ = tables.Close()
		return nil, err
	}
	if err := tables.Close(); err != nil {
		return nil, err
	}
	return inventory, nil
}

func compactTableQuery(ctx context.Context, tx *sql.Tx, table compactTable) (string, error) {
	if table.kind != "table" {
		return "", fmt.Errorf("cannot compare table %q of type %q", table.name, table.kind)
	}
	name := compactQuote(table.name)
	probe, err := tx.QueryContext(ctx, "SELECT * FROM main."+name+" LIMIT 0") //nolint:gosec // The identifier comes from sqlite_schema and is quoted by compactQuote.
	if err != nil {
		return "", err
	}
	columns, err := probe.Columns()
	if rowsErr := probe.Err(); err == nil {
		err = rowsErr
	}
	closeErr := probe.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	prefix := ""
	var order []string
	if !table.withoutRowid {
		for _, alias := range []string{"rowid", "_rowid_", "oid"} {
			shadowed := false
			for _, column := range columns {
				shadowed = shadowed || strings.EqualFold(column, alias)
			}
			if !shadowed {
				prefix, order = alias+",", []string{alias}
				break
			}
		}
		if prefix == "" {
			return "", fmt.Errorf("table %q shadows every rowid alias", table.name)
		}
	} else {
		for _, column := range columns {
			q := compactQuote(column)
			order = append(order, "typeof("+q+")", q+" COLLATE BINARY")
		}
	}
	if len(order) == 0 {
		return "", fmt.Errorf("table %q has no orderable columns", table.name)
	}
	return "SELECT " + prefix + "* FROM main." + name + " ORDER BY " + strings.Join(order, ","), nil
}

func compactCompareRows(ctx context.Context, left, right *sql.Tx, query string) error {
	l, err := left.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer l.Close()
	r, err := right.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer r.Close()
	columns, err := l.Columns()
	if err != nil {
		return err
	}
	otherColumns, err := r.Columns()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(columns, otherColumns) {
		return fmt.Errorf("columns differ")
	}
	for {
		ln, rn := l.Next(), r.Next()
		if !ln || !rn {
			if err := l.Err(); err != nil {
				return err
			}
			if err := r.Err(); err != nil {
				return err
			}
			if ln != rn {
				return fmt.Errorf("row counts differ")
			}
			return nil
		}
		lv, rv := make([]any, len(columns)), make([]any, len(columns))
		lp, rp := make([]any, len(columns)), make([]any, len(columns))
		for i := range columns {
			lp[i], rp[i] = &lv[i], &rv[i]
		}
		if err := l.Scan(lp...); err != nil {
			return err
		}
		if err := r.Scan(rp...); err != nil {
			return err
		}
		if !reflect.DeepEqual(lv, rv) {
			return fmt.Errorf("row values differ")
		}
	}
}
