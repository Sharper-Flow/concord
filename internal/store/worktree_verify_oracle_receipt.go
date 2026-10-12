package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

// Admission reads metadata and lengths only; full BLOB hash checks belong to
// the output read owner, not a nested query in a report fold.
func validateNativeOracleProducerTx(ctx context.Context, q queryer, work string, r *WorktreeVerifyResult, qualification string) error {
	refuse := func() error {
		return oracleFailure(KindMissingEvidence, "oracle receipt has no qualified native execute plan and complete case witnesses", "execute the pinned control through its persisted authorization")
	}
	if r == nil || r.Oracle == nil {
		return refuse()
	}
	o := r.Oracle
	if o.Protocol != "native_oracle_v2" || o.Phase != "execute" || o.Qualification != qualification || !o.StreamsComplete || !o.Stdout.Complete || !o.Stderr.Complete || o.Stdout.Ref == "" || o.Stderr.Ref == "" {
		return refuse()
	}
	var plan, digest, metadata string
	var stdoutLength, stderrLength sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT native_plan_json,native_plan_sha256,result_json,length(stdout_blob),length(stderr_blob) FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND lease_id=? AND state='released' AND outcome='completed' AND native_plan_json IS NOT NULL`, work, r.ProjectID, r.LeaseID).Scan(&plan, &digest, &metadata, &stdoutLength, &stderrLength)
	if err == sql.ErrNoRows {
		return refuse()
	}
	if err != nil {
		return err
	}
	var p nativeOraclePlan
	if json.Unmarshal([]byte(plan), &p) != nil || nativeDigest([]byte(plan)) != digest || digest != o.NativePlanSHA256 || !stdoutLength.Valid || !stderrLength.Valid || stdoutLength.Int64 != o.Stdout.Length || stderrLength.Int64 != o.Stderr.Length || o.Stdout.Length > nativeOracleStreamLimit || o.Stderr.Length > nativeOracleStreamLimit {
		return refuse()
	}
	if p.Request.Phase != "execute" || p.WorkID != work || p.ProjectID != r.ProjectID || p.SubjectCommit != o.SubjectCommit || p.ContractVersion != o.ContractVersion || p.BundleDigest != o.BundleDigest || p.Environment.Digest != o.BuildEnvironmentDigest || p.Environment.ToolchainIdentity != o.ToolchainIdentity || p.Request.AttemptID != o.AttemptID || p.Request.AttemptEpoch != o.AttemptEpoch || p.Request.WorkerPacketDigest != o.WorkerPacketDigest || p.Request.ControlID != o.ControlID || p.Request.PreparationRunRef != o.PreparationRunRef || p.Request.WorkerJobBinding == nil || o.WorkerJobBinding == nil || *p.Request.WorkerJobBinding != *o.WorkerJobBinding || p.Authorization.EventID != o.AuthorizationEventID || p.Authorization.Seq != o.AuthorizationSeq || p.Authorization.StartSeq != o.StartSeq || p.Bundle.Control.Cwd != o.LogicalCwd || p.Bundle.Control.RecipeSource != o.RecipeSource || !oracleEntryIdentical(p.Files, o.Files) || o.InputManifestDigest == "" {
		return refuse()
	}
	var dispatch []byte
	if err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE event_id=? AND seq=? AND subject_type='work_item' AND subject_id=? AND kind=?`, p.Authorization.EventID, p.Authorization.Seq, work, WorkflowActionCompleted).Scan(&dispatch); err != nil {
		if err == sql.ErrNoRows {
			return refuse()
		}
		return err
	}
	var d workflowActionCompletedPayload
	if json.Unmarshal(dispatch, &d) != nil || d.ActionID != "dispatch_worker" || d.WorkerAttemptID != o.AttemptID || d.AttemptEpoch != o.AttemptEpoch || d.WorkerPacketDigest != o.WorkerPacketDigest || d.WorkerSubjectCommit != o.SubjectCommit || d.WorkerWorktreeIdentity != p.WorktreeIdentity || d.WorkerLaneID == "" || d.WorkerJob == nil || *d.WorkerJob != *o.WorkerJobBinding {
		return refuse()
	}
	// The execution must still join the complete preparation it named: the
	// same candidate, contract, bundle, pinned inputs, and environment.
	prep, err := readOraclePreparationReceiptTx(ctx, q, work, r.ProjectID, o.PreparationRunRef)
	if err != nil {
		return err
	}
	if prep == nil || prep.SubjectCommit != o.SubjectCommit || prep.ContractVersion != p.ContractVersion || prep.BundleDigest != p.BundleDigest || prep.ManifestDigest != p.ManifestFile.SHA256 || prep.ManifestBlob != p.ManifestFile.BlobOID || !oracleEntryIdentical(prep.Files, p.Files) || prep.ToolchainIdentity != p.Environment.ToolchainIdentity || prep.BuildEnvironmentDigest != p.Environment.Digest || !oracleEntryIdentical(prep.CaseToTestMap, p.Manifest.Cases) {
		return refuse()
	}
	if err := validateNativeOracleHistoryTx(ctx, q, work, p, d, o); err != nil {
		if err == sql.ErrNoRows {
			return refuse()
		}
		return err
	}
	names, err := nativeOracleCaseTests(p.Manifest, p.Bundle.Control)
	if err != nil {
		return refuse()
	}
	if !slices.Equal(r.Command, p.Bundle.Control.Argv) || o.ControlID != p.Bundle.Control.ControlID || o.ManifestDigest != p.ManifestFile.SHA256 || !slices.Equal(o.SelectedTestNames, names) || !oracleEntryIdentical(o.CaseToTestMap, p.Manifest.Cases) {
		return refuse()
	}
	for i, stage := range o.Stages {
		if i >= len(p.Stages) || stage.Name != p.Stages[i].Name || !slices.Equal(stage.Argv, p.Stages[i].Argv) || stage.StdoutOffset < 0 || stage.StderrOffset < 0 || stage.StdoutLength < 0 || stage.StderrLength < 0 || int64(stage.StdoutOffset+stage.StdoutLength) > o.Stdout.Length || int64(stage.StderrOffset+stage.StderrLength) > o.Stderr.Length {
			return refuse()
		}
	}
	if qualification == "fail" {
		// A failure stands only on the stage that produced it: the compile
		// that ended the run, or the executed test program. Every other
		// recorded stage succeeded.
		failed := 0
		for i, stage := range o.Stages {
			if stage.ExitCode == 0 {
				continue
			}
			if !(stage.Name == "test" || (stage.Name == "compile" && i == len(o.Stages)-1)) {
				return refuse()
			}
			failed++
		}
		if failed != 1 {
			return refuse()
		}
	}
	if qualification == "pass" {
		if len(o.Stages) != len(p.Stages) || o.BinaryDigest == "" || !slices.Equal(o.SelectedTestNames, o.ObservedTestNames) || o.SelectedDistinctCount != len(o.SelectedTestNames) || len(o.SelectedTestNames) == 0 {
			return refuse()
		}
		for _, stage := range o.Stages {
			if stage.ExitCode != 0 {
				return refuse()
			}
		}
		var green string
		if err := q.QueryRowContext(ctx, `SELECT result_payload FROM durable_operations WHERE work_id=? AND op_id=? AND workflow_type_ref='worktree.verify' AND result_kind='completed'`, work, r.OperationRef).Scan(&green); err != nil {
			if err == sql.ErrNoRows {
				return refuse()
			}
			return err
		}
		if green != metadata {
			return refuse()
		}
	}
	return nil
}

// validateNativeOracleHistoryTx joins a retained execute receipt to the exact
// historical authorization tuple its plan pinned: the attempt's lane binding,
// the start that opened the dispatch, and the recorded job whose control
// bundle the plan executed. Lifecycle is not read: the attempt may have ended
// since the run. sql.ErrNoRows reports a missing join.
func validateNativeOracleHistoryTx(ctx context.Context, q queryer, work string, p nativeOraclePlan, d workflowActionCompletedPayload, o *NativeOracleResult) error {
	var lane, laneDigest, capability string
	var laneVersion int64
	if err := q.QueryRowContext(ctx, `SELECT lane_id,lane_version,lane_digest,capability_class FROM worker_attempts WHERE work_id=? AND attempt_id=?`, work, o.AttemptID).Scan(&lane, &laneVersion, &laneDigest, &capability); err != nil {
		return err
	}
	if lane != d.WorkerLaneID || laneVersion != d.WorkerLaneVersion || laneDigest != d.WorkerLaneDigest || capability != d.WorkerCapabilityClass {
		return sql.ErrNoRows
	}
	var start []byte
	if err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq=? AND seq<?`, work, WorkflowActionStarted, p.Authorization.StartSeq, p.Authorization.Seq).Scan(&start); err != nil {
		return err
	}
	var s workflowActionStartedPayload
	if json.Unmarshal(start, &s) != nil || s.ActionID != "dispatch_worker" || s.StepID != d.StepID || s.AttemptEpoch != o.AttemptEpoch {
		return sql.ErrNoRows
	}
	var later int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>? AND seq<? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.step_id')=?`, work, WorkflowActionStarted, p.Authorization.StartSeq, p.Authorization.Seq, d.StepID).Scan(&later); err != nil {
		return err
	}
	if later != 0 {
		return sql.ErrNoRows
	}
	b := o.WorkerJobBinding
	var jobRaw []byte
	if err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq<? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? AND json_extract(payload,'$.digest')=? ORDER BY seq DESC LIMIT 1`, work, WorkerJobRecorded, p.Authorization.Seq, b.JobID, b.Revision, b.Digest).Scan(&jobRaw); err != nil {
		return err
	}
	var job WorkerJobRecordedPayload
	if json.Unmarshal(jobRaw, &job) != nil || DeriveWorkerJobDigest(job) != b.Digest || job.ContractVersion != p.ContractVersion {
		return sql.ErrNoRows
	}
	bundle, err := nativeBundleForControl(job.AcceptanceOracle, o.ControlID)
	if err != nil || nativeBundleDigest(bundle) != p.BundleDigest || !slices.Contains(bundle.Control.ReadinessEvidenceRefs, o.PreparationRunRef) {
		return sql.ErrNoRows
	}
	return nil
}
