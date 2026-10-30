package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

type NativeOracleRequest struct {
	Phase                   string                     `json:"phase"`
	ExpectedContractVersion int64                      `json:"expected_contract_version,omitempty"`
	ControlBundle           *NativeOracleControlBundle `json:"control_bundle,omitempty"`
	PreparationRunRef       string                     `json:"preparation_run_ref,omitempty"`
}
type NativeOracleControlBundle struct {
	Owner   OracleOwner   `json:"owner"`
	Control OracleControl `json:"control"`
	Cases   []OracleCase  `json:"cases"`
}
type NativeOracleFile struct {
	Path    string `json:"path"`
	BlobOID string `json:"blob_oid"`
	SHA256  string `json:"sha256"`
}
type NativeOraclePreparation struct {
	Protocol               string                   `json:"protocol"`
	Phase                  string                   `json:"phase"`
	Qualification          string                   `json:"qualification"`
	RunRef                 string                   `json:"run_ref"`
	WorkID                 string                   `json:"work_id"`
	ProjectID              string                   `json:"project_id"`
	ContractVersion        int64                    `json:"contract_version"`
	SubjectCommit          string                   `json:"subject_commit"`
	BundleDigest           string                   `json:"bundle_digest"`
	LogicalArgvDigest      string                   `json:"logical_argv_digest"`
	LogicalCwd             string                   `json:"logical_cwd"`
	RecipeSource           WorkContextReadingSource `json:"recipe_source"`
	ManifestBlob           string                   `json:"manifest_blob"`
	ManifestDigest         string                   `json:"manifest_digest"`
	Files                  []NativeOracleFile       `json:"files"`
	ToolchainIdentity      string                   `json:"toolchain_identity"`
	BuildEnvironmentDigest string                   `json:"build_environment_digest"`
	SelectedTestNames      []string                 `json:"selected_test_names,omitempty"`
	CaseToTestMap          map[string][]string      `json:"case_to_test_map,omitempty"`
	SelectedDistinctCount  int                      `json:"selected_distinct_count"`
	NativePlanSHA256       string                   `json:"native_plan_sha256"`
	Stdout                 NativeOracleStream       `json:"stdout"`
	Stderr                 NativeOracleStream       `json:"stderr"`
	StreamsComplete        bool                     `json:"streams_complete"`
	RecipeKind             string                   `json:"recipe_kind,omitempty"`
	TargetMaterializer     string                   `json:"target_materializer,omitempty"`
	TargetRunner           string                   `json:"target_runner,omitempty"`
	SelectedWitnessIDs     []string                 `json:"selected_witness_ids,omitempty"`
	CaseToWitnessMap       map[string][]string      `json:"case_to_witness_map,omitempty"`
	IsolationPolicy        string                   `json:"isolation_policy,omitempty"`
	IsolationFingerprint   string                   `json:"isolation_fingerprint,omitempty"`
	IsolationEnforcement   string                   `json:"isolation_enforcement,omitempty"`
}
type NativeOracleStage struct {
	IsolationFingerprint string   `json:"isolation_fingerprint,omitempty"`
	IsolationEnforced    bool     `json:"isolation_enforced,omitempty"`
	Name                 string   `json:"name"`
	Argv                 []string `json:"argv"`
	ExitCode             int      `json:"exit_code"`
	StdoutOffset         int      `json:"stdout_offset"`
	StdoutLength         int      `json:"stdout_length"`
	StderrOffset         int      `json:"stderr_offset"`
	StderrLength         int      `json:"stderr_length"`
}
type NativeOracleResult struct {
	NativeOraclePreparation
	GitMetadata         *NativeGitMetadata  `json:"git_metadata,omitempty"`
	ControlID           string              `json:"control_id"`
	PreparationRunRef   string              `json:"preparation_run_ref,omitempty"`
	InputManifestDigest string              `json:"input_manifest_digest,omitempty"`
	BinaryDigest        string              `json:"binary_digest,omitempty"`
	Stages              []NativeOracleStage `json:"stages"`
	ObservedTestNames   []string            `json:"observed_test_names,omitempty"`
	ObservedWitnessIDs  []string            `json:"observed_witness_ids,omitempty"`
	Detail              string              `json:"detail,omitempty"`
}
type nativeOraclePlan struct {
	RequestID        string                    `json:"request_id"`
	Request          NativeOracleRequest       `json:"request"`
	WorkID           string                    `json:"work_id"`
	ProjectID        string                    `json:"project_id"`
	ContractVersion  int64                     `json:"contract_version"`
	SubjectCommit    string                    `json:"subject_commit"`
	WorktreeIdentity string                    `json:"worktree_identity"`
	Bundle           NativeOracleControlBundle `json:"bundle"`
	BundleDigest     string                    `json:"bundle_digest"`
	Manifest         nativeGoOracleManifest    `json:"manifest"`
	ManifestFile     NativeOracleFile          `json:"manifest_file"`
	Files            []NativeOracleFile        `json:"files"`
	Environment      nativeGoEnvironment       `json:"environment"`
	Target           *nativeTargetOraclePlan   `json:"target,omitempty"`
	Stages           []NativeOracleStage       `json:"stages"`
	StreamLimit      int                       `json:"stream_limit"`
	CachePolicy      string                    `json:"cache_policy"`
}

func nativePlanEnvironment(p nativeOraclePlan) (string, string) {
	if p.Target != nil {
		return p.Target.Environment.Digest, p.Target.Environment.RuntimeIdentity
	}
	return p.Environment.Digest, p.Environment.ToolchainIdentity
}
func nativePlanPrepareCommand(p nativeOraclePlan) ([]string, error) {
	if p.Target != nil {
		return p.Target.prepareCommand(p.Bundle.Control)
	}
	for _, stage := range p.Stages {
		if stage.Name == "compile" {
			return stage.Argv, nil
		}
	}
	return nil, oracleFailure(KindInvariantViolation, "native plan has no canonical prepare stage", "reconcile the retained native run")
}

func normalizeNativeOraclePlan(ctx context.Context, r GitRunner, entry WorktreeEntry, req WorktreeVerifyRequest, b NativeOracleControlBundle, version int64, subject, identity string) (nativeOraclePlan, error) {
	p := nativeOraclePlan{Request: *req.Oracle, RequestID: req.RequestID, WorkID: req.WorkID, ProjectID: req.ProjectID, ContractVersion: version, SubjectCommit: subject, WorktreeIdentity: identity, Bundle: b, BundleDigest: nativeBundleDigest(b), StreamLimit: nativeOracleStreamLimit, CachePolicy: "private-disposable-per-lease"}
	if b.Control.RecipeSource.ProjectID != req.ProjectID || b.Owner.Mechanism.ProjectID != req.ProjectID {
		return p, nativeUnavailable("this recipe requires the ambient Project's own repository")
	}
	f, raw, err := nativeGitFile(ctx, r, entry.Path, b.Control.RecipeSource.CommitOID, b.Control.RecipeSource.Path)
	if err != nil {
		return p, err
	}
	if len(raw) > 16*1024 {
		return p, oracleFailure(KindLimitExceeded, "recipe manifest exceeds 16384 bytes", "bound the pinned manifest")
	}
	kind, err := nativeOracleRecipeKind(raw)
	if err != nil {
		return p, err
	}
	switch kind {
	case "go_top_level_v1":
		return normalizeNativeGoOraclePlan(ctx, r, entry, p, f, raw, req)
	case nativeTargetKind:
		return normalizeNativeTargetOraclePlan(ctx, r, entry, p, f, raw, req)
	default:
		return p, nativeUnavailable("unsupported recipe family")
	}
}

func validateNativeOracleRequest(req WorktreeVerifyRequest) error {
	o := req.Oracle
	if len(req.Command) != 0 || o == nil {
		return oracleFailure(KindInvalidPayload, "command and oracle requests are disjoint", "supply exactly one native request variant")
	}
	switch o.Phase {
	case "prepare":
		if o.ExpectedContractVersion < 1 || o.ControlBundle == nil || o.PreparationRunRef != "" {
			return oracleFailure(KindInvalidPayload, "prepare requires only contract version and control bundle", "supply the declared prepare input")
		}
	case "execute":
		if o.ExpectedContractVersion != 0 || o.ControlBundle != nil || o.PreparationRunRef == "" {
			return oracleFailure(KindInvalidPayload, "execute requires only preparation_run_ref", "select one exact retained preparation")
		}
	default:
		return oracleFailure(KindInvalidPayload, "oracle phase must be prepare or execute", "select the native phase")
	}
	return nil
}

func nativeBundleDigest(bundle NativeOracleControlBundle) string {
	bundle.Control.ReadinessEvidenceRefs = nil
	b, _ := json.Marshal(bundle)
	return nativeDigest(b)
}
func nativeBundleForControl(oracle *AcceptanceOracle, id string) (NativeOracleControlBundle, error) {
	var b NativeOracleControlBundle
	if oracle == nil {
		return b, oracleFailure(KindMissingEvidence, "no declared oracle bundle", "supply the declared control bundle")
	}
	count := 0
	for _, c := range oracle.Controls {
		if c.ControlID == id {
			b.Control = c
			count++
		}
	}
	if count != 1 {
		return b, oracleFailure(KindInvalidPayload, "control does not resolve exactly once", "select one declared control")
	}
	for _, owner := range oracle.Owners {
		if owner.OwnerID == b.Control.OwnerID {
			b.Owner = owner
		}
	}
	for _, c := range oracle.Cases {
		if c.Judge == "" && slices.Contains(b.Control.CaseIDs, c.CaseID) && slices.Contains(c.ControlIDs, id) {
			b.Cases = append(b.Cases, c)
		}
	}
	return b, nil
}

func readNativePreparationInputs(ctx context.Context, q queryer, req WorktreeVerifyRequest, head string) (NativeOracleControlBundle, int64, error) {
	prep, err := readOraclePreparationReceiptTx(ctx, q, req.WorkID, req.ProjectID, req.Oracle.PreparationRunRef)
	if err != nil {
		return NativeOracleControlBundle{}, 0, err
	}
	if prep == nil || prep.SubjectCommit != head {
		return NativeOracleControlBundle{}, 0, oracleFailure(KindMissingEvidence, "preparation does not name the exact clean execution subject", "prepare the current subject before execution")
	}
	var raw, digest string
	err = q.QueryRowContext(ctx, `SELECT native_plan_json,native_plan_sha256 FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND state='released' AND outcome='completed' AND json_extract(result_json,'$.operation_ref')=?`, req.WorkID, req.ProjectID, prep.RunRef).Scan(&raw, &digest)
	if err != nil {
		return NativeOracleControlBundle{}, 0, err
	}
	var plan nativeOraclePlan
	if json.Unmarshal([]byte(raw), &plan) != nil || nativeDigest([]byte(raw)) != digest || plan.Request.Phase != "prepare" || plan.WorkID != req.WorkID || plan.ProjectID != req.ProjectID || plan.SubjectCommit != head || plan.BundleDigest != prep.BundleDigest || plan.BundleDigest != nativeBundleDigest(plan.Bundle) {
		return NativeOracleControlBundle{}, 0, oracleFailure(KindInvariantViolation, "preparation differs from its retained native producer plan", "reconcile the retained preparation")
	}
	version, err := activeWorkflowContractVersion(ctx, q, req.WorkID, "worktree_verify")
	if err != nil {
		return plan.Bundle, plan.ContractVersion, err
	}
	if version != plan.ContractVersion {
		return plan.Bundle, plan.ContractVersion, oracleFailure(KindVersionConflict, "preparation contract is no longer active", "prepare against the current contract")
	}
	return plan.Bundle, plan.ContractVersion, nil
}

func readOraclePreparationReceiptTx(ctx context.Context, q queryer, work, project, ref string) (*NativeOraclePreparation, error) {
	var raw, plan, digest string
	var stdout, stderr sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT result_json,native_plan_json,native_plan_sha256,length(stdout_blob),length(stderr_blob) FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND state='released' AND outcome='completed' AND json_extract(result_json,'$.operation_ref')=? AND json_extract(result_json,'$.oracle.phase')='prepare' AND json_extract(result_json,'$.oracle.qualification')='ready'`, work, project, ref).Scan(&raw, &plan, &digest, &stdout, &stderr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r WorktreeVerifyResult
	var p nativeOraclePlan
	if json.Unmarshal([]byte(raw), &r) != nil || json.Unmarshal([]byte(plan), &p) != nil || nativeDigest([]byte(plan)) != digest || r.Oracle == nil || r.Oracle.NativePlanSHA256 != digest || !stdout.Valid || !stderr.Valid || stdout.Int64 != r.Oracle.Stdout.Length || stderr.Int64 != r.Oracle.Stderr.Length || !r.Oracle.StreamsComplete || r.Oracle.Protocol != "native_oracle_v2" || r.Oracle.RunRef != ref || r.WorkID != work || r.ProjectID != project || r.ExitCode != 0 || !nativePreparationComplete(r.Oracle, p) {
		return nil, oracleFailure(KindInvariantViolation, "preparation receipt differs from its immutable native plan", "reconcile the preparation lease")
	}
	return &r.Oracle.NativeOraclePreparation, nil
}

func nativePreparationComplete(o *NativeOracleResult, p nativeOraclePlan) bool {
	if !nativePreparationJoinsValid(o, p) {
		return false
	}
	if p.Target != nil {
		return nativeTargetPreparationWitnessesComplete(o, p)
	}
	names, err := nativeOracleSelector(p.Manifest, p.Bundle.Control)
	if err != nil || len(names) == 0 || !slices.Equal(o.SelectedTestNames, names) || o.SelectedDistinctCount != len(names) || len(o.CaseToTestMap) == 0 || !oracleEntryIdentical(o.CaseToTestMap, p.Manifest.Cases) {
		return false
	}
	return o.RecipeKind == "" && o.TargetMaterializer == "" && o.TargetRunner == "" && len(o.SelectedWitnessIDs) == 0 && len(o.CaseToWitnessMap) == 0
}

func nativePreparationJoinsValid(o *NativeOracleResult, p nativeOraclePlan) bool {
	if !nativeIsolationResultMatches(p, &o.NativeOraclePreparation, o.Stages) || !nativeGitMetadataMatches(p, o.GitMetadata) || p.Request.Phase != "prepare" || o.Phase != "prepare" || o.Qualification != "ready" {
		return false
	}
	if !worktreeSHAPattern.MatchString(o.SubjectCommit) || o.SubjectCommit != p.SubjectCommit || o.ContractVersion < 1 || o.ContractVersion != p.ContractVersion || o.WorkID != p.WorkID || o.ProjectID != p.ProjectID {
		return false
	}
	if o.BundleDigest != p.BundleDigest || p.BundleDigest != nativeBundleDigest(p.Bundle) || o.LogicalArgvDigest != nativeDigest([]byte(workflowJSON(p.Bundle.Control.Argv))) || o.LogicalCwd == "" || o.LogicalCwd != p.Bundle.Control.Cwd || o.RecipeSource != p.Bundle.Control.RecipeSource {
		return false
	}
	if !worktreeSHAPattern.MatchString(o.ManifestBlob) || o.ManifestBlob != p.ManifestFile.BlobOID || !workflowDigest(o.ManifestDigest) || o.ManifestDigest != p.ManifestFile.SHA256 || len(o.Files) < 1 || len(o.Files) > 32 || !oracleEntryIdentical(o.Files, p.Files) {
		return false
	}
	digest, identity := nativePlanEnvironment(p)
	if o.ToolchainIdentity == "" || o.ToolchainIdentity != identity || !workflowDigest(o.BuildEnvironmentDigest) || o.BuildEnvironmentDigest != digest {
		return false
	}
	if len(p.Stages) == 0 || len(o.Stages) != len(p.Stages) {
		return false
	}
	for i, stage := range o.Stages {
		if stage.Name != p.Stages[i].Name || !slices.Equal(stage.Argv, p.Stages[i].Argv) || stage.ExitCode != 0 {
			return false
		}
	}
	return true
}

func readNativeOraclePlanInputs(ctx context.Context, q queryer, req WorktreeVerifyRequest, head string) (NativeOracleControlBundle, int64, error) {
	if req.Oracle.Phase == "execute" {
		return readNativePreparationInputs(ctx, q, req, head)
	}
	version, err := activeWorkflowContractVersion(ctx, q, req.WorkID, "worktree_verify")
	if err != nil {
		return NativeOracleControlBundle{}, version, err
	}
	if version != req.Oracle.ExpectedContractVersion {
		return NativeOracleControlBundle{}, version, oracleFailure(KindVersionConflict, "preparation contract version is not active", "prepare against the active approved contract")
	}
	return *req.Oracle.ControlBundle, version, nil
}

func (s *Store) nativeOracleAdmission(ctx context.Context, req WorktreeVerifyRequest, entry WorktreeEntry, observed oracleGitSubjectSnapshot, expected nativeOraclePlan) error {
	tx, err := beginOrdinaryTx(ctx, s.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	live, err := activeWorktreeEntryForProject(ctx, tx, "worktree_verify", req.WorkID, req.ProjectID)
	if err != nil {
		return err
	}
	if live.Path != entry.Path || live.ClaimOpID != entry.ClaimOpID || live.Branch != entry.Branch {
		return oracleFailure(KindProjectionConflict, "the active claim changed", "refresh the claimed worktree")
	}
	if err := requireNoOutsideRepairTx(ctx, tx, req.WorkID, "worktree_verify"); err != nil {
		return err
	}
	core, err := readCurrentOracleSubject(ctx, tx, req.WorkID)
	if err != nil {
		return err
	}
	if !observed.clean || core == "" || core != observed.head {
		return oracleFailure(KindMissingEvidence, "native verification requires clean HEAD equal to the current qualified subject", `run worktree_verify with command ["git","rev-parse","--verify","HEAD^{commit}"] and a fresh request identity, then refresh context`)
	}
	b, version, err := readNativeOraclePlanInputs(ctx, tx, req, observed.head)
	if err != nil {
		return err
	}
	if req.Oracle.Phase == "prepare" {
		if err := validateNativeBundleAuthorityTx(ctx, tx, req.WorkID, version, b); err != nil {
			return err
		}
	}
	if expected.SubjectCommit != observed.head || expected.ContractVersion != version || expected.BundleDigest != nativeBundleDigest(b) {
		return oracleFailure(KindProjectionConflict, "native plan applicability changed", "prepare a new exact run")
	}
	if err := acquireVerifyLeaseTx(ctx, tx, req, entry, req.nativeCommandJSON, req.Now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the native verify lease", false, "reconcile_operation", err), nativeLeaseRef(req.LeaseID))
	}
	return nil
}

func validateNativeBundleAuthorityTx(ctx context.Context, tx *sql.Tx, work string, version int64, b NativeOracleControlBundle) error {
	if len(b.Control.ReadinessEvidenceRefs) != 0 {
		return oracleFailure(KindInvalidPayload, "native prepare input must omit readiness references", "supply the declared bundle")
	}
	if !oracleControlIDPattern.MatchString(b.Control.ControlID) || !oracleOwnerIDPattern.MatchString(b.Owner.OwnerID) || b.Control.OwnerID != b.Owner.OwnerID || len(b.Cases) < 1 || len(b.Cases) > OracleCasesMax {
		return oracleFailure(KindInvalidPayload, "preparation bundle identities are invalid", "supply one reciprocal owner/control/case bundle")
	}
	if err := validateOracleOwnerEntry(b.Owner); err != nil {
		return err
	}
	if err := validateOracleControlDefinition(b.Control); err != nil {
		return err
	}
	if len(b.Cases) != len(b.Control.CaseIDs) {
		return oracleFailure(KindInvalidPayload, "preparation cases differ from its control", "supply exactly its declared cases")
	}
	seen := map[string]bool{}
	for _, c := range b.Cases {
		if seen[c.CaseID] || c.OwnerID != b.Owner.OwnerID || !slices.Contains(b.Control.CaseIDs, c.CaseID) || !slices.Contains(c.ControlIDs, b.Control.ControlID) {
			return oracleFailure(KindInvalidPayload, "preparation bundle is not reciprocal", "supply the exact owner/control/case mapping")
		}
		seen[c.CaseID] = true
	}
	if err := validateOracleCases(b.Cases, map[string]OracleOwner{b.Owner.OwnerID: b.Owner}); err != nil {
		return err
	}
	oracle := &AcceptanceOracle{Owners: []OracleOwner{b.Owner}, Cases: b.Cases, Controls: []OracleControl{b.Control}}
	if err := validateOracleCombinedBound(oracle); err != nil {
		return err
	}
	return validateAcceptanceOracleAuthorityTx(ctx, tx, work, version, oracle)
}

func (s *Store) nativeOracleReplay(ctx context.Context, req WorktreeVerifyRequest) (WorktreeVerifyResult, bool, error) {
	var state, outcome, raw, plan, digest, work, project, principal, client, agent, session, command string
	err := s.db.QueryRowContext(ctx, `SELECT state,outcome,coalesce(result_json,''),coalesce(native_plan_json,''),coalesce(native_plan_sha256,''),work_id,project_id,principal_ref,client_ref,agent_ref,session_ref,command_json FROM worktree_verify_leases WHERE lease_id=?`, req.LeaseID).Scan(&state, &outcome, &raw, &plan, &digest, &work, &project, &principal, &client, &agent, &session, &command)
	if err == sql.ErrNoRows {
		return WorktreeVerifyResult{}, false, nil
	}
	if err != nil {
		return WorktreeVerifyResult{}, true, err
	}
	var pinned nativeOraclePlan
	if json.Unmarshal([]byte(plan), &pinned) != nil || nativeDigest([]byte(plan)) != digest {
		return WorktreeVerifyResult{}, true, oracleFailure(KindInvariantViolation, "native plan digest or bytes are corrupt", "use a new native request")
	}
	expectedCommand := pinned.Bundle.Control.Argv
	if pinned.Request.Phase != "execute" {
		expectedCommand, err = nativePlanPrepareCommand(pinned)
		if err != nil {
			return WorktreeVerifyResult{}, true, err
		}
	}
	if len(expectedCommand) == 0 || command != workflowJSON(expectedCommand) {
		return WorktreeVerifyResult{}, true, oracleFailure(KindInvariantViolation, "native lease argv differs from its immutable producer plan", "reconcile the retained run")
	}
	b, _ := json.Marshal(req.Oracle)
	a, _ := json.Marshal(pinned.Request)
	if !bytes.Equal(a, b) || pinned.RequestID != req.RequestID || work != req.WorkID || project != req.ProjectID || principal != req.PrincipalRef || client != req.Owner.ClientRef || agent != req.Owner.AgentRef || session != req.Owner.SessionRef {
		return WorktreeVerifyResult{}, true, oracleFailure(KindInvalidOperation, "retry differs from the pinned native request or scope", "use the original request or a fresh identity")
	}
	if state == "held" {
		return WorktreeVerifyResult{}, true, oracleFailure(KindWorktreeLeaseHeld, "native run remains held and cannot be executed twice", "reconcile the held lease")
	}
	if outcome == "aborted" {
		return WorktreeVerifyResult{}, true, oracleFailure(KindInvalidOperation, "abandoned native run has no replayable outcome", "request a new run")
	}
	var r WorktreeVerifyResult
	if json.Unmarshal([]byte(raw), &r) != nil || r.Oracle == nil || r.Oracle.NativePlanSHA256 != digest || r.WorkID != work || r.ProjectID != project || r.LeaseID != req.LeaseID || r.OperationRef != worktreeVerifyOperationRef(req.LeaseID) || !slices.Equal(r.Command, expectedCommand) {
		return r, true, oracleFailure(KindInvariantViolation, "native replay metadata is corrupt", "reconcile the retained native run")
	}
	r.LeaseDisposition = &WorktreeVerifyLeaseDisposition{State: state, Outcome: outcome}
	return r, true, nil
}

func (s *Store) verifyNativeOracle(ctx context.Context, req WorktreeVerifyRequest) (WorktreeVerifyResult, error) {
	if err := validateNativeOracleRequest(req); err != nil {
		return WorktreeVerifyResult{}, err
	}
	if r, found, err := s.nativeOracleReplay(ctx, req); found || err != nil {
		return r, err
	}
	if req.Now.IsZero() {
		req.Now = time.Now().UTC()
	}
	runner := req.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	entry, err := activeWorktreeEntryForProject(ctx, s.db, "worktree_verify", req.WorkID, req.ProjectID)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if err := probeWorktreeReachable(ctx, runner, "worktree_verify", entry); err != nil {
		return WorktreeVerifyResult{}, err
	}
	canonical, err := normalizePath(entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	identity := nativeWorktreePathIdentity(canonical)
	before, err := snapshotOracleGitSubject(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	bundle, version, err := readNativeOraclePlanInputs(ctx, s.db, req, before.head)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	plan, err := normalizeNativeOraclePlan(ctx, runner, entry, req, bundle, version, before.head, identity)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if req.Oracle.Phase == "execute" {
		if err := validateNativeExecutionPreparation(ctx, s.db, req, plan); err != nil {
			return WorktreeVerifyResult{}, err
		}
	}
	planRaw, err := json.Marshal(plan)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if len(planRaw) > 65536 {
		return WorktreeVerifyResult{}, oracleFailure(KindLimitExceeded, "native producer plan exceeds 65536 bytes", "reduce the harness inventory")
	}
	req.nativePlanJSON = string(planRaw)
	req.nativePlanSHA256 = nativeDigest(planRaw)
	if req.Oracle.Phase == "prepare" {
		req.Command, err = nativePlanPrepareCommand(plan)
		if err != nil {
			return WorktreeVerifyResult{}, err
		}
	} else {
		req.Command = bundle.Control.Argv
	}
	if err := validateWorktreeVerifyCommand(req.Command); err != nil {
		return WorktreeVerifyResult{}, err
	}
	req.nativeCommandJSON = workflowJSON(req.Command)
	req.nativeAcceptedInputsDigest = nativeDigest([]byte(req.nativeCommandJSON + "\x00" + req.nativePlanSHA256))
	req.nativeEvidenceRefsJSON = workflowJSON([]string{worktreeVerifyOperationRef(req.LeaseID)})
	req.nativeLeaseOwner = currentProcessIdentity()
	fresh, err := snapshotOracleGitSubject(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if fresh != before {
		return WorktreeVerifyResult{}, oracleFailure(KindProjectionConflict, "candidate subject changed while the native plan was prepared", "request a new run against the current candidate")
	}
	if err := s.nativeOracleAdmission(ctx, req, entry, fresh, plan); err != nil {
		var completed *worktreeVerifyCompleted
		if errors.As(err, &completed) {
			return completed.result, completed.failure
		}
		return WorktreeVerifyResult{}, err
	}
	released := false
	defer releaseAbandonedVerifyLease(s, ctx, req.LeaseID, &released)
	capture := &nativeStreamCapture{stdout: []byte{}, stderr: []byte{}, complete: true}
	result := nativeOracleInitialResult(req, entry, plan)
	return s.executeNativeOracleLease(ctx, runner, entry, req, plan, before, capture, result, &released)
}

func validateNativeExecutionPreparation(ctx context.Context, q queryer, req WorktreeVerifyRequest, plan nativeOraclePlan) error {
	prep, err := readOraclePreparationReceiptTx(ctx, q, req.WorkID, req.ProjectID, req.Oracle.PreparationRunRef)
	if err != nil {
		return err
	}
	digest, identity := nativePlanEnvironment(plan)
	if prep == nil || prep.SubjectCommit != plan.SubjectCommit || prep.ContractVersion != plan.ContractVersion || prep.BundleDigest != plan.BundleDigest || !nativeIsolationPreparationMatches(plan, prep) || prep.BuildEnvironmentDigest != digest || prep.ToolchainIdentity != identity || prep.ManifestDigest != plan.ManifestFile.SHA256 || !oracleEntryIdentical(prep.Files, plan.Files) {
		return oracleFailure(KindMissingEvidence, "preparation subject, environment or pinned inputs differ from execution", "prepare this exact subject, environment and bundle")
	}
	return nil
}

func (s *Store) executeNativeOracleLease(ctx context.Context, runner GitRunner, entry WorktreeEntry, req WorktreeVerifyRequest, plan nativeOraclePlan, before oracleGitSubjectSnapshot, capture *nativeStreamCapture, result WorktreeVerifyResult, released *bool) (WorktreeVerifyResult, error) {
	leaseRef := nativeLeaseRef(req.LeaseID)
	var err error
	if plan.Target != nil {
		err = s.runNativeTargetOracle(ctx, runner, entry, req, plan, capture, result.Oracle)
	} else {
		err = s.runNativeGoOracle(ctx, runner, entry, req, plan, capture, result.Oracle)
	}
	effectErr := err
	if err != nil {
		result.Oracle.Qualification = "unavailable"
		result.Oracle.Detail = err.Error()
		result.ExitCode = -1
	} else {
		result.ExitCode = 0
		for _, stage := range result.Oracle.Stages {
			if stage.ExitCode != 0 {
				result.ExitCode = stage.ExitCode
				break
			}
		}
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), worktreeVerifyFinalizeTimeout)
	defer cancel()
	after, err := snapshotOracleGitSubject(finalCtx, runner, entry.Path)
	if err != nil {
		return result, annotateCommittedEffect(err, leaseRef)
	}
	result.TrackedFilesChanged = before != after
	if result.TrackedFilesChanged || !capture.complete || ctx.Err() != nil {
		result.Oracle.Qualification = "unavailable"
	}
	result.SubjectRef = oracleVerifySubject(before, after, result.TrackedFilesChanged)
	raw, err := marshalNativeVerifyRecord(&result, capture)
	if err != nil {
		return result, annotateCommittedEffect(err, leaseRef)
	}
	unavailable := result
	unavailableOracle := *result.Oracle
	unavailable.Oracle = &unavailableOracle
	unavailable.Oracle.Qualification = "unavailable"
	unavailable.Oracle.Detail = "native applicability or cancellation prevented publication"
	unavailableRaw, err := marshalNativeVerifyRecord(&unavailable, capture)
	if err != nil {
		return result, annotateCommittedEffect(err, leaseRef)
	}
	aborted := ctx.Err() != nil
	result, err = s.releaseNativeOracle(finalCtx, req, entry, after, plan, result, raw, unavailable, unavailableRaw, capture, aborted)
	if err != nil {
		return result, annotateCommittedEffect(err, leaseRef)
	}
	*released = true
	result.LeaseDisposition = &WorktreeVerifyLeaseDisposition{State: "released", Outcome: "completed"}
	if aborted {
		result.LeaseDisposition.Outcome = "aborted"
	}
	if effectErr != nil {
		return result, annotateCommittedEffect(effectErr, leaseRef)
	}
	if result.TrackedFilesChanged {
		return result, annotateCommittedEffect(oracleFailure(KindWorktreeVerifyMutated, "claimed worktree changed during native verification", "reconcile the candidate"), leaseRef)
	}
	return result, nil
}

func nativeLeaseRef(id string) SubjectCurrentVersion {
	return SubjectCurrentVersion{SubjectType: "worktree_verify_lease", SubjectID: id, Version: 1}
}
func nativeOracleInitialResult(req WorktreeVerifyRequest, entry WorktreeEntry, p nativeOraclePlan) WorktreeVerifyResult {
	r := WorktreeVerifyResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Path: entry.Path, Branch: entry.Branch, LeaseID: req.LeaseID, OperationRef: worktreeVerifyOperationRef(req.LeaseID), Command: req.Command}
	base := NativeOraclePreparation{Protocol: "native_oracle_v2", Phase: req.Oracle.Phase, Qualification: "unavailable", RunRef: r.OperationRef, WorkID: req.WorkID, ProjectID: req.ProjectID, ContractVersion: p.ContractVersion, SubjectCommit: p.SubjectCommit, BundleDigest: p.BundleDigest, LogicalArgvDigest: nativeDigest([]byte(workflowJSON(p.Bundle.Control.Argv))), LogicalCwd: p.Bundle.Control.Cwd, RecipeSource: p.Bundle.Control.RecipeSource, ManifestBlob: p.ManifestFile.BlobOID, ManifestDigest: p.ManifestFile.SHA256, Files: p.Files, NativePlanSHA256: req.nativePlanSHA256}
	base.BuildEnvironmentDigest, base.ToolchainIdentity = nativePlanEnvironment(p)
	var metadata *NativeGitMetadata
	if p.Target != nil {
		names, _ := nativeTargetCaseWitnesses(p.Target.Recipe, p.Bundle.Control)
		base.RecipeKind = nativeTargetKind
		base.TargetMaterializer = p.Target.Environment.Materializer
		base.TargetRunner = p.Target.Environment.Runner
		metadata = p.Target.GitMetadata
		base.SelectedWitnessIDs = names
		base.CaseToWitnessMap = p.Target.Recipe.Cases
		base.SelectedDistinctCount = len(names)
		if p.Target.Recipe.Isolation != "" {
			base.IsolationPolicy = p.Target.Recipe.Isolation
			base.IsolationFingerprint = nativeIsolationEnvironmentDigest(p.Target.Environment.Isolation)
			base.IsolationEnforcement = "unavailable"
		}
	} else {
		names, _ := nativeOracleSelector(p.Manifest, p.Bundle.Control)
		base.SelectedTestNames = names
		base.CaseToTestMap = p.Manifest.Cases
		base.SelectedDistinctCount = len(names)
	}
	r.Oracle = &NativeOracleResult{NativeOraclePreparation: base, GitMetadata: metadata, ControlID: p.Bundle.Control.ControlID, PreparationRunRef: req.Oracle.PreparationRunRef, Stages: []NativeOracleStage{}}
	return r
}

type nativeOracleReleaseStamp struct {
	Frontier     int64
	CoreReceipt  string
	Preparations string
}

func readNativeOracleReleaseStamp(ctx context.Context, q queryer, req WorktreeVerifyRequest) (nativeOracleReleaseStamp, error) {
	var stamp nativeOracleReleaseStamp
	err := q.QueryRowContext(ctx, `SELECT coalesce((SELECT effect_seq FROM work_items WHERE id=?),0),coalesce((`+oracleCurrentVerifyReceiptSQL+`),''),coalesce((SELECT json_array(lease_id,state,outcome,result_json,native_plan_json,native_plan_sha256,length(stdout_blob),length(stderr_blob)) FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND json_extract(result_json,'$.operation_ref')=?),'')`, req.WorkID, req.WorkID, WorktreeSetID(req.WorkID), req.WorkID, req.ProjectID, req.Oracle.PreparationRunRef).Scan(&stamp.Frontier, &stamp.CoreReceipt, &stamp.Preparations)
	return stamp, err
}

func (s *Store) releaseNativeOracle(ctx context.Context, req WorktreeVerifyRequest, entry WorktreeEntry, after oracleGitSubjectSnapshot, p nativeOraclePlan, result WorktreeVerifyResult, raw []byte, unavailable WorktreeVerifyResult, unavailableRaw []byte, capture *nativeStreamCapture, aborted bool) (WorktreeVerifyResult, error) {
	stamp, err := readNativeOracleReleaseStamp(ctx, s.db, req)
	if err != nil {
		return result, err
	}
	core, coreErr := readCurrentOracleSubject(ctx, s.db, req.WorkID)
	valid := coreErr == nil && core == p.SubjectCommit && after.clean && after.head == p.SubjectCommit
	if req.Oracle.Phase == "execute" {
		bundle, version, err := readNativePreparationInputs(ctx, s.db, req, after.head)
		valid = valid && err == nil && version == p.ContractVersion && nativeBundleDigest(bundle) == p.BundleDigest
	}
	checked, err := readNativeOracleReleaseStamp(ctx, s.db, req)
	if err != nil {
		return result, err
	}
	valid = valid && checked == stamp
	tx, err := beginOrdinaryTx(ctx, s.db)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	live, scopeErr := activeWorktreeEntryForProject(ctx, tx, "worktree_verify", req.WorkID, req.ProjectID)
	current, err := readNativeOracleReleaseStamp(ctx, tx, req)
	if err != nil {
		return result, err
	}
	version, versionErr := activeWorkflowContractVersion(ctx, tx, req.WorkID, "worktree_verify")
	outsideErr := requireNoOutsideRepairTx(ctx, tx, req.WorkID, "worktree_verify")
	if aborted || !valid || current != stamp || versionErr != nil || version != p.ContractVersion || outsideErr != nil || scopeErr != nil || live.ClaimOpID != entry.ClaimOpID || live.Path != entry.Path || live.Branch != entry.Branch {
		result = unavailable
		raw = unavailableRaw
	}
	outcome := "completed"
	if aborted {
		outcome = "aborted"
	}
	releasedAt := time.Now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE worktree_verify_leases SET state='released',released_at=?,exit_code=?,outcome=?,result_json=?,stdout_blob=?,stderr_blob=? WHERE lease_id=? AND state='held' AND native_plan_json=? AND native_plan_sha256=?`, releasedAt.Format(time.RFC3339Nano), result.ExitCode, outcome, string(raw), capture.stdout, capture.stderr, req.LeaseID, req.nativePlanJSON, req.nativePlanSHA256)
	if err != nil {
		return result, err
	}
	count, err := updated.RowsAffected()
	if err != nil {
		return result, err
	}
	if count != 1 {
		return result, oracleFailure(KindProjectionConflict, "native lease changed before release", "reconcile the lease")
	}
	if !aborted && req.Oracle.Phase == "execute" && result.Oracle.Qualification == "pass" && result.ExitCode == 0 && !result.TrackedFilesChanged {
		if err := recordWorktreeVerifyAuthorityTx(ctx, tx, req, req.nativeCommandJSON, req.Now, releasedAt, string(raw)); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}
