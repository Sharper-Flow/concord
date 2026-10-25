package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// WorkerDiffRequest binds a byte selector to the host-held review packet.
// Neither commit identities nor paths are caller-selected.
type WorkerDiffRequest struct {
	WorkID           string          `json:"work_id"`
	AttemptID        string          `json:"attempt_id"`
	WorktreeIdentity string          `json:"worktree_identity"`
	Packet           json.RawMessage `json:"packet"`
	Offset           int             `json:"offset"`
	Length           int             `json:"length"`
	Runner           GitRunner       `json:"-"`
}

type WorkerDiffPage struct {
	BaseCommit    string `json:"base_commit"`
	SubjectCommit string `json:"subject_commit"`
	ContentHash   string `json:"content_hash"`
	Offset        int    `json:"offset"`
	NextOffset    int    `json:"next_offset"`
	TotalBytes    int    `json:"total_bytes"`
	EOF           bool   `json:"eof"`
	Data          []byte `json:"data"`
}

type workerDiffSnapshot struct {
	window          WorkerDispatchWindow
	base, repo      string
	contractVersion int64
	job             WorkerJobBinding
	paths           []string
	repairing       bool
}

func workerDiffRefusal(detail string) error {
	return newFailure(KindUnauthorizedDispatch, "worker_diff", detail, false, "use a live review with its original packet, claimed worktree, scoped job, and complete first-editing subject history")
}

// firstWorkerEditingSubject never skips an editing authorization with a
// missing subject. The newest contract with an eligible editing authorization
// owns the foundation. A correction-only contract and a repairing Review do
// not reset the accumulated solution's base. Each earlier authorization is
// classified by the capability class its attempt recorded at dispatch, not by
// current lane content, and an unclassifiable row refuses.
func firstWorkerEditingSubject(ctx context.Context, q queryer, workID, identity string, reviewSeq int64) (string, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.contract_version,a.attempt_id IS NOT NULL,COALESCE(a.capability_class,''),COALESCE(json_extract(r.payload,'$.worker_subject_commit'),''),COALESCE(json_extract(r.payload,'$.worker_worktree_identity'),'') FROM worker_authorizations r LEFT JOIN worker_attempts a ON a.work_id=r.work_id AND a.attempt_id=r.attempt_id WHERE r.work_id=? AND r.seq>0 AND r.seq<? ORDER BY r.seq`, workID, reviewSeq)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type editingDispatch struct {
		contract      int64
		subject, tree string
		editing       bool
	}
	editors := make([]editingDispatch, 0, 8)
	var newestContract int64
	for rows.Next() {
		var d editingDispatch
		var attempted bool
		var class string
		if err := rows.Scan(&d.contract, &attempted, &class, &d.subject, &d.tree); err != nil {
			return "", err
		}
		if !attempted {
			return "", workerDiffRefusal("earlier authorization has no recorded worker attempt")
		}
		editing, known := laneFoundationClasses[class]
		if !known {
			return "", workerDiffRefusal("earlier worker attempt records no known capability class")
		}
		d.editing = editing
		if d.editing {
			if d.contract < 1 {
				return "", workerDiffRefusal("review contract has no recorded approval history")
			}
			newestContract = max(newestContract, d.contract)
		}
		editors = append(editors, d)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	for _, d := range editors {
		if d.contract != newestContract || !d.editing {
			continue
		}
		if d.tree == "" {
			return "", workerDiffRefusal("editing authorization has no recorded worktree identity")
		}
		if d.tree != identity {
			continue
		}
		if !worktreeSHAPattern.MatchString(d.subject) {
			return "", workerDiffRefusal("first editing authorization has no recorded raw subject")
		}
		return d.subject, nil
	}
	return "", workerDiffRefusal("review contract has no first editing authorization on this worktree")
}

// sampleWorkerDiff is SQL-only; Git runs after this snapshot releases the pool.
func sampleWorkerDiff(ctx context.Context, q queryer, req WorkerDiffRequest, snapshot *workerDiffSnapshot) error {
	if err := requireNoOutsideRepairTx(ctx, q, req.WorkID, "worker_diff"); err != nil {
		return err
	}
	window, err := findAuthorizedDispatchWindowBefore(ctx, q, req.WorkID, req.AttemptID, 0)
	if err != nil {
		return err
	}
	if window.WorktreeIdentity != req.WorktreeIdentity || !worktreeSHAPattern.MatchString(window.SubjectCommit) {
		return workerDiffRefusal("diff requires the exact review subject and worktree")
	}
	if err := ValidateRecoveryPacket(req.Packet, window.PacketDigest, req.WorkID, req.AttemptID); err != nil {
		return err
	}
	dispatch, auth, err := readNativeOracleDispatchBindingTx(ctx, q, req.WorkID, req.AttemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			return workerDiffRefusal("worker diff read requires the exact live review dispatch and candidate binding")
		}
		return err
	}
	lane, err := LookupLane(dispatch.WorkerLaneID, dispatch.WorkerLaneDigest)
	if err != nil {
		return err
	}
	if !slices.Contains(lane.Capabilities, "inspect_diff") || dispatch.WorkerJob == nil {
		return workerDiffRefusal("diff requires a live inspect_diff attempt with its pinned job")
	}
	version, err := activeWorkflowContractVersion(ctx, q, req.WorkID, "worker_diff")
	if errors.Is(err, sql.ErrNoRows) {
		return workerDiffRefusal("worker diff read requires an active workflow contract")
	}
	if err != nil {
		return err
	}
	var packet struct {
		Inputs struct {
			Binding struct {
				ContractVersion int64 `json:"contract_version"`
			} `json:"binding"`
			WorkContext *WorkContextView `json:"work_context"`
		} `json:"inputs"`
	}
	if json.Unmarshal(req.Packet, &packet) != nil || packet.Inputs.Binding.ContractVersion != version || packet.Inputs.WorkContext == nil || packet.Inputs.WorkContext.SubjectCommit != window.SubjectCommit {
		return workerDiffRefusal("review packet does not bind the active contract and held subject")
	}
	jobBinding := *dispatch.WorkerJob
	job, err := readWorkerDiffJobTx(ctx, q, req.WorkID, jobBinding, version, auth.Seq)
	if err != nil {
		return err
	}
	entry, err := activeWorktreeEntryForProject(ctx, q, "worker_diff", req.WorkID, job.ProjectScope)
	if err != nil {
		return err
	}
	if workerWorktreeIdentity(entry.Path) != req.WorktreeIdentity {
		return workerDiffRefusal("review job belongs to another claimed worktree")
	}
	base, err := firstWorkerEditingSubject(ctx, q, req.WorkID, req.WorktreeIdentity, auth.Seq)
	if err != nil {
		return err
	}
	*snapshot = workerDiffSnapshot{window: window, base: base, repo: entry.Path, contractVersion: version, job: jobBinding, paths: job.PathScope, repairing: workerLaneRepairs(dispatch.WorkerLaneID, dispatch.WorkerLaneDigest)}
	return nil
}

func readWorkerDiffJobTx(ctx context.Context, q queryer, workID string, binding WorkerJobBinding, contractVersion, authorizationSeq int64) (WorkerJobRecordedPayload, error) {
	var job WorkerJobRecordedPayload
	var raw []byte
	event := Event{SubjectType: SubjectWorkItem, SubjectID: workID, Kind: WorkerJobRecorded}
	err := q.QueryRowContext(ctx, `SELECT recorded_event_id,1,recorded_payload FROM worker_job_revisions WHERE work_id=? AND recorded_seq<? AND job_id=? AND revision=? AND digest=?`, workID, authorizationSeq, binding.JobID, binding.Revision, binding.Digest).Scan(&event.EventID, &event.PayloadVersion, &raw)
	if err != nil && err != sql.ErrNoRows {
		return job, err
	}
	if err != nil || json.Unmarshal(raw, &job) != nil || job.ContractVersion != contractVersion || DeriveWorkerJobDigest(job) != binding.Digest || len(job.PathScope) == 0 || validateWorkerJobRecordedPayload(event, job) != nil {
		return WorkerJobRecordedPayload{}, workerDiffRefusal("review has no exact recorded job with a nonempty contained path scope")
	}
	return job, nil
}

func revalidateWorkerDiff(ctx context.Context, q queryer, req WorkerDiffRequest, before workerDiffSnapshot) error {
	var after workerDiffSnapshot
	if err := sampleWorkerDiff(ctx, q, req, &after); err != nil {
		return err
	}
	if before.window != after.window || before.base != after.base || before.repo != after.repo || before.contractVersion != after.contractVersion || before.job != after.job || before.repairing != after.repairing || !slices.Equal(before.paths, after.paths) {
		return workerDiffRefusal("review authority changed while reading the committed diff")
	}
	return nil
}

func checkWorkerDiffSubject(ctx context.Context, runner GitRunner, before workerDiffSnapshot) error {
	args := []string{"--no-optional-locks", "-c", "core.fsmonitor=false"}
	if before.repairing {
		// A repairing pass can edit the checkout while reading its immutable
		// input B. This reader never substitutes mutable HEAD or output C.
		commit, err := runner.Run(ctx, before.repo, append(args, "rev-parse", "--verify", before.window.SubjectCommit+"^{commit}")...)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(commit)) != before.window.SubjectCommit {
			return workerDiffRefusal("review input commit is unavailable")
		}
		return nil
	}
	head, err := runner.Run(ctx, before.repo, append(args, "rev-parse", "--verify", "HEAD^{commit}")...)
	if err != nil {
		return err
	}
	status, err := runner.Run(ctx, before.repo, append(args, "status", "--porcelain=v1", "--untracked-files=all")...)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != before.window.SubjectCommit || len(status) != 0 {
		return workerDiffRefusal("review subject must remain the exact clean held HEAD")
	}
	return nil
}

// workerDiffSharedDiffArgs is the one owner of the diff argument list both
// editing-surface readers share: the global flags, the diff
// subcommand, and the closed flag set that disables rename detection,
// external diffs, and text conversion under the Myers algorithm. The review
// diff read appends its patch-shaping flags; the terminal delivery
// observation appends --name-only -z and the two commits. No second reader
// copies these flags.
func workerDiffSharedDiffArgs() []string {
	return []string{"--literal-pathspecs", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--diff-algorithm=myers", "--no-indent-heuristic"}
}

// ReadWorkerDiff reads immutable objects only, creates no evidence, and samples
// the same live authority again before returning at most one bounded byte page.
func (s *Store) ReadWorkerDiff(ctx context.Context, req WorkerDiffRequest) (WorkerDiffPage, error) {
	var empty WorkerDiffPage
	if req.WorkID == "" || req.AttemptID == "" || req.WorktreeIdentity == "" || len(req.Packet) == 0 || req.Offset < 0 || req.Length < 1 || req.Length > WorkerReadingPageMaxBytes {
		return empty, newFailure(KindInvalidPayload, "worker_diff", "diff requires a bound packet, nonnegative offset, and length from 1 to 4096 bytes", false, "select a bounded review diff page")
	}
	ctx, cancel := context.WithTimeout(ctx, workerDispatchReadingResolutionDeadline)
	defer cancel()
	var before workerDiffSnapshot
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return sampleWorkerDiff(ctx, q, req, &before)
	}); err != nil {
		return empty, err
	}
	runner := req.Runner
	if runner == nil {
		runner = CommittedReadGitRunner{}
	}
	if err := checkWorkerDiffSubject(ctx, runner, before); err != nil {
		return empty, err
	}
	args := append(workerDiffSharedDiffArgs(), "--full-index", "--src-prefix=a/", "--dst-prefix=b/", "--unified=3", before.base, before.window.SubjectCommit, "--")
	content, err := runner.Run(ctx, before.repo, append(args, before.paths...)...)
	if err != nil {
		return empty, err
	}
	if err := checkWorkerDiffSubject(ctx, runner, before); err != nil {
		return empty, err
	}
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return revalidateWorkerDiff(ctx, q, req, before)
	}); err != nil {
		return empty, err
	}
	if req.Offset > len(content) {
		return empty, newFailure(KindInvalidPayload, "worker_diff", "diff offset exceeds the immutable delta length", false, "select an offset inside the diff")
	}
	end := req.Offset + min(req.Length, len(content)-req.Offset)
	sum := sha256.Sum256(content)
	return WorkerDiffPage{BaseCommit: before.base, SubjectCommit: before.window.SubjectCommit, ContentHash: "sha256:" + hex.EncodeToString(sum[:]), Offset: req.Offset, NextOffset: end, TotalBytes: len(content), EOF: end == len(content), Data: append([]byte{}, content[req.Offset:end]...)}, nil
}
