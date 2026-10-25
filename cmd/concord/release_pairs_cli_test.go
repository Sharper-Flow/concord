package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/version"
)

// Coexistence uses two immutable released sources below the candidate's
// breaking floor, not the current source with migrations removed.
const releasedPairTag = "v11.61.0"
const releasedPairSchema = 114
const compatiblePairCommit = "v11.63.30"
const compatiblePairVersion = "v11.63.30"
const compatiblePairSchema = 116

// Read the shipped manifest, not a second list of migration numbers.
func releasedPendingBreaking(t *testing.T, schemaVersion int) []int {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.DatabaseForTesting().Query(`SELECT version FROM schema_migrations WHERE breaking=1 AND version>? ORDER BY version`, schemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(versions) == 0 {
		t.Fatal("the pair requires a pending breaking migration")
	}
	return versions
}

func assertReleasePendingBreaking(t *testing.T, plan store.UpgradeReadiness, versions []int) {
	t.Helper()
	if len(plan.PendingBreaking) != len(versions) {
		t.Fatalf("pending breaking migrations = %+v, want %v", plan.PendingBreaking, versions)
	}
	for i, m := range plan.PendingBreaking {
		if m.Version != versions[i] || !m.Breaking {
			t.Fatalf("pending breaking migrations = %+v, want %v", plan.PendingBreaking, versions)
		}
	}
}

func assertAppliedMigrationTail(t *testing.T, applied []int, baseline int) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "manifest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err := s.DatabaseForTesting().Query(`SELECT version FROM schema_migrations WHERE version>? ORDER BY version`, baseline)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var want []int
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		want = append(want, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, want) || len(want) == 0 {
		t.Fatalf("applied migration tail = %v, want exact manifest tail %v", applied, want)
	}
}

// extractReleasedSource checks out a release tag into a
// private directory. The bytes come from this repository's object store, so
// the built core is the released source, not a relabel of the working tree.
func extractReleasedSource(t *testing.T, tag string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	toplevel, err := exec.Command(git, "-C", working, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("the test source is not inside a git repository: %v", err)
	}
	root := strings.TrimSpace(string(toplevel))
	if err := exec.Command(git, "-C", root, "rev-parse", "--verify", "--quiet", tag+"^{commit}").Run(); err != nil {
		fetchRef := "+refs/tags/" + tag + ":refs/tags/" + tag
		t.Fatalf("the released source %s is absent from this clone; fetch it with: git fetch --no-tags --depth=1 origin %s", tag, fetchRef)
	}
	scratch := t.TempDir()
	tarball := filepath.Join(scratch, "source.tar")
	if out, err := exec.Command(git, "-C", root, "archive", "--output", tarball, tag).CombinedOutput(); err != nil {
		t.Fatalf("git archive %s failed: %v: %s", tag, err, out)
	}
	destination := filepath.Join(scratch, "source")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tar", "-x", "-f", tarball, "-C", destination).CombinedOutput(); err != nil {
		t.Fatalf("cannot extract %s: %v: %s", tag, err, out)
	}
	return destination
}

// buildCoreFrom builds one core binary from the source at directory,
// stamped as its release identity, and returns the binary path.
func buildCoreFrom(t *testing.T, directory, release string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("the Go toolchain is unavailable in this environment: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "concord")
	build := exec.Command("go", "build", "-o", binary,
		"-ldflags=-X github.com/sharper-flow/concord/internal/version.Value="+release,
		"./cmd/concord")
	build.Dir = directory
	var buildOut bytes.Buffer
	build.Stdout = &buildOut
	build.Stderr = &buildOut
	if err := build.Run(); err != nil {
		t.Fatalf("cannot build the %s core from %s: %v: %s", release, directory, err, buildOut.String())
	}
	return binary
}

// installReleaseTree lays a built core out as an installed release tree
// under dataRoot, the shape the installer stages (bin/concord inside a
// release root). The fence-protocol marker is stamped only when wanted: a
// released tree from before CON-807 carries none, which is exactly the
// legacy demonstration the pair test asserts beside coexistence.
func installReleaseTree(t *testing.T, dataRoot, release, binary string, fenceable bool) string {
	t.Helper()
	tree := filepath.Join(dataRoot, release)
	if err := os.MkdirAll(filepath.Join(tree, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "bin", "concord"), raw, 0o755); err != nil {
		t.Fatal(err)
	}
	if fenceable {
		marker := strconv.Itoa(hostlease.CurrentFenceProtocol) + "\n"
		if err := os.WriteFile(filepath.Join(tree, "fence-protocol"), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return tree
}

// runRelease runs one verb of a built release against the store at dbPath.
func runRelease(t *testing.T, binary, dbPath, verb, stdin string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	command := exec.Command(binary, verb)
	command.Stdin = strings.NewReader(stdin)
	command.Stdout = &out
	command.Stderr = &errOut
	command.Env = append(os.Environ(), dbOverrideEnv+"="+dbPath)
	err := command.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("cannot run %s: %v", binary, err)
	}
	return code, out.String(), errOut.String()
}

// coreDescriptor reads a built core's side-effect-free --version --json
// descriptor: the identity evidence the installer's staging probe uses
// (CON-807).
func coreDescriptor(t *testing.T, binary string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	command := exec.Command(binary, "--version", "--json")
	command.Stdout = &out
	command.Stderr = &out
	if err := command.Run(); err != nil {
		t.Fatalf("the core at %s answers no capability descriptor: %v: %s", binary, err, out.String())
	}
	var descriptor map[string]any
	if err := json.Unmarshal(out.Bytes(), &descriptor); err != nil {
		t.Fatalf("the descriptor of %s is not JSON: %v: %s", binary, err, out.String())
	}
	return descriptor
}

// coreAnswersRoute reports whether a built core answers a bootstrap
// argument route at all, without interpreting its output.
func coreAnswersRoute(binary string, args ...string) bool {
	return exec.Command(binary, args...).Run() == nil
}

// TestDistinctReleasedCoresCoexistOnOneStore proves CD-0111 D1/D3 with
// each released source's real adapter and core on one temporary store.
// Schema 114 and 116 coexist across additive migrations; the distinct
// candidate must refuse its breaking migration while either session lives.
// After both sessions stop, the candidate migrates forward under the fence,
// and both older cores refuse the incompatible floor. This is supported-pair
// evidence, not blanket rolling compatibility or projection/binary-skew repair.
func TestDistinctReleasedCoresCoexistOnOneStore(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three release cores from three distinct sources")
	}
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data", "concord")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataRoot, "concord.db")
	releasedSource := extractReleasedSource(t, releasedPairTag)
	compatibleSource := extractReleasedSource(t, compatiblePairCommit)
	older := buildCoreFrom(t, releasedSource, releasedPairTag)
	newer := buildCoreFrom(t, compatibleSource, compatiblePairVersion)
	oldRoot := installReleaseTree(t, dataRoot, releasedPairTag, older, false)
	newRoot := installReleaseTree(t, dataRoot, compatiblePairVersion, newer, true)
	oldBinary := filepath.Join(oldRoot, "bin", "concord")
	newBinary := filepath.Join(newRoot, "bin", "concord")
	// Each release's own adapter runtime, stamped to its own tree exactly
	// the way the installer stamps it: the sessions below call their own
	// pinned cores (CD-0111 D1).
	oldAdapter := stampAdapterRuntime(t, filepath.Join(releasedSource, "adapter", "opencode"), oldRoot)
	newAdapter := stampAdapterRuntime(t, filepath.Join(compatibleSource, "adapter", "opencode"), newRoot)

	// Distinct-source identity evidence: the released core answers its own
	// tag and predates the descriptor route (a known legacy core for the
	// installer's probe), while the compatible core reports a complete
	// descriptor pinned to its own tree.
	code, out, _ := runRelease(t, oldBinary, path, "--version", "")
	if code != 0 || strings.TrimSpace(out) != releasedPairTag {
		t.Fatalf("the released core must answer its tag: %d %q", code, out)
	}
	if coreAnswersRoute(oldBinary, "--version", "--json") {
		t.Fatal("the released core predates the descriptor route; it must not answer --version --json")
	}
	newerDescriptor := coreDescriptor(t, newBinary)
	if newerDescriptor["version"] != compatiblePairVersion || newerDescriptor["schema_version"] != float64(compatiblePairSchema) || newerDescriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) {
		t.Fatalf("the compatible core must report its actual release, schema and fence protocol: %+v", newerDescriptor)
	}
	newSchema, ok := newerDescriptor["schema_version"].(float64)
	if !ok || newSchema != compatiblePairSchema {
		t.Fatalf("the additive release must define schema %d: %v", compatiblePairSchema, newerDescriptor["schema_version"])
	}
	newSchemaVersion := int(newSchema)
	if newerDescriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) {
		t.Fatalf("the additive release must report its own fence capability: %v", newerDescriptor["fence_protocol"])
	}
	if coreBinary, ok := newerDescriptor["core_binary"].(string); !ok || coreBinary != newBinary {
		t.Fatalf("the new descriptor must pin its own core binary: %v", newerDescriptor["core_binary"])
	}
	if _, err := os.Stat(filepath.Join(oldRoot, "fence-protocol")); !os.IsNotExist(err) {
		t.Fatalf("the released tree must be staged as the legacy core it is: %v", err)
	}

	// The released core migrates the fresh store to its own head (114).
	var oldUpgrade struct {
		SchemaVersion int `json:"schema_version"`
	}
	code, out, errText := runRelease(t, oldBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the released core must migrate the fresh store: %d %s %s", code, out, errText)
	}
	if err := json.Unmarshal([]byte(out), &oldUpgrade); err != nil {
		t.Fatalf("the released core's upgrade report is not JSON: %v: %s", err, out)
	}
	if oldUpgrade.SchemaVersion != releasedPairSchema {
		t.Fatalf("the released core's schema head = %d, want the released %d", oldUpgrade.SchemaVersion, releasedPairSchema)
	}
	if oldUpgrade.SchemaVersion >= newSchemaVersion {
		t.Fatalf("the pair is not distinct: the released head %d is not behind the additive release's %d", oldUpgrade.SchemaVersion, newSchemaVersion)
	}

	// The old session is a real released-adapter operation: the released
	// adapter claims its lease by running its own pinned core (CD-0111 D1).
	oldSession := startAdapterSessionOperation(t, oldAdapter, path, "/srv/old-session")

	// The compatible core advances the store by its actual additive tail.
	var newUpgrade struct {
		SchemaVersion int   `json:"schema_version"`
		Applied       []int `json:"applied"`
	}
	code, out, errText = runRelease(t, newBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the newer core must advance the store: %d %s %s", code, out, errText)
	}
	if err := json.Unmarshal([]byte(out), &newUpgrade); err != nil {
		t.Fatalf("the newer core's upgrade report is not JSON: %v: %s", err, out)
	}
	if newUpgrade.SchemaVersion != newSchemaVersion || len(newUpgrade.Applied) != newSchemaVersion-releasedPairSchema {
		t.Fatalf("the newer core must apply its additive steps: %+v", newUpgrade)
	}
	for i, applied := range newUpgrade.Applied {
		if applied != releasedPairSchema+i+1 {
			t.Fatalf("the newer core must apply each additive step in order: %+v", newUpgrade)
		}
	}

	// Old-session continuity: the released adapter session keeps operating
	// on the store its newer pair advanced — floor admission working in the
	// old pair's direction — re-claiming its lease through its own core.
	oldSession.reclaimAndHold(t)

	// The newer release's CLI reads the same store and plans unblocked,
	// naming its own distinct binary, not a relabeled identity.
	code, out, errText = runRelease(t, newBinary, path, "upgrade", `{"plan":true}`)
	if code != 0 {
		t.Fatalf("the newer release's plan = %d: %s", code, errText)
	}
	var plan struct {
		SchemaVersion     int    `json:"schema_version"`
		ActivationBlocked bool   `json:"activation_blocked"`
		MigrationCommand  string `json:"migration_command"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("the newer release's plan is not JSON: %v: %s", err, out)
	}
	if plan.ActivationBlocked {
		t.Fatalf("a supported pair must plan unblocked: %s", out)
	}
	if plan.SchemaVersion != newSchemaVersion {
		t.Fatalf("the newer release's plan must read its schema: %d want %d", plan.SchemaVersion, newSchemaVersion)
	}
	if !strings.Contains(plan.MigrationCommand, newBinary) {
		t.Fatalf("the plan must name the newer release's own binary: %q", plan.MigrationCommand)
	}

	// The newer release's session is the same kind of real adapter
	// operation from the compatible release's own adapter, beside the old
	// one, and both leases are observable together, each naming its own
	// release root — the old one honestly recorded at fence protocol 0, a
	// legacy participant the maintenance boundary must fail closed on.
	newSession := startAdapterSessionOperation(t, newAdapter, path, "/srv/new-session")
	code, out, errText = runRelease(t, newBinary, path, "host-leases", `{}`)
	if code != 0 {
		t.Fatalf("the newer release's lease listing = %d: %s", code, errText)
	}
	var listing struct {
		Leases []hostlease.Lease `json:"leases"`
	}
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		t.Fatalf("the lease listing is not JSON: %v: %s", err, out)
	}
	roots := map[string]hostlease.Lease{}
	for _, lease := range listing.Leases {
		roots[lease.ReleaseRoot] = lease
	}
	if len(listing.Leases) != 2 {
		t.Fatalf("both releases' sessions must coexist in the lease set: %+v", listing.Leases)
	}
	oldLease, oldHeld := roots[oldRoot]
	newLease, newHeld := roots[newRoot]
	if !oldHeld || oldLease.CoreBinary != oldBinary {
		t.Fatalf("the released session must be pinned to its own release tree: %+v", listing.Leases)
	}
	if !newHeld || newLease.CoreBinary != newBinary {
		t.Fatalf("the newer session must be pinned to its own release tree: %+v", listing.Leases)
	}
	if oldLease.FenceProtocol != 0 {
		t.Fatalf("the released core's lease must read as unfenceable legacy, got protocol %d", oldLease.FenceProtocol)
	}
	if newLease.FenceProtocol != hostlease.CurrentFenceProtocol {
		t.Fatalf("the newer session's lease must record its fence protocol: %+v", newLease)
	}
	// The fail-closed legacy rule holds beside coexistence: the boundary's
	// own enumeration names the released tree, because its core predates
	// the fence protocol (CON-807, the operator-owned offline bootstrap's
	// subject).
	unfenceable, err := unfenceableReleaseRoots(dataRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfenceable) != 1 || unfenceable[0] != oldRoot {
		t.Fatalf("the released tree must be the boundary's unfenceable legacy entry: %v", unfenceable)
	}

	candidateVersion := "v0.0.0-con869-candidate"
	candidate := buildCoreFrom(t, repoSourceDir(t), candidateVersion)
	candidateRoot := installReleaseTree(t, dataRoot, candidateVersion, candidate, true)
	candidateBinary := filepath.Join(candidateRoot, "bin", "concord")
	descriptor := coreDescriptor(t, candidateBinary)
	breaking := releasedPendingBreaking(t, compatiblePairSchema)
	floor := breaking[len(breaking)-1]
	if descriptor["schema_version"] != float64(store.CurrentSchemaVersion()) || descriptor["core_binary"] != candidateBinary || descriptor["version"] != candidateVersion || descriptor["manifest_digest"] == newerDescriptor["manifest_digest"] {
		t.Fatalf("the candidate must identify its own distinct core and contract: %+v", descriptor)
	}
	assertPendingFloor := func(t *testing.T) {
		t.Helper()
		readiness, err := store.PlanUpgradeReadiness(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		assertReleasePendingBreaking(t, readiness, releasedPendingBreaking(t, compatiblePairSchema))
		if readiness.SchemaVersion != compatiblePairSchema || readiness.CompatibilityFloor != 111 || !readiness.ActivationBlocked {
			t.Fatalf("refusal must leave the compatible store and breaking floor pending: %+v", readiness)
		}
		if fence, err := hostlease.ReadFence(dataRoot); err != nil || fence != nil {
			t.Fatalf("a pre-commit refusal must close its own fence: %+v %v", fence, err)
		}
	}
	t.Run("breaking_candidate_refuses_live_legacy_session", func(t *testing.T) {
		code, _, errText := runRelease(t, candidateBinary, path, "upgrade", `{}`)
		if code != 1 || !strings.Contains(errText, "unfenceable legacy participant") || !strings.Contains(errText, oldRoot) || !strings.Contains(errText, "fence protocol 0") {
			t.Fatalf("the breaking candidate must name the live legacy session: %d %s", code, errText)
		}
		assertPendingFloor(t)
	})
	oldSession.stop()
	t.Run("breaking_candidate_refuses_live_compatible_session", func(t *testing.T) {
		code, _, errText := runRelease(t, candidateBinary, path, "upgrade", `{"confirm_sessions_stopped":true}`)
		if code != 1 || !strings.Contains(errText, "upgrade_blocked") || !strings.Contains(errText, newRoot) || !strings.Contains(errText, "migration 119") {
			t.Fatalf("confirmation cannot bypass a live older schema holder: %d %s", code, errText)
		}
		assertPendingFloor(t)
	})
	newSession.stop()
	t.Run("incompatible_floor_admission_after_sessions_stop", func(t *testing.T) {
		code, _, errText := runRelease(t, candidateBinary, path, "upgrade", `{}`)
		if code != 1 || !strings.Contains(errText, oldRoot) || !strings.Contains(errText, "confirm_sessions_stopped") {
			t.Fatalf("the stopped unmarked tree still requires confirmation: %d %s", code, errText)
		}
		assertPendingFloor(t)
		code, out, errText := runRelease(t, candidateBinary, path, "upgrade", `{"confirm_sessions_stopped":true}`)
		if code != 0 {
			t.Fatalf("the candidate must migrate forward after sessions stop: %d %s %s", code, out, errText)
		}
		var upgrade struct {
			SchemaVersion int   `json:"schema_version"`
			Applied       []int `json:"applied"`
		}
		if err := json.Unmarshal([]byte(out), &upgrade); err != nil {
			t.Fatal(err)
		}
		if upgrade.SchemaVersion != store.CurrentSchemaVersion() {
			t.Fatalf("the candidate must commit the complete pending tail: %+v", upgrade)
		}
		assertAppliedMigrationTail(t, upgrade.Applied, compatiblePairSchema)
		readiness, err := store.PlanUpgradeReadiness(context.Background(), path)
		if err != nil || readiness.CompatibilityFloor != floor || len(readiness.PendingBreaking) != 0 || readiness.ActivationBlocked {
			t.Fatalf("the committed store must require floor %d: %+v %v", floor, readiness, err)
		}
		fence, err := hostlease.ReadFence(dataRoot)
		if err != nil || fence == nil || fence.ReleaseRoot != candidateRoot || fence.CoreBinary != candidateBinary {
			t.Fatalf("the candidate's committed breaking boundary must remain open: %+v %v", fence, err)
		}
		for _, binary := range []string{oldBinary, newBinary} {
			code, _, errText := runRelease(t, binary, path, "upgrade", `{}`)
			if code != 1 || !strings.Contains(errText, "schema_unsupported") || !strings.Contains(errText, fmt.Sprintf("schema version %d", floor)) {
				t.Fatalf("the real older core %s must refuse the incompatible floor: %d %s", binary, code, errText)
			}
		}
		code, _, errText = runRelease(t, candidateBinary, path, "host-lease", `{"pid":`+pidJSON(os.Getpid())+`}`)
		if code != 1 || !strings.Contains(errText, "maintenance") {
			t.Fatalf("admission must remain excluded until activation: %d %s", code, errText)
		}
		code, _, errText = runRelease(t, candidateBinary, path, "upgrade", `{}`)
		if code != 0 {
			t.Fatalf("the actual candidate must support forward recovery: %d %s", code, errText)
		}
		if after, err := hostlease.ReadFence(dataRoot); err != nil || after == nil || after.FenceID != fence.FenceID {
			t.Fatalf("a candidate no-op must retain the activation boundary: %+v %v", after, err)
		}
	})
}

// releaseMigrationSnapshot reads logical schema and manifest state without
// opening through either core's migration path. Refusal must change neither
// schema objects nor migration rows, including pending additive steps.
func releaseMigrationSnapshot(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`
SELECT json_array('schema',type,name,tbl_name,sql) FROM sqlite_schema
UNION ALL
SELECT json_array('migration',version,name,checksum,applied_at,breaking) FROM schema_migrations
ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var snapshot strings.Builder
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		snapshot.WriteString(row)
		snapshot.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	// Fingerprint every actual table on either side of the floor. This is one
	// schema-independent no-write observer, not an old/new query fallback.
	tables, err := db.Query(`SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	if err := tables.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		probe, err := db.Query(`SELECT * FROM ` + quoted + ` LIMIT 0`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := probe.Columns()
		if err != nil {
			t.Fatal(err)
		}
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}
		if err := probe.Err(); err != nil {
			t.Fatal(err)
		}
		for i, column := range columns {
			columns[i] = `quote("` + strings.ReplaceAll(column, `"`, `""`) + `")`
		}
		data, err := db.Query(`SELECT json_array(` + strings.Join(columns, ",") + `) FROM ` + quoted + ` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		for data.Next() {
			var row string
			if err := data.Scan(&row); err != nil {
				t.Fatal(err)
			}
			snapshot.WriteString(name + ":" + row + "\n")
		}
		if err := data.Err(); err != nil {
			t.Fatal(err)
		}
		if err := data.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot.String()
}

// TestDistinctReleasedCoreBreakingUpgradeRequiresStoppedSessions proves
// CD-0111 D3 against the current breaking migration, not a relabeled binary:
// a live released adapter/core pair blocks all migration effects, including
// the additive tail. Only after the operator ends the legacy session and
// confirms its unfenceable tree is idle may the current core raise the floor.
func TestDistinctReleasedCoreBreakingUpgradeRequiresStoppedSessions(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data", "concord")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataRoot, "concord.db")
	releasedSource := extractReleasedSource(t, releasedPairTag)
	older := buildCoreFrom(t, releasedSource, releasedPairTag)
	const currentRelease = "v0.0.0-con836-breaking"
	current := buildCoreFrom(t, repoSourceDir(t), currentRelease)
	oldRoot := installReleaseTree(t, dataRoot, releasedPairTag, older, false)
	currentRoot := installReleaseTree(t, dataRoot, currentRelease, current, true)
	oldBinary := filepath.Join(oldRoot, "bin", "concord")
	currentBinary := filepath.Join(currentRoot, "bin", "concord")
	currentDescriptor := coreDescriptor(t, currentBinary)
	if currentDescriptor["version"] != currentRelease || currentDescriptor["core_binary"] != currentBinary ||
		currentDescriptor["schema_version"] != float64(store.CurrentSchemaVersion()) ||
		currentDescriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) {
		t.Fatalf("the current core must identify its own build and capabilities: %+v", currentDescriptor)
	}
	code, out, errText := runRelease(t, oldBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the released core must initialize its store: %d %s %s", code, out, errText)
	}
	oldAdapter := stampAdapterRuntime(t, filepath.Join(releasedSource, "adapter", "opencode"), oldRoot)
	oldSession := startAdapterSessionOperation(t, oldAdapter, path, "/srv/old-breaking-session")
	before, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	breaking := releasedPendingBreaking(t, releasedPairSchema)
	assertReleasePendingBreaking(t, before, breaking)
	if before.SchemaVersion != releasedPairSchema || before.CompatibilityFloor >= releasedPairSchema {
		t.Fatalf("the released store must await the shipped breaking manifest: %+v", before)
	}
	snapshot := releaseMigrationSnapshot(t, path)
	assertUnadvanced := func(t *testing.T) {
		t.Helper()
		if after := releaseMigrationSnapshot(t, path); after != snapshot {
			t.Fatalf("the refused upgrade changed logical migration state:\nbefore:\n%safter:\n%s", snapshot, after)
		}
		if fence, err := hostlease.ReadFence(dataRoot); err != nil || fence != nil {
			t.Fatalf("the refused upgrade must close only its own boundary: %+v %v", fence, err)
		}
	}
	// A confirmation cannot override an observable live legacy participant.
	for _, input := range []string{`{}`, `{"confirm_sessions_stopped":true}`} {
		code, out, errText = runRelease(t, currentBinary, path, "upgrade", input)
		if code != 1 || out != "" || !strings.Contains(errText, "unfenceable legacy participant") ||
			!strings.Contains(errText, "fence protocol 0") || !strings.Contains(errText, oldRoot) ||
			!strings.Contains(errText, fmt.Sprintf("pid %d", oldSession.command.Process.Pid)) ||
			!strings.Contains(errText, "/srv/old-breaking-session") {
			t.Fatalf("the breaking upgrade must name the live released session before effect: %d %s %s", code, out, errText)
		}
		assertUnadvanced(t)
	}
	// The refusal leaves the real old session able to claim and read its store.
	oldSession.reclaimAndHold(t)
	assertUnadvanced(t)
	oldSession.stop()
	if leases, err := hostlease.List(dataRoot); err != nil || len(leases) != 0 {
		t.Fatalf("the operator must end the old session before migration: %+v %v", leases, err)
	}
	code, out, errText = runRelease(t, currentBinary, path, "upgrade", `{}`)
	if code != 1 || out != "" || !strings.Contains(errText, "without a fence-protocol marker") ||
		!strings.Contains(errText, oldRoot) || !strings.Contains(errText, "confirm_sessions_stopped") {
		t.Fatalf("the idle legacy tree still requires the operator's confirmation: %d %s %s", code, out, errText)
	}
	assertUnadvanced(t)

	code, out, errText = runRelease(t, currentBinary, path, "upgrade", `{"confirm_sessions_stopped":true}`)
	if code != 0 || !strings.Contains(errText, "operator's confirmation") || !strings.Contains(errText, oldRoot) {
		t.Fatalf("the offline operator route must apply the breaking migration: %d %s %s", code, out, errText)
	}
	var report struct {
		SchemaVersion int   `json:"schema_version"`
		Applied       []int `json:"applied"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("the current upgrade report is not JSON: %v: %s", err, out)
	}
	if report.SchemaVersion != store.CurrentSchemaVersion() {
		t.Fatalf("the confirmed upgrade must apply the complete pending tail: %+v", report)
	}
	assertAppliedMigrationTail(t, report.Applied, releasedPairSchema)
	floor := breaking[len(breaking)-1]
	after, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || after.SchemaVersion != report.SchemaVersion || after.CompatibilityFloor != floor || len(after.PendingBreaking) != 0 {
		t.Fatalf("the breaking upgrade must raise the floor to %d: %+v %v", floor, after, err)
	}
	fence, err := hostlease.ReadFence(dataRoot)
	if err != nil || fence == nil || !fence.AuthorizesNativeMigration(currentRoot, currentBinary, report.SchemaVersion) {
		t.Fatalf("the committed breaking upgrade must retain its boundary for activation: %+v %v", fence, err)
	}
	// Re-open through the current core's store-backed route while activation
	// still owns admission exclusion; no second migration is necessary.
	code, out, errText = runRelease(t, currentBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the current core must operate the upgraded store: %d %s %s", code, out, errText)
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil || report.SchemaVersion != after.SchemaVersion || len(report.Applied) != 0 {
		t.Fatalf("the current core must read the upgraded store without new migrations: %+v %v: %s", report, err, out)
	}
	upgradedSnapshot := releaseMigrationSnapshot(t, path)
	code, out, errText = runRelease(t, oldBinary, path, "upgrade", `{}`)
	if code != 1 || out != "" || !strings.Contains(errText, "schema_unsupported") ||
		!strings.Contains(errText, fmt.Sprintf("defines schema version %d", floor)) ||
		!strings.Contains(errText, fmt.Sprintf("this binary defines %d", releasedPairSchema)) {
		t.Fatalf("the old core must refuse the breaking floor, not the maintenance fence: %d %s %s", code, out, errText)
	}
	if final := releaseMigrationSnapshot(t, path); final != upgradedSnapshot {
		t.Fatal("the old core's floor refusal changed the upgraded store")
	}
}

// The baseline is a released source; the candidate is the checkout under test.
const cutoverBaselineTag = "v13.4.0"
const cutoverBaselineSchema = 122

// cutoverEvidenceQueries fingerprints the living evidence a cutover must
// carry across the breaking floor: the active research consumer, the claimed
// worktree with its live session
// landing, and the completed retained worker report. Every query names only
// columns that exist identically at the baseline and candidate schemas.
var cutoverEvidenceQueries = []struct {
	name  string
	query string
}{
	{"active_research_packs", `SELECT pack_id,owner_work_id,current_revision,freshness,expected_version FROM active_research_packs ORDER BY pack_id`},
	{"active_research_revisions", `SELECT pack_id,revision,question,method,freshness FROM active_research_revisions ORDER BY pack_id,revision`},
	{"active_research_consumers", `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers ORDER BY pack_id,consumer_work_id`},
	{"worktree_entries", `SELECT set_id,project_id,claim_op_id,branch,base_sha,state FROM worktree_entries ORDER BY set_id,project_id`},
	{"worktree_occupancy", `SELECT worktree_id,session_ref,host_pid,has_process_identity FROM worktree_occupancy ORDER BY session_ref`},
	{"worker_attempts", `SELECT work_id,attempt_id,lane_id,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at FROM worker_attempts ORDER BY attempt_id`},
	{"durable_knowledge", `SELECT id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version FROM archived_work ORDER BY home_project_id,home_locator_id,id`},
	{"durable_knowledge_products", `SELECT home_project_id,home_locator_id,work_id,product_id FROM archived_work_products ORDER BY home_project_id,home_locator_id,work_id,product_id`},
	{"durable_knowledge_projects", `SELECT home_project_id,home_locator_id,work_id,project_id FROM archived_work_projects ORDER BY home_project_id,home_locator_id,work_id,project_id`},
	{"product_knowledge_homes", `SELECT product_id,project_id,locator_id FROM product_knowledge_homes ORDER BY product_id`},
}

// mustOpenCutoverStore opens the store file directly, without either core's
// migration path, so the witness reads and seeds the exact rows on disk.
func mustOpenCutoverStore(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", store.SQLiteFileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// captureCutoverEvidence reads every living-evidence population as opaque
// row strings, fixed by each query's ORDER BY, so equal stores produce
// equal fingerprints.
func captureCutoverEvidence(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	captured := map[string][]string{}
	for _, evidence := range cutoverEvidenceQueries {
		rows, err := db.Query(evidence.query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var population []string
		for rows.Next() {
			scanned := make([]sql.RawBytes, len(columns))
			values := make([]any, len(columns))
			for i := range scanned {
				values[i] = &scanned[i]
			}
			if err := rows.Scan(values...); err != nil {
				t.Fatal(err)
			}
			row := make([]string, len(columns))
			for i, raw := range scanned {
				row[i] = string(raw)
			}
			population = append(population, strings.Join(row, "\x1f"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		captured[evidence.name] = population
	}
	return captured
}

// Baseline receipts are the exact identities migration 145 must derive from
// the real released log. Only worker payloads remain retained after 151.
func captureBaselineCutoverEvidence(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	captured := captureCutoverEvidence(t, db)
	rows, err := db.Query(`SELECT seq,event_id,kind,subject_type,subject_id,actor,payload_version,payload FROM domain_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var version int
		var id, kind, subjectType, subjectID, actor string
		var payload []byte
		if err := rows.Scan(&seq, &id, &kind, &subjectType, &subjectID, &actor, &version, &payload); err != nil {
			t.Fatal(err)
		}
		captured["effect_receipts"] = append(captured["effect_receipts"], mustJSON(t, []any{seq, id, kind, subjectType, subjectID, actor, version, cliPayloadDigest(t, payload)}))
		if kind == store.WorkerDispatched || kind == store.WorkerCompleted || kind == store.WorkerFailed {
			captured["worker_payloads"] = append(captured["worker_payloads"], mustJSON(t, []any{id, string(payload)}))
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return captured
}

func captureCurrentCutoverEvidence(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	captured := captureCutoverEvidence(t, db)
	for _, query := range []struct{ name, sql string }{
		{"effect_receipts", `SELECT json_array(effect_seq,event_id,kind,subject_type,subject_id,actor,payload_version,payload_digest) FROM effect_receipts ORDER BY effect_seq`},
		{"worker_payloads", `SELECT json_array(event_id,payload) FROM (
SELECT r.effect_seq,r.event_id,CAST(a.dispatch_payload AS TEXT) AS payload FROM effect_receipts r JOIN worker_attempts a ON a.work_id=r.subject_id AND a.dispatch_event_id=r.event_id AND a.dispatch_seq=r.effect_seq AND a.dispatch_payload_version=r.payload_version AND a.dispatch_actor=r.actor WHERE r.kind='worker.dispatched'
UNION ALL SELECT r.effect_seq,r.event_id,CAST(a.terminal_payload AS TEXT) FROM effect_receipts r JOIN worker_attempts a ON a.work_id=r.subject_id AND a.terminal_event_id=r.event_id AND a.terminal_seq=r.effect_seq AND a.terminal_payload_version=r.payload_version AND a.terminal_kind=r.kind WHERE r.kind IN ('worker.completed','worker.failed')) ORDER BY effect_seq`},
	} {
		rows, err := db.Query(query.sql)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				t.Fatal(err)
			}
			captured[query.name] = append(captured[query.name], row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return captured
}

// seedCutoverLivingEvidence writes the baseline schema's minimal living
// evidence exactly the shapes the baseline binary itself writes: research
// packs and consumers are direct transactional writes at this schema, and
// the guarded projections are seeded under the same fold guard the
// migrations use. The claim landing and the completed report also enter the
// event log with their original actors, so the floor migration moves real
// log rows, not only projections.
func seedCutoverLivingEvidence(t *testing.T, path string, sessionPID int) {
	t.Helper()
	sessionStart, err := hostlease.ProcessStart(sessionPID)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := hostlease.List(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var baselineBinary string
	for _, lease := range leases {
		if lease.PID == sessionPID {
			baselineBinary = lease.CoreBinary
		}
	}
	if baselineBinary == "" {
		t.Fatal("the synthetic baseline session must identify its core")
	}
	db := mustOpenCutoverStore(t, path)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string) {
		t.Helper()
		if _, err := tx.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT OR IGNORE INTO fold_guard(active) VALUES (1)`)
	exec(`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at)
		VALUES('pack-cutover-living','cutover-owner',1,'current',1,'2026-10-12T00:00:00Z','2026-10-12T00:00:00Z')`)
	exec(`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at)
		VALUES('pack-cutover-living',1,'does the cutover preserve living evidence?','[]','[]','[]','synthetic cutover witness','2026-10-12T00:00:00Z')`)
	exec(`INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at)
		VALUES('pack-cutover-living',1,'cutover-consumer','decision_basis',1,'2026-10-12T00:00:00Z')`)
	exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES
		('cutover-product-created','product.created','product','cutover-product','operator','2026-10-12T00:00:00Z',1,
			'{"display_name":"Cutover product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}'),
		('cutover-project-created','project.created','project','project','operator','2026-10-12T00:00:00Z',1,
			'{"display_name":"Cutover project"}'),
		('cutover-product-membership','product_project.added','product','cutover-product','operator','2026-10-12T00:00:00Z',1,
			'{"product_id":"cutover-product","project_id":"project","role":"primary","reason":"operator bootstrap","expected_version":1,"resulting_version":2}'),
		('cutover-owner-created','work.created','work_item','cutover-owner','principal:cutover-operator','2026-10-12T00:00:00Z',2,
			'{"work_kind":"research","title":"cutover research owner","priority":0}'),
		('cutover-owner-membership','work.memberships_replaced','work_item','cutover-owner','principal:cutover-operator','2026-10-12T00:00:00Z',1,
			'{"memberships":[{"project_id":"project","role":"primary"}],"expected_version":1,"resulting_version":2}'),
		('cutover-owner-started','work.transitioned','work_item','cutover-owner','principal:cutover-operator','2026-10-12T00:00:00Z',1,
			'{"from":"needed","to":"in_progress","reason":"research in progress","expected_version":2,"resulting_version":3}'),
		('cutover-work-created','work.created','work_item','cutover-consumer','principal:cutover-operator','2026-10-12T00:00:00Z',2,
			'{"work_kind":"research","title":"cutover living consumer","priority":0}'),
		('cutover-consumer-membership','work.memberships_replaced','work_item','cutover-consumer','principal:cutover-operator','2026-10-12T00:00:00Z',1,
			'{"memberships":[{"project_id":"project","role":"primary"}],"expected_version":1,"resulting_version":2}'),
		('cutover-consumer-started','work.transitioned','work_item','cutover-consumer','principal:cutover-operator','2026-10-12T00:00:00Z',1,
			'{"from":"needed","to":"in_progress","reason":"research in progress","expected_version":2,"resulting_version":3}'),
		('op-cutover-claim:worktree-created','work.worktree_created','work_item','cutover-consumer','ses-cutover-living','2026-10-12T00:01:00Z',1,
			'{"expected_version":3,"resulting_version":4,"set_id":"wts:cutover-consumer","project_id":"project","claim_op_id":"op-cutover-claim","branch":"simplify/cutover-living","base_sha":"1f2e3d4c5b6a79887766554433221100ffeeddccb","path":"/srv/cutover-living-worktree","repository_id":"cutover-repo","occupant_session_ref":"ses-cutover-living","host_pid":` + fmt.Sprint(sessionPID) + `,"host_pid_start":` + fmt.Sprint(sessionStart) + `,"git_facts":{"branch":"simplify/cutover-living","head_sha":"1f2e3d4c5b6a79887766554433221100ffeeddccb"}}'),
		('cutover-consumer:session-claim-landed:ses-cutover-living:1','work.session_claim_landed','work_item','cutover-consumer','ses-cutover-living','2026-10-12T00:01:00Z',1,
			'{"work_id":"cutover-consumer","project_id":"project","claim_op_id":"op-cutover-claim","session_ref":"ses-cutover-living","source_directories":[],"landed_directory":"/srv/cutover-living-worktree","host_pid":` + fmt.Sprint(sessionPID) + `,"host_pid_start":` + fmt.Sprint(sessionStart) + `}'),
		('cutover-attempt-dispatched','worker.dispatched','work_item','cutover-consumer','worker:cutover-living','2026-10-12T00:02:00Z',3,
			'{"attempt_id":"attempt-cutover-living-1","lane_id":"implement","lane_version":1,"lane_digest":"sha256:ec541caf3d4df2d5fe70602cf65e747f19e5ac525b001fdd86ea7cf921b737fc","capability_class":"implementation","readback_model":"synthetic/cutover-worker","packet_schema_version":"1.0","report_schema_version":"1.0","packet_digest":"sha256:` + strings.Repeat("0", 64) + `","host_provenance":{"digest":"sha256:` + strings.Repeat("0", 64) + `","sources":[{"kind":"unenumerated"}]}}'),
		('cutover-attempt-completed','worker.completed','work_item','cutover-consumer','worker:cutover-living','2026-10-12T00:10:00Z',1,
			'{"attempt_id":"attempt-cutover-living-1","readback_model":"synthetic/cutover-worker","report_schema_version":"1.0"}')`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Materialize fold-owned rows with the released core; research stays direct authority.
	if code, out, errText := runRelease(t, baselineBinary, path, "recover-fold-guard", `{}`); code != 0 {
		t.Fatalf("materialize the released fixture history: %d %s %s", code, out, errText)
	}
}

// cutoverPlanningProjectionsAbsent reports whether none of the named tables
// the planning mirror owned remains in the migrated schema.
func cutoverPlanningProjectionsAbsent(t *testing.T, path string, tables ...string) bool {
	t.Helper()
	db := mustOpenCutoverStore(t, path)
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name IN (`+strings.TrimSuffix(strings.Repeat("?,", len(tables)), ",")+`)`, toAnySlice(tables)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	remaining := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		remaining++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return remaining == 0
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// TestCutoverBaselineCoreBreakingUpgradePreservesLivingEvidence exercises
// the maintenance fence and verified snapshot with a released baseline and
// the checked-out candidate core.
// An active baseline session blocks migration even with operator confirmation.
// After it stops, the confirmed upgrade preserves living evidence, archives
// retired planning projections, and excludes the baseline core at the new floor.
func TestCutoverBaselineCoreBreakingUpgradePreservesLivingEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the released baseline and checked-out candidate cores")
	}
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data", "concord")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataRoot, "concord.db")
	baselineVersion := "v0.0.0-con867-cutover-baseline"
	candidateVersion := "v0.0.0-con867-cutover-candidate"
	baselineSource := extractReleasedSource(t, cutoverBaselineTag)
	t.Logf("binary sources: baseline=%s candidate=checked-out source", cutoverBaselineTag)
	candidateSource := repoSourceDir(t)
	baseline := buildCoreFrom(t, baselineSource, baselineVersion)
	candidate := buildCoreFrom(t, candidateSource, candidateVersion)
	// The candidate tree carries the fence-protocol marker its installer
	// stamps. The baseline tree is staged without one: that unstamped
	// installation is what puts the stopped-tree confirmation in the
	// operator's hands, while the session itself remains a fenceable
	// participant its lease proves below.
	baselineRoot := installReleaseTree(t, dataRoot, baselineVersion, baseline, false)
	candidateRoot := installReleaseTree(t, dataRoot, candidateVersion, candidate, true)
	baselineBinary := filepath.Join(baselineRoot, "bin", "concord")
	candidateBinary := filepath.Join(candidateRoot, "bin", "concord")

	baselineDescriptor := coreDescriptor(t, baselineBinary)
	if baselineDescriptor["version"] != baselineVersion || baselineDescriptor["schema_version"] != float64(cutoverBaselineSchema) ||
		baselineDescriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) || baselineDescriptor["core_binary"] != baselineBinary {
		t.Fatalf("the baseline core must identify its own build and fence capability: %+v", baselineDescriptor)
	}
	candidateSchema := store.CurrentSchemaVersion()
	candidateDescriptor := coreDescriptor(t, candidateBinary)
	if candidateDescriptor["version"] != candidateVersion || candidateDescriptor["schema_version"] != float64(candidateSchema) ||
		candidateDescriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) || candidateDescriptor["core_binary"] != candidateBinary ||
		candidateDescriptor["manifest_digest"] == baselineDescriptor["manifest_digest"] {
		t.Fatalf("the candidate core must identify its own distinct build and contract: %+v", candidateDescriptor)
	}

	// The baseline core initializes its own store at schema 122.
	code, out, errText := runRelease(t, baselineBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the baseline core must initialize its store: %d %s %s", code, out, errText)
	}
	var initialized struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(out), &initialized); err != nil {
		t.Fatalf("the baseline upgrade report is not JSON: %v: %s", err, out)
	}
	if initialized.SchemaVersion != cutoverBaselineSchema {
		t.Fatalf("the baseline store's schema = %d, want %d", initialized.SchemaVersion, cutoverBaselineSchema)
	}

	// The shipped manifest, not a hardcoded list, owns the pending breaking
	// tail and the floor it will raise.
	breaking := releasedPendingBreaking(t, cutoverBaselineSchema)
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{"plan":true}`)
	if code != 0 {
		t.Fatalf("the candidate's plan must read the baseline store: %d %s %s", code, out, errText)
	}
	var plan struct {
		store.UpgradeReadiness
		SnapshotRequired bool `json:"snapshot_required"`
	}
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatalf("the candidate's plan is not JSON: %v: %s", err, out)
	}
	assertReleasePendingBreaking(t, plan.UpgradeReadiness, breaking)
	if plan.SchemaVersion != cutoverBaselineSchema || !plan.ActivationBlocked || !plan.SnapshotRequired {
		t.Fatalf("the baseline store must await the shipped breaking tail behind a verified snapshot: %+v", plan.UpgradeReadiness)
	}

	// A real baseline adapter session claims its lease through its own
	// pinned core, then the store carries the living evidence it works on.
	baselineAdapter := stampAdapterRuntime(t, filepath.Join(baselineSource, "adapter", "opencode"), baselineRoot)
	session := startAdapterSessionOperation(t, baselineAdapter, path, "/srv/cutover-baseline-session")
	sessionPID := session.command.Process.Pid
	seedCutoverLivingEvidence(t, path, sessionPID)
	code, out, errText = runRelease(t, baselineBinary, path, "project-locator-add", mustJSON(t, map[string]any{
		"project_id": "project", "locator_id": "cutover-knowledge", "kind": "canonical_path", "value": t.TempDir(), "expected_version": 1,
	}))
	if code != 0 {
		t.Fatalf("register baseline knowledge locator: %d %s %s", code, out, errText)
	}
	code, out, errText = runRelease(t, baselineBinary, path, "product-knowledge-home-designate", `{"product_id":"cutover-product","project_id":"project","locator_id":"cutover-knowledge","expected_version":2}`)
	if code != 0 {
		t.Fatalf("designate baseline Product knowledge home: %d %s %s", code, out, errText)
	}
	// Seed the released index's durable lesson and explicit Product scope
	// before cutover, not by reconstructing knowledge in the migrated store.
	knowledgeTx, err := mustOpenCutoverStore(t, path).Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer knowledgeTx.Rollback()
	if _, err := knowledgeTx.Exec(`
INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version)
VALUES('lesson-cutover','lesson','Preserve required research pins','2026-10-12T00:00:00Z','published','["cutover"]','completed',0,'A new research revision must not move an existing required consumer pin.','project','cutover-knowledge','.concord/docs/lessons/cutover.md','1f2e3d4c5b6a79887766554433221100ffeeddccb','sha256:` + strings.Repeat("a", 64) + `','explicit','1.0');
INSERT INTO archived_work_products(home_project_id,home_locator_id,work_id,product_id)
VALUES('project','cutover-knowledge','lesson-cutover','cutover-product');
INSERT INTO archived_work_projects(home_project_id,home_locator_id,work_id,project_id)
VALUES('project','cutover-knowledge','lesson-cutover','project');
DELETE FROM fold_guard;`); err != nil {
		t.Fatalf("seed baseline durable Product knowledge: %v", err)
	}
	if err := knowledgeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	want := captureBaselineCutoverEvidence(t, mustOpenCutoverStore(t, path))
	if len(want["durable_knowledge"]) != 1 || len(want["durable_knowledge_products"]) != 1 || len(want["durable_knowledge_projects"]) != 1 || len(want["product_knowledge_homes"]) != 1 {
		t.Fatalf("baseline must carry one durable lesson with Product and Project scope: %+v", want)
	}
	if len(want["effect_receipts"]) != 15 {
		t.Fatalf("seeded identities = %d rows, want Product, Project, Product membership, two work creations, two work memberships, two lifecycle transitions, worktree creation, landing, dispatch, completion, Project locator, and Product knowledge home", len(want["effect_receipts"]))
	}
	if len(want["active_research_consumers"]) != 1 ||
		!strings.HasPrefix(want["active_research_consumers"][0], "pack-cutover-living\x1f1\x1fcutover-consumer\x1fdecision_basis\x1f1\x1f") {
		t.Fatalf("seeded research consumers = %v, want the one required live consumer", want["active_research_consumers"])
	}
	if !strings.HasSuffix(want["worktree_occupancy"][0], fmt.Sprintf("\x1fses-cutover-living\x1f%d\x1f1", sessionPID)) {
		t.Fatalf("seeded occupancy = %q, want the live session's landing", want["worktree_occupancy"])
	}
	if len(want["worker_attempts"]) != 1 || !strings.Contains(want["worker_attempts"][0], "\x1fcompleted\x1f") {
		t.Fatalf("seeded worker attempts = %v, want the one completed retained report", want["worker_attempts"])
	}
	assertPreserved := func(t *testing.T, capture func(*testing.T, *sql.DB) map[string][]string) {
		t.Helper()
		got := capture(t, mustOpenCutoverStore(t, path))
		for name, expected := range want {
			if strings.Join(got[name], "\x1e") != strings.Join(expected, "\x1e") {
				t.Fatalf("%s evidence =\n%s\nwant\n%s", name, strings.Join(got[name], "\n"), strings.Join(expected, "\n"))
			}
		}
	}
	snapshot := releaseMigrationSnapshot(t, path)
	assertUnadvanced := func(t *testing.T) {
		t.Helper()
		if after := releaseMigrationSnapshot(t, path); after != snapshot {
			t.Fatalf("the refused upgrade changed logical migration state:\nbefore:\n%s\nafter:\n%s", snapshot, after)
		}
		if backups, err := filepath.Glob(filepath.Join(dataRoot, "backups", "*.db")); err != nil || len(backups) != 0 {
			t.Fatalf("a refused upgrade must not create a snapshot before the participant gate: %v %v", backups, err)
		}
		if fence, err := hostlease.ReadFence(dataRoot); err != nil || fence != nil {
			t.Fatalf("a refused upgrade must close the boundary it opened: %+v %v", fence, err)
		}
		assertPreserved(t, captureBaselineCutoverEvidence)
	}

	// The unstamped baseline tree refuses first, and the refusal names the
	// tree and the confirmation. The live participant itself is fenceable —
	// the legacy-participant gate never fires for it.
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{}`)
	if code != 1 || out != "" || !strings.Contains(errText, "without a fence-protocol marker") ||
		!strings.Contains(errText, baselineRoot) || !strings.Contains(errText, "confirm_sessions_stopped") {
		t.Fatalf("the unstamped baseline tree must refuse the breaking upgrade before effect: %d %s %s", code, out, errText)
	}
	if strings.Contains(errText, "unfenceable legacy participant") {
		t.Fatalf("the baseline session is a fenceable protocol-%d participant, not legacy: %s", hostlease.CurrentFenceProtocol, errText)
	}
	assertUnadvanced(t)

	// A confirmation cannot bypass the observable live baseline session: the
	// operator's own statement runs into the live-session gate, which names
	// the session's pid, tree, schema and directory.
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{"confirm_sessions_stopped":true}`)
	if code != 1 || out != "" || !strings.Contains(errText, "upgrade_blocked") ||
		!strings.Contains(errText, fmt.Sprintf("pid %d", sessionPID)) ||
		!strings.Contains(errText, baselineRoot) ||
		!strings.Contains(errText, fmt.Sprintf("schema version %d", cutoverBaselineSchema)) ||
		!strings.Contains(errText, fmt.Sprintf("before migration %d", breaking[0])) ||
		!strings.Contains(errText, "/srv/cutover-baseline-session") {
		t.Fatalf("confirmation cannot bypass the observable live baseline session: %d %s %s", code, out, errText)
	}
	assertUnadvanced(t)

	// The live session's own release reports its lease: one fenceable
	// protocol-1 participant pinned to its own tree at schema 122.
	code, out, errText = runRelease(t, baselineBinary, path, "host-leases", `{}`)
	if code != 0 {
		t.Fatalf("the baseline release must list its own lease: %d %s %s", code, out, errText)
	}
	var listing struct {
		Leases []hostlease.Lease `json:"leases"`
	}
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		t.Fatalf("the baseline lease listing is not JSON: %v: %s", err, out)
	}
	if len(listing.Leases) != 1 {
		t.Fatalf("the live baseline session must hold exactly one lease: %+v", listing.Leases)
	}
	lease := listing.Leases[0]
	if lease.ReleaseRoot != baselineRoot || lease.CoreBinary != baselineBinary || lease.SchemaVersion != cutoverBaselineSchema ||
		lease.FenceProtocol != hostlease.CurrentFenceProtocol || lease.Directory != "/srv/cutover-baseline-session" {
		t.Fatalf("the baseline session's lease must name its own fenceable tree: %+v", lease)
	}
	// Old-session continuity before the operator ends it.
	session.reclaimAndHold(t)
	session.stop()
	if leases, err := hostlease.List(dataRoot); err != nil || len(leases) != 0 {
		t.Fatalf("the ended session must release its lease before migration: %+v %v", leases, err)
	}

	// The stopped participant's unstamped tree still refuses without the
	// operator's confirmation; no effect either way.
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{}`)
	if code != 1 || out != "" || !strings.Contains(errText, "without a fence-protocol marker") ||
		!strings.Contains(errText, baselineRoot) || !strings.Contains(errText, "confirm_sessions_stopped") {
		t.Fatalf("the stopped session's unstamped tree still requires the operator's confirmation: %d %s %s", code, out, errText)
	}
	assertUnadvanced(t)

	// The confirmation route applies the complete manifest-derived tail.
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{"confirm_sessions_stopped":true}`)
	if code != 0 || !strings.Contains(errText, "operator's confirmation") || !strings.Contains(errText, baselineRoot) {
		t.Fatalf("the stopped-participant confirmation route must apply the breaking tail: %d %s %s", code, out, errText)
	}
	var report struct {
		SchemaVersion int    `json:"schema_version"`
		Applied       []int  `json:"applied"`
		Backup        string `json:"backup"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("the confirmed upgrade report is not JSON: %v: %s", err, out)
	}
	if report.SchemaVersion != candidateSchema {
		t.Fatalf("the confirmed upgrade must apply the complete pending tail: %+v", report)
	}
	assertAppliedMigrationTail(t, report.Applied, cutoverBaselineSchema)
	for _, pending := range breaking {
		found := false
		for _, applied := range report.Applied {
			if applied == pending {
				found = true
			}
		}
		if !found {
			t.Fatalf("the confirmed upgrade must apply pending breaking migration %d: %+v", pending, report)
		}
	}
	if report.Backup == "" || !strings.HasPrefix(report.Backup, dataRoot) {
		t.Fatalf("the breaking upgrade must name its pre-migration snapshot beside the store: %q", report.Backup)
	}
	// The pre-migration snapshot verifies at the baseline schema and watermark.
	if _, err := os.Stat(report.Backup); err != nil {
		t.Fatalf("the required pre-migration snapshot must exist: %v", err)
	}
	verified, err := store.VerifyBackup(context.Background(), report.Backup, cutoverBaselineSchema)
	if err != nil {
		t.Fatalf("the pre-migration snapshot must verify at the baseline schema: %v", err)
	}
	if verified.SchemaVersion != cutoverBaselineSchema || verified.EventWatermark != 0 {
		t.Fatalf("the verified snapshot = %+v, want the baseline schema and zero pre-revision inventory", verified)
	}

	// The migrated store requires the manifest-derived floor, keeps the
	// activation boundary open, and loses only the retired planning mirror.
	floor := breaking[len(breaking)-1]
	readiness, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil || readiness.SchemaVersion != candidateSchema || readiness.CompatibilityFloor != floor ||
		len(readiness.PendingBreaking) != 0 || readiness.ActivationBlocked {
		t.Fatalf("the migrated store must require the manifest-derived floor %d: %+v %v", floor, readiness, err)
	}
	fence, err := hostlease.ReadFence(dataRoot)
	if err != nil || fence == nil || !fence.AuthorizesNativeMigration(candidateRoot, candidateBinary, candidateSchema) {
		t.Fatalf("the committed breaking boundary must remain open for activation: %+v %v", fence, err)
	}
	if !cutoverPlanningProjectionsAbsent(t, path, "initiative_entries", "linear_outbox") {
		t.Fatal("the retired planning projections must be absent after the migration")
	}
	assertPreserved(t, captureCurrentCutoverEvidence)

	upgradedSnapshot := releaseMigrationSnapshot(t, path)
	// The candidate operates the migrated store with no further migration.
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the candidate must operate the migrated store: %d %s %s", code, out, errText)
	}
	var noop struct {
		SchemaVersion int   `json:"schema_version"`
		Applied       []int `json:"applied"`
	}
	if err := json.Unmarshal([]byte(out), &noop); err != nil || noop.SchemaVersion != candidateSchema || len(noop.Applied) != 0 {
		t.Fatalf("the candidate's no-op must apply nothing: %+v %v %s", noop, err, out)
	}
	// The baseline core refuses the advanced floor, and the refusal has no
	// effect on the migrated store or its living evidence.
	code, out, errText = runRelease(t, baselineBinary, path, "upgrade", `{}`)
	if code != 1 || out != "" || !strings.Contains(errText, "schema_unsupported") ||
		!strings.Contains(errText, fmt.Sprintf("defines schema version %d", floor)) ||
		!strings.Contains(errText, fmt.Sprintf("this binary defines %d", cutoverBaselineSchema)) {
		t.Fatalf("the baseline core must refuse the advanced floor without effect: %d %s %s", code, out, errText)
	}
	if after := releaseMigrationSnapshot(t, path); after != upgradedSnapshot {
		t.Fatal("the baseline core's floor refusal changed the migrated store")
	}
	assertPreserved(t, captureCurrentCutoverEvidence)

	// Continue the surviving owner's actual research through the current
	// transactional API. A successful upgrade no-op alone proves no authoring.
	ctx := context.Background()
	current, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	assertActiveWithoutLog := func() {
		t.Helper()
		var logs, active int
		if err := current.DatabaseForTesting().QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='domain_events'`).Scan(&logs); err != nil {
			t.Fatal(err)
		}
		if err := current.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id IN ('cutover-owner','cutover-consumer') AND lifecycle='in_progress'`).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if logs != 0 || active != 2 {
			t.Fatalf("continued work requires both surviving active items and no domain_events: logs=%d active=%d", logs, active)
		}
	}
	assertActiveWithoutLog()
	const continuedQuestion = "Can the surviving research owner publish a new revision without moving its required consumer?"
	if err := current.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.AppendResearchRevisionWithinTx(ctx, tx, store.AppendResearchRevisionRequest{
			PackID: "pack-cutover-living", ExpectedVersion: 1,
			Revision: store.ResearchRevisionInput{
				Question: continuedQuestion, Method: "compare the new revision with the retained required consumer pin",
				ScopeIn: json.RawMessage(`[]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`[]`),
			},
		})
		return err
	}); err != nil {
		t.Fatalf("surviving active research must remain authorable without the log: %v", err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	current, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	continued, err := current.ReadResearchPack(ctx, store.ResearchReadRequest{PackID: "pack-cutover-living", Limit: 100})
	if err != nil {
		t.Fatalf("read continued research after reopen: %v", err)
	}
	if continued.OwnerWorkID != "cutover-owner" || continued.CurrentRevision != 2 || continued.ExpectedVersion != 2 ||
		len(continued.Revisions) != 2 || continued.Revisions[0].Question != "does the cutover preserve living evidence?" || continued.Revisions[1].Question != continuedQuestion ||
		len(continued.Consumers) != 1 || continued.Consumers[0].ConsumerWorkID != "cutover-consumer" || continued.Consumers[0].Revision != 1 || !continued.Consumers[0].Required {
		t.Fatalf("continued research must persist its new revision and retain the original required consumer pin: %+v", continued)
	}
	assertActiveWithoutLog()
	afterContinuation := captureCurrentCutoverEvidence(t, current.DatabaseForTesting())
	for name, expected := range want {
		if name == "active_research_packs" || name == "active_research_revisions" {
			continue // Only the owner's research revision and pack version advance.
		}
		got := afterContinuation[name]
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("continued work changed retained %s: got %v, want %v", name, got, expected)
		}
	}
}

// stampAdapterRuntime copies one source tree's adapter runtime into a
// private directory and stamps the release constants exactly the way
// scripts/install.py stamps them at install time: absolute paths into the
// release the adapter is pinned to. A session that runs from the copy calls
// exactly that release's core binary, never through PATH (CD-0111 D1).
func stampAdapterRuntime(t *testing.T, sourceAdapterDir, releaseRoot string) string {
	t.Helper()
	runDir := filepath.Join(t.TempDir(), "adapter")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(sourceAdapterDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".ts") && !strings.HasSuffix(entry.Name(), ".js")) || strings.HasSuffix(entry.Name(), ".test.ts") || strings.HasSuffix(entry.Name(), ".test.js") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sourceAdapterDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, entry.Name()), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	core := filepath.Join(releaseRoot, "bin", "concord")
	stamped := "// Code stamped as the installer stamps generated-release.ts; DO NOT EDIT.\n" +
		"// CD-0111 D1: a session runs every core call against the release it\n" +
		"// started on. These constants are absolute paths into that release.\n" +
		fmt.Sprintf("export const releaseRoot: string = %q\n", releaseRoot) +
		fmt.Sprintf("export const coreBinary: string = %q\n", core)
	if err := os.WriteFile(filepath.Join(runDir, "generated-release.ts"), []byte(stamped), 0o644); err != nil {
		t.Fatal(err)
	}
	return runDir
}

// adapterSessionDriver is the program a representative pinned session runs:
// the plugin factory's lease claim (CD-0111 D1) through the release's own
// adapter code and its own stamped core, then store-backed reads through
// the same adapter dispatch runner and the same pinned core — the transport
// every adapter operation uses — holding the session and its lease open
// until released. The store-backed leg answers the CON-807 evidence bar:
// lease metadata alone never opens the shared store, while this read does.
const adapterSessionDriver = `import { claimHostLease, hostLeaseFault } from "./host-lease"
import { concordBinaryPath, defaultRunner } from "./dispatch"

async function claim(label: string) {
  await claimHostLease(process.pid, { directory: process.env.PAIR_SESSION_DIRECTORY ?? "" })
  const fault = hostLeaseFault()
  if (fault) {
    console.error(label + " FAULT " + fault)
    process.exit(1)
  }
  console.log(label + " OK")
}

async function storeBackedRead(label: string) {
  // The adapter's own dispatch runner, the release's own pinned core, and
  // the store-backed upgrade verb: the same transport invokeConcordOperation
  // uses. The read must open and answer over the shared store this pair
  // runs on — before and after the other release advances it additively.
  const abort = new AbortController()
  const result = await defaultRunner.run([concordBinaryPath(), "upgrade"], "{}", abort.signal)
  if (result.exitCode !== 0) {
    console.error(label + " FAULT exit " + result.exitCode + " " + result.stderr.slice(0, 300))
    process.exit(1)
  }
  const report = JSON.parse(result.stdout.trim().split("\n").pop() ?? "{}")
  if (typeof report.schema_version !== "number") {
    console.error(label + " FAULT no schema in " + result.stdout.slice(0, 200))
    process.exit(1)
  }
  console.log(label + " OK " + report.schema_version)
}

await claim("PAIR-ADAPTER-LEASE")
await storeBackedRead("PAIR-ADAPTER-READ")
// Hold the session open: the lease must stay live beside the other pair's.
const input = await new Promise<string>((resolve) => {
  let buffered = ""
  process.stdin.on("data", (chunk) => {
    buffered += chunk
    const lines = buffered.split("\n")
    buffered = lines.pop() ?? ""
    if (lines.length > 0) resolve(lines[0])
  })
  process.stdin.on("end", () => resolve(""))
})
if (input === "reclaim") {
  await claim("PAIR-ADAPTER-RECLAIM")
}
if (input === "reclaim" || input === "read") {
  await storeBackedRead("PAIR-ADAPTER-READ-AGAIN")
}
await new Promise<void>(() => {})
`

// adapterSession is one live pinned session: a bun process running the
// stamped adapter's lease claim against its release's core. It stays live
// until stopped, so its lease observes like a real session's.
type adapterSession struct {
	command  *exec.Cmd
	stdin    io.WriteCloser
	lines    chan string
	wait     chan error
	released bool
}

// startAdapterSessionOperation starts one representative released
// adapter/session operation (CON-807): the adapter code that release
// actually ships claims the host lease by calling its own pinned core —
// no CLI substitution, no synthetic pid. The bun process is the session.
func startAdapterSessionOperation(t *testing.T, adapterDir, dbPath, directory string) *adapterSession {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skipf("bun is unavailable for the adapter session operation: %v", err)
	}
	driver := filepath.Join(adapterDir, "pair-session-driver.ts")
	if err := os.WriteFile(driver, []byte(adapterSessionDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bun, "run", driver)
	command.Dir = adapterDir
	command.Env = append(os.Environ(), dbOverrideEnv+"="+dbPath, "PAIR_SESSION_DIRECTORY="+directory)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatalf("cannot start the pinned adapter session: %v", err)
	}
	session := &adapterSession{
		command: command,
		stdin:   stdin,
		lines:   make(chan string, 16),
		wait:    make(chan error, 1),
	}
	go func() { session.wait <- command.Wait() }()
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			session.lines <- scanner.Text()
		}
		close(session.lines)
	}()
	t.Cleanup(session.stop)
	session.awaitClaim(t, "PAIR-ADAPTER-LEASE")
	session.awaitClaim(t, "PAIR-ADAPTER-READ")
	return session
}

// awaitClaim waits for one labeled operation to succeed. Labels carry a
// suffix on success (a store-backed read appends the schema it read), so a
// success line is the label followed by " OK" and anything after it.
func (s *adapterSession) awaitClaim(t *testing.T, label string) string {
	t.Helper()
	for {
		select {
		case line, ok := <-s.lines:
			if !ok {
				t.Fatalf("the pinned adapter session ended before %s", label)
			}
			switch {
			case strings.HasPrefix(line, label+" OK"):
				return line
			case strings.HasPrefix(line, label+" FAULT"):
				t.Fatalf("the pinned adapter session's %s claim failed: %s", label, line)
			}
		case <-time.After(60 * time.Second):
			t.Fatalf("the pinned adapter session never reported %s", label)
		}
	}
}

// reclaimAndHold asks the session to re-claim its lease and re-run its
// store-backed read — old-session continuity across the newer release's
// advance, through the shared store and not only lease metadata — and keeps
// holding.
func (s *adapterSession) reclaimAndHold(t *testing.T) {
	t.Helper()
	if _, err := s.stdin.Write([]byte("reclaim\n")); err != nil {
		t.Fatalf("cannot signal the pinned session to re-claim: %v", err)
	}
	s.awaitClaim(t, "PAIR-ADAPTER-RECLAIM")
	s.awaitClaim(t, "PAIR-ADAPTER-READ-AGAIN")
}

// stop ends the session.
func (s *adapterSession) stop() {
	if s.released {
		return
	}
	s.released = true
	_ = s.command.Process.Kill()
	<-s.wait
}

// repoSourceDir names this repository's source root for building the
// current core inside the pair test.
func repoSourceDir(t *testing.T) string {
	t.Helper()
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join(working, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("cannot locate the repository source at %s: %v", root, err)
	}
	return root
}

// unmarkedInstalledReleaseTree installs a runnable release tree with no
// fence-protocol marker: a legacy core the maintenance fence cannot exclude.
func unmarkedInstalledReleaseTree(t *testing.T, root string) {
	t.Helper()
	legacy := filepath.Join(root, "v0.0.7", "bin")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "concord"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// An installed release tree without a fence-protocol marker names a legacy
// core the maintenance boundary cannot exclude. Without the operator's
// confirmation the incompatible migration refuses before effect, names the
// tree and the confirmation, and closes the boundary it opened (CON-807).
func TestUpgradeRefusesAnUnmarkedInstalledReleaseTree(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	unmarkedInstalledReleaseTree(t, root)

	code, _, errOut := runUpgradeStdin(t, `{}`)
	if code != 1 {
		t.Fatalf("an unmarked installed release tree must refuse the upgrade: %d %s", code, errOut)
	}
	if !strings.Contains(errOut, "without a fence-protocol marker") || !strings.Contains(errOut, "v0.0.7") || !strings.Contains(errOut, "confirm_sessions_stopped") {
		t.Fatalf("the refusal must name the unmarked tree and the confirmation: %s", errOut)
	}
	fence, err := hostlease.ReadFence(root)
	if err != nil || fence != nil {
		t.Fatalf("the refused boundary must close again: %+v %v", fence, err)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("the store must still read after the refusal: %v", err)
	}
	if len(plan.PendingBreaking) == 0 {
		t.Fatalf("the refusal must leave the breaking tail pending: %+v", plan)
	}
}

// The operator's confirmation that no session runs on an unmarked tree lets
// the incompatible migration proceed inside the boundary: the command names
// the tree it proceeded past, applies the breaking tail, and keeps the fence
// for the activation (CON-807).
func TestUpgradeProceedsPastAnUnmarkedTreeOnOperatorConfirmation(t *testing.T) {
	previous := version.Value
	version.Value = "v-test"
	t.Cleanup(func() { version.Value = previous })
	path, root := cliStoreRoot(t)
	pendingBreakingStore(t, path, true)
	unmarkedInstalledReleaseTree(t, root)

	code, _, errOut := runUpgradeStdin(t, `{"confirm_sessions_stopped":true}`)
	if code != 0 {
		t.Fatalf("a confirmed upgrade must apply: %d %s", code, errOut)
	}
	if !strings.Contains(errOut, "operator's confirmation") || !strings.Contains(errOut, "v0.0.7") {
		t.Fatalf("the command must name the tree it proceeded past: %s", errOut)
	}
	plan, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatalf("the migrated store must read: %v", err)
	}
	if len(plan.PendingBreaking) != 0 {
		t.Fatalf("the confirmed upgrade must apply the breaking tail: %+v", plan)
	}
	if fence, err := hostlease.ReadFence(root); err != nil || fence == nil {
		t.Fatalf("only the activation closes the boundary: %+v %v", fence, err)
	}
}
