package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
)

var errOracleReceiptRefused = errors.New("oracle receipt refused")

func validateNativeOracleProducerTx(ctx context.Context, q queryer, work string, r *WorktreeVerifyResult, qualification string) error {
	err := checkNativeOracleProducerTx(ctx, q, work, r, qualification)
	if errors.Is(err, errOracleReceiptRefused) {
		return oracleFailure(KindMissingEvidence, "oracle receipt has no qualified native execute plan and complete case witnesses", "execute the pinned preparation through native verification")
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
	digest, identity := nativePlanEnvironment(p)
	if !nativeOraclePlanMatchesReceipt(p, work, r.ProjectID, o, digest, identity) {
		return errOracleReceiptRefused
	}
	prep, err := readOraclePreparationReceiptTx(ctx, q, work, r.ProjectID, o.PreparationRunRef)
	if err != nil {
		return err
	}
	if !nativeOraclePreparationJoins(prep, p, o, digest, identity) {
		return errOracleReceiptRefused
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

func readNativeOracleExecutePlanTx(ctx context.Context, q queryer, work string, r *WorktreeVerifyResult) (nativeOraclePlan, string, error) {
	o := r.Oracle
	var plan, digest, metadata string
	var stdout, stderr sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT native_plan_json,native_plan_sha256,result_json,length(stdout_blob),length(stderr_blob) FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND lease_id=? AND state='released' AND outcome='completed' AND native_plan_json IS NOT NULL`, work, r.ProjectID, r.LeaseID).Scan(&plan, &digest, &metadata, &stdout, &stderr)
	if err == sql.ErrNoRows {
		return nativeOraclePlan{}, "", errOracleReceiptRefused
	}
	if err != nil {
		return nativeOraclePlan{}, "", err
	}
	var p nativeOraclePlan
	if json.Unmarshal([]byte(plan), &p) != nil || nativeDigest([]byte(plan)) != digest || digest != o.NativePlanSHA256 || !stdout.Valid || !stderr.Valid || stdout.Int64 != o.Stdout.Length || stderr.Int64 != o.Stderr.Length || o.Stdout.Length > nativeOracleStreamLimit || o.Stderr.Length > nativeOracleStreamLimit {
		return nativeOraclePlan{}, "", errOracleReceiptRefused
	}
	return p, metadata, nil
}

func nativeOraclePlanMatchesReceipt(p nativeOraclePlan, work, project string, o *NativeOracleResult, digest, identity string) bool {
	if !nativeIsolationResultMatches(p, &o.NativeOraclePreparation, o.Stages) || !nativeGitMetadataMatches(p, o.GitMetadata) {
		return false
	}
	return p.Request.Phase == "execute" && p.WorkID == work && p.ProjectID == project && p.SubjectCommit == o.SubjectCommit && p.ContractVersion == o.ContractVersion && p.BundleDigest == o.BundleDigest && p.BundleDigest == nativeBundleDigest(p.Bundle) && o.BuildEnvironmentDigest == digest && o.ToolchainIdentity == identity && p.Request.PreparationRunRef == o.PreparationRunRef && p.Bundle.Control.ControlID == o.ControlID && p.Bundle.Control.Cwd == o.LogicalCwd && p.Bundle.Control.RecipeSource == o.RecipeSource && oracleEntryIdentical(p.Files, o.Files) && o.InputManifestDigest != ""
}

func nativeOraclePreparationJoins(prep *NativeOraclePreparation, p nativeOraclePlan, o *NativeOracleResult, digest, identity string) bool {
	if prep == nil || !nativeIsolationPreparationMatches(p, prep) || prep.SubjectCommit != o.SubjectCommit || prep.ContractVersion != p.ContractVersion || prep.BundleDigest != p.BundleDigest || prep.ManifestDigest != p.ManifestFile.SHA256 || prep.ManifestBlob != p.ManifestFile.BlobOID || !oracleEntryIdentical(prep.Files, p.Files) || prep.ToolchainIdentity != identity || prep.BuildEnvironmentDigest != digest {
		return false
	}
	if p.Target != nil {
		return oracleEntryIdentical(prep.CaseToWitnessMap, p.Target.Recipe.Cases) && len(prep.SelectedWitnessIDs) > 0 && len(prep.CaseToTestMap) == 0 && len(prep.SelectedTestNames) == 0
	}
	return oracleEntryIdentical(prep.CaseToTestMap, p.Manifest.Cases)
}

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
	return err == nil && slices.Equal(o.SelectedTestNames, names) && oracleEntryIdentical(o.CaseToTestMap, p.Manifest.Cases) && o.RecipeKind == "" && o.TargetMaterializer == "" && o.TargetRunner == "" && len(o.SelectedWitnessIDs) == 0 && len(o.CaseToWitnessMap) == 0
}

func nativeOracleStagesWithinStreams(p nativeOraclePlan, o *NativeOracleResult) bool {
	for i, stage := range o.Stages {
		if i >= len(p.Stages) || stage.Name != p.Stages[i].Name || !slices.Equal(stage.Argv, p.Stages[i].Argv) || stage.StdoutOffset < 0 || stage.StderrOffset < 0 || stage.StdoutLength < 0 || stage.StderrLength < 0 || int64(stage.StdoutOffset+stage.StdoutLength) > o.Stdout.Length || int64(stage.StderrOffset+stage.StderrLength) > o.Stderr.Length {
			return false
		}
	}
	return true
}

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

func checkNativeOracleGreenOperationTx(ctx context.Context, q queryer, work, ref, metadata string) error {
	var green string
	err := q.QueryRowContext(ctx, `SELECT result_payload FROM durable_operations WHERE work_id=? AND op_id=? AND workflow_type_ref='worktree.verify' AND result_kind='completed'`, work, ref).Scan(&green)
	if err == sql.ErrNoRows {
		return errOracleReceiptRefused
	}
	if err != nil {
		return err
	}
	if green != metadata {
		return errOracleReceiptRefused
	}
	return nil
}
