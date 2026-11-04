package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"
)

type WorkflowReadContract struct {
	Version             int64                        `json:"version"`
	Premise             string                       `json:"premise"`
	LegislativeSubjects []string                     `json:"legislative_subjects"`
	RouteConventions    []string                     `json:"route_conventions"`
	SpecMandate         []string                     `json:"spec_mandate"`
	LawModifies         []string                     `json:"law_modifies"`
	RigorClass          string                       `json:"rigor_class"`
	ChangesProductTruth *bool                        `json:"changes_product_truth,omitempty"`
	ArchitectureBinding *WorkflowArchitectureBinding `json:"architecture_binding,omitempty"`
	ApprovedAt          string                       `json:"approved_at,omitempty"`
	ApprovedBy          string                       `json:"approved_by,omitempty"`
	SupersededBy        *int64                       `json:"superseded_by,omitempty"`
	// DecisionDomains carries the contract's declared classification fact.
	// nil is historical absence (a NULL projection column), never engineering;
	// an explicit engineering contract reads as the empty set. omitzero drops
	// only the nil fact from the encoding.
	DecisionDomains []string `json:"decision_domains,omitzero"`
}

// readWorkflowContractVersion reads one recorded contract version and every
// enrichment keyed by it. It answers sql.ErrNoRows when the work never had
// that version. Historical contracts do not infer a Product-truth flag.
func readWorkflowContractVersion(ctx context.Context, q queryer, workID string, version int64) (WorkflowReadContract, error) {
	var contract WorkflowReadContract
	var routes, mandates, modifies, subjects string
	var supersededBy sql.NullInt64
	var decisionDomains sql.NullString
	var truth sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT contract_version,premise,route_conventions,spec_mandate,law_modifies,legislative_subjects,rigor_class,decision_domains,approved_at,approved_by,superseded_by,changes_product_truth FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, version).Scan(&contract.Version, &contract.Premise, &routes, &mandates, &modifies, &subjects, &contract.RigorClass, &decisionDomains, &contract.ApprovedAt, &contract.ApprovedBy, &supersededBy, &truth)
	if err == sql.ErrNoRows {
		return contract, err
	}
	if err != nil {
		return contract, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow contract", true, "retry once the database is readable", err)
	}
	if json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil || json.Unmarshal([]byte(subjects), &contract.LegislativeSubjects) != nil || contract.LegislativeSubjects == nil || !validLegislativeSubjects(contract.LegislativeSubjects) {
		return contract, newFailure(KindInvariantViolation, "workflow_read", "workflow contract projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	contract.RouteConventions = nonNilStrings(contract.RouteConventions)
	contract.SpecMandate = nonNilStrings(contract.SpecMandate)
	contract.LawModifies = nonNilStrings(contract.LawModifies)
	// NULL is historical absence and stays nil; a recorded array decodes
	// as the declared set, where [] reads as engineering.
	if decisionDomains.Valid {
		if json.Unmarshal([]byte(decisionDomains.String), &contract.DecisionDomains) != nil {
			return contract, newFailure(KindInvariantViolation, "workflow_read", "workflow contract projection contains a malformed decision domain set", false, "restore a verified backup into a new path with concord restore, then swap")
		}
	}
	if supersededBy.Valid {
		contract.SupersededBy = &supersededBy.Int64
	}
	// The approval fold records its own Product-truth rule on the row. A NULL
	// marks a contract recorded before the step-plan cutover; those rows keep
	// historical absence rather than inferring from a later plan.
	if truth.Valid {
		recorded := truth.Int64 == 1
		contract.ChangesProductTruth = &recorded
	}
	if contract.ArchitectureBinding, err = readWorkflowArchitectureBinding(ctx, q, workID, contract.Version); err != nil {
		return contract, err
	}
	return contract, nil
}

type WorkflowReadParkedDelivery struct {
	WorkID       string `json:"work_id"`
	StepID       string `json:"step_id"`
	ResumeAction string `json:"resume_action"`
	// ParkedSeconds is a read-time display age derived from the work item's
	// last update. Elapsed time holds no authority here: an old park is
	// labeled, never resolved by clock.
	ParkedSeconds int64 `json:"parked_seconds"`
	// Unreconciled marks a terminal instance that left the delivery gate
	// uncrossed: cancelled or superseded while a delivery was still owed.
	Unreconciled bool `json:"unreconciled"`
}

// WorkflowReadBacklogAlignment is the recorded search, not inferred relations.
type WorkflowReadBacklogAlignment struct {
	Searched          string   `json:"searched"`
	Outcome           string   `json:"outcome"`
	RelatedIDs        []string `json:"related_ids"`
	AllMatchesRemoved bool     `json:"all_matches_removed"`
}

// readLatestWorkflowProposal reads the latest recorded proposal, or nil when
// none is recorded. A record with malformed arrays is an invariant
// violation; op names the reader in every failure.
func readLatestWorkflowProposal(ctx context.Context, q queryer, workID, op string) (*WorkflowProposalRecord, error) {
	var proposal WorkflowProposalRecord
	var affected, outcomes, constraints, questions string
	err := q.QueryRowContext(ctx, `SELECT work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at FROM workflow_proposal_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(&proposal.WorkVersion, &proposal.Problem, &affected, &proposal.Stakes, &outcomes, &constraints, &questions, &proposal.RecordedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, op, "cannot read latest workflow proposal record", true, "retry once the database is readable", err)
	}
	if json.Unmarshal([]byte(affected), &proposal.Affected) != nil || json.Unmarshal([]byte(outcomes), &proposal.UserOutcomes) != nil || json.Unmarshal([]byte(constraints), &proposal.Constraints) != nil || json.Unmarshal([]byte(questions), &proposal.OpenQuestions) != nil {
		return nil, newFailure(KindInvariantViolation, op, "proposal record projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	return &proposal, nil
}

func workflowCurrentProductTruth(contract *WorkflowReadContract, plan *WorkflowStepPlan) bool {
	if contract != nil && contract.ChangesProductTruth != nil {
		return *contract.ChangesProductTruth
	}
	if plan == nil {
		return false
	}
	truth := plan.Type != WorkflowStepPlanResearch
	if contract != nil {
		contract.ChangesProductTruth = &truth
	}
	return truth
}

func readWorkflowBacklogAlignment(ctx context.Context, q queryer, workID string) (*WorkflowReadBacklogAlignment, error) {
	var alignment WorkflowReadBacklogAlignment
	err := q.QueryRowContext(ctx, `SELECT searched,outcome FROM workflow_backlog_alignment WHERE work_id=? AND related_work_id IS NULL`, workID).Scan(&alignment.Searched, &alignment.Outcome)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow backlog alignment", true, "retry once the database is readable", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT related_work_id FROM workflow_backlog_alignment WHERE work_id=? AND related_work_id IS NOT NULL ORDER BY related_work_id`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow backlog alignment matches", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	alignment.RelatedIDs = []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow backlog alignment match", true, "retry once the database is readable", err)
		}
		alignment.RelatedIDs = append(alignment.RelatedIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow backlog alignment matches", true, "retry once the database is readable", err)
	}
	alignment.AllMatchesRemoved = alignment.Outcome == "related_found" && len(alignment.RelatedIDs) == 0
	return &alignment, nil
}

// parkedDeliveryRead derives the CD-0166 parked-delivery observation for a
// work item whose current step is the delivery gate. ParkedSeconds is a
// read-time display age taken from the work item's last update; elapsed time
// holds no authority here, so an old park is labeled and never resolved by
// clock. A terminal instance that left the gate uncrossed reads as unreconciled.
func parkedDeliveryRead(ctx context.Context, q queryer, plan *WorkflowStepPlan, workID, currentStep, state string) *WorkflowReadParkedDelivery {
	if plan == nil || !slices.Contains(WorkflowStepPlanTemplateActions(plan.Type, currentStep), "record_delivery") {
		return nil
	}
	parked := &WorkflowReadParkedDelivery{WorkID: workID, StepID: currentStep, ResumeAction: "record_delivery"}
	var updatedAt string
	if err := q.QueryRowContext(ctx, `SELECT updated_at FROM work_items WHERE id=?`, workID).Scan(&updatedAt); err == nil {
		if touched, parseErr := time.Parse(time.RFC3339, updatedAt); parseErr == nil {
			if age := int64(time.Now().UTC().Sub(touched).Seconds()); age > 0 {
				parked.ParkedSeconds = age
			}
		}
	}
	parked.Unreconciled = state == "cancelled" || state == "superseded"
	return parked
}
