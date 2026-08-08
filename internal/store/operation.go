package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// SubjectRef identifies a versioned projection without conflating equal IDs
// belonging to different subject types.
type SubjectRef struct {
	Type SubjectType
	ID   string
}

// VersionRef constructs a typed optimistic-concurrency key.
func VersionRef(subjectType SubjectType, id string) SubjectRef {
	return SubjectRef{Type: subjectType, ID: id}
}

// Operation is one accepted domain operation. ExpectedVersions checks the
// typed subject's version before the first event touching it; later events in
// the same operation observe the version just produced.
type Operation struct {
	Events           []Event
	ExpectedVersions map[SubjectRef]int64
}

// MembershipImpact describes canonical work whose derived Product scope may
// change during a Product↔Project membership operation.
type MembershipImpact struct {
	AffectedWorkCount      int
	TotalAffectedWorkCount int
	AffectedWorkIDs        []string
	EventIDs               []string
}

// ApplyOperationResult is the durable result of an accepted operation.
type ApplyOperationResult struct {
	Impact   MembershipImpact
	EventIDs []string
}

// Product is the typed current projection of a Product identity.
type Product struct {
	ID                      string `json:"id"`
	DisplayName             string `json:"display_name"`
	StageMaturity           string `json:"stage_maturity"`
	StageAudienceCommitment string `json:"stage_audience_commitment"`
	Version                 int64  `json:"version"`
	CreatedAt               string `json:"created_at"`
	UpdatedAt               string `json:"updated_at"`
}

// Project is the typed current projection of a Project identity.
type Project struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Version     int64  `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type projectionMutation func(context.Context, *sql.Tx, Event) error

// operationObserver is an unexported white-box measurement surface. It is
// passed through the operation call graph rather than stored globally, so
// production callers pay no instrumentation cost and concurrent workers cannot
// overwrite one another's samples.
type operationObserver struct {
	beginWait      time.Duration
	commitDuration time.Duration
}

func beginObservedTx(ctx context.Context, db *sql.DB, observer *operationObserver) (*sql.Tx, error) {
	started := time.Now()
	tx, err := db.BeginTx(ctx, nil)
	if observer != nil {
		observer.beginWait = time.Since(started)
	}
	return tx, err
}

func commitObservedTx(tx *sql.Tx, observer *operationObserver) error {
	started := time.Now()
	err := tx.Commit()
	if observer != nil {
		observer.commitDuration = time.Since(started)
	}
	return err
}

// Upcaster turns one persisted payload version into the next version. It must
// be pure: the input Event is copied by value, and the returned payload is the
// only state visible to the current fold.
type Upcaster func(Event) (Event, error)

// EventKindRegistration is the closed schema and projection contract for one
// event kind. Upcasters are keyed by their source version and must form a
// complete chain from MinSupported through CurrentVersion.
type EventKindRegistration struct {
	CurrentVersion int
	MinSupported   int
	Upcasters      map[int]Upcaster
	Fold           projectionMutation
}

// eventKindRegistry is the one registry used by live application, rebuild, and
// point-in-time reconstruction. Keeping version and fold metadata together
// prevents a reader from accidentally accepting a version that its fold cannot
// decode.
var eventKindRegistry = map[string]EventKindRegistration{
	"product.created":                 {CurrentVersion: 1, MinSupported: 1, Fold: foldProductCreated},
	"product.renamed":                 {CurrentVersion: 1, MinSupported: 1, Fold: foldProductRenamed},
	"product.stage_changed":           {CurrentVersion: 1, MinSupported: 1, Fold: foldProductStageChanged},
	"project.created":                 {CurrentVersion: 1, MinSupported: 1, Fold: foldProjectCreated},
	"project.renamed":                 {CurrentVersion: 1, MinSupported: 1, Fold: foldProjectRenamed},
	"project.locator_added":           {CurrentVersion: 1, MinSupported: 1, Fold: foldProjectLocatorAdded},
	"project.locator_updated":         {CurrentVersion: 1, MinSupported: 1, Fold: foldProjectLocatorUpdated},
	"project.locator_removed":         {CurrentVersion: 1, MinSupported: 1, Fold: foldProjectLocatorRemoved},
	"work.created":                    {CurrentVersion: 2, MinSupported: 1, Upcasters: map[int]Upcaster{1: upcastWorkCreatedV1}, Fold: foldWorkCreated},
	"work.intent_revised":             {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkIntentRevised},
	"work.memberships_replaced":       {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkMembershipsReplaced},
	"work.transitioned":               {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkTransitioned},
	"work.superseded":                 {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkSuperseded},
	"work.reopened":                   {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkReopened},
	"work.reopened_from_superseded":   {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkReopenedFromSuperseded},
	"relation.added":                  {CurrentVersion: 1, MinSupported: 1, Fold: foldRelationAdded},
	"relation.removed":                {CurrentVersion: 1, MinSupported: 1, Fold: foldRelationRemoved},
	"product_project.added":           {CurrentVersion: 1, MinSupported: 1, Fold: foldProductProjectAdded},
	"product_project.removed":         {CurrentVersion: 1, MinSupported: 1, Fold: foldProductProjectRemoved},
	"product_project.role_changed":    {CurrentVersion: 1, MinSupported: 1, Fold: foldProductProjectRoleChanged},
	"work_project.added":              {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkProjectAdded},
	"work_project.removed":            {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkProjectRemoved},
	"work_project.role_changed":       {CurrentVersion: 1, MinSupported: 1, Fold: foldWorkProjectRoleChanged},
	"compaction_link.published":       {CurrentVersion: 1, MinSupported: 1, Fold: foldCompactionLinkPublished},
	"epic_entry.added":                {CurrentVersion: 1, MinSupported: 1, Fold: foldEpicEntryAdded},
	"epic_entry.removed":              {CurrentVersion: 1, MinSupported: 1, Fold: foldEpicEntryRemoved},
	"epic_entry.reordered":            {CurrentVersion: 1, MinSupported: 1, Fold: foldEpicEntryReordered},
	"epic_entry.requiredness_changed": {CurrentVersion: 1, MinSupported: 1, Fold: foldEpicEntryRequirednessChanged},
}

func validateEventKindRegistry() error {
	for kind, registration := range eventKindRegistry {
		if registration.Fold == nil || registration.MinSupported < 1 || registration.MinSupported > registration.CurrentVersion {
			return fmt.Errorf("event kind %q has invalid registration bounds", kind)
		}
		for version := registration.MinSupported; version < registration.CurrentVersion; version++ {
			if registration.Upcasters[version] == nil {
				return fmt.Errorf("event kind %q has no upcaster from version %d", kind, version)
			}
		}
	}
	return nil
}

func upcastEvent(event Event) (Event, error) {
	registration, ok := registeredEventKind(event.Kind)
	if !ok {
		return Event{}, unknownEventKind(event.Kind)
	}
	if event.PayloadVersion < registration.MinSupported || event.PayloadVersion > registration.CurrentVersion {
		return Event{}, unsupportedEventVersion(event, registration)
	}
	for event.PayloadVersion < registration.CurrentVersion {
		upcaster := registration.Upcasters[event.PayloadVersion]
		if upcaster == nil {
			return Event{}, unsupportedEventVersion(event, registration)
		}
		var err error
		event, err = upcaster(event)
		if err != nil {
			return Event{}, err
		}
		if event.PayloadVersion <= 0 {
			return Event{}, unsupportedEventVersion(event, registration)
		}
	}
	if event.PayloadVersion != registration.CurrentVersion {
		return Event{}, unsupportedEventVersion(event, registration)
	}
	return event, nil
}

func validateRegisteredEvent(event Event) error {
	_, err := upcastEvent(event)
	return err
}

func foldRegisteredEvent(ctx context.Context, tx *sql.Tx, event Event) error {
	registration, ok := registeredEventKind(event.Kind)
	if !ok {
		return attributeFailure(unknownEventKind(event.Kind), event, "fold")
	}
	current, err := upcastEvent(event)
	if err != nil {
		return attributeFailure(err, event, "upcast")
	}
	if current.PayloadVersion != registration.CurrentVersion {
		return attributeFailure(unsupportedEventVersion(event, registration), event, "upcast")
	}
	if err := registration.Fold(ctx, tx, current); err != nil {
		stage := StageFold
		var failure *Failure
		if failureAs(err, &failure) && failure.Stage != "" {
			stage = failure.Stage
		}
		return attributeFailure(err, event, stage)
	}
	return nil
}

func registeredEventKind(kind string) (EventKindRegistration, bool) {
	registration, ok := eventKindRegistry[kind]
	return registration, ok
}

func unsupportedEventVersion(event Event, registration EventKindRegistration) *Failure {
	return newFailure(KindUnsupportedPayloadVersion, "fold_event",
		fmt.Sprintf("event %s uses payload version %d; supported range is %d..%d", event.EventID, event.PayloadVersion, registration.MinSupported, registration.CurrentVersion), false,
		"upcast the event or install a binary that supports its payload version")
}

// ApplyOperation appends and folds one operation in one transaction.
func ApplyOperation(ctx context.Context, s *Store, operation Operation) error {
	_, err := ApplyOperationWithResult(ctx, s, operation)
	return err
}

// ApplyOperationWithResult appends and folds one operation in one transaction,
// returning the bounded impact of any Product↔Project membership change.
func ApplyOperationWithResult(ctx context.Context, s *Store, operation Operation) (ApplyOperationResult, error) {
	return applyOperationWithResult(ctx, s, operation, nil)
}

// ApplyOperationTx applies one domain operation to a caller-owned transaction.
// The caller is responsible for commit or rollback; this is the mutation seam
// used when authorization, approval consumption, idempotency, and the domain
// effect must share one transaction.
func ApplyOperationTx(ctx context.Context, tx *sql.Tx, operation Operation) (ApplyOperationResult, error) {
	var output ApplyOperationResult
	if tx == nil {
		return output, newFailure(KindUnavailable, "apply_operation", "transaction is not open", false, "open a mutation transaction")
	}
	if len(operation.Events) == 0 {
		return output, newFailure(KindInvalidOperation, "apply_operation", "operation has no events", false, "supply at least one accepted event")
	}
	for subject, expected := range operation.ExpectedVersions {
		if !subject.Type.valid() || subject.ID == "" || expected < 0 {
			return output, newFailure(KindInvalidOperation, "apply_operation", "expected versions must use recognized typed subjects, non-empty IDs, and non-negative versions", false, "supply a typed subject reference")
		}
	}
	if err := enterFold(ctx, tx); err != nil {
		return output, err
	}
	checked := make(map[SubjectRef]bool, len(operation.ExpectedVersions))
	for _, event := range operation.Events {
		if err := event.validate(); err != nil {
			return output, err
		}
		if err := validateRegisteredEvent(event); err != nil {
			return output, attributeFailure(err, event, "upcast")
		}
		ref := VersionRef(event.SubjectType, event.SubjectID)
		if expected, hasExpected := operation.ExpectedVersions[ref]; hasExpected && !checked[ref] {
			got, exists, err := projectionVersion(ctx, tx, event.SubjectType, event.SubjectID)
			if err != nil {
				return output, err
			}
			if (expected == 0 && exists) || (expected > 0 && (!exists || got != expected)) {
				return output, versionConflict(event.SubjectType, event.SubjectID, expected, got, exists)
			}
			checked[ref] = true
		}
		seq, err := AppendEvent(ctx, tx, event)
		if err != nil {
			return output, err
		}
		event.Seq = seq
		if err := foldRegisteredEvent(ctx, tx, event); err != nil {
			return output, err
		}
		output.EventIDs = append(output.EventIDs, event.EventID)
	}
	var err error
	output.Impact, err = membershipImpact(ctx, tx, operation)
	if err != nil {
		return output, err
	}
	if err := validateMembershipInvariantsTx(ctx, tx); err != nil {
		return output, err
	}
	if err := validateEpicInvariantsTx(ctx, tx); err != nil {
		return output, err
	}
	if err := leaveFold(ctx, tx); err != nil {
		return output, err
	}
	return output, nil
}

// preCommitHook is deliberately unexported. Tests use it to stop a process at
// the exact point where all writes are present but before SQLite commit; the
// production API always passes nil and has no mutable hook or callback state.
type preCommitHook func() error

func applyOperationWithResult(ctx context.Context, s *Store, operation Operation, beforeCommit preCommitHook) (ApplyOperationResult, error) {
	return applyOperationObserved(ctx, s, operation, beforeCommit, nil)
}

func applyOperationObserved(ctx context.Context, s *Store, operation Operation, beforeCommit preCommitHook, observer *operationObserver) (ApplyOperationResult, error) {
	var output ApplyOperationResult
	if s == nil || s.db == nil {
		return output, newFailure(KindUnavailable, "apply_operation", "store is not open", false,
			"open a store before applying an operation")
	}
	if len(operation.Events) == 0 {
		return output, newFailure(KindInvalidOperation, "apply_operation", "operation has no events", false,
			"supply at least one accepted event")
	}
	for subject, expected := range operation.ExpectedVersions {
		if !subject.Type.valid() || subject.ID == "" || expected < 0 {
			return output, newFailure(KindInvalidOperation, "apply_operation",
				"expected versions must use recognized typed subjects, non-empty IDs, and non-negative versions", false,
				"supply a typed subject reference and zero for a subject that must not yet exist")
		}
	}

	tx, err := beginObservedTx(ctx, s.db, observer)
	if err != nil {
		return output, wrapFailure(KindUnavailable, "apply_operation", "cannot begin domain operation", true,
			"retry once the database is writable", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	if err := enterFold(ctx, tx); err != nil {
		return output, rollback(err)
	}

	checked := make(map[SubjectRef]bool, len(operation.ExpectedVersions))
	for _, event := range operation.Events {
		if err := event.validate(); err != nil {
			return output, rollback(err)
		}
		// Validate and upcast before AppendEvent so unsupported versions and
		// incomplete chains cannot leave even a log row behind. The fold below
		// deliberately repeats the same registry path after the original bytes
		// have been appended.
		if err := validateRegisteredEvent(event); err != nil {
			return output, rollback(attributeFailure(err, event, "upcast"))
		}
		ref := VersionRef(event.SubjectType, event.SubjectID)
		if expected, hasExpected := operation.ExpectedVersions[ref]; hasExpected && !checked[ref] {
			got, exists, err := projectionVersion(ctx, tx, event.SubjectType, event.SubjectID)
			if err != nil {
				return output, rollback(err)
			}
			if (expected == 0 && exists) || (expected > 0 && (!exists || got != expected)) {
				return output, rollback(versionConflict(event.SubjectType, event.SubjectID, expected, got, exists))
			}
			checked[ref] = true
		}
		seq, err := AppendEvent(ctx, tx, event)
		if err != nil {
			return output, rollback(err)
		}
		event.Seq = seq
		if err := foldRegisteredEvent(ctx, tx, event); err != nil {
			return output, rollback(err)
		}
		output.EventIDs = append(output.EventIDs, event.EventID)
	}
	if err := validateMembershipInvariantsTx(ctx, tx); err != nil {
		return output, rollback(err)
	}
	if err := validateEpicInvariantsTx(ctx, tx); err != nil {
		return output, rollback(err)
	}
	output.Impact, err = membershipImpact(ctx, tx, operation)
	if err != nil {
		return output, rollback(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return output, rollback(err)
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return output, rollback(err)
		}
	}
	if err := commitObservedTx(tx, observer); err != nil {
		return output, wrapFailure(KindUnavailable, "apply_operation", "cannot commit domain operation", true,
			"retry once the database is writable", err)
	}
	return output, nil
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
		_ = dropActiveResearchRebuildSnapshot(ctx, tx)
		_ = tx.Rollback()
		return cause
	}
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := snapshotActiveResearchForRebuild(ctx, tx); err != nil {
		return rollback(err)
	}
	// Active research is direct-table authority, but its work-item FKs prevent
	// deleting the fold projections in place. The transaction snapshots and
	// restores those rows byte-for-byte; the event log never becomes their source.
	events, err := readEvents(ctx, tx)
	if err != nil {
		return rollback(err)
	}
	// Version-window and chain failures are rejected before the first
	// projection DELETE. Fold/decode failures remain transactionally atomic, and
	// are attributed by the shared fold path below.
	for _, event := range events {
		if err := validateRegisteredEvent(event); err != nil {
			return rollback(attributeFailure(err, event, "upcast"))
		}
	}
	// Relations reference work_items, so clear the dependent projection first;
	// replay then restores the same event order under the fold guard.
	for _, table := range []string{"epic_entries", "relations", "work_projects", "work_items", "product_projects", "project_locators", "products", "projects"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return rollback(wrapFailure(KindUnavailable, "rebuild_from_log",
				"cannot clear "+table+" projection", true,
				"retry once the database is writable", err))
		}
	}
	for _, event := range events {
		// Historical knowledge is git-derived. Domain-log replay must not
		// rewrite archived_work, scope edges, or git watermarks.
		if err := foldRegisteredEvent(ctx, tx, event); err != nil {
			return rollback(err)
		}
	}
	if err := restoreActiveResearchAfterRebuild(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := validateMembershipInvariantsTx(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := validateEpicInvariantsTx(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := dropActiveResearchRebuildSnapshot(ctx, tx); err != nil {
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

func versionConflict(subjectType SubjectType, subjectID string, expected, got int64, exists bool) *Failure {
	actual := "missing"
	if exists {
		actual = fmt.Sprintf("%d", got)
	}
	return newFailure(KindVersionConflict, "apply_operation",
		fmt.Sprintf("%s %s has version %s, want %d", subjectType, subjectID, actual, expected), false,
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
		SELECT seq, event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload
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
		if err := rows.Scan(&event.Seq, &event.EventID, &event.Kind, &event.SubjectType, &event.SubjectID,
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
	if err := json.Unmarshal(event.Payload, target); err != nil {
		failure := wrapFailure(KindInvalidPayload, "fold_event",
			fmt.Sprintf("event %s payload does not match its kind", event.EventID), false,
			"repair the event payload before rebuilding", err)
		failure.Stage = StageDecode
		return failure
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
