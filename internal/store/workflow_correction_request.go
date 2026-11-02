package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
)

func workflowFoldCorrectionReturnTarget(entry RegisteredDefinition, currentStep string) (string, error) {
	target := workflowCorrectionReturnTarget(entry.Definition, currentStep)
	if target == "" {
		return "", newFailure(KindIllegalLifecycleTransition, "fold_event", "correction has no declared recovery route to a producer step", false, "repair the pinned workflow definition")
	}
	return target, nil
}

// WorkflowCorrectionContext retains the direction and source of a corrective
// return. It makes no claim about host subagent execution.
type WorkflowCorrectionContext struct {
	Disposition    string   `json:"disposition"`
	PredicateIDs   []string `json:"predicate_ids"`
	EvidenceRefs   []string `json:"evidence_refs"`
	Diagnosis      string   `json:"diagnosis,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
	SourceEventID  string   `json:"source_event_id"`
	SourceEventSeq int64    `json:"source_event_seq"`
}

func workflowCorrectionRequestActionDefinition() WorkflowActionDefinition {
	return workTransitionAction(currentActionDefinition("request_correction", true))
}

func workflowCorrectionRouteStrategy(definition WorkflowDefinition, currentStep string) string {
	return fmt.Sprintf("return to the producer step %q and re-produce the artifact", workflowCorrectionReturnTarget(definition, currentStep))
}

func workflowLatestComparableHealthySequence(ctx context.Context, q queryer, workID string, contractVersion int64) (int64, error) {
	var seq int64
	if err := q.QueryRowContext(ctx, `SELECT last_healthy_seq FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&seq); err != nil && err != sql.ErrNoRows {
		return 0, workflowProjectionError(err, "cannot read the healthy-verdict frontier")
	}
	return seq, nil
}

func workflowCorrectionHealthyBaseline(ctx context.Context, q queryer, workID string, contractVersion int64) (int64, error) {
	return workflowLatestComparableHealthySequence(ctx, q, workID, contractVersion)
}

type workflowCorrectionVerdictPrerequisites struct {
	nonOK           []workflowVerdictRecordedPayload
	contractVersion int64
	verdictSeq      int64
	productionSeq   int64
}

func workflowCorrectionVerdictState(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string) (*workflowCorrectionVerdictPrerequisites, error) {
	target := workflowCorrectionReturnTarget(definition, currentStep)
	if target == "" {
		return nil, nil
	}
	contractVersion, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the active workflow contract", true, "retry once the workflow contract is readable", err)
	}
	verdicts, err := latestWorkflowVerdicts(ctx, q, workID, contractVersion)
	if err != nil {
		return nil, err
	}
	nonOK := []workflowVerdictRecordedPayload{}
	for _, verdict := range verdicts {
		if verdict.VerdictKind != "ok" || verdict.IncomparableWithApproved {
			nonOK = append(nonOK, verdict)
		}
	}
	if len(nonOK) == 0 {
		return nil, nil
	}
	var verdictSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(verdict_seq),0) FROM workflow_predicate_verdicts WHERE work_id=?`, workID).Scan(&verdictSeq); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read the latest workflow verdict sequence", true, "retry once the workflow verdict projection is readable", err)
	}
	productionSeq, err := workflowArtifactProductionFrontier(ctx, q, workID, definition, target, 0, verdictSeq, subject)
	if err != nil {
		return nil, err
	}
	return &workflowCorrectionVerdictPrerequisites{nonOK: nonOK, contractVersion: contractVersion, verdictSeq: verdictSeq, productionSeq: productionSeq}, nil
}

func workflowVerdictCorrectionFromState(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, state *workflowCorrectionVerdictPrerequisites) (*WorkflowCorrectionContext, error) {
	if state.productionSeq == 0 {
		return nil, nil
	}
	baseline, err := workflowCorrectionHealthyBaseline(ctx, q, workID, state.contractVersion)
	if err != nil {
		return nil, err
	}
	var latestCorrectionSeq int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(prior_correction_seq),0) FROM workflow_predicate_verdicts WHERE work_id=? AND verdict_seq=? AND prior_correction_seq>?`, workID, state.verdictSeq, baseline).Scan(&latestCorrectionSeq); err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot inspect prior correction requests", true, "retry once the workflow correction projection is readable", err)
	}
	if latestCorrectionSeq >= state.verdictSeq || state.productionSeq <= latestCorrectionSeq {
		return nil, nil
	}
	sequences, err := workflowLatestVerdictSequences(ctx, q, workID, state.contractVersion, state.nonOK)
	if err != nil {
		return nil, err
	}
	var predicates, evidence []string
	for _, verdict := range state.nonOK {
		if sequences[verdict.PredicateID] <= latestCorrectionSeq || sequences[verdict.PredicateID] <= state.productionSeq {
			return nil, nil
		}
		predicates = append(predicates, verdict.PredicateID)
		for _, ref := range verdict.EvaluationEvidence {
			if !slices.Contains(evidence, ref) {
				evidence = append(evidence, ref)
			}
		}
	}
	return &WorkflowCorrectionContext{Disposition: "verification", PredicateIDs: nonNilStrings(predicates), EvidenceRefs: nonNilStrings(evidence), Diagnosis: "latest verification verdict is not healthy", Strategy: workflowCorrectionRouteStrategy(definition, currentStep)}, nil
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
	wantedIDs := map[string]bool{}
	for _, verdict := range wanted {
		wantedIDs[verdict.PredicateID] = true
	}
	sequences, seen := map[string]int64{}, map[string]bool{}
	rows, err := q.QueryContext(ctx, `SELECT verdict_seq,payload FROM workflow_predicate_verdicts WHERE work_id=? ORDER BY verdict_seq DESC`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_correction", "cannot read workflow verdict sequences", true, "retry once the workflow verdict projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var raw []byte
		if err := rows.Scan(&seq, &raw); err != nil {
			return nil, err
		}
		var verdict workflowVerdictRecordedPayload
		if err := json.Unmarshal(raw, &verdict); err != nil {
			return nil, newFailure(KindInvariantViolation, "workflow_correction", "workflow verdict payload is malformed", false, "restore a verified backup into a new path with concord restore, then swap")
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
		}
		if len(sequences) == len(wantedIDs) {
			break
		}
	}
	return sequences, rows.Err()
}

func workflowRefinementStepID(definition WorkflowDefinition) string {
	for _, step := range definition.StepGraph.Steps {
		if stepDeclaresAction(definition, step.ID, "checkpoint_refine") {
			return step.ID
		}
	}
	return ""
}

// The continuation must use this transaction's connection, never the pool
// parked behind it. The store pools one SQLite connection.
func workflowDeliveryGateCorrectionContextForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, registry DefinitionRegistry) (*WorkflowCorrectionContext, error) {
	if !workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		return nil, nil
	}
	tx, isTx := q.(*sql.Tx)
	if !isTx {
		db, isDB := q.(*sql.DB)
		if !isDB {
			return nil, newFailure(KindUnavailable, subject, "workflow action admission folds in the caller's transaction", false, "run the admission fold inside the mutation transaction")
		}
		readTx, err := beginReadTx(ctx, db)
		if err != nil {
			return nil, wrapFailure(KindUnavailable, subject, "cannot open the read transaction", true, "retry once the store is readable", err)
		}
		defer readTx.Rollback()
		tx = readTx
	}
	state, err := loadWorkflowAdmissionStateTxForRegistry(ctx, tx, workID, definition, currentStep, subject, registry)
	if err != nil {
		return nil, err
	}
	return workflowDeliveryGateCorrectionContextFolded(ctx, tx, workID, definition, currentStep, subject, state)
}

func workflowDeliveryGateCorrectionContextFolded(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, state WorkflowAdmissionState) (*WorkflowCorrectionContext, error) {
	if state.Review == nil || state.Review.Accepted == nil || state.Review.Candidate == nil {
		return nil, nil
	}
	if state.Review.Accepted.Report.OutputCandidate == *state.Review.Candidate && state.Review.DependenciesCurrent {
		return nil, nil
	}
	predicates, err := workflowActiveContractPredicateIDs(ctx, q, workID, subject)
	if err != nil || len(predicates) == 0 {
		return nil, err
	}
	target := workflowCorrectionReturnTarget(definition, currentStep)
	if target == "" {
		return nil, nil
	}
	seq, err := workflowReviewReportSequence(ctx, q, workID, state.Review.Accepted.ReportRef)
	if err != nil {
		return nil, err
	}
	return &WorkflowCorrectionContext{Disposition: "verification", PredicateIDs: predicates, EvidenceRefs: []string{state.Review.Accepted.ReportRef}, SourceEventID: state.Review.Accepted.ReportRef, SourceEventSeq: seq, Diagnosis: "the accepted current Review no longer covers the current candidate or depended-on revisions", Strategy: workflowReviewRecovery(fmt.Sprintf("return to step %q", target), "covering the current candidate")}, nil
}

const (
	workflowCorrectionMissingNone             = ""
	workflowCorrectionMissingVerdict          = "verdict"
	workflowCorrectionMissingAcceptedDelivery = "accepted_delivery"
	workflowCorrectionMissingGateReview       = "gate_review"
	workflowCorrectionMissingFreshReview      = "fresh_review"
)

func workflowCorrectionRequestUnavailableFailure(subject, missing string) *Failure {
	switch missing {
	case workflowCorrectionMissingAcceptedDelivery:
		return newFailure(KindInvalidOperation, subject, "request_correction requires recorded artifact production behind the current non-ok verdict", false, "record the producer step's artifact or reread the work pin")
	case workflowCorrectionMissingFreshReview:
		return newFailure(KindInvalidOperation, subject, "request_correction already consumed the accepted Review; a further correction requires a fresh accepted Review of the current candidate", false, workflowReviewRecovery("", "covering the current candidate before requesting another correction"))
	case workflowCorrectionMissingGateReview:
		return newFailure(KindInvalidOperation, subject, "request_correction at the delivery gate requires an outstanding Review correction", false, "reread the current work pin")
	default:
		return newFailure(KindInvalidOperation, subject, "request_correction requires a current non-ok verification verdict under the active workflow contract", false, "record the current verification verdict or reread the work pin")
	}
}

func workflowCorrectionRequestAdmissionForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeSeq int64, registry DefinitionRegistry) (*WorkflowCorrectionContext, string, error) {
	if workflowCorrectionReturnTarget(definition, currentStep) != "" {
		review, err := loadWorkflowReviewState(ctx, q, workID)
		if err != nil {
			return nil, workflowCorrectionMissingNone, err
		}
		if review.Accepted != nil && review.Candidate != nil && review.Accepted.Report.OutputCandidate == *review.Candidate && review.DependenciesCurrent && review.Accepted.Report.Disposition == "correction_required" {
			predicates, err := workflowActiveContractPredicateIDs(ctx, q, workID, subject)
			if err != nil {
				return nil, workflowCorrectionMissingNone, err
			}
			seq, err := workflowReviewReportSequence(ctx, q, workID, review.Accepted.ReportRef)
			if err != nil {
				return nil, workflowCorrectionMissingNone, err
			}
			consumed, err := workflowReviewCorrectionConsumed(ctx, q, workID, seq, excludeSeq)
			if err != nil {
				return nil, workflowCorrectionMissingNone, err
			}
			if consumed {
				return nil, workflowCorrectionMissingFreshReview, nil
			}
			return &WorkflowCorrectionContext{Disposition: "verification", PredicateIDs: predicates, EvidenceRefs: []string{review.Accepted.ReportRef}, SourceEventID: review.Accepted.ReportRef, SourceEventSeq: seq, Diagnosis: "the accepted current Review requires correction", Strategy: workflowReviewRecovery("perform the declared corrective work", "covering the corrected candidate")}, workflowCorrectionMissingNone, nil
		}
	}
	if workflowStepIsDeliveryGate(workflowStep(definition, currentStep)) {
		context, err := workflowDeliveryGateCorrectionContextForRegistry(ctx, q, workID, definition, currentStep, subject, registry)
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
		context, err := workflowVerdictCorrectionFromState(ctx, q, workID, definition, currentStep, subject, state)
		if err != nil || context != nil {
			return context, workflowCorrectionMissingNone, err
		}
		return nil, workflowCorrectionMissingAcceptedDelivery, nil
	}
	if workflowCorrectionReturnTarget(definition, currentStep) == "" {
		return nil, workflowCorrectionMissingNone, nil
	}
	return nil, workflowCorrectionMissingVerdict, nil
}

func workflowReviewReportSequence(ctx context.Context, q queryer, workID, reportRef string) (int64, error) {
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT effect_seq FROM effect_receipts WHERE event_id=? AND subject_type='work_item' AND subject_id=?`, reportRef, workID).Scan(&seq)
	return seq, err
}

func workflowReviewCorrectionConsumed(ctx context.Context, q queryer, workID string, reportSeq, excludeSeq int64) (bool, error) {
	var consumed int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_phase_exits WHERE work_id=? AND action_id='request_correction' AND exit_seq>? AND exit_seq<>?`, workID, reportSeq, excludeSeq).Scan(&consumed)
	return consumed != 0, err
}

func workflowCorrectionRequestAdmissionStateForRegistry(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, currentStep, subject string, excludeSeq int64, registry DefinitionRegistry) (bool, string, error) {
	context, missing, err := workflowCorrectionRequestAdmissionForRegistry(ctx, q, workID, definition, currentStep, subject, excludeSeq, registry)
	return context != nil, missing, err
}

func workflowActiveContractPredicateIDs(ctx context.Context, q queryer, workID, subject string) ([]string, error) {
	version, err := activeWorkflowContractVersion(ctx, q, workID, subject)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT predicate_id FROM workflow_contract_predicates WHERE work_id=? AND contract_version=? ORDER BY ordinal`, workID, version)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, subject, "cannot read active workflow predicates", true, "retry once the workflow contract is readable", err)
	}
	defer rows.Close()
	predicates := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		predicates = append(predicates, id)
	}
	return predicates, rows.Err()
}

func validateCorrectionRequestPayload(ctx context.Context, q queryer, workID string, payload json.RawMessage, subject string, correction *WorkflowCorrectionContext) error {
	if correction == nil {
		return newFailure(KindUnavailable, subject, "the current step admits no correction request", false, "reread_entities")
	}
	fields, err := workflowActionObject(payload)
	if err != nil {
		return err
	}
	predicates, evidence := workflowFieldStrings(fields, "predicate_ids"), workflowFieldStrings(fields, "evidence_refs")
	if workflowFieldStringDefault(fields, "diagnosis", "") == "" || workflowFieldStringDefault(fields, "strategy", "") == "" || len(predicates) == 0 || len(evidence) == 0 {
		return newFailure(KindInvalidPayload, subject, "request_correction requires diagnosis, strategy, predicate IDs, and bound evidence", false, "supply the complete correction disposition")
	}
	if fault := workflowCorrectionSchemaValuesFaultForAdmission(predicates, evidence, nil); fault != "" {
		return newFailure(KindInvalidPayload, subject, "request_correction carries correction values the closed response schema cannot represent: "+fault, false, "supply correction values the closed work_pin response schema accepts")
	}
	for _, predicate := range predicates {
		if !slices.Contains(correction.PredicateIDs, predicate) {
			return newFailure(KindInvalidPayload, subject, "request_correction names a predicate without a current non-ok verdict", false, "name only affected approved predicates")
		}
	}
	for _, ref := range evidence {
		bound, err := workflowEvidenceReferenceBound(ctx, q, workID, ref, subject)
		if err != nil {
			return err
		}
		if !bound {
			return newFailure(KindMissingEvidence, subject, "request_correction evidence is not durably bound: "+ref, false, "provide_evidence")
		}
	}
	return nil
}

func correctionReferenceStrings(values []string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, value := range values {
		if ValidReference(value) && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
