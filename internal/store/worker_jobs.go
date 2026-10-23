package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// WorkerJobRecorded is the event kind that records one immutable worker-job
// revision under its owning work aggregate (CD-0205). The work aggregate owns
// the job; attempt IDs and lane result tokens keep owning identity and
// evidence. Recording is fold-only: the projection row derives from this
// event, and a second recording of the same (job_id, revision) must carry
// byte-identical content or the fold refuses.
const WorkerJobRecorded = "worker.job_recorded"

// workerJobIDPattern bounds the stable job identity. It stays deliberately
// open — job ids name recorded behavioral jobs, not a closed vocabulary — but
// must not be empty or oversized, and must not contain control characters.
var workerJobIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{1,127}$`)

// workerJobPredicatePattern is the same closed predicate-id shape the lane
// report schema admits — one or more suffix characters after the prefix, up
// to the report grammar's 128-byte bound — so a recorded job can only
// declare predicates a dispatched report could tie.
var workerJobPredicatePattern = regexp.MustCompile(`^predicate:[A-Za-z0-9][A-Za-z0-9._:-]{0,117}$`)

// WorkerJobPrerequisite is one exact prerequisite reference: a recorded
// revision of another job, and optionally the result reference that satisfied
// it. A revision is ready only when every named prerequisite revision is
// recorded and satisfied.
type WorkerJobPrerequisite struct {
	JobID     string `json:"job_id"`
	Revision  int64  `json:"revision"`
	ResultRef string `json:"result_ref,omitempty"`
}

// WorkerJobReadiness carries the coordinator's semantic readiness assertion
// with its evidence references. The core records it; it never derives or
// judges it.
type WorkerJobReadiness struct {
	Ready    bool     `json:"ready"`
	Evidence []string `json:"evidence,omitempty"`
}

// WorkerJobRecordedPayload is the closed authoring shape of one immutable
// worker-job revision. Every field the packet binds travels on the record:
// the bounded objective, the stopping condition, Project/path scope, the
// relevant predicates and checks, exact prerequisite revision references,
// unresolved references, and the reserved integration work. ContractVersion
// is the core-derived parent authority: the fold verifies it against the
// work item's active approved contract, so a recording cannot assert parent
// authority the core state does not hold. The digest is core-derived over
// the content; the fold recomputes and refuses a mismatch, so a caller
// cannot assert a digest it did not derive from this content.
type WorkerJobRecordedPayload struct {
	WorkflowVersionFields
	JobID               string                  `json:"job_id"`
	Revision            int64                   `json:"revision"`
	ContractVersion     int64                   `json:"contract_version"`
	Objective           string                  `json:"objective"`
	StoppingCondition   string                  `json:"stopping_condition"`
	ProjectScope        string                  `json:"project_scope,omitempty"`
	PathScope           []string                `json:"path_scope"`
	PredicateIDs        []string                `json:"predicate_ids"`
	Checks              []string                `json:"checks"`
	Prerequisites       []WorkerJobPrerequisite `json:"prerequisites"`
	UnresolvedRefs      []string                `json:"unresolved_refs"`
	ReservedIntegration string                  `json:"reserved_integration,omitempty"`
	Readiness           *WorkerJobReadiness     `json:"readiness,omitempty"`
	// AcceptanceOracle is the owner-level acceptance oracle: typed
	// immutable job content, optional on every revision a pre-oracle
	// definition recorded and required only by oracle-capable definition
	// versions. It enters the content digest, so a revision's oracle is as
	// immutable as the rest of its content, and it reads back only from
	// this event — no projection column carries it.
	AcceptanceOracle *AcceptanceOracle `json:"acceptance_oracle,omitempty"`
	// The production-size members are retired (CD-0242 D6). They decode
	// the history a v1 revision may carry — they entered that revision's
	// content digest, so the digest derivation keeps them — and a live v2
	// recording refuses them. DirectCallerAllowance stays live content
	// (CD-0227 D1 as amended): a caller-capable definition records it
	// beside the explicit path scope.
	ProductionSizeMax     *int64                    `json:"production_size_max,omitempty"`
	MeasurementBasis      json.RawMessage           `json:"measurement_basis,omitempty"`
	DirectCallerAllowance *WorkerJobCallerAllowance `json:"direct_caller_allowance,omitempty"`
	// ChecksRepair is the core-derived checks repair metadata (CD-0205 D4):
	// the stated reason and, for every named unresolved revision, its
	// binding beside the old checks the exact recorded events hold. The
	// action path derives it from recorded history and the fold verifies it
	// against the same events, so a caller never asserts it. It enters the
	// content digest and is omitempty, so every historical revision without
	// one serializes byte-identically and keeps its digest.
	ChecksRepair *WorkerJobChecksRepair `json:"checks_repair,omitempty"`
	Digest       string                 `json:"digest"`
}

// DeriveWorkerJobDigest derives the canonical content digest of one worker-job
// revision. The digest covers every content field and excludes the digest
// field itself, so two recordings of one (job_id, revision) agree exactly when
// their content agrees. Go's struct marshal order is fixed, which makes the
// derivation deterministic for authors and for the fold that verifies it. The
// oracle member is omitempty: a revision without one serializes exactly as it
// did before the member existed, so every historical digest still holds.
func DeriveWorkerJobDigest(payload WorkerJobRecordedPayload) string {
	content := struct {
		JobID                 string                    `json:"job_id"`
		Revision              int64                     `json:"revision"`
		ContractVersion       int64                     `json:"contract_version"`
		Objective             string                    `json:"objective"`
		StoppingCondition     string                    `json:"stopping_condition"`
		ProjectScope          string                    `json:"project_scope,omitempty"`
		PathScope             []string                  `json:"path_scope"`
		PredicateIDs          []string                  `json:"predicate_ids"`
		Checks                []string                  `json:"checks"`
		Prerequisites         []WorkerJobPrerequisite   `json:"prerequisites"`
		UnresolvedRefs        []string                  `json:"unresolved_refs"`
		ReservedIntegration   string                    `json:"reserved_integration,omitempty"`
		Readiness             *WorkerJobReadiness       `json:"readiness,omitempty"`
		AcceptanceOracle      *AcceptanceOracle         `json:"acceptance_oracle,omitempty"`
		ProductionSizeMax     *int64                    `json:"production_size_max,omitempty"`
		MeasurementBasis      json.RawMessage           `json:"measurement_basis,omitempty"`
		DirectCallerAllowance *WorkerJobCallerAllowance `json:"direct_caller_allowance,omitempty"`
		ChecksRepair          *WorkerJobChecksRepair    `json:"checks_repair,omitempty"`
	}{payload.JobID, payload.Revision, payload.ContractVersion, payload.Objective, payload.StoppingCondition, payload.ProjectScope, payload.PathScope, payload.PredicateIDs, payload.Checks, payload.Prerequisites, payload.UnresolvedRefs, payload.ReservedIntegration, payload.Readiness, payload.AcceptanceOracle, payload.ProductionSizeMax, payload.MeasurementBasis, payload.DirectCallerAllowance, payload.ChecksRepair}
	raw, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// WorkerJobChecksRepairMaxBytes bounds the canonical JSON bytes of one
// recorded revision's checks-repair metadata, exactly as the acceptance
// oracle bound bounds its own content: the writer owns the figure, the
// schema leaves it unstated, and an over-bound repair refuses whole instead
// of truncating a named revision's recorded past. A revision without the
// metadata carries no bound and serializes byte-identically to its
// historical shape.
const WorkerJobChecksRepairMaxBytes = 32 * 1024

// validateWorkerJobRecordedPayload is the registered semantic validator: the
// event is a work-aggregate mutation that advances the work version, so the
// workflow version fields come first, then the closed content shape.
func validateWorkerJobRecordedPayload(event Event, payload WorkerJobRecordedPayload) error {
	if event.SubjectType != SubjectWorkItem || event.SubjectID == "" {
		return invalidWorkerPayload("worker.job_recorded must record under a work item")
	}
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if !workerJobIDPattern.MatchString(payload.JobID) || payload.Revision < 1 {
		return invalidWorkerPayload("worker.job_recorded job_id or revision has invalid shape")
	}
	if payload.ContractVersion < 1 {
		return invalidWorkerPayload("worker.job_recorded contract_version must name the parent contract")
	}
	if len(payload.Objective) < 1 || len(payload.Objective) > 4096 {
		return invalidWorkerPayload("worker.job_recorded objective must be between 1 and 4096 bytes")
	}
	if len(payload.StoppingCondition) < 1 || len(payload.StoppingCondition) > 2048 {
		return invalidWorkerPayload("worker.job_recorded stopping_condition must be between 1 and 2048 bytes")
	}
	if len(payload.ProjectScope) > 128 || len(payload.ReservedIntegration) > 4096 {
		return invalidWorkerPayload("worker.job_recorded project_scope or reserved_integration exceeds its bound")
	}
	if payload.PathScope == nil || payload.PredicateIDs == nil || payload.Checks == nil || payload.Prerequisites == nil || payload.UnresolvedRefs == nil {
		return invalidWorkerPayload("worker.job_recorded arrays must be present, possibly empty")
	}
	if len(payload.PathScope) > 64 || len(payload.PredicateIDs) > 8 || len(payload.Checks) > 64 || len(payload.Prerequisites) > 64 || len(payload.UnresolvedRefs) > 64 {
		return invalidWorkerPayload("worker.job_recorded carries too many scope, predicate, check, prerequisite, or unresolved entries")
	}
	for i, path := range payload.PathScope {
		if problem := normalizedRelativeRepoPathProblem("worker.job_recorded path_scope", path); problem != "" {
			return invalidWorkerPayload(problem)
		}
		for j, other := range payload.PathScope {
			if j < i && other == path {
				return invalidWorkerPayload("worker.job_recorded path_scope entries must be unique")
			}
		}
	}
	for _, predicate := range payload.PredicateIDs {
		if !workerJobPredicatePattern.MatchString(predicate) {
			return invalidWorkerPayload("worker.job_recorded predicate_ids entries must be declared predicate ids")
		}
	}
	for _, check := range payload.Checks {
		if len(check) < 1 || len(check) > 256 {
			return invalidWorkerPayload("worker.job_recorded checks entries must be between 1 and 256 bytes")
		}
	}
	for _, prerequisite := range payload.Prerequisites {
		if !workerJobIDPattern.MatchString(prerequisite.JobID) || prerequisite.Revision < 1 || len(prerequisite.ResultRef) > 512 {
			return invalidWorkerPayload("worker.job_recorded prerequisites must name a recorded revision and a bounded result reference")
		}
	}
	for _, unresolved := range payload.UnresolvedRefs {
		if len(unresolved) < 1 || len(unresolved) > 512 {
			return invalidWorkerPayload("worker.job_recorded unresolved_refs entries must be between 1 and 512 bytes")
		}
	}
	if payload.Readiness != nil && len(payload.Readiness.Evidence) > 16 {
		return invalidWorkerPayload("worker.job_recorded readiness carries too many evidence references")
	}
	// The retired size members decode history but record nothing new
	// (CD-0242 D6). A live recording is payload version 2; a replayed v1
	// revision keeps the members it may carry.
	if event.replaySourcePayloadVersion == 0 && (payload.ProductionSizeMax != nil || len(payload.MeasurementBasis) != 0) {
		return invalidWorkerPayload("worker.job_recorded production_size_max and measurement_basis are retired by CD-0242 D6: no production-line cap, size measurement, or mandatory split applies")
	}
	// The oracle graph is re-proved from the payload's own retained content:
	// the closed shape and the job's declared predicates decide, never a
	// registry read, so a replayed historical oracle holds without today's
	// Domain or contract state.
	if payload.AcceptanceOracle != nil {
		if err := validateAcceptanceOracle(payload.AcceptanceOracle, payload.PredicateIDs); err != nil {
			return err
		}
	}
	// The direct-caller allowance re-proves its closed shape from the
	// payload's own retained content, like the oracle graph: presence and
	// bounds decide, never a registry read.
	if payload.DirectCallerAllowance != nil {
		if err := validateWorkerJobAllowanceShape(payload.ProjectScope, payload.DirectCallerAllowance, invalidWorkerPayloadFailure); err != nil {
			return err
		}
	}
	if payload.ChecksRepair != nil {
		metadata, err := json.Marshal(payload.ChecksRepair)
		if err != nil {
			return invalidWorkerPayload("worker.job_recorded checks_repair cannot encode: " + err.Error())
		}
		if len(metadata) > WorkerJobChecksRepairMaxBytes {
			return invalidWorkerPayload(fmt.Sprintf("worker.job_recorded checks_repair metadata measures %d bytes and exceeds the %d byte bound; it is refused, never truncated", len(metadata), WorkerJobChecksRepairMaxBytes))
		}
		if len(payload.ChecksRepair.Reason) < 1 || len(payload.ChecksRepair.Reason) > 4096 {
			return invalidWorkerPayload("worker.job_recorded checks_repair reason must be between 1 and 4096 bytes")
		}
		entries := payload.ChecksRepair.Revisions
		if len(entries) < 1 || len(entries) > 64 {
			return invalidWorkerPayload("worker.job_recorded checks_repair names between 1 and 64 revisions")
		}
		seenRevisions := make(map[int64]bool, len(entries))
		for _, entry := range entries {
			if entry.WorkerJob.JobID != payload.JobID || entry.WorkerJob.Revision < 1 || entry.WorkerJob.Revision >= payload.Revision || entry.WorkerJob.Digest == "" || seenRevisions[entry.WorkerJob.Revision] {
				return invalidWorkerPayload("worker.job_recorded checks_repair names distinct earlier recorded revisions of the same job")
			}
			seenRevisions[entry.WorkerJob.Revision] = true
			if len(entry.Checks) < 1 || len(entry.Checks) > 64 {
				return invalidWorkerPayload("worker.job_recorded checks_repair carries the named revision's old checks")
			}
			for _, check := range entry.Checks {
				if len(check) < 1 || len(check) > 256 {
					return invalidWorkerPayload("worker.job_recorded checks_repair old checks entries must be between 1 and 256 bytes")
				}
			}
		}
	}
	if !workerProvenancePattern.MatchString(payload.Digest) {
		return invalidWorkerPayload("worker.job_recorded digest must be a sha256 digest")
	}
	if derived := DeriveWorkerJobDigest(payload); derived != payload.Digest {
		return invalidWorkerPayload("worker.job_recorded digest does not match the derived content digest")
	}
	return nil
}

// normalizedRelativeRepoPathProblem states why a path is not one normalized
// relative repository path (bounded, control-character free, no absolute,
// traversal, or self segment), or returns "" when it is. Path scope entries,
// measured inventory paths, and generator references share the rule; each
// caller owns its failure route and entry uniqueness.
func normalizedRelativeRepoPathProblem(label, path string) string {
	if len(path) < 1 || len(path) > 512 {
		return label + " entries must be between 1 and 512 bytes"
	}
	if strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return label + " entries must not contain control characters"
	}
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, "./") || strings.HasSuffix(path, "/") {
		return label + " entries must be normalized relative paths"
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return label + " entries must be normalized relative paths"
		}
	}
	return ""
}

// foldWorkerJobRecorded projects one immutable worker-job revision under the
// work aggregate and advances the work version. A revision is immutable by
// primary key and revisions of one job are contiguous: the first recording of
// a job is revision 1, and each later recording names the next revision. A
// re-recording of an existing revision is admitted only with the recorded
// digest, so a revision never changes content in place. The parent authority
// is core-derived and verified here, against the same projections, for the
// live append and for log-ordered replay alike: the contract version is the
// active approved contract, every predicate is one that contract approved,
// and the Project scope is a member Project of the work.
func foldWorkerJobRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerJobRecordedPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	active, err := activeWorkflowContractVersion(ctx, tx, event.SubjectID, "worker_job")
	if err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "fold_event", "worker.job_recorded requires an approved parent contract", false, "approve the work contract before recording a worker job")
		}
		return err
	}
	if active != payload.ContractVersion {
		return newFailure(KindInvalidPayload, "fold_event", "worker.job_recorded contract_version does not match the active parent contract", false, "record the job under the active contract version")
	}
	for _, predicate := range payload.PredicateIDs {
		var declared int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? AND predicate_id=?`, event.SubjectID, active, predicate).Scan(&declared); err != nil {
			if err == sql.ErrNoRows {
				return newFailure(KindInvalidPayload, "fold_event", "worker.job_recorded predicate_ids name a predicate the active parent contract did not approve", false, "record only predicates the active parent contract approved")
			}
			return wrapFailure(KindUnavailable, "fold_event", "cannot verify the worker-job predicate authority", true, "retry once the contract projection is readable", err)
		}
	}
	if payload.ProjectScope != "" {
		var member int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_projects WHERE work_id=? AND project_id=?`, event.SubjectID, payload.ProjectScope).Scan(&member); err != nil {
			if err == sql.ErrNoRows {
				return newFailure(KindInvalidPayload, "fold_event", "worker.job_recorded project_scope is not a member Project of the work", false, "record the job under one of the work's member Projects")
			}
			return wrapFailure(KindUnavailable, "fold_event", "cannot verify the worker-job Project scope", true, "retry once the membership projection is readable", err)
		}
	}
	var recorded string
	switch err := tx.QueryRowContext(ctx, `SELECT digest FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, event.SubjectID, payload.JobID, payload.Revision).Scan(&recorded); {
	case err == nil:
		if recorded != payload.Digest {
			return newFailure(KindProjectionConflict, "fold_event", "a worker-job revision is immutable and cannot change content in place", false, "record a new revision of the job instead")
		}
		return advanceWorkflowVersion(ctx, tx, event, payload.WorkflowVersionFields)
	case err != sql.ErrNoRows:
		return wrapFailure(KindUnavailable, "fold_event", "cannot compare the recorded worker-job revision", true, "retry once the worker-job projection is readable", err)
	}
	next, err := nextWorkerJobRevision(ctx, tx, event.SubjectID, payload.JobID)
	if err != nil {
		return err
	}
	if payload.Revision != next {
		return newFailure(KindInvalidPayload, "fold_event", "worker.job_recorded revision must be the next revision of the job", false, "record the job's next contiguous revision")
	}
	// The checks repair's metadata is derived truth at the folded event's
	// own strict prefix: the fold re-derives it from the exact recorded
	// events before this event and refuses a payload whose metadata asserts
	// old checks, bindings, or names the history does not hold (CD-0205
	// D4). The digest check above already pinned the bytes.
	if err := verifyWorkerJobChecksRepairTx(ctx, tx, event.SubjectID, payload, event.Seq-1); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	readiness, err := json.Marshal(payload.Readiness)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event", "cannot encode worker-job readiness", false, "report the encoding failure", err)
	}
	var readinessColumn any
	if payload.Readiness != nil {
		readinessColumn = string(readiness)
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	if _, err := tx.ExecContext(ctx, `INSERT INTO worker_job_revisions
		(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,reserved_integration,readiness,digest,state,recorded_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'recorded', ?)`,
		event.SubjectID, payload.JobID, payload.Revision, payload.ContractVersion, payload.Objective, payload.StoppingCondition, payload.ProjectScope,
		workflowJSON(nonNilStrings(payload.PathScope)), workflowJSON(nonNilStrings(payload.PredicateIDs)), workflowJSON(nonNilStrings(payload.Checks)),
		workflowJSON(nonNilPrerequisites(payload.Prerequisites)), workflowJSON(nonNilStrings(payload.UnresolvedRefs)), payload.ReservedIntegration, readinessColumn, payload.Digest, now); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot record the worker-job revision", true, "retry once the database is writable", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE worker_job_revisions SET (group_permission,group_review_ref,correction_ref)=(SELECT group_permission,group_review_ref,correction_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision<? AND (group_permission<>'' OR correction_ref<>'') ORDER BY revision DESC LIMIT 1) WHERE work_id=? AND job_id=? AND revision=? AND EXISTS(SELECT 1 FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision<? AND (group_permission<>'' OR correction_ref<>''))`, event.SubjectID, payload.JobID, payload.Revision, event.SubjectID, payload.JobID, payload.Revision, event.SubjectID, payload.JobID, payload.Revision); err != nil {
		return workflowProjectionError(err, "cannot carry the assignment's admitted group to its new revision")
	}
	return nil
}

func nonNilPrerequisites(values []WorkerJobPrerequisite) []WorkerJobPrerequisite {
	if values == nil {
		return []WorkerJobPrerequisite{}
	}
	return values
}

// nextWorkerJobRevision returns the revision the next recording of a job
// takes: one past the highest recorded revision, or 1 for a new job.
func nextWorkerJobRevision(ctx context.Context, q queryer, workID, jobID string) (int64, error) {
	var highest int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM worker_job_revisions WHERE work_id=? AND job_id=?`, workID, jobID).Scan(&highest); err != nil {
		return 0, wrapFailure(KindUnavailable, "worker_job", "cannot read the job's recorded revisions", true, "retry once the worker-job projection is readable", err)
	}
	return highest + 1, nil
}

// workflowRecordWorkerJobEvents builds the events the record_worker_job
// action appends: one worker.job_recorded event authored through the shared
// builder.
func workflowRecordWorkerJobEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, fields map[string]json.RawMessage, eventID string, expected int64) ([]Event, error) {
	payload, err := authorWorkerJobRevisionTx(ctx, tx, request, fields)
	if err != nil {
		return nil, err
	}
	event, err := workerJobRecordedEvent(eventID, request, actor, expected, payload)
	if err != nil {
		return nil, err
	}
	return []Event{event}, nil
}

// workerJobRecordedEvent serializes one authored revision into its
// worker.job_recorded event's closed field map.
func workerJobRecordedEvent(eventID string, request WorkflowActionExecutionRequest, actor string, expected int64, payload *WorkerJobRecordedPayload) (Event, error) {
	values := map[string]any{}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "workflow_action", "cannot encode the worker-job revision", false, "report the encoding failure", err)
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "workflow_action", "cannot encode the worker-job revision", false, "report the encoding failure", err)
	}
	return workflowTypedEvent(eventID, WorkerJobRecorded, request.WorkID, actor, request.Now, expected, values), nil
}

// authorWorkerJobRevisionTx shares the authority checks and core-derived
// revision content between ordinary job authoring and split successors.
// All reads use the caller's transaction.
func authorWorkerJobRevisionTx(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, fields map[string]json.RawMessage) (*WorkerJobRecordedPayload, error) {
	jobID := workflowFieldStringDefault(fields, "job_id", "")
	if !workerJobIDPattern.MatchString(jobID) {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job job_id has invalid shape", false, "name the job with a stable identifier")
	}
	contractVersion, err := activeWorkflowContractVersion(ctx, tx, request.WorkID, "worker_job")
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, newFailure(KindInvalidOperation, "workflow_action", "record_worker_job requires an approved parent contract", false, "approve the work contract before recording a worker job")
		}
		return nil, err
	}
	var project string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, request.WorkID).Scan(&project); err != nil && err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the work's primary Project", true, "retry once the membership projection is readable", err)
	}
	revision, err := nextWorkerJobRevision(ctx, tx, request.WorkID, jobID)
	if err != nil {
		return nil, err
	}
	var prerequisites []WorkerJobPrerequisite
	if raw, ok := fields["prerequisites"]; ok {
		if err := json.Unmarshal(raw, &prerequisites); err != nil {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job prerequisites must be prerequisite objects", false, "name each prerequisite by job_id and revision")
		}
	}
	ready := false
	if raw, ok := fields["ready"]; ok {
		if err := json.Unmarshal(raw, &ready); err != nil {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job ready must be a boolean", false, "state the coordinator readiness as true or false")
		}
	}
	readinessEvidence := workflowFieldStrings(fields, "readiness_evidence")
	if ready && len(readinessEvidence) == 0 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job fields.readiness_evidence requires at least one evidence reference when ready=true", false, "correct_request")
	}
	// The acceptance oracle is authored through the pinned
	// definition's declared action member: an oracle-capable version
	// requires one, and every earlier version refuses the member, so
	// capability travels with the pin instead of a behavior flag. The
	// authority joins (approved predicates, affected Domains, pinned law,
	// Project scope, retained readiness evidence) run here, at author
	// time, inside the caller's transaction.
	registered, err := verifyWorkflowDefinitionPinTx(ctx, tx, nil, request.WorkID)
	if err != nil {
		return nil, err
	}
	oracleCapable := workflowOwnerOracleActive(registered.Definition)
	var oracle *AcceptanceOracle
	if raw, present := fields["acceptance_oracle"]; present && len(raw) > 0 && string(raw) != "null" {
		if !oracleCapable {
			return nil, oracleFailure(KindInvalidPayload, "record_worker_job acceptance_oracle is not admitted by the pinned workflow definition version "+fmt.Sprint(registered.Definition.Version), "record the job under an oracle-capable definition version or drop the oracle")
		}
		oracle, err = decodeAcceptanceOracle(raw)
		if err != nil {
			return nil, err
		}
	}
	if oracleCapable && oracle == nil {
		return nil, oracleFailure(KindInvalidPayload, "record_worker_job requires an acceptance_oracle on definition version "+fmt.Sprint(registered.Definition.Version), "author the closed owner/case/control oracle for the job")
	}
	// The checks repair (CD-0205 D4): the authoring fields travel together
	// and only on a repair-capable pinned version; the core derives the
	// recorded metadata from the exact recorded events and the caller never
	// asserts it.
	repairCapable := workflowChecksRepairActive(registered.Definition)
	var repairRevisions []int64
	repairReason := ""
	if raw, present := fields["repair_revisions"]; present {
		if !repairCapable {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job repair_revisions is not admitted by the pinned workflow definition version "+fmt.Sprint(registered.Definition.Version), false, "record the repair on a repair-capable definition version")
		}
		if err := json.Unmarshal(raw, &repairRevisions); err != nil {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job repair_revisions must be an array of revision integers", false, "name the unresolved revisions the repair repairs")
		}
	}
	if raw, present := fields["repair_reason"]; present {
		if !repairCapable {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job repair_reason is not admitted by the pinned workflow definition version "+fmt.Sprint(registered.Definition.Version), false, "record the repair on a repair-capable definition version")
		}
		if err := json.Unmarshal(raw, &repairReason); err != nil {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job repair_reason must be one bounded prose string", false, "state why the named revisions' checks could not be executed or decided")
		}
	}
	payload := WorkerJobRecordedPayload{
		JobID: jobID, Revision: revision, ContractVersion: contractVersion,
		Objective:           workflowFieldStringDefault(fields, "objective", ""),
		StoppingCondition:   workflowFieldStringDefault(fields, "stopping_condition", ""),
		ProjectScope:        project,
		PathScope:           nonNilStrings(workflowFieldStrings(fields, "path_scope")),
		PredicateIDs:        nonNilStrings(workflowFieldStrings(fields, "predicate_ids")),
		Checks:              nonNilStrings(workflowFieldStrings(fields, "checks")),
		Prerequisites:       nonNilPrerequisites(prerequisites),
		UnresolvedRefs:      nonNilStrings(workflowFieldStrings(fields, "unresolved_refs")),
		ReservedIntegration: workflowFieldStringDefault(fields, "reserved_integration", ""),
		Readiness:           &WorkerJobReadiness{Ready: ready, Evidence: nonNilStrings(readinessEvidence)},
		AcceptanceOracle:    oracle,
	}
	if oracle != nil {
		if err := validateAcceptanceOracle(oracle, payload.PredicateIDs); err != nil {
			return nil, err
		}
		if err := validateAcceptanceOracleAuthorityTx(ctx, tx, request.WorkID, contractVersion, oracle); err != nil {
			return nil, err
		}
	}
	// The direct-caller allowance authors through the pinned definition's
	// declared member like the oracle: a pre-caller version refuses the
	// member, and a caller-capable version parses and joins it here, at
	// author time, inside the caller's transaction (CD-0227 D1).
	if err := authorWorkerJobAllowanceTx(ctx, tx, request.WorkID, contractVersion, registered.Definition, fields, &payload); err != nil {
		return nil, err
	}
	if len(repairRevisions) != 0 || repairReason != "" {
		if len(repairRevisions) == 0 || repairReason == "" {
			return nil, newFailure(KindInvalidPayload, "workflow_action", "record_worker_job repair_revisions and repair_reason are supplied together", false, "name the unresolved revisions and state why their checks could not run")
		}
		watermark, err := workerChecksRepairWatermarkTx(ctx, tx, request.WorkID)
		if err != nil {
			return nil, err
		}
		metadata, err := deriveWorkerJobChecksRepairTx(ctx, tx, request.WorkID, payload, repairRevisions, repairReason, watermark)
		if err != nil {
			return nil, err
		}
		payload.ChecksRepair = metadata
	}
	payload.Digest = DeriveWorkerJobDigest(payload)
	return &payload, nil
}

// verifyWorkerDispatchedJobBindingTx is the dispatch-side anti-fabrication
// boundary (CD-0205): a worker.dispatched event that carries a worker-job
// binding must name a recorded revision of that exact job, with the digest
// the recording derived, and that revision must not already be satisfied. A
// binding with no backing record refuses the dispatch; a satisfied revision
// refuses a redispatch, on resume as on first ask — except the one dispatch
// the funded confirming review of an unconfirmed checks repair admits, whose
// authorization consumed the funding on the exact satisfied repairing
// revision (CD-0205 D4). The check runs in the fold, so it holds for live
// appends and for log-ordered replay alike: a recording always precedes its
// dispatches, and a satisfaction always precedes the redispatch it refuses,
// in any legal log. A job a split replaced dispatches no revision again
// (CD-0225 D3).
func verifyWorkerDispatchedJobBindingTx(ctx context.Context, tx *sql.Tx, event Event, attemptID string, binding *WorkerJobBinding) error {
	workID := event.SubjectID
	var digest, state string
	if err := tx.QueryRowContext(ctx, `SELECT digest,state FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, binding.JobID, binding.Revision).Scan(&digest, &state); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched worker_job does not name a recorded worker-job revision", false, "record the job revision before dispatching under it")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify the dispatched worker-job binding", true, "retry once the worker-job projection is readable", err)
	}
	if digest != binding.Digest {
		return newFailure(KindInvalidPayload, "fold_event", "worker.dispatched worker_job digest does not match the recorded revision", false, "dispatch under the recorded revision digest")
	}
	if state == "satisfied" {
		admitted, err := dispatchConsumedChecksRepairReviewTx(ctx, tx, workID, attemptID, *binding)
		if err != nil {
			return err
		}
		if !admitted {
			return newFailure(KindIllegalLifecycleTransition, "fold_event", "a satisfied worker-job revision cannot redispatch", false, "record a new revision of the job or dispatch another ready job")
		}
	}
	return nil
}

// dispatchConsumedChecksRepairReviewTx reports whether the attempt's
// dispatch_worker authorization dispatched the confirming review of exactly
// this repairing revision.
func dispatchConsumedChecksRepairReviewTx(ctx context.Context, q queryer, workID, attemptID string, binding WorkerJobBinding) (bool, error) {
	var jobID string
	var revision int64
	var digest string
	err := q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.checks_repair_review.worker_job.job_id'), json_extract(payload,'$.checks_repair_review.worker_job.revision'), json_extract(payload,'$.checks_repair_review.worker_job.digest') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? AND json_extract(payload,'$.checks_repair_review') IS NOT NULL ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attemptID).Scan(&jobID, &revision, &digest)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot read the confirming review's dispatched authorization", true, "retry once the dispatch authorization is readable", err)
	}
	return jobID == binding.JobID && revision == binding.Revision && digest == binding.Digest, nil
}

// markWorkerJobRepairReadinessTx resolves the named revisions of an accepted
// repairing revision for prerequisite readiness (CD-0205 D4): each named
// revision records the repairing acceptance as its satisfaction reference,
// so a later job's prerequisite pins resolve through that real recorded
// acceptance while the repair stands unconfirmed or confirmed. The
// correction walk keeps the named obligations unresolved until the
// confirming review's accepted ship verdict, so this readiness resolution
// never clears correction debt. It is a no-op for a non-repair revision.
func markWorkerJobRepairReadinessTx(ctx context.Context, tx *sql.Tx, event Event, binding *WorkerJobBinding) error {
	if binding == nil {
		return nil
	}
	repairs, err := readWorkerJobChecksRepairsTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	metadata, recorded := repairs[*binding]
	if !recorded {
		return nil
	}
	// Only the acceptance that opens the repair grants readiness: a later
	// acceptance bound to the repairing revision, such as the voiding
	// no_ship review's, never restores it (CD-0205 D4).
	history, err := workerChecksRepairWalkTx(ctx, tx, event.SubjectID, event.Seq-1)
	if err != nil {
		return err
	}
	repair := history.repairOfBinding(binding.JobID, binding.Revision)
	if repair == nil || repair.acceptanceSeq != 0 || repair.voided {
		return nil
	}
	// Two repairs can be recorded before either result is accepted. Check
	// again at the acceptance that would open the second repair (CD-0205 D4).
	if open := history.openUnconfirmedRepairBeside(binding.JobID, binding.Revision); open != nil {
		return newFailure(KindInvalidOperation, "fold_event",
			fmt.Sprintf("a second unconfirmed checks repair of the job cannot open while revision %d's repair stands unconfirmed", open.binding.Revision),
			false, "let the open repair's confirming review end before accepting another repairing revision of the job")
	}
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	for _, entry := range metadata.Revisions {
		if _, err := tx.ExecContext(ctx, `UPDATE worker_job_revisions SET state='satisfied', satisfied_at=?, satisfied_result_ref=? WHERE work_id=? AND job_id=? AND revision=? AND state='recorded'`, now, event.EventID, event.SubjectID, entry.WorkerJob.JobID, entry.WorkerJob.Revision); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot record the repairing revision's provisional readiness", true, "retry once the database is writable", err)
		}
	}
	return nil
}

// revertWorkerJobRepairReadinessTx voids one repair's provisional readiness:
// the named revisions lose the satisfaction the repairing acceptance gave
// them when the confirming review fails, is abandoned, is rejected, or
// completes no_ship, or when a contract supersession voids the repair. The
// satisfied-reference comparison keeps a named revision some later real
// acceptance satisfied exactly where it stands.
func revertWorkerJobRepairReadinessTx(ctx context.Context, tx *sql.Tx, workID string, binding WorkerJobBinding) error {
	var repairRef sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, binding.JobID, binding.Revision).Scan(&repairRef); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the repairing revision's disposition", true, "retry once the worker-job projection is readable", err)
	}
	repairs, err := readWorkerJobChecksRepairsTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	metadata, recorded := repairs[binding]
	if !recorded {
		return nil
	}
	for _, entry := range metadata.Revisions {
		if _, err := tx.ExecContext(ctx, `UPDATE worker_job_revisions SET state='recorded', satisfied_at=NULL, satisfied_result_ref=NULL WHERE work_id=? AND job_id=? AND revision=? AND state='satisfied' AND satisfied_result_ref=?`, workID, entry.WorkerJob.JobID, entry.WorkerJob.Revision, repairRef); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot void the repairing revision's provisional readiness", true, "retry once the database is writable", err)
		}
	}
	return nil
}

// voidWorkerJobRepairByAttemptTx reverts the provisional readiness of the
// repair whose confirming review the attempt dispatched, if any, and the
// repair is not confirmed. Prefix is the voiding event's own strict prefix,
// so the walked state is the pre-void state.
func voidWorkerJobRepairByAttemptTx(ctx context.Context, tx *sql.Tx, workID, attemptID string, prefix int64) error {
	_, binding, err := dispatchCompletionJobForAttempt(ctx, tx, workID, attemptID)
	if err != nil || binding == nil {
		return err
	}
	history, err := workerChecksRepairWalkTx(ctx, tx, workID, prefix)
	if err != nil {
		return err
	}
	repair := history.confirmingAttemptOf(attemptID)
	if repair == nil || repair.confirmed || !repair.openUnconfirmed() {
		return nil
	}
	return revertWorkerJobRepairReadinessTx(ctx, tx, workID, *binding)
}

// markAcceptedWorkerJobTx records satisfaction and provisional repair readiness
// without clearing correction debt. A confirming review judges the repairing
// revision; it never replaces that revision's recorded satisfaction reference.
func markAcceptedWorkerJobTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload) error {
	if p.ActionID != "accept_worker_result" || p.WorkerJob == nil {
		return nil
	}
	confirming, err := dispatchConsumedChecksRepairReviewTx(ctx, tx, event.SubjectID, p.WorkerAttemptID, *p.WorkerJob)
	if err != nil || confirming {
		return err
	}
	if err := markWorkerJobSatisfiedTx(ctx, tx, event, p.WorkerJob); err != nil {
		return err
	}
	return markWorkerJobRepairReadinessTx(ctx, tx, event, p.WorkerJob)
}

// markWorkerJobSatisfiedTx records the disposition an accepted worker result
// satisfies, on the exact recorded revision the accepted attempt was
// dispatched under. The guard derived the binding from the attempt's own
// worker.dispatched event, so the caller never asserts it; the fold refuses
// an acceptance whose derived disposition names a revision that is not
// recorded under that digest — a report cannot claim another or later
// revision. The acceptance event's own identifier becomes the recorded
// result reference, so a later prerequisite can pin the exact acceptance
// that satisfied its dependency. Satisfaction is a projection update in log
// order, so rebuilds derive the same state.
func markWorkerJobSatisfiedTx(ctx context.Context, tx *sql.Tx, event Event, binding *WorkerJobBinding) error {
	now := event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	result, err := tx.ExecContext(ctx, `UPDATE worker_job_revisions SET state='satisfied', satisfied_at=?, satisfied_result_ref=? WHERE work_id=? AND job_id=? AND revision=? AND digest=?`, now, event.EventID, event.SubjectID, binding.JobID, binding.Revision, binding.Digest)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot record the worker-job disposition", true, "retry once the database is writable", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the worker-job disposition result", true, "retry once the database is writable", err)
	}
	if rows == 0 {
		return newFailure(KindInvalidPayload, "fold_event", "the accepted worker-job disposition does not name the recorded dispatched revision", false, "accept under the attempt's own dispatched job revision")
	}
	return nil
}

// WorkerJobRevisionView is the browse-side view of one recorded worker-job
// revision: the dispatch binding it yields, its recorded content, its
// disposition, and whether a dispatcher may select it now.
type WorkerJobRevisionView struct {
	Binding             WorkerJobBinding        `json:"binding"`
	ContractVersion     int64                   `json:"-"`
	Objective           string                  `json:"objective"`
	StoppingCondition   string                  `json:"stopping_condition"`
	ProjectScope        string                  `json:"project_scope"`
	PathScope           []string                `json:"path_scope"`
	PredicateIDs        []string                `json:"predicate_ids"`
	Checks              []string                `json:"checks"`
	Prerequisites       []WorkerJobPrerequisite `json:"prerequisites"`
	UnresolvedRefs      []string                `json:"unresolved_refs"`
	ReservedIntegration string                  `json:"reserved_integration"`
	// AcceptanceOracle reads back from the job-recorded event — never a
	// projection column — so the view carries exactly the content the
	// revision's digest covers. The direct-caller allowance joins beside it
	// from the same events.
	AcceptanceOracle      *AcceptanceOracle         `json:"acceptance_oracle,omitempty"`
	DirectCallerAllowance *WorkerJobCallerAllowance `json:"direct_caller_allowance,omitempty"`
	ChecksRepair          *WorkerJobChecksRepair    `json:"checks_repair,omitempty"`
	State                 string                    `json:"state"`
	Ready                 bool                      `json:"ready"`
	SatisfiedResultRef    string                    `json:"satisfied_result_ref,omitempty"`
	RecordedAt            string                    `json:"recorded_at"`
}

// workerJobReadyPredicate is the one admission rule for selecting a revision
// (CD-0205), evaluated over the worker_job_revisions row aliased j: the
// revision is the latest recorded revision of its job, it is not yet
// satisfied, its recorded parent authority is still the work's active
// approved contract so a supersession strands old-authority revisions, its
// coordinator readiness is asserted ready with evidence, its unresolved
// references are empty, and every exact prerequisite revision is recorded and
// satisfied — pinned, when the prerequisite names a result reference, to the
// exact recorded acceptance that satisfied it.
const workerJobReadyPredicate = `j.state='recorded'
  AND j.revision=(SELECT MAX(latest.revision) FROM worker_job_revisions latest WHERE latest.work_id=j.work_id AND latest.job_id=j.job_id)
  AND j.contract_version=(SELECT MAX(contract.contract_version) FROM workflow_contracts contract WHERE contract.work_id=j.work_id AND contract.superseded_by IS NULL)
  AND j.readiness IS NOT NULL AND json_extract(j.readiness,'$.ready')=1 AND COALESCE(json_array_length(j.readiness,'$.evidence'),0)>0
  AND json_array_length(j.unresolved_refs)=0
  AND NOT EXISTS (SELECT 1 FROM json_each(j.prerequisites) prerequisite
    WHERE NOT EXISTS (SELECT 1 FROM worker_job_revisions satisfied
      WHERE satisfied.work_id=j.work_id
        AND satisfied.job_id=json_extract(prerequisite.value,'$.job_id')
        AND satisfied.revision=json_extract(prerequisite.value,'$.revision')
        AND satisfied.state='satisfied'
        AND (COALESCE(json_extract(prerequisite.value,'$.result_ref'),'')=''
          OR COALESCE(json_extract(prerequisite.value,'$.result_ref'),'')=COALESCE(satisfied.satisfied_result_ref,''))))`

// readWorkerJobRevisions reads every recorded revision of the work, in job
// and revision order, with the derived readiness of each. The oracle member
// joins from the job-recorded events after the projection rows are fully
// read and closed: no nested query runs while rows are open, and the read
// uses the queryer already in hand.
func readWorkerJobRevisions(ctx context.Context, q queryer, workID string) ([]WorkerJobRevisionView, error) {
	rows, err := q.QueryContext(ctx, `SELECT j.job_id,j.revision,j.digest,j.contract_version,j.objective,j.stopping_condition,j.project_scope,j.path_scope,j.predicate_ids,j.checks,j.prerequisites,j.unresolved_refs,j.reserved_integration,j.state,COALESCE(j.satisfied_result_ref,''),j.recorded_at,
  CASE WHEN `+workerJobReadyPredicate+` THEN 1 ELSE 0 END
FROM worker_job_revisions j WHERE j.work_id=? ORDER BY j.job_id,j.revision`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot read worker-job revisions", true, "retry once the worker-job projection is readable", err)
	}
	views := []WorkerJobRevisionView{}
	for rows.Next() {
		var view WorkerJobRevisionView
		var pathScope, predicates, checks, prerequisites, unresolved string
		if err := rows.Scan(&view.Binding.JobID, &view.Binding.Revision, &view.Binding.Digest, &view.ContractVersion, &view.Objective, &view.StoppingCondition, &view.ProjectScope, &pathScope, &predicates, &checks, &prerequisites, &unresolved, &view.ReservedIntegration, &view.State, &view.SatisfiedResultRef, &view.RecordedAt, &view.Ready); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan worker-job revisions", true, "retry once the worker-job projection is readable", err)
		}
		for _, column := range []struct {
			raw    string
			target any
		}{{pathScope, &view.PathScope}, {predicates, &view.PredicateIDs}, {checks, &view.Checks}, {prerequisites, &view.Prerequisites}, {unresolved, &view.UnresolvedRefs}} {
			if err := json.Unmarshal([]byte(column.raw), column.target); err != nil {
				_ = rows.Close()
				return nil, wrapFailure(KindInvariantViolation, "worker_job", "a worker-job revision column is not valid JSON", false, "rebuild the worker-job projection from the event log", err)
			}
		}
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot scan worker-job revisions", true, "retry once the worker-job projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot close the worker-job revision read", true, "retry once the worker-job projection is readable", err)
	}
	oracles, err := readWorkerJobOraclesTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	allowances, err := readWorkerJobAllowancesTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	repairs, err := readWorkerJobChecksRepairsTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	for index := range views {
		oracle, recorded := oracles[views[index].Binding]
		if !recorded {
			return nil, wrapFailure(KindInvariantViolation, "worker_job", "a worker-job revision holds no job-recorded event for its oracle", false, "rebuild the worker-job events from the authoritative log", nil)
		}
		views[index].AcceptanceOracle = oracle
		if allowance, recorded := allowances[views[index].Binding]; recorded {
			views[index].DirectCallerAllowance = allowance
		}
		views[index].ChecksRepair = repairs[views[index].Binding]
	}
	return views, nil
}

// requireWorkerJobRevisionReadyTx is the dispatch-time admission of one
// selected revision: it must be recorded under the named digest, carry the
// work's active parent contract authority, and satisfy the one readiness
// rule. Readiness depends on later dispositions, so the live dispatch
// boundary owns it; the fold keeps the immutable facts. Parent authority is
// compared explicitly so a stale assignment names the supersession, not a
// generic unreadiness: a revision recorded under a superseded contract stays
// undispatchable until a new revision is recorded under the active contract.
func requireWorkerJobRevisionReadyTx(ctx context.Context, q queryer, workID string, binding WorkerJobBinding) error {
	if err := requireWorkerJobRevisionStateReadyTx(ctx, q, workID, binding); err != nil {
		return err
	}
	oracle, err := readWorkerJobOracle(ctx, q, workID, binding)
	if err != nil {
		return err
	}
	if oracle != nil {
		var contractVersion int64
		if err := q.QueryRowContext(ctx, `SELECT contract_version FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, binding.JobID, binding.Revision).Scan(&contractVersion); err != nil {
			return err
		}
		return validateNativeJobPreparations(ctx, q, workID, contractVersion, oracle)
	}
	return nil
}

// The state recheck performs SQL only. Native release separately binds the
// prevalidated oracle metadata to unchanged event and preparation snapshots.
func requireWorkerJobRevisionStateReadyTx(ctx context.Context, q queryer, workID string, binding WorkerJobBinding) error {
	var digest string
	var contractVersion int64
	var ready bool
	err := q.QueryRowContext(ctx, `SELECT j.digest, j.contract_version, CASE WHEN `+workerJobReadyPredicate+` THEN 1 ELSE 0 END FROM worker_job_revisions j WHERE j.work_id=? AND j.job_id=? AND j.revision=?`, workID, binding.JobID, binding.Revision).Scan(&digest, &contractVersion, &ready)
	if err == sql.ErrNoRows {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_job does not name a recorded worker-job revision", false, "record the job with record_worker_job before dispatching it")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_action", "cannot read the selected worker-job revision", true, "retry once the worker-job projection is readable", err)
	}
	if digest != binding.Digest {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_job digest does not match the recorded revision", false, "dispatch the recorded revision digest")
	}
	active, err := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
	if err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindInvalidOperation, "workflow_action", "dispatch_worker worker_job requires an approved parent contract", false, "approve the work contract before dispatching a worker job")
		}
		return err
	}
	if contractVersion != active {
		return newFailure(KindInvalidOperation, "workflow_action", "the selected worker-job revision carries contract authority of version "+fmt.Sprint(contractVersion)+" while the active parent contract is version "+fmt.Sprint(active), false, "record a new revision of the job under the active contract before dispatching it")
	}
	if !ready {
		return newFailure(KindInvalidOperation, "workflow_action", "the selected worker-job revision is not ready: it is satisfied, superseded by a later revision, lacks coordinator readiness, carries unresolved references, or waits on an unsatisfied prerequisite", false, "select a ready revision from the work's worker jobs, or record a new revision")
	}
	return nil
}

// workerJobCapabilityClass admits only capability classes in the closed lane
// registry. Every registered worker executes a bounded job on a job-capable
// pin; the lane-step dispatch join separately confines where it may dispatch.
func workerJobCapabilityClass(class string) bool {
	for _, lane := range builtinLaneRegistry().entries {
		if lane.CapabilityClass == class {
			return true
		}
	}
	return false
}

// WorkerPacketJob is the inputs.worker_job a job-bound lane packet carries:
// the selected revision's identity and digest beside the bounded content the
// worker executes. The packet digest the dispatch records covers these bytes,
// so the window, the report, and the disposition all bind one revision.
type WorkerPacketJob struct {
	JobID               string                  `json:"job_id"`
	Revision            int64                   `json:"revision"`
	Digest              string                  `json:"digest"`
	Objective           string                  `json:"objective"`
	StoppingCondition   string                  `json:"stopping_condition"`
	ProjectScope        string                  `json:"project_scope"`
	PathScope           []string                `json:"path_scope"`
	PredicateIDs        []string                `json:"predicate_ids"`
	Checks              []string                `json:"checks"`
	Prerequisites       []WorkerJobPrerequisite `json:"prerequisites"`
	UnresolvedRefs      []string                `json:"unresolved_refs"`
	ReservedIntegration string                  `json:"reserved_integration"`
	// AcceptanceOracle is the one oracle copy every admitted lane receives:
	// the same recorded job content and digest the implement,
	// review, and verify packets bind. Optional and omitted on every
	// revision a pre-oracle definition recorded.
	AcceptanceOracle *AcceptanceOracle `json:"acceptance_oracle,omitempty"`
	// DirectCallerAllowance rides a caller-capable revision's packet
	// (CD-0227 D1): the conditional caller scope the worker may use
	// without another coordinator turn. Optional and omitted on every
	// revision recorded without one.
	DirectCallerAllowance *WorkerJobCallerAllowance `json:"direct_caller_allowance,omitempty"`
	// ChecksRepair rides the repairing revision's packet (CD-0205 D4): the
	// confirming review's packet carries the old checks, the new checks, and
	// the stated reason. Optional and omitted on every non-repair revision.
	ChecksRepair *WorkerJobChecksRepair `json:"checks_repair,omitempty"`
	// CallerExclusions is the recorded paths of the work's other jobs in
	// correction, computed by the core at dispatch admission and bound to
	// the packet, never the job digest. Only the implementation capability
	// class carries it.
	CallerExclusions []string `json:"caller_exclusions,omitempty"`
}

// packetJobFromView projects a recorded revision onto the packet shape.
func packetJobFromView(view WorkerJobRevisionView) WorkerPacketJob {
	return WorkerPacketJob{
		JobID: view.Binding.JobID, Revision: view.Binding.Revision, Digest: view.Binding.Digest,
		Objective: view.Objective, StoppingCondition: view.StoppingCondition, ProjectScope: view.ProjectScope,
		PathScope: nonNilStrings(view.PathScope), PredicateIDs: nonNilStrings(view.PredicateIDs), Checks: nonNilStrings(view.Checks),
		Prerequisites: nonNilPrerequisites(view.Prerequisites), UnresolvedRefs: nonNilStrings(view.UnresolvedRefs), ReservedIntegration: view.ReservedIntegration,
		AcceptanceOracle:      view.AcceptanceOracle,
		DirectCallerAllowance: view.DirectCallerAllowance,
		ChecksRepair:          view.ChecksRepair,
	}
}

// validateWorkerPacketJob is the dispatch-time job binding (CD-0205). On a
// job-capable pin the packet must carry inputs.worker_job naming a ready
// recorded revision, with content equal to the record; on every earlier pin
// the member is refused, so historical instances keep their legacy shape and
// no job is ever inferred from a task string. The one satisfied revision a
// dispatch admits is the confirming review of an open unconfirmed checks
// repair; its exact binding rides back non-nil for that dispatch.
func validateWorkerPacketJob(ctx context.Context, q queryer, definition WorkflowDefinition, workID string, lane LaneDefinition, packetRaw json.RawMessage) (*WorkerJobBinding, *WorkerJobBinding, error) {
	refuse := func(message, remedy string) (*WorkerJobBinding, *WorkerJobBinding, error) {
		return nil, nil, newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet "+message, false, remedy)
	}
	var packet struct {
		StepID string `json:"step_id"`
		Inputs struct {
			WorkerJob json.RawMessage `json:"worker_job"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return refuse("is not readable", "build a fresh packet from the current work pin")
	}
	present := len(packet.Inputs.WorkerJob) != 0 && string(packet.Inputs.WorkerJob) != "null"
	if !workflowWorkerJobsActive(definition) || !stepDeclaresAction(definition, packet.StepID, "record_worker_job") || !workerJobCapabilityClass(lane.CapabilityClass) {
		if present {
			return refuse("carries inputs.worker_job, which the pinned workflow definition does not admit for a "+lane.CapabilityClass+" lane", "dispatch without a worker job")
		}
		return nil, nil, nil
	}
	if !present {
		return refuse("carries no inputs.worker_job; the pinned workflow definition dispatches only a selected ready worker-job revision", "record the bounded job with record_worker_job and dispatch its ready revision")
	}
	decoder := json.NewDecoder(strings.NewReader(string(packet.Inputs.WorkerJob)))
	decoder.DisallowUnknownFields()
	var claimed WorkerPacketJob
	if err := decoder.Decode(&claimed); err != nil {
		return refuse("inputs.worker_job is not one closed worker-job object", "build the worker job from the recorded revision")
	}
	binding := WorkerJobBinding{JobID: claimed.JobID, Revision: claimed.Revision, Digest: claimed.Digest}
	var repairReview *WorkerJobBinding
	if err := requireWorkerJobRevisionReadyTx(ctx, q, workID, binding); err != nil {
		// The one dispatch a satisfied revision admits is the confirming
		// review of an open unconfirmed checks repair (CD-0205 D4): a
		// review-capable lane whose packet binds the exact satisfied
		// repairing revision, before that repair's one confirming dispatch
		// exists. Every other refusal keeps its text.
		admitted, admitErr := admitWorkerChecksRepairReviewDispatchTx(ctx, q, workID, lane, binding)
		if admitErr != nil {
			return nil, nil, admitErr
		}
		if !admitted {
			return nil, nil, err
		}
		repairReview = &binding
	}
	views, err := readWorkerJobRevisions(ctx, q, workID)
	if err != nil {
		return nil, nil, err
	}
	for _, view := range views {
		if view.Binding != binding {
			continue
		}
		expected := packetJobFromView(view)
		// The caller exclusions are the core's own computed set: an
		// implementation dispatch on a caller-capable pin carries exactly
		// what the core computes, and every other lane or pin carries none.
		if lane.CapabilityClass == "implementation" && workflowCallerScopeActive(definition) {
			exclusions, err := workerJobCallerExclusionsTx(ctx, q, workID, binding.JobID)
			if err != nil {
				return nil, nil, err
			}
			expected.CallerExclusions = exclusions
		} else if claimed.CallerExclusions != nil {
			return refuse("inputs.worker_job carries caller_exclusions, which only an implementation lane admits", "build the worker job from the recorded revision without caller exclusions")
		}
		recorded, _ := json.Marshal(expected)
		normalized, _ := json.Marshal(claimed)
		if string(recorded) != string(normalized) {
			return refuse("inputs.worker_job content does not match the recorded revision", "build the worker job from the recorded revision")
		}
		if lane.CapabilityClass == "verification" && len(view.Checks) == 0 {
			return refuse("inputs.worker_job for a verification worker job requires nonempty checks", "record a new revision with the commands the verification worker must execute")
		}
		return &binding, repairReview, nil
	}
	return refuse("inputs.worker_job names no recorded revision", "record the job with record_worker_job before dispatching it")
}

// dispatchCompletionJobForAttempt reads the worker-job binding the
// dispatch_worker completion authorized for an attempt. found is false when
// no dispatch_worker completion names the attempt.
func dispatchCompletionJobForAttempt(ctx context.Context, q queryer, workID, attemptID string) (bool, *WorkerJobBinding, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.worker_job'),'') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, wrapFailure(KindUnavailable, "fold_event", "cannot read the dispatch authorization's worker-job binding", true, "retry once the event log is readable", err)
	}
	if raw == "" {
		return true, nil, nil
	}
	var job WorkerJobBinding
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		return true, nil, wrapFailure(KindInvalidPayload, "fold_event", "the dispatch authorization's worker-job binding is not a binding object", false, "repair the dispatch authorization record", err)
	}
	return true, &job, nil
}

// sameWorkerJob reports whether two optional bindings name the same revision.
func sameWorkerJob(a, b *WorkerJobBinding) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
