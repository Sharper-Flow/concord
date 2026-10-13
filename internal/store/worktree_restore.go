package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type WorktreeRestoration struct {
	Restored           bool   `json:"restored,omitempty"`
	RestoredTip        string `json:"restored_tip,omitempty"`
	RestorationWarning string `json:"restoration_warning,omitempty"`
}

// RestoreResumeWorktree restores native state only. All store reads finish
// before Git runs; restoration neither opens a transaction nor replaces a claim.
func (s *Store) RestoreResumeWorktree(ctx context.Context, workID string, entry WorktreeEntry, runner GitRunner) (WorktreeRestoration, error) {
	current, err := activeWorktreeEntryForProject(ctx, s.db, "work_resume", workID, entry.ProjectID)
	if err != nil {
		return WorktreeRestoration{}, err
	}
	if !sameRestoreClaim(current, entry) {
		return WorktreeRestoration{}, restoreConflict("the active claim changed before resume verification")
	}
	repo, err := s.ProjectCanonicalPath(ctx, entry.ProjectID)
	if err != nil {
		return WorktreeRestoration{}, err
	}
	if runner == nil {
		runner = ExecGitRunner{}
	}
	restored, err := restoreResumeWorktree(ctx, filepath.Join(filepath.Dir(s.Path()), "worktrees"), repo, entry, runner)
	if err != nil {
		return restored, err
	}
	current, err = activeWorktreeEntryForProject(ctx, s.db, "work_resume", workID, entry.ProjectID)
	if err != nil {
		return restored, err
	}
	if !sameRestoreClaim(current, entry) {
		return restored, restoreConflict("the active claim changed during resume verification")
	}
	return restored, nil
}

func sameRestoreClaim(current, expected WorktreeEntry) bool {
	return current.SetID == expected.SetID && current.ProjectID == expected.ProjectID && current.ClaimOpID == expected.ClaimOpID && current.Path == expected.Path && current.Branch == expected.Branch && current.BaseSHA == expected.BaseSHA
}

func restoreResumeWorktree(ctx context.Context, root, repo string, entry WorktreeEntry, runner GitRunner) (WorktreeRestoration, error) {
	var result WorktreeRestoration
	if err := ValidateWorktreeClaimIntent(entry.Branch, entry.BaseSHA, entry.Path); err != nil {
		return result, err
	}
	relative, err := filepath.Rel(root, entry.Path)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return result, restoreConflict("the recorded worktree path is outside the Concord worktree root")
	}
	info, err := os.Lstat(entry.Path)
	if err == nil {
		if !info.IsDir() {
			return result, restoreConflict("the recorded worktree path already exists and is not a directory")
		}
		_, err = verifyResumeWorktree(ctx, runner, repo, entry)
		return result, err
	}
	if !errors.Is(err, os.ErrNotExist) {
		return result, wrapFailure(KindUnavailable, "work_resume", "cannot inspect the recorded worktree path", true, "restore filesystem access before resuming", err)
	}
	// Git's force flag also permits a duplicate checkout of a live branch.
	// Restrict that permission to this entry's missing registration only.
	if err := verifyRestoreRegistration(ctx, runner, repo, entry); err != nil {
		return result, err
	}
	tip, exists, err := worktreeBranchHead(ctx, runner, repo, entry.Branch)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, newFailure(KindProjectionNotFound, "work_resume", "the worktree and its branch are both gone: "+entry.Branch, false, "check for a preserved remote branch before reclaiming the absent entry and replaying work_start")
	}
	if _, err := runner.Run(ctx, repo, "merge-base", "--is-ancestor", entry.BaseSHA, tip); err != nil {
		return result, restoreConflict("the recorded branch no longer contains the claim's base")
	}
	// Reserve an absent directory exclusively before checkout. A directory
	// another process creates meanwhile is never adopted or overwritten.
	if err := reserveRestoreDirectory(root, relative); err != nil {
		return result, err
	}
	if _, err := runner.Run(ctx, repo, "worktree", "add", "--force", "--", entry.Path, entry.Branch); err != nil {
		return result, wrapFailure(KindGitUnreachable, "work_resume", "cannot restore the recorded worktree", false, "inspect the recorded path and Git registration before resuming; no existing directory may be removed automatically", err)
	}
	facts, err := verifyResumeWorktree(ctx, runner, repo, entry)
	if err != nil {
		return result, err
	}
	if facts.headSHA != tip {
		return result, restoreConflict("the recorded branch changed during worktree restoration")
	}
	result.Restored = true
	result.RestoredTip = facts.headSHA
	result.RestorationWarning = "The recorded worktree was restored from its surviving branch. Uncommitted changes in the lost directory are not recoverable."
	return result, nil
}

func restoreConflict(message string) error {
	return newFailure(KindProjectionConflict, "work_resume", message, false, "preserve the existing content and inspect the recorded worktree; resume never removes or overwrites a directory")
}

func verifyResumeWorktree(ctx context.Context, runner GitRunner, repo string, entry WorktreeEntry) (worktreeFacts, error) {
	ok, facts, err := probeWorktree(ctx, runner, repo, entry.Path, entry.Branch, entry.BaseSHA)
	if err != nil {
		return facts, err
	}
	if !ok {
		return facts, restoreConflict("the existing directory does not match the recorded worktree and branch")
	}
	top, err := runner.Run(ctx, entry.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return facts, wrapFailure(KindGitUnreachable, "work_resume", "cannot verify the worktree root", true, "restore repository access before resuming", err)
	}
	actual, err := normalizePath(strings.TrimSpace(string(top)))
	if err != nil {
		return facts, err
	}
	expected, err := normalizePath(entry.Path)
	if err != nil || actual != expected {
		return facts, restoreConflict("the existing directory is not the recorded worktree root")
	}
	return facts, nil
}

func verifyRestoreRegistration(ctx context.Context, runner GitRunner, repo string, entry WorktreeEntry) error {
	output, err := runner.Run(ctx, repo, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return wrapFailure(KindGitUnreachable, "work_resume", "cannot inspect worktree registrations", true, "restore repository access before resuming", err)
	}
	for _, record := range strings.Split(string(output), "\x00\x00") {
		var path, branch string
		locked := false
		for _, field := range strings.Split(record, "\x00") {
			switch {
			case strings.HasPrefix(field, "worktree "):
				path = strings.TrimPrefix(field, "worktree ")
			case strings.HasPrefix(field, "branch "):
				branch = strings.TrimPrefix(field, "branch ")
			case field == "locked" || strings.HasPrefix(field, "locked "):
				locked = true
			}
		}
		if path == entry.Path && (branch != "refs/heads/"+entry.Branch || locked) {
			return restoreConflict("the missing worktree registration is locked or names another branch")
		}
		if branch == "refs/heads/"+entry.Branch && path != entry.Path {
			return restoreConflict("the recorded branch is already assigned to another worktree")
		}
	}
	return nil
}

func reserveRestoreDirectory(root, relative string) error {
	if err := os.Mkdir(root, 0o750); err != nil && !errors.Is(err, os.ErrExist) {
		return wrapFailure(KindUnavailable, "work_resume", "cannot open the Concord worktree root", true, "restore filesystem access before resuming", err)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return restoreConflict("the Concord worktree root is not a real directory")
	}
	contained, err := os.OpenRoot(root)
	if err != nil {
		return wrapFailure(KindUnavailable, "work_resume", "cannot open the Concord worktree root", true, "restore filesystem access before resuming", err)
	}
	defer func() { _ = contained.Close() }()
	// Git uses a path, not the os.Root descriptor. Refuse symlinked parents
	// so it cannot follow an escaping parent during the native checkout.
	parent := "."
	for _, segment := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		parent = filepath.Join(parent, segment)
		info, err := contained.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			if err := contained.Mkdir(parent, 0o750); err != nil {
				return restoreConflict("the recorded worktree parent cannot be created inside the Concord worktree root")
			}
			continue
		}
		if err != nil || !info.IsDir() {
			return restoreConflict("the recorded worktree parent is not a real directory")
		}
	}
	if err := contained.Mkdir(relative, 0o750); err != nil {
		return restoreConflict("the recorded worktree directory appeared before restoration")
	}
	return nil
}
