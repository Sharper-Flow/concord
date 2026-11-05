package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
