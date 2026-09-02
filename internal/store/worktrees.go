package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
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
	if err := branchIsMergedInto(ctx, runner, repoRoot, entry.Branch, defaultRef); err != nil {
		return out, err
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

// ValidateWorktreeClaimIntent is the exported form of the claim's own intent
// validation, for callers that derive a claim's inputs and must know the
// claim will accept them before attempting it. The store owns the validation;
// it never owns the derivation (issue #316: the claim verifies intent, it
// does not author it).
func ValidateWorktreeClaimIntent(branch, baseSHA, path string) error {
	if !worktreeBranchPattern.MatchString(branch) {
		return newFailure(KindInvalidOperation, "worktree_claim", "branch is not a bounded git ref name", false, "supply a plain branch name without spaces or shell characters")
	}
	if !worktreeSHAPattern.MatchString(baseSHA) {
		return newFailure(KindInvalidOperation, "worktree_claim", "base is not a full commit SHA", false, "pin the exact base commit SHA")
	}
	if !filepath.IsAbs(path) {
		return newFailure(KindInvalidOperation, "worktree_claim", "worktree path must be absolute", false, "supply an absolute filesystem path")
	}
	return nil
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

// branchIsMergedInto reports whether branch is already contained in defaultRef,
// answering by tree identity rather than commit reachability.
//
// Commit reachability is the wrong question under a squash merge. Squashing
// rewrites a branch's commits into one new commit on the default branch, so the
// original branch tip never becomes an ancestor of it. A repository that permits
// squash merge only — as GitHub's merge queue commonly enforces — therefore makes
// `merge-base --is-ancestor` refuse every branch that actually merged.
//
// Merging a contained branch adds nothing, so the merged tree equals the default
// ref's own tree. That equality holds for squash, rebase, and fast-forward alike,
// because all three land the same content.
func branchIsMergedInto(ctx context.Context, runner GitRunner, repoRoot, branch, defaultRef string) error {
	mergedTree, mergeErr := runner.Run(ctx, repoRoot, "merge-tree", "--write-tree", defaultRef, branch)
	if mergeErr != nil {
		// A non-zero exit means the merge conflicts, so the branch carries
		// content the default ref does not hold. It is not merged.
		return newFailure(KindInvalidOperation, "worktree_reclaim", "worktree branch does not merge cleanly into "+defaultRef, false, "merge the branch before reclaiming")
	}
	defaultTree, treeErr := runner.Run(ctx, repoRoot, "rev-parse", defaultRef+"^{tree}")
	if treeErr != nil {
		return newFailure(KindGitUnreachable, "worktree_reclaim", "cannot resolve the tree of "+defaultRef, false, "retry once the repository is reachable")
	}
	if firstLine(mergedTree) != firstLine(defaultTree) {
		return newFailure(KindInvalidOperation, "worktree_reclaim", "worktree head is not merged into "+defaultRef, false, "merge the branch before reclaiming")
	}
	return nil
}

// firstLine returns the first line of git output with surrounding space removed.
// `merge-tree --write-tree` prints the tree object id on its own first line and
// may print more after it.
func firstLine(out []byte) string {
	text := strings.TrimSpace(string(out))
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return strings.TrimSpace(text[:index])
	}
	return text
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

// Worktree drift classes (issue #675). Each class names one divergence
// between the filesystem under the Concord worktree root, the durable claim
// rows, and the folded work lifecycle.
const (
	// WorktreeDriftOrphan: a directory exists under the worktree root with no
	// active claim pinned to it and no active entry at its path.
	WorktreeDriftOrphan = "orphan"
	// WorktreeDriftStaleClaim: a verified claim's pinned worktree no longer
	// exists on disk.
	WorktreeDriftStaleClaim = "stale_claim"
	// WorktreeDriftStrandedNeeded: a work item at needed holds an active
	// worktree entry whose path no longer exists, so no driver will notice.
	WorktreeDriftStrandedNeeded = "stranded_needed"
)

// Typed recovery actions. Where a Concord operation owns the recovery, the
// action names that operation; orphan removal has no typed owner and names
// the host action.
const (
	WorktreeRecoveryRemoveOrphan = "remove_worktree"
	WorktreeRecoveryReclaim      = "worktree_reclaim"
	WorktreeRecoveryClaim        = "worktree_claim"
)

// worktreeIDNamePattern mirrors the agent surface's shared id definition
// (contracts/agent-tool-surface-payloads.schema.json $defs/id). An orphan
// directory whose name cannot be a work id reports no work_id rather than a
// value the surface would refuse.
var worktreeIDNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// WorktreeDrift is one classified divergence with its typed recovery action.
type WorktreeDrift struct {
	Class          string `json:"class"`
	ProjectID      string `json:"project_id"`
	WorkID         string `json:"work_id,omitempty"`
	Path           string `json:"path"`
	ClaimState     string `json:"claim_state,omitempty"`
	Lifecycle      string `json:"lifecycle,omitempty"`
	RecoveryAction string `json:"recovery_action"`
}

// WorktreeAudit is the bounded result of one audit pass.
type WorktreeAudit struct {
	Root  string          `json:"root"`
	Drift []WorktreeDrift `json:"drift"`
}

// WorktreeAudit enumerates on-disk worktrees under the Concord worktree root
// (the database directory's worktrees/<project_id>/<work_id> convention owned
// by LocateWorktree) against active claims, folded entries, and work
// lifecycle, classifying every divergence (issue #675). It is a pure read:
// it names the typed recovery action for each drift row and repairs nothing.
//
// A pending claim is intent, not verified fact — its worktree may simply not
// be created yet, and retrying the claim reconciles it — so only verified
// claims and active entries are audited. A stranded_needed row co-occurs with
// its stale_claim row by design: the claim and the work item are different
// subjects, and recovering the work requires both actions in order.
//
// Output is bounded by limit and ordered deterministically (class, project,
// path), so a truncated pass is stable for the caller.
func (s *Store) WorktreeAudit(ctx context.Context, productID string, limit int) (WorktreeAudit, error) {
	if s == nil || s.db == nil {
		return WorktreeAudit{}, newFailure(KindUnavailable, "worktree_audit", "store is not open", false, "open the authority database")
	}
	return worktreeAudit(ctx, s.db, filepath.Join(filepath.Dir(s.Path()), "worktrees"), productID, limit)
}

func worktreeAudit(ctx context.Context, q queryer, root string, productID string, limit int) (WorktreeAudit, error) {
	if productID == "" {
		return WorktreeAudit{}, newFailure(KindUnknownScope, "worktree_audit", "worktree audit requires one Product scope", false, "select one Product before auditing worktrees")
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	var projects []string
	projectRows, err := q.QueryContext(ctx, `SELECT project_id FROM product_projects WHERE product_id=? ORDER BY project_id`, productID)
	if err != nil {
		return WorktreeAudit{}, wrapFailure(KindUnavailable, "worktree_audit", "cannot read Product Projects", true, "retry once the database is readable", err)
	}
	defer projectRows.Close()
	for projectRows.Next() {
		var id string
		if err := projectRows.Scan(&id); err != nil {
			return WorktreeAudit{}, err
		}
		projects = append(projects, id)
	}
	if err := projectRows.Err(); err != nil {
		return WorktreeAudit{}, err
	}

	type auditClaim struct {
		workID, projectID, path, state string
	}
	claims := []auditClaim{}
	claimRows, err := q.QueryContext(ctx, `SELECT c.work_id,c.project_id,c.pinned_path,c.state FROM worktree_claims c JOIN product_projects pp ON pp.project_id=c.project_id WHERE pp.product_id=? AND c.state IN ('pending','verified') ORDER BY c.pinned_path`, productID)
	if err != nil {
		return WorktreeAudit{}, wrapFailure(KindUnavailable, "worktree_audit", "cannot read worktree claims", true, "retry once the database is readable", err)
	}
	defer claimRows.Close()
	for claimRows.Next() {
		var c auditClaim
		if err := claimRows.Scan(&c.workID, &c.projectID, &c.path, &c.state); err != nil {
			return WorktreeAudit{}, err
		}
		claims = append(claims, c)
	}
	if err := claimRows.Err(); err != nil {
		return WorktreeAudit{}, err
	}

	type auditEntry struct {
		workID, projectID, path string
	}
	entries := []auditEntry{}
	entryRows, err := q.QueryContext(ctx, `SELECT e.set_id,e.project_id,e.path FROM worktree_entries e JOIN product_projects pp ON pp.project_id=e.project_id WHERE pp.product_id=? AND e.state='active' ORDER BY e.path`, productID)
	if err != nil {
		return WorktreeAudit{}, wrapFailure(KindUnavailable, "worktree_audit", "cannot read worktree entries", true, "retry once the database is readable", err)
	}
	defer entryRows.Close()
	for entryRows.Next() {
		var setID string
		var e auditEntry
		if err := entryRows.Scan(&setID, &e.projectID, &e.path); err != nil {
			return WorktreeAudit{}, err
		}
		e.workID = strings.TrimPrefix(setID, worktreeSetPrefix)
		entries = append(entries, e)
	}
	if err := entryRows.Err(); err != nil {
		return WorktreeAudit{}, err
	}

	// Only needed-work ids with an active entry in this Product matter for the
	// stranded class; the join answers exactly that set.
	strandedIDs := map[string]bool{}
	strandedRows, err := q.QueryContext(ctx, `SELECT w.id FROM work_items w JOIN worktree_entries e ON e.set_id=?||w.id JOIN product_projects pp ON pp.project_id=e.project_id WHERE pp.product_id=? AND e.state='active' AND w.lifecycle='needed'`, worktreeSetPrefix, productID)
	if err != nil {
		return WorktreeAudit{}, wrapFailure(KindUnavailable, "worktree_audit", "cannot read work lifecycle", true, "retry once the database is readable", err)
	}
	defer strandedRows.Close()
	for strandedRows.Next() {
		var id string
		if err := strandedRows.Scan(&id); err != nil {
			return WorktreeAudit{}, err
		}
		strandedIDs[id] = true
	}
	if err := strandedRows.Err(); err != nil {
		return WorktreeAudit{}, err
	}

	claimedPaths := map[string]bool{}
	for _, c := range claims {
		claimedPaths[c.path] = true
	}
	enteredPaths := map[string]bool{}
	for _, e := range entries {
		enteredPaths[e.path] = true
	}

	exists := func(path string) (bool, error) {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, wrapFailure(KindUnavailable, "worktree_audit", "cannot inspect worktree path "+path, true, "retry once the worktree root is readable", err)
		}
		return true, nil
	}

	drift := []WorktreeDrift{}
	// Orphan: on disk, claimed by nobody. Classes are appended in a fixed
	// order over sorted inputs, so the result needs no explicit sort.
	for _, projectID := range projects {
		projectDir := filepath.Join(root, projectID)
		dirs, err := os.ReadDir(projectDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return WorktreeAudit{}, wrapFailure(KindUnavailable, "worktree_audit", "cannot enumerate worktrees under "+projectDir, true, "retry once the worktree root is readable", err)
		}
		for _, dir := range dirs {
			if !dir.IsDir() {
				continue
			}
			path := filepath.Join(projectDir, dir.Name())
			if claimedPaths[path] || enteredPaths[path] {
				continue
			}
			row := WorktreeDrift{Class: WorktreeDriftOrphan, ProjectID: projectID, Path: path, RecoveryAction: WorktreeRecoveryRemoveOrphan}
			if worktreeIDNamePattern.MatchString(dir.Name()) {
				row.WorkID = dir.Name()
			}
			drift = append(drift, row)
		}
	}
	// Stale claim: the verified locator points at a path the disk no longer
	// holds. ReclaimWorktree reconciles exactly this shape (already_absent).
	for _, c := range claims {
		if c.state != worktreeStateVerified {
			continue
		}
		present, err := exists(c.path)
		if err != nil {
			return WorktreeAudit{}, err
		}
		if present {
			continue
		}
		drift = append(drift, WorktreeDrift{Class: WorktreeDriftStaleClaim, ProjectID: c.projectID, WorkID: c.workID, Path: c.path, ClaimState: worktreeStateVerified, RecoveryAction: WorktreeRecoveryReclaim})
	}
	// Stranded needed work: the entry still says active, the disk disagrees,
	// and nothing is driving the work item to notice.
	for _, e := range entries {
		if !strandedIDs[e.workID] {
			continue
		}
		present, err := exists(e.path)
		if err != nil {
			return WorktreeAudit{}, err
		}
		if present {
			continue
		}
		drift = append(drift, WorktreeDrift{Class: WorktreeDriftStrandedNeeded, ProjectID: e.projectID, WorkID: e.workID, Path: e.path, Lifecycle: "needed", RecoveryAction: WorktreeRecoveryClaim})
	}
	if len(drift) > limit {
		drift = drift[:limit]
	}
	return WorktreeAudit{Root: root, Drift: drift}, nil
}

// Session worktree retargeting (CD-0096). The effective target is the tree
// later Concord operations resolve through; it is distinct from the host
// process directory, which never changes (CD-0096 D1). The target derives
// from registered Project and work identity only — no operation in this
// section accepts a worktree path from a caller (CD-0096 D2).

// SessionWorktreeOwner identifies the agent session an effective target
// belongs to. The tuple matches the authority grant identity; principal_ref
// is derived from the registered client (CD-0080 D1) and needs no separate
// column for ownership.
type SessionWorktreeOwner struct {
	ClientRef  string `json:"client_ref"`
	AgentRef   string `json:"agent_ref"`
	SessionRef string `json:"session_ref"`
}

// SessionWorktreeTarget is the durable effective-target binding of one
// session. TargetVersion is the optimistic-concurrency pin: every retarget
// must name the version it expects, and a mismatch fails closed because a
// silently re-derived target is a directory change no one authorized
// (CD-0096 D5).
type SessionWorktreeTarget struct {
	SessionWorktreeOwner
	WorkID        string `json:"work_id"`
	ProjectID     string `json:"project_id"`
	Branch        string `json:"branch"`
	Path          string `json:"path"`
	State         string `json:"state"`
	TargetVersion int64  `json:"target_version"`
	PrincipalRef  string `json:"principal_ref"`
	ClaimedAt     string `json:"claimed_at"`
	UpdatedAt     string `json:"updated_at"`
}

const (
	sessionTargetActive   = "active"
	sessionTargetReleased = "released"
)

// WorktreeRetargetRequest drives the in-session retarget. WorktreeRoot is the
// Concord worktree root (the parent worktree-locate policy derives from);
// callers read it from the store path, the request carries it so the
// tx-scoped core never touches the store handle (single-connection
// invariant).
type WorktreeRetargetRequest struct {
	Owner  SessionWorktreeOwner
	WorkID string
	// ExpectedWorkVersion is the work item's version pin, consumed only when
	// the retarget must create the canonical worktree (the claim bumps the
	// work version). Adopting an existing verified worktree does not bump it.
	ExpectedWorkVersion int64
	// ExpectedTargetVersion is the session's target pin. Zero means the
	// session holds no target yet; any other value must match the stored
	// target_version exactly, or the retarget fails closed.
	ExpectedTargetVersion int64
	WorktreeRoot          string
	PrincipalRef          string
	RequestID             string
	OpID                  string
	Now                   time.Time
	Runner                GitRunner
}

// RetargetSessionWorktree adopts or creates the current work item's canonical
// worktree and records it as the session's persistent effective target. The
// write owns its transaction.
func (s *Store) RetargetSessionWorktree(ctx context.Context, req WorktreeRetargetRequest) (SessionWorktreeTarget, error) {
	if s == nil || s.db == nil {
		return SessionWorktreeTarget{}, newFailure(KindUnavailable, "worktree_retarget", "store is not open", false, "open the authority database")
	}
	if req.WorktreeRoot == "" {
		req.WorktreeRoot = filepath.Join(filepath.Dir(s.Path()), "worktrees")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionWorktreeTarget{}, wrapFailure(KindUnavailable, "worktree_retarget", "cannot begin retarget", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	transaction := &Transaction{tx: tx, clock: s.Clock}
	out, err := RetargetSessionWorktreeTx(ctx, transaction, req)
	if err != nil {
		return SessionWorktreeTarget{}, err
	}
	if err := tx.Commit(); err != nil {
		return SessionWorktreeTarget{}, wrapFailure(KindUnavailable, "worktree_retarget", "cannot commit retarget", true, "retry the same operation with the same op id", err)
	}
	return out, nil
}

// RetargetSessionWorktreeTx is the retarget on an existing transaction, so
// the agent tool surface can compose it with its idempotency envelope.
func RetargetSessionWorktreeTx(ctx context.Context, transaction *Transaction, req WorktreeRetargetRequest) (SessionWorktreeTarget, error) {
	tx, err := transactionSQL(transaction, "worktree_retarget")
	if err != nil {
		return SessionWorktreeTarget{}, err
	}
	if req.Now.IsZero() {
		req.Now = transaction.now()
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	return retargetSessionWorktreeRawTx(ctx, transaction, tx, req, runner)
}

func retargetSessionWorktreeRawTx(ctx context.Context, transaction *Transaction, tx *sql.Tx, req WorktreeRetargetRequest, runner GitRunner) (SessionWorktreeTarget, error) {
	if req.OpID == "" || req.WorkID == "" || req.PrincipalRef == "" || req.RequestID == "" || req.WorktreeRoot == "" {
		return SessionWorktreeTarget{}, newFailure(KindInvalidOperation, "worktree_retarget", "retarget operation is missing identity fields", false, "supply op, work, principal, and request ids")
	}
	if err := validateSessionWorktreeOwner(req.Owner); err != nil {
		return SessionWorktreeTarget{}, err
	}
	if req.ExpectedTargetVersion < 0 {
		return SessionWorktreeTarget{}, newFailure(KindInvalidOperation, "worktree_retarget", "expected target version cannot be negative", false, "supply the session's current target version or zero")
	}

	// Active work only: a terminal work item holds no live implementation
	// surface, and its worktree is the reclaim route's subject, not a target.
	var lifecycle string
	err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.WorkID).Scan(&lifecycle)
	if err == sql.ErrNoRows {
		return SessionWorktreeTarget{}, newFailure(KindUnknownScope, "worktree_retarget", "work item does not exist", false, "select one existing work item")
	}
	if err != nil {
		return SessionWorktreeTarget{}, wrapFailure(KindUnavailable, "worktree_retarget", "cannot read the work item", true, "retry once the database is readable", err)
	}
	if lifecycle != "needed" && lifecycle != "in_progress" {
		return SessionWorktreeTarget{}, newFailure(KindInvalidTransition, "worktree_retarget", "work item is "+lifecycle+", so it holds no retargetable worktree", false, "retarget active work, or reclaim a terminal work item's worktree")
	}

	existing, found, err := sessionWorktreeTargetRowTx(ctx, tx, req.Owner)
	if err != nil {
		return SessionWorktreeTarget{}, err
	}
	if found {
		if req.ExpectedTargetVersion != existing.TargetVersion {
			return SessionWorktreeTarget{}, newFailure(KindVersionConflict, "worktree_retarget",
				fmt.Sprintf("session target version is %d, request pinned %d", existing.TargetVersion, req.ExpectedTargetVersion), false, "reread the session target and retry with its current version")
		}
		if existing.State == sessionTargetActive && existing.WorkID != req.WorkID {
			return SessionWorktreeTarget{}, newFailure(KindWorktreeOwnershipConflict, "worktree_retarget",
				fmt.Sprintf("session %s is bound to work %s; adopting work %s is a takeover (CD-0096 D3)", sessionOwnerLabel(req.Owner), existing.WorkID, req.WorkID), false, "release the current binding or obtain an operator takeover override")
		}
	} else if req.ExpectedTargetVersion != 0 {
		return SessionWorktreeTarget{}, newFailure(KindVersionConflict, "worktree_retarget",
			fmt.Sprintf("session holds no target but the request pinned version %d", req.ExpectedTargetVersion), false, "retry with expected_target_version 0 for a first binding")
	}

	// One active holder per work item: the in-session route and the CD-0088
	// bootstrap route converge on one owner (CD-0096 D6).
	holder, held, err := sessionWorktreeHolderTx(ctx, tx, req.WorkID, req.Owner)
	if err != nil {
		return SessionWorktreeTarget{}, err
	}
	if held {
		return SessionWorktreeTarget{}, newFailure(KindWorktreeOwnershipConflict, "worktree_retarget",
			fmt.Sprintf("worktree of work %s is held by session %s; adopting it is a takeover (CD-0096 D3)", req.WorkID, sessionOwnerLabel(holder)), false, "contact_operator")
	}
	launchSession, launchActive, err := activeBootstrapLaunchTx(ctx, tx, req.WorkID)
	if err != nil {
		return SessionWorktreeTarget{}, err
	}
	if launchActive && launchSession != req.Owner.SessionRef {
		return SessionWorktreeTarget{}, newFailure(KindWorktreeOwnershipConflict, "worktree_retarget",
			fmt.Sprintf("worktree of work %s is held by the launched session %s; adopting it is a takeover (CD-0096 D3)", req.WorkID, launchSession), false, "contact_operator")
	}

	// The canonical target comes from identity, never from input: the work's
	// primary Project locates the repository, and the locator policy derives
	// branch and path (CD-0096 D2, worktree-locate).
	var projectID string
	err = tx.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, req.WorkID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return SessionWorktreeTarget{}, newFailure(KindUnknownScope, "worktree_retarget", "work has no primary Project", false, "capture the work against a Project before retargeting")
	}
	if err != nil {
		return SessionWorktreeTarget{}, wrapFailure(KindUnavailable, "worktree_retarget", "cannot read the work's primary Project", true, "retry once the database is readable", err)
	}
	branch := "work/" + req.WorkID
	canonicalPath := filepath.Join(req.WorktreeRoot, projectID, req.WorkID)

	entries, err := worktreeEntriesTx(ctx, tx, req.WorkID)
	if err != nil {
		return SessionWorktreeTarget{}, err
	}
	var adopted *WorktreeEntry
	for i, candidate := range entries {
		if candidate.ProjectID == projectID && candidate.State == worktreeEntryActive {
			adopted = &entries[i]
			break
		}
	}
	target := SessionWorktreeTarget{SessionWorktreeOwner: req.Owner, WorkID: req.WorkID, ProjectID: projectID, Branch: branch, Path: canonicalPath, State: sessionTargetActive, PrincipalRef: req.PrincipalRef, ClaimedAt: req.Now.Format(time.RFC3339Nano), UpdatedAt: req.Now.Format(time.RFC3339Nano)}
	if adopted != nil {
		// Adopt the verified locator. A stored path the locator policy no
		// longer derives is drift, not a target (CD-0096 D2).
		if adopted.Path != canonicalPath || adopted.Branch != branch {
			return SessionWorktreeTarget{}, newFailure(KindProjectionConflict, "worktree_retarget",
				fmt.Sprintf("stored worktree %s (%s) no longer matches the derived canonical target %s (%s)", adopted.Path, adopted.Branch, canonicalPath, branch), false, "reconcile the existing worktree claim before retargeting")
		}
	} else {
		// Create the canonical worktree through the claim's own atomic
		// create-verify route on this transaction. The base commit comes
		// from the Project's repository at HEAD, resolved through git facts
		// only — no caller input reaches the claim intent.
		repoRoot, resErr := worktreeRepoRootTx(ctx, tx, WorktreeClaimRequest{ProjectID: projectID})
		if resErr != nil {
			return SessionWorktreeTarget{}, resErr
		}
		baseSHA, shaErr := resolveCommitSHARunner(ctx, runner, repoRoot, "HEAD")
		if shaErr != nil {
			return SessionWorktreeTarget{}, shaErr
		}
		result, claimErr := ClaimWorktreeTx(ctx, transaction, WorktreeClaimRequest{
			OpID: req.OpID, WorkID: req.WorkID, ProjectID: projectID,
			Branch: branch, BaseSHA: baseSHA, Path: canonicalPath,
			RepoRoot:     repoRoot,
			PrincipalRef: req.PrincipalRef, RequestID: req.RequestID,
			ExpectedVersion: req.ExpectedWorkVersion, Now: req.Now, Runner: runner,
		})
		if claimErr != nil {
			return SessionWorktreeTarget{}, claimErr
		}
		target.Path = result.Entry.Path
	}

	if found {
		target.TargetVersion = existing.TargetVersion + 1
		target.ClaimedAt = existing.ClaimedAt
		if _, err := tx.ExecContext(ctx, `UPDATE session_worktree_targets SET work_id=?,project_id=?,branch=?,path=?,state=?,target_version=?,principal_ref=?,updated_at=? WHERE client_ref=? AND agent_ref=? AND session_ref=?`,
			target.WorkID, target.ProjectID, target.Branch, target.Path, target.State, target.TargetVersion, target.PrincipalRef, target.UpdatedAt, req.Owner.ClientRef, req.Owner.AgentRef, req.Owner.SessionRef); err != nil {
			return SessionWorktreeTarget{}, wrapFailure(KindUnavailable, "worktree_retarget", "cannot update session target", true, "retry the same operation with the same op id", err)
		}
		return target, nil
	}
	target.TargetVersion = 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_worktree_targets(client_ref,agent_ref,session_ref,work_id,project_id,branch,path,state,target_version,principal_ref,claimed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		req.Owner.ClientRef, req.Owner.AgentRef, req.Owner.SessionRef, target.WorkID, target.ProjectID, target.Branch, target.Path, target.State, target.TargetVersion, target.PrincipalRef, target.ClaimedAt, target.UpdatedAt); err != nil {
		return SessionWorktreeTarget{}, wrapFailure(KindUnavailable, "worktree_retarget", "cannot persist session target", true, "retry the same operation with the same op id", err)
	}
	return target, nil
}

// SessionWorktreeTarget reads the session's current effective target. The
// second return is false when the session holds no target row.
func (s *Store) SessionWorktreeTarget(ctx context.Context, owner SessionWorktreeOwner) (SessionWorktreeTarget, bool, error) {
	if s == nil || s.db == nil {
		return SessionWorktreeTarget{}, false, newFailure(KindUnavailable, "worktree_retarget", "store is not open", false, "open the authority database")
	}
	return sessionWorktreeTargetRowTx(ctx, s.db, owner)
}

func validateSessionWorktreeOwner(owner SessionWorktreeOwner) error {
	for label, value := range map[string]string{"client ref": owner.ClientRef, "agent ref": owner.AgentRef, "session ref": owner.SessionRef} {
		if len(value) < 2 || len(value) > 128 {
			return newFailure(KindInvalidOperation, "worktree_retarget", "session identity is missing a bounded "+label, false, "supply the client, agent, and session identity of the calling session")
		}
	}
	return nil
}

func sessionWorktreeTargetRowTx(ctx context.Context, q queryer, owner SessionWorktreeOwner) (SessionWorktreeTarget, bool, error) {
	var target SessionWorktreeTarget
	err := q.QueryRowContext(ctx, `SELECT client_ref,agent_ref,session_ref,work_id,project_id,branch,path,state,target_version,principal_ref,claimed_at,updated_at FROM session_worktree_targets WHERE client_ref=? AND agent_ref=? AND session_ref=?`,
		owner.ClientRef, owner.AgentRef, owner.SessionRef).Scan(&target.ClientRef, &target.AgentRef, &target.SessionRef, &target.WorkID, &target.ProjectID, &target.Branch, &target.Path, &target.State, &target.TargetVersion, &target.PrincipalRef, &target.ClaimedAt, &target.UpdatedAt)
	if err == sql.ErrNoRows {
		return SessionWorktreeTarget{}, false, nil
	}
	if err != nil {
		return SessionWorktreeTarget{}, false, wrapFailure(KindUnavailable, "worktree_retarget", "cannot read session target", true, "retry once the database is readable", err)
	}
	return target, true, nil
}

// sessionWorktreeHolderTx reports the session actively targeting workID,
// excluding the requesting owner (its own binding is handled above).
func sessionWorktreeHolderTx(ctx context.Context, tx *sql.Tx, workID string, owner SessionWorktreeOwner) (SessionWorktreeOwner, bool, error) {
	var holder SessionWorktreeOwner
	err := tx.QueryRowContext(ctx, `SELECT client_ref,agent_ref,session_ref FROM session_worktree_targets WHERE work_id=? AND state='active' AND NOT (client_ref=? AND agent_ref=? AND session_ref=?)`,
		workID, owner.ClientRef, owner.AgentRef, owner.SessionRef).Scan(&holder.ClientRef, &holder.AgentRef, &holder.SessionRef)
	if err == sql.ErrNoRows {
		return SessionWorktreeOwner{}, false, nil
	}
	if err != nil {
		return SessionWorktreeOwner{}, false, wrapFailure(KindUnavailable, "worktree_retarget", "cannot read session target holders", true, "retry once the database is readable", err)
	}
	return holder, true, nil
}

// activeBootstrapLaunchTx reports whether a CD-0088 bootstrap launch is live
// for the work item, and the launched session's identity when it is. A live
// child session owns the worktree it runs in.
func activeBootstrapLaunchTx(ctx context.Context, tx *sql.Tx, workID string) (string, bool, error) {
	var sessionID string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(launch_session_id,'') FROM bootstrap_operations WHERE work_id=? AND state IN ('pending','creating','native_ready','completed') AND launch_state IN ('prepared','running')`, workID).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, wrapFailure(KindUnavailable, "worktree_retarget", "cannot read bootstrap launches", true, "retry once the database is readable", err)
	}
	return sessionID, true, nil
}

func sessionOwnerLabel(owner SessionWorktreeOwner) string {
	return owner.ClientRef + "/" + owner.AgentRef + "/" + owner.SessionRef
}
