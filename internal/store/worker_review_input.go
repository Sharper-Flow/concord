package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// WorkerReviewInput is dispatch-owned context, not a worker candidate claim.
type WorkerReviewInput struct {
	InputCandidate     WorkerReviewCandidate           `json:"input_candidate"`
	Dependencies       WorkerReviewDependencies        `json:"dependencies"`
	Size               *WorkSizeSummary                `json:"size,omitempty"`
	RetainedFailedEdit *WorkerReviewRetainedFailedEdit `json:"retained_failed_edit,omitempty"`
}

// WorkerReviewRetainedFailedEdit discloses the unaccepted delta being reviewed.
// It carries no authority independent of the sealed dispatch preparation.
type WorkerReviewRetainedFailedEdit struct {
	AcceptedCandidate WorkerReviewCandidate `json:"accepted_candidate"`
	ChangedPaths      []string              `json:"changed_paths"`
}

// WorkerReviewRetainedPathsMaxBytes bounds the serialized path array emitted
// from the native diff's maxGitOutput bytes. Each raw byte needs at most six
// JSON bytes; each NUL separator covers quotes and a comma, plus two brackets.
const WorkerReviewRetainedPathsMaxBytes = 6*maxGitOutput + 2

func retainedFailedEditInput(proof *retainedReviewProof) *WorkerReviewRetainedFailedEdit {
	if proof == nil {
		return nil
	}
	return &WorkerReviewRetainedFailedEdit{AcceptedCandidate: proof.State.Candidate, ChangedPaths: append([]string{}, proof.ChangedPaths...)}
}

type continuityReviewGitRunner struct{}

func (continuityReviewGitRunner) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	// Continuity must not refresh the index or start a configured fsmonitor.
	return (CommittedReadGitRunner{}).Run(ctx, path, append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)...)
}

// Continuity observes the same delta owner after closing its SQL snapshot.
// This descriptor is unsealed: dispatch compares it with its own preparation.
func observeContinuityRetainedReview(ctx context.Context, input *WorkerReviewInput, sample *retainedReviewState, entries []WorktreeEntry, receipt *WorktreeVerifyResult) (*WorkerReviewRetainedFailedEdit, error) {
	if sample == nil || input.InputCandidate == sample.Candidate {
		return nil, nil
	}
	missing := func() error {
		return reviewFailure(KindMissingEvidence, "retained Review input lacks its qualified clean descendant observation", "preserve unjudged work and inspect the current candidate, claim and clean HEAD; qualify and reread continuity only after they identify the same scoped descendant; if no such descendant exists, request maintainer investigation rather than repeating qualification of the unchanged mismatched HEAD")
	}
	entry, path, found := selectDispatchClaimEntry(entries, receipt.Path)
	if !found || entry.ProjectID != input.InputCandidate.ProjectID {
		return nil, missing()
	}
	runner := continuityReviewGitRunner{}
	observation, err := observeDispatchSubject(ctx, runner, path)
	if err != nil {
		return nil, err
	}
	if observation.State != "clean" || observation.Commit != input.InputCandidate.Commit || observation.Tree != input.InputCandidate.Tree {
		return nil, missing()
	}
	proof, err := observeRetainedReview(ctx, runner, &workerDispatchProof{probePath: path, observation: observation}, sample, entry)
	if err != nil {
		return nil, err
	}
	if proof == nil {
		return nil, missing()
	}
	return retainedFailedEditInput(proof), nil
}

// readReviewCandidateTree uses the same immutable Git tree lookup as terminal
// acceptance. Callers must close their SQL snapshot before invoking it.
func readReviewCandidateTree(ctx context.Context, runner GitRunner, path, commit string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, workerDispatchObservationDeadline)
	defer cancel()
	raw, err := runner.Run(probeCtx, path, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return "", dispatchSubjectProbeFailure("candidate tree", probeCtx, err)
	}
	tree := strings.TrimSpace(string(raw))
	if !worktreeSHAPattern.MatchString(tree) {
		return "", newFailure(KindGitUnreachable, workerDispatchFreshnessOp, "the candidate tree is not one raw tree OID", true, "restore the referenced Git tree")
	}
	return tree, nil
}

func validateWorkerReviewInput(ctx context.Context, tx *sql.Tx, workID string, lane LaneDefinition, definition WorkflowDefinition, packetRaw json.RawMessage) error {
	var packet struct {
		Inputs struct {
			ReviewInput json.RawMessage `json:"review_input"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return err
	}
	expected, err := currentWorkerReviewInput(ctx, tx, workID, lane, definition)
	if err != nil {
		return err
	}
	return compareWorkerPacketRecord[WorkerReviewInput]("review_input", packet.Inputs.ReviewInput, expected)
}

func currentWorkerReviewInput(ctx context.Context, q queryer, workID string, lane LaneDefinition, definition WorkflowDefinition) (*WorkerReviewInput, error) {
	if lane.CapabilityClass == "review" && workflowDefinitionProducesDelivery(definition) {
		prepared := workerDispatchPreparationFrom(ctx)
		if prepared == nil || prepared.proof.observation == nil || prepared.proof.observation.Tree == "" {
			return nil, reviewFailure(KindMissingEvidence, "Review input requires the native candidate tree observation", workflowReviewRecovery("prepare dispatch from the qualified clean candidate with its native tree observation", "covering that candidate"))
		}
		dependencies, err := workflowReviewDependencies(ctx, q, workID)
		if err != nil {
			return nil, err
		}
		size, err := readWorkSizeSummary(ctx, q, workID)
		if err != nil {
			return nil, err
		}
		return &WorkerReviewInput{InputCandidate: WorkerReviewCandidate{ProjectID: prepared.proof.projectID, Commit: prepared.proof.observation.Commit, Tree: prepared.proof.observation.Tree}, Dependencies: dependencies, Size: size, RetainedFailedEdit: retainedFailedEditInput(prepared.proof.retainedReview)}, nil
	}
	return nil, nil
}
