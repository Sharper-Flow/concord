package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
func workflowCompletedInstanceOffShapeFailure(subject string) error {
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
// the limit keeps its ordinary approval-free correction dispatch.
func WorkflowFailedWorkerRetryBinding(ctx context.Context, s *Store, workID string) (*WorkflowRetryApprovalBinding, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_correction", "store is not open", false, "open the authority database")
	}
	return workflowFailedWorkerRetryBinding(ctx, s.db, workID)
}

// WorkflowFailedWorkerRetryBindingTx is the transaction-scoped form used by
// the approval and dispatch boundary. It rereads the binding before approval
// consumption, so a stale approval cannot authorize a different attempt.
func WorkflowFailedWorkerRetryBindingTx(ctx context.Context, transaction *Transaction, workID string) (*WorkflowRetryApprovalBinding, error) {
	q, err := transactionSQL(transaction, "workflow_correction")
	if err != nil {
		return nil, err
	}
	return workflowFailedWorkerRetryBinding(ctx, q, workID)
}

func workflowFailedWorkerRetryBinding(ctx context.Context, q queryer, workID string) (*WorkflowRetryApprovalBinding, error) {
	var stepID string
	if err := q.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&stepID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read the current workflow step", true, "retry once the workflow projection is readable", err)
	}
	correction, err := workflowCorrectionContextForDispatch(ctx, q, workID, stepID, "")
	if err != nil || correction == nil {
		return nil, err
	}
	verification := correction.Escalated && correction.Disposition == "verification" && correction.FailedAttemptID == ""
	switch {
	case correction.Disposition == "failed":
	case verification:
	case correction.Escalated && correction.Disposition == "rejected" && correction.FailedAttemptID != "":
	default:
		return nil, nil
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

func workflowCorrectionVerdicts(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) ([]workflowVerdictRecordedPayload, int64, int64, error) {
	if !workflowCorrectionWorkflow(definition) || workflowCorrectionTargetStep(definition, currentStep) == "" {
		return nil, 0, 0, nil
	}
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, wrapFailure(KindUnavailable, subject, "cannot read the active workflow contract", true, "retry once the workflow contract is readable", err)
	}
	verdicts, err := latestWorkflowVerdicts(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, 0, 0, err
	}
	nonOK := make([]workflowVerdictRecordedPayload, 0, len(verdicts))
	for _, verdict := range verdicts {
		if verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved {
			nonOK = append(nonOK, verdict)
		}
	}
	if len(nonOK) == 0 {
		return nil, 0, 0, nil
	}
	var verdictSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=?`, string(SubjectWorkItem), workID, WorkflowVerdictRecorded).Scan(&verdictSeq); err != nil {
		return nil, 0, 0, wrapFailure(KindUnavailable, subject, "cannot read the latest workflow verdict sequence", true, "retry once the workflow verdict projection is readable", err)
	}
	acceptedDispatchSeq, accepted, acceptedErr := workflowAcceptedWorkerDelivery(ctx, q, workID, verdictSeq, subject)
	if acceptedErr != nil {
		return nil, 0, 0, acceptedErr
	}
	if !accepted {
		return nil, 0, 0, nil
	}
	return nonOK, verdictSeq, acceptedDispatchSeq, nil
}

// workflowAcceptedWorkerDelivery requires both a completed worker attempt and
// its folded accept_worker_result action before a verdict can request correction.
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
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM worker_attempts a JOIN domain_events accepted ON accepted.subject_type=? AND accepted.subject_id=a.work_id AND accepted.kind=? AND accepted.seq>? AND accepted.seq<=? AND json_extract(accepted.payload,'$.action_id')='accept_worker_result' AND json_extract(accepted.payload,'$.worker_attempt_id')=a.attempt_id WHERE a.work_id=? AND a.attempt_id=? AND a.lifecycle_state='completed')`, string(SubjectWorkItem), WorkflowActionCompleted, dispatchSeq, throughSeq, workID, attemptID).Scan(&accepted); err != nil {
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

func workflowCorrectionAttemptCount(ctx context.Context, q queryer, workID string, seq int64, subject string) (int64, error) {
	var count int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM worker_attempts a JOIN domain_events dispatch ON dispatch.subject_type=? AND dispatch.subject_id=a.work_id AND dispatch.kind=? AND json_extract(dispatch.payload,'$.attempt_id')=a.attempt_id WHERE a.work_id=? AND dispatch.seq<=? AND dispatch.seq>COALESCE((SELECT MAX(accepted.seq) FROM domain_events accepted WHERE accepted.subject_type=dispatch.subject_type AND accepted.subject_id=dispatch.subject_id AND accepted.kind=? AND accepted.seq<? AND json_extract(accepted.payload,'$.action_id')='accept_worker_result'),0)`, string(SubjectWorkItem), WorkerDispatched, workID, seq, WorkflowActionCompleted, seq).Scan(&count); err != nil {
		return 0, wrapFailure(KindUnavailable, subject, "cannot count correction attempts", true, "retry once the worker attempt projection is readable", err)
	}
	return count, nil
}

func workflowVerdictCorrectionContext(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (*WorkflowCorrectionContext, error) {
	verdicts, seq, acceptedDispatchSeq, err := workflowCorrectionVerdicts(ctx, q, workID, definition, currentStep, subject)
	if err != nil || len(verdicts) == 0 {
		return nil, err
	}
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the active workflow contract", true, "retry once the workflow contract is readable", err)
	}
	var lastHealthySeq int64
	lastHealthySeq, healthyErr := workflowLatestComparableHealthySequence(ctx, q, workID, contractVersion, seq)
	if healthyErr != nil {
		return nil, healthyErr
	}
	// A cutoff in the contract's ancestry bounds the healthy baseline: the
	// corrected contract's verification window opens at the supersession, so
	// historical healthy verdicts and the corrections they closed count
	// neither as health nor toward the successor's bound.
	cutoff, cutoffErr := workflowCompleteStepCorrectionEvidenceCutoff(ctx, q, workID, contractVersion)
	if cutoffErr != nil {
		return nil, cutoffErr
	}
	if lastHealthySeq < cutoff {
		lastHealthySeq = cutoff
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

func workflowCorrectionRequestAvailable(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (bool, error) {
	context, err := workflowVerdictCorrectionContext(ctx, q, workID, definition, currentStep, subject)
	return context != nil, err
}

func validateCorrectionRequestPayload(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep string, payload json.RawMessage, subject string) error {
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
	context, err := workflowVerdictCorrectionContext(ctx, q, workID, definition, currentStep, subject)
	if err != nil {
		return err
	}
	if context == nil {
		return newFailure(KindInvalidOperation, subject, "request_correction requires a current non-ok verification verdict after worker delivery", false, "record the current verification verdict or reread the work pin")
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
// A later dispatch consumes the record, while all earlier worker attempts stay immutable.
func workflowCorrectionContext(ctx context.Context, q queryer, workID, stepID string) (*WorkflowCorrectionContext, error) {
	return workflowCorrectionContextForDispatch(ctx, q, workID, stepID, "")
}

func workflowCorrectionContextForDispatch(ctx context.Context, q queryer, workID, stepID, dispatchAttemptID string) (*WorkflowCorrectionContext, error) {
	query := `SELECT d.seq,d.payload FROM domain_events d
WHERE d.subject_type=? AND d.subject_id=? AND d.kind=?
	  AND json_extract(d.payload,'$.action_id') IN ('record_worker_failure','reject_worker_result','request_correction')
  AND NOT EXISTS (SELECT 1 FROM domain_events newer
    WHERE newer.subject_type=d.subject_type AND newer.subject_id=d.subject_id
      AND newer.kind=? AND newer.seq>d.seq
      AND json_extract(newer.payload,'$.action_id')='dispatch_worker'`
	args := []any{string(SubjectWorkItem), workID, WorkflowActionCompleted, WorkflowActionCompleted}
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
		var lastHealthySeq int64
		if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.verdict_kind')='ok' AND seq<?`, string(SubjectWorkItem), workID, WorkflowVerdictRecorded, seq).Scan(&lastHealthySeq); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot inspect the correction sequence", true, "retry once the workflow verdict projection is readable", err)
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

func workflowRejectedWorkerResultAvailable(ctx context.Context, q queryer, workID, currentStep, subject string) (bool, error) {
	if currentStep == "" {
		return false, nil
	}
	startSeq, _, found, err := latestWorkflowActionStart(ctx, q, workID, currentStep)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	var attemptID, lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT a.attempt_id,a.lifecycle_state FROM worker_attempts a JOIN domain_events d ON d.subject_type=? AND d.subject_id=a.work_id AND d.kind=? AND json_extract(d.payload,'$.attempt_id')=a.attempt_id WHERE a.work_id=? AND a.lifecycle_state='completed' AND d.seq>? AND NOT EXISTS (SELECT 1 FROM domain_events r WHERE r.subject_type=d.subject_type AND r.subject_id=d.subject_id AND r.kind=? AND r.seq<d.seq AND json_extract(r.payload,'$.worker_attempt_id')=a.attempt_id AND json_extract(r.payload,'$.action_id') IN ('accept_worker_result','reject_worker_result')) ORDER BY d.seq DESC LIMIT 1`, string(SubjectWorkItem), WorkerDispatched, workID, startSeq, WorkflowActionCompleted).Scan(&attemptID, &lifecycle); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, wrapFailure(KindUnavailable, subject, "cannot inspect completed worker results", true, "retry once the worker attempt projection is readable", err)
	}
	return attemptID != "" && lifecycle == "completed", nil
}

// validateWorkerPacketCorrection refuses a packet that does not consume the
// current correction context. An escalated correction is admissible only
// through the approval-gated boundary, which sets escalatedRetryApproved after
// it consumed the operator approval bound to this correction.
func validateWorkerPacketCorrection(ctx context.Context, q queryer, workID, currentStep string, packetRaw json.RawMessage, escalatedRetryApproved bool) error {
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
	if correction.Escalated && !escalatedRetryApproved {
		return newFailure(KindApprovalRequired, "workflow_action", "worker retry reached the three-attempt limit", false, "escalate the correction to the operator")
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
