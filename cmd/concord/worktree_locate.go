package main

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sharper-flow/concord/internal/store"
)

// runWorktreeLocate derives the three inputs a worktree_claim requires —
// branch, base commit SHA, and absolute worktree path — from registered
// authority and repository facts, read-only. It is the named owner of the
// worktree locator policy (issue #316):
//
//   - Branch: `work/<work_id>`. The work item's identity names the branch, so
//     no second naming scheme exists to drift. A work ID that cannot form a
//     valid branch is refused, never sanitized.
//   - Path: `<data root>/worktrees/<project_id>/<work_id>`, where the data
//     root is the database's parent directory. Two Projects sharing a
//     repository basename stay distinct because the key is the Project ID,
//     never a path.
//   - Base: the commit SHA of the Project's `canonical_path` repository at
//     the requested ref (default `HEAD`, which is the default branch under
//     the trunk-stays-on-default rule).
//
// The store still owns validation: the derived intent passes
// store.ValidateWorktreeClaimIntent — the claim's own patterns — before it is
// returned, so a caller feeding this output to concord_work_transition.
// worktree_claim is accepted on the first attempt.
//
// Placement: a core CLI verb rather than a host script because the inputs are
// authority data (the Project's registered locator) that only the core can
// read; a host script would duplicate database access or double-hop through
// this verb anyway. Recorded in docs/capability-placement.md §6.
func runWorktreeLocate(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		ProjectID string `json:"project_id"`
		WorkID    string `json:"work_id"`
		Ref       string `json:"ref"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "worktree-locate", err.Error())
		return 1
	}
	if request.ProjectID == "" || request.WorkID == "" {
		writeOperatorDiagnostic(errOut, "worktree-locate", "project_id and work_id are required")
		return 1
	}
	ref := request.Ref
	if ref == "" {
		ref = "HEAD"
	}
	ctx := context.Background()

	var repo string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT normalized_value FROM project_locators WHERE kind=? AND project_id=? ORDER BY locator_id LIMIT 1`, store.LocatorCanonicalPath, request.ProjectID).Scan(&repo); err != nil {
		writeOperatorDiagnostic(errOut, "worktree-locate", "Project has no canonical_path locator; register the repository's canonical path locator")
		return 1
	}

	branch := "work/" + request.WorkID
	path := filepath.Join(filepath.Dir(s.Path()), "worktrees", request.ProjectID, request.WorkID)
	sha, err := resolveCommitSHA(ctx, repo, ref)
	if err != nil {
		writeOperatorDiagnostic(errOut, "worktree-locate", err.Error())
		return 1
	}
	if err := store.ValidateWorktreeClaimIntent(branch, sha, path); err != nil {
		writeOperatorDiagnostic(errOut, "worktree-locate", fmt.Sprintf("derived intent fails the claim's own validation: %v", err))
		return 1
	}
	return writeJSON(out, map[string]string{
		"branch":   branch,
		"base_sha": sha,
		"path":     path,
		"repo":     repo,
		"ref":      ref,
	}, errOut)
}

// resolveCommitSHA pins a ref to its full commit SHA through the native git
// executable. `rev-parse --verify <ref>^{commit}` refuses anything that does
// not resolve to exactly one commit, including ambiguous or truncated input.
func resolveCommitSHA(ctx context.Context, repo, ref string) (string, error) {
	if strings.ContainsAny(ref, " \t\n\r\x00") {
		return "", fmt.Errorf("ref contains whitespace or NUL")
	}
	output, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--verify", ref+"^{commit}").Output()
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s to a commit in %s", ref, repo)
	}
	sha := strings.TrimSpace(string(output))
	if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
		return "", fmt.Errorf("resolved value is not a full 40-character SHA")
	}
	return sha, nil
}
