package store

import (
	"context"
	"encoding/json"
	"strings"
)

// nativeReviewRepair is producer-observed, never a request field. B authorizes
// the scoped pass; C is separately materialized, compiled/collected, and tested
// under its execute lease. B's preparation grants readiness, not proof on C.
type nativeReviewRepair struct {
	Base         string   `json:"base"`
	Commit       string   `json:"commit"`
	ChangedPaths []string `json:"changed_paths"`
}

type nativeReviewRepairKey struct{}

func nativeReviewRepairFrom(ctx context.Context) *nativeReviewRepair {
	r, _ := ctx.Value(nativeReviewRepairKey{}).(*nativeReviewRepair)
	return r
}

func observeNativeReviewRepair(ctx context.Context, q queryer, runner GitRunner, req WorktreeVerifyRequest, entry WorktreeEntry, observed oracleGitSubjectSnapshot) (*nativeReviewRepair, error) {
	if req.Oracle.Phase != "execute" {
		return nil, nil
	}
	d, _, err := readNativeOracleDispatchBindingTx(ctx, q, req.WorkID, req.Oracle.AttemptID)
	if err != nil || d.WorkerSubjectCommit == observed.head {
		return nil, err
	}
	if !observed.clean || !workerLaneRepairs(d.WorkerLaneID, d.WorkerLaneVersion, d.WorkerLaneDigest) {
		return nil, nativeOracleSubjectMoved(d.WorkerSubjectCommit, observed.head)
	}
	probeCtx, cancel := context.WithTimeout(ctx, workerTerminalObservationDeadline)
	defer cancel()
	if _, err := runner.Run(probeCtx, entry.Path, "merge-base", "--is-ancestor", d.WorkerSubjectCommit, observed.head); err != nil {
		return nil, oracleFailure(KindUnauthorizedDispatch, "repair output is not a proved descendant of its assigned candidate", "restore the assigned candidate and apply only the scoped repair")
	}
	names, err := runner.Run(probeCtx, entry.Path, append(workerDiffSharedDiffArgs(), "--name-only", "-z", d.WorkerSubjectCommit, observed.head)...)
	if err != nil {
		return nil, workerDeliveryProbeFailure("repair changed paths", probeCtx, err)
	}
	repair := &nativeReviewRepair{Base: d.WorkerSubjectCommit, Commit: observed.head, ChangedPaths: []string{}}
	for _, path := range strings.Split(string(names), "\x00") {
		if path != "" {
			repair.ChangedPaths = append(repair.ChangedPaths, path)
		}
	}
	if err := validateNativeReviewRepairTx(ctx, q, req.WorkID, d, repair); err != nil {
		return nil, err
	}
	return repair, nil
}

func validateNativeReviewRepairTx(ctx context.Context, q queryer, work string, d workflowActionCompletedPayload, repair *nativeReviewRepair) error {
	refuse := func() error {
		return oracleFailure(KindUnauthorizedDispatch, "repair output does not match its admitted editing scope and input candidate", "restore the assigned candidate or reject the result and record the needed scope")
	}
	if repair == nil || repair.Base != d.WorkerSubjectCommit || !worktreeSHAPattern.MatchString(repair.Commit) || !workerLaneRepairs(d.WorkerLaneID, d.WorkerLaneVersion, d.WorkerLaneDigest) || d.WorkerJob == nil {
		return refuse()
	}
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? AND json_extract(payload,'$.digest')=? ORDER BY seq DESC LIMIT 1`, work, WorkerJobRecorded, d.WorkerJob.JobID, d.WorkerJob.Revision, d.WorkerJob.Digest).Scan(&raw)
	if err != nil {
		return err
	}
	var job WorkerJobRecordedPayload
	if json.Unmarshal(raw, &job) != nil || DeriveWorkerJobDigest(job) != d.WorkerJob.Digest || len(job.PathScope) == 0 {
		return refuse()
	}
	for _, path := range repair.ChangedPaths {
		if !workerJobPathsIntersect(job.PathScope, path) {
			return refuse()
		}
	}
	return nil
}
