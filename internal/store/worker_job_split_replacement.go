package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

type workerJobSplitReplacementInput struct {
	AttemptID                   string                  `json:"attempt_id"`
	AttemptEpoch                int64                   `json:"attempt_epoch"`
	SupplementalClassifications []WorkerJobMeasuredPath `json:"supplemental_classifications,omitempty"`
}

// WorkerJobSplitReplacedAttempt retains the completed report and the
// core-measured overrun. Its classifications serve this evidence only;
// neither the predecessor inventory nor descendant authority changes.
type WorkerJobSplitReplacedAttempt struct {
	workerJobSplitReplacementInput
	ObservedCommit       string   `json:"observed_commit"`
	ObservedCount        int64    `json:"observed_count"`
	Cap                  int64    `json:"cap"`
	ReportRef            string   `json:"report_ref"`
	ForeignAcceptedPaths []string `json:"foreign_accepted_paths,omitempty"`
}

func admitWorkerJobSplitReplacementTx(ctx context.Context, tx *sql.Tx, workID, actor string, predecessor WorkerJobBinding, input *workerJobSplitReplacementInput) (*WorkerJobSplitReplacedAttempt, error) {
	if input == nil {
		return nil, nil
	}
	job, err := workflowDispatchedJobForAttempt(ctx, tx, workID, input.AttemptID)
	if err != nil {
		return nil, err
	}
	if job == nil || *job != predecessor {
		return nil, workerJobSplitFailure("replaced_attempt does not bind the exact predecessor revision", "name the completed attempt dispatched under the predecessor binding")
	}
	var epoch int64
	var executor, capability, lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT json_extract(auth.payload,'$.attempt_epoch'),a.lifecycle_state,a.capability_class,COALESCE(json_extract(d.payload,'$.lane_actor_ref'),'') FROM worker_attempts a JOIN domain_events d ON d.subject_type=? AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id JOIN domain_events auth ON auth.subject_type=d.subject_type AND auth.subject_id=d.subject_id AND auth.kind=? AND json_extract(auth.payload,'$.action_id')='dispatch_worker' AND json_extract(auth.payload,'$.worker_attempt_id')=a.attempt_id WHERE a.work_id=? AND a.attempt_id=?`, string(SubjectWorkItem), WorkerDispatched, WorkflowActionCompleted, workID, input.AttemptID).Scan(&epoch, &lifecycle, &capability, &executor); err != nil {
		if err == sql.ErrNoRows {
			return nil, workerJobSplitFailure("replaced_attempt has no recorded dispatch", "name the predecessor's completed implementation attempt")
		}
		return nil, workflowProjectionError(err, "cannot read the replaced attempt's dispatch")
	}
	if epoch != input.AttemptEpoch || lifecycle != "completed" || capability != "implementation" {
		return nil, workerJobSplitFailure("replaced_attempt must name a completed implementation attempt at its dispatched epoch", "name the exact completed overrun attempt and epoch")
	}
	if err := workflowActorsDistinctForExecutor(ctx, tx, workID, executor, actor, "", false, "workflow_action"); err != nil {
		return nil, err
	}
	var disposed int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id') IN (`+workerAttemptDisposalActions+`) AND json_extract(payload,'$.worker_attempt_id')=?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, input.AttemptID).Scan(&disposed); err != nil {
		return nil, workflowProjectionError(err, "cannot read the replaced attempt's dispositions")
	}
	if disposed != 0 {
		return nil, workerJobSplitFailure("replaced_attempt already has a disposition", "split without replacing an already disposed attempt")
	}
	content, err := readWorkerJobRecordedTx(ctx, tx, workID, predecessor)
	if err != nil {
		return nil, err
	}
	if content == nil || content.ProductionSizeMax == nil || content.MeasurementBasis == nil {
		return nil, workerJobSplitFailure("replaced_attempt requires a capped predecessor", "replace only a completed overrun of a capped job")
	}
	completed, found, err := readWorkerCompletedSizeTx(ctx, tx, workID, input.AttemptID, 0)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, workerJobSplitFailure("replaced_attempt has no completed report", "record its completed report before replacing it")
	}
	foreign, err := workerJobForeignAcceptedPathsTx(ctx, tx, workID, predecessor.JobID, input.AttemptID, content.MeasurementBasis)
	if err != nil {
		return nil, err
	}
	fields := map[string]json.RawMessage{}
	if input.SupplementalClassifications != nil {
		fields["supplemental_classifications"], _ = json.Marshal(input.SupplementalClassifications)
	}
	reported, unavailable := workerJobReportedLineCount(content.MeasurementBasis, completed.observed)
	if unavailable != "" || completed.size == nil || completed.size.Count != reported {
		return nil, workerJobSplitFailure(strings.TrimSpace("replaced_attempt report and sealed observation do not establish an agreed size "+unavailable), "reject a report without agreed size evidence instead of replacing it as an overrun")
	}
	supplemental, err := admitWorkerSupplementalClassifications("record_worker_job split.replaced_attempt", fields, content.MeasurementBasis, completed, foreign)
	if err != nil {
		return nil, err
	}
	count, unavailable := workerJobProductionSize(content.MeasurementBasis, supplemental, completed.observed, foreign)
	if unavailable != "" {
		return nil, newFailure(KindMissingEvidence, "workflow_action", "record_worker_job split cannot establish the replaced attempt's overrun: "+unavailable, false, "reject the result and dispatch a correction that delivers measurable production paths")
	}
	if count <= *content.ProductionSizeMax {
		return nil, workerJobSplitFailure(fmt.Sprintf("replaced_attempt observed count %d does not exceed its cap %d", count, *content.ProductionSizeMax), "accept an in-cap result instead of replacing it as an overrun")
	}
	for path := range foreign {
		if !slices.ContainsFunc(completed.observed.Paths, func(observed WorkerObservedPath) bool { return observed.Path == path }) {
			delete(foreign, path)
		}
	}
	return &WorkerJobSplitReplacedAttempt{
		workerJobSplitReplacementInput: workerJobSplitReplacementInput{AttemptID: input.AttemptID, AttemptEpoch: epoch, SupplementalClassifications: supplemental},
		ObservedCommit:                 completed.observed.Commit, ObservedCount: count, Cap: *content.ProductionSizeMax, ReportRef: completed.reportRef,
		ForeignAcceptedPaths: slices.Sorted(maps.Keys(foreign)),
	}, nil
}

func refuseSplitReplacedWorkerAttemptTx(ctx context.Context, q queryer, workID, attemptID string, beforeSeq int64) error {
	var replaced int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.replaced_attempt.attempt_id')=? AND (?=0 OR seq<?)`, string(SubjectWorkItem), workID, WorkerJobSplit, attemptID, beforeSeq, beforeSeq).Scan(&replaced); err != nil {
		return workflowProjectionError(err, "cannot read the attempt's split replacement")
	}
	if replaced != 0 {
		return newFailure(KindIllegalLifecycleTransition, "workflow_action", "worker attempt "+attemptID+" was replaced by a split and cannot receive another disposition", false, "dispatch and dispose of a split successor instead")
	}
	return nil
}

// validateSplitReplacementCompletionTx binds the disposition to the split
// the same operation authored; ordinary job recording cannot dispose an attempt.
func validateSplitReplacementCompletionTx(ctx context.Context, tx *sql.Tx, event Event, payload workflowActionCompletedPayload) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE event_id=? AND subject_type=? AND subject_id=? AND kind=? AND actor=? AND seq<?`, strings.TrimSuffix(event.EventID, ":completed")+":semantic", string(SubjectWorkItem), event.SubjectID, WorkerJobSplit, event.Actor, event.Seq).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return invalidWorkerPayload("record_worker_job worker_attempt_id requires a replacement split from the same operation")
		}
		return workflowProjectionError(err, "cannot read the replacement split")
	}
	var split WorkerJobSplitPayload
	if err := json.Unmarshal([]byte(raw), &split); err != nil {
		return err
	}
	if split.ReplacedAttempt == nil || split.ReplacedAttempt.AttemptID != payload.WorkerAttemptID || split.ReplacedAttempt.AttemptEpoch != payload.AttemptEpoch {
		return invalidWorkerPayload("record_worker_job disposition does not match its split's replaced attempt")
	}
	var disposed int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND seq<? AND json_extract(payload,'$.action_id') IN (`+workerAttemptDisposalActions+`) AND json_extract(payload,'$.worker_attempt_id')=?`, string(SubjectWorkItem), event.SubjectID, WorkflowActionCompleted, event.Seq, payload.WorkerAttemptID).Scan(&disposed); err != nil {
		return workflowProjectionError(err, "cannot read prior dispositions of the replaced attempt")
	}
	if disposed != 0 {
		return invalidWorkerPayload("record_worker_job cannot replace an attempt that already has a disposition")
	}
	return nil
}

func validateWorkerJobSplitReplacementShape(replacement *WorkerJobSplitReplacedAttempt) error {
	if replacement == nil {
		return nil
	}
	if !workflowString(replacement.AttemptID, 128) || replacement.AttemptEpoch < 1 || replacement.AttemptEpoch > 2147483647 || !worktreeSHAPattern.MatchString(replacement.ObservedCommit) || !workflowString(replacement.ReportRef, 512) || replacement.Cap < 1 || replacement.Cap > WorkerJobSizeCapMax || replacement.ObservedCount <= replacement.Cap {
		return invalidWorkerPayload("worker.job_split replaced_attempt requires an exact completed report and an observed count above its recorded cap")
	}
	if replacement.SupplementalClassifications != nil {
		if len(replacement.SupplementalClassifications) < 1 || len(replacement.SupplementalClassifications) > WorkerJobMeasuredPathsMax {
			return invalidWorkerPayload("worker.job_split replaced_attempt supplemental_classifications carries 1 to 64 paths")
		}
		if err := validateWorkerJobMeasuredPaths("worker.job_split replaced_attempt supplemental_classifications", replacement.SupplementalClassifications, workerJobSizeFoldFailure); err != nil {
			return err
		}
	}
	for index, path := range replacement.ForeignAcceptedPaths {
		if problem := normalizedRelativeRepoPathProblem("worker.job_split replaced_attempt foreign_accepted_paths", path); problem != "" {
			return invalidWorkerPayload(problem)
		}
		if slices.Contains(replacement.ForeignAcceptedPaths[:index], path) {
			return invalidWorkerPayload("worker.job_split replaced_attempt foreign_accepted_paths must be distinct")
		}
	}
	return nil
}

func verifyWorkerJobSplitReplacementTx(ctx context.Context, tx *sql.Tx, event Event, payload WorkerJobSplitPayload, predecessor *WorkerJobRecordedPayload) error {
	replacement := payload.ReplacedAttempt
	if replacement == nil {
		return nil
	}
	if predecessor.ProductionSizeMax == nil || predecessor.MeasurementBasis == nil || replacement.Cap != *predecessor.ProductionSizeMax {
		return invalidWorkerPayload("worker.job_split replaced_attempt cap does not match its predecessor")
	}
	var bound int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events d JOIN domain_events auth ON auth.subject_type=d.subject_type AND auth.subject_id=d.subject_id AND auth.kind=? AND json_extract(auth.payload,'$.action_id')='dispatch_worker' AND json_extract(auth.payload,'$.worker_attempt_id')=json_extract(d.payload,'$.attempt_id') AND json_extract(auth.payload,'$.attempt_epoch')=? AND auth.seq<d.seq WHERE d.subject_type=? AND d.subject_id=? AND d.kind=? AND d.seq<? AND json_extract(d.payload,'$.attempt_id')=? AND json_extract(d.payload,'$.capability_class')='implementation' AND json_extract(d.payload,'$.worker_job.job_id')=? AND json_extract(d.payload,'$.worker_job.revision')=? AND json_extract(d.payload,'$.worker_job.digest')=?`, WorkflowActionCompleted, replacement.AttemptEpoch, string(SubjectWorkItem), event.SubjectID, WorkerDispatched, event.Seq, replacement.AttemptID, payload.Predecessor.JobID, payload.Predecessor.Revision, payload.Predecessor.Digest).Scan(&bound); err != nil {
		return workflowProjectionError(err, "cannot verify the replaced attempt's dispatch binding")
	}
	if bound != 1 {
		return invalidWorkerPayload("worker.job_split replaced_attempt does not bind the exact dispatched predecessor and epoch")
	}
	completed, found, err := readWorkerCompletedSizeTx(ctx, tx, event.SubjectID, replacement.AttemptID, event.Seq)
	if err != nil {
		return err
	}
	if !found || completed.reportRef != replacement.ReportRef || completed.observed == nil || completed.observed.Commit != replacement.ObservedCommit {
		return invalidWorkerPayload("worker.job_split replaced_attempt does not bind its exact completed report")
	}
	reported, unavailable := workerJobReportedLineCount(predecessor.MeasurementBasis, completed.observed)
	if unavailable != "" || completed.size == nil || completed.size.Count != reported {
		return invalidWorkerPayload("worker.job_split replaced_attempt report does not agree with its sealed observation")
	}
	foreign := map[string]bool{}
	for _, path := range replacement.ForeignAcceptedPaths {
		if !slices.ContainsFunc(completed.observed.Paths, func(observed WorkerObservedPath) bool { return observed.Path == path }) {
			return invalidWorkerPayload("worker.job_split replaced_attempt foreign path is not in its sealed observation")
		}
		foreign[path] = true
	}
	fields := map[string]json.RawMessage{}
	if replacement.SupplementalClassifications != nil {
		fields["supplemental_classifications"], _ = json.Marshal(replacement.SupplementalClassifications)
	}
	supplemental, err := admitWorkerSupplementalClassifications("worker.job_split replaced_attempt", fields, predecessor.MeasurementBasis, completed, foreign)
	if err != nil {
		return err
	}
	count, unavailable := workerJobProductionSize(predecessor.MeasurementBasis, supplemental, completed.observed, foreign)
	if unavailable != "" || count != replacement.ObservedCount {
		return invalidWorkerPayload(strings.TrimSpace("worker.job_split replaced_attempt count does not match the retained overrun evidence " + unavailable))
	}
	return nil
}
