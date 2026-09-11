package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// AppendWorkflowStalenessObservation records a typed observation before its
// consequential workflow action. The event does not advance work version.
func AppendWorkflowStalenessObservation(ctx context.Context, s *Store, eventID, workID, actor, acceptedInputsDigest string, payload json.RawMessage, observedAt time.Time) error {
	event, present, err := workflowStalenessObservationEvent(eventID, workID, actor, acceptedInputsDigest, payload, observedAt)
	if err != nil || !present {
		return err
	}
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "workflow_staleness", "store is not open", false, "open the authority database")
	}
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		return appendWorkflowStalenessObservationTx(ctx, transaction, event)
	}); err != nil {
		return err
	}
	return s.SyncDurable(ctx)
}

func appendWorkflowStalenessObservationTx(ctx context.Context, transaction *Transaction, event Event) error {
	tx, err := transactionSQL(transaction, "workflow_staleness")
	if err != nil {
		return err
	}
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	defer func() { _ = leaveFold(ctx, tx) }()
	recorded, err := workflowStalenessObservationRecordedTx(ctx, tx, event)
	if err != nil {
		return err
	}
	if recorded {
		return nil
	}
	_, err = applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}})
	return err
}

func workflowStalenessObservationEvent(eventID, workID, actor, acceptedInputsDigest string, payload json.RawMessage, observedAt time.Time) (Event, bool, error) {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return Event{}, false, err
	}
	driftRaw, present := fields["observed_drift"]
	if !present {
		return Event{}, false, nil
	}
	var drift map[string]json.RawMessage
	if err := json.Unmarshal(driftRaw, &drift); err != nil || drift == nil {
		return Event{}, false, newFailure(KindInvalidPayload, "workflow_staleness", "observed_drift is not an object", false, "supply the typed staleness observation")
	}
	ruleID := workflowFieldStringDefault(fields, "staleness_rule_id", "")
	severity := workflowFieldStringDefault(drift, "severity", "")
	if ruleID == "" {
		return Event{}, false, newFailure(KindInvalidPayload, "workflow_staleness", "staleness_rule_id is required", false, "supply the owning staleness rule")
	}
	if !validDigest(acceptedInputsDigest) {
		return Event{}, false, newFailure(KindInvalidPayload, "workflow_staleness", "accepted input digest is invalid", false, "supply the workflow action input digest")
	}
	if observedAt.IsZero() {
		return Event{}, false, newFailure(KindInvalidPayload, "workflow_staleness", "staleness observation time is required", false, "supply the observation time")
	}
	encoded, _ := json.Marshal(map[string]any{
		"rule_id":                ruleID,
		"severity":               severity,
		"drifted":                workflowFieldBool(drift, "drifted"),
		"observed_at":            observedAt.UTC().Format(time.RFC3339Nano),
		"accepted_inputs_digest": acceptedInputsDigest,
	})
	return Event{EventID: eventID, Kind: WorkflowStalenessObserved, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actor, OccurredAt: observedAt, PayloadVersion: 1, Payload: encoded}, true, nil
}

func workflowStalenessObservationRecordedTx(ctx context.Context, tx *sql.Tx, event Event) (bool, error) {
	var kind, subjectType, subjectID, actor, payload string
	var payloadVersion int
	err := tx.QueryRowContext(ctx, `SELECT kind,subject_type,subject_id,actor,payload_version,payload FROM domain_events WHERE event_id=?`, event.EventID).Scan(&kind, &subjectType, &subjectID, &actor, &payloadVersion, &payload)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_staleness", "cannot inspect the staleness observation event", true, "retry once the database is readable", err)
	}
	if kind != WorkflowStalenessObserved || subjectType != string(event.SubjectType) || subjectID != event.SubjectID || actor != event.Actor || payloadVersion != event.PayloadVersion {
		return false, newFailure(KindOperationConflict, "workflow_staleness", "staleness observation event identity is already in use", false, "retry with a new operation identity")
	}
	var recorded, requested workflowStalenessObservedPayload
	if err := json.Unmarshal([]byte(payload), &recorded); err != nil {
		return false, newFailure(KindInvariantViolation, "workflow_staleness", "recorded staleness observation event is malformed", false, "rebuild projections from the event log")
	}
	if err := json.Unmarshal(event.Payload, &requested); err != nil {
		return false, newFailure(KindInvalidPayload, "workflow_staleness", "staleness observation event is malformed", false, "supply the typed staleness observation")
	}
	if recorded.RuleID != requested.RuleID || recorded.Severity != requested.Severity || recorded.Drifted != requested.Drifted || recorded.AcceptedInputsDigest != requested.AcceptedInputsDigest {
		return false, newFailure(KindOperationConflict, "workflow_staleness", "staleness observation event identity has different content", false, "retry with a new operation identity")
	}
	return true, nil
}
