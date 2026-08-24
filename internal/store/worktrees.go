package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CD-0008 D1 worktree lifecycle. Worktrees and branches are native git
// resources bound to work; Concord records the durable claim, drives native
// creation through the GitRunner seam, verifies the result, and appends the
// verified locator as domain state. Possession grants no Product authority.

const (
	// worktreeSetPrefix derives the one optional worktree set per
	// implementation work item (CD-0008 D1: one canonical work_id, one
	// optional worktree_set_id).
	worktreeSetPrefix = "wts:"

	worktreeStatePending   = "pending"
	worktreeStateVerified  = "verified"
	worktreeStateReclaimed = "reclaimed"

	worktreeEntryActive    = "active"
	worktreeEntryReclaimed = "reclaimed"
)

var (
	worktreeBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	worktreeSHAPattern    = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// WorktreeEntry is the folded, verified locator state for one Project's
// implementation worktree in a work item's set.
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

type worktreeCreatedPayload struct {
	ExpectedVersion  int64           `json:"expected_version"`
	ResultingVersion int64           `json:"resulting_version"`
	SetID            string          `json:"set_id"`
	ProjectID        string          `json:"project_id"`
	ClaimOpID        string          `json:"claim_op_id"`
	Branch           string          `json:"branch"`
	BaseSHA          string          `json:"base_sha"`
	Path             string          `json:"path"`
	RepositoryID     string          `json:"repository_id"`
	GitFacts         json.RawMessage `json:"git_facts"`
}

type worktreeReclaimedPayload struct {
	ExpectedVersion  int64           `json:"expected_version"`
	ResultingVersion int64           `json:"resulting_version"`
	SetID            string          `json:"set_id"`
	ProjectID        string          `json:"project_id"`
	GitFacts         json.RawMessage `json:"git_facts"`
}

func foldWorktreeCreated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p worktreeCreatedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.SetID == "" || p.ProjectID == "" || p.ClaimOpID == "" || p.Branch == "" || p.BaseSHA == "" || p.Path == "" || p.RepositoryID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "worktree creation payload is missing required fields", false, "supply set, project, claim, branch, base, path, and repository identity")
	}
	if p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "worktree creation version must advance by exactly one", false, "supply expected and resulting versions one apart")
	}
	var state, existingClaim string
	if err := tx.QueryRowContext(ctx, `SELECT state,claim_op_id FROM worktree_entries WHERE set_id=? AND project_id=?`, p.SetID, p.ProjectID).Scan(&state, &existingClaim); err == nil {
		switch {
		case state == worktreeEntryActive && existingClaim == p.ClaimOpID:
			// The same claim appending twice is an idempotent replay.
			return nil
		case state == worktreeEntryReclaimed:
			// Reclamation freed the slot; the new verified claim replaces the
			// reclaimed row (CD-0008 D1: at most one ACTIVE per Project).
		default:
			// Anything else tries to establish a second active worktree for
			// one Project.
			return newFailure(KindProjectionConflict, "fold_event", "worktree set already holds an active worktree for this Project", false, "reclaim the existing worktree before claiming another")
		}
	} else if err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts) VALUES(?,?,?,?,?,?,?, 'active', ?, NULL, ?)
		ON CONFLICT(set_id, project_id) DO UPDATE SET claim_op_id=excluded.claim_op_id, branch=excluded.branch, base_sha=excluded.base_sha, path=excluded.path, repository_id=excluded.repository_id, state='active', verified_at=excluded.verified_at, reclaimed_at=NULL, git_facts=excluded.git_facts`,
		p.SetID, p.ProjectID, p.ClaimOpID, p.Branch, p.BaseSHA, p.Path, p.RepositoryID, event.OccurredAt.Format(time.RFC3339Nano), string(p.GitFacts)); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "work_items", event, p.ExpectedVersion, p.ResultingVersion, "work item")
}

func foldWorktreeReclaimed(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p worktreeReclaimedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.SetID == "" || p.ProjectID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "worktree reclamation payload is missing required fields", false, "supply set and project")
	}
	if p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "worktree reclamation version must advance by exactly one", false, "supply expected and resulting versions one apart")
	}
	res, err := tx.ExecContext(ctx, `UPDATE worktree_entries SET state='reclaimed', reclaimed_at=?, git_facts=? WHERE set_id=? AND project_id=? AND state='active'`,
		event.OccurredAt.Format(time.RFC3339Nano), string(p.GitFacts), p.SetID, p.ProjectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "no active worktree to reclaim for this Project", false, "claim a worktree before reclaiming it")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state=?, updated_at=? WHERE set_id=? AND project_id=? AND state=?`,
		worktreeStateReclaimed, event.OccurredAt.Format(time.RFC3339Nano), p.SetID, p.ProjectID, worktreeStateVerified); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "work_items", event, p.ExpectedVersion, p.ResultingVersion, "work item")
}

// WorktreeSetID derives the one optional worktree set id for a work item.
func WorktreeSetID(workID string) string { return worktreeSetPrefix + workID }

// WorktreeEntries lists folded entries for a work item's set.
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

// WorktreeClaimRequest drives the durable claim operation. The claim is
// atomic: the pinned intent row and the verified locator event commit together
// or not at all, and an interruption is reconciled by retrying with the same
// OpID.
type WorktreeClaimRequest struct {
	OpID      string
	WorkID    string
	ProjectID string
	Branch    string
	BaseSHA   string
	Path      string
	// RepoRoot is the repository to create from. When empty it is derived
	// from the Project's canonical_path locator.
	RepoRoot        string
	PrincipalRef    string
	RequestID       string
	ExpectedVersion int64
	Now             time.Time
	Runner          GitRunner
}

type WorktreeClaimResult struct {
	Entry      WorktreeEntry
	Reconciled bool
}

func (s *Store) ClaimWorktree(ctx context.Context, req WorktreeClaimRequest) (WorktreeClaimResult, error) {
	if s == nil || s.db == nil {
		return WorktreeClaimResult{}, newFailure(KindUnavailable, "worktree_claim", "store is not open", false, "open the authority database")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorktreeClaimResult{}, wrapFailure(KindUnavailable, "worktree_claim", "cannot begin claim", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	out, err := claimWorktreeRawTx(ctx, tx, req)
	if err != nil {
		return WorktreeClaimResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorktreeClaimResult{}, wrapFailure(KindUnavailable, "worktree_claim", "cannot commit claim", true, "retry the same operation with the same op id", err)
	}
	return out, nil
}

// ClaimWorktreeTx is the durable claim on an existing transaction, so the
// agent tool surface can compose it with its own idempotency envelope.
func ClaimWorktreeTx(ctx context.Context, transaction *Transaction, req WorktreeClaimRequest) (WorktreeClaimResult, error) {
	tx, err := transactionSQL(transaction, "worktree_claim")
	if err != nil {
		return WorktreeClaimResult{}, err
	}
	if req.Now.IsZero() {
		req.Now = transaction.now()
	}
	return claimWorktreeRawTx(ctx, tx, req)
}

func claimWorktreeRawTx(ctx context.Context, tx *sql.Tx, req WorktreeClaimRequest) (WorktreeClaimResult, error) {
	out := WorktreeClaimResult{}
	if req.OpID == "" || req.WorkID == "" || req.ProjectID == "" || req.PrincipalRef == "" || req.RequestID == "" {
		return out, newFailure(KindInvalidOperation, "worktree_claim", "claim operation is missing identity fields", false, "supply op, work, project, principal, and request ids")
	}
	if !worktreeBranchPattern.MatchString(req.Branch) {
		return out, newFailure(KindInvalidOperation, "worktree_claim", "branch is not a bounded git ref name", false, "supply a plain branch name without spaces or shell characters")
	}
	if !worktreeSHAPattern.MatchString(req.BaseSHA) {
		return out, newFailure(KindInvalidOperation, "worktree_claim", "base is not a full commit SHA", false, "pin the exact base commit SHA")
	}
	if !filepath.IsAbs(req.Path) {
		return out, newFailure(KindInvalidOperation, "worktree_claim", "worktree path must be absolute", false, "supply an absolute filesystem path")
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	setID := WorktreeSetID(req.WorkID)

	// Phase 1: the durable claim. An existing row for this OpID reconciles
	// with its pinned intent; the pinned values win over later arguments so
	// a retry can never redirect the operation.
	var state, pinnedBranch, pinnedBase, pinnedPath string
	err := tx.QueryRowContext(ctx, `SELECT state,pinned_branch,pinned_base_sha,pinned_path FROM worktree_claims WHERE op_id=?`, req.OpID).Scan(&state, &pinnedBranch, &pinnedBase, &pinnedPath)
	switch {
	case err == nil:
		if pinnedBranch != req.Branch || pinnedBase != req.BaseSHA || pinnedPath != req.Path {
			return out, newFailure(KindInvalidOperation, "worktree_claim", "retry does not match the pinned intent", false, "retry with the same op, branch, base, and path")
		}
		if state == worktreeStateVerified || state == worktreeStateReclaimed {
			// Idempotent replay: return the folded state without side effects.
			entry, readErr := worktreeEntryByClaim(ctx, tx, req.OpID)
			if readErr != nil {
				return out, readErr
			}
			if state == worktreeStateReclaimed && entry.State != worktreeEntryReclaimed {
				return out, newFailure(KindInvalidOperation, "worktree_claim", "claim is reclaimed but its entry is not", false, "contact_operator")
			}
			out.Entry = entry
			out.Reconciled = true
			return out, nil
		}
		out.Reconciled = true
	case err == sql.ErrNoRows:
		// A second active worktree for one Project is refused before git is
		// ever invoked (CD-0008 D1: at most one active per affected Project).
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_claims WHERE work_id=? AND project_id=? AND state IN ('pending','verified')`, req.WorkID, req.ProjectID).Scan(&active); err != nil {
			return out, wrapFailure(KindUnavailable, "worktree_claim", "cannot read active claims", true, "retry once the database is readable", err)
		}
		if active > 0 {
			return out, newFailure(KindProjectionConflict, "worktree_claim", "work already holds an active worktree for this Project", false, "reclaim the existing worktree before claiming another")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			req.OpID, req.WorkID, req.ProjectID, setID, req.Branch, req.BaseSHA, req.Path, worktreeStatePending, req.PrincipalRef, req.RequestID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return out, wrapFailure(KindUnavailable, "worktree_claim", "cannot persist claim", true, "retry once the database is writable", err)
		}
		pinnedBranch, pinnedBase, pinnedPath = req.Branch, req.BaseSHA, req.Path
	default:
		return out, wrapFailure(KindUnavailable, "worktree_claim", "cannot read claim", true, "retry once the database is readable", err)
	}

	repoRoot, resErr := worktreeRepoRootTx(ctx, tx, req)
	if resErr != nil {
		return out, resErr
	}

	// Phase 2: probe before creating or retrying. Git worktree creation is
	// not idempotent; the probe owns retry safety for an interrupted create.
	created, facts, probeErr := probeWorktree(ctx, runner, repoRoot, pinnedPath, pinnedBranch, pinnedBase)
	if probeErr != nil {
		return out, probeErr
	}
	if !created {
		if _, err := runner.Run(ctx, repoRoot, "worktree", "add", pinnedPath, "-b", pinnedBranch, pinnedBase); err != nil {
			return out, wrapFailure(KindGitUnreachable, "worktree_claim", "native worktree creation failed; the claim stays pending for reconciliation", true, "retry the same operation with the same op id", err)
		}
		var verifyErr error
		created, facts, verifyErr = probeWorktree(ctx, runner, repoRoot, pinnedPath, pinnedBranch, pinnedBase)
		if verifyErr != nil {
			return out, verifyErr
		}
		if !created {
			return out, newFailure(KindGitUnreachable, "worktree_claim", "created worktree did not verify against the pinned intent", false, "contact_operator")
		}
	}

	// Phase 3: append the verified locator as domain state and complete the
	// claim in the same transaction.
	payload, _ := json.Marshal(worktreeCreatedPayload{ExpectedVersion: req.ExpectedVersion, ResultingVersion: req.ExpectedVersion + 1, SetID: setID, ProjectID: req.ProjectID, ClaimOpID: req.OpID, Branch: pinnedBranch, BaseSHA: pinnedBase, Path: pinnedPath, RepositoryID: facts.repositoryID, GitFacts: facts.raw()})
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: fmt.Sprintf("%s:worktree-created", req.OpID), Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.PrincipalRef, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion}}, true, false); err != nil {
		return out, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state=?, updated_at=? WHERE op_id=?`, worktreeStateVerified, now.Format(time.RFC3339Nano), req.OpID); err != nil {
		return out, wrapFailure(KindUnavailable, "worktree_claim", "cannot complete claim", true, "retry the same operation with the same op id", err)
	}
	entry, err := worktreeEntryByClaim(ctx, tx, req.OpID)
	if err != nil {
		return out, err
	}
	out.Entry = entry
	return out, nil
}

// WorktreeReclaimRequest reclaims a worktree from git facts: the tree must be
// clean, the head merged into the default branch, and no native remove may be
// forced. A stale Concord projection never overrides stronger git truth.
type WorktreeReclaimRequest struct {
	WorkID          string
	ProjectID       string
	DefaultRef      string
	PrincipalRef    string
	RequestID       string
	ExpectedVersion int64
	Now             time.Time
	Runner          GitRunner
}

func (s *Store) ReclaimWorktree(ctx context.Context, req WorktreeReclaimRequest) (WorktreeEntry, error) {
	if s == nil || s.db == nil {
		return WorktreeEntry{}, newFailure(KindUnavailable, "worktree_reclaim", "store is not open", false, "open the authority database")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorktreeEntry{}, wrapFailure(KindUnavailable, "worktree_reclaim", "cannot begin reclaim", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	out, err := reclaimWorktreeRawTx(ctx, tx, req)
	if err != nil {
		return WorktreeEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorktreeEntry{}, wrapFailure(KindUnavailable, "worktree_reclaim", "cannot commit reclaim", true, "retry the same operation", err)
	}
	return out, nil
}

// ReclaimWorktreeTx derives reclamation from git facts on an existing
// transaction. The verified reclamation event lands inside the caller's
// transaction; the native remove follows it.
func ReclaimWorktreeTx(ctx context.Context, transaction *Transaction, req WorktreeReclaimRequest) (WorktreeEntry, error) {
	tx, err := transactionSQL(transaction, "worktree_reclaim")
	if err != nil {
		return WorktreeEntry{}, err
	}
	if req.Now.IsZero() {
		req.Now = transaction.now()
	}
	return reclaimWorktreeRawTx(ctx, tx, req)
}

func reclaimWorktreeRawTx(ctx context.Context, tx *sql.Tx, req WorktreeReclaimRequest) (WorktreeEntry, error) {
	var out WorktreeEntry
	if req.WorkID == "" || req.ProjectID == "" || req.PrincipalRef == "" || req.RequestID == "" {
		return out, newFailure(KindInvalidOperation, "worktree_reclaim", "reclaim operation is missing identity fields", false, "supply work, project, principal, and request ids")
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	setID := WorktreeSetID(req.WorkID)

	entries, err := worktreeEntriesTx(ctx, tx, req.WorkID)
	if err != nil {
		return out, err
	}
	var entry WorktreeEntry
	for _, candidate := range entries {
		if candidate.ProjectID == req.ProjectID {
			entry = candidate
			break
		}
	}
	if entry.ProjectID == "" || entry.State != worktreeEntryActive {
		return out, newFailure(KindProjectionNotFound, "worktree_reclaim", "no active worktree for this Project", false, "claim a worktree before reclaiming it")
	}

	repoRoot, resErr := worktreeRepoRootTx(ctx, tx, WorktreeClaimRequest{ProjectID: req.ProjectID})
	if resErr != nil {
		return out, resErr
	}

	// A stale projection reconciles against stronger git truth: if the native
	// worktree is already gone, reclamation records that fact instead of
	// demanding unreachable probes.
	if _, probeErr := runner.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "HEAD"); probeErr != nil {
		if err := appendReclaimedTx(ctx, tx, req, setID, now, jsonMustMarshal(map[string]any{"already_absent": true})); err != nil {
			return out, err
		}
		return worktreeEntryAfterReclaimTx(ctx, tx, req.WorkID, req.ProjectID)
	}

	// Derive git facts. Reclamation is refused unless the tree is clean and
	// the head is merged into the default branch. The verified reclamation
	// event lands before the native remove: a stale directory left by a
	// failed remove holds no authority, while a removed worktree with an
	// active projection would.
	statusOut, statusErr := runner.Run(ctx, entry.Path, "status", "--porcelain")
	if statusErr != nil {
		return out, wrapFailure(KindGitUnreachable, "worktree_reclaim", "cannot read worktree status", true, "retry once the worktree is reachable", statusErr)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		return out, newFailure(KindInvalidOperation, "worktree_reclaim", "worktree tree is dirty", false, "commit or discard the changes before reclaiming")
	}
	defaultRef := req.DefaultRef
	if defaultRef == "" {
		refOut, refErr := runner.Run(ctx, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD")
		if refErr != nil || strings.TrimSpace(string(refOut)) == "" {
			return out, newFailure(KindGitUnreachable, "worktree_reclaim", "cannot resolve the default branch", false, "set origin/HEAD or supply the merge target ref")
		}
		defaultRef = strings.TrimPrefix(strings.TrimSpace(string(refOut)), "refs/remotes/")
	}
	if _, err := runner.Run(ctx, repoRoot, "merge-base", "--is-ancestor", entry.Branch, defaultRef); err != nil {
		return out, newFailure(KindInvalidOperation, "worktree_reclaim", "worktree head is not merged into "+defaultRef, false, "merge the branch before reclaiming")
	}

	if err := appendReclaimedTx(ctx, tx, req, setID, now, jsonMustMarshal(map[string]any{"clean_tree": true, "head_reachable": true, "default_ref": defaultRef})); err != nil {
		return out, err
	}
	if _, err := runner.Run(ctx, repoRoot, "worktree", "remove", entry.Path); err != nil {
		return out, wrapFailure(KindGitUnreachable, "worktree_reclaim", "reclaimed in Concord but native removal failed", true, "remove the worktree manually; the projection already records reclamation", err)
	}
	return worktreeEntryAfterReclaimTx(ctx, tx, req.WorkID, req.ProjectID)
}

// worktreeRepoRootTx resolves the repository to create from: the explicit
// override or the Project's canonical_path locator. It reads through the
// claim's own transaction; the outer write lock makes a second connection's
// read deadlock on SQLite's single writer.
func worktreeRepoRootTx(ctx context.Context, tx *sql.Tx, req WorktreeClaimRequest) (string, error) {
	if req.RepoRoot != "" {
		return req.RepoRoot, nil
	}
	var normalized string
	err := tx.QueryRowContext(ctx, `SELECT normalized_value FROM project_locators WHERE kind=? AND project_id=? ORDER BY locator_id LIMIT 1`, LocatorCanonicalPath, req.ProjectID).Scan(&normalized)
	if err == sql.ErrNoRows {
		return "", newFailure(KindUnknownScope, "worktree_claim", "Project has no canonical_path locator", false, "register the repository's canonical path locator")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "worktree_claim", "cannot read Project locators", true, "retry once the database is readable", err)
	}
	return normalized, nil
}

func worktreeEntryByClaim(ctx context.Context, q queryer, opID string) (WorktreeEntry, error) {
	var e WorktreeEntry
	var facts string
	var reclaimed sql.NullString
	err := q.QueryRowContext(ctx, `SELECT set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,reclaimed_at,git_facts FROM worktree_entries WHERE claim_op_id=?`, opID).
		Scan(&e.SetID, &e.ProjectID, &e.ClaimOpID, &e.Branch, &e.BaseSHA, &e.Path, &e.RepositoryID, &e.State, &e.VerifiedAt, &reclaimed, &facts)
	e.ReclaimedAt = reclaimed.String
	if err == sql.ErrNoRows {
		return e, newFailure(KindProjectionNotFound, "worktree_claim", "verified claim has no folded entry", false, "contact_operator")
	}
	if err != nil {
		return e, wrapFailure(KindUnavailable, "worktree_claim", "cannot read worktree entry", true, "retry once the database is readable", err)
	}
	e.GitFacts = json.RawMessage(facts)
	return e, nil
}

type worktreeFacts struct {
	branch       string
	headSHA      string
	repositoryID string
}

func (f worktreeFacts) raw() json.RawMessage {
	payload, _ := json.Marshal(map[string]any{"branch": f.branch, "head_sha": f.headSHA})
	return payload
}

// probeWorktree reports whether path is a worktree of repoRoot on branch with
// base as an ancestor of its head. It fails closed on unreachable git.
func probeWorktree(ctx context.Context, runner GitRunner, repoRoot, path, branch, base string) (bool, worktreeFacts, error) {
	branchOut, branchErr := runner.Run(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
	if branchErr != nil {
		return false, worktreeFacts{}, nil
	}
	if strings.TrimSpace(string(branchOut)) != branch {
		return false, worktreeFacts{}, nil
	}
	headOut, headErr := runner.Run(ctx, path, "rev-parse", "HEAD")
	if headErr != nil {
		return false, worktreeFacts{}, nil
	}
	head := strings.TrimSpace(string(headOut))
	commonOut, commonErr := runner.Run(ctx, path, "rev-parse", "--git-common-dir")
	if commonErr != nil {
		return false, worktreeFacts{}, newFailure(KindGitUnreachable, "worktree_claim", "cannot determine worktree topology during verification", true, "retry once the worktree is reachable")
	}
	common := strings.TrimSpace(string(commonOut))
	if !filepath.IsAbs(common) {
		common = filepath.Join(path, common)
	}
	commonRoot := filepath.Dir(common)
	rootOut, rootErr := runner.Run(ctx, repoRoot, "rev-parse", "--show-toplevel")
	if rootErr != nil {
		return false, worktreeFacts{}, newFailure(KindGitUnreachable, "worktree_claim", "cannot resolve the repository root during verification", true, "retry once the repository is reachable")
	}
	root := strings.TrimSpace(string(rootOut))
	canonicalCommon, ccErr := normalizePath(commonRoot)
	canonicalRoot, crErr := normalizePath(root)
	if ccErr != nil || crErr != nil || canonicalCommon != canonicalRoot {
		return false, worktreeFacts{}, nil
	}
	if _, err := runner.Run(ctx, repoRoot, "merge-base", "--is-ancestor", base, head); err != nil {
		return false, worktreeFacts{}, nil
	}
	return true, worktreeFacts{branch: branch, headSHA: head, repositoryID: canonicalRoot}, nil
}

func jsonMustMarshal(v any) json.RawMessage {
	out, _ := json.Marshal(v)
	return out
}

func appendReclaimedTx(ctx context.Context, tx *sql.Tx, req WorktreeReclaimRequest, setID string, now time.Time, facts json.RawMessage) error {
	payload, _ := json.Marshal(worktreeReclaimedPayload{ExpectedVersion: req.ExpectedVersion, ResultingVersion: req.ExpectedVersion + 1, SetID: setID, ProjectID: req.ProjectID, GitFacts: facts})
	_, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{
		EventID: fmt.Sprintf("%s:%s:worktree-reclaimed", req.WorkID, req.ProjectID), Kind: "work.worktree_reclaimed", SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.PrincipalRef, OccurredAt: now, PayloadVersion: 1, Payload: payload,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion}}, true, false)
	return err
}

func worktreeEntryAfterReclaimTx(ctx context.Context, tx *sql.Tx, workID, projectID string) (WorktreeEntry, error) {
	entries, err := worktreeEntriesTx(ctx, tx, workID)
	if err != nil {
		return WorktreeEntry{}, err
	}
	for _, candidate := range entries {
		if candidate.ProjectID == projectID {
			return candidate, nil
		}
	}
	return WorktreeEntry{}, newFailure(KindUnavailable, "worktree_reclaim", "reclaimed entry is not readable", true, "retry the read")
}
