package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

func (s *Store) RelationEndpoints(ctx context.Context, relationID string) ([]string, error) {
	id, err := strconv.ParseInt(relationID, 10, 64)
	if err != nil || id < 1 {
		return nil, newFailure(KindRelationNotFound, "relation_scope", "relation ID is invalid", false, "supply a known relation ID")
	}
	var from, to string
	err = s.db.QueryRowContext(ctx, `SELECT r.work_id_from,r.work_id_to FROM relations r JOIN work_items wf ON wf.id=r.work_id_from JOIN work_items wt ON wt.id=r.work_id_to WHERE r.id=?`, id).Scan(&from, &to)
	if err == sql.ErrNoRows {
		return nil, newFailure(KindRelationNotFound, "relation_scope", "relation or one of its work endpoints does not exist", false, "reread the relation graph")
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "relation_scope", "cannot resolve relation endpoints", true, "retry once the database is readable", err)
	}
	return []string{from, to}, nil
}

func (s *Store) ProductsForWorkIDs(ctx context.Context, ids []string) (map[string][]string, error) {
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
	rows, err := s.db.QueryContext(ctx, `SELECT wp.work_id,pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id IN (`+strings.Join(placeholders, ",")+") ORDER BY wp.work_id,pp.product_id", args...)
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
	rows, err := s.db.QueryContext(ctx, `SELECT product_id FROM archived_work_products WHERE work_id=? ORDER BY product_id`, id)
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
	out := make(map[string][]string)
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT project_id,product_id FROM product_projects WHERE project_id IN (`+strings.Join(placeholders, ",")+") ORDER BY project_id,product_id", args...)
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
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE id=?)`, id).Scan(&exists)
	return exists, err
}
func (s *Store) KnowledgeExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM archived_work WHERE id=?)`, id).Scan(&exists)
	return exists, err
}

// ResolveCompactionHome applies PM6's deterministic home order: a unique
// Product-designated home wins; otherwise the unique primary work Project's
// canonical path locator is used. Multiple candidates are ambiguous and never
// silently selected.
func (s *Store) ResolveCompactionHome(ctx context.Context, workID string) (KnowledgeHome, error) {
	if workID == "" {
		return KnowledgeHome{}, newFailure(KindInvalidOperation, "compaction_home", "work ID is empty", false, "supply a terminal work ID")
	}
	type candidate struct{ project, locator, value string }
	var productHomes []candidate
	rows, err := s.db.QueryContext(ctx, `SELECT ph.project_id,ph.locator_id,pl.locator_value FROM product_knowledge_homes ph JOIN project_locators pl ON pl.locator_id=ph.locator_id AND pl.kind='canonical_path' WHERE ph.product_id IN (SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=?) ORDER BY ph.project_id,ph.locator_id`, workID)
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
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary'`, workID).Scan(&count)
	if err != nil {
		return KnowledgeHome{}, err
	}
	if count == 0 {
		return KnowledgeHome{}, newFailure(KindUnknownScope, "compaction_home", "terminal work has no eligible canonical home", false, "designate a Product home or primary Project locator")
	}
	if count > 1 {
		return KnowledgeHome{}, newFailure(KindAmbiguousScope, "compaction_home", "primary work membership has multiple canonical locators", false, "leave exactly one eligible primary Project locator")
	}
	err = s.db.QueryRowContext(ctx, `SELECT wp.project_id,pl.locator_id,pl.locator_value FROM work_projects wp JOIN project_locators pl ON pl.project_id=wp.project_id AND pl.kind='canonical_path' WHERE wp.work_id=? AND wp.role='primary'`, workID).Scan(&primary.project, &primary.locator, &primary.value)
	if err == sql.ErrNoRows {
		return KnowledgeHome{}, newFailure(KindUnknownScope, "compaction_home", "primary Project locator disappeared", false, "restore the canonical Project locator")
	}
	if err != nil {
		return KnowledgeHome{}, err
	}
	return KnowledgeHome{HomeProjectID: primary.project, HomeLocatorID: primary.locator, RepoPath: primary.value, HeadRef: "HEAD"}, nil
}

func (s *Store) KnowledgeHomeForLocator(ctx context.Context, projectID, locatorID, headRef string) (KnowledgeHome, error) {
	var value string
	if err := s.db.QueryRowContext(ctx, `SELECT locator_value FROM project_locators WHERE project_id=? AND locator_id=? AND kind='canonical_path'`, projectID, locatorID).Scan(&value); err != nil {
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
