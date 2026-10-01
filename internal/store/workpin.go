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
	WorkID                  string                    `json:"work_id"`
	Title                   string                    `json:"title"`
	LinearIssueKey          string                    `json:"linear_issue_key"`
	ProjectID               string                    `json:"project_id"`
	ProjectDisplayName      string                    `json:"project_display_name"`
	Version                 int64                     `json:"version"`
	Lifecycle               string                    `json:"lifecycle"`
	CancelledInstanceCloses int64                     `json:"cancelled_instance_closes"`
	WorkflowType            string                    `json:"workflow_type"`
	Step                    string                    `json:"step"`
	SelfRepair              *WorkflowSelfRepair       `json:"self_repair,omitempty"`
	Attempt                 *WorkPinAttempt           `json:"attempt"`
	PendingOperatorDecision *WorkflowOperatorQuestion `json:"pending_operator_decision"`
	// WithheldOperatorDecision names the checkpoint action whose question the
	// step declares but the gate holds closed, with the reason and the remedy.
	// It stays nil when a question is open and when the step has none.
	WithheldOperatorDecision *WorkflowOperatorQuestionWithheld `json:"withheld_operator_decision,omitempty"`
	Watermark                string                            `json:"watermark"`
	NextValidIntents         []WorkPinIntent                   `json:"next_valid_intents"`
	// DrivingSessions lists the distinct agent sessions that have driven this
	// workflow, with each session's most recent action and action time. It is
	// derived from workflow actors and actions, not session identity evidence.
	DrivingSessions []WorkPinDrivingSession    `json:"driving_sessions"`
	Correction      *WorkflowCorrectionContext `json:"correction,omitempty"`
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

type WorkPinAttempt struct {
	ID    string `json:"id"`
	Epoch int64  `json:"epoch"`
	Lane  string `json:"lane"`
	State string `json:"state"`
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
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot open a consistent work pin snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	return ReadWorkPinTx(ctx, tx, workID)
}

// ReadWorkPinTransactionTx adapts the opaque store transaction used by agent
// mutations to the SQL transaction-scoped pin reader.
func ReadWorkPinTransactionTx(ctx context.Context, transaction *Transaction, workID string) (WorkPin, error) {
	tx, err := transactionSQL(transaction, "work_pin")
	if err != nil {
		return WorkPin{}, err
	}
	return ReadWorkPinTx(ctx, tx, workID)
}

// ReadWorkPinTx derives a pin from the transaction supplied by its caller.
// Callers that hold a mutation transaction must use this function. The read
// runs in named phases — identity, instance, step intents, contract,
// attempt, recovery intents — each of which owns its own refusals; this
// function owns their order and the two early exits the phases cannot
// express (the contract-ambiguity escape hatch and the terminal-state
// intent selection).
func ReadWorkPinTx(ctx context.Context, tx *sql.Tx, workID string) (WorkPin, error) {
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
	registered, readDefinition, instanceState, err := workPinReadInstanceTx(ctx, tx, workID, &pin)
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
		// Duplicate recovery belongs to earlier, running steps. At complete,
		// the one-active-contract gate refuses it, a terminal instance
		// cannot run any earlier-step recovery, and a completed instance
		// keeps the route closed on every shape but the complete-step
		// correction admission.
		if instanceState != "completed" && !workflowCompleteStepCorrectionStep(registered.Definition, pin.Step) && !workflowCompletedInstanceActionImmutable(instanceState, "supersede_contract", pin.Lifecycle) && !isTerminalLifecycle(pin.Lifecycle) {
			pin.NextValidIntents = []WorkPinIntent{workPinIntentForAction(workflowContractRecoveryActionDefinition(), pin.Version, "operator_contract_correction")}
		}
		return pin, nil
	}
	var activeContractVersion int64
	if len(activeContractVersions) == 1 {
		activeContractVersion = activeContractVersions[0]
	}
	if err := workPinStepIntentsTx(ctx, tx, workID, &pin, registered.Definition); err != nil {
		return pin, err
	}
	if err := workPinReadContractTx(ctx, tx, workID, &pin, readDefinition, registered.Definition, instanceState, activeContractVersion); err != nil {
		return pin, err
	}
	if err := workPinReadAttemptTx(ctx, tx, workID, &pin); err != nil {
		return pin, err
	}
	if err := workPinRecoveryIntentsTx(ctx, tx, workID, &pin, registered.Definition, instanceState); err != nil {
		return pin, err
	}
	return pin, nil
}

// workPinReadIdentityTx fills the pin's item identity: version, lifecycle,
// title, Linear key, primary project, the closed-instance count, the
// watermark, and the driving sessions. The watermark belongs on every pin
// this read returns with a nil error, so it is assigned before any phase
// that can return partial data.
func workPinReadIdentityTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) error {
	if err := tx.QueryRowContext(ctx, `SELECT w.version,w.lifecycle,w.title,COALESCE(l.human_key,''),COALESCE(p.id,''),COALESCE(p.display_name,'') FROM work_items w LEFT JOIN linear_issue_links l ON l.work_id=w.id AND l.link_state='confirmed' LEFT JOIN work_projects wp ON wp.work_id=w.id AND wp.role='primary' LEFT JOIN projects p ON p.id=wp.project_id WHERE w.id=?`, workID).Scan(&pin.Version, &pin.Lifecycle, &pin.Title, &pin.LinearIssueKey, &pin.ProjectID, &pin.ProjectDisplayName); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "work_pin", "work item is not recorded", false, "reread_entities")
		}
		return wrapFailure(KindUnavailable, "work_pin", "cannot read work item", true, "retry once the database is readable", err)
	}
	pin.WorkID = workID
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_instances wi INDEXED BY workflow_instances_state JOIN work_items w ON w.id=wi.work_id JOIN work_projects wp ON wp.work_id=w.id WHERE wi.instance_state='cancelled' AND w.lifecycle='completed' AND wp.project_id=?`, pin.ProjectID).Scan(&pin.CancelledInstanceCloses); err != nil {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read cancelled-instance lifecycle closes", true, "retry once the database is readable", err)
	}
	var watermark int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&watermark); err != nil {
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

// workPinReadInstanceTx reads the workflow instance, sets the pin's step and
// workflow type, and verifies the pinned definition against the registry.
// It returns the registered definition, the raw read definition, and the
// instance state.
func workPinReadInstanceTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) (RegisteredDefinition, WorkflowReadDefinition, string, error) {
	var definition WorkflowReadDefinition
	var instanceState string
	if err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest,current_step,instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&definition.Ref, &definition.Version, &definition.Digest, &pin.Step, &instanceState); err != nil {
		if err == sql.ErrNoRows {
			return RegisteredDefinition{}, WorkflowReadDefinition{}, "", newFailure(KindProjectionNotFound, "work_pin", "workflow instance is not recorded", false, "reread_entities")
		}
		return RegisteredDefinition{}, WorkflowReadDefinition{}, "", wrapFailure(KindUnavailable, "work_pin", "cannot read workflow instance", true, "retry once the database is readable", err)
	}
	pin.WorkflowType = definition.Ref
	registered, err := verifyReadWorkflowDefinition(definition)
	if err != nil {
		return RegisteredDefinition{}, WorkflowReadDefinition{}, "", err
	}
	return registered, definition, instanceState, nil
}

// workPinStepIntentsTx fills the pin's step intents: the declared actions
// under the dispatch hold, the evidence-binding recovery, and the
// post-rejection review debt that hides the delivery exit until the fresh
// review stands behind it.
func workPinStepIntentsTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, definition WorkflowDefinition) error {
	dispatchHoldsAdvance, holdErr := workflowDispatchHoldsStepAdvance(ctx, tx, definition, workID, pin.Step, 0)
	if holdErr != nil {
		return holdErr
	}
	evidenceRecovery, recoveryErr := workflowEvidenceBindingRecoveryAvailable(ctx, tx, definition, workID, pin.Step)
	if recoveryErr != nil {
		return recoveryErr
	}
	pin.NextValidIntents = workPinIntents(definition, pin.Step, pin.Version, dispatchHoldsAdvance)
	if evidenceRecovery && !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowActionDefinitionByID(definition, "bind_evidence"), pin.Version, "evidence_binding_recovery"))
	}
	postRejectionDebt, debtErr := workflowPostRejectionReviewOutstanding(ctx, tx, workID, definition, "work_pin")
	if debtErr != nil {
		return debtErr
	}
	if !postRejectionDebt || !workflowPostRejectionReviewStep(definition, pin.Step) {
		return nil
	}
	// The pin never offers an advance the delivery guard will refuse, so
	// an unreviewed repaired result hides the refinement step's accepted
	// result, its recorded delivery, and the gate's delivery exit —
	// except the acceptance of a completed review whose dispatch settles
	// the debt, which is itself the fresh review the guard waits for.
	pin.NextValidIntents = workPinWithoutAction(pin.NextValidIntents, "record_delivery")
	pin.NextValidIntents = workPinWithoutAction(pin.NextValidIntents, "accept_worker_result")
	if stepDeclaresAction(definition, pin.Step, "start_refine") {
		ready, readyErr := workflowPostRejectionReviewReady(ctx, tx, workID, definition, "work_pin")
		if readyErr != nil {
			return readyErr
		}
		if ready {
			pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowActionDefinitionByID(definition, "accept_worker_result"), pin.Version, "post_rejection_review"))
		}
	}
	return nil
}

// workPinReadContractTx reads the one active contract and every enrichment
// that hangs off it: predicates, verified criteria on a completed instance,
// self-repair, the open operator question, the late-verdict recovery, and
// the verdict evidence. A work item with no contract row skips the block.
func workPinReadContractTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, readDefinition WorkflowReadDefinition, definition WorkflowDefinition, instanceState string, activeContractVersion int64) error {
	var contract WorkflowReadContract
	var required, routes, mandate, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,premise,required_evidence,route_conventions,spec_mandate,law_modifies,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=? AND superseded_by IS NULL`, workID, activeContractVersion).Scan(&contract.Version, &contract.Premise, &required, &routes, &mandate, &modifies, &contract.RigorClass); err == nil {
		if jsonErr := decodeWorkflowPinContract(&contract, required, routes, mandate, modifies); jsonErr != nil {
			return jsonErr
		}
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
		selfRepair, repairErr := readWorkflowSelfRepair(ctx, tx, workID, contract.Version)
		if repairErr != nil {
			return repairErr
		}
		contract.SelfRepair = selfRepair
		pin.SelfRepair = contract.SelfRepair
		pending, withheld, questionErr := workflowOperatorQuestionTx(ctx, tx, workID, pin.Step, pin.Version, readDefinition, contract)
		if questionErr != nil {
			return questionErr
		}
		pin.PendingOperatorDecision, pin.WithheldOperatorDecision = pending, withheld
		lateVerdictRecovery, recoveryErr := workflowLateVerdictRecoveryAvailable(ctx, tx, workID, definition, pin.Step)
		if recoveryErr != nil {
			return recoveryErr
		}
		if lateVerdictRecovery {
			pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(currentActionDefinition("record_verdict", true), pin.Version, "late_verdict_recovery"))
		}
		if stepDeclaresAction(definition, pin.Step, "record_verdict") || lateVerdictRecovery {
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

// workPinReadAttemptTx reads the latest worker attempt and its workflow
// epoch. An attempt row without a valid epoch is an invariant violation: the
// pin refuses rather than guess which attempt window a dispatch targets.
func workPinReadAttemptTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) error {
	var attempt WorkPinAttempt
	var epoch sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT a.attempt_id,a.lane_id,a.lifecycle_state,COALESCE((SELECT json_extract(e.payload,'$.attempt_epoch') FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=a.work_id AND e.kind=? AND json_extract(e.payload,'$.action_id')='dispatch_worker' AND json_extract(e.payload,'$.worker_attempt_id')=a.attempt_id ORDER BY e.seq DESC LIMIT 1),(SELECT json_extract(e.payload,'$.attempt_epoch') FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=a.work_id AND e.kind=? AND json_extract(e.payload,'$.step_id')=(SELECT current_step FROM workflow_instances WHERE work_id=a.work_id) ORDER BY e.seq DESC LIMIT 1),0) FROM worker_attempts a WHERE a.work_id=? ORDER BY a.dispatched_at DESC,a.attempt_id DESC LIMIT 1`, WorkflowActionCompleted, WorkflowActionStarted, workID).Scan(&attempt.ID, &attempt.Lane, &attempt.State, &epoch); err == nil {
		if !epoch.Valid || epoch.Int64 < 1 {
			return newFailure(KindInvariantViolation, "work_pin", "worker attempt has no valid workflow epoch", false, "rebuild the worker and workflow projections from the event log")
		}
		attempt.Epoch = epoch.Int64
		pin.Attempt = &attempt
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read worker attempt", true, "retry once the database is readable", err)
	}
	return nil
}

// workPinRecoveryIntentsTx completes the intent set with every recovery and
// correction route the current shape admits: worker-failure and contract
// correction, the delivery-gate correction request, escalated retry, the
// stale-design dispatch hold, the rejected-worker-result correction, and the
// confirm_premise and terminal-state selections that close the set.
func workPinRecoveryIntentsTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, definition WorkflowDefinition, instanceState string) error {
	workerFailureRecovery, recoveryErr := workflowWorkerFailureRecoveryAvailable(ctx, tx, workID, definition, pin.Step, "work_pin")
	if recoveryErr != nil {
		return recoveryErr
	}
	if workerFailureRecovery {
		pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workerFailureRecoveryActionDefinition(), pin.Version, "worker_failure_recovery"))
	}
	contractCorrection, correctionErr := workflowContractCorrectionAvailable(ctx, tx, workID, definition, pin.Step, "work_pin")
	if correctionErr != nil {
		return correctionErr
	}
	if contractCorrection && !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowContractRecoveryActionDefinition(), pin.Version, "operator_contract_correction"))
	}
	if err := workPinCorrectionRequestIntentsTx(ctx, tx, workID, pin, definition); err != nil {
		return err
	}
	if workPinContainsAction(pin.NextValidIntents, "dispatch_worker") {
		_, staleDesign, designErr := readCurrentWorkflowDesign(ctx, tx, workID)
		if designErr != nil {
			return designErr
		}
		if staleDesign {
			pin.NextValidIntents = workPinWithoutAction(pin.NextValidIntents, "dispatch_worker")
		}
	}
	if stepDeclaresAction(definition, pin.Step, "dispatch_worker") {
		rejected, rejectionErr := workflowRejectedWorkerResultAvailable(ctx, tx, workID, definition, pin.Step, "work_pin", 0)
		if rejectionErr != nil {
			return rejectionErr
		}
		if rejected {
			pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowCorrectionActionDefinition(), pin.Version, "worker_result_rejection"))
		}
	}
	// An unavailable confirmation is not advertised. confirm_premise is
	// answered only through an open operator question, so a pin without one
	// hides the action instead of sending the caller into a guaranteed
	// refusal. The gate closes the question both when the investigation
	// precondition withholds it and when no approved contract exists, and
	// neither state lets the selection guard through.
	if pin.PendingOperatorDecision == nil && workPinContainsAction(pin.NextValidIntents, "confirm_premise") {
		pin.NextValidIntents = workPinWithoutAction(pin.NextValidIntents, "confirm_premise")
	}
	// A completed instance with nonterminal work retains only the admitted
	// complete-step contract correction. Every other action remains immutable.
	if !isTerminalLifecycle(instanceState) {
		return nil
	}
	if instanceState == "completed" && contractCorrection && !workflowCompletedInstanceActionImmutable(instanceState, "supersede_contract", pin.Lifecycle) {
		pin.NextValidIntents = []WorkPinIntent{workPinIntentForAction(workflowContractRecoveryActionDefinition(), pin.Version, "operator_contract_correction")}
	} else {
		pin.NextValidIntents = []WorkPinIntent{}
	}
	return nil
}

// workPinCorrectionRequestIntentsTx applies the correction-request routes:
// the delivery gate replaces request_correction with its own bounded intent,
// an unescalated verification correction is advertised once, and an escalated
// correction collapses the set to the retry intents.
func workPinCorrectionRequestIntentsTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, definition WorkflowDefinition) error {
	correctionRequest, correctionErr := workflowCorrectionRequestContext(ctx, tx, workID, definition, pin.Step, "work_pin")
	if correctionErr != nil {
		return correctionErr
	}
	if workflowStepIsDeliveryGate(workflowStep(definition, pin.Step)) {
		// The gate's corrective return carries one reason code on every
		// pinned shape, and it is advertised only when the post-rejection
		// review shape stands behind it.
		pin.NextValidIntents = workPinWithoutAction(pin.NextValidIntents, "request_correction")
		if correctionRequest != nil && !correctionRequest.Escalated {
			pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowCorrectionRequestActionDefinition(), pin.Version, "delivery_gate_correction"))
		}
	} else if correctionRequest != nil && !correctionRequest.Escalated && !workPinContainsAction(pin.NextValidIntents, "request_correction") {
		pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(workflowCorrectionRequestActionDefinition(), pin.Version, "verification_correction"))
	}
	correction, correctionErr := workflowCorrectionContext(ctx, tx, workID, pin.Step)
	if correctionErr != nil {
		return correctionErr
	}
	pin.Correction = correction
	if correction != nil && correction.Escalated {
		pin.NextValidIntents = workPinEscalatedRetryIntents(pin.NextValidIntents)
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
// session that initialized the workflow, while action events identify later
// coordinator sessions that took over the item.
func workPinDrivingSessionsTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorkPinDrivingSession, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.session_ref,
		       COALESCE((SELECT COALESCE(json_extract(e.payload,'$.action_id'),e.kind)
		                    FROM domain_events e
		                   WHERE e.subject_type=? AND e.subject_id=?
		                     AND e.actor=a.actor_ref AND e.kind IN (?,?,?,?)
		                   ORDER BY e.occurred_at DESC,e.seq DESC LIMIT 1),
		                'workflow.actor_recorded'),
		       COALESCE((SELECT e.occurred_at
		                    FROM domain_events e
		                   WHERE e.subject_type=? AND e.subject_id=?
		                     AND e.actor=a.actor_ref AND e.kind IN (?,?,?,?)
		                   ORDER BY e.occurred_at DESC,e.seq DESC LIMIT 1),
		                a.first_seen_at) AS last_acted_at
		FROM workflow_actors a
		WHERE a.actor_class=?
		  AND (
			 a.actor_ref=(SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?)
			 OR EXISTS (
				SELECT 1 FROM domain_events e
			WHERE e.subject_type=? AND e.subject_id=? AND e.kind IN (?,?,?,?) AND e.actor=a.actor_ref
			 )
		  )
		ORDER BY last_acted_at DESC,a.session_ref
		LIMIT 17`, string(SubjectWorkItem), workID, WorkflowActionStarted, WorkflowActionCompleted, WorkflowActionCheckpointed, WorkflowActionFailed,
		string(SubjectWorkItem), workID, WorkflowActionStarted, WorkflowActionCompleted, WorkflowActionCheckpointed, WorkflowActionFailed,
		string(ActorAgent), workID, string(SubjectWorkItem), workID, WorkflowActionStarted, WorkflowActionCompleted, WorkflowActionCheckpointed, WorkflowActionFailed)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot read driving coordinator sessions", true, "retry once the database is readable", err)
	}
	defer rows.Close()
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
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT json_extract(payload,'$.evidence_kind') AS evidence_kind, json_extract(payload,'$.immutable_subject_ref') AS immutable_subject_ref FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref') IS NOT NULL ORDER BY immutable_subject_ref, evidence_kind LIMIT 100`, string(SubjectWorkItem), workID, WorkflowEvidenceBound)
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
		return newFailure(KindInvariantViolation, "work_pin", "workflow contract projection contains malformed arrays", false, "rebuild projections from the event log")
	}
	contract.RequiredEvidence = nonNilStrings(contract.RequiredEvidence)
	contract.RouteConventions = nonNilStrings(contract.RouteConventions)
	contract.SpecMandate = nonNilStrings(contract.SpecMandate)
	contract.LawModifies = nonNilStrings(contract.LawModifies)
	return nil
}

// workPinIntents lists the actions the current step offers. When
// dispatchHoldsAdvance is set, a worker dispatch in the current attempt holds
// every advancing exit except accept_worker_result, so those advances are
// omitted: the pin states what the caller may do, and an action the fold
// refuses is not one of them (CD-0133 D4).
func workPinIntents(definition WorkflowDefinition, stepID string, version int64, dispatchHoldsAdvance bool) []WorkPinIntent {
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
		if dispatchHoldsAdvance && action.ExecutionMode == ActionAdvance && actionID != "accept_worker_result" {
			continue
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
	if step != nil && step.Kind == WorkflowStepHumanCheckpoint && workflowContractCorrectionCheckpoint(definition, stepID) {
		intents = append(intents, workPinIntentForAction(workflowContractRecoveryActionDefinition(), version, "operator_contract_correction"))
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

func workPinWithoutAction(intents []WorkPinIntent, actionID string) []WorkPinIntent {
	filtered := make([]WorkPinIntent, 0, len(intents))
	for _, intent := range intents {
		if intent.ActionID != actionID {
			filtered = append(filtered, intent)
		}
	}
	return filtered
}

// workPinEscalatedRetryIntents keeps the dispatch_worker route visible when a
// correction reached the three-attempt limit (CD-0173). The fold still
// refuses the retry until the operator approval CD-0148 binds is consumed,
// so the pin advertises the action under the escalated reason instead of
// hiding a route that exists behind the approval wall.
func workPinEscalatedRetryIntents(intents []WorkPinIntent) []WorkPinIntent {
	out := make([]WorkPinIntent, 0, len(intents))
	for _, intent := range intents {
		if intent.ActionID == "dispatch_worker" {
			intent.ReasonCode = "escalated_retry_requires_approval"
		}
		out = append(out, intent)
	}
	return out
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

// workflowEvidenceBindingRecoveryAvailable reports whether bind_evidence is
// admitted past its declared binding step for this work item. The fold admits
// that recovery when the current step follows the binding step and the binding
// satisfies an outstanding requirement, but the pin listed only declared step
// actions. A caller that reaches a human checkpoint with a required evidence
// kind unbound then sees no route out, which is the wedge the acceptance
// deliverable gate refuses on. The conditions here are the fold's conditions,
// so the pin never advertises an action the fold would refuse.
func workflowEvidenceBindingRecoveryAvailable(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, workID, currentStep string) (bool, error) {
	if stepDeclaresAction(definition, currentStep, "bind_evidence") {
		return false, nil
	}
	bindingStep := workflowEvidenceBindingStep(definition, currentStep)
	if bindingStep == "" || !workflowStepFollows(definition, bindingStep, currentStep) {
		return false, nil
	}
	outstanding, err := outstandingWorkflowEvidenceRequirementsForWork(ctx, tx, workID, definition)
	if err != nil {
		return false, err
	}
	return len(outstanding) != 0, nil
}
