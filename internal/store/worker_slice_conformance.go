package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const workerSliceConformanceOp = "worker_slice_conformance"

type PrepareWorkerSliceConformanceRequest struct {
	WorkID          string
	AttemptID       string
	ExpectedVersion int64
	Runner          GitRunner
}

type WorkerSliceConformancePreparation struct {
	sample      workerSliceConformanceSample
	measurement *workerSliceConformanceMeasurement
}

type workerSliceConformanceMeasurement struct {
	base, end, tree, algorithm string
	claim                      WorktreeEntry
	result                     sliceDiffResult
}

type workerSliceConformanceSample struct {
	workID, attemptID, step, project, locatorID, repoPath string
	expectedVersion, version, contractVersion             int64
	attempt                                               WorkerAttempt
	window                                                WorkerDispatchWindow
	job                                                   WorkerJobRevisionView
	design                                                WorkflowDesignRecord
	entries                                               []WorktreeEntry
}

func sampleSliceConformance(ctx context.Context, q queryer, request PrepareWorkerSliceConformanceRequest) (workerSliceConformanceSample, error) {
	var sample workerSliceConformanceSample
	sample.workID, sample.attemptID, sample.expectedVersion = request.WorkID, request.AttemptID, request.ExpectedVersion
	if request.WorkID == "" || request.AttemptID == "" {
		return sample, newFailure(KindInvalidPayload, workerSliceConformanceOp, "slice conformance sampling requires the work and attempt identities", false, "supply the completed attempt the native observation seals")
	}
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, request.WorkID).Scan(&sample.version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sample, newFailure(KindProjectionNotFound, workerSliceConformanceOp, "the work item is not recorded", false, "supply the work item the attempt completed")
		}
		return sample, wrapFailure(KindUnavailable, workerSliceConformanceOp, "cannot read the work version", true, "retry once the database is readable", err)
	}
	if sample.version != request.ExpectedVersion {
		return sample, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the work item is not at the expected version", true, "prepare the same slice again against the current work version")
	}
	if err := q.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&sample.step); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sample, newFailure(KindProjectionNotFound, workerSliceConformanceOp, "the workflow instance is not projected", false, "record the workflow projection before preparing a slice observation")
		}
		return sample, wrapFailure(KindUnavailable, workerSliceConformanceOp, "cannot read the workflow step", true, "retry once the database is readable", err)
	}
	contractVersion, err := activeWorkflowContractVersion(ctx, q, request.WorkID, workerSliceConformanceOp)
	if errors.Is(err, sql.ErrNoRows) {
		return sample, newFailure(KindProjectionNotFound, workerSliceConformanceOp, "the work item holds no active contract", false, "approve a contract before preparing a slice observation")
	}
	if err != nil {
		return sample, err
	}
	sample.contractVersion = contractVersion
	sample.attempt, err = workerAttemptByIDCore(ctx, q, request.AttemptID)
	if err != nil {
		return sample, err
	}
	if sample.attempt.WorkID != request.WorkID {
		return sample, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the attempt completed another work item", false, "supply the attempt bound to this work item")
	}
	if sample.attempt.LifecycleState != "completed" {
		return sample, newFailure(KindProjectionConflict, workerSliceConformanceOp, "slice conformance seals only a completed attempt", false, "record the attempt's completion before preparing its observation")
	}
	lane, laneErr := builtinLaneRegistry().Lookup(sample.attempt.LaneID, sample.attempt.LaneVersion, sample.attempt.LaneDigest)
	if laneErr != nil {
		return sample, laneErr
	}
	if !containsString(lane.Capabilities, "edit_scoped_files") {
		return sample, newFailure(KindUnauthorizedDispatch, workerSliceConformanceOp, "the attempt's registered lane grants no edit_scoped_files capability", false, "seal only a registered editing lane's completed attempt")
	}
	sample.window, err = findAuthorizedDispatchWindowCore(ctx, q, request.WorkID, request.AttemptID)
	if err != nil {
		return sample, err
	}
	if sample.window.StepID != sample.step {
		return sample, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the dispatch authorization does not bind the current workflow step", true, "prepare the same slice again against the current workflow")
	}
	if sample.window.SubjectCommit == "" || !worktreeSHAPattern.MatchString(sample.window.SubjectCommit) ||
		!workflowDesignDigestShape(sample.window.WorktreeIdentity) ||
		!workflowDesignDigestShape(sample.window.PacketDigest) {
		return sample, newFailure(KindInvalidPayload, workerSliceConformanceOp, "the dispatch authorization records no valid subject commit, worktree identity, or packet digest", false, "reconcile the recorded authorization")
	}
	dispatched, err := workflowDispatchedJobForAttempt(ctx, q, request.WorkID, request.AttemptID)
	if err != nil {
		return sample, err
	}
	found, authorized, err := dispatchCompletionJobForAttempt(ctx, q, request.WorkID, request.AttemptID)
	if err != nil {
		return sample, err
	}
	if !found || dispatched == nil {
		return sample, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the dispatch authorization binds no worker-job revision", true, "dispatch the attempt under a recorded worker-job revision")
	}
	if !sameWorkerJob(dispatched, authorized) {
		return sample, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the dispatched worker-job binding differs from the authorization's bound revision", true, "redispatch the attempt under one exact worker-job revision")
	}
	views, err := readWorkerJobRevisions(ctx, q, request.WorkID)
	if err != nil {
		return sample, err
	}
	if index := slices.IndexFunc(views, func(view WorkerJobRevisionView) bool { return view.Binding == *dispatched }); index >= 0 {
		sample.job = views[index]
	}
	if sample.job.Binding.JobID == "" {
		return sample, newFailure(KindProjectionNotFound, workerSliceConformanceOp, "the dispatched worker-job revision is not recorded", false, "record the exact dispatched revision before preparing its observation")
	}
	if sample.job.ContractVersion != sample.contractVersion {
		return sample, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the dispatched job revision does not record the active contract", true, "record the job under the active contract and redispatch")
	}
	current, absent, err := readCurrentWorkflowDesign(ctx, q, request.WorkID)
	if err != nil {
		return sample, err
	}
	if absent || current == nil || current.Architecture == nil || current.Revision == nil || current.Digest == "" {
		return sample, newFailure(KindInvalidPayload, workerSliceConformanceOp, "the work item holds no current architecture design to seal", false, "record the typed architecture design before preparing the slice observation")
	}
	sample.design = *current
	if sample.job.ArchitectureBinding == nil {
		return sample, newFailure(KindInvalidPayload, workerSliceConformanceOp, "the dispatched job revision binds no design identity", false, "record the job under an architecture-capable definition and redispatch")
	}
	bound, boundFound := findArchitectureSlice(*current.Architecture, sample.job.ArchitectureBinding.SliceID)
	if !boundFound {
		return sample, newFailure(KindInvalidPayload, workerSliceConformanceOp, "the bound design declares no slice "+sample.job.ArchitectureBinding.SliceID, false, "dispatch a job bound to a slice the current design declares")
	}
	if bound.Kind != DesignArchitectureSliceKindEdit {
		return sample, newFailure(KindInvalidPayload, workerSliceConformanceOp, "the bound slice grants no editing authority", false, "seal only an edit slice's completed attempt")
	}
	sample.project, err = workflowJobBindingAuthorityTx(ctx, q, request.WorkID, sample.job.ArchitectureBinding, sample.job.PathScope, sample.job.Checks, sample.job.AcceptanceOracle)
	if err != nil {
		return sample, err
	}
	if sample.job.ProjectScope != sample.project {
		return sample, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the job's Project scope does not join the bound slice owner", false, "record the job with the bound slice owner's Project scope")
	}
	sample.entries, err = worktreeEntriesCore(ctx, q, request.WorkID)
	if err != nil {
		return sample, wrapFailure(KindUnavailable, workerSliceConformanceOp, "cannot read the claimed worktree entries", true, "retry once the database is readable", err)
	}
	sample.locatorID, sample.repoPath, err = uniqueProjectRepositoryLocator(ctx, q, sample.project)
	if err != nil {
		return sample, err
	}
	return sample, nil
}

func sampleSliceConformanceInto(ctx context.Context, q queryer, request PrepareWorkerSliceConformanceRequest, sample *workerSliceConformanceSample) error {
	sampled, sampleErr := sampleSliceConformance(ctx, q, request)
	*sample = sampled
	return sampleErr
}

func (p *WorkerSliceConformancePreparation) revalidateTx(ctx context.Context, tx *sql.Tx) error {
	if p == nil || p.sample.workID == "" {
		return newFailure(KindInvalidPayload, workerSliceConformanceOp, "revalidation requires a prepared slice conformance sample", false, "prepare the slice observation before revalidating it")
	}
	current, err := sampleSliceConformance(ctx, tx, PrepareWorkerSliceConformanceRequest{WorkID: p.sample.workID, AttemptID: p.sample.attemptID, ExpectedVersion: p.sample.expectedVersion})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(p.sample, current) {
		return newFailure(KindProjectionConflict, workerSliceConformanceOp, "the sampled slice authority changed under the native observation", true, "prepare the same slice again against the current authority")
	}
	return nil
}

// PrepareWorkerSliceConformance pairs one SQL-sampled authority with one
// sealed native slice observation. Native facts alone grant no accepted-prefix
// authority: the ingest revalidates the SQL sample in its own transaction
// before anything accepts the observation.
func PrepareWorkerSliceConformance(ctx context.Context, s *Store, request PrepareWorkerSliceConformanceRequest) (*WorkerSliceConformancePreparation, error) {
	var sample workerSliceConformanceSample
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return sampleSliceConformanceInto(ctx, q, request, &sample)
	}); err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	measurement, err := observeSliceConformance(probeCtx, &sliceConformanceGit{seam: request.Runner}, sample)
	if err != nil {
		return nil, err
	}
	return &WorkerSliceConformancePreparation{sample: sample, measurement: measurement}, nil
}

func observeSliceConformance(ctx context.Context, git *sliceConformanceGit, sample workerSliceConformanceSample) (*workerSliceConformanceMeasurement, error) {
	entry, probePath, err := selectWorkerClaimEntry(sample.entries, sample.window.WorktreeIdentity)
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) { // a completed attempt cannot be abandoned: adapt the shared selector's refusal
			err = newFailure(failure.Kind, workerSliceConformanceOp, failure.Detail, failure.RetrySafe, "restore the attempt's recorded worktree claim")
		}
		return nil, err
	}
	if entry.ProjectID != sample.project {
		return nil, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the claimed worktree belongs to another Project than the bound slice owner", false, "claim the bound owner Project's worktree for this slice")
	}
	canonicalStore := func(dir string) (string, error) {
		common, err := sliceConformanceTrimLF(git.run(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir"))
		if err != nil {
			return "", err
		}
		if common == "" || !filepath.IsAbs(common) {
			return "", newFailure(KindGitUnreachable, workerSliceConformanceOp, "Git reported an empty or relative common directory", true, "retry once Git reports the worktree's absolute common directory")
		}
		canonical, canonicalErr := filepath.EvalSymlinks(common)
		if canonicalErr != nil {
			return "", wrapFailure(KindGitUnreachable, workerSliceConformanceOp, "the reported Git common directory is not reachable", true, "retry once the worktree's Git object store resolves on the filesystem", canonicalErr)
		}
		return canonical, nil
	}
	claimStore, err := canonicalStore(probePath)
	if err != nil {
		return nil, err
	}
	repoStore, err := canonicalStore(sample.repoPath)
	if err != nil {
		return nil, err
	}
	if claimStore != repoStore {
		return nil, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the claimed worktree does not share the registered repository's Git object store", false, "claim the registered repository's worktree for this slice")
	}
	base := sample.window.SubjectCommit
	end, tree, parent, err := observeSliceCleanHead(ctx, git, probePath)
	if err != nil {
		return nil, err
	}
	if parent != base {
		return nil, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the worktree HEAD is not one commit whose sole parent is the authorization's subject commit", false, "deliver the slice as one commit on the authorized base")
	}
	raw, err := git.run(ctx, probePath, sliceDiffGitArgs(base, end, "--raw", "--no-abbrev")...)
	if err != nil {
		return nil, err
	}
	paths, statusByPath, err := parseSliceRawDiff(raw)
	if err != nil {
		return nil, err
	}
	roles, err := indexSliceDiffRoles(sliceDiffInput{ProjectID: sample.project, Files: sample.design.Architecture.Files})
	if err != nil {
		return nil, err
	}
	numstat, err := git.run(ctx, probePath, sliceDiffGitArgs(base, end, "--numstat", "--text")...)
	if err != nil {
		return nil, err
	}
	oldBlobs, newBlobs := map[string][]byte{}, map[string][]byte{}
	for _, path := range paths {
		if roles[path] != DesignArchitectureFileRoleSource {
			continue
		}
		if statusByPath[path] != "A" {
			blob, blobErr := git.run(ctx, probePath, "cat-file", "blob", base+":"+path)
			if blobErr != nil {
				return nil, blobErr
			}
			oldBlobs[path] = blob
		}
		if statusByPath[path] != "D" {
			blob, blobErr := git.run(ctx, probePath, "cat-file", "blob", end+":"+path)
			if blobErr != nil {
				return nil, blobErr
			}
			newBlobs[path] = blob
		}
	}
	bound, _ := findArchitectureSlice(*sample.design.Architecture, sample.job.ArchitectureBinding.SliceID)
	unchanged := make([]string, 0, len(sample.design.Architecture.UnchangedFiles))
	for _, file := range sample.design.Architecture.UnchangedFiles {
		if file.ProjectID == sample.project {
			unchanged = append(unchanged, file.Path)
		}
	}
	result, err := measureSliceDiff(sliceDiffInput{
		ProjectID: sample.project, RawDiff: raw, Numstat: numstat, OldBlobs: oldBlobs, NewBlobs: newBlobs,
		Files: sample.design.Architecture.Files, SliceFiles: bound.Files, Unchanged: unchanged, Limit: bound.MaxChangedLines,
	})
	if err != nil {
		return nil, err
	}
	endNow, treeNow, parentNow, err := observeSliceCleanHead(ctx, git, probePath)
	if err != nil {
		return nil, err
	}
	if endNow != end || treeNow != tree || parentNow != parent {
		return nil, newFailure(KindProjectionConflict, workerSliceConformanceOp, "the claimed worktree's clean HEAD, tree, or lineage drifted under the measurement", true, "prepare the same slice again against the settled result")
	}
	if ctx.Err() != nil {
		return nil, wrapFailure(KindTimeout, workerSliceConformanceOp, "the slice conformance probe did not finish before its whole-probe deadline", true, "retry once Git answers within the deadline", ctx.Err())
	}
	return &workerSliceConformanceMeasurement{base: base, end: end, tree: tree, algorithm: "git-diff-myers-v1", claim: entry, result: result}, nil
}

func observeSliceCleanHead(ctx context.Context, git *sliceConformanceGit, dir string) (string, string, string, error) {
	status, err := git.run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", "", "", err
	}
	if len(status) != 0 {
		return "", "", "", newFailure(KindProjectionConflict, workerSliceConformanceOp, "the claimed worktree is dirty; the conformance measures only a committed clean result", false, "commit the slice result, then prepare the conformance again")
	}
	end, err := sliceConformanceTrimLF(git.run(ctx, dir, "rev-parse", "HEAD"))
	if err != nil {
		return "", "", "", err
	}
	object, err := git.run(ctx, dir, "cat-file", "commit", end)
	if err != nil {
		return "", "", "", err
	}
	header, _, complete := bytes.Cut(object, []byte("\n\n"))
	tree, parent, parents := "", "", 0
	for i, line := range strings.Split(string(header), "\n") {
		switch {
		case i == 0 && strings.HasPrefix(line, "tree "):
			tree = strings.TrimPrefix(line, "tree ")
		case i == parents+1 && strings.HasPrefix(line, "parent "):
			parent, parents = strings.TrimPrefix(line, "parent "), parents+1
		case strings.HasPrefix(line, "tree "), strings.HasPrefix(line, "parent "):
			parents = 2 // a tree or parent header outside the opening run
		}
	}
	if !complete || parents != 1 || !worktreeSHAPattern.MatchString(end) ||
		!worktreeSHAPattern.MatchString(tree) || !worktreeSHAPattern.MatchString(parent) {
		return "", "", "", newFailure(KindGitUnreachable, workerSliceConformanceOp, "the worktree's raw HEAD or its commit header is not one raw commit with a valid tree and exactly one parent", true, "retry once the worktree resolves one raw commit")
	}
	return end, tree, parent, nil
}

func sliceConformanceTrimLF(out []byte, err error) (string, error) {
	return strings.TrimSuffix(string(out), "\n"), err
}

func sliceDiffGitArgs(base, end string, selection ...string) []string {
	return append([]string{"diff", "-z", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--diff-algorithm=myers", "--no-indent-heuristic", "--ignore-submodules=none"}, append(selection, base, end)...)
}

type sliceConformanceGit struct {
	consumed int
	overflow bool
	mu       sync.Mutex
	seam     GitRunner
}

func (g *sliceConformanceGit) take(n int) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n > maxGitOutput-g.consumed {
		g.consumed, g.overflow = maxGitOutput, true
	} else {
		g.consumed += n
	}
	if g.overflow {
		return newFailure(KindUnavailable, workerSliceConformanceOp, "the slice conformance probe spent its aggregate Git output allowance", true, "retry once the repository emits bounded output")
	}
	return nil
}

func (g *sliceConformanceGit) run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	if err := g.take(0); err != nil {
		return nil, err
	}
	if g.seam != nil {
		out, err := g.seam.Run(ctx, repo, args...)
		if err != nil {
			return nil, err
		}
		if err := g.take(len(out)); err != nil {
			return nil, err
		}
		return out, nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout, diag := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := exec.CommandContext(runCtx, "git", append([]string{"-C", repo, "--no-replace-objects", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null"}, args...)...) //nolint:gosec // fixed git binary, separate argv values, no shell.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_LAZY_FETCH=1", // newer Git honors this directly
		"GIT_ALLOW_PROTOCOL=", // empty allowlist overrides repo config and denies every transport, so older Git that ignores the variable above still cannot lazy-fetch
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); return nil }
	cmd.WaitDelay = boundedGitWaitDelay
	cmd.Stdout = &sliceConformanceGitWriter{probe: g, cancel: cancel, buf: stdout, limit: maxGitOutput}
	cmd.Stderr = &sliceConformanceGitWriter{probe: g, cancel: cancel, buf: diag, limit: 2048} // bounded diagnosis only
	runErr := cmd.Run()
	if err := g.take(0); err != nil {
		return nil, err
	}
	if runErr != nil {
		detail := strings.TrimRight("cannot run the confined Git command: "+strings.TrimSpace(diag.String()), " :")
		if ctx.Err() != nil {
			return nil, wrapFailure(KindTimeout, workerSliceConformanceOp, "the slice conformance probe did not finish before its context ended", true, "retry once Git answers within the caller's probe deadline", runErr)
		}
		return nil, wrapFailure(KindGitUnreachable, workerSliceConformanceOp, detail, true, "retry once the repository is reachable", runErr)
	}
	return stdout.Bytes(), nil
}

type sliceConformanceGitWriter struct {
	probe  *sliceConformanceGit
	cancel context.CancelFunc
	buf    *bytes.Buffer
	limit  int
}

func (w *sliceConformanceGitWriter) Write(p []byte) (int, error) {
	n := len(p)
	if err := w.probe.take(n); err != nil {
		w.cancel()
		return n, err
	}
	if room := w.limit - w.buf.Len(); len(p) > room {
		p = p[:room]
	}
	w.buf.Write(p)
	return n, nil
}
