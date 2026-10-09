package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/hostlease"
)

var bootstrapIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// BootstrapRequest is the bounded host request for one captured work item and
// its one canonical Project worktree.
type BootstrapRequest struct {
	ProductID             string   `json:"product_id"`
	ProjectID             string   `json:"project_id"`
	Title                 string   `json:"title"`
	ValueStatement        string   `json:"value_statement"`
	Kind                  string   `json:"kind"`
	Task                  string   `json:"task"`
	IdempotencyKey        string   `json:"idempotency_key"`
	Priority              int64    `json:"priority"`
	Urgency               string   `json:"urgency"`
	Tags                  []string `json:"tags"`
	WorkflowTypeRef       string   `json:"workflow_type_ref"`
	ExternalRef           string   `json:"external_ref"`
	RaisedFromWorkID      string   `json:"raised_from_work_id,omitempty"`
	GoverningRequirements []string `json:"governing_requirements"`
	Ref                   string   `json:"ref"`
	// DefectIntake is the capture admission record a bug capture requires
	// and a research capture may carry as its cluster RCA identity. It rides
	// the request into the work.created payload; an absent record leaves the
	// marshalled request, and therefore the capture identity, unchanged.
	DefectIntake *DefectIntake `json:"defect_intake,omitempty"`
	// SessionRef is the session that will occupy the claimed worktree. It
	// stays out of the marshalled request because the canonical identity
	// digest is built from it: the same capture asked for by two sessions is
	// one work item, not two.
	SessionRef string `json:"-"`
	// HostPID is the OpenCode process whose adapter records the bootstrap.
	// The claim's occupancy row carries its process identity, so the row
	// never waits for the landing to become releasable. It stays out of the
	// marshalled request with SessionRef: the digest names the capture, not
	// the recording process.
	HostPID int `json:"-"`
	// ResolvedRef carries the directory-derived default branch the calling
	// host resolved for an omitted ref. It stays out of the marshalled
	// request with SessionRef and HostPID: the canonical identity names the
	// caller's capture intent, where an omitted ref stays the empty
	// sentinel, and the resolution applies after the digest. One capture
	// intent therefore hashes identically from the main checkout and a
	// linked worktree.
	ResolvedRef string `json:"-"`
}

// BootstrapResult is the durable result of a bootstrap operation.
type BootstrapResult struct {
	OperationID string
	Replayed    bool
	ProductID   string
	ProjectID   string
	WorkID      string
	WorkVersion int64
	Entry       WorktreeEntry
}

// BootstrapOrigin is the verified linked worktree that a work_start call is
// leaving. The origin is read-only input to bootstrap and is never reclaimed
// by the chained start.
type BootstrapOrigin struct {
	ProjectID string
	WorkID    string
	Branch    string
	Path      string
	Lifecycle string
}

// ExistingBootstrapRequest identifies an existing live work item that needs
// its first canonical worktree. The work identity stays the operation identity.
type ExistingBootstrapRequest struct {
	ProductID string `json:"product_id"`
	ProjectID string `json:"project_id"`
	WorkID    string `json:"work_id"`
	Ref       string `json:"ref"`
	// SessionRef is the session that will occupy the recovered worktree. The
	// identity digest names an explicit field set that excludes it, so a
	// resume from a second session recovers the same operation.
	SessionRef string `json:"-"`
}

// ValidateBootstrapOrigin validates a clean Concord worktree with no
// active verify lease or worker attempt. It runs before bootstrap records new
// work, so every refusal has no effect.
func (s *Store) ValidateBootstrapOrigin(ctx context.Context, projectID, path string, runner GitRunner) (BootstrapOrigin, error) {
	var origin BootstrapOrigin
	if s == nil || s.db == nil {
		return origin, newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	if projectID == "" || path == "" {
		return origin, newFailure(KindInvalidOperation, "work_bootstrap", "linked bootstrap origin is missing Project or path identity", false, "run work_start from a Project worktree")
	}
	if runner == nil {
		runner = ExecGitRunner{}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return origin, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot resolve the linked bootstrap origin path", false, "run work_start from a reachable worktree", err)
	}

	readOrigin := func(q queryer) (BootstrapOrigin, string, error) {
		var current BootstrapOrigin
		var claimID string
		err := q.QueryRowContext(ctx, `SELECT e.project_id,e.branch,e.path,w.id,w.lifecycle,e.claim_op_id FROM worktree_entries e JOIN worktree_claims c ON c.op_id=e.claim_op_id JOIN work_items w ON w.id=c.work_id WHERE e.project_id=? AND e.path=? AND e.state='active'`, projectID, filepath.Clean(path)).Scan(&current.ProjectID, &current.Branch, &current.Path, &current.WorkID, &current.Lifecycle, &claimID)
		if err == sql.ErrNoRows {
			return current, claimID, newFailure(KindInvalidOperation, "work_bootstrap", "linked bootstrap origin is not an active Concord worktree", false, "run work_start from the active worktree")
		}
		if err != nil {
			return current, claimID, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read the linked bootstrap origin", true, "retry once the database is readable", err)
		}
		return current, claimID, nil
	}
	origin, claimID, err := readOrigin(s.db)
	if err != nil {
		return origin, err
	}
	// Git runs without a transaction so native probing holds neither the
	// writer lock nor the pool's only connection.
	status, err := runner.Run(ctx, filepath.Clean(path), "status", "--porcelain")
	if err != nil {
		return origin, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect linked bootstrap origin "+origin.WorkID, true, "restore access to the origin worktree and retry", err)
	}
	if strings.TrimSpace(string(status)) != "" {
		return origin, newFailure(KindInvalidOperation, "work_bootstrap", "cannot chain from dirty worktree of "+origin.WorkID, false, "commit or discard the origin changes before starting new work")
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read the linked bootstrap origin", true, "retry the same operation", err)
	}
	defer tx.Rollback()
	current, currentClaimID, err := readOrigin(tx)
	if err != nil {
		return origin, err
	}
	if current != origin || currentClaimID != claimID {
		return origin, newFailure(KindProjectionConflict, "work_bootstrap", "linked bootstrap origin changed during the git probe", true, "retry from the current active worktree")
	}
	var leased bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worktree_verify_leases WHERE path=? AND state='held')`, filepath.Clean(path)).Scan(&leased); err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot inspect origin verify leases", true, "retry once the database is readable", err)
	}
	if leased {
		return origin, newFailure(KindResourceBusy, "work_bootstrap", "cannot chain from worktree "+origin.WorkID+" while a verify lease is active", true, "release the origin verify lease and retry")
	}
	var dispatched bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempts WHERE work_id=? AND lifecycle_state='dispatched')`, origin.WorkID).Scan(&dispatched); err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot inspect origin worker attempts", true, "retry once the database is readable", err)
	}
	openWindow, err := bootstrapOriginHasOpenDispatchWindow(ctx, tx, origin.WorkID)
	if err != nil {
		return origin, err
	}
	if dispatched || openWindow {
		return origin, newFailure(KindResourceBusy, "work_bootstrap", "cannot chain from worktree "+origin.WorkID+" while a worker attempt is dispatched or open", true, "complete or close the origin worker attempt and retry")
	}
	if err := tx.Commit(); err != nil {
		return origin, wrapFailure(KindUnavailable, "work_bootstrap", "cannot finish reading the linked bootstrap origin", true, "retry the same operation", err)
	}
	return origin, nil
}

// ResumeWorktreeLocation resolves the active worktree a running session
// enters when it resumes an existing work item by work identity (issue #891).
// The read refuses typed before any effect: unknown work, terminal work, a
// Project the item does not hold, a Project outside the requested Product
// scope, and an item with no active worktree in the Project each refuse. The
// read records nothing (CD-0104 D1); the origin gate stays with the caller,
// exactly as work-bootstrap runs it for a capture.
func (s *Store) ResumeWorktreeLocation(ctx context.Context, productID, projectID, workID string) (WorktreeEntry, error) {
	var entry WorktreeEntry
	if s == nil || s.db == nil {
		return entry, newFailure(KindUnavailable, "work_resume", "store is not open", false, "open the authority database")
	}
	if productID == "" || projectID == "" || workID == "" {
		return entry, newFailure(KindInvalidOperation, "work_resume", "resume requires the Product, Project, and work identity", false, "supply all three identities")
	}
	var lifecycle string
	if err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return entry, newFailure(KindUnknownScope, "work_resume", "work item does not exist", false, "check the work identity before resuming")
		}
		return entry, wrapFailure(KindUnavailable, "work_resume", "cannot read the work item", true, "retry once the database is readable", err)
	}
	if isTerminalLifecycle(lifecycle) {
		return entry, newFailure(KindInvalidOperation, "work_resume", "cannot resume terminal work item "+workID+" ("+lifecycle+")", false, "resume live work or capture new work instead")
	}
	projects, err := s.ProjectsForWork(ctx, workID)
	if err != nil {
		return entry, err
	}
	member := false
	for _, project := range projects {
		if project.ID == projectID {
			member = true
			break
		}
	}
	if !member {
		return entry, newFailure(KindUnknownScope, "work_resume", "work item "+workID+" does not hold Project "+projectID, false, "resume from a Project the work item belongs to")
	}
	_, products, err := s.ScopeVersion(ctx, projectID)
	if err != nil || len(products) != 1 || products[0] != productID {
		return entry, newFailure(KindUnknownScope, "work_resume", "claimed Project is not in the requested Product scope", false, "resume from the Project's own Product")
	}
	return activeWorktreeEntryForProject(ctx, s.db, "work_resume", workID, projectID)
}

// BootstrapExistingWorktree claims the first canonical worktree for an
// existing live item. An active entry is returned as a read-only resume. A
// work item whose captured bootstrap names a different Product or Project
// claims the requested Project's worktree through the ordinary claim route,
// so one work item holds one worktree per Project it lives in.
func (s *Store) BootstrapExistingWorktree(ctx context.Context, req ExistingBootstrapRequest, phaseHook BootstrapPhaseHook) (BootstrapResult, error) {
	if s == nil || s.db == nil {
		return BootstrapResult{}, newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	if err := validateExistingBootstrapRequest(req); err != nil {
		return BootstrapResult{}, err
	}
	// An omitted ref stays omitted: the base resolution treats it as
	// default-based and pins the fetched origin default head, while a
	// caller-supplied ref, including HEAD, stays an exact pin.
	entry, err := s.ResumeWorktreeLocation(ctx, req.ProductID, req.ProjectID, req.WorkID)
	if err == nil {
		var version int64
		if versionErr := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, req.WorkID).Scan(&version); versionErr != nil {
			return BootstrapResult{}, wrapFailure(KindUnavailable, "work_resume", "cannot read the existing work version", true, "retry once the database is readable", versionErr)
		}
		return BootstrapResult{OperationID: entry.ClaimOpID, Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: req.WorkID, WorkVersion: version, Entry: entry}, nil
	}
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionNotFound {
		return BootstrapResult{}, err
	}
	identity, found, crossProject, err := s.existingBootstrapIdentity(ctx, req.WorkID, req.ProductID, req.ProjectID)
	if err != nil {
		return BootstrapResult{}, err
	}
	if found {
		return s.bootstrapWorktreeMode(ctx, BootstrapRequest{
			ProductID: req.ProductID, ProjectID: req.ProjectID, GoverningRequirements: identity.GoverningRequirements,
			IdempotencyKey: identity.IdempotencyKey, Ref: req.Ref, SessionRef: req.SessionRef,
		}, identity.OperationID, req.WorkID, identity.Digest, true, req, phaseHook, ExecGitRunner{})
	}
	if crossProject {
		return s.claimCrossProjectWorktree(ctx, req, identity.GoverningRequirements, ExecGitRunner{})
	}
	operationID, workID, digest, err := CanonicalExistingBootstrapIdentity(req)
	if err != nil {
		return BootstrapResult{}, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot derive existing bootstrap identity", false, "supply bounded work identity", err)
	}
	return s.bootstrapWorktreeMode(ctx, BootstrapRequest{ProductID: req.ProductID, ProjectID: req.ProjectID, IdempotencyKey: "bootstrap-existing-" + digest[7:55], Ref: req.Ref, SessionRef: req.SessionRef}, operationID, workID, digest, true, req, phaseHook, ExecGitRunner{})
}

type existingBootstrapIdentity struct {
	IdempotencyKey        string
	OperationID           string
	Digest                string
	GoverningRequirements []string
}

// existingBootstrapIdentity resolves the durable identity of the work item's
// first bootstrap operation. found reports the same-project reclaimed-claim
// replay: the stored operation and idempotency key replay with the stored
// governing requirements. crossProject reports that the first bootstrap is
// bound to a different Product or Project, and the resume falls through to
// the per-Project claim route with the stored requirements as the declared
// set.
func (s *Store) existingBootstrapIdentity(ctx context.Context, workID, productID, projectID string) (identity existingBootstrapIdentity, found bool, crossProject bool, err error) {
	var storedProductID, storedProjectID, requestJSON, operationState, claimState string
	err = s.db.QueryRowContext(ctx, `SELECT b.idempotency_key,b.operation_id,b.request_digest,b.request_json,b.product_id,b.project_id,b.state,coalesce(c.state,'') FROM bootstrap_operations b LEFT JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.work_id=?`, workID).
		Scan(&identity.IdempotencyKey, &identity.OperationID, &identity.Digest, &requestJSON, &storedProductID, &storedProjectID, &operationState, &claimState)
	if err == sql.ErrNoRows {
		return identity, false, false, nil
	}
	if err != nil {
		return identity, false, false, wrapFailure(KindUnavailable, "work_resume", "cannot read the existing bootstrap identity", true, "retry once the database is readable", err)
	}
	var storedRequest struct {
		GoverningRequirements []string `json:"governing_requirements"`
	}
	if err := json.Unmarshal([]byte(requestJSON), &storedRequest); err != nil {
		return identity, false, false, newFailure(KindInvariantViolation, "work_resume", "existing bootstrap identity has invalid durable request data", false, "contact_operator")
	}
	identity.GoverningRequirements = storedRequest.GoverningRequirements
	if storedProductID != productID || storedProjectID != projectID {
		return identity, false, true, nil
	}
	if operationState != "completed" || claimState != worktreeStateReclaimed {
		return identity, false, false, nil
	}
	return identity, true, false, nil
}

// claimCrossProjectWorktree routes a resume that names a Project other than
// the one that captured the work through the ordinary per-Project claim. The
// first bootstrap operation stays bound to its original Product and Project:
// bootstrap_operations pins one journal row per work identity, and the second
// claim needs no journal row, because migration 93 scopes the branch slot to
// one repository and the work-derived branch name holds in each Project's own
// repository. The route owns exactly one bounded preflight for its fresh
// creation: it refreshes the origin default branch and resolves location and
// base under the same fixed window through resolveFreshCreationBase, and pins
// that prepared intent durably, so the claim below reconciles the pinned row
// instead of running a second preflight. It requires the target Project
// checkout on its default branch, applies the same scope semantics as a first
// bootstrap with the stored governing requirements as the declared set, and
// derives the operation identity from the Product, Project, and work
// identities alone, so a retry reconciles the same claim.
func (s *Store) claimCrossProjectWorktree(ctx context.Context, req ExistingBootstrapRequest, declared []string, runner GitRunner) (_ BootstrapResult, retErr error) {
	operationID, _, _, err := CanonicalExistingBootstrapIdentity(req)
	if err != nil {
		return BootstrapResult{}, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot derive existing bootstrap identity", false, "supply bounded work identity", err)
	}
	// The direct claim is a fresh creation: the shared Git preflight refreshes
	// the origin default branch and resolves the base under one bounded
	// window, so the first claim in this Project pins the fetched default
	// head. A refresh failure refuses before any claim row, branch, or
	// worktree exists (CD-0195 D2 keeps the fetch outside transactions). A
	// claim row this operation already pinned is an exact replay or recovery:
	// it keeps its stored base and owes no new fetch.
	var location WorktreeLocation
	var pinnedBase string
	rowErr := s.db.QueryRowContext(ctx, `SELECT pinned_base_sha FROM worktree_claims WHERE op_id=?`, operationID).Scan(&pinnedBase)
	switch {
	case rowErr == sql.ErrNoRows:
		location, err = s.resolveFreshCreationBase(ctx, runner, req.ProjectID, req.WorkID, req.Ref)
		if err != nil {
			return BootstrapResult{}, err
		}
	case rowErr != nil:
		return BootstrapResult{}, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read the existing claim intent", true, "retry once the database is readable", rowErr)
	default:
		// Recovery: the claim row survives with its pinned intent. Re-pin the
		// stored base so the recovery does not move onto a newer tracking
		// head, and skip the preflight a fresh creation owes.
		var branch, path string
		if err := s.db.QueryRowContext(ctx, `SELECT pinned_branch,pinned_path FROM worktree_claims WHERE op_id=?`, operationID).Scan(&branch, &path); err != nil {
			return BootstrapResult{}, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read the existing claim intent", true, "retry once the database is readable", err)
		}
		repo, repoErr := s.ProjectCanonicalPath(ctx, req.ProjectID)
		if repoErr != nil {
			return BootstrapResult{}, repoErr
		}
		location = WorktreeLocation{Branch: branch, BaseSHA: pinnedBase, Path: path, Repo: repo, Ref: req.Ref}
		if err := validateBootstrapDefaultBranch(ctx, ExecGitRunner{}, location.Repo); err != nil {
			return BootstrapResult{}, err
		}
	}
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		return BootstrapResult{}, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read the resume scope", true, "retry the same operation", err)
	}
	defer tx.finish(&retErr)
	if err := validateBootstrapScopeTx(ctx, tx.Tx, req.ProductID, req.ProjectID, declared); err != nil {
		return BootstrapResult{}, err
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, req.WorkID).Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			return BootstrapResult{}, newFailure(KindUnknownScope, "work_resume", "work item does not exist", false, "check the work identity before resuming")
		}
		return BootstrapResult{}, wrapFailure(KindUnavailable, "work_resume", "cannot read the existing work version", true, "retry once the database is readable", err)
	}
	// The scope read closes before the claim opens its own transaction: the
	// store pools one connection, and an open transaction would park the
	// claim on the pool forever. A fresh creation pins its prepared intent
	// in this same read: the claim below reconciles this row instead of
	// treating the operation as another first creation, so the route runs
	// exactly one bounded preflight — the one above that resolved the base —
	// and a retry after an interruption takes the recovery branch with the
	// stored base and owes no new fetch.
	if rowErr == sql.ErrNoRows {
		if err := pinBootstrapClaimTx(ctx, tx.Tx, operationID, req.WorkID, req.ProjectID, location, s.now()); err != nil {
			return BootstrapResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return BootstrapResult{}, wrapFailure(KindUnavailable, "work_bootstrap", "cannot record the resume claim intent", true, "retry the same operation", err)
	}
	claim, err := s.ClaimWorktree(ctx, WorktreeClaimRequest{
		OpID: operationID, WorkID: req.WorkID, ProjectID: req.ProjectID, BaseSHA: location.BaseSHA,
		PrincipalRef: "operator", RequestID: operationID, SessionRef: req.SessionRef,
		ExpectedVersion: version, Runner: ExecGitRunner{},
	})
	if err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{OperationID: operationID, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: req.WorkID, WorkVersion: version + 1, Entry: claim.Entry}, nil
}

// resolveFreshCreationBase resolves a fresh creation's base under one
// coherent bounded preflight: the origin default-branch refresh, the
// default-ref read, the pinned-base probe, and the default-branch checkout
// check share the preflight's fixed deadline (CD-0088 D2). The window closes
// once the base is resolved, before any durable or native creation effect, so
// a refusal here leaves no work item, claim, branch, or worktree behind. An
// omitted ref is default-based and resolves through the tracking ref the
// preflight just refreshed; every caller-supplied ref, including HEAD, stays
// an exact pin.
func (s *Store) resolveFreshCreationBase(ctx context.Context, runner GitRunner, projectID, workID, ref string) (WorktreeLocation, error) {
	repo, err := s.ProjectCanonicalPath(ctx, projectID)
	if err != nil {
		return WorktreeLocation{}, err
	}
	preflight, err := startCreationPreflight(ctx, runner, repo)
	if err != nil {
		return WorktreeLocation{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "origin default branch refresh failed before creation; no work item, claim, branch, or worktree was created", true, "restore access to the origin remote and replay the same request", err)
	}
	location, locateErr := locateWorktree(preflight.ctx, s.db, filepath.Dir(s.Path()), projectID, workID, ref, runner)
	if locateErr == nil {
		locateErr = validateBootstrapDefaultBranch(preflight.ctx, runner, repo)
	}
	deadlineTripped := preflight.tripped(ctx)
	preflight.close()
	if locateErr != nil {
		if deadlineTripped {
			return WorktreeLocation{}, freshnessDeadlineRefusal("work_bootstrap")
		}
		return WorktreeLocation{}, locateErr
	}
	return location, nil
}

func bootstrapOriginHasOpenDispatchWindow(ctx context.Context, tx *sql.Tx, workID string) (bool, error) {
	var open bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM domain_events started
		JOIN domain_events completed ON completed.subject_type=started.subject_type AND completed.subject_id=started.subject_id AND completed.kind=? AND json_extract(completed.payload,'$.action_id')='dispatch_worker' AND completed.seq>started.seq
		WHERE started.subject_type=? AND started.subject_id=? AND started.kind=? AND json_extract(started.payload,'$.action_id')='dispatch_worker'
		AND NOT EXISTS (SELECT 1 FROM domain_events dispatched WHERE dispatched.subject_type=started.subject_type AND dispatched.subject_id=started.subject_id AND dispatched.kind=? AND dispatched.seq>completed.seq AND json_extract(dispatched.payload,'$.attempt_id')=json_extract(completed.payload,'$.worker_attempt_id'))
	)`, WorkerDispatched, string(SubjectWorkItem), workID, WorkflowActionStarted, WorkerDispatched).Scan(&open)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "work_bootstrap", "cannot inspect origin worker attempt windows", true, "retry once the database is readable", err)
	}
	return open, nil
}

func processStartIdentity(pid int64) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.FormatInt(pid, 10) + "/stat")
	if err != nil {
		return "", err
	}
	closeParen := strings.LastIndex(string(data), ")")
	if closeParen < 0 {
		return "", errors.New("process stat has no command boundary")
	}
	fields := strings.Fields(string(data)[closeParen+1:])
	if len(fields) < 20 {
		return "", errors.New("process stat lacks a start identity")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", errors.New("process stat start identity is invalid")
	}
	return fields[19], nil
}

func launchOwnerAlive(pid int64, start string) bool {
	current, err := processStartIdentity(pid)
	return err == nil && current == start
}

type bootstrapPrepared struct {
	Result   BootstrapResult
	State    string
	Location WorktreeLocation
}

// BootstrapPhaseHook is a test seam for failures between durable phases.
type BootstrapPhaseHook func(string) error

// CanonicalBootstrapIdentity returns the stable operation and work IDs for a
// normalized request. The identity is the caller's capture intent: the ref
// the caller named, with an omitted ref staying the empty sentinel, so the
// same caller input hashes identically from any invocation directory. The
// directory-derived default branch applies after the digest, as resolution.
func CanonicalBootstrapIdentity(req BootstrapRequest) (string, string, string, error) {
	if req.Urgency == "" {
		req.Urgency = "standard"
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if req.GoverningRequirements == nil {
		req.GoverningRequirements = []string{}
	}
	// One capture intent hashes identically whether the caller declared an
	// empty related list or omitted it, so the digest names the record's
	// meaning rather than its spelling. The normalization copies the record
	// rather than writing through the caller's pointer.
	req.DefectIntake = normalizedDefectIntake(req.DefectIntake)
	data, err := json.Marshal(req)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return "bootstrap-" + digest[:48], "work-" + digest[:24], "sha256:" + digest, nil
}

// CanonicalExistingBootstrapIdentity returns the stable operation identity for
// the first worktree of one existing work item. The ref is not part of it:
// replay must use the pinned ref recorded by the first operation.
func CanonicalExistingBootstrapIdentity(req ExistingBootstrapRequest) (string, string, string, error) {
	identity := struct {
		Mode      string `json:"mode"`
		ProductID string `json:"product_id"`
		ProjectID string `json:"project_id"`
		WorkID    string `json:"work_id"`
	}{Mode: "existing", ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: req.WorkID}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return "bootstrap-" + digest[:48], req.WorkID, "sha256:" + digest, nil
}

// BootstrapWorktree commits the capture and pinned native intent before it
// invokes git. A pending journal row is safe to replay after any later error.
func (s *Store) BootstrapWorktree(ctx context.Context, req BootstrapRequest, phaseHook BootstrapPhaseHook) (BootstrapResult, error) {
	return s.bootstrapWorktree(ctx, req, phaseHook, ExecGitRunner{})
}

func (s *Store) bootstrapWorktree(ctx context.Context, req BootstrapRequest, phaseHook BootstrapPhaseHook, runner GitRunner) (BootstrapResult, error) {
	if s == nil || s.db == nil {
		return BootstrapResult{}, newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	operationID, workID, digest, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		return BootstrapResult{}, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot derive bootstrap identity", false, "supply JSON-safe input", err)
	}
	operationID, workID, digest, err = s.resolveBootstrapRowIntent(ctx, req, operationID, workID, digest)
	if err != nil {
		return BootstrapResult{}, err
	}
	if req.Ref == "" {
		req.Ref = req.ResolvedRef
	}
	return s.bootstrapWorktreeMode(ctx, req, operationID, workID, digest, false, req, phaseHook, runner)
}

// resolveBootstrapRowIntent reconciles a digest against the row the
// idempotency key already holds before the capture flow runs. The canonical
// identity predates the caller-intent rule for recorded rows: a caller
// capture that differs from the row's stored request only by an omitted ref
// the row holds resolved adopts the row's persisted operation, work, and
// digest identities, so the replay returns the row's original work item and
// worktree. An explicit ref, or any other field change, stays the typed
// input conflict.
func (s *Store) resolveBootstrapRowIntent(ctx context.Context, req BootstrapRequest, operationID, workID, digest string) (string, string, string, error) {
	var rowDigest, rowOperationID, rowWorkID, requestJSON string
	err := s.db.QueryRowContext(ctx, `SELECT request_digest,operation_id,work_id,request_json FROM bootstrap_operations WHERE idempotency_key=?`, req.IdempotencyKey).Scan(&rowDigest, &rowOperationID, &rowWorkID, &requestJSON)
	if err == sql.ErrNoRows {
		return operationID, workID, digest, nil
	}
	if err != nil {
		return "", "", "", wrapFailure(KindUnavailable, "work_bootstrap", "cannot read bootstrap journal", true, "retry once the database is readable", err)
	}
	if rowDigest == digest {
		return operationID, workID, digest, nil
	}
	if !bootstrapRowMatchesCallerIntent(requestJSON, req) {
		return "", "", "", newFailure(KindInvalidOperation, "work_bootstrap", "idempotency key is bound to different input", false, "use the original request or a new idempotency key")
	}
	return rowOperationID, rowWorkID, rowDigest, nil
}

// bootstrapRowMatchesCallerIntent reports that the caller's capture intent
// matches a persisted row's stored request in every canonical field, with
// the ref the only difference and the caller's ref omitted. The row holds
// the ref its original invocation directory resolved, so an identical
// capture replayed from another directory stays the same operation. The
// comparison marshals both sides, which carries exactly the digest-bearing
// field set and excludes SessionRef, HostPID, and ResolvedRef.
func bootstrapRowMatchesCallerIntent(requestJSON string, req BootstrapRequest) bool {
	if req.Ref != "" {
		return false
	}
	var stored BootstrapRequest
	if err := json.Unmarshal([]byte(requestJSON), &stored); err != nil {
		return false
	}
	caller, row := req, stored
	caller.Ref, row.Ref = "", ""
	if caller.Urgency == "" {
		caller.Urgency = "standard"
	}
	if row.Urgency == "" {
		row.Urgency = "standard"
	}
	if caller.Tags == nil {
		caller.Tags = []string{}
	}
	if row.Tags == nil {
		row.Tags = []string{}
	}
	if caller.GoverningRequirements == nil {
		caller.GoverningRequirements = []string{}
	}
	if row.GoverningRequirements == nil {
		row.GoverningRequirements = []string{}
	}
	caller.DefectIntake = normalizedDefectIntake(caller.DefectIntake)
	row.DefectIntake = normalizedDefectIntake(row.DefectIntake)
	callerData, err := json.Marshal(caller)
	if err != nil {
		return false
	}
	rowData, err := json.Marshal(row)
	if err != nil {
		return false
	}
	return string(callerData) == string(rowData)
}

func (s *Store) bootstrapWorktreeMode(ctx context.Context, req BootstrapRequest, operationID, workID, digest string, existing bool, journalRequest any, phaseHook BootstrapPhaseHook, runner GitRunner) (BootstrapResult, error) {
	if s == nil || s.db == nil {
		return BootstrapResult{}, newFailure(KindUnavailable, "work_bootstrap", "store is not open", false, "open the authority database")
	}
	if !existing {
		if err := validateBootstrapRequest(req); err != nil {
			return BootstrapResult{}, err
		}
	} else if err := validateExistingBootstrapRequest(ExistingBootstrapRequest{ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, Ref: req.Ref}); err != nil {
		return BootstrapResult{}, err
	}
	if req.Urgency == "" {
		req.Urgency = "standard"
	}
	// An omitted ref stays omitted through the location read: locateWorktree
	// resolves it as default-based — the fetched origin default head on a
	// fresh creation, whose preflight just refreshed the tracking cache —
	// and every caller-supplied ref, including HEAD, stays an exact pin.
	location, existingState, dbExisting, err := s.pinnedBootstrapLocation(ctx, req.IdempotencyKey, digest, operationID)
	if err != nil {
		return BootstrapResult{}, err
	}
	if existingState == "rolling_back" {
		if err := s.rollbackBootstrap(ctx, operationID, workID, location, ExecGitRunner{}, errors.New("resume interrupted bootstrap rollback")); err != nil {
			return BootstrapResult{}, err
		}
		return BootstrapResult{}, newFailure(KindInvalidOperation, "work_bootstrap", "bootstrap operation was rolled back", false, "use a new idempotency key")
	}
	if !dbExisting {
		// The fresh creation owes its base the shared Git preflight: the
		// bounded, noninteractive fetch of the registered origin default
		// branch and the base-resolution probes run under one fixed deadline,
		// outside every transaction (CD-0195 D2) and before the capture, so a
		// refusal leaves no work item, claim, branch, or worktree. A
		// caller-pinned ref stays an exact pin: the fetch refreshes the
		// cache, the pin does not move.
		location, err = s.resolveFreshCreationBase(ctx, runner, req.ProjectID, workID, req.Ref)
		if err != nil {
			return BootstrapResult{}, err
		}
	} else if err := validateBootstrapDefaultBranch(ctx, runner, location.Repo); err != nil {
		return BootstrapResult{}, err
	}
	prepared, err := s.prepareBootstrapMode(ctx, req, operationID, workID, digest, existing, journalRequest, location, runner)
	if err != nil {
		return BootstrapResult{}, err
	}
	location = prepared.Location
	if phaseHook != nil {
		if err := phaseHook("after_prepare"); err != nil {
			return BootstrapResult{}, err
		}
	}
	if prepared.Result.Replayed && prepared.Result.Entry.State == worktreeEntryActive {
		return prepared.Result, nil
	}
	if prepared.State == "pending" {
		if err := s.setBootstrapState(ctx, operationID, "pending", "creating"); err != nil {
			return BootstrapResult{}, err
		}
	}

	facts, err := reconcileBootstrapNative(ctx, runner, location.Repo, location, prepared.Result.Replayed)
	if err != nil {
		return BootstrapResult{}, err
	}
	if err := s.setBootstrapState(ctx, operationID, "creating", "native_ready"); err != nil {
		return BootstrapResult{}, err
	}
	if phaseHook != nil {
		if err := phaseHook("after_native_create"); err != nil {
			return BootstrapResult{}, err
		}
	}
	result, err := s.finalizeBootstrap(ctx, req, operationID, workID, location, facts)
	if err != nil {
		return BootstrapResult{}, err
	}
	result.Replayed = prepared.Result.Replayed
	return result, nil
}

// rollbackBootstrap removes only native state that still matches the pinned
// intent, then closes the durable claim and operation in one transaction.
func (s *Store) rollbackBootstrap(ctx context.Context, operationID, workID string, location WorktreeLocation, runner GitRunner, cause error) error {
	now := s.now()
	done := false
	err := s.TransactDurable(ctx, func(transaction *Transaction) error {
		return beginBootstrapRollbackTx(ctx, transaction, operationID, workID, now, cause.Error(), &done)
	})
	if err != nil || done {
		return err
	}
	if err := removeBootstrapWorktree(ctx, runner, location); err != nil {
		return err
	}
	if branchSHA, exists, err := bootstrapBranchHead(ctx, runner, location.Repo, location.Branch); err != nil {
		return err
	} else if exists {
		if branchSHA != location.BaseSHA {
			return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap branch contains changes and cannot be removed", false, "preserve the branch and contact_operator")
		}
		if attached, err := branchAttachedElsewhere(ctx, runner, location.Repo, location.Branch); err != nil {
			return err
		} else if attached {
			return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap branch is attached to another worktree", false, "detach the exact pinned branch before retrying")
		}
		if err := deleteBootstrapBranch(ctx, runner, location.Repo, location.Branch, location.BaseSHA); err != nil {
			return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot remove the failed bootstrap branch", false, "remove the exact pinned branch before retrying", err)
		}
	}
	err = s.TransactDurable(ctx, func(transaction *Transaction) error {
		return rollbackBootstrapTx(ctx, transaction, operationID, workID, now, cause.Error())
	})
	if err != nil {
		return wrapFailure(KindUnavailable, "work_bootstrap", "cannot record bootstrap rollback", false, "restore the authority database before retrying", err)
	}
	return nil
}

func removeBootstrapWorktree(ctx context.Context, runner GitRunner, location WorktreeLocation) error {
	if _, err := os.Lstat(location.Path); errors.Is(err, os.ErrNotExist) {
		ownerPID := int64(os.Getpid())
		ownerStart, ownerErr := processStartIdentity(ownerPID)
		if ownerErr != nil {
			return ownerErr
		}
		lockPath, lockErr := acquireBootstrapGitPath(ctx, runner, location.Repo, "refs/heads/"+location.Branch, ownerPID, ownerStart)
		if lockErr != nil {
			return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap branch has an active Git lock", false, "retry after the other Git operation completes")
		}
		_ = os.Remove(lockPath)
		return nil
	} else if err != nil {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the failed bootstrap worktree path", false, "restore repository access before retrying", err)
	}
	unlock, err := lockBootstrapWorktreeRefs(ctx, runner, location)
	if err != nil {
		return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap worktree changed while rollback acquired its Git locks", false, "preserve the worktree and retry after the other Git operation completes")
	}
	defer unlock()
	ok, facts, err := probeWorktree(ctx, runner, location.Repo, location.Path, location.Branch, location.BaseSHA)
	if err != nil {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot compensate the failed bootstrap", false, "restore repository access before retrying", err)
	}
	if !ok {
		return nil
	}
	if facts.headSHA != location.BaseSHA {
		return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap worktree contains changes and cannot be removed", false, "preserve the worktree and contact_operator")
	}
	status, err := runner.Run(ctx, location.Path, "status", "--porcelain")
	if err != nil {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the failed bootstrap worktree", false, "restore repository access before retrying", err)
	}
	if len(status) != 0 {
		return newFailure(KindProjectionConflict, "work_bootstrap", "failed bootstrap worktree is dirty and cannot be removed", false, "preserve the worktree and contact_operator")
	}
	if _, err := runner.Run(ctx, location.Repo, "worktree", "remove", location.Path); err != nil {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot remove the failed bootstrap worktree", false, "remove the exact pinned worktree before retrying", err)
	}
	return nil
}

func lockBootstrapWorktreeRefs(ctx context.Context, runner GitRunner, location WorktreeLocation) (func(), error) {
	ownerPID := int64(os.Getpid())
	ownerStart, err := processStartIdentity(ownerPID)
	if err != nil {
		return nil, err
	}
	targets := []struct {
		directory string
		gitPath   string
	}{
		{directory: location.Repo, gitPath: "refs/heads/" + location.Branch},
		{directory: location.Path, gitPath: "HEAD"},
	}
	locked := make([]string, 0, len(targets))
	unlock := func() {
		for index := len(locked) - 1; index >= 0; index-- {
			_ = os.Remove(locked[index])
		}
	}
	for _, target := range targets {
		lockPath, err := acquireBootstrapGitPath(ctx, runner, target.directory, target.gitPath, ownerPID, ownerStart)
		if err != nil {
			unlock()
			return nil, err
		}
		locked = append(locked, lockPath)
	}
	return unlock, nil
}

func acquireBootstrapGitPath(ctx context.Context, runner GitRunner, directory, gitPath string, ownerPID int64, ownerStart string) (string, error) {
	output, err := runner.Run(ctx, directory, "rev-parse", "--git-path", gitPath)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(output))
	if !filepath.IsAbs(path) {
		path = filepath.Join(directory, path)
	}
	lockPath := filepath.Clean(path) + ".lock"
	if err := acquireBootstrapGitLock(lockPath, ownerPID, ownerStart); err != nil {
		return "", err
	}
	return lockPath, nil
}

func acquireBootstrapGitLock(lockPath string, ownerPID int64, ownerStart string) error {
	marker := []byte("concord-bootstrap-lock-v1\n" + strconv.FormatInt(ownerPID, 10) + "\n" + ownerStart + "\n")
	for attempt := 0; attempt < 4; attempt++ {
		temporary, err := os.CreateTemp(filepath.Dir(lockPath), filepath.Base(lockPath)+".concord-*")
		if err != nil {
			return err
		}
		temporaryPath := temporary.Name()
		if _, err = temporary.Write(marker); err == nil {
			err = temporary.Sync()
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(temporaryPath)
			return err
		}
		err = os.Link(temporaryPath, lockPath)
		_ = os.Remove(temporaryPath)
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		reclaimed, reclaimErr := reclaimStaleBootstrapGitLock(lockPath)
		if reclaimErr != nil {
			if errors.Is(reclaimErr, os.ErrNotExist) {
				continue
			}
			return reclaimErr
		}
		if !reclaimed {
			return err
		}
	}
	return os.ErrExist
}

func reclaimStaleBootstrapGitLock(lockPath string) (bool, error) {
	// #nosec G304 -- lockPath is a Git metadata path derived by the bootstrap lock owner.
	file, err := os.Open(lockPath)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return false, err
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	openedInfo, err := file.Stat()
	if err != nil {
		return false, err
	}
	pathInfo, err := os.Stat(lockPath)
	if err != nil {
		return false, err
	}
	if !os.SameFile(openedInfo, pathInfo) {
		return false, os.ErrNotExist
	}
	content, err := io.ReadAll(io.LimitReader(file, 257))
	if err != nil {
		return false, err
	}
	if len(content) > 256 {
		return false, nil
	}
	parts := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(parts) != 3 || parts[0] != "concord-bootstrap-lock-v1" {
		return false, nil
	}
	ownerPID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || launchOwnerAlive(ownerPID, parts[2]) {
		return false, nil
	}
	currentInfo, err := os.Stat(lockPath)
	if err != nil {
		return false, err
	}
	if !os.SameFile(openedInfo, currentInfo) {
		return false, os.ErrNotExist
	}
	if err := os.Remove(lockPath); err != nil {
		return false, err
	}
	return true, nil
}

func deleteBootstrapBranch(ctx context.Context, runner GitRunner, repo, branch, expectedSHA string) error {
	hookDir, err := os.MkdirTemp("", "concord-bootstrap-ref-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(hookDir) }()
	if err := os.WriteFile(filepath.Join(hookDir, "expected-sha"), []byte(expectedSHA+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(hookDir, "expected-ref"), []byte("refs/heads/"+branch+"\n"), 0o600); err != nil {
		return err
	}
	hook := `#!/bin/sh
set -eu
[ "$1" = "prepared" ] || exit 0
dir=${0%/*}
IFS= read -r expected_sha < "$dir/expected-sha"
IFS= read -r expected_ref < "$dir/expected-ref"
observed_sha=$(git rev-parse --verify "$expected_ref^{commit}" 2>/dev/null || true)
[ "$observed_sha" = "$expected_sha" ] || exit 1
git worktree list --porcelain > "$dir/worktrees"
while IFS= read -r line; do
  if [ "$line" = "branch $expected_ref" ]; then
    exit 1
  fi
done < "$dir/worktrees"
`
	hookPath := filepath.Join(hookDir, "reference-transaction")
	// #nosec G306 -- Git hooks must be executable by the repository owner.
	if err := os.WriteFile(hookPath, []byte(hook), 0o700); err != nil {
		return err
	}
	output, err := runner.Run(ctx, repo, "-c", "core.hooksPath="+hookDir, "branch", "-d", "--", branch)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func beginBootstrapRollbackTx(ctx context.Context, transaction *Transaction, operationID, workID string, now time.Time, reason string, done *bool) error {
	*done = false
	tx, err := transactionSQL(transaction, "work_bootstrap")
	if err != nil {
		return err
	}
	var state, storedWork string
	if err := tx.QueryRowContext(ctx, `SELECT state,work_id FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state, &storedWork); err != nil {
		return err
	}
	if storedWork != workID {
		return newFailure(KindInvariantViolation, "work_bootstrap", "rollback work identity differs from the bootstrap operation", false, "use the exact bootstrap operation")
	}
	if state == "rolled_back" {
		*done = true
		return nil
	}
	if state == "rolling_back" {
		return nil
	}
	if state != "completed" {
		return newFailure(KindOperationConflict, "work_bootstrap", "bootstrap operation is not ready for rollback", false, "reconcile the bootstrap operation")
	}
	result, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='rolling_back',failure_reason=?,updated_at=? WHERE operation_id=? AND state='completed'`, reason, now.Format(time.RFC3339Nano), operationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return newFailure(KindOperationConflict, "work_bootstrap", "bootstrap rollback could not acquire exclusive state", false, "reconcile the bootstrap operation")
	}
	return nil
}

func rollbackBootstrapTx(ctx context.Context, transaction *Transaction, operationID, workID string, now time.Time, reason string) error {
	tx, err := transactionSQL(transaction, "work_bootstrap")
	if err != nil {
		return err
	}
	var state string
	var expected int64
	if err := tx.QueryRowContext(ctx, `SELECT state,expected_version FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state, &expected); err != nil {
		return err
	}
	if state == "rolled_back" {
		return nil
	}
	if state != "rolling_back" {
		return newFailure(KindOperationConflict, "work_bootstrap", "bootstrap rollback lost its exclusive state", false, "reconcile the bootstrap operation")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state='reclaimed',updated_at=? WHERE op_id=? AND state IN ('pending','verified')`, now.Format(time.RFC3339Nano), operationID); err != nil {
		return err
	}
	var lifecycle string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle,version FROM work_items WHERE id=?`, workID).Scan(&lifecycle, &version); err != nil {
		return err
	}
	if lifecycle == "needed" || lifecycle == "in_progress" {
		payload, marshalErr := json.Marshal(workTransitionPayload{From: lifecycle, To: "cancelled", Reason: "bootstrap rolled back: " + reason, ExpectedVersion: version, ResultingVersion: version + 1})
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{EventID: operationID + ":rolled-back", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}, newFoldScope(tx), false); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='rolled_back',failure_reason=?,updated_at=? WHERE operation_id=?`, reason, now.Format(time.RFC3339Nano), operationID)
	return err
}

func (s *Store) pinnedBootstrapLocation(ctx context.Context, idempotencyKey, digest, operationID string) (WorktreeLocation, string, bool, error) {
	var out WorktreeLocation
	var storedDigest, repo, state string
	err := s.db.QueryRowContext(ctx, `SELECT request_digest,repo_path,state FROM bootstrap_operations WHERE idempotency_key=?`, idempotencyKey).Scan(&storedDigest, &repo, &state)
	if err == sql.ErrNoRows {
		return out, "", false, nil
	}
	if err != nil {
		return out, "", false, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read bootstrap journal", true, "retry once the database is readable", err)
	}
	if storedDigest != digest {
		return out, "", false, newFailure(KindInvalidOperation, "work_bootstrap", "idempotency key is bound to different input", false, "use the original request or a new idempotency key")
	}
	if state == "rolled_back" {
		return out, state, false, newFailure(KindInvalidOperation, "work_bootstrap", "bootstrap operation was rolled back", false, "use a new idempotency key")
	}
	var branch, base, path string
	if err := s.db.QueryRowContext(ctx, `SELECT pinned_branch,pinned_base_sha,pinned_path FROM worktree_claims WHERE op_id=?`, operationID).Scan(&branch, &base, &path); err != nil {
		return out, "", false, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no pinned worktree intent", false, "contact_operator", err)
	}
	return WorktreeLocation{Branch: branch, BaseSHA: base, Path: path, Repo: repo}, state, true, nil
}

func validateBootstrapRequest(req BootstrapRequest) error {
	for name, value := range map[string]string{"product_id": req.ProductID, "project_id": req.ProjectID, "idempotency_key": req.IdempotencyKey, "kind": req.Kind} {
		if !bootstrapIDPattern.MatchString(value) || len(value) > 128 {
			return newFailure(KindInvalidOperation, "work_bootstrap", name+" is not a valid bounded identifier", false, "supply an identifier with letters, digits, and _ . : -")
		}
	}
	for name, value := range map[string]string{"title": req.Title, "value_statement": req.ValueStatement, "external_ref": req.ExternalRef} {
		if value == "" && name != "external_ref" || len(value) > 256 || strings.ContainsRune(value, '\x00') || !utf8.ValidString(value) {
			return newFailure(KindInvalidOperation, "work_bootstrap", name+" is empty, too long, or contains NUL", false, "supply bounded prose")
		}
	}
	if req.Task == "" || len(req.Task) > 8192 || strings.ContainsRune(req.Task, '\x00') || !utf8.ValidString(req.Task) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "task is empty, too long, or contains NUL", false, "supply bounded UTF-8 task text")
	}
	if req.Kind != "task" && req.Kind != "bug" && req.Kind != "decision" && req.Kind != "research" && req.Kind != "other" {
		return newFailure(KindInvalidOperation, "work_bootstrap", "kind is not a declared work kind", false, "use task, bug, decision, research, or other")
	}
	if req.Urgency != "" && req.Urgency != "standard" && req.Urgency != "expedite" {
		return newFailure(KindInvalidOperation, "work_bootstrap", "urgency is not recognized", false, "use standard or expedite")
	}
	if req.Priority < -100 || req.Priority > 100 || len(req.Tags) > 32 || len(req.GoverningRequirements) > 32 {
		return newFailure(KindInvalidOperation, "work_bootstrap", "priority or list field exceeds its bound", false, "use the declared field bounds")
	}
	for _, values := range [][]string{req.Tags, req.GoverningRequirements} {
		seen := map[string]bool{}
		for _, tag := range values {
			if !bootstrapIDPattern.MatchString(tag) || seen[tag] {
				return newFailure(KindInvalidOperation, "work_bootstrap", "tag or governing requirement is invalid or duplicated", false, "supply unique bounded identifiers")
			}
			seen[tag] = true
		}
	}
	if req.WorkflowTypeRef != "" && !bootstrapIDPattern.MatchString(req.WorkflowTypeRef) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "workflow_type_ref is not a valid identifier", false, "supply a bounded workflow reference")
	}
	if req.RaisedFromWorkID != "" && !bootstrapIDPattern.MatchString(req.RaisedFromWorkID) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "raised_from_work_id is not a valid identifier", false, "supply a bounded work identifier")
	}
	// The defect intake record validates before the journal, the claim, and
	// every native Git effect, so a malformed record refuses with no effect.
	if err := ValidateDefectIntake(req.Kind, req.DefectIntake); err != nil {
		return err
	}
	if req.Ref != "" && (len(req.Ref) > 128 || strings.HasPrefix(req.Ref, "-") || strings.ContainsAny(req.Ref, " \t\n\r\x00")) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "ref is not a bounded rev-syntax value", false, "supply one safe repository ref")
	}
	return nil
}

func validateExistingBootstrapRequest(req ExistingBootstrapRequest) error {
	for name, value := range map[string]string{"product_id": req.ProductID, "project_id": req.ProjectID, "work_id": req.WorkID} {
		if !bootstrapIDPattern.MatchString(value) || len(value) > 128 {
			return newFailure(KindInvalidOperation, "work_bootstrap", name+" is not a valid bounded identifier", false, "supply an identifier with letters, digits, and _ . : -")
		}
	}
	if req.Ref != "" && (len(req.Ref) > 128 || strings.HasPrefix(req.Ref, "-") || strings.ContainsAny(req.Ref, " \t\n\r\x00")) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "ref is not a bounded rev-syntax value", false, "supply one safe repository ref")
	}
	return nil
}

// bootstrapJournalProbe is the pre-transaction observation of the bootstrap
// journal and its pinned claim (CD-0195 D2). journalErr keeps the tri-state
// of the journal read — nil, sql.ErrNoRows, or a transient error the
// transaction re-read re-checks.
type bootstrapJournalProbe struct {
	journalErr   error
	digest       string
	state        string
	claimState   string
	location     WorktreeLocation
	branchSHA    string
	branchExists bool
}

// probeBootstrapJournal observes the journal row, the claim row a completed
// replay reads, and every git probe with no write transaction open. A fresh
// journal probes that the canonical path and branch are absent; a reopen of a
// reclaimed claim re-pins the branch head.
func (s *Store) probeBootstrapJournal(ctx context.Context, runner GitRunner, req BootstrapRequest, operationID string, location WorktreeLocation) (bootstrapJournalProbe, error) {
	var probe bootstrapJournalProbe
	probe.location = location
	probe.journalErr = s.db.QueryRowContext(ctx, `SELECT request_digest,state FROM bootstrap_operations WHERE idempotency_key=?`, req.IdempotencyKey).Scan(&probe.digest, &probe.state)
	if probe.journalErr == nil {
		if err := s.db.QueryRowContext(ctx, `SELECT b.repo_path,c.pinned_branch,c.pinned_base_sha,c.pinned_path,c.state FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.operation_id=?`, operationID).Scan(&probe.location.Repo, &probe.location.Branch, &probe.location.BaseSHA, &probe.location.Path, &probe.claimState); err != nil {
			return probe, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no pinned worktree intent", false, "contact_operator", err)
		}
	}
	if probe.journalErr == sql.ErrNoRows {
		if err := validateBootstrapNativeAbsent(ctx, runner, location); err != nil {
			return probe, err
		}
		return probe, nil
	}
	if probe.journalErr == nil && probe.state == "completed" && probe.claimState == worktreeStateReclaimed {
		sha, exists, headErr := bootstrapBranchHead(ctx, runner, probe.location.Repo, probe.location.Branch)
		if headErr != nil {
			return probe, headErr
		}
		probe.branchSHA, probe.branchExists = sha, exists
	}
	return probe, nil
}

func (s *Store) prepareBootstrapMode(ctx context.Context, req BootstrapRequest, operationID, workID, digest string, existing bool, journalRequest any, location WorktreeLocation, runner GitRunner) (_ bootstrapPrepared, retErr error) {
	var out bootstrapPrepared
	// The pre-transaction observation (CD-0195 D2) runs in
	// probeBootstrapJournal. The transaction re-reads both rows and refuses
	// an observation that drifted.
	probe, probeErr := s.probeBootstrapJournal(ctx, runner, req, operationID, location)
	if probeErr != nil {
		return out, probeErr
	}
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot begin bootstrap journal", true, "retry the same operation", err)
	}
	defer tx.finish(&retErr)
	var storedDigest, state string
	replayed := false
	restarted := false
	var journalMissing bool
	expectedVersion := int64(0)
	err = tx.QueryRowContext(ctx, `SELECT request_digest,state FROM bootstrap_operations WHERE idempotency_key=?`, req.IdempotencyKey).Scan(&storedDigest, &state)
	switch {
	case probe.journalErr == sql.ErrNoRows && err != nil && err != sql.ErrNoRows,
		probe.journalErr == nil && (err != nil || storedDigest != probe.digest || state != probe.state):
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "bootstrap journal changed under the prepare probe", true, "retry the same idempotency key", err)
	}
	journalMissing = err == sql.ErrNoRows
	if err == nil {
		replayed = true
		if storedDigest != digest {
			return out, newFailure(KindInvalidOperation, "work_bootstrap", "idempotency key is bound to different input", false, "use the original request or a new idempotency key")
		}
		var claimState string
		if err := tx.QueryRowContext(ctx, `SELECT b.repo_path,c.pinned_branch,c.pinned_base_sha,c.pinned_path,c.state FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.operation_id=?`, operationID).Scan(&location.Repo, &location.Branch, &location.BaseSHA, &location.Path, &claimState); err != nil {
			return out, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no pinned worktree intent", false, "contact_operator", err)
		}
		if location != probe.location || claimState != probe.claimState {
			return out, wrapFailure(KindUnavailable, "work_bootstrap", "bootstrap claim changed under the prepare probe", true, "retry the same idempotency key", nil)
		}
		if state == "completed" {
			switch claimState {
			case worktreeStatePending, worktreeStateVerified:
				replay, err := replayCompletedBootstrapTx(ctx, tx.Tx, req, operationID, workID, state, location)
				if err != nil {
					return out, err
				}
				return replay, tx.Commit()
			case worktreeStateReclaimed:
				location, expectedVersion, err = reopenReclaimedBootstrapStoreTx(ctx, tx.Tx, operationID, workID, location, probe.branchSHA, probe.branchExists, s.Clock)
				if err != nil {
					return out, err
				}
				state = "pending"
				restarted = true
				err = sql.ErrNoRows
			default:
				return out, newFailure(KindInvariantViolation, "work_bootstrap", "completed bootstrap journal has an invalid claim state", false, "contact_operator")
			}
		}
	} else if err != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read bootstrap journal", true, "retry once the database is readable", err)
	}

	if err == sql.ErrNoRows {
		state = "pending"
		if existing {
			replay, handled, existingErr := replayExistingBootstrapTx(ctx, tx.Tx, req, workID)
			if existingErr != nil {
				return out, existingErr
			}
			if handled {
				return replay, tx.Commit()
			}
		}
		if existing && !restarted {
			var priorOperation bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bootstrap_operations WHERE work_id=?)`, workID).Scan(&priorOperation); err != nil {
				return out, err
			}
			if priorOperation {
				return out, newFailure(KindProjectionConflict, "work_bootstrap", "existing work item has a bootstrap operation for another Project or Product", false, "resume with the original Project and Product")
			}
		}
		if err := validateBootstrapScopeTx(ctx, tx.Tx, req.ProductID, req.ProjectID, req.GoverningRequirements); err != nil {
			return out, err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE id=?)`, workID).Scan(&exists); err != nil {
			return out, err
		}
		if existing {
			if !exists {
				return out, newFailure(KindUnknownScope, "work_bootstrap", "existing work item does not exist", false, "check the work identity before starting")
			}
			var lifecycle string
			if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil {
				return out, err
			}
			if isTerminalLifecycle(lifecycle) {
				return out, newFailure(KindInvalidOperation, "work_bootstrap", "cannot bootstrap terminal work item "+workID+" ("+lifecycle+")", false, "start live work or capture new work instead")
			}
			var member bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_projects WHERE work_id=? AND project_id=?)`, workID, req.ProjectID).Scan(&member); err != nil {
				return out, err
			}
			if !member {
				return out, newFailure(KindUnknownScope, "work_bootstrap", "work item does not hold Project "+req.ProjectID, false, "start from a Project the work item belongs to")
			}
			if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&expectedVersion); err != nil {
				return out, err
			}
		} else if exists && !restarted {
			return out, newFailure(KindProjectionConflict, "work_bootstrap", "derived work ID already exists without its journal", false, "use a new idempotency key")
		}
		now := s.now()
		if !existing && !restarted {
			priority := req.Priority
			workPayload, _ := json.Marshal(workCreatedPayload{WorkID: workID, WorkKind: req.Kind, Title: req.Title, Task: req.Task, ValueStatement: req.ValueStatement, Priority: &priority, Urgency: req.Urgency, Tags: req.Tags, WorkflowTypeRef: req.WorkflowTypeRef, ExternalRef: req.ExternalRef, RaisedFromWorkID: req.RaisedFromWorkID, DefectIntake: req.DefectIntake})
			membershipPayload, _ := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{{ProjectID: req.ProjectID, Role: "primary"}}, ExpectedVersion: 1, ResultingVersion: 2})
			events := []Event{
				{EventID: operationID + ":work-created", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 3, Payload: workPayload},
				{EventID: operationID + ":memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
			}
			if _, err := applyOperationTx(ctx, tx.Tx, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}, newFoldScope(tx.Tx), false); err != nil {
				return out, err
			}
			// Session-prepare reads C19 continuity unconditionally, so a
			// captured work item pins a workflow instance: a capture that
			// names no workflow_type_ref (the common case, CD-0035) pins the
			// kind-driven default instead of skipping initialization (#650).
			// Imported work items stay instance-less; continuity answers for
			// them with typed workflow absence.
			workflowRef := req.WorkflowTypeRef
			if workflowRef == "" {
				workflowRef = DefaultWorkflowRefForKind(req.Kind)
			}
			definition, definitionErr := BuiltinWorkflowDefinitionForRef(workflowRef)
			if definitionErr != nil {
				return out, definitionErr
			}
			actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/" + operationID, ActorClass: ActorOperator}
			if err := InitializeWorkflowTx(ctx, &Transaction{tx: tx.Tx, clock: s.Clock}, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: now}); err != nil {
				return out, err
			}
			expectedVersion = 4
		}
		if journalMissing {
			if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, req.IdempotencyKey, operationID, digest, bootstrapJSON(journalRequest), req.ProductID, req.ProjectID, workID, location.Repo, expectedVersion, "pending", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
				return out, err
			}
			if err := pinBootstrapClaimTx(ctx, tx.Tx, operationID, workID, req.ProjectID, location, now); err != nil {
				return out, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot commit bootstrap journal", true, "retry the same idempotency key", err)
	}
	return bootstrapPrepared{Result: BootstrapResult{OperationID: operationID, Replayed: replayed, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID}, State: state, Location: location}, nil
}

func replayCompletedBootstrapTx(ctx context.Context, tx *sql.Tx, req BootstrapRequest, operationID, workID, state string, location WorktreeLocation) (bootstrapPrepared, error) {
	entry, err := worktreeEntryByClaim(ctx, tx, operationID)
	if err != nil {
		return bootstrapPrepared{}, err
	}
	version, err := workVersionTx(ctx, tx, workID)
	if err != nil {
		return bootstrapPrepared{}, err
	}
	return bootstrapPrepared{Result: BootstrapResult{OperationID: operationID, Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, State: state, Location: location}, nil
}

func replayExistingBootstrapTx(ctx context.Context, tx *sql.Tx, req BootstrapRequest, workID string) (bootstrapPrepared, bool, error) {
	entry, activeErr := activeWorktreeEntryForProject(ctx, tx, "work_bootstrap", workID, req.ProjectID)
	if activeErr == nil {
		version, versionErr := workVersionTx(ctx, tx, workID)
		if versionErr != nil {
			return bootstrapPrepared{}, false, versionErr
		}
		return bootstrapPrepared{Result: BootstrapResult{Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, State: "completed", Location: WorktreeLocation{Branch: entry.Branch, BaseSHA: entry.BaseSHA, Path: entry.Path}}, true, nil
	}
	var activeFailure *Failure
	if !errors.As(activeErr, &activeFailure) || activeFailure.Kind != KindProjectionNotFound {
		return bootstrapPrepared{}, false, activeErr
	}
	return bootstrapPrepared{}, false, nil
}

// reopenReclaimedBootstrapStoreTx is the SQL half of a reclaimed bootstrap
// reopen: it re-pins the branch head the pre-transaction git probe read and
// starts a new claim incarnation. It runs SQL only (CD-0195 D2).
func reopenReclaimedBootstrapStoreTx(ctx context.Context, tx *sql.Tx, operationID, workID string, location WorktreeLocation, branchSHA string, branchExists bool, clock func() time.Time) (WorktreeLocation, int64, error) {
	version, err := workVersionTx(ctx, tx, workID)
	if err != nil {
		return location, 0, err
	}
	if branchExists {
		location.BaseSHA = branchSHA
	}
	now := nowFromClock(clock).Format(time.RFC3339Nano)
	if result, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='pending',expected_version=?,updated_at=? WHERE operation_id=? AND state='completed'`, version, now, operationID); err != nil {
		return location, 0, err
	} else if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return location, 0, newFailure(KindInvariantViolation, "work_bootstrap", "completed bootstrap journal could not be reopened", false, "contact_operator")
	}
	// The reopen starts a new incarnation of this claim row: the bump scopes
	// the incarnation's occupancy-release and reclaim events to their own
	// event identities instead of re-deriving the first incarnation's.
	if result, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET pinned_base_sha=?,state='pending',incarnation=incarnation+1,updated_at=? WHERE op_id=? AND state='reclaimed'`, location.BaseSHA, now, operationID); err != nil {
		return location, 0, err
	} else if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
		return location, 0, newFailure(KindInvariantViolation, "work_bootstrap", "reclaimed bootstrap claim could not be reopened", false, "contact_operator")
	}
	return location, version, nil
}

func (s *Store) setBootstrapState(ctx context.Context, operationID, from, to string) (retErr error) {
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		return wrapFailure(KindUnavailable, "work_bootstrap", "cannot begin bootstrap phase record", true, "retry the same operation", err)
	}
	defer tx.finish(&retErr)
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state); err != nil {
		return err
	}
	if state == to || state == "native_ready" || state == "completed" {
		return tx.Commit()
	}
	if state != from {
		return newFailure(KindInvalidOperation, "work_bootstrap", "bootstrap journal phase conflicts with the requested transition", true, "retry the exact operation")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state=?,updated_at=? WHERE operation_id=? AND state=?`, to, s.now().Format(time.RFC3339Nano), operationID, from); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "work_bootstrap", "cannot commit bootstrap phase record", true, "retry the same operation", err)
	}
	return nil
}

func reconcileBootstrapNative(ctx context.Context, runner GitRunner, repo string, location WorktreeLocation, allowExisting bool) (worktreeFacts, error) {
	if ok, facts, err := probeWorktree(ctx, runner, repo, location.Path, location.Branch, location.BaseSHA); err != nil {
		return worktreeFacts{}, err
	} else if ok {
		if !allowExisting {
			return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "fresh bootstrap found a pre-existing canonical worktree", false, "resolve the conflicting native state and use a new idempotency key")
		}
		if facts.headSHA != location.BaseSHA {
			return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "existing canonical worktree has commits after the pinned base", false, "resolve the conflicting native state without adopting it")
		}
		return facts, nil
	}
	if _, err := os.Lstat(location.Path); err == nil {
		return worktreeFacts{}, newFailure(KindInvalidOperation, "work_bootstrap", "expected worktree path conflicts with the pinned intent", false, "resolve the conflicting path without removing native resources")
	} else if !os.IsNotExist(err) {
		return worktreeFacts{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the expected worktree path", true, "restore access to the worktree path", err)
	}
	branchSHA, branchExists, err := bootstrapBranchHead(ctx, runner, repo, location.Branch)
	if err != nil {
		return worktreeFacts{}, err
	}
	if !branchExists {
		if _, err := runner.Run(ctx, repo, "worktree", "add", location.Path, "-b", location.Branch, location.BaseSHA); err != nil {
			if allowExisting {
				return retryBootstrapProbe(ctx, runner, repo, location, err)
			}
			retrySafe := gitFailureRetrySafe(err)
			return worktreeFacts{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "native worktree creation failed; the recorded operation is safe to replay", retrySafe, "retry the same idempotency key", err)
		}
		return verifyBootstrapNative(ctx, runner, repo, location)
	}
	if !allowExisting {
		return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "fresh bootstrap found a pre-existing canonical branch", false, "resolve the conflicting native state and use a new idempotency key")
	}
	if branchSHA != location.BaseSHA {
		return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "existing branch does not match the pinned base", false, "resolve the conflicting native state without adopting it")
	}
	attached, err := branchAttachedElsewhere(ctx, runner, repo, location.Branch)
	if err != nil {
		return worktreeFacts{}, err
	}
	if attached {
		return worktreeFacts{}, newFailure(KindInvalidOperation, "work_bootstrap", "existing branch is attached to another worktree", false, "resolve the native branch attachment without removing native resources")
	}
	if _, err := runner.Run(ctx, repo, "worktree", "add", location.Path, location.Branch); err != nil {
		return retryBootstrapProbe(ctx, runner, repo, location, err)
	}
	return verifyBootstrapNative(ctx, runner, repo, location)
}

func retryBootstrapProbe(ctx context.Context, runner GitRunner, repo string, location WorktreeLocation, cause error) (worktreeFacts, error) {
	if ok, facts, err := probeWorktree(ctx, runner, repo, location.Path, location.Branch, location.BaseSHA); err == nil && ok {
		if facts.headSHA != location.BaseSHA {
			return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "recovered worktree has commits after the pinned base", false, "resolve the conflicting native state without adopting it")
		}
		return facts, nil
	}
	return worktreeFacts{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "native worktree creation failed; the recorded operation is safe to replay", gitFailureRetrySafe(cause), "retry the same idempotency key", cause)
}

func gitFailureRetrySafe(err error) bool {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode() < 128
	}
	return true
}

func verifyBootstrapNative(ctx context.Context, runner GitRunner, repo string, location WorktreeLocation) (worktreeFacts, error) {
	ok, facts, err := probeWorktree(ctx, runner, repo, location.Path, location.Branch, location.BaseSHA)
	if err != nil {
		return worktreeFacts{}, err
	}
	if !ok {
		return worktreeFacts{}, newFailure(KindGitUnreachable, "work_bootstrap", "native worktree did not verify against the pinned intent", false, "contact_operator")
	}
	if facts.headSHA != location.BaseSHA {
		return worktreeFacts{}, newFailure(KindProjectionConflict, "work_bootstrap", "native worktree has commits after the pinned base", false, "resolve the conflicting native state without adopting it")
	}
	return facts, nil
}

func validateBootstrapDefaultBranch(ctx context.Context, runner GitRunner, repo string) error {
	headOut, headErr := runner.Run(ctx, repo, "symbolic-ref", "--quiet", "HEAD")
	defaultRef, defaultErr := bootstrapDefaultBranchRef(ctx, runner, repo)
	if headErr != nil || defaultErr != nil {
		return newFailure(KindGitUnreachable, "work_bootstrap", "cannot prove the Project default branch checkout", false, "check out the default branch and set origin/HEAD")
	}
	head := strings.TrimSpace(string(headOut))
	if head != "refs/heads/"+strings.TrimPrefix(defaultRef, "origin/") {
		return newFailure(KindInvalidOperation, "work_bootstrap", "Project main worktree is not on its default branch", false, "check out the branch named by origin/HEAD")
	}
	return nil
}

// DefaultBranchRef returns the Project default branch ref from origin/HEAD.
// A linked worktree uses this ref instead of its own terminal branch as the
// base for the next claimed worktree.
func DefaultBranchRef(ctx context.Context, repo string) (string, error) {
	return bootstrapDefaultBranchRef(ctx, ExecGitRunner{}, repo)
}

func bootstrapDefaultBranchRef(ctx context.Context, runner GitRunner, repo string) (string, error) {
	defaultOut, err := runner.Run(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", newFailure(KindGitUnreachable, "work_bootstrap", "cannot resolve the Project default branch", false, "set origin/HEAD")
	}
	const remotePrefix = "refs/remotes/origin/"
	value := strings.TrimSpace(string(defaultOut))
	if !strings.HasPrefix(value, remotePrefix) || len(strings.TrimPrefix(value, remotePrefix)) == 0 {
		return "", newFailure(KindGitUnreachable, "work_bootstrap", "origin/HEAD does not name a default branch", false, "set origin/HEAD")
	}
	return "origin/" + strings.TrimPrefix(value, remotePrefix), nil
}

func validateBootstrapNativeAbsent(ctx context.Context, runner GitRunner, location WorktreeLocation) error {
	if _, err := os.Lstat(location.Path); err == nil {
		return newFailure(KindProjectionConflict, "work_bootstrap", "canonical worktree path exists before the bootstrap operation", false, "resolve the pre-existing native state and use a new idempotency key")
	} else if !os.IsNotExist(err) {
		return wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the canonical worktree path", true, "restore access to the worktree path", err)
	}
	if _, exists, err := bootstrapBranchHead(ctx, runner, location.Repo, location.Branch); err != nil {
		return err
	} else if exists {
		return newFailure(KindProjectionConflict, "work_bootstrap", "canonical branch exists before the bootstrap operation", false, "resolve the pre-existing native state and use a new idempotency key")
	}
	return nil
}

func bootstrapBranchHead(ctx context.Context, runner GitRunner, repo, branch string) (string, bool, error) {
	ref := "refs/heads/" + branch
	_, err := runner.Run(ctx, repo, "show-ref", "--verify", "--quiet", ref)
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect the canonical branch", true, "restore access to the repository and retry", err)
	}
	out, err := runner.Run(ctx, repo, "rev-parse", "--verify", ref)
	if err != nil {
		return "", false, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot resolve the canonical branch", true, "restore access to the repository and retry", err)
	}
	return strings.TrimSpace(string(out)), true, nil
}

func branchAttachedElsewhere(ctx context.Context, runner GitRunner, repo, branch string) (bool, error) {
	out, err := runner.Run(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, wrapFailure(KindGitUnreachable, "work_bootstrap", "cannot inspect native worktree attachments", true, "restore access to the repository and retry", err)
	}
	want := "branch refs/heads/" + branch
	for _, block := range strings.Split(strings.TrimSpace(string(out)), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if line == want {
				return true, nil
			}
		}
	}
	return false, nil
}

func pinBootstrapClaimTx(ctx context.Context, tx *sql.Tx, operationID, workID, projectID string, location WorktreeLocation, now time.Time) error {
	if err := ValidateWorktreeClaimIntent(location.Branch, location.BaseSHA, location.Path); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM worktree_claims WHERE work_id=? AND project_id=? AND state IN ('pending','verified')`, workID, projectID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return newFailure(KindProjectionConflict, "work_bootstrap", "work already has an active canonical worktree", false, "replay the original operation")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, operationID, workID, projectID, WorktreeSetID(workID), location.Repo, location.Branch, location.BaseSHA, location.Path, worktreeStatePending, "operator", operationID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if isUniqueViolation(err) {
		return newFailure(KindProjectionConflict, "work_bootstrap", "another active claim already pins this repository's branch or this native path", false, "replay the original operation")
	}
	return err
}

func validateBootstrapScopeTx(ctx context.Context, tx *sql.Tx, productID, projectID string, declared []string) error {
	var member bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, productID, projectID).Scan(&member); err != nil {
		return err
	}
	if !member {
		return newFailure(KindUnknownScope, "work_bootstrap", "Project is not a member of product_id", false, "use a Product and Project with an existing membership")
	}
	var productCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM product_projects WHERE project_id=?`, projectID).Scan(&productCount); err != nil {
		return err
	}
	if productCount != 1 {
		return newFailure(KindInvalidOperation, "work_bootstrap", "Project has cross-Product scope", false, "use the ordinary agent approval path for cross-Product work")
	}
	applicable, err := governingRequirementsForProjectIDs(ctx, tx, []string{projectID})
	if err != nil {
		return err
	}
	if missing := MissingGoverningRequirements(applicable, declared); len(missing) != 0 {
		return newFailure(KindInvalidOperation, "work_bootstrap", "governing requirements need ordinary agent approval", false, "use the ordinary agent approval path with all governing requirements")
	}
	return nil
}

func (s *Store) finalizeBootstrap(ctx context.Context, req BootstrapRequest, operationID, workID string, location WorktreeLocation, facts worktreeFacts) (_ BootstrapResult, retErr error) {
	tx, err := s.beginDurableTx(ctx)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer tx.finish(&retErr)
	var digest, state string
	var expected int64
	if err := tx.QueryRowContext(ctx, `SELECT request_digest,state,expected_version FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&digest, &state, &expected); err != nil {
		return BootstrapResult{}, err
	}
	var pinnedBranch, pinnedBase, pinnedPath, pinnedRepo string
	if err := tx.QueryRowContext(ctx, `SELECT c.pinned_branch,c.pinned_base_sha,c.pinned_path,b.repo_path FROM worktree_claims c JOIN bootstrap_operations b ON b.operation_id=c.op_id WHERE c.op_id=?`, operationID).Scan(&pinnedBranch, &pinnedBase, &pinnedPath, &pinnedRepo); err != nil {
		return BootstrapResult{}, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap finalization has no pinned worktree intent", false, "contact_operator", err)
	}
	if location.Branch != pinnedBranch || location.BaseSHA != pinnedBase || location.Path != pinnedPath || location.Repo != pinnedRepo {
		return BootstrapResult{}, newFailure(KindInvariantViolation, "work_bootstrap", "bootstrap finalization differs from the pinned worktree intent", false, "contact_operator")
	}
	if state == "completed" {
		entry, err := worktreeEntryByClaim(ctx, tx.Tx, operationID)
		if err != nil {
			return BootstrapResult{}, err
		}
		version, err := workVersionTx(ctx, tx.Tx, workID)
		if err != nil {
			return BootstrapResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return BootstrapResult{}, err
		}
		return BootstrapResult{OperationID: operationID, Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, nil
	}
	if digest == "" {
		return BootstrapResult{}, newFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no request digest", false, "contact_operator")
	}
	if state != "native_ready" || facts.branch != location.Branch || facts.headSHA != location.BaseSHA || facts.repositoryID == "" {
		return BootstrapResult{}, newFailure(KindInvariantViolation, "work_bootstrap", "bootstrap native facts do not match the pinned finalization state", false, "contact_operator")
	}
	// The occupancy row's process start time is derived from /proc here, on
	// the live path only: the fold replays the recorded value and never
	// re-derives it.
	var hostPIDStart uint64
	if req.HostPID > 0 {
		start, startErr := hostlease.ProcessStart(req.HostPID)
		if startErr != nil {
			return BootstrapResult{}, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read the host process start time", true, "retry once the host process is observable", startErr)
		}
		hostPIDStart = start
	}
	payload := marshalWorktreeCreated(expected, WorktreeSetID(workID), req.ProjectID, operationID, location, facts, req.SessionRef, req.HostPID, hostPIDStart)
	eventID := operationID + ":worktree-created"
	var priorCreation bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events WHERE event_id=?)`, eventID).Scan(&priorCreation); err != nil {
		return BootstrapResult{}, err
	}
	if priorCreation {
		eventID = fmt.Sprintf("%s:%d", eventID, expected)
	}
	if _, err := applyOperationTx(ctx, tx.Tx, Operation{Events: []Event{{EventID: eventID, Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}}, newFoldScope(tx.Tx), false); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state=?,updated_at=? WHERE op_id=? AND state=?`, worktreeStateVerified, s.now().Format(time.RFC3339Nano), operationID, worktreeStatePending); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='completed',updated_at=? WHERE operation_id=? AND state='native_ready'`, s.now().Format(time.RFC3339Nano), operationID); err != nil {
		return BootstrapResult{}, err
	}
	entry, err := worktreeEntryByClaim(ctx, tx.Tx, operationID)
	if err != nil {
		return BootstrapResult{}, err
	}
	version := expected + 1
	if err := tx.Commit(); err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{OperationID: operationID, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, nil
}

func bootstrapJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func workVersionTx(ctx context.Context, tx *sql.Tx, workID string) (int64, error) {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err == sql.ErrNoRows {
		return 0, newFailure(KindProjectionNotFound, "work_bootstrap", "work item does not exist", false, "replay the original operation")
	} else if err != nil {
		return 0, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read work item version", true, "retry once the database is readable", err)
	}
	return version, nil
}
