package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
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

// CompleteWorkflowTxWithRegistry is the caller-owned transaction seam.  No
// completion or notice is appended until every earlier clause has passed. The
// caller passes the fold scope its region owns; the completion enters and
// releases one depth level on it, so a boundary-owned guard survives until the
// boundary's own level closes.
func CompleteWorkflowTxWithRegistry(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, event Event, scope *foldScope) error {
	if tx == nil {
		return newFailure(KindUnavailable, "complete_workflow", "transaction is not open", false, "open a mutation transaction")
	}
	if event.Kind != WorkflowCompleted {
		return newFailure(KindInvalidOperation, "complete_workflow", "completion entry point requires workflow.completed", false, "supply the typed completion event")
	}
	if err := event.validate(); err != nil {
		return err
	}
	registration, _ := registeredEventKind(WorkflowCompleted)
	if event.PayloadVersion != registration.CurrentVersion {
		return unsupportedEventVersion(event, registration)
	}
	var payload workflowCompletedPayload
	if err := decodeWorkflowPayload(event, &payload); err != nil {
		return err
	}
	if payload.ImpactVerdict != "breaking" && payload.ImpactVerdict != "non-breaking" {
		return newFailure(KindInvalidPayload, "complete_workflow", "completion requires impact_verdict breaking or non-breaking", false, "supply the delivered change impact verdict")
	}
	// CD-0183 D3: the gate below admits only ok verdicts, and the completion
	// closes the work item lifecycle to its terminal state. Cancellation keeps
	// the lifecycle route and supersession stays atomic with its relation, so
	// the completion names completed or refuses before any effect.
	if payload.TerminalState != "completed" {
		return newFailure(KindInvalidPayload, "complete_workflow", "workflow completion closes only as completed", false, "cancel through the lifecycle route or resolve an overlap with resolution_kind supersedes")
	}
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if active, err := outsideRepairActiveTx(ctx, tx, event.SubjectID); err != nil {
		return err
	} else if active {
		return newOutsideRepairRouteFailure("complete_workflow", "workflow completion refused: outside-repair disposition is active")
	}
	if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	if scope == nil {
		return newFailure(KindInvalidOperation, "complete_workflow", "fold scope is required", false, "open the fold scope with beginFold")
	}
	// The terminal fold and the lifecycle close resolve the same verified pin
	// through the transaction's scope: bind the caller's registry before any
	// write, and refuse a scope from another transaction or one already bound
	// to a different registry.
	if err := scope.bindRegistry(tx, registry); err != nil {
		return err
	}
	// The completion holds one depth level on the caller's scope for the whole
	// gate: clause folds write fold-only projections, and a nested level must
	// not drop the boundary's protection early.
	if err := scope.enter(ctx); err != nil {
		return err
	}
	defer func() { _ = scope.close(ctx) }()

	// The expected version is checked before clause 1.  This is the lock/fence
	// boundary described by the contract; every subsequent mutation uses the
	// same transaction and therefore sees one consistent projection.
	if err := workflowCompletionVersion(ctx, tx, event.SubjectID, payload.ExpectedVersion); err != nil {
		return err
	}
	contract, definition, err := workflowCompletionContract(ctx, tx, registry, event.SubjectID)
	if err != nil {
		return err
	}
	if err := requireActor(ctx, tx, event.Actor); err != nil {
		return err
	}
	if err := workflowCompletionCallerAuthority(ctx, tx, event.SubjectID, event.Actor); err != nil {
		return err
	}
	if workflowDefinitionProducesDelivery(definition) {
		review, err := loadWorkflowReviewState(ctx, tx, event.SubjectID, 0)
		if err != nil {
			return err
		}
		if err := workflowAdmitReview(review, ""); err != nil {
			return err
		}
	}

	// The complete-step correction's supersession bounds the evidence history
	// the completion clauses read: the successor accepts only bindings
	// recorded after the supersession that approved it.
	evidenceCutoff, err := workflowCompleteStepCorrectionEvidenceCutoff(ctx, tx, event.SubjectID, contract.Version)
	if err != nil {
		return err
	}

	// Clause 1: durable, event-folded evidence bound to the approved contract.
	if err := verifyCompletionEvidence(ctx, tx, event.SubjectID, contract.RequiredEvidence, definition.RequiredEvidenceKinds, evidenceCutoff); err != nil {
		return workflowClauseError(err, 1)
	}

	// Clause 2: every declared condition is terminal.  Resolution authority and
	// evidence are validated by the condition folds; completion only consumes
	// that authoritative projection.
	var openConditions int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_external_conditions WHERE work_id=? AND condition_state='open'`, event.SubjectID).Scan(&openConditions); err != nil {
		return workflowClauseError(wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect workflow conditions", true, "retry once the database is readable", err), 2)
	}
	if openConditions != 0 {
		return workflowClauseFailure(KindNotTerminal, 2, "workflow has unresolved external conditions", "reread_entities")
	}

	// Clause 3: modifying edges must be covered by the approved candidate scope
	// and every declared mandate must have durable evidence.  There is no
	// inferred or response-wording authority here.
	if err := verifyCompletionScopeAndMandates(ctx, tx, event.SubjectID, contract, evidenceCutoff); err != nil {
		return workflowClauseError(err, 3)
	}
	// Completion never treats an amendment declaration as permission to leave a
	// conflict unresolved. The Git-derived projection must actually be clear.
	if contract.LawBoundaryVersion == 1 {
		mandated, mandateErr := currentWorkflowLawMandate(contract.SpecMandate, contract.ArchitectureBinding)
		if mandateErr != nil {
			return workflowClauseError(mandateErr, 3)
		}
		if err := checkMandatedLawsTx(ctx, tx, event.SubjectID, mandated, contract.LawModifies, false); err != nil {
			return workflowClauseError(err, 3)
		}
		if err := checkWorkflowLawChangeEnactmentTx(ctx, tx, event.SubjectID, contract.LawChange, contract.ArchitectureBinding); err != nil {
			return workflowClauseError(err, 3)
		}
	}

	// Clause 4: every approved predicate has one latest verdict at this contract
	// version, with the complete pinned actor tuple and durable evidence.
	verdicts, err := latestWorkflowVerdicts(ctx, tx, event.SubjectID, contract.Version)
	if err != nil {
		return err
	}
	verdictByPredicate := make(map[string]*workflowVerdictRecordedPayload, len(verdicts))
	for i := range verdicts {
		verdictByPredicate[verdicts[i].PredicateID] = &verdicts[i]
	}
	for _, predicate := range contract.Predicates {
		verdict := verdictByPredicate[predicate.PredicateID]
		if verdict == nil {
			return workflowClauseFailure(KindMissingEvidence, 4, "workflow verdict is missing for approved predicate "+predicate.PredicateID, "provide_evidence")
		}
		if err := workflowCompletionActorDistinct(ctx, tx, event.SubjectID, verdict.VerdictActorRef); err != nil {
			return workflowClauseError(err, 4)
		}
		if err := verifyWorkflowCriterionAssociation(ctx, tx, event.SubjectID, *verdict); err != nil {
			return workflowClauseError(err, 4)
		}
	}
	for _, verdict := range verdicts {
		if !containsPredicateID(contract.Predicates, verdict.PredicateID) {
			// CD-0006 R1: delivery cannot answer an unapproved contract.
			return workflowClauseFailure(KindOutcomeMismatch, 6, "delivered predicate "+verdict.PredicateID+" is not approved by the contract", "contact_operator")
		}
	}

	// Clause 5: the premise confirmation carries the authority the contract's
	// declared decision fact demands — a verified operator for decision-bearing
	// work, and under consequence_v1 an authenticated non-worker coordinator
	// for engineering. Legacy contracts keep the operator requirement.
	var confirmedBy string
	if err := tx.QueryRowContext(ctx, `SELECT confirmed_by FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=?`, event.SubjectID, contract.Version).Scan(&confirmedBy); err != nil {
		if err == sql.ErrNoRows {
			if definition.ApprovalPolicy == WorkflowApprovalPolicyConsequenceV1 && workflowPremiseContractIsEngineering(contract) {
				return workflowClauseFailure(KindMissingEvidence, 5, "workflow premise is not confirmed", "record the coordinator confirmation")
			}
			return workflowClauseFailure(KindApprovalRequired, 5, "workflow premise is not confirmed", "request_approval")
		}
		return workflowClauseError(wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect premise confirmation", true, "retry once the database is readable", err), 5)
	}
	if err := workflowPremiseConfirmationAuthority(ctx, tx, definition, contract, event.SubjectID, confirmedBy); err != nil {
		return workflowClauseError(err, 5)
	}

	if err := verifyBlockingStaleness(ctx, tx, event.SubjectID, definition, contract.RequiredEvidence); err != nil {
		return workflowClauseError(err, 6)
	}
	warnings, err := workflowStalenessWarnings(ctx, tx, event.SubjectID, definition, contract.RequiredEvidence)
	if err != nil {
		return workflowClauseError(err, 6)
	}

	// Clause 7: derive and append every required notice and completion event in
	// this transaction.  Notice conflicts are returned before the completion
	// append, so the caller's rollback removes all prior writes.
	notices, err := completionNoticeEvents(ctx, tx, event.SubjectID, contract.Version, payload.ImpactVerdict, event)
	if err != nil {
		return workflowClauseError(err, 7)
	}
	nextVersion := *payload.ExpectedVersion
	for _, notice := range notices {
		nextVersion++
		var noticePayload map[string]any
		if err := json.Unmarshal(notice.Payload, &noticePayload); err != nil {
			return workflowClauseFailure(KindOperationConflict, 7, "impact notice payload is malformed", "reconcile_operation")
		}
		noticePayload["expected_version"] = nextVersion - 1
		noticePayload["resulting_version"] = nextVersion
		notice.Payload, _ = json.Marshal(noticePayload)
		if _, err := appendEvent(ctx, tx, notice, true); err != nil {
			return workflowClauseFailure(KindOperationConflict, 7, "impact notice append conflicted", "reconcile_operation")
		}
		notice.Seq = 0
		if err := foldRegisteredEvent(ctx, tx, notice, scope); err != nil {
			return workflowClauseError(err, 7)
		}
	}

	completion := event
	var completionPayload map[string]any
	if err := json.Unmarshal(completion.Payload, &completionPayload); err != nil {
		return workflowClauseFailure(KindInvalidPayload, 7, "completion metadata is malformed", "supply closed terminal metadata")
	}
	completionPayload["expected_version"] = nextVersion
	completionPayload["resulting_version"] = nextVersion + 1
	if len(warnings) != 0 {
		completionPayload["warnings"] = warnings
	}
	completion.Payload, _ = json.Marshal(completionPayload)
	if _, err := appendEvent(ctx, tx, completion, true); err != nil {
		return workflowClauseFailure(KindOperationConflict, 7, "completion append conflicted", "reconcile_operation")
	}
	completion.Seq = 0
	if err := foldRegisteredEvent(ctx, tx, completion, scope); err != nil {
		return workflowClauseError(err, 7)
	}
	// CD-0183 D3: the workflow's terminal state closes the work item
	// lifecycle in this same transaction, so the lifecycle follows the
	// workflow instead of stranding in_progress after a recorded completion. The completion fold has already marked the
	// instance terminal, so the terminal-lifecycle close keeps that record:
	// workflow.completed remains the only fold that may mark an instance
	// completed. Replay folds the logged transition exactly as it folded
	// here, because the event is part of the log.
	if err := appendWorkflowTerminalLifecycleTx(ctx, tx, scope, completion, payload.TerminalState); err != nil {
		return workflowClauseError(err, 7)
	}
	return nil
}

// appendWorkflowTerminalLifecycleTx appends and folds the work.transitioned
// event that moves the work item to the workflow's terminal state. The
// completion event must already be folded: the transition's fold keeps the
// instance record only because the instance is terminal when it runs.
func appendWorkflowTerminalLifecycleTx(ctx context.Context, tx *sql.Tx, scope *foldScope, completion Event, terminalState string) error {
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
	if _, err := appendEvent(ctx, tx, transition, true); err != nil {
		return err
	}
	transition.Seq = 0
	return foldRegisteredEvent(ctx, tx, transition, scope)
}

type workflowCompletionContractData struct {
	Version                 int64
	Predicates              []WorkflowReadPredicate
	RequiredEvidence        []string
	SpecMandate             []string
	LawModifies             []string
	LawBoundaryVersion      int
	DecisionDomains         []string
	DecisionDomainsPresent  bool
	OutcomeKind             string
	OutcomePayload          string
	ArchitectureBinding     *WorkflowArchitectureBinding
	VerificationObligations []WorkflowVerificationObligation
	LawChange               *WorkflowLawChange
}

// workflowPremiseConfirmationAuthority holds one stored confirmation to the
// authority its contract's declared decision fact demands: a verified operator
// for decision-bearing work, and under consequence_v1 an authenticated
// non-worker coordinator for engineering. Legacy contracts keep the operator
// requirement, and a missing new-policy fact fails closed.
func workflowPremiseConfirmationAuthority(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, contract workflowCompletionContractData, workID, confirmedBy string) error {
	if definition.ApprovalPolicy != WorkflowApprovalPolicyConsequenceV1 {
		if err := requireActorClass(ctx, tx, confirmedBy, "operator"); err != nil {
			return newFailure(KindApprovalRequired, "complete_workflow", "workflow premise confirmation is not operator typed", false, "request_approval")
		}
		return nil
	}
	if !contract.DecisionDomainsPresent {
		return newFailure(KindInvariantViolation, "complete_workflow", "consequence_v1 completion requires the contract's stored decision-domain fact", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	decision, err := ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: contract.DecisionDomains})
	if err != nil {
		return err
	}
	if decision.Consent != WorkflowConsentNone {
		if err := requireActorClass(ctx, tx, confirmedBy, "operator"); err != nil {
			return newFailure(KindApprovalRequired, "complete_workflow", "workflow premise confirmation is not operator typed", false, "request_approval")
		}
		return nil
	}
	if err := requireActorClass(ctx, tx, confirmedBy, "agent"); err != nil {
		return newFailure(KindInvalidOperation, "complete_workflow", "engineering premise confirmation is not coordinator typed", false, "record the coordinator confirmation")
	}
	authority, authorityErr := workflowEvaluationAuthority(ctx, tx, workID, confirmedBy)
	if authorityErr != nil {
		return authorityErr
	}
	if authority == WorkflowWorkerEvaluator {
		return newFailure(KindInvalidOperation, "complete_workflow", "worker actors cannot hold an engineering premise confirmation", false, "record the coordinator confirmation")
	}
	return nil
}

// workflowPremiseContractIsEngineering reports whether the contract's stored
// decision fact classifies the work as engineering under consequence_v1.
func workflowPremiseContractIsEngineering(contract workflowCompletionContractData) bool {
	return contract.DecisionDomainsPresent && len(contract.DecisionDomains) == 0
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

func workflowCompletionContract(ctx context.Context, tx queryer, registry DefinitionRegistry, workID string) (workflowCompletionContractData, WorkflowDefinition, error) {
	result, err := workflowCompletionContractRows(ctx, tx, workID)
	if err != nil {
		return workflowCompletionContractData{}, WorkflowDefinition{}, err
	}
	definition, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, workID)
	if err != nil {
		return workflowCompletionContractData{}, WorkflowDefinition{}, err
	}
	if err := validateWorkflowCompletionContractPredicates(result, definition.Definition); err != nil {
		return workflowCompletionContractData{}, WorkflowDefinition{}, err
	}
	return result, definition.Definition, nil
}

// validateWorkflowCompletionContractPredicates validates the stored contract's
// approved predicates against one already-verified definition: decode, kind
// match, and pinned-definition membership. The completion gate and the
// acceptance deliverables check share it, so no owner re-derives the rule.
func validateWorkflowCompletionContractPredicates(contract workflowCompletionContractData, definition WorkflowDefinition) error {
	for _, item := range contract.Predicates {
		approved, decodeErr := DecodeWorkflowPredicate([]byte(item.OutcomePayload))
		if decodeErr != nil || string(approved.Kind) != item.OutcomeKind {
			return newFailure(KindInvariantViolation, "complete_workflow", "approved outcome predicate is malformed or mismatched", false, "reread_entities")
		}
		if err := ValidateWorkflowPredicateForDefinition(definition, approved); err != nil {
			return newFailure(KindInvariantViolation, "complete_workflow", "approved outcome predicate is outside the pinned definition", false, "reread_entities")
		}
	}
	return nil
}

// workflowCompletionContractRows reads the active contract's stored facts
// without resolving the instance pin. Owners that already hold the verified
// definition reuse this reader so one transaction resolves the pin once.
func workflowCompletionContractRows(ctx context.Context, tx queryer, workID string) (workflowCompletionContractData, error) {
	var result workflowCompletionContractData
	var required, mandates, modifies string
	activeVersion, activeErr := activeWorkflowContractVersion(ctx, tx, workID, "complete_workflow")
	if activeErr == nil {
		result.Version = activeVersion
	}
	if activeErr != nil {
		if activeErr == sql.ErrNoRows {
			return result, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract is missing or ambiguous", false, "reread_entities")
		}
		return result, activeErr
	}
	var decisionDomains, lawChange sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT required_evidence,spec_mandate,law_modifies,law_boundary_version,decision_domains,law_change FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, result.Version).Scan(&required, &mandates, &modifies, &result.LawBoundaryVersion, &decisionDomains, &lawChange); err != nil {
		if err == sql.ErrNoRows {
			return result, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract is missing or ambiguous", false, "reread_entities")
		}
		return result, wrapFailure(KindUnavailable, "complete_workflow", "cannot read approved workflow contract", true, "retry once the database is readable", err)
	}
	if err := json.Unmarshal([]byte(required), &result.RequiredEvidence); err != nil {
		return result, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract arrays are malformed", false, "reread_entities")
	}
	if err := json.Unmarshal([]byte(mandates), &result.SpecMandate); err != nil || json.Unmarshal([]byte(modifies), &result.LawModifies) != nil {
		return result, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract arrays are malformed", false, "reread_entities")
	}
	var predicateErr error
	result.Predicates, predicateErr = readWorkflowContractPredicates(ctx, tx, workID, result.Version)
	if predicateErr != nil || len(result.Predicates) == 0 {
		return result, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract predicates are missing", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	domains, domainsPresent, domainsErr := workflowStoredDecisionDomains(decisionDomains)
	if domainsErr != nil {
		return result, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract decision domains are malformed", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	result.DecisionDomainsPresent, result.DecisionDomains = domainsPresent, domains
	if lawChange.Valid {
		result.LawChange = &WorkflowLawChange{}
		if err := json.Unmarshal([]byte(lawChange.String), result.LawChange); err != nil {
			return result, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract law change is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
	}
	result.OutcomeKind = result.Predicates[0].OutcomeKind
	result.OutcomePayload = result.Predicates[0].OutcomePayload
	var bindingErr error
	result.ArchitectureBinding, bindingErr = readWorkflowArchitectureBinding(ctx, tx, workID, result.Version)
	if bindingErr != nil {
		return result, bindingErr
	}
	obligationRows, err := tx.QueryContext(ctx, `SELECT law_id,obligation_id FROM workflow_contract_verification_obligations WHERE work_id=? AND contract_version=? ORDER BY law_id,obligation_id`, workID, result.Version)
	if err != nil {
		return result, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow verification obligations", true, "retry once the database is readable", err)
	}
	for obligationRows.Next() {
		var obligation WorkflowVerificationObligation
		if err := obligationRows.Scan(&obligation.LawID, &obligation.ObligationID); err != nil {
			obligationRows.Close()
			return result, wrapFailure(KindUnavailable, "complete_workflow", "cannot scan workflow verification obligation", true, "retry once the database is readable", err)
		}
		result.VerificationObligations = append(result.VerificationObligations, obligation)
	}
	if err := obligationRows.Err(); err != nil {
		obligationRows.Close()
		return result, wrapFailure(KindUnavailable, "complete_workflow", "cannot scan workflow verification obligations", true, "retry once the database is readable", err)
	}
	if err := obligationRows.Close(); err != nil {
		return result, wrapFailure(KindUnavailable, "complete_workflow", "cannot close workflow verification obligations", true, "retry once the database is readable", err)
	}
	return result, nil
}

// workflowStoredDecisionDomains decodes the stored contract decision-domain
// column: SQL NULL is the absent fact, a JSON array the present one; each
// enclosing reader keeps its own refusal wording.
func workflowStoredDecisionDomains(stored sql.NullString) ([]string, bool, error) {
	if !stored.Valid {
		return nil, false, nil
	}
	var domains []string
	if err := json.Unmarshal([]byte(stored.String), &domains); err != nil {
		return nil, false, err
	}
	return domains, true, nil
}

func VerifyWorkflowInstanceDefinitionTx(ctx context.Context, tx queryer, registry DefinitionRegistry, workID string) (RegisteredDefinition, error) {
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	var pin WorkflowDefinitionPin
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pin.Ref, &pin.Version, &pin.Digest); err != nil {
		if err == sql.ErrNoRows {
			return RegisteredDefinition{}, newFailure(KindInvariantViolation, "complete_workflow", "workflow definition pin is missing", false, "reread_entities")
		}
		return RegisteredDefinition{}, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow definition pin", true, "retry once the database is readable", err)
	}
	return verifyWorkflowDefinitionPinForFold(registry, pin)
}

// verifyWorkflowDefinitionPinForFold resolves the current family definition;
// recorded instance versions and digests never select another definition.
func verifyWorkflowDefinitionPinForFold(registry DefinitionRegistry, pin WorkflowDefinitionPin) (RegisteredDefinition, error) {
	return resolveCurrentWorkflowDefinition(registry, pin.Ref)
}

// missingPredicateVerdictsForContract lists the unverdicted approved
// predicates from one already-read contract; owners holding the verified
// definition reuse it so one transaction resolves the pin once.
func missingPredicateVerdictsForContract(ctx context.Context, tx queryer, workID string, contract workflowCompletionContractData) ([]string, error) {
	verdicts, err := latestWorkflowVerdicts(ctx, tx, workID, contract.Version)
	if err != nil {
		return nil, err
	}
	verdicted := make(map[string]bool, len(verdicts))
	for _, verdict := range verdicts {
		verdicted[verdict.PredicateID] = true
	}
	var missing []string
	for _, predicate := range contract.Predicates {
		if !verdicted[predicate.PredicateID] {
			missing = append(missing, predicate.PredicateID)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

// workflowEvidenceRequirement represents one completion evidence requirement.
type workflowEvidenceRequirement struct {
	Kind      string
	Reference string
}

func workflowEvidenceRequirementInputs(ctx context.Context, q queryer, workID string) ([]string, []string, []WorkflowVerificationObligation, int64, error) {
	var requiredJSON, mandateJSON string
	contractVersion, activeErr := activeWorkflowContractVersion(ctx, q, workID, "workflow_action")
	if activeErr != nil {
		if activeErr == sql.ErrNoRows {
			return nil, nil, nil, 0, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract is missing", false, "reread_entities")
		}
		return nil, nil, nil, 0, activeErr
	}
	if err := q.QueryRowContext(ctx, `SELECT required_evidence,spec_mandate FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&requiredJSON, &mandateJSON); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, nil, 0, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract is missing", false, "reread_entities")
		}
		return nil, nil, nil, 0, wrapFailure(KindUnavailable, "workflow_action", "cannot read approved workflow evidence requirements", true, "retry once the workflow contract is readable", err)
	}
	var required, mandates []string
	if err := json.Unmarshal([]byte(requiredJSON), &required); err != nil {
		return nil, nil, nil, 0, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract evidence kinds are malformed", false, "reread_entities")
	}
	if err := json.Unmarshal([]byte(mandateJSON), &mandates); err != nil {
		return nil, nil, nil, 0, newFailure(KindInvariantViolation, "workflow_action", "approved workflow contract law mandates are malformed", false, "reread_entities")
	}
	rows, err := q.QueryContext(ctx, `SELECT law_id,obligation_id FROM workflow_contract_verification_obligations WHERE work_id=? AND contract_version=? ORDER BY law_id,obligation_id`, workID, contractVersion)
	if err != nil {
		return nil, nil, nil, 0, wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow verification obligations", true, "retry once the workflow contract is readable", err)
	}
	var obligations []WorkflowVerificationObligation
	for rows.Next() {
		var obligation WorkflowVerificationObligation
		if err := rows.Scan(&obligation.LawID, &obligation.ObligationID); err != nil {
			_ = rows.Close()
			return nil, nil, nil, 0, wrapFailure(KindUnavailable, "workflow_action", "cannot read workflow verification obligation", true, "retry once the workflow contract is readable", err)
		}
		obligations = append(obligations, obligation)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, nil, 0, wrapFailure(KindUnavailable, "workflow_action", "cannot scan workflow verification obligations", true, "retry once the workflow contract is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, 0, wrapFailure(KindUnavailable, "workflow_action", "cannot close workflow verification obligations", true, "retry once the workflow contract is readable", err)
	}
	cutoff, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, q, workID, contractVersion)
	if cutoffErr != nil {
		return nil, nil, nil, 0, cutoffErr
	}
	return required, mandates, obligations, cutoff, nil
}

// outstandingWorkflowEvidenceRequirements returns every declared requirement
// the evidence history does not satisfy. The cutoff bounds that history: a
// successor approved through the complete-step correction route accepts only
// bindings recorded after its supersession, and a cutoff of zero admits the
// whole history.
func outstandingWorkflowEvidenceRequirements(ctx context.Context, q queryer, workID string, required, mandates []string, definition WorkflowDefinition, obligations []WorkflowVerificationObligation, cutoff int64) ([]workflowEvidenceRequirement, error) {
	return unboundWorkflowEvidenceRequirements(ctx, q, workID, workflowEvidenceRequirementDefinitions(required, mandates, definition, obligations), cutoff, 0)
}

// beforeSeq excludes a binding whose completion event is being validated.
// Zero reads the current history; cutoff still excludes disproved evidence.
func unboundWorkflowEvidenceRequirements(ctx context.Context, q queryer, workID string, requirements []workflowEvidenceRequirement, cutoff, beforeSeq int64) ([]workflowEvidenceRequirement, error) {
	outstanding := make([]workflowEvidenceRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		var found int
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=? AND e.kind=? AND e.seq>? AND (?=0 OR e.seq<?) AND (?='' OR json_extract(e.payload,'$.evidence_kind')=?) AND (?='' OR json_extract(e.payload,'$.immutable_subject_ref')=?) AND (?<>'' OR (SELECT count(*) FROM durable_operations d WHERE d.op_id=json_extract(e.payload,'$.producer_run_ref') AND d.work_id=e.subject_id AND d.principal_ref=json_extract(e.payload,'$.producer_id') AND d.request_id=json_extract(e.payload,'$.producer_watermark') AND d.result_kind='completed' AND EXISTS(SELECT 1 FROM json_each(d.evidence_refs) WHERE value=json_extract(e.payload,'$.immutable_subject_ref')))=1))`, workID, WorkflowEvidenceBound, cutoff, beforeSeq, beforeSeq, requirement.Kind, requirement.Kind, requirement.Reference, requirement.Reference, requirement.Reference).Scan(&found); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot inspect workflow evidence requirement", true, "retry once the database is readable", err)
		}
		if found == 0 {
			outstanding = append(outstanding, requirement)
		}
	}
	return outstanding, nil
}

func workflowEvidenceRequirementDefinitions(required, mandates []string, definition WorkflowDefinition, obligations []WorkflowVerificationObligation) []workflowEvidenceRequirement {
	needed := append([]string(nil), required...)
	for _, kind := range definition.RequiredEvidenceKinds {
		if !slices.Contains(needed, string(kind)) {
			needed = append(needed, string(kind))
		}
	}
	var requirements []workflowEvidenceRequirement
	for _, kind := range needed {
		requirements = append(requirements, workflowEvidenceRequirement{Kind: kind})
	}
	for _, mandate := range mandates {
		requirements = append(requirements, workflowEvidenceRequirement{Reference: mandate})
	}
	for _, obligation := range obligations {
		requirements = append(requirements, workflowEvidenceRequirement{Kind: obligation.ObligationID, Reference: obligation.LawID})
	}
	return requirements
}

func workflowEvidenceTupleBound(ctx context.Context, q queryer, workID, kind, reference string, afterSeq int64) (bool, error) {
	var count int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.evidence_kind')=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, afterSeq, kind, reference).Scan(&count); err != nil {
		return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect workflow evidence requirement", true, "retry once the database is readable", err)
	}
	return count != 0, nil
}

// Premise confirmation requires producer proof for every approved criterion
// before leaving the stage where Main can associate receipts.
func requireAcceptanceDeliverables(ctx context.Context, tx queryer, definition WorkflowDefinition, workID string) error {
	verdict, err := latestWorkflowVerdict(ctx, tx, workID)
	if err != nil {
		return err
	}
	if verdict == nil {
		return newFailure(KindMissingEvidence, "workflow_action", "premise confirmation requires a recorded workflow verdict", false, "record_verdict before confirming the premise")
	}
	// The completion clause demands a verdict for every approved predicate,
	// and confirm_premise is the last step where record_verdict is declared.
	// Leaving verify with a predicate unverdicted wedges the item at
	// complete with no declared recovery action (#911), so the confirmation
	// refuses here and names what is missing.
	contract, contractErr := workflowCompletionContractRows(ctx, tx, workID)
	if contractErr != nil {
		return contractErr
	}
	missing, missingErr := missingPredicateVerdictsForContract(ctx, tx, workID, contract)
	if missingErr != nil {
		return missingErr
	}
	if err := validateWorkflowCompletionContractPredicates(contract, definition); err != nil {
		return err
	}
	if len(missing) != 0 {
		return newFailure(KindMissingEvidence, "workflow_action", "premise confirmation requires a verdict for every approved predicate: missing "+strings.Join(missing, ", "), false, "record_verdict for each approved predicate before confirming the premise")
	}
	verdicts, err := latestWorkflowVerdicts(ctx, tx, workID, contract.Version)
	if err != nil {
		return err
	}
	for _, verdict := range verdicts {
		if !containsPredicateID(contract.Predicates, verdict.PredicateID) {
			continue
		}
		if err := verifyWorkflowCriterionAssociation(ctx, tx, workID, verdict); err != nil {
			return err
		}
	}
	cutoff, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, tx, workID, contract.Version)
	if cutoffErr != nil {
		return cutoffErr
	}
	requirements, err := outstandingWorkflowEvidenceRequirements(ctx, tx, workID, contract.RequiredEvidence, contract.SpecMandate, definition, contract.VerificationObligations, cutoff)
	if err != nil {
		return err
	}
	if len(requirements) != 0 {
		var missing strings.Builder
		for i, requirement := range requirements {
			if i != 0 {
				missing.WriteString(", ")
			}
			if requirement.Kind != "" {
				missing.WriteString(requirement.Kind)
			} else {
				missing.WriteString(requirement.Reference)
			}
		}
		return newFailure(KindMissingEvidence, "workflow_action", "premise confirmation requires every contract-required evidence kind bound: missing "+missing.String(), false, "bind_evidence for each required kind before confirming the premise")
	}
	return nil
}

// verifyCompletionEvidence refuses completion while a required evidence kind
// lacks a durable binding inside the accepted history. The cutoff bounds that
// history at the complete-step correction's supersession, so predecessor
// bindings cannot satisfy the successor's required kinds.
func verifyCompletionEvidence(ctx context.Context, tx *sql.Tx, workID string, required []string, definitionRequired []EvidenceKind, cutoff int64) error {
	needed := append([]string(nil), required...)
	for _, kind := range definitionRequired {
		if !slices.Contains(needed, string(kind)) {
			needed = append(needed, string(kind))
		}
	}
	for _, kind := range needed {
		found, err := workflowEvidenceKindBound(ctx, tx, workID, kind, cutoff)
		if err != nil {
			return err
		}
		if !found {
			return newFailure(KindMissingEvidence, "complete_workflow", "required workflow evidence is missing or not durably bound", false, "provide_evidence")
		}
	}
	return nil
}

func verifyCompletionScopeAndMandates(ctx context.Context, tx *sql.Tx, workID string, contract workflowCompletionContractData, cutoff int64) error {
	for _, mandate := range contract.SpecMandate {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, cutoff, mandate).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect spec-mandate evidence", true, "retry once the database is readable", err)
		}
		if count == 0 {
			return newFailure(KindInvariantViolation, "complete_workflow", "declared spec mandate is not satisfied", false, "reread_entities")
		}
	}
	for _, obligation := range contract.VerificationObligations {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>? AND json_extract(payload,'$.evidence_kind')=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, cutoff, obligation.ObligationID, obligation.LawID).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect verification-obligation evidence", true, "retry once the database is readable", err)
		}
		if count == 0 {
			return newFailure(KindInvariantViolation, "complete_workflow", "declared verification obligation is not satisfied", false, "provide_evidence")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT target_work_id FROM workflow_impact_edges WHERE work_id=? AND edge_kind='modifies' ORDER BY edge_id`, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect workflow impact scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var targets []string
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow impact scope", true, "retry once the database is readable", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, target := range targets {
		if target == workID {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_candidate_sets WHERE work_id=? AND candidate_kind='work_item' AND candidate_ref=?`, workID, target).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect declared workflow scope", true, "retry once the database is readable", err)
		}
		if count != 1 {
			return newFailure(KindInvariantViolation, "complete_workflow", "modifying edge target is outside the declared scope", false, "reread_entities")
		}
	}
	return nil
}

// workflowCompletionActorDistinct requires Main to record the verdict.
func workflowCompletionActorDistinct(ctx context.Context, tx queryer, workID, verdictActor string) error {
	return workflowRequireMain(ctx, tx, workID, verdictActor)
}

// workflowActorsDistinctForExecutor rejects a worker accepting its own result
// and requires Main to accept it.
func workflowActorsDistinctForExecutor(ctx context.Context, tx queryer, workID, executorRef, verdictActor string) error {
	if executorRef != "" && executorRef == verdictActor {
		return reviewFailure(KindUnauthorized, "a worker cannot accept its own result", "return the terminal result to Main")
	}
	return workflowRequireMain(ctx, tx, workID, verdictActor)
}

func verifyBlockingStaleness(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, required []string) error {
	observations, err := workflowStalenessObservationState(ctx, tx, workID)
	if err != nil {
		return err
	}
	for _, rule := range definition.StalenessRules {
		if rule.Severity != "block" {
			continue
		}
		if observation, observed := observations[rule.ID]; observed {
			if observation.Drifted {
				return newFailure(KindStaleRequiresReview, "complete_workflow", "blocking staleness rule has drifted", false, "refresh_context")
			}
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, rule.InputRef).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect staleness evidence", true, "retry once the database is readable", err)
		}
		if count == 0 && !slices.Contains(required, rule.InputRef) {
			return newFailure(KindStaleRequiresReview, "complete_workflow", "blocking staleness rule has drifted", false, "refresh_context")
		}
	}
	return nil
}

func workflowStalenessWarnings(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, required []string) ([]string, error) {
	observations, err := workflowStalenessObservationState(ctx, tx, workID)
	if err != nil {
		return nil, err
	}
	var warnings []string
	for _, rule := range definition.StalenessRules {
		if rule.Severity != "warning" {
			continue
		}
		if observation, observed := observations[rule.ID]; observed {
			if observation.Drifted && observation.Severity == "warning" {
				warnings = append(warnings, rule.ID)
			}
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, rule.InputRef).Scan(&count); err != nil {
			return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect warning staleness evidence", true, "retry once the database is readable", err)
		}
		if count == 0 && !slices.Contains(required, rule.InputRef) {
			warnings = append(warnings, rule.ID)
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}

func workflowRecordedStalenessWarnings(ctx context.Context, q queryer, workID string) ([]string, error) {
	observations, err := workflowStalenessObservationState(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	warnings := make([]string, 0, len(observations))
	for ruleID, observation := range observations {
		if observation.Drifted {
			warnings = append(warnings, ruleID)
		}
	}
	sort.Strings(warnings)
	return warnings, nil
}

func workflowStalenessObservationState(ctx context.Context, q queryer, workID string) (map[string]workflowStalenessObservedPayload, error) {
	rows, err := q.QueryContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC`, workID, WorkflowStalenessObserved)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_staleness", "cannot read staleness observation events", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	observations := map[string]workflowStalenessObservedPayload{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_staleness", "cannot scan staleness observation event", true, "retry once the database is readable", err)
		}
		var observation workflowStalenessObservedPayload
		if err := json.Unmarshal([]byte(raw), &observation); err != nil {
			return nil, newFailure(KindInvariantViolation, "workflow_staleness", "staleness observation event is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		if _, exists := observations[observation.RuleID]; !exists {
			observations[observation.RuleID] = observation
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_staleness", "cannot scan staleness observation events", true, "retry once the database is readable", err)
	}
	return observations, nil
}

func completionNoticeEvents(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64, impactVerdict string, completion Event) ([]Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT work_id,edge_id,edge_class FROM workflow_impact_edges WHERE target_work_id=? AND edge_kind='depends_on' AND edge_class IN ('hard','soft') ORDER BY work_id,edge_id`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect workflow impact edges", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	type edge struct{ owner, id, class string }
	var edges []edge
	for rows.Next() {
		var e edge
		if err := rows.Scan(&e.owner, &e.id, &e.class); err != nil {
			return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow impact edge", true, "retry once the database is readable", err)
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	selected := make(map[string]edge, len(edges))
	for _, candidate := range edges {
		current, exists := selected[candidate.owner]
		if !exists || candidate.class == "hard" && current.class != "hard" || candidate.class == current.class && candidate.id < current.id {
			selected[candidate.owner] = candidate
		}
	}
	owners := make([]string, 0, len(selected))
	for owner := range selected {
		owners = append(owners, owner)
	}
	sort.Strings(owners)
	var events []Event
	for _, owner := range owners {
		e := selected[owner]
		noticeID := WorkflowNoticeID(workID, contractVersion, "work_item", workID, e.owner, impactVerdict)
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_impact_notices WHERE notice_id=?`, noticeID).Scan(&existing); err != nil {
			return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect impact notice identity", true, "retry once the database is readable", err)
		}
		if existing != 0 {
			continue
		}
		payload := map[string]any{"work_id": workID, "expected_version": int64(0), "resulting_version": int64(0), "notice_id": noticeID, "source_contract_version": contractVersion, "entity_kind": "work_item", "entity_ref": workID, "target_work_id": e.owner, "edge_owner_work_id": e.owner, "edge_id": e.id, "old_hash": nil, "new_hash": nil, "severity": impactVerdict}
		raw, _ := json.Marshal(payload)
		events = append(events, Event{EventID: "notice-event:" + noticeID, Kind: WorkflowImpactNoticeRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: completion.Actor, OccurredAt: completion.OccurredAt, PayloadVersion: 2, Payload: raw})
	}
	return events, nil
}

// workflowEvidenceKindBound reports a durable evidence binding of the kind
// inside the accepted history. afterSeq bounds that history: bindings at or
// before it do not count, and zero admits the whole history.
func workflowEvidenceKindBound(ctx context.Context, q queryer, workID, kind string, afterSeq int64) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq>? ORDER BY seq`, workID, WorkflowEvidenceBound, afterSeq)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect workflow evidence bindings", true, "retry once the database is readable", err)
	}
	var payloads [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow evidence binding", true, "retry once the database is readable", err)
		}
		payloads = append(payloads, raw)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot scan workflow evidence bindings", true, "retry once the database is readable", err)
	}
	if err := rows.Close(); err != nil {
		return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot close workflow evidence bindings", true, "retry once the database is readable", err)
	}
	for _, raw := range payloads {
		var payload workflowEvidenceBoundPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return false, newFailure(KindInvariantViolation, "complete_workflow", "workflow evidence binding payload is malformed", false, "reread_entities")
		}
		if payload.EvidenceKind != kind {
			continue
		}
		var authoritative int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE op_id=? AND work_id=? AND principal_ref=? AND request_id=? AND result_kind='completed' AND EXISTS (SELECT 1 FROM json_each(durable_operations.evidence_refs) WHERE value=?)`, payload.ProducerRunRef, workID, payload.ProducerID, payload.ProducerWatermark, payload.ImmutableSubjectRef).Scan(&authoritative); err != nil {
			return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot verify workflow evidence authority", true, "retry once the database is readable", err)
		}
		if authoritative == 1 {
			return true, nil
		}
	}
	return false, nil
}

// latestWorkflowVerdict keeps the unbounded latest-history read every live
// caller uses; the bounded variant serves replay folds that see the intact
// log.
func latestWorkflowVerdict(ctx context.Context, tx queryer, workID string) (*workflowVerdictRecordedPayload, error) {
	return latestWorkflowVerdictBefore(ctx, tx, workID, 0)
}

// latestWorkflowVerdictBefore is the single verdict-history query owner.
// beforeSeq bounds the read for a replay fold: 0 keeps the unbounded
// latest-history semantics, and a fold passes its own sequence so a verdict
// recorded after it never enters the fold's decision.
func latestWorkflowVerdictBefore(ctx context.Context, tx queryer, workID string, beforeSeq int64) (*workflowVerdictRecordedPayload, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT 1`, workID, WorkflowVerdictRecorded, beforeSeq, beforeSeq).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow verdict", true, "retry once the database is readable", err)
	}
	var verdict workflowVerdictRecordedPayload
	if err := json.Unmarshal(raw, &verdict); err != nil {
		return nil, newFailure(KindInvariantViolation, "complete_workflow", "workflow verdict payload is malformed", false, "reread_entities")
	}
	return &verdict, nil
}

func latestWorkflowVerdicts(ctx context.Context, q queryer, workID string, contractVersion int64) ([]workflowVerdictRecordedPayload, error) {
	return latestWorkflowVerdictsBefore(ctx, q, workID, contractVersion, 0)
}

// Recovery folds see the intact log during replay, so their verdict input
// must stop before the binding whose completion they validate.
func latestWorkflowVerdictsBefore(ctx context.Context, q queryer, workID string, contractVersion, beforeSeq int64) ([]workflowVerdictRecordedPayload, error) {
	if contractVersion <= 0 {
		return nil, newFailure(KindInvariantViolation, "complete_workflow", "active workflow contract version is invalid", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	contracts, err := workflowContractPredicateHistory(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT seq,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND (?=0 OR seq<?) ORDER BY seq DESC`, workID, WorkflowVerdictRecorded, beforeSeq, beforeSeq)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow verdicts", true, "retry once the database is readable", err)
	}
	// The verdict history drains fully before any per-contract query runs: the
	// pooled connection is single, and a query inside this scan would park on
	// the rows the scan still holds.
	type collectedVerdict struct {
		seq int64
		raw []byte
	}
	collected := make([]collectedVerdict, 0, 8)
	for rows.Next() {
		var verdict collectedVerdict
		if err := rows.Scan(&verdict.seq, &verdict.raw); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot scan workflow verdict", true, "retry once the database is readable", err)
		}
		collected = append(collected, verdict)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot scan workflow verdicts", true, "retry once the database is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot close workflow verdicts", true, "retry once the database is readable", err)
	}
	// A contract whose ancestry carries the complete-step correction route
	// accepts no verdict recorded at or before the cutoff, however identical
	// the predicate payloads remain: the route exists because durable evidence
	// disproved the predecessor's premise. The cutoff persists across later
	// ordinary successors, so no descendant inherits the disproved verdicts.
	cutoff, err := workflowCompleteStepCorrectionEvidenceCutoff(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	result := make([]workflowVerdictRecordedPayload, 0)
	seen := make(map[string]bool)
	for _, collectedVerdict := range collected {
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal(collectedVerdict.raw, &verdict); err != nil {
			return nil, newFailure(KindInvariantViolation, "complete_workflow", "workflow verdict payload is malformed", false, "reread_entities")
		}
		if collectedVerdict.seq <= cutoff {
			continue
		}
		if verdict.ContractVersion <= 0 || verdict.ContractVersion > contractVersion || seen[verdict.PredicateID] {
			continue
		}
		if verdict.ContractVersion < contractVersion {
			if !workflowPredicateHistoryCompatible(contracts, verdict.ContractVersion, contractVersion, verdict.PredicateID, verdict) {
				continue
			}
		}
		seen[verdict.PredicateID] = true
		result = append(result, verdict)
	}
	return result, nil
}

type workflowContractDefinitionPin struct {
	definitionRef     string
	definitionVersion int64
	definitionDigest  string
}

type workflowContractDefinitionPinsData map[int64]workflowContractDefinitionPin

func workflowContractDefinitionPins(ctx context.Context, q queryer, workID string, currentVersion int64) (workflowContractDefinitionPinsData, error) {
	rows, err := q.QueryContext(ctx, `SELECT contract_version,definition_ref,definition_version,definition_digest FROM workflow_contracts WHERE work_id=? AND contract_version<=? ORDER BY contract_version`, workID, currentVersion)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow contract definition pins", true, "retry once the database is readable", err)
	}
	pins := make(workflowContractDefinitionPinsData)
	for rows.Next() {
		var version int64
		var pin workflowContractDefinitionPin
		if err := rows.Scan(&version, &pin.definitionRef, &pin.definitionVersion, &pin.definitionDigest); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot scan workflow contract definition pins", true, "retry once the database is readable", err)
		}
		pins[version] = pin
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot enumerate workflow contract definition pins", true, "retry once the database is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot close workflow contract definition pins", true, "retry once the database is readable", err)
	}
	return pins, nil
}

func workflowContractDefinitionPinsCompatible(pins workflowContractDefinitionPinsData, from, to int64) bool {
	if from <= 0 || to < from {
		return false
	}
	var expectedRef, expectedDigest string
	seen := from
	for version := from; version <= to; version++ {
		pin, ok := pins[version]
		if !ok || version != seen || pin.definitionRef == "" || !validDigest(pin.definitionDigest) {
			return false
		}
		if expectedRef == "" {
			expectedRef, expectedDigest = pin.definitionRef, pin.definitionDigest
		} else if pin.definitionRef != expectedRef || pin.definitionDigest != expectedDigest {
			return false
		}
		seen++
	}
	return seen == to+1
}

type workflowContractPredicateHistoryData map[int64]map[string]WorkflowReadPredicate

func workflowContractPredicateHistory(ctx context.Context, q queryer, workID string, currentVersion int64) (workflowContractPredicateHistoryData, error) {
	rows, err := q.QueryContext(ctx, `SELECT contract_version FROM workflow_contracts WHERE work_id=? AND contract_version<=? ORDER BY contract_version`, workID, currentVersion)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow contract history", true, "retry once the database is readable", err)
	}
	versions := make(map[int64]bool)
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot scan workflow contract history", true, "retry once the database is readable", err)
		}
		versions[version] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot enumerate workflow contract history", true, "retry once the database is readable", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot close workflow contract history", true, "retry once the database is readable", err)
	}
	history := make(workflowContractPredicateHistoryData, len(versions))
	for version := range versions {
		predicates, err := readWorkflowContractPredicates(ctx, q, workID, version)
		if err != nil {
			return nil, err
		}
		history[version] = make(map[string]WorkflowReadPredicate, len(predicates))
		for _, predicate := range predicates {
			history[version][predicate.PredicateID] = predicate
		}
	}
	return history, nil
}

func workflowPredicateHistoryCompatible(history workflowContractPredicateHistoryData, from, to int64, predicateID string, verdict workflowVerdictRecordedPayload) bool {
	if verdict.CriterionDigest != "" {
		approved, ok := history[to][predicateID]
		return ok && verdict.PredicateID == predicateID && approved.OutcomeKind != "" &&
			verdict.CriterionDigest == workflowCriterionDigest(approved.OutcomeKind, json.RawMessage(approved.OutcomePayload))
	}
	for version := from; version <= to; version++ {
		if _, ok := history[version]; !ok {
			return false
		}
		if version == from && verdict.PredicateID != predicateID {
			return false
		}
		if version == to {
			break
		}
		left, leftOK := history[version][predicateID]
		right, rightOK := history[version+1][predicateID]
		if !leftOK || !rightOK || left.OutcomeKind != right.OutcomeKind {
			return false
		}
		leftPayload, leftErr := canonicalJSON(json.RawMessage(left.OutcomePayload))
		rightPayload, rightErr := canonicalJSON(json.RawMessage(right.OutcomePayload))
		if leftErr != nil || rightErr != nil || string(leftPayload) != string(rightPayload) {
			return false
		}
	}
	approved := history[to][predicateID]
	return approved.PredicateID == predicateID && approved.OutcomeKind != "" && verdict.PredicateID == predicateID
}

func containsPredicateID(predicates []WorkflowReadPredicate, predicateID string) bool {
	for _, predicate := range predicates {
		if predicate.PredicateID == predicateID {
			return true
		}
	}
	return false
}

// WorkflowChangedRefsDigest is the deterministic digest used by callers that
// construct terminal metadata from action results.
func WorkflowChangedRefsDigest(refs []string) string {
	copyRefs := append([]string(nil), refs...)
	sort.Strings(copyRefs)
	h := sha256.Sum256([]byte(strings.Join(copyRefs, "\x00")))
	return "sha256:" + hex.EncodeToString(h[:])
}

// workflowCompletionCallerAuthority requires Main to complete the work.
func workflowCompletionCallerAuthority(ctx context.Context, tx *sql.Tx, workID, completingActor string) error {
	return workflowRequireMain(ctx, tx, workID, completingActor)
}
