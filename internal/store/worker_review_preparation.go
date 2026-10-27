package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

// WorkerOraclePreparationBinding freezes one control's native preparation in
// the dispatch authorization, without changing the recorded job or packet.
type WorkerOraclePreparationBinding struct {
	ControlID         string `json:"control_id"`
	PreparationRunRef string `json:"preparation_run_ref"`
}

type nativeReviewDispatchSample struct {
	binding WorkerJobBinding
	job     WorkerJobRecordedPayload
}

// nativeReviewPlanner resolves one control's producer plan on the observed
// runtime; dispatch uses normalizeNativeOraclePlan.
type nativeReviewPlanner func(ctx context.Context, r GitRunner, entry WorktreeEntry, req WorktreeVerifyRequest, b NativeOracleControlBundle, version int64, subject, identity string, auth nativeOracleAuthorization) (nativeOraclePlan, error)

type nativeReviewDispatchProof struct {
	binding WorkerJobBinding
	plans   []nativeOraclePlan
}

// sampleNativeReviewDispatchTx samples only the selected Review job. Retried
// dispatches derive that selection from the rejected attempt's authorization.
func sampleNativeReviewDispatchTx(ctx context.Context, q queryer, request PrepareWorkerDispatchRequest) (*nativeReviewDispatchSample, error) {
	var binding *WorkerJobBinding
	review := false
	if len(request.DispatchPayload) != 0 {
		var payload struct {
			WorkerPacket struct {
				LaneID     string `json:"lane_id"`
				LaneDigest string `json:"lane_digest"`
			} `json:"worker_packet"`
		}
		if json.Unmarshal(request.DispatchPayload, &payload) == nil {
			lane, err := LookupLane(payload.WorkerPacket.LaneID, payload.WorkerPacket.LaneDigest)
			review = err == nil && lane.CapabilityClass == "review"
			binding = WorkerJobBindingFromDispatchPayload(request.DispatchPayload)
		}
	} else if request.WorkerAttemptID != "" {
		var raw []byte
		err := q.QueryRowContext(ctx, `SELECT payload FROM worker_authorizations WHERE work_id=? AND attempt_id=?`, request.WorkID, request.WorkerAttemptID).Scan(&raw)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		var d workflowActionCompletedPayload
		if err == nil && json.Unmarshal(raw, &d) == nil {
			review, binding = d.WorkerCapabilityClass == "review", d.WorkerJob
		}
	}
	if !review || binding == nil {
		return nil, nil
	}
	// Authoritative job admission supplies the refusal for any other binding.
	if requireWorkerReviewJobRevisionStateTx(ctx, q, request.WorkID, *binding) != nil {
		return nil, nil
	}
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT recorded_payload FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=? AND recorded_seq>0`, request.WorkID, binding.JobID, binding.Revision, binding.Digest).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil // Authoritative job admission supplies the refusal.
	}
	if err != nil {
		return nil, err
	}
	sample := &nativeReviewDispatchSample{binding: *binding}
	if json.Unmarshal(raw, &sample.job) != nil || DeriveWorkerJobDigest(sample.job) != binding.Digest {
		return nil, oracleFailure(KindInvariantViolation, "Review job differs from its recorded digest", "reconcile the recorded job")
	}
	if sample.job.AcceptanceOracle == nil {
		return nil, nil
	}
	return sample, nil
}

// This observes the same family-owned runtime and pinned inputs as execute,
// after the freshness read snapshot closes. Normalization runs no test stages
// and creates no preparation receipt. Its output is sealed, never caller JSON.
// A terminal acceptance or an unclean observation observes nothing.
func observeNativeReviewDispatch(ctx context.Context, runner GitRunner, planner nativeReviewPlanner, entry WorktreeEntry, work string, dispatch *workerDispatchProof, sample *nativeReviewDispatchSample) (*nativeReviewDispatchProof, error) {
	if sample == nil || dispatch.acceptance || dispatch.observation.State != "clean" {
		return nil, nil
	}
	if planner == nil {
		planner = normalizeNativeOraclePlan
	}
	subject, identity := dispatch.observation.Commit, dispatch.sessionIdentity
	proof := &nativeReviewDispatchProof{binding: sample.binding}
	if len(sample.job.AcceptanceOracle.Controls) == 0 || len(sample.job.AcceptanceOracle.Controls) > OracleControlsMax {
		return nil, missingReviewPreparation()
	}
	for _, control := range sample.job.AcceptanceOracle.Controls {
		bundle, err := nativeBundleForControl(sample.job.AcceptanceOracle, control.ControlID)
		if err != nil {
			return nil, err
		}
		req := WorktreeVerifyRequest{WorkID: work, ProjectID: entry.ProjectID, Oracle: &NativeOracleRequest{Phase: "prepare"}}
		plan, err := planner(ctx, runner, entry, req, bundle, sample.job.ContractVersion, subject, identity, nativeOracleAuthorization{})
		if err != nil {
			return nil, err
		}
		proof.plans = append(proof.plans, plan)
	}
	return proof, nil
}

func missingReviewPreparation() error {
	return oracleFailure(KindMissingEvidence, "Review requires a qualified native preparation for the exact current candidate, bundle, contract, Project, and runtime environment", "explicitly qualify the candidate and prepare the unchanged control bundle through worktree_verify, then dispatch Review again")
}

func reviewPreparationMatches(p *NativeOraclePreparation, plan nativeOraclePlan) bool {
	digest, identity := nativePlanEnvironment(plan)
	return p != nil && p.WorkID == plan.WorkID && p.ProjectID == plan.ProjectID && p.SubjectCommit == plan.SubjectCommit && p.ContractVersion == plan.ContractVersion && p.BundleDigest == nativeBundleDigest(plan.Bundle) && p.BuildEnvironmentDigest == digest && p.ToolchainIdentity == identity && p.ManifestBlob == plan.ManifestFile.BlobOID && p.ManifestDigest == plan.ManifestFile.SHA256 && oracleEntryIdentical(p.Files, plan.Files)
}

// Recorded refs have priority in their declared order when they qualify on
// the observed runtime. Only otherwise do we select the latest qualified
// same-bundle preparation. Rowid orders completed
// receipts with equal release timestamps deterministically, never by ref text.
func selectNativeReviewPreparationTx(ctx context.Context, q queryer, plan nativeOraclePlan) (string, error) {
	for _, ref := range plan.Bundle.Control.ReadinessEvidenceRefs {
		p, err := readOraclePreparationReceiptTx(ctx, q, plan.WorkID, plan.ProjectID, ref)
		if err != nil {
			return "", err
		}
		if reviewPreparationMatches(p, plan) {
			return ref, nil
		}
	}
	digest, identity := nativePlanEnvironment(plan)
	rows, err := q.QueryContext(ctx, `SELECT json_extract(result_json,'$.operation_ref') FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND state='released' AND outcome='completed' AND json_extract(result_json,'$.oracle.phase')='prepare' AND json_extract(result_json,'$.oracle.qualification')='ready' AND json_extract(result_json,'$.oracle.subject_commit')=? AND json_extract(result_json,'$.oracle.contract_version')=? AND json_extract(result_json,'$.oracle.bundle_digest')=? AND json_extract(result_json,'$.oracle.build_environment_digest')=? AND json_extract(result_json,'$.oracle.toolchain_identity')=? ORDER BY released_at DESC,rowid DESC`, plan.WorkID, plan.ProjectID, plan.SubjectCommit, plan.ContractVersion, nativeBundleDigest(plan.Bundle), digest, identity)
	if err != nil {
		return "", err
	}
	// Close the cursor before receipt reads on the single pooled connection.
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return "", err
		}
		refs = append(refs, ref)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		p, err := readOraclePreparationReceiptTx(ctx, q, plan.WorkID, plan.ProjectID, ref)
		if err != nil {
			return "", err
		}
		if reviewPreparationMatches(p, plan) {
			return ref, nil
		}
	}
	return "", missingReviewPreparation()
}

// bindNativeReviewPreparationsTx selects each control's preparation for a
// Review dispatch (CD-0205 D1) against the sealed outside-transaction plan of
// the current candidate and runtime environment. A recorded readiness
// reference that qualifies on that plan stays the binding; otherwise the
// latest qualified preparation of the unchanged bundle under the same
// contract, Project, candidate, and runtime environment binds.
func bindNativeReviewPreparationsTx(ctx context.Context, q queryer, work string, binding WorkerJobBinding, oracle *AcceptanceOracle, subject string) ([]WorkerOraclePreparationBinding, error) {
	core, err := readCurrentOracleSubject(ctx, q, work)
	if err != nil {
		return nil, err
	}
	if core == "" || subject != core {
		return nil, missingReviewPreparation()
	}
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT contract_version FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=? AND digest=?`, work, binding.JobID, binding.Revision, binding.Digest).Scan(&version); err != nil {
		return nil, err
	}
	var plans map[string]nativeOraclePlan
	if prepared := workerDispatchPreparationFrom(ctx); prepared != nil && prepared.proof.nativeReview != nil && prepared.proof.workID == work && prepared.proof.nativeReview.binding == binding && prepared.observationMatches(subject) {
		plans = map[string]nativeOraclePlan{}
		for _, plan := range prepared.proof.nativeReview.plans {
			if plan.SubjectCommit == subject && plan.ContractVersion == version && plan.ProjectID == prepared.proof.projectID {
				plans[plan.Bundle.Control.ControlID] = plan
			}
		}
	}
	selected := make([]WorkerOraclePreparationBinding, 0, len(oracle.Controls))
	for _, control := range oracle.Controls {
		plan, ok := plans[control.ControlID]
		if !ok {
			return nil, missingReviewPreparation()
		}
		ref, err := selectNativeReviewPreparationTx(ctx, q, plan)
		if err != nil {
			return nil, err
		}
		selected = append(selected, WorkerOraclePreparationBinding{ControlID: control.ControlID, PreparationRunRef: ref})
	}
	return selected, nil
}

// freezeReviewDispatchPreparationsTx records, in a Review dispatch
// authorization, one qualified preparation per control on the current
// candidate, or refuses before an attempt; job content stays unchanged
// (CD-0205 D1). A checks-repair confirming Review keeps its own binding.
func freezeReviewDispatchPreparationsTx(ctx context.Context, q queryer, work string, lane LaneDefinition, job, repairReview *WorkerJobBinding, completionValues map[string]any) error {
	if lane.CapabilityClass != "review" || job == nil || repairReview != nil {
		return nil
	}
	oracle, err := readWorkerJobOracle(ctx, q, work, *job)
	if err != nil || oracle == nil {
		return err
	}
	subject, _ := completionValues["worker_subject_commit"].(string)
	selected, err := bindNativeReviewPreparationsTx(ctx, q, work, *job, oracle, subject)
	if err != nil {
		return err
	}
	completionValues["worker_oracle_preparations"] = selected
	return nil
}

// No execution path selects latest. A Review authorization that froze
// preparations is consumed only through that mapping; every other
// authorization, including one recorded without a mapping, keeps the
// recorded-reference rule it was admitted under.
func nativeDispatchPreparationRef(d workflowActionCompletedPayload, bundle NativeOracleControlBundle, requested string) (string, bool) {
	if d.WorkerCapabilityClass != "review" || len(d.WorkerOraclePreparations) == 0 {
		if requested == "" && len(bundle.Control.ReadinessEvidenceRefs) > 0 {
			requested = bundle.Control.ReadinessEvidenceRefs[0]
		}
		return requested, requested != "" && slices.Contains(bundle.Control.ReadinessEvidenceRefs, requested)
	}
	selected := ""
	for _, binding := range d.WorkerOraclePreparations {
		if binding.ControlID == bundle.Control.ControlID {
			if selected != "" {
				return "", false
			}
			selected = binding.PreparationRunRef
		}
	}
	return selected, selected != "" && (requested == "" || requested == selected)
}

func validateWorkerOraclePreparationBindings(p workflowActionCompletedPayload) error {
	if len(p.WorkerOraclePreparations) == 0 {
		return nil
	}
	if p.ActionID != "dispatch_worker" || p.WorkerCapabilityClass != "review" || p.WorkerJob == nil || len(p.WorkerOraclePreparations) > OracleControlsMax {
		return oracleFailure(KindInvalidPayload, "native preparation bindings require a Review job dispatch and at most 64 controls", "retain the store-selected Review preparation bindings")
	}
	seen := map[string]bool{}
	for _, b := range p.WorkerOraclePreparations {
		if seen[b.ControlID] || !oracleControlIDPattern.MatchString(b.ControlID) || workflowEvidenceRefsFault([]string{b.PreparationRunRef}, 1) != "" || b.PreparationRunRef == "" {
			return oracleFailure(KindInvalidPayload, "native preparation binding has a duplicate or invalid control/ref", "retain one bounded preparation reference per control")
		}
		seen[b.ControlID] = true
	}
	return nil
}

// latestNativeBundlePreparationTx reads the latest qualified preparation of
// the bundle on the current core subject. The packet projects it for Review;
// dispatch admission still requires its runtime environment before freezing it.
func latestNativeBundlePreparationTx(ctx context.Context, q queryer, work, project string, version int64, bundle NativeOracleControlBundle) (*NativeOraclePreparation, error) {
	core, err := readCurrentOracleSubject(ctx, q, work)
	if err != nil || core == "" {
		return nil, err
	}
	var ref string
	err = q.QueryRowContext(ctx, `SELECT json_extract(result_json,'$.operation_ref') FROM worktree_verify_leases WHERE work_id=? AND project_id=? AND state='released' AND outcome='completed' AND json_extract(result_json,'$.oracle.phase')='prepare' AND json_extract(result_json,'$.oracle.qualification')='ready' AND json_extract(result_json,'$.oracle.subject_commit')=? AND json_extract(result_json,'$.oracle.contract_version')=? AND json_extract(result_json,'$.oracle.bundle_digest')=? ORDER BY released_at DESC,rowid DESC LIMIT 1`, work, project, core, version, nativeBundleDigest(bundle)).Scan(&ref)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return readOraclePreparationReceiptTx(ctx, q, work, project, ref)
}
