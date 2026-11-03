package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
)

// WorkPin is the one point-in-time projection that a caller needs to prepare
// the next operation for a work item.
type WorkPin struct {
	Review                  *WorkflowReviewState `json:"review,omitempty"`
	WorkID                  string               `json:"work_id"`
	Title                   string               `json:"title"`
	LinearIssueKey          string               `json:"linear_issue_key"`
	ProjectID               string               `json:"project_id"`
	ProjectDisplayName      string               `json:"project_display_name"`
	Version                 int64                `json:"version"`
	Lifecycle               string               `json:"lifecycle"`
	CancelledInstanceCloses int64                `json:"cancelled_instance_closes"`
	WorkflowType            string               `json:"workflow_type"`
	Step                    string               `json:"step"`
	Watermark               string               `json:"watermark"`
	// Obligations lists verification obligations from the active contract.
	Obligations *[]string `json:"obligations,omitempty"`
	// OutstandingEvidenceKinds is known kind-debt, not proof that reference
	// obligations are satisfied. Nil omits an unknown or partial view; a
	// non-nil empty slice records computed zero kind-debt.
	OutstandingEvidenceKinds *[]string       `json:"outstanding_evidence_kinds,omitempty"`
	NextValidIntents         []WorkPinIntent `json:"next_valid_intents"`
	// DrivingSessions lists the distinct agent sessions that have driven this
	// workflow, with each session's most recent action and action time. It is
	// derived from workflow actors and actions, not session identity evidence.
	DrivingSessions []WorkPinDrivingSession `json:"driving_sessions"`
	// WorkContext carries the strict reader's exact view for dispatch.
	// WorkContextUnavailable distinguishes an over-bound view from absence.
	// The partial recovery pin does not evaluate either field.
	WorkContext            *WorkContextView        `json:"work_context,omitempty"`
	WorkContextUnavailable *WorkContextUnavailable `json:"work_context_unavailable,omitempty"`
	// VerifiedCriteria carries the approved contract predicates and their latest
	// verdict kinds only after a workflow reaches completed.
	VerifiedCriteria []WorkPinVerifiedCriterion `json:"verified_criteria,omitempty"`
	// VerdictEvidence exposes the bound immutable evidence set at steps where
	// record_verdict is declarable, so a caller cites qualifying refs without
	// a raw store read (#974). It stays nil at every other step.
	VerdictEvidence []WorkPinEvidence `json:"verdict_evidence,omitempty"`
}

type WorkPinVerifiedCriterion struct {
	PredicateID    string          `json:"predicate_id"`
	Ordinal        int             `json:"ordinal"`
	OutcomeKind    string          `json:"outcome_kind"`
	OutcomePayload json.RawMessage `json:"outcome_payload"`
	VerdictKind    string          `json:"verdict_kind"`
}

type WorkPinEvidence struct {
	EvidenceKind        string `json:"evidence_kind"`
	ImmutableSubjectRef string `json:"immutable_subject_ref"`
}

type WorkPinDrivingSession struct {
	SessionRef   string `json:"session_ref"`
	LastActionID string `json:"last_action_id"`
	LastActedAt  string `json:"last_acted_at"`
}

type WorkPinIntent struct {
	Tool            string   `json:"tool"`
	Operation       string   `json:"operation"`
	ReasonCode      string   `json:"reason_code"`
	ActionID        string   `json:"action_id"`
	RequiredFields  []string `json:"required_fields"`
	ExpectedVersion int64    `json:"expected_version"`
}

// ReadWorkPin opens one read transaction and derives a complete pin.
func ReadWorkPin(ctx context.Context, s *Store, workID string) (WorkPin, error) {
	var pin WorkPin
	if s == nil || s.db == nil {
		return pin, newFailure(KindUnavailable, "work_pin", "store is not open", false, "open the authority database")
	}
	// The pin is advisory: when the verification is unavailable the read
	// proceeds without a proof, and the folds report law currency and the
	// confirmation unverified instead of failing the pin.
	if verified, err := s.PrepareWorkLawProof(ctx, workID); err == nil {
		ctx = verified
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot open a consistent work pin snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	return ReadWorkPinTx(ctx, tx, workID)
}

// ReadMutationWorkPinTxForRegistry derives a mutation pin in the caller's transaction.
func ReadMutationWorkPinTxForRegistry(ctx context.Context, transaction *Transaction, registry DefinitionRegistry, workID string, ownEventIDs []string) (WorkPin, bool, error) {
	tx, err := transactionSQL(transaction, "work_pin")
	if err != nil {
		return WorkPin{}, false, err
	}
	return readMutationWorkPinForRegistryTx(ctx, tx, registry, workID, ownEventIDs)
}

func readMutationWorkPinForRegistryTx(ctx context.Context, tx *sql.Tx, registry DefinitionRegistry, workID string, ownEventIDs []string) (WorkPin, bool, error) {
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "work_pin"); err != nil {
		return WorkPin{}, false, err
	}
	identity := WorkPin{WorkID: workID}
	err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&identity.Version)
	var instance bool
	switch err {
	case sql.ErrNoRows:
		removed := false
		for _, eventID := range ownEventIDs {
			var expected int64
			err := tx.QueryRowContext(ctx, `SELECT expected_version FROM work_removal_operations WHERE work_id=? AND event_id=? AND state='committed'`, workID, eventID).Scan(&expected)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return WorkPin{}, false, err
			}
			identity.Version = expected + 1
			removed = true
			break
		}
		if !removed {
			return WorkPin{}, false, newFailure(KindProjectionNotFound, "work_pin", "work item is not recorded", false, "reread_entities")
		}
	case nil:
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=?)`, workID).Scan(&instance); err != nil {
			return WorkPin{}, false, err
		}
	default:
		return WorkPin{}, false, err
	}
	if !instance {
		return identity, false, nil
	}
	pin, err := readWorkPinForRegistryTx(ctx, tx, workID, registry)
	return pin, true, err
}

// ReadWorkPinTx derives a pin from the transaction supplied by its caller.
func ReadWorkPinTx(ctx context.Context, tx *sql.Tx, workID string) (WorkPin, error) {
	return readWorkPinForRegistryTx(ctx, tx, workID, nil)
}

func readWorkPinForRegistryTx(ctx context.Context, tx *sql.Tx, workID string, _ DefinitionRegistry) (WorkPin, error) {
	var pin WorkPin
	if tx == nil {
		return pin, newFailure(KindUnavailable, "work_pin", "transaction is not open", false, "supply an active read transaction")
	}
	if len(workID) < 2 || len(workID) > 128 {
		return pin, newFailure(KindInvalidOperation, "work_pin", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	if err := workPinReadIdentityTx(ctx, tx, workID, &pin); err != nil {
		return pin, err
	}
	instanceState, err := workPinReadPlanTx(ctx, tx, workID, &pin)
	if err != nil {
		return pin, err
	}
	// The pin reads; it does not adjudicate. A work item whose projection
	// carries duplicate active contracts must stay readable, because the
	// operator reaches the recovery that retires them through this pin. A
	// strict selection here would make the ambiguity permanent.
	//
	// Every enrichment below this point resolves the one approved contract,
	// and an ambiguous projection has none. The pin therefore reports where
	// the work item stands and the one route that repairs it, and stops.
	activeContractVersions, contractErr := activeWorkflowContractVersions(ctx, tx, workID)
	if contractErr != nil {
		return pin, contractErr
	}
	if len(activeContractVersions) > 1 {
		return pin, nil
	}
	var activeContractVersion int64
	if len(activeContractVersions) == 1 {
		activeContractVersion = activeContractVersions[0]
	}
	if err := workPinReadContractTx(ctx, tx, workID, &pin, instanceState, activeContractVersion); err != nil {
		return pin, err
	}
	review, err := loadWorkflowReviewState(ctx, tx, workID)
	if err != nil {
		return pin, err
	}
	pin.Review = &review
	if err := workPinReadWorkContextTx(ctx, tx, workID, &pin); err != nil {
		return pin, err
	}
	return pin, nil
}

// workPinReadWorkContextTx uses the caller's transaction, never the pooled connection.
func workPinReadWorkContextTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) error {
	view, unavailable, err := readWorkContextProjection(ctx, tx, workID)
	pin.WorkContext, pin.WorkContextUnavailable = view, unavailable
	return err
}

// workPinReadIdentityTx fills the pin's item identity: version, lifecycle,
// title, Linear key, primary project, the closed-instance count, the
// watermark, and the driving sessions. The watermark belongs on every pin
// this read returns with a nil error, so it is assigned before any phase
// that can return partial data.
func workPinReadIdentityTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) error {
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "work_pin"); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT w.version,w.lifecycle,w.title,COALESCE(l.human_key,''),COALESCE(p.id,''),COALESCE(p.display_name,'') FROM work_items w LEFT JOIN linear_issue_links l ON l.work_id=w.id LEFT JOIN work_projects wp ON wp.work_id=w.id AND wp.role='primary' LEFT JOIN projects p ON p.id=wp.project_id WHERE w.id=?`, workID).Scan(&pin.Version, &pin.Lifecycle, &pin.Title, &pin.LinearIssueKey, &pin.ProjectID, &pin.ProjectDisplayName); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "work_pin", "work item is not recorded", false, "reread_entities")
		}
		return wrapFailure(KindUnavailable, "work_pin", "cannot read work item", true, "retry once the database is readable", err)
	}
	pin.WorkID = workID
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_instances wi INDEXED BY workflow_instances_state JOIN work_items w ON w.id=wi.work_id JOIN work_projects wp ON wp.work_id=w.id WHERE wi.instance_state='cancelled' AND w.lifecycle='completed' AND wp.project_id=?`, pin.ProjectID).Scan(&pin.CancelledInstanceCloses); err != nil {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read cancelled-instance lifecycle closes", true, "retry once the database is readable", err)
	}
	watermark, err := readWorkEffectSeq(ctx, tx, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read work watermark", true, "retry once the database is readable", err)
	}
	pin.Watermark = "seq:" + strconv.FormatInt(watermark, 10)
	sessions, err := workPinDrivingSessionsTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	pin.DrivingSessions = sessions
	return nil
}

// workPinReadPlanTx derives the next actions from the current stored plan.
func workPinReadPlanTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) (string, error) {
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return "", newFailure(KindProjectionNotFound, "work_pin", "workflow instance is not recorded", false, "reread_entities")
		}
		return "", wrapFailure(KindUnavailable, "work_pin", "cannot read workflow instance", true, "retry once the database is readable", err)
	}
	plan, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil {
		return "", err
	}
	pin.NextValidIntents = []WorkPinIntent{}
	if plan != nil {
		pin.WorkflowType = string(plan.Type)
		frontier := WorkflowStepPlanFrontier(*plan)
		if len(frontier) > 0 {
			pin.Step = frontier[0].ID
		}
		if isTerminalLifecycle(state) || isTerminalLifecycle(pin.Lifecycle) {
			return state, nil
		}
		ids := []string{"insert_step", "pick_type"}
		if WorkflowStepPlanComplete(*plan) {
			ids = append(ids, "complete")
		}
		for _, step := range frontier {
			actions := WorkflowStepPlanTemplateActions(plan.Type, step.ID)
			if actions == nil {
				actions = []string{"complete_step", "record_review", "record_delivery", "link_successor", "bind_evidence"}
			}
			for _, action := range actions {
				if action != "complete" {
					ids = append(ids, action)
				}
			}
		}
		return state, workPinAddCatalogIntents(pin, ids)
	}
	pin.Step = "intake"
	if isTerminalLifecycle(state) || isTerminalLifecycle(pin.Lifecycle) {
		return state, nil
	}
	prelude, err := readWorkflowPreludeTx(ctx, tx, workID)
	if err != nil {
		return "", err
	}
	var ids []string
	switch {
	case prelude == nil:
		ids = []string{"record_intake"}
	case prelude.Intake.Outcome == WorkflowIntakeClarify && prelude.Intake.OpenQuestionsFound && prelude.Clarify == nil:
		pin.Step = "clarify"
		ids = []string{"record_clarify"}
	case prelude.Intake.Outcome == WorkflowIntakeProceed && !prelude.Intake.OpenQuestionsFound,
		prelude.Intake.Outcome == WorkflowIntakeClarify && prelude.Intake.OpenQuestionsFound && prelude.Clarify != nil:
		ids = []string{"pick_type"}
	}
	return state, workPinAddCatalogIntents(pin, ids)
}

func workPinAddCatalogIntents(pin *WorkPin, ids []string) error {
	for _, id := range ids {
		if workPinContainsAction(pin.NextValidIntents, id) {
			continue
		}
		action, err := catalogAction(id)
		if err != nil {
			return err
		}
		pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(action, pin.Version, "declared_step_action"))
	}
	return nil
}

// verifyReadWorkflowDefinitionForRegistry is still used by continuity reads.
func verifyReadWorkflowDefinitionForRegistry(definition WorkflowReadDefinition, registry DefinitionRegistry) (RegisteredDefinition, error) {
	if registry == nil {
		return verifyReadWorkflowDefinition(definition)
	}
	return resolveCurrentWorkflowDefinition(registry, definition.Ref)
}

// workPinReadContractTx reads the one active contract and every enrichment
// that hangs off it: predicates, verified criteria on a completed instance,
// self-repair, the late-verdict recovery, and
// the verdict evidence. A work item with no contract row skips the block.
// The late-verdict recovery's availability is the folded admission state's
// answer, not a per-site re-derivation.
func workPinReadContractTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, instanceState string, activeContractVersion int64) error {
	var contract WorkflowReadContract
	var required, routes, mandate, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,premise,required_evidence,route_conventions,spec_mandate,law_modifies,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, activeContractVersion).Scan(&contract.Version, &contract.Premise, &required, &routes, &mandate, &modifies, &contract.RigorClass); err == nil {
		if jsonErr := decodeWorkflowPinContract(&contract, required, routes, mandate, modifies); jsonErr != nil {
			return jsonErr
		}
		kinds, evidenceErr := workPinOutstandingEvidenceKindsTx(ctx, tx, workID, activeContractVersion)
		if evidenceErr != nil {
			return evidenceErr
		}
		pin.OutstandingEvidenceKinds = kinds
		_, _, obligations, obligationErr := workflowEvidenceRequirementInputs(ctx, tx, workID)
		if obligationErr != nil {
			return obligationErr
		}
		ids := make([]string, 0, len(obligations))
		for _, obligation := range obligations {
			ids = append(ids, obligation.ObligationID)
		}
		ids = uniqueSorted(ids)
		pin.Obligations = &ids
		predicates, predicateErr := readWorkflowContractPredicates(ctx, tx, workID, contract.Version)
		if predicateErr != nil {
			return predicateErr
		}
		contract.OutcomePredicates = predicates
		if pin.Lifecycle == "completed" && instanceState == "completed" {
			criteria, criteriaErr := workPinVerifiedCriteriaTx(ctx, tx, workID, contract)
			if criteriaErr != nil {
				return criteriaErr
			}
			pin.VerifiedCriteria = criteria
		}
		if workPinContainsAction(pin.NextValidIntents, "record_verdict") {
			verdictEvidence, evidenceErr := workPinVerdictEvidenceTx(ctx, tx, workID)
			if evidenceErr != nil {
				return evidenceErr
			}
			pin.VerdictEvidence = verdictEvidence
		}
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read workflow contract", true, "retry once the database is readable", err)
	}
	return nil
}

func workPinVerifiedCriteriaTx(ctx context.Context, tx *sql.Tx, workID string, contract WorkflowReadContract) ([]WorkPinVerifiedCriterion, error) {
	verdicts, err := latestWorkflowVerdicts(ctx, tx, workID, contract.Version)
	if err != nil {
		return nil, err
	}
	verdictByPredicate := make(map[string]workflowVerdictRecordedPayload, len(verdicts))
	for _, verdict := range verdicts {
		verdictByPredicate[verdict.PredicateID] = verdict
	}
	criteria := make([]WorkPinVerifiedCriterion, 0, len(contract.OutcomePredicates))
	for _, predicate := range contract.OutcomePredicates {
		verdict, ok := verdictByPredicate[predicate.PredicateID]
		if !ok || !json.Valid([]byte(predicate.OutcomePayload)) {
			return nil, nil
		}
		criteria = append(criteria, WorkPinVerifiedCriterion{
			PredicateID:    predicate.PredicateID,
			Ordinal:        predicate.Ordinal,
			OutcomeKind:    predicate.OutcomeKind,
			OutcomePayload: json.RawMessage(predicate.OutcomePayload),
			VerdictKind:    verdict.VerdictKind,
		})
	}
	return criteria, nil
}

// workPinDrivingSessionsTx returns the bounded set of agent sessions that have
// driven the work item, most recent first. The execution actor preserves the
// session that initialized the workflow, while current action facts identify later
// coordinator sessions that took over the item.
func workPinDrivingSessionsTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorkPinDrivingSession, error) {
	rows, err := tx.QueryContext(ctx, workPinDrivingSessionsSQL, workPinDrivingSessionsArgs(workID)...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot read driving coordinator sessions", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	return scanWorkPinDrivingSessions(rows)
}

func workPinDrivingSessionsArgs(workID string) []any {
	return []any{workID, string(ActorAgent), workID, workID}
}

const workPinDrivingSessionsSQL = `
		SELECT a.session_ref,
		       COALESCE(f.last_action_id,'workflow.actor_recorded'),
		       COALESCE(f.last_acted_at,a.first_seen_at) AS last_acted_at
		FROM workflow_actors a
		LEFT JOIN workflow_coordinator_actions f ON f.work_id=? AND f.actor_ref=a.actor_ref
		WHERE a.actor_class=?
		  AND (
			 a.actor_ref=(SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?)
			 OR a.actor_ref IN (
				SELECT actor_ref FROM workflow_coordinator_actions WHERE work_id=?
			 )
		  )
		ORDER BY last_acted_at DESC,a.session_ref
		LIMIT 17`

func scanWorkPinDrivingSessions(rows *sql.Rows) ([]WorkPinDrivingSession, error) {
	sessions := make([]WorkPinDrivingSession, 0, 4)
	for rows.Next() {
		var session WorkPinDrivingSession
		if err := rows.Scan(&session.SessionRef, &session.LastActionID, &session.LastActedAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_pin", "cannot scan driving coordinator session", true, "retry once the database is readable", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot enumerate driving coordinator sessions", true, "retry once the database is readable", err)
	}
	if len(sessions) > 16 {
		return nil, newFailure(KindLimitExceeded, "work_pin", "driving coordinator sessions exceed the pin bound", false, "reduce the number of active coordinator sessions")
	}
	return sessions, nil
}

// workPinVerdictEvidenceTx reads the distinct bound evidence pairs for a
// work item, ordered deterministically. The set is read-only; binding stays
// owned by the workflow actions that mint evidence events.
func workPinVerdictEvidenceTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorkPinEvidence, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT json_extract(payload,'$.evidence_kind') AS evidence_kind, json_extract(payload,'$.immutable_subject_ref') AS immutable_subject_ref FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref') IS NOT NULL ORDER BY immutable_subject_ref, evidence_kind LIMIT 100`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot read bound verdict evidence", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []WorkPinEvidence{}
	for rows.Next() {
		var entry WorkPinEvidence
		if scanErr := rows.Scan(&entry.EvidenceKind, &entry.ImmutableSubjectRef); scanErr != nil {
			return nil, wrapFailure(KindUnavailable, "work_pin", "cannot scan bound verdict evidence", true, "retry once the database is readable", scanErr)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot iterate bound verdict evidence", true, "retry once the database is readable", err)
	}
	return out, nil
}

func decodeWorkflowPinContract(contract *WorkflowReadContract, required, routes, mandate, modifies string) error {
	if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandate), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
		return newFailure(KindInvariantViolation, "work_pin", "workflow contract projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	contract.RequiredEvidence = nonNilStrings(contract.RequiredEvidence)
	contract.RouteConventions = nonNilStrings(contract.RouteConventions)
	contract.SpecMandate = nonNilStrings(contract.SpecMandate)
	contract.LawModifies = nonNilStrings(contract.LawModifies)
	return nil
}

// workPinIntents lists the actions the current step offers, the pure shape
// read. The per-intent workflowAdmit decision in workPinStepIntentsTx drops
// what the folded state refuses — a dispatched worker's hold hides every
// advancing exit except accept_worker_result (CD-0133 D4) — so the hold is
// the shared admission's answer, never a per-site predicate here.
func workPinIntents(definition WorkflowDefinition, stepID string, version int64) []WorkPinIntent {
	step := workflowStep(definition, stepID)
	if step == nil {
		return []WorkPinIntent{}
	}
	actions := make(map[string]WorkflowActionDefinition, len(definition.ActionDefinitions))
	for _, action := range definition.ActionDefinitions {
		actions[action.ID] = action
	}
	intents := make([]WorkPinIntent, 0, len(step.Actions))
	for _, actionID := range step.Actions {
		action, ok := actions[actionID]
		if !ok {
			continue
		}
		// The approve intent advertises the pinned declaration's selected
		// shape; an unusable pinned initial declaration is never advertised.
		if actionID == "approve_contract" && workflowDeclaresDecisionDomains(definition) {
			selected, err := workflowDecisionFactInitialActionDefinition(definition)
			if err != nil {
				continue
			}
			action = selected
		}
		payload := publicWorkflowActionPayload(action)
		fields := make([]string, 0, len(payload.Fields))
		for _, field := range payload.Fields {
			if field.Required {
				fields = append(fields, field.Name)
			}
		}
		intents = append(intents, WorkPinIntent{Tool: "concord_work_transition", Operation: "workflow_action", ReasonCode: "declared_step_action", ActionID: action.ID, RequiredFields: fields, ExpectedVersion: version})
	}
	return intents
}

func workPinContainsAction(intents []WorkPinIntent, actionID string) bool {
	for _, intent := range intents {
		if intent.ActionID == actionID {
			return true
		}
	}
	return false
}

func workPinIntentForAction(action WorkflowActionDefinition, version int64, reason string) WorkPinIntent {
	payload := publicWorkflowActionPayload(action)
	fields := make([]string, 0, len(payload.Fields))
	for _, field := range payload.Fields {
		if field.Required {
			fields = append(fields, field.Name)
		}
	}
	return WorkPinIntent{Tool: "concord_work_transition", Operation: "workflow_action", ReasonCode: reason, ActionID: action.ID, RequiredFields: fields, ExpectedVersion: version}
}

func publicWorkflowActionPayload(action WorkflowActionDefinition) WorkflowPayloadDefinition {
	if action.PublicPayload != nil {
		return *action.PublicPayload
	}
	if policy, ok := builtinActionPolicies[action.ID]; ok && policy.PublicPayload != nil {
		return *policy.PublicPayload
	}
	return action.Payload
}

// workflowActionDefinitionByID returns the pinned definition's action, or a bare
// action carrying the ID when the definition omits it.
func workflowActionDefinitionByID(definition WorkflowDefinition, actionID string) WorkflowActionDefinition {
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			return action
		}
	}
	return WorkflowActionDefinition{ID: actionID}
}

func workPinUnverdictedCheckRequirements(ctx context.Context, q queryer, workID string, contractVersion int64) ([]workflowEvidenceRequirement, bool, error) {
	predicates, err := readWorkflowContractPredicates(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, false, err
	}
	verdicts, err := latestWorkflowVerdictsBefore(ctx, q, workID, contractVersion, 0)
	if err != nil {
		return nil, false, err
	}
	verdicted := make(map[string]bool, len(verdicts))
	for _, verdict := range verdicts {
		verdicted[verdict.PredicateID] = true
	}
	var requirements []workflowEvidenceRequirement
	for _, predicate := range predicates {
		if verdicted[predicate.PredicateID] {
			continue
		}
		approved, decodeErr := DecodeWorkflowPredicate([]byte(predicate.OutcomePayload))
		if decodeErr != nil || string(approved.Kind) != predicate.OutcomeKind {
			return nil, false, nil
		}
		if approved.Kind == PredicateCheck {
			requirements = append(requirements, workflowEvidenceRequirement{Kind: string(EvidenceVerification), Reference: approved.ImmutableSubjectRef})
		}
	}
	return requirements, true, nil
}

// workPinOutstandingEvidenceKindsTx reports the contract's unbound evidence
// kinds. Past the evidence-binding step it also reports verification for each
// unverdicted active check predicate whose subject is unbound; a malformed
// predicate leaves the debt unknown.
func workPinOutstandingEvidenceKindsTx(ctx context.Context, q queryer, workID string, contractVersion int64) (*[]string, error) {
	required, mandates, obligations, err := workflowEvidenceRequirementInputs(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	var requirements []workflowEvidenceRequirement
	for _, kind := range required {
		requirements = append(requirements, workflowEvidenceRequirement{Kind: kind})
	}
	for _, mandate := range mandates {
		requirements = append(requirements, workflowEvidenceRequirement{Reference: mandate})
	}
	for _, obligation := range obligations {
		requirements = append(requirements, workflowEvidenceRequirement{Kind: obligation.ObligationID, Reference: obligation.LawID})
	}
	checks, known, checkErr := workPinUnverdictedCheckRequirements(ctx, q, workID, contractVersion)
	if checkErr != nil || !known {
		return nil, checkErr
	}
	requirements = append(requirements, checks...)
	requirements, err = unboundWorkflowEvidenceRequirements(ctx, q, workID, requirements, 0)
	if err != nil {
		return nil, err
	}
	kinds := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		kinds = append(kinds, requirement.Kind)
	}
	kinds = uniqueSorted(kinds)
	return &kinds, nil
}
