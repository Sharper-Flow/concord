package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CD-0025: research reliance is declared at the workflow boundary where a
// consumer starts relying on a pack, and the engine proves it inside the same
// transaction as the action. CD-0009 D6's consequential-boundary query is this
// check, not a PM4 blocker and never a heuristic.

// ResearchBindingDeclaration is one declared reliance carried by a workflow
// action. The consumer is always the action's own work item.
type ResearchBindingDeclaration struct {
	PackID   string          `json:"pack_id"`
	Revision int64           `json:"revision"`
	UseRole  ResearchUseRole `json:"use_role"`
	Required bool            `json:"required"`
}

// BindResearchRelianceTx validates declared research bindings and records the
// consumer pin inside the caller's transaction. For each declaration: the pack
// must exist, the revision must exist, and a required
// binding on a revision whose freshness is not current fails closed with
// KindResearchConsumerBlocked. Each consumer holds one pin per pack through
// this route. An identical declaration is a no-op; a changed declaration replaces
// the pin and advances the pack version once. Omitted required pins still gate.
func BindResearchRelianceTx(ctx context.Context, tx *sql.Tx, consumerWorkID string, declarations []ResearchBindingDeclaration, now time.Time) error {
	if len(declarations) > 16 {
		return newFailure(KindInvalidPayload, "research_reliance", "at most 16 research bindings may be declared on one action", false, "declare fewer bindings")
	}
	seen := map[string]bool{}
	for _, declaration := range declarations {
		if declaration.PackID == "" || declaration.Revision < 1 {
			return newFailure(KindInvalidPayload, "research_reliance", "binding requires a pack and a revision of at least 1", false, "supply pack_id and revision")
		}
		if !validResearchUseRole(declaration.UseRole) {
			return newFailure(KindInvalidPayload, "research_reliance", "binding use_role is not recognized", false, "supply context, design_input, verification_basis, or decision_basis")
		}
		key := declaration.PackID
		if seen[key] {
			return newFailure(KindInvalidPayload, "research_reliance", "binding declares one pack twice", false, "declare one revision per pack")
		}
		seen[key] = true

		// Issue #122: the freshness verdict is the pinned revision's, not the
		// pack summary — pack-level churn must not clear a required binding.
		var freshness sql.NullString
		var revision sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT r.revision, r.freshness FROM active_research_packs p LEFT JOIN active_research_revisions r ON r.pack_id=p.pack_id AND r.revision=? WHERE p.pack_id=?`, declaration.Revision, declaration.PackID).Scan(&revision, &freshness)
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "research_reliance", "declared research pack does not exist", false, "check the pack identifier")
		}
		if err != nil {
			return wrapFailure(KindUnavailable, "research_reliance", "cannot read declared research pack", true, "retry once the database is readable", err)
		}
		if !revision.Valid {
			return newFailure(KindProjectionNotFound, "research_reliance", "declared research revision does not exist", false, "pin an existing revision")
		}
		// CD-0009 D6: a required consumer cannot proceed on stale or unknown
		// research. Fail closed at the boundary where reliance is declared.
		if !freshness.Valid || freshness.String == "" {
			freshness.String = string(ResearchUnknown)
		}
		if declaration.Required && freshness.String != string(ResearchCurrent) {
			return newFailure(KindResearchConsumerBlocked, "research_reliance", fmt.Sprintf("required research binding on %s freshness", freshness.String), false, "rebind to a current revision or declare the binding non-required")
		}

		if err := replaceResearchConsumerPinTx(ctx, tx, consumerWorkID, declaration, now); err != nil {
			return err
		}
	}
	return checkRequiredResearchRelianceTx(ctx, tx, consumerWorkID)
}

func replaceResearchConsumerPinTx(ctx context.Context, tx *sql.Tx, consumerWorkID string, declaration ResearchBindingDeclaration, now time.Time) error {
	var pins, identical int
	err := tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(CASE WHEN revision=? AND use_role=? AND required=? THEN 1 ELSE 0 END),0) FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, declaration.Revision, declaration.UseRole, boolInt(declaration.Required), declaration.PackID, consumerWorkID).Scan(&pins, &identical)
	if err != nil {
		return researchUnavailable("cannot inspect existing research consumer pins", err)
	}
	if pins == 1 && identical == 1 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, declaration.PackID, consumerWorkID); err != nil {
		return researchUnavailable("cannot replace research consumer pins", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES(?,?,?,?,?,?)`, declaration.PackID, declaration.Revision, consumerWorkID, declaration.UseRole, boolInt(declaration.Required), now.UTC().Format(time.RFC3339Nano)); err != nil {
		return researchUnavailable("cannot record research consumer pin", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE active_research_packs SET expected_version=expected_version+1, updated_at=? WHERE pack_id=?`, now.UTC().Format(time.RFC3339Nano), declaration.PackID); err != nil {
		return researchUnavailable("cannot advance research pack version", err)
	}
	return nil
}

func checkRequiredResearchRelianceTx(ctx context.Context, tx *sql.Tx, consumerWorkID string) error {
	var packID, freshness string
	err := tx.QueryRowContext(ctx, `SELECT c.pack_id,COALESCE(r.freshness,'unknown') FROM active_research_consumers c LEFT JOIN active_research_revisions r ON r.pack_id=c.pack_id AND r.revision=c.revision JOIN work_items w ON w.id=c.consumer_work_id WHERE c.consumer_work_id=? AND c.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded') AND COALESCE(r.freshness,'unknown')<>'current' ORDER BY c.pack_id,c.revision LIMIT 1`, consumerWorkID).Scan(&packID, &freshness)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return researchUnavailable("cannot inspect retained required research pins", err)
	}
	return newFailure(KindResearchConsumerBlocked, "research_reliance", fmt.Sprintf("required research binding %s on %s freshness", packID, freshness), false, "use research_bindings to rebind to a current revision or declare the binding non-required, or restore current freshness")
}
