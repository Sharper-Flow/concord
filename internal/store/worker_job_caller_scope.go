package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// The direct-caller surface (CD-0227 as amended by CD-0242 D2/D6/D7/D14):
// a caller-capable definition records a closed direct-caller allowance
// beside a job's explicit path scope; a dispatch binds the recorded paths of
// the work's other corrections as exclusions from the allowance. No size
// cap or measurement exists beside it.

// Closed bounds of the caller surface. An over-bound member refuses whole;
// content is never truncated to fit.
const (
	// WorkerJobCallerExclusionPathMax is the packet's declared
	// caller_exclusions item maxLength (worker_packet5_worker_job), counted
	// in Unicode characters like the schema keyword, never bytes.
	WorkerJobCallerExclusionPathMax = 512
	WorkerJobCallerExclusionsMax    = 64
	WorkerJobAllowanceMechanismsMax = 8
	WorkerJobAllowanceDomainsMax    = 8
)

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

// workflowCallerScopeActive reports whether a pinned definition carries the
// direct-caller allowance: the record_worker_job action's declared payload
// names the direct_caller_allowance member. Capability is derived from the
// declared action member exactly like workflowOwnerOracleActive, so no
// capability bit or behavior flag exists.
func workflowCallerScopeActive(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "record_worker_job" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "direct_caller_allowance" {
				return true
			}
		}
		return false
	}
	return false
}

// invalidWorkerPayloadFailure adapts the payload validator's refusal
// constructor to the shared allowance shape check.
func invalidWorkerPayloadFailure(detail, _ string) error {
	return invalidWorkerPayload(detail)
}

// authoredFieldPresent reports whether one authored member arrived as a
// present, non-null JSON value.
func authoredFieldPresent(fields map[string]json.RawMessage, name string) bool {
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
		return nil, newFailure(KindInvalidPayload, "workflow_action", refusal, false, "supply the declared closed object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, newFailure(KindInvalidPayload, "workflow_action", refusal, false, "supply exactly one object")
	}
	return &value, nil
}

// authorWorkerJobAllowanceTx authors the direct_caller_allowance member of
// one record_worker_job call. A pin without the declared member never
// reaches here with one: the action preflight refuses an undeclared payload
// member. On a caller-capable pin the member is parsed, shape-checked, and
// joined against the active contract's Domain and law boundaries, all
// before the digest is derived (CD-0227 D1).
func authorWorkerJobAllowanceTx(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, definition WorkflowDefinition, fields map[string]json.RawMessage, payload *WorkerJobRecordedPayload) error {
	if !workflowCallerScopeActive(definition) {
		return nil
	}
	if !authoredFieldPresent(fields, "direct_caller_allowance") {
		return nil
	}
	allowance, err := decodeClosedWorkerJobObject[WorkerJobCallerAllowance](fields["direct_caller_allowance"], "record_worker_job direct_caller_allowance is not one closed direct-caller-allowance object")
	if err != nil {
		return err
	}
	if err := validateWorkerJobAllowanceShape(payload.ProjectScope, allowance, workerJobAllowanceAuthorFailure); err != nil {
		return err
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
	payload.DirectCallerAllowance = allowance
	return nil
}

func workerJobAllowanceAuthorFailure(detail, recovery string) error {
	return newFailure(KindInvalidPayload, "workflow_action", detail, false, recovery)
}

// validateWorkerJobAllowanceShape enforces the closed shape the retained
// payload must satisfy on authoring and on replay alike: the allowance binds
// the job's own Project with bounded mechanisms, Domains, and pinned law
// bindings.
func validateWorkerJobAllowanceShape(projectScope string, allowance *WorkerJobCallerAllowance, fail func(detail, recovery string) error) error {
	if allowance == nil {
		return nil
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

// readWorkerJobAllowancesTx scans the work's worker.job_recorded events once
// and maps every recorded revision binding onto the direct-caller allowance
// its event retained (absent when the revision carries none). Like the
// oracle reader, the allowance reads from the events — no projection column
// carries it — and the queryer already in hand is used, never a *Store
// reach through the pool inside a transaction.
func readWorkerJobAllowancesTx(ctx context.Context, q queryer, workID string) (map[WorkerJobBinding]*WorkerJobCallerAllowance, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.job_id'), json_extract(payload,'$.revision'), json_extract(payload,'$.digest'), json_extract(payload,'$.direct_caller_allowance') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=?`, string(SubjectWorkItem), workID, WorkerJobRecorded)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot read recorded worker-job allowances", true, "retry once the event log is readable", err)
	}
	defer rows.Close()
	allowances := map[WorkerJobBinding]*WorkerJobCallerAllowance{}
	for rows.Next() {
		var jobID, digest string
		var revision int64
		var rawAllowance sql.NullString
		if err := rows.Scan(&jobID, &revision, &digest, &rawAllowance); err != nil {
			return nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot scan recorded worker-job allowances", true, "retry once the event log is readable", err)
		}
		if !rawAllowance.Valid || rawAllowance.String == "" || rawAllowance.String == "null" {
			continue
		}
		var allowance WorkerJobCallerAllowance
		if err := json.Unmarshal([]byte(rawAllowance.String), &allowance); err != nil {
			return nil, newFailure(KindInvariantViolation, "worker_job_caller_scope", "a recorded worker-job revision carries a direct-caller allowance that is not one closed object", false, "rebuild the worker-job events from the authoritative log")
		}
		allowances[WorkerJobBinding{JobID: jobID, Revision: revision, Digest: digest}] = &allowance
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot scan recorded worker-job allowances", true, "retry once the event log is readable", err)
	}
	return allowances, nil
}

// workerJobCorrectionStatesTx reports which of the work's jobs are in
// correction: each carries an implementation-lane attempt that completed or
// failed since the job's last satisfied revision — or with no satisfied
// revision at all. It also returns, per correction job, the implementation
// attempt ids dispatched after that cutoff. The read uses the queryer
// already in hand and issues no nested query while rows are open.
func workerJobCorrectionStatesTx(ctx context.Context, q queryer, workID string) (map[string]bool, map[string][]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.worker_job.job_id'), json_extract(payload,'$.attempt_id'), seq FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.capability_class')='implementation' AND json_extract(payload,'$.worker_job.job_id') IS NOT NULL ORDER BY seq`, string(SubjectWorkItem), workID, WorkerDispatched)
	if err != nil {
		return nil, nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot read the work's implementation dispatches", true, "retry once the event log is readable", err)
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
			return nil, nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot scan the work's implementation dispatches", true, "retry once the event log is readable", err)
		}
		byJob[jobID] = append(byJob[jobID], dispatchRecord{attempt: attemptID, seq: seq})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot scan the work's implementation dispatches", true, "retry once the event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot close the implementation dispatch read", true, "retry once the event log is readable", err)
	}
	corrections := map[string]bool{}
	attempts := map[string][]string{}
	for jobID, dispatches := range byJob {
		cutoff, err := workerJobSatisfactionSeqTx(ctx, q, workID, jobID)
		if err != nil {
			return nil, nil, err
		}
		for _, dispatch := range dispatches {
			if dispatch.seq <= cutoff {
				continue
			}
			lifecycle, err := workerAttemptLifecycleTx(ctx, q, workID, dispatch.attempt)
			if err != nil {
				return nil, nil, err
			}
			if lifecycle == "" {
				continue
			}
			attempts[jobID] = append(attempts[jobID], dispatch.attempt)
			if lifecycle == "completed" || lifecycle == "failed" {
				corrections[jobID] = true
			}
		}
	}
	return corrections, attempts, nil
}

// workerJobSatisfactionSeqTx returns the highest event seq that satisfied a
// revision of the named job, or zero when no revision is satisfied.
func workerJobSatisfactionSeqTx(ctx context.Context, q queryer, workID, jobID string) (int64, error) {
	var cutoff int64
	err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(e.seq),0) FROM worker_job_revisions r JOIN domain_events e ON e.event_id=r.satisfied_result_ref WHERE r.work_id=? AND r.job_id=? AND r.state='satisfied'`, workID, jobID).Scan(&cutoff)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot read the job's satisfied revisions", true, "retry once the worker-job projection is readable", err)
	}
	return cutoff, nil
}

// workerAttemptLifecycleTx returns one attempt's lifecycle state, or the
// empty string when no projection row exists for it.
func workerAttemptLifecycleTx(ctx context.Context, q queryer, workID, attemptID string) (string, error) {
	var lifecycle string
	err := q.QueryRowContext(ctx, `SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&lifecycle)
	switch {
	case err == nil:
		return lifecycle, nil
	case err == sql.ErrNoRows:
		return "", nil
	default:
		return "", wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot read the worker attempt state", true, "retry once the worker-attempt projection is readable", err)
	}
}

// workerJobCorrectionPathsTx returns the recorded paths of every job in
// correction, keyed by job_id: the path scope each job's latest recorded
// revision declares (CD-0227 D1's recorded correction paths). The revisions
// read once for all jobs — no per-job event scans.
func workerJobCorrectionPathsTx(ctx context.Context, q queryer, workID string) (map[string][]string, error) {
	corrections, _, err := workerJobCorrectionStatesTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	paths := map[string][]string{}
	if len(corrections) == 0 {
		return paths, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT r.job_id, r.path_scope FROM worker_job_revisions r
		WHERE r.work_id=? AND r.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=r.work_id AND latest.job_id=r.job_id)`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot read the correction jobs' recorded path scopes", true, "retry once the worker-job projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var jobID, raw string
		if err := rows.Scan(&jobID, &raw); err != nil {
			return nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot scan the correction jobs' recorded path scopes", true, "retry once the worker-job projection is readable", err)
		}
		if !corrections[jobID] {
			continue
		}
		var scope []string
		if err := json.Unmarshal([]byte(raw), &scope); err != nil {
			return nil, newFailure(KindInvariantViolation, "worker_job_caller_scope", "a recorded worker-job revision carries a path scope that is not one string list", false, "rebuild the worker-job projection from the event log")
		}
		paths[jobID] = scope
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_caller_scope", "cannot scan the correction jobs' recorded path scopes", true, "retry once the worker-job projection is readable", err)
	}
	return paths, nil
}

// workerJobCorrectionExclusions returns the recorded paths of the jobs in
// correction other than jobID, sorted and deduplicated, with no bound.
func workerJobCorrectionExclusions(measured map[string][]string, jobID string) []string {
	seen := map[string]bool{}
	exclusions := []string{}
	for correctionJob, paths := range measured {
		if correctionJob == jobID {
			continue
		}
		for _, path := range paths {
			if !seen[path] {
				seen[path] = true
				exclusions = append(exclusions, path)
			}
		}
	}
	slices.Sort(exclusions)
	return exclusions
}

// validateWorkerJobPacketExclusions refuses a caller-exclusions set above
// the packet's declared bounds (worker_packet5_worker_job): at most
// WorkerJobCallerExclusionsMax paths, each at most
// WorkerJobCallerExclusionPathMax Unicode characters. Both sites that
// produce or project the set share this one guard: the computation owner
// refuses the dispatch on its error, and the continuity ready projection
// omits the whole set, so the read stays available and the dispatch refuses
// with its recovery. The character count matches the schema keyword's
// Unicode semantics, never a byte length.
func validateWorkerJobPacketExclusions(jobID string, paths []string) error {
	if len(paths) > WorkerJobCallerExclusionsMax {
		return newFailure(KindInvalidOperation, "workflow_action",
			fmt.Sprintf("worker job %s's caller exclusions would carry %d paths; the packet declares at most %d", jobID, len(paths), WorkerJobCallerExclusionsMax),
			false, "accept the open correction job's result through accept_worker_result, then dispatch again")
	}
	for _, path := range paths {
		if utf8.RuneCountInString(path) > WorkerJobCallerExclusionPathMax {
			return newFailure(KindInvalidOperation, "workflow_action",
				fmt.Sprintf("worker job %s's caller exclusions carry a path above the packet's %d-character bound", jobID, WorkerJobCallerExclusionPathMax),
				false, "accept the open correction job's result through accept_worker_result, then dispatch again")
		}
	}
	return nil
}

// workerJobCallerExclusionsTx computes the caller_exclusions one job's
// implementation dispatch carries (CD-0227 D1) and refuses a set above the
// packet's declared bounds.
func workerJobCallerExclusionsTx(ctx context.Context, q queryer, workID, jobID string) ([]string, error) {
	paths, err := workerJobCorrectionPathsTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	exclusions := workerJobCorrectionExclusions(paths, jobID)
	if err := validateWorkerJobPacketExclusions(jobID, exclusions); err != nil {
		return nil, err
	}
	return exclusions, nil
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

// workerPacketCallerExclusions reads the caller exclusions one validated
// dispatch packet carries on inputs.worker_job.
func workerPacketCallerExclusions(packetRaw json.RawMessage) []string {
	var packet struct {
		Inputs struct {
			WorkerJob struct {
				CallerExclusions []string `json:"caller_exclusions"`
			} `json:"worker_job"`
		} `json:"inputs"`
	}
	if json.Unmarshal(packetRaw, &packet) != nil {
		return nil
	}
	return packet.Inputs.WorkerJob.CallerExclusions
}

// WorkerAttemptIDFromRejectionPayload extracts the rejected attempt one
// combined rejection payload carries, so its derived retry dispatch anchors
// at the same admitted subject.
func WorkerAttemptIDFromRejectionPayload(payload json.RawMessage) string {
	var parsed struct {
		AttemptID string `json:"attempt_id"`
	}
	if json.Unmarshal(payload, &parsed) != nil {
		return ""
	}
	return parsed.AttemptID
}
