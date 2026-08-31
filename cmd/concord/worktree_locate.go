package main

import (
	"context"
	"io"

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

	location, err := s.LocateWorktree(ctx, request.ProjectID, request.WorkID, ref)
	if err != nil {
		writeOperatorDiagnostic(errOut, "worktree-locate", err.Error())
		return 1
	}
	return writeJSON(out, location, errOut)
}
