package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Core-owned dispatch subject freshness. The dispatch admission is the only
// live subject-freshness owner: when the admitted work-context view pins a
// qualified subject, the claimed worktree's live clean HEAD must equal that
// subject before any attempt, window, or start exists. Review joins that
// qualified subject to Main's current integrated candidate, including baseline
// Review before any implementation result exists.
//
// No Git subprocess ever runs under a SQL transaction (CD-0195 D2). The
// authenticated mutation boundary calls PrepareWorkerDispatchFreshness after
// its replay, budget, and approval handling and before the action
// transaction opens; that preparation samples the work version, active
// contract and step, canonical work-context view, and raw worktree claim
// entries in one read snapshot, closes it, resolves the canonical claim
// path, and only then probes Git under one fixed deadline. The sealed
// preparation rides the context into the dispatch admission, which
// revalidates every sampled identity with SQL only and refuses on any
// change before the dispatch completion. Git failure or deadline refuses the
// mutation before any effect; a subject-less non-oracle dispatch probes
// nothing. A combined rejection that carries a retry prepares the same one
// proof before the outer transaction, and the derived retry inside the
// transaction reuses that proof without a second probe.
//
// Live assembly requires preparation; recorded history replays without a
// probe. No path substitutes prose or a caller value for the core observation.
// The review lane checks HEAD/tree again for drift after authorization.

// workerDispatchObservationDeadline is the fixed total budget for one
// dispatch subject probe phase — status, commit and tree together, not per
// command. It is a constant of the owning package, not configuration.
const workerDispatchObservationDeadline = 5 * time.Second

const workerDispatchFreshnessOp = "worker_dispatch_freshness"

// workerDispatchProof is the sealed store-derived preparation: every field
// is either a SQL-sampled identity or the store's own Git observation. No
// caller JSON, packet body, or adapter value can produce one; the only
// constructors delegate to the native preparation owner below.
type workerDispatchProof struct {
	workID string

	workVersion     int64
	instanceFound   bool
	currentStep     string
	contractVersion int64

	sessionIdentity string

	viewPresent bool
	viewDigest  string

	claimPath   string
	claimBranch string
	claimOpID   string
	projectID   string
	claimState  string
	probePath   string

	observation *WorkerObservedDelivery
	acceptance  bool
	pinReceipt  *WorktreeVerifyResult

	// readings carries one sealed witness per declared required reading:
	// the SQL-sampled source identities sampled in the same read snapshot,
	// plus the Git resolution resolved after that snapshot closed.
	readings []dispatchReadingWitness
}

// WorkerDispatchPreparation is the opaque prepared proof the authenticated
// mutation boundary carries to the dispatch admission. Its contents are
// store-owned; a caller can pass it along or drop it, never alter it.
type WorkerDispatchPreparation struct {
	proof workerDispatchProof
}

type workerDispatchPreparationKeyType struct{}

// WithWorkerDispatchPreparation returns a context that carries one sealed
// preparation into the dispatch admission.
func WithWorkerDispatchPreparation(ctx context.Context, preparation *WorkerDispatchPreparation) context.Context {
	return context.WithValue(ctx, workerDispatchPreparationKeyType{}, preparation)
}

func workerDispatchPreparationFrom(ctx context.Context) *WorkerDispatchPreparation {
	preparation, _ := ctx.Value(workerDispatchPreparationKeyType{}).(*WorkerDispatchPreparation)
	return preparation
}

type workerDispatchRetryRecursionKeyType struct{}

// withWorkerDispatchRetryRecursion marks the derived retry dispatch a
// combined rejection carries, so the admission revalidates only the
// identities the rejection cannot lawfully move: the version and step the
// rejection itself advanced stay at the outer entry's own revalidation.
func withWorkerDispatchRetryRecursion(ctx context.Context) context.Context {
	return context.WithValue(ctx, workerDispatchRetryRecursionKeyType{}, true)
}

func workerDispatchRetryRecursionFrom(ctx context.Context) bool {
	marked, _ := ctx.Value(workerDispatchRetryRecursionKeyType{}).(bool)
	return marked
}

// PrepareWorkerDispatchRequest names the dispatch mutation the caller is
// about to authorize. Runner injects a test GitRunner; nil runs the real
// git.
type PrepareWorkerDispatchRequest struct {
	WorkID          string
	SessionWorktree string
	Runner          GitRunner
	// WorkerJob is the dispatched job binding the dispatch payload carried,
	// resolved by the authenticated mutation boundary before preparation.
	// When it names a capped job, its recorded base_commit anchors the
	// correction's measured delta.
	WorkerJob *WorkerJobBinding
	// WorkerAttemptID is the rejected attempt a combined rejection's derived
	// retry re-dispatches; its authorization's job binding anchors the same
	// probe when the retry carries no packet of its own.
	WorkerAttemptID string
}

// workerDispatchSample is the SQL-sampled identity set one dispatch
// preparation carries to the probe and the admission revalidation.
type workerDispatchSample struct {
	version         int64
	instanceFound   bool
	currentStep     string
	contractVersion int64

	view    *WorkContextView
	viewRaw []byte
	entries []WorktreeEntry

	// readings holds the SQL-sampled source identities of the view's
	// declared required readings, from the same snapshot.
	readings   []dispatchReadingWitness
	pinReceipt *WorktreeVerifyResult
}

// sampleWorkerDispatchStateInto samples the dispatch state into the
// caller's sample so the preparation's read snapshot closure stays a single
// call. It performs SQL only.
func sampleWorkerDispatchStateInto(ctx context.Context, q queryer, request PrepareWorkerDispatchRequest, sample *workerDispatchSample) error {
	sampled, err := sampleWorkerDispatchState(ctx, q, request)
	if err != nil {
		return err
	}
	*sample = sampled
	return nil
}

// sampleWorkerDispatchState reads the work version, the workflow instance
// step, the active contract, the canonical work-context view, and the raw
// worktree entry list through one queryer so every sampled identity observes
// one snapshot. It performs SQL only; the caller resolves canonical paths
// and probes Git only after the snapshot closes.
func sampleWorkerDispatchState(ctx context.Context, q queryer, request PrepareWorkerDispatchRequest) (workerDispatchSample, error) {
	workID := request.WorkID
	var sample workerDispatchSample
	if err := q.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&sample.version); err != nil {
		if err == sql.ErrNoRows {
			return sample, newFailure(KindProjectionNotFound, workerDispatchFreshnessOp, "the work item is not recorded", false, "reread_entities")
		}
		return sample, wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot read the work version", true, "retry once the work item is readable", err)
	}
	var step string
	err := q.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step)
	switch {
	case err == nil:
		sample.instanceFound = true
		sample.currentStep = step
	case err == sql.ErrNoRows:
	default:
		return sample, wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot read the workflow step", true, "retry once the workflow projection is readable", err)
	}
	if sample.instanceFound {
		contractVersion, contractErr := activeWorkflowContractVersion(ctx, q, workID, workerDispatchFreshnessOp)
		if contractErr == nil {
			sample.contractVersion = contractVersion
		} else if contractErr != sql.ErrNoRows {
			return sample, contractErr
		}
	}
	view, viewErr := readWorkContextView(ctx, q, workID)
	if viewErr != nil {
		return sample, viewErr
	}
	sample.view = view
	if view != nil {
		raw, marshalErr := json.Marshal(view)
		if marshalErr != nil {
			return sample, wrapFailure(KindInvariantViolation, workerDispatchFreshnessOp, "cannot encode the sampled work-context view", false, "repair the work-context projection", marshalErr)
		}
		sample.viewRaw = raw
	}
	entries, entriesErr := worktreeEntriesCore(ctx, q, workID)
	if entriesErr != nil {
		return sample, entriesErr
	}
	sample.entries = entries
	if view != nil && len(view.RequiredReading) > 0 {
		witnesses, witnessErr := sampleDispatchReadingIdentities(ctx, q, workID, view.RequiredReading)
		if witnessErr != nil {
			return sample, witnessErr
		}
		sample.readings = witnesses
	}
	sample.pinReceipt, err = readCurrentOracleSubjectReceipt(ctx, q, workID)
	if err != nil {
		return sample, err
	}
	return sample, nil
}

// PrepareWorkerDispatchFreshness samples the dispatch identities with plain
// SQL reads, closes those reads, and — only when the sampled work-context
// view pins a qualified subject and an active claim matches the session
// worktree — observes HEAD, its tree and clean status at the claimed worktree under
// one fixed deadline. Every failure is a typed refusal before any
// transaction opens, so zero dispatch effects exist by construction. A
// subject-less view performs no probe.
func PrepareWorkerDispatchFreshness(ctx context.Context, s *Store, request PrepareWorkerDispatchRequest) (*WorkerDispatchPreparation, error) {
	return prepareWorkerSubjectFreshness(ctx, s, request, false)
}

// PrepareWorkflowAcceptanceFreshness uses the same sealed native owner as
// dispatch. It probes the active worktree that owns the current subject pin.
func PrepareWorkflowAcceptanceFreshness(ctx context.Context, s *Store, request PrepareWorkerDispatchRequest) (*WorkerDispatchPreparation, error) {
	return prepareWorkerSubjectFreshness(ctx, s, request, true)
}

func prepareWorkerSubjectFreshness(ctx context.Context, s *Store, request PrepareWorkerDispatchRequest, acceptance bool) (*WorkerDispatchPreparation, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, workerDispatchFreshnessOp, "store is not open", false, "open the authority database")
	}
	if request.WorkID == "" {
		return nil, newFailure(KindInvalidPayload, workerDispatchFreshnessOp, "dispatch freshness preparation requires the work identity", false, "supply the work item the mutation authorizes")
	}
	var sample workerDispatchSample
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return sampleWorkerDispatchStateInto(ctx, q, request, &sample)
	}); err != nil {
		return nil, err
	}
	// The read snapshot is closed; every store read below this line is the
	// admission's own transactional revalidation. No git subprocess can run
	// while any SQL transaction is open.
	var proof workerDispatchProof
	proof.workID = request.WorkID
	proof.workVersion = sample.version
	proof.instanceFound = sample.instanceFound
	proof.currentStep = sample.currentStep
	proof.contractVersion = sample.contractVersion
	proof.acceptance = acceptance
	proof.pinReceipt = sample.pinReceipt
	proof.viewPresent = sample.view != nil
	if sample.view != nil {
		proof.viewDigest = nativeDigest(sample.viewRaw)
	}
	entries := sample.entries
	if acceptance && sample.pinReceipt != nil {
		request.SessionWorktree = sample.pinReceipt.Path
		entries = nil
		for _, candidate := range sample.entries {
			if candidate.ProjectID == sample.pinReceipt.ProjectID {
				entries = append(entries, candidate)
			}
		}
	}
	canonicalSession, canonicalErr := canonicalWorkerWorktreePath(request.SessionWorktree)
	if canonicalErr == nil {
		proof.sessionIdentity = workerWorktreeIdentity(canonicalSession)
	}
	entry, probePath, claimFound := selectDispatchClaimEntry(entries, request.SessionWorktree)
	if claimFound {
		proof.claimPath, proof.claimBranch, proof.claimOpID, proof.projectID, proof.claimState = entry.Path, entry.Branch, entry.ClaimOpID, entry.ProjectID, entry.State
		proof.probePath = probePath
	}
	if acceptance && sample.pinReceipt != nil && (!claimFound || entry.Path != sample.pinReceipt.Path || entry.ProjectID != sample.pinReceipt.ProjectID) {
		return nil, newFailure(KindStaleRequiresReview, workerDispatchFreshnessOp, "terminal acceptance cannot establish the current subject's claimed worktree", false, "claim and verify the subject's worktree")
	}
	runner := request.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	// The declared readings resolve after the snapshot closed, under one
	// shared fixed deadline across every reading and both arms, on the
	// committed-read policy runner: no lazy fetch, no replacement refs. The
	// subject probe below keeps its own deadline; neither phase holds any
	// SQL.
	if len(sample.readings) > 0 && !acceptance {
		proof.readings = sample.readings
		resolutionRunner := request.Runner
		if resolutionRunner == nil {
			resolutionRunner = CommittedReadGitRunner{}
		}
		if err := resolveDispatchReadingWitnesses(ctx, resolutionRunner, proof.readings); err != nil {
			return nil, err
		}
	}
	hasSubject := sample.view != nil && sample.view.SubjectCommit != ""
	if acceptance {
		hasSubject = sample.pinReceipt != nil
	}
	if !hasSubject || probePath == "" {
		// A subject-less non-oracle dispatch, or a dispatch whose session
		// worktree matches no active claim, probes nothing: the admission's
		// own claim check refuses the latter, and absence of a subject
		// grants no freshness comparison.
		return &WorkerDispatchPreparation{proof: proof}, nil
	}
	observation, err := observeDispatchSubject(ctx, runner, probePath)
	if err != nil {
		return nil, err
	}
	proof.observation = observation
	return &WorkerDispatchPreparation{proof: proof}, nil
}

// selectDispatchClaimEntry resolves the active claim entry whose canonical
// path is the session worktree. It runs only after the read snapshot closed:
// canonical path resolution is filesystem work, and the sealed entry keeps
// the durable raw fields the admission revalidates with SQL. A list with no
// matching entry reports claimFound false; the admission's own claim check
// refuses that dispatch.
func selectDispatchClaimEntry(entries []WorktreeEntry, sessionWorktree string) (WorktreeEntry, string, bool) {
	canonical, canonicalErr := canonicalWorkerWorktreePath(sessionWorktree)
	if canonicalErr != nil {
		return WorktreeEntry{}, "", false
	}
	identity := workerWorktreeIdentity(canonical)
	for _, entry := range entries {
		if entry.State != worktreeEntryActive {
			continue
		}
		entryCanonical, entryErr := canonicalWorkerWorktreePath(entry.Path)
		if entryErr != nil {
			continue
		}
		if workerWorktreeIdentity(entryCanonical) == identity {
			return entry, canonical, true
		}
	}
	return WorktreeEntry{}, "", false
}

// observeDispatchSubject runs the read-only Git observation of one claimed
// worktree under the single fixed probe deadline. A failed command or the
// deadline produces a typed refusal before any effect.
func observeDispatchSubject(ctx context.Context, runner GitRunner, path string) (*WorkerObservedDelivery, error) {
	probeCtx, cancel := context.WithTimeout(ctx, workerDispatchObservationDeadline)
	defer cancel()
	status, err := runner.Run(probeCtx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, dispatchSubjectProbeFailure("clean status", probeCtx, err)
	}
	head, err := runner.Run(probeCtx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, dispatchSubjectProbeFailure("HEAD commit", probeCtx, err)
	}
	commit := strings.TrimSpace(string(head))
	if !worktreeSHAPattern.MatchString(commit) {
		return nil, newFailure(KindGitUnreachable, workerDispatchFreshnessOp, "the claimed worktree's HEAD is not one raw commit OID", true, "retry once the worktree resolves a commit")
	}
	observation := &WorkerObservedDelivery{Commit: commit, State: observedDeliveryState(status)}
	if observation.State == "clean" {
		observation.Tree, err = readReviewCandidateTree(probeCtx, runner, path, commit)
		if err != nil {
			return nil, err
		}
	}
	return observation, nil
}

// dispatchSubjectProbeFailure classifies one probe failure: the fixed
// deadline maps to the timeout kind, every other git failure to the
// git-unreachable kind. Both refuse before any effect.
func dispatchSubjectProbeFailure(what string, probeCtx context.Context, err error) error {
	if probeCtx.Err() != nil {
		return wrapFailure(KindTimeout, workerDispatchFreshnessOp, "the dispatch subject observation exceeded its fixed probe deadline while reading "+what, true, "retry the dispatch once the worktree answers", err)
	}
	return wrapFailure(KindGitUnreachable, workerDispatchFreshnessOp, "cannot observe the claimed worktree's "+what, true, "restore the referenced objects or repair repository access, then prepare the dispatch again", err)
}

// admitWorkerDispatchSubjectFreshness is the transactional half of the one
// dispatch freshness owner. It revalidates the sealed preparation's sampled
// identities with SQL only, refuses when the live clean HEAD does not match
// the admitted subject, and joins the current integrated candidate for
// a review dispatch. Missing preparation refuses live assembly.
func admitWorkerDispatchSubjectFreshness(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, definition WorkflowDefinition, lane LaneDefinition, admittedContext *WorkContextView) error {
	proof := workerDispatchPreparationFrom(ctx)
	if proof == nil {
		return newFailure(KindInvalidOperation, workerDispatchFreshnessOp, "the dispatch reached live action assembly without its sealed preparation", false, "run the dispatch through the authenticated mutation boundary, which prepares one proof per dispatch before the transaction opens")
	}
	if proof.proof.acceptance {
		return newFailure(KindInvalidOperation, workerDispatchFreshnessOp, "dispatch requires its own sealed preparation, not a terminal acceptance preparation", false, "prepare the dispatch through its native mutation boundary")
	}
	if proof.proof.workID != request.WorkID {
		return newFailure(KindInvalidPayload, workerDispatchFreshnessOp, "the sealed preparation names work "+proof.proof.workID+", not this dispatch's work", false, "prepare the dispatch this mutation authorizes")
	}
	if request.SessionWorktreeIdentity != proof.proof.sessionIdentity {
		return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the dispatch's session worktree does not match the sealed preparation's claim identity", true, "prepare the dispatch again from the claimed worktree")
	}
	if err := proof.revalidateDispatchIdentitiesTx(ctx, tx, request.WorkID, !workerDispatchRetryRecursionFrom(ctx)); err != nil {
		return err
	}
	subject := ""
	if admittedContext != nil {
		subject = admittedContext.SubjectCommit
	}
	if subject != "" && !proof.observationMatches(subject) {
		return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the claimed worktree's live clean HEAD does not match the admitted dispatch subject "+subject, true, "re-qualify the candidate at the claimed worktree, then rebuild the packet and dispatch again")
	}
	if lane.CapabilityClass == "review" && workflowDefinitionProducesDelivery(definition) {
		return validateReviewAcceptedDeliveryTx(ctx, tx, request.WorkID, subject, request.SessionWorktreeIdentity)
	}
	return nil
}

// observationMatches reports whether the prepared observation is one clean
// observation of exactly the named commit.
func (p *WorkerDispatchPreparation) observationMatches(subject string) bool {
	return p.proof.observation != nil && p.proof.observation.State == "clean" && p.proof.observation.Commit == subject
}

// revalidateDispatchIdentitiesTx compares the sealed preparation's sampled
// identities against current state with SQL only, refusing on any change
// under the probe. The full form covers the work version, the workflow step,
// the active contract, and the canonical work-context view; the derived-retry
// form covers only the identities a rejection cannot lawfully move (the
// view, the claim, and through them the subject), because the rejection
// itself legitimately advances the version and step between the outer entry
// and the derived dispatch.
func (p *WorkerDispatchPreparation) revalidateDispatchIdentitiesTx(ctx context.Context, tx *sql.Tx, workID string, full bool) error {
	if full {
		version, lifecycleErr := workflowInstanceStateVersionTx(ctx, tx, workID)
		if lifecycleErr != nil {
			return lifecycleErr
		}
		if version != p.proof.workVersion {
			return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the work item changed under the dispatch subject observation", true, "prepare the same dispatch again against the current work")
		}
		if p.proof.instanceFound {
			var currentStep string
			if err := tx.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
				if err == sql.ErrNoRows {
					return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the workflow instance left the work under the dispatch subject observation", true, "prepare the same dispatch again against the current workflow")
				}
				return wrapFailure(KindUnavailable, workerDispatchFreshnessOp, "cannot read the workflow step", true, "retry once the workflow projection is readable", err)
			}
			if currentStep != p.proof.currentStep {
				return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the workflow step changed under the dispatch subject observation", true, "prepare the same dispatch again against the current workflow")
			}
			contractVersion, contractErr := activeWorkflowContractVersion(ctx, tx, workID, workerDispatchFreshnessOp)
			if contractErr != nil && contractErr != sql.ErrNoRows {
				return contractErr
			}
			effective := int64(0)
			if contractErr == nil {
				effective = contractVersion
			}
			if effective != p.proof.contractVersion {
				return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the active workflow contract changed under the dispatch subject observation", true, "prepare the same dispatch again against the current contract")
			}
		}
	}
	view, viewErr := readWorkContextView(ctx, tx, workID)
	if viewErr != nil {
		return viewErr
	}
	if (view != nil) != p.proof.viewPresent {
		return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the work context changed under the dispatch subject observation", true, "prepare the same dispatch again against the current work context")
	}
	if view != nil {
		raw, marshalErr := json.Marshal(view)
		if marshalErr != nil {
			return wrapFailure(KindInvariantViolation, workerDispatchFreshnessOp, "cannot encode the current work-context view", false, "repair the work-context projection", marshalErr)
		}
		if nativeDigest(raw) != p.proof.viewDigest {
			return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the work context changed under the dispatch subject observation", true, "prepare the same dispatch again against the current work context")
		}
	}
	return p.revalidateDispatchClaimTx(ctx, tx, workID)
}

// revalidateDispatchClaimTx is the SQL-only claim revalidation: the durable
// entry is matched by its recorded identity fields and state, never by
// re-resolving its canonical path. The primary key is (set_id, project_id),
// so the sampled tuple matches at most one row; a replaced or deactivated
// claim refuses here.
func (p *WorkerDispatchPreparation) revalidateDispatchClaimTx(ctx context.Context, tx *sql.Tx, workID string) error {
	entries, err := worktreeEntriesCore(ctx, tx, workID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.ProjectID != p.proof.projectID || entry.ClaimOpID != p.proof.claimOpID || entry.Path != p.proof.claimPath || entry.Branch != p.proof.claimBranch {
			continue
		}
		if entry.State != p.proof.claimState {
			return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the claimed worktree state changed under the dispatch subject observation", true, "prepare the same dispatch again against the current claim")
		}
		return nil
	}
	if p.proof.claimPath == "" {
		return nil
	}
	return newFailure(KindProjectionConflict, workerDispatchFreshnessOp, "the claimed worktree changed under the dispatch subject observation", true, "prepare the same dispatch again against the current claim")
}

// revalidateDispatchProofForRetryTx revalidates the shared proof at the
// derived retry dispatch: a changed work-context view or claim cannot borrow
// the proof prepared before the outer rejection transaction. The rejection
// legitimately moved the version and step, so those comparisons stay at the
// outer entry.
func revalidateDispatchProofForRetryTx(ctx context.Context, tx *sql.Tx, workID string) error {
	proof := workerDispatchPreparationFrom(ctx)
	if proof == nil {
		return newFailure(KindInvalidOperation, workerDispatchFreshnessOp, "the derived retry dispatch reached live assembly without its sealed preparation", false, "run the combined rejection through the authenticated mutation boundary, which prepares one proof before the transaction opens")
	}
	return proof.revalidateDispatchIdentitiesTx(ctx, tx, workID, false)
}

// revalidateDispatchProofAtRejectEntry revalidates the shared proof at the
// outer rejection entry, against the pre-rejection state the preparation
// sampled.
func revalidateDispatchProofAtRejectEntry(ctx context.Context, tx *sql.Tx, workID string) error {
	proof := workerDispatchPreparationFrom(ctx)
	if proof == nil {
		return newFailure(KindInvalidOperation, workerDispatchFreshnessOp, "the combined rejection reached live action assembly without its sealed preparation", false, "run the combined rejection through the authenticated mutation boundary, which prepares one proof before the transaction opens")
	}
	return proof.revalidateDispatchIdentitiesTx(ctx, tx, workID, true)
}

// workflowDefinitionProducesDelivery reports whether a pinned workflow
// produces code delivery whose progression requires an accepted Review.
func workflowDefinitionProducesDelivery(definition WorkflowDefinition) bool {
	return workflowCapabilities(definition).has(WorkflowCapabilityProducesDelivery)
}

// Review consumes the current integrated candidate, or the qualified initial
// baseline before Main has accepted any result. An earlier Implement result
// is not a prerequisite for obtaining baseline continuation authority.
func validateReviewAcceptedDeliveryTx(ctx context.Context, tx *sql.Tx, workID, subject, worktreeIdentity string) error {
	if subject == "" {
		return newFailure(KindUnauthorizedDispatch, workerDispatchFreshnessOp, "review of implementation work requires a nonempty qualified subject; qualify the accepted candidate through worktree-verify before review", false, "run worktree-verify on the clean accepted candidate, then rebuild the packet")
	}
	receipt, err := readCurrentOracleSubjectReceipt(ctx, tx, workID)
	if err != nil {
		return err
	}
	if receipt == nil || receipt.SubjectRef != "commit:"+subject || workerWorktreeIdentity(receipt.Path) != worktreeIdentity {
		return reviewFailure(KindMissingEvidence, "Review input is not the qualified candidate on its claimed worktree", "qualify the clean current candidate through worktree-verify, then dispatch Review")
	}
	state, err := loadWorkflowReviewState(ctx, tx, workID)
	if err != nil {
		return err
	}
	if state.Candidate != nil && (state.Candidate.Commit != subject || state.Candidate.ProjectID != receipt.ProjectID) {
		return reviewFailure(KindStaleRequiresReview, "Review input differs from Main's current integrated candidate", "dispatch Review on the accepted current candidate")
	}
	return nil
}
