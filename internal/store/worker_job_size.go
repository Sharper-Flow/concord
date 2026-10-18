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
	WorkerJobMeasuredPathsMax       = 256
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
	payload.ProductionSizeMax = &sizeCap
	payload.MeasurementBasis = basis
	payload.DirectCallerAllowance = allowance
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

// workerJobSizeRevision is one earlier recorded revision's size content,
// read back from its worker.job_recorded event.
type workerJobSizeRevision struct {
	revision int64
	sizeCap  int64
	basis    WorkerJobMeasurementBasis
}

// readWorkerJobSizeRevisionsTx scans the work's worker.job_recorded events
// once and returns every earlier recorded revision of the named job that
// carries the size members, in revision order. Like the oracle reader, the
// size content reads from the events — no projection column carries it — and
// the queryer already in hand is used, never a *Store reach through the pool
// inside a transaction.
func readWorkerJobSizeRevisionsTx(ctx context.Context, q queryer, workID, jobID string) ([]workerJobSizeRevision, error) {
	rows, err := q.QueryContext(ctx, `SELECT json_extract(payload,'$.revision'), json_extract(payload,'$.production_size_max'), json_extract(payload,'$.measurement_basis') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? ORDER BY 1`, string(SubjectWorkItem), workID, WorkerJobRecorded, jobID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot read recorded worker-job size content", true, "retry once the event log is readable", err)
	}
	defer rows.Close()
	earlier := []workerJobSizeRevision{}
	for rows.Next() {
		var revision int64
		var rawCap, rawBasis sql.NullString
		if err := rows.Scan(&revision, &rawCap, &rawBasis); err != nil {
			return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot scan recorded worker-job size content", true, "retry once the event log is readable", err)
		}
		if !rawCap.Valid || !rawBasis.Valid || rawCap.String == "" || rawCap.String == "null" || rawBasis.String == "" || rawBasis.String == "null" {
			continue
		}
		var sizeCap int64
		var basis WorkerJobMeasurementBasis
		if err := json.Unmarshal([]byte(rawCap.String), &sizeCap); err != nil {
			return nil, newFailure(KindInvariantViolation, "worker_job_size", "a recorded worker-job revision carries a production_size_max that is not one integer", false, "rebuild the worker-job events from the authoritative log")
		}
		if err := json.Unmarshal([]byte(rawBasis.String), &basis); err != nil {
			return nil, newFailure(KindInvariantViolation, "worker_job_size", "a recorded worker-job revision carries a measurement basis that is not one closed object", false, "rebuild the worker-job events from the authoritative log")
		}
		earlier = append(earlier, workerJobSizeRevision{revision: revision, sizeCap: sizeCap, basis: basis})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot scan recorded worker-job size content", true, "retry once the event log is readable", err)
	}
	return earlier, nil
}

// validateWorkerJobSizeInheritanceTx holds CD-0225 D1's inheritance rule:
// once a job has a capped revision, every later revision of the same job_id
// stays capped, never raises the lowest earlier cap, and never reclassifies
// a path an earlier revision classified production away from production.
func validateWorkerJobSizeInheritanceTx(ctx context.Context, tx *sql.Tx, workID, jobID string, sizeCap int64, basis *WorkerJobMeasurementBasis) error {
	earlier, err := readWorkerJobSizeRevisionsTx(ctx, tx, workID, jobID)
	if err != nil {
		return err
	}
	if len(earlier) == 0 {
		return nil
	}
	lowest := earlier[0].sizeCap
	production := map[string]bool{}
	for _, revision := range earlier {
		lowest = min(lowest, revision.sizeCap)
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
	earlier, err := readWorkerJobSizeRevisionsTx(ctx, tx, workID, jobID)
	if err != nil {
		return err
	}
	if len(earlier) == 0 {
		return nil
	}
	return workerJobSizeAuthorFailure("record_worker_job revision of a capped job must stay capped",
		"carry all three size members: a later revision of a capped job cannot drop the cap")
}
