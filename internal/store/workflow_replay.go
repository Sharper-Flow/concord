package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// WorkflowReplayImportResult records the durable work performed by an
// authenticated workflow replay/import. Existing event identities are
// verified, not re-appended; new events are folded through the normal event
// registry in the same transaction.
type WorkflowReplayImportResult struct {
	ProcessedEventIDs []string
	ExistingEventIDs  []string
	AppendedEventIDs  []string
}

// ImportWorkflowEventStream validates and imports a declared event stream in
// order. A rejected event rolls back every earlier append from this stream.
// This is the owning replay/import API; callers must not write domain_events.
func ImportWorkflowEventStream(ctx context.Context, s *Store, events []Event) (WorkflowReplayImportResult, error) {
	var result WorkflowReplayImportResult
	if s == nil || s.db == nil {
		return result, newFailure(KindUnavailable, "workflow_replay", "store is not open", false, "open the authority database")
	}
	if len(events) == 0 {
		return result, newFailure(KindInvalidOperation, "workflow_replay", "event stream is empty", false, "supply at least one event")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, wrapFailure(KindUnavailable, "workflow_replay", "cannot begin replay import", true, "retry once the database is writable", err)
	}
	rollback := func(cause error) (WorkflowReplayImportResult, error) {
		_ = tx.Rollback()
		return WorkflowReplayImportResult{}, cause
	}
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	for _, event := range events {
		if err := event.validate(); err != nil {
			return rollback(err)
		}
		if err := validateRegisteredEvent(event); err != nil {
			return rollback(attributeFailure(err, event, "upcast"))
		}
		result.ProcessedEventIDs = append(result.ProcessedEventIDs, event.EventID)
		existing, err := existingWorkflowReplayEvent(ctx, tx, event.EventID)
		if err != nil {
			return rollback(err)
		}
		if existing != nil {
			if err := sameWorkflowReplayEvent(*existing, event); err != nil {
				return rollback(err)
			}
			result.ExistingEventIDs = append(result.ExistingEventIDs, event.EventID)
			continue
		}
		ref := VersionRef(event.SubjectType, event.SubjectID)
		version, exists, err := projectionVersion(ctx, tx, event.SubjectType, event.SubjectID)
		if err != nil {
			return rollback(err)
		}
		expected := int64(0)
		if exists {
			expected = version
		}
		if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{ref: expected}}); err != nil {
			return rollback(err)
		}
		result.AppendedEventIDs = append(result.AppendedEventIDs, event.EventID)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return WorkflowReplayImportResult{}, wrapFailure(KindUnavailable, "workflow_replay", "cannot commit replay import", true, "retry once the database is writable", err)
	}
	return result, nil
}

func existingWorkflowReplayEvent(ctx context.Context, tx *sql.Tx, eventID string) (*Event, error) {
	var event Event
	var subjectType, occurredAt, payload string
	if err := tx.QueryRowContext(ctx, `SELECT kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events WHERE event_id=?`, eventID).Scan(&event.Kind, &subjectType, &event.SubjectID, &event.Actor, &occurredAt, &event.PayloadVersion, &payload); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_replay", "cannot inspect an imported event", true, "retry once the event authority is readable", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return nil, newFailure(KindInvalidEvent, "workflow_replay", "imported event has an invalid occurred_at timestamp", false, "repair the event authority")
	}
	event.EventID = eventID
	event.SubjectType = SubjectType(subjectType)
	event.OccurredAt = parsed
	event.Payload = []byte(payload)
	return &event, nil
}

func sameWorkflowReplayEvent(existing, declared Event) error {
	if existing.Kind != declared.Kind || existing.SubjectType != declared.SubjectType || existing.SubjectID != declared.SubjectID || existing.Actor != declared.Actor || !existing.OccurredAt.Equal(declared.OccurredAt) || existing.PayloadVersion != declared.PayloadVersion || string(existing.Payload) != string(declared.Payload) {
		return newFailure(KindDuplicateEvent, "workflow_replay", fmt.Sprintf("event %q already exists with different content", declared.EventID), false, "supply the original event identity and content")
	}
	return nil
}
