package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Retained recovery is an input proof, not acceptance of the failed editor.
// SQL samples the entire editing population before Git observes the delta.
type retainedReviewState struct {
	Candidate     WorkerReviewCandidate    `json:"candidate"`
	AcceptanceSeq int64                    `json:"acceptance_seq"`
	Job           WorkerJobBinding         `json:"job"`
	Contract      int64                    `json:"contract_version"`
	Dependencies  WorkerReviewDependencies `json:"dependencies"`
	Editors       []retainedReviewEditor   `json:"editors"`
}

type retainedReviewEditor struct {
	Attempt       string           `json:"attempt_id"`
	Authorization int64            `json:"authorization_seq"`
	AuthDigest    string           `json:"authorization_digest"`
	Worktree      string           `json:"worktree_identity"`
	Dispatch      int64            `json:"dispatch_seq"`
	Terminal      int64            `json:"terminal_seq"`
	TerminalRef   string           `json:"terminal_ref"`
	TerminalHash  string           `json:"terminal_digest"`
	Job           WorkerJobBinding `json:"job"`
	Paths         []string         `json:"paths"`
}

type retainedReviewProof struct {
	State        retainedReviewState   `json:"state"`
	Input        WorkerReviewCandidate `json:"input"`
	ChangedPaths []string              `json:"changed_paths"`
	ClaimOp      string                `json:"claim_op"`
	ClaimBranch  string                `json:"claim_branch"`
}

func sampleRetainedReviewDispatch(ctx context.Context, q queryer, req PrepareWorkerDispatchRequest) (*retainedReviewState, error) {
	var payload struct {
		WorkerPacket struct {
			LaneID     string `json:"lane_id"`
			LaneDigest string `json:"lane_digest"`
		} `json:"worker_packet"`
	}
	if json.Unmarshal(req.DispatchPayload, &payload) != nil {
		return nil, nil
	}
	lane, err := LookupLane(payload.WorkerPacket.LaneID, payload.WorkerPacket.LaneDigest)
	binding := WorkerJobBindingFromDispatchPayload(req.DispatchPayload)
	if err != nil || lane.CapabilityClass != "review" || binding == nil {
		return nil, nil
	}
	return sampleRetainedReviewState(ctx, q, req.WorkID, *binding, "")
}

// An unchanged ready job is necessary but insufficient: every editing
// authorization since this candidate's acceptance must be a failed same-job
// attempt. Missing attempt rows and completed-but-rejected results refuse too.
func sampleRetainedReviewState(ctx context.Context, q queryer, work string, binding WorkerJobBinding, self string) (*retainedReviewState, error) {
	review, err := readWorkflowReviewFacts(ctx, q, work)
	if err != nil || review.Candidate == nil {
		return nil, err
	}
	if err := requireWorkerJobRevisionStateReadyTx(ctx, q, work, binding); err != nil {
		return nil, nil // The ordinary job owner supplies its specific refusal.
	}
	var ready int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM worker_job_revisions j WHERE j.work_id=? AND `+workerJobReadyPredicate, work).Scan(&ready); err != nil {
		return nil, err
	}
	job, err := readWorkerJobRevisionTx(ctx, q, work, binding)
	if err != nil {
		return nil, err
	}
	version, err := activeWorkflowContractVersion(ctx, q, work, workerDispatchFreshnessOp)
	if err != nil {
		return nil, err
	}
	if ready != 1 || job.ContractVersion != version || job.ProjectScope != review.Candidate.ProjectID || len(job.PathScope) == 0 || job.ChecksRepair != nil {
		return nil, nil
	}
	// The latest accepted result must be the producer of the current candidate;
	// an intervening acceptance cannot be hidden by an unchanged candidate OID.
	var acceptedAttempt string
	var acceptedSeq int64
	err = q.QueryRowContext(ctx, `SELECT attempt_id,seq FROM worker_attempt_dispositions WHERE work_id=? AND action_id='accept_worker_result' ORDER BY seq DESC LIMIT 1`, work).Scan(&acceptedAttempt, &acceptedSeq)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var conflict bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id=? AND action_id IN ('reject_worker_result','record_worker_failure') AND seq>=?)`, work, acceptedAttempt, acceptedSeq).Scan(&conflict); err != nil {
		return nil, err
	}
	if conflict {
		return nil, nil
	}
	auth, result, err := readReviewAttempt(ctx, q, work, acceptedAttempt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	candidate, err := workflowAcceptedResultCandidate(ctx, q, work, auth, result)
	if err != nil || candidate == nil || *candidate != *review.Candidate {
		return nil, err
	}
	state := &retainedReviewState{Candidate: *candidate, AcceptanceSeq: acceptedSeq, Job: binding, Contract: version}
	state.Dependencies, err = workflowReviewDependencies(ctx, q, work)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT a.attempt_id,a.seq,a.payload,a.contract_version,
 COALESCE(t.dispatch_seq,0),COALESCE(t.lifecycle_state,''),COALESCE(t.terminal_kind,''),
 COALESCE(t.terminal_seq,0),COALESCE(t.terminal_event_id,''),COALESCE(t.terminal_payload,''),
 EXISTS(SELECT 1 FROM worker_attempt_dispositions d WHERE d.work_id=a.work_id AND d.attempt_id=a.attempt_id AND d.action_id IN ('accept_worker_result','accept_worker_evidence','reject_worker_result'))
 FROM worker_authorizations a LEFT JOIN worker_attempts t USING(work_id,attempt_id)
 WHERE a.work_id=? AND a.seq>? AND a.attempt_id<>? ORDER BY a.seq`, work, acceptedSeq, self)
	if err != nil {
		return nil, err
	}
	type editorRow struct {
		editor          retainedReviewEditor
		auth            workflowActionCompletedPayload
		version         int64
		lifecycle, kind string
		disposition     bool
	}
	var editors []editorRow
	for rows.Next() {
		var row editorRow
		var raw, terminal []byte
		if err := rows.Scan(&row.editor.Attempt, &row.editor.Authorization, &raw, &row.version, &row.editor.Dispatch, &row.lifecycle, &row.kind, &row.editor.Terminal, &row.editor.TerminalRef, &terminal, &row.disposition); err != nil {
			rows.Close()
			return nil, err
		}
		if err := json.Unmarshal(raw, &row.auth); err != nil {
			rows.Close()
			return nil, err
		}
		// Unknown lane identities cannot be inferred to be non-editing.
		lane, err := LookupLane(row.auth.WorkerLaneID, row.auth.WorkerLaneDigest)
		if err != nil {
			rows.Close()
			return nil, nil
		}
		if !workerLaneEdits(lane.ID, lane.Digest) {
			continue
		}
		row.editor.AuthDigest, row.editor.TerminalHash = nativeDigest(raw), nativeDigest(terminal)
		row.editor.Worktree = row.auth.WorkerWorktreeIdentity
		editors = append(editors, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(editors) == 0 {
		return nil, err
	}
	for _, row := range editors {
		if row.version != version || row.auth.WorkerJob == nil || row.auth.WorkerJob.JobID != binding.JobID || row.lifecycle != "failed" || row.kind != WorkerFailed || row.editor.Dispatch == 0 || row.editor.Terminal <= row.editor.Dispatch || row.disposition {
			return nil, nil
		}
		failedJob, err := readWorkerJobRevisionTx(ctx, q, work, *row.auth.WorkerJob)
		if err != nil {
			return nil, err
		}
		if failedJob.ProjectScope != job.ProjectScope || len(failedJob.PathScope) == 0 {
			return nil, nil
		}
		row.editor.Job, row.editor.Paths = *row.auth.WorkerJob, failedJob.PathScope
		state.Editors = append(state.Editors, row.editor)
	}
	return state, nil
}

func observeRetainedReview(ctx context.Context, runner GitRunner, proof *workerDispatchProof, sample *retainedReviewState, entry WorktreeEntry) (*retainedReviewProof, error) {
	if sample == nil || proof.acceptance || entry.MemberID != "" || entry.ProjectID != sample.Candidate.ProjectID || proof.observation.State != "clean" || proof.observation.Commit == sample.Candidate.Commit {
		return nil, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, workerDispatchObservationDeadline)
	defer cancel()
	if _, err := runner.Run(probeCtx, proof.probePath, "--no-replace-objects", "merge-base", "--is-ancestor", sample.Candidate.Commit, proof.observation.Commit); err != nil {
		return nil, nil // No ancestry proof means no exception to candidate equality.
	}
	args := append([]string{"--no-replace-objects"}, workerDiffSharedDiffArgs()...)
	raw, err := runner.Run(probeCtx, proof.probePath, append(args, "--name-only", "-z", sample.Candidate.Commit, proof.observation.Commit, "--")...)
	if err != nil {
		return nil, dispatchSubjectProbeFailure("retained recovery changed paths", probeCtx, err)
	}
	// Dispatch's runner bounds time, not bytes; continuity's committed-read
	// runner refuses past maxGitOutput, and the packet capacity derives from it.
	if len(raw) > maxGitOutput {
		return nil, newFailure(KindLimitExceeded, workerDispatchFreshnessOp, "the retained delta's changed-path list exceeds the Review disclosure bound", false, "retain the candidate and failed-attempt evidence; no admitted retained-input recovery exists for this delta: request maintainer investigation, not a reset or truncated disclosure")
	}
	retained := &retainedReviewProof{State: *sample, Input: WorkerReviewCandidate{ProjectID: entry.ProjectID, Commit: proof.observation.Commit, Tree: proof.observation.Tree}, ChangedPaths: []string{}, ClaimOp: entry.ClaimOpID, ClaimBranch: entry.Branch}
	for _, path := range strings.Split(string(raw), "\x00") {
		if path != "" {
			retained.ChangedPaths = append(retained.ChangedPaths, path)
		}
	}
	return retained, nil
}

func retainedReviewCurrent(ctx context.Context, q queryer, work string, proof *retainedReviewProof, self, identity string) (bool, error) {
	if proof == nil || proof.Input.ProjectID != proof.State.Candidate.ProjectID || proof.Input.Commit == proof.State.Candidate.Commit || proof.ChangedPaths == nil {
		return false, nil
	}
	state, err := sampleRetainedReviewState(ctx, q, work, proof.State.Job, self)
	if err != nil || state == nil || reviewDigest(state) != reviewDigest(proof.State) {
		return false, err
	}
	job, err := readWorkerJobRevisionTx(ctx, q, work, state.Job)
	if err != nil {
		return false, err
	}
	for _, editor := range state.Editors {
		if editor.Worktree != identity {
			return false, nil
		}
	}
	for _, path := range proof.ChangedPaths {
		if !workerJobContainsPath(job.PathScope, path) {
			return false, nil
		}
		for _, editor := range state.Editors {
			if !workerJobContainsPath(editor.Paths, path) {
				return false, nil
			}
		}
	}
	// Dispatch consumes the live qualification. Acceptance consumes the sealed
	// input proof and the terminal producer's output, which may be a repair.
	if self == "" {
		receipt, err := readCurrentOracleSubjectReceipt(ctx, q, work)
		if err != nil || receipt == nil || receipt.SubjectRef != "commit:"+proof.Input.Commit || receipt.ProjectID != proof.Input.ProjectID || workerWorktreeIdentity(receipt.Path) != identity {
			return false, err
		}
	}
	entries, err := worktreeEntriesCore(ctx, q, work)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.State == worktreeEntryActive && entry.MemberID == "" && entry.ProjectID == proof.Input.ProjectID && entry.ClaimOpID == proof.ClaimOp && entry.Branch == proof.ClaimBranch && workerWorktreeIdentity(entry.Path) == identity {
			return true, nil
		}
	}
	return false, nil
}

func bindRetainedReviewCompletion(ctx context.Context, lane LaneDefinition, job *WorkerJobBinding, values map[string]any) error {
	prepared := workerDispatchPreparationFrom(ctx)
	if lane.CapabilityClass != "review" || prepared == nil || prepared.proof.retainedReview == nil {
		return nil
	}
	proof := prepared.proof.retainedReview
	if job == nil || *job != proof.State.Job {
		return reviewFailure(KindStaleRequiresReview, "retained Review preparation binds another assignment", "prepare Review for the exact current ready assignment")
	}
	values["worker_retained_review"] = proof
	return nil
}

func validateRetainedReviewMetadata(auth workflowActionCompletedPayload) error {
	proof := auth.WorkerRetainedReview
	if proof == nil {
		return nil
	}
	valid := auth.ActionID == "dispatch_worker" && auth.WorkerCapabilityClass == "review" && auth.WorkerJob != nil && *auth.WorkerJob == proof.State.Job && auth.WorkerSubjectCommit == proof.Input.Commit && auth.WorkerWorktreeIdentity != "" && proof.State.AcceptanceSeq > 0 && proof.State.Contract > 0 && len(proof.State.Editors) > 0 && proof.ChangedPaths != nil && proof.ClaimOp != "" && proof.ClaimBranch != "" && proof.Input.ProjectID != "" && proof.Input.ProjectID == proof.State.Candidate.ProjectID && proof.Input.Commit != proof.State.Candidate.Commit
	for _, candidate := range []WorkerReviewCandidate{proof.Input, proof.State.Candidate} {
		valid = valid && worktreeSHAPattern.MatchString(candidate.Commit) && worktreeSHAPattern.MatchString(candidate.Tree)
	}
	if !valid {
		return reviewFailure(KindInvalidPayload, "retained Review proof lacks its exact dispatch, candidate or failed-edit identities", "record only the authenticated producer's retained Review proof on its Review dispatch")
	}
	return nil
}
