package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sharper-flow/concord/internal/version"
)

// PlanUpgradeReadiness inspects the store at path and answers, without
// applying anything, whether this binary may activate against it (CON-807).
// It creates no store, changes no permissions, applies no migration, and
// repairs no manifest: one plain read-only SQLite connection (mode=ro) on
// the existing store answers every question, and SQLite's own WAL and
// shared-memory coordination files may appear beside it. Unknown or
// unreadable readiness fails closed with KindReadinessUnknown: the caller
// must not guess an activation decision.
//
// All queries run inside one read transaction over the single connection,
// so the answer describes one snapshot of the store.
//
// The returned commands are descriptive defaults, not exact commands: only a
// caller that knows a release identity can name the exact binary path and
// installer invocation. cmd/concord replaces both fields before the plan
// reaches an operator, and the installer records the exact commands beside
// the prepared candidate.
//
// Schema-floor admission is necessary, not sufficient. An unblocked plan
// says this binary may open the store; it does not promise that every
// release pair coexists. A session keeps the pinned adapter/core pair it
// started with (CD-0111 D1), and a pair whose surfaces disagree in ways the
// manifest cannot see is refused by the digest check, not by this plan.
func PlanUpgradeReadiness(ctx context.Context, path string) (UpgradeReadiness, error) {
	if strings.TrimSpace(path) == "" {
		return UpgradeReadiness{}, newFailure(KindReadinessUnknown, "readiness",
			"empty database path", false, "pass the store path to plan against")
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// An absent store is a known state, not unknown readiness: it
			// holds no shared state a breaking migration could change, and
			// the first open creates it fully migrated. Refusing it would
			// block every first install.
			return UpgradeReadiness{
				StorePresent:       false,
				FreshStore:         true,
				AppliedVersions:    []int{},
				Pending:            []PendingMigration{},
				PendingBreaking:    []PendingMigration{},
				CompatibilityFloor: 0,
				MigrationCommand:   migrationCommand,
				ActivationCommand:  activationCommand,
			}, nil
		}
		return UpgradeReadiness{}, wrapFailure(KindReadinessUnknown, "readiness",
			"cannot inspect the store", false, "confirm the path and permissions", err)
	}
	db, err := openReadonlyStore(path)
	if err != nil {
		return UpgradeReadiness{}, err
	}
	defer func() { _ = db.Close() }()
	applied, fresh, err := readAppliedFromDB(ctx, db, path)
	if err != nil {
		return UpgradeReadiness{}, err
	}
	if fresh {
		return UpgradeReadiness{
			StorePresent:       true,
			FreshStore:         true,
			AppliedVersions:    []int{},
			Pending:            pendingList(nil),
			PendingBreaking:    pendingBreakingList(nil),
			CompatibilityFloor: 0,
			MigrationCommand:   migrationCommand,
			ActivationCommand:  activationCommand,
		}, nil
	}
	// checkManifest is itself read-only: it compares recorded checksums
	// against this binary's shipped definitions. Drift or a newer schema is
	// a known-typed refusal, and readiness must fail closed on both.
	if err := checkManifest(applied); err != nil {
		return UpgradeReadiness{}, err
	}
	plan := UpgradeReadiness{
		StorePresent:       true,
		FreshStore:         false,
		AppliedVersions:    appliedVersions(applied),
		Pending:            pendingList(applied),
		PendingBreaking:    pendingBreakingList(applied),
		CompatibilityFloor: appliedBreakingFloor(applied),
		MigrationCommand:   migrationCommand,
		ActivationCommand:  activationCommand,
	}
	plan.SchemaVersion = schemaVersionOf(applied)
	for _, m := range plan.PendingBreaking {
		plan.ActivationBlocked = true
		plan.Blockers = append(plan.Blockers, fmt.Sprintf(
			"breaking migration %d (%s) is pending; activation would strand every session that predates it",
			m.Version, m.Name))
	}
	if !plan.ActivationBlocked && len(plan.Pending) > 0 && version.Value == version.Development {
		plan.ActivationBlocked = true
		plan.Blockers = append(plan.Blockers,
			"an unstamped development build applies no migration to an existing store (CD-0139)")
	}
	return plan, nil
}

// openReadonlyStore opens one plain read-only SQLite connection on the
// existing store. mode=ro refuses every write to the database file; the
// WAL and shared-memory coordination files SQLite itself coordinates may
// appear beside the store, which the plan accepts. Any failure to open or
// reach the store fails closed.
func openReadonlyStore(path string) (*sql.DB, error) {
	dsn := "file:" + escapeSQLiteURIPath(path) + "?mode=ro&_pragma=busy_timeout(" + pragmaBusyTimeout + ")"
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, wrapFailure(KindReadinessUnknown, "readiness",
			"cannot open the store read-only", false, "confirm the database is readable", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, wrapFailure(KindReadinessUnknown, "readiness",
			"cannot reach the store read-only", false, "confirm the database is readable", err)
	}
	return db, nil
}

// readAppliedFromDB answers the manifest questions inside one read
// transaction, so a single snapshot answers every query: is the schema
// manifest present, is the store fresh, and which migrations are applied.
func readAppliedFromDB(ctx context.Context, db *sql.DB, path string) (map[int]appliedMigration, bool, error) {
	tx, err := beginReadTx(ctx, db)
	if err != nil {
		return nil, false, wrapFailure(KindReadinessUnknown, "readiness",
			"cannot begin the read-only snapshot", false, "confirm the database is readable", err)
	}
	defer func() { _ = tx.Rollback() }()
	var present string
	err = tx.QueryRowContext(ctx,
		`SELECT name FROM sqlite_schema WHERE type='table' AND name='schema_migrations'`).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		var objects int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema`).Scan(&objects); err != nil {
			return nil, false, wrapFailure(KindReadinessUnknown, "readiness",
				"cannot read the store's schema", false, "confirm the database is readable", err)
		}
		if objects == 0 {
			// An empty file with no schema objects is a fresh store:
			// activation may migrate it.
			return nil, true, nil
		}
		return nil, false, newFailure(KindReadinessUnknown, "readiness",
			fmt.Sprintf("the store at %s has no schema manifest; its provenance is unknown", path), false,
			"do not activate; run the operator-owned offline bootstrap to rebuild the manifest")
	}
	if err != nil {
		return nil, false, wrapFailure(KindReadinessUnknown, "readiness",
			"cannot read the schema manifest", false, "confirm the database is readable", err)
	}
	applied, err := appliedMigrations(ctx, tx)
	if err != nil && strings.Contains(err.Error(), "no such column: breaking") {
		// The migration command repairs this column additively, marking every
		// recorded row breaking; the plan reads that result without writing it.
		applied, err = legacyManifestMigrations(ctx, tx)
	}
	if err != nil {
		return nil, false, wrapFailure(KindReadinessUnknown, "readiness",
			"cannot read the applied migrations", false, "confirm the database is readable", err)
	}
	if len(applied) == 0 {
		return nil, false, newFailure(KindReadinessUnknown, "readiness",
			"the schema manifest is empty on a non-fresh store", false,
			"do not activate; run the operator-owned offline bootstrap")
	}
	if err := checkBinaryCompatibility(ctx, tx); err != nil {
		return nil, false, err
	}
	return applied, false, nil
}

// UpgradeReadiness is the read-only activation answer for one store path.
type UpgradeReadiness struct {
	// StorePresent reports whether the database file exists.
	StorePresent bool `json:"store_present"`
	// FreshStore reports a store this binary may migrate at open: no file,
	// or an empty one.
	FreshStore bool `json:"fresh_store"`
	// AppliedVersions lists the recorded migration versions, in order.
	AppliedVersions []int `json:"applied_versions"`
	// SchemaVersion is the highest applied migration version, 0 when none.
	SchemaVersion int `json:"schema_version"`
	// Pending lists the shipped migrations this store has not applied.
	Pending []PendingMigration `json:"pending"`
	// PendingBreaking lists the pending migrations flagged Breaking.
	PendingBreaking []PendingMigration `json:"pending_breaking"`
	// Blockers report why activation must wait for the operator. A prepared
	// candidate stays unactivated while ActivationBlocked is set.
	ActivationBlocked bool `json:"activation_blocked"`
	// Blockers name why activation is blocked, one line each.
	Blockers []string `json:"blockers,omitempty"`
	// CompatibilityFloor is the highest breaking migration this store has
	// applied: a binary defining that version may open the store even when
	// later additive migrations have run. It is the store's floor, not this
	// binary's schema version, and 0 when nothing breaking is applied.
	// Admission at the floor is necessary, not sufficient, for a release
	// pair to coexist; CD-0111 D1 pins each session's adapter/core pair.
	CompatibilityFloor int `json:"compatibility_floor"`
	// MigrationCommand is the exact operator-run migration command. The
	// store fills a descriptive default; a caller that knows the release
	// identity replaces it before the plan reaches an operator.
	MigrationCommand string `json:"migration_command"`
	// ActivationCommand is the exact operator-run activation command, with
	// the same descriptive-default rule as MigrationCommand.
	ActivationCommand string `json:"activation_command"`
}

// PendingMigration is one not-yet-applied shipped migration.
type PendingMigration struct {
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Breaking bool   `json:"breaking"`
}

const (
	// migrationCommand and activationCommand are descriptive defaults. A
	// caller that knows the prepared candidate replaces both with the exact
	// absolute invocations before an operator reads them.
	migrationCommand  = "concord upgrade"
	activationCommand = "re-run the installer to activate the prepared release"
)

// appliedBreakingFloor computes the store's compatibility floor from what it
// actually applied: the highest breaking version in the manifest. Later
// additive migrations never move it, and a manifest that applied nothing
// breaking has no floor (schema.go's checkManifest rule, issue #722).
func appliedBreakingFloor(applied map[int]appliedMigration) int {
	floor := 0
	for version, row := range applied {
		if row.Breaking && version > floor {
			floor = version
		}
	}
	return floor
}

// KindReadinessUnknown marks a store whose activation readiness cannot be
// established by reading. The planner never repairs, guesses, or writes.
const KindReadinessUnknown FailureKind = "readiness_unknown"

// escapeSQLiteURIPath percent-encodes one absolute filesystem path for the
// path component of a file: URI. SQLite resolves %XX escapes in URI
// filenames, so a path containing '?', '#', or spaces names exactly the
// file the planner observed — never a fragment-shortened neighbor.
func escapeSQLiteURIPath(path string) string {
	var builder strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			builder.WriteByte(c)
		case strings.IndexByte("/._~-", c) >= 0:
			builder.WriteByte(c)
		default:
			fmt.Fprintf(&builder, "%%%02X", c)
		}
	}
	return builder.String()
}

func appliedVersions(applied map[int]appliedMigration) []int {
	versions := make([]int, 0, len(applied))
	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			versions = append(versions, m.Version)
		}
	}
	return versions
}

func schemaVersionOf(applied map[int]appliedMigration) int {
	highest := 0
	for version := range applied {
		if version > highest {
			highest = version
		}
	}
	return highest
}

func pendingList(applied map[int]appliedMigration) []PendingMigration {
	pending := []PendingMigration{}
	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			continue
		}
		pending = append(pending, PendingMigration{Version: m.Version, Name: m.Name, Breaking: m.Breaking})
	}
	return pending
}

func pendingBreakingList(applied map[int]appliedMigration) []PendingMigration {
	pending := []PendingMigration{}
	for _, m := range pendingList(applied) {
		if m.Breaking {
			pending = append(pending, m)
		}
	}
	return pending
}
