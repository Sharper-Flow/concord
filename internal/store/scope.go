package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

func (s *Store) RelationEndpoints(ctx context.Context, relationID string) ([]string, error) {
	return relationEndpoints(ctx, s.db, relationID)
}

func relationEndpoints(ctx context.Context, q queryer, relationID string) ([]string, error) {
	id, err := strconv.ParseInt(relationID, 10, 64)
	if err != nil || id < 1 {
		return nil, newFailure(KindRelationNotFound, "relation_scope", "relation ID is invalid", false, "supply a known relation ID")
	}
	var from, to string
	err = q.QueryRowContext(ctx, `SELECT r.work_id_from,r.work_id_to FROM relations r JOIN work_items wf ON wf.id=r.work_id_from JOIN work_items wt ON wt.id=r.work_id_to WHERE r.id=?`, id).Scan(&from, &to)
	if err == sql.ErrNoRows {
		return nil, newFailure(KindRelationNotFound, "relation_scope", "relation or one of its work endpoints does not exist", false, "reread the relation graph")
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "relation_scope", "cannot resolve relation endpoints", true, "retry once the database is readable", err)
	}
	return []string{from, to}, nil
}

func (s *Store) ProductsForWorkIDs(ctx context.Context, ids []string) (map[string][]string, error) {
	return productsForWorkIDs(ctx, s.db, ids)
}

func productsForWorkIDs(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	out := make(map[string][]string)
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT wp.work_id,pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id IN (`+strings.Join(placeholders, ",")+") ORDER BY wp.work_id,pp.product_id", args...)
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

func (s *Store) ProductsForKnowledgeID(ctx context.Context, id string) ([]string, error) {
	return productsForKnowledgeID(ctx, s.db, id)
}

func productsForKnowledgeID(ctx context.Context, q queryer, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT product_id FROM archived_work_products WHERE work_id=? ORDER BY product_id`, id)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "scope", "cannot resolve knowledge Product scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
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

func (s *Store) WorkExists(ctx context.Context, id string) (bool, error) {
	return workExistsCore(ctx, s.db, id)
}

func workExistsCore(ctx context.Context, q queryer, id string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE id=?)`, id).Scan(&exists)
	return exists, err
}
func (s *Store) KnowledgeExists(ctx context.Context, id string) (bool, error) {
	return knowledgeExists(ctx, s.db, id)
}

func knowledgeExists(ctx context.Context, q queryer, id string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM archived_work WHERE id=?)`, id).Scan(&exists)
	return exists, err
}

// ResolveCompactionHome applies PM6's deterministic home order: a unique
// Product-designated home wins; otherwise the unique primary work Project's
// canonical path locator is used. Multiple candidates are ambiguous and never
// silently selected.
func (s *Store) ResolveCompactionHome(ctx context.Context, workID string) (KnowledgeHome, error) {
	return resolveCompactionHome(ctx, s.db, workID)
}

func resolveCompactionHome(ctx context.Context, q queryer, workID string) (KnowledgeHome, error) {
	if workID == "" {
		return KnowledgeHome{}, newFailure(KindInvalidOperation, "compaction_home", "work ID is empty", false, "supply a terminal work ID")
	}
	type candidate struct{ project, locator, value string }
	var productHomes []candidate
	rows, err := q.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id,pl.locator_value FROM product_knowledge_homes ph JOIN project_locators pl ON pl.locator_id=ph.locator_id AND pl.kind='canonical_path' WHERE ph.product_id IN (SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=?) ORDER BY ph.project_id,ph.locator_id`, workID)
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
		return KnowledgeHome{}, newFailure(KindAmbiguousScope, "compaction_home", "multiple Product knowledge homes are eligible", false, "designate one Product knowledge home")
	}
	var primary candidate
	var count int
	err = q.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary'`, workID).Scan(&count)
	if err != nil {
		return KnowledgeHome{}, err
	}
	if count == 0 {
		return KnowledgeHome{}, newFailure(KindUnknownScope, "compaction_home", "terminal work has no eligible canonical home", false, "designate a Product home or primary Project locator")
	}
	if count > 1 {
		return KnowledgeHome{}, newFailure(KindAmbiguousScope, "compaction_home", "primary work membership has multiple canonical locators", false, "leave exactly one eligible primary Project locator")
	}
	err = q.QueryRowContext(ctx, `SELECT wp.project_id,pl.locator_id,pl.locator_value FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary'`, workID).Scan(&primary.project, &primary.locator, &primary.value)
	if err == sql.ErrNoRows {
		return KnowledgeHome{}, newFailure(KindUnknownScope, "compaction_home", "primary Project locator disappeared", false, "restore the canonical Project locator")
	}
	if err != nil {
		return KnowledgeHome{}, err
	}
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

// ResolveKnowledgeQueryHome resolves the git authority before any watermark or
// archived-row read. Product homes have precedence over ambient Project scope;
// Project-only calls use exactly one canonical-path locator. A caller-supplied
// KnowledgeHome is evidence to compare, never an authority override.
func (s *Store) ResolveKnowledgeQueryHome(ctx context.Context, productID, projectID string, supplied KnowledgeHome, op string) (KnowledgeHome, error) {
	if s == nil || s.db == nil {
		return KnowledgeHome{}, newFailure(KindUnavailable, op, "store is not open", false, "open a store before resolving knowledge authority")
	}
	return resolveKnowledgeQueryHome(ctx, s.db, productID, projectID, supplied, op)
}

func resolveKnowledgeQueryHome(ctx context.Context, q queryer, productID, projectID string, supplied KnowledgeHome, op string) (KnowledgeHome, error) {
	var resolved KnowledgeHome
	if productID != "" {
		candidates, err := productKnowledgeHomeCandidates(ctx, q, productID)
		if err != nil {
			return KnowledgeHome{}, err
		}
		if len(candidates) == 0 {
			return KnowledgeHome{}, newFailure(KindUnknownScope, op, "Product has no unique canonical knowledge home", false, "designate exactly one Product knowledge home")
		}
		if len(candidates) > 1 {
			return KnowledgeHome{}, newFailure(KindAmbiguousScope, op, "Product has multiple canonical knowledge homes", false, "designate exactly one Product knowledge home")
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
	} else if projectID != "" {
		candidates, err := projectCanonicalHomeCandidates(ctx, q, projectID)
		if err != nil {
			return KnowledgeHome{}, err
		}
		if len(candidates) == 0 {
			return KnowledgeHome{}, newFailure(KindUnknownScope, op, "Project has no canonical-path knowledge locator", false, "designate exactly one canonical Project locator")
		}
		if len(candidates) > 1 {
			return KnowledgeHome{}, newFailure(KindAmbiguousScope, op, "Project has multiple canonical-path knowledge locators", false, "leave exactly one canonical Project locator")
		}
		resolved = candidates[0]
	} else {
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
		copy := *failure
		copy.Op = op
		return &copy
	}
	return wrapFailure(KindUnavailable, op, "cannot verify the canonical knowledge locator", true, "retry once the database is readable", err)
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
