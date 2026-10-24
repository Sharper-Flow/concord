package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// WorkSizeOverridden records the operator's size beside the model decision it
// was recorded against (CD-0229 D6). Only a recorded operator approval actor
// can fold it; the operator's size governs until the operator records another.
const WorkSizeOverridden = "work.size_overridden"

// workSizeOverrideApprovalSubject admits an override only through its
// operator-approved route.
var workSizeOverrideApprovalSubject = workflowApprovalBindingSubject{
	operation: "work size override",
	actorHint: "submit the size override through the approved operator route",
	freshHint: "request a fresh approval for the exact size override",
}

type workSizeOverriddenPayload struct {
	ModelSequence           int      `json:"model_sequence"`
	Size                    WorkSize `json:"size"`
	Reason                  string   `json:"reason"`
	ApprovalRef             string   `json:"approval_ref"`
	ApprovalOperationDigest string   `json:"approval_operation_digest"`
	ApprovalScopeJSON       string   `json:"approval_scope_json"`
	ApprovalVersionsJSON    string   `json:"approval_versions_json"`
	ApprovalConsequence     string   `json:"approval_consequence"`
	// CarriedFromEventID decodes a historical carried override on replay.
	// No live size revision carries an override to another item.
	CarriedFromEventID string `json:"carried_from_event_id,omitempty"`
}

func (p workSizeOverriddenPayload) binding() workflowApprovalBinding {
	return workflowApprovalBinding{ApprovalRef: p.ApprovalRef, OperationDigest: p.ApprovalOperationDigest, ScopeJSON: p.ApprovalScopeJSON, VersionsJSON: p.ApprovalVersionsJSON, Consequence: p.ApprovalConsequence}
}

// WorkSizeOverrideRequest records one operator size for a sized work item.
// The approval boundary consumes the one-use approval in the same
// Transaction before this request runs.
type WorkSizeOverrideRequest struct {
	WorkID                  string
	ExpectedVersion         int64
	Size                    WorkSize
	Reason                  string
	EventID                 string
	OccurredAt              time.Time
	ApprovalRef             string
	ApprovalOperationDigest string
	ApprovalScopeJSON       string
	ApprovalVersionsJSON    string
	ApprovalConsequence     string
}

// WorkSizeDecision is a work item's size: the model's current record, the
// operator's size when one is recorded, and the size that governs.
type WorkSizeDecision struct {
	Model        WorkSizeRecord
	OperatorSize *WorkSize
	Effective    WorkSize
}

func validWorkSize(size WorkSize) bool {
	switch size {
	case WorkSizeSmall, WorkSizeLarger, WorkSizeCrucial:
		return true
	}
	return false
}

// OverrideWorkSizeTx derives the operator actor from the consumed approval,
// records it, and appends work.size_overridden against the item's current
// model record. An unsized item refuses: the model decides first. An
// override changes the effective size in place.
func OverrideWorkSizeTx(ctx context.Context, transaction *Transaction, request WorkSizeOverrideRequest) (WorkSizeRevisionResult, error) {
	tx, err := transactionSQL(transaction, "work_size_override")
	if err != nil {
		return WorkSizeRevisionResult{}, err
	}
	if !ValidReference(request.WorkID) || !workflowString(request.EventID, 128) || request.ExpectedVersion <= 0 || request.ExpectedVersion > (1<<63-1)-2 {
		return WorkSizeRevisionResult{}, newFailure(KindInvalidPayload, "work_size_override", "work, event identity or version is invalid", false, "supply bounded override fields")
	}
	before, sized, err := readEffectiveWorkSize(ctx, tx, request.WorkID)
	if err != nil {
		return WorkSizeRevisionResult{}, err
	}
	model := before.Model
	if !sized {
		return WorkSizeRevisionResult{}, newFailure(KindInvalidOperation, "work_size_override", "the work item has no model size to override", false, "run the size model first")
	}
	operator, err := workflowOperatorFromConsumedApprovalTx(ctx, tx, workflowApprovalBinding{
		ApprovalRef: request.ApprovalRef, OperationDigest: request.ApprovalOperationDigest,
		ScopeJSON: request.ApprovalScopeJSON, VersionsJSON: request.ApprovalVersionsJSON,
		Consequence: request.ApprovalConsequence,
	}, "work_size_override")
	if err != nil {
		return WorkSizeRevisionResult{}, err
	}
	if request.OccurredAt.IsZero() {
		request.OccurredAt = transaction.now()
	}
	actor := workflowTypedEvent(request.EventID+":operator", WorkflowActorRecorded, request.WorkID, operator.ref, request.OccurredAt, request.ExpectedVersion, map[string]any{
		"actor_ref": operator.ref, "principal_ref": operator.PrincipalRef, "client_ref": operator.ClientRef,
		"agent_ref": operator.AgentRef, "session_ref": operator.SessionRef, "actor_class": string(ActorOperator),
	})
	payload, err := json.Marshal(workSizeOverriddenPayload{
		ModelSequence: model.Sequence, Size: request.Size, Reason: request.Reason,
		ApprovalRef: request.ApprovalRef, ApprovalOperationDigest: request.ApprovalOperationDigest,
		ApprovalScopeJSON: request.ApprovalScopeJSON, ApprovalVersionsJSON: request.ApprovalVersionsJSON,
		ApprovalConsequence: request.ApprovalConsequence,
	})
	if err != nil {
		return WorkSizeRevisionResult{}, wrapFailure(KindInvalidPayload, "work_size_override", "cannot encode the size override", false, "supply the bounded override fields", err)
	}
	override := Event{EventID: request.EventID, Kind: WorkSizeOverridden, SubjectType: SubjectWorkItem, SubjectID: request.WorkID, Actor: operator.ref, OccurredAt: request.OccurredAt.UTC(), PayloadVersion: 1, Payload: payload}
	applied, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{actor, override}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, request.WorkID): request.ExpectedVersion}}, newFoldScope(tx))
	if err != nil {
		return WorkSizeRevisionResult{}, err
	}
	return workSizeRevisionResultTx(ctx, tx, request.WorkID, applied.EventIDs)
}

func foldWorkSizeOverridden(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p workSizeOverriddenPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if !validWorkSize(p.Size) || !workflowString(p.Reason, 4096) || p.ModelSequence < 1 {
		return newFailure(KindInvalidPayload, "fold_event", "size override needs a size from the vocabulary, a bounded reason and a model sequence", false, "record the operator's size against the current model record")
	}
	if p.CarriedFromEventID != "" {
		return newFailure(KindInvalidOperation, "fold_event", "carried size overrides are retired", false, "record an approved in-place size override")
	} else if err := authorizeWorkflowOperatorApprovalTx(ctx, tx, event, p.binding(), workSizeOverrideApprovalSubject); err != nil {
		return err
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, event.SubjectID).Scan(&lifecycle); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "work item is not recorded", false, "override the size of existing work")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read the overridden work item", true, "retry once the database is readable", err)
	}
	if isTerminalLifecycle(lifecycle) {
		return newFailure(KindInvalidTransition, "fold_event", "terminal work cannot be sized", false, "override the size of nonterminal work")
	}
	model, sized, err := readWorkSize(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if !sized || model.Sequence != p.ModelSequence {
		return newFailure(KindStaleContext, "fold_event", "size override names a model record that is not the current one", false, "record the override against the current model record")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_size_overrides(work_id,event_id,model_sequence,size_class,recorded_at) VALUES(?,?,?,?,?)
		ON CONFLICT(work_id) DO UPDATE SET event_id=excluded.event_id, model_sequence=excluded.model_sequence, size_class=excluded.size_class, recorded_at=excluded.recorded_at`,
		event.SubjectID, event.EventID, p.ModelSequence, string(p.Size), event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot write the size override", true, "retry once the database is writable", err)
	}
	return nil
}

// readEffectiveWorkSize reads a work item's size decision. ok is false when
// the item was never sized; an unsized item is never treated as small.
func readEffectiveWorkSize(ctx context.Context, q queryer, workID string) (WorkSizeDecision, bool, error) {
	model, sized, err := readWorkSize(ctx, q, workID)
	if err != nil || !sized {
		return WorkSizeDecision{}, false, err
	}
	decision := WorkSizeDecision{Model: model, Effective: model.Size}
	var size string
	err = q.QueryRowContext(ctx, `SELECT size_class FROM work_size_overrides WHERE work_id=?`, workID).Scan(&size)
	if err == sql.ErrNoRows {
		return decision, true, nil
	}
	if err != nil {
		return WorkSizeDecision{}, false, wrapFailure(KindUnavailable, "work_size", "cannot read the operator's size", true, "retry once the database is readable", err)
	}
	operator := WorkSize(size)
	decision.OperatorSize, decision.Effective = &operator, operator
	return decision, true, nil
}
