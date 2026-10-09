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

// extractReleasedSource checks out a release tag or pinned commit into a
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
		fetchRef := tag
		if tag == releasedPairTag {
			fetchRef = "+refs/tags/" + tag + ":refs/tags/" + tag
		}
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
	if descriptor["schema_version"] != float64(store.CurrentSchemaVersion()) || descriptor["core_binary"] != candidateBinary || descriptor["version"] != candidateVersion || descriptor["manifest_digest"] == newerDescriptor["manifest_digest"] {
		t.Fatalf("the candidate must identify its own distinct core and contract: %+v", descriptor)
	}
	assertPendingFloor := func(t *testing.T) {
		t.Helper()
		readiness, err := store.PlanUpgradeReadiness(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if readiness.SchemaVersion != compatiblePairSchema || readiness.CompatibilityFloor != 111 || !readiness.ActivationBlocked || len(readiness.PendingBreaking) != 3 || readiness.PendingBreaking[0].Version != 119 || readiness.PendingBreaking[1].Version != 120 || readiness.PendingBreaking[2].Version != 121 {
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
		if upgrade.SchemaVersion != store.CurrentSchemaVersion() || len(upgrade.Applied) != 5 || upgrade.Applied[0] != 117 || upgrade.Applied[1] != 118 || upgrade.Applied[2] != 119 || upgrade.Applied[3] != 120 || upgrade.Applied[4] != 121 {
			t.Fatalf("the candidate must commit the actual breaking tail: %+v", upgrade)
		}
		readiness, err := store.PlanUpgradeReadiness(context.Background(), path)
		if err != nil || readiness.CompatibilityFloor != 121 || len(readiness.PendingBreaking) != 0 || readiness.ActivationBlocked {
			t.Fatalf("the committed store must require floor 121: %+v %v", readiness, err)
		}
		fence, err := hostlease.ReadFence(dataRoot)
		if err != nil || fence == nil || fence.ReleaseRoot != candidateRoot || fence.CoreBinary != candidateBinary {
			t.Fatalf("the candidate's committed breaking boundary must remain open: %+v %v", fence, err)
		}
		for _, binary := range []string{oldBinary, newBinary} {
			code, _, errText := runRelease(t, binary, path, "upgrade", `{}`)
			if code != 1 || !strings.Contains(errText, "schema_unsupported") || !strings.Contains(errText, "schema version 121") {
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
	if before.SchemaVersion != releasedPairSchema || before.CompatibilityFloor >= releasedPairSchema ||
		len(before.PendingBreaking) != 3 || before.PendingBreaking[0].Version != 119 || !before.PendingBreaking[0].Breaking ||
		before.PendingBreaking[1].Version != 120 || !before.PendingBreaking[1].Breaking ||
		before.PendingBreaking[2].Version != 121 || !before.PendingBreaking[2].Breaking {
		t.Fatalf("the released store must await the breaking worker-attempt migration first: %+v", before)
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
	after, err := store.PlanUpgradeReadiness(context.Background(), path)
	// The floor is the highest breaking version applied: the outside-repair
	// disposition step (121) raises it past the initiative violation
	// projection step (120) and the v119 rebuild.
	if err != nil || after.SchemaVersion != report.SchemaVersion || after.CompatibilityFloor != 121 || len(after.PendingBreaking) != 0 {
		t.Fatalf("the breaking upgrade must raise the floor to 121: %+v %v", after, err)
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
		!strings.Contains(errText, "defines schema version 121") ||
		!strings.Contains(errText, fmt.Sprintf("this binary defines %d", releasedPairSchema)) {
		t.Fatalf("the old core must refuse the breaking floor, not the maintenance fence: %d %s %s", code, out, errText)
	}
	if final := releaseMigrationSnapshot(t, path); final != upgradedSnapshot {
		t.Fatal("the old core's floor refusal changed the upgraded store")
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ts") || strings.HasSuffix(entry.Name(), ".test.ts") {
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
