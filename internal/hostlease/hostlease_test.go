package hostlease

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLeaseRoundTripKeepsALiveProcessAndPrunesAStaleOne(t *testing.T) {
	root := t.TempDir()
	start, err := ProcessStart(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessStart(self) error = %v", err)
	}
	releaseRoot, coreBinary := releaseTree(t, "v1.2.3")
	lease := Lease{
		PID:            os.Getpid(),
		ReleaseRoot:    releaseRoot,
		CoreBinary:     coreBinary,
		PidStart:       start,
		SchemaVersion:  73,
		ManifestDigest: "sha256:" + strings.Repeat("a", 64),
		RecordedAt:     "2026-09-06T00:00:00Z",
	}
	if err := Write(root, lease); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	live, err := List(root)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(live) != 1 || live[0] != lease {
		t.Fatalf("List() = %+v, want the written lease", live)
	}

	// A recycled pid: the same pid with the start time it no longer has is
	// stale and must be pruned, and the replacement lease is live.
	recycled := lease
	recycled.PidStart = start + 1
	if err := Write(root, recycled); err == nil || !strings.Contains(err.Error(), "not live") {
		t.Fatalf("Write() for a recycled pid = %v, want a stale-process refusal", err)
	}
	if err := os.WriteFile(filepath.Join(Directory(root), "424242.json"), mustJSON(t, Lease{PID: 424242, PidStart: 1, ReleaseRoot: "/releases/v0.0.9", SchemaVersion: 1}), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err = List(root)
	if err != nil {
		t.Fatalf("List() with a stale entry error = %v", err)
	}
	if len(live) != 1 {
		t.Fatalf("List() = %d leases, want 1 after pruning", len(live))
	}
	if _, err := os.Stat(filepath.Join(Directory(root), "424242.json")); !os.IsNotExist(err) {
		t.Fatalf("stale lease was not pruned: %v", err)
	}

	// A malformed lease is an observation failure, never a silent prune.
	if err := os.WriteFile(filepath.Join(Directory(root), "99.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := List(root); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("List() with a malformed lease = %v, want a refusal", err)
	}
}

// A lease file written before session locations existed still reads, and a
// lease that carries them round-trips both fields, so a breaking-migration
// refusal can name the terminal behind the pid.
func TestLeaseCarriesSessionLocationAndReadsOlderFiles(t *testing.T) {
	root := t.TempDir()
	start, err := ProcessStart(os.Getpid())
	if err != nil {
		t.Fatalf("ProcessStart(self) error = %v", err)
	}
	releaseRoot, coreBinary := releaseTree(t, "v2.0.0")
	located := Lease{
		PID:            os.Getpid(),
		PidStart:       start,
		ReleaseRoot:    releaseRoot,
		CoreBinary:     coreBinary,
		SchemaVersion:  73,
		ManifestDigest: "sha256:" + strings.Repeat("b", 64),
		RecordedAt:     "2026-09-18T00:00:00Z",
		Directory:      "/workspace/card-site",
		Worktree:       "/workspace/concord/worktrees/card/work-1",
	}
	if err := Write(root, located); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	live, err := List(root)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(live) != 1 || live[0] != located {
		t.Fatalf("List() = %+v, want the located lease", live)
	}
	if err := os.Remove(filepath.Join(Directory(root), strconv.Itoa(os.Getpid())+".json")); err != nil {
		t.Fatal(err)
	}
	legacy := Lease{PID: os.Getpid(), PidStart: start, ReleaseRoot: "/releases/v1.2.3", SchemaVersion: 72}
	if err := os.WriteFile(filepath.Join(Directory(root), strconv.Itoa(os.Getpid())+".json"), mustJSON(t, legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	live, err = List(root)
	if err != nil {
		t.Fatalf("List() over a legacy lease error = %v", err)
	}
	if len(live) != 1 || live[0].Directory != "" || live[0].Worktree != "" {
		t.Fatalf("List() over a legacy lease = %+v, want empty location fields", live)
	}
}

func TestListReadsAnAbsentDirectoryAsEmpty(t *testing.T) {
	live, err := List(t.TempDir())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("List() = %+v, want none", live)
	}
}

// releaseTree creates a synthetic installed release whose core binary
// exists, because admission refuses a lease that pins a removed core.
func releaseTree(t *testing.T, version string) (root, binary string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), version)
	binary = filepath.Join(root, "bin", "concord")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("core"), 0o700); err != nil { //nolint:gosec // a synthetic executable stand-in.
		t.Fatal(err)
	}
	return root, binary
}

func selfLease(t *testing.T, schemaVersion int) Lease {
	t.Helper()
	start, _ := ProcessStart(os.Getpid())
	releaseRoot, coreBinary := releaseTree(t, "v9.0.0")
	return Lease{
		PID:            os.Getpid(),
		PidStart:       start,
		ReleaseRoot:    releaseRoot,
		CoreBinary:     coreBinary,
		SchemaVersion:  schemaVersion,
		ManifestDigest: "sha256:" + strings.Repeat("c", 64),
		RecordedAt:     "2026-10-01T00:00:00Z",
	}
}

// The maintenance fence is the shared session-admission exclusion of the
// CON-807 boundary: while it is open, no new lease may be written, an
// already-open fence keeps one identity across both commands that hold it,
// and closing it reopens admission (CON-807).
func TestFenceExcludesNewAdmissionAndIsAdoptedNotRewritten(t *testing.T) {
	root := t.TempDir()
	fence, err := EnsureFence(root, Fence{
		ReleaseRoot:   "/releases/v9.1.0",
		CoreBinary:    "/releases/v9.1.0/bin/concord",
		SchemaVersion: 116,
		Notice:        "session admission reopens when v9.1.0 activates",
	})
	if err != nil {
		t.Fatalf("EnsureFence() error = %v", err)
	}
	if fence.FenceID == "" || fence.CreatedAt == "" {
		t.Fatalf("EnsureFence() returned an incomplete fence: %+v", fence)
	}
	err = Write(root, selfLease(t, 115))
	if err == nil || !errors.Is(err, ErrMaintenanceExcluded) {
		t.Fatalf("Write() under an open fence = %v, want a maintenance exclusion", err)
	}
	if !strings.Contains(err.Error(), "session admission reopens when v9.1.0 activates") {
		t.Fatalf("the exclusion lost the fence notice: %v", err)
	}
	adopted, err := EnsureFence(root, Fence{CoreBinary: "/releases/v9.2.0/bin/concord", Notice: "different"})
	if err != nil {
		t.Fatalf("EnsureFence() over an open fence error = %v", err)
	}
	if adopted.FenceID != fence.FenceID || adopted.Notice != fence.Notice {
		t.Fatalf("an open fence was rewritten: %+v want %+v", adopted, fence)
	}
	if err := RemoveFenceOwned(root, fence.FenceID); err != nil {
		t.Fatalf("RemoveFenceOwned() error = %v", err)
	}
	if err := Write(root, selfLease(t, 115)); err != nil {
		t.Fatalf("Write() after the fence closed = %v, want admission", err)
	}
	live, err := List(root)
	if err != nil || len(live) != 1 {
		t.Fatalf("List() after the fence closed = %v, %v, want the admitted lease", live, err)
	}
}

// An unreadable fence fails closed on both sides: admission refuses rather
// than treating a possibly-open boundary as absent, and so does the reader
// the migration and activation consult (CON-807 fail-closed rule).
func TestFenceFailsClosedOnAMalformedRecord(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(FencePath(root), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(root, selfLease(t, 115)); err == nil || !strings.Contains(err.Error(), "malformed maintenance fence") {
		t.Fatalf("Write() over a malformed fence = %v, want a refusal that names the malformed fence", err)
	}
	if _, err := ReadFence(root); err == nil {
		t.Fatal("ReadFence() over a malformed fence must refuse")
	}
}

// Compatible releases coexist at the lease plane: a session holding the
// compatibility floor's release and a session on the current release are
// both live at once, and neither excludes the other (CON-807 rolling-first).
func TestLeasesCoexistAcrossSchemaVersions(t *testing.T) {
	root := t.TempDir()
	if err := Write(root, selfLease(t, 111)); err != nil {
		t.Fatalf("Write() for the floor lease = %v", err)
	}
	other := selfLease(t, 115)
	other.PID = os.Getppid()
	otherStart, err := ProcessStart(other.PID)
	if err != nil {
		t.Skip("the parent process is not observable in this environment")
	}
	other.PidStart = otherStart
	if err := Write(root, other); err != nil {
		t.Fatalf("Write() for the current lease = %v", err)
	}
	live, err := List(root)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	versions := map[int]bool{}
	for _, lease := range live {
		versions[lease.SchemaVersion] = true
	}
	if !versions[111] || !versions[115] {
		t.Fatalf("both schema generations must stay live together: %+v", live)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// Release cleanup deletes trees while holding the admission lock. A claim
// that queued behind it must not land on the deleted release: the session
// would hold a pinned core that no longer exists (CON-807 retention).
func TestWriteRefusesALeaseWhosePinnedCoreWasRemoved(t *testing.T) {
	root := t.TempDir()
	lease := selfLease(t, 115)
	if err := os.RemoveAll(lease.ReleaseRoot); err != nil {
		t.Fatal(err)
	}
	err := Write(root, lease)
	if err == nil || !errors.Is(err, ErrReleaseRemoved) {
		t.Fatalf("Write() for a removed release = %v, want ErrReleaseRemoved", err)
	}
	live, err := List(root)
	if err != nil || len(live) != 0 {
		t.Fatalf("List() after a refused admission = %v, %v, want no lease", live, err)
	}
}

// One maintenance command holds the data root at a time, and the second
// refuses at once instead of migrating under the first's boundary (CON-807).
func TestMaintenanceIsExclusiveAndDoesNotWait(t *testing.T) {
	root := t.TempDir()
	release, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := AcquireMaintenance(root); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("a second maintenance command must refuse while the first runs: %v", err)
	}
	release()
	again, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	again()
}
