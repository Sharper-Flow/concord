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
	if live {
		if err := checkWorkflowFootprintIdentityTx(ctx, q, workID, decision.Model.Footprint); err != nil {
			return nil, err
		}
	}
	var eventID string
	err = q.QueryRowContext(ctx, `SELECT event_id FROM work_size_assessments WHERE work_id=? AND sequence=?`, workID, decision.Model.Sequence).Scan(&eventID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_action", "cannot read the size assessment", true, "retry once the database is readable", err)
	}
	return &workflowFootprintBinding{ContractVersion: contractVersion, AssessmentEventID: eventID}, nil
}

// The assessment's shard declaration and behavior matches belong to its
// indexed knowledge commit, not the candidate's Git HEAD. Reuse the footprint
// join to compare that identity in the same transaction as approval.
func checkWorkflowFootprintIdentityTx(ctx context.Context, q queryer, workID string, recorded WorkSizeFootprint) error {
	product, err := architectureBindingProductID(ctx, q, workID)
	if err != nil {
		return err
	}
	home, err := resolveKnowledgeQueryHome(ctx, q, product, "", KnowledgeHome{}, "workflow_action")
	if err != nil {
		return err
	}
	selected, err := footprintSelectTx(ctx, q, DomainFootprintRequest{Product: product}, home, nil, nil, recorded.KnowledgeCommit)
	if err != nil {
		return err
	}
	current := selected.result
	mapMatches := (recorded.PackageMapContentHash == nil) == (current.PackageMapContentHash == nil)
	if mapMatches && recorded.PackageMapContentHash != nil {
		mapMatches = *recorded.PackageMapContentHash == *current.PackageMapContentHash
	}
	if recorded.RegistryContentHash != current.RegistryContentHash || !mapMatches {
		return newFailure(KindStaleContext, "workflow_action", "the footprint registry or package map changed after the size assessment", true, "rerun the size assessment before approving or superseding the contract")
	}
	return nil
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
// event of contractVersion recorded, with that event's sequence: the frontier
// after which the contract's proof must be produced. The binding is nil when
// that contract carries none.
func readWorkflowFootprintBindingTx(ctx context.Context, q queryer, workID string, contractVersion int64) (*workflowFootprintBinding, int64, error) {
	return readWorkflowFootprintBindingBeforeTx(ctx, q, workID, contractVersion, 0)
}

func readWorkflowFootprintBindingBeforeTx(ctx context.Context, q queryer, workID string, contractVersion, beforeSeq int64) (*workflowFootprintBinding, int64, error) {
	var assessment sql.NullString
	var seq int64
	err := q.QueryRowContext(ctx, `SELECT coalesce(json_extract(payload,'$.footprint_binding.assessment_event_id'),json_extract(payload,'$.successor_contract.footprint_binding.assessment_event_id')),seq FROM domain_events
		WHERE subject_type='work_item' AND subject_id=?
		  AND ((kind=? AND json_extract(payload,'$.contract_version')=?) OR (kind=? AND json_extract(payload,'$.successor_contract.contract_version')=?))
		  AND (?=0 OR seq<?)
		ORDER BY seq DESC LIMIT 1`, workID, WorkflowContractApproved, contractVersion, WorkflowContractSuperseded, contractVersion, beforeSeq, beforeSeq).Scan(&assessment, &seq)
	if err == sql.ErrNoRows {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, wrapFailure(KindUnavailable, "workflow_admission", "cannot read the contract's footprint binding", true, "retry once the database is readable", err)
	}
	if !assessment.Valid {
		return nil, seq, nil
	}
	return &workflowFootprintBinding{ContractVersion: contractVersion, AssessmentEventID: assessment.String}, seq, nil
}
