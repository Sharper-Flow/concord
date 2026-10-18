package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// The worker-job production-size surface (CD-0225 D1, CD-0227 D1): an
// editing job under a size-capable definition records the immutable
// production_size_max cap, the measurement basis that binds it, and the
// conditional direct-caller allowance; a read-only job records none. The
// members are digest-covered event content that reads back only from the
// worker.job_recorded events, like the acceptance oracle beside them.

// Closed bounds of the size surface. An over-bound member refuses whole;
// content is never truncated to fit.
const (
	WorkerJobSizeCapMax             = 2147483647
	WorkerJobMeasuredPathsMax       = 64
	WorkerJobCallerExclusionsMax    = 64
	WorkerJobAllowanceMechanismsMax = 8
	WorkerJobAllowanceDomainsMax    = 8
)

// workerJobMeasuredPathClasses is the closed classification vocabulary of
// one measured inventory path.
var workerJobMeasuredPathClasses = []string{"production", "test", "documentation", "generated"}

// WorkerJobMeasuredPath is one measured inventory entry: a normalized
// relative repository path and its production, test, documentation, or
// generated classification. A generated exclusion requires its owning
// generator reference.
type WorkerJobMeasuredPath struct {
	Path         string `json:"path"`
	Class        string `json:"class"`
	GeneratorRef string `json:"generator_ref,omitempty"`
}

// WorkerJobMeasurementBasis binds the cap to the measured surface: the
// immutable Git base commit the delta is counted against and the exact path
// inventory with its classifications (CD-0225 D1).
type WorkerJobMeasurementBasis struct {
	BaseCommit string                  `json:"base_commit"`
	Paths      []WorkerJobMeasuredPath `json:"paths"`
}

// WorkerJobCallerAllowance is the conditional direct-caller scope an editing
// job may carry (CD-0227 D1): the permitted Project, the changed owner
// mechanisms, and the parent contract's approved Domain and pinned law
// boundaries. It reuses the oracle's mechanism and law-binding types, and
// its joins run through the same authority functions.
type WorkerJobCallerAllowance struct {
	ProjectID   string             `json:"project_id"`
	Mechanisms  []OracleMechanism  `json:"mechanisms"`
	DomainIDs   []string           `json:"domain_ids"`
	LawBindings []OracleLawBinding `json:"law_bindings"`
}

// workflowJobSizeActive reports whether a pinned definition carries the
// production-size members: the record_worker_job action's declared payload
// names the production_size_max member. Capability is derived from the
// declared action member exactly like workflowOwnerOracleActive, so the
// caller allowance rides the same gate and no capability bit or behavior
// flag exists.
func workflowJobSizeActive(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "record_worker_job" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "production_size_max" {
				return true
			}
		}
		return false
	}
	return false
}

// workerJobSizeFailure adapts one surface's refusal constructor: the fold's
// registered event validator refuses through validate_worker_event, and the
// record_worker_job authoring guard refuses through workflow_action with the
// rule-naming recovery action.
type workerJobSizeFailure func(detail, recovery string) error

func workerJobSizeFoldFailure(detail, _ string) error {
	return invalidWorkerPayload(detail)
}

func workerJobSizeAuthorFailure(detail, recovery string) error {
	return newFailure(KindInvalidPayload, "workflow_action", detail, false, recovery)
}

// workerJobSizeFieldPresent reports whether one authored member arrived as a
// present, non-null JSON value.
func workerJobSizeFieldPresent(fields map[string]json.RawMessage, name string) bool {
	raw, ok := fields[name]
	return ok && len(raw) > 0 && string(raw) != "null"
}

// decodeClosedWorkerJobObject decodes one authored closed object strictly:
// an unknown member is a payload no pinned contract declared, so it refuses
// rather than silently dropping content out of the digest.
func decodeClosedWorkerJobObject[T any](raw json.RawMessage, refusal string) (*T, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return nil, workerJobSizeAuthorFailure(refusal, "supply the declared closed object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, workerJobSizeAuthorFailure(refusal, "supply exactly one object")
	}
	return &value, nil
}

// authorWorkerJobSizeTx authors the size members of one record_worker_job
// call. A pin without the declared members never reaches here with one: the
// action preflight refuses an undeclared payload member. On a size-capable
// pin the members are present together or absent together; a full trio is parsed, shape-checked,
// joined against the active contract's Domain and law boundaries, and held
// to the job's earlier recorded caps, all before the digest is derived.
func authorWorkerJobSizeTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, definition WorkflowDefinition, fields map[string]json.RawMessage, payload *WorkerJobRecordedPayload) error {
	if !workflowJobSizeActive(definition) {
		return nil
	}
	present := 0
	for _, member := range []string{"production_size_max", "measurement_basis", "direct_caller_allowance"} {
		if workerJobSizeFieldPresent(fields, member) {
			present++
		}
	}
	if present == 0 {
		// A read-only revision is shape-valid, but a job that already has a
		// capped revision cannot go uncapped: its own history decides.
		return refuseUncappedRevisionOfCappedJob(ctx, tx, workID, payload.JobID)
	}
	if present != 3 {
		return workerJobSizeAuthorFailure(
			"record_worker_job carries an incomplete size binding: production_size_max, measurement_basis, and direct_caller_allowance are present together or absent together",
			"carry all three members for an editing job or none for a read-only job")
	}
	var sizeCap int64
	if err := json.Unmarshal(fields["production_size_max"], &sizeCap); err != nil {
		return workerJobSizeAuthorFailure("record_worker_job production_size_max must be one integer", "supply the production-size cap as a number")
	}
	basis, err := decodeClosedWorkerJobObject[WorkerJobMeasurementBasis](fields["measurement_basis"], "record_worker_job measurement_basis is not one closed measurement-basis object")
	if err != nil {
		return err
	}
	allowance, err := decodeClosedWorkerJobObject[WorkerJobCallerAllowance](fields["direct_caller_allowance"], "record_worker_job direct_caller_allowance is not one closed direct-caller-allowance object")
	if err != nil {
		return err
	}
	if err := validateWorkerJobSizeShape(payload.ProjectScope, &sizeCap, basis, allowance, workerJobSizeAuthorFailure); err != nil {
		return err
	}
	for _, measured := range basis.Paths {
		if !slices.ContainsFunc(payload.PathScope, func(scope string) bool { return measured.Path == scope || strings.HasPrefix(measured.Path, scope+"/") }) {
			return workerJobSizeAuthorFailure("record_worker_job measurement_basis path "+measured.Path+" is outside the job's declared path_scope",
				"inventory only paths path_scope contains: equal to an entry or under an entry with a trailing slash")
		}
	}
	for _, domainID := range allowance.DomainIDs {
		if err := validateOracleDomainTx(ctx, tx, workID, contractVersion, "direct-caller allowance", domainID); err != nil {
			return err
		}
	}
	for _, binding := range allowance.LawBindings {
		if err := validateOracleLawPinTx(ctx, tx, workID, contractVersion, "direct-caller allowance", binding); err != nil {
			return err
		}
	}
	if err := validateWorkerJobSizeInheritanceTx(ctx, tx, workID, payload.JobID, sizeCap, basis); err != nil {
		return err
	}
	if err := validateWorkerJobCorrectionBaseTx(ctx, tx, workID, payload.JobID, basis.BaseCommit); err != nil {
		return err
	}
	payload.ProductionSizeMax = &sizeCap
	payload.MeasurementBasis = basis
	payload.DirectCallerAllowance = allowance
	return nil
}

// validateWorkerJobCorrectionBaseTx holds CD-0225 D1's correction rule: a
// job in correction keeps its original base, so a new revision recording
// another base hides the job's unaccepted work behind a later foundation and
// refuses. The original base is the lowest recorded capped revision's base.
func validateWorkerJobCorrectionBaseTx(ctx context.Context, tx *sql.Tx, workID, jobID, baseCommit string) error {
	corrections, err := workerJobCorrectionStatesTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if !corrections[jobID] {
		return nil
	}
	content, err := readWorkerJobSizeContentTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	original := ""
	originalRevision := int64(0)
	for binding, entry := range content {
		if binding.JobID != jobID || entry.basis == nil {
			continue
		}
		if original == "" || entry.revision < originalRevision {
			original = entry.basis.BaseCommit
			originalRevision = entry.revision
		}
	}
	if original == "" {
		return nil
	}
	if baseCommit != original {
		return workerJobSizeAuthorFailure("record_worker_job measurement_basis base_commit "+baseCommit+" hides the correction's original base "+original,
			"record the correction revision on the original base_commit")
	}
	return nil
}

// validateWorkerJobSizeShape enforces the closed shape the retained payload
// must satisfy on authoring and on replay alike: the three members are
// present together or absent together, the cap is bounded, the basis pins a
// raw base commit and a bounded unique contained-path inventory, and the
// allowance binds the job's own Project with bounded mechanisms, Domains,
// and pinned law bindings.
func validateWorkerJobSizeShape(projectScope string, sizeCap *int64, basis *WorkerJobMeasurementBasis, allowance *WorkerJobCallerAllowance, fail workerJobSizeFailure) error {
	present := 0
	if sizeCap != nil {
		present++
	}
	if basis != nil {
		present++
	}
	if allowance != nil {
		present++
	}
	if present != 0 && present != 3 {
		return fail("worker.job_recorded production_size_max, measurement_basis, and direct_caller_allowance are present together or absent together: an editing job carries all three and a read-only job none",
			"carry all three members for an editing job or none for a read-only job")
	}
	if present == 0 {
		return nil
	}
	if *sizeCap < 1 || *sizeCap > WorkerJobSizeCapMax {
		return fail(fmt.Sprintf("worker.job_recorded production_size_max %d is outside 1 to %d", *sizeCap, WorkerJobSizeCapMax),
			"record a positive production-size cap of at most "+fmt.Sprint(WorkerJobSizeCapMax))
	}
	if !worktreeSHAPattern.MatchString(basis.BaseCommit) {
		return fail("worker.job_recorded measurement_basis base_commit must be a raw git commit oid", "pin the clean integration base commit")
	}
	if len(basis.Paths) < 1 || len(basis.Paths) > WorkerJobMeasuredPathsMax {
		return fail(fmt.Sprintf("worker.job_recorded measurement_basis carries %d paths; 1 to %d are declared", len(basis.Paths), WorkerJobMeasuredPathsMax),
			"inventory the explicit edit paths, at most "+fmt.Sprint(WorkerJobMeasuredPathsMax))
	}
	seen := map[string]bool{}
	for _, measured := range basis.Paths {
		if problem := normalizedRelativeRepoPathProblem("worker.job_recorded measurement_basis paths", measured.Path); problem != "" {
			return fail(problem, "supply a normalized relative repository path")
		}
		if seen[measured.Path] {
			return fail("worker.job_recorded measurement_basis carries path "+measured.Path+" twice", "inventory each measured path once")
		}
		seen[measured.Path] = true
		if !containsString(workerJobMeasuredPathClasses, measured.Class) {
			return fail("worker.job_recorded measurement_basis path "+measured.Path+" carries class "+measured.Class+" outside the closed classification vocabulary",
				"classify each measured path production, test, documentation, or generated")
		}
		switch {
		case measured.Class == "generated" && measured.GeneratorRef == "":
			return fail("worker.job_recorded measurement_basis path "+measured.Path+" classifies generated without its owning generator_ref", "name the generator that owns the generated path")
		case measured.Class != "generated" && measured.GeneratorRef != "":
			return fail("worker.job_recorded measurement_basis path "+measured.Path+" carries generator_ref outside the generated classification", "drop generator_ref or classify the path generated")
		}
		if measured.GeneratorRef != "" {
			if problem := normalizedRelativeRepoPathProblem("worker.job_recorded measurement_basis generator_ref", measured.GeneratorRef); problem != "" {
				return fail(problem, "supply a normalized relative repository path")
			}
		}
	}
	if len(allowance.ProjectID) < 2 || len(allowance.ProjectID) > oracleProjectIDMaxBytes {
		return fail("worker.job_recorded direct_caller_allowance project_id must be between 2 and 128 bytes", "name the permitted Project")
	}
	if allowance.ProjectID != projectScope {
		return fail("worker.job_recorded direct_caller_allowance project_id does not equal the recorded project_scope", "name the job's own Project scope")
	}
	if len(allowance.Mechanisms) < 1 || len(allowance.Mechanisms) > WorkerJobAllowanceMechanismsMax {
		return fail(fmt.Sprintf("worker.job_recorded direct_caller_allowance carries %d mechanisms; 1 to %d are declared", len(allowance.Mechanisms), WorkerJobAllowanceMechanismsMax),
			"name the changed owner mechanisms, at most "+fmt.Sprint(WorkerJobAllowanceMechanismsMax))
	}
	for _, mechanism := range allowance.Mechanisms {
		if mechanism.ProjectID != allowance.ProjectID {
			return fail("worker.job_recorded direct_caller_allowance mechanism project_id "+mechanism.ProjectID+" is not the allowance's permitted Project", "name only mechanisms of the allowance's one permitted Project")
		}
		if err := validateWorkContextRepoPath(mechanism.Path); err != nil {
			return fail("worker.job_recorded direct_caller_allowance mechanism path must be a contained repository path", "supply a normalized relative source path")
		}
		if err := oracleBoundedText(mechanism.EntryPoint, "direct-caller allowance mechanism entry_point"); err != nil {
			return fail("worker.job_recorded direct_caller_allowance mechanism entry_point must be between 1 and 512 bytes without control characters", "name the entry point that owns the behavior")
		}
	}
	if len(allowance.DomainIDs) < 1 || len(allowance.DomainIDs) > WorkerJobAllowanceDomainsMax {
		return fail(fmt.Sprintf("worker.job_recorded direct_caller_allowance carries %d domain_ids; 1 to %d are declared", len(allowance.DomainIDs), WorkerJobAllowanceDomainsMax),
			"name the approved affected Domains the allowance keeps, at most "+fmt.Sprint(WorkerJobAllowanceDomainsMax))
	}
	if err := validateUniqueOracleReferences(allowance.DomainIDs, func(value string) bool { return len(value) >= 1 && len(value) <= oracleDomainIDMaxBytes },
		"worker.job_recorded direct_caller_allowance domain_id ", " is outside the Domain identity bound", "worker.job_recorded direct_caller_allowance names Domain ", "name each Domain once per allowance"); err != nil {
		return err
	}
	if allowance.LawBindings == nil {
		return fail("worker.job_recorded direct_caller_allowance law_bindings must be present, possibly empty", "carry law_bindings as a list, empty when no law binds the allowance")
	}
	if len(allowance.LawBindings) > OracleLawBindingsMax {
		return fail("worker.job_recorded direct_caller_allowance carries more than 8 law bindings", "bind at most 8 pinned law revisions per allowance")
	}
	seenLaws := map[string]bool{}
	for _, binding := range allowance.LawBindings {
		if err := validateWorkerJobLawSource("direct-caller allowance", binding.Source); err != nil {
			return err
		}
		if seenLaws[binding.Source.LawID] {
			return fail("worker.job_recorded direct_caller_allowance binds law "+binding.Source.LawID+" twice", "bind each law once per allowance")
		}
		seenLaws[binding.Source.LawID] = true
		if err := oracleBoundedText(binding.Clause, "direct-caller allowance law binding clause for law "+binding.Source.LawID); err != nil {
			return fail("worker.job_recorded direct_caller_allowance law binding clause for law "+binding.Source.LawID+" must be between 1 and 512 bytes without control characters", "state the clause the obligation serves")
		}
	}
	return nil
}

// workerJobSizeRevision is one recorded revision's size content, read back
// from its worker.job_recorded event.
type workerJobSizeRevision struct {
	revision  int64
	sizeCap   int64
	basis     *WorkerJobMeasurementBasis
	allowance *WorkerJobCallerAllowance
}

// readWorkerJobSizeContentTx scans the work's worker.job_recorded events once
// and maps every recorded revision binding onto the size content its event
// retained (nil members when the revision predates the size members). Like
// the oracle reader, the size content reads from the events — no projection
// column carries it — and the queryer already in hand is used, never a
// *Store reach through the pool inside a transaction.
func readWorkerJobSizeContentTx(ctx context.Context, q queryer, workID string) (map[WorkerJobBinding]workerJobSizeRevision, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.job_id'), json_extract(payload,'$.revision'), json_extract(payload,'$.digest'), json_extract(payload,'$.production_size_max'), json_extract(payload,'$.measurement_basis'), json_extract(payload,'$.direct_caller_allowance') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=?`, string(SubjectWorkItem), workID, WorkerJobRecorded)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot read recorded worker-job size content", true, "retry once the event log is readable", err)
	}
	defer rows.Close()
	content := map[WorkerJobBinding]workerJobSizeRevision{}
	for rows.Next() {
		var jobID, digest string
		var revision int64
		var rawCap, rawBasis, rawAllowance sql.NullString
		if err := rows.Scan(&jobID, &revision, &digest, &rawCap, &rawBasis, &rawAllowance); err != nil {
			return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot scan recorded worker-job size content", true, "retry once the event log is readable", err)
		}
		entry := workerJobSizeRevision{revision: revision}
		if present(rawCap) {
			var sizeCap int64
			if err := json.Unmarshal([]byte(rawCap.String), &sizeCap); err != nil {
				return nil, newFailure(KindInvariantViolation, "worker_job_size", "a recorded worker-job revision carries a production_size_max that is not one integer", false, "rebuild the worker-job events from the authoritative log")
			}
			entry.sizeCap = sizeCap
		}
		if present(rawBasis) {
			var basis WorkerJobMeasurementBasis
			if err := json.Unmarshal([]byte(rawBasis.String), &basis); err != nil {
				return nil, newFailure(KindInvariantViolation, "worker_job_size", "a recorded worker-job revision carries a measurement basis that is not one closed object", false, "rebuild the worker-job events from the authoritative log")
			}
			entry.basis = &basis
		}
		if present(rawAllowance) {
			var allowance WorkerJobCallerAllowance
			if err := json.Unmarshal([]byte(rawAllowance.String), &allowance); err != nil {
				return nil, newFailure(KindInvariantViolation, "worker_job_size", "a recorded worker-job revision carries a direct-caller allowance that is not one closed object", false, "rebuild the worker-job events from the authoritative log")
			}
			entry.allowance = &allowance
		}
		content[WorkerJobBinding{JobID: jobID, Revision: revision, Digest: digest}] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot scan recorded worker-job size content", true, "retry once the event log is readable", err)
	}
	return content, nil
}

func present(raw sql.NullString) bool {
	return raw.Valid && raw.String != "" && raw.String != "null"
}

// latestWorkerJobSizeRevision returns the job's highest recorded revision
// entry, or nil when the job holds no recorded revision.
func latestWorkerJobSizeRevision(content map[WorkerJobBinding]workerJobSizeRevision, jobID string) *workerJobSizeRevision {
	var latest *workerJobSizeRevision
	for binding, entry := range content {
		if binding.JobID != jobID {
			continue
		}
		copied := entry
		if latest == nil || copied.revision > latest.revision {
			latest = &copied
		}
	}
	return latest
}

// workerJobCorrectionStatesTx reports which of the work's jobs are in
// correction (CD-0225 D1): each carries an implementation-lane attempt that
// completed, failed, or was rejected since the job's last satisfied revision
// — or with no satisfied revision at all. The read uses the queryer already
// in hand and issues no nested query while rows are open.
func workerJobCorrectionStatesTx(ctx context.Context, q queryer, workID string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.worker_job.job_id'), json_extract(payload,'$.attempt_id'), seq FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.capability_class')='implementation' AND json_extract(payload,'$.worker_job.job_id') IS NOT NULL ORDER BY seq`, string(SubjectWorkItem), workID, WorkerDispatched)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot read the work's implementation dispatches", true, "retry once the event log is readable", err)
	}
	type dispatchRecord struct {
		attempt string
		seq     int64
	}
	byJob := map[string][]dispatchRecord{}
	for rows.Next() {
		var jobID, attemptID string
		var seq int64
		if err := rows.Scan(&jobID, &attemptID, &seq); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot scan the work's implementation dispatches", true, "retry once the event log is readable", err)
		}
		byJob[jobID] = append(byJob[jobID], dispatchRecord{attempt: attemptID, seq: seq})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot scan the work's implementation dispatches", true, "retry once the event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot close the implementation dispatch read", true, "retry once the event log is readable", err)
	}
	corrections := map[string]bool{}
	for jobID, dispatches := range byJob {
		cutoff, err := workerJobSatisfactionSeqTx(ctx, q, workID, jobID)
		if err != nil {
			return nil, err
		}
		for _, dispatch := range dispatches {
			if dispatch.seq <= cutoff {
				continue
			}
			terminal, err := workerAttemptTerminatedTx(ctx, q, workID, dispatch.attempt)
			if err != nil {
				return nil, err
			}
			if terminal {
				corrections[jobID] = true
				break
			}
		}
	}
	return corrections, nil
}

// workerJobSatisfactionSeqTx returns the highest event seq that satisfied a
// revision of the named job, or zero when no revision is satisfied.
func workerJobSatisfactionSeqTx(ctx context.Context, q queryer, workID, jobID string) (int64, error) {
	var cutoff int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(e.seq),0) FROM worker_job_revisions r JOIN domain_events e ON e.event_id=r.satisfied_result_ref WHERE r.work_id=? AND r.job_id=? AND r.state='satisfied'`, workID, jobID).Scan(&cutoff)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "worker_job_size", "cannot read the job's satisfied revisions", true, "retry once the worker-job projection is readable", err)
	}
	return cutoff, nil
}

// workerAttemptTerminatedTx reports whether one attempt reached a terminal
// state the correction rule counts: a completed or failed attempt in the
// worker-attempt projection, or a recorded rejection of the attempt.
func workerAttemptTerminatedTx(ctx context.Context, q queryer, workID, attemptID string) (bool, error) {
	var lifecycle string
	err := q.QueryRowContext(ctx, `SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&lifecycle)
	switch {
	case err == nil && (lifecycle == "completed" || lifecycle == "failed"):
		return true, nil
	case err != nil && err != sql.ErrNoRows:
		return false, wrapFailure(KindUnavailable, "worker_job_size", "cannot read the worker attempt state", true, "retry once the worker-attempt projection is readable", err)
	}
	var rejected int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='reject_worker_result' AND json_extract(payload,'$.worker_attempt_id')=?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attemptID).Scan(&rejected); err != nil {
		return false, wrapFailure(KindUnavailable, "worker_job_size", "cannot read the attempt's rejections", true, "retry once the event log is readable", err)
	}
	return rejected > 0, nil
}

// workerJobMeasuredPathsTx returns one job's measured paths (CD-0225 D1):
// the inventory its latest recorded revision declared. Slice 3 extends this
// one function with the sealed observed_delivery paths of the job's earlier
// implementation attempts; every caller shares the seam.
func workerJobMeasuredPathsTx(ctx context.Context, q queryer, workID, jobID string) ([]string, error) {
	content, err := readWorkerJobSizeContentTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	latest := latestWorkerJobSizeRevision(content, jobID)
	if latest == nil || latest.basis == nil {
		return []string{}, nil
	}
	paths := make([]string, 0, len(latest.basis.Paths))
	for _, measured := range latest.basis.Paths {
		paths = append(paths, measured.Path)
	}
	return paths, nil
}

// workerJobCallerExclusionsTx computes the caller_exclusions one job's
// implementation dispatch carries (CD-0227 D1) and refuses a set above the
// packet's declared bound.
func workerJobCallerExclusionsTx(ctx context.Context, q queryer, workID, jobID string) ([]string, error) {
	exclusions, err := workerJobCorrectionPathsTx(ctx, q, workID, jobID)
	if err != nil {
		return nil, err
	}
	if len(exclusions) > WorkerJobCallerExclusionsMax {
		return nil, newFailure(KindInvalidOperation, "workflow_action",
			fmt.Sprintf("worker job %s's caller exclusions would carry %d paths; the packet declares at most %d", jobID, len(exclusions), WorkerJobCallerExclusionsMax),
			false, "accept or split an open correction job through accept_worker_result or record_worker_job, then dispatch again")
	}
	return exclusions, nil
}

// workerJobCorrectionPathsTx reads the measured paths of the work's OTHER
// jobs currently in correction, sorted and deduplicated, with no bound.
func workerJobCorrectionPathsTx(ctx context.Context, q queryer, workID, jobID string) ([]string, error) {
	corrections, err := workerJobCorrectionStatesTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	exclusions := []string{}
	for correctionJob := range corrections {
		if correctionJob == jobID {
			continue
		}
		measured, err := workerJobMeasuredPathsTx(ctx, q, workID, correctionJob)
		if err != nil {
			return nil, err
		}
		for _, path := range measured {
			if !seen[path] {
				seen[path] = true
				exclusions = append(exclusions, path)
			}
		}
	}
	slices.Sort(exclusions)
	return exclusions, nil
}

// validateWorkerJobSizeInheritanceTx holds CD-0225 D1's inheritance rule:
// once a job has a capped revision, every later revision of the same job_id
// stays capped, never raises the lowest earlier cap, and never reclassifies
// a path an earlier revision classified production away from production.
func validateWorkerJobSizeInheritanceTx(ctx context.Context, tx *sql.Tx, workID, jobID string, sizeCap int64, basis *WorkerJobMeasurementBasis) error {
	content, err := readWorkerJobSizeContentTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	earlier := make([]workerJobSizeRevision, 0, len(content))
	for binding, entry := range content {
		if binding.JobID == jobID {
			earlier = append(earlier, entry)
		}
	}
	slices.SortFunc(earlier, func(a, b workerJobSizeRevision) int { return int(a.revision - b.revision) })
	if len(earlier) == 0 {
		return nil
	}
	lowest := earlier[0].sizeCap
	production := map[string]bool{}
	for _, revision := range earlier {
		lowest = min(lowest, revision.sizeCap)
		if revision.basis == nil {
			continue
		}
		for _, measured := range revision.basis.Paths {
			if measured.Class == "production" {
				production[measured.Path] = true
			}
		}
	}
	if sizeCap > lowest {
		return workerJobSizeAuthorFailure(fmt.Sprintf("record_worker_job production_size_max %d raises the job's lowest recorded cap %d", sizeCap, lowest),
			"record a cap at or below the lowest capped revision of the job")
	}
	classified := map[string]string{}
	for _, measured := range basis.Paths {
		classified[measured.Path] = measured.Class
	}
	for path := range production {
		if classified[path] != "production" {
			return workerJobSizeAuthorFailure("record_worker_job measurement_basis reclassifies measured path "+path+" away from an earlier revision's production classification",
				"keep every path an earlier revision classified production classified production")
		}
	}
	return nil
}

// A capped job's later revision must stay capped: a member-less revision of
// a job with an earlier capped revision refuses at authoring. The trio
// presence rule alone cannot express this, because a read-only revision is
// shape-valid; the job's own history decides.
func refuseUncappedRevisionOfCappedJob(ctx context.Context, tx *sql.Tx, workID, jobID string) error {
	content, err := readWorkerJobSizeContentTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	for binding, entry := range content {
		if binding.JobID == jobID && entry.basis != nil {
			return workerJobSizeAuthorFailure("record_worker_job revision of a capped job must stay capped",
				"carry all three size members: a later revision of a capped job cannot drop the cap")
		}
	}
	return nil
}

// admitWorkerJobSizeDispatchTx is the size-capable dispatch admission
// (CD-0225 D1, CD-0227 D1). On a pin without the declared members every
// dispatch admits unchanged. An implementation dispatch of an uncapped job
// refuses: the recorded cap is what makes an editing dispatch admissible. A
// capped implementation dispatch serializes against the work's corrections —
// another job's correction holds its measured paths — and binds the base:
// a first dispatch measures from base_commit, and a correction dispatches
// only a clean subject that is the recorded base or its sealed descendant.
func admitWorkerJobSizeDispatchTx(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, lane LaneDefinition, binding WorkerJobBinding, subject string) error {
	if !workflowJobSizeActive(definition) {
		return nil
	}
	content, err := readWorkerJobSizeContentTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	entry, recorded := content[binding]
	if !recorded || entry.basis == nil {
		if lane.CapabilityClass != "implementation" {
			return nil
		}
		return newFailure(KindInvalidOperation, "workflow_action",
			"dispatch_worker implementation lane requires a capped worker job: the pinned definition version "+fmt.Sprint(definition.Version)+" records production size",
			false, "record a capped revision with record_worker_job")
	}
	if lane.CapabilityClass != "implementation" {
		return nil
	}
	corrections, err := workerJobCorrectionStatesTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	pathScope, err := workerJobPathScopeTx(ctx, tx, workID, binding)
	if err != nil {
		return err
	}
	for correctionJob := range corrections {
		if correctionJob == binding.JobID {
			continue
		}
		measured, err := workerJobMeasuredPathsTx(ctx, tx, workID, correctionJob)
		if err != nil {
			return err
		}
		for _, measuredPath := range measured {
			if !workerJobPathsIntersect(pathScope, measuredPath) {
				continue
			}
			return newFailure(KindInvalidOperation, "workflow_action",
				"dispatch_worker implementation job "+binding.JobID+"'s path scope intersects correction job "+correctionJob+"'s measured paths",
				false, "accept correction job "+correctionJob+"'s result or split it through record_worker_job")
		}
	}
	// The base rules bind the admitted clean subject. A packet without a
	// work-context subject falls back to the sealed preparation's clean
	// observation of the claimed worktree; with neither, no foundation can
	// be compared and the dispatch refuses.
	proof := workerDispatchPreparationFrom(ctx)
	if subject == "" && proof != nil && proof.proof.workID == workID && proof.proof.observation != nil && proof.proof.observation.State == "clean" {
		subject = proof.proof.observation.Commit
	}
	if subject == "" {
		return newFailure(KindProjectionConflict, "workflow_action",
			"the implementation dispatch of worker job "+binding.JobID+" has no admitted clean subject to compare with its recorded base_commit",
			true, "prepare the dispatch from the claimed clean worktree through the authenticated mutation boundary, then dispatch again")
	}
	if !corrections[binding.JobID] {
		if subject != entry.basis.BaseCommit {
			return newFailure(KindInvalidOperation, "workflow_action",
				"dispatch_worker implementation job "+binding.JobID+"'s recorded base_commit "+entry.basis.BaseCommit+" does not equal the admitted clean subject "+subject,
				false, "record a fresh revision with the current foundation through record_worker_job")
		}
		return nil
	}
	if proof == nil || proof.proof.workID != workID {
		return newFailure(KindInvalidOperation, "workflow_action",
			"the correction dispatch of worker job "+binding.JobID+" reached assembly without its sealed subject preparation",
			false, "prepare the dispatch through the authenticated mutation boundary")
	}
	if proof.proof.observation == nil || proof.proof.observation.State != "clean" || proof.proof.observation.Commit != subject {
		return newFailure(KindProjectionConflict, "workflow_action",
			"the correction dispatch of worker job "+binding.JobID+" has no clean admitted subject at the claimed worktree",
			true, "re-qualify the candidate at the claimed worktree, then prepare and dispatch again")
	}
	if proof.proof.correctionAncestry.failure != nil {
		return proof.proof.correctionAncestry.failure
	}
	if subject != entry.basis.BaseCommit && !proof.proof.correctionAncestry.strictDescendant {
		return newFailure(KindUnauthorizedDispatch, "workflow_action",
			"the correction dispatch of worker job "+binding.JobID+" is not the recorded base "+entry.basis.BaseCommit+" nor its descendant",
			false, "dispatch a subject that is the recorded base_commit or descends from it")
	}
	return nil
}

// workerJobPathScopeTx reads the recorded path scope of one revision from
// the worker-job projection.
func workerJobPathScopeTx(ctx context.Context, tx *sql.Tx, workID string, binding WorkerJobBinding) ([]string, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT path_scope FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, binding.JobID, binding.Revision).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return []string{}, nil
		}
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot read the dispatched job's path scope", true, "retry once the worker-job projection is readable", err)
	}
	var pathScope []string
	if err := json.Unmarshal([]byte(raw), &pathScope); err != nil {
		return nil, newFailure(KindInvariantViolation, "worker_job_size", "a recorded worker-job revision carries a path scope that is not one string list", false, "rebuild the worker-job projection from the event log")
	}
	return pathScope, nil
}

// workerJobPathsIntersect reports whether one measured path overlaps a
// recorded path scope: equal to an entry, under one, or containing one.
func workerJobPathsIntersect(pathScope []string, measuredPath string) bool {
	for _, scope := range pathScope {
		if measuredPath == scope || strings.HasPrefix(measuredPath, scope+"/") || strings.HasPrefix(scope, measuredPath+"/") {
			return true
		}
	}
	return false
}

// WorkerJobBindingFromDispatchPayload extracts the dispatched job binding
// one dispatch_worker payload carries, for the pre-transaction freshness
// preparation. It returns nil when the payload names no readable binding;
// the dispatch admission re-refuses authoritatively in the transaction.
func WorkerJobBindingFromDispatchPayload(payload json.RawMessage) *WorkerJobBinding {
	var parsed struct {
		WorkerPacket struct {
			Inputs struct {
				WorkerJob *WorkerJobBinding `json:"worker_job"`
			} `json:"inputs"`
		} `json:"worker_packet"`
	}
	if json.Unmarshal(payload, &parsed) != nil || parsed.WorkerPacket.Inputs.WorkerJob == nil {
		return nil
	}
	binding := *parsed.WorkerPacket.Inputs.WorkerJob
	if binding.JobID == "" || binding.Revision < 1 || binding.Digest == "" {
		return nil
	}
	return &binding
}

// WorkerAttemptIDFromRejectionPayload extracts the rejected attempt one
// combined rejection payload carries, so its derived retry's preparation
// anchors at the retried job's recorded base.
func WorkerAttemptIDFromRejectionPayload(payload json.RawMessage) string {
	var parsed struct {
		AttemptID string `json:"attempt_id"`
	}
	if json.Unmarshal(payload, &parsed) != nil {
		return ""
	}
	return parsed.AttemptID
}
