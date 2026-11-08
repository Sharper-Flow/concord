package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

func workflowClauseFailure(kind FailureKind, clause int, detail, recovery string) *Failure {
	failure := newFailure(kind, "complete_workflow", detail, false, recovery)
	failure.Clause = clause
	return failure
}

func workflowClauseError(err error, clause int) error {
	var failure *Failure
	if failureAs(err, &failure) {
		if failure.Clause == 0 {
			failure.Clause = clause
		}
		return failure
	}
	return workflowClauseFailure(KindOperationConflict, clause, "workflow completion clause failed", "reconcile_operation")
}

func workflowCompletionRequired() error {
	return newFailure(KindWorkflowCompletionRequired, "append_event", "workflow.completed is reserved for the ordered completion gate", false, "use the ordered workflow completion entry point")
}

func completeWorkflowInScopeTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, payload workflowCompletedPayload) error {
	// The expected version is checked before any other read.  This is the lock/fence
	// boundary described by the contract; every subsequent mutation uses the
	// same transaction and therefore sees one consistent projection.
	if err := workflowCompletionVersion(ctx, tx, event.SubjectID, payload.ExpectedVersion); err != nil {
		return err
	}
	if _, err := completionActiveContractVersion(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	return appendWorkflowCompletionTx(ctx, tx, event, scope, payload)
}

func completionActiveContractVersion(ctx context.Context, q queryer, workID string) (int64, error) {
	version, err := activeWorkflowContractVersion(ctx, q, workID, "complete_workflow")
	if err == sql.ErrNoRows {
		return 0, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract is missing or ambiguous", false, "reread_entities")
	}
	return version, err
}

const workflowCompletionTeaching = "Before complete, confirm_premise for the active contract and record delivery for change, fix, or removal. Research requires premise confirmation but not delivery."

// The active premise confirmation and delivery are recorded terminal facts.
func requireWorkflowCompletionFactsTx(ctx context.Context, q queryer, workID string, planType WorkflowStepPlanType, contractVersion int64) error {
	var confirmed int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=?)`, workID, contractVersion).Scan(&confirmed); err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot read the premise confirmation", true, "retry once the database is readable", err)
	}
	if confirmed == 0 {
		return newFailure(KindMissingEvidence, "complete_workflow", "the active contract premise is not confirmed", false, "confirm the active contract premise")
	}
	if planType == WorkflowStepPlanResearch {
		return nil
	}
	var delivered int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_delivery_state WHERE work_id=?)`, workID).Scan(&delivered); err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot read the delivery assertion", true, "retry once the database is readable", err)
	}
	if delivered == 0 {
		return newFailure(KindMissingEvidence, "complete_workflow", "delivery assertion is missing", false, "record delivery")
	}
	return nil
}

func appendWorkflowCompletionTx(ctx context.Context, tx *sql.Tx, event Event, scope *writeScope, payload workflowCompletedPayload) error {
	nextVersion := *payload.ExpectedVersion
	completion := event
	var completionPayload map[string]any
	if err := json.Unmarshal(completion.Payload, &completionPayload); err != nil {
		return workflowClauseFailure(KindInvalidPayload, 7, "completion metadata is malformed", "supply closed terminal metadata")
	}
	completionPayload["expected_version"] = nextVersion
	completionPayload["resulting_version"] = nextVersion + 1
	completion.Payload, _ = json.Marshal(completionPayload)
	seq, err := appendEvent(ctx, tx, completion, true)
	if err != nil {
		return workflowClauseFailure(KindOperationConflict, 7, "completion append conflicted", "reconcile_operation")
	}
	completion.Seq = seq
	scope.eventIDs = append(scope.eventIDs, completion.EventID)
	if err := applyEffect(ctx, tx, completion); err != nil {
		return workflowClauseError(err, 7)
	}
	if err := advanceWorkEffectSeqTx(ctx, tx, completion); err != nil {
		return workflowClauseError(err, 7)
	}
	// The workflow's terminal state closes the work item
	// lifecycle in this same transaction, so the lifecycle follows the
	// workflow instead of stranding in_progress after a recorded completion.
	// The completion fold has already marked the instance terminal, so the
	// terminal-lifecycle close keeps that record. Replay folds the logged
	// transition exactly as it folded here, because the event is part of the log.
	if err := appendWorkflowTerminalLifecycleTx(ctx, tx, scope, completion, payload.TerminalState); err != nil {
		return workflowClauseError(err, 7)
	}
	return nil
}

// appendWorkflowTerminalLifecycleTx appends and folds the work.transitioned
// event that moves the work item to the workflow's terminal state. The
// completion event must already be folded: the transition's fold keeps the
// instance record only because the instance is terminal when it runs.
func appendWorkflowTerminalLifecycleTx(ctx context.Context, tx *sql.Tx, scope *writeScope, completion Event, terminalState string) error {
	var from string
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle,version FROM work_items WHERE id=?`, completion.SubjectID).Scan(&from, &version); err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot read the work item for its terminal lifecycle", true, "retry once the database is readable", err)
	}
	payload, err := json.Marshal(map[string]any{"from": from, "to": terminalState, "reason": "workflow " + terminalState, "evidence_refs": []string{}, "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		return newFailure(KindInvariantViolation, "complete_workflow", "terminal lifecycle payload cannot be encoded", false, "repair the workflow completion path")
	}
	transition := Event{EventID: completion.EventID + ":lifecycle", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: completion.SubjectID, Actor: completion.Actor, OccurredAt: completion.OccurredAt, PayloadVersion: 1, Payload: payload}
	seq, err := appendEvent(ctx, tx, transition, true)
	if err != nil {
		return err
	}
	transition.Seq = seq
	scope.eventIDs = append(scope.eventIDs, transition.EventID)
	if err := applyEffect(ctx, tx, transition); err != nil {
		return err
	}
	return advanceWorkEffectSeqTx(ctx, tx, transition)
}

func workflowCompletionVersion(ctx context.Context, tx *sql.Tx, workID string, expected *int64) error {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindInvariantViolation, "complete_workflow", "workflow work item is missing", false, "reread_entities")
		}
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot lock workflow work item", true, "retry once the database is readable", err)
	}
	if expected == nil || *expected != version {
		return newFailure(KindOperationConflict, "complete_workflow", fmt.Sprintf("workflow version changed before completion (got %d)", version), false, "reconcile_operation")
	}
	return nil
}

// WorkflowChangedRefsDigest is the deterministic digest used by callers that
// construct terminal metadata from action results.
func WorkflowChangedRefsDigest(refs []string) string {
	copyRefs := append([]string(nil), refs...)
	sort.Strings(copyRefs)
	h := sha256.Sum256([]byte(strings.Join(copyRefs, "\x00")))
	return "sha256:" + hex.EncodeToString(h[:])
}
