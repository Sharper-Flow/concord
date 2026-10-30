package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// Tests wrap the real backup to observe the admission boundary at the snapshot.
var snapshotDatabase = store.BackupDatabase

// snapshotBeforeMutation owns the upgrade snapshot policy. It returns
// the intended path even on failure. Only an absent source reports present=false;
// the caller decides whether absence permits its action.
func snapshotBeforeMutation(path, dataRoot, operation, releaseVersion string) (destination string, present bool, err error) {
	destination = filepath.Join(dataRoot, "backups",
		fmt.Sprintf("pre-%s-%s-%s", operation, releaseVersion, time.Now().UTC().Format("20060102T150405.000000000Z")))
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return destination, false, nil
	}
	if err != nil {
		return destination, true, fmt.Errorf("cannot inspect the source database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return destination, true, fmt.Errorf("source database %s is not a regular file", path)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return destination, true, fmt.Errorf("cannot create the snapshot directory: %w", err)
	}
	if _, err := snapshotDatabase(context.Background(), path, destination); err != nil {
		return destination, true, fmt.Errorf("database snapshot or verification failed: %w", err)
	}
	return destination, true, nil
}
