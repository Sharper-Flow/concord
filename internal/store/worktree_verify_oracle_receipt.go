package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

// errOracleReceiptRefused marks a receipt that fails one admission join. The
// producer check maps it to the single missing-evidence refusal.
var errOracleReceiptRefused = errors.New("oracle receipt refused")

// validateNativeOracleProducerTx admits an execute receipt only when it joins
// its persisted plan, the dispatch that authorized it, the preparation it
// named, the recorded job history, its recipe selection, and the stages that
// support its qualification. Admission reads metadata and lengths only; full
// BLOB hash checks belong to the output read owner, not a nested query in a
// report fold.
func validateNativeOracleProducerTx(ctx context.Context, q queryer, work string, r *WorktreeVerifyResult, qualification string) error {
	err := checkNativeOracleProducerTx(ctx, q, work, r, qualification)
	if errors.Is(err, errOracleReceiptRefused) {
		return oracleFailure(KindMissingEvidence, "oracle receipt has no qualified native execute plan and complete case witnesses", "execute the pinned control through its persisted authorization")
	}
	return err
}

func checkNativeOracleProducerTx(ctx context.Context, q queryer, work string, r *WorktreeVerifyResult, qualification string) error {
	if r == nil || r.Oracle == nil || !nativeOracleReceiptComplete(r.Oracle, qualification) {
		return errOracleReceiptRefused
	}
	o := r.Oracle
	p, metadata, err := readNativeOracleExecutePlanTx(ctx, q, work, r)
	if err != nil {
		return err
	}
	envDigest, envIdentity := nativePlanEnvironment(p)
	if !nativeOraclePlanMatchesReceipt(p, work, r.ProjectID, o, envDigest, envIdentity) {
		return errOracleReceiptRefused
	}
	d, err := readNativeOracleAuthorizingDispatchTx(ctx, q, work, p)
	if err != nil {
		return err
	}
	if !nativeOracleDispatchMatches(d, p, o) {
		return errOracleReceiptRefused
	}
	if p.ReviewRepair != nil {
		if err := validateNativeReviewRepairTx(ctx, q, work, d, p.ReviewRepair); err != nil {
			return errOracleReceiptRefused
		}
	}
	// The original preparation qualifies assignment readiness and the pinned
	// harness. A repairing execute prepares C in its own immutable snapshot:
	// metadata/compile (or target collection) stages must precede its test.
	prep, err := readOraclePreparationReceiptTx(ctx, q, work, r.ProjectID, o.PreparationRunRef)
	if err != nil {
		return err
	}
	if !nativeOraclePreparationJoins(prep, p, o, envDigest, envIdentity) {
		return errOracleReceiptRefused
	}
	if err := validateNativeOracleHistoryTx(ctx, q, work, p, d, o); err != nil {
		if err == sql.ErrNoRows {
			return errOracleReceiptRefused
		}
		return err
	}
	if !nativeOracleSelectionMatches(r.Command, p, o) || !nativeOracleStagesWithinStreams(p, o) {
		return errOracleReceiptRefused
	}
	switch qualification {
	case "fail":
		if !nativeOracleFailureStandsOnOneStage(p, o) {
			return errOracleReceiptRefused
		}
	case "pass":
		if !nativeOraclePassComplete(p, o) {
			return errOracleReceiptRefused
		}
		return checkNativeOracleGreenOperationTx(ctx, q, work, r.OperationRef, metadata)
	}
	return nil
}

func nativeOracleReceiptComplete(o *NativeOracleResult, qualification string) bool {
	return o.Protocol == "native_oracle_v2" && o.Phase == "execute" && o.Qualification == qualification && o.StreamsComplete && o.Stdout.Complete && o.Stderr.Complete && o.Stdout.Ref != "" && o.Stderr.Ref != ""
}

// readNativeOracleExecutePlanTx reads the released lease's immutable plan and
// result metadata, and refuses a plan whose digest or stream lengths differ
// from the receipt.
func readNativeOracleExecutePlanTx(ctx context.Context, q queryer, work string, r *WorktreeVerifyResult) (nativeOraclePlan, string, error) {
	o := r.Oracle
	var plan, digest, metadata string
	var stdoutLength, stderrLength sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT native_plan_json,native_plan_sha256,result_json,length(stdout_blob),length(stderr_blob) FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND lease_id=? AND state='released' AND outcome='completed' AND native_plan_json IS NOT NULL`, work, r.ProjectID, r.LeaseID).Scan(&plan, &digest, &metadata, &stdoutLength, &stderrLength)
	if err == sql.ErrNoRows {
		return nativeOraclePlan{}, "", errOracleReceiptRefused
	}
	if err != nil {
		return nativeOraclePlan{}, "", err
	}
	var p nativeOraclePlan
	if json.Unmarshal([]byte(plan), &p) != nil || nativeDigest([]byte(plan)) != digest || digest != o.NativePlanSHA256 || !stdoutLength.Valid || !stderrLength.Valid || stdoutLength.Int64 != o.Stdout.Length || stderrLength.Int64 != o.Stderr.Length || o.Stdout.Length > nativeOracleStreamLimit || o.Stderr.Length > nativeOracleStreamLimit {
		return nativeOraclePlan{}, "", errOracleReceiptRefused
	}
	return p, metadata, nil
}

func nativeOraclePlanMatchesReceipt(p nativeOraclePlan, work, projectID string, o *NativeOracleResult, envDigest, envIdentity string) bool {
	return p.Request.Phase == "execute" && p.WorkID == work && p.ProjectID == projectID && p.SubjectCommit == o.SubjectCommit && p.ContractVersion == o.ContractVersion && p.BundleDigest == o.BundleDigest && o.BuildEnvironmentDigest == envDigest && o.ToolchainIdentity == envIdentity &&
		p.Request.AttemptID == o.AttemptID && p.Request.AttemptEpoch == o.AttemptEpoch && p.Request.WorkerPacketDigest == o.WorkerPacketDigest && p.Request.ControlID == o.ControlID && p.Request.PreparationRunRef == o.PreparationRunRef &&
		p.Request.WorkerJobBinding != nil && o.WorkerJobBinding != nil && *p.Request.WorkerJobBinding == *o.WorkerJobBinding &&
		p.Authorization.EventID == o.AuthorizationEventID && p.Authorization.Seq == o.AuthorizationSeq && p.Authorization.StartSeq == o.StartSeq &&
		p.Bundle.Control.Cwd == o.LogicalCwd && p.Bundle.Control.RecipeSource == o.RecipeSource && oracleEntryIdentical(p.Files, o.Files) && o.InputManifestDigest != ""
}

// readNativeOracleAuthorizingDispatchTx reads the dispatch_worker completion the plan's
// authorization names.
func readNativeOracleAuthorizingDispatchTx(ctx context.Context, q queryer, work string, p nativeOraclePlan) (workflowActionCompletedPayload, error) {
	var dispatch []byte
	if err := q.QueryRowContext(ctx, `SELECT payload FROM worker_authorizations WHERE event_id=? AND seq=? AND work_id=? AND attempt_id=?`, p.Authorization.EventID, p.Authorization.Seq, work, p.Request.AttemptID).Scan(&dispatch); err != nil {
		if err == sql.ErrNoRows {
			return workflowActionCompletedPayload{}, errOracleReceiptRefused
		}
		return workflowActionCompletedPayload{}, err
	}
	var d workflowActionCompletedPayload
	if json.Unmarshal(dispatch, &d) != nil {
		return workflowActionCompletedPayload{}, errOracleReceiptRefused
	}
	return d, nil
}

func nativeOracleDispatchMatches(d workflowActionCompletedPayload, p nativeOraclePlan, o *NativeOracleResult) bool {
	subject := o.SubjectCommit
	if p.ReviewRepair != nil {
		if p.ReviewRepair.Commit != o.SubjectCommit || !workerLaneRepairs(d.WorkerLaneID, d.WorkerLaneVersion, d.WorkerLaneDigest) {
			return false
		}
		subject = p.ReviewRepair.Base
	}
	return d.ActionID == "dispatch_worker" && d.WorkerAttemptID == o.AttemptID && d.AttemptEpoch == o.AttemptEpoch && d.WorkerPacketDigest == o.WorkerPacketDigest && d.WorkerSubjectCommit == subject && d.WorkerWorktreeIdentity == p.WorktreeIdentity && d.WorkerLaneID != "" && d.WorkerJob != nil && *d.WorkerJob == *o.WorkerJobBinding
}

func nativeOraclePreparationJoins(prep *NativeOraclePreparation, p nativeOraclePlan, o *NativeOracleResult, envDigest, envIdentity string) bool {
	preparationSubject := o.SubjectCommit
	if p.ReviewRepair != nil {
		preparationSubject = p.ReviewRepair.Base
	}
	if prep == nil || prep.SubjectCommit != preparationSubject || prep.ContractVersion != p.ContractVersion || prep.BundleDigest != p.BundleDigest || prep.ManifestDigest != p.ManifestFile.SHA256 || prep.ManifestBlob != p.ManifestFile.BlobOID || !oracleEntryIdentical(prep.Files, p.Files) || prep.ToolchainIdentity != envIdentity || prep.BuildEnvironmentDigest != envDigest {
		return false
	}
	if p.Target != nil {
		return oracleEntryIdentical(prep.CaseToWitnessMap, p.Target.Recipe.Cases) && len(prep.SelectedWitnessIDs) > 0 && len(prep.CaseToTestMap) == 0 && len(prep.SelectedTestNames) == 0
	}
	return oracleEntryIdentical(prep.CaseToTestMap, p.Manifest.Cases)
}

// nativeOracleSelectionMatches joins the receipt's command, control, manifest,
// and selected cases to the recipe family the plan pinned. A target receipt
// joins the exact admitted materializer/runner pair the plan decided — a
// presence check on the axis names alone would let an unsupported pair grade
// a witness.
func nativeOracleSelectionMatches(command []string, p nativeOraclePlan, o *NativeOracleResult) bool {
	if !slices.Equal(command, p.Bundle.Control.Argv) || o.ControlID != p.Bundle.Control.ControlID || o.ManifestDigest != p.ManifestFile.SHA256 {
		return false
	}
	if p.Target != nil {
		pair, err := nativeTargetPairOf(p.Target.Recipe)
		if err != nil {
			return false
		}
		witnesses, err := nativeTargetCaseWitnesses(p.Target.Recipe, p.Bundle.Control)
		if err != nil {
			return false
		}
		return slices.Equal(o.SelectedWitnessIDs, witnesses) && o.RecipeKind == nativeTargetKind && o.TargetMaterializer == pair.Materializer && o.TargetRunner == pair.Runner && o.TargetMaterializer == p.Target.Environment.Materializer && o.TargetRunner == p.Target.Environment.Runner && o.SelectedDistinctCount == len(witnesses) && len(o.SelectedTestNames) == 0 && len(o.CaseToTestMap) == 0 && oracleEntryIdentical(o.CaseToWitnessMap, p.Target.Recipe.Cases)
	}
	names, err := nativeOracleCaseTests(p.Manifest, p.Bundle.Control)
	if err != nil {
		return false
	}
	return slices.Equal(o.SelectedTestNames, names) && oracleEntryIdentical(o.CaseToTestMap, p.Manifest.Cases) && o.RecipeKind == "" && o.TargetMaterializer == "" && o.TargetRunner == "" && len(o.SelectedWitnessIDs) == 0 && len(o.CaseToWitnessMap) == 0
}

// nativeOracleStagesWithinStreams admits recorded stages that follow the plan
// in order and whose stream slices lie inside the captured streams.
func nativeOracleStagesWithinStreams(p nativeOraclePlan, o *NativeOracleResult) bool {
	for i, stage := range o.Stages {
		if i >= len(p.Stages) || stage.Name != p.Stages[i].Name || !slices.Equal(stage.Argv, p.Stages[i].Argv) || stage.StdoutOffset < 0 || stage.StderrOffset < 0 || stage.StdoutLength < 0 || stage.StderrLength < 0 || int64(stage.StdoutOffset+stage.StdoutLength) > o.Stdout.Length || int64(stage.StderrOffset+stage.StderrLength) > o.Stderr.Length {
			return false
		}
	}
	return true
}

// nativeOracleFailureStandsOnOneStage admits a failure only on the stage that
// produced it: the executed test or check stage that ended the run — or, for
// Go, the compile that ended it. Every other recorded stage succeeded.
func nativeOracleFailureStandsOnOneStage(p nativeOraclePlan, o *NativeOracleResult) bool {
	failed := 0
	for i, stage := range o.Stages {
		if stage.ExitCode == 0 {
			continue
		}
		if stage.Name != "test" && (p.Target != nil || stage.Name != "compile" || i != len(o.Stages)-1) {
			return false
		}
		failed++
	}
	return failed == 1
}

// nativeOraclePassComplete admits a pass only when every planned stage ran and
// succeeded and the observed cases equal the selected ones.
func nativeOraclePassComplete(p nativeOraclePlan, o *NativeOracleResult) bool {
	if len(o.Stages) != len(p.Stages) || o.BinaryDigest == "" || o.SelectedDistinctCount == 0 {
		return false
	}
	if p.Target != nil {
		if !slices.Equal(o.SelectedWitnessIDs, o.ObservedWitnessIDs) || o.SelectedDistinctCount != len(o.SelectedWitnessIDs) || len(o.ObservedTestNames) != 0 {
			return false
		}
	} else if !slices.Equal(o.SelectedTestNames, o.ObservedTestNames) || o.SelectedDistinctCount != len(o.SelectedTestNames) || len(o.SelectedTestNames) == 0 || len(o.ObservedWitnessIDs) != 0 {
		return false
	}
	for _, stage := range o.Stages {
		if stage.ExitCode != 0 {
			return false
		}
	}
	return true
}

// checkNativeOracleGreenOperationTx joins a pass to the completed verify
// operation whose result is the lease's own result metadata.
func checkNativeOracleGreenOperationTx(ctx context.Context, q queryer, work, operationRef, metadata string) error {
	var green string
	if err := q.QueryRowContext(ctx, `SELECT result_payload FROM durable_operations WHERE work_id=? AND op_id=? AND workflow_type_ref='worktree.verify' AND result_kind='completed'`, work, operationRef).Scan(&green); err != nil {
		if err == sql.ErrNoRows {
			return errOracleReceiptRefused
		}
		return err
	}
	if green != metadata {
		return errOracleReceiptRefused
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
	if err := q.QueryRowContext(ctx, `SELECT start_payload FROM worker_authorizations WHERE work_id=? AND attempt_id=? AND event_id=? AND seq=? AND start_seq=? AND start_seq>0 AND start_seq<seq`, work, o.AttemptID, p.Authorization.EventID, p.Authorization.Seq, p.Authorization.StartSeq).Scan(&start); err != nil {
		return err
	}
	var s workflowActionStartedPayload
	if json.Unmarshal(start, &s) != nil || s.ActionID != "dispatch_worker" || s.StepID != d.StepID || s.AttemptEpoch != o.AttemptEpoch {
		return sql.ErrNoRows
	}
	// The authorization writer captures the latest same-step dispatch start
	// in its transaction. Its immutable snapshot is the pairing proof.
	b := o.WorkerJobBinding
	var jobRaw []byte
	if err := q.QueryRowContext(ctx, `SELECT recorded_payload FROM worker_job_revisions WHERE work_id=? AND recorded_seq>0 AND recorded_seq<? AND job_id=? AND revision=? AND digest=?`, work, p.Authorization.Seq, b.JobID, b.Revision, b.Digest).Scan(&jobRaw); err != nil {
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
