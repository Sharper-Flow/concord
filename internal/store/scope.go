package store

import (
	"context"
	"strings"
)

// ProductsForWorkIDs returns distinct Product identities across every Project
// membership, including secondary memberships that widen work visibility.
func (s *Store) ProductsForWorkIDs(ctx context.Context, ids []string) (map[string][]string, error) {
	return productsForWorkIDs(ctx, s.db, ids)
}

func productsForWorkIDs(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	out := make(map[string][]string)
	if len(ids) == 0 {
		return out, nil
	}
	for _, id := range ids {
		retained, err := retainedExecutionExistsQ(ctx, q, id)
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "scope", "cannot read execution presence", true, "retry once the database is readable", err)
		}
		if retained {
			continue
		}
		var outcomes int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM archived_work WHERE id=? AND type='work_note'`, id).Scan(&outcomes); err != nil {
			return nil, wrapFailure(KindUnavailable, "scope", "cannot resolve canonical outcome identity", true, "retry once the database is readable", err)
		}
		if outcomes > 1 {
			return nil, newFailure(KindUnknownScope, "scope", "work "+id+" has no unique canonical outcome scope", false, "resolve the outcome within an explicitly authorized knowledge home")
		}
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	// Removed execution is authorized through the outcome's frozen scope,
	// never through the knowledge home's current Product memberships.
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT wp.work_id,pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id IN (`+strings.Join(placeholders, ",")+`)
		UNION SELECT p.work_id,p.product_id FROM archived_work_products p JOIN archived_work a ON a.home_project_id=p.home_project_id AND a.home_locator_id=p.home_locator_id AND a.id=p.work_id
		WHERE a.type='work_note' AND NOT EXISTS(SELECT 1 FROM work_items w WHERE w.id=a.id) AND p.work_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY 1,2`, append(args, args...)...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "scope", "cannot resolve work Product scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var work, product string
		if err := rows.Scan(&work, &product); err != nil {
			return nil, err
		}
		out[work] = append(out[work], product)
	}
	return out, rows.Err()
}

func (s *Store) ProductsForProjectIDs(ctx context.Context, ids []string) (map[string][]string, error) {
	return productsForProjectIDs(ctx, s.db, ids)
}

func productsForProjectIDs(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	out := make(map[string][]string)
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	rows, err := q.QueryContext(ctx, `SELECT project_id,product_id FROM product_projects WHERE project_id IN (`+strings.Join(placeholders, ",")+") ORDER BY project_id,product_id", args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "scope", "cannot resolve Project Product scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var project, product string
		if err := rows.Scan(&project, &product); err != nil {
			return nil, err
		}
		out[project] = append(out[project], product)
	}
	return out, rows.Err()
}

func workExistsCore(ctx context.Context, q queryer, id string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE id=?)`, id).Scan(&exists)
	return exists, err
}

// resolveKnowledgeQueryHome resolves the Product's one designated canonical
// knowledge home.
func resolveKnowledgeQueryHome(ctx context.Context, q queryer, productID string, op string) (KnowledgeHome, error) {
	if productID == "" {
		return KnowledgeHome{}, newFailure(KindInvalidFilter, op, "knowledge query requires a Product", false, "supply the Product whose designated knowledge home answers")
	}
	candidates, err := productKnowledgeHomeCandidates(ctx, q, productID)
	if err != nil {
		return KnowledgeHome{}, err
	}
	if len(candidates) == 0 {
		return KnowledgeHome{}, newFailure(KindUnknownScope, op, "Product has no unique canonical knowledge home", false, "designate exactly one Product knowledge home")
	}
	if len(candidates) > 1 {
		return KnowledgeHome{}, newAmbiguousScopeFailure(op, "Product has multiple canonical knowledge homes", "designate exactly one Product knowledge home", knowledgeHomeCandidateIDs(candidates))
	}
	return candidates[0], nil
}

// knowledgeHomeCandidateIDs renders the enumerated home identities an
// ambiguous-scope refusal names, in the order the resolution query produced.
func knowledgeHomeCandidateIDs(homes []KnowledgeHome) []string {
	candidates := make([]string, 0, len(homes))
	for _, home := range homes {
		candidates = append(candidates, home.HomeProjectID+"/"+home.HomeLocatorID)
	}
	return candidates
}

func productKnowledgeHomeCandidates(ctx context.Context, q queryer, productID string) ([]KnowledgeHome, error) {
	rows, err := q.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id,pl.locator_value FROM product_knowledge_homes ph JOIN project_locators pl ON pl.locator_id=ph.locator_id AND pl.project_id=ph.project_id AND pl.kind='canonical_path' WHERE ph.product_id=? ORDER BY ph.project_id,ph.locator_id`, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "knowledge_home", "cannot resolve Product knowledge homes", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var homes []KnowledgeHome
	for rows.Next() {
		var home KnowledgeHome
		if err := rows.Scan(&home.HomeProjectID, &home.HomeLocatorID, &home.RepoPath); err != nil {
			return nil, wrapFailure(KindUnavailable, "knowledge_home", "cannot decode Product knowledge home", true, "retry once the database is readable", err)
		}
		home, err = currentLawHome(home)
		if err != nil {
			return nil, err
		}
		homes = append(homes, home)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "knowledge_home", "cannot finish resolving Product knowledge homes", true, "retry once the database is readable", err)
	}
	return homes, nil
}

func projectCanonicalHomeCandidates(ctx context.Context, q queryer, projectID string) ([]KnowledgeHome, error) {
	rows, err := q.QueryContext(ctx, `SELECT project_id,locator_id,locator_value FROM project_locators WHERE project_id=? AND kind='canonical_path' ORDER BY locator_id`, projectID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "knowledge_home", "cannot resolve Project canonical locators", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var homes []KnowledgeHome
	for rows.Next() {
		var home KnowledgeHome
		if err := rows.Scan(&home.HomeProjectID, &home.HomeLocatorID, &home.RepoPath); err != nil {
			return nil, wrapFailure(KindUnavailable, "knowledge_home", "cannot decode Project canonical locator", true, "retry once the database is readable", err)
		}
		home, err = currentLawHome(home)
		if err != nil {
			return nil, err
		}
		homes = append(homes, home)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "knowledge_home", "cannot finish resolving Project canonical locators", true, "retry once the database is readable", err)
	}
	return homes, nil
}
