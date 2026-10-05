package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// craftPartialStore writes a store whose manifest stops before the first
// shipped breaking migration, with checksums this binary computes.
func craftPartialStore(t *testing.T, path string) {
	t.Helper()
	craftStoreApplyingThrough(t, path, firstBreakingVersion(t)-1)
}

// craftStoreApplyingThrough records a manifest that applied every shipped
// migration up to and including through. The plan reads only the manifest,
// so the schema objects themselves are not needed for planning.
func craftStoreApplyingThrough(t *testing.T, path string, through int) {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, schemaManifestDDL); err != nil {
		t.Fatalf("manifest ddl: %v", err)
	}
	for _, m := range migrations {
		if m.Version > through {
			break
		}
		breaking := 0
		if m.Breaking {
			breaking = 1
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum, applied_at, breaking) VALUES (?,?,?,?,?)`,
			m.Version, m.Name, m.checksum(), "2026-01-01T00:00:00Z", breaking); err != nil {
			t.Fatalf("insert migration %d: %v", m.Version, err)
		}
	}
}

// recordExtraManifestRow appends one manifest row this binary does not
// define, the way a newer release's applied step reads down here.
func recordExtraManifestRow(t *testing.T, path string, version int, breaking bool) {
	t.Helper()
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	flag := 0
	if breaking {
		flag = 1
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO schema_migrations (version, name, checksum, applied_at, breaking) VALUES (?,?,?,?,?)`,
		version, "future_step", strings.Repeat("c", 64), "2026-01-01T00:00:00Z", flag); err != nil {
		t.Fatalf("insert future migration %d: %v", version, err)
	}
}

func firstBreakingVersion(t *testing.T) int {
	t.Helper()
	for _, m := range migrations {
		if m.Breaking {
			return m.Version
		}
	}
	t.Fatal("the shipped sequence declares no breaking migration")
	return 0
}

func TestPlanUpgradeReadinessMissingStoreIsFreshAndPureRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("missing store must plan, not fail: %v", err)
	}
	if plan.StorePresent || !plan.FreshStore || plan.ActivationBlocked {
		t.Fatalf("a missing store must read fresh and unblocked: %+v", plan)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("planning created files in the store directory: %v %+v", err, entries)
	}
}

func TestPlanUpgradeReadinessMigratedStoreIsUnblockedAndPureRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = db.Close()
	before := fileDigest(t, path)
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("migrated store must plan, not fail: %v", err)
	}
	if after := fileDigest(t, path); after != before {
		t.Fatal("planning changed the database file")
	}
	if plan.ActivationBlocked || len(plan.Pending) != 0 || plan.SchemaVersion != CurrentSchemaVersion() {
		t.Fatalf("a fully migrated store must read clean: %+v", plan)
	}
	floor := 0
	for _, migration := range migrations {
		if migration.Breaking && migration.Version > floor {
			floor = migration.Version
		}
	}
	if plan.CompatibilityFloor != floor {
		t.Fatalf("compatibility floor = %d, want highest applied breaking migration %d", plan.CompatibilityFloor, floor)
	}
	if plan.MigrationCommand != "concord upgrade" || plan.ActivationCommand == "" {
		t.Fatalf("the plan must carry exact operator commands: %+v", plan)
	}
}

func TestPlanUpgradeReadinessPendingBreakingBlocksActivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	craftPartialStore(t, path)
	before := fileDigest(t, path)
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("a readable blocked store still plans: %v", err)
	}
	if after := fileDigest(t, path); after != before {
		t.Fatal("planning changed the database file")
	}
	if !plan.ActivationBlocked {
		t.Fatalf("a pending breaking step must block activation: %+v", plan)
	}
	stop := firstBreakingVersion(t)
	found := false
	for _, m := range plan.PendingBreaking {
		if m.Version == stop {
			found = true
		}
	}
	if !found || len(plan.Blockers) == 0 {
		t.Fatalf("the plan must name the blocking step %d and why: %+v", stop, plan)
	}
	if plan.CompatibilityFloor != 0 {
		t.Fatalf("a store that applied no breaking step has no floor, got %d", plan.CompatibilityFloor)
	}
	if plan.MigrationCommand != "concord upgrade" {
		t.Fatalf("the migration command must be exact: %q", plan.MigrationCommand)
	}
}

// A store written by a release that predates the manifest's breaking column
// still plans, read-only: the plan reads every recorded row as breaking, the
// value the migration command's additive repair writes, and leaves the
// column absent for that command to add.
func TestPlanUpgradeReadinessReadsAManifestThatPredatesTheBreakingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	craftPartialStore(t, path)
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `ALTER TABLE schema_migrations DROP COLUMN breaking`); err != nil {
		t.Fatalf("drop the breaking column: %v", err)
	}
	_ = db.Close()
	before := fileDigest(t, path)
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("a legacy manifest must plan, not fail: %v", err)
	}
	if after := fileDigest(t, path); after != before {
		t.Fatal("planning repaired or otherwise changed the database file")
	}
	if !plan.ActivationBlocked || len(plan.PendingBreaking) == 0 {
		t.Fatalf("the shipped breaking step stays pending on a legacy manifest: %+v", plan)
	}
	if want := firstBreakingVersion(t) - 1; plan.CompatibilityFloor != want {
		t.Fatalf("legacy rows read as breaking: floor = %d, want %d", plan.CompatibilityFloor, want)
	}
}

// The floor is what the store applied, not what this binary defines: a
// store through the first breaking step reports that step as its floor.
func TestPlanUpgradeReadinessFloorIsTheHighestAppliedBreakingStep(t *testing.T) {
	first := firstBreakingVersion(t)
	second := 0
	for _, m := range migrations {
		if m.Version > first && m.Breaking {
			second = m.Version
			break
		}
	}
	if second == 0 {
		t.Skip("the shipped sequence declares only one breaking migration")
	}
	path := filepath.Join(t.TempDir(), "store.db")
	craftStoreApplyingThrough(t, path, second)
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("a readable store through the second breaking step must plan: %v", err)
	}
	if plan.CompatibilityFloor != second {
		t.Fatalf("compatibility floor = %d, want the highest applied breaking step %d", plan.CompatibilityFloor, second)
	}
	for _, pending := range plan.PendingBreaking {
		if pending.Version <= second {
			t.Fatalf("a breaking step recorded as applied is still pending: %+v", pending)
		}
	}
}

// Compatible pairs coexist at the floor (CON-807): a newer release's
// additive steps never block this binary's activation and never move the
// floor, which is exactly what lets an old session and a new release share
// one store while only additive steps ship.
func TestPlanUpgradeReadinessAdmitsAdditiveStepsBeyondThisBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = db.Close()
	recordExtraManifestRow(t, path, CurrentSchemaVersion()+5, false)
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("an additive step beyond this binary must not refuse planning: %v", err)
	}
	if plan.ActivationBlocked {
		t.Fatalf("an additive step beyond this binary must not block activation: %+v", plan)
	}
	shipped := 0
	for _, m := range migrations {
		if m.Breaking && m.Version > shipped {
			shipped = m.Version
		}
	}
	if plan.CompatibilityFloor != shipped || plan.SchemaVersion != CurrentSchemaVersion()+5 {
		t.Fatalf("floor = %d schema = %d, want floor %d and the recorded high water: %+v",
			plan.CompatibilityFloor, plan.SchemaVersion, shipped, plan)
	}
}

// Schema-floor admission is a boundary, not a compatibility promise for
// every release pair: a breaking step this binary does not define refuses
// the plan outright, because that step changed schema this binary writes.
func TestPlanUpgradeReadinessRefusesABreakingStepBeyondThisBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	_ = db.Close()
	recordExtraManifestRow(t, path, CurrentSchemaVersion()+5, true)
	_, err = PlanUpgradeReadiness(context.Background(), path)
	if err == nil {
		t.Fatal("a breaking step beyond this binary must fail closed")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindSchemaUnsupported {
		t.Fatalf("a breaking step beyond this binary must refuse with schema_unsupported, got %v", err)
	}
}

func TestPlanUpgradeReadinessFailsClosedOnUnknownStates(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.db")
	if err := os.WriteFile(junk, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanUpgradeReadiness(context.Background(), junk); err == nil {
		t.Fatal("an unreadable store must fail closed")
	} else {
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindReadinessUnknown {
			t.Fatalf("an unreadable store must fail with readiness_unknown, got %v", err)
		}
	}
	alien := filepath.Join(dir, "alien.db")
	db, err := sql.Open(driverName, dataSourceName(alien))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE other (k TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	_, err = PlanUpgradeReadiness(context.Background(), alien)
	if err == nil {
		t.Fatal("a manifest-less non-empty store must fail closed")
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindReadinessUnknown {
		t.Fatalf("a manifest-less store must fail with readiness_unknown, got %v", err)
	}
	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanUpgradeReadiness(context.Background(), empty)
	if err != nil || !plan.FreshStore || plan.ActivationBlocked {
		t.Fatalf("an empty file is a fresh store: %v %+v", err, plan)
	}
}

// Planning reads the CURRENT committed state through a live WAL (CON-807):
// with a writer connection holding un-checkpointed committed frames, the
// plain read-only connection must see those frames. A breaking step that
// committed only into the WAL must read as applied, never as pending.
func TestPlanUpgradeReadinessReadsCurrentWALState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Keep the writer open so the committed tail stays in the WAL with a
	// live -shm beside the store.
	head := CurrentSchemaVersion()
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking)
		VALUES(?, 'wal_committed_additive_step', 'wal-currency-probe', '2026-01-01T00:00:00Z', 0)`, head+1); err != nil {
		t.Fatalf("cannot commit into the WAL: %v", err)
	}
	before := fileDigest(t, path)
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("plan over a live WAL: %v", err)
	}
	if after := fileDigest(t, path); after != before {
		t.Fatal("planning changed the database file beside a live WAL")
	}
	if plan.SchemaVersion != head+1 {
		t.Fatalf("the planner read a stale snapshot: schema %d want the WAL-committed %d", plan.SchemaVersion, head+1)
	}
	if plan.ActivationBlocked {
		t.Fatalf("a live WAL must not read as a blocked store: %+v", plan)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// The URI-escape probe: a store path containing URI metacharacters must name
// exactly that store. An unescaped file: URI would silently read the
// fragment-stripped neighbor — the wrong database — and look pure while doing
// it.
func TestPlanUpgradeReadinessEscapesTheStorePath(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "store.db")
	requested := filepath.Join(dir, "store.db#suffix")
	for _, path := range []string{plain, requested} {
		db, err := sql.Open(driverName, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
			t.Fatal(err)
		}
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
		if path == requested {
			if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking)
				VALUES(?, 'future_breaking', 'synthetic', '2026-01-01T00:00:00Z', 1)`, CurrentSchemaVersion()+1); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := PlanUpgradeReadiness(context.Background(), requested)
	if err == nil && !plan.ActivationBlocked {
		t.Fatal("the planner read the wrong store: the requested store carries an unsupported breaking step")
	}
	if err == nil && plan.SchemaVersion != CurrentSchemaVersion()+1 {
		t.Fatalf("the planner answered from the neighbor store: schema %d want %d", plan.SchemaVersion, CurrentSchemaVersion()+1)
	}
}

// The committed-WAL currency probe against a live index: a breaking step that
// committed only into the write-ahead log — nothing checkpointed — must block
// the plan.
func TestPlanUpgradeReadinessBlocksOnABreakingRowCommittedToTheLiveWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking)
		VALUES(?, 'future_breaking', 'synthetic', '2026-01-01T00:00:00Z', 1)`, CurrentSchemaVersion()+1); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err == nil && !plan.ActivationBlocked {
		t.Fatalf("the planner admitted a release against a breaking step committed only in the WAL: %+v", plan)
	}
}

// ---- Cross-process WAL currency (CON-807) ----

// readinessChildRoleEnv names the subprocess role the cross-process probe
// re-executes this test binary for. The env carries the role and its store.
const readinessChildRoleEnv = "CONCORD_READINESS_CHILD_ROLE"

func readinessChild() bool {
	return os.Getenv(readinessChildRoleEnv) != ""
}

// The child holds the store open as a live session with committed WAL frames
// and signals readiness, then waits for the parent's release signal.
func readinessChildLiveSession(t *testing.T) {
	path := os.Getenv("CONCORD_READINESS_CHILD_STORE")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at,breaking)
		VALUES(?, 'child_committed_additive_step', 'child-probe', '2026-01-01T00:00:00Z', 0)`, CurrentSchemaVersion()+1); err != nil {
		t.Fatal(err)
	}
	fmt.Println("READINESS-CHILD-LIVE")
	if _, err := fmt.Fscanln(os.Stdin); err != nil && err.Error() != "EOF" && err.Error() != "unexpected newline" {
		t.Fatalf("child wait: %v", err)
	}
}

func TestReadinessChildHelper(t *testing.T) {
	if !readinessChild() {
		return
	}
	switch os.Getenv(readinessChildRoleEnv) {
	case "live-session":
		readinessChildLiveSession(t)
	default:
		t.Fatalf("unknown child role %q", os.Getenv(readinessChildRoleEnv))
	}
	os.Exit(0)
}

// childStdinPipes holds the live children's release pipes.
var childStdinPipes []io.WriteCloser

func startReadinessChild(t *testing.T, role, store string) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=TestReadinessChildHelper", "-test.v=false")
	command.Env = append(os.Environ(), readinessChildRoleEnv+"="+role, "CONCORD_READINESS_CHILD_STORE="+store)
	command.Stdout = os.Stdout
	if role == "live-session" {
		stdin, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		childStdinPipes = append(childStdinPipes, stdin)
		t.Cleanup(func() { _, _ = stdin.Write([]byte("\n")) })
	}
	if err := command.Start(); err != nil {
		t.Fatalf("cannot start the %s child: %v", role, err)
	}
	return command
}

func releaseChildStdins(t *testing.T) {
	t.Helper()
	for _, pipe := range childStdinPipes {
		_, _ = pipe.Write([]byte("\n"))
	}
	childStdinPipes = nil
}

// A live session in another process holds the wal-index; the plain
// read-only connection answers through that index with SQLite's own
// locking and sees the WAL-committed frame.
func TestPlanUpgradeReadinessSeesACommittedWALThroughALiveIndexCrossProcess(t *testing.T) {
	if readinessChild() {
		return
	}
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sql.Open(driverName, dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	child := startReadinessChild(t, "live-session", path)
	defer func() { _ = child.Wait() }()
	// Wait for the child's committed WAL frame before planning.
	deadline := time.After(15 * time.Second)
	for {
		if wal, err := os.Stat(path + "-wal"); err == nil && wal.Size() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the live child never committed into the WAL")
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	plan, err := PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("a live cross-process index must plan: %v", err)
	}
	if plan.SchemaVersion != CurrentSchemaVersion()+1 {
		t.Fatalf("the planner missed the child's WAL-committed frame: schema %d want %d", plan.SchemaVersion, CurrentSchemaVersion()+1)
	}
	if plan.ActivationBlocked {
		t.Fatalf("the child's additive step must not block: %+v", plan)
	}
	releaseChildStdins(t)
}
