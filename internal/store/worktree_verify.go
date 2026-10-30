package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// SessionWorktreeOwner identifies the agent session holding a verify lease.
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

const (
	defaultWorktreeVerifyBytes    = 16384
	maxWorktreeVerifyCommandArgs  = 16
	maxWorktreeVerifyCommandPart  = 256
	worktreeVerifyFinalizeTimeout = 5 * time.Second
)

type WorktreeVerifyRequest struct {
	Owner     SessionWorktreeOwner
	WorkID    string
	ProjectID string
	Command   []string
	Oracle    *NativeOracleRequest
	// NativeAuthorization is trusted host wiring, never decoded caller input.
	NativeAuthorization        func(context.Context, *Transaction) error `json:"-"`
	nativePlanJSON             string
	nativePlanSHA256           string
	nativeCommandJSON          string
	nativeAcceptedInputsDigest string
	nativeEvidenceRefsJSON     string
	nativeLeaseOwner           verifyLeaseOwnerProcess
	LeaseID                    string
	PrincipalRef               string
	RequestID                  string
	Now                        time.Time
	Runner                     GitRunner
	RunCommand                 func(context.Context, string, []string, int) (int, []byte, bool, error)
	MaxOutputBytes             int
	SupportedBudgetSeconds     int
	CallerBudgetApplied        bool
}

type WorktreeVerifyResult struct {
	Oracle              *NativeOracleResult `json:"oracle,omitempty"`
	WorkID              string              `json:"work_id"`
	ProjectID           string              `json:"project_id"`
	Branch              string              `json:"branch"`
	Path                string              `json:"path"`
	LeaseID             string              `json:"lease_id"`
	OperationRef        string              `json:"operation_ref"`
	SubjectRef          string              `json:"subject_ref,omitempty"`
	Command             []string            `json:"command"`
	ExitCode            int                 `json:"exit_code"`
	Output              string              `json:"output"`
	OutputTruncated     bool                `json:"output_truncated"`
	TrackedFilesChanged bool                `json:"tracked_files_changed"`
	// Observed at release commit or replay, not part of the immutable receipt.
	LeaseDisposition *WorktreeVerifyLeaseDisposition `json:"-"`
}

type WorktreeVerifyLeaseDisposition struct {
	State   string `json:"state"`
	Outcome string `json:"outcome,omitempty"`
}

// VerifyWorktree holds no transaction across the external command.
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
	if req.NativeAuthorization != nil && (req.Oracle == nil || req.Oracle.Phase != "execute") {
		return WorktreeVerifyResult{}, oracleFailure(KindInvalidPayload, "worker execution authorization requires the native execute producer", "select a recorded native control")
	}
	if req.Oracle != nil {
		return s.verifyNativeOracle(ctx, req)
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
	entry, err := activeWorktreeEntryForProject(ctx, s.db, "worktree_verify", req.WorkID, req.ProjectID)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if err := probeWorktreeReachable(ctx, runner, "worktree_verify", entry); err != nil {
		return WorktreeVerifyResult{}, err
	}
	result := WorktreeVerifyResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Branch: entry.Branch, Path: entry.Path, LeaseID: req.LeaseID, OperationRef: worktreeVerifyOperationRef(req.LeaseID), Command: req.Command}
	return s.verifyWorktreeCommand(ctx, req, entry, runner, runCommand, maxOutput, now, string(commandJSON), result)
}

func (s *Store) verifyWorktreeCommand(ctx context.Context, req WorktreeVerifyRequest, entry WorktreeEntry, runner GitRunner, runCommand func(context.Context, string, []string, int) (int, []byte, bool, error), maxOutput int, now time.Time, commandJSON string, result WorktreeVerifyResult) (WorktreeVerifyResult, error) {
	before, err := snapshotTrackedFiles(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	beforeSubject, err := snapshotOracleGitSubject(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if err := s.acquireCommandVerifyLease(ctx, req, entry, commandJSON, now); err != nil {
		var completed *worktreeVerifyCompleted
		if errors.As(err, &completed) {
			return completed.result, completed.failure
		}
		return WorktreeVerifyResult{}, err
	}
	leaseRef := SubjectCurrentVersion{SubjectType: "worktree_verify_lease", SubjectID: req.LeaseID, Version: 1}
	// Every exit after acquire owes a release, including cancellation and panic.
	released := false
	defer releaseAbandonedVerifyLease(s, ctx, req.LeaseID, &released)
	exitCode, output, truncated, runErr := runCommand(ctx, entry.Path, req.Command, maxOutput)
	exitCode, output, cancelErr := normalizeWorktreeVerifyOutcome(ctx, exitCode, output, runErr)
	finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeVerifyFinalizeTimeout)
	defer finalizeCancel()
	after, err := snapshotTrackedFiles(finalizeCtx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(err, leaseRef)
	}
	afterSubject, err := snapshotOracleGitSubject(finalizeCtx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(err, leaseRef)
	}
	releaseTx, err := beginOrdinaryTx(finalizeCtx, s.db)
	if err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot begin verify release", true, "retry the same operation with the same lease id", err), leaseRef)
	}
	defer releaseTx.Rollback()
	changed := before != after || beforeSubject.head != afterSubject.head || before.head != beforeSubject.head || after.head != afterSubject.head
	outcome := "completed"
	if changed {
		outcome = "refused_mutated"
	}
	if cancelErr != nil {
		outcome = "aborted"
	}
	boundedOutput, outputTruncated := boundText(string(output), maxOutput)
	if outputTruncated {
		truncated = true
	}
	result.ExitCode, result.Output, result.OutputTruncated, result.TrackedFilesChanged = exitCode, boundedOutput, truncated, changed
	result.SubjectRef = oracleVerifySubject(beforeSubject, afterSubject, changed)
	resultJSON, _ := json.Marshal(result)
	releasedAt := nowFromClock(nil)
	if _, err := releaseTx.ExecContext(finalizeCtx, `UPDATE worktree_verify_leases SET state='released', released_at=?, exit_code=?, outcome=?, result_json=? WHERE lease_id=? AND state='held'`, releasedAt.Format(time.RFC3339Nano), exitCode, outcome, string(resultJSON), req.LeaseID); err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot release the verify lease", true, "retry the same operation with the same lease id", err), leaseRef)
	}
	// Failed or mutated runs cannot stand as verification authority.
	if cancelErr == nil && !changed && exitCode == 0 {
		if err := recordWorktreeVerifyAuthorityTx(ctx, releaseTx, req, commandJSON, now, releasedAt, string(resultJSON)); err != nil {
			return WorktreeVerifyResult{}, annotateCommittedEffect(err, leaseRef)
		}
	}
	if err := releaseTx.Commit(); err != nil {
		return WorktreeVerifyResult{}, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the verify release", true, "retry the same operation with the same lease id", err), leaseRef)
	}
	released = true
	if cancelErr != nil {
		return result, annotateCommittedEffect(cancelErr, leaseRef)
	}
	if changed {
		return result, annotateCommittedEffect(newFailure(KindWorktreeVerifyMutated, "worktree_verify", "tracked files changed in "+entry.Path+" while the verify command ran; a verifier that edits its subject verifies nothing (CD-0096 D3)", false, "reconcile_operation"), leaseRef)
	}
	return result, nil
}

func normalizeWorktreeVerifyOutcome(ctx context.Context, exitCode int, output []byte, runErr error) (int, []byte, error) {
	cancelErr := ctx.Err()
	if cancelErr == nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
		cancelErr = runErr
	}
	if runErr != nil {
		exitCode = -1
		output = append([]byte(runErr.Error()+"\n"), output...)
	}
	if cancelErr != nil {
		exitCode = -1
	}
	return exitCode, output, cancelErr
}

func (s *Store) acquireCommandVerifyLease(ctx context.Context, req WorktreeVerifyRequest, entry WorktreeEntry, commandJSON string, now time.Time) error {
	tx, err := beginOrdinaryTx(ctx, s.db)
	if err != nil {
		return wrapFailure(KindUnavailable, "worktree_verify", "cannot begin verify", true, "retry the same operation with the same lease id", err)
	}
	defer tx.Rollback()
	live, err := activeWorktreeEntryForProject(ctx, tx, "worktree_verify", req.WorkID, req.ProjectID)
	if err != nil {
		return err
	}
	if live.Path != entry.Path || live.Branch != entry.Branch || live.ClaimOpID != entry.ClaimOpID {
		return wrapFailure(KindProjectionConflict, "worktree_verify", "worktree changed under the verify probe", true, "retry the same operation with the same lease id", nil)
	}
	if err := acquireVerifyLeaseTx(ctx, tx, req, live, commandJSON, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the verify lease", true, "retry the same operation with the same lease id", err), nativeLeaseRef(req.LeaseID))
	}
	return nil
}

func releaseAbandonedVerifyLease(s *Store, ctx context.Context, leaseID string, released *bool) {
	if *released {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeVerifyFinalizeTimeout)
	defer cancel()
	tx, err := beginOrdinaryTx(releaseCtx, s.db)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(releaseCtx, `UPDATE worktree_verify_leases SET state='released', released_at=?, outcome='aborted' WHERE lease_id=? AND state='held'`, nowFromClock(nil).Format(time.RFC3339Nano), leaseID); err != nil {
		return
	}
	_ = tx.Commit()
}

type verifyLeaseOwnerProcess struct {
	pid     int
	started string
}

func currentProcessIdentity() verifyLeaseOwnerProcess {
	pid := os.Getpid()
	started, ok := procfsStartTime(pid)
	if !ok {
		return verifyLeaseOwnerProcess{pid: pid}
	}
	return verifyLeaseOwnerProcess{pid: pid, started: started}
}

// comm may contain spaces and parentheses; starttime is field 22 of proc stat.
func procfsStartTime(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	_, rest, ok := strings.Cut(string(data), ") ")
	if !ok {
		return "", false
	}
	fields := strings.Fields(rest)
	if len(fields) < 20 {
		return "", false
	}
	return fields[19], true
}

func verifyOwnerProcessGone(identity verifyLeaseOwnerProcess) bool {
	if identity.pid <= 0 || identity.started == "" {
		return false
	}
	started, ok := procfsStartTime(identity.pid)
	if !ok {
		return true
	}
	return started != identity.started
}

func worktreeVerifyOperationRef(leaseID string) string {
	if digest, _, ok := strings.Cut(leaseID, ":worktree-verify:"); ok {
		return "worktree_verify:" + digest
	}
	return "worktree_verify:" + leaseID
}

func recordWorktreeVerifyAuthorityTx(ctx context.Context, tx *sql.Tx, req WorktreeVerifyRequest, commandJSON string, acquired, released time.Time, resultJSON string) error {
	acceptedDigest := req.nativeAcceptedInputsDigest
	evidenceJSON := req.nativeEvidenceRefsJSON
	if req.nativePlanSHA256 == "" {
		commandDigest := sha256.Sum256([]byte(commandJSON))
		acceptedDigest = "sha256:" + hex.EncodeToString(commandDigest[:])
		evidenceJSON = workflowJSON([]string{worktreeVerifyOperationRef(req.LeaseID)})
	} else if acceptedDigest == "" || evidenceJSON == "" {
		return newFailure(KindInvariantViolation, "worktree_verify", "native authority lacks its pre-finalized identity metadata", false, "reconcile the native lease")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO durable_operations
		(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,
		 accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,
		 result_kind,result_payload,evidence_refs,changed_refs,completed_at)
		VALUES(?, 1, ?, 'worktree.verify', '', '', 'external_effect', ?, '{}', ?, ?, ?, '', 'completed', ?, ?, '[]', ?)`,
		worktreeVerifyOperationRef(req.LeaseID), req.WorkID, acceptedDigest, req.PrincipalRef, req.RequestID, acquired.UTC().Format(time.RFC3339Nano), resultJSON, evidenceJSON, released.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return wrapFailure(KindUnavailable, "worktree_verify", "cannot record the verify run's evidence authority", true, "retry the same operation with the same lease id", err)
	}
	return nil
}

type worktreeVerifyCompleted struct {
	result  WorktreeVerifyResult
	failure error
}

func annotateCommittedEffect(err error, ref SubjectCurrentVersion) error {
	if err == nil {
		return nil
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		failure = wrapFailure(KindUnavailable, "worktree_verify", "verify failed after its lease committed: "+err.Error(), false, "reconcile_operation", err)
	}
	failure.EffectPossible = true
	failure.CommittedRefs = append(failure.CommittedRefs, ref)
	return failure
}

func (c *worktreeVerifyCompleted) Error() string { return "verify lease already reached its outcome" }

func acquireVerifyLeaseTx(ctx context.Context, tx *sql.Tx, req WorktreeVerifyRequest, entry WorktreeEntry, commandJSON string, now time.Time) error {
	var state, pinnedJSON, clientRef, agentRef, sessionRef, outcome, resultJSON string
	var pinnedPlan, pinnedDigest, work, project, principal string
	err := tx.QueryRowContext(ctx, `SELECT state,command_json,client_ref,agent_ref,session_ref,outcome,coalesce(result_json,''),coalesce(native_plan_json,''),coalesce(native_plan_sha256,''),work_id,project_id,principal_ref FROM worktree_verify_leases WHERE lease_id=?`, req.LeaseID).Scan(&state, &pinnedJSON, &clientRef, &agentRef, &sessionRef, &outcome, &resultJSON, &pinnedPlan, &pinnedDigest, &work, &project, &principal)
	switch {
	case err == nil:
		if pinnedPlan != req.nativePlanJSON || pinnedDigest != req.nativePlanSHA256 || work != req.WorkID || project != req.ProjectID || principal != req.PrincipalRef || pinnedJSON != commandJSON || clientRef != req.Owner.ClientRef || agentRef != req.Owner.AgentRef || sessionRef != req.Owner.SessionRef {
			return newFailure(KindInvalidOperation, "worktree_verify", "retry differs from the immutable lease command, plan, owner, or scope", false, "use the original request or a fresh identity")
		}
		if state != "held" {
			if outcome == "aborted" {
				return newFailure(KindInvalidOperation, "worktree_verify", "verify lease "+req.LeaseID+" was abandoned before it recorded an outcome", false, "retry with a new lease id (a new idempotency key)")
			}
			var recorded WorktreeVerifyResult
			var failure error
			if json.Unmarshal([]byte(resultJSON), &recorded) == nil {
				if outcome == "refused_mutated" {
					failure = annotateCommittedEffect(newFailure(KindWorktreeVerifyMutated, "worktree_verify", "tracked files changed in "+entry.Path+" while the verify command ran; a verifier that edits its subject verifies nothing (CD-0096 D3)", false, "reconcile_operation"), SubjectCurrentVersion{SubjectType: "worktree_verify_lease", SubjectID: req.LeaseID, Version: 1})
				}
				return &worktreeVerifyCompleted{result: recorded, failure: failure}
			}
			return newFailure(KindUnavailable, "worktree_verify", "released verify lease carries no readable outcome", true, "retry the read of the lease outcome")
		}
		if req.Oracle != nil {
			return newFailure(KindWorktreeLeaseHeld, "worktree_verify", "native run "+req.LeaseID+" remains held and cannot be executed twice", false, "reconcile the held lease before requesting another run")
		}
		if clientRef != req.Owner.ClientRef || agentRef != req.Owner.AgentRef || sessionRef != req.Owner.SessionRef {
			return newFailure(KindWorktreeLeaseHeld, "worktree_verify", "verify lease "+req.LeaseID+" is held by session "+clientRef+"/"+agentRef+"/"+sessionRef, true, "retry_same_request")
		}
		if pinnedJSON != commandJSON {
			return newFailure(KindInvalidOperation, "worktree_verify", "retry does not match the pinned command", false, "retry with the same command or a new idempotency key")
		}
		return nil
	case err == sql.ErrNoRows:
		return insertVerifyLeaseTx(ctx, tx, req, entry, commandJSON, now, now.Format(time.RFC3339Nano))
	default:
		return wrapFailure(KindUnavailable, "worktree_verify", "cannot read the verify lease", true, "retry once the database is readable", err)
	}
}

func insertVerifyLeaseTx(ctx context.Context, tx *sql.Tx, req WorktreeVerifyRequest, entry WorktreeEntry, commandJSON string, now time.Time, acquiredAt string) error {
	owner := req.nativeLeaseOwner
	if req.Oracle == nil {
		owner = currentProcessIdentity()
	}
	insertLease := func() error {
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome,owner_pid,owner_started,native_plan_json,native_plan_sha256,event_watermark) VALUES(?,?,?,?, 'held', ?,?,?,?,?,?, 'running', ?, ?,?,?,(SELECT COALESCE((SELECT effect_seq FROM work_items WHERE id=?),0)))`, req.LeaseID, req.WorkID, req.ProjectID, entry.Path, req.Owner.ClientRef, req.Owner.AgentRef, req.Owner.SessionRef, req.PrincipalRef, commandJSON, acquiredAt, owner.pid, owner.started, nullableText(req.nativePlanJSON), nullableText(req.nativePlanSHA256), req.WorkID)
		return insertErr
	}
	if insertErr := insertLease(); insertErr != nil {
		holderLeaseID, holder, holderOwner, held, holderErr := heldWorktreeVerifyLeaseTx(ctx, tx, entry.Path)
		if holderErr != nil {
			return holderErr
		}
		if held && verifyOwnerProcessGone(holderOwner) {
			if _, reclaimErr := tx.ExecContext(ctx, `UPDATE worktree_verify_leases SET state='released', released_at=?, outcome='aborted' WHERE lease_id=? AND state='held'`, now.Format(time.RFC3339Nano), holderLeaseID); reclaimErr != nil {
				return wrapFailure(KindUnavailable, "worktree_verify", "cannot reclaim the verify lease of a dead holder process", true, "retry the same operation with the same lease id", reclaimErr)
			}
			if insertErr := insertLease(); insertErr != nil {
				return wrapFailure(KindUnavailable, "worktree_verify", "cannot persist the reclaimed verify lease", true, "retry the same operation with the same lease id", insertErr)
			}
			return nil
		}
		if held {
			return newFailure(KindWorktreeLeaseHeld, "worktree_verify", "worktree "+entry.Path+" holds an active verify lease held by session "+sessionOwnerLabel(holder), true, "retry_same_request")
		}
		return wrapFailure(KindUnavailable, "worktree_verify", "cannot persist the verify lease", true, "retry the same operation with the same lease id", insertErr)
	}
	return nil
}

func heldWorktreeVerifyLeaseTx(ctx context.Context, tx *sql.Tx, path string) (string, SessionWorktreeOwner, verifyLeaseOwnerProcess, bool, error) {
	var leaseID string
	var holder SessionWorktreeOwner
	var owner verifyLeaseOwnerProcess
	err := tx.QueryRowContext(ctx, `SELECT lease_id,client_ref,agent_ref,session_ref,owner_pid,owner_started FROM worktree_verify_leases WHERE path=? AND state='held'`, path).Scan(&leaseID, &holder.ClientRef, &holder.AgentRef, &holder.SessionRef, &owner.pid, &owner.started)
	if err == sql.ErrNoRows {
		return "", SessionWorktreeOwner{}, verifyLeaseOwnerProcess{}, false, nil
	}
	if err != nil {
		return "", SessionWorktreeOwner{}, verifyLeaseOwnerProcess{}, false, wrapFailure(KindUnavailable, "worktree_verify", "cannot read held verify leases", true, "retry once the database is readable", err)
	}
	return leaseID, holder, owner, true, nil
}

type ActiveWorktreeVerifyLease struct {
	LeaseID    string   `json:"lease_id"`
	WorkID     string   `json:"work_id"`
	ProjectID  string   `json:"project_id"`
	Path       string   `json:"path"`
	Command    []string `json:"command"`
	AcquiredAt string   `json:"acquired_at"`
}

func heldWorktreeVerifyLeasesByOwnerTx(ctx context.Context, tx *sql.Tx, owner SessionWorktreeOwner) ([]ActiveWorktreeVerifyLease, error) {
	rows, err := tx.QueryContext(ctx, `SELECT lease_id,work_id,project_id,path,command_json,acquired_at FROM worktree_verify_leases WHERE client_ref=? AND agent_ref=? AND session_ref=? AND state='held' ORDER BY acquired_at DESC LIMIT 4`, owner.ClientRef, owner.AgentRef, owner.SessionRef)
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

// cappedOutput drains streams even after the recorded output reaches its cap.
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

func RunWorktreeVerifyCommand(ctx context.Context, dir string, command []string, maxOutput int) (int, []byte, bool, error) {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...) //nolint:gosec // argv values stay separate; no shell is invoked.
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = worktreeVerifyFinalizeTimeout
	out := &cappedOutput{limit: maxOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		_, _ = out.Write([]byte(err.Error()))
		return -1, out.buffer.Bytes(), out.truncated, nil
	}
	err := cmd.Wait()
	// The owned group must leave no background writers before the after snapshot.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if ctx.Err() != nil {
		return -1, out.buffer.Bytes(), out.truncated, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), out.buffer.Bytes(), out.truncated, nil
		}
		_, _ = out.Write([]byte("\n" + err.Error()))
		return -1, out.buffer.Bytes(), out.truncated, nil
	}
	return 0, out.buffer.Bytes(), out.truncated, nil
}

func retitleFailure(err error, op string) error {
	var failure *Failure
	if errors.As(err, &failure) {
		branded := *failure
		branded.Op = op
		return &branded
	}
	return err
}
