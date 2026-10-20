package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
)

// workflowRecordWorkerJobSplitEvents authors the split variant: every
// successor through the one job-authoring builder, then the split record
// that binds them to the predecessor. Every check runs before any event, so
// an incomplete split, a missing control, cyclic lineage, a stale
// predecessor, or an active predecessor attempt refuses without effects.
func workflowRecordWorkerJobSplitEvents(ctx context.Context, tx *sql.Tx, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64) ([]Event, error) {
	input, err := decodeClosedWorkerJobObject[workerJobSplitInput](raw, "record_worker_job split is not one closed split object")
	if err != nil {
		return nil, err
	}
	splits, err := readWorkerJobSplitsTx(ctx, tx, request.WorkID)
	if err != nil {
		return nil, err
	}
	predecessor, err := admitWorkerJobSplitPredecessorTx(ctx, tx, request.WorkID, input.Predecessor, splits, input.ReplacedAttempt)
	if err != nil {
		return nil, err
	}
	replacement, err := admitWorkerJobSplitReplacementTx(ctx, tx, request.WorkID, actor, input.Predecessor, input.ReplacedAttempt)
	if err != nil {
		return nil, err
	}
	if len(input.Successors) < WorkerJobSplitSuccessorsMin || len(input.Successors) > WorkerJobSplitSuccessorsMax {
		return nil, workerJobSplitFailure(fmt.Sprintf("carries %d successors; %d to %d are declared", len(input.Successors), WorkerJobSplitSuccessorsMin, WorkerJobSplitSuccessorsMax), "partition into at most 64 successors and split a successor again for a larger partition")
	}
	successors := make([]WorkerJobRecordedPayload, 0, len(input.Successors))
	for _, rawSuccessor := range input.Successors {
		fields, err := workflowActionObject(rawSuccessor)
		if err != nil {
			return nil, err
		}
		jobID := workflowFieldStringDefault(fields, "job_id", "")
		if jobID == predecessor.JobID || slices.ContainsFunc(successors, func(successor WorkerJobRecordedPayload) bool { return successor.JobID == jobID }) {
			return nil, workerJobSplitFailure("names successor job "+jobID+" twice or as the predecessor", "give every successor a new distinct job_id")
		}
		next, err := nextWorkerJobRevision(ctx, tx, request.WorkID, jobID)
		if err != nil {
			return nil, err
		}
		if next != 1 {
			return nil, workerJobSplitFailure("names successor job "+jobID+", which is already recorded", "give every successor a new job_id")
		}
		payload, err := authorWorkerJobRevisionTx(ctx, tx, request, fields)
		if err != nil {
			return nil, err
		}
		successors = append(successors, *payload)
	}
	if problem := workerJobSplitCoverageProblem(*predecessor, successors); problem != "" {
		return nil, workerJobSplitFailure("is incomplete: "+problem, "carry every predecessor obligation unchanged in at least one successor, under the predecessor's cap")
	}
	if problem := workerJobSplitLineageProblem(splits, predecessor.JobID, successors); problem != "" {
		return nil, workerJobSplitFailure("has cyclic lineage: "+problem, "name no predecessor-lineage job as a successor prerequisite and keep sibling prerequisites acyclic")
	}
	if problem := workerJobSplitAllocationProblem(*predecessor, successors, input.Allocation); problem != "" {
		return nil, workerJobSplitFailure("allocation "+problem, "allocate every predecessor inventory path to a successor whose path_scope contains it")
	}
	events := make([]Event, 0, len(successors)+1)
	split := map[string]any{"predecessor": input.Predecessor, "allocation": input.Allocation}
	if replacement != nil {
		split["replaced_attempt"] = replacement
	}
	bindings := make([]WorkerJobBinding, 0, len(successors))
	for index := range successors {
		event, err := workerJobRecordedEvent(eventID+":successor:"+fmt.Sprint(index), request, actor, expected+int64(index), &successors[index])
		if err != nil {
			return nil, err
		}
		events = append(events, event)
		bindings = append(bindings, WorkerJobBinding{JobID: successors[index].JobID, Revision: successors[index].Revision, Digest: successors[index].Digest})
	}
	split["successors"] = bindings
	if predecessor.MeasurementBasis != nil {
		split["base_commit"] = predecessor.MeasurementBasis.BaseCommit
	}
	return append(events, workflowTypedEvent(eventID, WorkerJobSplit, request.WorkID, actor, request.Now, expected+int64(len(successors)), split)), nil
}

// admitWorkerJobLineageRevisionTx holds the split mapping on later
// revisions (CD-0225 D3): a replaced predecessor records no further
// revision, and a later revision of a successor keeps every predecessor
// oracle item its first revision carried, byte-equal, and every path the
// split allocated to it inside its path_scope. A successor cannot depend
// on its replaced predecessor lineage.
func admitWorkerJobLineageRevisionTx(ctx context.Context, tx *sql.Tx, workID string, payload *WorkerJobRecordedPayload) error {
	splits, err := readWorkerJobSplitsTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if workerJobSplitReplacing(splits, payload.JobID) != nil {
		return newFailure(KindIllegalLifecycleTransition, "workflow_action", "worker job "+payload.JobID+" is replaced by its split and cannot record another revision", false, "work through the split's successor jobs")
	}
	split := workerJobSplitCreating(splits, payload.JobID)
	if split == nil {
		return nil
	}
	if problem := workerJobSplitLineageProblem(splits, split.Predecessor.JobID, []WorkerJobRecordedPayload{*payload}); problem != "" {
		return workerJobSizeAuthorFailure("record_worker_job revision of split successor "+payload.JobID+" has cyclic lineage: "+problem,
			"name no job of the successor's predecessor lineage as a prerequisite")
	}
	first := split.Successors[slices.IndexFunc(split.Successors, func(successor WorkerJobBinding) bool { return successor.JobID == payload.JobID })]
	original, err := readWorkerJobRecordedTx(ctx, tx, workID, first)
	if err != nil {
		return err
	}
	predecessor, err := readWorkerJobRecordedTx(ctx, tx, workID, split.Predecessor)
	if err != nil {
		return err
	}
	if original == nil || predecessor == nil {
		return newFailure(KindInvariantViolation, "workflow_action", "a recorded split names a job revision with no job-recorded event", false, "rebuild the worker-job events from the authoritative log")
	}
	inherited := workerJobOracleItems(predecessor.AcceptanceOracle)
	revised := workerJobOracleItems(payload.AcceptanceOracle)
	for _, item := range workerJobOracleItems(original.AcceptanceOracle) {
		if slices.Contains(inherited, item) && !slices.Contains(revised, item) {
			return workerJobSizeAuthorFailure("record_worker_job revision of split successor "+payload.JobID+" drops or changes oracle item "+item.id+" the split assigned to it",
				"keep every oracle owner, case, and control the split assigned to the successor unchanged")
		}
	}
	for _, entry := range split.Allocation {
		if entry.JobID != payload.JobID {
			continue
		}
		for _, path := range entry.Paths {
			if !workerJobPathContained(payload.PathScope, path) {
				return workerJobSizeAuthorFailure("record_worker_job revision of split successor "+payload.JobID+" drops allocated path "+path+" from its path_scope",
					"keep every path the split allocated to the successor inside its path_scope")
			}
		}
	}
	return nil
}
