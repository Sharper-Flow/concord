package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
)

// The bounded integration attribution reader (the integration settlement).
// A coordinator correction cites the failed combined verify run by its
// lease id or its worktree_verify operation reference; this file derives
// the core-owned identity from that citation alone. The caller supplies no
// population, no commit, and no sequence: the run's subject comes from the
// lease's retained immutable result, and the accepted input population
// comes from current acceptance rows through the acquire-time effect watermark.
//
// The reader is information only. It never requires the green durable
// producer join, never mints verification evidence, and never turns the
// failed run into acceptance authority: a failed run keeps exactly its
// released lease. Runs that cannot anchor attribution — mutated, aborted,
// subject-less, green, missing, or cited with a digest that differs from
// the retained result bytes — refuse typed, naming the cause.
//
// Every read takes the caller's queryer, so the derivation can run inside
// the enclosing transaction without parking on the pooled connection.

// workerFailureIntegrationFailure is one typed reader refusal. The detail
// names the cause; the recovery action names the citation the boundary
// accepts.
func workerFailureIntegrationFailure(detail string) *Failure {
	return newFailure(KindInvalidPayload, "worker_failure_integration", detail, false, "cite one released failed combined verify run of this work by its lease id or its worktree_verify operation reference")
}

// workerFailureIntegrationLeaseQuery resolves one native-run locator to a
// released lease of the work. The locator may be the lease id or the
// operation reference the retained result records; both arms are aliases of
// one lease, and a locator that matches two leases refuses at the caller.
const workerFailureIntegrationLeaseQuery = `SELECT lease_id, project_id, outcome, exit_code, COALESCE(result_json,''), event_watermark, command_json
FROM worktree_verify_leases
WHERE work_id=? AND state='released'
AND (lease_id=? OR (result_json IS NOT NULL AND json_valid(result_json) AND json_extract(result_json,'$.operation_ref')=?))`

// workerFailureIntegrationAcceptQuery reads the accepted worker-job
// completions of the work at or below the watermark, in event order. The
// first acceptance's own identifier is the result reference; the current
// job satisfaction can advance independently. No latest-job lookup or
// timestamp participates. A confirming review judges an already
// accepted revision; its core-marked dispatch grants no second input result.
const workerFailureIntegrationAcceptQuery = `SELECT receipt.event_id,
	json_extract(e.first_acceptance_payload,'$.worker_job.job_id'),
	json_extract(e.first_acceptance_payload,'$.worker_job.revision'),
	json_extract(e.first_acceptance_payload,'$.worker_job.digest')
FROM worker_attempt_dispositions e
LEFT JOIN effect_receipts receipt ON receipt.effect_seq=e.first_acceptance_seq
WHERE e.work_id=? AND e.first_acceptance_seq<=?
AND e.action_id='accept_worker_result'
AND json_extract(e.first_acceptance_payload,'$.worker_job') IS NOT NULL
AND NOT EXISTS (
	SELECT 1 FROM worker_authorizations d
	WHERE d.work_id=e.work_id AND d.seq<e.first_acceptance_seq
	AND d.attempt_id=e.attempt_id
	AND json_extract(d.payload,'$.checks_repair_review.worker_job.job_id')=json_extract(e.first_acceptance_payload,'$.worker_job.job_id')
	AND json_extract(d.payload,'$.checks_repair_review.worker_job.revision')=json_extract(e.first_acceptance_payload,'$.worker_job.revision')
	AND json_extract(d.payload,'$.checks_repair_review.worker_job.digest')=json_extract(e.first_acceptance_payload,'$.worker_job.digest')
)
ORDER BY e.first_acceptance_seq`

// workerFailureIntegrationJobRecordedQuery joins one accepted revision to
// its recorded revision row, by the exact job, revision, and digest the
// acceptance names.
const workerFailureIntegrationJobRecordedQuery = `SELECT EXISTS(SELECT 1 FROM worker_job_revisions
WHERE work_id=? AND job_id=? AND revision=? AND digest=?)`

// workerFailureIntegrationLease is one resolved lease row, scanned raw:
// ambiguity is decided on row identity alone, and only the single resolved
// row's content is validated. The result JSON is retained verbatim because
// the identity digest hashes the stored bytes.
type workerFailureIntegrationLease struct {
	leaseID    string
	projectID  string
	outcome    string
	exitCode   sql.NullInt64
	resultJSON string
	watermark  sql.NullInt64
	commandRaw string
}

// deriveWorkerFailureIntegration derives the integration variant of the
// core-derived identity from the coordinator observation's cited combined
// run. The observation must carry at least one native_run source whose
// locator is the run's verify lease id or its operation reference; every
// native_run source in the same observation must resolve to that same
// lease. The derived identity names the canonical operation reference and
// the digest over the retained result bytes, and carries either the exact
// accepted input population below the acquire watermark or the explicit
// inputs_unavailable marker for a lease recorded before the watermark
// column existed.
func deriveWorkerFailureIntegration(ctx context.Context, q queryer, workID string, observation *WorkerFailureObservation) (*WorkerFailureIdentity, error) {
	if workID == "" {
		return nil, newFailure(KindInvalidInput, "worker_failure_integration", "the integration reader received no work identity", false, "derive for the work the correction names")
	}
	if err := validateWorkerFailureObservation(observation); err != nil {
		return nil, err
	}
	if observation.Observer != WorkerFailureObserverCoordinator || observation.Phase != WorkerFailurePhaseIntegration {
		return nil, workerFailureBoundFailure("failure_attribution integration identity derives only for a coordinator integration observation; " + observation.Observer + "/" + observation.Phase + " cannot anchor a combined run")
	}
	native := make([]WorkerFailureSource, 0, len(observation.Sources))
	for _, source := range observation.Sources {
		if source.Kind == WorkerFailureSourceNativeRun {
			native = append(native, source)
		}
	}
	if len(native) == 0 {
		return nil, workerFailureIntegrationFailure("integration attribution anchors on a native_run source; the observation cites none")
	}
	lease, err := resolveWorkerFailureIntegrationLease(ctx, q, workID, native)
	if err != nil {
		return nil, err
	}
	result, resultDigest, err := workerFailureIntegrationRunIdentity(lease, workID)
	if err != nil {
		return nil, err
	}
	for _, source := range native {
		if source.Digest != "" && source.Digest != resultDigest {
			return nil, workerFailureIntegrationFailure("the native_run source digest " + source.Digest + " differs from the retained result digest " + resultDigest + " of run " + lease.leaseID + "; the citation must match the retained bytes")
		}
	}
	inputs, unavailable, err := workerFailureIntegrationInputs(ctx, q, workID, lease.watermark)
	if err != nil {
		return nil, err
	}
	integration := &WorkerFailureIntegrationIdentity{
		NativeRun:         WorkerFailureSubjectRef{Kind: WorkerFailureSubjectNativeRun, Locator: result.OperationRef, Digest: resultDigest},
		InputResults:      inputs,
		InputsUnavailable: unavailable,
	}
	// The derived identity always satisfies its recorded shape, except the
	// known-watermark zero-acceptance edge: the recorded-shape gate requires
	// a named population, and deriving the exact empty population is the
	// reader's honest answer for a run that started before any acceptance.
	if len(integration.InputResults) > 0 || integration.InputsUnavailable {
		if err := validateWorkerFailureIntegrationIdentity(integration); err != nil {
			return nil, wrapFailure(KindInvariantViolation, "worker_failure_integration", "the derived integration identity fails its own structural validation", false, "reconcile_operation", err)
		}
	}
	return &WorkerFailureIdentity{Kind: WorkerFailureIdentityIntegration, Integration: integration}, nil
}

// resolveWorkerFailureIntegrationLease resolves every native_run source to
// one released lease of the work. A locator that names no lease, a source
// that resolves nothing beside a resolved one, and a locator that names two
// leases each refuse; two sources that name different leases refuse.
func resolveWorkerFailureIntegrationLease(ctx context.Context, q queryer, workID string, sources []WorkerFailureSource) (*workerFailureIntegrationLease, error) {
	var resolved *workerFailureIntegrationLease
	unresolved := []string{}
	for _, source := range sources {
		rows, err := q.QueryContext(ctx, workerFailureIntegrationLeaseQuery, workID, source.Locator, source.Locator)
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "worker_failure_integration", "cannot read the cited verify lease", true, "retry once the operational state is readable", err)
		}
		found := []*workerFailureIntegrationLease{}
		for rows.Next() {
			lease := &workerFailureIntegrationLease{}
			if err := rows.Scan(&lease.leaseID, &lease.projectID, &lease.outcome, &lease.exitCode, &lease.resultJSON, &lease.watermark, &lease.commandRaw); err != nil {
				_ = rows.Close()
				return nil, wrapFailure(KindUnavailable, "worker_failure_integration", "cannot scan the cited verify lease", true, "retry once the operational state is readable", err)
			}
			found = append(found, lease)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, "worker_failure_integration", "cannot read the cited verify lease", true, "retry once the operational state is readable", err)
		}
		_ = rows.Close()
		switch {
		case len(found) > 1:
			candidates := make([]string, len(found))
			for index, lease := range found {
				candidates[index] = lease.leaseID
			}
			return nil, newAmbiguousScopeFailure("worker_failure_integration", "the native run locator "+source.Locator+" names "+strconv.Itoa(len(found))+" released leases of "+workID+"; one locator resolves one lease", "cite the run's lease id or its unambiguous worktree_verify operation reference", candidates)
		case len(found) == 1 && resolved == nil:
			resolved = found[0]
		case len(found) == 1 && resolved.leaseID != found[0].leaseID:
			return nil, workerFailureIntegrationFailure("the native run sources name different leases " + resolved.leaseID + " and " + found[0].leaseID + "; every native_run source must resolve the same combined run")
		case len(found) == 0:
			unresolved = append(unresolved, source.Locator)
		}
	}
	if resolved == nil {
		return nil, newFailure(KindProjectionNotFound, "worker_failure_integration", "the native run locator "+strings.Join(unresolved, ", ")+" names no released verify lease of work "+workID+"; the cited combined run is missing", false, "cite the failed run's lease id or its worktree_verify operation reference")
	}
	if len(unresolved) > 0 {
		return nil, workerFailureIntegrationFailure("the native run locator " + strings.Join(unresolved, ", ") + " names no released verify lease of " + workID + "; every native_run source must resolve the cited combined run " + resolved.leaseID)
	}
	return resolved, nil
}

// workerFailureIntegrationRunIdentity classifies the resolved lease and
// resolves its immutable result: only a released run that completed with a
// nonzero exit result on a clean unchanged subject anchors integration
// attribution. The retained result must match its lease row on work,
// Project, lease, operation reference, command, and exit result; a mismatch
// is structural corruption. The returned digest is the digest over the
// retained result bytes as stored.
func workerFailureIntegrationRunIdentity(lease *workerFailureIntegrationLease, workID string) (*WorktreeVerifyResult, string, error) {
	switch lease.outcome {
	case "completed":
	case "aborted":
		return nil, "", newFailure(KindCancelled, "worker_failure_integration", "the cited verify run "+lease.leaseID+" was aborted and cannot anchor integration attribution", false, "cite the released combined run that completed with a nonzero exit result")
	case "refused_mutated":
		return nil, "", newFailure(KindWorktreeVerifyMutated, "worker_failure_integration", "the cited verify run "+lease.leaseID+" mutated its subject and cannot anchor integration attribution", false, "cite a failed run whose tracked files stayed unchanged")
	default:
		return nil, "", workerFailureIntegrationFailure("the cited verify run " + lease.leaseID + " recorded outcome " + lease.outcome + "; only a completed run anchors integration attribution")
	}
	if !lease.exitCode.Valid {
		return nil, "", newFailure(KindInvariantViolation, "worker_failure_integration", "the cited verify run "+lease.leaseID+" retains no exit result", false, "reconcile_operation")
	}
	if lease.exitCode.Int64 == 0 {
		return nil, "", workerFailureIntegrationFailure("the cited verify run " + lease.leaseID + " completed green; a green run is verification authority, and integration attribution anchors only the failed combined run")
	}
	var command []string
	if err := json.Unmarshal([]byte(lease.commandRaw), &command); err != nil {
		return nil, "", newFailure(KindInvariantViolation, "worker_failure_integration", "the cited verify run "+lease.leaseID+" retains a malformed command", false, "reconcile_operation")
	}
	if !json.Valid([]byte(lease.resultJSON)) {
		return nil, "", newFailure(KindInvariantViolation, "worker_failure_integration", "the cited verify run "+lease.leaseID+" retains no readable result", false, "reconcile_operation")
	}
	var result WorktreeVerifyResult
	if err := json.Unmarshal([]byte(lease.resultJSON), &result); err != nil {
		return nil, "", newFailure(KindInvariantViolation, "worker_failure_integration", "the cited verify run "+lease.leaseID+" retains a malformed result", false, "reconcile_operation")
	}
	if result.WorkID != workID || result.ProjectID != lease.projectID || result.LeaseID != lease.leaseID ||
		result.OperationRef != worktreeVerifyOperationRef(lease.leaseID) || result.ExitCode != int(lease.exitCode.Int64) ||
		!slices.Equal(result.Command, command) || result.TrackedFilesChanged {
		return nil, "", newFailure(KindInvariantViolation, "worker_failure_integration", "the retained result of run "+lease.leaseID+" differs from its lease row on work, Project, lease, operation reference, command, exit result, or subject state", false, "reconcile_operation")
	}
	if oid, ok := strings.CutPrefix(result.SubjectRef, "commit:"); !ok || !worktreeSHAPattern.MatchString(oid) {
		return nil, "", newFailure(KindMissingEvidence, "worker_failure_integration", "the cited verify run "+lease.leaseID+" resolved no clean unchanged subject; integration attribution names the actual combined run's clean subject, never a caller commit", false, "cite the failed run whose subject stayed clean and unchanged")
	}
	sum := sha256.Sum256([]byte(lease.resultJSON))
	return &result, "sha256:" + hex.EncodeToString(sum[:]), nil
}

// workerFailureIntegrationInputs enumerates the accepted worker-job
// revision population the cited run could observe in event order: every
// accept_worker_result completion of the work carrying a worker_job binding
// at or below the lease's acquire watermark, ordered by event sequence,
// with the acceptance event id as the result reference. A lease without its
// watermark reports the population explicitly unavailable — never an empty
// accepted list and never a timestamp reconstruction. Distinct attempts
// accepting one revision are structural corruption: the confirming judgment
// is excluded, and repeated acceptance of one attempt retains its first input.
func workerFailureIntegrationInputs(ctx context.Context, q queryer, workID string, watermark sql.NullInt64) ([]WorkerFailureInputResult, bool, error) {
	if !watermark.Valid {
		return nil, true, nil
	}
	rows, err := q.QueryContext(ctx, workerFailureIntegrationAcceptQuery, workID, watermark.Int64)
	if err != nil {
		return nil, false, wrapFailure(KindUnavailable, "worker_failure_integration", "cannot read the accepted input history", true, "retry once the current rows are readable", err)
	}
	type acceptance struct {
		eventID  string
		jobID    string
		revision int64
		digest   string
	}
	accepted := []acceptance{}
	for rows.Next() {
		var entry acceptance
		var eventID, jobID, digest sql.NullString
		var revision sql.NullInt64
		if err := rows.Scan(&eventID, &jobID, &revision, &digest); err != nil {
			_ = rows.Close()
			return nil, false, wrapFailure(KindUnavailable, "worker_failure_integration", "cannot scan the accepted input history", true, "retry once the current rows are readable", err)
		}
		if !eventID.Valid || eventID.String == "" {
			_ = rows.Close()
			return nil, false, newFailure(KindInvariantViolation, "worker_failure_integration", "the first accepted input of "+workID+" has no effect receipt", false, "reconcile_operation")
		}
		if !jobID.Valid || !revision.Valid || !digest.Valid {
			_ = rows.Close()
			return nil, false, newFailure(KindInvariantViolation, "worker_failure_integration", "an accepted worker-job completion of "+workID+" carries an incomplete worker_job binding", false, "reconcile_operation")
		}
		entry.eventID, entry.jobID, entry.revision, entry.digest = eventID.String, jobID.String, revision.Int64, digest.String
		accepted = append(accepted, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, false, wrapFailure(KindUnavailable, "worker_failure_integration", "cannot read the accepted input history", true, "retry once the current rows are readable", err)
	}
	// The rows are fully read and closed before the per-revision recording
	// join runs: no nested query executes while rows are open.
	_ = rows.Close()
	inputs := make([]WorkerFailureInputResult, 0, len(accepted))
	seen := map[string]bool{}
	for _, entry := range accepted {
		key := entry.jobID + ":" + strconv.FormatInt(entry.revision, 10)
		if seen[key] {
			return nil, false, newFailure(KindInvariantViolation, "worker_failure_integration", "the acceptance history of "+workID+" repeats revision "+key+"; a satisfied revision cannot re-accept, so the repeat is structural corruption", false, "reconcile_operation")
		}
		seen[key] = true
		if !workerJobIDPattern.MatchString(entry.jobID) || entry.revision < 1 || !workerProvenancePattern.MatchString(entry.digest) {
			return nil, false, newFailure(KindInvariantViolation, "worker_failure_integration", "the accepted worker-job completion at "+key+" of "+workID+" carries a malformed binding", false, "reconcile_operation")
		}
		var recorded int
		if err := q.QueryRowContext(ctx, workerFailureIntegrationJobRecordedQuery, workID, entry.jobID, entry.revision, entry.digest).Scan(&recorded); err != nil {
			return nil, false, wrapFailure(KindUnavailable, "worker_failure_integration", "cannot read the recorded job revision of "+key, true, "retry once the current rows are readable", err)
		}
		if recorded != 1 {
			return nil, false, newFailure(KindInvariantViolation, "worker_failure_integration", "the accepted revision "+key+" of "+workID+" names no retained worker.job_recorded recording with that digest; the acceptance join is corrupt", false, "reconcile_operation")
		}
		inputs = append(inputs, WorkerFailureInputResult{Job: &WorkerJobBinding{JobID: entry.jobID, Revision: entry.revision, Digest: entry.digest}, ResultRef: entry.eventID})
	}
	return inputs, false, nil
}
