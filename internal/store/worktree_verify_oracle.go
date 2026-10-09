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
	AttemptID               string                     `json:"attempt_id,omitempty"`
	AttemptEpoch            int64                      `json:"attempt_epoch,omitempty"`
	WorkerPacketDigest      string                     `json:"worker_packet_digest,omitempty"`
	WorkerJobBinding        *WorkerJobBinding          `json:"worker_job_binding,omitempty"`
	ControlID               string                     `json:"control_id,omitempty"`
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
	SelectedTestNames      []string                 `json:"selected_test_names"`
	CaseToTestMap          map[string][]string      `json:"case_to_test_map"`
	SelectedDistinctCount  int                      `json:"selected_distinct_count"`
	NativePlanSHA256       string                   `json:"native_plan_sha256"`
	Stdout                 NativeOracleStream       `json:"stdout"`
	Stderr                 NativeOracleStream       `json:"stderr"`
	StreamsComplete        bool                     `json:"streams_complete"`
}

type NativeOracleStage struct {
	Name         string   `json:"name"`
	Argv         []string `json:"argv"`
	ExitCode     int      `json:"exit_code"`
	StdoutOffset int      `json:"stdout_offset"`
	StdoutLength int      `json:"stdout_length"`
	StderrOffset int      `json:"stderr_offset"`
	StderrLength int      `json:"stderr_length"`
}

type NativeOracleResult struct {
	NativeOraclePreparation
	AttemptID            string              `json:"attempt_id,omitempty"`
	AttemptEpoch         int64               `json:"attempt_epoch,omitempty"`
	WorkerPacketDigest   string              `json:"worker_packet_digest,omitempty"`
	WorkerJobBinding     *WorkerJobBinding   `json:"worker_job_binding,omitempty"`
	ControlID            string              `json:"control_id"`
	PreparationRunRef    string              `json:"preparation_run_ref,omitempty"`
	AuthorizationEventID string              `json:"authorization_event_id,omitempty"`
	AuthorizationSeq     int64               `json:"authorization_seq,omitempty"`
	StartSeq             int64               `json:"start_seq,omitempty"`
	InputManifestDigest  string              `json:"input_manifest_digest,omitempty"`
	BinaryDigest         string              `json:"binary_digest,omitempty"`
	Stages               []NativeOracleStage `json:"stages"`
	ObservedTestNames    []string            `json:"observed_test_names,omitempty"`
	Detail               string              `json:"detail,omitempty"`
}

type nativeOracleAuthorization struct {
	EventID  string `json:"event_id"`
	Seq      int64  `json:"seq"`
	StartSeq int64  `json:"start_seq"`
}

// The plan holds logical slots, never an ephemeral host pathname.
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
	Authorization    nativeOracleAuthorization `json:"authorization"`
	Stages           []NativeOracleStage       `json:"stages"`
	StreamLimit      int                       `json:"stream_limit"`
	CachePolicy      string                    `json:"cache_policy"`
}

func validateNativeOracleRequest(req WorktreeVerifyRequest) error {
	o := req.Oracle
	if len(req.Command) != 0 || o == nil {
		return oracleFailure(KindInvalidPayload, "command and oracle requests are disjoint", "supply exactly one native request variant")
	}
	switch o.Phase {
	case "prepare":
		if o.ExpectedContractVersion < 1 || o.ControlBundle == nil || o.AttemptID != "" || o.AttemptEpoch != 0 || o.WorkerPacketDigest != "" || o.WorkerJobBinding != nil || o.ControlID != "" || o.PreparationRunRef != "" {
			return oracleFailure(KindInvalidPayload, "prepare carries missing or execute-only fields", "supply contract version and control bundle only")
		}
	case "execute":
		if o.ExpectedContractVersion != 0 || o.ControlBundle != nil || o.AttemptID == "" || o.AttemptEpoch < 1 || !workflowDigest(o.WorkerPacketDigest) || o.WorkerJobBinding == nil || o.ControlID == "" || o.PreparationRunRef == "" {
			return oracleFailure(KindInvalidPayload, "execute carries missing or prepare-only fields", "supply the exact persisted attempt, packet, job, control, and preparation binding")
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
		return b, oracleFailure(KindUnauthorizedDispatch, "the authorized job has no oracle", "record and authorize an oracle-bound job")
	}
	count := 0
	for _, c := range oracle.Controls {
		if c.ControlID == id {
			b.Control = c
			count++
		}
	}
	if count != 1 {
		return b, oracleFailure(KindUnauthorizedDispatch, "the control does not resolve exactly once in the authorized job", "select that job's recorded control")
	}
	for _, owner := range oracle.Owners {
		if owner.OwnerID == b.Control.OwnerID {
			b.Owner = owner
		}
	}
	for _, c := range oracle.Cases {
		if slices.Contains(b.Control.CaseIDs, c.CaseID) {
			b.Cases = append(b.Cases, c)
		}
	}
	return b, nil
}

// SQL-only admission joins the immutable dispatch, preceding start, live
// projection, exact job event and core subject. It does not reuse the spawn
// gate's single-use-window condition: dispatched attempts remain eligible.
func readNativeOracleAuthorizationTx(ctx context.Context, tx queryer, req WorktreeVerifyRequest, observed oracleGitSubjectSnapshot, identity string) (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
	var b NativeOracleControlBundle
	var auth nativeOracleAuthorization
	var version int64
	o := req.Oracle
	refuse := func() (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
		return b, version, auth, oracleFailure(KindUnauthorizedDispatch, "native oracle execution lacks the exact live dispatch and candidate binding", "authorize a fresh exact job/packet/candidate before execution")
	}
	if !observed.clean || !worktreeSHAPattern.MatchString(observed.head) {
		return refuse()
	}
	core, err := readCurrentOracleSubject(ctx, tx, req.WorkID)
	if err != nil {
		return b, version, auth, err
	}
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT d.event_id,d.seq,d.payload FROM domain_events d JOIN worker_attempts a ON a.work_id=d.subject_id AND a.attempt_id=json_extract(d.payload,'$.worker_attempt_id')
 AND a.lane_id=json_extract(d.payload,'$.worker_lane_id') AND a.lane_version=json_extract(d.payload,'$.worker_lane_version') AND a.lane_digest=json_extract(d.payload,'$.worker_lane_digest') AND a.capability_class=json_extract(d.payload,'$.worker_capability_class')
 WHERE d.subject_type='work_item' AND d.subject_id=? AND d.kind=? AND json_extract(d.payload,'$.action_id')='dispatch_worker' AND json_extract(d.payload,'$.worker_attempt_id')=? AND a.lifecycle_state IN ('in_flight','dispatched') ORDER BY d.seq DESC LIMIT 1`, req.WorkID, WorkflowActionCompleted, o.AttemptID).Scan(&auth.EventID, &auth.Seq, &payload)
	if err == sql.ErrNoRows {
		return refuse()
	}
	if err != nil {
		return b, version, auth, err
	}
	var dispatch workflowActionCompletedPayload
	if err := json.Unmarshal(payload, &dispatch); err != nil {
		return refuse()
	}
	if dispatch.WorkerSubjectCommit == "" || dispatch.WorkerSubjectCommit != observed.head || dispatch.WorkerSubjectCommit != core || dispatch.AttemptEpoch != o.AttemptEpoch || dispatch.WorkerPacketDigest != o.WorkerPacketDigest || dispatch.WorkerWorktreeIdentity != identity || dispatch.WorkerJob == nil || *dispatch.WorkerJob != *o.WorkerJobBinding {
		return refuse()
	}
	var epoch int64
	err = tx.QueryRowContext(ctx, `SELECT seq,json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.step_id')=? AND seq<? ORDER BY seq DESC LIMIT 1`, req.WorkID, WorkflowActionStarted, dispatch.StepID, auth.Seq).Scan(&auth.StartSeq, &epoch)
	if err == sql.ErrNoRows {
		return refuse()
	}
	if err != nil {
		return b, version, auth, err
	}
	if epoch != o.AttemptEpoch {
		return refuse()
	}
	var jobRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq<? AND json_extract(payload,'$.job_id')=? AND json_extract(payload,'$.revision')=? AND json_extract(payload,'$.digest')=? ORDER BY seq DESC LIMIT 1`, req.WorkID, WorkerJobRecorded, auth.Seq, o.WorkerJobBinding.JobID, o.WorkerJobBinding.Revision, o.WorkerJobBinding.Digest).Scan(&jobRaw)
	if err == sql.ErrNoRows {
		return refuse()
	}
	if err != nil {
		return b, version, auth, err
	}
	var job WorkerJobRecordedPayload
	if err := json.Unmarshal(jobRaw, &job); err != nil {
		return refuse()
	}
	if DeriveWorkerJobDigest(job) != o.WorkerJobBinding.Digest {
		return refuse()
	}
	if err := requireWorkerJobRevisionReadyTx(ctx, tx, req.WorkID, *o.WorkerJobBinding); err != nil {
		return b, version, auth, err
	}
	b, err = nativeBundleForControl(job.AcceptanceOracle, o.ControlID)
	if err != nil {
		return b, version, auth, err
	}
	if !slices.Contains(b.Control.ReadinessEvidenceRefs, o.PreparationRunRef) {
		return refuse()
	}
	version = job.ContractVersion
	preparation, err := readOraclePreparationReceiptTx(ctx, tx, req.WorkID, req.ProjectID, o.PreparationRunRef)
	if err != nil {
		return b, version, auth, err
	}
	if preparation == nil || preparation.ContractVersion != version || preparation.SubjectCommit != observed.head || preparation.BundleDigest != nativeBundleDigest(b) {
		return refuse()
	}
	return b, version, auth, nil
}

func readOraclePreparationReceiptTx(ctx context.Context, q queryer, workID, projectID, runRef string) (*NativeOraclePreparation, error) {
	var raw, plan, digest string
	var stdoutLength, stderrLength sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT result_json,native_plan_json,native_plan_sha256,length(stdout_blob),length(stderr_blob) FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND state='released' AND outcome='completed' AND json_extract(result_json,'$.operation_ref')=? AND json_extract(result_json,'$.oracle.phase')='prepare' AND json_extract(result_json,'$.oracle.qualification')='ready'`, workID, projectID, runRef).Scan(&raw, &plan, &digest, &stdoutLength, &stderrLength)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r WorktreeVerifyResult
	var p nativeOraclePlan
	if json.Unmarshal([]byte(raw), &r) != nil || json.Unmarshal([]byte(plan), &p) != nil || nativeDigest([]byte(plan)) != digest || r.Oracle == nil || r.Oracle.NativePlanSHA256 != digest || !stdoutLength.Valid || !stderrLength.Valid || stdoutLength.Int64 != r.Oracle.Stdout.Length || stderrLength.Int64 != r.Oracle.Stderr.Length || !r.Oracle.StreamsComplete || r.Oracle.Protocol != "native_oracle_v2" || r.Oracle.RunRef != runRef || r.WorkID != workID || r.ProjectID != projectID || r.ExitCode != 0 || !nativePreparationComplete(r.Oracle, p) {
		return nil, oracleFailure(KindInvariantViolation, "preparation receipt differs from its immutable native plan", "reconcile the preparation lease")
	}
	return &r.Oracle.NativeOraclePreparation, nil
}

// nativePreparationComplete admits a ready preparation only when every
// recorded field is present and equals the immutable plan that produced it:
// a qualified subject, the pinned manifest, files, environment, selected
// tests, case map, and every planned stage with a zero exit.
func nativePreparationComplete(o *NativeOracleResult, p nativeOraclePlan) bool {
	names, err := nativeOracleSelector(p.Manifest, p.Bundle.Control)
	if err != nil || p.Request.Phase != "prepare" || o.Phase != "prepare" || o.Qualification != "ready" {
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
	if o.ToolchainIdentity == "" || o.ToolchainIdentity != p.Environment.ToolchainIdentity || !workflowDigest(o.BuildEnvironmentDigest) || o.BuildEnvironmentDigest != p.Environment.Digest {
		return false
	}
	if len(names) == 0 || !slices.Equal(o.SelectedTestNames, names) || o.SelectedDistinctCount != len(names) || len(o.CaseToTestMap) == 0 || !oracleEntryIdentical(o.CaseToTestMap, p.Manifest.Cases) {
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

func validateNativeJobPreparations(ctx context.Context, q queryer, work, project string, version int64, oracle *AcceptanceOracle) error {
	core, err := readCurrentOracleSubject(ctx, q, work)
	if err != nil {
		return err
	}
	if core == "" {
		return oracleFailure(KindMissingEvidence, "ready oracle job requires a qualified current core subject", `run worktree_verify with command ["git","rev-parse","--verify","HEAD^{commit}"], prepare that subject, then record the job`)
	}
	for _, control := range oracle.Controls {
		bundle, err := nativeBundleForControl(oracle, control.ControlID)
		if err != nil {
			return err
		}
		if len(control.ReadinessEvidenceRefs) == 0 {
			return oracleFailure(KindMissingEvidence, "job control has no qualified native preparation", "prepare its exact bundle")
		}
		for _, ref := range control.ReadinessEvidenceRefs {
			p, err := readOraclePreparationReceiptTx(ctx, q, work, project, ref)
			if err != nil {
				return err
			}
			if p == nil || p.ContractVersion != version || p.SubjectCommit != core || p.BundleDigest != nativeBundleDigest(bundle) {
				return oracleFailure(KindMissingEvidence, "job preparation is absent, foreign, or stale for its bundle and candidate", "prepare the exact current candidate and bundle before dispatch")
			}
		}
	}
	return nil
}

func readSelectedOraclePreparations(ctx context.Context, q queryer, work string) ([]NativeOraclePreparation, error) {
	jobs, err := readWorkerJobRevisions(ctx, q, work)
	if err != nil {
		return nil, err
	}
	var selected *WorkerJobRevisionView
	for i := range jobs {
		if jobs[i].Ready {
			if selected != nil {
				return nil, nil
			}
			selected = &jobs[i]
		}
	}
	if selected == nil || selected.AcceptanceOracle == nil {
		return nil, nil
	}
	var preparations []NativeOraclePreparation
	seen := map[string]bool{}
	for _, control := range selected.AcceptanceOracle.Controls {
		bundle, err := nativeBundleForControl(selected.AcceptanceOracle, control.ControlID)
		if err != nil {
			return nil, err
		}
		for _, ref := range control.ReadinessEvidenceRefs {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			p, err := readOraclePreparationReceiptTx(ctx, q, work, selected.ProjectScope, ref)
			if err != nil {
				return nil, err
			}
			if p == nil || p.ContractVersion != selected.ContractVersion || p.BundleDigest != nativeBundleDigest(bundle) {
				return nil, oracleFailure(KindMissingEvidence, "selected job has no exact native preparation", "prepare and record the exact bundle")
			}
			preparations = append(preparations, *p)
		}
	}
	raw, _ := json.Marshal(preparations)
	if len(raw) > 32*1024 {
		return nil, oracleFailure(KindLimitExceeded, "selected preparation metadata exceeds 32 KiB", "reduce the selected harness inventory")
	}
	return preparations, nil
}

func (s *Store) nativeOracleAdmission(ctx context.Context, req WorktreeVerifyRequest, entry WorktreeEntry, observed oracleGitSubjectSnapshot, identity string, expected *nativeOraclePlan, acquire bool) (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
	var b NativeOracleControlBundle
	var version int64
	var auth nativeOracleAuthorization
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return b, version, auth, err
	}
	defer tx.Rollback()
	live, err := activeWorktreeEntryForProject(ctx, tx, "worktree_verify", req.WorkID, req.ProjectID)
	if err != nil {
		return b, version, auth, err
	}
	if live.Path != entry.Path || live.ClaimOpID != entry.ClaimOpID || live.Branch != entry.Branch {
		return b, version, auth, oracleFailure(KindProjectionConflict, "the active claim changed", "refresh the claimed worktree")
	}
	if err := requireNoOutsideRepairTx(ctx, tx, req.WorkID, "worktree_verify"); err != nil {
		return b, version, auth, err
	}
	if req.Oracle.Phase == "execute" {
		b, version, auth, err = readNativeOracleAuthorizationTx(ctx, tx, req, observed, identity)
	} else {
		version, err = activeWorkflowContractVersion(ctx, tx, req.WorkID, "worktree_verify")
		if err == nil && version != req.Oracle.ExpectedContractVersion {
			err = oracleFailure(KindVersionConflict, "preparation contract version is not active", "prepare against the active approved contract")
		}
		core, coreErr := readCurrentOracleSubject(ctx, tx, req.WorkID)
		if err == nil {
			err = coreErr
		}
		if err == nil && (!observed.clean || observed.head != core || core == "") {
			err = oracleFailure(KindMissingEvidence, "preparation requires clean HEAD equal to the current core subject", `run worktree_verify with command ["git","rev-parse","--verify","HEAD^{commit}"] and a fresh request identity, then refresh context`)
		}
		b = *req.Oracle.ControlBundle
		if err == nil {
			err = validateNativeBundleAuthorityTx(ctx, tx, req.WorkID, version, b)
		}
	}
	if err != nil {
		return b, version, auth, err
	}
	if expected != nil && (expected.SubjectCommit != observed.head || expected.ContractVersion != version || expected.BundleDigest != nativeBundleDigest(b) || expected.Authorization != auth) {
		return b, version, auth, oracleFailure(KindUnauthorizedDispatch, "native plan applicability changed", "request a new explicitly authorized run")
	}
	if acquire {
		if err := acquireVerifyLeaseTx(ctx, tx, req, entry, req.nativeCommandJSON, req.Now); err != nil {
			return b, version, auth, err
		}
		if err := tx.Commit(); err != nil {
			return b, version, auth, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the native verify lease", false, "reconcile_operation", err), nativeLeaseRef(req.LeaseID))
		}
		return b, version, auth, nil
	}
	err = tx.Commit()
	return b, version, auth, err
}

func validateNativeBundleAuthorityTx(ctx context.Context, tx *sql.Tx, workID string, version int64, b NativeOracleControlBundle) error {
	if len(b.Control.ReadinessEvidenceRefs) != 0 {
		return oracleFailure(KindInvalidPayload, "native prepare input must omit readiness references", "prepare before recording the ready job")
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
		return oracleFailure(KindInvalidPayload, "preparation cases differ from its control", "supply exactly the control's cases")
	}
	seen := map[string]bool{}
	for _, c := range b.Cases {
		if seen[c.CaseID] || c.OwnerID != b.Owner.OwnerID || !slices.Contains(b.Control.CaseIDs, c.CaseID) || !slices.Contains(c.ControlIDs, b.Control.ControlID) {
			return oracleFailure(KindInvalidPayload, "preparation bundle is not reciprocal", "supply the exact owner/control/case mapping")
		}
		seen[c.CaseID] = true
	}
	if _, err := validateOracleCases(b.Cases, map[string]OracleOwner{b.Owner.OwnerID: b.Owner}); err != nil {
		return err
	}
	oracle := &AcceptanceOracle{Owners: []OracleOwner{b.Owner}, Cases: b.Cases, Controls: []OracleControl{b.Control}}
	if err := validateOracleCombinedBound(oracle); err != nil {
		return err
	}
	return validateAcceptanceOracleAuthorityTx(ctx, tx, workID, version, oracle)
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
		return WorktreeVerifyResult{}, true, oracleFailure(KindInvariantViolation, "native plan digest or bytes are corrupt or historical", "use a new authorized native request")
	}
	var expectedCommand []string
	if pinned.Request.Phase == "execute" {
		expectedCommand = pinned.Bundle.Control.Argv
	} else {
		for _, stage := range pinned.Stages {
			if stage.Name == "compile" {
				expectedCommand = stage.Argv
			}
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
		return WorktreeVerifyResult{}, true, oracleFailure(KindWorktreeLeaseHeld, "native run remains held and cannot be executed twice", "reconcile the held lease before requesting another run")
	}
	if outcome == "aborted" {
		return WorktreeVerifyResult{}, true, oracleFailure(KindInvalidOperation, "abandoned native run is spent and has no replayable outcome", "request a new authorized run")
	}
	var r WorktreeVerifyResult
	if json.Unmarshal([]byte(raw), &r) != nil || r.Oracle == nil || r.Oracle.NativePlanSHA256 != digest || r.WorkID != work || r.ProjectID != project || r.LeaseID != req.LeaseID || r.OperationRef != worktreeVerifyOperationRef(req.LeaseID) || !slices.Equal(r.Command, expectedCommand) {
		return r, true, oracleFailure(KindInvariantViolation, "native replay metadata is corrupt", "reconcile the retained native run")
	}
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
	identity := workerWorktreeIdentity(canonical)
	before, err := snapshotOracleGitSubject(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	bundle, version, auth, err := s.nativeOracleAdmission(ctx, req, entry, before, identity, nil, false)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	plan, err := normalizeNativeOraclePlan(ctx, runner, entry, req, bundle, version, before.head, identity, auth)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if req.Oracle.Phase == "execute" {
		prep, err := readOraclePreparationReceiptTx(ctx, s.db, req.WorkID, req.ProjectID, req.Oracle.PreparationRunRef)
		if err != nil {
			return WorktreeVerifyResult{}, err
		}
		if prep == nil || prep.BuildEnvironmentDigest != plan.Environment.Digest || prep.ToolchainIdentity != plan.Environment.ToolchainIdentity || prep.ManifestDigest != plan.ManifestFile.SHA256 || !oracleEntryIdentical(prep.Files, plan.Files) {
			return WorktreeVerifyResult{}, oracleFailure(KindUnauthorizedDispatch, "preparation environment or pinned inputs differ from execution", "request explicit preparation for this environment and bundle")
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
		for _, stage := range plan.Stages {
			if stage.Name == "compile" {
				req.Command = stage.Argv
			}
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
	// Plan normalization ran Git and toolchain subprocesses after the first
	// snapshot. Re-read the external subject immediately before the SQL-only
	// acquisition so the lease pins the pinned, current, and recorded subject.
	fresh, err := snapshotOracleGitSubject(ctx, runner, entry.Path)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if fresh != before {
		return WorktreeVerifyResult{}, oracleFailure(KindProjectionConflict, "candidate subject changed while the native plan was prepared", "request a new authorized run against the current candidate")
	}
	_, _, _, err = s.nativeOracleAdmission(ctx, req, entry, fresh, identity, &plan, true)
	if err != nil {
		var completed *worktreeVerifyCompleted
		if errors.As(err, &completed) {
			return completed.result, completed.failure
		}
		return WorktreeVerifyResult{}, err
	}
	leaseRef := nativeLeaseRef(req.LeaseID)
	released := false
	defer releaseAbandonedVerifyLease(s, ctx, req.LeaseID, &released)
	capture := &nativeStreamCapture{stdout: []byte{}, stderr: []byte{}, complete: true}
	result := nativeOracleInitialResult(req, entry, plan)
	err = s.runNativeGoOracle(ctx, runner, entry, req, plan, capture, result.Oracle)
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
	after, afterErr := snapshotOracleGitSubject(finalCtx, runner, entry.Path)
	if afterErr != nil {
		return result, annotateCommittedEffect(afterErr, leaseRef)
	}
	result.TrackedFilesChanged = before != after
	if result.TrackedFilesChanged || !capture.complete || ctx.Err() != nil {
		result.Oracle.Qualification = "unavailable"
	}
	// Final admission is read before serialization; release rechecks this
	// same tuple under its own SQL-only transaction before publication.
	_, _, _, admitErr := s.nativeOracleAdmission(finalCtx, req, entry, after, identity, &plan, false)
	if admitErr != nil {
		result.Oracle.Qualification = "unavailable"
		result.Oracle.Detail = admitErr.Error()
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
	result, err = s.releaseNativeOracle(finalCtx, req, entry, after, identity, plan, result, raw, unavailable, unavailableRaw, capture, ctx.Err() != nil)
	if err != nil {
		return result, annotateCommittedEffect(err, leaseRef)
	}
	released = true
	if effectErr != nil {
		return result, annotateCommittedEffect(effectErr, leaseRef)
	}
	if result.TrackedFilesChanged {
		return result, annotateCommittedEffect(oracleFailure(KindWorktreeVerifyMutated, "claimed worktree changed during native verification", "reconcile the candidate"), leaseRef)
	}
	return result, nil
}

func nativeLeaseRef(leaseID string) SubjectCurrentVersion {
	return SubjectCurrentVersion{SubjectType: "worktree_verify_lease", SubjectID: leaseID, Version: 1}
}

func nativeOracleInitialResult(req WorktreeVerifyRequest, entry WorktreeEntry, p nativeOraclePlan) WorktreeVerifyResult {
	names, _ := nativeOracleSelector(p.Manifest, p.Bundle.Control)
	r := WorktreeVerifyResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Path: entry.Path, Branch: entry.Branch, LeaseID: req.LeaseID, OperationRef: worktreeVerifyOperationRef(req.LeaseID), Command: req.Command}
	r.Oracle = &NativeOracleResult{NativeOraclePreparation: NativeOraclePreparation{Protocol: "native_oracle_v2", Phase: req.Oracle.Phase, Qualification: "unavailable", RunRef: r.OperationRef, WorkID: req.WorkID, ProjectID: req.ProjectID, ContractVersion: p.ContractVersion, SubjectCommit: p.SubjectCommit, BundleDigest: p.BundleDigest, LogicalArgvDigest: nativeDigest([]byte(workflowJSON(p.Bundle.Control.Argv))), LogicalCwd: p.Bundle.Control.Cwd, RecipeSource: p.Bundle.Control.RecipeSource, ManifestBlob: p.ManifestFile.BlobOID, ManifestDigest: p.ManifestFile.SHA256, Files: p.Files, ToolchainIdentity: p.Environment.ToolchainIdentity, BuildEnvironmentDigest: p.Environment.Digest, SelectedTestNames: names, CaseToTestMap: p.Manifest.Cases, SelectedDistinctCount: len(names), NativePlanSHA256: req.nativePlanSHA256}, ControlID: p.Bundle.Control.ControlID, AttemptID: req.Oracle.AttemptID, AttemptEpoch: req.Oracle.AttemptEpoch, WorkerPacketDigest: req.Oracle.WorkerPacketDigest, WorkerJobBinding: req.Oracle.WorkerJobBinding, PreparationRunRef: req.Oracle.PreparationRunRef, AuthorizationEventID: p.Authorization.EventID, AuthorizationSeq: p.Authorization.Seq, StartSeq: p.Authorization.StartSeq, Stages: []NativeOracleStage{}}
	return r
}

// The stamp binds the immutable event frontier and the mutable native
// dependencies read by authorization. SQL produces its deterministic metadata
// representation; neither BLOB bodies nor host observations enter it.
type nativeOracleReleaseStamp struct {
	Frontier     int64
	CoreReceipt  string
	Attempt      string
	Preparations string
}

func readNativeOracleReleaseStamp(ctx context.Context, q queryer, req WorktreeVerifyRequest) (nativeOracleReleaseStamp, error) {
	var stamp nativeOracleReleaseStamp
	job := WorkerJobBinding{}
	if req.Oracle.WorkerJobBinding != nil {
		job = *req.Oracle.WorkerJobBinding
	}
	err := q.QueryRowContext(ctx, `SELECT
		coalesce((SELECT max(seq) FROM domain_events WHERE subject_type='work_item' AND subject_id=?),0),
		coalesce((`+oracleCurrentVerifyReceiptSQL+`),''),
		coalesce((SELECT json_array(lane_id,lane_version,lane_digest,capability_class,lifecycle_state)
			FROM worker_attempts WHERE work_id=? AND attempt_id=?),''),
		(SELECT json_group_array(json_array(lease_id,state,outcome,result_json,native_plan_json,native_plan_sha256,stdout_length,stderr_length))
		 FROM (SELECT lease_id,state,outcome,result_json,native_plan_json,native_plan_sha256,length(stdout_blob) stdout_length,length(stderr_blob) stderr_length
			FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND json_extract(result_json,'$.operation_ref') IN
			(SELECT refs.value FROM domain_events j,json_each(j.payload,'$.acceptance_oracle.controls') controls,json_each(controls.value,'$.readiness_evidence_refs') refs
			 WHERE j.subject_type='work_item' AND j.subject_id=? AND j.kind=? AND json_extract(j.payload,'$.job_id')=? AND json_extract(j.payload,'$.revision')=? AND json_extract(j.payload,'$.digest')=?)
			ORDER BY lease_id))`, req.WorkID, req.WorkID, WorktreeSetID(req.WorkID), req.WorkID, req.Oracle.AttemptID,
		req.WorkID, req.ProjectID, req.WorkID, WorkerJobRecorded, job.JobID, job.Revision, job.Digest).
		Scan(&stamp.Frontier, &stamp.CoreReceipt, &stamp.Attempt, &stamp.Preparations)
	return stamp, err
}

func (s *Store) releaseNativeOracle(ctx context.Context, req WorktreeVerifyRequest, entry WorktreeEntry, after oracleGitSubjectSnapshot, identity string, p nativeOraclePlan, result WorktreeVerifyResult, raw []byte, unavailable WorktreeVerifyResult, unavailableRaw []byte, capture *nativeStreamCapture, aborted bool) (WorktreeVerifyResult, error) {
	// Bracket metadata validation with the same SQL stamp checked under the
	// release transaction. A changed dependency invalidates the proof instead
	// of repeating hashing or JSON finalization while holding the connection.
	stamp, err := readNativeOracleReleaseStamp(ctx, s.db, req)
	if err != nil {
		return result, err
	}
	valid := true
	if req.Oracle.Phase == "execute" {
		_, _, auth, authErr := readNativeOracleAuthorizationTx(ctx, s.db, req, after, identity)
		valid = authErr == nil && auth == p.Authorization
	} else {
		core, coreErr := readCurrentOracleSubject(ctx, s.db, req.WorkID)
		valid = coreErr == nil && core == p.SubjectCommit && after.clean && after.head == p.SubjectCommit
	}
	checked, err := readNativeOracleReleaseStamp(ctx, s.db, req)
	if err != nil {
		return result, err
	}
	valid = valid && checked == stamp
	tx, err := s.db.BeginTx(ctx, nil)
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
	if req.Oracle.Phase == "execute" && valid && current == stamp {
		if readyErr := requireWorkerJobRevisionStateReadyTx(ctx, tx, req.WorkID, *req.Oracle.WorkerJobBinding); readyErr != nil {
			valid = false
		}
	}
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
