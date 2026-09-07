package main

import (
	"context"
	"io"
	"os"

	"github.com/sharper-flow/concord/internal/store"
)

type workResumeInput struct {
	ProductID string `json:"product_id"`
	ProjectID string `json:"project_id"`
	WorkID    string `json:"work_id"`
}

type workResumeOutput struct {
	SchemaVersion string            `json:"schema_version"`
	ProductID     string            `json:"product_id"`
	ProjectID     string            `json:"project_id"`
	WorkID        string            `json:"work_id"`
	Worktree      workBootstrapTree `json:"worktree"`
}

// runWorkResume resolves the worktree a session enters when it resumes an
// existing work item by work identity (issue #891). It applies the same
// origin gate work-bootstrap applies to a capture — the default checkout, or
// a linked worktree ValidateBootstrapOrigin admits — and then reads the
// item's active entry for the resolved Project. It records nothing: the
// session's worktree is the directory it runs in, and the host owns that
// fact (CD-0104 D1).
func runWorkResume(raw []byte, s *store.Store, out, errOut io.Writer) int {
	var input workResumeInput
	if err := decodeObject(raw, &input); err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", err.Error())
		return 1
	}
	if !sessionPrepareID.MatchString(input.ProductID) || !sessionPrepareID.MatchString(input.ProjectID) || !sessionPrepareID.MatchString(input.WorkID) {
		writeOperatorDiagnostic(errOut, "work-resume", "product_id, project_id, and work_id are required")
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", "cannot read invocation directory")
		return 1
	}
	ctx := context.Background()
	resolution, err := s.ResolveProject(ctx, cwd, cwd)
	if err != nil || resolution.ProjectID != input.ProjectID {
		writeOperatorDiagnostic(errOut, "work-resume", "invocation must resolve to the requested Project")
		return 1
	}
	if !resolution.MainWorktree {
		if _, err := s.ValidateBootstrapOrigin(ctx, input.ProjectID, resolution.Repository.WorktreePath, store.ExecGitRunner{}); err != nil {
			writeOperatorDiagnostic(errOut, "work-resume", err.Error())
			return 1
		}
	}
	entry, err := s.ResumeWorktreeLocation(ctx, input.ProductID, input.ProjectID, input.WorkID)
	if err != nil {
		writeOperatorDiagnostic(errOut, "work-resume", err.Error())
		return 1
	}
	return writeJSON(out, workResumeOutput{
		SchemaVersion: "1.0", ProductID: input.ProductID, ProjectID: input.ProjectID, WorkID: input.WorkID,
		Worktree: workBootstrapTree{SetID: entry.SetID, Branch: entry.Branch, BaseSHA: entry.BaseSHA, Path: entry.Path, State: entry.State},
	}, errOut)
}
