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
// must exist with a nonterminal owner, the revision must exist, and a required
// binding on a revision whose freshness is not current fails closed with
// KindResearchConsumerBlocked. Recording the binding is idempotent per
// (pack, revision, consumer): a redeclared identical binding records nothing,
// and the pack's expected version advances only when a row lands. Conflicting
// redeclarations refuse; retained required pins are checked even when omitted.
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
		key := fmt.Sprintf("%s:%d", declaration.PackID, declaration.Revision)
		if seen[key] {
			return newFailure(KindInvalidPayload, "research_reliance", "binding declares one pack revision twice", false, "declare each pack revision once")
		}
		seen[key] = true

		// Issue #122: the freshness verdict is the pinned revision's, not the
		// pack summary — pack-level churn must not clear a required binding.
		var freshness sql.NullString
		var revision sql.NullInt64
		var ownerTerminal int
		err := tx.QueryRowContext(ctx, `SELECT r.revision, r.freshness, CASE WHEN w.lifecycle IN ('completed','cancelled','superseded') THEN 1 ELSE 0 END FROM active_research_packs p JOIN work_items w ON w.id=p.owner_work_id LEFT JOIN active_research_revisions r ON r.pack_id=p.pack_id AND r.revision=? WHERE p.pack_id=?`, declaration.Revision, declaration.PackID).Scan(&revision, &freshness, &ownerTerminal)
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "research_reliance", "declared research pack does not exist", false, "check the pack identifier")
		}
		if err != nil {
			return wrapFailure(KindUnavailable, "research_reliance", "cannot read declared research pack", true, "retry once the database is readable", err)
		}
		if !revision.Valid {
			return newFailure(KindProjectionNotFound, "research_reliance", "declared research revision does not exist", false, "pin an existing revision")
		}
		if ownerTerminal == 1 {
			return newFailure(KindInvalidOperation, "research_reliance", "declared research pack has a terminal owner", false, "rebind to an active pack")
		}
		// CD-0009 D6: a required consumer cannot proceed on stale or unknown
		// research. Fail closed at the boundary where reliance is declared.
		if !freshness.Valid || freshness.String == "" {
			freshness.String = string(ResearchUnknown)
		}
		if declaration.Required && freshness.String != string(ResearchCurrent) {
			return newFailure(KindResearchConsumerBlocked, "research_reliance", fmt.Sprintf("required research binding on %s freshness", freshness.String), false, "rebind to a current revision or declare the binding non-required")
		}

		insert, err := tx.ExecContext(ctx, `INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES(?,?,?,?,?,?) ON CONFLICT(pack_id,revision,consumer_work_id) DO NOTHING`,
			declaration.PackID, declaration.Revision, consumerWorkID, declaration.UseRole, boolInt(declaration.Required), now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return wrapFailure(KindUnavailable, "research_reliance", "cannot record research consumer binding", true, "retry once the database is writable", err)
		}
		inserted, err := insert.RowsAffected()
		if err != nil {
			return researchUnavailable("cannot inspect research consumer insertion", err)
		}
		if inserted == 0 {
			var role string
			var required bool
			if err := tx.QueryRowContext(ctx, `SELECT use_role,required FROM active_research_consumers WHERE pack_id=? AND revision=? AND consumer_work_id=?`, declaration.PackID, declaration.Revision, consumerWorkID).Scan(&role, &required); err != nil {
				return researchUnavailable("cannot inspect existing research consumer binding", err)
			}
			if role != string(declaration.UseRole) || required != declaration.Required {
				return newFailure(KindInvalidPayload, "research_reliance", "research binding redeclaration conflicts with the retained pin", false, "declare the retained use_role and requiredness")
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE active_research_packs SET expected_version=expected_version+1, updated_at=? WHERE pack_id=?`, now.UTC().Format(time.RFC3339Nano), declaration.PackID); err != nil {
				return wrapFailure(KindUnavailable, "research_reliance", "cannot advance research pack version", true, "retry once the database is writable", err)
			}
		}
	}
	return checkRequiredResearchRelianceTx(ctx, tx, consumerWorkID)
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
	return newFailure(KindResearchConsumerBlocked, "research_reliance", fmt.Sprintf("required research binding %s on %s freshness", packID, freshness), false, "restore current freshness or explicitly release the retained required pin")
}
