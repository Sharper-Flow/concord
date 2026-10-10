package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

type ResearchRetirementCandidate struct {
	PackID          string `json:"pack_id"`
	ExpectedVersion int64  `json:"expected_version"`
}

type RetireResearchPacksRequest struct {
	ProductID  string                        `json:"product_id"`
	Candidates []ResearchRetirementCandidate `json:"candidates"`
	DryRun     bool                          `json:"dry_run"`
}

type ResearchRetirementClassification string

const (
	ResearchRetirementEligible        ResearchRetirementClassification = "eligible"
	ResearchRetirementRetired         ResearchRetirementClassification = "retired"
	ResearchRetirementProtected       ResearchRetirementClassification = "protected"
	ResearchRetirementVersionConflict ResearchRetirementClassification = "version_conflict"
)

type ResearchRetirementCandidateResult struct {
	PackID           string                           `json:"pack_id"`
	OwnerWorkID      string                           `json:"owner_work_id"`
	ExpectedVersion  int64                            `json:"expected_version"`
	CurrentVersion   int64                            `json:"current_version"`
	Classification   ResearchRetirementClassification `json:"classification"`
	ProtectionReason string                           `json:"protection_reason,omitempty"`
}

type RetireResearchPacksResult struct {
	DryRun     bool                                `json:"dry_run"`
	Candidates []ResearchRetirementCandidateResult `json:"candidates"`
}

// RetireResearchPacksWithinTx classifies only the explicitly named packs, in
// request order. All owners must be visible in ProductID through the existing
// Project membership derivation before any deletion. Missing packs refuse the
// batch; stale versions and protected packs are returned without deletion.
// The caller owns authorization, idempotency, and commit/rollback. DryRun never
// writes, including receipts. Any error must abort the caller's transaction.
func RetireResearchPacksWithinTx(ctx context.Context, transaction *Transaction, req RetireResearchPacksRequest) (RetireResearchPacksResult, error) {
	tx, err := transactionSQL(transaction, "research_retire")
	if err != nil {
		return RetireResearchPacksResult{}, err
	}
	if req.ProductID == "" || len(req.Candidates) < 1 || len(req.Candidates) > 100 {
		return RetireResearchPacksResult{}, researchInvalid("product_id and 1-100 retirement candidates are required")
	}
	seen := make(map[string]bool, len(req.Candidates))
	for _, candidate := range req.Candidates {
		if candidate.PackID == "" || candidate.ExpectedVersion < 1 {
			return RetireResearchPacksResult{}, researchInvalid("retirement candidates require pack_id and positive expected_version")
		}
		if seen[candidate.PackID] {
			return RetireResearchPacksResult{}, researchInvalid("retirement candidates must name distinct packs")
		}
		seen[candidate.PackID] = true
	}
	return retireResearchPacksTx(ctx, tx, req)
}

func retireResearchPacksTx(ctx context.Context, tx *sql.Tx, req RetireResearchPacksRequest) (RetireResearchPacksResult, error) {
	out := RetireResearchPacksResult{DryRun: req.DryRun, Candidates: make([]ResearchRetirementCandidateResult, 0, len(req.Candidates))}
	owners := make([]string, 0, len(req.Candidates))
	for _, candidate := range req.Candidates {
		result, err := classifyResearchRetirementTx(ctx, tx, candidate)
		if err != nil {
			return RetireResearchPacksResult{}, err
		}
		out.Candidates = append(out.Candidates, result)
		owners = append(owners, result.OwnerWorkID)
	}
	products, err := productsForWorkIDs(ctx, tx, owners)
	if err != nil {
		return RetireResearchPacksResult{}, err
	}
	for _, result := range out.Candidates {
		if !slices.Contains(products[result.OwnerWorkID], req.ProductID) {
			return RetireResearchPacksResult{}, newFailure(KindUnauthorized, "research_retire", "research pack owner is outside the requested Product scope", false, "select packs whose owners belong to the requested Product")
		}
	}
	if req.DryRun {
		return out, nil
	}
	for i := range out.Candidates {
		result := &out.Candidates[i]
		if result.Classification != ResearchRetirementEligible {
			continue
		}
		deleted, err := tx.ExecContext(ctx, `DELETE FROM active_research_packs WHERE pack_id=? AND expected_version=?`, result.PackID, result.ExpectedVersion)
		if err != nil {
			return RetireResearchPacksResult{}, researchUnavailable("cannot retire research pack", err)
		}
		n, err := deleted.RowsAffected()
		if err != nil {
			return RetireResearchPacksResult{}, researchUnavailable("cannot verify research retirement", err)
		}
		if n != 1 {
			return RetireResearchPacksResult{}, newFailure(KindInvariantViolation, "research_retire", "research retirement version fence did not delete exactly one pack", false, "abort the transaction and reload the retirement candidates")
		}
		result.Classification = ResearchRetirementRetired
	}
	return out, nil
}

func classifyResearchRetirementTx(ctx context.Context, tx *sql.Tx, candidate ResearchRetirementCandidate) (ResearchRetirementCandidateResult, error) {
	out := ResearchRetirementCandidateResult{PackID: candidate.PackID, ExpectedVersion: candidate.ExpectedVersion}
	var lifecycle sql.NullString
	var activePin bool
	err := tx.QueryRowContext(ctx, `SELECT p.owner_work_id,p.expected_version,w.lifecycle,
		EXISTS(SELECT 1 FROM active_research_consumers c LEFT JOIN work_items consumer ON consumer.id=c.consumer_work_id
			WHERE c.pack_id=p.pack_id AND (consumer.id IS NULL OR consumer.lifecycle NOT IN `+terminalLifecycleSQLList()+`))
		FROM active_research_packs p LEFT JOIN work_items w ON w.id=p.owner_work_id WHERE p.pack_id=?`, candidate.PackID).Scan(&out.OwnerWorkID, &out.CurrentVersion, &lifecycle, &activePin)
	if err == sql.ErrNoRows {
		return out, researchNotFound(fmt.Sprintf("research retirement candidate %s does not exist", candidate.PackID))
	}
	if err != nil {
		return out, researchUnavailable("cannot inspect research retirement candidate", err)
	}
	if !lifecycle.Valid {
		return out, newFailure(KindInvariantViolation, "research_retire", "research retirement candidate has no owner work item", false, "repair the research owner reference before retirement")
	}
	switch {
	case out.CurrentVersion != candidate.ExpectedVersion:
		out.Classification = ResearchRetirementVersionConflict
	case !isTerminalLifecycle(lifecycle.String):
		out.Classification = ResearchRetirementProtected
		out.ProtectionReason = "owner_active"
	case activePin:
		out.Classification = ResearchRetirementProtected
		out.ProtectionReason = "active_pin"
	default:
		out.Classification = ResearchRetirementEligible
	}
	return out, nil
}
