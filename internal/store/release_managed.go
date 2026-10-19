package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// installedReleaseLink is the entry the installer writes into a data root it
// manages: <data_root>/current links <data_root>/<version> (scripts/install.py).
const installedReleaseLink = "current"

// installerReleaseName is the installer's VERSION_RE: the only names it gives
// a release tree under the data root.
var installerReleaseName = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ReleaseManagedRecovery is the route a refused binary takes instead of the
// installed store (CD-0240 D2).
const ReleaseManagedRecovery = "run the installed release, or set CONCORD_DB_PATH to a fresh path outside any repository; to work against this history, restore a backup of it to such a path"

// AdmitStorePath refuses, before any file is touched, a database in a data
// root the installer manages unless the running binary is one the installer
// placed there (CD-0240 D1). A branch, development, or test build, including
// one built inside a managed worktree under the data root, is not, so it
// never opens, migrates, or writes the installed store, whatever its version
// stamp. An unresolvable data root or binary fails closed.
func AdmitStorePath(path string) error {
	return admitStorePath(path, os.Executable)
}

func admitStorePath(path string, executable func() (string, error)) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return releaseManagedRefusal(path, fmt.Sprintf("the database path cannot be resolved: %v", err))
	}
	// A database reached through a symlink belongs to the directory of the
	// file it names as well as to the directory that holds the link.
	directories := []string{filepath.Dir(absolute)}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil && filepath.Dir(resolved) != directories[0] {
		directories = append(directories, filepath.Dir(resolved))
	}
	for _, dataRoot := range directories {
		if err := admitDataRoot(path, dataRoot, executable); err != nil {
			return err
		}
	}
	return nil
}

func admitDataRoot(path, dataRoot string, executable func() (string, error)) error {
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
	if installerPlaced(root, binary) {
		return nil
	}
	return releaseManagedRefusal(path, fmt.Sprintf("the running binary %s is not a release the installer placed in the data root %s, whose installed release is %s (%s)", binary, root, installed, link))
}

// installerPlaced reports whether binary sits where the installer places a
// core under root: <root>/<version>/bin/concord for a release tree, or
// <root>/.concord-transactions/<id>/stage/version/bin/concord for a staged
// candidate.
func installerPlaced(root, binary string) bool {
	relative, err := filepath.Rel(root, binary)
	if err != nil {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	switch len(parts) {
	case 3:
		return installerReleaseName.MatchString(parts[0]) && parts[1] == "bin" && parts[2] == "concord"
	case 6:
		return parts[0] == ".concord-transactions" && parts[1] != ".." && parts[1] != "." &&
			parts[2] == "stage" && parts[3] == "version" && parts[4] == "bin" && parts[5] == "concord"
	}
	return false
}

func releaseManagedRefusal(path, reason string) *Failure {
	return newFailure(KindInvalidOperation, "open",
		fmt.Sprintf("a binary the installer did not place refuses the release-managed store %s: %s", path, reason),
		true, ReleaseManagedRecovery)
}
