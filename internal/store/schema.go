package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// migration is one ordered, immutable step of the schema manifest. A recorded
// migration is never edited: its checksum is compared on every open, so an
// edited step is reported instead of silently diverging from the live schema.
type migration struct {
	Version int
	Name    string
	SQL     string
	// Breaking states that a binary predating this migration can no longer
	// operate the database correctly once it applies. It is true when the
	// migration removes or renames a schema object an older binary may name,
	// or constrains an existing one so that an older binary's writes can be
	// rejected. Creating a table, an index, or a trigger, and adding a
	// nullable or defaulted column, leave an older binary unaffected: it never
	// names what it does not know.
	//
	// One additive shape is not safe under a rolling upgrade: state that later
	// effects must keep true. A column the fold advances, or a new table the
	// write path maintains per effect, needs a writer an older binary does not
	// run, so the state drifts behind its effects with no convergence
	// (migration 108's last_activity_at drifted this way).
	// FoldMaintained names that shape beside the SQL, and
	// scripts/check-migration-compatibility.py binds an advance declaration
	// to this bit.
	//
	// The manifest records this bit per applied migration, and the highest
	// breaking version applied is the database's compatibility floor. A binary
	// that defines that version may open the database even when later
	// migrations have run. scripts/check-migration-compatibility.py classifies
	// the SQL and refuses a declaration its statements contradict, so the bit
	// cannot drift from what the migration does.
	Breaking bool
	// FoldMaintained declares how the fold keeps an added column true or the
	// write path maintains a newly created table per effect. The SQL shape
	// cannot see the Go fold writer, so this declaration is the author's
	// signed claim beside the SQL: the checker holds it to a closed
	// vocabulary and to its consequences, and cannot derive it.
	//
	// "advance": the fold moves the column as later events apply, or the
	// write path maintains a new table per effect. An older binary leaves
	// either state wrong with no convergence — the migration 108 /
	// last_activity_at class. Such a step must declare Breaking: true.
	//
	// "origin": on a column added to a pre-existing table, the fold sets the
	// column once and first derivation wins —
	// the execution_started_at class.
	//
	// Empty: the step declares no fold-maintained state.
	FoldMaintained string
}

// migrationRunner is the narrow surface one migration step needs: read the
// schema it is about to change, then change it.
type migrationRunner interface {
	queryer
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// applyMigration runs one step's SQL.
func applyMigration(ctx context.Context, q migrationRunner, m migration) error {
	if _, err := q.ExecContext(ctx, m.SQL); err != nil {
		return wrapFailure(KindUnavailable, "migrate",
			fmt.Sprintf("migration %d (%s) failed", m.Version, m.Name), false,
			"correct the migration definition", err)
	}
	return nil
}

// migrations is the ordered manifest. Append new steps; never rewrite applied
// ones.
var migrations = []migration{
	{
		Version: 199, Name: "retire_workflow_design_proposal_records", Breaking: true,
		SQL: `
DROP TABLE workflow_design_records;
DROP TABLE workflow_proposal_records;
`,
	},
	{
		Version: 200, Name: "retire_workflow_contract_law_modifications", Breaking: true,
		SQL: `
DROP TABLE workflow_contract_law_modifications;
`,
	},
	{
		Version: 201, Name: "retire_runtime_state_writers", Breaking: true,
		SQL: `
DROP TABLE runtime_state_writers;
`,
	},
}

// schemaManifestDDL creates the manifest itself. It is applied before any
// migration and is not part of the versioned sequence.
const schemaManifestDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL,
    breaking   INTEGER NOT NULL DEFAULT 1
);`

func (m migration) checksum() string {
	sum := sha256.Sum256([]byte(m.SQL))
	return hex.EncodeToString(sum[:])
}

// CurrentSchemaVersion returns the highest checksummed migration known to this
// binary.
func CurrentSchemaVersion() int {
	return migrations[len(migrations)-1].Version
}

// migrationLockBudget bounds how long a concurrent opener waits for another
// process to finish migrating. Every migration lengthens the single migrating
// transaction while the connection busy timeout stays fixed, so one timeout is
// not a safe ceiling: the budget must cover a full manifest apply, not one
// lock acquisition. It is bounded to fail rather than hang.
const migrationLockBudget = 90 * time.Second

const (
	migrationRetryInitialDelay = 25 * time.Millisecond
	migrationRetryMaxDelay     = 500 * time.Millisecond
)

type schemaAbsentHookKey struct{}

func afterSchemaManifestAbsent(ctx context.Context, probe string) {
	if hook, ok := ctx.Value(schemaAbsentHookKey{}).(func(string)); ok {
		hook(probe)
	}
}

type manifestSnapshot struct {
	present    bool
	inspectErr error
	emptyErr   error
	count      int
	countErr   error
	applied    map[int]appliedMigration
	appliedErr error
	// legacy marks a manifest without the compatibility column; legacyErr
	// is its refusal and replaces appliedErr for every caller.
	legacy    bool
	legacyErr error
}

func inspectManifestSnapshot(ctx context.Context, db *sql.DB, probe string) manifestSnapshot {
	var result manifestSnapshot
	tx, err := beginReadTx(ctx, db)
	if err != nil {
		result.inspectErr = err
		return result
	}
	defer func() { _ = tx.Rollback() }()
	var present string
	err = tx.QueryRowContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&present)
	if err == sql.ErrNoRows {
		afterSchemaManifestAbsent(ctx, probe)
		result.emptyErr = requireEmptySchema(ctx, tx)
		return result
	}
	if err != nil {
		result.inspectErr = err
		return result
	}
	result.present = true
	result.countErr = tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&result.count)
	result.applied, result.appliedErr = appliedMigrations(ctx, tx)
	if result.appliedErr != nil && strings.Contains(result.appliedErr.Error(), "no such column: breaking") {
		result.legacy = true
		result.legacyErr = legacyManifestMigrations(ctx, tx)
	}
	return result
}

// migrationScope selects which pending steps one migration pass may apply.
type migrationScope int

const (
	// migrateForOpen applies pending additive steps and stops before the
	// first pending breaking step. A database with no applied
	// migration has no older release to protect and applies every step.
	migrateForOpen migrationScope = iota
	// migrateForUpgrade applies every pending step, breaking ones included.
	// Only concord upgrade reaches it, after the live-session check.
	migrateForUpgrade
	// migrateForUnstamped applies every pending step only after the caller has
	// established that no migration is recorded. The transaction checks that
	// precondition again to prevent concurrent development openers from racing
	// into an existing store.
	migrateForUnstamped
)

// Migrate brings the database up to this binary's schema version, breaking
// steps included. It is idempotent, applies the manifest and all pending
// steps in one transaction, and fails closed on drift or on a database
// written by a newer binary. Open never calls it: an open applies additive
// steps only, through migrateAtOpen, and a breaking step waits for concord
// upgrade.
//
// Concurrent openers of one database are serialized by SQLite. The first
// caller applies the manifest; the rest observe a busy database. Because the
// migrating transaction grows with every added step while the connection busy
// timeout does not, a single blocked attempt is retried within a bounded
// budget, and each retry re-checks the read-only fast path so a waiter returns
// as soon as the winner commits.
func Migrate(ctx context.Context, db *sql.DB, clock ...func() time.Time) error {
	_, err := migrate(ctx, db, migrateForUpgrade, clock...)
	return err
}

func migrateUnstamped(ctx context.Context, db *sql.DB, clock ...func() time.Time) error {
	_, err := migrate(ctx, db, migrateForUnstamped, clock...)
	return err
}

// migrateAtOpen applies the pending additive steps and refuses with
// KindUpgradeRequired when the next pending step is breaking. The additive
// steps it applied stay committed; the refusal names the step and the
// command that applies it.
func migrateAtOpen(ctx context.Context, db *sql.DB, clock ...func() time.Time) error {
	_, err := migrate(ctx, db, migrateForOpen, clock...)
	return err
}

func migrate(ctx context.Context, db *sql.DB, scope migrationScope, clock ...func() time.Time) ([]int, error) {
	deadline := time.Now().Add(migrationLockBudget)
	delay := migrationRetryInitialDelay
	for {
		current, err := migrationManifestCurrent(ctx, db, scope)
		if err != nil {
			return nil, err
		}
		if current {
			return nil, nil
		}
		applied, err := migrateOnce(ctx, db, scope, clock...)
		if err == nil {
			return applied, nil
		}
		if !migrationLockContended(err) || !time.Now().Before(deadline) {
			return nil, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, wrapFailure(KindUnavailable, "migrate", "schema migration was cancelled while waiting for another process", true,
				"retry once the database is writable", ctx.Err())
		case <-timer.C:
		}
		if delay < migrationRetryMaxDelay {
			delay *= 2
			if delay > migrationRetryMaxDelay {
				delay = migrationRetryMaxDelay
			}
		}
	}
}

// migrationManifestCurrent reports whether the database is already at this
// binary's schema version. It reads without opening a write transaction, so a
// routine open of an up-to-date database never contends for the write lock.
// Drift and newer-binary detection still run here: skipping the write
// transaction must never skip the manifest check. An open-scoped pass also
// refuses here when the next pending step is breaking, so a database that
// waits for concord upgrade is refused without a write transaction.
func migrationManifestCurrent(ctx context.Context, db *sql.DB, scope migrationScope) (bool, error) {
	snapshot := inspectManifestSnapshot(ctx, db, "migrationManifestCurrent")
	if snapshot.inspectErr != nil {
		if migrationLockContended(snapshot.inspectErr) {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, "migrate", "cannot inspect the schema manifest", true,
			"confirm the database is readable", snapshot.inspectErr)
	}
	if !snapshot.present {
		return false, snapshot.emptyErr
	}
	if snapshot.legacy {
		return false, snapshot.legacyErr
	}
	if snapshot.appliedErr != nil {
		return false, nil
	}
	applied := snapshot.applied
	if len(applied) == 0 {
		return false, unknownManifest()
	}
	if err := checkManifest(applied); err != nil {
		return false, err
	}
	if scope == migrateForOpen {
		if blocked := openStopsBefore(applied); blocked != nil {
			return false, upgradeRequired(*blocked)
		}
	}
	for _, m := range migrations {
		if _, done := applied[m.Version]; !done {
			return false, nil
		}
	}
	return true, nil
}

func unknownManifest() error {
	return newFailure(KindReadinessUnknown, "migrate", "the store has no usable schema manifest; its provenance is unknown", false, "do not activate; use the operator-owned offline bootstrap")
}

func requireEmptySchema(ctx context.Context, q queryer) error {
	var objects int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
		return wrapFailure(KindUnavailable, "migrate", "cannot inspect the store schema", true, "confirm the database is readable", err)
	}
	if objects != 0 {
		return unknownManifest()
	}
	return nil
}

// unstampedStoreFresh reads the manifest without changing it. An unstamped
// binary may migrate only a store with no applied migrations. An up-to-date
// existing store is admitted without a migration pass.
func unstampedStoreFresh(ctx context.Context, db *sql.DB) (map[int]appliedMigration, bool, error) {
	snapshot := inspectManifestSnapshot(ctx, db, "unstampedStoreFresh")
	if snapshot.inspectErr != nil {
		return nil, false, wrapFailure(KindUnavailable, "open", "cannot inspect the schema manifest", true,
			"confirm the database is readable", snapshot.inspectErr)
	}
	if !snapshot.present {
		return map[int]appliedMigration{}, true, snapshot.emptyErr
	}
	if snapshot.countErr != nil {
		return nil, false, wrapFailure(KindUnavailable, "open", "cannot count applied migrations", true,
			"confirm the database is readable", snapshot.countErr)
	}
	if snapshot.count == 0 {
		return nil, false, unknownManifest()
	}
	if snapshot.legacy {
		return nil, false, snapshot.legacyErr
	}
	if snapshot.appliedErr != nil {
		return nil, false, snapshot.appliedErr
	}
	applied := snapshot.applied
	if err := checkManifest(applied); err != nil {
		return nil, false, err
	}
	for _, m := range migrations {
		if _, done := applied[m.Version]; !done {
			return nil, false, unstampedMigrationRefused(applied)
		}
	}
	return applied, false, nil
}

// unstampedRefusal is what an unstamped build reports when it refuses to
// migrate an existing store: the applied head and the pending tail
// this build defines. No manifest row names the release that applied it.
type unstampedRefusal struct {
	SchemaVersion    int
	HeadName         string
	FirstPending     int
	FirstPendingName string
	Pending          int
	PendingBreaking  int
}

// UnstampedRecovery is the isolation route every unstamped migration refusal
// names. The readiness plan blocks on the same rule without a
// refusal, so the command layer names it from here.
const UnstampedRecovery = "set CONCORD_DB_PATH to a fresh path outside any repository, then retry"

func unstampedMigrationRefused(applied map[int]appliedMigration) *Failure {
	return unstampedRefusalFacts(applied).failure("unstamped development build refuses schema migration for an existing store")
}

func (refusal unstampedRefusal) failure(lead string) *Failure {
	return newFailure(KindInvalidOperation, "open", fmt.Sprintf("%s at schema version %d (%q); this build would apply %d pending migration(s) from %d %q, %d breaking; the store does not record which release applied its migrations",
		lead, refusal.SchemaVersion, refusal.HeadName, refusal.Pending, refusal.FirstPending, refusal.FirstPendingName, refusal.PendingBreaking), true, UnstampedRecovery)
}

func unstampedRefusalFacts(applied map[int]appliedMigration) unstampedRefusal {
	var refusal unstampedRefusal
	for recorded := range applied {
		refusal.SchemaVersion = max(refusal.SchemaVersion, recorded)
	}
	for _, m := range migrations {
		if m.Version == refusal.SchemaVersion {
			refusal.HeadName = m.Name
		}
		if _, done := applied[m.Version]; done {
			continue
		}
		if refusal.Pending == 0 {
			refusal.FirstPending, refusal.FirstPendingName = m.Version, m.Name
		}
		refusal.Pending++
		if m.Breaking {
			refusal.PendingBreaking++
		}
	}
	return refusal
}

// openStopsBefore reports the breaking step an open-scoped pass may not
// apply: the first pending step, when it is breaking and the database already
// carries applied migrations. A fresh database applies every step.
func openStopsBefore(applied map[int]appliedMigration) *migration {
	if len(applied) == 0 {
		return nil
	}
	for i := range migrations {
		if _, done := applied[migrations[i].Version]; done {
			continue
		}
		if migrations[i].Breaking {
			return &migrations[i]
		}
		return nil
	}
	return nil
}

// pendingBreaking lists, in order, the breaking steps the database has not
// applied, whatever additive steps sit between them. Upgrade gates on them; a
// fresh database has none to gate.
func pendingBreaking(applied map[int]appliedMigration) []migration {
	if len(applied) == 0 {
		return nil
	}
	var pending []migration
	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			continue
		}
		if m.Breaking {
			pending = append(pending, m)
		}
	}
	return pending
}

// migrationLockContended reports whether a failure means another process holds
// the write lock. It never treats drift or an unsupported schema as contention.
func migrationLockContended(err error) bool {
	var failure *Failure
	if errors.As(err, &failure) {
		if failure.Kind != KindUnavailable || failure.EffectPossible {
			return false
		}
		err = failure.Err
	}
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "database is locked") || strings.Contains(err.Error(), "database table is locked")
}

// upgradeRequired is the open-time refusal for a pending breaking step. It
// writes nothing and names the command that applies the step.
func upgradeRequired(m migration) *Failure {
	return newFailure(KindUpgradeRequired, "open",
		fmt.Sprintf("the database stops before breaking migration %d (%s); this binary defines schema version %d and never applies a breaking migration at open",
			m.Version, m.Name, CurrentSchemaVersion()),
		true, fmt.Sprintf("run concord upgrade; it refuses while a live session holds a release that predates migration %d", m.Version))
}

func migrateOnce(ctx context.Context, db *sql.DB, scope migrationScope, clock ...func() time.Time) ([]int, error) {
	write, err := beginWriteTx(ctx, db, nil, schemaWriteAdmission)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "migrate", "cannot begin schema migration", true,
			"retry once the database is writable", err)
	}
	tx := write.Tx
	rollback := func(cause error) ([]int, error) {
		write.finish(&cause)
		return nil, cause
	}
	var manifestExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='schema_migrations')`).Scan(&manifestExists); err != nil {
		return rollback(err)
	}
	if !manifestExists {
		if err := requireEmptySchema(ctx, tx); err != nil {
			return rollback(err)
		}
	} else {
		applied, err := appliedMigrations(ctx, tx)
		if err != nil && strings.Contains(err.Error(), "no such column: breaking") {
			err = legacyManifestMigrations(ctx, tx)
		}
		if err != nil {
			return rollback(err)
		}
		if len(applied) == 0 {
			return rollback(unknownManifest())
		}
		if err := checkManifest(applied); err != nil {
			return rollback(err)
		}
	}
	if scope == migrateForUnstamped {
		stop, err := inspectUnstampedManifest(ctx, tx)
		if stop || err != nil {
			return rollback(err)
		}
	}

	if _, err := tx.ExecContext(ctx, schemaManifestDDL); err != nil {
		return rollback(wrapFailure(KindUnavailable, "migrate", "cannot create the schema manifest", true,
			"check database permissions", err))
	}

	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	if err := checkManifest(applied); err != nil {
		return rollback(err)
	}
	if !manifestExists {
		if _, err := tx.ExecContext(ctx, schemaBaseline198); err != nil {
			return rollback(wrapFailure(KindUnavailable, "migrate", "cannot initialize the fresh store baseline", true, "check database permissions", err))
		}
	}

	versions, stopped, err := applyPendingMigrations(ctx, tx, scope, applied, clock...)
	if err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, ordinaryCommitFailure(ctx, write, "migrate", "cannot commit schema migration", err)
	}
	if stopped != nil {
		return nil, upgradeRequired(*stopped)
	}
	return versions, nil
}

func inspectUnstampedManifest(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			count = 0
		} else {
			return false, wrapFailure(KindUnavailable, "migrate", "cannot inspect the schema manifest", true,
				"confirm the database is readable", err)
		}
	}
	if count > 0 {
		applied, err := appliedMigrations(ctx, tx)
		if err != nil {
			return false, err
		}
		for _, m := range migrations {
			if _, done := applied[m.Version]; !done {
				return false, unstampedMigrationRefused(applied)
			}
		}
		return true, nil
	}
	return false, nil
}

func applyPendingMigrations(ctx context.Context, tx *sql.Tx, scope migrationScope, applied map[int]appliedMigration, clock ...func() time.Time) ([]int, *migration, error) {
	var versions []int
	var stopped *migration
	for i := range migrations {
		m := migrations[i]
		if _, done := applied[m.Version]; done {
			continue
		}
		if scope == migrateForOpen && m.Breaking && len(applied) > 0 {
			stopped = &migrations[i]
			break
		}
		if err := applyMigration(ctx, tx, m); err != nil {
			return nil, nil, err
		}
		if err := advanceStoreRevision(ctx, tx); err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum, applied_at, breaking) VALUES (?, ?, ?, ?, ?)`,
			m.Version, m.Name, m.checksum(), nowFromClock(firstClock(clock)).Format(time.RFC3339Nano), m.Breaking); err != nil {
			return nil, nil, wrapFailure(KindUnavailable, "migrate",
				fmt.Sprintf("cannot record migration %d (%s)", m.Version, m.Name), true,
				"retry once the database is writable", err)
		}
		versions = append(versions, m.Version)
	}
	return versions, stopped, nil
}

// appliedMigration is one manifest row: what was applied, and whether it left
// an older binary able to operate the database.
type appliedMigration struct {
	Checksum string
	Breaking bool
}

// appliedMigrations reads the manifest through the caller's queryer, so the
// read opens no write transaction.
func appliedMigrations(ctx context.Context, tx queryer) (map[int]appliedMigration, error) {
	return readManifest(ctx, tx, `SELECT version, checksum, breaking FROM schema_migrations ORDER BY version`)
}

// legacyManifestMigrations reads a manifest that predates the breaking
// column. A manifest claiming a supported version without the column has
// unknown provenance and is never repaired in place.
func legacyManifestMigrations(ctx context.Context, tx queryer) error {
	applied, err := readManifest(ctx, tx, `SELECT version, checksum, 1 FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		return unknownManifest()
	}
	for version := range applied {
		if version >= 199 {
			return unknownManifest()
		}
	}
	return checkManifest(applied)
}

func readManifest(ctx context.Context, tx queryer, query string) (map[int]appliedMigration, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "migrate", "cannot read the schema manifest", true,
			"confirm the database is readable", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var checksum string
		var breaking bool
		if err := rows.Scan(&version, &checksum, &breaking); err != nil {
			return nil, wrapFailure(KindUnavailable, "migrate", "cannot read a manifest row", true,
				"confirm the database is readable", err)
		}
		applied[version] = appliedMigration{Checksum: checksum, Breaking: breaking}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "migrate", "cannot read the schema manifest", true,
			"confirm the database is readable", err)
	}
	return applied, nil
}

// checkManifest compares manifest against this binary's
// definition. Both directions matter: an edited historical step means the live
// schema no longer matches the code, and an unknown newer step means the
// database was written by a binary that knows more than this one.
// The rule is the compatibility floor, not version equality. A migration this
// binary does not define is admitted when the database recorded it as
// additive, because an additive migration only adds schema objects and this
// binary never names them. A breaking one is refused: it removed or
// constrained something this binary still writes.
//
// The floor is the highest breaking version applied, so one breaking migration
// closes the door for every binary older than it, and later additive
// migrations do not move it further. Nothing here blocks an upgrade; a newer
// binary defines everything the database records and migrates forward.
//
// Either refusal names a version chosen across the whole map rather than the
// first one the range happens to reach: the earliest drifted migration, and
// the highest breaking version this binary does not define. Go randomizes map
// order, so returning from inside the range made the reported number vary
// between runs against one unchanged database, and the operator read that as
// the database moving under them.
func checkManifest(applied map[int]appliedMigration) error {
	if len(applied) > 0 {
		if _, ok := applied[199]; !ok {
			return newFailure(KindSchemaUnsupported, "migrate", "the existing store is below the minimum supported schema version 199 or is missing its version 199 manifest entry", false, "upgrade the store with bridge release v67.0.0 before using this release")
		}
	}
	// Retained migrations apply in order inside one transaction, so a recorded
	// step after an unrecorded one means the manifest was altered.
	missing := 0
	for _, m := range migrations {
		if _, ok := applied[m.Version]; !ok {
			if missing == 0 {
				missing = m.Version
			}
			continue
		}
		if missing != 0 {
			return newFailure(KindSchemaDrift, "migrate", fmt.Sprintf("the schema manifest records migration %d without migration %d", m.Version, missing), false, "restore a verified store with an intact schema manifest")
		}
	}
	known := make(map[int]migration, len(migrations))
	for _, m := range migrations {
		known[m.Version] = m
	}

	floor := 0
	drifted := 0
	for version, row := range applied {
		if version < 199 {
			continue
		}
		m, ok := known[version]
		if !ok {
			if row.Breaking && version > floor {
				floor = version
			}
			continue
		}
		if m.checksum() != row.Checksum {
			if drifted == 0 || version < drifted {
				drifted = version
			}
		}
	}
	if drifted > 0 {
		return newFailure(KindSchemaDrift, "migrate",
			fmt.Sprintf("migration %d (%s) no longer matches its recorded checksum", drifted, known[drifted].Name),
			false, "restore the original migration definition; applied migrations are immutable")
	}
	if floor > 0 {
		return newFailure(KindSchemaUnsupported, "migrate",
			fmt.Sprintf("the database requires a binary that defines schema version %d; this binary defines %d, and migration %d changed the schema in a way this binary cannot operate",
				floor, CurrentSchemaVersion(), floor),
			true, "upgrade to a binary that defines this schema version and retry")
	}
	return nil
}
