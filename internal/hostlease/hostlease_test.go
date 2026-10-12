package hostlease

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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
// maintenance boundary: while it is open, no new lease may be written, an
// already-open fence keeps one identity across both commands that hold it,
// and closing it reopens admission.
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
// the migration and activation consult.
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
// both live at once, and neither excludes the other.
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
// would hold a pinned core that no longer exists.
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
// refuses at once instead of migrating under the first's boundary.
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

// A first-install bootstrap locks the same way an established root does:
// the acquisition creates an absent root before opening it, so the lock is
// held from the first command on and a second command refuses.
// The release retains the bootstrapped root and its ancestors: retention
// (obs:1ca149d633e69626) is the approved outcome, because no cleanup can
// prove a replacement holder has not taken the directory over.
func TestMaintenanceLocksAnAbsentRootBeforeAnyStateWork(t *testing.T) {
	root := filepath.Join(t.TempDir(), "concord")
	release, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatalf("acquire over an absent root: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("the acquisition must create the root it locks: %v (%v)", info, err)
	}
	if _, err := AcquireMaintenance(root); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("a second command must refuse against the bootstrapped root: %v", err)
	}
	release()
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		t.Fatalf("the release must retain the empty bootstrapped root: %v (%v)", info, err)
	}
}

// A symlinked data root is refused by the open itself, never followed: the
// lock would otherwise serialize on a directory the operator did not name,
// and the migration would run against whatever the link points at.
func TestMaintenanceRefusesASymlinkedDataRoot(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "concord")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireMaintenance(link)
	if err == nil || errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("a symlinked data root must be refused, not locked: %v", err)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("the refusal must name the symlink: %v", err)
	}
	// The refusal must not have locked the link's target either.
	targetRelease, err := AcquireMaintenance(target)
	if err != nil {
		t.Fatalf("the refused acquisition locked the symlink's target: %v", err)
	}
	targetRelease()
}

// The lock identity is the directory the path names at admission. A root
// removed and recreated between the open and the flock leaves the lock on
// an orphaned inode; the acquisition must release the stale descriptor,
// re-open the current path, and admit a lock the next command refuses
// against — deterministically, without sleeping.
func TestMaintenanceReacquiresAfterTheRootIsReplacedBetweenOpenAndFlock(t *testing.T) {
	root := t.TempDir()
	calls := 0
	open := openMaintenanceDirectory
	flock := func(fd int, how int) error {
		calls++
		if calls == 1 {
			// The deterministic race: between the production open and
			// this flock, the root the descriptor names is removed and a
			// different directory is created at the same path.
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return syscall.Flock(fd, how)
	}
	file, err := acquireMaintenanceLock(root, open, flock)
	if err != nil {
		t.Fatalf("the acquisition must re-acquire on the recreated root: %v", err)
	}
	defer func() { _ = file.Close() }()
	// The admitted lock must sit on the directory the path now names: a
	// second command opening that directory refuses, while the orphaned
	// inode the stale descriptor held would have left it free.
	if _, err := AcquireMaintenance(root); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("the admitted lock must cover the current root: %v", err)
	}
	if calls < 2 {
		t.Fatalf("the acquisition retried %d times, want a bounded local sequence", calls)
	}
}

// A replacement that outlives the bounded retry sequence refuses the
// command instead of looping or sleeping.
func TestMaintenanceRefusesWhenTheRootNeverStopsChanging(t *testing.T) {
	root := t.TempDir()
	open := openMaintenanceDirectory
	flock := func(fd int, how int) error {
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		return syscall.Flock(fd, how)
	}
	_, err := acquireMaintenanceLock(root, open, flock)
	if !errors.Is(err, ErrMaintenanceRootReplaced) {
		t.Fatalf("a churning root must refuse with the typed error: %v", err)
	}
}

// The lock dies with its holder: a process killed while holding the
// maintenance lock leaves the root immediately acquirable, because the
// kernel releases a flock when the open file description closes.
func TestMaintenanceLockIsReleasedWhenTheHolderDies(t *testing.T) {
	if os.Getenv("TEST_CONCORD_HOLD_MAINTENANCE") == "1" {
		release, err := AcquireMaintenance(os.Args[len(os.Args)-1])
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		fmt.Println("held")
		// Block until the parent kills this process; the lock must not
		// outlive it.
		time.Sleep(10 * time.Second)
		return
	}
	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=TestMaintenanceLockIsReleasedWhenTheHolderDies", root)
	command.Env = append(os.Environ(), "TEST_CONCORD_HOLD_MAINTENANCE=1")
	pipe, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(pipe)
	line, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "held" {
		_ = command.Process.Kill()
		t.Fatalf("the holder never reported the lock: %q %v", line, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	release, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatalf("the lock must die with its holder: %v", err)
	}
	release()
}

// A root replaced by a symlink between the open and the flock is never
// admitted: the confirmation must be an lstat, not a stat that follows the
// link back to the inode the descriptor holds — the rename-aside-and-link
// probe. The refusal names the symlink, and the moved directory is unlocked
// again afterwards.
func TestMaintenanceRefusesARootReplacedByASymlinkBetweenOpenAndFlock(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "concord")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved-aside")
	open := openMaintenanceDirectory
	flock := func(fd int, how int) error {
		if err := os.Rename(root, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, root); err != nil {
			t.Fatal(err)
		}
		return syscall.Flock(fd, how)
	}
	_, err := acquireMaintenanceLock(root, open, flock)
	if err == nil {
		t.Fatal("a root the path no longer names as a directory must not be admitted")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("the refusal must name the symlink: %v", err)
	}
	if _, err := os.Lstat(root); err != nil {
		t.Fatalf("the probe must leave the planted symlink in place: %v", err)
	}
	// The moved-aside directory is unlocked again: the stale descriptor was
	// released with the refused acquisition.
	probe, err := os.Open(moved)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the refused acquisition must release the stale descriptor: %v", err)
	}
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
}

// The release retains the empty bootstrap chain the acquisition created
// (obs:1ca149d633e69626): retained disk space is the accepted cost
// of never letting a cleanup delete a directory a replacement holder may
// own. A root another participant recreated during the hold, a root the
// command never bootstrapped, and the bootstrapped chain itself all survive
// the release.
func TestMaintenanceReleaseRetainsTheEmptyRootItBootstrapped(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "share", "concord")
	release, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		t.Fatalf("the release must retain the bootstrapped root: %v (%v)", info, err)
	}
	if info, err := os.Lstat(filepath.Dir(root)); err != nil || !info.IsDir() {
		t.Fatalf("the release must retain the bootstrapped ancestor: %v (%v)", info, err)
	}
	next, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatalf("the retained root must admit the next command: %v", err)
	}
	next()
}

// A root the command did not bootstrap is retained by the release like any
// other: nothing in the release path distinguishes a foreign root
// from a bootstrapped one, because nothing is removed at all.
func TestMaintenanceReleaseRetainsARootItDidNotBootstrap(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "concord")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		t.Fatalf("the release must retain a root it did not bootstrap: %v (%v)", info, err)
	}
}

// The external-replacement interleaving, exercised deterministically:
// while the first holder still holds the lock, another
// participant moves the root away and publishes its own directory at the
// path, and a second real acquisition holds the replacement — a different
// inode, so the flocks do not collide. Retention keeps both roots: the
// first holder's release deletes nothing, so the second holder's root and
// the moved-aside root both survive. Exclusion against this external
// replacement is explicitly not promised; retention is what keeps it safe.
func TestMaintenanceReleaseRetainsAReplacedRootAndItsSecondHolder(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "concord")
	aside := filepath.Join(parent, "aside")
	first, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatal(err)
	}
	// The external replacement: the root the first holder flocks is renamed
	// away, and a foreign empty root is published at the same path.
	if err := os.Rename(root, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireMaintenance(root)
	if err != nil {
		t.Fatalf("the replacement root must admit its own holder: %v", err)
	}
	first()
	second()
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		t.Fatalf("the release must retain the second holder's root: %v (%v)", info, err)
	}
	if info, err := os.Lstat(aside); err != nil || !info.IsDir() {
		t.Fatalf("the release must retain the moved-aside root: %v (%v)", info, err)
	}
}
