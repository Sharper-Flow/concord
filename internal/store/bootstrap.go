package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
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
	GoverningRequirements []string `json:"governing_requirements"`
	Ref                   string   `json:"ref"`
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

type bootstrapPrepared struct {
	Result   BootstrapResult
	State    string
	Location WorktreeLocation
}

// BootstrapPhaseHook is a test seam for failures between durable phases.
type BootstrapPhaseHook func(string) error

// CanonicalBootstrapIdentity returns the stable operation and work IDs for a
// normalized request.
func CanonicalBootstrapIdentity(req BootstrapRequest) (string, string, string, error) {
	if req.Urgency == "" {
		req.Urgency = "standard"
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	if req.Tags == nil {
		req.Tags = []string{}
	}
	if req.GoverningRequirements == nil {
		req.GoverningRequirements = []string{}
	}
	data, err := json.Marshal(req)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return "bootstrap-" + digest[:48], "work-" + digest[:24], "sha256:" + digest, nil
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
	if err := validateBootstrapRequest(req); err != nil {
		return BootstrapResult{}, err
	}
	if req.Urgency == "" {
		req.Urgency = "standard"
	}
	if req.Ref == "" {
		req.Ref = "HEAD"
	}
	operationID, workID, digest, err := CanonicalBootstrapIdentity(req)
	if err != nil {
		return BootstrapResult{}, wrapFailure(KindInvalidOperation, "work_bootstrap", "cannot derive bootstrap identity", false, "supply JSON-safe input", err)
	}
	location, existing, err := s.pinnedBootstrapLocation(ctx, req.IdempotencyKey, digest, operationID)
	if err != nil {
		return BootstrapResult{}, err
	}
	if !existing {
		location, err = s.LocateWorktree(ctx, req.ProjectID, workID, req.Ref)
		if err != nil {
			return BootstrapResult{}, err
		}
	}
	if err := validateBootstrapDefaultBranch(ctx, runner, location.Repo); err != nil {
		return BootstrapResult{}, err
	}
	prepared, err := s.prepareBootstrap(ctx, req, operationID, workID, digest, location, runner)
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
	if err := s.SyncDurable(ctx); err != nil {
		return BootstrapResult{}, err
	}
	result.Replayed = prepared.Result.Replayed
	return result, nil
}

func (s *Store) pinnedBootstrapLocation(ctx context.Context, idempotencyKey, digest, operationID string) (WorktreeLocation, bool, error) {
	var out WorktreeLocation
	var storedDigest, repo string
	err := s.db.QueryRowContext(ctx, `SELECT request_digest,repo_path FROM bootstrap_operations WHERE idempotency_key=?`, idempotencyKey).Scan(&storedDigest, &repo)
	if err == sql.ErrNoRows {
		return out, false, nil
	}
	if err != nil {
		return out, false, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read bootstrap journal", true, "retry once the database is readable", err)
	}
	if storedDigest != digest {
		return out, false, newFailure(KindInvalidOperation, "work_bootstrap", "idempotency key is bound to different input", false, "use the original request or a new idempotency key")
	}
	var branch, base, path string
	if err := s.db.QueryRowContext(ctx, `SELECT pinned_branch,pinned_base_sha,pinned_path FROM worktree_claims WHERE op_id=?`, operationID).Scan(&branch, &base, &path); err != nil {
		return out, false, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no pinned worktree intent", false, "contact_operator", err)
	}
	return WorktreeLocation{Branch: branch, BaseSHA: base, Path: path, Repo: repo}, true, nil
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
	if req.Ref != "" && (len(req.Ref) > 128 || strings.HasPrefix(req.Ref, "-") || strings.ContainsAny(req.Ref, " \t\n\r\x00")) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "ref is not a bounded rev-syntax value", false, "supply one safe repository ref")
	}
	return nil
}

func (s *Store) prepareBootstrap(ctx context.Context, req BootstrapRequest, operationID, workID, digest string, location WorktreeLocation, runner GitRunner) (bootstrapPrepared, error) {
	var out bootstrapPrepared
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot begin bootstrap journal", true, "retry the same operation", err)
	}
	defer tx.Rollback()
	var storedDigest, state string
	replayed := false
	err = tx.QueryRowContext(ctx, `SELECT request_digest,state FROM bootstrap_operations WHERE idempotency_key=?`, req.IdempotencyKey).Scan(&storedDigest, &state)
	if err == nil {
		replayed = true
		if storedDigest != digest {
			return out, newFailure(KindInvalidOperation, "work_bootstrap", "idempotency key is bound to different input", false, "use the original request or a new idempotency key")
		}
		if err := tx.QueryRowContext(ctx, `SELECT b.repo_path,c.pinned_branch,c.pinned_base_sha,c.pinned_path FROM bootstrap_operations b JOIN worktree_claims c ON c.op_id=b.operation_id WHERE b.operation_id=?`, operationID).Scan(&location.Repo, &location.Branch, &location.BaseSHA, &location.Path); err != nil {
			return out, wrapFailure(KindInvariantViolation, "work_bootstrap", "bootstrap journal has no pinned worktree intent", false, "contact_operator", err)
		}
		if state == "completed" {
			entry, entryErr := worktreeEntryByClaim(ctx, tx, operationID)
			if entryErr != nil {
				return out, entryErr
			}
			version, versionErr := workVersionTx(ctx, tx, workID)
			if versionErr != nil {
				return out, versionErr
			}
			if err := tx.Commit(); err != nil {
				return out, err
			}
			return bootstrapPrepared{Result: BootstrapResult{OperationID: operationID, Replayed: true, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID, WorkVersion: version, Entry: entry}, State: state, Location: location}, nil
		}
	} else if err != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot read bootstrap journal", true, "retry once the database is readable", err)
	}

	if err == sql.ErrNoRows {
		state = "pending"
		if err := validateBootstrapNativeAbsent(ctx, runner, location); err != nil {
			return out, err
		}
		if err := validateBootstrapScopeTx(ctx, tx, req.ProductID, req.ProjectID, req.GoverningRequirements); err != nil {
			return out, err
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE id=?)`, workID).Scan(&exists); err != nil {
			return out, err
		}
		if exists {
			return out, newFailure(KindProjectionConflict, "work_bootstrap", "derived work ID already exists without its journal", false, "use a new idempotency key")
		}
		now := s.now()
		priority := req.Priority
		workPayload, _ := json.Marshal(workCreatedPayload{WorkID: workID, WorkKind: req.Kind, Title: req.Title, ValueStatement: req.ValueStatement, Priority: &priority, Urgency: req.Urgency, Tags: req.Tags, WorkflowTypeRef: req.WorkflowTypeRef, ExternalRef: req.ExternalRef})
		membershipPayload, _ := json.Marshal(workMembershipsPayload{Memberships: []workMembershipPayload{{ProjectID: req.ProjectID, Role: "primary"}}, ExpectedVersion: 1, ResultingVersion: 2})
		events := []Event{
			{EventID: operationID + ":work-created", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 2, Payload: workPayload},
			{EventID: operationID + ":memberships", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: now, PayloadVersion: 1, Payload: membershipPayload},
		}
		if _, err := applyOperationTx(ctx, tx, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}}, true, false); err != nil {
			return out, err
		}
		expectedVersion := int64(2)
		if req.WorkflowTypeRef != "" {
			definition, definitionErr := BuiltinWorkflowDefinitionForRef(req.WorkflowTypeRef)
			if definitionErr != nil {
				return out, definitionErr
			}
			actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord", AgentRef: "agent/concord", SessionRef: "session/" + operationID, ActorClass: ActorOperator}
			if err := InitializeWorkflowTx(ctx, &Transaction{tx: tx, clock: s.Clock}, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: now}); err != nil {
				return out, err
			}
			expectedVersion = 4
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bootstrap_operations(idempotency_key,operation_id,request_digest,request_json,product_id,project_id,work_id,repo_path,expected_version,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, req.IdempotencyKey, operationID, digest, bootstrapJSON(req), req.ProductID, req.ProjectID, workID, location.Repo, expectedVersion, "pending", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return out, err
		}
		if err := pinBootstrapClaimTx(ctx, tx, operationID, workID, req.ProjectID, location, expectedVersion, now); err != nil {
			return out, err
		}
	}
	if err := tx.Commit(); err != nil {
		return out, wrapFailure(KindUnavailable, "work_bootstrap", "cannot commit bootstrap journal", true, "retry the same idempotency key", err)
	}
	if err := s.SyncDurable(ctx); err != nil {
		return out, err
	}
	return bootstrapPrepared{Result: BootstrapResult{OperationID: operationID, Replayed: replayed, ProductID: req.ProductID, ProjectID: req.ProjectID, WorkID: workID}, State: state, Location: location}, nil
}

func (s *Store) setBootstrapState(ctx context.Context, operationID, from, to string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "work_bootstrap", "cannot begin bootstrap phase record", true, "retry the same operation", err)
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM bootstrap_operations WHERE operation_id=?`, operationID).Scan(&state); err != nil {
		return err
	}
	if state == to || state == "native_ready" || state == "completed" {
		return nil
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
	return s.SyncDurable(ctx)
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
			return worktreeFacts{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "native worktree creation failed; the recorded operation is safe to replay", true, "retry the same idempotency key", err)
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
	return worktreeFacts{}, wrapFailure(KindGitUnreachable, "work_bootstrap", "native worktree creation failed; the recorded operation is safe to replay", true, "retry the same idempotency key", cause)
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
	defaultOut, defaultErr := runner.Run(ctx, repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	if headErr != nil || defaultErr != nil {
		return newFailure(KindGitUnreachable, "work_bootstrap", "cannot prove the Project default branch checkout", false, "check out the default branch and set origin/HEAD")
	}
	head := strings.TrimSpace(string(headOut))
	defaultRef := strings.TrimSpace(string(defaultOut))
	const remotePrefix = "refs/remotes/origin/"
	if !strings.HasPrefix(defaultRef, remotePrefix) || head != "refs/heads/"+strings.TrimPrefix(defaultRef, remotePrefix) {
		return newFailure(KindInvalidOperation, "work_bootstrap", "Project main worktree is not on its default branch", false, "check out the branch named by origin/HEAD")
	}
	return nil
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

func pinBootstrapClaimTx(ctx context.Context, tx *sql.Tx, operationID, workID, projectID string, location WorktreeLocation, expectedVersion int64, now time.Time) error {
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
	_, err := tx.ExecContext(ctx, `INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, operationID, workID, projectID, WorktreeSetID(workID), location.Branch, location.BaseSHA, location.Path, worktreeStatePending, "operator", operationID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
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

func (s *Store) finalizeBootstrap(ctx context.Context, req BootstrapRequest, operationID, workID string, location WorktreeLocation, facts worktreeFacts) (BootstrapResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BootstrapResult{}, err
	}
	defer tx.Rollback()
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
		entry, err := worktreeEntryByClaim(ctx, tx, operationID)
		if err != nil {
			return BootstrapResult{}, err
		}
		version, err := workVersionTx(ctx, tx, workID)
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
	payload, _ := json.Marshal(worktreeCreatedPayload{ExpectedVersion: expected, ResultingVersion: expected + 1, SetID: WorktreeSetID(workID), ProjectID: req.ProjectID, ClaimOpID: operationID, Branch: location.Branch, BaseSHA: location.BaseSHA, Path: location.Path, RepositoryID: facts.repositoryID, GitFacts: facts.raw()})
	if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{{EventID: operationID + ":worktree-created", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected}}, true, false); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worktree_claims SET state=?,updated_at=? WHERE op_id=? AND state=?`, worktreeStateVerified, s.now().Format(time.RFC3339Nano), operationID, worktreeStatePending); err != nil {
		return BootstrapResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bootstrap_operations SET state='completed',updated_at=? WHERE operation_id=? AND state='native_ready'`, s.now().Format(time.RFC3339Nano), operationID); err != nil {
		return BootstrapResult{}, err
	}
	entry, err := worktreeEntryByClaim(ctx, tx, operationID)
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
