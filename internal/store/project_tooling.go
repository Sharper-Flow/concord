package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// projectToolingManifestPath is the per-project record of declared checks the
// refine-exit proof guard reads (CD-0192). The guard compares the bound
// worktree-verify run's argv against the manifest the Project declares on its
// default ref, never the working tree: a change branch must not declare its
// own passing check.
const projectToolingManifestPath = ".concord/tooling.v1.json"

// projectToolingRefusalBudget bounds the declared-check list one refusal
// renders, so a 64-tool manifest cannot push a failure message past the
// envelope bounds.
const projectToolingRefusalBudget = 900

// ProjectDeclaredTool is one declared check: the tool's stable id and the
// invocation the default ref declares, kept verbatim so a refusal can name
// exactly what the Project declared.
type ProjectDeclaredTool struct {
	ID         string
	Invocation string
}

// ProjectToolingManifest is the resolved tooling declaration the refine-exit
// proof guard compares the bound verify run against.
type ProjectToolingManifest struct {
	Project  string
	Declared []ProjectDeclaredTool
}

// ParseProjectToolingManifest reads the declared checks out of a
// .concord/tooling.v1.json document. It admits only a current manifest whose
// tools each carry a splittable invocation, because the guard's argv equality
// is exact: an invocation that splits to nothing declares no argv at all.
func ParseProjectToolingManifest(raw []byte) (*ProjectToolingManifest, error) {
	var document struct {
		SchemaVersion string `json:"schema_version"`
		Project       string `json:"project"`
		Tools         []struct {
			ID         string `json:"id"`
			Invocation string `json:"invocation"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest is not valid JSON: "+err.Error(), false, "repair "+projectToolingManifestPath+" on the default ref")
	}
	if document.SchemaVersion != "1.0" {
		return nil, newFailure(KindSchemaUnsupported, "project_tooling", fmt.Sprintf("tooling manifest schema_version must be \"1.0\", got %q", document.SchemaVersion), false, "declare schema_version \"1.0\" in "+projectToolingManifestPath)
	}
	if document.Project == "" {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest names no project", false, "declare the project identifier in "+projectToolingManifestPath)
	}
	if len(document.Tools) == 0 {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest declares no tools", false, "declare at least one tool in "+projectToolingManifestPath)
	}
	declared := make([]ProjectDeclaredTool, 0, len(document.Tools))
	for _, tool := range document.Tools {
		if tool.ID == "" {
			return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest declares a tool without an id", false, "declare every tool's stable id in "+projectToolingManifestPath)
		}
		if len(strings.Fields(tool.Invocation)) == 0 {
			return nil, newFailure(KindInvalidPayload, "project_tooling", fmt.Sprintf("tooling manifest tool %q declares no argv", tool.ID), false, "declare a splittable invocation in "+projectToolingManifestPath)
		}
		declared = append(declared, ProjectDeclaredTool{ID: tool.ID, Invocation: tool.Invocation})
	}
	return &ProjectToolingManifest{Project: document.Project, Declared: declared}, nil
}

// ProjectToolingInvocationDeclared reports the guard's exact argv equality:
// the declared invocation splits on whitespace into argv, and the lease
// command must equal one declared split element for element. The tooling
// schema and scripts/check-project-tooling.py forbid shell quoting and shell
// operators in an invocation, so the split is exact and no shell re-tokenizes
// the command.
func ProjectToolingInvocationDeclared(tooling *ProjectToolingManifest, command []string) bool {
	if tooling == nil {
		return false
	}
	for _, tool := range tooling.Declared {
		if slices.Equal(strings.Fields(tool.Invocation), command) {
			return true
		}
	}
	return false
}

// ProjectToolingDeclaredText renders the declared checks a refusal names, or
// the none-declared statement a Project without a manifest gets.
func ProjectToolingDeclaredText(tooling *ProjectToolingManifest) string {
	if tooling == nil || len(tooling.Declared) == 0 {
		return "the Project declares no checks in " + projectToolingManifestPath
	}
	parts := make([]string, 0, len(tooling.Declared))
	used := 0
	for _, tool := range tooling.Declared {
		part := tool.ID + " (" + tool.Invocation + ")"
		if used > 0 && used+len(part)+2 > projectToolingRefusalBudget {
			parts = append(parts, fmt.Sprintf("and %d more declared check(s)", len(tooling.Declared)-len(parts)))
			break
		}
		parts = append(parts, part)
		used += len(part) + 2
	}
	return "declared checks: " + strings.Join(parts, ", ")
}

// RefineProofManifestRequired reports whether a record_delivery action on the
// work item's pinned definition runs the refine-exit proof guard, so the
// caller must resolve the Project tooling manifest before the action's
// transaction opens. Every other action and definition resolves nothing.
func RefineProofManifestRequired(ctx context.Context, s *Store, workID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, newFailure(KindUnavailable, "project_tooling", "store is not open", false, "open the authority database")
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return false, err
	}
	var currentStep string
	if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		return false, wrapFailure(KindUnavailable, "project_tooling", "cannot read the workflow step", true, "retry once the workflow projection is readable", err)
	}
	return workflowRefineProofGateActive(entry.Definition, currentStep), nil
}

// ResolveWorkProjectTooling resolves the tooling manifest the work item's
// primary Project declares on its default ref. A nil manifest with a nil
// error reports that the Project declares none there: the work item has no
// primary Project, no registered repository, or no manifest at the ref. Git
// runs outside any store transaction; the caller resolves before the action's
// transaction opens (CD-0192, amending CD-0138 D3).
func ResolveWorkProjectTooling(ctx context.Context, s *Store, workID string) (*ProjectToolingManifest, error) {
	return resolveWorkProjectTooling(ctx, s, workID, ExecGitRunner{})
}

func resolveWorkProjectTooling(ctx context.Context, s *Store, workID string, runner GitRunner) (*ProjectToolingManifest, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "project_tooling", "store is not open", false, "open the authority database")
	}
	var projectID string
	err := s.db.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary' ORDER BY project_id LIMIT 1`, workID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "project_tooling", "cannot read the work item's Project", true, "retry once the database is readable", err)
	}
	var repoRoot string
	err = s.db.QueryRowContext(ctx, `SELECT normalized_value FROM project_locators WHERE kind=? AND project_id=? ORDER BY locator_id LIMIT 1`, LocatorCanonicalPath, projectID).Scan(&repoRoot)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "project_tooling", "cannot read the Project's repository locator", true, "retry once the database is readable", err)
	}
	defaultRef, err := worktreeAuditDefaultRef(ctx, runner, repoRoot, "")
	if err != nil {
		return nil, retitleFailure(err, "project_tooling")
	}
	// ls-tree separates "absent at the ref" (empty output, exit 0) from an
	// unreachable repository (nonzero exit), which cat-file -e cannot: a
	// missing path inside an existing rev exits 128 there.
	listing, err := runner.Run(ctx, repoRoot, "ls-tree", defaultRef, "--", projectToolingManifestPath)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "project_tooling", "cannot inspect "+projectToolingManifestPath+" on the default ref", false, "retry once the Project repository is reachable", err)
	}
	if len(strings.TrimSpace(string(listing))) == 0 {
		return nil, nil
	}
	raw, err := runner.Run(ctx, repoRoot, "show", defaultRef+":"+projectToolingManifestPath)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "project_tooling", "cannot read "+projectToolingManifestPath+" from the default ref", false, "retry once the Project repository is reachable", err)
	}
	return ParseProjectToolingManifest(raw)
}
