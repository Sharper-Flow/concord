package main

import (
	"context"
	"io"

	"github.com/sharper-flow/concord/internal/store"
)

// runProjectResolve maps replaceable path or remote evidence to a stable Project ID;
// only the core reads registered locators, never a host directory heuristic.
// The Product hop names the Product for host launchers; scope_version lets a
// cache distinguish a moved repository from changed Product-to-Project membership.
// The read is unauthenticated: filesystem access to the authority database is the
// trust boundary. It appends no event and must not become a second write authority.
func runProjectResolve(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var request struct {
		Directory string `json:"directory"`
		Worktree  string `json:"worktree"`
	}
	if err := decodeObject(raw, &request); err != nil {
		writeOperatorDiagnostic(errOut, "project-resolve", diagnosticText(err))
		return 1
	}
	if request.Directory == "" {
		writeOperatorDiagnostic(errOut, "project-resolve", "directory is required")
		return 1
	}
	// Defaulting the worktree preserves linked-worktree distinctions.
	worktree := request.Worktree
	if worktree == "" {
		worktree = request.Directory
	}
	ctx := context.Background()

	resolution, err := s.ResolveProject(ctx, request.Directory, worktree)
	if err != nil {
		writeOperatorDiagnostic(errOut, "project-resolve", diagnosticText(err))
		return 1
	}
	scopeVersion, productIDs, err := s.ScopeVersion(ctx, resolution.ProjectID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "project-resolve", diagnosticText(err))
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
