package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	// RequireTerminal gates the request on terminal work (the CD-0096 D3
	// Destroy tier). The reclaim surface leaves it false.
	RequireTerminal bool
	// OperatorApprovalRef names the operator approval consumed for a removal
	// the terminal gate would otherwise refuse (CD-0096 D3 Destroy). Empty
	// keeps the gate. The git safety gates are unaffected by this field.
	OperatorApprovalRef string
	// Destructive declares that the consumed approval also covers discarding
	// the git safety gates: the clean-tree and merged-branch checks are
	// skipped and the native remove is forced. It requires a non-empty
	// OperatorApprovalRef; the surface guarantees the pairing and the store
	// refuses the combination that would skip gates unapproved.
	Destructive bool
	// ObservedSessionDirectories carries the working directories the calling
	// host reported for its live sessions (issue #722). The store owns the
	// worktree path and the host owns session liveness, so the caller that
	// can see both supplies the observation and the removal decides on it.
	// A caller with no host, such as the CLI, supplies none and reaches the
	// git gates alone.
	ObservedSessionDirectories []SessionDirectory
}

// SessionDirectory is one live host session and the directory it runs in, as
// the caller observed it. The store never resolves it against a session
// registry: the observation is the caller's, and the removal only asks whether
// the directory it is about to delete is one of them.
type SessionDirectory struct {
	SessionRef string
	Directory  string
}

// WorktreeDestroyRequest drives the CD-0096 D3 Destroy tier: merged terminal
// work reclaims under the unchanged CD-0095 git gates; non-terminal work and
// any destructive removal refuse typed without a consumed operator approval.
type WorktreeDestroyRequest struct {
	WorkID          string
	ProjectID       string
	DefaultRef      string
	ExpectedVersion int64
	// OperatorApprovalRef names the consumed operator approval. Empty means
	// the safe path only.
	OperatorApprovalRef string
	// Destructive declares that the approval also covers discarding the git
	// safety gates.
	Destructive  bool
	PrincipalRef string
	RequestID    string
	Now          time.Time
	Runner       GitRunner
	// ObservedSessionDirectories carries the caller's live host sessions. The
	// destructive approval covers the git gates, never the occupancy gate.
	ObservedSessionDirectories []SessionDirectory
}

// DestroyWorktree reclaims the work item's worktree under the Destroy tier's
// authority gates. The write owns its transaction.
func (s *Store) DestroyWorktree(ctx context.Context, req WorktreeDestroyRequest) (WorktreeEntry, error) {
	reclaimReq := WorktreeReclaimRequest{
		WorkID: req.WorkID, ProjectID: req.ProjectID, DefaultRef: req.DefaultRef,
		PrincipalRef: req.PrincipalRef, RequestID: req.RequestID,
		ExpectedVersion: req.ExpectedVersion, Now: req.Now, Runner: req.Runner,
		RequireTerminal: true, OperatorApprovalRef: req.OperatorApprovalRef, Destructive: req.Destructive,
		ObservedSessionDirectories: req.ObservedSessionDirectories,
	}
	if s == nil || s.db == nil {
		return WorktreeEntry{}, newFailure(KindUnavailable, "worktree_destroy", "store is not open", false, "open the authority database")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorktreeEntry{}, wrapFailure(KindUnavailable, "worktree_destroy", "cannot begin destroy", true, "retry once the database is writable", err)
	}
	defer tx.Rollback()
	transaction := &Transaction{tx: tx, clock: s.Clock}
	out, err := DestroyWorktreeTx(ctx, transaction, reclaimReq)
	if err != nil {
		return WorktreeEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorktreeEntry{}, wrapFailure(KindUnavailable, "worktree_destroy", "cannot commit destroy", true, "retry the same operation", err)
	}
	return out, nil
}

// DestroyWorktreeTx is the destroy on an existing transaction, so the agent
// tool surface can compose it with its idempotency envelope.
func DestroyWorktreeTx(ctx context.Context, transaction *Transaction, req WorktreeReclaimRequest) (WorktreeEntry, error) {
	tx, err := transactionSQL(transaction, "worktree_destroy")
	if err != nil {
		return WorktreeEntry{}, err
	}
	if req.Now.IsZero() {
		req.Now = transaction.now()
	}
	return reclaimWorktreeRawTx(ctx, tx, req)
}

// ReclaimWorktree reclaims the worktree on its own transaction (CD-0095).
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
	// The Destroy tier labels its own refusals and keeps its own recovery
	// actions; the reclaim surface is unchanged beneath it.
	op := "worktree_reclaim"
	if req.RequireTerminal {
		op = "worktree_destroy"
	}

	// CD-0096 D3 Destroy: merged terminal work reclaims without approval.
	// Non-terminal work refuses typed unless the operator approved this
	// exact removal.
	if req.RequireTerminal {
		var lifecycle string
		err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.WorkID).Scan(&lifecycle)
		if err == sql.ErrNoRows {
			return out, newFailure(KindUnknownScope, op, "work item does not exist", false, "select one existing work item")
		}
		if err != nil {
			return out, wrapFailure(KindUnavailable, op, "cannot read the work item", true, "retry once the database is readable", err)
		}
		if !isTerminalLifecycle(lifecycle) && req.OperatorApprovalRef == "" {
			return out, newFailure(KindInvalidTransition, op, "work item is "+lifecycle+", so its worktree is not merged terminal work", false, "complete or cancel the work first, or obtain an operator-approved destroy")
		}
		if req.Destructive && req.OperatorApprovalRef == "" {
			return out, newFailure(KindInvalidOperation, op, "destructive removal requires the operator approval it would consume", false, "obtain an operator-approved destructive destroy")
		}
	}

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
		return out, newFailure(KindProjectionNotFound, op, "no active worktree for this Project", false, "claim a worktree before reclaiming it")
	}

	repoRoot, resErr := worktreeRepoRootTx(ctx, tx, WorktreeClaimRequest{ProjectID: req.ProjectID})
	if resErr != nil {
		return out, resErr
	}

	// A stale projection reconciles against stronger git truth: if the native
	// worktree is already gone, reclamation records that fact instead of
	// demanding unreachable probes.
	if _, probeErr := runner.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "HEAD"); probeErr != nil {
		facts := jsonMustMarshal(map[string]any{"already_absent": true})
		if req.Destructive {
			facts = jsonMustMarshal(map[string]any{"already_absent": true, "forced": true, "operator_override": req.OperatorApprovalRef})
		}
		if err := appendReclaimedTx(ctx, tx, req, setID, now, facts); err != nil {
			return out, err
		}
		return worktreeEntryAfterReclaimTx(ctx, tx, req.WorkID, req.ProjectID)
	}

	// Issue #722: a worktree a live session runs in is not safe to remove.
	// The host resolves a session's directory once per prompt, so removing it
	// leaves that session alive but unable to answer another prompt. This gate
	// sits above the tier split because the destructive approval covers the
	// git gates, which protect committed and uncommitted work, and never
	// authorizes stranding a session. It sits below the already-absent branch
	// because a directory that is already gone strands nobody, and stale-claim
	// recovery must stay reachable.
	if occupant, occupied := occupyingSession(entry.Path, req.ObservedSessionDirectories); occupied {
		return out, newFailure(KindWorktreeOwnershipConflict, op,
			fmt.Sprintf("session %s runs in worktree %s; removing it would leave that session unable to send another prompt", occupant.SessionRef, entry.Path),
			false, "end that session, or move it out of the worktree, then remove it")
	}

	// A destructive removal runs under its consumed operator approval: the
	// clean-tree and merged-branch gates are skipped and the native remove
	// is forced (CD-0096 D3 Destroy).
	if req.Destructive {
		facts := jsonMustMarshal(map[string]any{"forced": true, "operator_override": req.OperatorApprovalRef})
		if err := appendReclaimedTx(ctx, tx, req, setID, now, facts); err != nil {
			return out, err
		}
		if _, err := runner.Run(ctx, repoRoot, "worktree", "remove", "--force", entry.Path); err != nil {
			return out, wrapFailure(KindGitUnreachable, op, "reclaimed in Concord but native removal failed", true, "remove the worktree manually; the projection already records reclamation", err)
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
		return out, wrapFailure(KindGitUnreachable, op, "cannot read worktree status", true, "retry once the worktree is reachable", statusErr)
	}
	if strings.TrimSpace(string(statusOut)) != "" {
		recovery := "commit or discard the changes before reclaiming"
		if req.RequireTerminal {
			recovery = "commit or discard the changes, or obtain an operator-approved destructive destroy"
		}
		return out, newFailure(KindInvalidOperation, op, "worktree tree is dirty", false, recovery)
	}
	defaultRef := req.DefaultRef
	if defaultRef == "" {
		refOut, refErr := runner.Run(ctx, repoRoot, "symbolic-ref", "refs/remotes/origin/HEAD")
		if refErr != nil || strings.TrimSpace(string(refOut)) == "" {
			return out, newFailure(KindGitUnreachable, op, "cannot resolve the default branch", false, "set origin/HEAD or supply the merge target ref")
		}
		defaultRef = strings.TrimPrefix(strings.TrimSpace(string(refOut)), "refs/remotes/")
	}
	if err := branchIsMergedInto(ctx, runner, repoRoot, entry.Branch, defaultRef, op); err != nil {
		return out, err
	}

	if err := appendReclaimedTx(ctx, tx, req, setID, now, jsonMustMarshal(map[string]any{"clean_tree": true, "head_reachable": true, "default_ref": defaultRef})); err != nil {
		return out, err
	}
	if _, err := runner.Run(ctx, repoRoot, "worktree", "remove", entry.Path); err != nil {
		return out, wrapFailure(KindGitUnreachable, op, "reclaimed in Concord but native removal failed", true, "remove the worktree manually; the projection already records reclamation", err)
	}
	return worktreeEntryAfterReclaimTx(ctx, tx, req.WorkID, req.ProjectID)
}

// occupyingSession reports the first observed session whose directory is the
// worktree or sits beneath it. The comparison is lexical on cleaned absolute
// paths: the worktree directory still exists at this point, but an observed
// session directory need not, so resolving symlinks here would refuse on the
// filesystem rather than on the question asked. The separator test keeps a
// sibling that merely shares a name prefix out of the match.
func occupyingSession(worktreePath string, observed []SessionDirectory) (SessionDirectory, bool) {
	if worktreePath == "" || len(observed) == 0 {
		return SessionDirectory{}, false
	}
	root := filepath.Clean(worktreePath)
	for _, candidate := range observed {
		if candidate.Directory == "" {
			continue
		}
		directory := filepath.Clean(candidate.Directory)
		if directory == root || strings.HasPrefix(directory, root+string(filepath.Separator)) {
			return candidate, true
		}
	}
	return SessionDirectory{}, false
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
func branchIsMergedInto(ctx context.Context, runner GitRunner, repoRoot, branch, defaultRef, op string) error {
	mergedTree, mergeErr := runner.Run(ctx, repoRoot, "merge-tree", "--write-tree", defaultRef, branch)
	if mergeErr != nil {
		// A non-zero exit means the merge conflicts, so the branch carries
		// content the default ref does not hold. It is not merged.
		return newFailure(KindInvalidOperation, op, "worktree branch does not merge cleanly into "+defaultRef, false, "merge the branch before reclaiming")
	}
	defaultTree, treeErr := runner.Run(ctx, repoRoot, "rev-parse", defaultRef+"^{tree}")
	if treeErr != nil {
		return newFailure(KindGitUnreachable, op, "cannot resolve the tree of "+defaultRef, false, "retry once the repository is reachable")
	}
	if firstLine(mergedTree) != firstLine(defaultTree) {
		return newFailure(KindInvalidOperation, op, "worktree head is not merged into "+defaultRef, false, "merge the branch before reclaiming")
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
	// WorktreeDriftTerminalPresent: the worktree is on disk and still
	// claimed, and its work item is terminal. Nothing is wrong with it and
	// nothing will ever use it again. It is the shape a merged branch leaves
	// behind, and the one class whose named action the audit can perform
	// itself, because reclaiming it is a store decision under store gates.
	WorktreeDriftTerminalPresent = "terminal_present"
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
	// Terminal work with an active entry: the same join, the other side of
	// the lifecycle. The lifecycle rides along so the row can say which.
	terminalLifecycle := map[string]string{}
	terminalRows, err := q.QueryContext(ctx, `SELECT w.id, w.lifecycle FROM work_items w JOIN worktree_entries e ON e.set_id=?||w.id JOIN product_projects pp ON pp.project_id=e.project_id WHERE pp.product_id=? AND e.state='active' AND w.lifecycle IN `+terminalLifecycleSQLList(), worktreeSetPrefix, productID) //nolint:gosec // the IN list is the closed terminal set, never caller input
	if err != nil {
		return WorktreeAudit{}, wrapFailure(KindUnavailable, "worktree_audit", "cannot read terminal work", true, "retry once the database is readable", err)
	}
	defer terminalRows.Close()
	for terminalRows.Next() {
		var id, lifecycle string
		if err := terminalRows.Scan(&id, &lifecycle); err != nil {
			return WorktreeAudit{}, err
		}
		terminalLifecycle[id] = lifecycle
	}
	if err := terminalRows.Err(); err != nil {
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
	// Terminal present: the entry is active, the disk agrees, and the work
	// is finished. Reclaim is the named action, and the only safe one.
	for _, e := range entries {
		lifecycle, terminal := terminalLifecycle[e.workID]
		if !terminal {
			continue
		}
		present, err := exists(e.path)
		if err != nil {
			return WorktreeAudit{}, err
		}
		if !present {
			continue
		}
		drift = append(drift, WorktreeDrift{Class: WorktreeDriftTerminalPresent, ProjectID: e.projectID, WorkID: e.workID, Path: e.path, Lifecycle: lifecycle, RecoveryAction: WorktreeRecoveryReclaim})
	}
	if len(drift) > limit {
		drift = drift[:limit]
	}
	return WorktreeAudit{Root: root, Drift: drift}, nil
}

// Outcomes of one row in a reclaim pass.
const (
	WorktreeAuditReclaimed = "reclaimed"
	WorktreeAuditRefused   = "refused"
)

// WorktreeAuditReclaimRequest drives one reclaim pass over a Product's
// terminal-present worktrees. The observations and runner are the same
// inputs a direct reclaim takes, because each row runs the direct reclaim.
type WorktreeAuditReclaimRequest struct {
	ProductID                  string
	DefaultRef                 string
	PrincipalRef               string
	RequestID                  string
	Now                        time.Time
	Runner                     GitRunner
	Limit                      int
	ObservedSessionDirectories []SessionDirectory
}

// WorktreeAuditReclaimRow is the outcome of one terminal-present worktree.
// A refusal carries the typed kind and detail the reclaim gate produced, so
// the caller can tell a dirty tree from an occupied one from an unmerged
// head without parsing prose.
type WorktreeAuditReclaimRow struct {
	ProjectID string `json:"project_id"`
	WorkID    string `json:"work_id"`
	Path      string `json:"path"`
	Lifecycle string `json:"lifecycle"`
	Outcome   string `json:"outcome"`
	// Version is the work item's version after a reclamation, the same bump
	// a direct reclaim returns; zero on a refused row.
	Version     int64  `json:"version,omitempty"`
	RefusalKind string `json:"refusal_kind,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// WorktreeAuditReclaimResult is one pass: what the audit reported for the
// classes it can only report, and what happened to each row it could act on.
type WorktreeAuditReclaimResult struct {
	Root       string                    `json:"root"`
	ReportOnly []WorktreeDrift           `json:"report_only"`
	Rows       []WorktreeAuditReclaimRow `json:"rows"`
}

// WorktreeAuditReclaim performs the one safe action the audit names. It runs
// the audit, then reclaims each terminal-present worktree through the same
// gates a direct reclaim runs: clean tree, head merged by tree identity, no
// observed session inside. Every other class is returned as report-only,
// because its named action is not a store decision.
//
// Each row reclaims in its own transaction. Rows are independent, and one
// refusal must not roll back another row's reclamation; the pass is a loop
// over direct reclaims, not one large write. A refused row is reported with
// its typed kind and left on disk. The pass is idempotent: a second run over
// the same state reclaims nothing and reports the same refusals.
func (s *Store) WorktreeAuditReclaim(ctx context.Context, req WorktreeAuditReclaimRequest) (WorktreeAuditReclaimResult, error) {
	if s == nil || s.db == nil {
		return WorktreeAuditReclaimResult{}, newFailure(KindUnavailable, "worktree_audit_reclaim", "store is not open", false, "open the authority database")
	}
	audit, err := s.WorktreeAudit(ctx, req.ProductID, req.Limit)
	if err != nil {
		return WorktreeAuditReclaimResult{}, err
	}
	out := WorktreeAuditReclaimResult{Root: audit.Root, ReportOnly: []WorktreeDrift{}, Rows: []WorktreeAuditReclaimRow{}}
	for _, drift := range audit.Drift {
		if drift.Class != WorktreeDriftTerminalPresent {
			out.ReportOnly = append(out.ReportOnly, drift)
			continue
		}
		row := WorktreeAuditReclaimRow{ProjectID: drift.ProjectID, WorkID: drift.WorkID, Path: drift.Path, Lifecycle: drift.Lifecycle}
		version, err := currentWorkVersion(ctx, s.db, drift.WorkID)
		if err != nil {
			return WorktreeAuditReclaimResult{}, err
		}
		_, reclaimErr := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
			WorkID: drift.WorkID, ProjectID: drift.ProjectID, DefaultRef: req.DefaultRef,
			PrincipalRef: req.PrincipalRef, RequestID: req.RequestID + ":" + drift.WorkID,
			ExpectedVersion: version, Now: req.Now, Runner: req.Runner, RequireTerminal: true,
			ObservedSessionDirectories: req.ObservedSessionDirectories,
		})
		if reclaimErr == nil {
			row.Outcome, row.Version = WorktreeAuditReclaimed, version+1
			out.Rows = append(out.Rows, row)
			continue
		}
		var failure *Failure
		if !errors.As(reclaimErr, &failure) {
			return WorktreeAuditReclaimResult{}, reclaimErr
		}
		row.Outcome, row.RefusalKind, row.Detail = WorktreeAuditRefused, string(failure.Kind), failure.Detail
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

func currentWorkVersion(ctx context.Context, q queryer, workID string) (int64, error) {
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return 0, wrapFailure(KindUnavailable, "worktree_audit_reclaim", "cannot read work version", true, "retry once the database is readable", err)
	}
	return version, nil
}

// SessionWorktreeOwner identifies the agent session that holds a verify
// lease. The tuple matches the authority grant identity; principal_ref is
// derived from the registered client (CD-0080 D1) and needs no separate
// column for ownership.
type SessionWorktreeOwner struct {
	ClientRef  string `json:"client_ref"`
	AgentRef   string `json:"agent_ref"`
	SessionRef string `json:"session_ref"`
}

func validateSessionWorktreeOwner(owner SessionWorktreeOwner) error {
	for label, value := range map[string]string{"client ref": owner.ClientRef, "agent ref": owner.AgentRef, "session ref": owner.SessionRef} {
		if len(value) < 2 || len(value) > 128 {
			return newFailure(KindInvalidOperation, "worktree_verify", "session identity is missing a bounded "+label, false, "supply the client, agent, and session identity of the calling session")
		}
	}
	return nil
}

func sessionOwnerLabel(owner SessionWorktreeOwner) string {
	return owner.ClientRef + "/" + owner.AgentRef + "/" + owner.SessionRef
}

// Cross-worktree tiers (CD-0096 D3): Inspect, Verify, and Destroy. Each tier
// resolves its subject through the calling session's Project and the work
// item's folded worktree entry — never through a caller path (CD-0096 D2) —
// so a worktree outside the session's Project is not reachable as a target
// at all.

const (
	// WorktreeInspectModeStatus reads `git status --porcelain`.
	WorktreeInspectModeStatus = "status"
	// WorktreeInspectModeDiff reads the worktree's diff against HEAD.
	WorktreeInspectModeDiff = "diff"
	// WorktreeInspectModeFile reads one file's content.
	WorktreeInspectModeFile = "file"

	defaultWorktreeInspectBytes  = 16384
	defaultWorktreeVerifyBytes   = 16384
	maxWorktreeVerifyCommandArgs = 16
	maxWorktreeVerifyCommandPart = 256
)

// activeWorktreeEntryForProject resolves the work item's active worktree
// entry inside the calling session's Project. The Project selector is the
// same-Project tier boundary: an entry in any other Project is invisible.
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
	return WorktreeEntry{}, newFailure(KindProjectionNotFound, op, "work item holds no active worktree in Project "+projectID, false, "claim or retarget the canonical worktree before tiered access")
}

// probeWorktreeReachable refuses typed when the folded entry points at a
// tree the host cannot reach, or at a branch the tree no longer checks out.
// Both are drift, not inspection subjects.
func probeWorktreeReachable(ctx context.Context, runner GitRunner, op string, entry WorktreeEntry) error {
	branchOut, err := runner.Run(ctx, entry.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return wrapFailure(KindGitUnreachable, op, "worktree at "+entry.Path+" is not reachable on disk", true, "reconcile the worktree drift with the audit read before tiered access", err)
	}
	if strings.TrimSpace(string(branchOut)) != entry.Branch {
		return newFailure(KindProjectionConflict, op, "worktree at "+entry.Path+" is on branch "+strings.TrimSpace(string(branchOut))+", the stored claim says "+entry.Branch, false, "reconcile the worktree claim before tiered access")
	}
	return nil
}

// WorktreeInspectRequest drives the CD-0096 Inspect tier: a read-only view
// of files, Git status, and diffs of one active same-Project worktree. The
// persistent effective target is never a factor and never changes.
type WorktreeInspectRequest struct {
	WorkID    string
	ProjectID string
	Mode      string
	// Path is the relative file selector for file mode. It is a selector
	// inside the derived worktree, never a worktree path (CD-0096 D2).
	Path     string
	Runner   GitRunner
	MaxBytes int
}

// WorktreeInspectResult is the bounded content of one inspection.
type WorktreeInspectResult struct {
	WorkID    string `json:"work_id"`
	ProjectID string `json:"project_id"`
	Branch    string `json:"branch"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

// InspectWorktree reads files, Git status, or a diff from the work item's
// active worktree in the calling session's Project (CD-0096 D3 Inspect). It
// is a pure read: no lease is taken and no persistent state changes.
func (s *Store) InspectWorktree(ctx context.Context, req WorktreeInspectRequest) (WorktreeInspectResult, error) {
	if s == nil || s.db == nil {
		return WorktreeInspectResult{}, newFailure(KindUnavailable, "worktree_inspect", "store is not open", false, "open the authority database")
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
		result.Content, result.Truncated = content, len(content) > maxBytes
	}
	return result, nil
}

// safeWorktreeRelativePath accepts one bounded relative selector. Absolute
// paths, parent traversal, and unclean forms refuse typed, because a
// selector that escapes the derived worktree reads a file no tier granted.
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

// boundText keeps the first maxBytes of text and reports truncation.
func boundText(text string, maxBytes int) (string, bool) {
	if len(text) > maxBytes {
		return text[:maxBytes], true
	}
	return text, false
}

// readBoundedFile reads at most maxBytes plus one byte, so the caller can
// report truncation without buffering the whole file.
func readBoundedFile(path string, maxBytes int) (string, error) {
	file, err := os.Open(path) //nolint:gosec // the path is the derived worktree joined to a validated clean relative selector.
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

// WorktreeVerifyRequest drives the CD-0096 Verify tier: the requested
// command runs in the work item's active worktree in the calling session's
// Project, under an exclusive lease, and completion refuses when tracked
// files changed while the lease was held.
type WorktreeVerifyRequest struct {
	Owner     SessionWorktreeOwner
	WorkID    string
	ProjectID string
	Command   []string
	// LeaseID scopes the lease and its pinned command. Retrying an
	// interrupted verify with the same LeaseID resumes only the command the
	// lease first pinned.
	LeaseID        string
	PrincipalRef   string
	RequestID      string
	Now            time.Time
	Runner         GitRunner
	RunCommand     func(ctx context.Context, dir string, command []string, maxOutput int) (int, []byte, bool, error)
	MaxOutputBytes int
}

// WorktreeVerifyResult is the bounded record of one leased run.
type WorktreeVerifyResult struct {
	WorkID              string   `json:"work_id"`
	ProjectID           string   `json:"project_id"`
	Branch              string   `json:"branch"`
	Path                string   `json:"path"`
	LeaseID             string   `json:"lease_id"`
	Command             []string `json:"command"`
	ExitCode            int      `json:"exit_code"`
	Output              string   `json:"output"`
	OutputTruncated     bool     `json:"output_truncated"`
	TrackedFilesChanged bool     `json:"tracked_files_changed"`
}

// VerifyWorktree acquires the exclusive verify lease, runs the command, and
// compares tracked-file state across the lease. The lease acquire and its
// release each own one transaction; the command runs between them, so no
// transaction spans the external effect.
func (s *Store) VerifyWorktree(ctx context.Context, req WorktreeVerifyRequest) (WorktreeVerifyResult, error) {
	if s == nil || s.db == nil {
		return WorktreeVerifyResult{}, newFailure(KindUnavailable, "worktree_verify", "store is not open", false, "open the authority database")
	}
	if req.LeaseID == "" || req.PrincipalRef == "" || req.RequestID == "" {
		return WorktreeVerifyResult{}, newFailure(KindInvalidOperation, "worktree_verify", "verify operation is missing identity fields", false, "supply lease, principal, and request ids")
	}
	if err := validateSessionWorktreeOwner(req.Owner); err != nil {
		return WorktreeVerifyResult{}, retitleFailure(err, "worktree_verify")
	}
	if err := validateWorktreeVerifyCommand(req.Command); err != nil {
		return WorktreeVerifyResult{}, err
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	runCommand := req.RunCommand
	if runCommand == nil {
		runCommand = RunWorktreeVerifyCommand
	}
	maxOutput := req.MaxOutputBytes
	if maxOutput <= 0 {
		maxOutput = defaultWorktreeVerifyBytes
	}
	now := req.Now
	if now.IsZero() {
		now = nowFromClock(nil)
	}
	commandJSON, _ := json.Marshal(req.Command)

	// Lease phase: resolve the subject, probe it, pin the lease, and
	// snapshot tracked state before the command runs. Any refusal here
	// rolls the transaction back, so no lease exists to release.
	acquireTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorktreeVerifyResult{}, wrapFailure(KindUnavailable, "worktree_verify", "cannot begin verify", true, "retry the same operation with the same lease id", err)
	}
	defer acquireTx.Rollback()
	entry, err := activeWorktreeEntryForProject(ctx, acquireTx, "worktree_verify", req.WorkID, req.ProjectID)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if err := probeWorktreeReachable(ctx, runner, "worktree_verify", entry); err != nil {
		return WorktreeVerifyResult{}, err
	}
	if err := acquireVerifyLeaseTx(ctx, acquireTx, req, entry, string(commandJSON), now); err != nil {
		var completed *worktreeVerifyCompleted
		if errors.As(err, &completed) {
			// The lease already reached its durable outcome (the crash
			// window between release and the caller's idempotency record).
			// Report the recorded result; never run the command twice.
			return completed.result, completed.failure
		}
		return WorktreeVerifyResult{}, err
	}
	before, err := snapshotTrackedFiles(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if err := acquireTx.Commit(); err != nil {
		return WorktreeVerifyResult{}, wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the verify lease", true, "retry the same operation with the same lease id", err)
	}

	exitCode, output, truncated, runErr := runCommand(ctx, entry.Path, req.Command, maxOutput)
	if runErr != nil {
		// A command that cannot run is a run outcome, not a store failure:
		// the release below still records and reports it.
		exitCode = -1
		output = append([]byte(runErr.Error()+"\n"), output...)
	}

	releaseTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorktreeVerifyResult{}, wrapFailure(KindUnavailable, "worktree_verify", "cannot begin verify release", true, "retry the same operation with the same lease id", err)
	}
	defer releaseTx.Rollback()
	after, err := snapshotTrackedFiles(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	changed := before != after
	outcome := "completed"
	if changed {
		outcome = "refused_mutated"
	}
	boundedOutput, outputTruncated := boundText(string(output), maxOutput)
	if outputTruncated {
		truncated = true
	}
	result := WorktreeVerifyResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Branch: entry.Branch, Path: entry.Path, LeaseID: req.LeaseID, Command: req.Command, ExitCode: exitCode, Output: boundedOutput, OutputTruncated: truncated, TrackedFilesChanged: changed}
	resultJSON, _ := json.Marshal(result)
	releasedAt := nowFromClock(nil)
	if _, err := releaseTx.ExecContext(ctx, `UPDATE worktree_verify_leases SET state='released', released_at=?, exit_code=?, outcome=?, result_json=? WHERE lease_id=? AND state='held'`,
		releasedAt.Format(time.RFC3339Nano), exitCode, outcome, string(resultJSON), req.LeaseID); err != nil {
		return WorktreeVerifyResult{}, wrapFailure(KindUnavailable, "worktree_verify", "cannot release the verify lease", true, "retry the same operation with the same lease id", err)
	}
	if err := releaseTx.Commit(); err != nil {
		return WorktreeVerifyResult{}, wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the verify release", true, "retry the same operation with the same lease id", err)
	}
	if changed {
		return result, newFailure(KindWorktreeVerifyMutated, "worktree_verify",
			"tracked files changed in "+entry.Path+" while the verify command ran; a verifier that edits its subject verifies nothing (CD-0096 D3)", false, "reconcile_operation")
	}
	return result, nil
}

// worktreeVerifyCompleted carries the durable outcome of an already-released
// lease back through the acquire error path, so a same-lease retry reports
// the recorded result instead of running the command again.
type worktreeVerifyCompleted struct {
	result  WorktreeVerifyResult
	failure error
}

func (c *worktreeVerifyCompleted) Error() string {
	return "verify lease already reached its outcome"
}

// acquireVerifyLeaseTx pins the exclusive lease. A foreign held lease for the
// worktree refuses typed, naming the holder. The same lease id resumes only
// under its original owner and pinned command, so an interrupted retry can
// never redirect the run, and a released lease reports its recorded outcome.
func acquireVerifyLeaseTx(ctx context.Context, tx *sql.Tx, req WorktreeVerifyRequest, entry WorktreeEntry, commandJSON string, now time.Time) error {
	var state, pinnedJSON, clientRef, agentRef, sessionRef, outcome, resultJSON string
	err := tx.QueryRowContext(ctx, `SELECT state,command_json,client_ref,agent_ref,session_ref,outcome,coalesce(result_json,'') FROM worktree_verify_leases WHERE lease_id=?`, req.LeaseID).
		Scan(&state, &pinnedJSON, &clientRef, &agentRef, &sessionRef, &outcome, &resultJSON)
	switch {
	case err == nil:
		if state != "held" {
			var recorded WorktreeVerifyResult
			var failure error
			if json.Unmarshal([]byte(resultJSON), &recorded) == nil {
				if outcome == "refused_mutated" {
					failure = newFailure(KindWorktreeVerifyMutated, "worktree_verify",
						"tracked files changed in "+entry.Path+" while the verify command ran; a verifier that edits its subject verifies nothing (CD-0096 D3)", false, "reconcile_operation")
				}
				return &worktreeVerifyCompleted{result: recorded, failure: failure}
			}
			return newFailure(KindUnavailable, "worktree_verify", "released verify lease carries no readable outcome", true, "retry the read of the lease outcome")
		}
		if clientRef != req.Owner.ClientRef || agentRef != req.Owner.AgentRef || sessionRef != req.Owner.SessionRef {
			return newFailure(KindWorktreeLeaseHeld, "worktree_verify",
				"verify lease "+req.LeaseID+" is held by session "+clientRef+"/"+agentRef+"/"+sessionRef, true, "retry_same_request")
		}
		if pinnedJSON != commandJSON {
			return newFailure(KindInvalidOperation, "worktree_verify", "retry does not match the pinned command", false, "retry with the same command or a new idempotency key")
		}
		return nil
	case err == sql.ErrNoRows:
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome) VALUES(?,?,?,?, 'held', ?,?,?,?,?,?, 'running')`,
			req.LeaseID, req.WorkID, req.ProjectID, entry.Path, req.Owner.ClientRef, req.Owner.AgentRef, req.Owner.SessionRef, req.PrincipalRef, commandJSON, now.Format(time.RFC3339Nano)); insertErr != nil {
			// The one-held partial index refused: name the actual holder.
			holder, held, holderErr := heldWorktreeVerifyLeaseTx(ctx, tx, entry.Path)
			if holderErr != nil {
				return holderErr
			}
			if held {
				return newFailure(KindWorktreeLeaseHeld, "worktree_verify",
					"worktree "+entry.Path+" holds an active verify lease held by session "+sessionOwnerLabel(holder), true, "retry_same_request")
			}
			return wrapFailure(KindUnavailable, "worktree_verify", "cannot persist the verify lease", true, "retry the same operation with the same lease id", insertErr)
		}
		return nil
	default:
		return wrapFailure(KindUnavailable, "worktree_verify", "cannot read the verify lease", true, "retry once the database is readable", err)
	}
}

// heldWorktreeVerifyLeaseTx reports the session holding the worktree's
// active verify lease, for the typed contention refusal.
func heldWorktreeVerifyLeaseTx(ctx context.Context, tx *sql.Tx, path string) (SessionWorktreeOwner, bool, error) {
	var holder SessionWorktreeOwner
	err := tx.QueryRowContext(ctx, `SELECT client_ref,agent_ref,session_ref FROM worktree_verify_leases WHERE path=? AND state='held'`, path).
		Scan(&holder.ClientRef, &holder.AgentRef, &holder.SessionRef)
	if err == sql.ErrNoRows {
		return SessionWorktreeOwner{}, false, nil
	}
	if err != nil {
		return SessionWorktreeOwner{}, false, wrapFailure(KindUnavailable, "worktree_verify", "cannot read held verify leases", true, "retry once the database is readable", err)
	}
	return holder, true, nil
}

// ActiveWorktreeVerifyLease is one held verify lease of the reading
// session, carried by the pinned continuity projection (CD-0096 D5).
type ActiveWorktreeVerifyLease struct {
	LeaseID    string   `json:"lease_id"`
	WorkID     string   `json:"work_id"`
	ProjectID  string   `json:"project_id"`
	Path       string   `json:"path"`
	Command    []string `json:"command"`
	AcquiredAt string   `json:"acquired_at"`
}

// heldWorktreeVerifyLeasesByOwnerTx reads the session's held verify leases,
// newest first, bounded. The continuity re-pin carries them so an
// interrupted verify stays visible to the session that pinned it.
func heldWorktreeVerifyLeasesByOwnerTx(ctx context.Context, tx *sql.Tx, owner SessionWorktreeOwner) ([]ActiveWorktreeVerifyLease, error) {
	rows, err := tx.QueryContext(ctx, `SELECT lease_id,work_id,project_id,path,command_json,acquired_at FROM worktree_verify_leases WHERE client_ref=? AND agent_ref=? AND session_ref=? AND state='held' ORDER BY acquired_at DESC LIMIT 4`,
		owner.ClientRef, owner.AgentRef, owner.SessionRef)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_verify", "cannot read held verify leases", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	leases := []ActiveWorktreeVerifyLease{}
	for rows.Next() {
		var lease ActiveWorktreeVerifyLease
		var commandJSON string
		if err := rows.Scan(&lease.LeaseID, &lease.WorkID, &lease.ProjectID, &lease.Path, &commandJSON, &lease.AcquiredAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "worktree_verify", "cannot decode held verify lease", true, "retry once the database is readable", err)
		}
		lease.Command = []string{}
		_ = json.Unmarshal([]byte(commandJSON), &lease.Command)
		leases = append(leases, lease)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worktree_verify", "cannot enumerate held verify leases", true, "retry once the database is readable", err)
	}
	return leases, nil
}

// worktreeSnapshot is the tracked-file state of one worktree: the porcelain
// status (worktree and index) and the head commit. Two equal snapshots mean
// no tracked file changed between them.
type worktreeSnapshot struct {
	status string
	head   string
}

func snapshotTrackedFiles(ctx context.Context, runner GitRunner, path string) (worktreeSnapshot, error) {
	statusOut, err := runner.Run(ctx, path, "status", "--porcelain")
	if err != nil {
		return worktreeSnapshot{}, wrapFailure(KindGitUnreachable, "worktree_verify", "cannot read worktree status", true, "retry once the worktree is reachable", err)
	}
	headOut, err := runner.Run(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return worktreeSnapshot{}, wrapFailure(KindGitUnreachable, "worktree_verify", "cannot read the worktree head", true, "retry once the worktree is reachable", err)
	}
	return worktreeSnapshot{status: string(statusOut), head: strings.TrimSpace(string(headOut))}, nil
}

// validateWorktreeVerifyCommand accepts a bounded argv. A shell command
// string is never accepted; the values run as separate arguments.
func validateWorktreeVerifyCommand(command []string) error {
	if len(command) < 1 || len(command) > maxWorktreeVerifyCommandArgs {
		return newFailure(KindInvalidOperation, "worktree_verify", "verify command must be one to sixteen argv values", false, "supply the command and its arguments as separate values")
	}
	for _, part := range command {
		if len(part) < 1 || len(part) > maxWorktreeVerifyCommandPart {
			return newFailure(KindInvalidOperation, "worktree_verify", "verify command values must be one to 256 bytes", false, "supply bounded command and argument values")
		}
	}
	return nil
}

// cappedOutput drains a command stream while recording at most limit bytes.
type cappedOutput struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	if w.buffer.Len() >= w.limit {
		w.truncated = true
		return len(p), nil
	}
	room := w.limit - w.buffer.Len()
	if len(p) > room {
		w.buffer.Write(p[:room])
		w.truncated = true
	} else {
		w.buffer.Write(p)
	}
	return len(p), nil
}

// RunWorktreeVerifyCommand executes argv in dir under the caller's context,
// returning the exit code and bounded combined output. A command that cannot
// start reports exit code -1 with the start failure as output.
func RunWorktreeVerifyCommand(ctx context.Context, dir string, command []string, maxOutput int) (int, []byte, bool, error) {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...) //nolint:gosec // argv values stay separate and no shell is invoked; the tier grants the command.
	cmd.Dir = dir
	out := &cappedOutput{limit: maxOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return -1, []byte(err.Error()), false, nil
	}
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), out.buffer.Bytes(), out.truncated, nil
		}
		return -1, append(out.buffer.Bytes(), []byte("\n"+err.Error())...), out.truncated, nil
	}
	return 0, out.buffer.Bytes(), out.truncated, nil
}

// retitleFailure rebrands a typed failure from another operation label so a
// reused validator reports the operation the caller invoked.
func retitleFailure(err error, op string) error {
	var failure *Failure
	if errors.As(err, &failure) {
		branded := *failure
		branded.Op = op
		return &branded
	}
	return err
}
