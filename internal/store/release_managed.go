package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// installedReleaseLink is the entry the installer writes into a data root it
// manages: <data_root>/current links <data_root>/<version> (scripts/install.py).
const installedReleaseLink = "current"

// ReleaseManagedRecovery is the route a refused binary takes instead of the
// installed store (CD-0240 D2).
const ReleaseManagedRecovery = "run the installed release, or set CONCORD_DB_PATH to a fresh path outside any repository; to work against this history, restore a backup of it to such a path"

// AdmitStorePath refuses, before any file is touched, a database whose
// directory is a data root the installer manages when the running binary
// does not live under that root (CD-0240 D1). Every binary the installer
// places, an installed release tree, a prepared candidate, or a transaction
// stage, lives under the data root. A branch, development, or test build
// does not, so it never opens, migrates, or writes the installed store,
// whatever its version stamp. An unresolvable data root or binary fails
// closed.
func AdmitStorePath(path string) error {
	return admitStorePath(path, os.Executable)
}

func admitStorePath(path string, executable func() (string, error)) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return releaseManagedRefusal(path, fmt.Sprintf("the database path cannot be resolved: %v", err))
	}
	dataRoot := filepath.Dir(absolute)
	link := filepath.Join(dataRoot, installedReleaseLink)
	// A missing entry, or a parent that is not a directory, is no data root
	// the installer manages.
	if _, err := os.Lstat(link); errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil
	} else if err != nil {
		return releaseManagedRefusal(path, fmt.Sprintf("%s cannot be inspected: %v", link, err))
	}
	installed := "unknown"
	if target, err := os.Readlink(link); err == nil {
		installed = filepath.Base(target)
	}
	root, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return releaseManagedRefusal(path, fmt.Sprintf("the data root %s cannot be resolved: %v", dataRoot, err))
	}
	binary, err := executable()
	if err == nil {
		binary, err = filepath.EvalSymlinks(binary)
	}
	if err != nil {
		return releaseManagedRefusal(path, fmt.Sprintf("the running binary cannot be located: %v", err))
	}
	if strings.HasPrefix(binary, root+string(filepath.Separator)) {
		return nil
	}
	return releaseManagedRefusal(path, fmt.Sprintf("the running binary %s is not under the data root %s, whose installed release is %s (%s)", binary, root, installed, link))
}

func releaseManagedRefusal(path, reason string) *Failure {
	return newFailure(KindInvalidOperation, "open",
		fmt.Sprintf("a binary the installer did not place refuses the release-managed store %s: %s", path, reason),
		true, ReleaseManagedRecovery)
}
