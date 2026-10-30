package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
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
type scopedProjectionMutation func(context.Context, *sql.Tx, Event, *writeScope) error

// operationObserver is an unexported white-box measurement surface. It is
// passed through the operation call graph rather than stored globally, so
// production callers pay no instrumentation cost and concurrent workers cannot
// overwrite one another's samples.
type operationObserver struct {
	beginWait      time.Duration
	commitDuration time.Duration
}

func beginObservedTx(ctx context.Context, s *Store, durable bool, observer *operationObserver) (*writeTx, error) {
	started := time.Now()
	tx, err := s.beginStoreWriteTx(ctx, durable)
	if observer != nil {
		observer.beginWait = time.Since(started)
	}
	return tx, err
}

func commitObservedTx(tx *writeTx, observer *operationObserver) error {
	started := time.Now()
	err := tx.Commit()
	if observer != nil {
		observer.commitDuration = time.Since(started)
	}
	return err
}

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
// event kind.
type EventKindRegistration struct {
	CurrentVersion  int
	ValidatePayload func(Event) error
	Authority       EventAppendAuthority
	Fold            scopedProjectionMutation
}

func registerEventKind[T any](currentVersion int, authority EventAppendAuthority, fold projectionMutation, semantic eventPayloadSemantic[T]) EventKindRegistration {
	return registerScopedEventKind(currentVersion, authority, func(ctx context.Context, tx *sql.Tx, event Event, _ *writeScope) error {
		return fold(ctx, tx, event)
	}, semantic)
}

func registerScopedEventKind[T any](currentVersion int, authority EventAppendAuthority, fold scopedProjectionMutation, semantic eventPayloadSemantic[T]) EventKindRegistration {
	return EventKindRegistration{
		CurrentVersion: currentVersion,
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

// eventKindRegistry binds each current payload schema to its fold and authority.
var eventKindRegistry = map[string]EventKindRegistration{
	"product.created":                         registerEventKind[productCreatedPayload](1, EventAppendAuthorityGeneric, foldProductCreated, nil),
	"product.renamed":                         registerEventKind[productRenamedPayload](1, EventAppendAuthorityGeneric, foldProductRenamed, nil),
	"product.stage_changed":                   registerEventKind[productStageChangedPayload](1, EventAppendAuthorityGeneric, foldProductStageChanged, nil),
	"project.created":                         registerEventKind[projectCreatedPayload](2, EventAppendAuthorityGeneric, foldProjectCreated, nil),
	"project.renamed":                         registerEventKind[projectRenamedPayload](1, EventAppendAuthorityGeneric, foldProjectRenamed, nil),
	"project.stage_changed":                   registerEventKind[projectStageChangedPayload](1, EventAppendAuthorityGeneric, foldProjectStageChanged, nil),
	"project.locator_added":                   registerEventKind[projectLocatorPayload](2, EventAppendAuthorityGeneric, foldProjectLocatorAdded, nil),
	"project.locator_updated":                 registerEventKind[projectLocatorPayload](2, EventAppendAuthorityGeneric, foldProjectLocatorUpdated, nil),
	"project.locator_removed":                 registerEventKind[projectLocatorPayload](1, EventAppendAuthorityGeneric, foldProjectLocatorRemoved, nil),
	"project.governing_requirement_declared":  registerEventKind[GoverningRequirement](1, EventAppendAuthorityGeneric, foldProjectGoverningRequirementDeclared, nil),
	"project.governing_requirement_withdrawn": registerEventKind[GoverningRequirement](1, EventAppendAuthorityGeneric, foldProjectGoverningRequirementWithdrawn, nil),
	"work.created":                            registerEventKind[workCreatedPayload](4, EventAppendAuthorityGeneric, foldWorkCreated, validateWorkCreatedPayload),
	"work.intent_revised":                     registerEventKind[workIntentPayload](1, EventAppendAuthorityGeneric, foldWorkIntentRevised, nil),
	WorkRemoved:                               registerEventKind[workRemovedPayload](1, EventAppendAuthorityGeneric, foldWorkRemoved, nil),
	"work.memberships_replaced":               registerEventKind[workMembershipsPayload](1, EventAppendAuthorityGeneric, foldWorkMembershipsReplaced, nil),
	"work.worktree_created":                   registerEventKind[worktreeCreatedPayload](1, EventAppendAuthorityGeneric, foldWorktreeCreated, nil),
	"work.message_sent":                       registerEventKind[messageSentPayload](1, EventAppendAuthorityGeneric, foldMessageSent, nil),
	WorkObservationRecorded:                   registerEventKind[workObservationRecordedPayload](1, EventAppendAuthorityGeneric, foldWorkObservationRecorded, nil),
	WorkSized:                                 registerEventKind[workSizedPayload](2, EventAppendAuthorityGeneric, foldWorkSized, nil),
	EventExternalObservationCaptured:          registerEventKind[externalObservationCapturedPayload](1, EventAppendAuthorityGeneric, foldExternalObservationCaptured, nil),
	EventExternalObservationVerified:          registerEventKind[externalObservationVerifiedPayload](1, EventAppendAuthorityGeneric, foldExternalObservationVerified, nil),
	"work.message_withdrawn":                  registerEventKind[messageWithdrawnPayload](1, EventAppendAuthorityGeneric, foldMessageWithdrawn, nil),
	"work.worktree_reclaimed":                 registerEventKind[worktreeReclaimedPayload](1, EventAppendAuthorityGeneric, foldWorktreeReclaimed, nil),
	"work.worktree_removal_settled":           registerEventKind[worktreeRemovalSettledPayload](1, EventAppendAuthorityGeneric, foldWorktreeRemovalSettled, nil),
	"work.worktree_occupancy_released":        registerEventKind[worktreeOccupancyReleasedPayload](2, EventAppendAuthorityGeneric, foldWorktreeOccupancyReleased, nil),
	"work.session_vacated":                    registerEventKind[sessionVacatedPayload](2, EventAppendAuthorityGeneric, foldSessionVacated, validateSessionVacatedPayload),
	"work.session_vacate_landed":              registerEventKind[sessionVacateLandedPayload](1, EventAppendAuthorityGeneric, foldSessionVacateLanded, nil),
	"work.project_handoff_recorded":           registerEventKind[projectHandoffRecordedPayload](1, EventAppendAuthorityGeneric, foldProjectHandoffRecorded, validateProjectHandoffRecordedPayload),
	"work.project_handoff_consumed":           registerEventKind[projectHandoffConsumedPayload](1, EventAppendAuthorityGeneric, foldProjectHandoffConsumed, nil),
	"work.session_claim_landed":               registerEventKind[sessionClaimLandedPayload](2, EventAppendAuthorityGeneric, foldSessionClaimLanded, nil),
	"work.transitioned":                       registerScopedEventKind[workTransitionPayload](1, EventAppendAuthorityGeneric, foldWorkTransitioned, nil),
	"work.superseded":                         registerEventKind[workSupersededPayload](1, EventAppendAuthorityGeneric, foldWorkSuperseded, nil),
	"work.reopened":                           registerEventKind[workReopenedPayload](1, EventAppendAuthorityGeneric, foldWorkReopened, nil),
	"work.reopened_from_superseded":           registerEventKind[workReopenedFromSupersededPayload](1, EventAppendAuthorityGeneric, foldWorkReopenedFromSuperseded, nil),
	"relation.added":                          registerEventKind[relationPayload](1, EventAppendAuthorityGeneric, foldRelationAdded, nil),
	"relation.removed":                        registerEventKind[relationPayload](1, EventAppendAuthorityGeneric, foldRelationRemoved, nil),
	"product_project.added":                   registerEventKind[membershipPayload](1, EventAppendAuthorityGeneric, productMembershipEdge.foldAdded, nil),
	"product_project.removed":                 registerEventKind[membershipPayload](1, EventAppendAuthorityGeneric, productMembershipEdge.foldRemoved, nil),
	"product_project.role_changed":            registerEventKind[membershipPayload](1, EventAppendAuthorityGeneric, productMembershipEdge.foldRoleChanged, nil),
	"product.knowledge_home_designated":       registerEventKind[knowledgeHomePayload](1, EventAppendAuthorityGeneric, foldProductKnowledgeHomeDesignated, nil),
	"product.knowledge_home_cleared":          registerEventKind[knowledgeHomePayload](1, EventAppendAuthorityGeneric, foldProductKnowledgeHomeCleared, nil),
	"work_project.added":                      registerEventKind[membershipPayload](1, EventAppendAuthorityGeneric, workMembershipEdge.foldAdded, nil),
	"work_project.removed":                    registerEventKind[membershipPayload](1, EventAppendAuthorityGeneric, workMembershipEdge.foldRemoved, nil),
	"work_project.role_changed":               registerEventKind[membershipPayload](1, EventAppendAuthorityGeneric, workMembershipEdge.foldRoleChanged, nil),
	"compaction_link.published":               registerEventKind[compactionLinkPayload](3, EventAppendAuthorityGeneric, foldCompactionLinkPublished, nil),
	"domain.observation_recorded":             registerEventKind[domainObservationRecordedPayload](1, EventAppendAuthorityGeneric, foldDomainObservationRecorded, nil),
	"domain.observation_dismissed":            registerEventKind[domainObservationDismissedPayload](1, EventAppendAuthorityGeneric, foldDomainObservationDismissed, nil),
	WorkflowDefinitionSelected:                workflowScopedRegistration[workflowDefinitionSelectedPayload](1, foldWorkflowDefinitionSelected),
	WorkflowContractApproved:                  workflowScopedRegistration[workflowContractApprovedPayload](6, foldWorkflowContractApproved),
	WorkflowOverlapResolved:                   workflowRegistration[workflowOverlapResolvedPayload](2, foldWorkflowOverlapResolved),
	WorkflowNativeRunRecorded:                 registerEventKind[nativeRunPayload](2, EventAppendAuthorityGeneric, foldNativeRunRecorded, validateNativeRunPayload),
	WorkflowContractSuperseded:                workflowScopedRegistration[workflowContractSupersededPayload](5, foldWorkflowContractSuperseded),
	WorkflowCandidateSetRevised:               workflowRegistration[workflowCandidateSetRevisedPayload](1, foldWorkflowCandidateSetRevised),
	WorkflowActorRecorded:                     workflowRegistration[workflowActorRecordedPayload](1, foldWorkflowActorRecorded),
	WorkflowActionStarted:                     workflowScopedRegistration[workflowActionStartedPayload](1, foldWorkflowActionStarted),
	WorkflowActionCheckpointed:                workflowScopedRegistration[workflowActionCheckpointedPayload](1, foldWorkflowActionCheckpointed),
	WorkflowActionCompleted:                   workflowScopedRegistration[workflowActionCompletedPayload](8, foldWorkflowActionCompleted),
	WorkflowActionFailed:                      workflowScopedRegistration[workflowActionFailedPayload](1, foldWorkflowActionFailed),
	WorkflowEvidenceBound:                     workflowRegistration[workflowEvidenceBoundPayload](1, foldWorkflowEvidenceBound),
	WorkflowVerdictRecorded:                   workflowRegistration[workflowVerdictRecordedPayload](2, foldWorkflowVerdictRecorded),
	WorkflowPremiseConfirmed:                  workflowScopedRegistration[workflowPremiseConfirmedPayload](2, foldWorkflowPremiseConfirmed),
	WorkflowSuccessorLinked:                   workflowRegistration[workflowSuccessorLinkedPayload](3, foldWorkflowSuccessorLinked),
	WorkflowContextCheckpointed:               workflowRegistration[workflowContextCheckpointedPayload](1, foldWorkflowContextCheckpointed),
	WorkflowContextBoundaryCrossed:            workflowRegistration[workflowContextBoundaryCrossedPayload](1, foldWorkflowContextBoundaryCrossed),
	WorkflowWorkContextRecorded:               workflowRegistration[workflowWorkContextRecordedPayload](1, foldWorkflowWorkContextRecorded),
	WorkflowProposalRecorded:                  workflowRegistration[workflowProposalRecordedPayload](1, foldWorkflowProposalRecorded),
	WorkflowDesignRecorded:                    workflowRegistration[workflowDesignRecordedPayload](1, foldWorkflowDesignRecorded),
	WorkflowBacklogAlignmentRecorded:          workflowRegistration[workflowBacklogAlignmentRecordedPayload](1, foldWorkflowBacklogAlignmentRecorded),
	WorkflowDiagnosisRecorded:                 workflowRegistration[workflowDiagnosisRecordedPayload](1, foldWorkflowDiagnosisRecorded),
	WorkflowDeliveryCorrected:                 workflowRegistration[workflowDeliveryCorrectedPayload](2, foldWorkflowDeliveryCorrected),
	WorkflowCompleted:                         workflowScopedRegistration[workflowCompletedPayload](2, foldWorkflowCompleted),
	WorkflowOutsideRepairDispositionSet:       workflowRegistration[workflowOutsideRepairDispositionPayload](1, foldOutsideRepairDispositionSet),
	WorkflowOutsideRepairReconciled:           registerScopedEventKind[workflowOutsideRepairReconcilePayload](1, EventAppendAuthorityWorkflow, foldOutsideRepairReconciled, validateWorkflowPayload[workflowOutsideRepairReconcilePayload]),
	WorkflowOutsideRepairResumed:              workflowRegistration[workflowOutsideRepairResumePayload](1, foldOutsideRepairResumed),
}

// validateEventKindRegistry checks one registration map. It takes the map as a
// parameter so a test can check a malformed copy without writing the shared
// eventKindRegistry, which parallel tests read while they fold events.
func validateEventKindRegistry(registry map[string]EventKindRegistration) error {
	for kind, registration := range registry {
		if kind == "" {
			return fmt.Errorf("event kind key is empty")
		}
		switch registration.Authority {
		case EventAppendAuthorityGeneric, EventAppendAuthorityWorkflow:
		default:
			return fmt.Errorf("event kind %q has invalid append authority", kind)
		}
		if registration.ValidatePayload == nil || registration.Fold == nil || registration.CurrentVersion < 1 {
			return fmt.Errorf("event kind %q has invalid registration bounds", kind)
		}
	}
	return nil
}

type preparedRegisteredEvent struct {
	registration EventKindRegistration
	stage        FailureStage
}

func prepareRegisteredEvent(event Event) (preparedRegisteredEvent, error) {
	switch event.Kind {
	case WorkResized, WorkerJobSplit, WorkerJobSplitDischarged,
		"product.planning_mode_set", "initiative_entry.added", "initiative_entry.removed",
		"initiative_entry.reordered", "initiative_entry.requiredness_changed", "initiative.narrative_revised":
		return preparedRegisteredEvent{stage: StageDecode}, retiredEventAppendRefused(event.Kind)
	case retiredManagedResourceCreated, retiredManagedResourceConsumerAdded,
		retiredManagedResourceUpdated, retiredDomainResourceAttachments:
		return preparedRegisteredEvent{stage: StageDecode}, catalogRetiredEventAppendRefused(event.Kind)
	}
	registration, ok := registeredEventKind(event.Kind)
	if !ok {
		return preparedRegisteredEvent{stage: StageFold}, unknownEventKind(event.Kind)
	}
	if event.PayloadVersion != registration.CurrentVersion {
		return preparedRegisteredEvent{stage: StageDecode}, unsupportedEventVersion(event, registration)
	}
	if err := registration.ValidatePayload(event); err != nil {
		return preparedRegisteredEvent{stage: StageDecode}, err
	}
	return preparedRegisteredEvent{registration: registration}, nil
}

func validateRegisteredEvent(event Event) error {
	_, err := prepareRegisteredEvent(event)
	return err
}

func applyEffect(ctx context.Context, tx *sql.Tx, event Event, scopes ...*writeScope) error {
	prepared, err := prepareRegisteredEvent(event)
	if err != nil {
		return attributeFailure(err, event, prepared.stage)
	}
	var scope *writeScope
	if len(scopes) == 1 {
		scope = scopes[0]
	}
	if err := prepared.registration.Fold(ctx, tx, event, scope); err != nil {
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

// CurrentEventPayloadVersion returns the registry-owned current payload version.
// It panics if kind is not registered.
func CurrentEventPayloadVersion(kind string) int {
	registration, ok := registeredEventKind(kind)
	if !ok {
		panic(fmt.Sprintf("event %q is not registered", kind))
	}
	return registration.CurrentVersion
}

func unsupportedEventVersion(event Event, registration EventKindRegistration) *Failure {
	return newFailure(KindUnsupportedPayloadVersion, "fold_event",
		fmt.Sprintf("event %s uses payload version %d; current version is %d", event.EventID, event.PayloadVersion, registration.CurrentVersion), false,
		"emit the current payload version")
}

// ApplyOperation records and applies one operation in one transaction.
func ApplyOperation(ctx context.Context, s *Store, operation Operation) error {
	_, err := ApplyOperationWithResult(ctx, s, operation)
	return err
}

// ApplyOperationWithResult records and applies one operation in one transaction,
// returning the bounded impact of any Product↔Project membership change.
func ApplyOperationWithResult(ctx context.Context, s *Store, operation Operation) (ApplyOperationResult, error) {
	return applyOperationWithResult(ctx, s, operation, nil)
}

// ApplyOperationTx applies one domain operation within the opaque store-owned
// transaction supplied by Transact. It is the mutation seam used when
// authorization, approval consumption, idempotency, and the domain effect must
// share one transaction. The transaction's write scope is reused when a write
// region already owns it, so nesting stays depth-counted on one scope.
func ApplyOperationTx(ctx context.Context, transaction *Transaction, operation Operation) (ApplyOperationResult, error) {
	tx, err := transactionSQL(transaction, "apply_operation")
	if err != nil {
		return ApplyOperationResult{}, err
	}
	scope := transaction.write
	if scope == nil {
		scope = newWriteScope(tx)
	}
	return applyOperationTx(ctx, tx, operation, scope, false)
}

// applyWorkflowOperationTx is the private append route used by the workflow
// dispatcher and initialization path. Generic callers cannot acquire this
// authority through ApplyOperation or ApplyOperationTx. The caller passes the
// write scope its region owns; enter and close stay paired inside.
func applyWorkflowOperationTx(ctx context.Context, tx *sql.Tx, operation Operation, scope *writeScope) (ApplyOperationResult, error) {
	return applyOperationTx(ctx, tx, operation, scope, true)
}

func applyOperationTx(ctx context.Context, tx *sql.Tx, operation Operation, scope *writeScope, workflowAuthority bool) (ApplyOperationResult, error) {
	var output ApplyOperationResult
	if tx == nil {
		return output, newFailure(KindUnavailable, "apply_operation", "transaction is not open", false, "open a mutation transaction")
	}
	if scope == nil {
		return output, newFailure(KindInvalidOperation, "apply_operation", "write scope is required", false, "open the write scope with beginWrite")
	}
	if len(operation.Events) == 0 {
		return output, newFailure(KindInvalidOperation, "apply_operation", "operation has no events", false, "supply at least one accepted event")
	}
	startReceipt := len(scope.eventIDs)
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
	if err := scope.enter(ctx); err != nil {
		return output, err
	}
	checked := make(map[SubjectRef]bool, len(operation.ExpectedVersions))
	for _, event := range operation.Events {
		if err := applyOperationEventTx(ctx, tx, event, operation.ExpectedVersions, checked, scope, workflowAuthority); err != nil {
			return output, err
		}
	}
	output.EventIDs = append([]string{}, scope.eventIDs[startReceipt:]...)
	var err error
	output.Impact, err = membershipImpact(ctx, tx, operation)
	if err != nil {
		return output, err
	}
	if err := validateMembershipInvariantsTx(ctx, tx); err != nil {
		return output, err
	}
	if err := scope.close(ctx); err != nil {
		return output, err
	}
	return output, nil
}

func applyOperationEventTx(ctx context.Context, tx *sql.Tx, event Event, expectedVersions map[SubjectRef]int64, checked map[SubjectRef]bool, scope *writeScope, workflowAuthority bool) error {
	if err := event.validate(); err != nil {
		return err
	}
	if event.SubjectType == SubjectWorkItem && event.Kind != WorkRemoved {
		if err := refuseRemovedWorkTx(ctx, tx, event.SubjectID, "apply_operation"); err != nil {
			return err
		}
		if err := refusePublishedWorkTx(ctx, tx, event.SubjectID, "apply_operation"); err != nil {
			return err
		}
	}
	// Validate the current payload before recording any effect.
	if err := validateRegisteredEvent(event); err != nil {
		return attributeFailure(err, event, StageDecode)
	}
	if err := checkOperationEventVersionTx(ctx, tx, event, expectedVersions, checked); err != nil {
		return err
	}
	var seq Sequence
	var err error
	if workflowAuthority {
		seq, err = appendEvent(ctx, tx, event, true)
	} else {
		seq, err = AppendEvent(ctx, tx, event)
	}
	if err != nil {
		return err
	}
	event.Seq = seq
	scope.eventIDs = append(scope.eventIDs, event.EventID)
	if err := applyEffect(ctx, tx, event, scope); err != nil {
		return err
	}
	return advanceWorkEffectSeqTx(ctx, tx, event)
}

func checkOperationEventVersionTx(ctx context.Context, tx *sql.Tx, event Event, expectedVersions map[SubjectRef]int64, checked map[SubjectRef]bool) error {
	ref := VersionRef(event.SubjectType, event.SubjectID)
	if expected, hasExpected := expectedVersions[ref]; hasExpected && !checked[ref] {
		got, exists, err := projectionVersion(ctx, tx, event.SubjectType, event.SubjectID)
		if err != nil {
			return err
		}
		if expected > 0 && !exists {
			return absentSubject(event.SubjectType, event.SubjectID)
		}
		if (expected == 0 && exists) || got != expected {
			actions, actionErr := interveningWorkflowActions(ctx, tx, event.SubjectType, event.SubjectID, expected, got)
			if actionErr != nil {
				return actionErr
			}
			return versionConflictWithActions(event.SubjectType, event.SubjectID, expected, got, exists, actions)
		}
		checked[ref] = true
	}
	return nil
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

	tx, err := beginObservedTx(ctx, s, false, observer)
	if err != nil {
		return output, wrapFailure(KindUnavailable, "apply_operation", "cannot begin domain operation", true,
			"retry once the database is writable", err)
	}
	rollback := func(cause error) error {
		_ = tx.Rollback()
		return cause
	}
	scope, err := beginWrite(ctx, tx.Tx)
	if err != nil {
		return output, rollback(err)
	}
	startReceipt := len(scope.eventIDs)

	for _, event := range operation.Events {
		if isWorkflowAdvancementEvent(event.Kind) {
			return output, rollback(workflowDispatcherRequired(event.Kind))
		}
	}
	checked := make(map[SubjectRef]bool, len(operation.ExpectedVersions))
	for _, event := range operation.Events {
		if err := applyOperationEventTx(ctx, tx.Tx, event, operation.ExpectedVersions, checked, scope, false); err != nil {
			return output, rollback(err)
		}
	}
	output.EventIDs = append([]string{}, scope.eventIDs[startReceipt:]...)
	if err := validateMembershipInvariantsTx(ctx, tx.Tx); err != nil {
		return output, rollback(err)
	}
	output.Impact, err = membershipImpact(ctx, tx.Tx, operation)
	if err != nil {
		return output, rollback(err)
	}
	if err := scope.close(ctx); err != nil {
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

func enterWrite(ctx context.Context, tx *sql.Tx) error {
	return mechanismFailure(sqlitedb.EnterWrite(ctx, tx))
}

func leaveWrite(ctx context.Context, tx *sql.Tx) error {
	return mechanismFailure(sqlitedb.LeaveWrite(ctx, tx))
}

func unknownEventKind(kind string) *Failure {
	return newFailure(KindUnknownEventKind, "fold_event", "no projection mutation is registered for "+kind,
		false, "install a binary that recognizes the event or restore a verified snapshot")
}

// absentSubject refuses an expected version named against a subject that has
// no current row. No version can satisfy it, so this is not an optimistic
// concurrency conflict: a version_conflict here would carry no current version
// for the caller to retry against, and the envelope refuses that pairing.
func absentSubject(subjectType SubjectType, subjectID string) *Failure {
	return newFailure(KindProjectionNotFound, "apply_operation",
		fmt.Sprintf("%s %s has no current row, so no expected version can match", subjectType, subjectID), false,
		"create the subject, or retry without an expected version")
}

func versionConflictForQuery(ctx context.Context, q queryer, subjectID string, expected, got int64) (*Failure, error) {
	actions, err := interveningWorkflowActions(ctx, q, SubjectWorkItem, subjectID, expected, got)
	if err != nil {
		return nil, err
	}
	return versionConflictWithActions(SubjectWorkItem, subjectID, expected, got, true, actions), nil
}

func versionConflictWithActions(subjectType SubjectType, subjectID string, expected, got int64, exists bool, actions []InterveningAction) *Failure {
	// A version conflict promises the caller a live version to re-read, and
	// every layer above depends on that promise: the agent envelope refuses a
	// version_conflict that names no current version. An absent subject has no
	// live version to name, so reporting one as a version conflict produces a
	// refusal that cannot be expressed, and the caller receives a transport
	// fault in place of the decision the core reached.
	if !exists {
		return newFailure(KindProjectionNotFound, "apply_operation",
			fmt.Sprintf("%s %s does not exist, so its version cannot be compared with %d", subjectType, subjectID, expected), false,
			"create the subject before pinning its version")
	}
	f := newFailure(KindVersionConflict, "apply_operation",
		fmt.Sprintf("%s %s has version %d, want %d", subjectType, subjectID, got, expected), false,
		"reload the subject and retry with its current version")
	// Carry the typed current version so higher layers can surface
	// error.current_version structurally instead of regexing the detail string.
	f.CurrentVersions = []SubjectCurrentVersion{{SubjectType: subjectType, SubjectID: subjectID, Version: got}}
	f.InterveningActions = actions
	return f
}

// interveningWorkflowActions reads the distinct workflow actions that advanced
// a work item after the caller's pin. It reads both action phases because a
// concurrent action can be started before it completes.
func interveningWorkflowActions(ctx context.Context, q queryer, subjectType SubjectType, subjectID string, expected, got int64) ([]InterveningAction, error) {
	actions := []InterveningAction{}
	if subjectType != SubjectWorkItem || got <= expected {
		return actions, nil
	}
	var floor int64
	if err := q.QueryRowContext(ctx, `SELECT version_actions_floor FROM work_items WHERE id=?`, subjectID).Scan(&floor); err != nil && err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "apply_operation", "cannot read the conflict retention floor", true, "retry once the database is readable", err)
	}
	if expected < floor {
		return nil, newFailure(KindLimitExceeded, "apply_operation", "intervening workflow actions precede the conflict retention floor", false, "reread the work item before retrying")
	}
	rows, err := q.QueryContext(ctx, `
		SELECT action_id, session_ref FROM work_version_actions
		WHERE work_id=? AND resulting_version>? AND resulting_version<=?
		GROUP BY action_id, session_ref ORDER BY MIN(effect_seq) LIMIT 21`, subjectID, expected, got)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "apply_operation", "cannot read intervening workflow actions", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var action InterveningAction
		if err := rows.Scan(&action.ActionID, &action.SessionRef); err != nil {
			return nil, wrapFailure(KindUnavailable, "apply_operation", "cannot scan intervening workflow action", true, "retry once the database is readable", err)
		}
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "apply_operation", "cannot enumerate intervening workflow actions", true, "retry once the database is readable", err)
	}
	if len(actions) > 20 {
		return nil, newFailure(KindLimitExceeded, "apply_operation", "intervening workflow actions exceed the conflict bound", false, "reread the work item before retrying")
	}
	return actions, nil
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
	Reason                  string `json:"reason,omitempty"`
	ExpectedVersion         int64  `json:"expected_version,omitempty"`
	ResultingVersion        int64  `json:"resulting_version,omitempty"`
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
			"repair the event payload before retrying the operation", err)
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
			"restore the current rows from a verified snapshot")
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

// maxDisplayNameLength bounds a Product or Project display name. It matches the
// agent contract's `short` bound, so a stored name is always representable by
// the read surface. SQLite counts characters in length(), and this counts
// runes, so the trigger in migration 50 and this check agree on multi-byte
// names.
const maxDisplayNameLength = 256

func validDisplayName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= maxDisplayNameLength
}

func foldProductCreated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload productCreatedPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if !validDisplayName(payload.DisplayName) || !validateProductStage(payload.StageMaturity, payload.StageAudienceCommitment) {
		return newFailure(KindInvalidPayload, "fold_event", "product.created payload has invalid fields", false,
			"supply a display name of 1 to 256 characters and accepted Product stage values")
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO products
			(id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, event.SubjectID, payload.DisplayName,
		payload.StageMaturity, payload.StageAudienceCommitment, now, now)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
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
	if !validDisplayName(payload.DisplayName) {
		return newFailure(KindInvalidPayload, "fold_event", "product.renamed payload has an invalid display name", false,
			"supply a display name of 1 to 256 characters")
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
	result, err := tx.ExecContext(ctx, "UPDATE products SET "+fields+`, version = version + 1, updated_at = ? WHERE id = ?`, args...) //nolint:gosec // callers choose one of two fixed field clauses and all values stay parameter-bound.
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
	if !validDisplayName(payload.DisplayName) {
		return newFailure(KindInvalidPayload, "fold_event", "project.created payload has an invalid display name", false,
			"supply a display name of 1 to 256 characters")
	}
	overrideMaturity, overrideAudience, err := decodeProjectStagePair(payload.StageMaturityOverride, payload.StageAudienceCommitmentOverride, true)
	if err != nil {
		return err
	}
	now := event.OccurredAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
		INSERT INTO projects (id, display_name, stage_maturity_override, stage_audience_commitment_override, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, event.SubjectID, payload.DisplayName, valueOrNil(overrideMaturity), valueOrNil(overrideAudience), now, now)
	if err != nil {
		if sqlitedb.IsUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "Project already exists", false,
				"append a rename event for the existing Project")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot create Project projection", true,
			"retry once the database is writable", err)
	}
	return nil
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
		WHERE id = ?`, valueOrNil(overrideMaturity), valueOrNil(overrideAudience), now, event.SubjectID)
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
	if !validDisplayName(payload.DisplayName) {
		return newFailure(KindInvalidPayload, "fold_event", "project.renamed payload has an invalid display name", false,
			"supply a display name of 1 to 256 characters")
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
