// Package hostlease records which release each live host session runs on.
// The installer reads the lease set to decide which release directories it
// may remove (CD-0111 D2), and concord upgrade reads it to refuse while a
// live session predates a pending breaking migration (CD-0111 D3). Both
// decisions belong to callers; this package only writes, reads, and prunes
// the leases themselves, and owns the maintenance fence that excludes new
// session admission across an incompatible maintenance boundary (CON-807).
//
// A lease is live when its process exists and started when the lease says it
// did. The start time separates a live pid from a recycled one. Liveness
// reads /proc, so the package operates on Linux, the only release platform.
package hostlease

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Lease is one host session's claim on the release it runs.
type Lease struct {
	PID            int    `json:"pid"`
	PidStart       uint64 `json:"pid_start"`
	ReleaseRoot    string `json:"release_root"`
	CoreBinary     string `json:"core_binary"`
	SchemaVersion  int    `json:"schema_version"`
	ManifestDigest string `json:"manifest_digest"`
	RecordedAt     string `json:"recorded_at"`
	// FenceProtocol is the maintenance-boundary protocol the core that
	// wrote this lease speaks (CON-807). A writer at the current protocol
	// refuses admission while a fence is open, so its sessions can be
	// excluded across a boundary. A lease that carries no protocol was
	// written by a legacy core that admits sessions without looking at the
	// fence: an unfenceable participant a boundary must fail closed on.
	FenceProtocol int `json:"fence_protocol"`
	// Directory and Worktree locate the host session that holds the lease.
	// A breaking-migration outage names them so the operator can end the
	// right sessions without correlating pids by hand. Both are optional:
	// a lease file written before they existed still reads with both empty.
	Directory string `json:"directory,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
}

// CurrentFenceProtocol is the maintenance-boundary protocol this package
// speaks: a writer at this protocol checks the fence before admitting a
// session, under the shared admission lock. Recognition is exact: only this
// number names semantics this package implements, so a lease or release
// tree carrying any other number — higher included — is an unknown
// participant the boundary fails closed on, never a capability.
const CurrentFenceProtocol = 1

// FenceOperationUpgrade names the one operation this package records as the
// authorized opener of a maintenance boundary: the core's incompatible-
// migration command (CON-807). A boundary that names no operation, or any
// other operation, is unattributed or foreign: it stays unchanged and
// authorizes no migration or activation.
const FenceOperationUpgrade = "concord-upgrade-incompatible-migration"

// ErrStaleLease marks a lease whose process no longer matches. Write uses it
// so a session cannot claim liveness for a process that already ended.
var ErrStaleLease = errors.New("hostlease: the process is not live with the recorded start time")

// ErrMaintenanceExcluded marks a session admission refused because a
// maintenance fence is open. New sessions fail closed until the boundary
// completes: the fence exists precisely so the final lease check, the
// incompatible migration, and the activation observe one admission set.
var ErrMaintenanceExcluded = errors.New("hostlease: session admission is excluded by an open maintenance boundary")

// ErrReleaseRemoved marks a session admission refused because the core the
// lease pins no longer exists: release cleanup removed its tree.
var ErrReleaseRemoved = errors.New("hostlease: the pinned release was removed; start a new session on the active release")

// Fence is the durable session-admission exclusion record (CON-807). The
// migration command opens it before its final lease check, and the
// installer's activation removes it after the prepared candidate commits
// and release cleanup finishes. Between those instants no new session may
// claim a lease, which is what makes a lease observation under the fence
// more than a snapshot.
type Fence struct {
	// FenceID identifies this boundary opening; a random 16-byte hex.
	FenceID string `json:"fence_id"`
	// CreatedAt is when the boundary opened, RFC 3339 UTC.
	CreatedAt string `json:"created_at"`
	// Operation attributes the boundary to the operation that opened it:
	// FenceOperationUpgrade for the core's incompatible-migration command.
	// An existing fence naming no operation, or a different one, is
	// unattributed or foreign: it authorizes no migration (CON-807).
	Operation string `json:"operation,omitempty"`
	// ReleaseRoot and CoreBinary name the binary that opened the boundary.
	ReleaseRoot string `json:"release_root"`
	CoreBinary  string `json:"core_binary"`
	// SchemaVersion is the schema that binary defines.
	SchemaVersion int `json:"schema_version"`
	// Notice tells a refused session where the boundary's commands live.
	Notice string `json:"notice"`
}

// AuthorizesNativeMigration reports whether an existing fence names exactly
// this binary's own incompatible-migration operation: the upgrade operation,
// this release root and core binary, and the schema this binary defines. A
// boundary meeting all of them is the one this same command opened — the
// legitimate failed-migration or committed-migration recovery tie — and any
// other boundary, whatever it carries, stays unchanged and authorizes no
// migration.
func (f Fence) AuthorizesNativeMigration(releaseRoot, coreBinary string, schemaVersion int) bool {
	return f.Operation == FenceOperationUpgrade &&
		f.ReleaseRoot == releaseRoot &&
		f.CoreBinary == coreBinary &&
		f.SchemaVersion == schemaVersion
}

// Directory returns the lease directory for a data root. The database path
// and the releases live under the same root, so the caller derives both from
// one XDG data home.
func Directory(dataRoot string) string {
	return filepath.Join(dataRoot, "hosts")
}

// FencePath returns the maintenance fence path for a data root. It sits
// beside the lease directory, never inside it: List must not mistake it for
// a lease, and pruning must never remove it.
func FencePath(dataRoot string) string {
	return filepath.Join(dataRoot, "maintenance.json")
}

// EnsureFence opens the maintenance boundary when it is not already open and
// returns the fence in effect. An existing fence is adopted unchanged: the
// boundary is shared by the migration and the activation, so the second
// participant must not rewrite the first one's record. A fence file that
// cannot be read or parsed fails closed rather than guessing.
//
// The check and the write run under the shared admission lock, and the record
// is fsynced with its directory before the call returns: once EnsureFence
// reports the boundary open, the exclusion is durable, and no admission that
// had already passed the fence check can still land after it — that admission
// either completed before the lock was taken or waits, re-reads the fence,
// and refuses.
func EnsureFence(dataRoot string, fence Fence) (Fence, error) {
	acquired, err := withAdmissionLock(dataRoot, func() (Fence, error) {
		if existing, err := ReadFence(dataRoot); existing != nil || err != nil {
			return derefFence(existing), err
		}
		if fence.FenceID == "" {
			raw := make([]byte, 16)
			if _, err := rand.Read(raw); err != nil {
				return Fence{}, fmt.Errorf("hostlease: cannot generate a fence id: %w", err)
			}
			fence.FenceID = hex.EncodeToString(raw)
		}
		if fence.CreatedAt == "" {
			fence.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		encoded, err := json.Marshal(fence)
		if err != nil {
			return Fence{}, fmt.Errorf("hostlease: cannot encode the maintenance fence: %w", err)
		}
		target := FencePath(dataRoot)
		temporary := target + ".tmp"
		if err := writeSynced(temporary, encoded); err != nil {
			return Fence{}, fmt.Errorf("hostlease: cannot write %s: %w", temporary, err)
		}
		if err := os.Rename(temporary, target); err != nil {
			_ = os.Remove(temporary)
			return Fence{}, fmt.Errorf("hostlease: cannot place %s: %w", target, err)
		}
		if err := syncDir(filepath.Dir(target)); err != nil {
			return Fence{}, fmt.Errorf("hostlease: cannot make %s durable: %w", target, err)
		}
		return fence, nil
	})
	if err != nil {
		return Fence{}, err
	}
	return acquired, nil
}

// ReadFence returns the open maintenance fence, or nil when the boundary is
// closed. A fence file that exists but cannot be read or parsed is an error:
// an unreadable exclusion record may be an open boundary, and admission must
// fail closed instead of treating it as absent.
func ReadFence(dataRoot string) (*Fence, error) {
	encoded, err := os.ReadFile(FencePath(dataRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("hostlease: cannot read %s: %w", FencePath(dataRoot), err)
	}
	var fence Fence
	if err := json.Unmarshal(encoded, &fence); err != nil {
		return nil, fmt.Errorf("hostlease: refusing malformed maintenance fence %s: %w", FencePath(dataRoot), err)
	}
	if fence.FenceID == "" {
		return nil, fmt.Errorf("hostlease: refusing malformed maintenance fence %s: no fence id", FencePath(dataRoot))
	}
	return &fence, nil
}

// RemoveFence closes the maintenance boundary. The activation calls it only
// after the prepared candidate committed and release cleanup finished, under
// the shared admission lock so a concurrent admission cannot slip between the
// last cleanup decision and the reopened boundary. Removing an absent fence
// is a no-op so completion stays idempotent.
func RemoveFence(dataRoot string) error {
	_, err := withAdmissionLock(dataRoot, func() (struct{}, error) {
		if err := os.Remove(FencePath(dataRoot)); err != nil && !os.IsNotExist(err) {
			return struct{}{}, fmt.Errorf("hostlease: cannot remove %s: %w", FencePath(dataRoot), err)
		}
		return struct{}{}, nil
	})
	return err
}

// RemoveFenceOwned closes the maintenance boundary only when the fence in
// effect still carries fenceID. Removal by position — unlink whatever sits
// at the path — lets an operation that observed no fence on entry delete a
// boundary a concurrent operation opened in between; removal by identity
// makes the closer prove it owns what it closes. Removing an absent fence
// is a no-op so completion stays idempotent; a fence with a different
// identity is left in place untouched.
func RemoveFenceOwned(dataRoot string, fenceID string) error {
	_, err := withAdmissionLock(dataRoot, func() (struct{}, error) {
		existing, err := ReadFence(dataRoot)
		if err != nil {
			return struct{}{}, err
		}
		if existing == nil || existing.FenceID != fenceID {
			return struct{}{}, nil
		}
		if err := os.Remove(FencePath(dataRoot)); err != nil && !os.IsNotExist(err) {
			return struct{}{}, fmt.Errorf("hostlease: cannot remove %s: %w", FencePath(dataRoot), err)
		}
		return struct{}{}, nil
	})
	return err
}

// NewFenceID mints the identity a boundary opener claims before it calls
// EnsureFence. Pre-minting lets the opener learn afterwards whether the
// fence in effect is the one it opened (EnsureFence returns the same id) or
// a boundary a concurrent operation opened first (a different id, never
// this caller's to close).
func NewFenceID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("hostlease: cannot generate a fence id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func derefFence(fence *Fence) Fence {
	if fence == nil {
		return Fence{}
	}
	return *fence
}

// Write records the lease for the process, replacing any earlier lease the
// same process wrote. It fails when the process is not live as described,
// and when a maintenance fence is open it refuses with ErrMaintenanceExcluded:
// admitting a session mid-boundary would strand it on a release the
// incompatible migration is about to make unusable. The fence check and the
// lease write run under the shared admission lock, so a boundary opening
// concurrently cannot interleave between them: the admission either lands
// before the fence or observes it and refuses.
func Write(dataRoot string, lease Lease) error {
	if lease.PID <= 0 {
		return fmt.Errorf("hostlease: lease pid %d is invalid", lease.PID)
	}
	_, err := withAdmissionLock(dataRoot, func() (struct{}, error) {
		if fence, err := ReadFence(dataRoot); err != nil {
			return struct{}{}, err
		} else if fence != nil {
			notice := fence.Notice
			if notice == "" {
				notice = "session admission reopens when the prepared release activates"
			}
			return struct{}{}, fmt.Errorf("%w: %s opened it at %s; %s",
				ErrMaintenanceExcluded, fence.CoreBinary, fence.CreatedAt, notice)
		}
		// Release cleanup deletes trees while holding this lock, so a claim
		// queued behind it must confirm its pinned core survived: admitting
		// it otherwise strands the session on a deleted release.
		if info, err := os.Stat(lease.CoreBinary); err != nil || !info.Mode().IsRegular() {
			return struct{}{}, fmt.Errorf("%w: %s", ErrReleaseRemoved, lease.CoreBinary)
		}
		start, err := ProcessStart(lease.PID)
		if err != nil {
			return struct{}{}, err
		}
		if start != lease.PidStart {
			return struct{}{}, fmt.Errorf("%w: pid %d now starts at %d, lease says %d", ErrStaleLease, lease.PID, start, lease.PidStart)
		}
		dir := Directory(dataRoot)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return struct{}{}, fmt.Errorf("hostlease: cannot create %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // the lease directory is forced to private directory permissions; 0700 is the minimum a directory admits.
			return struct{}{}, fmt.Errorf("hostlease: cannot secure %s: %w", dir, err)
		}
		encoded, err := json.Marshal(lease)
		if err != nil {
			return struct{}{}, fmt.Errorf("hostlease: cannot encode the lease: %w", err)
		}
		target := filepath.Join(dir, strconv.Itoa(lease.PID)+".json")
		temporary := target + ".tmp"
		if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
			return struct{}{}, fmt.Errorf("hostlease: cannot write %s: %w", temporary, err)
		}
		if err := os.Rename(temporary, target); err != nil {
			_ = os.Remove(temporary)
			return struct{}{}, fmt.Errorf("hostlease: cannot replace %s: %w", target, err)
		}
		return struct{}{}, nil
	})
	return err
}

// admissionLockName is the shared lock file serializing session admission
// against maintenance-boundary changes. It holds no state: only the flock.
func admissionLockPath(dataRoot string) string {
	return filepath.Join(dataRoot, "admission.lock")
}

// withAdmissionLock runs fn while holding the exclusive admission lock for
// the data root. Admission (Write) and boundary changes (EnsureFence,
// RemoveFence) take the same lock, which is what makes the fence exclusion
// shared: a lease cannot land between a boundary's check and its record, and
// a boundary cannot open between a lease's fence check and its write. The
// lock is advisory and Linux-only, matching the release platform.
func withAdmissionLock[T any](dataRoot string, fn func() (T, error)) (T, error) {
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		var zero T
		return zero, fmt.Errorf("hostlease: cannot create %s: %w", dataRoot, err)
	}
	file, err := os.OpenFile(admissionLockPath(dataRoot), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("hostlease: cannot open %s: %w", admissionLockPath(dataRoot), err)
	}
	defer func() { _ = file.Close() }()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		var zero T
		return zero, fmt.Errorf("hostlease: cannot lock %s: %w", admissionLockPath(dataRoot), err)
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	return fn()
}

// ErrMaintenanceBusy marks a maintenance command refused because another
// maintenance command holds the data root.
var ErrMaintenanceBusy = errors.New("hostlease: another maintenance command is in progress")

// AcquireMaintenance takes the exclusive maintenance lock for the data root
// and returns its release. One maintenance command runs at a time: a run
// decides whether to close the boundary it opened from what it alone could
// have committed, and that decision is only sound when no other run can
// migrate under the same boundary meanwhile (CON-807). The lock is a flock
// on the data root directory itself, so it leaves no file behind, and the
// installer's commands take the same lock (scripts/install.py
// maintenance_lock): no installer recovery or boundary close overlaps a
// migration. The lock does not wait: a second command refuses with
// ErrMaintenanceBusy and the operator re-runs it after the first finishes.
func AcquireMaintenance(dataRoot string) (func(), error) {
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		return nil, fmt.Errorf("hostlease: cannot create %s: %w", dataRoot, err)
	}
	path := dataRoot
	file, err := os.Open(path) //nolint:gosec // path is the operator's data root; the lock opens the directory itself.
	if err != nil {
		return nil, fmt.Errorf("hostlease: cannot open %s: %w", path, err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s is held; re-run when it finishes", ErrMaintenanceBusy, path)
		}
		return nil, fmt.Errorf("hostlease: cannot lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// writeSynced writes bytes and fsyncs the file, so a later rename makes the
// content durable, not only the directory entry.
func writeSynced(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // path is a fence or lease file this package derives under the data root.
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// syncDir fsyncs a directory so entries created inside it survive a crash.
func syncDir(path string) error {
	dir, err := os.Open(path) //nolint:gosec // path is a directory this package derives under the data root.
	if err != nil {
		return err
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return err
	}
	return dir.Close()
}

// List returns the live leases under the data root and prunes the stale
// ones. A lease whose process ended or was recycled is stale. A lease file
// this package cannot interpret is an error, not a stale entry: deleting an
// unreadable lease could remove a release a newer release's session still
// holds, so the caller must observe the failure instead of guessing.
func List(dataRoot string) ([]Lease, error) {
	dir := Directory(dataRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("hostlease: cannot read %s: %w", dir, err)
	}
	var live []Lease
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("hostlease: refusing unrecognized entry %s", filepath.Join(dir, name))
		}
		path := filepath.Join(dir, name)
		encoded, err := os.ReadFile(path) //nolint:gosec // the path is a directory entry this function just classified, inside the caller's data root.
		if err != nil {
			return nil, fmt.Errorf("hostlease: cannot read %s: %w", path, err)
		}
		var lease Lease
		if err := json.Unmarshal(encoded, &lease); err != nil {
			return nil, fmt.Errorf("hostlease: refusing malformed lease %s: %w", filepath.Join(dir, name), err)
		}
		start, err := ProcessStart(lease.PID)
		if err != nil || start != lease.PidStart {
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("hostlease: cannot prune %s: %w", path, err)
			}
			continue
		}
		live = append(live, lease)
	}
	return live, nil
}

// ProcessStart reads the process start time from /proc: field 22 of
// /proc/<pid>/stat, in clock ticks. The comm field can contain spaces and
// parentheses, so the field is counted after its closing parenthesis.
func ProcessStart(pid int) (uint64, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("hostlease: process %d is not observable: %w", pid, err)
	}
	text := string(raw)
	end := strings.LastIndexByte(text, ')')
	if end < 0 || end+2 > len(text) {
		return 0, fmt.Errorf("hostlease: /proc/%d/stat is malformed", pid)
	}
	fields := strings.Fields(text[end+2:])
	// Field 3 (state) is the first field after comm, so starttime is field
	// 22 overall: index 22-3 in this slice.
	const starttime = 22 - 3
	if len(fields) <= starttime {
		return 0, fmt.Errorf("hostlease: /proc/%d/stat is truncated", pid)
	}
	value, err := strconv.ParseUint(fields[starttime], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("hostlease: /proc/%d/stat starttime is not a number: %w", pid, err)
	}
	return value, nil
}

// linuxUserHZ is the kernel's fixed USER_HZ: the unit /proc reports process
// start times in on every Linux release platform, independent of the
// configured kernel tick rate.
const linuxUserHZ = 100

// WallStart reports when the process began, as wall-clock time. /proc records
// the start as clock ticks since boot, so the conversion subtracts the boot
// age that /proc/uptime reports. The value is approximate: it reads two files
// and the clock in separate instants, so callers must compare it only against
// times the subject provably predates or postdates by more than the skew.
func WallStart(pid int) (time.Time, error) {
	ticks, err := ProcessStart(pid)
	if err != nil {
		return time.Time{}, err
	}
	raw, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return time.Time{}, fmt.Errorf("hostlease: cannot read /proc/uptime: %w", err)
	}
	uptimeText := strings.Fields(string(raw))
	if len(uptimeText) == 0 {
		return time.Time{}, fmt.Errorf("hostlease: /proc/uptime is empty")
	}
	uptime, err := strconv.ParseFloat(uptimeText[0], 64)
	if err != nil || uptime < 0 {
		return time.Time{}, fmt.Errorf("hostlease: /proc/uptime is not a number of seconds")
	}
	age := uptime - float64(ticks)/linuxUserHZ
	if age < 0 {
		age = 0
	}
	return time.Now().Add(-time.Duration(age * float64(time.Second))), nil
}
