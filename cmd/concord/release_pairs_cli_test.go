package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agent"
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
		if upgrade.SchemaVersion != store.CurrentSchemaVersion() || len(upgrade.Applied) != store.CurrentSchemaVersion()-compatiblePairSchema {
			t.Fatalf("the candidate must commit the complete pending tail: %+v", upgrade)
		}
		for i, applied := range upgrade.Applied {
			if applied != compatiblePairSchema+i+1 {
				t.Fatalf("the candidate must apply every pending step in order: %+v", upgrade)
			}
		}
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
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`
SELECT json_array('schema',type,name,tbl_name,sql) FROM sqlite_schema
UNION ALL
SELECT json_array('migration',version,name,checksum,applied_at,breaking) FROM schema_migrations
UNION ALL
SELECT json_array('events',count(*),max(seq)) FROM domain_events
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
	if report.SchemaVersion != store.CurrentSchemaVersion() || len(report.Applied) != store.CurrentSchemaVersion()-releasedPairSchema {
		t.Fatalf("the confirmed upgrade must apply the complete pending tail: %+v", report)
	}
	for i, applied := range report.Applied {
		if applied != releasedPairSchema+i+1 {
			t.Fatalf("the confirmed upgrade must apply every pending step in order: %+v", report)
		}
	}
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

// The research-context pair's baseline is the newest release below the
// promoted research-context workflow heads. Both cores define the same
// schema, so the pair isolates the registry-head delta, and every baseline
// refusal below is the unsupported-head refusal, never a schema-floor
// refusal.
const researchPairBaselineTag = "v14.4.7"

// cutoverEvidenceQueries fingerprints the living evidence a cutover must
// carry across the breaking floor: the original event log with its actors,
// the active research consumer, the claimed worktree with its live session
// landing, and the completed retained worker report. Every query names only
// columns that exist identically at the baseline and candidate schemas.
var cutoverEvidenceQueries = []struct {
	name  string
	query string
}{
	{"domain_events", `SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`},
	{"active_research_packs", `SELECT pack_id,owner_work_id,current_revision,freshness,expected_version FROM active_research_packs ORDER BY pack_id`},
	{"active_research_revisions", `SELECT pack_id,revision,question,method,freshness FROM active_research_revisions ORDER BY pack_id,revision`},
	{"active_research_consumers", `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers ORDER BY pack_id,consumer_work_id`},
	{"worktree_entries", `SELECT set_id,project_id,claim_op_id,branch,base_sha,state FROM worktree_entries ORDER BY set_id,project_id`},
	{"worktree_occupancy", `SELECT worktree_id,session_ref,host_pid,has_process_identity FROM worktree_occupancy ORDER BY session_ref`},
	{"worker_attempts", `SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,dispatched_at,completed_at FROM worker_attempts ORDER BY attempt_id`},
}

// mustOpenCutoverStore opens the store file directly, without either core's
// migration path, so the witness reads and seeds the exact rows on disk.
func mustOpenCutoverStore(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// evidenceQueries is the shared shape of one pair's fingerprinted
// populations: a stable name beside the read that captures it.
type evidenceQueries = []struct {
	name  string
	query string
}

// captureEvidenceRows reads every population one query list names as opaque
// row strings. Row order is fixed by each query's ORDER BY, so equal stores
// produce equal fingerprints.
func captureEvidenceRows(t *testing.T, db *sql.DB, queries evidenceQueries) map[string][]string {
	t.Helper()
	captured := map[string][]string{}
	for _, evidence := range queries {
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

// captureCutoverEvidence reads every living-evidence population as opaque
// row strings, fixed by each query's ORDER BY, so equal stores produce
// equal fingerprints.
func captureCutoverEvidence(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	return captureEvidenceRows(t, db, cutoverEvidenceQueries)
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
	exec(`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at)
		VALUES('cutover-product','Cutover product','prototype','operator_only',1,'2026-10-12T00:00:00Z','2026-10-12T00:00:00Z');
		INSERT INTO projects(id,display_name,version,created_at,updated_at)
		VALUES('project','Cutover project',1,'2026-10-12T00:00:00Z','2026-10-12T00:00:00Z');
		INSERT INTO product_projects(product_id,project_id,role) VALUES('cutover-product','project','primary')`)
	exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES
		('cutover-owner','research','cutover research owner','in_progress',0,1,'2026-10-12T00:00:00Z','2026-10-12T00:00:00Z'),
		('cutover-consumer','research','cutover living consumer','in_progress',0,1,'2026-10-12T00:00:00Z','2026-10-12T00:00:00Z')`)
	exec(`INSERT INTO work_projects(work_id,project_id,role) VALUES('cutover-owner','project','primary'),('cutover-consumer','project','primary')`)
	exec(`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at)
		VALUES('pack-cutover-living','cutover-owner',1,'current',1,'2026-10-12T00:00:00Z','2026-10-12T00:00:00Z')`)
	exec(`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at)
		VALUES('pack-cutover-living',1,'does the cutover preserve living evidence?','[]','[]','[]','synthetic cutover witness','2026-10-12T00:00:00Z')`)
	exec(`INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at)
		VALUES('pack-cutover-living',1,'cutover-consumer','decision_basis',1,'2026-10-12T00:00:00Z')`)
	exec(`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at)
		VALUES('set-cutover','project','op-cutover-claim','simplify/cutover-living','1f2e3d4c5b6a79887766554433221100ffeeddccb','/srv/cutover-living-worktree','cutover-repo','active','2026-10-12T00:01:00Z')`)
	exec(fmt.Sprintf(`INSERT INTO worktree_occupancy(worktree_id,session_ref,recorded_at,host_pid,host_pid_start,has_process_identity)
		VALUES('set-cutover:project:op-cutover-claim','ses-cutover-living','2026-10-12T00:01:00Z',%d,NULL,1)`, sessionPID))
	exec(`INSERT INTO worker_attempts(work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,failure_kind,failure_detail,dispatched_at,completed_at,failed_at)
		VALUES('cutover-consumer','attempt-cutover-living-1','implement',1,'sha256:` + strings.Repeat("a", 64) + `','implementation','synthetic/cutover-worker','1.0','1.0','completed','','','2026-10-12T00:02:00Z','2026-10-12T00:10:00Z',NULL)`)
	exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES
		('cutover-work-created','work.created','work_item','cutover-consumer','principal:cutover-operator','2026-10-12T00:00:00Z',2,
			'{"work_kind":"research","title":"cutover living consumer","priority":1}'),
		('cutover-consumer:session-claim-landed:ses-cutover-living:1','work.session_claim_landed','work_item','cutover-consumer','ses-cutover-living','2026-10-12T00:01:00Z',1,
			'{"work_id":"cutover-consumer","project_id":"project","claim_op_id":"op-cutover-claim","session_ref":"ses-cutover-living","source_directories":[],"landed_directory":"/srv/cutover-living-worktree","host_pid":` + fmt.Sprint(sessionPID) + `}'),
		('cutover-attempt-completed','worker.completed','work_item','cutover-consumer','worker:cutover-living','2026-10-12T00:10:00Z',1,
			'{"attempt_id":"attempt-cutover-living-1","readback_model":"synthetic/cutover-worker","report_schema_version":"1.0"}')`)
	exec(`DELETE FROM fold_guard`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
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
	want := captureCutoverEvidence(t, mustOpenCutoverStore(t, path))
	if len(want["domain_events"]) != 3 {
		t.Fatalf("seeded event log = %d rows, want the three living-evidence events", len(want["domain_events"]))
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
	assertPreserved := func(t *testing.T) {
		t.Helper()
		got := captureCutoverEvidence(t, mustOpenCutoverStore(t, path))
		for _, evidence := range cutoverEvidenceQueries {
			if strings.Join(got[evidence.name], "\x1e") != strings.Join(want[evidence.name], "\x1e") {
				t.Fatalf("%s evidence =\n%s\nwant\n%s", evidence.name, strings.Join(got[evidence.name], "\n"), strings.Join(want[evidence.name], "\n"))
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
		assertPreserved(t)
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
	if report.SchemaVersion != candidateSchema || len(report.Applied) != candidateSchema-cutoverBaselineSchema {
		t.Fatalf("the confirmed upgrade must apply the complete pending tail: %+v", report)
	}
	for i, applied := range report.Applied {
		if applied != cutoverBaselineSchema+i+1 {
			t.Fatalf("the confirmed upgrade must apply every pending step in order: %+v", report)
		}
	}
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
	if verified.SchemaVersion != cutoverBaselineSchema || verified.EventWatermark != int64(len(want["domain_events"])) {
		t.Fatalf("the verified snapshot = %+v, want the baseline schema and the seeded event watermark %d", verified, len(want["domain_events"]))
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
	assertPreserved(t)

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
	assertPreserved(t)
}

// The research-context pair runs on one synthetic Product, Project, and
// pack-owner work item; the fixture directory is a label the pair's fixed
// project resolver answers with, never a path either binary probes.
const pairFixtureDirectory = "/srv/research-pair-fixture"

const pairProductID = "pair-product"
const pairProjectID = "pair-project"
const pairOwnerID = "pair-research-owner"
const pairPackID = "pair-evidence-pack"
const pairPrincipalRef = "principal/pair-operator"
const pairClientRef = "client/pair-author"
const pairAgentRef = "agent/pair-author"
const pairRootDomainID = "pair-root"
const pairChildDomainID = "pair-child"
const pairSelectedFindingID = "pair-finding-selected"

// researchPairEvidenceQueries fingerprints the living evidence the
// research-context pair must carry across the promoted workflow heads: the
// original event bytes, the work rows, the complete sourced-research
// authority with its citations, the consuming selection pin, the promoted
// workflow pin, and the Domain registry the selection named. Every query
// names only columns that exist identically at the baseline and candidate
// schemas.
var researchPairEvidenceQueries = []struct {
	name  string
	query string
}{
	{"domain_events", `SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`},
	{"work_items", `SELECT id,kind,title,lifecycle,priority,version FROM work_items ORDER BY id`},
	{"research_packs", `SELECT pack_id,owner_work_id,current_revision,freshness,expected_version FROM active_research_packs ORDER BY pack_id`},
	{"research_revisions", `SELECT pack_id,revision,question,method,freshness FROM active_research_revisions ORDER BY pack_id,revision`},
	{"research_findings", `SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode FROM active_research_findings ORDER BY pack_id,revision,finding_id`},
	{"research_sources", `SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,accessed_at FROM active_research_sources ORDER BY pack_id,revision,source_id`},
	{"research_citations", `SELECT pack_id,revision,finding_id,source_id FROM active_research_finding_sources ORDER BY pack_id,revision,finding_id,source_id`},
	{"research_consumers", `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers ORDER BY pack_id,revision,consumer_work_id`},
	{"workflow_instances", `SELECT work_id,definition_ref,definition_version,definition_digest,current_step,instance_state FROM workflow_instances ORDER BY work_id`},
	{"domain_registries", `SELECT product_id,root_domain_id,content_hash FROM domain_registries ORDER BY product_id`},
	{"domains", `SELECT product_id,domain_id,parent_domain_id,status,registry_content_hash FROM domains ORDER BY product_id,domain_id`},
}

// captureResearchPairEvidence reads every paired population as opaque row
// strings, fixed by each query's ORDER BY, so equal stores produce equal
// fingerprints.
func captureResearchPairEvidence(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	return captureEvidenceRows(t, db, researchPairEvidenceQueries)
}

// assertResearchPairEvidenceEqual fails when one pair stage changed a
// fingerprinted population.
func assertResearchPairEvidenceEqual(t *testing.T, stage string, got, want map[string][]string) {
	t.Helper()
	for _, evidence := range researchPairEvidenceQueries {
		if strings.Join(got[evidence.name], "\x1e") != strings.Join(want[evidence.name], "\x1e") {
			t.Fatalf("%s changed %s evidence:\ngot\n%s\nwant\n%s", stage, evidence.name, strings.Join(got[evidence.name], "\n"), strings.Join(want[evidence.name], "\n"))
		}
	}
}

// seedResearchPairIdentity authors the Product, its Project, the membership,
// and the pack-owner work item through the ordinary event append both
// releases' folds share at this schema.
func seedResearchPairIdentity(t *testing.T, s *store.Store) {
	t.Helper()
	observedAt := time.Unix(1, 0).UTC()
	events := []store.Event{
		{EventID: "pair-product-created", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: pairProductID, Actor: "operator", OccurredAt: observedAt, PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Pair product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "pair-project-created", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: pairProjectID, Actor: "operator", OccurredAt: observedAt, PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Pair project"}`)},
		{EventID: "pair-product-project", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: pairProductID, Actor: "operator", OccurredAt: observedAt, PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"` + pairProductID + `","project_id":"` + pairProjectID + `","role":"primary","reason":"pair fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "pair-owner-created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: pairOwnerID, Actor: "operator", OccurredAt: observedAt, PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"research","title":"Pair research owner","priority":1}`)},
		{EventID: "pair-owner-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: pairOwnerID, Actor: "operator", OccurredAt: observedAt, PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"` + pairProjectID + `","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{
		store.VersionRef(store.SubjectProduct, pairProductID): 0,
		store.VersionRef(store.SubjectProject, pairProjectID): 0,
		store.VersionRef(store.SubjectWorkItem, pairOwnerID):  0,
	}}); err != nil {
		t.Fatal(err)
	}
}

// seedResearchPairDomainRegistry installs the Domain registry fixture rows
// the selection's declared Domain resolves against, under the same fold
// guard the projection's own writers hold.
func seedResearchPairDomainRegistry(t *testing.T, s *store.Store) {
	t.Helper()
	registryHash := "sha256:" + strings.Repeat("7", 64)
	tx, err := s.DatabaseForTesting().Begin()
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	exec(`INSERT OR IGNORE INTO fold_guard(active) VALUES (1)`)
	exec(`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,'1.0',?,'pair-fixture')`,
		pairProductID, pairProjectID, "pair-locator", pairProductID, pairRootDomainID, registryHash)
	exec(`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,NULL,'current',?,'pair-fixture')`,
		pairProjectID, "pair-locator", pairProductID, pairRootDomainID, "Pair root", "The pair fixture's root Domain", registryHash)
	exec(`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,'current',?,'pair-fixture')`,
		pairProjectID, "pair-locator", pairProductID, pairChildDomainID, "Pair child", "The pair fixture's child Domain", pairRootDomainID, registryHash)
	exec(`DELETE FROM fold_guard`)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedResearchPairPack authors the sourced-research fixture pack through the
// owning research mutations: revision one carries the selected finding with
// its linked source beside an unselected sibling, and the newer revision two
// carries its own finding and source as the exact-selection negative
// controls.
func seedResearchPairPack(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	identity := func(key string) store.ResearchMutationIdentity {
		return store.ResearchMutationIdentity{PrincipalRef: pairPrincipalRef, Tool: "release-pair-seed", OperationKind: "research_mutation", IdempotencyKey: key}
	}
	packVersion := func() int64 {
		t.Helper()
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pairPackID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	revision := func(question string) store.ResearchRevisionInput {
		return store.ResearchRevisionInput{Question: question, ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "release pair comparison"}
	}
	if _, err := s.CreateResearchPack(ctx, store.CreateResearchPackRequest{Identity: identity("pair-pack-create"), PackID: pairPackID, OwnerWorkID: pairOwnerID, Revision: revision("Does the pair preserve the selected sourced research?")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchSource(ctx, store.ResearchSourceRequest{Identity: identity("pair-source-selected"), PackID: pairPackID, ExpectedVersion: packVersion(), Source: store.ResearchSource{
		SourceID: "pair-source-selected", Kind: store.SourceCode, Locator: "internal/store/work_context_research.go", Title: "The selected revision's source descriptor", PublisherOrAuthor: "Synthetic Author", AccessedAt: "2026-10-14T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchFinding(ctx, store.ResearchFindingRequest{Identity: identity("pair-finding-selected"), PackID: pairPackID, ExpectedVersion: packVersion(), Finding: store.ResearchFinding{
		PackID: pairPackID, FindingID: pairSelectedFindingID, Kind: store.FindingInference, Statement: "The selected revision's finding resolves beside its linked source.",
		Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive, Scopes: store.ResearchScopes{Mode: "home"}, SourceIDs: []string{"pair-source-selected"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchFinding(ctx, store.ResearchFindingRequest{Identity: identity("pair-finding-sibling"), PackID: pairPackID, ExpectedVersion: packVersion(), Finding: store.ResearchFinding{
		PackID: pairPackID, FindingID: "pair-finding-sibling", Kind: store.FindingObservation, Statement: "The unselected sibling finding never enters the shared view.",
		Confidence: store.ConfidenceMedium, Freshness: store.ResearchCurrent, Status: store.FindingActive, Scopes: store.ResearchScopes{Mode: "home"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendResearchRevision(ctx, store.AppendResearchRevisionRequest{Identity: identity("pair-revision-2"), PackID: pairPackID, ExpectedVersion: packVersion(), Revision: revision("Does the newer revision own the route?")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchSource(ctx, store.ResearchSourceRequest{Identity: identity("pair-source-newer"), PackID: pairPackID, ExpectedVersion: packVersion(), Source: store.ResearchSource{
		SourceID: "pair-source-newer", Kind: store.SourceIssue, Locator: "https://example.invalid/pair/newer", Title: "The newer revision's source descriptor", PublisherOrAuthor: "Synthetic Author", AccessedAt: "2026-10-15T00:00:00Z",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchFinding(ctx, store.ResearchFindingRequest{Identity: identity("pair-finding-newer"), PackID: pairPackID, ExpectedVersion: packVersion(), Finding: store.ResearchFinding{
		PackID: pairPackID, FindingID: "pair-finding-newer", Kind: store.FindingConclusion, Statement: "The newer revision's finding stays out of the exact selection.",
		Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive, Scopes: store.ResearchScopes{Mode: "home"}, SourceIDs: []string{"pair-source-newer"},
	}}); err != nil {
		t.Fatal(err)
	}
}

// authorResearchPairSelection captures the consuming work item through the
// public capture admission, which pins the registered research-context head,
// then authors the selection through the admitted record_work_context
// action: the same-action research binding first, then the closed selection
// tuple. No constructor mirror and no unsupported pinned definition authors
// the event.
func authorResearchPairSelection(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	scopeVersion, _, err := s.ScopeVersion(ctx, pairProjectID)
	if err != nil {
		t.Fatal(err)
	}
	service := agent.NewService(s)
	service.ProjectHostProber = func(context.Context, string, string) (store.ResolvedProjectHost, error) {
		return store.ResolvedProjectHost{Canonical: pairFixtureDirectory, Host: store.HostRepository{CanonicalPath: pairFixtureDirectory}}, nil
	}
	service.ProjectHostMatcher = func(context.Context, *store.Transaction, store.ResolvedProjectHost) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: pairProjectID}, nil
	}
	publicKey, _, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RegisterTrustedClient(ctx, agent.ClientRegistration{
		ClientRef: pairClientRef, KeyID: "pair-author-key", PublicKey: publicKey,
		Policy: agent.TrustedClientPolicy{
			PrincipalRef: pairPrincipalRef,
			Capabilities: []agent.Capability{"product_read", "work_define", "work_transition", "research"},
			ProductScope: []string{pairProductID}, ProjectScope: []string{pairProjectID}, AgentScope: []string{pairAgentRef},
		},
	}); err != nil {
		t.Fatal(err)
	}
	envelope := agent.CallEnvelope{
		SchemaVersion: "1.0", RequestID: "pair-author", ClientRef: pairClientRef, PrincipalRef: pairPrincipalRef,
		SessionRef: "session/pair-author", AgentRef: pairAgentRef, Directory: pairFixtureDirectory, Worktree: pairFixtureDirectory,
		AmbientProjectID: pairProjectID, SelectedProductID: pairProductID, ScopeVersion: scopeVersion, ManifestDigest: agent.ManifestDigest,
	}
	dispatch := func(tool, operation string, input map[string]any) agent.Envelope {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		response, err := agent.Dispatch(ctx, s, service, agent.InvokeRequest{Tool: tool, Operation: operation, Input: raw}, envelope)
		if err != nil {
			t.Fatal(err)
		}
		if response.Outcome != agent.OutcomeOK || response.Error != nil {
			t.Fatalf("%s %s refused: %+v", tool, operation, response.Error)
		}
		return response
	}
	changed := func(response agent.Envelope, entityKind string) (string, int64) {
		t.Helper()
		if response.ChangedRefs == nil {
			t.Fatalf("%s %s changed no entity", response.Tool, response.Operation)
		}
		for _, ref := range *response.ChangedRefs {
			if ref.EntityKind != entityKind {
				continue
			}
			version, err := strconv.ParseInt(ref.Version, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return ref.ID, version
		}
		t.Fatalf("%s %s changed no %s ref: %+v", response.Tool, response.Operation, entityKind, *response.ChangedRefs)
		return "", 0
	}
	captured := dispatch("concord_work_define", "capture", map[string]any{
		"title": "Pair research consumer", "value_statement": "Consume the selected sourced research finding.",
		"kind": "bug", "task": "Author the pair's research selection.", "project_ids": []string{pairProjectID}, "workflow_type_ref": "workflow.break_fix",
		"defect_intake": map[string]any{
			"failure_shape": "pair-consumer", "reproduction": "Author a research selection on the promoted workflow head.", "searched": "the synthetic pair store", "related_defect_ids": []string{},
		},
		"idempotency_key": "pair-consumer-capture",
	})
	workID, version := changed(captured, "work_item")
	dispatch("concord_work_transition", "workflow_action", map[string]any{
		"work_id": workID, "expected_version": version, "action_id": "record_work_context",
		"fields": map[string]any{
			"finding_refs": []string{}, "context_findings": []map[string]any{},
			"research_finding_refs": []map[string]any{{"pack_id": pairPackID, "revision": 1, "finding_id": pairSelectedFindingID, "domain_id": pairChildDomainID}},
		},
		"research_bindings": []map[string]any{{"pack_id": pairPackID, "revision": 1, "use_role": "context", "required": true}},
		"idempotency_key":   "pair-research-context",
	})
	return workID
}

// readResearchPairView reads the work's continuity snapshot and returns the
// current shared view's research entries beside the serialized view bytes.
func readResearchPairView(t *testing.T, s *store.Store, workID string) ([]store.WorkContextResearchFindingView, []byte) {
	t.Helper()
	snapshot, err := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WorkContext == nil {
		t.Fatal("the continuity snapshot carries no work context view")
	}
	raw, err := json.Marshal(snapshot.WorkContext)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.WorkContext.ResearchFindings, raw
}

// assertResearchPairSelection asserts the integrated receipt: exactly the
// selected revision-one finding resolves with its native fields and full
// linked source descriptor, while the unselected sibling and the newer
// revision stay out of the view entirely.
func assertResearchPairSelection(t *testing.T, entries []store.WorkContextResearchFindingView, raw []byte) {
	t.Helper()
	if len(entries) != 1 {
		t.Fatalf("the shared view carries %d research entries, want exactly the selection", len(entries))
	}
	entry := entries[0]
	if entry.Selection.PackID != pairPackID || entry.Selection.Revision != 1 || entry.Selection.FindingID != pairSelectedFindingID || entry.Selection.DomainID != pairChildDomainID {
		t.Fatalf("the shared view names the wrong selection: %+v", entry.Selection)
	}
	if !entry.Required || entry.State != store.WorkContextResearchStateResolved || entry.Ordinal != 0 || entry.SelectionEventID == "" || entry.SelectionEventSeq == 0 {
		t.Fatalf("the selected entry must be the required resolved ordinal zero of its declaration event: %+v", entry)
	}
	finding := entry.Finding
	if finding == nil {
		t.Fatal("the resolved entry carries no finding")
	}
	if finding.Revision != 1 || finding.Kind != store.FindingInference || finding.Confidence != store.ConfidenceHigh ||
		finding.Freshness != store.ResearchCurrent || finding.Status != store.FindingActive ||
		finding.Statement != "The selected revision's finding resolves beside its linked source." ||
		strings.Join(finding.SourceIDs, ",") != "pair-source-selected" || finding.Scopes.Mode != "home" {
		t.Fatalf("the resolved entry lost the native finding's exact fields: %+v", finding)
	}
	if entry.Sources == nil || len(*entry.Sources) != 1 {
		t.Fatalf("the resolved entry must carry exactly its linked source descriptor: %+v", entry.Sources)
	}
	source := (*entry.Sources)[0]
	if source.SourceID != "pair-source-selected" || source.Revision != 1 || source.Kind != store.SourceCode ||
		source.Locator != "internal/store/work_context_research.go" || source.Title != "The selected revision's source descriptor" ||
		source.PublisherOrAuthor != "Synthetic Author" || source.AccessedAt != "2026-10-14T00:00:00Z" {
		t.Fatalf("the resolved entry lost the linked source descriptor's exact fields: %+v", source)
	}
	for _, withheld := range []string{"pair-finding-sibling", "The unselected sibling finding", "pair-finding-newer", "The newer revision's finding", "pair-source-newer"} {
		if strings.Contains(string(raw), withheld) {
			t.Fatalf("the shared view leaked the %q control: %s", withheld, raw)
		}
	}
}

// TestResearchContextPostCutoverPairPreservesEvidence exercises the
// research-context pair on one disposable store: the newest release below
// the promoted workflow heads initializes the store and operates its
// sourced-research fixtures, the candidate upgrades it with no new migration
// and authors a selection on the promoted head through the admitted capture
// and record_work_context route, and the baseline refuses that unsupported
// head with zero mutation. The shared view is the integrated receipt: the
// exact revision-one finding with its native fields and full linked source
// descriptor, never the sibling or newer-revision controls. Backup and
// restore preservation stays with the backup witness.
func TestResearchContextPostCutoverPairPreservesEvidence(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the released baseline and checked-out candidate cores")
	}
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data", "concord")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataRoot, "concord.db")
	baselineVersion := "v0.0.0-con911-pair-baseline"
	candidateVersion := "v0.0.0-con911-pair-candidate"
	baselineSource := extractReleasedSource(t, researchPairBaselineTag)
	t.Logf("binary sources: baseline=%s candidate=checked-out source", researchPairBaselineTag)
	baseline := buildCoreFrom(t, baselineSource, baselineVersion)
	candidate := buildCoreFrom(t, repoSourceDir(t), candidateVersion)
	baselineBinary := filepath.Join(installReleaseTree(t, dataRoot, baselineVersion, baseline, true), "bin", "concord")
	candidateBinary := filepath.Join(installReleaseTree(t, dataRoot, candidateVersion, candidate, true), "bin", "concord")

	// Both cores define the same schema: the promotion moved workflow
	// registry heads, not migrations, so no floor refusal can shadow the
	// unsupported-head refusal below.
	candidateSchema := store.CurrentSchemaVersion()
	baselineDescriptor := coreDescriptor(t, baselineBinary)
	if baselineDescriptor["version"] != baselineVersion || baselineDescriptor["schema_version"] != float64(candidateSchema) {
		t.Fatalf("the baseline core must define the candidate's exact schema: %+v", baselineDescriptor)
	}
	candidateDescriptor := coreDescriptor(t, candidateBinary)
	if candidateDescriptor["version"] != candidateVersion || candidateDescriptor["schema_version"] != float64(candidateSchema) ||
		candidateDescriptor["manifest_digest"] == baselineDescriptor["manifest_digest"] {
		t.Fatalf("the candidate core must identify its own distinct build and contract: %+v", candidateDescriptor)
	}

	// The baseline core initializes the disposable store; the pair carries
	// no breaking migration, so no maintenance boundary opens.
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
	if initialized.SchemaVersion != candidateSchema {
		t.Fatalf("the baseline store's schema = %d, want %d", initialized.SchemaVersion, candidateSchema)
	}

	// The seeded fixture state is the population every later stage must
	// preserve: authored through the owning mutations, never through a
	// mirror of their rows.
	seeded, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	seedResearchPairIdentity(t, seeded)
	// The Domain registry's home pair references a Project locator row, so
	// the locator enters through its own owning mutation first. The owning
	// admission resolves a canonical path on disk, so the fixture directory
	// is real; nothing later probes it.
	pairRepo := filepath.Join(root, "pair-repo")
	if err := os.MkdirAll(pairRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := seeded.AddProjectLocator(context.Background(), pairProjectID, store.ProjectLocator{
		ID: "pair-locator", ProjectID: pairProjectID, Kind: store.LocatorCanonicalPath, Value: pairRepo,
	}, 1); err != nil {
		t.Fatal(err)
	}
	seedResearchPairDomainRegistry(t, seeded)
	seedResearchPairPack(t, seeded)
	if err := seeded.Close(); err != nil {
		t.Fatal(err)
	}
	want := captureResearchPairEvidence(t, mustOpenCutoverStore(t, path))
	// An append carries the prior revision's findings, sources, scopes, and
	// citations into the successor, and a restated brief degrades the copied
	// freshness to unknown. The exact-selection control therefore reads: the
	// selected finding resolves from its assessed revision one, never from
	// the degraded successor copy beside it.
	if len(want["research_findings"]) != 5 || len(want["research_sources"]) != 3 || len(want["research_citations"]) != 3 {
		t.Fatalf("seeded sourced research = findings %d sources %d citations %d, want five findings, three sources, and three citations", len(want["research_findings"]), len(want["research_sources"]), len(want["research_citations"]))
	}
	copiedDB := mustOpenCutoverStore(t, path)
	var copiedFreshness string
	if err := copiedDB.QueryRow(`SELECT freshness FROM active_research_findings WHERE pack_id=? AND revision=2 AND finding_id=?`, pairPackID, pairSelectedFindingID).Scan(&copiedFreshness); err != nil {
		t.Fatal(err)
	}
	if copiedFreshness != "unknown" {
		t.Fatalf("the restated successor's copied finding freshness = %q, want unknown", copiedFreshness)
	}

	// The baseline still operates the seeded store it owns.
	code, out, errText = runRelease(t, baselineBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the baseline core must operate the seeded store: %d %s %s", code, out, errText)
	}
	var noop struct {
		SchemaVersion int   `json:"schema_version"`
		Applied       []int `json:"applied"`
	}
	if err := json.Unmarshal([]byte(out), &noop); err != nil || noop.SchemaVersion != candidateSchema || len(noop.Applied) != 0 {
		t.Fatalf("the baseline's seeded-store no-op must apply nothing: %+v %v %s", noop, err, out)
	}
	assertResearchPairEvidenceEqual(t, "the baseline's seeded-store no-op", captureResearchPairEvidence(t, mustOpenCutoverStore(t, path)), want)

	// The candidate opens and upgrades the seeded store without a new
	// migration and changes no fingerprinted population.
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the candidate must operate the baseline store: %d %s %s", code, out, errText)
	}
	if err := json.Unmarshal([]byte(out), &noop); err != nil || noop.SchemaVersion != candidateSchema || len(noop.Applied) != 0 {
		t.Fatalf("the candidate's upgrade must apply nothing: %+v %v %s", noop, err, out)
	}
	assertResearchPairEvidenceEqual(t, "the candidate's no-op upgrade", captureResearchPairEvidence(t, mustOpenCutoverStore(t, path)), want)

	// The candidate authors the selection on the promoted head through the
	// owning admission, and the instance pin names exactly the registered
	// head the capture selected.
	authored, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	workID := authorResearchPairSelection(t, authored)
	registered, err := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	var pinnedRef string
	var pinnedVersion int64
	var pinnedDigest string
	if err := authored.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pinnedRef, &pinnedVersion, &pinnedDigest); err != nil {
		t.Fatal(err)
	}
	if pinnedRef != registered.Definition.Ref || pinnedVersion != registered.Definition.Version || pinnedDigest != registered.Digest {
		t.Fatalf("the capture pinned %s version %d digest %s, want the registered head %s version %d digest %s", pinnedRef, pinnedVersion, pinnedDigest, registered.Definition.Ref, registered.Definition.Version, registered.Digest)
	}
	entries, viewRaw := readResearchPairView(t, authored, workID)
	assertResearchPairSelection(t, entries, viewRaw)
	if err := authored.Close(); err != nil {
		t.Fatal(err)
	}
	authoredFingerprint := captureResearchPairEvidence(t, mustOpenCutoverStore(t, path))
	assertAuthored := func(t *testing.T) {
		t.Helper()
		assertResearchPairEvidenceEqual(t, "the later binary stage", captureResearchPairEvidence(t, mustOpenCutoverStore(t, path)), authoredFingerprint)
	}

	// The candidate reopens the store, and the selection survives unchanged.
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the candidate must reopen its store: %d %s %s", code, out, errText)
	}
	if err := json.Unmarshal([]byte(out), &noop); err != nil || noop.SchemaVersion != candidateSchema || len(noop.Applied) != 0 {
		t.Fatalf("the candidate's reopen must apply nothing: %+v %v %s", noop, err, out)
	}
	assertAuthored(t)
	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	reopenedEntries, reopenedRaw := readResearchPairView(t, reopened, workID)
	assertResearchPairSelection(t, reopenedEntries, reopenedRaw)
	if string(reopenedRaw) != string(viewRaw) {
		t.Fatalf("the reopened view drifted:\nbefore:\n%s\nafter:\n%s", viewRaw, reopenedRaw)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	// The baseline refuses the unsupported workflow head before any effect:
	// no migration, no rewritten row, and no requirement that the old
	// binary understand the newly promoted head or its digest.
	migrationSnapshot := releaseMigrationSnapshot(t, path)
	code, out, errText = runRelease(t, baselineBinary, path, "upgrade", `{}`)
	if code != 1 || out != "" || !strings.Contains(errText, "schema_unsupported") ||
		!strings.Contains(errText, "binary compatibility refused") ||
		!strings.Contains(errText, fmt.Sprintf("workflow definition %s version %d is not registered by this binary", registered.Definition.Ref, registered.Definition.Version)) {
		t.Fatalf("the baseline core must refuse the unsupported head without effect: %d %s %s", code, out, errText)
	}
	if after := releaseMigrationSnapshot(t, path); after != migrationSnapshot {
		t.Fatal("the baseline's unsupported-head refusal rewrote the store")
	}
	assertAuthored(t)

	// The refusal left the store operable by the release that owns the head.
	code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the candidate must operate the store after the refusal: %d %s %s", code, out, errText)
	}
	if err := json.Unmarshal([]byte(out), &noop); err != nil || noop.SchemaVersion != candidateSchema || len(noop.Applied) != 0 {
		t.Fatalf("the candidate's post-refusal no-op must apply nothing: %+v %v %s", noop, err, out)
	}
	assertAuthored(t)
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
