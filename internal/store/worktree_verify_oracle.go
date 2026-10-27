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
	SelectedTestNames      []string                 `json:"selected_test_names,omitempty"`
	CaseToTestMap          map[string][]string      `json:"case_to_test_map,omitempty"`
	SelectedDistinctCount  int                      `json:"selected_distinct_count"`
	NativePlanSHA256       string                   `json:"native_plan_sha256"`
	Stdout                 NativeOracleStream       `json:"stdout"`
	Stderr                 NativeOracleStream       `json:"stderr"`
	StreamsComplete        bool                     `json:"streams_complete"`
	// Target-runner wire properties. Go preparations leave every field
	// empty; a target preparation leaves SelectedTestNames and
	// CaseToTestMap empty and never fabricates Go test names.
	RecipeKind           string              `json:"recipe_kind,omitempty"`
	TargetMaterializer   string              `json:"target_materializer,omitempty"`
	TargetRunner         string              `json:"target_runner,omitempty"`
	SelectedWitnessIDs   []string            `json:"selected_witness_ids,omitempty"`
	CaseToWitnessMap     map[string][]string `json:"case_to_witness_map,omitempty"`
	IsolationPolicy      string              `json:"isolation_policy,omitempty"`
	IsolationFingerprint string              `json:"isolation_fingerprint,omitempty"`
	IsolationEnforcement string              `json:"isolation_enforcement,omitempty"`
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
	// ObservedWitnessIDs is the target-runner analog of
	// ObservedTestNames: the witnesses the host actually observed passing.
	ObservedWitnessIDs []string `json:"observed_witness_ids,omitempty"`
	Detail             string   `json:"detail,omitempty"`
}

type nativeOracleAuthorization struct {
	EventID  string `json:"event_id"`
	Seq      int64  `json:"seq"`
	StartSeq int64  `json:"start_seq"`
}

// The plan holds logical slots, never an ephemeral host pathname. The Go
// family fills Manifest and Environment and leaves Target nil; a
// target_runner_v1 plan fills Target and leaves the Go fields zero. Stored
// historical plans are read strictly by family: a target plan never falls
// back to the Go reader, and an old Go plan never gains host fields.
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
	Authorization    nativeOracleAuthorization `json:"authorization"`
	Stages           []NativeOracleStage       `json:"stages"`
	StreamLimit      int                       `json:"stream_limit"`
	CachePolicy      string                    `json:"cache_policy"`
	ReviewRepair     *nativeReviewRepair       `json:"review_repair,omitempty"`
}

// nativePlanEnvironment returns the family-owned environment identity the
// preparation/execution join compares.
func nativePlanEnvironment(p nativeOraclePlan) (digest, identity string) {
	if p.Target != nil {
		return p.Target.Environment.Digest, p.Target.Environment.RuntimeIdentity
	}
	return p.Environment.Digest, p.Environment.ToolchainIdentity
}

// nativePlanPrepareCommand selects the canonical prepare lease command for
// the plan's family. Go preparations pin the compile stage argv; target
// preparations pin their canonical collection or check-input stage.
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

// normalizeNativeOraclePlan resolves one native request into the immutable
// producer plan for its recipe family. The recipe bytes decide the family:
// the closed envelope peek selects exactly one strict reader, and an
// unknown, malformed, or unsupported family refuses as unavailable — it
// never falls back to Go.
func normalizeNativeOraclePlan(ctx context.Context, r GitRunner, entry WorktreeEntry, req WorktreeVerifyRequest, b NativeOracleControlBundle, version int64, subject, identity string, auth nativeOracleAuthorization) (nativeOraclePlan, error) {
	p := nativeOraclePlan{Request: *req.Oracle, RequestID: req.RequestID, WorkID: req.WorkID, ProjectID: req.ProjectID, ContractVersion: version, SubjectCommit: subject, WorktreeIdentity: identity, Bundle: b, BundleDigest: nativeBundleDigest(b), Authorization: auth, StreamLimit: nativeOracleStreamLimit, CachePolicy: "private-disposable-per-lease"}
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

// readNativeOracleDispatchTx joins the immutable dispatch completion, its
// preceding start, and the exact recorded worker job to produce the plan's
// preparatory inputs: the control bundle, the job's contract version, and
// the authorization event and start ids. It validates only the request's
// binding to those immutable events, so a forged attempt, packet, job,
// control, or preparation reference refuses typed before any stage. Live
// claim, core subject, readiness, and receipt evaluation never run here.
func readNativeOracleDispatchTx(ctx context.Context, q queryer, req WorktreeVerifyRequest, observedHead, identity string) (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
	b, version, auth, err := readNativeOracleDispatchBundleTx(ctx, q, req, observedHead, identity)
	if err != nil && !oracleSubjectMoved(err) {
		return b, version, auth, err
	}
	dispatch, _, dispatchErr := readNativeOracleDispatchBindingTx(ctx, q, req.WorkID, req.Oracle.AttemptID)
	if dispatchErr != nil {
		return b, version, auth, dispatchErr
	}
	if ref, ok := nativeDispatchPreparationRef(dispatch, b, req.Oracle.PreparationRunRef); !ok || ref != req.Oracle.PreparationRunRef {
		return b, version, auth, oracleFailure(KindUnauthorizedDispatch, "native oracle execution lacks the exact live dispatch and candidate binding", "authorize a fresh exact job/packet/candidate before execution")
	}
	return b, version, auth, err
}

// readNativeOracleDispatchBundleTx resolves the immutable job/control join
// before preparation selection. The execute reader additionally requires its
// exact preparation reference; worker control resolution selects from this
// same bundle instead of interpreting ordinary job check strings.
func readNativeOracleDispatchBundleTx(ctx context.Context, q queryer, req WorktreeVerifyRequest, observedHead, identity string) (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
	var b NativeOracleControlBundle
	var auth nativeOracleAuthorization
	var version int64
	o := req.Oracle
	refuse := func() (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
		return b, version, auth, oracleFailure(KindUnauthorizedDispatch, "native oracle execution lacks the exact live dispatch and candidate binding", "authorize a fresh exact job/packet/candidate before execution")
	}
	if !worktreeSHAPattern.MatchString(observedHead) {
		return refuse()
	}
	dispatch, auth, err := readNativeOracleDispatchBindingTx(ctx, q, req.WorkID, o.AttemptID)
	if err != nil {
		return b, version, auth, err
	}
	if dispatch.WorkerSubjectCommit == "" || dispatch.AttemptEpoch != o.AttemptEpoch || dispatch.WorkerPacketDigest != o.WorkerPacketDigest || dispatch.WorkerWorktreeIdentity != identity || dispatch.WorkerJob == nil || *dispatch.WorkerJob != *o.WorkerJobBinding {
		return refuse()
	}
	var epoch int64
	err = q.QueryRowContext(ctx, `SELECT start_seq,json_extract(start_payload,'$.attempt_epoch') FROM worker_authorizations WHERE work_id=? AND attempt_id=? AND start_seq>0 AND start_seq<seq AND json_extract(start_payload,'$.action_id')='dispatch_worker' AND json_extract(start_payload,'$.step_id')=?`, req.WorkID, o.AttemptID, dispatch.StepID).Scan(&auth.StartSeq, &epoch)
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
	err = q.QueryRowContext(ctx, `SELECT recorded_payload FROM worker_job_revisions WHERE work_id=? AND recorded_seq>0 AND recorded_seq<? AND job_id=? AND revision=? AND digest=?`, req.WorkID, auth.Seq, o.WorkerJobBinding.JobID, o.WorkerJobBinding.Revision, o.WorkerJobBinding.Digest).Scan(&jobRaw)
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
	b, err = nativeBundleForControl(job.AcceptanceOracle, o.ControlID)
	if err != nil {
		return b, version, auth, err
	}
	if dispatch.WorkerSubjectCommit != observedHead && !workerLaneRepairs(dispatch.WorkerLaneID, dispatch.WorkerLaneDigest) {
		return b, version, auth, nativeOracleSubjectMoved(dispatch.WorkerSubjectCommit, observedHead)
	}
	return b, job.ContractVersion, auth, nil
}

// A non-repairing assignment keeps its exact input subject. A repairing pass
// has its own producer-observed ancestry and containment admission.
func nativeOracleSubjectMoved(subject, head string) error {
	return newRouteFailure(KindUnauthorizedDispatch, "worker_oracle",
		"native oracle execution requires this assignment's subject "+subject+", and HEAD is "+head+"; restore the assigned candidate or finish this attempt and authorize a scoped repairing Review on the clean candidate",
		false, "worktree_verify", "record_worker_job", "dispatch_worker")
}

func oracleSubjectMoved(err error) bool {
	var f *Failure
	return errors.As(err, &f) && f.Kind == KindUnauthorizedDispatch && f.RecoveryAction == RecoveryUseDeclaredRoute
}

// readNativeOracleDispatchBindingTx is the shared live-attempt join. A native
// Task executes while in_flight; terminal readback records worker.dispatched
// later. Execution never fabricates that terminal evidence or its lane actor.
func readNativeOracleDispatchBindingTx(ctx context.Context, q queryer, workID, attemptID string) (workflowActionCompletedPayload, nativeOracleAuthorization, error) {
	var dispatch workflowActionCompletedPayload
	var auth nativeOracleAuthorization
	var payload []byte
	err := q.QueryRowContext(ctx, `SELECT d.event_id,d.seq,d.payload FROM worker_authorizations d JOIN worker_attempts a ON a.work_id=d.work_id AND a.attempt_id=d.attempt_id
 AND a.lane_id=json_extract(d.payload,'$.worker_lane_id') AND a.lane_digest=json_extract(d.payload,'$.worker_lane_digest') AND a.capability_class=json_extract(d.payload,'$.worker_capability_class')
	 WHERE d.work_id=? AND d.attempt_id=? AND a.lifecycle_state IN ('in_flight','dispatched')`, workID, attemptID).Scan(&auth.EventID, &auth.Seq, &payload)
	if err == sql.ErrNoRows || err == nil && json.Unmarshal(payload, &dispatch) != nil {
		return dispatch, auth, oracleFailure(KindUnauthorizedDispatch, "native oracle execution lacks the exact live dispatch and candidate binding", "authorize a fresh exact job/packet/candidate before execution")
	}
	return dispatch, auth, err
}

// readNativeOracleAuthorizationTx is the exact execute authorization the
// acquiring admission evaluates once: the immutable dispatch join above plus
// the live validations it owns — clean HEAD, the current core subject, the
// job's readiness with its qualified preparations, and the preparation
// receipt binding.
func readNativeOracleAuthorizationTx(ctx context.Context, tx queryer, req WorktreeVerifyRequest, observed oracleGitSubjectSnapshot, identity string) (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
	var b NativeOracleControlBundle
	var auth nativeOracleAuthorization
	var version int64
	refuse := func() (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
		return b, version, auth, oracleFailure(KindUnauthorizedDispatch, "native oracle execution lacks the exact live dispatch and candidate binding", "authorize a fresh exact job/packet/candidate before execution")
	}
	if !observed.clean || !worktreeSHAPattern.MatchString(observed.head) {
		return refuse()
	}
	// The dispatch join runs first so a genuine attempt whose HEAD moved
	// names its recovery route before the core subject is compared.
	b, version, auth, err := readNativeOracleDispatchTx(ctx, tx, req, observed.head, identity)
	if err != nil {
		return b, version, auth, err
	}
	core, err := readCurrentOracleSubject(ctx, tx, req.WorkID)
	if err != nil {
		return b, version, auth, err
	}
	dispatch, _, err := readNativeOracleDispatchBindingTx(ctx, tx, req.WorkID, req.Oracle.AttemptID)
	if err != nil {
		return b, version, auth, err
	}
	repair := nativeReviewRepairFrom(ctx)
	preparationSubject := observed.head
	if repair != nil {
		if repair.Commit != observed.head || validateNativeReviewRepairTx(ctx, tx, req.WorkID, dispatch, repair) != nil {
			return refuse()
		}
		preparationSubject = repair.Base
	}
	if core == "" || (core != observed.head && (repair == nil || core != repair.Base)) {
		return refuse()
	}
	if repair == nil && dispatch.WorkerCapabilityClass == "review" && len(dispatch.WorkerOraclePreparations) != 0 {
		// The Review authorization froze its preparation (CD-0205 D1);
		// job admission is state-only and the receipt join below binds the subject.
		if err := requireWorkerReviewJobRevisionStateTx(ctx, tx, req.WorkID, *req.Oracle.WorkerJobBinding); err != nil {
			return b, version, auth, err
		}
	} else if repair == nil {
		if err := requireWorkerJobRevisionReadyTx(ctx, tx, req.WorkID, *req.Oracle.WorkerJobBinding); err != nil {
			return b, version, auth, err
		}
	} else if err := requireWorkerJobRevisionStateReadyTx(ctx, tx, req.WorkID, *req.Oracle.WorkerJobBinding); err != nil {
		return b, version, auth, err
	}
	preparation, err := readOraclePreparationReceiptTx(ctx, tx, req.WorkID, req.ProjectID, req.Oracle.PreparationRunRef)
	if err != nil {
		return b, version, auth, err
	}
	if preparation == nil || preparation.ContractVersion != version || preparation.SubjectCommit != preparationSubject || preparation.BundleDigest != nativeBundleDigest(b) {
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
// witnesses, case map, and every planned stage with a zero exit. The family
// readers are strict: a target preparation never satisfies the Go reader
// and an old Go preparation never satisfies the target reader.
func nativePreparationComplete(o *NativeOracleResult, p nativeOraclePlan) bool {
	if !nativePreparationJoinsValid(o, p) {
		return false
	}
	if p.Target != nil {
		return nativeTargetPreparationWitnessesComplete(o, p)
	}
	names, err := nativeOracleSelector(p.Manifest, p.Bundle.Control)
	if err != nil {
		return false
	}
	if len(names) == 0 || !slices.Equal(o.SelectedTestNames, names) || o.SelectedDistinctCount != len(names) || len(o.CaseToTestMap) == 0 || !oracleEntryIdentical(o.CaseToTestMap, p.Manifest.Cases) {
		return false
	}
	if o.RecipeKind != "" || o.TargetMaterializer != "" || o.TargetRunner != "" || len(o.SelectedWitnessIDs) != 0 || len(o.CaseToWitnessMap) != 0 {
		return false
	}
	return true
}

// nativePreparationJoinsValid checks the shared family-independent joins of
// a retained preparation: phases, subject, contract, bundle, pinned files,
// and environment identity.
func nativePreparationJoinsValid(o *NativeOracleResult, p nativeOraclePlan) bool {
	if !nativeIsolationResultMatches(p, &o.NativeOraclePreparation, o.Stages) {
		return false
	}
	if p.Request.Phase != "prepare" || o.Phase != "prepare" || o.Qualification != "ready" {
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

func validateNativeJobPreparations(ctx context.Context, q queryer, work string, version int64, oracle *AcceptanceOracle) error {
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
			p, err := readOraclePreparationReceiptTx(ctx, q, work, control.RecipeSource.ProjectID, ref)
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
	selected, err := selectedPreparationJobTx(ctx, q, work)
	if err != nil || selected == nil || selected.AcceptanceOracle == nil {
		return nil, err
	}
	var preparations []NativeOraclePreparation
	seen := map[string]bool{}
	for _, control := range selected.AcceptanceOracle.Controls {
		bundle, err := nativeBundleForControl(selected.AcceptanceOracle, control.ControlID)
		if err != nil {
			return nil, err
		}
		if preparations, err = appendControlPreparationsTx(ctx, q, work, selected.ContractVersion, bundle, seen, preparations); err != nil {
			return nil, err
		}
	}
	raw, _ := json.Marshal(preparations)
	if len(raw) > 32*1024 {
		return nil, oracleFailure(KindLimitExceeded, "selected preparation metadata exceeds 32 KiB", "reduce the selected harness inventory")
	}
	return preparations, nil
}

// selectedPreparationJobTx selects the job whose preparations the context
// carries: the sole ready revision, or with none ready the final-Review
// revision (CD-0205 D2). Several ready revisions select none; dispatch
// admission still checks the bound job's preparations.
func selectedPreparationJobTx(ctx context.Context, q queryer, work string) (*WorkerJobRevisionView, error) {
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
	if selected != nil {
		return selected, nil
	}
	final, err := finalReviewWorkerJobTx(ctx, q, work)
	if err != nil || final == nil {
		return nil, err
	}
	for i := range jobs {
		if jobs[i].Binding == *final {
			return &jobs[i], nil
		}
	}
	return nil, nil
}

// appendControlPreparationsTx appends the control's recorded preparations,
// each exact for its bundle and contract, then the latest qualified
// preparation of the unchanged bundle on the current candidate, which a
// Review dispatch may bind and freeze (CD-0205 D1).
func appendControlPreparationsTx(ctx context.Context, q queryer, work string, version int64, bundle NativeOracleControlBundle, seen map[string]bool, preparations []NativeOraclePreparation) ([]NativeOraclePreparation, error) {
	project := bundle.Control.RecipeSource.ProjectID
	for _, ref := range bundle.Control.ReadinessEvidenceRefs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		p, err := readOraclePreparationReceiptTx(ctx, q, work, project, ref)
		if err != nil {
			return nil, err
		}
		if p == nil || p.ContractVersion != version || p.BundleDigest != nativeBundleDigest(bundle) {
			return nil, oracleFailure(KindMissingEvidence, "selected job has no exact native preparation", "prepare and record the exact bundle")
		}
		preparations = append(preparations, *p)
	}
	p, err := latestNativeBundlePreparationTx(ctx, q, work, project, version, bundle)
	if err != nil {
		return nil, err
	}
	if p != nil && !seen[p.RunRef] {
		seen[p.RunRef] = true
		preparations = append(preparations, *p)
	}
	return preparations, nil
}

// readNativeOraclePlanInputs is the read-only preparatory read that pins the
// immutable plan inputs: for execute, the dispatch join's control bundle,
// contract version, and authorization event and start ids; for prepare, the
// active contract version and the request's own bundle. It performs no live
// claim, core, readiness, or receipt evaluation and no lease effect — the
// acquiring admission re-derives the same inputs and enforces every exact
// validation once, before any stage can run. Its source order still refuses
// a forged execute request typed before normalization begins.
func readNativeOraclePlanInputs(ctx context.Context, q queryer, req WorktreeVerifyRequest, observedHead, identity string) (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
	if req.Oracle.Phase == "execute" {
		return readNativeOracleDispatchTx(ctx, q, req, observedHead, identity)
	}
	version, err := activeWorkflowContractVersion(ctx, q, req.WorkID, "worktree_verify")
	if err != nil {
		return NativeOracleControlBundle{}, version, nativeOracleAuthorization{}, err
	}
	if version != req.Oracle.ExpectedContractVersion {
		return NativeOracleControlBundle{}, version, nativeOracleAuthorization{}, oracleFailure(KindVersionConflict, "preparation contract version is not active", "prepare against the active approved contract")
	}
	return *req.Oracle.ControlBundle, version, nativeOracleAuthorization{}, nil
}

// nativeOracleAdmission is the permission gate, evaluated exactly once at
// lease acquire and before any stage: live claim freshness, no outside
// repair, the exact authorization (execute: clean HEAD, core subject,
// dispatch join, job readiness, preparation receipt; prepare: active
// version, core subject, bundle authority), and plan applicability, then the
// lease effect in one transaction. Publication validity stays with the
// release path's SQL-stamped checks.
func (s *Store) nativeOracleAdmission(ctx context.Context, req WorktreeVerifyRequest, entry WorktreeEntry, observed oracleGitSubjectSnapshot, identity string, expected *nativeOraclePlan) (NativeOracleControlBundle, int64, nativeOracleAuthorization, error) {
	var b NativeOracleControlBundle
	var version int64
	var auth nativeOracleAuthorization
	tx, err := beginOrdinaryTx(ctx, s.db)
	if err != nil {
		return b, version, auth, err
	}
	defer tx.Rollback()
	live, err := worktreeVerifyEntry(ctx, tx, req)
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
	if err := authorizeNativeExecutionTx(ctx, tx, req, s.Clock, s.Path()); err != nil {
		return b, version, auth, err
	}
	if err := acquireVerifyLeaseTx(ctx, tx, req, entry, req.nativeCommandJSON, req.Now); err != nil {
		return b, version, auth, err
	}
	if err := tx.Commit(); err != nil {
		return b, version, auth, annotateCommittedEffect(wrapFailure(KindUnavailable, "worktree_verify", "cannot commit the native verify lease", false, "reconcile_operation", err), nativeLeaseRef(req.LeaseID))
	}
	return b, version, auth, nil
}

func authorizeNativeExecutionTx(ctx context.Context, tx *sql.Tx, req WorktreeVerifyRequest, clock func() time.Time, path string) error {
	if req.NativeAuthorization == nil {
		return nil
	}
	transaction := &Transaction{tx: tx, clock: clock, path: path}
	defer func() { transaction.tx = nil }()
	return req.NativeAuthorization(ctx, transaction)
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
		return WorktreeVerifyResult{}, true, oracleFailure(KindWorktreeLeaseHeld, "native run remains held and cannot be executed twice", "reconcile the held lease before requesting another run")
	}
	if outcome == "aborted" {
		return WorktreeVerifyResult{}, true, oracleFailure(KindInvalidOperation, "abandoned native run is spent and has no replayable outcome", "request a new authorized run")
	}
	var r WorktreeVerifyResult
	if json.Unmarshal([]byte(raw), &r) != nil || r.Oracle == nil || r.Oracle.NativePlanSHA256 != digest || r.WorkID != work || r.ProjectID != project || r.LeaseID != req.LeaseID || r.OperationRef != worktreeVerifyOperationRef(req.LeaseID) || !slices.Equal(r.Command, expectedCommand) {
		return r, true, oracleFailure(KindInvariantViolation, "native replay metadata is corrupt", "reconcile the retained native run")
	}
	if req.NativeAuthorization != nil {
		if err := s.Transact(ctx, func(tx *Transaction) error {
			return req.NativeAuthorization(ctx, tx)
		}); err != nil {
			return WorktreeVerifyResult{}, true, err
		}
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
	entry, err := worktreeVerifyEntry(ctx, s.db, req)
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
	bundle, version, auth, err := readNativeOraclePlanInputs(ctx, s.db, req, before.head, identity)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	repair, err := s.prepareNativeReviewRepair(ctx, runner, req, entry, before)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	if repair != nil {
		ctx = context.WithValue(ctx, nativeReviewRepairKey{}, repair)
	}
	plan, err := normalizeNativeOraclePlan(ctx, runner, entry, req, bundle, version, before.head, identity, auth)
	if err != nil {
		return WorktreeVerifyResult{}, err
	}
	plan.ReviewRepair = repair
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
	_, _, _, err = s.nativeOracleAdmission(ctx, req, entry, fresh, identity, &plan)
	if err != nil {
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
	return s.executeNativeOracleLease(ctx, runner, entry, req, plan, before, identity, capture, result, &released)
}

func validateNativeExecutionPreparation(ctx context.Context, q queryer, req WorktreeVerifyRequest, plan nativeOraclePlan) error {
	prep, err := readOraclePreparationReceiptTx(ctx, q, req.WorkID, req.ProjectID, req.Oracle.PreparationRunRef)
	if err != nil {
		return err
	}
	digest, identity := nativePlanEnvironment(plan)
	if prep == nil || !nativeIsolationPreparationMatches(plan, prep) || prep.BuildEnvironmentDigest != digest || prep.ToolchainIdentity != identity || prep.ManifestDigest != plan.ManifestFile.SHA256 || !oracleEntryIdentical(prep.Files, plan.Files) {
		return oracleFailure(KindUnauthorizedDispatch, "preparation environment or pinned inputs differ from execution", "request explicit preparation for this environment and bundle")
	}
	return nil
}

func (s *Store) prepareNativeReviewRepair(ctx context.Context, runner GitRunner, req WorktreeVerifyRequest, entry WorktreeEntry, before oracleGitSubjectSnapshot) (*nativeReviewRepair, error) {
	if req.Oracle.Phase != "execute" {
		return nil, nil
	}
	dispatch, _, err := readNativeOracleDispatchBindingTx(ctx, s.db, req.WorkID, req.Oracle.AttemptID)
	if err != nil {
		return nil, err
	}
	// The SQL sample is closed before Git; no queryer enters the probe.
	repair, err := observeNativeReviewRepair(ctx, runner, dispatch, entry, before)
	if err != nil {
		return nil, err
	}
	if repair != nil {
		if err := validateNativeReviewRepairTx(ctx, s.db, req.WorkID, dispatch, repair); err != nil {
			return nil, err
		}
	}
	return repair, nil
}

func (s *Store) executeNativeOracleLease(ctx context.Context, runner GitRunner, entry WorktreeEntry, req WorktreeVerifyRequest, plan nativeOraclePlan, before oracleGitSubjectSnapshot, identity string, capture *nativeStreamCapture, result WorktreeVerifyResult, released *bool) (WorktreeVerifyResult, error) {
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
	after, afterErr := snapshotOracleGitSubject(finalCtx, runner, entry.Path)
	if afterErr != nil {
		return result, annotateCommittedEffect(afterErr, leaseRef)
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
	result, err = s.releaseNativeOracle(finalCtx, req, entry, after, identity, plan, result, raw, unavailable, unavailableRaw, capture, aborted)
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

func nativeLeaseRef(leaseID string) SubjectCurrentVersion {
	return SubjectCurrentVersion{SubjectType: "worktree_verify_lease", SubjectID: leaseID, Version: 1}
}

func nativeOracleInitialResult(req WorktreeVerifyRequest, entry WorktreeEntry, p nativeOraclePlan) WorktreeVerifyResult {
	r := WorktreeVerifyResult{WorkID: req.WorkID, ProjectID: req.ProjectID, Path: entry.Path, Branch: entry.Branch, LeaseID: req.LeaseID, OperationRef: worktreeVerifyOperationRef(req.LeaseID), Command: req.Command}
	base := NativeOraclePreparation{Protocol: "native_oracle_v2", Phase: req.Oracle.Phase, Qualification: "unavailable", RunRef: r.OperationRef, WorkID: req.WorkID, ProjectID: req.ProjectID, ContractVersion: p.ContractVersion, SubjectCommit: p.SubjectCommit, BundleDigest: p.BundleDigest, LogicalArgvDigest: nativeDigest([]byte(workflowJSON(p.Bundle.Control.Argv))), LogicalCwd: p.Bundle.Control.Cwd, RecipeSource: p.Bundle.Control.RecipeSource, ManifestBlob: p.ManifestFile.BlobOID, ManifestDigest: p.ManifestFile.SHA256, Files: p.Files, NativePlanSHA256: req.nativePlanSHA256}
	digest, identity := nativePlanEnvironment(p)
	base.ToolchainIdentity = identity
	base.BuildEnvironmentDigest = digest
	if p.Target != nil {
		names, _ := nativeTargetCaseWitnesses(p.Target.Recipe, p.Bundle.Control)
		base.RecipeKind = nativeTargetKind
		base.TargetMaterializer = p.Target.Environment.Materializer
		base.TargetRunner = p.Target.Environment.Runner
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
	r.Oracle = &NativeOracleResult{NativeOraclePreparation: base, ControlID: p.Bundle.Control.ControlID, AttemptID: req.Oracle.AttemptID, AttemptEpoch: req.Oracle.AttemptEpoch, WorkerPacketDigest: req.Oracle.WorkerPacketDigest, WorkerJobBinding: req.Oracle.WorkerJobBinding, PreparationRunRef: req.Oracle.PreparationRunRef, AuthorizationEventID: p.Authorization.EventID, AuthorizationSeq: p.Authorization.Seq, StartSeq: p.Authorization.StartSeq, Stages: []NativeOracleStage{}}
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
		coalesce((SELECT effect_seq FROM work_items WHERE id=?),0),
		coalesce((`+oracleCurrentVerifyReceiptSQL+`),''),
		coalesce((SELECT json_array(lane_id,lane_digest,capability_class,lifecycle_state)
			FROM worker_attempts WHERE work_id=? AND attempt_id=?),''),
		(SELECT json_group_array(json_array(lease_id,state,outcome,result_json,native_plan_json,native_plan_sha256,stdout_length,stderr_length))
		 FROM (SELECT lease_id,state,outcome,result_json,native_plan_json,native_plan_sha256,length(stdout_blob) stdout_length,length(stderr_blob) stderr_length
			FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND json_extract(result_json,'$.operation_ref') IN
			(SELECT refs.value FROM worker_job_revisions j,json_each(j.recorded_payload,'$.acceptance_oracle.controls') controls,json_each(controls.value,'$.readiness_evidence_refs') refs
			 WHERE j.work_id=? AND j.job_id=? AND j.revision=? AND j.digest=? AND j.recorded_payload IS NOT NULL)
			ORDER BY lease_id))`, req.WorkID, req.WorkID, WorktreeSetID(req.WorkID), req.WorkID, req.Oracle.AttemptID,
		req.WorkID, req.ProjectID, req.WorkID, job.JobID, job.Revision, job.Digest).
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
	var valid bool
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
	tx, err := beginOrdinaryTx(ctx, s.db)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	live, scopeErr := worktreeVerifyEntry(ctx, tx, req)
	current, err := readNativeOracleReleaseStamp(ctx, tx, req)
	if err != nil {
		return result, err
	}
	version, versionErr := activeWorkflowContractVersion(ctx, tx, req.WorkID, "worktree_verify")
	outsideErr := requireNoOutsideRepairTx(ctx, tx, req.WorkID, "worktree_verify")
	if req.Oracle.Phase == "execute" && valid && current == stamp {
		dispatch, _, dispatchErr := readNativeOracleDispatchBindingTx(ctx, tx, req.WorkID, req.Oracle.AttemptID)
		if dispatchErr != nil || nativeDispatchJobStateTx(ctx, tx, req.WorkID, dispatch, *req.Oracle.WorkerJobBinding) != nil {
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
