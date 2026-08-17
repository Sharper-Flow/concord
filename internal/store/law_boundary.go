package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// LawBoundaryCheck is the bounded result of checking a workflow's declared
// law population against the Git-derived projection. It contains no heuristic
// suggestions and does not author state.
type LawBoundaryCheck struct {
	Mandated  []string
	Modified  []string
	Conflicts []LawConflict
}

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
	if len(lawIDs) > 32 {
		return nil, newFailure(KindInvalidPayload, "query_law_conflicts", "law conflict query exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	if len(lawIDs) == 0 {
		return []LawConflict{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(lawIDs)), ",")
	rows, err := s.db.QueryContext(ctx, `SELECT source_law_id,target_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(append([]any{homeProjectID, homeLocatorID}, stringArgs(lawIDs)...), stringArgs(lawIDs)...)...)
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
	_, err := checkMandatedLawsDB(ctx, s.db, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
	return err
}

func checkMandatedLawsTx(ctx context.Context, tx *sql.Tx, workID string, mandated, modified []string, allowAmendment bool) error {
	if len(mandated) == 0 {
		return validateLawModificationSubset(mandated, modified)
	}
	homeProjectID, homeLocatorID, err := workflowLawHomeTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	_, err = checkMandatedLawsTxAtHome(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
	return err
}

func checkMandatedLawsTxAtHome(ctx context.Context, tx *sql.Tx, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) (LawBoundaryCheck, error) {
	if tx == nil {
		return LawBoundaryCheck{}, newFailure(KindUnavailable, "check_mandated_laws", "transaction is not open", false, "open a mutation transaction")
	}
	return checkMandatedLawsQuery(ctx, tx, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

func checkMandatedLawsDB(ctx context.Context, db *sql.DB, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) (LawBoundaryCheck, error) {
	if homeProjectID == "" || homeLocatorID == "" {
		return LawBoundaryCheck{}, newFailure(KindUnknownScope, "check_mandated_laws", "canonical law home is incomplete", false, "resolve one canonical Git knowledge home")
	}
	if err := validateLawModificationSubset(mandated, modified); err != nil {
		return LawBoundaryCheck{}, err
	}
	return checkMandatedLawsQuery(ctx, db, homeProjectID, homeLocatorID, mandated, modified, allowAmendment)
}

type lawQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func checkMandatedLawsQuery(ctx context.Context, q lawQueryer, homeProjectID, homeLocatorID string, mandated, modified []string, allowAmendment bool) (LawBoundaryCheck, error) {
	if len(mandated) > 32 || len(modified) > 32 {
		return LawBoundaryCheck{}, newFailure(KindInvalidPayload, "check_mandated_laws", "law mandate exceeds the bounded list size", false, "supply at most 32 law IDs")
	}
	if err := validateLawModificationSubset(mandated, modified); err != nil {
		return LawBoundaryCheck{}, err
	}
	result := LawBoundaryCheck{Mandated: append([]string(nil), mandated...), Modified: append([]string(nil), modified...)}
	if len(mandated) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(mandated)), ",")
	args := []any{homeProjectID, homeLocatorID}
	for _, id := range mandated {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT law_id,status FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholders+`) LIMIT 33`, args...)
	if err != nil {
		return LawBoundaryCheck{}, wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read the derived law subjects", true, "retry once the knowledge projection is readable", err)
	}
	accepted := map[string]bool{}
	for rows.Next() {
		if len(accepted) == 32 {
			_ = rows.Close()
			return LawBoundaryCheck{}, newFailure(KindInvalidPayload, "check_mandated_laws", "derived law subject query exceeds the bounded result size", false, "reduce the law mandate before retrying")
		}
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return LawBoundaryCheck{}, wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law subject", true, "retry once the knowledge projection is readable", err)
		}
		if status == "accepted" {
			accepted[id] = true
		}
	}
	if err := rows.Close(); err != nil {
		return LawBoundaryCheck{}, wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law subjects", true, "retry once the knowledge projection is readable", err)
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
		return LawBoundaryCheck{}, failure
	}
	conflictRows, err := q.QueryContext(ctx, `SELECT source_law_id,target_law_id FROM law_relations WHERE home_project_id=? AND home_locator_id=? AND kind='conflicts_with' AND source_law_id IN (`+placeholders+`) AND target_law_id IN (`+placeholders+`) ORDER BY source_law_id,target_law_id LIMIT 33`, append(append([]any{homeProjectID, homeLocatorID}, stringArgs(mandated)...), stringArgs(mandated)...)...)
	if err != nil {
		return LawBoundaryCheck{}, wrapFailure(KindUnavailable, "check_mandated_laws", "cannot read derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	modifiedSet := make(map[string]bool, len(modified))
	for _, id := range modified {
		modifiedSet[id] = true
	}
	for conflictRows.Next() {
		if len(result.Conflicts) == 32 {
			_ = conflictRows.Close()
			return LawBoundaryCheck{}, newFailure(KindInvalidPayload, "check_mandated_laws", "derived law conflict query exceeds the bounded result size", false, "reduce the law set or resolve conflicts before retrying")
		}
		var source, target string
		if err := conflictRows.Scan(&source, &target); err != nil {
			conflictRows.Close()
			return LawBoundaryCheck{}, wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode a derived law conflict", true, "retry once the knowledge projection is readable", err)
		}
		result.Conflicts = append(result.Conflicts, LawConflict{SourceLawID: source, TargetLawID: target})
		if !allowAmendment || (!modifiedSet[source] && !modifiedSet[target]) {
			_ = conflictRows.Close()
			return result, newFailure(KindRelationConflict, "check_mandated_laws", fmt.Sprintf("mandated laws have an unresolved explicit conflict: %s and %s", source, target), false, "resolve the Git law conflict or declare and approve the amendment path")
		}
	}
	if err := conflictRows.Close(); err != nil {
		return LawBoundaryCheck{}, wrapFailure(KindUnavailable, "check_mandated_laws", "cannot finish reading derived law conflicts", true, "retry once the knowledge projection is readable", err)
	}
	return result, nil
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
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

func workflowLawHomeTx(ctx context.Context, tx *sql.Tx, workID string) (string, string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id FROM product_knowledge_homes ph JOIN product_projects pp ON pp.product_id=ph.product_id JOIN work_projects wp ON wp.project_id=pp.project_id WHERE wp.work_id=? ORDER BY ph.project_id,ph.locator_id`, workID)
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
	}
	var homes [][2]string
	for rows.Next() {
		var project, locator string
		if err := rows.Scan(&project, &locator); err != nil {
			rows.Close()
			return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot decode the workflow Git knowledge home", true, "retry once the workflow scope is readable", err)
		}
		duplicate := false
		for _, home := range homes {
			duplicate = duplicate || home[0] == project && home[1] == locator
		}
		if !duplicate {
			homes = append(homes, [2]string{project, locator})
		}
	}
	if err := rows.Close(); err != nil {
		return "", "", err
	}
	if len(homes) == 1 {
		return homes[0][0], homes[0][1], nil
	}
	if len(homes) > 1 {
		return "", "", newFailure(KindAmbiguousScope, "check_mandated_laws", "workflow resolves to multiple canonical Git law homes", false, "resolve one Product knowledge home")
	}
	var project, locator string
	err = tx.QueryRowContext(ctx, `SELECT wp.project_id,pl.locator_id FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary'`, workID).Scan(&project, &locator)
	if err == sql.ErrNoRows {
		return "", "", newFailure(KindUnknownScope, "check_mandated_laws", "workflow has no canonical Git law home", false, "designate a Product home or primary Project locator")
	}
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the primary Git law home", true, "retry once the workflow scope is readable", err)
	}
	return project, locator, nil
}

func workflowLawHomeDB(ctx context.Context, db *sql.DB, workID string) (string, string, error) {
	rows, err := db.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id FROM product_knowledge_homes ph JOIN product_projects pp ON pp.product_id=ph.product_id JOIN work_projects wp ON wp.project_id=pp.project_id WHERE wp.work_id=? ORDER BY ph.project_id,ph.locator_id`, workID)
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
	if len(homes) == 1 {
		return homes[0][0], homes[0][1], nil
	}
	if len(homes) > 1 {
		return "", "", newFailure(KindAmbiguousScope, "check_mandated_laws", "workflow resolves to multiple canonical Git law homes", false, "resolve one Product knowledge home")
	}
	var project, locator string
	err = db.QueryRowContext(ctx, `SELECT wp.project_id,pl.locator_id FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary'`, workID).Scan(&project, &locator)
	if err == sql.ErrNoRows {
		return "", "", newFailure(KindUnknownScope, "check_mandated_laws", "workflow has no canonical Git law home", false, "designate a Product home or primary Project locator")
	}
	if err != nil {
		return "", "", wrapFailure(KindUnavailable, "check_mandated_laws", "cannot resolve the primary Git law home", true, "retry once the workflow scope is readable", err)
	}
	return project, locator, nil
}

// SortLawIDs is a presentation helper only. Sorting never decides relation
// precedence; it merely makes bounded diagnostics deterministic.
func SortLawIDs(ids []string) []string {
	result := append([]string(nil), ids...)
	sort.Strings(result)
	return result
}
