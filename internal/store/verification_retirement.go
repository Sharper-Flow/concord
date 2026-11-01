package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/sharper-flow/concord/internal/hostlease"
)

func checkVerificationRetirement(ctx context.Context, q queryer) error {
	rows, err := q.QueryContext(ctx, `SELECT l.lease_id,l.work_id,COALESCE(w.lifecycle,''),l.path,l.owner_pid,l.owner_started
FROM worktree_verify_leases l LEFT JOIN work_items w ON w.id=l.work_id
WHERE l.state='held' OR l.outcome='running' ORDER BY l.lease_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var lease, work, lifecycle, path, started string
		var pid int
		if err := rows.Scan(&lease, &work, &lifecycle, &path, &pid, &started); err != nil {
			return err
		}
		recovery := verificationRetirementRecovery(lifecycle, path, pid, started, hostlease.ProcessStart, os.Lstat)
		if recovery != "" {
			return newFailure(KindResourceBusy, "migrate", fmt.Sprintf("repository verification lease %s for work %s is still held or running", lease, work), false, recovery)
		}
	}
	return rows.Err()
}

func verificationRetirementRecovery(lifecycle, path string, pid int, started string, processStart func(int) (uint64, error), lstat func(string) (os.FileInfo, error)) string {
	const settle = "complete or cancel the named work with the installed release, then retry concord upgrade"
	terminal := lifecycle == "completed" || lifecycle == "cancelled" || lifecycle == "superseded"
	if pid == 0 && started == "" {
		if !terminal {
			return settle
		}
		// Pre-identity leases cannot be reclaimed by the predecessor. Terminal
		// work with no worktree has no reachable verifier left to resume.
		if filepath.IsAbs(path) {
			_, err := lstat(path)
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				return ""
			}
			if err != nil {
				return "retry concord upgrade on the original Concord host with the recorded worktree path readable"
			}
		}
		return "on the original Concord host, preserve changes and remove the terminal work's unused worktree with native Git, then retry concord upgrade"
	}
	want, err := strconv.ParseUint(started, 10, 64)
	if pid <= 0 || err != nil || want == 0 {
		return "ask a maintainer to recover the incomplete verification owner identity from the predecessor snapshot; do not hand-edit lease rows"
	}
	// An absent /proc mount must not make every recorded owner look dead.
	if _, err := processStart(os.Getpid()); err != nil {
		return "retry concord upgrade on the original Concord host with /proc readable"
	}
	// A live owner is still verifying, whatever its work's lifecycle: changing
	// the lifecycle neither stops nor settles the run.
	got, err := processStart(pid)
	switch {
	case err == nil && got == want:
		return fmt.Sprintf("wait for verification owner pid %d (start %s) to exit or end its Concord session, then retry concord upgrade", pid, started)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return "retry concord upgrade on the original Concord host with the named verification owner's /proc entry readable"
	case !terminal:
		return settle
	}
	return ""
}
