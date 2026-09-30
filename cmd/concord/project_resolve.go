package main

import (
	"context"
	"io"

	"github.com/sharper-flow/concord/internal/store"
)

// runProjectResolve answers "which Project owns this directory", the question
// CD-0008 D1 makes unanswerable from the filesystem alone: a path or remote is
// replaceable evidence for a stable Project ID, never the identity itself. A
// host that joins on a directory basename invents an identity Concord does not
// have, so the mapping is authority data and only the core can read it.
//
// The answer carries the Product hop as well. CD-0078 places terminal
// placement in the host and expects host launchers to show Concord state, and
// a Project ID alone does not name the Product that state belongs to.
// scope_version is the membership watermark, so a host cache can tell a moved
// repository from changed Product↔Project authority.
//
// Placement: a core CLI verb rather than a host script, the same rationale
// worktree-locate carries — the inputs are registered locators only the core
// can read. Recorded in .concord/docs/capability-placement.md §6 and CD-0079.
//
// The verb is unauthenticated, as worktree-locate is. CD-0079 D2 records why:
// the trust boundary is filesystem access to the authority database, which a
// caller able to exec this verb already holds. This is a read. It appends no
// event, and CD-0021's rejection of a second write authority still binds.
func runProjectResolve(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		Directory string `json:"directory"`
		Worktree  string `json:"worktree"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "project-resolve", err.Error())
		return 1
	}
	if request.Directory == "" {
		writeOperatorDiagnostic(errOut, "project-resolve", "directory is required")
		return 1
	}
	// A caller that names only the directory is asking about that directory.
	// Defaulting the worktree keeps the common host call a single field while
	// preserving the linked-worktree distinction CD-0008 D1 needs.
	worktree := request.Worktree
	if worktree == "" {
		worktree = request.Directory
	}
	ctx := context.Background()

	resolution, err := s.ResolveProject(ctx, request.Directory, worktree)
	if err != nil {
		writeOperatorDiagnostic(errOut, "project-resolve", err.Error())
		return 1
	}
	scopeVersion, productIDs, err := s.ScopeVersion(ctx, resolution.ProjectID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "project-resolve", err.Error())
		return 1
	}
	if productIDs == nil {
		productIDs = []string{}
	}

	locators := make([]map[string]string, 0, len(resolution.Locators))
	for _, locator := range resolution.Locators {
		locators = append(locators, map[string]string{
			"locator_id":       locator.ID,
			"kind":             string(locator.Kind),
			"normalized_value": locator.NormalizedValue,
		})
	}
	return writeJSON(out, map[string]any{
		"project_id":    resolution.ProjectID,
		"product_ids":   productIDs,
		"scope_version": scopeVersion,
		"main_worktree": resolution.MainWorktree,
		"repository": map[string]string{
			"canonical_path": resolution.Repository.CanonicalPath,
			"git_remote":     resolution.Repository.GitRemote,
		},
		"locators": locators,
	}, errOut)
}

// runProjectCanonicalPath answers "where does this Project's repository
// live" with the one fact the question needs: the Project's registered
// canonical_path locator, normalized. The adapter's second-session routing
// compares two Projects' canonical paths (CD-0182), and that comparison
// needs no git fact, so the read resolves no default branch and no commit.
// worktree-locate stays the owner of branch, base, and path derivation
// (issue #316); a Project whose repository cannot resolve a default ref
// still answers here.
//
// Placement: a core CLI verb rather than a host script, the same rationale
// worktree-locate carries — the value is registered locator data only the
// core can read. Recorded in .concord/docs/capability-placement.md §6.
//
// The verb is unauthenticated, as worktree-locate and project-resolve are.
// CD-0079 D2 records why: the trust boundary is filesystem access to the
// authority database, which a caller able to exec this verb already holds.
// This is a read. It appends no event, and CD-0021's rejection of a second
// write authority still binds.
func runProjectCanonicalPath(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		ProjectID string `json:"project_id"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "project-canonical-path", err.Error())
		return 1
	}
	if request.ProjectID == "" {
		writeOperatorDiagnostic(errOut, "project-canonical-path", "project_id is required")
		return 1
	}
	path, err := s.ProjectCanonicalPath(context.Background(), request.ProjectID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "project-canonical-path", err.Error())
		return 1
	}
	return writeJSON(out, map[string]string{
		"project_id":     request.ProjectID,
		"canonical_path": path,
	}, errOut)
}
