package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// ExternalCondition is the immutable condition projection supplied to an
// explicit resolver request.  A resolver can inspect only this authoritative
// identity; it cannot replace the owning durable operation.
type ExternalCondition struct {
	WorkID              string
	ConditionID         string
	AwaitType           string
	AwaitRef            string
	ResolutionAuthority string
	State               string
}

// Resolution is the resolver's typed result.  Evidence is committed only when
// the condition fold verifies every reference against ResolutionAuthority.
type Resolution struct {
	ResolutionEvidence []string `json:"resolution_evidence"`
	ResolvedByEvent    string   `json:"resolved_by_event"`
	ActorRef           string   `json:"actor_ref"`
}

// ConditionResolver is deliberately request-driven.  The engine never starts
// a timer, polling loop, watcher, or background resolver.
type ConditionResolver interface {
	Resolve(context.Context, ExternalCondition, time.Time) (Resolution, error)
}

// ConditionAuthorityReader is the read seam used by consequential readiness
// checks. It allows an authority provider to report an unreadable source
// without mutating the persisted condition projection.
type ConditionAuthorityReader interface {
	Readable(context.Context, ExternalCondition) error
}

// ConditionResolution is a descriptive alias for callers that prefer the
// longer contract terminology.
type ConditionResolution = Resolution

// WorkflowReadyResult is a read-only consequential-boundary projection. An
// unknown condition is reported separately from an ordinary open blocker; no
// read path rewrites or resolves it.
type WorkflowReadyResult struct {
	Ready              bool
	UnknownConditions  []string
	BlockingConditions []string
}

// DeriveWorkflowReady evaluates ordinary blocks and unreadable condition
// authorities fail closed. It performs no mutation or automatic resolution.
func DeriveWorkflowReady(ctx context.Context, s *Store, workID string) (WorkflowReadyResult, error) {
	return DeriveWorkflowReadyWithReader(ctx, s, workID, nil)
}

// DeriveWorkflowReadyWithReader is the explicit read boundary for readiness.
// A reader failure is an unknown, blocking condition; it is never treated as a
// resolved condition and never rewrites workflow state.
func DeriveWorkflowReadyWithReader(ctx context.Context, s *Store, workID string, reader ConditionAuthorityReader) (WorkflowReadyResult, error) {
	result := WorkflowReadyResult{Ready: true}
	if s == nil || s.db == nil {
		return result, newFailure(KindUnavailable, "derive_workflow_ready", "store is not open", false, "open the authority database")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT condition_id,resolution_authority FROM workflow_external_conditions WHERE work_id=? AND condition_state='open' ORDER BY condition_id`, workID)
	if err != nil {
		return result, wrapFailure(KindUnavailable, "derive_workflow_ready", "cannot inspect workflow conditions", true, "retry once the database is readable", err)
	}
	type conditionAuthority struct{ id, authority string }
	var conditions []conditionAuthority
	for rows.Next() {
		var conditionID, authority string
		if err := rows.Scan(&conditionID, &authority); err != nil {
			return result, wrapFailure(KindUnavailable, "derive_workflow_ready", "cannot read workflow condition", true, "retry once the database is readable", err)
		}
		conditions = append(conditions, conditionAuthority{id: conditionID, authority: authority})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, wrapFailure(KindUnavailable, "derive_workflow_ready", "cannot scan workflow conditions", true, "retry once the database is readable", err)
	}
	rows.Close()
	for _, condition := range conditions {
		conditionID, authority := condition.id, condition.authority
		result.Ready = false
		if reader != nil {
			var conditionRow ExternalCondition
			conditionRow.WorkID = workID
			conditionRow.ConditionID = conditionID
			conditionRow.ResolutionAuthority = authority
			if err := reader.Readable(ctx, conditionRow); err != nil {
				result.UnknownConditions = append(result.UnknownConditions, conditionID)
				continue
			}
		}
		const prefix = "durable_operation:"
		if !strings.HasPrefix(authority, prefix) || len(authority) == len(prefix) {
			result.UnknownConditions = append(result.UnknownConditions, conditionID)
			continue
		}
		var owner string
		if err := s.db.QueryRowContext(ctx, `SELECT work_id FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, strings.TrimPrefix(authority, prefix)).Scan(&owner); err != nil || owner != workID {
			result.UnknownConditions = append(result.UnknownConditions, conditionID)
			continue
		}
		result.BlockingConditions = append(result.BlockingConditions, conditionID)
	}
	return result, nil
}

// ResolveWorkflowConditionsAtBoundary performs one explicit consequential
// boundary check. Resolver errors mean that condition is not eligible and it
// remains open; malformed or unauthorized resolution data aborts the entire
// boundary transaction. All eligible resolutions and their event folds share
// one transaction, so a later invalid condition cannot leave an earlier one
// resolved.
func ResolveWorkflowConditionsAtBoundary(ctx context.Context, s *Store, workID string, resolver ConditionResolver, now time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, newFailure(KindUnavailable, "resolve_workflow_conditions_boundary", "store is not open", false, "open the authority database")
	}
	if strings.TrimSpace(workID) == "" || resolver == nil || now.IsZero() {
		return 0, newFailure(KindInvalidOperation, "resolve_workflow_conditions_boundary", "work, resolver, and observation time are required", false, "supply one explicit consequential boundary request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "resolve_workflow_conditions_boundary", "cannot begin condition boundary", true, "retry once the database is writable", err)
	}
	rollback := func(cause error) (int, error) { _ = tx.Rollback(); return 0, cause }
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	defer func() { _ = leaveFold(ctx, tx) }()
	resolved, err := resolveWorkflowConditionsAtBoundaryTx(ctx, tx, workID, resolver, now)
	if err != nil {
		return rollback(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, wrapFailure(KindUnavailable, "resolve_workflow_conditions_boundary", "cannot commit condition boundary", true, "retry once the database is writable", err)
	}
	return resolved, nil
}

// resolveWorkflowConditionsAtBoundaryTx is the transaction-scoped primitive
// shared by explicit boundary checks and the owning-action coordinator. The
// caller owns fold_guard and commit/rollback.
func resolveWorkflowConditionsAtBoundaryTx(ctx context.Context, tx *sql.Tx, workID string, resolver ConditionResolver, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT work_id,condition_id,await_type,await_ref,resolution_authority,condition_state FROM workflow_external_conditions WHERE work_id=? AND condition_state='open' ORDER BY condition_id`, workID)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "resolve_workflow_conditions_boundary", "cannot inspect open workflow conditions", true, "retry once the database is readable", err)
	}
	var conditions []ExternalCondition
	for rows.Next() {
		var condition ExternalCondition
		if err := rows.Scan(&condition.WorkID, &condition.ConditionID, &condition.AwaitType, &condition.AwaitRef, &condition.ResolutionAuthority, &condition.State); err != nil {
			rows.Close()
			return 0, wrapFailure(KindUnavailable, "resolve_workflow_conditions_boundary", "cannot read open workflow condition", true, "retry once the database is readable", err)
		}
		conditions = append(conditions, condition)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, wrapFailure(KindUnavailable, "resolve_workflow_conditions_boundary", "cannot scan open workflow conditions", true, "retry once the database is readable", err)
	}
	rows.Close()
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return 0, wrapFailure(KindUnavailable, "resolve_workflow_conditions_boundary", "cannot read workflow version", true, "retry once the database is readable", err)
	}
	resolved := 0
	for _, condition := range conditions {
		resolution, resolveErr := resolver.Resolve(ctx, condition, now.UTC())
		if resolveErr != nil {
			// A provider miss, non-terminal state, or unreadable authority is
			// explicitly ineligible. It is not permission to infer resolution.
			continue
		}
		if !workflowList(resolution.ResolutionEvidence, 32, 1) || !workflowString(resolution.ResolvedByEvent, 128) || !workflowString(resolution.ActorRef, 128) {
			return 0, newFailure(KindInvariantViolation, "resolve_workflow_conditions_boundary", "resolver returned invalid condition evidence", false, "reconcile_operation")
		}
		payload := map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "condition_id": condition.ConditionID, "resolution_evidence": resolution.ResolutionEvidence, "resolved_by_event": resolution.ResolvedByEvent}
		raw, _ := json.Marshal(payload)
		event := Event{EventID: resolution.ResolvedByEvent, Kind: WorkflowConditionResolved, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: resolution.ActorRef, OccurredAt: now.UTC(), PayloadVersion: 1, Payload: raw}
		if _, err := appendEvent(ctx, tx, event, true); err != nil {
			return 0, newFailure(KindOperationConflict, "resolve_workflow_conditions_boundary", "condition resolution event conflicted", false, "reconcile_operation")
		}
		if err := foldRegisteredEvent(ctx, tx, event); err != nil {
			return 0, err
		}
		version++
		resolved++
	}
	return resolved, nil
}

// ResolveWorkflowCondition performs exactly one explicit condition check and
// appends the typed resolution event only when the owning authority verifies
// the returned evidence.  Resolver failures leave the condition open.
func ResolveWorkflowCondition(ctx context.Context, s *Store, workID, conditionID string, resolver ConditionResolver, now time.Time) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "resolve_workflow_condition", "store is not open", false, "open the authority database")
	}
	if strings.TrimSpace(workID) == "" || strings.TrimSpace(conditionID) == "" || resolver == nil || now.IsZero() {
		return newFailure(KindInvalidOperation, "resolve_workflow_condition", "work, condition, resolver, and observation time are required", false, "supply one explicit condition resolution request")
	}
	var condition ExternalCondition
	if err := s.db.QueryRowContext(ctx, `SELECT work_id,condition_id,await_type,await_ref,resolution_authority,condition_state FROM workflow_external_conditions WHERE work_id=? AND condition_id=?`, workID, conditionID).Scan(&condition.WorkID, &condition.ConditionID, &condition.AwaitType, &condition.AwaitRef, &condition.ResolutionAuthority, &condition.State); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "resolve_workflow_condition", "workflow condition is not recorded", false, "reread_entities")
		}
		return wrapFailure(KindUnavailable, "resolve_workflow_condition", "cannot read workflow condition", true, "retry once the database is readable", err)
	}
	if condition.State != "open" {
		return newFailure(KindNotTerminal, "resolve_workflow_condition", "workflow condition is already terminal", false, "reread_entities")
	}
	resolution, err := resolver.Resolve(ctx, condition, now.UTC())
	if err != nil {
		return err
	}
	if !workflowList(resolution.ResolutionEvidence, 32, 1) || !workflowString(resolution.ResolvedByEvent, 128) || !workflowString(resolution.ActorRef, 128) {
		return newFailure(KindInvalidOperation, "resolve_workflow_condition", "resolver returned incomplete resolution evidence", false, "supply the owning authority evidence")
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return wrapFailure(KindUnavailable, "resolve_workflow_condition", "cannot read workflow version", true, "retry once the database is readable", err)
	}
	payload := map[string]any{
		"work_id":             workID,
		"expected_version":    version,
		"resulting_version":   version + 1,
		"condition_id":        conditionID,
		"resolution_evidence": resolution.ResolutionEvidence,
		"resolved_by_event":   resolution.ResolvedByEvent,
	}
	raw, _ := json.Marshal(payload)
	event := Event{EventID: resolution.ResolvedByEvent, Kind: WorkflowConditionResolved, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: resolution.ActorRef, OccurredAt: now.UTC(), PayloadVersion: 1, Payload: raw}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "resolve_workflow_condition", "cannot begin condition resolution", true, "retry once the database is writable", err)
	}
	rollback := func(cause error) error { _ = tx.Rollback(); return cause }
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		return rollback(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "resolve_workflow_condition", "cannot commit condition resolution", true, "retry once the database is writable", err)
	}
	return nil
}

// ResolveCondition is a short compatibility spelling for the explicit public
// request boundary.
func ResolveCondition(ctx context.Context, s *Store, workID, conditionID string, resolver ConditionResolver, now time.Time) error {
	return ResolveWorkflowCondition(ctx, s, workID, conditionID, resolver, now)
}

// CancelWorkflowCondition records an operator-authorized terminal condition
// without pretending that the awaited external authority resolved it.
func CancelWorkflowCondition(ctx context.Context, s *Store, workID, conditionID, actorRef string, evidence []string, now time.Time) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "cancel_workflow_condition", "store is not open", false, "open the authority database")
	}
	if strings.TrimSpace(workID) == "" || strings.TrimSpace(conditionID) == "" || strings.TrimSpace(actorRef) == "" || now.IsZero() || len(evidence) == 0 {
		return newFailure(KindInvalidOperation, "cancel_workflow_condition", "work, condition, actor, evidence, and observation time are required", false, "supply the operator cancellation authority")
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		return wrapFailure(KindUnavailable, "cancel_workflow_condition", "cannot read workflow version", true, "retry once the database is readable", err)
	}
	eventID := "condition-cancelled:" + workID + ":" + conditionID
	payload, _ := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"condition_id": conditionID, "cancellation_authority": "operator",
		"cancellation_evidence": evidence, "cancelled_by_event": eventID,
	})
	event := Event{EventID: eventID, Kind: WorkflowConditionCancelled, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: now.UTC(), PayloadVersion: 1, Payload: payload}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "cancel_workflow_condition", "cannot begin condition cancellation", true, "retry once the database is writable", err)
	}
	rollback := func(cause error) error { _ = tx.Rollback(); return cause }
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		return rollback(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "cancel_workflow_condition", "cannot commit condition cancellation", true, "retry once the database is writable", err)
	}
	return nil
}
