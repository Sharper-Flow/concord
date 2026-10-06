package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const workflowCorrectionAttemptLimit int64 = 3

// workflowCompleteStepCorrectionRoute is the reserved contract route
// convention a successor declares when the operator approved it at the pinned
// complete step. It is the durable marker that cuts predecessor verdicts off
// the successor's predicates: the route exists because durable evidence
// disproved the predecessor's premise, so the verdicts that predecessor
// carried hold no authority, however identical the predicate payloads remain.
const workflowCompleteStepCorrectionRoute = "complete_step_correction"

// workflowCompleteStepCorrectionStep reports whether the pinned step is the
// completion step of a break-fix or implementation workflow, the only shape
// the complete-step correction route and its reserved convention apply to.
func workflowCompleteStepCorrectionStep(definition WorkflowDefinition, currentStep string) bool {
	return workflowCorrectionWorkflow(definition) && stepDeclaresAction(definition, currentStep, "complete")
}

// workflowCompletedInstanceSupersedeOffShape reports a completed workflow
// instance whose pinned shape does not carry the complete-step correction
// step. That step is the only shape whose supersession admission reopens the
// instance, so on any other shape a completed instance keeps every recovery
// route closed: a supersession there folds without the return that reopens
// the work.
func workflowCompletedInstanceSupersedeOffShape(state string, definition WorkflowDefinition, currentStep string) bool {
	return state == "completed" && !workflowCompleteStepCorrectionStep(definition, currentStep)
}

// workflowCompletedInstanceOffShapeFailure is the refusal every admission
// surface names for a completed instance off the supported shape, so a
// divergence between the surfaces cannot reopen the route.
func workflowCompletedInstanceOffShapeFailure(subject string) *Failure {
	return newFailure(KindInvalidOperation, subject, "a completed workflow instance supersedes its contract only on the pinned complete-step correction shape", false, "start a successor workflow")
}

// workflowCompleteStepCorrectionEvidenceCutoff returns the domain-event
// sequence of the latest supersession in contractVersion's ancestry that
// produced a contract declaring the reserved complete-step correction route,
// and zero when the ancestry declares none. The cutoff persists across later
// ordinary successors: once a correction cut the predecessor's verdicts and
// evidence off, no descendant reads them again — only fresh post-cutoff
// evidence and verdicts re-establish a contract. Evidence bound at or before
// the cutoff carried the disproved premise, so the evidence-requirement
// surfaces accept only bindings recorded after it.
func workflowCompleteStepCorrectionEvidenceCutoff(ctx context.Context, q queryer, workID string, contractVersion int64) (int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT contract_version,route_conventions FROM workflow_contracts WHERE work_id=? AND contract_version<=? ORDER BY contract_version DESC`, workID, contractVersion)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot read the workflow contract ancestry", true, "retry once the workflow projection is readable", err)
	}
	routeVersion := int64(0)
	for rows.Next() {
		var version int64
		var routesJSON string
		if err := rows.Scan(&version, &routesJSON); err != nil {
			rows.Close()
			return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan the workflow contract ancestry", true, "retry once the workflow projection is readable", err)
		}
		var routes []string
		if json.Unmarshal([]byte(routesJSON), &routes) == nil && containsString(routes, workflowCompleteStepCorrectionRoute) {
			routeVersion = version
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot enumerate the workflow contract ancestry", true, "retry once the workflow projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot close the workflow contract ancestry", true, "retry once the workflow projection is readable", err)
	}
	if routeVersion == 0 {
		return 0, nil
	}
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.new_contract_version')=?`, string(SubjectWorkItem), workID, WorkflowContractSuperseded, routeVersion).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot read the complete-step correction cutoff", true, "retry once the workflow projection is readable", err)
	}
	if seq == 0 {
		return 0, newFailure(KindInvariantViolation, "workflow_correction", "complete-step correction contract has no supersession event", false, "rebuild the workflow contract projection")
	}
	return seq, nil
}

// WorkflowCorrectionContext is the bounded correction record projected for a
// work pin and for the next worker packet.
type WorkflowCorrectionContext struct {
	Disposition        string   `json:"disposition"`
	AttemptCount       int64    `json:"attempt_count"`
	AttemptLimit       int64    `json:"attempt_limit"`
	Escalated          bool     `json:"escalated"`
	PredicateIDs       []string `json:"predicate_ids"`
	EvidenceRefs       []string `json:"evidence_refs"`
	Diagnosis          string   `json:"diagnosis,omitempty"`
	Strategy           string   `json:"strategy,omitempty"`
	FailureKind        string   `json:"failure_kind,omitempty"`
	FailureDetail      string   `json:"failure_detail,omitempty"`
	FailedAttemptID    string   `json:"failed_attempt_id,omitempty"`
	FailedAttemptEpoch int64    `json:"failed_attempt_epoch,omitempty"`
}

// WorkflowRetryApprovalBinding is the durable identity that an operator
// approval must bind before a failed worker attempt can run again. It also
// names the one correction an escalated wall admission may consume. An
// escalated verification correction carries no failed attempt, so its wall
// binds the correction's attempt count instead.
type WorkflowRetryApprovalBinding struct {
	FailedAttemptID    string
	FailedAttemptEpoch int64
	ContractVersion    int64
	CorrectionAttempts int64
}

// WorkflowFailedWorkerRetryBinding reads the current failed worker attempt
// and active contract without opening a nested store connection. An escalated
// rejected result carries the same failed attempt identity, so its wall
// admission binds it exactly like a failed disposition. An escalated
// verification correction binds its attempt count, because the request that
// closed the correction carried no worker attempt. A rejected result below
// the limit keeps its ordinary approval-free correction dispatch. When no
// correction record stands behind the current step and the same-step failed
// count reaches the limit, the binding keys to the latest counted failed
// attempt at the step, so the same-step wall keeps its operator-approvable
// escape. A nil registry means the builtin registry.
func WorkflowFailedWorkerRetryBinding(ctx context.Context, s *Store, registry DefinitionRegistry, workID string) (*WorkflowRetryApprovalBinding, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_correction", "store is not open", false, "open the authority database")
	}
	// The admission fold runs in the caller's transaction, so the pool-backed
	// read opens its own short read transaction around the same single
	// implementation (the store connection invariant).
	readTx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot open the binding read transaction", true, "retry once the store is readable", err)
	}
	defer func() { _ = readTx.Rollback() }()
	return workflowFailedWorkerRetryBinding(ctx, readTx, registry, workID)
}

// WorkflowFailedWorkerRetryBindingTx is the transaction-scoped form used by
// the approval and dispatch boundary. It rereads the binding before approval
// consumption, so a stale approval cannot authorize a different attempt.
// A nil registry means the builtin registry.
func WorkflowFailedWorkerRetryBindingTx(ctx context.Context, transaction *Transaction, registry DefinitionRegistry, workID string) (*WorkflowRetryApprovalBinding, error) {
	q, err := transactionSQL(transaction, "workflow_correction")
	if err != nil {
		return nil, err
	}
	return workflowFailedWorkerRetryBinding(ctx, q, registry, workID)
}

func workflowFailedWorkerRetryBinding(ctx context.Context, q queryer, registry DefinitionRegistry, workID string) (*WorkflowRetryApprovalBinding, error) {
	var stepID, pinRef, pinDigest string
	var pinVersion int64
	if err := q.QueryRowContext(ctx, `SELECT current_step,COALESCE(definition_ref,''),COALESCE(definition_version,0),COALESCE(definition_digest,'') FROM workflow_instances WHERE work_id=?`, workID).Scan(&stepID, &pinRef, &pinVersion, &pinDigest); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read the current workflow step", true, "retry once the workflow projection is readable", err)
	}
	pin := WorkflowDefinitionPin{Ref: pinRef, Version: pinVersion, Digest: pinDigest}
	definition, err := workflowPinnedDefinitionForBinding(registry, pin)
	if err != nil {
		return nil, err
	}
	// The wall and escalation admission is the shared derivation: the fold
	// carries the correction escalation and the same-step wall count, and the
	// pure workflowAdmit decides whether a dispatch stands behind the
	// operator's approval. The identity reads below bind that admission to
	// the attempt the approval must name.
	state, _, err := loadWorkflowAdmissionStateTx(ctx, q, workID, definition, stepID, "workflow_correction")
	if err != nil {
		return nil, err
	}
	decision := workflowAdmit(definition, state, "dispatch_worker")
	if !decision.Admitted && !decision.ApprovalRequired {
		return nil, nil
	}
	if decision.ApprovalRequired && state.FailedWorkerRetry != nil {
		return state.FailedWorkerRetry, nil
	}
	correction, err := workflowCorrectionContextForDispatch(ctx, q, workID, stepID, "")
	if err != nil {
		return nil, err
	}
	if correction == nil {
		// No correction record stands behind the current refusal — a later
		// dispatch whose attempt materialized consumed the record — but the
		// same-step wall can still refuse: CD-0164 keeps that wall operator
		// approvable, so when the folded admission arms the wall the binding
		// keys to the latest counted failed attempt instead of a consumed
		// correction.
		if !decision.ApprovalRequired {
			return nil, nil
		}
		return workflowSameStepWallRetryBinding(ctx, q, definition, workID, stepID)
	}
	verification := correction.Escalated && correction.Disposition == "verification" && correction.FailedAttemptID == ""
	switch {
	case correction.Disposition == "failed":
	case verification:
	case correction.Escalated && correction.Disposition == "rejected" && correction.FailedAttemptID != "":
	default:
		// The record behind the current refusal admits no approval-free
		// dispatch, but the same-step wall can still refuse: CD-0164 keeps
		// that wall operator approvable, so when the folded admission arms
		// the wall the binding keys to the latest counted failed attempt
		// instead of this correction.
		if !decision.ApprovalRequired {
			return nil, nil
		}
		return workflowSameStepWallRetryBinding(ctx, q, definition, workID, stepID)
	}
	contractVersion, err := latestWorkflowContractVersion(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	binding := &WorkflowRetryApprovalBinding{
		FailedAttemptID: correction.FailedAttemptID, FailedAttemptEpoch: correction.FailedAttemptEpoch,
		ContractVersion: contractVersion,
	}
	if verification {
		binding.CorrectionAttempts = correction.AttemptCount
	}
	return binding, nil
}

// A terminal authorization needs exact retry approval even when its worker
// supplied no admissible dispatch evidence or its failure has no disposition.
// The latest authorization at this step owns the identity; an older failure
// cannot replace a newer live or completed attempt. Step entry and acceptance
// reset the window through the same anchor the failed-attempt wall reads.
func workflowCurrentFailedWorkerRetryBinding(ctx context.Context, q queryer, definition WorkflowDefinition, workID, stepID string) (*WorkflowRetryApprovalBinding, error) {
	anchor, err := workflowSameStepWindowAnchor(ctx, q, definition, workID, "workflow_correction", 0)
	if err != nil {
		return nil, err
	}
	var attemptID, lifecycle string
	var epoch int64
	err = q.QueryRowContext(ctx, `SELECT a.attempt_id,a.lifecycle_state,json_extract(c.payload,'$.attempt_epoch') FROM domain_events c JOIN worker_attempts a ON a.work_id=c.subject_id AND a.attempt_id=json_extract(c.payload,'$.worker_attempt_id') WHERE c.subject_type=? AND c.subject_id=? AND c.kind=? AND json_extract(c.payload,'$.action_id')='dispatch_worker' AND json_extract(c.payload,'$.step_id')=? AND c.seq>? ORDER BY c.seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, stepID, anchor).Scan(&attemptID, &lifecycle, &epoch)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot inspect the latest authorized worker attempt", true, "retry once the worker authorization is readable", err)
	}
	if lifecycle != "failed" {
		return nil, nil
	}
	contractVersion, err := latestWorkflowContractVersion(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	return &WorkflowRetryApprovalBinding{FailedAttemptID: attemptID, FailedAttemptEpoch: epoch, ContractVersion: contractVersion}, nil
}

// workflowSameStepWallRetryBinding binds the same-step wall's approval when
// no correction record stands behind it. The caller's folded admission owns
// the arming decision; this read names the durable identity the approval
// must bind — the latest counted failed attempt's id plus the attempt epoch
// its dispatch completion recorded — and the active contract version. Below
// the limit, or with no counted failed attempt, the wall is not armed and no
// binding exists.
func workflowSameStepWallRetryBinding(ctx context.Context, q queryer, definition WorkflowDefinition, workID, currentStep string) (*WorkflowRetryApprovalBinding, error) {
	count, attemptID, attemptEpoch, err := workflowSameStepWallState(ctx, q, definition, workID, currentStep, "workflow_correction")
	if err != nil {
		return nil, err
	}
	if count < workflowCorrectionAttemptLimit || attemptID == "" {
		return nil, nil
	}
	contractVersion, err := latestWorkflowContractVersion(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	return &WorkflowRetryApprovalBinding{
		FailedAttemptID: attemptID, FailedAttemptEpoch: attemptEpoch, ContractVersion: contractVersion,
	}, nil
}

// workflowPinnedDefinitionForBinding resolves the pinned workflow definition
// the same-step window anchor needs. A nil registry means the builtin
// registry.
func workflowPinnedDefinitionForBinding(registry DefinitionRegistry, pin WorkflowDefinitionPin) (WorkflowDefinition, error) {
	if registry == nil {
		registry = BuiltinWorkflowRegistry()
	}
	entry, err := VerifyWorkflowDefinitionPin(registry, pin)
	if err != nil {
		return WorkflowDefinition{}, err
	}
	return entry.Definition, nil
}

// validateFailedWorkerRetryIdentity refuses a retry that reuses the failed
// worker identity. The dispatch fold mints the next step epoch separately.
func validateFailedWorkerRetryIdentity(ctx context.Context, q queryer, workID, currentStep string, payload json.RawMessage) error {
	correction, err := workflowCorrectionContextForDispatch(ctx, q, workID, currentStep, "")
	if err != nil {
		return err
	}
	if correction == nil || correction.FailedAttemptID == "" {
		return nil
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	attemptID := workflowFieldStringDefault(fields, "attempt_id", "")
	if attemptID == "" || attemptID == correction.FailedAttemptID {
		return newFailure(KindStaleAttempt, "workflow_action", "worker retry must use a fresh attempt identity", false, "mint a new fenced worker attempt")
	}
	return nil
}

func workflowCorrectionActionDefinition() WorkflowActionDefinition {
	action := currentActionDefinition("reject_worker_result", true)
	action.RequiredCapability = "work_transition"
	return action
}

// validateRejectCorrectionPinValues refuses a reject whose correction values
// the closed response schema cannot represent. The request-level evidence
// refs are not payload fields, so the payload declaration alone cannot cover
// every value the correction pin will carry; the apply boundary checks the
// full set here before any effect is recorded.
func validateRejectCorrectionPinValues(payload json.RawMessage, evidenceRefs []string) error {
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	fault := workflowCorrectionSchemaValuesFault(workflowFieldStrings(fields, "predicate_ids"), workflowFieldStrings(fields, "evidence_refs"), evidenceRefs)
	if fault == "" {
		return nil
	}
	return newFailure(KindInvalidPayload, "workflow_action", "reject_worker_result carries correction values the closed response schema cannot represent: "+fault, false, "supply correction values the closed work_pin response schema accepts")
}

func workflowCorrectionRequestActionDefinition() WorkflowActionDefinition {
	action := currentActionDefinition("request_correction", true)
	action.RequiredCapability = "work_transition"
	return action
}

// workflowCorrectionTargetStep returns the nearest external-effect step before
// the current verification or completion step. The lookup uses the pinned
// definition, so historical workflow versions keep their own route.
func workflowCorrectionTargetStep(definition WorkflowDefinition, currentStep string) string {
	preferred := ""
	fallback := ""
	preferredAction := "start_execution"
	if definition.WorkKind == WorkKindBreakFix {
		preferredAction = "start_repair"
	}
	for _, candidate := range definition.StepGraph.Steps {
		if candidate.Kind != WorkflowStepExternalEffect || !workflowStepFollows(definition, candidate.ID, currentStep) {
			continue
		}
		if fallback == "" {
			fallback = candidate.ID
		}
		if containsString(candidate.Actions, preferredAction) {
			preferred = candidate.ID
		}
	}
	if preferred != "" {
		return preferred
	}
	return fallback
}

func workflowCorrectionWorkflow(definition WorkflowDefinition) bool {
	return definition.WorkKind == WorkKindImplementation || definition.WorkKind == WorkKindBreakFix
}

// workflowAcceptedWorkerDelivery requires both a completed worker attempt and
// its folded accept action before a verdict can request correction. Either
// accept action binds the delivery: accept_worker_result accepts a delivered
// result, and accept_worker_evidence binds the evidence a review dispatch
// delivered at its confirmation step.
func workflowAcceptedWorkerDelivery(ctx context.Context, q queryer, workID string, throughSeq int64, subject string) (int64, bool, error) {
	var dispatchSeq int64
	var attemptID string
	if err := q.QueryRowContext(ctx, `SELECT d.seq,json_extract(d.payload,'$.attempt_id') FROM domain_events d WHERE d.subject_type=? AND d.subject_id=? AND d.kind=? AND d.seq<=? ORDER BY d.seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkerDispatched, throughSeq).Scan(&dispatchSeq, &attemptID); err != nil {
		if err == sql.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, wrapFailure(KindUnavailable, subject, "cannot inspect worker delivery", true, "retry once the worker delivery projection is readable", err)
	}
	var accepted int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempts a JOIN domain_events accepted ON accepted.subject_type=? AND accepted.subject_id=a.work_id AND accepted.kind=? AND accepted.seq>? AND accepted.seq<=? AND json_extract(accepted.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence') AND json_extract(accepted.payload,'$.worker_attempt_id')=a.attempt_id WHERE a.work_id=? AND a.attempt_id=? AND a.lifecycle_state='completed')`, string(SubjectWorkItem), WorkflowActionCompleted, dispatchSeq, throughSeq, workID, attemptID).Scan(&accepted); err != nil {
		return 0, false, wrapFailure(KindUnavailable, subject, "cannot inspect accepted worker delivery", true, "retry once the worker delivery projection is readable", err)
	}
	return dispatchSeq, accepted != 0, nil
}

// workflowLatestComparableHealthySequence returns the latest sequence before
// beforeSeq at which every active predicate had a comparable healthy verdict.
// A single ok verdict cannot reset a sequence when another predicate remains
// unhealthy or has no verdict.
func workflowLatestComparableHealthySequence(ctx context.Context, q queryer, workID string, contractVersion, beforeSeq int64) (int64, error) {
	rows, err := q.QueryContext(ctx, `SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=?`, workID, contractVersion)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot read active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	predicates := make(map[string]bool)
	for rows.Next() {
		var predicateID string
		if err := rows.Scan(&predicateID); err != nil {
			rows.Close()
			return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan active workflow predicate", true, "retry once the workflow contract is readable", err)
		}
		predicates[predicateID] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot enumerate active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	if err := rows.Close(); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot close active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	if len(predicates) == 0 {
		return 0, nil
	}
	history, err := workflowContractPredicateHistory(ctx, q, workID, contractVersion)
	if err != nil {
		return 0, err
	}
	pins, err := workflowContractDefinitionPins(ctx, q, workID, contractVersion)
	if err != nil {
		return 0, err
	}
	type verdictAtSequence struct {
		seq     int64
		verdict workflowVerdictRecordedPayload
	}
	latest := make(map[string]verdictAtSequence, len(predicates))
	verdictRows, err := q.QueryContext(ctx, `SELECT seq,payload,payload_version FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND seq<? ORDER BY seq DESC`, workID, WorkflowVerdictRecorded, beforeSeq)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot read workflow verdict history", true, "retry once the workflow verdict projection is readable", err)
	}
	defer verdictRows.Close()
	for verdictRows.Next() {
		var seq int64
		var raw []byte
		var payloadVersion int
		if err := verdictRows.Scan(&seq, &raw, &payloadVersion); err != nil {
			return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan workflow verdict history", true, "retry once the workflow verdict projection is readable", err)
		}
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal(raw, &verdict); err != nil {
			return 0, newFailure(KindInvariantViolation, "workflow_correction", "workflow verdict payload is malformed", false, "rebuild workflow projections from the event log")
		}
		if payloadVersion == 1 {
			verdict.PredicateID = "predicate:primary"
		}
		if !predicates[verdict.PredicateID] || verdict.ContractVersion <= 0 || verdict.ContractVersion > contractVersion {
			continue
		}
		if verdict.ContractVersion < contractVersion && (!workflowPredicateHistoryCompatible(history, verdict.ContractVersion, contractVersion, verdict.PredicateID, verdict) || !workflowContractDefinitionPinsCompatible(pins, verdict.ContractVersion, contractVersion)) {
			continue
		}
		latest[verdict.PredicateID] = verdictAtSequence{seq: seq, verdict: verdict}
		healthy := len(latest) == len(predicates)
		if healthy {
			var healthySeq int64
			for predicateID := range predicates {
				candidate := latest[predicateID]
				if candidate.verdict.VerdictKind != "ok" || candidate.verdict.IncomparableWithApproved {
					healthy = false
					break
				}
				if candidate.seq > healthySeq {
					healthySeq = candidate.seq
				}
			}
			if healthy {
				return healthySeq, nil
			}
		}
	}
	if err := verdictRows.Err(); err != nil {
		return 0, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan workflow verdict history", true, "retry once the workflow verdict projection is readable", err)
	}
	return 0, nil
}

// workflowCorrectionHealthyBaseline returns the sequence a correction request
// window opens at: the later of the latest comparable-healthy verdict set
// under contractVersion (CD-0143 D3's sequence-ending state) and the
// contract's complete-step supersession cutoff. An ok verdict that is
// incomparable with the approved result, or an ok verdict beside another
// predicate's stale verdict, is not a comparable-healthy set and ends neither
// health nor the counted window, so every counting surface derives its
// baseline here.
func workflowCorrectionHealthyBaseline(ctx context.Context, q queryer, workID string, contractVersion, beforeSeq int64) (int64, error) {
	lastHealthySeq, err := workflowLatestComparableHealthySequence(ctx, q, workID, contractVersion, beforeSeq)
	if err != nil {
		return 0, err
	}
	cutoff, err := workflowCompleteStepCorrectionEvidenceCutoff(ctx, q, workID, contractVersion)
	if err != nil {
		return 0, err
	}
	if lastHealthySeq < cutoff {
		lastHealthySeq = cutoff
	}
	return lastHealthySeq, nil
}

// workflowCorrectionActiveHealthyBaseline resolves the active contract and
// returns its healthy baseline before beforeSeq. A work item without an
// active contract has no healthy verdict set to open a window from, so the
// baseline opens at the log start and every recorded request counts.
func workflowCorrectionActiveHealthyBaseline(ctx context.Context, q queryer, workID string, beforeSeq int64, subject string) (int64, error) {
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return workflowCorrectionHealthyBaseline(ctx, q, workID, contractVersion, beforeSeq)
}

func workflowCorrectionAttemptCount(ctx context.Context, q queryer, workID string, seq int64, subject string) (int64, error) {
	var count int64
	// Authorization already creates an attempt. Dispatch evidence corroborates
	// that same identity, so count it once rather than requiring or doubling it.
	if err := q.QueryRowContext(ctx, `SELECT count(DISTINCT a.attempt_id) FROM worker_attempts a JOIN domain_events opening ON opening.subject_type=? AND opening.subject_id=a.work_id AND ((opening.kind=? AND json_extract(opening.payload,'$.attempt_id')=a.attempt_id) OR (opening.kind=? AND json_extract(opening.payload,'$.action_id')='dispatch_worker' AND json_extract(opening.payload,'$.worker_attempt_id')=a.attempt_id)) WHERE a.work_id=? AND opening.seq<=? AND opening.seq>COALESCE((SELECT MAX(accepted.seq) FROM domain_events accepted WHERE accepted.subject_type=opening.subject_type AND accepted.subject_id=opening.subject_id AND accepted.kind=? AND accepted.seq<? AND json_extract(accepted.payload,'$.action_id')='accept_worker_result'),0)`, string(SubjectWorkItem), WorkerDispatched, WorkflowActionCompleted, workID, seq, WorkflowActionCompleted, seq).Scan(&count); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot count correction attempts", true, "retry once the worker attempt projection is readable", err)
	}
	return count, nil
}

// workflowSameStepFailedAttemptCount counts the failed worker attempts whose
// dispatch completed at the current step after the window anchor. The anchor
// and the counted population have one owner: workflowSameStepWallState.
func workflowSameStepFailedAttemptCount(ctx context.Context, q queryer, definition WorkflowDefinition, workID, currentStep, subject string) (int64, error) {
	count, _, _, err := workflowSameStepWallState(ctx, q, definition, workID, currentStep, subject)
	return count, err
}

// workflowSameStepWallState returns the same-step failed attempt count and
// the latest counted failed attempt's identity. The window anchor is the
// later of the last accepted worker result (the CD-0164 D2 reset) and the
// latest step entry. An entry is a completion whose fold moves the instance
// between steps: an advancing action's completion or a correction request's
// return. A supersede_contract completion holds the step except at the pinned
// complete step, and CD-0164 D3 keeps counted dispatches across an
// operator-approved supersession, so only the complete-step correction shape
// opens the window. Dispatch actions and fresh fenced starts do not reset the
// window, so a coordinator that re-dispatches a failed lane without a
// correction record climbs the same wall a recorded correction climbs.
func workflowSameStepWallState(ctx context.Context, q queryer, definition WorkflowDefinition, workID, currentStep, subject string) (int64, string, int64, error) {
	anchor, err := workflowSameStepWindowAnchor(ctx, q, definition, workID, subject, 0)
	if err != nil {
		return 0, "", 0, err
	}
	var count int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts a JOIN domain_events dc ON dc.subject_type=? AND dc.subject_id=a.work_id AND dc.kind=? AND json_extract(dc.payload,'$.action_id')='dispatch_worker' AND json_extract(dc.payload,'$.worker_attempt_id')=a.attempt_id AND json_extract(dc.payload,'$.step_id')=? WHERE a.work_id=? AND a.lifecycle_state='failed' AND dc.seq>?`, string(SubjectWorkItem), WorkflowActionCompleted, currentStep, workID, anchor).Scan(&count); err != nil {
		return 0, "", 0, wrapFailure(KindUnavailable, subject, "cannot count same-step failed attempts", true, "retry once the worker attempt projection is readable", err)
	}
	var attemptID string
	var attemptEpoch int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(json_extract(dc.payload,'$.worker_attempt_id'),''),COALESCE(json_extract(dc.payload,'$.attempt_epoch'),0) FROM worker_attempts a JOIN domain_events dc ON dc.subject_type=? AND dc.subject_id=a.work_id AND dc.kind=? AND json_extract(dc.payload,'$.action_id')='dispatch_worker' AND json_extract(dc.payload,'$.worker_attempt_id')=a.attempt_id AND json_extract(dc.payload,'$.step_id')=? WHERE a.work_id=? AND a.lifecycle_state='failed' AND dc.seq>? ORDER BY dc.seq DESC LIMIT 1`, string(SubjectWorkItem), WorkflowActionCompleted, currentStep, workID, anchor).Scan(&attemptID, &attemptEpoch); err != nil {
		if err == sql.ErrNoRows {
			return count, "", 0, nil
		}
		return 0, "", 0, wrapFailure(KindUnavailable, subject, "cannot read the latest same-step failed attempt", true, "retry once the worker attempt projection is readable", err)
	}
	return count, attemptID, attemptEpoch, nil
}

// workflowSameStepWindowAnchor returns the event sequence the same-step
// window opens after: the later of the last accepted worker result and the
// latest step entry. It is the one owner of the anchor query, so the wall's
// refusal count and its approval binding read one window.
// excludeSeq names the fold's own in-flight completion, which must not count
// against its admission; read surfaces pass zero so every settled event counts.
func workflowSameStepWindowAnchor(ctx context.Context, q queryer, definition WorkflowDefinition, workID, subject string, excludeSeq int64) (int64, error) {
	var acceptedSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result'`, string(SubjectWorkItem), workID, WorkflowActionCompleted).Scan(&acceptedSeq); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot inspect accepted worker results", true, "retry once the workflow projection is readable", err)
	}
	anchor := acceptedSeq
	entries := append(advancingWorkflowActions(definition), "request_correction")
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(entries)), ",")
	args := []any{string(SubjectWorkItem), workID, WorkflowActionCompleted}
	for _, action := range entries {
		args = append(args, action)
	}
	entryBound := ""
	if excludeSeq > 0 {
		entryBound = " AND seq<>?"
		args = append(args, excludeSeq)
	}
	rows, err := q.QueryContext(ctx, `SELECT seq,json_extract(payload,'$.action_id'),json_extract(payload,'$.step_id') FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id') IN (`+placeholders+`)`+entryBound, args...)
	if err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot inspect the workflow step's entries", true, "retry once the workflow event log is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var actionID, stepID string
		if err := rows.Scan(&seq, &actionID, &stepID); err != nil {
			return 0, wrapFailure(KindUnavailable, subject, "cannot scan the workflow step's entries", true, "retry once the workflow event log is readable", err)
		}
		if actionID == "supersede_contract" && !stepDeclaresAction(definition, stepID, "complete") {
			continue
		}
		if seq > anchor {
			anchor = seq
		}
	}
	if err := rows.Err(); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot scan the workflow step's entries", true, "retry once the workflow event log is readable", err)
	}
	return anchor, nil
}

// workflowSameStepWallFailure is the wall refusal's typed form, so the shared
// admission and the folding guard carry one refusal.
func workflowSameStepWallFailure(currentStep string, count int64) *Failure {
	return newFailure(KindApprovalRequired, "workflow_action",
		fmt.Sprintf("worker dispatch at step %s reached the three-failed-attempt limit: %d failed attempts dispatched at this step since the last accepted result or step entry", currentStep, count),
		false, "escalate the failed attempts to the operator")
}

func workflowCorrectionVerdictState(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (*workflowCorrectionVerdictPrerequisites, error) {
	if !workflowCorrectionWorkflow(definition) || workflowCorrectionTargetStep(definition, currentStep) == "" {
		return nil, nil
	}
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the active workflow contract", true, "retry once the workflow contract is readable", err)
	}
	verdicts, err := latestWorkflowVerdicts(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	nonOK := make([]workflowVerdictRecordedPayload, 0, len(verdicts))
	for _, verdict := range verdicts {
		if verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved {
			nonOK = append(nonOK, verdict)
		}
	}
	if len(nonOK) == 0 {
		return nil, nil
	}
	var verdictSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=?`, string(SubjectWorkItem), workID, WorkflowVerdictRecorded).Scan(&verdictSeq); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the latest workflow verdict sequence", true, "retry once the workflow verdict projection is readable", err)
	}
	acceptedDispatchSeq, accepted, acceptedErr := workflowAcceptedWorkerDelivery(ctx, q, workID, verdictSeq, subject)
	if acceptedErr != nil {
		return nil, acceptedErr
	}
	return &workflowCorrectionVerdictPrerequisites{
		nonOK: nonOK, contractVersion: contractVersion, verdictSeq: verdictSeq,
		acceptedDispatchSeq: acceptedDispatchSeq, accepted: accepted,
	}, nil
}

// workflowCorrectionVerdictPrerequisites is the verdict-side prerequisite
// state behind a correction request: the active contract's latest non-ok
// verdicts, the verdict sequence, and whether the worker delivery behind that
// sequence was accepted. The accepted-completed-delivery route (CD-0143 D1)
// and the checkpoint failed-review route read one state, so both surfaces
// answer from one derivation.
type workflowCorrectionVerdictPrerequisites struct {
	nonOK               []workflowVerdictRecordedPayload
	contractVersion     int64
	verdictSeq          int64
	acceptedDispatchSeq int64
	accepted            bool
}

// workflowVerdictCorrectionFromState closes the accepted-completed-delivery
// route (CD-0143 D1) on the derived verdict state: the route admits only a
// correction whose every current non-ok verdict postdates the accepted
// delivery and any prior correction.
func workflowVerdictCorrectionFromState(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, state *workflowCorrectionVerdictPrerequisites) (*WorkflowCorrectionContext, error) {
	if !state.accepted {
		return nil, nil
	}
	verdicts := state.nonOK
	seq := state.verdictSeq
	acceptedDispatchSeq := state.acceptedDispatchSeq
	contractVersion := state.contractVersion
	lastHealthySeq, healthyErr := workflowCorrectionHealthyBaseline(ctx, q, workID, contractVersion, seq)
	if healthyErr != nil {
		return nil, healthyErr
	}
	var latestCorrectionSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' AND seq>? AND seq<?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, lastHealthySeq, seq).Scan(&latestCorrectionSeq); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot inspect prior correction requests", true, "retry once the workflow correction projection is readable", err)
	}
	if latestCorrectionSeq >= seq {
		return nil, nil
	}
	verdictSequences, sequenceErr := workflowLatestVerdictSequences(ctx, q, workID, contractVersion, verdicts)
	if sequenceErr != nil {
		return nil, sequenceErr
	}
	if acceptedDispatchSeq <= latestCorrectionSeq {
		return nil, nil
	}
	for _, verdict := range verdicts {
		if verdictSequences[verdict.PredicateID] <= latestCorrectionSeq || verdictSequences[verdict.PredicateID] <= acceptedDispatchSeq {
			return nil, nil
		}
	}
	var priorCorrections int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' AND seq>? AND seq<?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, lastHealthySeq, seq).Scan(&priorCorrections); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot count correction requests", true, "retry once the workflow correction projection is readable", err)
	}
	attempts := priorCorrections + 1
	predicates := make([]string, 0, len(verdicts))
	evidence := make([]string, 0)
	for _, verdict := range verdicts {
		predicates = append(predicates, verdict.PredicateID)
		for _, ref := range verdict.EvaluationEvidence {
			if !contains(evidence, ref) {
				evidence = append(evidence, ref)
			}
		}
	}
	return &WorkflowCorrectionContext{
		Disposition: "verification", AttemptCount: attempts, AttemptLimit: workflowCorrectionAttemptLimit, Escalated: attempts > workflowCorrectionAttemptLimit,
		PredicateIDs: nonNilStrings(predicates), EvidenceRefs: nonNilStrings(evidence),
		Diagnosis: "latest verification verdict is not healthy", Strategy: fmt.Sprintf("repeat the external-effect step %q", workflowCorrectionTargetStep(definition, currentStep)),
	}, nil
}

func workflowLatestVerdictSequences(ctx context.Context, q queryer, workID string, contractVersion int64, wanted []workflowVerdictRecordedPayload) (map[string]int64, error) {
	history, err := workflowContractPredicateHistory(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	pins, err := workflowContractDefinitionPins(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	wantedIDs := make(map[string]bool, len(wanted))
	for _, verdict := range wanted {
		wantedIDs[verdict.PredicateID] = true
	}
	sequences := make(map[string]int64, len(wanted))
	seen := make(map[string]bool)
	foundWanted := 0
	rows, err := q.QueryContext(ctx, `SELECT seq,payload,payload_version FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC`, workID, WorkflowVerdictRecorded)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read workflow verdict sequences", true, "retry once the workflow verdict projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var raw []byte
		var payloadVersion int
		if err := rows.Scan(&seq, &raw, &payloadVersion); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan workflow verdict sequence", true, "retry once the workflow verdict projection is readable", err)
		}
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal(raw, &verdict); err != nil {
			return nil, newFailure(KindInvariantViolation, "workflow_correction", "workflow verdict payload is malformed", false, "rebuild workflow projections from the event log")
		}
		if payloadVersion == 1 {
			verdict.PredicateID = "predicate:primary"
		}
		if verdict.ContractVersion <= 0 || verdict.ContractVersion > contractVersion || seen[verdict.PredicateID] {
			continue
		}
		if verdict.ContractVersion < contractVersion && (!workflowPredicateHistoryCompatible(history, verdict.ContractVersion, contractVersion, verdict.PredicateID, verdict) || !workflowContractDefinitionPinsCompatible(pins, verdict.ContractVersion, contractVersion)) {
			continue
		}
		seen[verdict.PredicateID] = true
		if wantedIDs[verdict.PredicateID] {
			sequences[verdict.PredicateID] = seq
			foundWanted++
		}
		if foundWanted == len(wantedIDs) {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot scan workflow verdict sequences", true, "retry once the workflow verdict projection is readable", err)
	}
	return sequences, nil
}

// workflowRefinementStepID returns the ID of the definition's refinement pass,
// the step that declares start_refine, or "" when the pinned shape carries no
// refinement step. The refinement pass is where a review result is accepted or
// rejected, so the post-rejection review gate reads its history.
func workflowRefinementStepID(definition WorkflowDefinition) string {
	for i := range definition.StepGraph.Steps {
		if stepDeclaresAction(definition, definition.StepGraph.Steps[i].ID, "start_refine") {
			return definition.StepGraph.Steps[i].ID
		}
	}
	return ""
}

// workflowPostRejectionReviewStep reports whether the current step is one
// whose advance carries an unreviewed repaired result toward delivery: the
// refinement step that enters the delivery gate, and the gate itself.
func workflowPostRejectionReviewStep(definition WorkflowDefinition, currentStep string) bool {
	return stepDeclaresAction(definition, currentStep, "start_refine") || workflowStepIsDeliveryGate(workflowStep(definition, currentStep))
}

// workflowReviewSettlesDebt is the single owner of the review-verdict half of
// the settlement rule: a fresh accepted review settles post-rejection review
// debt only when its typed verdict is ship, or absent for the pre-CD-0197
// reports that carry no verdict. A no_ship review binds its findings and
// leaves the debt outstanding, so refine does not advance to delivery and a
// parked delivery gate keeps admitting the evidence-bearing corrective return.
func workflowReviewSettlesDebt(verdict string) bool {
	return verdict == "" || verdict == "ship"
}

// workflowReviewSettlesDebtSQL is the SQL form of workflowReviewSettlesDebt
// over the attempt's worker.completed record: the review verdict is absent, or
// ship. The settlement query composes this fragment, so the verdict rule
// cannot drift between the settlement query and the guard that records the
// accept's admission decision on its completion (CD-0201 D3).
const workflowReviewSettlesDebtSQL = "COALESCE(json_extract(wc.payload,'$.review.verdict'),'') IN ('','ship')"

// workflowPostRejectionFrontier returns the seq frontier a settling review
// dispatch must postdate: the refinement step's latest failed review outcome,
// or the latest non-review dispatch or completion at the refinement step that
// follows it, whichever is later. A rejection and an accepted no_ship review
// both require a fresh settling review.
func workflowPostRejectionFrontier(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, subject string) (int64, error) {
	rejectSeq, _, err := workflowRefinementReviewFailure(ctx, q, workID, definition, subject)
	if err != nil || rejectSeq == 0 {
		return 0, err
	}
	frontier, err := workflowPostRejectionRepairFrontier(ctx, q, workID, workflowRefinementStepID(definition), rejectSeq, subject)
	if err != nil {
		return 0, err
	}
	if frontier == 0 {
		frontier = rejectSeq
	}
	return frontier, nil
}

// workflowPostRejectionRepairFrontier returns the latest event seq of any
// non-review worker dispatch or completion at the refinement step that
// follows the rejection, or 0 when none exists. Activity after a review's
// dispatch means the review never saw the result it would cover.
func workflowPostRejectionRepairFrontier(ctx context.Context, q queryer, workID, refineStep string, rejectSeq int64, subject string) (int64, error) {
	var frontier int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(e.seq),0) FROM domain_events e WHERE e.subject_type=? AND e.subject_id=? AND e.kind IN (?,?) AND e.seq>? AND EXISTS(SELECT 1 FROM domain_events d WHERE d.subject_type=e.subject_type AND d.subject_id=e.subject_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=json_extract(e.payload,'$.attempt_id') AND json_extract(d.payload,'$.capability_class')<>'review') AND EXISTS(SELECT 1 FROM domain_events a WHERE a.subject_type=e.subject_type AND a.subject_id=e.subject_id AND a.kind=? AND json_extract(a.payload,'$.action_id')='dispatch_worker' AND json_extract(a.payload,'$.step_id')=? AND json_extract(a.payload,'$.worker_attempt_id')=json_extract(e.payload,'$.attempt_id'))`,
		string(SubjectWorkItem), workID, WorkerDispatched, WorkerCompleted, rejectSeq, WorkerDispatched, WorkflowActionCompleted, refineStep).Scan(&frontier); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot read the refinement repair history", true, "retry once the worker dispatch projection is readable", err)
	}
	return frontier, nil
}

// workflowRefinementReviewFailure reads the latest refinement disposition that
// requires a fresh settling review. Accepting a no_ship report binds findings,
// not permission to ship; it carries the same review obligation as a rejected
// result without changing the recorded worker disposition.
func workflowRefinementReviewFailure(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, subject string) (int64, []byte, error) {
	if !workflowCorrectionWorkflow(definition) {
		return 0, nil, nil
	}
	refineStep := workflowRefinementStepID(definition)
	if refineStep == "" {
		return 0, nil, nil
	}
	var seq int64
	var raw []byte
	if err := q.QueryRowContext(ctx, `SELECT acc.seq,acc.payload FROM domain_events acc
		WHERE acc.subject_type=? AND acc.subject_id=? AND acc.kind=? AND json_extract(acc.payload,'$.step_id')=?
		AND (json_extract(acc.payload,'$.action_id')='reject_worker_result'
		OR (json_extract(acc.payload,'$.action_id')='accept_worker_result' AND EXISTS(
			SELECT 1 FROM domain_events wd JOIN domain_events wc
			ON wc.subject_type=wd.subject_type AND wc.subject_id=wd.subject_id AND wc.kind=?
			AND json_extract(wc.payload,'$.attempt_id')=json_extract(wd.payload,'$.attempt_id')
			AND wc.seq>wd.seq AND wc.seq<acc.seq AND json_extract(wc.payload,'$.review.verdict')='no_ship'
			WHERE wd.subject_type=acc.subject_type AND wd.subject_id=acc.subject_id AND wd.kind=?
			AND json_extract(wd.payload,'$.attempt_id')=json_extract(acc.payload,'$.worker_attempt_id')
			AND json_extract(wd.payload,'$.capability_class')='review')))
		ORDER BY acc.seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowActionCompleted, refineStep, WorkerCompleted, WorkerDispatched).Scan(&seq, &raw); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil, nil
		}
		return 0, nil, wrapFailure(KindUnavailable, subject, "cannot read the refinement review history", true, "retry once the workflow correction projection is readable", err)
	}
	return seq, raw, nil
}

// workflowStaleReviewAcceptTx reports whether one accept names a completed
// review that a newer completed review has superseded. The newest completed
// review stays acceptable whatever the folded state names; an older one
// cannot bind, because the newer evidence supersedes its findings
// (CD-0206 D3).
func workflowStaleReviewAcceptTx(ctx context.Context, q queryer, workID, attemptID, subject string) (bool, error) {
	var stale int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM domain_events wc
		JOIN domain_events wd ON wd.subject_type=wc.subject_type AND wd.subject_id=wc.subject_id AND wd.kind=?
			AND json_extract(wd.payload,'$.attempt_id')=json_extract(wc.payload,'$.attempt_id')
			AND json_extract(wd.payload,'$.capability_class')='review'
		WHERE wc.subject_type=? AND wc.subject_id=? AND wc.kind=? AND json_extract(wc.payload,'$.attempt_id')=?
		AND EXISTS(SELECT 1 FROM domain_events wc2
			JOIN domain_events wd2 ON wd2.subject_type=wc2.subject_type AND wd2.subject_id=wc2.subject_id AND wd2.kind=?
				AND json_extract(wd2.payload,'$.attempt_id')=json_extract(wc2.payload,'$.attempt_id')
				AND json_extract(wd2.payload,'$.capability_class')='review'
			WHERE wc2.subject_type=wc.subject_type AND wc2.subject_id=wc.subject_id AND wc2.kind=? AND wc2.seq>wc.seq))`,
		WorkerDispatched, string(SubjectWorkItem), workID, WorkerCompleted, attemptID, WorkerDispatched, WorkerCompleted).Scan(&stale)
	if err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot read the completed review history", true, "retry once the workflow projection is readable", err)
	}
	return stale == 1, nil
}

// workflowPostRejectionReviewOutstanding reports a refinement history where a
// rejected result or accepted no_ship review has no fresh settling review. A
// review is an accepted worker attempt dispatched on the review capability
// class after the debt's frontier; a repair attempt accepted after the
// rejection does not cover it, so the repaired result reaches delivery
// unreviewed.
func workflowPostRejectionReviewOutstanding(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, subject string) (bool, error) {
	rejectSeq, _, err := workflowRefinementReviewFailure(ctx, q, workID, definition, subject)
	if err != nil || rejectSeq == 0 {
		return false, err
	}
	return workflowPostRejectionReviewMissing(ctx, q, workID, workflowRefinementStepID(definition), rejectSeq, subject)
}

// workflowPostRejectionReviewMissing reports whether no accepted review
// attempt covers the rejected refinement result. The query encodes the shared
// fresh-review dispatch-order rule — the accepted attempt must dispatch on
// the review capability class after the debt's frontier, so a repair
// completed after the review's dispatch reopens the debt — and the verdict
// rule workflowReviewSettlesDebt: only an accepted review whose verdict is
// ship or absent settles, so a no_ship review leaves the debt outstanding.
func workflowPostRejectionReviewMissing(ctx context.Context, q queryer, workID, refineStep string, rejectSeq int64, subject string) (bool, error) {
	frontier, err := workflowPostRejectionRepairFrontier(ctx, q, workID, refineStep, rejectSeq, subject)
	if err != nil {
		return false, err
	}
	if frontier == 0 {
		frontier = rejectSeq
	}
	var reviewed int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events acc
		JOIN domain_events disp ON disp.subject_type=acc.subject_type AND disp.subject_id=acc.subject_id AND disp.kind=? AND json_extract(disp.payload,'$.attempt_id')=json_extract(acc.payload,'$.worker_attempt_id') AND json_extract(disp.payload,'$.capability_class')=? AND disp.seq>?
		JOIN domain_events wc ON wc.subject_type=acc.subject_type AND wc.subject_id=acc.subject_id AND wc.kind=? AND json_extract(wc.payload,'$.attempt_id')=json_extract(acc.payload,'$.worker_attempt_id')
		WHERE acc.subject_type=? AND acc.subject_id=? AND acc.kind=? AND json_extract(acc.payload,'$.action_id')='accept_worker_result' AND json_extract(acc.payload,'$.step_id')=? AND acc.seq>? AND `+workflowReviewSettlesDebtSQL+`)`, WorkerDispatched, "review", frontier, WorkerCompleted, string(SubjectWorkItem), workID, WorkflowActionCompleted, refineStep, rejectSeq).Scan(&reviewed); err != nil {
		return false, wrapFailure(KindUnavailable, subject, "cannot read the post-rejection review history", true, "retry once the worker delivery projection is readable", err)
	}
	return reviewed == 0, nil
}

// workflowDeliveryGateCorrectionContext is the correction context of a
// workflow instance parked on a CD-0166 delivery gate whose refinement history
// carries an outstanding review after rejection or an accepted no_ship report.
// The context names the rejected result's predicates, or the active contract's
// predicates, so the evidence-bearing return names what the fresh review must
// cover. A gate without the outstanding review admits no
// correction: record_delivery stays the only route off an ordinary parked
// gate.
func workflowDeliveryGateCorrectionContext(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (*WorkflowCorrectionContext, error) {
	if !workflowCorrectionWorkflow(definition) || !workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		return nil, nil
	}
	// The gate's correction prerequisite is the shared derivation: the
	// tx-scoped loader folds the refinement history once, and the debt the
	// state carries is the same one the delivery guard and the work pin read,
	// so a no_ship review keeps the corrective return admitted here exactly
	// as it keeps record_delivery hidden. A pool-backed reader cannot answer
	// the fold, so the read opens its own short transaction around the same
	// single implementation — a non-transactional twin deliberately does not
	// exist.
	lawTx, isTx := q.(*sql.Tx)
	if !isTx {
		db, isDB := q.(*sql.DB)
		if !isDB {
			return nil, newFailure(KindUnavailable, subject, "workflow action admission folds in the caller's transaction", false, "run the admission fold inside the mutation transaction")
		}
		readTx, beginErr := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if beginErr != nil {
			return nil, wrapFailure(KindUnavailable, subject, "cannot open the read transaction", true, "retry once the store is readable", beginErr)
		}
		defer func() {
			_ = readTx.Rollback()
		}()
		lawTx = readTx
	}
	// The fold and its continuation read on the transaction's connection: on
	// the pool-backed queryer the continuation's queries would park behind
	// the read transaction this wrapper holds (the store connection
	// invariant).
	state, _, err := loadWorkflowAdmissionStateTx(ctx, lawTx, workID, definition, currentStep, subject)
	if err != nil {
		return nil, err
	}
	return workflowDeliveryGateCorrectionContextFolded(ctx, lawTx, workID, definition, currentStep, subject, state)
}

// workflowDeliveryGateCorrectionContextFolded is the gate's corrective return
// over an admission state the caller already folded. The shared loader passes
// its own state here, so the gate derivation reads the debt it carries instead
// of re-entering the loader.
func workflowDeliveryGateCorrectionContextFolded(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, state WorkflowAdmissionState) (*WorkflowCorrectionContext, error) {
	if state.ReviewDebt != ReviewDebtOutstanding {
		return nil, nil
	}
	rejectSeq, raw, err := workflowRefinementReviewFailure(ctx, q, workID, definition, subject)
	if err != nil || rejectSeq == 0 {
		return nil, err
	}
	var fields struct {
		ActionID             string   `json:"action_id"`
		AttemptID            string   `json:"worker_attempt_id"`
		CorrectionPredicates []string `json:"correction_predicate_ids"`
		CorrectionEvidence   []string `json:"correction_evidence_refs"`
		ResultEvidence       []string `json:"result_evidence_refs"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, newFailure(KindInvariantViolation, subject, "review disposition payload is malformed", false, "rebuild workflow projections from the event log")
	}
	predicates := correctionReferenceStrings(fields.CorrectionPredicates)
	if len(predicates) == 0 {
		contractPredicates, contractErr := workflowActiveContractPredicateIDs(ctx, q, workID, subject)
		if contractErr != nil {
			return nil, contractErr
		}
		predicates = contractPredicates
	}
	if len(predicates) == 0 {
		return nil, nil
	}
	lastHealthySeq, baselineErr := workflowCorrectionActiveHealthyBaseline(ctx, q, workID, rejectSeq, subject)
	if baselineErr != nil {
		return nil, baselineErr
	}
	var priorCorrections int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' AND seq>? AND seq<?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, lastHealthySeq, rejectSeq).Scan(&priorCorrections); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot count correction requests", true, "retry once the workflow correction projection is readable", err)
	}
	target := workflowCorrectionTargetStep(definition, currentStep)
	if target == "" {
		return nil, nil
	}
	attempts := priorCorrections + 1
	evidence := correctionReferenceStrings(append(fields.CorrectionEvidence, fields.ResultEvidence...))
	disposition := "rejected"
	diagnosis := "the refinement pass rejected a result and its repaired result has no fresh accepted review"
	if fields.ActionID == "accept_worker_result" {
		disposition = "verification"
		diagnosis = "the accepted refinement review has a no_ship verdict and no fresh settling review covers it"
		evidence = correctionReferenceStrings(append(evidence, fields.AttemptID))
	}
	return &WorkflowCorrectionContext{
		Disposition: disposition, AttemptCount: attempts, AttemptLimit: workflowCorrectionAttemptLimit, Escalated: attempts > workflowCorrectionAttemptLimit,
		PredicateIDs: nonNilStrings(predicates), EvidenceRefs: nonNilStrings(evidence),
		Diagnosis: diagnosis, Strategy: fmt.Sprintf("return to step %q and dispatch a fresh review of the repaired result", target),
		FailedAttemptID: "", FailedAttemptEpoch: 0,
	}, nil
}

// Correction prerequisite classes. The shared admission derivation names the
// one prerequisite a closed correction request is missing, so the pin, the
// preflight, the fold, the dispatch resolver, and the guards state one answer
// and name the same gap.
const (
	workflowCorrectionMissingNone               = ""
	workflowCorrectionMissingVerdict            = "verdict"
	workflowCorrectionMissingAcceptedDelivery   = "accepted_delivery"
	workflowCorrectionMissingFailureDisposition = "failure_disposition"
	workflowCorrectionMissingGateReview         = "gate_review"
)

// workflowCorrectionRequestUnavailableFailure is the one refusal a closed
// correction request produces. The class decides the message, so a refusal
// names the actual missing prerequisite instead of a generic verdict claim.
func workflowCorrectionRequestUnavailableFailure(subject, missing string) *Failure {
	switch missing {
	case workflowCorrectionMissingVerdict:
		return newFailure(KindInvalidOperation, subject, "request_correction requires a current non-ok verification verdict under the active workflow contract", false, "record the current verification verdict or reread the work pin")
	case workflowCorrectionMissingFailureDisposition:
		return newFailure(KindInvalidOperation, subject, "request_correction requires the checkpoint failure record that dispositions the failed review attempt", false, "record the failed review attempt at the checkpoint with record_worker_failure")
	case workflowCorrectionMissingAcceptedDelivery:
		return newFailure(KindInvalidOperation, subject, "request_correction requires an accepted worker delivery behind the current non-ok verdict", false, "accept the completed worker result or reread the work pin")
	case workflowCorrectionMissingGateReview:
		return newFailure(KindInvalidOperation, subject, "request_correction at the delivery gate requires an outstanding post-rejection review", false, "reread the current work pin")
	default:
		return newFailure(KindInvalidOperation, subject, "request_correction requires a current non-ok verification verdict after worker delivery", false, "record the current verification verdict or reread the work pin")
	}
}

// workflowAttemptDispatchSequence reads the sequence of the worker dispatch
// behind one dispatched attempt: the freshness anchor the failed-review
// correction's current verdicts must postdate.
func workflowAttemptDispatchSequence(ctx context.Context, q queryer, workID, attemptID, subject string) (int64, error) {
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, string(SubjectWorkItem), workID, WorkerDispatched, attemptID).Scan(&seq); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot inspect the failed attempt dispatch", true, "retry once the worker attempt projection is readable", err)
	}
	return seq, nil
}

// workflowCheckpointFailedReviewCorrection is the amended CD-0143 D1 and
// CD-0193 D1 admission. At a human_checkpoint step whose latest attempt a
// worker actually dispatched failed and whose exact hold-mode
// record_worker_failure dispositions that attempt, a current non-ok verdict
// admits the evidence-bearing corrective return the accepted-delivery route
// cannot. The failure disposition is not an accepted success: it opens only
// this typed return, and the counting follows the CD-0164 D4 verification
// comparator (requests since the last comparable-healthy verdict, plus the
// pending request). The admitted pair must be current: the dispositioned
// attempt's dispatch must postdate the step recovery anchor — a later
// accepted delivery, step entry, or correction request superseded it — and
// every current non-ok verdict must postdate the attempt's dispatch, so a
// correction request that consumed the pair ended it and only a fresh verdict
// or a fresh dispositioned failed review reopens the return.
func workflowCheckpointFailedReviewCorrection(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, state *workflowCorrectionVerdictPrerequisites, excludeSeq int64) (*WorkflowCorrectionContext, string, error) {
	if step := workflowStep(definition, currentStep); step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return nil, workflowCorrectionMissingAcceptedDelivery, nil
	}
	attemptID, lifecycle, found, err := latestDispatchedAttemptAtStep(ctx, q, workID, currentStep, 0)
	if err != nil {
		return nil, workflowCorrectionMissingNone, err
	}
	if !found || lifecycle != "failed" {
		return nil, workflowCorrectionMissingAcceptedDelivery, nil
	}
	var recorded int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='record_worker_failure' AND json_extract(payload,'$.worker_attempt_id')=?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, attemptID).Scan(&recorded); err != nil {
		return nil, workflowCorrectionMissingNone, wrapFailure(KindUnavailable, subject, "cannot inspect the checkpoint failure record", true, "retry once the workflow projection is readable", err)
	}
	if recorded == 0 {
		return nil, workflowCorrectionMissingFailureDisposition, nil
	}
	anchor, anchorErr := workflowSameStepWindowAnchor(ctx, q, definition, workID, subject, excludeSeq)
	if anchorErr != nil {
		return nil, workflowCorrectionMissingNone, anchorErr
	}
	dispatchSeq, dispatchErr := workflowAttemptDispatchSequence(ctx, q, workID, attemptID, subject)
	if dispatchErr != nil {
		return nil, workflowCorrectionMissingNone, dispatchErr
	}
	if dispatchSeq <= anchor {
		return nil, workflowCorrectionMissingVerdict, nil
	}
	verdictSequences, sequenceErr := workflowLatestVerdictSequences(ctx, q, workID, state.contractVersion, state.nonOK)
	if sequenceErr != nil {
		return nil, workflowCorrectionMissingNone, sequenceErr
	}
	for _, verdict := range state.nonOK {
		if verdictSequences[verdict.PredicateID] <= dispatchSeq {
			return nil, workflowCorrectionMissingVerdict, nil
		}
	}
	var lastHealthySeq int64
	if lastHealthySeq, err = workflowCorrectionHealthyBaseline(ctx, q, workID, state.contractVersion, state.verdictSeq); err != nil {
		return nil, workflowCorrectionMissingNone, err
	}
	var priorCorrections int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' AND seq>? AND seq<?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, lastHealthySeq, state.verdictSeq).Scan(&priorCorrections); err != nil {
		return nil, workflowCorrectionMissingNone, wrapFailure(KindUnavailable, subject, "cannot count correction requests", true, "retry once the workflow correction projection is readable", err)
	}
	target := workflowCorrectionTargetStep(definition, currentStep)
	if target == "" {
		return nil, workflowCorrectionMissingAcceptedDelivery, nil
	}
	attempts := priorCorrections + 1
	predicates := make([]string, 0, len(state.nonOK))
	evidence := make([]string, 0)
	for _, verdict := range state.nonOK {
		predicates = append(predicates, verdict.PredicateID)
		for _, ref := range verdict.EvaluationEvidence {
			if !contains(evidence, ref) {
				evidence = append(evidence, ref)
			}
		}
	}
	return &WorkflowCorrectionContext{
		Disposition: "failed", AttemptCount: attempts, AttemptLimit: workflowCorrectionAttemptLimit, Escalated: attempts > workflowCorrectionAttemptLimit,
		PredicateIDs: nonNilStrings(predicates), EvidenceRefs: nonNilStrings(evidence),
		Diagnosis: "the checkpoint review attempt failed and the latest verification verdict is not healthy", Strategy: fmt.Sprintf("return to step %q and dispatch a fresh attempt", target),
		FailedAttemptID: attemptID,
	}, workflowCorrectionMissingNone, nil
}

// workflowCorrectionRequestAdmission derives the correction request the
// current step admits and names the one missing prerequisite when it admits
// none. It is the single derivation behind the availability check, the work
// pin, the preflight, the fold, and the dispatch refusal, so every surface
// answers from one read. excludeSeq names the fold's own in-flight completion
// the admission must not count against itself; read surfaces pass zero. A
// CD-0166 delivery gate corrects through the post-rejection review return; a
// checkpoint with a dispositioned failed review corrects through the
// failed-review return; every other step corrects through the verification
// verdict.
func workflowCorrectionRequestAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeSeq int64) (*WorkflowCorrectionContext, string, error) {
	if workflowCorrectionWorkflow(definition) && workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		context, err := workflowDeliveryGateCorrectionContext(ctx, q, workID, definition, currentStep, subject)
		if err != nil || context != nil {
			return context, workflowCorrectionMissingNone, err
		}
		return nil, workflowCorrectionMissingGateReview, nil
	}
	state, err := workflowCorrectionVerdictState(ctx, q, workID, definition, currentStep, subject)
	if err != nil {
		return nil, workflowCorrectionMissingNone, err
	}
	if state != nil {
		context, contextErr := workflowVerdictCorrectionFromState(ctx, q, workID, definition, currentStep, subject, state)
		if contextErr != nil {
			return nil, workflowCorrectionMissingNone, contextErr
		}
		if context != nil {
			return context, workflowCorrectionMissingNone, nil
		}
		return workflowCheckpointFailedReviewCorrection(ctx, q, workID, definition, currentStep, subject, state, excludeSeq)
	}
	// No non-ok verdict stands under the active contract. A step without a
	// correction route keeps the shape-neutral default refusal; a correction
	// route names the absent verdict.
	if !workflowCorrectionWorkflow(definition) || workflowCorrectionTargetStep(definition, currentStep) == "" {
		return nil, workflowCorrectionMissingNone, nil
	}
	return nil, workflowCorrectionMissingVerdict, nil
}

// workflowCorrectionRequestAdmissionState reports the admission as the
// boolean surfaces hold plus the missing-prerequisite class the refusal
// constructor reads. excludeSeq names the fold's own in-flight completion so
// the anchor judges settled entries only; read surfaces pass zero.
func workflowCorrectionRequestAdmissionState(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeSeq int64) (bool, string, error) {
	context, missing, err := workflowCorrectionRequestAdmission(ctx, q, workID, definition, currentStep, subject, excludeSeq)
	return context != nil, missing, err
}

// workflowActiveContractPredicateIDs lists the active contract's predicate IDs
// in ordinal order, or an empty slice when the item has no active contract.
func workflowActiveContractPredicateIDs(ctx context.Context, q queryer, workID, subject string) ([]string, error) {
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY ordinal`, workID, contractVersion)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	defer rows.Close()
	predicates := make([]string, 0, 4)
	for rows.Next() {
		var predicateID string
		if err := rows.Scan(&predicateID); err != nil {
			return nil, wrapFailure(KindUnavailable, subject, "cannot scan active workflow predicate", true, "retry once the workflow contract is readable", err)
		}
		predicates = append(predicates, predicateID)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot enumerate active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	return predicates, nil
}

// validateCorrectionRequestPayload refuses a correction payload the folded
// correction request cannot represent. context is the one admission derivation
// the caller already folded — the admission state's CorrectionRequestContext
// at the guard and preflight sites, the fold's own derived request — so this
// check binds predicates and evidence only and never re-enters the loader.
// A nil context refuses: the step admits no correction request to bind
// against.
func validateCorrectionRequestPayload(ctx context.Context, q queryer, workID string, payload json.RawMessage, subject string, context *WorkflowCorrectionContext) error {
	if context == nil {
		return newFailure(KindUnavailable, subject, "the current step admits no correction request", false, "reread_entities")
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	diagnosis := workflowFieldStringDefault(fields, "diagnosis", "")
	strategy := workflowFieldStringDefault(fields, "strategy", "")
	predicates := workflowFieldStrings(fields, "predicate_ids")
	evidence := workflowFieldStrings(fields, "evidence_refs")
	if diagnosis == "" || strategy == "" || len(predicates) == 0 || len(evidence) == 0 {
		return newFailure(KindInvalidPayload, subject, "request_correction requires diagnosis, strategy, predicate IDs, and bound evidence", false, "supply the complete correction disposition")
	}
	// The request path records every correction the verdict admits, including
	// the escalated one: CD-0164 D4 arms the approval wall at dispatch when the
	// request count passes the limit, so the wall stays operator approvable
	// (CD-0148) instead of refusing the record a fresh attempt is bound to.
	for _, predicate := range predicates {
		if !contains(context.PredicateIDs, predicate) {
			return newFailure(KindInvalidPayload, subject, "request_correction names a predicate without a current non-ok verdict", false, "name only affected approved predicates")
		}
	}
	for _, ref := range evidence {
		bound, boundErr := workflowEvidenceReferenceBound(ctx, q, workID, ref, subject, 0)
		if boundErr != nil {
			return boundErr
		}
		if !bound {
			return newFailure(KindMissingEvidence, subject, "request_correction evidence is not durably bound: "+ref, false, "provide_evidence")
		}
	}
	return nil
}

// workflowCorrectionContext reads the latest unconsumed failure or rejection.
// A later dispatch whose worker attempt materializes consumes the record, while
// all earlier worker attempts stay immutable.
func workflowCorrectionContext(ctx context.Context, q queryer, workID, stepID string) (*WorkflowCorrectionContext, error) {
	return workflowCorrectionContextForDispatch(ctx, q, workID, stepID, "")
}

// The consuming dispatch must have a worker attempt that materialized: a
// worker.dispatched event for the completion's worker_attempt_id. A dispatch
// intent folded without its host worker-dispatch call (an interruption between
// the workflow action and the dispatch) leaves the correction record live, so
// the retry binding stays readable and the escalation wall keeps its
// operator-approvable escape. json_extract returns NULL for a missing path and
// a plain = with NULL on either side matches no row, so a completion without a
// worker_attempt_id consumes nothing.
func workflowCorrectionContextForDispatch(ctx context.Context, q queryer, workID, stepID, dispatchAttemptID string) (*WorkflowCorrectionContext, error) {
	query := `SELECT d.seq,d.payload FROM domain_events d
WHERE d.subject_type=? AND d.subject_id=? AND d.kind=?
	  AND json_extract(d.payload,'$.action_id') IN ('record_worker_failure','reject_worker_result','request_correction')
  AND NOT EXISTS (SELECT 1 FROM domain_events newer
    WHERE newer.subject_type=d.subject_type AND newer.subject_id=d.subject_id
      AND newer.kind=? AND newer.seq>d.seq
      AND json_extract(newer.payload,'$.action_id')='dispatch_worker'
      AND EXISTS (SELECT 1 FROM domain_events dispatched
        WHERE dispatched.subject_type=newer.subject_type AND dispatched.subject_id=newer.subject_id
          AND dispatched.kind=?
          AND json_extract(dispatched.payload,'$.attempt_id')=json_extract(newer.payload,'$.worker_attempt_id'))`
	args := []any{string(SubjectWorkItem), workID, WorkflowActionCompleted, WorkflowActionCompleted, WorkerDispatched}
	if dispatchAttemptID != "" {
		query += ` AND json_extract(newer.payload,'$.worker_attempt_id')<>?`
		args = append(args, dispatchAttemptID)
	}
	query += `)
ORDER BY d.seq DESC LIMIT 1`
	var seq int64
	var raw []byte
	if err := q.QueryRowContext(ctx, query, args...).Scan(&seq, &raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read correction context", true, "retry once the workflow projection is readable", err)
	}
	var fields struct {
		ActionID             string   `json:"action_id"`
		AttemptID            string   `json:"worker_attempt_id"`
		AttemptEpoch         int64    `json:"attempt_epoch"`
		CorrectionDiagnosis  string   `json:"correction_diagnosis"`
		CorrectionStrategy   string   `json:"correction_strategy"`
		CorrectionPredicates []string `json:"correction_predicate_ids"`
		CorrectionEvidence   []string `json:"correction_evidence_refs"`
		ResultEvidence       []string `json:"result_evidence_refs"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "correction completion payload is malformed", false, "rebuild workflow projections from the event log")
	}
	if fields.ActionID == "request_correction" {
		// The CD-0164 D4 window the recorded request owns opens at the same
		// comparable-healthy baseline the admission derived before it admitted
		// the request, so the dispatched packet's count and escalation state
		// match the admitted count and an incomparable or partial ok verdict
		// set cannot reset the window between the two reads.
		lastHealthySeq, baselineErr := workflowCorrectionActiveHealthyBaseline(ctx, q, workID, seq, "workflow_correction")
		if baselineErr != nil {
			return nil, baselineErr
		}
		var attempts int64
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='request_correction' AND seq>? AND seq<=?`, string(SubjectWorkItem), workID, WorkflowActionCompleted, lastHealthySeq, seq).Scan(&attempts); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot count correction requests", true, "retry once the workflow correction projection is readable", err)
		}
		return &WorkflowCorrectionContext{
			Disposition: "verification", AttemptCount: attempts, AttemptLimit: workflowCorrectionAttemptLimit, Escalated: attempts > workflowCorrectionAttemptLimit,
			PredicateIDs: correctionReferenceStrings(fields.CorrectionPredicates), EvidenceRefs: correctionReferenceStrings(fields.CorrectionEvidence), Diagnosis: fields.CorrectionDiagnosis, Strategy: fields.CorrectionStrategy,
		}, nil
	}
	if fields.AttemptID == "" && fields.ActionID != "request_correction" {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "correction completion has no worker attempt", false, "rebuild workflow projections from the event log")
	}
	if stepID != "" && fields.ActionID != "request_correction" {
		var actionStep string
		if err := q.QueryRowContext(ctx, `SELECT json_extract(payload,'$.step_id') FROM domain_events WHERE seq=?`, seq).Scan(&actionStep); err == nil && actionStep != "" && actionStep != stepID {
			return nil, nil
		}
	}
	count, err := workflowCorrectionAttemptCount(ctx, q, workID, seq, "workflow_correction")
	if err != nil {
		return nil, err
	}
	if count < 1 {
		return nil, newFailure(KindInvariantViolation, "workflow_correction", "correction has no worker attempt history", false, "rebuild the worker attempt projection")
	}
	var failureKind, failureDetail string
	if fields.AttemptID != "" {
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(failure_kind,''),COALESCE(failure_detail,'') FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, fields.AttemptID).Scan(&failureKind, &failureDetail); err != nil && err != sql.ErrNoRows {
			return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read worker attempt detail", true, "retry once the worker attempt projection is readable", err)
		}
	}
	if fields.ActionID == "record_worker_failure" && fields.CorrectionDiagnosis == "" {
		fields.CorrectionDiagnosis = failureDetail
		if fields.CorrectionDiagnosis == "" {
			fields.CorrectionDiagnosis = "worker attempt failed"
		}
	}
	if fields.ActionID == "record_worker_failure" && fields.CorrectionStrategy == "" {
		fields.CorrectionStrategy = "retry with a fresh fenced attempt"
	}
	return &WorkflowCorrectionContext{
		Disposition: dispositionForCorrection(fields.ActionID), AttemptCount: count, AttemptLimit: workflowCorrectionAttemptLimit, Escalated: count >= workflowCorrectionAttemptLimit,
		PredicateIDs: correctionReferenceStrings(fields.CorrectionPredicates), EvidenceRefs: correctionReferenceStrings(append(fields.CorrectionEvidence, fields.ResultEvidence...)),
		Diagnosis: fields.CorrectionDiagnosis, Strategy: fields.CorrectionStrategy, FailureKind: failureKind, FailureDetail: failureDetail,
		FailedAttemptID: fields.AttemptID, FailedAttemptEpoch: fields.AttemptEpoch,
	}, nil
}

// correctionReferenceStrings keeps only the entries a work pin correction may
// carry. Evidence locators are admissible at 1 to 2048 bytes on the tool
// surface, while the correction projection's predicate and evidence lists must
// satisfy the reference rule the closed pin schema states. Entries outside
// that rule stay in the durable completion payload and drop out of the bounded
// pin view instead of failing the whole mutation result.
func correctionReferenceStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !ValidReference(value) {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func dispositionForCorrection(actionID string) string {
	if actionID == "request_correction" {
		return "requested"
	}
	if actionID == "reject_worker_result" {
		return "rejected"
	}
	return "failed"
}

// workflowRejectedWorkerResultAvailable reports whether a completed worker
// result at the step is still open to rejection. excludeSeq names the fold's
// own in-flight event, whose payload names the same attempt; read surfaces
// pass 0 because every settled event counts.
func workflowRejectedWorkerResultAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeSeq int64) (bool, error) {
	if currentStep == "" {
		return false, nil
	}
	boundary, found, err := workflowStepPassBoundary(ctx, q, definition, workID, currentStep, subject)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	var attemptID, lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT a.attempt_id,a.lifecycle_state FROM worker_attempts a JOIN domain_events d ON d.subject_type=? AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id WHERE a.work_id=? AND a.lifecycle_state='completed' AND d.seq>? AND NOT EXISTS (SELECT 1 FROM domain_events r WHERE r.subject_type=d.subject_type AND r.subject_id=d.subject_id AND r.kind=? AND json_extract(r.payload,'$.worker_attempt_id')=a.attempt_id AND json_extract(r.payload,'$.action_id') IN ('accept_worker_result','accept_worker_evidence','reject_worker_result') AND r.seq<>?) ORDER BY d.seq DESC LIMIT 1`, string(SubjectWorkItem), WorkerDispatched, workID, boundary, WorkflowActionCompleted, excludeSeq).Scan(&attemptID, &lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect completed worker results", true, "retry once the worker attempt projection is readable", err)
	}
	return attemptID != "" && lifecycle == "completed", nil
}

// validateWorkerPacketCorrection refuses a packet that does not consume the
// current correction context. The same-step wall and the escalation stay out
// of this check: the pure workflowAdmit decides them over the folded state,
// and the fold refuses before event assembly runs, so re-deriving them here
// would be a second derivation of the folded conditions.
func validateWorkerPacketCorrection(ctx context.Context, q queryer, workID, currentStep string, packetRaw json.RawMessage) error {
	var packet struct {
		AttemptID string `json:"attempt_id"`
		Inputs    struct {
			Correction *WorkflowCorrectionContext `json:"correction"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(packetRaw, &packet); err != nil {
		return newFailure(KindInvalidPayload, "workflow_action", "dispatch_worker worker_packet is malformed", false, "supply the lane packet bound to this work item and attempt")
	}
	correction, err := workflowCorrectionContextForDispatch(ctx, q, workID, currentStep, packet.AttemptID)
	if err != nil {
		return err
	}
	if correction == nil {
		if packet.Inputs.Correction != nil {
			return newFailure(KindInvalidPayload, "workflow_action", "worker packet carries correction context without a durable correction", false, "build the packet from the current work pin")
		}
		return nil
	}
	if packet.Inputs.Correction == nil || !sameWorkflowCorrection(packet.Inputs.Correction, correction) {
		return newFailure(KindInvalidPayload, "workflow_action", "worker packet does not consume the current correction context", false, "build a fresh packet from the current work pin")
	}
	return nil
}

func sameWorkflowCorrection(left, right *WorkflowCorrectionContext) bool {
	return left.Disposition == right.Disposition && left.AttemptCount == right.AttemptCount && left.AttemptLimit == right.AttemptLimit && left.Escalated == right.Escalated && left.Diagnosis == right.Diagnosis && left.Strategy == right.Strategy && left.FailureKind == right.FailureKind && left.FailureDetail == right.FailureDetail && sameCorrectionStrings(left.PredicateIDs, right.PredicateIDs) && sameCorrectionStrings(left.EvidenceRefs, right.EvidenceRefs)
}

func sameCorrectionStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
