package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Core-observed terminal output. A completed editing-lane report
// records one closed observed_delivery value the store observed itself at
// the attempt's authorized worktree, and every terminal event derives its
// consumed_context_frontier from the attempt's own dispatch authorization.
//
// No Git subprocess ever runs under a SQL transaction (CD-0195 D2). The CLI
// ingress calls PrepareWorkerTerminalDelivery before its durable transaction
// opens; that preparation samples the window, attempt, and claim identities
// in one read snapshot, closes it, and only then probes Git under one fixed
// deadline. The sealed preparation rides the context into
// ApplyWorkerEvidenceOperationTx, the existing signed terminal-ingest owner,
// which revalidates every sampled identity with SQL only and refuses on any
// change before the append. Git failure or deadline refuses the completion
// with zero terminal, derived, or nonce effects; no unavailable observation
// exists. Failed terminals never probe Git.

// workerTerminalObservationDeadline is the fixed total budget for one
// terminal delivery probe phase — all git commands together, not per
// command. It is a constant of the owning package, not configuration.
const workerTerminalObservationDeadline = 5 * time.Second

const workerTerminalObservationOp = "worker_terminal_observation"

// workerTerminalDeliveryProof is the sealed store-derived preparation: every
// field is either a SQL-sampled identity or the store's own Git observation.
// No caller JSON, packet body, or adapter value can produce one; the only
// constructor is PrepareWorkerTerminalDelivery.
type workerTerminalDeliveryProof struct {
	eventID string
	replay  bool

	windowStartSeq         int64
	windowEpoch            int64
	windowPacketDigest     string
	windowWorktreeIdentity string
	windowSubjectCommit    string
	frontier               int64
	frontierPresent        bool

	attemptState string
	laneID       string
	laneVersion  int64
	laneDigest   string

	claimPath   string
	claimBranch string
	claimOpID   string
	projectID   string
	claimState  string
	probePath   string

	observation *WorkerObservedDelivery
}

// WorkerTerminalPreparation is the opaque prepared proof the CLI ingress
// carries to the terminal ingest seam. Its contents are store-owned; a
// caller can pass it along or drop it, never alter it.
type WorkerTerminalPreparation struct {
	proof workerTerminalDeliveryProof
}

type workerTerminalPreparationKeyType struct{}

// WithWorkerTerminalPreparation returns a context that carries one sealed
// preparation into the signed terminal ingest seam.
func WithWorkerTerminalPreparation(ctx context.Context, preparation *WorkerTerminalPreparation) context.Context {
	return context.WithValue(ctx, workerTerminalPreparationKeyType{}, preparation)
}

func workerTerminalPreparationFrom(ctx context.Context) *WorkerTerminalPreparation {
	preparation, _ := ctx.Value(workerTerminalPreparationKeyType{}).(*WorkerTerminalPreparation)
	return preparation
}

// PrepareWorkerTerminalDeliveryRequest names the terminal completion the
// caller is about to authenticate. Runner injects a test GitRunner; nil runs
// the real git.
type PrepareWorkerTerminalDeliveryRequest struct {
	WorkID    string
	AttemptID string
	EventID   string
	Runner    GitRunner
}

// PrepareWorkerTerminalDelivery samples the dispatch window, attempt, and
// claim identities with plain SQL reads, closes those reads, and — only for
// a new completed editing or review attempt whose authorization recorded a
// worktree binding — observes HEAD and clean status at the claimed worktree
// under one fixed deadline. An exact already-recorded event is a replay: the
// preparation marks it and performs no Git calls. Every failure is a typed
// refusal before any transaction opens, so zero terminal, derived, or nonce
// effects exist by construction.
// workerTerminalSample is the SQL-sampled identity set one delivery
// preparation carries to the probe and the ingest revalidation. Every field
// comes from one read snapshot through SQL only; the raw worktree entry list
// is sealed as recorded, and canonical path resolution happens only after
// the snapshot closes.
type workerTerminalSample struct {
	eventFound bool

	attempt WorkerAttempt
	window  WorkerDispatchWindow
	entries []WorktreeEntry
}

// sampleWorkerTerminalDeliveryInto samples the terminal delivery state into
// the caller's sample so the preparation's read snapshot closure stays a
// single call. It performs SQL only.
func sampleWorkerTerminalDeliveryInto(ctx context.Context, q queryer, workID, attemptID, eventID string, sample *workerTerminalSample) error {
	sampled, err := sampleWorkerTerminalDelivery(ctx, q, workID, attemptID, eventID)
	if err != nil {
		return err
	}
	*sample = sampled
	return nil
}

// sampleWorkerTerminalDelivery reads the existing event identity, the
// attempt, the authorization window, and the raw worktree entry list through
// one queryer so every sampled identity observes one snapshot. It performs
// SQL only; the caller resolves canonical paths and probes Git only after
// the snapshot closes.

func sampleWorkerTerminalDelivery(ctx context.Context, q queryer, workID, attemptID, eventID string) (workerTerminalSample, error) {
	var sample workerTerminalSample
	var existingVersion int
	err := q.QueryRowContext(ctx, `SELECT payload_version FROM effect_receipts WHERE event_id=?`, eventID).Scan(&existingVersion)
	switch {
	case err == nil:
		sample.eventFound = true
		return sample, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return sample, wrapFailure(KindUnavailable, workerTerminalObservationOp, "cannot read the existing terminal event identity", true, "retry once the current rows are readable", err)
	}
	attempt, err := workerAttemptByIDCore(ctx, q, attemptID)
	if err != nil {
		return sample, err
	}
	sample.attempt = attempt
	window, err := findAuthorizedDispatchWindowCore(ctx, q, workID, attemptID)
	if err != nil {
		return sample, err
	}
	sample.window = window
	if window.WorktreeIdentity == "" {
		return sample, nil
	}
	entries, err := worktreeEntriesCore(ctx, q, workID)
	if err != nil {
		return sample, err
	}
	sample.entries = entries
	return sample, nil
}

func PrepareWorkerTerminalDelivery(ctx context.Context, s *Store, request PrepareWorkerTerminalDeliveryRequest) (*WorkerTerminalPreparation, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, workerTerminalObservationOp, "store is not open", false, "open the authority database")
	}
	if request.WorkID == "" || request.AttemptID == "" || request.EventID == "" {
		return nil, newFailure(KindInvalidPayload, workerTerminalObservationOp, "terminal delivery preparation requires the work, attempt, and event identities", false, "supply the completion identity the assertion binds")
	}
	var sample workerTerminalSample
	if err := s.readSnapshot(ctx, func(q queryer) error {
		return sampleWorkerTerminalDeliveryInto(ctx, q, request.WorkID, request.AttemptID, request.EventID, &sample)
	}); err != nil {
		return nil, err
	}
	// The read snapshot is closed; every store read below this line is the
	// ingest seam's own transactional revalidation. No git subprocess can
	// run while any SQL transaction is open.
	var proof workerTerminalDeliveryProof
	proof.eventID = request.EventID
	if sample.eventFound {
		// An exact acknowledgment returns the original observation without
		// Git: the ingest compares the stored row and appends nothing.
		proof.replay = true
		return &WorkerTerminalPreparation{proof: proof}, nil
	}
	proof.attemptState = sample.attempt.LifecycleState
	proof.laneID, proof.laneVersion, proof.laneDigest = sample.attempt.LaneID, sample.attempt.LaneVersion, sample.attempt.LaneDigest
	proof.windowStartSeq = sample.window.StartSeq
	proof.windowEpoch = sample.window.AttemptEpoch
	proof.windowPacketDigest = sample.window.PacketDigest
	proof.windowWorktreeIdentity = sample.window.WorktreeIdentity
	proof.windowSubjectCommit = sample.window.SubjectCommit
	proof.frontier, proof.frontierPresent = sample.window.ContextFrontier, sample.window.ContextFrontierPresent
	lane, err := LookupLane(sample.attempt.LaneID, sample.attempt.LaneVersion, sample.attempt.LaneDigest)
	if err != nil {
		return nil, err
	}
	editing := workerLaneEdits(sample.attempt.LaneID, sample.attempt.LaneVersion, sample.attempt.LaneDigest)
	reviewing := slices.Contains(lane.Capabilities, "inspect_diff")
	if !editing && !reviewing {
		return &WorkerTerminalPreparation{proof: proof}, nil
	}
	if sample.window.WorktreeIdentity == "" {
		// A missing historical binding remains explicit history, never
		// fabricated clean evidence.
		return &WorkerTerminalPreparation{proof: proof}, nil
	}
	entry, probePath, err := selectWorkerClaimEntry(sample.entries, sample.window.WorktreeIdentity)
	if err != nil {
		return nil, err
	}
	proof.claimPath, proof.claimBranch, proof.claimOpID, proof.projectID, proof.claimState = entry.Path, entry.Branch, entry.ClaimOpID, entry.ProjectID, entry.State
	proof.probePath = probePath
	runner := request.Runner
	if runner == nil {
		runner = ExecGitRunner{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, workerTerminalObservationDeadline)
	defer cancel()
	observation, err := observeWorkerDelivery(probeCtx, runner, proof.probePath, sample.window.SubjectCommit)
	if err != nil {
		return nil, err
	}
	if reviewing {
		if observation.State != "clean" || (!editing && observation.Commit != sample.window.SubjectCommit) {
			return nil, newFailure(KindUnauthorizedDispatch, workerTerminalObservationOp, "review requires a clean authorized output candidate", false, "restore the assigned candidate or finish and commit the scoped repair before reporting")
		}
		if observation.Commit != sample.window.SubjectCommit {
			if _, err := runner.Run(probeCtx, proof.probePath, "merge-base", "--is-ancestor", sample.window.SubjectCommit, observation.Commit); err != nil {
				return nil, workerDeliveryProbeFailure("review output ancestry", probeCtx, err)
			}
		}
	}
	proof.observation = observation
	return &WorkerTerminalPreparation{proof: proof}, nil
}

// selectWorkerClaimEntry resolves the active claim entry whose canonical path
// carries the authorization's worktree identity. It runs only after the read
// snapshot closed: canonical path resolution is filesystem work, and the
// sealed entry keeps the durable raw fields the ingest revalidates with SQL.
// An active entry whose path cannot resolve cannot be the probe target and is
// skipped; a list with no matching entry refuses.
func selectWorkerClaimEntry(entries []WorktreeEntry, identity string) (WorktreeEntry, string, error) {
	for _, entry := range entries {
		if entry.State != worktreeEntryActive {
			continue
		}
		canonical, canonicalErr := canonicalWorkerWorktreePath(entry.Path)
		if canonicalErr != nil {
			continue
		}
		if workerWorktreeIdentity(canonical) == identity {
			return entry, canonical, nil
		}
	}
	return WorktreeEntry{}, "", newFailure(KindUnauthorizedDispatch, workerTerminalObservationOp, "no active claim matches the authorization's worktree identity", false, "restore the attempt's recorded worktree claim, or close the attempt through worker-abandon")
}

// observeWorkerDelivery runs the read-only Git observation of one claimed
// worktree under the single fixed probe deadline — status, HEAD, and, for a
// clean tree whose dispatch recorded a subject commit, the changed-path set
// between that subject and HEAD under the shared diff flags. A dirty tree
// or a subject-less dispatch records no delta. A failed command or the
// deadline produces a typed refusal.
func observeWorkerDelivery(ctx context.Context, runner GitRunner, path, subject string) (*WorkerObservedDelivery, error) {
	probeCtx, cancel := context.WithTimeout(ctx, workerTerminalObservationDeadline)
	defer cancel()
	status, err := runner.Run(probeCtx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, workerDeliveryProbeFailure("clean status", probeCtx, err)
	}
	head, err := runner.Run(probeCtx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, workerDeliveryProbeFailure("HEAD commit", probeCtx, err)
	}
	commit := strings.TrimSpace(string(head))
	if !worktreeSHAPattern.MatchString(commit) {
		return nil, newFailure(KindGitUnreachable, workerTerminalObservationOp, "the worktree's HEAD is not one raw commit OID", true, "retry once the worktree resolves a commit")
	}
	observation := &WorkerObservedDelivery{Commit: commit, State: observedDeliveryState(status)}
	if observation.State != "clean" {
		return observation, nil
	}
	readTree := func(commit string) (string, error) {
		raw, err := runner.Run(probeCtx, path, "rev-parse", "--verify", commit+"^{tree}")
		if err != nil {
			return "", workerDeliveryProbeFailure("candidate tree", probeCtx, err)
		}
		tree := strings.TrimSpace(string(raw))
		if !worktreeSHAPattern.MatchString(tree) {
			return "", newFailure(KindGitUnreachable, workerTerminalObservationOp, "the candidate tree is not one raw tree OID", true, "restore the referenced Git tree and retry the completion")
		}
		return tree, nil
	}
	observation.Tree, err = readTree(commit)
	if err != nil {
		return nil, err
	}
	if subject == "" {
		return observation, nil
	}
	observation.BaseTree, err = readTree(subject)
	if err != nil {
		return nil, err
	}
	names, err := runner.Run(probeCtx, path, append(workerDiffSharedDiffArgs(), "--name-only", "-z", subject, commit)...)
	if err != nil {
		return nil, workerDeliveryProbeFailure("changed paths", probeCtx, err)
	}
	observation.Base = subject
	observation.ChangedPaths = []string{}
	for _, name := range strings.Split(string(names), "\x00") {
		if name != "" {
			observation.ChangedPaths = append(observation.ChangedPaths, name)
		}
	}
	return observation, nil
}

func observedDeliveryState(status []byte) string {
	if len(status) == 0 {
		return "clean"
	}
	return "dirty"
}

// workerDeliveryProbeFailure classifies one probe failure: the fixed
// deadline maps to the timeout kind, every other git failure to the
// git-unreachable kind. Both refuse before any effect; the attempt stays
// recoverable through the existing recovery route. A killed subprocess whose
// probe context expired is a deadline outcome, not a git failure.
func workerDeliveryProbeFailure(what string, probeCtx context.Context, err error) error {
	if probeCtx.Err() != nil {
		return wrapFailure(KindTimeout, workerTerminalObservationOp, "the terminal delivery observation exceeded its fixed probe deadline while reading "+what, true, "retry the same completion once the worktree answers", err)
	}
	return wrapFailure(KindGitUnreachable, workerTerminalObservationOp, "cannot observe the attempt's "+what, true, "retry the same completion once the worktree is reachable", err)
}

// enrichWorkerTerminalEventTx derives the terminal delivery identity inside
// the ingest transaction. The frontier comes from the attempt's own
// authorization; the observation comes only from the sealed preparation the
// ingress sampled outside this transaction. Caller-authored derived bytes
// refuse closed.
func enrichWorkerTerminalEventTx(ctx context.Context, tx *sql.Tx, event Event) (Event, error) {
	switch event.Kind {
	case WorkerCompleted, WorkerFailed:
		registration, _ := registeredEventKind(event.Kind)
		if event.PayloadVersion != registration.CurrentVersion {
			return Event{}, unsupportedEventVersion(event, registration)
		}
	}
	var rawMembers map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &rawMembers); err != nil {
		return Event{}, invalidWorkerPayload("worker terminal payload is not a JSON object")
	}
	if _, forged := rawMembers["consumed_context_frontier"]; forged {
		return Event{}, invalidWorkerPayload("consumed_context_frontier is core-derived identity; no caller can supply it")
	}
	if event.Kind == WorkerCompleted {
		if _, forged := rawMembers["observed_delivery"]; forged {
			return Event{}, invalidWorkerPayload("observed_delivery is core-derived identity; no caller can supply it")
		}
	}
	var attemptID string
	if err := json.Unmarshal(rawMembers["attempt_id"], &attemptID); err != nil || attemptID == "" {
		return Event{}, invalidWorkerPayload("worker terminal payload has no attempt identity")
	}
	window, err := findAuthorizedDispatchWindowCore(ctx, tx, event.SubjectID, attemptID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnauthorizedDispatch {
			// No authorized workflow window: the attempt was never opened by
			// a dispatch_worker action on this work item. The terminal event
			// lands without derived attribution, exactly as before.
			return event, nil
		}
		return Event{}, err
	}
	enriched := event
	switch event.Kind {
	case WorkerCompleted:
		var payload WorkerCompletedPayload
		if err := decodeClosedWorkerPayload(event, &payload); err != nil {
			return Event{}, err
		}
		deliveryErr := enrichWorkerObservedDeliveryTx(ctx, tx, event, window, &payload)
		if deliveryErr != nil {
			return Event{}, deliveryErr
		}
		if err := bindWorkerReceiptSubjectsTx(ctx, tx, event.SubjectID, payload.Evidence); err != nil {
			return Event{}, err
		}
		if window.ContextFrontierPresent || payload.ObservedDelivery != nil || len(payload.Evidence) > 0 {
			if window.ContextFrontierPresent {
				frontier := window.ContextFrontier
				payload.ConsumedContextFrontier = &frontier
			}
			raw, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				return Event{}, wrapFailure(KindInvariantViolation, workerTerminalObservationOp, "cannot encode the enriched completion payload", false, "repair the terminal delivery identity", marshalErr)
			}
			enriched.Payload = raw
		}
	case WorkerFailed:
		if window.ContextFrontierPresent {
			var payload WorkerFailedPayload
			if err := decodeClosedWorkerPayload(event, &payload); err != nil {
				return Event{}, err
			}
			frontier := window.ContextFrontier
			payload.ConsumedContextFrontier = &frontier
			raw, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				return Event{}, wrapFailure(KindInvariantViolation, workerTerminalObservationOp, "cannot encode the enriched failure payload", false, "repair the frontier attribution", marshalErr)
			}
			enriched.Payload = raw
		}
	}
	return enriched, nil
}

// enrichWorkerObservedDeliveryTx attaches the prepared delivery observation
// to a completed editing or review payload, refusing when the preparation
// is missing or any sampled identity changed under the probe.
func enrichWorkerObservedDeliveryTx(ctx context.Context, tx *sql.Tx, event Event, window WorkerDispatchWindow, payload *WorkerCompletedPayload) error {
	attempt, err := workerAttemptByIDCore(ctx, tx, payload.AttemptID)
	if err != nil {
		return err
	}
	lane, err := workerAttemptLane(ctx, tx, payload.AttemptID)
	if err != nil {
		return err
	}
	if (!slices.Contains(lane.Capabilities, "edit_scoped_files") && !slices.Contains(lane.Capabilities, "inspect_diff")) || window.WorktreeIdentity == "" {
		return nil
	}
	preparation := workerTerminalPreparationFrom(ctx)
	if preparation == nil {
		return newFailure(KindInvariantViolation, workerTerminalObservationOp, "the completed editing or review attempt reached the ingest seam without the core output observation", false, "record the completion through the authenticated worker evidence boundary")
	}
	proof := preparation.proof
	if proof.replay {
		return newFailure(KindInvariantViolation, workerTerminalObservationOp, "the prepared completion already exists; the ingest appends no second observation", false, "acknowledge the recorded event instead")
	}
	if proof.eventID != event.EventID {
		return newFailure(KindInvariantViolation, workerTerminalObservationOp, "the prepared delivery observation names a different event identity", false, "prepare the completion that is being recorded")
	}
	liveWindow, err := findAuthorizedDispatchWindowCore(ctx, tx, event.SubjectID, payload.AttemptID)
	if err != nil {
		return err
	}
	if liveWindow.StartSeq != proof.windowStartSeq || liveWindow.AttemptEpoch != proof.windowEpoch || liveWindow.PacketDigest != proof.windowPacketDigest || liveWindow.WorktreeIdentity != proof.windowWorktreeIdentity || liveWindow.SubjectCommit != proof.windowSubjectCommit || liveWindow.ContextFrontier != proof.frontier || liveWindow.ContextFrontierPresent != proof.frontierPresent {
		return newFailure(KindProjectionConflict, workerTerminalObservationOp, "the dispatch authorization changed under the terminal observation", true, "prepare the same completion again against the current authorization")
	}
	if attempt.LifecycleState != proof.attemptState || attempt.LaneID != proof.laneID || attempt.LaneVersion != proof.laneVersion || attempt.LaneDigest != proof.laneDigest {
		return newFailure(KindProjectionConflict, workerTerminalObservationOp, "the worker attempt changed under the terminal observation", true, "prepare the same completion again against the current attempt")
	}
	// The claim revalidation is SQL only: the durable entry is matched by
	// its recorded identity fields and state, never by re-resolving its
	// canonical path. The primary key is (set_id, project_id), so the
	// sampled tuple matches at most one row; a replaced or deactivated
	// claim refuses here.
	entries, err := worktreeEntriesCore(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	claimIntact := false
	for _, entry := range entries {
		if entry.ProjectID != proof.projectID || entry.ClaimOpID != proof.claimOpID || entry.Path != proof.claimPath || entry.Branch != proof.claimBranch {
			continue
		}
		if entry.State != proof.claimState {
			return newFailure(KindProjectionConflict, workerTerminalObservationOp, "the claimed worktree state changed under the terminal observation", true, "prepare the same completion again against the current claim")
		}
		claimIntact = true
	}
	if !claimIntact {
		return newFailure(KindProjectionConflict, workerTerminalObservationOp, "the claimed worktree changed under the terminal observation", true, "prepare the same completion again against the current claim")
	}
	if proof.observation == nil {
		return newFailure(KindInvariantViolation, workerTerminalObservationOp, "the prepared output proof carries no observation for an editing or review completion", false, "prepare the completion against a reachable claimed worktree")
	}
	// The observed delta is measured from the authorization's own subject
	// commit, or recorded absent for a subject-less authorization.
	if proof.observation.Base != "" && proof.observation.Base != liveWindow.SubjectCommit {
		return newFailure(KindInvariantViolation, workerTerminalObservationOp, "the prepared delivery observation names base "+proof.observation.Base+", not the dispatch's recorded subject commit", false, "prepare the completion against the recorded authorization")
	}
	payload.ObservedDelivery = proof.observation
	return nil
}

// Receipt subjects come from the retained native producer, never the packet's
// input candidate or a worker's subject claim. The fold still validates all
// control, recipe, result, and current-subject joins.
func bindWorkerReceiptSubjectsTx(ctx context.Context, q queryer, workID string, evidence []WorkerReportEvidence) error {
	for _, entry := range evidence {
		receipt := entry.OracleReceipt
		if receipt == nil || receipt.RunRef == "" {
			continue
		}
		producer, err := readOracleControlProducerTx(ctx, q, workID, receipt.RunRef, receipt.Result)
		if err != nil {
			return err
		}
		if producer == nil {
			current, err := readCurrentOracleSubject(ctx, q, workID)
			if err != nil {
				return err
			}
			producer, err = readMatchedOracleControlProducerTx(ctx, q, workID, receipt, current)
			if err != nil {
				return err
			}
		}
		if producer == nil || producer.Oracle == nil {
			return oracleFailure(KindMissingEvidence, "reported receipt has no native producer", "execute the assigned control or report unavailable")
		}
		if receipt.SubjectCommit != "" && receipt.SubjectCommit != producer.Oracle.SubjectCommit {
			return oracleFailure(KindMissingEvidence, "reported receipt subject differs from its producer", "use the producer's receipt unchanged")
		}
		receipt.SubjectCommit = producer.Oracle.SubjectCommit
	}
	return nil
}
