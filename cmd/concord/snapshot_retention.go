package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
)

const managedSnapshotCount = 8
const managedSnapshotAge = 30 * 24 * time.Hour

var managedSnapshotName = regexp.MustCompile(`^pre-(upgrade|repair)-(.+)-(\d{8}T\d{6}\.\d{9}Z)$`)

type snapshotRetentionReport struct {
	Deleted         []string `json:"deleted"`
	DeferredReason  string   `json:"deferred_reason,omitempty"`
	ProtectedExcess int      `json:"protected_excess"`
}

type managedSnapshot struct {
	name               string
	created            time.Time
	database, manifest os.FileInfo
	sidecars           map[string]os.FileInfo
}

// pruneManagedSnapshots owns only the CLI's managed backup namespace, not
// operator-selected exports. The caller holds the installer/core maintenance
// lock. Recovery records exclude the entire set: none identifies an exact pin.
func pruneManagedSnapshots(dataRoot, newSnapshot string, now time.Time) (snapshotRetentionReport, error) {
	report := snapshotRetentionReport{Deleted: []string{}}
	if newSnapshot == "" {
		return report, nil
	}
	backups := filepath.Join(dataRoot, "backups")
	if filepath.Dir(newSnapshot) != backups {
		return report, fmt.Errorf("new snapshot is outside the managed backups directory")
	}
	if err := snapshotDirectoryWithoutSymlinks(backups); err != nil {
		return report, err
	}
	root, err := os.OpenRoot(backups)
	if err != nil {
		return report, err
	}
	defer func() { _ = root.Close() }()
	entries, err := os.ReadDir(backups)
	if err != nil {
		return report, err
	}
	var snapshots []managedSnapshot
	newVerified := false
	for _, entry := range entries {
		candidate, ok := verifiedManagedSnapshot(root, backups, entry.Name())
		if !ok {
			continue
		}
		snapshots = append(snapshots, candidate)
		newVerified = newVerified || entry.Name() == filepath.Base(newSnapshot)
	}
	if !newVerified {
		return report, fmt.Errorf("new managed snapshot %s is not a verified regular snapshot/manifest pair", newSnapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].created.Equal(snapshots[j].created) {
			return snapshots[i].name > snapshots[j].name
		}
		return snapshots[i].created.After(snapshots[j].created)
	})
	reason, err := snapshotRecoveryExclusion(dataRoot)
	if err != nil {
		return report, err
	}
	report.DeferredReason = reason
	for i, snapshot := range snapshots {
		if i < managedSnapshotCount && !snapshot.created.Before(now.Add(-managedSnapshotAge)) {
			continue
		}
		if i == 0 || reason != "" {
			report.ProtectedExcess++
			continue
		}
		// Recheck exclusion and pair identity immediately before each removal.
		// An unexpected change ends the pass rather than substituting new bytes.
		if reason, err := snapshotRecoveryExclusion(dataRoot); err != nil || reason != "" {
			report.DeferredReason = reason
			return report, err
		}
		if !sameSnapshotMember(root, snapshot.name, snapshot.database) || !sameSnapshotMember(root, snapshot.name+".manifest.json", snapshot.manifest) {
			return report, fmt.Errorf("snapshot pair %s changed during retention", snapshot.name)
		}
		currentSidecars, ok := snapshotSidecars(root, snapshot.name)
		if !ok || len(currentSidecars) != len(snapshot.sidecars) {
			return report, fmt.Errorf("snapshot sidecars %s changed during retention", snapshot.name)
		}
		for name, info := range snapshot.sidecars {
			if !sameSnapshotMember(root, name, info) {
				return report, fmt.Errorf("snapshot sidecar %s changed during retention", name)
			}
		}
		// Unlink the database first. A failed second unlink leaves inventory
		// only, never an unmanifested database advertised as recoverable.
		if err := root.Remove(snapshot.name); err != nil {
			return report, fmt.Errorf("remove snapshot %s: %w", snapshot.name, err)
		}
		if err := root.Remove(snapshot.name + ".manifest.json"); err != nil {
			return report, fmt.Errorf("snapshot %s removed but manifest cleanup failed: %w", snapshot.name, err)
		}
		for name := range snapshot.sidecars {
			if err := root.Remove(name); err != nil {
				return report, fmt.Errorf("snapshot %s removed but sidecar cleanup failed: %w", snapshot.name, err)
			}
		}
		report.Deleted = append(report.Deleted, filepath.Join(backups, snapshot.name))
	}
	return report, nil
}

func verifiedManagedSnapshot(root *os.Root, backups, name string) (managedSnapshot, bool) {
	match := managedSnapshotName.FindStringSubmatch(name)
	if match == nil {
		return managedSnapshot{}, false
	}
	if _, err := time.Parse("20060102T150405.000000000Z", match[3]); err != nil {
		return managedSnapshot{}, false
	}
	database, err := root.Lstat(name)
	if err != nil || !database.Mode().IsRegular() {
		return managedSnapshot{}, false
	}
	manifest, err := root.Lstat(name + ".manifest.json")
	if err != nil || !manifest.Mode().IsRegular() {
		return managedSnapshot{}, false
	}
	// SQLite can leave empty WAL and shared-memory files after verification.
	// Reject symlinks and any sidecar containing uncheckpointed database data.
	if _, ok := snapshotSidecars(root, name); !ok {
		return managedSnapshot{}, false
	}
	verified, err := store.VerifyBackup(context.Background(), filepath.Join(backups, name))
	if err != nil {
		return managedSnapshot{}, false
	}
	if !sameSnapshotMember(root, name, database) || !sameSnapshotMember(root, name+".manifest.json", manifest) {
		return managedSnapshot{}, false
	}
	created, err := time.Parse(time.RFC3339Nano, verified.CreatedAt)
	sidecars, ok := snapshotSidecars(root, name)
	return managedSnapshot{name, created, database, manifest, sidecars}, err == nil && ok
}

func snapshotSidecars(root *os.Root, name string) (map[string]os.FileInfo, bool) {
	files := map[string]os.FileInfo{}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := root.Lstat(name + suffix)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || (suffix != "-shm" && info.Size() != 0) {
			return nil, false
		}
		files[name+suffix] = info
	}
	return files, true
}

func sameSnapshotMember(root *os.Root, name string, before os.FileInfo) bool {
	after, err := root.Lstat(name)
	return err == nil && after.Mode().IsRegular() && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

func snapshotDirectoryWithoutSymlinks(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("snapshot retention refuses non-directory or symlink %s", current)
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func snapshotRecoveryExclusion(dataRoot string) (string, error) {
	for _, path := range []string{hostlease.FencePath(dataRoot), filepath.Join(dataRoot, "prepared-release.json")} {
		if _, err := os.Lstat(path); err == nil {
			return "unfinished recovery record: " + path, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	transactions := filepath.Join(dataRoot, ".concord-transactions")
	if info, err := os.Lstat(transactions); err == nil {
		if !info.IsDir() {
			return "uninspectable installer journal directory: " + transactions, nil
		}
		entries, err := os.ReadDir(transactions)
		if err != nil {
			return "", err
		}
		if len(entries) != 0 {
			return "unfinished installer transaction: " + transactions, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	for _, directory := range []string{dataRoot, filepath.Join(dataRoot, "backups")} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return "", err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".concord-restore-") {
				return "unfinished restore staging: " + filepath.Join(directory, entry.Name()), nil
			}
		}
	}
	return "", nil
}

// A restore to an arbitrary destination must still exclude cleanup at its
// managed source. Destination staging alone cannot supply that exclusion.
func lockManagedSnapshotRestore(source string) (func(), error) {
	path, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	backups := filepath.Dir(path)
	if filepath.Base(backups) != "backups" || !managedSnapshotName.MatchString(filepath.Base(path)) {
		return func() {}, nil
	}
	if err := snapshotDirectoryWithoutSymlinks(backups); err != nil {
		return nil, err
	}
	return hostlease.AcquireMaintenance(filepath.Dir(backups))
}
