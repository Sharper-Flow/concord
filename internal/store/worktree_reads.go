package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	worktreeSetPrefix           = "wts:"
	worktreeStatePending        = "pending"
	worktreeStateVerified       = "verified"
	worktreeStateReclaimed      = "reclaimed"
	worktreeEntryActive         = "active"
	worktreeEntryReclaimed      = "reclaimed"
	WorktreeInspectModeStatus   = "status"
	WorktreeInspectModeDiff     = "diff"
	WorktreeInspectModeFile     = "file"
	defaultWorktreeInspectBytes = 16384
)

var worktreeSHAPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
var worktreeBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)

// WorktreeEntry is a retained locator read by verification, inspection and Review.
type WorktreeEntry struct {
	SetID        string          `json:"set_id"`
	ProjectID    string          `json:"project_id"`
	ClaimOpID    string          `json:"claim_op_id"`
	Branch       string          `json:"branch"`
	BaseSHA      string          `json:"base_sha"`
	Path         string          `json:"path"`
	RepositoryID string          `json:"repository_id"`
	State        string          `json:"state"`
	VerifiedAt   string          `json:"verified_at"`
	ReclaimedAt  string          `json:"reclaimed_at,omitempty"`
	GitFacts     json.RawMessage `json:"git_facts"`
}

func WorktreeSetID(workID string) string { return worktreeSetPrefix + workID }

func (s *Store) WorktreeEntries(ctx context.Context, workID string) ([]WorktreeEntry, error) {
	return worktreeEntriesCore(ctx, s.db, workID)
}

func worktreeEntriesCore(ctx context.Context, q queryer, workID string) ([]WorktreeEntry, error) {
	rows, err := q.QueryContext(ctx, `SELECT set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts FROM worktree_entries WHERE set_id=? ORDER BY project_id`, WorktreeSetID(workID))
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_entries", "cannot read worktree entries", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []WorktreeEntry
	for rows.Next() {
		var e WorktreeEntry
		var facts string
		var reclaimed sql.NullString
		if err := rows.Scan(&e.SetID, &e.ProjectID, &e.ClaimOpID, &e.Branch, &e.BaseSHA, &e.Path, &e.RepositoryID, &e.State, &e.VerifiedAt, &reclaimed, &facts); err != nil {
			return nil, err
		}
		e.ReclaimedAt = reclaimed.String
		e.GitFacts = json.RawMessage(facts)
		out = append(out, e)
	}
	return out, rows.Err()
}

func worktreeEntriesTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorktreeEntry, error) {
	return worktreeEntriesCore(ctx, tx, workID)
}

func activeWorktreeEntryForProject(ctx context.Context, q queryer, op, workID, projectID string) (WorktreeEntry, error) {
	if workID == "" || projectID == "" {
		return WorktreeEntry{}, newFailure(KindUnknownScope, op, "worktree tier access requires the work item and the session's Project", false, "supply the work identity and run from a registered Project")
	}
	entries, err := worktreeEntriesCore(ctx, q, workID)
	if err != nil {
		return WorktreeEntry{}, err
	}
	for _, candidate := range entries {
		if candidate.ProjectID == projectID && candidate.State == worktreeEntryActive {
			return candidate, nil
		}
	}
	return WorktreeEntry{}, newFailure(KindProjectionNotFound, op, "work item holds no active worktree in Project "+projectID, false, "reconcile the recorded worktree locator before tiered access")
}

func probeWorktreeReachable(ctx context.Context, runner GitRunner, op string, entry WorktreeEntry) error {
	branchOut, err := runner.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return wrapFailure(KindGitUnreachable, op, "worktree at "+entry.Path+" is not reachable on disk", true, "restore the host worktree before tiered access", err)
	}
	if strings.TrimSpace(string(branchOut)) != entry.Branch {
		return newFailure(KindProjectionConflict, op, "worktree at "+entry.Path+" is on branch "+strings.TrimSpace(string(branchOut))+", the stored claim says "+entry.Branch, false, "reconcile the worktree locator before tiered access")
	}
	return nil
}

type WorktreeInspectRequest struct {
	WorkID    string
	ProjectID string
	Mode      string
	RunRef    string
	Stream    string
	Offset    int64
	Length    int64
	Path      string
	Runner    GitRunner
	MaxBytes  int
}

type WorktreeInspectResult struct {
	OracleOutput *NativeOracleOutputPage `json:"oracle_output,omitempty"`
	WorkID       string                  `json:"work_id"`
	ProjectID    string                  `json:"project_id"`
	Branch       string                  `json:"branch"`
	Path         string                  `json:"path"`
	Mode         string                  `json:"mode"`
	Content      string                  `json:"content"`
	Truncated    bool                    `json:"truncated"`
}

func (s *Store) InspectWorktree(ctx context.Context, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	if s == nil || s.db == nil {
		return WorktreeInspectResult{}, newFailure(KindUnavailable, "worktree_inspect", "store is not open", false, "open the authority database")
	}
	if req.Mode == "oracle_output" {
		return s.readNativeVerifyOutput(ctx, req)
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultWorktreeInspectBytes
	}
	switch req.Mode {
	case WorktreeInspectModeStatus, WorktreeInspectModeDiff:
		if req.Path != "" {
			return WorktreeInspectResult{}, newFailure(KindInvalidOperation, "worktree_inspect", "path applies to file mode only", false, "omit the path or select file mode")
		}
	case WorktreeInspectModeFile:
		if err := safeWorktreeRelativePath(req.Path); err != nil {
			return WorktreeInspectResult{}, err
		}
	default:
		return WorktreeInspectResult{}, newFailure(KindInvalidOperation, "worktree_inspect", "mode must be status, diff, or file", false, "select one inspection mode")
	}
	if err := publishedWorkAbsenceInScopeQ(ctx, s.db, req.WorkID, "", req.ProjectID, "worktree_inspect"); err != nil {
		return WorktreeInspectResult{}, err
	}
	entry, err := activeWorktreeEntryForProject(ctx, s.db, "worktree_inspect", req.WorkID, req.ProjectID)
	if err != nil {
		return WorktreeInspectResult{}, err
	}
	if err := probeWorktreeReachable(ctx, runner, "worktree_inspect", entry); err != nil {
		return WorktreeInspectResult{}, err
	}
	result := WorktreeInspectResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Branch: entry.Branch, Path: entry.Path, Mode: req.Mode}
	switch req.Mode {
	case WorktreeInspectModeStatus:
		out, runErr := runner.Run(ctx, entry.Path, "status", "--porcelain")
		if runErr != nil {
			return WorktreeInspectResult{}, wrapFailure(KindGitUnreachable, "worktree_inspect", "cannot read worktree status", true, "retry once the worktree is reachable", runErr)
		}
		result.Content, result.Truncated = boundText(string(out), maxBytes)
	case WorktreeInspectModeDiff:
		out, runErr := runner.Run(ctx, entry.Path, "diff", "HEAD")
		if runErr != nil {
			return WorktreeInspectResult{}, wrapFailure(KindGitUnreachable, "worktree_inspect", "cannot read the worktree diff", true, "retry once the worktree is reachable", runErr)
		}
		result.Content, result.Truncated = boundText(string(out), maxBytes)
	case WorktreeInspectModeFile:
		content, readErr := readBoundedFile(filepath.Join(entry.Path, filepath.FromSlash(req.Path)), maxBytes)
		if readErr != nil {
			return WorktreeInspectResult{}, readErr
		}
		result.Content, result.Truncated = boundText(content, maxBytes)
	}
	return result, nil
}

func safeWorktreeRelativePath(rel string) error {
	if rel == "" || len(rel) > 512 || strings.ContainsRune(rel, 0) {
		return newFailure(KindInvalidOperation, "worktree_inspect", "file selector must be a bounded relative path", false, "supply one relative path inside the worktree")
	}
	if filepath.IsAbs(rel) || rel != filepath.Clean(filepath.FromSlash(rel)) {
		return newFailure(KindInvalidOperation, "worktree_inspect", "file selector must be a clean relative path", false, "supply one relative path without absolute or parent segments")
	}
	for _, element := range strings.Split(filepath.ToSlash(rel), "/") {
		if element == ".." {
			return newFailure(KindInvalidOperation, "worktree_inspect", "file selector must not traverse outside the worktree", false, "supply one relative path without parent segments")
		}
	}
	return nil
}

func boundText(text string, maxBytes int) (string, bool) {
	if len(text) > maxBytes {
		return text[:maxBytes], true
	}
	return text, false
}

func readBoundedFile(path string, maxBytes int) (string, error) {
	file, err := os.Open(path) //nolint:gosec // derived worktree joined to a validated relative selector.
	if err != nil {
		if os.IsNotExist(err) {
			return "", newFailure(KindProjectionNotFound, "worktree_inspect", "file selector does not exist in the worktree", false, "inspect status first and select an existing file")
		}
		return "", wrapFailure(KindUnavailable, "worktree_inspect", "cannot read the selected file", true, "retry once the worktree is readable", err)
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, maxBytes+1)
	read, err := io.ReadFull(file, buffer)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", wrapFailure(KindUnavailable, "worktree_inspect", "cannot read the selected file", true, "retry once the worktree is readable", err)
	}
	return string(buffer[:read]), nil
}

func jsonMustMarshal(v any) json.RawMessage { out, _ := json.Marshal(v); return out }
