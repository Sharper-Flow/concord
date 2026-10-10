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
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT wp.work_id,pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id IN (`+strings.Join(placeholders, ",")+") ORDER BY wp.work_id,pp.product_id", args...)
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

// ResolveLessonPublicationHome resolves the only tree a lesson publication
// may write: the active verified worktree claim on the deterministic
// knowledge-home Project (PM6's home order supplies the Project; the claim
// supplies the path and the branch). The source work names the lesson and its
// home. When that work is terminal and its original worktree is gone, the
// caller names a distinct live publication work with publicationWorkID; its
// claim on the same knowledge-home Project is used, and the lesson still
// names the terminal source work. The publication owner must be live — a
// terminal work holds no active lane and cannot own a claimed worktree a
// publication writes through. There is no fallback to the canonical default
// checkout and no claim outside the home Project: both refuse typed.
func (s *Store) ResolveLessonPublicationHome(ctx context.Context, sourceWorkID, publicationWorkID string) (KnowledgeHome, error) {
	return resolveLessonPublicationHome(ctx, s.db, sourceWorkID, publicationWorkID)
}

func resolveLessonPublicationHome(ctx context.Context, q queryer, sourceWorkID, publicationWorkID string) (KnowledgeHome, error) {
	home, err := resolveCompactionHome(ctx, q, sourceWorkID)
	if err != nil {
		return KnowledgeHome{}, err
	}
	owner := publicationWorkID
	if owner == "" {
		owner = sourceWorkID
	}
	var lifecycle string
	switch err := q.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, owner).Scan(&lifecycle); {
	case err == sql.ErrNoRows:
		return KnowledgeHome{}, newFailure(KindUnknownScope, "lesson_publish", "the publication owner work does not exist", false, "name a live publication work that claims the knowledge-home worktree")
	case err != nil:
		return KnowledgeHome{}, wrapFailure(KindUnavailable, "lesson_publish", "cannot read the publication owner work", true, "retry once the database is readable", err)
	case lifecycle == "completed" || lifecycle == "cancelled" || lifecycle == "superseded":
		return KnowledgeHome{}, newFailure(KindUnknownScope, "lesson_publish", "the publication owner work is terminal, so it cannot own the claimed worktree", false, "name a live publication work that claims the knowledge-home worktree")
	}
	rows, err := q.QueryContext(ctx, `SELECT project_id,pinned_path,pinned_branch FROM worktree_claims WHERE work_id=? AND state='verified' ORDER BY project_id`, owner)
	if err != nil {
		return KnowledgeHome{}, wrapFailure(KindUnavailable, "lesson_publish", "cannot read the claimed knowledge-home worktree", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var homeClaim, foreignClaim *worktreeClaimRow
	for rows.Next() {
		row := worktreeClaimRow{}
		if err := rows.Scan(&row.projectID, &row.path, &row.branch); err != nil {
			return KnowledgeHome{}, wrapFailure(KindUnavailable, "lesson_publish", "cannot decode the claimed knowledge-home worktree", true, "retry once the database is readable", err)
		}
		if row.projectID == home.HomeProjectID {
			homeClaim = &row
		} else {
			foreignClaim = &row
		}
	}
	if err := rows.Err(); err != nil {
		return KnowledgeHome{}, wrapFailure(KindUnavailable, "lesson_publish", "cannot finish reading the claimed knowledge-home worktree", true, "retry once the database is readable", err)
	}
	if homeClaim != nil {
		return KnowledgeHome{HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, RepoPath: homeClaim.path, HeadRef: homeClaim.branch}, nil
	}
	if foreignClaim != nil {
		return KnowledgeHome{}, newFailure(KindUnknownScope, "lesson_publish", "the publication work holds its claimed worktree on a foreign Project", false, "claim the knowledge-home Project's worktree for the publication work")
	}
	return KnowledgeHome{}, newFailure(KindUnknownScope, "lesson_publish", "the knowledge-home Project has no claimed worktree for the publication work", false, "claim the knowledge-home worktree for a live publication work and name it with publication_work_id")
}

type worktreeClaimRow struct {
	projectID string
	path      string
	branch    string
}

// NewLessonPublicationWorktreeRefusal is the typed refusal for a lesson
// publication whose resolved claimed worktree is not the calling session's
// host-verified worktree. The agent effect boundary raises it, so the refusal
// stays a store Failure end to end while the grant comparison stays in the
// agent plane, which owns grants (CD-0026 D1).
func NewLessonPublicationWorktreeRefusal() *Failure {
	return newFailure(KindUnknownScope, "lesson_publish", "the claimed knowledge-home worktree is not the calling session's verified worktree", false, "run lesson publication from the claimed knowledge-home worktree, or claim that worktree for a live publication work")
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
