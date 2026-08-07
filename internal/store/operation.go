package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Operation is one accepted domain operation. ExpectedVersions is keyed by
// subject ID and checks that subject's version before the first event touching
// it; later events in the same operation observe the version just produced.
type Operation struct {
	Events           []Event
	ExpectedVersions map[string]int64
}

// Product is the typed current projection of a Product identity.
type Product struct {
	ID                      string
	DisplayName             string
	StageMaturity           string
	StageAudienceCommitment string
	Version                 int64
	CreatedAt               string
	UpdatedAt               string
}

// Project is the typed current projection of a Project identity.
type Project struct {
	ID          string
	DisplayName string
	Version     int64
	CreatedAt   string
	UpdatedAt   string
}

type projectionMutation func(context.Context, *sql.Tx, Event) error

// projectionRegistry is shared by live application and recovery replay. A
// rebuild must exercise exactly the same state transitions as normal writes.
var projectionRegistry = map[string]projectionMutation{
	"product.created":               foldProductCreated,
	"product.renamed":               foldProductRenamed,
	"product.stage_changed":         foldProductStageChanged,
	"project.created":               foldProjectCreated,
	"project.renamed":               foldProjectRenamed,
	"work.created":                  foldWorkCreated,
	"work.transitioned":             foldWorkTransitioned,
	"work.superseded":               foldWorkSuperseded,
	"work.reopened":                 foldWorkReopened,
	"work.reopened_from_superseded": foldWorkReopenedFromSuperseded,
	"relation.added":                foldRelationAdded,
	"relation.removed":              foldRelationRemoved,
}

// ApplyOperation appends and folds one operation in one transaction.
func ApplyOperation(ctx context.Context, s *Store, operation Operation) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "apply_operation", "store is not open", false,
			"open a store before applying an operation")
	}
	if len(operation.Events) == 0 {
		return newFailure(KindInvalidOperation, "apply_operation", "operation has no events", false,
			"supply at least one accepted event")
	}
	for subjectID, expected := range operation.ExpectedVersions {
		if subjectID == "" || expected < 0 {
			return newFailure(KindInvalidOperation, "apply_operation",
				"expected versions must use non-empty IDs and non-negative versions", false,
				"supply zero for a subject that must not yet exist")
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "apply_operation", "cannot begin domain operation", true,
			"retry once the database is writable", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}

	checked := make(map[string]bool, len(operation.ExpectedVersions))
	for _, event := range operation.Events {
		if err := event.validate(); err != nil {
			return rollback(err)
		}
		mutation, ok := projectionRegistry[event.Kind]
		if !ok {
			return rollback(unknownEventKind(event.Kind))
		}
		if expected, hasExpected := operation.ExpectedVersions[event.SubjectID]; hasExpected && !checked[event.SubjectID] {
			got, exists, err := projectionVersion(ctx, tx, event.SubjectType, event.SubjectID)
			if err != nil {
				return rollback(err)
			}
			if (expected == 0 && exists) || (expected > 0 && (!exists || got != expected)) {
				return rollback(versionConflict(event.SubjectID, expected, got, exists))
			}
			checked[event.SubjectID] = true
		}
		if _, err := AppendEvent(ctx, tx, event); err != nil {
			return rollback(err)
		}
		if err := mutation(ctx, tx, event); err != nil {
			return rollback(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "apply_operation", "cannot commit domain operation", true,
			"retry once the database is writable", err)
	}
	return nil
}

// RebuildFromLog replaces every live projection with a fold of the complete
// append-only log, preserving the prior state if any event cannot be folded.
func RebuildFromLog(ctx context.Context, s *Store) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "rebuild_from_log", "store is not open", false,
			"open a store before rebuilding projections")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot begin projection rebuild", true,
			"retry once the database is writable", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	events, err := readEvents(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	// Relations reference work_items, so clear the dependent projection first;
	// replay then restores the same event order under the fold guard.
	for _, table := range []string{"relations", "work_items", "products", "projects"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return rollback(wrapFailure(KindUnavailable, "rebuild_from_log",
				"cannot clear "+table+" projection", true,
				"retry once the database is writable", err))
		}
	}
	for _, event := range events {
		mutation, ok := projectionRegistry[event.Kind]
		if !ok {
			return rollback(unknownEventKind(event.Kind))
		}
		if err := mutation(ctx, tx, event); err != nil {
			return rollback(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "rebuild_from_log", "cannot commit projection rebuild", true,
			"retry once the database is writable", err)
	}
	return nil
}

func enterFold(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard (active) VALUES (1)`); err != nil {
		return wrapFailure(KindUnavailable, "fold", "cannot enable projection fold guard", true,
			"retry once the database is writable", err)
	}
	return nil
}

func leaveFold(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active = 1`); err != nil {
		return wrapFailure(KindUnavailable, "fold", "cannot disable projection fold guard", true,
			"retry once the database is writable", err)
	}
	return nil
}

func unknownEventKind(kind string) *Failure {
	return newFailure(KindUnknownEventKind, "fold_event", "no projection mutation is registered for "+kind,
		false, "install a binary that recognizes the event or repair the log")
}

func versionConflict(subjectID string, expected, got int64, exists bool) *Failure {
	actual := "missing"
	if exists {
		actual = fmt.Sprintf("%d", got)
	}
	return newFailure(KindVersionConflict, "apply_operation",
		fmt.Sprintf("subject %s has version %s, want %d", subjectID, actual, expected), false,
		"reload the subject and retry with its current version")
}

func projectionVersion(ctx context.Context, tx *sql.Tx, subjectType SubjectType, subjectID string) (int64, bool, error) {
	table, ok := projectionTable(subjectType)
	if !ok {
		return 0, false, newFailure(KindInvalidSubject, "apply_operation",
			"no projection exists for subject type "+string(subjectType), false,
			"use a Product or Project subject")
	}
	var version int64
	err := tx.QueryRowContext(ctx, "SELECT version FROM "+table+" WHERE id = ?", subjectID).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, wrapFailure(KindUnavailable, "apply_operation",
			"cannot read "+string(subjectType)+" version", true,
			"retry once the database is readable", err)
	}
	return version, true, nil
}

func projectionTable(subjectType SubjectType) (string, bool) {
	switch subjectType {
	case SubjectProduct:
		return "products", true
	case SubjectProject:
		return "projects", true
	case SubjectWorkItem:
		return "work_items", true
	default:
		return "", false
	}
}

func readEvents(ctx context.Context, tx *sql.Tx) ([]Event, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload
		FROM domain_events ORDER BY seq`)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "rebuild_from_log", "cannot read the event log", true,
			"retry once the database is readable", err)
	}
	defer func() { _ = rows.Close() }()
	var events []Event
	for rows.Next() {
		var event Event
		var occurredAt string
		if err := rows.Scan(&event.EventID, &event.Kind, &event.SubjectType, &event.SubjectID,
			&event.Actor, &occurredAt, &event.PayloadVersion, &event.Payload); err != nil {
			return nil, wrapFailure(KindInvalidEvent, "rebuild_from_log", "cannot decode an event row", false,
				"repair the event log before rebuilding", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, wrapFailure(KindInvalidEvent, "rebuild_from_log", "event has an invalid occurrence time", false,
				"repair the event log before rebuilding", err)
		}
		event.OccurredAt = parsed
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "rebuild_from_log", "cannot read the event log", true,
			"retry once the database is readable", err)
	}
	return events, nil
}

type productCreatedPayload struct {
	DisplayName             string `json:"display_name"`
	StageMaturity           string `json:"stage_maturity"`
	StageAudienceCommitment string `json:"stage_audience_commitment"`
}

type productRenamedPayload struct {
	DisplayName string `json:"display_name"`
}

type productStageChangedPayload struct {
	StageMaturity           string `json:"stage_maturity"`
	StageAudienceCommitment string `json:"stage_audience_commitment"`
}

type projectCreatedPayload struct {
	DisplayName string `json:"display_name"`
}

type projectRenamedPayload struct {
	DisplayName string `json:"display_name"`
}

func decodePayload(event Event, target any) error {
	if event.PayloadVersion != 1 {
		return newFailure(KindUnsupportedPayloadVersion, "fold_event",
			fmt.Sprintf("event %s uses payload version %d", event.EventID, event.PayloadVersion), false,
			"upcast the event or install a binary that supports its payload version")
	}
	if err := json.Unmarshal(event.Payload, target); err != nil {
		return wrapFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("event %s payload does not match its kind", event.EventID), false,
			"repair the event payload before rebuilding", err)
	}
	return nil
}

func checkSubject(event Event, want SubjectType) error {
	if event.SubjectType != want {
		return newFailure(KindInvalidSubject, "fold_event",
			fmt.Sprintf("%s event has subject type %q", event.Kind, event.SubjectType), false,
			"use the subject type required by the event kind")
	}
	if event.SubjectID == "" {
		return newFailure(KindInvalidEvent, "fold_event", "event subject ID is empty", false,
			"repair the event log before rebuilding")
	}
	return nil
}

func validateProductStage(maturity, audience string) bool {
	validMaturity := map[string]bool{
		"prototype": true, "alpha": true, "beta": true, "production": true, "deprecated": true,
	}
	validAudience := map[string]bool{"operator_only": true, "limited": true, "public": true}
	return validMaturity[maturity] && validAudience[audience]
}

func foldProductCreated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload productCreatedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.DisplayName == "" || !validateProductStage(payload.StageMaturity, payload.StageAudienceCommitment) {
		return newFailure(KindInvalidPayload, "fold_event", "product.created payload has invalid fields", false,
			"supply a display name and accepted Product stage values")
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO products
			(id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, event.SubjectID, payload.DisplayName,
		payload.StageMaturity, payload.StageAudienceCommitment, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "Product already exists", false,
				"append a rename or update event for the existing Product")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot create Product projection", true,
			"retry once the database is writable", err)
	}
	return nil
}

func foldProductRenamed(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload productRenamedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.DisplayName == "" {
		return newFailure(KindInvalidPayload, "fold_event", "product.renamed payload has no display name", false,
			"supply a non-empty display name")
	}
	return updateProduct(ctx, tx, event, `display_name = ?`, payload.DisplayName)
}

func foldProductStageChanged(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload productStageChangedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if !validateProductStage(payload.StageMaturity, payload.StageAudienceCommitment) {
		return newFailure(KindInvalidPayload, "fold_event", "product.stage_changed payload has invalid fields", false,
			"supply accepted Product stage values")
	}
	return updateProduct(ctx, tx, event,
		`stage_maturity = ?, stage_audience_commitment = ?`, payload.StageMaturity, payload.StageAudienceCommitment)
}

func updateProduct(ctx context.Context, tx *sql.Tx, event Event, fields string, args ...any) error {
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	args = append(args, now, event.SubjectID)
	result, err := tx.ExecContext(ctx, "UPDATE products SET "+fields+`, version = version + 1, updated_at = ? WHERE id = ?`, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update Product projection", true,
			"retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify Product projection update", true,
			"retry once the database is readable", err)
	} else if affected == 0 {
		return newFailure(KindProjectionNotFound, "fold_event", "Product does not exist", false,
			"create the Product before changing it")
	}
	return nil
}

func foldProjectCreated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProject); err != nil {
		return err
	}
	var payload projectCreatedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.DisplayName == "" {
		return newFailure(KindInvalidPayload, "fold_event", "project.created payload has no display name", false,
			"supply a non-empty display name")
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO projects (id, display_name, version, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?)`, event.SubjectID, payload.DisplayName, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "Project already exists", false,
				"append a rename event for the existing Project")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot create Project projection", true,
			"retry once the database is writable", err)
	}
	return nil
}

func foldProjectRenamed(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProject); err != nil {
		return err
	}
	var payload projectRenamedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if payload.DisplayName == "" {
		return newFailure(KindInvalidPayload, "fold_event", "project.renamed payload has no display name", false,
			"supply a non-empty display name")
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx,
		`UPDATE projects SET display_name = ?, version = version + 1, updated_at = ? WHERE id = ?`,
		payload.DisplayName, now, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update Project projection", true,
			"retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify Project projection update", true,
			"retry once the database is readable", err)
	} else if affected == 0 {
		return newFailure(KindProjectionNotFound, "fold_event", "Project does not exist", false,
			"create the Project before renaming it")
	}
	return nil
}
