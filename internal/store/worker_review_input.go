package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// WorkerReviewInput is dispatch-owned context, not a worker candidate claim.
type WorkerReviewInput struct {
	InputCandidate WorkerReviewCandidate    `json:"input_candidate"`
	Dependencies   WorkerReviewDependencies `json:"dependencies"`
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
			return nil, reviewFailure(KindMissingEvidence, "Review input requires the native candidate tree observation", "prepare dispatch from the qualified clean candidate")
		}
		dependencies, err := workflowReviewDependencies(ctx, q, workID)
		if err != nil {
			return nil, err
		}
		return &WorkerReviewInput{InputCandidate: WorkerReviewCandidate{ProjectID: prepared.proof.projectID, Commit: prepared.proof.observation.Commit, Tree: prepared.proof.observation.Tree}, Dependencies: dependencies}, nil
	}
	return nil, nil
}
