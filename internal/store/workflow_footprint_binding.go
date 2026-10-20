package store

import (
	"context"
	"database/sql"
)

// workflowFootprintBinding names the size assessment whose footprint checks a
// small-composition contract owes. The approval or successor contract that
// carries it is the only record of the binding; a later assessment never
// changes what an approved contract owes.
type workflowFootprintBinding struct {
	ContractVersion   int64  `json:"contract_version"`
	AssessmentEventID string `json:"assessment_event_id"`
}

// deriveWorkflowFootprintBindingTx derives the binding a contract version of
// definition carries: none outside the small composition, and otherwise the
// recorded assessment that sizes the work item small. live also requires
// that assessment's checks to stay executable; replay checks the recorded
// identity only, since executable-check support is current code, not history.
func deriveWorkflowFootprintBindingTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, contractVersion int64, live bool) (*workflowFootprintBinding, error) {
	if workflowCompositionSize(definition.Ref, definition.Version) != WorkSizeSmall {
		return nil, nil
	}
	decision, sized, err := readEffectiveWorkSize(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if !sized || decision.Effective != WorkSizeSmall {
		return nil, newFailure(KindInvalidOperation, "workflow_action", "a small-composition contract requires the work item's recorded size to be small", false, "resize the work item so its successor takes the composition its size selects")
	}
	if retention := workflowFullRetentionFor(decision.Model.Footprint); live && retention != nil {
		return nil, newFailure(KindInvalidOperation, "workflow_action", "the size assessment no longer proves every footprint check executable ("+retention.Reason+")", false, "rerun the size assessment once every footprint check has an executing suite, or resize the work item")
	}
	var eventID string
	err = q.QueryRowContext(ctx, `SELECT event_id FROM work_size_assessments WHERE work_id=? AND sequence=?`, workID, decision.Model.Sequence).Scan(&eventID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the size assessment", true, "retry once the database is readable", err)
	}
	return &workflowFootprintBinding{ContractVersion: contractVersion, AssessmentEventID: eventID}, nil
}

// checkWorkflowFootprintBindingTx refuses a contract whose recorded binding is
// not the one its definition and the size record at this point derive.
func checkWorkflowFootprintBindingTx(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, contractVersion int64, recorded *workflowFootprintBinding, live bool) error {
	want, err := deriveWorkflowFootprintBindingTx(ctx, q, workID, definition, contractVersion, live)
	if err != nil {
		return err
	}
	if (want == nil) != (recorded == nil) || (want != nil && *want != *recorded) {
		return newFailure(KindInvalidPayload, "fold_event", "footprint_binding does not name the size assessment the contract owes", false, "derive the binding from the recorded size assessment")
	}
	return nil
}

// readWorkflowFootprintBindingTx reads the binding the approval or successor
// event of contractVersion recorded, or nil when that contract carries none.
func readWorkflowFootprintBindingTx(ctx context.Context, q queryer, workID string, contractVersion int64) (*workflowFootprintBinding, error) {
	var assessment sql.NullString
	err := q.QueryRowContext(ctx, `SELECT coalesce(json_extract(payload,'$.footprint_binding.assessment_event_id'),json_extract(payload,'$.successor_contract.footprint_binding.assessment_event_id')) FROM domain_events
		WHERE subject_type='work_item' AND subject_id=?
		  AND ((kind=? AND json_extract(payload,'$.contract_version')=?) OR (kind=? AND json_extract(payload,'$.successor_contract.contract_version')=?))
		ORDER BY seq DESC LIMIT 1`, workID, WorkflowContractApproved, contractVersion, WorkflowContractSuperseded, contractVersion).Scan(&assessment)
	if err == sql.ErrNoRows || (err == nil && !assessment.Valid) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_admission", "cannot read the contract's footprint binding", true, "retry once the database is readable", err)
	}
	return &workflowFootprintBinding{ContractVersion: contractVersion, AssessmentEventID: assessment.String}, nil
}
