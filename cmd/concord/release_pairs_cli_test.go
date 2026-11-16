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

// The legacy pair is two immutable released sources below the supported store
// floor; they coexist across additive migrations, and the candidate never
// migrates their store. The bridge release is the oldest supported released
// source: it raises an older store to the floor and serves as the supported
// baseline.
const legacyPairTag = "v11.61.0"
const legacyPairSchema = 114
const compatiblePairTag = "v11.63.30"
const compatiblePairSchema = 116
const bridgeReleaseTag = "v67.0.0"
const bridgeReleaseSchema = 199

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
// release root). An unmarked tree demonstrates the legacy case beside coexistence.
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
// descriptor: the identity evidence the installer's staging probe uses.
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

// buildBridgeRelease builds the released bridge core into a release tree under
// dataRoot. Its descriptor binds the tag to the supported floor it defines.
func buildBridgeRelease(t *testing.T, dataRoot string, fenceable bool) (source, root, binary string) {
	t.Helper()
	source = extractReleasedSource(t, bridgeReleaseTag)
	root = installReleaseTree(t, dataRoot, bridgeReleaseTag, buildCoreFrom(t, source, bridgeReleaseTag), fenceable)
	binary = filepath.Join(root, "bin", "concord")
	descriptor := coreDescriptor(t, binary)
	if descriptor["version"] != bridgeReleaseTag || descriptor["schema_version"] != float64(bridgeReleaseSchema) ||
		descriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) || descriptor["core_binary"] != binary {
		t.Fatalf("the bridge core must report its release, the supported floor and its fence protocol: %+v", descriptor)
	}
	return source, root, binary
}

// assertFloorRefusal requires a released core that predates the store's floor
// to refuse it before any effect, naming the floor and its own schema.
func assertFloorRefusal(t *testing.T, binary, path string, floor, defines int) {
	t.Helper()
	before := releaseMigrationSnapshot(t, path)
	code, out, errText := runRelease(t, binary, path, "upgrade", `{}`)
	if code != 1 || out != "" || !strings.Contains(errText, "schema_unsupported") ||
		!strings.Contains(errText, fmt.Sprintf("defines schema version %d", floor)) ||
		!strings.Contains(errText, fmt.Sprintf("this binary defines %d", defines)) {
		t.Fatalf("the released core %s must refuse floor %d before effect: %d %s %s", binary, floor, code, out, errText)
	}
	if after := releaseMigrationSnapshot(t, path); after != before {
		t.Fatalf("the floor refusal of %s changed the store", binary)
	}
}

// TestDistinctReleasedCoresCoexistOnOneStore uses each released source's real
// adapter and core on one temporary store. Schema 114 and 116 coexist across
// additive migrations below the supported floor. The candidate refuses that
// store before any effect, with sessions live or stopped, and names the bridge
// release. After the bridge release raises the store to the floor, the
// candidate migrates forward under the fence, and every older core refuses the
// new floor. This is supported-pair evidence, not blanket rolling
// compatibility or projection/binary-skew repair.
func TestDistinctReleasedCoresCoexistOnOneStore(t *testing.T) {
	if testing.Short() {
		t.Skip("builds four release cores from four distinct sources")
	}
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data", "concord")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataRoot, "concord.db")
	releasedSource := extractReleasedSource(t, legacyPairTag)
	compatibleSource := extractReleasedSource(t, compatiblePairTag)
	older := buildCoreFrom(t, releasedSource, legacyPairTag)
	newer := buildCoreFrom(t, compatibleSource, compatiblePairTag)
	oldRoot := installReleaseTree(t, dataRoot, legacyPairTag, older, false)
	newRoot := installReleaseTree(t, dataRoot, compatiblePairTag, newer, true)
	oldBinary := filepath.Join(oldRoot, "bin", "concord")
	newBinary := filepath.Join(newRoot, "bin", "concord")
	// Each release's own adapter runtime, stamped to its own tree exactly
	// the way the installer stamps it: the sessions below call their own
	// pinned cores.
	oldAdapter := stampAdapterRuntime(t, filepath.Join(releasedSource, "adapter", "opencode"), oldRoot)
	newAdapter := stampAdapterRuntime(t, filepath.Join(compatibleSource, "adapter", "opencode"), newRoot)

	// Distinct-source identity evidence: the released core answers its own
	// tag and predates the descriptor route (a known legacy core for the
	// installer's probe), while the compatible core reports a complete
	// descriptor pinned to its own tree.
	code, out, _ := runRelease(t, oldBinary, path, "--version", "")
	if code != 0 || strings.TrimSpace(out) != legacyPairTag {
		t.Fatalf("the released core must answer its tag: %d %q", code, out)
	}
	if coreAnswersRoute(oldBinary, "--version", "--json") {
		t.Fatal("the released core predates the descriptor route; it must not answer --version --json")
	}
	newerDescriptor := coreDescriptor(t, newBinary)
	if newerDescriptor["version"] != compatiblePairTag || newerDescriptor["schema_version"] != float64(compatiblePairSchema) || newerDescriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) {
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
	if oldUpgrade.SchemaVersion != legacyPairSchema {
		t.Fatalf("the released core's schema head = %d, want the released %d", oldUpgrade.SchemaVersion, legacyPairSchema)
	}
	if oldUpgrade.SchemaVersion >= newSchemaVersion {
		t.Fatalf("the pair is not distinct: the released head %d is not behind the additive release's %d", oldUpgrade.SchemaVersion, newSchemaVersion)
	}

	// The old session is a real released-adapter operation: the released
	// adapter claims its lease by running its own pinned core.
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
	if newUpgrade.SchemaVersion != newSchemaVersion || len(newUpgrade.Applied) != newSchemaVersion-legacyPairSchema {
		t.Fatalf("the newer core must apply its additive steps: %+v", newUpgrade)
	}
	for i, applied := range newUpgrade.Applied {
		if applied != legacyPairSchema+i+1 {
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
	// the fence protocol used by the operator-owned offline bootstrap.
	unfenceable, err := unfenceableReleaseRoots(dataRoot, newRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(unfenceable) != 1 || unfenceable[0] != oldRoot {
		t.Fatalf("the released tree must be the boundary's unfenceable legacy entry: %v", unfenceable)
	}

	candidateVersion := "v0.0.0-floor-candidate"
	candidate := buildCoreFrom(t, repoSourceDir(t), candidateVersion)
	candidateRoot := installReleaseTree(t, dataRoot, candidateVersion, candidate, true)
	candidateBinary := filepath.Join(candidateRoot, "bin", "concord")
	descriptor := coreDescriptor(t, candidateBinary)
	if descriptor["schema_version"] != float64(store.CurrentSchemaVersion()) || descriptor["core_binary"] != candidateBinary || descriptor["version"] != candidateVersion || descriptor["manifest_digest"] == newerDescriptor["manifest_digest"] {
		t.Fatalf("the candidate must identify its own distinct core and contract: %+v", descriptor)
	}
	// Below the supported floor the candidate refuses before the maintenance
	// boundary, a snapshot or any migration, and names the bridge release.
	assertBelowFloorRefusal := func(t *testing.T) {
		t.Helper()
		before := releaseMigrationSnapshot(t, path)
		snapshots, err := filepath.Glob(filepath.Join(dataRoot, "backups", "*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range []string{`{"plan":true}`, `{}`, `{"confirm_sessions_stopped":true}`} {
			code, out, errText := runRelease(t, candidateBinary, path, "upgrade", input)
			if code != 1 || out != "" || !strings.Contains(errText, "schema_unsupported") ||
				!strings.Contains(errText, fmt.Sprintf("minimum supported schema version %d", bridgeReleaseSchema)) ||
				!strings.Contains(errText, "bridge release "+bridgeReleaseTag) {
				t.Fatalf("the candidate must refuse the schema %d store and name the bridge release (%s): %d %s %s", compatiblePairSchema, input, code, out, errText)
			}
		}
		if after := releaseMigrationSnapshot(t, path); after != before {
			t.Fatalf("the below-floor refusal changed the store:\nbefore:\n%safter:\n%s", before, after)
		}
		if after, err := filepath.Glob(filepath.Join(dataRoot, "backups", "*")); err != nil || len(after) != len(snapshots) {
			t.Fatalf("the below-floor refusal must take no snapshot: %v %v", after, err)
		}
		if fence, err := hostlease.ReadFence(dataRoot); err != nil || fence != nil {
			t.Fatalf("the below-floor refusal must open no boundary: %+v %v", fence, err)
		}
	}
	t.Run("below_floor_candidate_refuses_live_sessions_before_effect", func(t *testing.T) {
		assertBelowFloorRefusal(t)
		// Both released sessions keep operating the store the refusal left alone.
		oldSession.reclaimAndHold(t)
		newSession.reclaimAndHold(t)
	})
	oldSession.stop()
	newSession.stop()
	t.Run("below_floor_candidate_refuses_stopped_store_before_effect", func(t *testing.T) {
		assertBelowFloorRefusal(t)
	})

	// The bridge release raises the stopped store to the supported floor on
	// the operator's confirmation for the unmarked legacy tree.
	_, bridgeRoot, bridgeBinary := buildBridgeRelease(t, dataRoot, true)
	code, out, errText = runRelease(t, bridgeBinary, path, "upgrade", `{"confirm_sessions_stopped":true}`)
	if code != 0 || !strings.Contains(errText, "operator's confirmation") || !strings.Contains(errText, oldRoot) {
		t.Fatalf("the bridge release must raise the stopped store to the floor: %d %s %s", code, out, errText)
	}
	var bridged struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(out), &bridged); err != nil || bridged.SchemaVersion != bridgeReleaseSchema {
		t.Fatalf("the bridged store schema = %+v %v, want %d: %s", bridged, err, bridgeReleaseSchema, out)
	}
	bridgeFence, err := hostlease.ReadFence(dataRoot)
	if err != nil || bridgeFence == nil || bridgeFence.ReleaseRoot != bridgeRoot || bridgeFence.CoreBinary != bridgeBinary {
		t.Fatalf("the bridge release's committed breaking boundary must remain open: %+v %v", bridgeFence, err)
	}
	// The bridge release's activation closes its own boundary; the test stands
	// in for that installer step.
	if err := hostlease.RemoveFenceOwned(dataRoot, bridgeFence.FenceID); err != nil {
		t.Fatal(err)
	}
	assertFloorRefusal(t, oldBinary, path, bridgeReleaseSchema, legacyPairSchema)
	assertFloorRefusal(t, newBinary, path, bridgeReleaseSchema, compatiblePairSchema)

	breaking := releasedPendingBreaking(t, bridgeReleaseSchema)
	floor := breaking[len(breaking)-1]
	t.Run("bridged_store_upgrades_after_sessions_stop", func(t *testing.T) {
		readiness, err := store.PlanUpgradeReadiness(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		assertReleasePendingBreaking(t, readiness, breaking)
		if readiness.SchemaVersion != bridgeReleaseSchema || readiness.CompatibilityFloor != bridgeReleaseSchema || !readiness.ActivationBlocked {
			t.Fatalf("the bridged store must await the retained breaking tail: %+v", readiness)
		}
		code, out, errText := runRelease(t, candidateBinary, path, "upgrade", `{}`)
		if code != 1 || out != "" || !strings.Contains(errText, "without a fence-protocol marker") || !strings.Contains(errText, oldRoot) || !strings.Contains(errText, "confirm_sessions_stopped") {
			t.Fatalf("the stopped unmarked tree still requires confirmation: %d %s %s", code, out, errText)
		}
		if fence, err := hostlease.ReadFence(dataRoot); err != nil || fence != nil {
			t.Fatalf("a pre-commit refusal must close its own fence: %+v %v", fence, err)
		}
		code, out, errText = runRelease(t, candidateBinary, path, "upgrade", `{"confirm_sessions_stopped":true}`)
		if code != 0 {
			t.Fatalf("the candidate must migrate the bridged store forward after sessions stop: %d %s %s", code, out, errText)
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
		assertAppliedMigrationTail(t, upgrade.Applied, bridgeReleaseSchema)
		readiness, err = store.PlanUpgradeReadiness(context.Background(), path)
		if err != nil || readiness.CompatibilityFloor != floor || len(readiness.PendingBreaking) != 0 || readiness.ActivationBlocked {
			t.Fatalf("the committed store must require floor %d: %+v %v", floor, readiness, err)
		}
		fence, err := hostlease.ReadFence(dataRoot)
		if err != nil || fence == nil || fence.ReleaseRoot != candidateRoot || fence.CoreBinary != candidateBinary {
			t.Fatalf("the candidate's committed breaking boundary must remain open: %+v %v", fence, err)
		}
		assertFloorRefusal(t, oldBinary, path, floor, legacyPairSchema)
		assertFloorRefusal(t, newBinary, path, floor, compatiblePairSchema)
		assertFloorRefusal(t, bridgeBinary, path, floor, bridgeReleaseSchema)
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

// TestDistinctReleasedCoreBreakingUpgradeRequiresStoppedSessions exercises the
// retained breaking tail on a store the bridge release created at the
// supported floor, not a relabeled binary. A live session that a released
// legacy core leased blocks every migration effect, even with confirmation:
// that core cannot open the supported store, and the fence cannot exclude it.
// Only after the operator ends the session and confirms its unfenceable tree
// is idle may the current core raise the floor.
func TestDistinctReleasedCoreBreakingUpgradeRequiresStoppedSessions(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data", "concord")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataRoot, "concord.db")
	_, _, bridgeBinary := buildBridgeRelease(t, dataRoot, true)
	legacy := buildCoreFrom(t, extractReleasedSource(t, legacyPairTag), legacyPairTag)
	const currentRelease = "v0.0.0-floor-breaking"
	current := buildCoreFrom(t, repoSourceDir(t), currentRelease)
	legacyRoot := installReleaseTree(t, dataRoot, legacyPairTag, legacy, false)
	currentRoot := installReleaseTree(t, dataRoot, currentRelease, current, true)
	legacyBinary := filepath.Join(legacyRoot, "bin", "concord")
	currentBinary := filepath.Join(currentRoot, "bin", "concord")
	currentDescriptor := coreDescriptor(t, currentBinary)
	if currentDescriptor["version"] != currentRelease || currentDescriptor["core_binary"] != currentBinary ||
		currentDescriptor["schema_version"] != float64(store.CurrentSchemaVersion()) ||
		currentDescriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) {
		t.Fatalf("the current core must identify its own build and capabilities: %+v", currentDescriptor)
	}
	code, out, errText := runRelease(t, bridgeBinary, path, "upgrade", `{}`)
	if code != 0 {
		t.Fatalf("the bridge release must initialize the store: %d %s %s", code, out, errText)
	}
	var initialized struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(out), &initialized); err != nil || initialized.SchemaVersion != bridgeReleaseSchema {
		t.Fatalf("the bridge release's store schema = %+v %v, want %d: %s", initialized, err, bridgeReleaseSchema, out)
	}
	assertFloorRefusal(t, legacyBinary, path, bridgeReleaseSchema, legacyPairSchema)

	// A session pinned to the legacy release outlives the store it can no
	// longer open: the released legacy core leases a live process, and the
	// lease carries no fence protocol.
	holder := exec.Command("sleep", "600")
	if err := holder.Start(); err != nil {
		t.Fatalf("cannot start the legacy session process: %v", err)
	}
	holderDone := make(chan struct{})
	go func() {
		_ = holder.Wait()
		close(holderDone)
	}()
	stopHolder := func() {
		_ = holder.Process.Kill()
		<-holderDone
	}
	t.Cleanup(stopHolder)
	const legacyDirectory = "/srv/stranded-legacy-session"
	code, out, errText = runRelease(t, legacyBinary, path, "host-lease", mustJSON(t, map[string]any{"pid": holder.Process.Pid, "directory": legacyDirectory}))
	if code != 0 {
		t.Fatalf("the released legacy core must lease its session: %d %s %s", code, out, errText)
	}
	leases, err := hostlease.List(dataRoot)
	if err != nil || len(leases) != 1 || leases[0].PID != holder.Process.Pid || leases[0].ReleaseRoot != legacyRoot ||
		leases[0].SchemaVersion != legacyPairSchema || leases[0].FenceProtocol != 0 {
		t.Fatalf("the legacy session must hold one unfenceable legacy lease: %+v %v", leases, err)
	}

	before, err := store.PlanUpgradeReadiness(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	breaking := releasedPendingBreaking(t, bridgeReleaseSchema)
	assertReleasePendingBreaking(t, before, breaking)
	if before.SchemaVersion != bridgeReleaseSchema || before.CompatibilityFloor != bridgeReleaseSchema {
		t.Fatalf("the bridged store must await the retained breaking manifest: %+v", before)
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
			!strings.Contains(errText, "fence protocol 0") || !strings.Contains(errText, legacyRoot) ||
			!strings.Contains(errText, fmt.Sprintf("pid %d", holder.Process.Pid)) ||
			!strings.Contains(errText, legacyDirectory) {
			t.Fatalf("the breaking upgrade must name the live legacy session before effect: %d %s %s", code, out, errText)
		}
		assertUnadvanced(t)
	}
	stopHolder()
	if leases, err := hostlease.List(dataRoot); err != nil || len(leases) != 0 {
		t.Fatalf("the operator must end the legacy session before migration: %+v %v", leases, err)
	}
	code, out, errText = runRelease(t, currentBinary, path, "upgrade", `{}`)
	if code != 1 || out != "" || !strings.Contains(errText, "without a fence-protocol marker") ||
		!strings.Contains(errText, legacyRoot) || !strings.Contains(errText, "confirm_sessions_stopped") {
		t.Fatalf("the idle legacy tree still requires the operator's confirmation: %d %s %s", code, out, errText)
	}
	assertUnadvanced(t)

	code, out, errText = runRelease(t, currentBinary, path, "upgrade", `{"confirm_sessions_stopped":true}`)
	if code != 0 || !strings.Contains(errText, "operator's confirmation") || !strings.Contains(errText, legacyRoot) {
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
	assertAppliedMigrationTail(t, report.Applied, bridgeReleaseSchema)
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
	assertFloorRefusal(t, bridgeBinary, path, floor, bridgeReleaseSchema)
	assertFloorRefusal(t, legacyBinary, path, floor, legacyPairSchema)
}

type cutoverEvidenceQuery struct {
	name  string
	query string
}

// cutoverEvidenceQueries fingerprints the living evidence a cutover must carry
// across the breaking floor. Every query names only columns that exist
// identically at the baseline and candidate schemas.
var cutoverEvidenceQueries = []cutoverEvidenceQuery{
	{"products", `SELECT id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at FROM products ORDER BY id`},
	{"projects", `SELECT id,display_name,version,repository_mode FROM projects ORDER BY id`},
	{"product_projects", `SELECT product_id,project_id,role FROM product_projects ORDER BY product_id,project_id`},
	{"project_locators", `SELECT locator_id,project_id,kind,locator_value,normalized_value FROM project_locators ORDER BY locator_id`},
	{"product_knowledge_homes", `SELECT product_id,project_id,locator_id FROM product_knowledge_homes ORDER BY product_id`},
	{"durable_knowledge", `SELECT id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version FROM archived_work ORDER BY home_project_id,home_locator_id,id`},
	{"durable_knowledge_products", `SELECT home_project_id,home_locator_id,work_id,product_id FROM archived_work_products ORDER BY home_project_id,home_locator_id,work_id,product_id`},
	{"durable_knowledge_projects", `SELECT home_project_id,home_locator_id,work_id,project_id FROM archived_work_projects ORDER BY home_project_id,home_locator_id,work_id,project_id`},
	{"effect_receipts", `SELECT effect_seq,event_id,kind,subject_type,subject_id,actor,payload_version,payload_digest FROM effect_receipts ORDER BY effect_seq`},
}

// retiredCutoverQueries fingerprints baseline state the retained breaking
// tail retires; only the pre-migration snapshot keeps it.
var retiredCutoverQueries = []cutoverEvidenceQuery{
	{"runtime_state_writers", `SELECT surface,definition_ref,knowledge_projection_version,digest,binary_version FROM runtime_state_writers ORDER BY surface,definition_ref,knowledge_projection_version,digest`},
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

// captureCutoverEvidence reads every evidence population as opaque row
// strings, fixed by each query's ORDER BY, so equal stores produce equal
// fingerprints.
func captureCutoverEvidence(t *testing.T, db *sql.DB, queries []cutoverEvidenceQuery) map[string][]string {
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

// seedCutoverLivingEvidence writes the baseline's Product, Project, locator and
// knowledge home through the baseline core's own verbs. The durable lesson
// index and the runtime writer row have no CLI writer, so they enter the store
// directly, the lesson under the store's write guard.
func seedCutoverLivingEvidence(t *testing.T, baselineBinary, path, knowledgeRoot string) {
	t.Helper()
	for _, step := range []struct {
		verb  string
		input map[string]any
	}{
		{"product-create", map[string]any{"product_id": "cutover-product", "display_name": "Cutover product", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only", "project_id": "project", "project_display_name": "Cutover project", "role": "primary"}},
		{"project-locator-add", map[string]any{"project_id": "project", "locator_id": "cutover-knowledge", "kind": "canonical_path", "value": knowledgeRoot, "expected_version": 1}},
		{"project-repository-mode-update", map[string]any{"project_id": "project", "repository_mode": "on", "expected_version": 2}},
		{"product-knowledge-home-designate", map[string]any{"product_id": "cutover-product", "project_id": "project", "locator_id": "cutover-knowledge", "expected_version": 2}},
	} {
		if code, out, errText := runRelease(t, baselineBinary, path, step.verb, mustJSON(t, step.input)); code != 0 {
			t.Fatalf("baseline %s: %d %s %s", step.verb, code, out, errText)
		}
	}
	db := mustOpenCutoverStore(t, path)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
INSERT INTO write_guard(active) VALUES(1);
INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode,manifest_schema_version)
VALUES('lesson-cutover','lesson','Preserve required research pins','2026-10-12T00:00:00Z','published','["cutover"]','completed',0,'A new research revision must not move an existing required consumer pin.','project','cutover-knowledge','.concord/docs/lessons/cutover.md','1f2e3d4c5b6a79887766554433221100ffeeddccb','sha256:` + strings.Repeat("a", 64) + `','explicit','1.0');
INSERT INTO archived_work_products(home_project_id,home_locator_id,work_id,product_id)
VALUES('project','cutover-knowledge','lesson-cutover','cutover-product');
INSERT INTO archived_work_projects(home_project_id,home_locator_id,work_id,project_id)
VALUES('project','cutover-knowledge','lesson-cutover','project');
DELETE FROM write_guard;
INSERT INTO runtime_state_writers(surface,definition_ref,knowledge_projection_version,digest,binary_version)
VALUES('workflow','change',0,'sha256:` + strings.Repeat("b", 64) + `','v0.0.0-cutover-baseline');`); err != nil {
		t.Fatalf("seed baseline durable Product knowledge and runtime writer state: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// cutoverTablesAbsent reports whether none of the named tables remains
// in the migrated schema.
func cutoverTablesAbsent(t *testing.T, path string, tables ...string) bool {
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

// TestCutoverBaselineCoreBreakingUpgradePreservesLivingEvidence exercises the
// maintenance fence and verified snapshot with the released bridge baseline
// and the checked-out candidate core. An active baseline session blocks
// migration even with operator confirmation. After it stops, the confirmed
// upgrade preserves living evidence, retires the tail's retired state, keeps
// that state in the verified snapshot, and excludes the baseline core at the
// new floor.
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
	candidateVersion := "v0.0.0-cutover-candidate"
	// The baseline tree is staged without a fence-protocol marker: that
	// unstamped installation puts the stopped-tree confirmation in the
	// operator's hands, while the session itself remains a fenceable
	// participant its lease proves below.
	baselineSource, baselineRoot, baselineBinary := buildBridgeRelease(t, dataRoot, false)
	t.Logf("binary sources: baseline=%s candidate=checked-out source", bridgeReleaseTag)
	candidate := buildCoreFrom(t, repoSourceDir(t), candidateVersion)
	candidateRoot := installReleaseTree(t, dataRoot, candidateVersion, candidate, true)
	candidateBinary := filepath.Join(candidateRoot, "bin", "concord")
	baselineDescriptor := coreDescriptor(t, baselineBinary)
	candidateSchema := store.CurrentSchemaVersion()
	candidateDescriptor := coreDescriptor(t, candidateBinary)
	if candidateDescriptor["version"] != candidateVersion || candidateDescriptor["schema_version"] != float64(candidateSchema) ||
		candidateDescriptor["fence_protocol"] != float64(hostlease.CurrentFenceProtocol) || candidateDescriptor["core_binary"] != candidateBinary ||
		candidateDescriptor["manifest_digest"] == baselineDescriptor["manifest_digest"] {
		t.Fatalf("the candidate core must identify its own distinct build and contract: %+v", candidateDescriptor)
	}

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
	if initialized.SchemaVersion != bridgeReleaseSchema {
		t.Fatalf("the baseline store's schema = %d, want %d", initialized.SchemaVersion, bridgeReleaseSchema)
	}

	// The shipped manifest, not a hardcoded list, owns the pending breaking
	// tail and the floor it will raise.
	breaking := releasedPendingBreaking(t, bridgeReleaseSchema)
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
	if plan.SchemaVersion != bridgeReleaseSchema || !plan.ActivationBlocked || !plan.SnapshotRequired {
		t.Fatalf("the baseline store must await the shipped breaking tail behind a verified snapshot: %+v", plan.UpgradeReadiness)
	}

	// A real baseline adapter session claims its lease through its own pinned
	// core, then the store carries the living evidence it works on.
	baselineAdapter := stampAdapterRuntime(t, filepath.Join(baselineSource, "adapter", "opencode"), baselineRoot)
	session := startAdapterSessionOperation(t, baselineAdapter, path, "/srv/cutover-baseline-session")
	sessionPID := session.command.Process.Pid
	seedCutoverLivingEvidence(t, baselineBinary, path, t.TempDir())
	want := captureCutoverEvidence(t, mustOpenCutoverStore(t, path), append(append([]cutoverEvidenceQuery{}, cutoverEvidenceQueries...), retiredCutoverQueries...))
	for _, name := range []string{"products", "projects", "product_projects", "project_locators", "product_knowledge_homes", "durable_knowledge", "durable_knowledge_products", "durable_knowledge_projects", "runtime_state_writers"} {
		if len(want[name]) != 1 {
			t.Fatalf("baseline %s = %v, want one seeded row", name, want[name])
		}
	}
	if len(want["effect_receipts"]) == 0 {
		t.Fatal("the baseline core's own writes must leave effect receipts")
	}
	postCutoverWant := make(map[string][]string, len(cutoverEvidenceQueries))
	for _, evidence := range cutoverEvidenceQueries {
		postCutoverWant[evidence.name] = want[evidence.name]
	}
	assertPreserved := func(t *testing.T, got, expectedEvidence map[string][]string) {
		t.Helper()
		for name, expected := range expectedEvidence {
			if strings.Join(got[name], "\x1e") != strings.Join(expected, "\x1e") {
				t.Fatalf("%s evidence =\n%s\nwant\n%s", name, strings.Join(got[name], "\n"), strings.Join(expected, "\n"))
			}
		}
	}
	var baselineRevision int64
	if err := mustOpenCutoverStore(t, path).QueryRow(`SELECT revision FROM store_revision WHERE id=1`).Scan(&baselineRevision); err != nil {
		t.Fatal(err)
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
		assertPreserved(t, captureCutoverEvidence(t, mustOpenCutoverStore(t, path), append(append([]cutoverEvidenceQuery{}, cutoverEvidenceQueries...), retiredCutoverQueries...)), want)
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
		!strings.Contains(errText, fmt.Sprintf("schema version %d", bridgeReleaseSchema)) ||
		!strings.Contains(errText, fmt.Sprintf("before migration %d", breaking[0])) ||
		!strings.Contains(errText, "/srv/cutover-baseline-session") {
		t.Fatalf("confirmation cannot bypass the observable live baseline session: %d %s %s", code, out, errText)
	}
	assertUnadvanced(t)

	// The live session's own release reports its lease: one fenceable
	// participant pinned to its own tree at the supported floor.
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
	if lease.ReleaseRoot != baselineRoot || lease.CoreBinary != baselineBinary || lease.SchemaVersion != bridgeReleaseSchema ||
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
	assertAppliedMigrationTail(t, report.Applied, bridgeReleaseSchema)
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
	// The pre-migration snapshot verifies at the baseline schema and revision
	// and keeps the state the tail retires.
	if _, err := os.Stat(report.Backup); err != nil {
		t.Fatalf("the required pre-migration snapshot must exist: %v", err)
	}
	verified, err := store.VerifyBackup(context.Background(), report.Backup, bridgeReleaseSchema)
	if err != nil {
		t.Fatalf("the pre-migration snapshot must verify at the baseline schema: %v", err)
	}
	if verified.SchemaVersion != bridgeReleaseSchema || verified.EventWatermark != baselineRevision {
		t.Fatalf("the verified snapshot = %+v, want the baseline schema and store revision %d", verified, baselineRevision)
	}
	assertPreserved(t, captureCutoverEvidence(t, mustOpenCutoverStore(t, report.Backup), append(append([]cutoverEvidenceQuery{}, cutoverEvidenceQueries...), retiredCutoverQueries...)), want)

	// The migrated store requires the manifest-derived floor, keeps the
	// activation boundary open, and no longer holds the retired state.
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
	if !cutoverTablesAbsent(t, path, "workflow_contract_law_modifications", "runtime_state_writers") {
		t.Fatal("the state the retained breaking tail retires must be absent after the migration")
	}
	assertPreserved(t, captureCutoverEvidence(t, mustOpenCutoverStore(t, path), cutoverEvidenceQueries), postCutoverWant)

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
	// The baseline core refuses the advanced floor without effect on the
	// migrated store or its living evidence.
	assertFloorRefusal(t, baselineBinary, path, floor, bridgeReleaseSchema)
	assertPreserved(t, captureCutoverEvidence(t, mustOpenCutoverStore(t, path), cutoverEvidenceQueries), postCutoverWant)

	// The current store opens, closes and reopens with its living evidence
	// intact after the baseline core's refusal.
	ctx := context.Background()
	for range 2 {
		current, err := store.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		afterContinuation := captureCutoverEvidence(t, current.DatabaseForTesting(), cutoverEvidenceQueries)
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
		for name, expected := range postCutoverWant {
			if got := afterContinuation[name]; !reflect.DeepEqual(got, expected) {
				t.Fatalf("continued work changed retained %s: got %v, want %v", name, got, expected)
			}
		}
	}
}

// stampAdapterRuntime copies one source tree's adapter runtime into a
// private directory and stamps the release constants exactly the way
// scripts/install.py stamps them at install time: absolute paths into the
// release the adapter is pinned to. A session that runs from the copy calls
// exactly that release's core binary, never through PATH.
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
// the plugin factory's lease claim through the release's own
// adapter code and its own stamped core, then store-backed reads through
// the same adapter dispatch runner and the same pinned core — the transport
// every adapter operation uses — holding the session and its lease open
// until released. The store-backed leg answers the evidence bar:
// lease metadata alone never opens the shared store, while this read does.
// startAdapterSessionOperation prepends the import of the release's own core
// transport.
const adapterSessionDriver = `import { createInterface } from "node:readline"
import { claimHostLease, hostLeaseFault } from "./host-lease"

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
for await (const input of createInterface({ input: process.stdin })) {
  if (input === "") break
  if (input === "reclaim") {
    await claim("PAIR-ADAPTER-RECLAIM")
  }
  if (input === "reclaim" || input === "read") {
    await storeBackedRead("PAIR-ADAPTER-READ-AGAIN")
  }
}
await new Promise<void>(() => {})
`

// adapterTransportModule names the module that exports the core transport in
// the release's own adapter: older releases ship dispatch.ts, later releases
// core-runtime.ts.
func adapterTransportModule(t *testing.T, adapterDir string) string {
	t.Helper()
	for _, module := range []string{"core-runtime", "dispatch"} {
		if _, err := os.Stat(filepath.Join(adapterDir, module+".ts")); err == nil {
			return "./" + module
		}
	}
	t.Fatalf("the adapter at %s ships no core transport module", adapterDir)
	return ""
}

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
// adapter/session operation: the adapter code that release
// actually ships claims the host lease by calling its own pinned core —
// no CLI substitution, no synthetic pid. The bun process is the session.
func startAdapterSessionOperation(t *testing.T, adapterDir, dbPath, directory string) *adapterSession {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skipf("bun is unavailable for the adapter session operation: %v", err)
	}
	driver := filepath.Join(adapterDir, "pair-session-driver.ts")
	transport := adapterTransportModule(t, adapterDir)
	program := fmt.Sprintf("import { concordBinaryPath, defaultRunner } from %q\n", transport) + adapterSessionDriver
	if err := os.WriteFile(driver, []byte(program), 0o644); err != nil {
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
// tree and the confirmation, and closes the boundary it opened.
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
// for the activation.
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
