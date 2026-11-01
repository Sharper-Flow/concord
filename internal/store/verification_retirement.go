package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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
	if lifecycle != "completed" && lifecycle != "cancelled" && lifecycle != "superseded" {
		return "complete or cancel the named work with the installed release, then retry concord upgrade"
	}
	// An absent /proc mount must not make every recorded owner look dead.
	if _, err := processStart(os.Getpid()); err != nil {
		return "retry concord upgrade on the original Concord host with /proc readable"
	}
	if pid == 0 && started == "" {
		// Pre-identity leases cannot be reclaimed by the predecessor. Terminal
		// work with no worktree has no reachable verifier left to resume.
		if filepath.IsAbs(path) {
			if _, err := lstat(path); os.IsNotExist(err) {
				return ""
			}
		}
		return "on the original Concord host, preserve changes and remove the terminal work's unused worktree with native Git, then retry concord upgrade"
	}
	want, err := strconv.ParseUint(started, 10, 64)
	if pid <= 0 || err != nil || want == 0 {
		return "ask a maintainer to recover the incomplete verification owner identity from the predecessor snapshot; do not hand-edit lease rows"
	}
	got, err := processStart(pid)
	if errors.Is(err, os.ErrNotExist) || (err == nil && got != want) {
		return ""
	}
	if err != nil {
		return "retry concord upgrade on the original Concord host with the named verification owner's /proc entry readable"
	}
	return fmt.Sprintf("wait for verification owner pid %d (start %s) to exit, then retry concord upgrade", pid, started)
}
