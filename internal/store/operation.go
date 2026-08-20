package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
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
	ID                              string  `json:"id"`
	DisplayName                     string  `json:"display_name"`
	StageMaturityOverride           *string `json:"stage_maturity_override,omitempty"`
	StageAudienceCommitmentOverride *string `json:"stage_audience_commitment_override,omitempty"`
	Version                         int64   `json:"version"`
	CreatedAt                       string  `json:"created_at"`
	UpdatedAt                       string  `json:"updated_at"`
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

// EventAppendAuthority is the closed set of routes allowed to append an event.
// The zero value is deliberately invalid so every registry entry must opt in.
type EventAppendAuthority uint8

const (
	EventAppendAuthorityInvalid EventAppendAuthority = iota
	EventAppendAuthorityGeneric
	EventAppendAuthorityWorkflow
)

type eventPayloadSemantic[T any] func(Event, T) error

// EventKindRegistration is the closed schema and projection contract for one
// event kind. Upcasters are keyed by their source version and must form a
// complete chain from MinSupported through CurrentVersion.
type EventKindRegistration struct {
	CurrentVersion  int
	MinSupported    int
	Upcasters       map[int]Upcaster
	ValidatePayload func(Event) error
	Authority       EventAppendAuthority
	Fold            projectionMutation
}

func registerEventKind[T any](currentVersion, minSupported int, upcasters map[int]Upcaster, authority EventAppendAuthority, fold projectionMutation, semantic eventPayloadSemantic[T]) EventKindRegistration {
	if upcasters == nil {
		upcasters = map[int]Upcaster{}
	}
	return EventKindRegistration{
		CurrentVersion: currentVersion,
		MinSupported:   minSupported,
		Upcasters:      upcasters,
		ValidatePayload: func(event Event) error {
			var payload T
			if err := decodeRegisteredPayload(event, &payload); err != nil {
				return err
			}
			if semantic != nil {
				return semantic(event, payload)
			}
			return nil
		},
		Authority: authority,
		Fold:      fold,
	}
}

func decodeRegisteredPayload(event Event, target any) error {
	if !isJSONObject(event.Payload) {
		return newFailure(KindInvalidPayload, "validate_event", "payload is not a JSON object", false, "encode the payload as a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(event.Payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		failure := wrapFailure(KindInvalidPayload, "validate_event", fmt.Sprintf("event %s payload does not match its registered schema", event.EventID), false, "repair the event payload", err)
		failure.Stage = StageDecode
		return failure
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		failure := newFailure(KindInvalidPayload, "validate_event", fmt.Sprintf("event %s payload contains trailing data", event.EventID), false, "repair the event payload")
		failure.Stage = StageDecode
		return failure
	}
	return nil
}

// eventKindRegistry is the one registry used by live application, rebuild, and
// point-in-time reconstruction. Keeping version and fold metadata together
// prevents a reader from accidentally accepting a version that its fold cannot
// decode.
var eventKindRegistry = map[string]EventKindRegistration{
	"product.created":                         registerEventKind[productCreatedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProductCreated, nil),
	"product.renamed":                         registerEventKind[productRenamedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProductRenamed, nil),
	"product.stage_changed":                   registerEventKind[productStageChangedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProductStageChanged, nil),
	"project.created":                         registerEventKind[projectCreatedPayload](2, 1, map[int]Upcaster{1: upcastProjectCreatedV1}, EventAppendAuthorityGeneric, foldProjectCreated, nil),
	"project.renamed":                         registerEventKind[projectRenamedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProjectRenamed, nil),
	"project.stage_changed":                   registerEventKind[projectStageChangedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProjectStageChanged, nil),
	"project.locator_added":                   registerEventKind[projectLocatorPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProjectLocatorAdded, nil),
	"project.locator_updated":                 registerEventKind[projectLocatorPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProjectLocatorUpdated, nil),
	"project.locator_removed":                 registerEventKind[projectLocatorPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProjectLocatorRemoved, nil),
	"project.governing_requirement_declared":  registerEventKind[GoverningRequirement](1, 1, nil, EventAppendAuthorityGeneric, foldProjectGoverningRequirementDeclared, nil),
	"project.governing_requirement_withdrawn": registerEventKind[GoverningRequirement](1, 1, nil, EventAppendAuthorityGeneric, foldProjectGoverningRequirementWithdrawn, nil),
	"work.created":                            registerEventKind[workCreatedPayload](2, 1, map[int]Upcaster{1: upcastWorkCreatedV1}, EventAppendAuthorityGeneric, foldWorkCreated, nil),
	"work.intent_revised":                     registerEventKind[workIntentPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkIntentRevised, nil),
	"work.memberships_replaced":               registerEventKind[workMembershipsPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkMembershipsReplaced, nil),
	"work.worktree_created":                   registerEventKind[worktreeCreatedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorktreeCreated, nil),
	"work.resource_claimed":                   registerEventKind[resourceClaimedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldResourceClaimed, nil),
	"work.message_sent":                       registerEventKind[messageSentPayload](1, 1, nil, EventAppendAuthorityGeneric, foldMessageSent, nil),
	"work.observation_recorded":               registerEventKind[workObservationRecordedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkObservationRecorded, nil),
	"work.message_withdrawn":                  registerEventKind[messageWithdrawnPayload](1, 1, nil, EventAppendAuthorityGeneric, foldMessageWithdrawn, nil),
	"work.resource_claim_released":            registerEventKind[resourceClaimReleasedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldResourceClaimReleased, nil),
	"work.worktree_reclaimed":                 registerEventKind[worktreeReclaimedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorktreeReclaimed, nil),
	"work.transitioned":                       registerEventKind[workTransitionPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkTransitioned, nil),
	"work.superseded":                         registerEventKind[workSupersededPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkSuperseded, nil),
	"work.reopened":                           registerEventKind[workReopenedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkReopened, nil),
	"work.reopened_from_superseded":           registerEventKind[workReopenedFromSupersededPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkReopenedFromSuperseded, nil),
	"relation.added":                          registerEventKind[relationPayload](1, 1, nil, EventAppendAuthorityGeneric, foldRelationAdded, nil),
	"relation.removed":                        registerEventKind[relationPayload](1, 1, nil, EventAppendAuthorityGeneric, foldRelationRemoved, nil),
	"product_project.added":                   registerEventKind[membershipPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProductProjectAdded, nil),
	"product_project.removed":                 registerEventKind[membershipPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProductProjectRemoved, nil),
	"product_project.role_changed":            registerEventKind[membershipPayload](1, 1, nil, EventAppendAuthorityGeneric, foldProductProjectRoleChanged, nil),
	"work_project.added":                      registerEventKind[membershipPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkProjectAdded, nil),
	"work_project.removed":                    registerEventKind[membershipPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkProjectRemoved, nil),
	"work_project.role_changed":               registerEventKind[membershipPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkProjectRoleChanged, nil),
	"compaction_link.published":               registerEventKind[compactionLinkPayload](2, 1, map[int]Upcaster{1: upcastCompactionLinkPublishedV1}, EventAppendAuthorityGeneric, foldCompactionLinkPublished, nil),
	"managed_resource.created":                registerEventKind[managedResourceCreatedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldManagedResourceCreated, nil),
	"managed_resource.consumer_added":         registerEventKind[managedResourceConsumerAddedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldManagedResourceConsumerAdded, nil),
	"domain.project_attachments_replaced":     registerEventKind[domainProjectAttachmentsReplacedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldDomainProjectAttachmentsReplaced, nil),
	"domain.resource_attachments_replaced":    registerEventKind[domainResourceAttachmentsReplacedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldDomainResourceAttachmentsReplaced, nil),
	"initiative_entry.added":                  registerEventKind[initiativeEntryPayload](1, 1, nil, EventAppendAuthorityGeneric, foldInitiativeEntryAdded, nil),
	"initiative_entry.removed":                registerEventKind[initiativeEntryPayload](1, 1, nil, EventAppendAuthorityGeneric, foldInitiativeEntryRemoved, nil),
	"initiative_entry.reordered":              registerEventKind[initiativeEntryPayload](1, 1, nil, EventAppendAuthorityGeneric, foldInitiativeEntryReordered, nil),
	"initiative_entry.requiredness_changed":   registerEventKind[initiativeEntryPayload](1, 1, nil, EventAppendAuthorityGeneric, foldInitiativeEntryRequirednessChanged, nil),
	"initiative.narrative_revised":            registerEventKind[initiativeNarrativePayload](1, 1, nil, EventAppendAuthorityGeneric, foldInitiativeNarrativeRevised, nil),
	WorkerDispatched:                          registerEventKind[WorkerDispatchedPayload](3, 1, map[int]Upcaster{1: upcastWorkerDispatchedV1, 2: upcastWorkerDispatchedV2}, EventAppendAuthorityGeneric, foldWorkerDispatched, validateWorkerDispatchedPayload),
	WorkerCompleted:                           registerEventKind[WorkerCompletedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkerCompleted, validateWorkerCompletedPayload),
	WorkerFailed:                              registerEventKind[WorkerFailedPayload](1, 1, nil, EventAppendAuthorityGeneric, foldWorkerFailed, validateWorkerFailedPayload),
	WorkflowDefinitionSelected:                workflowRegistration[workflowDefinitionSelectedPayload](1, nil, foldWorkflowDefinitionSelected),
	WorkflowContractApproved:                  workflowRegistration[workflowContractApprovedPayload](3, map[int]Upcaster{1: upcastWorkflowContractApprovedV1, 2: upcastWorkflowContractApprovedV2}, foldWorkflowContractApproved),
	WorkflowOverlapResolved:                   workflowRegistration[workflowOverlapResolvedPayload](1, nil, foldWorkflowOverlapResolved),
	WorkflowContractSuperseded:                workflowRegistration[workflowContractSupersededPayload](1, nil, foldWorkflowContractSuperseded),
	WorkflowCandidateSetRevised:               workflowRegistration[workflowCandidateSetRevisedPayload](1, nil, foldWorkflowCandidateSetRevised),
	WorkflowActorRecorded:                     workflowRegistration[workflowActorRecordedPayload](1, nil, foldWorkflowActorRecorded),
	WorkflowActionStarted:                     workflowRegistration[workflowActionStartedPayload](1, nil, foldWorkflowActionStarted),
	WorkflowActionCheckpointed:                workflowRegistration[workflowActionCheckpointedPayload](1, nil, foldWorkflowActionCheckpointed),
	WorkflowActionCompleted:                   workflowRegistration[workflowActionCompletedPayload](2, map[int]Upcaster{1: upcastWorkflowActionCompletedV1}, foldWorkflowActionCompleted),
	WorkflowActionFailed:                      workflowRegistration[workflowActionFailedPayload](1, nil, foldWorkflowActionFailed),
	WorkflowEvidenceBound:                     workflowRegistration[workflowEvidenceBoundPayload](1, nil, foldWorkflowEvidenceBound),
	WorkflowVerdictRecorded:                   workflowRegistration[workflowVerdictRecordedPayload](1, nil, foldWorkflowVerdictRecorded),
	WorkflowPremiseConfirmed:                  workflowRegistration[workflowPremiseConfirmedPayload](1, nil, foldWorkflowPremiseConfirmed),
	WorkflowSuccessorLinked:                   workflowRegistration[workflowSuccessorLinkedPayload](1, nil, foldWorkflowSuccessorLinked),
	WorkflowImpactDeclared:                    workflowRegistration[workflowImpactDeclaredPayload](1, nil, foldWorkflowImpactDeclared),
	WorkflowImpactNoticeRecorded:              workflowRegistration[workflowImpactNoticeRecordedPayload](2, map[int]Upcaster{1: upcastWorkflowImpactNoticeRecordedV1}, foldWorkflowImpactNoticeRecorded),
	WorkflowConditionAdded:                    workflowRegistration[workflowConditionAddedPayload](1, nil, foldWorkflowConditionAdded),
	WorkflowConditionResolved:                 workflowRegistration[workflowConditionResolvedPayload](1, nil, foldWorkflowConditionResolved),
	WorkflowConditionCancelled:                workflowRegistration[workflowConditionCancelledPayload](1, nil, foldWorkflowConditionCancelled),
	WorkflowContextCheckpointed:               workflowRegistration[workflowContextCheckpointedPayload](1, nil, foldWorkflowContextCheckpointed),
	WorkflowNativeRunRecorded:                 workflowRegistration[workflowNativeRunRecordedPayload](1, nil, foldWorkflowNativeRunRecorded),
	WorkflowContextBoundaryCrossed:            workflowRegistration[workflowContextBoundaryCrossedPayload](1, nil, foldWorkflowContextBoundaryCrossed),
	WorkflowCompleted:                         workflowRegistration[workflowCompletedPayload](2, map[int]Upcaster{1: upcastWorkflowCompletedV1}, foldWorkflowCompleted),
}

func validateEventKindRegistry() error {
	for kind, registration := range eventKindRegistry {
		if kind == "" {
			return fmt.Errorf("event kind key is empty")
		}
		if registration.Authority != EventAppendAuthorityGeneric && registration.Authority != EventAppendAuthorityWorkflow {
			return fmt.Errorf("event kind %q has invalid append authority", kind)
		}
		if registration.ValidatePayload == nil || registration.Fold == nil || registration.Upcasters == nil || registration.MinSupported < 1 || registration.MinSupported > registration.CurrentVersion {
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
	return upcastEventWithRegistration(event, registration)
}

func upcastEventWithRegistration(event Event, registration EventKindRegistration) (Event, error) {
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

type preparedRegisteredEvent struct {
	current      Event
	registration EventKindRegistration
	stage        FailureStage
}

func prepareRegisteredEvent(event Event) (preparedRegisteredEvent, error) {
	registration, ok := registeredEventKind(event.Kind)
	if !ok {
		return preparedRegisteredEvent{stage: StageFold}, unknownEventKind(event.Kind)
	}
	current, err := upcastEventWithRegistration(event, registration)
	if err != nil {
		return preparedRegisteredEvent{stage: StageUpcast}, err
	}
	if err := registration.ValidatePayload(current); err != nil {
		return preparedRegisteredEvent{stage: StageDecode}, err
	}
	return preparedRegisteredEvent{current: current, registration: registration}, nil
}

func validateRegisteredEvent(event Event) error {
	_, err := prepareRegisteredEvent(event)
	return err
}

func foldRegisteredEvent(ctx context.Context, tx *sql.Tx, event Event) error {
	prepared, err := prepareRegisteredEvent(event)
	if err != nil {
		return attributeFailure(err, event, prepared.stage)
	}
	if err := prepared.registration.Fold(ctx, tx, prepared.current); err != nil {
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

// ApplyOperationTx applies one domain operation within the opaque store-owned
// transaction supplied by Transact. It is the mutation seam used when
// authorization, approval consumption, idempotency, and the domain effect must
// share one transaction.
func ApplyOperationTx(ctx context.Context, transaction *Transaction, operation Operation) (ApplyOperationResult, error) {
	tx, err := transactionSQL(transaction, "apply_operation")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	return applyOperationTx(ctx, tx, operation, true, false)
}

// applyWorkflowOperationTx is the private append route used by the workflow
// dispatcher and initialization path. Generic callers cannot acquire this
// authority through ApplyOperation or ApplyOperationTx.
func applyWorkflowOperationTx(ctx context.Context, tx *sql.Tx, operation Operation) (ApplyOperationResult, error) {
	return applyOperationTx(ctx, tx, operation, false, true)
}

func applyOperationTx(ctx context.Context, tx *sql.Tx, operation Operation, ownFoldGuard bool, workflowAuthority bool) (ApplyOperationResult, error) {
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
	if !workflowAuthority {
		for _, event := range operation.Events {
			if isWorkflowAdvancementEvent(event.Kind) {
				return output, workflowDispatcherRequired(event.Kind)
			}
		}
	}
	if ownFoldGuard {
		if err := enterFold(ctx, tx); err != nil {
			return output, err
		}
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
		var seq Sequence
		var err error
		if workflowAuthority {
			seq, err = appendEvent(ctx, tx, event, true)
		} else {
			seq, err = AppendEvent(ctx, tx, event)
		}
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
	if err := validateDomainAttachmentInvariantsTx(ctx, tx); err != nil {
		return output, err
	}
	if err := validateInitiativeInvariantsTx(ctx, tx); err != nil {
		return output, err
	}
	if ownFoldGuard {
		if err := leaveFold(ctx, tx); err != nil {
			return output, err
		}
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

	for _, event := range operation.Events {
		if isWorkflowAdvancementEvent(event.Kind) {
			return output, rollback(workflowDispatcherRequired(event.Kind))
		}
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
	if err := validateDomainAttachmentInvariantsTx(ctx, tx); err != nil {
		return output, rollback(err)
	}
	if err := validateInitiativeInvariantsTx(ctx, tx); err != nil {
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
	// Product knowledge homes are Git-derived authority and are not replayed
	// from the domain event log. Snapshot them while their Project/locator FKs
	// are rebuilt, then restore the exact rows after replay.
	type knowledgeHomeSnapshot struct{ productID, projectID, locatorID string }
	var knowledgeHomes []knowledgeHomeSnapshot
	homeRows, err := tx.QueryContext(ctx, `SELECT product_id,project_id,locator_id FROM product_knowledge_homes ORDER BY product_id`)
	if err != nil {
		return rollback(wrapFailure(KindUnavailable, "rebuild_from_log", "cannot snapshot Product knowledge homes", true, "retry once the knowledge projection is readable", err))
	}
	for homeRows.Next() {
		var home knowledgeHomeSnapshot
		if err := homeRows.Scan(&home.productID, &home.projectID, &home.locatorID); err != nil {
			homeRows.Close()
			return rollback(err)
		}
		knowledgeHomes = append(knowledgeHomes, home)
	}
	if err := homeRows.Err(); err != nil {
		homeRows.Close()
		return rollback(err)
	}
	if err := homeRows.Close(); err != nil {
		return rollback(err)
	}
	// Version-window and chain failures are rejected before the first
	// projection DELETE. Fold/decode failures remain transactionally atomic, and
	// are attributed by the shared fold path below.
	replayCtx := workflowReplayContext(ctx)
	for _, event := range events {
		prepared, err := prepareRegisteredEvent(event)
		if err != nil {
			return rollback(attributeFailure(err, event, prepared.stage))
		}
	}
	// Relations reference work_items, so clear the dependent projection first;
	// replay then restores the same event order under the fold guard.
	for _, table := range []string{
		// Domain attachments and C15 membership depend on Product projections;
		// clear their edges and sets before Product memberships and resources.
		"domain_resource_attachment_edges", "domain_project_attachment_edges",
		"domain_resource_attachment_sets", "domain_project_attachment_sets",
		"resource_products", "managed_resources",
		// work-referencing RESTRICT-FK tables clear before work_items:
		// observations (CD-0030), messages (CD-0029), claims (CD-0028).
		"work_observations", "work_messages", "resource_claims",
		"worker_attempts",
		"workflow_contract_law_revisions", "workflow_contract_law_modifications", "workflow_overlap_resolutions",
		"workflow_contract_verification_obligations", "workflow_contract_law_additions", "workflow_contract_domain_relation_modifications", "workflow_contract_domain_modifications", "workflow_contract_affected_domains", "workflow_law_addition_reservations", "workflow_architecture_bindings",
		"workflow_premise_confirmations", "workflow_context_boundaries", "workflow_context_checkpoints", "workflow_native_runs", "workflow_impact_notices", "workflow_impact_edges",
		"workflow_external_conditions", "workflow_checkpoints", "workflow_candidate_sets",
		"workflow_contracts", "workflow_decision_records", "workflow_instances", "workflow_actors",
		"initiative_entries", "relations", "work_projects", "work_items", "product_projects",
		"project_governing_requirements", "product_knowledge_homes", "project_locators", "products", "projects",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return rollback(wrapFailure(KindUnavailable, "rebuild_from_log",
				"cannot clear "+table+" projection", true,
				"retry once the database is writable", err))
		}
	}
	for _, event := range events {
		// Historical knowledge is git-derived. Domain-log replay must not
		// rewrite archived_work, scope edges, or git watermarks.
		if err := foldRegisteredEvent(replayCtx, tx, event); err != nil {
			return rollback(err)
		}
	}
	if err := restoreActiveResearchAfterRebuild(ctx, tx); err != nil {
		return rollback(err)
	}
	for _, home := range knowledgeHomes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES(?,?,?)`, home.productID, home.projectID, home.locatorID); err != nil {
			return rollback(wrapFailure(KindUnavailable, "rebuild_from_log", "cannot restore Product knowledge home", true, "retry once the database is writable", err))
		}
	}
	if err := validateMembershipInvariantsTx(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := validateDomainAttachmentInvariantsTx(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := validateInitiativeInvariantsTx(ctx, tx); err != nil {
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
	f := newFailure(KindVersionConflict, "apply_operation",
		fmt.Sprintf("%s %s has version %s, want %d", subjectType, subjectID, actual, expected), false,
		"reload the subject and retry with its current version")
	// Carry the typed current version so higher layers can surface
	// error.current_version structurally instead of regexing the detail string.
	f.CurrentVersions = []SubjectCurrentVersion{{SubjectType: subjectType, SubjectID: subjectID, Version: got, Exists: exists}}
	return f
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
	DisplayName                     string          `json:"display_name"`
	StageMaturityOverride           json.RawMessage `json:"stage_maturity_override"`
	StageAudienceCommitmentOverride json.RawMessage `json:"stage_audience_commitment_override"`
}

type projectRenamedPayload struct {
	DisplayName string `json:"display_name"`
}

type projectStageChangedPayload struct {
	StageMaturityOverride           json.RawMessage `json:"stage_maturity_override"`
	StageAudienceCommitmentOverride json.RawMessage `json:"stage_audience_commitment_override"`
	Reason                          string          `json:"reason,omitempty"`
	ExpectedVersion                 int64           `json:"expected_version,omitempty"`
	ResultingVersion                int64           `json:"resulting_version,omitempty"`
}

func upcastProjectCreatedV1(event Event) (Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(event.Payload, &fields); err != nil || fields == nil {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "project.created v1 payload is not a JSON object", false, "repair the stored Project event payload")
	}
	if _, exists := fields["stage_maturity_override"]; exists {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "project.created v1 cannot contain a stage override", false, "use project.created v2 for paired stage overrides")
	}
	if _, exists := fields["stage_audience_commitment_override"]; exists {
		return Event{}, newFailure(KindInvalidPayload, "upcast_event", "project.created v1 cannot contain a stage override", false, "use project.created v2 for paired stage overrides")
	}
	fields["stage_maturity_override"] = json.RawMessage("null")
	fields["stage_audience_commitment_override"] = json.RawMessage("null")
	payload, err := json.Marshal(fields)
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "upcast_event", "cannot normalize project.created v1 payload", false, "repair the stored Project event payload", err)
	}
	event.Payload = payload
	event.PayloadVersion = 2
	return event, nil
}

func decodeProjectStagePair(maturityRaw, audienceRaw json.RawMessage, allowAbsent bool) (*string, *string, error) {
	maturityAbsent, audienceAbsent := len(maturityRaw) == 0, len(audienceRaw) == 0
	if maturityAbsent || audienceAbsent {
		if allowAbsent && maturityAbsent && audienceAbsent {
			return nil, nil, nil
		}
		return nil, nil, newFailure(KindInvalidPayload, "fold_event", "Project stage override must contain both fields", false, "supply both stage override fields or neither")
	}
	var maturity, audience *string
	if string(maturityRaw) != "null" {
		var value string
		if err := json.Unmarshal(maturityRaw, &value); err != nil {
			return nil, nil, newFailure(KindInvalidPayload, "fold_event", "Project maturity override is not a string or null", false, "use an accepted maturity value or null")
		}
		maturity = &value
	}
	if string(audienceRaw) != "null" {
		var value string
		if err := json.Unmarshal(audienceRaw, &value); err != nil {
			return nil, nil, newFailure(KindInvalidPayload, "fold_event", "Project audience override is not a string or null", false, "use an accepted audience commitment or null")
		}
		audience = &value
	}
	if (maturity == nil) != (audience == nil) {
		return nil, nil, newFailure(KindInvalidPayload, "fold_event", "Project stage override must contain a paired maturity and audience commitment", false, "supply both override values or set both to null")
	}
	if maturity != nil && !validateProductStage(*maturity, *audience) {
		return nil, nil, newFailure(KindInvalidPayload, "fold_event", "Project stage override contains an invalid value", false, "use accepted maturity and audience commitment values")
	}
	return maturity, audience, nil
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
	overrideMaturity, overrideAudience, err := decodeProjectStagePair(payload.StageMaturityOverride, payload.StageAudienceCommitmentOverride, true)
	if err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO projects (id, display_name, stage_maturity_override, stage_audience_commitment_override, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, event.SubjectID, payload.DisplayName, nullableProjectStage(overrideMaturity), nullableProjectStage(overrideAudience), now, now)
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

func nullableProjectStage(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func foldProjectStageChanged(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProject); err != nil {
		return err
	}
	var payload projectStageChangedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	overrideMaturity, overrideAudience, err := decodeProjectStagePair(payload.StageMaturityOverride, payload.StageAudienceCommitmentOverride, false)
	if err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE projects
		SET stage_maturity_override = ?, stage_audience_commitment_override = ?, version = version + 1, updated_at = ?
		WHERE id = ?`, nullableProjectStage(overrideMaturity), nullableProjectStage(overrideAudience), now, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update Project stage projection", true, "retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify Project stage projection update", true, "retry once the database is readable", err)
	} else if affected == 0 {
		return newFailure(KindProjectionNotFound, "fold_event", "Project does not exist", false, "create the Project before changing its stage")
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
