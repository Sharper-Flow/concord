package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// requireRecordedInvestigationArtifact resolves the investigation's refs in
// the caller's transaction. A comparison work item is required only when the
// Product holds another item.
func requireRecordedInvestigationArtifact(ctx context.Context, q queryer, workID string) error {
	comparisonRequired, err := productHoldsAnotherWorkItem(ctx, q, workID)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT refs FROM work_observations WHERE work_id=? ORDER BY recorded_at DESC, observation_id`, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_confirmation", "cannot inspect recorded investigation artifacts", true, "retry once the workflow evidence is readable", err)
	}
	var candidates [][]string
	for rows.Next() {
		var refsJSON string
		if err := rows.Scan(&refsJSON); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "workflow_confirmation", "cannot read recorded investigation artifact refs", true, "retry once the workflow evidence is readable", err)
		}
		var refs []string
		if json.Unmarshal([]byte(refsJSON), &refs) != nil {
			rows.Close()
			return newFailure(KindInvariantViolation, "workflow_confirmation", "recorded investigation artifact refs are malformed", false, "restore a verified backup into a new path with concord restore, then swap")
		}
		candidates = append(candidates, refs)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "workflow_confirmation", "cannot inspect recorded investigation artifacts", true, "retry once the workflow evidence is readable", err)
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "workflow_confirmation", "cannot inspect recorded investigation artifacts", true, "retry once the workflow evidence is readable", err)
	}
	if len(candidates) > 0 {
		domains, err := currentInvestigationDomains(ctx, q, workID)
		if err != nil {
			return err
		}
		for _, refs := range candidates {
			qualified, err := investigationRefsQualified(ctx, q, workID, refs, comparisonRequired, domains)
			if err != nil {
				return err
			}
			if qualified {
				return nil
			}
		}
	}
	if comparisonRequired {
		return newFailure(KindMissingEvidence, "workflow_confirmation", "premise confirmation requires a recorded investigation artifact naming a current Domain of the Product and another work item; its refs must hold the bare domain_id of the current Domain and the id of another work item", false, "call concord_work_define.observation_record with this work_id and refs containing the bare domain_id of a current Product Domain and the id of another work item")
	}
	return newFailure(KindMissingEvidence, "workflow_confirmation", "premise confirmation requires a recorded investigation artifact naming a current Domain of the Product; its ref must hold the bare domain_id of the current Domain", false, "call concord_work_define.observation_record with this work_id and refs containing the bare domain_id of a current Product Domain")
}

func investigationRefsQualified(ctx context.Context, q queryer, workID string, refs []string, comparisonRequired bool, currentDomains map[string]bool) (bool, error) {
	namesDomain, namesWork := false, false
	for _, ref := range refs {
		if !namesDomain {
			namesDomain = currentDomains[ref]
		}
		if !namesWork && ref != workID {
			known, err := investigationRefIsKnownWork(ctx, q, ref)
			if err != nil {
				return false, err
			}
			namesWork = known
		}
		if namesDomain && (namesWork || !comparisonRequired) {
			return true, nil
		}
	}
	return false, nil
}

func productHoldsAnotherWorkItem(ctx context.Context, q queryer, workID string) (bool, error) {
	var held int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM work_items other
		JOIN work_projects wp2 ON wp2.work_id = other.id
		JOIN product_projects pp2 ON pp2.project_id = wp2.project_id
		WHERE other.id <> ? AND pp2.product_id IN (
			SELECT pp.product_id FROM work_projects wp
			JOIN product_projects pp ON pp.project_id = wp.project_id
			WHERE wp.work_id = ?))`, workID, workID).Scan(&held)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_confirmation", "cannot count the Product's work items", true, "retry once the work projection is readable", err)
	}
	return held == 1, nil
}

// currentInvestigationDomains returns the current Domain IDs of the Product
// that owns the work's contract, read from the committed registry the
// transaction's knowledge-source proof binds. A work with no single owning
// Product, or a Product with no designated knowledge home, has no Domain
// registry, so no ref can name a current Domain and the confirmation answers
// with its missing-evidence guidance. When a registry exists but the
// transaction carries no proof that still binds it, the refs cannot be
// decided: the answer is a typed stale-review refusal that names the
// verification, never a report that the artifact names no Domain.
func currentInvestigationDomains(ctx context.Context, q queryer, workID string) (map[string]bool, error) {
	current := map[string]bool{}
	productID, err := architectureBindingProductID(ctx, q, workID)
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnknownScope {
			return current, nil
		}
		return nil, err
	}
	homes, err := productKnowledgeHomeCandidates(ctx, q, productID)
	if err != nil {
		return nil, err
	}
	if len(homes) == 0 {
		return current, nil
	}
	snapshot, err := lawSnapshotForProduct(ctx, q, productID, "workflow_confirmation")
	if err != nil {
		var failure *Failure
		if failureAs(err, &failure) && failure.Kind == KindUnavailable {
			return nil, err
		}
		return nil, wrapFailure(KindStaleRequiresReview, "workflow_confirmation", "the investigation artifact's Domain refs cannot be checked: this read carries no current verification of the Product's committed Domain registry", false, "verify the Product's knowledge home, then confirm the premise or reread the work pin", err)
	}
	for _, domain := range snapshot.Registry().Domains {
		if domainRegistryStatus(domain.Status) == "current" {
			current[domain.DomainID] = true
		}
	}
	return current, nil
}

func investigationRefIsKnownWork(ctx context.Context, q queryer, ref string) (bool, error) {
	var found string
	err := q.QueryRowContext(ctx, `SELECT id FROM work_items WHERE id=?`, ref).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "workflow_confirmation", "cannot resolve investigation artifact work ref", true, "retry once the work item projection is readable", err)
	}
	return true, nil
}
