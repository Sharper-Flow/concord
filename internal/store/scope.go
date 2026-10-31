package store

import (
	"context"
	"database/sql"
	"strings"
)

// ProductsForWorkIDs returns distinct Product identities across every Project
// membership, including secondary memberships that widen work visibility.
func (s *Store) ProductsForWorkIDs(ctx context.Context, ids []string) (map[string][]string, error) {
	return productsForWorkIDs(ctx, s.db, ids)
}

// ResearchPackOwnerWorkIDs resolves identities only. Product membership and
// caller authorization belong to the work-reference boundary.
func (s *Store) ResearchPackOwnerWorkIDs(ctx context.Context, ids []string) ([]string, error) {
	return researchPackOwnerWorkIDs(ctx, s.db, ids)
}

func ResearchPackOwnerWorkIDsTx(ctx context.Context, transaction *Transaction, ids []string) ([]string, error) {
	tx, err := transactionSQL(transaction, "research_scope")
	if err != nil {
		return nil, err
	}
	return researchPackOwnerWorkIDs(ctx, tx, ids)
}

func researchPackOwnerWorkIDs(ctx context.Context, q queryer, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	if len(ids) > 100 {
		return nil, newFailure(KindLimitExceeded, "research_scope", "at most 100 research pack identities may be resolved", false, "supply fewer pack identities")
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	rows, err := q.QueryContext(ctx, `SELECT pack_id,owner_work_id FROM active_research_packs WHERE pack_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY pack_id`, args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "research_scope", "cannot resolve research pack ownership", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	found := make(map[string]bool, len(ids))
	owners := []string{}
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			return nil, err
		}
		found[id] = true
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if !found[id] {
			return nil, newFailure(KindProjectionNotFound, "research_scope", "research pack does not exist", false, "check the pack identifier")
		}
	}
	return owners, nil
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

// ResolveCompactionHome applies PM6's deterministic home order: a unique
// Product-designated home wins; otherwise the unique primary work Project's
// canonical path locator is used. Product-designated home candidates come from
// the primary membership's Product alone: the primary Project's Product owns
// the compaction home, so a secondary membership in another Product must not
// widen the candidate set into an ambiguity. Multiple candidates are ambiguous
// and never silently selected.
func (s *Store) ResolveCompactionHome(ctx context.Context, workID string) (KnowledgeHome, error) {
	if err := publishedWorkAbsenceQ(ctx, s.db, workID, "compaction_home"); err != nil {
		return KnowledgeHome{}, err
	}
	return resolveCompactionHome(ctx, s.db, workID)
}

func resolveCompactionHome(ctx context.Context, q queryer, workID string) (KnowledgeHome, error) {
	if workID == "" {
		return KnowledgeHome{}, newFailure(KindInvalidOperation, "compaction_home", "work ID is empty", false, "supply a terminal work ID")
	}
	type candidate struct{ project, locator, value string }
	var productHomes []candidate
	rows, err := q.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id,pl.locator_value FROM product_knowledge_homes ph JOIN project_locators pl ON pl.locator_id=ph.locator_id AND pl.kind='canonical_path' WHERE ph.product_id IN (SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? AND wp.role='primary') ORDER BY ph.project_id,ph.locator_id`, workID)
	if err != nil {
		return KnowledgeHome{}, wrapFailure(KindUnavailable, "compaction_home", "cannot resolve Product knowledge homes", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.project, &c.locator, &c.value); err != nil {
			rows.Close()
			return KnowledgeHome{}, err
		}
		productHomes = append(productHomes, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return KnowledgeHome{}, err
	}
	if err := rows.Close(); err != nil {
		return KnowledgeHome{}, err
	}
	if len(productHomes) == 1 {
		c := productHomes[0]
		return KnowledgeHome{HomeProjectID: c.project, HomeLocatorID: c.locator, RepoPath: c.value, HeadRef: "HEAD"}, nil
	}
	if len(productHomes) > 1 {
		candidates := make([]string, 0, len(productHomes))
		for _, c := range productHomes {
			candidates = append(candidates, c.project+"/"+c.locator)
		}
		return KnowledgeHome{}, newAmbiguousScopeFailure("compaction_home", "multiple Product knowledge homes are eligible", "designate one Product knowledge home", candidates)
	}
	rows, err = q.QueryContext(ctx, `SELECT wp.project_id,pl.locator_id,pl.locator_value FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary' ORDER BY wp.project_id,pl.locator_id`, workID)
	if err != nil {
		return KnowledgeHome{}, wrapFailure(KindUnavailable, "compaction_home", "cannot resolve the primary Project locators", true, "retry once the database is readable", err)
	}
	var primaryLocators []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.project, &c.locator, &c.value); err != nil {
			rows.Close()
			return KnowledgeHome{}, err
		}
		primaryLocators = append(primaryLocators, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return KnowledgeHome{}, err
	}
	if err := rows.Close(); err != nil {
		return KnowledgeHome{}, err
	}
	if len(primaryLocators) == 0 {
		return KnowledgeHome{}, newFailure(KindUnknownScope, "compaction_home", "terminal work has no eligible canonical home", false, "designate a Product home or primary Project locator")
	}
	if len(primaryLocators) > 1 {
		candidates := make([]string, 0, len(primaryLocators))
		for _, c := range primaryLocators {
			candidates = append(candidates, c.project+"/"+c.locator)
		}
		return KnowledgeHome{}, newAmbiguousScopeFailure("compaction_home", "primary work membership has multiple canonical locators", "leave exactly one eligible primary Project locator", candidates)
	}
	primary := primaryLocators[0]
	return KnowledgeHome{HomeProjectID: primary.project, HomeLocatorID: primary.locator, RepoPath: primary.value, HeadRef: "HEAD"}, nil
}

func (s *Store) KnowledgeHomeForLocator(ctx context.Context, projectID, locatorID, headRef string) (KnowledgeHome, error) {
	return knowledgeHomeForLocator(ctx, s.db, projectID, locatorID, headRef)
}

func knowledgeHomeForLocator(ctx context.Context, q queryer, projectID, locatorID, headRef string) (KnowledgeHome, error) {
	var value string
	if err := q.QueryRowContext(ctx, `SELECT locator_value FROM project_locators WHERE project_id=? AND locator_id=? AND kind='canonical_path'`, projectID, locatorID).Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return KnowledgeHome{}, newFailure(KindUnknownScope, "compaction_home", "recorded knowledge locator no longer exists", false, "restore the recorded canonical Project locator")
		}
		return KnowledgeHome{}, err
	}
	if headRef == "" {
		headRef = "HEAD"
	}
	return KnowledgeHome{HomeProjectID: projectID, HomeLocatorID: locatorID, RepoPath: value, HeadRef: headRef}, nil
}

func resolveKnowledgeQueryHome(ctx context.Context, q queryer, productID, projectID string, supplied KnowledgeHome, op string) (KnowledgeHome, error) {
	var resolved KnowledgeHome
	switch {
	case productID != "":
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
		resolved = candidates[0]
		if projectID != "" {
			var member bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, productID, projectID).Scan(&member); err != nil {
				return KnowledgeHome{}, wrapFailure(KindUnavailable, op, "cannot validate Product/Project membership", true, "retry once the database is readable", err)
			}
			if !member {
				return KnowledgeHome{}, newFailure(KindUnknownScope, op, "Project is not a member of the requested Product", false, "supply a Project belonging to the Product")
			}
		}
	case projectID != "":
		candidates, err := projectCanonicalHomeCandidates(ctx, q, projectID)
		if err != nil {
			return KnowledgeHome{}, err
		}
		if len(candidates) == 0 {
			return KnowledgeHome{}, newFailure(KindUnknownScope, op, "Project has no canonical-path knowledge locator", false, "designate exactly one canonical Project locator")
		}
		if len(candidates) > 1 {
			return KnowledgeHome{}, newAmbiguousScopeFailure(op, "Project has multiple canonical-path knowledge locators", "leave exactly one canonical Project locator", knowledgeHomeCandidateIDs(candidates))
		}
		resolved = candidates[0]
	default:
		if supplied.HomeProjectID == "" || supplied.HomeLocatorID == "" || supplied.RepoPath == "" || supplied.HeadRef == "" {
			return KnowledgeHome{}, newFailure(KindInvalidFilter, op, "unscoped knowledge query requires a complete explicit home", false, "supply a Project, Product, or a locator-verified KnowledgeHome")
		}
		var err error
		resolved, err = knowledgeHomeForLocator(ctx, q, supplied.HomeProjectID, supplied.HomeLocatorID, supplied.HeadRef)
		if err != nil {
			return KnowledgeHome{}, knowledgeHomeResolutionFailure(err, op)
		}
	}
	if err := compareKnowledgeHomeEvidence(supplied, resolved, op); err != nil {
		return KnowledgeHome{}, err
	}
	return resolved, nil
}

func knowledgeHomeResolutionFailure(err error, op string) error {
	var failure *Failure
	if failureAs(err, &failure) {
		classified := *failure
		classified.Op = op
		return &classified
	}
	return wrapFailure(KindUnavailable, op, "cannot verify the canonical knowledge locator", true, "retry once the database is readable", err)
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
		home.HeadRef = "HEAD"
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
		home.HeadRef = "HEAD"
		homes = append(homes, home)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "knowledge_home", "cannot finish resolving Project canonical locators", true, "retry once the database is readable", err)
	}
	return homes, nil
}

func compareKnowledgeHomeEvidence(supplied, resolved KnowledgeHome, op string) error {
	if supplied.HomeProjectID != "" && supplied.HomeProjectID != resolved.HomeProjectID || supplied.HomeLocatorID != "" && supplied.HomeLocatorID != resolved.HomeLocatorID || supplied.RepoPath != "" && supplied.RepoPath != resolved.RepoPath || supplied.HeadRef != "" && supplied.HeadRef != resolved.HeadRef {
		return newFailure(KindInvalidFilter, op, "caller KnowledgeHome does not match the authoritative canonical home", false, "use the Product or Project-resolved KnowledgeHome")
	}
	return nil
}
