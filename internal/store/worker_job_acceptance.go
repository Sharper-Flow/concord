package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
)

// workerJobProductionSize is the one CD-0225 D1 measurement: added plus
// deleted lines over the observed paths whose class is production, between
// the job's recorded base and the delivered tree. A path the inventory does
// not classify takes its supplemental class, else production. Paths in
// foreign belong to other jobs' accepted work and never enter the count. It
// returns the count, or the reason no count can be established: an absent,
// dirty, or differently based observation, or a binary production path, is
// unavailable evidence, never zero lines.
func workerJobProductionSize(basis *WorkerJobMeasurementBasis, supplemental []WorkerJobMeasuredPath, observation *WorkerObservedDelivery, foreign map[string]bool) (int64, string) {
	return workerJobLineCount(basis, supplemental, observation, foreign, false)
}

// workerJobReportedLineCount is the count a worker report claims: the text
// lines of the whole base..tree delta's production paths under the inventory
// and production default. A binary path has no text lines on either side, so
// it adds nothing here; the final measurement decides whether it makes the
// size unavailable.
func workerJobReportedLineCount(basis *WorkerJobMeasurementBasis, observation *WorkerObservedDelivery) (int64, string) {
	return workerJobLineCount(basis, nil, observation, nil, true)
}

func workerJobLineCount(basis *WorkerJobMeasurementBasis, supplemental []WorkerJobMeasuredPath, observation *WorkerObservedDelivery, foreign map[string]bool, textOnly bool) (int64, string) {
	switch {
	case basis == nil:
		return 0, "the job records no measurement basis"
	case observation == nil:
		return 0, "the completion carries no sealed delivery observation"
	case observation.State != "clean":
		return 0, "the delivered tree was " + observation.State + ", so no delta was observed"
	case observation.Base != basis.BaseCommit:
		return 0, "the sealed observation measured from base " + observation.Base + ", not the job's recorded base_commit " + basis.BaseCommit
	}
	classes := map[string]string{}
	for _, measured := range basis.Paths {
		classes[measured.Path] = measured.Class
	}
	for _, measured := range supplemental {
		if _, inventoried := classes[measured.Path]; !inventoried {
			classes[measured.Path] = measured.Class
		}
	}
	var count int64
	for _, path := range observation.Paths {
		if foreign[path.Path] {
			continue
		}
		if class, classified := classes[path.Path]; classified && class != "production" {
			continue
		}
		if path.Binary {
			if textOnly {
				continue
			}
			return 0, "production path " + path.Path + " is binary and has no line count"
		}
		if path.Added > math.MaxInt64-count || path.Deleted > math.MaxInt64-count-path.Added {
			return 0, "the production line count exceeds the measurement range"
		}
		count += path.Added + path.Deleted
	}
	return count, ""
}

// workerCompletedSizeRecord is the size-bearing content of one attempt's
// worker.completed event.
type workerCompletedSizeRecord struct {
	reportRef string
	observed  *WorkerObservedDelivery
	size      *WorkerProductionSize
	repairs   []WorkerDirectCallerRepair
}

type workerJobSizeAcceptance struct {
	supplemental []WorkerJobMeasuredPath
	size         WorkerProductionSize
}

// admitWorkerJobSizeAcceptanceTx is the size-capable acceptance gate
// (CD-0225 D1-D2, CD-0227 D2-D3). It applies to the acceptance of a capped
// job's implementation attempt. The report and sealed observation must agree
// under the recorded inventory and production default. The accepting
// coordinator's supplemental classifications then yield the final recorded
// count checked against the cap. Every changed path outside path_scope belongs
// to one evidenced direct-caller repair inside the allowance, and a count
// above the cap refuses with the typed overrun and the split route. The
// attempt itself stays the fold's to refuse when it is missing or not
// completed.
func admitWorkerJobSizeAcceptanceTx(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, job *WorkerJobBinding, attemptID string, fields map[string]json.RawMessage) (*workerJobSizeAcceptance, error) {
	if !workflowJobSizeActive(definition) {
		return nil, nil
	}
	supplementalPresent := workerJobSizeFieldPresent(fields, "supplemental_classifications")
	refuseSupplemental := func() (*workerJobSizeAcceptance, error) {
		if !supplementalPresent {
			return nil, nil
		}
		return nil, workerJobSizeAuthorFailure("accept_worker_result supplemental_classifications applies only to the acceptance of a capped job's implementation attempt",
			"accept without supplemental_classifications")
	}
	if job == nil {
		return refuseSupplemental()
	}
	content, err := readWorkerJobSizeContentTx(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	entry, recorded := content[*job]
	capability, err := workerAttemptCapabilityTx(ctx, tx, attemptID)
	if err != nil {
		return nil, err
	}
	if !recorded || entry.basis == nil || capability != "implementation" {
		return refuseSupplemental()
	}
	completed, found, err := readWorkerCompletedSizeTx(ctx, tx, workID, attemptID, 0)
	if err != nil || !found {
		return nil, err
	}
	foreign, err := workerJobForeignAcceptedPathsTx(ctx, tx, workID, job.JobID, attemptID, entry.basis)
	if err != nil {
		return nil, err
	}
	unavailableSize := func(cause string) error {
		return newFailure(KindMissingEvidence, "workflow_action", "accept_worker_result cannot establish worker job "+job.JobID+"'s production size: "+cause,
			false, "reject the result with reject_worker_result and dispatch a correction that delivers a clean measurable tree")
	}
	reportedCount, unavailable := workerJobReportedLineCount(entry.basis, completed.observed)
	if unavailable != "" {
		return nil, unavailableSize(unavailable)
	}
	if completed.size == nil || completed.size.Count != reportedCount {
		return nil, newFailure(KindMissingEvidence, "workflow_action",
			fmt.Sprintf("accept_worker_result: worker job %s's reported production_size does not agree with the observed count %d under the recorded inventory and production default between base %s and tree %s", job.JobID, reportedCount, entry.basis.BaseCommit, completed.observed.Commit),
			false, "reject the result with reject_worker_result; a report and observation that disagree establish no size")
	}
	supplemental, err := admitWorkerSupplementalClassifications("accept_worker_result", fields, entry.basis, completed, foreign)
	if err != nil {
		return nil, err
	}
	// Unavailable evidence is judged on the final measured set: the job's own
	// production paths after classification and the foreign subtraction.
	count, unavailable := workerJobProductionSize(entry.basis, supplemental, completed.observed, foreign)
	if unavailable != "" {
		return nil, unavailableSize(unavailable)
	}
	if err := admitWorkerCallerRepairsTx(ctx, tx, workID, *job, entry, attemptID, completed, foreign); err != nil {
		return nil, err
	}
	if count > entry.sizeCap {
		failure := newRouteFailure(KindInvalidOperation, "workflow_action",
			fmt.Sprintf("accept_worker_result: worker job %s revision %d's observed production size %d exceeds its cap %d at subject %s; the completed attempt stays awaiting disposition", job.JobID, job.Revision, count, entry.sizeCap, completed.observed.Commit),
			false, "record_worker_job")
		failure.SizeOverrun = &SizeOverrun{JobID: job.JobID, Revision: job.Revision, Cap: entry.sizeCap, Observed: count, Subject: completed.observed.Commit}
		return nil, failure
	}
	return &workerJobSizeAcceptance{supplemental: supplemental, size: WorkerProductionSize{Base: entry.basis.BaseCommit, Tree: completed.observed.Commit, Count: count}}, nil
}

// admitWorkerSupplementalClassifications decodes and admits a coordinator's
// supplemental classifications: each names an observed path of this job's
// delta that the inventory does not classify. The disposition's own boundary
// enforces executor distinctness. A split uses the classes only for overrun
// evidence; acceptance records them with its accepted measurement.
func admitWorkerSupplementalClassifications(action string, fields map[string]json.RawMessage, basis *WorkerJobMeasurementBasis, completed workerCompletedSizeRecord, foreign map[string]bool) ([]WorkerJobMeasuredPath, error) {
	if !workerJobSizeFieldPresent(fields, "supplemental_classifications") {
		return nil, nil
	}
	var supplemental []WorkerJobMeasuredPath
	if err := json.Unmarshal(fields["supplemental_classifications"], &supplemental); err != nil {
		return nil, workerJobSizeAuthorFailure(action+" supplemental_classifications is not a list of classified paths", "supply each path with its class")
	}
	if len(supplemental) < 1 || len(supplemental) > WorkerJobMeasuredPathsMax {
		return nil, workerJobSizeAuthorFailure(fmt.Sprintf("%s supplemental_classifications carries %d paths; 1 to %d are declared", action, len(supplemental), WorkerJobMeasuredPathsMax), "classify at most the declared number of paths")
	}
	if err := validateWorkerJobMeasuredPaths(action+" supplemental_classifications", supplemental, workerJobSizeAuthorFailure); err != nil {
		return nil, err
	}
	observed := map[string]bool{}
	for _, path := range completed.observed.Paths {
		observed[path.Path] = !foreign[path.Path]
	}
	for _, measured := range supplemental {
		if slices.ContainsFunc(basis.Paths, func(inventoried WorkerJobMeasuredPath) bool { return inventoried.Path == measured.Path }) {
			return nil, workerJobSizeAuthorFailure(action+" supplemental_classifications path "+measured.Path+" is already classified by the job's inventory, which an acceptance cannot change",
				"classify only observed paths the inventory does not name")
		}
		if !observed[measured.Path] {
			return nil, workerJobSizeAuthorFailure(action+" supplemental_classifications path "+measured.Path+" is not a path of this job's observed delta",
				"classify only paths the sealed observation records")
		}
	}
	return supplemental, nil
}

// admitWorkerCallerRepairsTx holds CD-0227 D2's containment: every observed
// path of this job's delta outside path_scope belongs to exactly one
// evidenced repair whose owner is an allowance mechanism, every repair path
// is a path of that delta, and no repair path falls under the dispatch's
// caller exclusions, another work item's recorded job scope in the
// allowance's Project, or another work item's held path claim. Architecture
// overlap needs no reader here: the allowance's Domains are the contract's
// approved affected Domains, and every workflow action already passes the
// Domain-overlap boundary.
func admitWorkerCallerRepairsTx(ctx context.Context, tx *sql.Tx, workID string, job WorkerJobBinding, entry workerJobSizeRevision, attemptID string, completed workerCompletedSizeRecord, foreign map[string]bool) error {
	refuse := func(detail string) error {
		return newFailure(KindInvalidOperation, "workflow_action", "accept_worker_result: worker job "+job.JobID+" "+detail, false,
			"reconcile the caller path through the coordinator, then reject the result with reject_worker_result or record a revision with the needed scope through record_worker_job")
	}
	changed := map[string]bool{}
	for _, path := range completed.observed.Paths {
		if !foreign[path.Path] {
			changed[path.Path] = true
		}
	}
	pathScope, err := workerJobPathScopeTx(ctx, tx, workID, job)
	if err != nil {
		return err
	}
	exclusions, err := workerDispatchCallerExclusionsTx(ctx, tx, workID, attemptID)
	if err != nil {
		return err
	}
	repaired := map[string]bool{}
	for _, repair := range completed.repairs {
		if !slices.ContainsFunc(entry.allowance.Mechanisms, func(mechanism OracleMechanism) bool {
			return mechanism.Path == repair.Owner.Path && mechanism.EntryPoint == repair.Owner.EntryPoint
		}) {
			return refuse("reports a caller repair of owner " + repair.Owner.Path + " " + repair.Owner.EntryPoint + ", which is not a mechanism of its direct-caller allowance")
		}
		for _, path := range repair.ChangedPaths {
			switch {
			case !changed[path]:
				return refuse("reports caller-repair path " + path + ", which is not a path of its observed delta")
			case repaired[path]:
				return refuse("reports caller-repair path " + path + " in two repairs")
			case workerJobPathsIntersect(exclusions, path):
				return refuse("repaired caller path " + path + ", which its dispatch excluded as another correction's measured path")
			}
			repaired[path] = true
		}
	}
	for _, path := range slices.Sorted(maps.Keys(changed)) {
		if workerJobPathsIntersect(pathScope, path) {
			continue
		}
		if !repaired[path] {
			return refuse("changed path " + path + " outside its path_scope without an evidenced direct-caller repair")
		}
		owner, err := workerCallerPathForeignOwnerTx(ctx, tx, workID, entry.allowance.ProjectID, path)
		if err != nil {
			return err
		}
		if owner != "" {
			return refuse("repaired caller path " + path + ", which " + owner)
		}
	}
	return nil
}

// workerCallerPathForeignOwnerTx names another work item that owns a caller
// path: a non-terminal item whose recorded worker job in the same Project
// scopes the path, or an item holding a path claim on it. It returns the
// empty string when no other item owns the path.
func workerCallerPathForeignOwnerTx(ctx context.Context, tx *sql.Tx, workID, projectID, path string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.work_id, r.path_scope FROM worker_job_revisions r JOIN work_items w ON w.id=r.work_id WHERE r.work_id<>? AND r.project_scope=? AND w.lifecycle NOT IN ('completed','cancelled','superseded') ORDER BY r.work_id, r.job_id, r.revision`, workID, projectID)
	if err != nil {
		return "", wrapFailure(KindUnavailable, "worker_job_size", "cannot read other work items' job scopes", true, "retry once the worker-job projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var otherWork, raw string
		if err := rows.Scan(&otherWork, &raw); err != nil {
			return "", wrapFailure(KindUnavailable, "worker_job_size", "cannot scan other work items' job scopes", true, "retry once the worker-job projection is readable", err)
		}
		var scope []string
		if err := json.Unmarshal([]byte(raw), &scope); err != nil {
			return "", newFailure(KindInvariantViolation, "worker_job_size", "a recorded worker-job revision carries a path scope that is not one string list", false, "rebuild the worker-job projection from the event log")
		}
		if workerJobPathsIntersect(scope, path) {
			return "work item " + otherWork + "'s recorded job scope covers", nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", wrapFailure(KindUnavailable, "worker_job_size", "cannot scan other work items' job scopes", true, "retry once the worker-job projection is readable", err)
	}
	claims, err := tx.QueryContext(ctx, `SELECT c.holder_work_id, c.resource_key FROM resource_claims c WHERE c.state='held' AND c.holder_work_id<>? AND substr(c.resource_key,1,5)='path:' AND EXISTS (SELECT 1 FROM work_projects wp WHERE wp.work_id=c.holder_work_id AND wp.project_id=?) ORDER BY c.resource_key`, workID, projectID)
	if err != nil {
		return "", wrapFailure(KindUnavailable, "worker_job_size", "cannot read held path claims", true, "retry once the resource claims are readable", err)
	}
	defer claims.Close()
	for claims.Next() {
		var holder, key string
		if err := claims.Scan(&holder, &key); err != nil {
			return "", wrapFailure(KindUnavailable, "worker_job_size", "cannot scan held path claims", true, "retry once the resource claims are readable", err)
		}
		if workerJobPathsIntersect([]string{key[len("path:"):]}, path) {
			return "work item " + holder + " holds through claim " + key + " over", nil
		}
	}
	if err := claims.Err(); err != nil {
		return "", wrapFailure(KindUnavailable, "worker_job_size", "cannot scan held path claims", true, "retry once the resource claims are readable", err)
	}
	return "", nil
}

// workerJobForeignAcceptedPathsTx returns the observed paths of other jobs'
// attempts accepted after this job's first implementation dispatch: accepted
// work its delivered subject may carry, which never enters this job's count.
// A foreign path that is also one of this job's measured paths — its
// inventory, or the observed delta of its own earlier attempt sealed before
// that acceptance — refuses as a serialization breach rather than a
// subtraction (CD-0225 D1).
func workerJobForeignAcceptedPathsTx(ctx context.Context, tx *sql.Tx, workID, jobID, attemptID string, basis *WorkerJobMeasurementBasis) (map[string]bool, error) {
	foreign := map[string]bool{}
	_, firstSeq, found, err := workerJobFirstImplementationDispatchTx(ctx, tx, workID, jobID)
	if err != nil || !found {
		return foreign, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT seq, json_extract(payload,'$.worker_attempt_id'), json_extract(payload,'$.worker_job.job_id') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' AND json_extract(payload,'$.worker_job.job_id') IS NOT NULL AND json_extract(payload,'$.worker_job.job_id')<>? AND seq>? ORDER BY seq`, string(SubjectWorkItem), workID, WorkflowActionCompleted, jobID, firstSeq)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot read other jobs' acceptances", true, "retry once the event log is readable", err)
	}
	type acceptance struct {
		seq            int64
		attempt, owner string
	}
	var accepted []acceptance
	for rows.Next() {
		var record acceptance
		if err := rows.Scan(&record.seq, &record.attempt, &record.owner); err != nil {
			_ = rows.Close()
			return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot scan other jobs' acceptances", true, "retry once the event log is readable", err)
		}
		accepted = append(accepted, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot enumerate other jobs' acceptances", true, "retry once the event log is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot close the acceptance read", true, "retry once the event log is readable", err)
	}
	if len(accepted) == 0 {
		return foreign, nil
	}
	observed, err := workerAttemptObservedPathsTx(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	_, own, err := workerJobCorrectionStatesTx(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	for _, record := range accepted {
		for _, path := range observed[record.attempt].paths {
			measured := slices.ContainsFunc(basis.Paths, func(inventoried WorkerJobMeasuredPath) bool { return inventoried.Path == path })
			for _, earlier := range own[jobID] {
				if earlier != attemptID && observed[earlier].seq < record.seq && slices.Contains(observed[earlier].paths, path) {
					measured = true
				}
			}
			if measured {
				return nil, newFailure(KindInvalidOperation, "workflow_action",
					"accept_worker_result: job "+record.owner+"'s accepted change to "+path+" entered worker job "+jobID+"'s measured paths, which a correction serializes",
					false, "reject the completed attempt with reject_worker_result, then split worker job "+jobID+" through record_worker_job without replaced_attempt so each successor measures from a base that separates the accepted change")
			}
			foreign[path] = true
		}
	}
	return foreign, nil
}

// readWorkerCompletedSizeTx reads the size-bearing content of the attempt's
// worker.completed event. found is false when no completion is recorded.
func readWorkerCompletedSizeTx(ctx context.Context, tx *sql.Tx, workID, attemptID string, beforeSeq int64) (workerCompletedSizeRecord, bool, error) {
	var record workerCompletedSizeRecord
	var raw string
	switch err := tx.QueryRowContext(ctx, `SELECT event_id,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=? AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerCompleted, attemptID, beforeSeq, beforeSeq).Scan(&record.reportRef, &raw); {
	case err == sql.ErrNoRows:
		return record, false, nil
	case err != nil:
		return record, false, wrapFailure(KindUnavailable, "worker_job_size", "cannot read the accepted attempt's completion", true, "retry once the event log is readable", err)
	}
	var payload struct {
		ObservedDelivery    *WorkerObservedDelivery    `json:"observed_delivery"`
		ProductionSize      *WorkerProductionSize      `json:"production_size"`
		DirectCallerRepairs []WorkerDirectCallerRepair `json:"direct_caller_repairs"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return record, false, newFailure(KindInvariantViolation, "worker_job_size", "the accepted attempt's completion payload is not readable", false, "rebuild the worker events from the authoritative log")
	}
	record.observed, record.size, record.repairs = payload.ObservedDelivery, payload.ProductionSize, payload.DirectCallerRepairs
	return record, true, nil
}

// workerDispatchCallerExclusionsTx reads the caller exclusions the
// attempt's dispatch authorization bound to its packet.
func workerDispatchCallerExclusionsTx(ctx context.Context, tx *sql.Tx, workID, attemptID string) ([]string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(payload,'$.worker_caller_exclusions'),'[]') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attemptID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "worker_job_size", "cannot read the dispatch's caller exclusions", true, "retry once the event log is readable", err)
	}
	var exclusions []string
	if err := json.Unmarshal([]byte(raw), &exclusions); err != nil {
		return nil, newFailure(KindInvariantViolation, "worker_job_size", "a dispatch authorization carries caller exclusions that are not one string list", false, "rebuild the workflow events from the authoritative log")
	}
	return exclusions, nil
}

// workerAttemptCapabilityTx returns the attempt's lane capability class, or
// the empty string when no attempt row exists.
func workerAttemptCapabilityTx(ctx context.Context, tx *sql.Tx, attemptID string) (string, error) {
	var capability string
	err := tx.QueryRowContext(ctx, `SELECT capability_class FROM worker_attempts WHERE attempt_id=?`, attemptID).Scan(&capability)
	if err != nil && err != sql.ErrNoRows {
		return "", wrapFailure(KindUnavailable, "worker_job_size", "cannot read the accepted attempt's capability class", true, "retry once the worker-attempt projection is readable", err)
	}
	return capability, nil
}
