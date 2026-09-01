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
	return newFailure(KindWorkflowCompletionRequired, "append_event", "workflow.completed is reserved for CompleteWorkflowTx", false, "use the ordered workflow completion entry point")
}

// CompleteWorkflow is the only public completion entry point.  It owns one
// SQLite transaction from the first gate clause through all impact notices and
// the reserved completion append.
func CompleteWorkflow(ctx context.Context, s *Store, event Event) error {
	return CompleteWorkflowWithRegistry(ctx, s, BuiltinWorkflowRegistry(), event)
}

// CompleteWorkflowWithRegistry is the test and embedded-engine seam for a
// pinned definition registry.  The registry is read-only during completion.
func CompleteWorkflowWithRegistry(ctx context.Context, s *Store, registry DefinitionRegistry, event Event) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "complete_workflow", "store is not open", false, "open a store before completing a workflow")
	}
	// A replay of the exact completion identity is an idempotent no-op.  This
	// check happens before opening the mutation transaction and never treats a
	// different event identity as a replay.
	var existingKind string
	err := s.db.QueryRowContext(ctx, `SELECT kind FROM domain_events WHERE event_id=?`, event.EventID).Scan(&existingKind)
	if err == nil && existingKind == WorkflowCompleted {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot begin workflow completion", true, "retry once the database is writable", err)
	}
	if err := CompleteWorkflowTxWithRegistry(ctx, tx, registry, event); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot commit workflow completion", true, "retry once the database is writable", err)
	}
	// committed; the durability barrier must hold before acknowledging
	if err := s.SyncDurable(ctx); err != nil {
		return err
	}
	return nil
}

// CompleteWorkflowTx evaluates the complete ordered gate using the built-in
// v1 definitions.
func CompleteWorkflowTx(ctx context.Context, tx *sql.Tx, event Event) error {
	return CompleteWorkflowTxWithRegistry(ctx, tx, BuiltinWorkflowRegistry(), event)
}

// CompleteWorkflowTxWithRegistry is the caller-owned transaction seam.  No
// completion or notice is appended until every earlier clause has passed.
func CompleteWorkflowTxWithRegistry(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, event Event) error {
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
	if err := workflowBase(event, payload.WorkflowVersionFields); err != nil {
		return err
	}
	if !workflowExecutionAllowsStaleRecovery("complete", event.Payload) {
		if err := checkWorkflowLawRevisionStalenessTx(ctx, tx, event.SubjectID); err != nil {
			return err
		}
	}
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	// CompleteWorkflowTxWithRegistry is also called by the workflow_action
	// boundary, which already owns fold_guard. Do not open a nested fold guard
	// (or delete the caller's guard on return).
	var guardCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM fold_guard WHERE active=1`).Scan(&guardCount); err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect projection fold guard", true, "retry once the database is readable", err)
	}
	ownsFoldGuard := guardCount == 0
	if ownsFoldGuard {
		if err := enterFold(ctx, tx); err != nil {
			return err
		}
		defer func() { _ = leaveFold(ctx, tx) }()
	}

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
	if err := workflowActorsDistinct(ctx, tx, event.SubjectID, event.Actor, "", false, "complete_workflow"); err != nil {
		return err
	}

	// Clause 1: durable, event-folded evidence bound to the approved contract.
	if err := verifyCompletionEvidence(ctx, tx, event.SubjectID, contract.RequiredEvidence, definition.RequiredEvidenceKinds); err != nil {
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
	if err := verifyCompletionScopeAndMandates(ctx, tx, event.SubjectID, contract); err != nil {
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
	}

	// Clause 4: verdict presence and the complete pinned actor tuple.
	verdict, err := latestWorkflowVerdict(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if verdict == nil || verdict.ContractVersion != contract.Version {
		return workflowClauseFailure(KindMissingEvidence, 4, "workflow verdict is missing for the approved contract", "provide_evidence")
	}
	if err := workflowCompletionActorDistinct(ctx, tx, event.SubjectID, verdict.VerdictActorRef, verdict.VerdictModel, definition.EvaluatorIndependence.ModelDistinct); err != nil {
		return workflowClauseError(err, 4)
	}
	if err := verifyVerdictEvidence(ctx, tx, event.SubjectID, verdict.EvaluationEvidence); err != nil {
		return workflowClauseError(err, 4)
	}

	// Clause 5: premise confirmation is an operator approval, not a payload bit.
	var confirmedBy string
	if err := tx.QueryRowContext(ctx, `SELECT confirmed_by FROM workflow_premise_confirmations WHERE work_id=? AND contract_version=?`, event.SubjectID, contract.Version).Scan(&confirmedBy); err != nil {
		if err == sql.ErrNoRows {
			return workflowClauseFailure(KindApprovalRequired, 5, "workflow premise is not confirmed", "request_approval")
		}
		return workflowClauseError(wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect premise confirmation", true, "retry once the database is readable", err), 5)
	}
	if err := requireActorClass(ctx, tx, confirmedBy, "operator"); err != nil {
		return workflowClauseFailure(KindApprovalRequired, 5, "workflow premise confirmation is not operator typed", "request_approval")
	}

	// Clause 6: the folded evaluator verdict is the authority.  Incomparable,
	// weaker, and non-success verdicts are never coerced into completion.
	if verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved {
		return workflowClauseFailure(KindOutcomeMismatch, 6, "delivered outcome is weaker or incomparable with the approved outcome", "contact_operator")
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
		if err := foldRegisteredEvent(ctx, tx, notice); err != nil {
			return workflowClauseFailure(KindOperationConflict, 7, "impact notice fold conflicted", "reconcile_operation")
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
	if err := foldRegisteredEvent(ctx, tx, completion); err != nil {
		return workflowClauseFailure(KindOperationConflict, 7, "completion fold conflicted", "reconcile_operation")
	}
	return nil
}

type workflowCompletionContractData struct {
	Version             int64
	RequiredEvidence    []string
	SpecMandate         []string
	LawModifies         []string
	LawBoundaryVersion  int
	OutcomeKind         string
	OutcomePayload      string
	ArchitectureBinding *WorkflowArchitectureBinding
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

func workflowCompletionContract(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID string) (workflowCompletionContractData, WorkflowDefinition, error) {
	var result workflowCompletionContractData
	var required, mandates, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT c.contract_version,c.required_evidence,c.spec_mandate,c.law_modifies,c.law_boundary_version,p.outcome_kind,p.outcome_payload FROM workflow_contracts c JOIN workflow_contract_predicates p ON p.work_id=c.work_id AND p.contract_version=c.contract_version AND p.ordinal=0 WHERE c.work_id=? AND c.superseded_by IS NULL ORDER BY c.contract_version DESC LIMIT 1`, workID).Scan(&result.Version, &required, &mandates, &modifies, &result.LawBoundaryVersion, &result.OutcomeKind, &result.OutcomePayload); err != nil {
		if err == sql.ErrNoRows {
			return result, WorkflowDefinition{}, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract is missing or ambiguous", false, "reread_entities")
		}
		return result, WorkflowDefinition{}, wrapFailure(KindUnavailable, "complete_workflow", "cannot read approved workflow contract", true, "retry once the database is readable", err)
	}
	if err := json.Unmarshal([]byte(required), &result.RequiredEvidence); err != nil {
		return result, WorkflowDefinition{}, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract arrays are malformed", false, "reread_entities")
	}
	if err := json.Unmarshal([]byte(mandates), &result.SpecMandate); err != nil || json.Unmarshal([]byte(modifies), &result.LawModifies) != nil {
		return result, WorkflowDefinition{}, newFailure(KindInvariantViolation, "complete_workflow", "approved workflow contract arrays are malformed", false, "reread_entities")
	}
	var bindingErr error
	result.ArchitectureBinding, bindingErr = readWorkflowArchitectureBinding(ctx, tx, workID, result.Version)
	if bindingErr != nil {
		return result, WorkflowDefinition{}, bindingErr
	}
	definition, err := VerifyWorkflowInstanceDefinitionTx(ctx, tx, registry, workID)
	if err != nil {
		return result, WorkflowDefinition{}, err
	}
	if definition.Definition.OutcomeSchema.DefaultKind != "" && result.OutcomeKind != string(definition.Definition.OutcomeSchema.DefaultKind) {
		// The payload's kind is still decoded below; this check only rejects a
		// contract that disagrees with a pinned family default.
		return result, WorkflowDefinition{}, newFailure(KindInvariantViolation, "complete_workflow", "approved outcome kind disagrees with the pinned definition", false, "reread_entities")
	}
	approved, err := DecodeWorkflowPredicate([]byte(result.OutcomePayload))
	if err != nil || string(approved.Kind) != result.OutcomeKind {
		return result, WorkflowDefinition{}, newFailure(KindInvariantViolation, "complete_workflow", "approved outcome predicate is malformed or mismatched", false, "reread_entities")
	}
	if err := ValidateWorkflowPredicateForDefinition(definition.Definition, approved); err != nil {
		return result, WorkflowDefinition{}, newFailure(KindInvariantViolation, "complete_workflow", "approved outcome predicate is outside the pinned definition", false, "reread_entities")
	}
	return result, definition.Definition, nil
}

func VerifyWorkflowInstanceDefinitionTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID string) (RegisteredDefinition, error) {
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
	return VerifyWorkflowDefinitionPin(registry, pin)
}

func verifyCompletionEvidence(ctx context.Context, tx *sql.Tx, workID string, required []string, definitionRequired []EvidenceKind) error {
	needed := append([]string(nil), required...)
	for _, kind := range definitionRequired {
		if !contains(needed, string(kind)) {
			needed = append(needed, string(kind))
		}
	}
	for _, kind := range needed {
		found, err := workflowEvidenceKindBound(ctx, tx, workID, kind)
		if err != nil {
			return err
		}
		if !found {
			return newFailure(KindMissingEvidence, "complete_workflow", "required workflow evidence is missing or not durably bound", false, "provide_evidence")
		}
	}
	return nil
}

func verifyCompletionScopeAndMandates(ctx context.Context, tx *sql.Tx, workID string, contract workflowCompletionContractData) error {
	for _, mandate := range contract.SpecMandate {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, mandate).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect spec-mandate evidence", true, "retry once the database is readable", err)
		}
		if count == 0 {
			return newFailure(KindInvariantViolation, "complete_workflow", "declared spec mandate is not satisfied", false, "reread_entities")
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT target_work_id FROM workflow_impact_edges WHERE work_id=? AND edge_kind='modifies' ORDER BY edge_id`, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect workflow impact scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow impact scope", true, "retry once the database is readable", err)
		}
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
	return rows.Err()
}

// workflowCompletionActorDistinct enforces CD-0013 D5 actor distinctness and,
// where the definition declares it, the CD-0017 D6 readback-model dimension.
// The executing model is read from the instance because it is a property of the
// run, not of the actor identity; the verdict model travels on the verdict
// event for the same reason.
func workflowCompletionActorDistinct(ctx context.Context, tx *sql.Tx, workID, verdictActor, verdictModel string, requireModelDistinct bool) error {
	return workflowActorsDistinct(ctx, tx, workID, verdictActor, verdictModel, requireModelDistinct, "complete_workflow")
}

func workflowActorsDistinct(ctx context.Context, tx *sql.Tx, workID, verdictActor, verdictModel string, requireModelDistinct bool, operation string) error {
	var executing WorkflowActor
	if err := tx.QueryRowContext(ctx, `SELECT a.actor_ref,a.principal_ref,a.client_ref,a.agent_ref,a.session_ref,a.actor_class,i.execution_model FROM workflow_instances i JOIN workflow_actors a ON a.actor_ref=i.execution_actor_ref WHERE i.work_id=?`, workID).Scan(&executing.ActorRef, &executing.PrincipalRef, &executing.ClientRef, &executing.AgentRef, &executing.SessionRef, &executing.ActorClass, &executing.Model); err != nil {
		return newFailure(KindUnauthorized, operation, "executing actor tuple is incomplete", false, "contact_operator")
	}
	var verdict WorkflowActor
	if err := tx.QueryRowContext(ctx, `SELECT actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class FROM workflow_actors WHERE actor_ref=?`, verdictActor).Scan(&verdict.ActorRef, &verdict.PrincipalRef, &verdict.ClientRef, &verdict.AgentRef, &verdict.SessionRef, &verdict.ActorClass); err != nil {
		return newFailure(KindUnauthorized, operation, "verdict actor tuple is incomplete", false, "contact_operator")
	}
	verdict.Model = verdictModel
	if err := ValidateDistinctWorkflowActors(executing, verdict, requireModelDistinct); err != nil {
		return err
	}
	return nil
}

func verifyVerdictEvidence(ctx context.Context, tx *sql.Tx, workID string, refs []string) error {
	for _, ref := range refs {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, ref).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect verdict evidence", true, "retry once the database is readable", err)
		}
		if count == 0 {
			return newFailure(KindMissingEvidence, "complete_workflow", "verdict evidence is not durably bound", false, "provide_evidence")
		}
	}
	return nil
}

func verifyBlockingStaleness(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, required []string) error {
	for _, rule := range definition.StalenessRules {
		if rule.Severity != "block" {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, rule.InputRef).Scan(&count); err != nil {
			return wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect staleness evidence", true, "retry once the database is readable", err)
		}
		if count == 0 && !contains(required, rule.InputRef) {
			return newFailure(KindStaleRequiresReview, "complete_workflow", "blocking staleness rule has drifted", false, "refresh_context")
		}
	}
	return nil
}

func workflowStalenessWarnings(ctx context.Context, tx *sql.Tx, workID string, definition WorkflowDefinition, required []string) ([]string, error) {
	var warnings []string
	for _, rule := range definition.StalenessRules {
		if rule.Severity != "warning" {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, rule.InputRef).Scan(&count); err != nil {
			return nil, wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect warning staleness evidence", true, "retry once the database is readable", err)
		}
		if count == 0 && !contains(required, rule.InputRef) {
			warnings = append(warnings, rule.ID)
		}
	}
	sort.Strings(warnings)
	return warnings, nil
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

func workflowEvidenceKindBound(ctx context.Context, tx *sql.Tx, workID, kind string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq`, workID, WorkflowEvidenceBound)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot inspect workflow evidence bindings", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot read workflow evidence binding", true, "retry once the database is readable", err)
		}
		var payload workflowEvidenceBoundPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return false, newFailure(KindInvariantViolation, "complete_workflow", "workflow evidence binding payload is malformed", false, "reread_entities")
		}
		if payload.EvidenceKind != kind {
			continue
		}
		var authoritative int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM durable_operations WHERE op_id=? AND work_id=? AND principal_ref=? AND request_id=? AND result_kind='completed' AND EXISTS (SELECT 1 FROM json_each(durable_operations.evidence_refs) WHERE value=?)`, payload.ProducerRunRef, workID, payload.ProducerID, payload.ProducerWatermark, payload.ImmutableSubjectRef).Scan(&authoritative); err != nil {
			return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot verify workflow evidence authority", true, "retry once the database is readable", err)
		}
		if authoritative == 1 {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, wrapFailure(KindUnavailable, "complete_workflow", "cannot scan workflow evidence bindings", true, "retry once the database is readable", err)
	}
	return false, nil
}

func latestWorkflowVerdict(ctx context.Context, tx *sql.Tx, workID string) (*workflowVerdictRecordedPayload, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowVerdictRecorded).Scan(&raw); err != nil {
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

// WorkflowChangedRefsDigest is the deterministic digest used by callers that
// construct terminal metadata from action results.
func WorkflowChangedRefsDigest(refs []string) string {
	copyRefs := append([]string(nil), refs...)
	sort.Strings(copyRefs)
	h := sha256.Sum256([]byte(strings.Join(copyRefs, "\x00")))
	return "sha256:" + hex.EncodeToString(h[:])
}
