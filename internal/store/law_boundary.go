package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type LawConflict struct {
	SourceLawID string `json:"source_law_id"`
	TargetLawID string `json:"target_law_id"`
}

// QueryLawConflictsAtHome reads only the derived projection for one resolved
// Git home. It is intentionally bounded and cannot create rows or events.
func (s *Store) QueryLawConflictsAtHome(ctx context.Context, homeProjectID, homeLocatorID string, lawIDs []string) ([]LawConflict, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "query_law_conflicts", "store is not open", false, "open a store before checking law conflicts")
	}
	return queryLawConflictsAtHome(ctx, s.db, homeProjectID, homeLocatorID, lawIDs)
}

func queryLawConflictsAtHome(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, lawIDs []string) ([]LawConflict, error) {
	if len(lawIDs) > 32 {
		return nil, newFailure(KindInvalidPayload, "query_law_conflicts", "law conflict query exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	if len(lawIDs) == 0 {
		return []LawConflict{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(lawIDs)), ",")
	rows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(append([]any{homeProjectID, homeLocatorID}, stringArgs(lawIDs)...), stringArgs(lawIDs)...)...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "query_law_conflicts", "cannot read derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	defer rows.Close()
	conflicts := make([]LawConflict, 0)
	for rows.Next() {
		if len(conflicts) == 32 {
			_ = rows.Close()
			return nil, newFailure(KindInvalidPayload, "query_law_conflicts", "derived law conflict query exceeds the bounded result size", false, "reduce the law set or resolve conflicts before retrying")
		}
		var conflict LawConflict
		if err := rows.Scan(&conflict.SourceLawID, &conflict.TargetLawID); err != nil {
			return nil, wrapFailure(KindUnavailable, "query_law_conflicts", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
		}
		conflicts = append(conflicts, conflict)
	}
	return conflicts, rows.Err()
}

// CheckMandatedLawsAtHome performs the planning/completion law boundary check
// against an already resolved canonical home. Planning may use the explicit
// amendment path; completion never does.
func (s *Store) CheckMandatedLawsAtHome(ctx context.Context, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "check_mandated_laws", "store is not open", false, "open a store before checking mandated laws")
	}
	return checkMandatedLawsQuery(ctx, s.db, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func checkMandatedLawsTx(ctx context.Context, tx *sql.Tx, workID string, mandated, modified []string, allowAmendment bool) error {
	if len(mandated) == 0 {
		return validateLawModificationSubset(mandated, modified)
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, tx, workID)
	if err != nil {
		return err
	}
	return checkMandatedLawsTxAtHome(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func checkMandatedLawsTxAtHome(ctx context.Context, tx *sql.Tx, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	if tx == nil {
		return newFailure(KindUnavailable, "check_mandated_laws", "transaction is not open", false, "open a mutation transaction")
	}
	return checkMandatedLawsQuery(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func checkMandatedLawsQuery(ctx context.Context, q queryer, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) error {
	if homeProjectID == "" || homeLocatorID == "" {
		return newFailure(KindUnknownScope, "check_mandated_laws", "canonical law home is incomplete", false, "resolve one canonical Git knowledge home")
	}
	if len(mandated) > 32 || len(modified) > 32 {
		return newFailure(KindInvalidPayload, "check_mandated_laws", "law mandate exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	if err := validateLawModificationSubset(mandated, modified); err != nil {
		return err
	}
	if len(mandated) == 0 {
		return nil
	}
	// CD-0200: a mandated law resolves across the Product's registered
	// source set. A one-element set keeps the single-home path below
	// unchanged; a larger set federates the acceptance and conflict checks.
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return err
	}
	if productID != "" {
		sources, err := resolveKnowledgeQuerySources(ctx, q, productID, "check_mandated_laws")
		if err != nil {
			return err
		}
		if len(sources) > 1 {
			return checkMandatedLawsAcrossSources(ctx, q, sources, mandated, modified, allowAmendment)
		}
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(mandated)), ",")
	args := []any{homeProjectID, homeLocatorID}
	for _, id := range mandated {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT law_id,status,authority_tier FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholders+`) LIMIT 33`, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the derived law subjects", true, "retry once the knowledge projection is readable", err)
	}
	accepted := map[string]bool{}
	authority := map[string]string{}
	for rows.Next() {
		if len(accepted) == 32 {
			_ = rows.Close()
			return newFailure(KindInvalidPayload, "check_mandated_laws", "derived law subject query exceeds the bounded result size", false, "reduce the law mandate before retrying")
		}
		var id, status, tier string
		if err := rows.Scan(&id, &status, &tier); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law subject", true, "retry once the knowledge projection is readable", err)
		}
		if status == "accepted" {
			accepted[id] = true
		}
		authority[id] = tier
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law subjects", true, "retry once the knowledge projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law subjects", true, "retry once the knowledge projection is readable", err)
	}
	missing := make([]string, 0)
	for _, id := range mandated {
		if !accepted[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) != 0 {
		failure := newFailure(KindProjectionNotFound, "check_mandated_laws", "a mandated law is unknown or not currently accepted: "+strings.Join(missing, ","), false, "publish and rebuild the accepted Git law projection")
		failure.CandidateIDs = missing
		return failure
	}
	conflictRows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(append([]any{homeProjectID, homeLocatorID}, stringArgs(mandated)...), stringArgs(mandated)...)...)
	if err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	modifiedSet := make(map[string]bool, len(modified))
	for _, id := range modified {
		modifiedSet[id] = true
	}
	conflicts := 0
	for conflictRows.Next() {
		if conflicts == 32 {
			_ = conflictRows.Close()
			return newFailure(KindInvalidPayload, "check_mandated_laws", "derived law conflict query exceeds the bounded result size", false, "reduce the law set or resolve conflicts before retrying")
		}
		var source, target string
		if err := conflictRows.Scan(&source, &target); err != nil {
			conflictRows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
		}
		conflicts++
		// The tier gates the law the contract revises, not the law it leaves
		// alone. A contract that brings a derived record into conformance with
		// an untouched legislated commitment resolves the conflict without
		// changing anything the operator legislated, so it carries a contract
		// revision line. Revising a legislated endpoint still meets the
		// refusal that sends the conflict to an operator checkpoint.
		revisesLegislated := (modifiedSet[source] && authority[source] != "derived") ||
			(modifiedSet[target] && authority[target] != "derived")
		if !allowAmendment || (!modifiedSet[source] && !modifiedSet[target]) || revisesLegislated {
			_ = conflictRows.Close()
			return newFailure(KindRelationConflict, "check_mandated_laws", fmt.Sprintf("mandated laws have an unresolved explicit conflict: %s and %s", source, target), false, "resolve the Git law conflict or declare and approve the amendment path")
		}
	}
	if err := conflictRows.Err(); err != nil {
		conflictRows.Close()
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	if err := conflictRows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	return nil
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

// validateDerivedLawModification holds the tier-scoped half of the
// Product-truth boundary that CD-0041 D5 states and the CON-336 decision
// (obs:fb9642cbd1700ec6) resolved: a contract that does not change Product
// truth may revise derived law in-contract, and nothing else. It reads each
// modified law's authority_tier from the accepted Git law projection through
// the work's canonical law home, so the approve path and the fold answer with
// one query and cannot drift. A missing name or a non-derived tier is a
// refusal; the caller's recovery hint names the Product-changing route.
func validateDerivedLawModification(ctx context.Context, q queryer, workID string, modified []string) error {
	if len(modified) == 0 {
		return nil
	}
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return err
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(modified)), ",")
	args := []any{homeProjectID, homeLocatorID}
	for _, id := range modified {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT law_id,authority_tier FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholders+`) LIMIT 33`, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "check_derived_law_modification", "cannot read the law authority tiers", true, "retry once the law projection is readable", err)
	}
	tiers := map[string]string{}
	for rows.Next() {
		var id, tier string
		if err := rows.Scan(&id, &tier); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "check_derived_law_modification", "cannot decode a law authority tier", true, "retry once the law projection is readable", err)
		}
		tiers[id] = tier
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return wrapFailure(KindUnavailable, "check_derived_law_modification", "cannot finish reading law authority tiers", true, "retry once the law projection is readable", err)
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "check_derived_law_modification", "cannot finish reading law authority tiers", true, "retry once the law projection is readable", err)
	}
	nonDerived := make([]string, 0)
	for _, id := range modified {
		tier, ok := tiers[id]
		if !ok {
			return newFailure(KindProjectionNotFound, "check_derived_law_modification", "a modified law is unknown to the accepted Git law projection: "+id, false, "publish and rebuild the accepted Git law projection")
		}
		if tier != "derived" {
			nonDerived = append(nonDerived, id)
		}
	}
	if len(nonDerived) != 0 {
		return newFailure(KindInvalidPayload, "check_derived_law_modification", "a contract that does not change Product truth may revise only derived law: "+strings.Join(nonDerived, ","), false, "leave law_modifies empty or select a Product-changing workflow")
	}
	return nil
}

func validateLawModificationSubset(mandated, modified []string) error {
	mandate := make(map[string]bool, len(mandated))
	for _, id := range mandated {
		if id == "" || mandate[id] {
			return newFailure(KindInvalidPayload, "check_mandated_laws", "law mandate contains an empty or duplicate ID", false, "supply a unique bounded law mandate")
		}
		mandate[id] = true
	}
	seen := map[string]bool{}
	for _, id := range modified {
		if id == "" || seen[id] || !mandate[id] {
			return newFailure(KindInvalidPayload, "check_mandated_laws", "law_modifies must be a subset of spec_mandate", false, "declare every modified law in spec_mandate")
		}
		seen[id] = true
	}
	return nil
}

// checkMandatedLawsAcrossSources is the federated mandated-law boundary check
// (CD-0200). Each mandated bare ID must resolve to exactly one accepted law
// subject across the verified source set: a law held by two sources refuses
// as ambiguous instead of picking one. Conflicts read per source, because a
// cross-source conflicts_with pair refuses at the declaring source's rebuild
// and can never project.
func checkMandatedLawsAcrossSources(ctx context.Context, q queryer, sources []KnowledgeHome, mandated, modified []string, allowAmendment bool) error {
	type resolution struct {
		source KnowledgeHome
		status string
		tier   string
	}
	resolved := make(map[string]resolution, len(mandated))
	placeholders := strings.TrimRight(strings.Repeat("?,", len(mandated)), ",")
	for _, source := range sources {
		args := []any{source.HomeProjectID, source.HomeLocatorID}
		for _, id := range mandated {
			args = append(args, id)
		}
		rows, err := q.QueryContext(ctx, `SELECT law_id,status,authority_tier FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholders+`) LIMIT 33`, args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every law ID stays parameter-bound.
		if err != nil {
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the derived law subjects of source "+source.HomeProjectID, true, "retry once the knowledge projection is readable", err)
		}
		for rows.Next() {
			var lawID, status, tier string
			if err := rows.Scan(&lawID, &status, &tier); err != nil {
				rows.Close()
				return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law subject", true, "retry once the knowledge projection is readable", err)
			}
			if _, clash := resolved[lawID]; clash {
				rows.Close()
				failure := newFailure(KindKnowledgeAmbiguous, "check_mandated_laws", "mandated law is held by more than one registered source: "+lawID, false, "qualify the reference as project_id/law_id or remove the duplicate law")
				failure.CandidateIDs = []string{source.HomeProjectID + "/" + lawID}
				return failure
			}
			resolved[lawID] = resolution{source: source, status: status, tier: tier}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law subjects", true, "retry once the knowledge projection is readable", err)
		}
		if err := rows.Close(); err != nil {
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law subjects", true, "retry once the knowledge projection is readable", err)
		}
	}
	missing := make([]string, 0)
	for _, id := range mandated {
		if r, ok := resolved[id]; !ok || r.status != "accepted" {
			missing = append(missing, id)
		}
	}
	if len(missing) != 0 {
		failure := newFailure(KindProjectionNotFound, "check_mandated_laws", "a mandated law is unknown or not currently accepted: "+strings.Join(missing, ","), false, "publish and rebuild the accepted Git law projection")
		failure.CandidateIDs = missing
		return failure
	}
	modifiedSet := make(map[string]bool, len(modified))
	for _, id := range modified {
		modifiedSet[id] = true
	}
	for _, source := range sources {
		args := []any{source.HomeProjectID, source.HomeLocatorID}
		for _, id := range mandated {
			args = append(args, id)
		}
		conflictRows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(args, stringArgs(mandated)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every law ID stays parameter-bound.
		if err != nil {
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read derived law conflicts", true, "retry once the knowledge projection is readable", err)
		}
		conflicts := 0
		for conflictRows.Next() {
			if conflicts == 32 {
				_ = conflictRows.Close()
				return newFailure(KindInvalidPayload, "check_mandated_laws", "derived law conflict query exceeds the bounded result size", false, "reduce the law set or resolve conflicts before retrying")
			}
			var sourceLaw, targetLaw string
			if err := conflictRows.Scan(&sourceLaw, &targetLaw); err != nil {
				conflictRows.Close()
				return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
			}
			conflicts++
			revisesLegislated := (modifiedSet[sourceLaw] && resolved[sourceLaw].tier != "derived") ||
				(modifiedSet[targetLaw] && resolved[targetLaw].tier != "derived")
			if !allowAmendment || (!modifiedSet[sourceLaw] && !modifiedSet[targetLaw]) || revisesLegislated {
				_ = conflictRows.Close()
				return newFailure(KindRelationConflict, "check_mandated_laws", fmt.Sprintf("mandated laws have an unresolved explicit conflict: %s and %s", sourceLaw, targetLaw), false, "resolve the Git law conflict or declare and approve the amendment path")
			}
		}
		if err := conflictRows.Err(); err != nil {
			conflictRows.Close()
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
		}
		if err := conflictRows.Close(); err != nil {
			return wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
		}
	}
	return nil
}

// workflowLawHome resolves the workflow's canonical Git law home over whichever
// read handle the caller already holds, per the store's queryer contract. The
// law home follows the work item's primary Project membership: the primary
// Project's Product owns the contract, so a secondary membership in another
// Product must never widen the home choice or make the workflow unrecordable.
func workflowLawHome(ctx context.Context, q queryer, workID string) (string, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id FROM product_knowledge_homes ph JOIN product_projects pp ON pp.product_id=ph.product_id JOIN work_projects wp ON wp.project_id=pp.project_id WHERE wp.work_id=? AND wp.role='primary' ORDER BY ph.project_id,ph.locator_id`, workID)
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
	}
	defer rows.Close()
	var homes [][2]string
	for rows.Next() {
		var project, locator string
		if err := rows.Scan(&project, &locator); err != nil {
			return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
		}
		if len(homes) == 0 || homes[len(homes)-1][0] != project || homes[len(homes)-1][1] != locator {
			homes = append(homes, [2]string{project, locator})
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
	}
	if len(homes) == 1 {
		return homes[0][0], homes[0][1], nil
	}
	if len(homes) > 1 {
		candidates := make([]string, 0, len(homes))
		for _, home := range homes {
			candidates = append(candidates, home[0]+"/"+home[1])
		}
		return "", "", newAmbiguousScopeFailure("check_mandated_laws", "workflow resolves to multiple canonical Git law homes", "resolve one Product knowledge home", candidates)
	}
	var project, locator string
	err = q.QueryRowContext(ctx, `SELECT wp.project_id,pl.locator_id FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary'`, workID).Scan(&project, &locator)
	if err == sql.ErrNoRows {
		return "", "", newFailure(KindUnknownScope, "check_mandated_laws", "workflow has no canonical Git law home", false, "designate a Product home or primary Project locator")
	}
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the primary Git law home", true, "retry once the workflow scope is readable", err)
	}
	return project, locator, nil
}
