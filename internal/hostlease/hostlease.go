// Package hostlease records session release leases and owns the maintenance
// fence. Callers decide release cleanup and breaking-migration admission.
// Liveness uses Linux /proc and compares process start time to reject recycled pids.
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
	// FenceProtocol identifies the writer's admission protocol. A missing
	// protocol identifies an unfenceable participant; the boundary fails closed.
	FenceProtocol int `json:"fence_protocol"`
	// Directory and Worktree locate the session in migration refusals. Both are
	// optional; older leases can omit them.
	Directory string `json:"directory,omitempty"`
	Worktree  string `json:"worktree,omitempty"`
}

// Writers using CurrentFenceProtocol check the fence under the admission lock.
// Recognition is exact: any other number, including a higher one, fails closed.
const CurrentFenceProtocol = 1

// FenceOperationUpgrade identifies the incompatible-migration command; an absent
// or foreign operation authorizes neither migration nor activation.
const FenceOperationUpgrade = "concord-upgrade-incompatible-migration"

// ErrStaleLease marks a lease whose process no longer matches. Write uses it
// so a session cannot claim liveness for a process that already ended.
var ErrStaleLease = errors.New("hostlease: the process is not live with the recorded start time")

// ErrMaintenanceExcluded refuses session admission while a fence is open,
// preserving one admission set across final lease check, migration and activation.
var ErrMaintenanceExcluded = errors.New("hostlease: session admission is excluded by an open maintenance boundary")

// ErrReleaseRemoved marks a session admission refused because the core the
// lease pins no longer exists: release cleanup removed its tree.
var ErrReleaseRemoved = errors.New("hostlease: the pinned release was removed; start a new session on the active release")

// Fence excludes admission from before the final lease check through migration
// and release cleanup; the installer's activation removes it.
type Fence struct {
	// FenceID identifies this boundary opening; a random 16-byte hex.
	FenceID string `json:"fence_id"`
	// CreatedAt is when the boundary opened, RFC 3339 UTC.
	CreatedAt string `json:"created_at"`
	// Operation attributes the fence to its opener. A missing or foreign
	// operation authorizes no migration.
	Operation string `json:"operation,omitempty"`
	// ReleaseRoot and CoreBinary name the binary that opened the boundary.
	ReleaseRoot string `json:"release_root"`
	CoreBinary  string `json:"core_binary"`
	// SchemaVersion is the schema that binary defines.
	SchemaVersion int `json:"schema_version"`
	// Notice tells a refused session where the boundary's commands live.
	Notice string `json:"notice"`
}

// AuthorizesNativeMigration requires the upgrade operation, this release root,
// core binary and schema; any other fence stays unchanged and authorizes no migration.
func (f Fence) AuthorizesNativeMigration(releaseRoot, coreBinary string, schemaVersion int) bool {
	return f.Operation == FenceOperationUpgrade &&
		f.ReleaseRoot == releaseRoot &&
		f.CoreBinary == coreBinary &&
		f.SchemaVersion == schemaVersion
}

// Directory returns the lease directory under the same data root as the store and releases.
func Directory(dataRoot string) string {
	return filepath.Join(dataRoot, "hosts")
}

// FencePath sits outside the lease directory so listing or pruning cannot remove it.
func FencePath(dataRoot string) string {
	return filepath.Join(dataRoot, "maintenance.json")
}

// EnsureFence opens or adopts a fence unchanged. An unreadable fence fails closed.
// The check and write hold the shared admission lock; file and directory fsync
// make exclusion durable before return, with no admission able to land while open.
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

// ReadFence returns nil when closed; an unreadable or malformed fence is an
// error because admission must fail closed.
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

// RemoveFenceOwned closes only the matching fence. An absent fence is a no-op;
// a different identity remains untouched, protecting concurrent boundaries.
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

// NewFenceID pre-mints an identity so an opener can distinguish its own fence
// from a concurrent boundary it must not close.
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

// Write replaces the process's earlier lease. It refuses stale processes and
// returns ErrMaintenanceExcluded for open fences; checks and write share the lock.
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
		// Cleanup holds this lock; queued claims must verify their core survived.
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

// admissionLockPath names the shared lock file serializing session admission
// against maintenance-boundary changes. It holds no state: only the flock.
func admissionLockPath(dataRoot string) string {
	return filepath.Join(dataRoot, "admission.lock")
}

// withAdmissionLock runs fn under the exclusive shared admission lock. Write,
// EnsureFence and RemoveFenceOwned use it; the advisory lock is Linux-only.
func withAdmissionLock[T any](dataRoot string, fn func() (T, error)) (T, error) {
	if err := os.MkdirAll(dataRoot, 0o700); err != nil { //nolint:gosec // dataRoot is the operator's data root; the admission path creates its missing levels.
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

// ErrMaintenanceRootReplaced marks a maintenance acquisition that ended
// without admission because the data root kept being removed or replaced
// while the lock was being taken.
var ErrMaintenanceRootReplaced = errors.New("hostlease: the data root kept changing while the maintenance lock was taken")

// maintenanceAcquireAttempts bounds retries when root replacement leaves a stale
// descriptor; a churning root refuses without waiting, so the operator retries.
const maintenanceAcquireAttempts = 3

// AcquireMaintenance takes the exclusive non-waiting flock on the data root,
// shared with the installer; a competing command receives ErrMaintenanceBusy.
// Bootstrap creates the root before opening it. The open rejects symlinks;
// after flock, lstat confirms the path still names the descriptor's device and
// inode. A replaced root is reopened within a bounded sequence without sleeping.
// The release removes nothing: the root, ancestors, and their state remain because
// a path-based cleanup cannot safely exclude a replacement holder. Retained disk
// space is the cost; exclusion against arbitrary external replacement is not promised.
func AcquireMaintenance(dataRoot string) (func(), error) {
	file, err := acquireMaintenanceLock(dataRoot, openMaintenanceDirectory, syscall.Flock)
	if err != nil {
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// AcquireMaintenanceInherited validates and shares the installer's lock with its
// upgrade child, using the normal post-flock checks. Closing the duplicate must
// not LOCK_UN: Linux shares flock ownership with the parent until cutover exits.
func AcquireMaintenanceInherited(dataRoot string, descriptor int) (func(), error) {
	file, err := acquireMaintenanceLock(dataRoot, func(path string) (*os.File, error) {
		duplicate, err := syscall.Dup(descriptor)
		if err != nil {
			return nil, err
		}
		syscall.CloseOnExec(duplicate)
		file := os.NewFile(uintptr(duplicate), "inherited maintenance lock")
		current, pathErr := os.Lstat(path) //nolint:gosec // path is the operator's data root; lstat checks identity without following symlinks.
		held, statErr := file.Stat()
		if pathErr != nil || statErr != nil || !current.IsDir() || !sameFile(held, current) {
			_ = file.Close()
			return nil, fmt.Errorf("inherited descriptor does not name the data root directory")
		}
		return file, nil
	}, syscall.Flock)
	if err != nil {
		return nil, err
	}
	child, err := acquireUpgradeChildLock(dataRoot)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = child.Close()
		_ = file.Close()
	}, nil
}

// A duplicated root descriptor proves ownership but cannot exclude another
// child sharing that description. Each child opens this stable guard separately;
// it is never unlinked, so all inherited upgrades contend on one inode.
func acquireUpgradeChildLock(dataRoot string) (*os.File, error) {
	path := filepath.Join(dataRoot, "native-upgrade.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) //nolint:gosec // A fixed name inside the validated data root; O_NOFOLLOW rejects symlinked guards.
	if err != nil {
		return nil, fmt.Errorf("hostlease: cannot open native upgrade lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: native upgrade lock: %v", ErrMaintenanceBusy, err)
		}
		return nil, fmt.Errorf("hostlease: cannot lock native upgrade: %w", err)
	}
	current, pathErr := os.Lstat(path) //nolint:gosec // The owned guard's identity is checked without following symlinks.
	held, statErr := file.Stat()
	if pathErr != nil || statErr != nil || !current.Mode().IsRegular() || !sameFile(held, current) {
		_ = file.Close()
		return nil, fmt.Errorf("hostlease: native upgrade lock identity changed")
	}
	return file, nil
}

// openMaintenanceDirectory opens the data root for flocking: read-only, a
// directory, and never through a symlink. The refusal on a symlink is the
// open itself, so no window exists where a replaced path is followed.
func openMaintenanceDirectory(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0) //nolint:gosec // path is the operator's data root; the lock opens the directory itself.
}

// dirIdentity fails closed if platform stat data cannot prove the directory identity.
func dirIdentity(info os.FileInfo) (uint64, uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return stat.Dev, stat.Ino, true
}

// acquireMaintenanceLock takes open and flock as parameters so tests can act between them.
func acquireMaintenanceLock(
	dataRoot string,
	open func(string) (*os.File, error),
	flock func(int, int) error,
) (*os.File, error) {
	for attempt := 1; attempt <= maintenanceAcquireAttempts; attempt++ {
		// Bootstrap takes the same lock as an established root. Release retains
		// the directory because deletion could remove a replacement holder's root.
		if err := os.MkdirAll(dataRoot, 0o700); err != nil { //nolint:gosec // dataRoot is the operator's data root; the acquisition creates its missing levels.
			return nil, fmt.Errorf("hostlease: cannot create %s: %w", dataRoot, err)
		}
		file, err := open(dataRoot)
		if err != nil {
			if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
				return nil, maintenanceRootOpenRefusal(dataRoot)
			}
			if errors.Is(err, os.ErrNotExist) && attempt < maintenanceAcquireAttempts {
				continue
			}
			return nil, fmt.Errorf("hostlease: cannot open %s: %w", dataRoot, err)
		}
		if err := flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = file.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				return nil, fmt.Errorf("%w: %s is held; re-run when it finishes", ErrMaintenanceBusy, dataRoot)
			}
			return nil, fmt.Errorf("hostlease: cannot lock %s: %w", dataRoot, err)
		}
		// The flock is real only when the descriptor still names the
		// directory the path names: a root removed or replaced before
		// admission leaves the lock on an orphaned inode, and the next
		// command would lock a different directory altogether. The
		// confirmation is an lstat, so a root replaced by a symlink is
		// never followed into admission.
		current, err := os.Lstat(dataRoot) //nolint:gosec // dataRoot is the operator's data root; the confirmation is read without following symlinks.
		if err != nil {
			_ = file.Close()
			if errors.Is(err, os.ErrNotExist) && attempt < maintenanceAcquireAttempts {
				continue
			}
			return nil, fmt.Errorf("hostlease: cannot confirm %s: %w", dataRoot, err)
		}
		held, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			return nil, fmt.Errorf("hostlease: cannot confirm %s: %w", dataRoot, statErr)
		}
		if !current.IsDir() || !sameFile(held, current) {
			_ = file.Close()
			continue
		}
		return file, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrMaintenanceRootReplaced, dataRoot)
}

func maintenanceRootOpenRefusal(dataRoot string) error {
	// The open refused without following the path; lstat only names the reason.
	if info, statErr := os.Lstat(dataRoot); statErr == nil && info.Mode()&os.ModeSymlink != 0 { //nolint:gosec // dataRoot is the operator's data root; the lstat only names the reason for the operator.
		return fmt.Errorf("hostlease: refusing symlinked data root %s", dataRoot)
	}
	return fmt.Errorf("hostlease: refusing data root %s: it is not a directory", dataRoot)
}

// sameFile compares kernel device and inode identities; unavailable stat data never matches.
func sameFile(a, b os.FileInfo) bool {
	leftDev, leftIno, lok := dirIdentity(a)
	rightDev, rightIno, rok := dirIdentity(b)
	return lok && rok && leftDev == rightDev && leftIno == rightIno
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

// List prunes stale leases. An unreadable lease is an error, not a stale entry:
// deleting it could remove a release still held by a newer session.
func List(dataRoot string) ([]Lease, error) {
	return list(dataRoot, true)
}

// Observe returns live leases without removing stale lease files.
func Observe(dataRoot string) ([]Lease, error) {
	return list(dataRoot, false)
}

func list(dataRoot string, prune bool) ([]Lease, error) {
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
			if prune {
				if err := os.Remove(path); err != nil {
					return nil, fmt.Errorf("hostlease: cannot prune %s: %w", path, err)
				}
			}
			continue
		}
		live = append(live, lease)
	}
	return live, nil
}

// ProcessStart reads field 22 of /proc/<pid>/stat in clock ticks. Count after
// comm's closing parenthesis because comm can contain spaces and parentheses.
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
