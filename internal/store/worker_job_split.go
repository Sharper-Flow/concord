package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// WorkerJobSplit is the event kind that records one atomic split of a
// worker-job revision into successor jobs under the work aggregate (CD-0225
// D3). The same record_worker_job action appends the successors'
// worker.job_recorded events first, so the split names revisions the fold
// already holds. The predecessor reads as replaced from this event: it cannot
// dispatch again or record another revision.
const WorkerJobSplit = "worker.job_split"

// Closed bounds of the split record. Successors share the job-reference
// collection limit; a larger partition uses further recorded splits.
const (
	WorkerJobSplitSuccessorsMin   = 2
	WorkerJobSplitSuccessorsMax   = 64
	workerJobSplitEvidenceRefsMax = 16
)

// WorkerJobSplitAllocation assigns predecessor paths to one successor, with
// the evidence for the assignment.
type WorkerJobSplitAllocation struct {
	JobID        string   `json:"job_id"`
	Paths        []string `json:"paths"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// WorkerJobSplitPayload is the recorded split. BaseCommit is the
// predecessor's recorded measurement base, core-derived and absent for an
// uncapped predecessor.
type WorkerJobSplitPayload struct {
	WorkflowVersionFields
	Predecessor     WorkerJobBinding               `json:"predecessor"`
	BaseCommit      string                         `json:"base_commit,omitempty"`
	Successors      []WorkerJobBinding             `json:"successors"`
	Allocation      []WorkerJobSplitAllocation     `json:"allocation"`
	ReplacedAttempt *WorkerJobSplitReplacedAttempt `json:"replaced_attempt,omitempty"`
}

// workerJobSplitInput is the authored split member of record_worker_job.
type workerJobSplitInput struct {
	Predecessor     WorkerJobBinding                `json:"predecessor"`
	Successors      []json.RawMessage               `json:"successors"`
	Allocation      []WorkerJobSplitAllocation      `json:"allocation"`
	ReplacedAttempt *workerJobSplitReplacementInput `json:"replaced_attempt,omitempty"`
}

func validWorkerJobBinding(binding WorkerJobBinding) bool {
	return workerJobIDPattern.MatchString(binding.JobID) && binding.Revision >= 1 && workerProvenancePattern.MatchString(binding.Digest)
}

// validateWorkerJobSplitPayload re-proves the split's closed shape from its
// own retained content.
func validateWorkerJobSplitPayload(event Event, payload WorkerJobSplitPayload) error {
	if event.SubjectType != SubjectWorkItem || event.SubjectID == "" {
		return invalidWorkerPayload("worker.job_split must record under a work item")
	}
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if payload.ReplacedAttempt != nil && event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < 2 {
		return invalidWorkerPayload("worker.job_split replaced_attempt requires payload version >= 2")
	}
	if !validWorkerJobBinding(payload.Predecessor) {
		return invalidWorkerPayload("worker.job_split predecessor must name a job_id, revision, and digest")
	}
	if payload.BaseCommit != "" && !worktreeSHAPattern.MatchString(payload.BaseCommit) {
		return invalidWorkerPayload("worker.job_split base_commit must be a raw git commit oid")
	}
	if len(payload.Successors) < WorkerJobSplitSuccessorsMin || len(payload.Successors) > WorkerJobSplitSuccessorsMax {
		return invalidWorkerPayload(fmt.Sprintf("worker.job_split carries %d successors; %d to %d are declared", len(payload.Successors), WorkerJobSplitSuccessorsMin, WorkerJobSplitSuccessorsMax))
	}
	successors := map[string]bool{}
	for _, successor := range payload.Successors {
		if !validWorkerJobBinding(successor) || successor.Revision != 1 || successor.JobID == payload.Predecessor.JobID || successors[successor.JobID] {
			return invalidWorkerPayload("worker.job_split successors must be distinct new jobs at revision 1, none the predecessor")
		}
		successors[successor.JobID] = true
	}
	if len(payload.Allocation) < 1 || len(payload.Allocation) > len(payload.Successors) {
		return invalidWorkerPayload("worker.job_split allocation carries one entry per allocated successor")
	}
	allocated := map[string]bool{}
	for _, entry := range payload.Allocation {
		if !successors[entry.JobID] || allocated[entry.JobID] {
			return invalidWorkerPayload("worker.job_split allocation names each successor at most once and no other job")
		}
		allocated[entry.JobID] = true
		if len(entry.Paths) < 1 || len(entry.Paths) > WorkerJobMeasuredPathsMax || len(entry.EvidenceRefs) < 1 || len(entry.EvidenceRefs) > workerJobSplitEvidenceRefsMax {
			return invalidWorkerPayload("worker.job_split allocation entries carry 1 to 64 paths and 1 to 16 evidence references")
		}
		for index, path := range entry.Paths {
			if problem := normalizedRelativeRepoPathProblem("worker.job_split allocation paths", path); problem != "" {
				return invalidWorkerPayload(problem)
			}
			if slices.Contains(entry.Paths[:index], path) {
				return invalidWorkerPayload("worker.job_split allocation paths must be unique per entry")
			}
		}
		if !workflowList(entry.EvidenceRefs, workerJobSplitEvidenceRefsMax, 1) {
			return invalidWorkerPayload("worker.job_split allocation evidence_refs must be distinct references between 2 and 128 bytes")
		}
	}
	return validateWorkerJobSplitReplacementShape(payload.ReplacedAttempt)
}

// foldWorkerJobSplit verifies the split against the recorded revisions:
// the predecessor is recorded under its digest and was not split before,
// and every successor is the revision 1 the same action just recorded.
func foldWorkerJobSplit(ctx context.Context, tx *sql.Tx, event Event) error {
	var payload WorkerJobSplitPayload
	if err := decodeClosedWorkerPayload(event, &payload); err != nil {
		return err
	}
	for _, binding := range append([]WorkerJobBinding{payload.Predecessor}, payload.Successors...) {
		var digest string
		if err := tx.QueryRowContext(ctx, `SELECT digest FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, event.SubjectID, binding.JobID, binding.Revision).Scan(&digest); err != nil {
			if err == sql.ErrNoRows {
				return newFailure(KindInvalidPayload, "fold_event", "worker.job_split names job "+binding.JobID+" revision "+fmt.Sprint(binding.Revision)+", which is not recorded", false, "record the split through record_worker_job")
			}
			return wrapFailure(KindUnavailable, "fold_event", "cannot verify the split's job revisions", true, "retry once the worker-job projection is readable", err)
		}
		if digest != binding.Digest {
			return newFailure(KindInvalidPayload, "fold_event", "worker.job_split names job "+binding.JobID+" under a digest the recorded revision does not carry", false, "record the split through record_worker_job")
		}
	}
	if err := refuseWorkerJobReplacedBeforeTx(ctx, tx, event, payload.Predecessor.JobID, "be split again"); err != nil {
		return err
	}
	var latest int64
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT revision,state FROM worker_job_revisions WHERE work_id=? AND job_id=? ORDER BY revision DESC LIMIT 1`, event.SubjectID, payload.Predecessor.JobID).Scan(&latest, &state); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the split predecessor's state", true, "retry once the worker-job projection is readable", err)
	}
	if latest != payload.Predecessor.Revision || state == "satisfied" {
		return invalidWorkerPayload("worker.job_split predecessor must be the current unsatisfied revision")
	}
	predecessor, err := readWorkerJobRecordedTx(ctx, tx, event.SubjectID, payload.Predecessor)
	if err != nil {
		return err
	}
	if predecessor == nil {
		return invalidWorkerPayload("worker.job_split predecessor has no recorded content")
	}
	base := ""
	if predecessor.MeasurementBasis != nil {
		base = predecessor.MeasurementBasis.BaseCommit
	}
	if payload.BaseCommit != base {
		return invalidWorkerPayload("worker.job_split base_commit does not match the predecessor's recorded measurement basis")
	}
	if err := verifyWorkerJobSplitReplacementTx(ctx, tx, event, payload, predecessor); err != nil {
		return err
	}
	for index, binding := range payload.Successors {
		var recorded int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE event_id=? AND subject_type=? AND subject_id=? AND kind=? AND seq<? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? AND json_extract(payload,'$.digest')=?`, event.EventID+":successor:"+fmt.Sprint(index), string(SubjectWorkItem), event.SubjectID, WorkerJobRecorded, event.Seq, binding.JobID, binding.Revision, binding.Digest).Scan(&recorded); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot verify the split's successor events", true, "retry once the event log is readable", err)
		}
		if recorded != 1 {
			return invalidWorkerPayload("worker.job_split successor was not recorded by the same split action")
		}
	}
	return advanceWorkflowVersion(ctx, tx, event, payload.WorkflowVersionFields)
}

// refuseWorkerJobReplacedBeforeTx is the fold-side replaced-job refusal. It
// reads only splits earlier in the log than the folding event, so a rebuild
// derives the same result as the live append.
func refuseWorkerJobReplacedBeforeTx(ctx context.Context, tx *sql.Tx, event Event, jobID, use string) error {
	var earlier int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq<? AND json_extract(payload,'$.predecessor.job_id')=?`, string(SubjectWorkItem), event.SubjectID, WorkerJobSplit, event.Seq, jobID).Scan(&earlier); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the job's earlier splits", true, "retry once the event log is readable", err)
	}
	if earlier != 0 {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "worker job "+jobID+" is replaced by a split and cannot "+use, false, "work through the split's successor jobs")
	}
	return nil
}

// readWorkerJobSplitsTx reads every recorded split of the work in log order.
func readWorkerJobSplitsTx(ctx context.Context, q queryer, workID string) ([]WorkerJobSplitPayload, error) {
	events, err := readWorkerJobSplitEventsTx(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	var splits []WorkerJobSplitPayload
	for _, event := range events {
		splits = append(splits, event.payload)
	}
	return splits, nil
}

// workerJobSplitReplacing returns the split that replaced the job, or nil.
func workerJobSplitReplacing(splits []WorkerJobSplitPayload, jobID string) *WorkerJobSplitPayload {
	for index := range splits {
		if splits[index].Predecessor.JobID == jobID {
			return &splits[index]
		}
	}
	return nil
}

// workerJobSplitCreating returns the split that created the job as a
// successor, or nil.
func workerJobSplitCreating(splits []WorkerJobSplitPayload, jobID string) *WorkerJobSplitPayload {
	for index := range splits {
		if slices.ContainsFunc(splits[index].Successors, func(successor WorkerJobBinding) bool { return successor.JobID == jobID }) {
			return &splits[index]
		}
	}
	return nil
}

// readWorkerJobRecordedTx reads one recorded revision's full content from
// its worker.job_recorded event.
func readWorkerJobRecordedTx(ctx context.Context, q queryer, workID string, binding WorkerJobBinding) (*WorkerJobRecordedPayload, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? AND json_extract(payload,'$.digest')=? ORDER BY seq LIMIT 1`, string(SubjectWorkItem), workID, WorkerJobRecorded, binding.JobID, binding.Revision, binding.Digest).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job", "cannot read the recorded worker-job revision", true, "retry once the event log is readable", err)
	}
	var payload WorkerJobRecordedPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, newFailure(KindInvariantViolation, "worker_job", "a recorded worker-job revision is not one closed job payload", false, "rebuild the worker-job events from the authoritative log")
	}
	return &payload, nil
}

func workerJobSplitFailure(detail, recovery string) error {
	return newFailure(KindInvalidOperation, "workflow_action", "record_worker_job split "+detail, false, recovery)
}

// admitWorkerJobSplitPredecessorTx admits the named predecessor: the job's
// latest recorded revision under its digest, not satisfied, not already
// split, and with no other attempt still in flight or awaiting disposition.
func admitWorkerJobSplitPredecessorTx(ctx context.Context, tx *sql.Tx, workID string, binding WorkerJobBinding, splits []WorkerJobSplitPayload, replacement *workerJobSplitReplacementInput) (*WorkerJobRecordedPayload, error) {
	if workerJobSplitReplacing(splits, binding.JobID) != nil {
		return nil, workerJobSplitFailure("names predecessor job "+binding.JobID+", which an earlier split already replaced", "split one of that split's successor jobs instead")
	}
	var latest int64
	var state string
	switch err := tx.QueryRowContext(ctx, `SELECT revision,state FROM worker_job_revisions WHERE work_id=? AND job_id=? ORDER BY revision DESC LIMIT 1`, workID, binding.JobID).Scan(&latest, &state); {
	case err == sql.ErrNoRows:
		return nil, workerJobSplitFailure("names predecessor job "+binding.JobID+", which is not recorded", "name the predecessor's current revision and digest")
	case err != nil:
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the predecessor's revisions", true, "retry once the worker-job projection is readable", err)
	}
	if latest != binding.Revision {
		return nil, workerJobSplitFailure(fmt.Sprintf("names stale predecessor revision %d of job %s; the current revision is %d", binding.Revision, binding.JobID, latest), "name the predecessor's current revision and digest")
	}
	if state == "satisfied" {
		return nil, workerJobSplitFailure("names predecessor job "+binding.JobID+" revision "+fmt.Sprint(binding.Revision)+", which is already satisfied", "split only an unsatisfied job")
	}
	predecessor, err := readWorkerJobRecordedTx(ctx, tx, workID, binding)
	if err != nil {
		return nil, err
	}
	if predecessor == nil {
		return nil, workerJobSplitFailure("names predecessor job "+binding.JobID+" under a digest its current revision does not carry", "name the predecessor's current revision and digest")
	}
	replaced := ""
	if replacement != nil {
		replaced = replacement.AttemptID
	}
	var attemptID, lifecycle string
	switch err := tx.QueryRowContext(ctx, `SELECT a.attempt_id, a.lifecycle_state FROM worker_attempts a JOIN domain_events d ON d.subject_type=? AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.action_id')='dispatch_worker' AND json_extract(d.payload,'$.worker_attempt_id')=a.attempt_id
		WHERE a.work_id=? AND a.attempt_id<>? AND json_extract(d.payload,'$.worker_job.job_id')=? AND (a.lifecycle_state IN ('in_flight','dispatched') OR (a.lifecycle_state='completed' AND NOT EXISTS (
			SELECT 1 FROM domain_events f WHERE f.subject_type=? AND f.subject_id=a.work_id AND f.kind=? AND json_extract(f.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result') AND json_extract(f.payload,'$.worker_attempt_id')=a.attempt_id)))
		ORDER BY d.seq LIMIT 1`, string(SubjectWorkItem), WorkflowActionCompleted, workID, replaced, binding.JobID, string(SubjectWorkItem), WorkflowActionCompleted).Scan(&attemptID, &lifecycle); {
	case err == nil:
		if lifecycle == "completed" {
			return nil, workerJobSplitFailure("cannot replace predecessor job "+binding.JobID+": its completed attempt "+attemptID+" awaits disposition", "accept or reject attempt "+attemptID+" before splitting the job")
		}
		return nil, workerJobSplitFailure("cannot replace predecessor job "+binding.JobID+" while its attempt "+attemptID+" is active", "wait for attempt "+attemptID+" to finish and dispose of it before splitting the job")
	case err != sql.ErrNoRows:
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the predecessor's attempts", true, "retry once the worker-attempt projection is readable", err)
	}
	return predecessor, nil
}

// workerJobSplitCoverageProblem checks the full disposition of the
// predecessor's obligations: every scope entry, predicate, check,
// prerequisite, stopping condition, reserved integration, and oracle owner,
// case, and control appears unchanged in at least one successor, and a
// capped predecessor's successors are capped at or below its cap. It
// returns the first uncovered item, or the empty string.
func workerJobSplitCoverageProblem(predecessor WorkerJobRecordedPayload, successors []WorkerJobRecordedPayload) string {
	carried := func(covered func(WorkerJobRecordedPayload) bool) bool {
		return slices.ContainsFunc(successors, covered)
	}
	if predecessor.ProductionSizeMax != nil {
		for _, successor := range successors {
			if successor.ProductionSizeMax == nil || *successor.ProductionSizeMax > *predecessor.ProductionSizeMax {
				return fmt.Sprintf("successor job %s is not capped at or below the predecessor's cap %d", successor.JobID, *predecessor.ProductionSizeMax)
			}
		}
	}
	for _, list := range []struct {
		member string
		values func(WorkerJobRecordedPayload) []string
	}{
		{"path_scope entry", func(job WorkerJobRecordedPayload) []string { return job.PathScope }},
		{"predicate", func(job WorkerJobRecordedPayload) []string { return job.PredicateIDs }},
		{"check", func(job WorkerJobRecordedPayload) []string { return job.Checks }},
	} {
		for _, value := range list.values(predecessor) {
			if !carried(func(successor WorkerJobRecordedPayload) bool { return slices.Contains(list.values(successor), value) }) {
				return "no successor carries predecessor " + list.member + " " + value
			}
		}
	}
	for _, prerequisite := range predecessor.Prerequisites {
		if !carried(func(successor WorkerJobRecordedPayload) bool {
			return slices.Contains(successor.Prerequisites, prerequisite)
		}) {
			return fmt.Sprintf("no successor carries predecessor prerequisite %s revision %d", prerequisite.JobID, prerequisite.Revision)
		}
	}
	if !carried(func(successor WorkerJobRecordedPayload) bool {
		return successor.StoppingCondition == predecessor.StoppingCondition
	}) {
		return "no successor carries the predecessor's stopping_condition"
	}
	if predecessor.ReservedIntegration != "" && !carried(func(successor WorkerJobRecordedPayload) bool {
		return successor.ReservedIntegration == predecessor.ReservedIntegration
	}) {
		return "no successor carries the predecessor's reserved_integration"
	}
	for _, item := range workerJobOracleItems(predecessor.AcceptanceOracle) {
		if !carried(func(successor WorkerJobRecordedPayload) bool {
			return slices.Contains(workerJobOracleItems(successor.AcceptanceOracle), item)
		}) {
			return "no successor carries predecessor oracle " + item.id + " unchanged"
		}
	}
	return ""
}

// workerJobOracleItem is one oracle owner, case, or control: its identifier
// and exact serialized obligation content. Subject-bound preparation receipts
// are validated separately and must be refreshed for each candidate.
type workerJobOracleItem struct {
	id, content string
}

func workerJobOracleItems(oracle *AcceptanceOracle) []workerJobOracleItem {
	if oracle == nil {
		return nil
	}
	items := make([]workerJobOracleItem, 0)
	add := func(id string, value any) {
		raw, _ := json.Marshal(value)
		items = append(items, workerJobOracleItem{id: id, content: string(raw)})
	}
	for _, owner := range oracle.Owners {
		add(owner.OwnerID, owner)
	}
	for _, oracleCase := range oracle.Cases {
		add(oracleCase.CaseID, oracleCase)
	}
	for _, control := range oracle.Controls {
		control.ReadinessEvidenceRefs = nil
		add(control.ControlID, control)
	}
	return items
}

// workerJobSplitLineageProblem refuses cyclic lineage: no successor may
// require the predecessor or any job the predecessor descends from, because
// those resolve only once this split is satisfied, and the successors'
// prerequisites on one another must be acyclic.
func workerJobSplitLineageProblem(splits []WorkerJobSplitPayload, predecessorJobID string, successors []WorkerJobRecordedPayload) string {
	lineage := map[string]bool{}
	for job := predecessorJobID; job != "" && !lineage[job]; {
		lineage[job] = true
		if parent := workerJobSplitCreating(splits, job); parent != nil {
			job = parent.Predecessor.JobID
		} else {
			break
		}
	}
	siblings := map[string][]string{}
	for _, successor := range successors {
		siblings[successor.JobID] = nil
	}
	for _, successor := range successors {
		for _, prerequisite := range successor.Prerequisites {
			if lineage[prerequisite.JobID] {
				return "successor job " + successor.JobID + " requires predecessor-lineage job " + prerequisite.JobID
			}
			if _, sibling := siblings[prerequisite.JobID]; sibling {
				siblings[successor.JobID] = append(siblings[successor.JobID], prerequisite.JobID)
			}
		}
	}
	const unvisited, visiting, done = 0, 1, 2
	state := map[string]int{}
	var visit func(string) string
	visit = func(job string) string {
		switch state[job] {
		case visiting:
			return "successor prerequisites cycle through job " + job
		case done:
			return ""
		}
		state[job] = visiting
		for _, next := range siblings[job] {
			if problem := visit(next); problem != "" {
				return problem
			}
		}
		state[job] = done
		return ""
	}
	for _, successor := range successors {
		if problem := visit(successor.JobID); problem != "" {
			return problem
		}
	}
	return ""
}

// workerJobSplitAllocationProblem checks the allocation against the
// successors: each entry names a successor, each path lies inside that
// successor's path_scope, and every predecessor inventory path is
// allocated. The declared schema and the fold own the entry shape.
func workerJobSplitAllocationProblem(predecessor WorkerJobRecordedPayload, successors []WorkerJobRecordedPayload, allocation []WorkerJobSplitAllocation) string {
	allocated := map[string]bool{}
	for _, entry := range allocation {
		index := slices.IndexFunc(successors, func(successor WorkerJobRecordedPayload) bool { return successor.JobID == entry.JobID })
		if index < 0 {
			return "names job " + entry.JobID + ", which is not a successor"
		}
		for _, path := range entry.Paths {
			if !workerJobPathContained(successors[index].PathScope, path) {
				return "assigns path " + path + " to successor job " + entry.JobID + ", whose path_scope does not contain it"
			}
			allocated[path] = true
		}
	}
	if predecessor.MeasurementBasis != nil {
		for _, measured := range predecessor.MeasurementBasis.Paths {
			if !allocated[measured.Path] {
				return "leaves predecessor inventory path " + measured.Path + " unallocated"
			}
		}
	}
	return ""
}

// workerJobPathContained reports whether a path is a path_scope entry or
// under one.
func workerJobPathContained(pathScope []string, path string) bool {
	return slices.ContainsFunc(pathScope, func(scope string) bool { return path == scope || strings.HasPrefix(path, scope+"/") })
}
