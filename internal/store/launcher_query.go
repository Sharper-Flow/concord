package store

// This file is an in-process launcher read adapter. It deliberately is not a
// new Product-memory query: it composes the accepted projections in one read
// transaction for the two launcher modes.

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

type LauncherProductRequest struct {
	Product string
	Limit   int
	Depth   int
}

type LauncherBlocker struct {
	ID          string
	Title       string
	Authority   string
	Age         string
	External    bool
	ConditionID string
	// IssueKey is the blocker's confirmed Linear key when the blocker itself
	// is linked, so the launcher row can carry the blocking ticket reference.
	IssueKey string
}

type LauncherWork struct {
	ID             string
	Kind           string
	Title          string
	LinearIssueKey string
	LinearIssueURL string
	Lifecycle      string
	Priority       int64
	Urgency        string
	CreatedAt      string
	UpdatedAt      string
	TerminalAt     string
	Worktree       string
	Live           int
	ProjectCount   int
	Blocked        bool
	Ready          bool
	Terminal       bool
	Blockers       []LauncherBlocker
}

type LauncherProductResult struct {
	ResultMeta
	// Works is the active work segment ordered by last activity derived
	// from the event log (newest work_item-subject event time DESC, id),
	// the default ordering the launcher work list renders. The store alone
	// orders the rows and returns the Product's complete active set: no
	// limit cuts the segment and no omission-by-limit state exists for it.
	// TerminalWorks is the completed-history segment bounded by Limit and
	// ordered by terminal_time DESC, id, so the list renders active work
	// first and reaches terminal history by scrolling.
	Works         []LauncherWork
	TerminalWorks []LauncherWork
	Edges         []RelationEdge
}

// LauncherSearchRequest selects a private launcher projection, not a new PM query or
// public tool. One operation owns the Product-scoped work matches.
type LauncherSearchRequest struct {
	Product string
	Query   string
	Limit   int
}

type LauncherSearchResult struct {
	ResultMeta
	Works []LauncherWork
}

// ResolveLauncherWorkProduct selects the Product scope for direct work
// forwarding from the landing Project: the Project the session names, else
// the work's primary Project. The launcher does not write a remembered
// Product to the store. preferredProduct is an inherited selection; the
// landing Project owns the scope, so it is honored only when it is one of
// the landing Project's Products.
func (s *Store) ResolveLauncherWorkProduct(ctx context.Context, workID, projectID, preferredProduct string) (string, error) {
	if workID == "" {
		return "", unknownScope("launcher.forward", "work forwarding requires a work")
	}
	tx, err := beginRead(ctx, s, "launcher.forward")
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	return resolveLandingProjectProductTx(ctx, tx, workID, projectID, preferredProduct)
}

// resolveLandingProjectProductTx applies the landing-Project Product rule
// inside the caller's read transaction: the landing Project is the named
// member Project or the work's primary Project, and its Product set must
// resolve to one Product, or the inherited selection must name one of them.
func resolveLandingProjectProductTx(ctx context.Context, tx *sql.Tx, workID, projectID, preferredProduct string) (string, error) {
	landing := projectID
	if landing == "" {
		err := tx.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&landing)
		if err == sql.ErrNoRows {
			return "", unknownScope("launcher.forward", "work has no primary Project")
		}
		if err != nil {
			return "", wrapFailure(KindUnavailable, "launcher.forward", "cannot read work Project memberships", true, "retry once the database is readable", err)
		}
	} else {
		var members int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM work_projects WHERE work_id=? AND project_id=?`, workID, landing).Scan(&members)
		if err != nil {
			return "", wrapFailure(KindUnavailable, "launcher.forward", "cannot read work Project memberships", true, "retry once the database is readable", err)
		}
		if members == 0 {
			return "", newFailure(KindUnknownScope, "launcher.forward", "work item does not hold Project "+landing, false, "forward from a Project the work item belongs to")
		}
	}
	products, err := landingProjectProductsTx(ctx, tx, landing)
	if err != nil {
		return "", err
	}
	if len(products) == 0 {
		return "", unknownScope("launcher.forward", "landing Project has no Product")
	}
	if len(products) > 1 {
		for _, productID := range products {
			if productID == preferredProduct {
				return productID, nil
			}
		}
		return "", newAmbiguousScopeFailure("launcher.forward", "landing Project spans more than one Product", "name one of the landing Project's Products", products)
	}
	return products[0], nil
}

func landingProjectProductsTx(ctx context.Context, tx *sql.Tx, projectID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT product_id FROM product_projects WHERE project_id=? ORDER BY product_id`, projectID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "launcher.forward", "cannot read Project Products", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var products []string
	for rows.Next() {
		var productID string
		if err := rows.Scan(&productID); err != nil {
			return nil, wrapFailure(KindUnavailable, "launcher.forward", "cannot decode Project Products", true, "retry once the database is readable", err)
		}
		products = append(products, productID)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "launcher.forward", "cannot enumerate Project Products", true, "retry once the database is readable", err)
	}
	return products, nil
}

// LauncherLinearIssue resolves a confirmed Linear issue link without a remote
// read. The session scope follows the same landing-Project Product rule as
// direct work forwarding.
type LauncherLinearIssue struct {
	WorkID    string
	ProductID string
}

// ResolveLauncherLinearIssue resolves a confirmed Linear issue link and its
// Product in one read transaction. projectID names the landing Project
// (empty means the work's primary Project); preferredProduct is honored only
// when it is one of the landing Project's Products.
func (s *Store) ResolveLauncherLinearIssue(ctx context.Context, humanKey, issueURL, projectID, preferredProduct string) (LauncherLinearIssue, error) {
	tx, err := beginRead(ctx, s, "launcher.forward")
	if err != nil {
		return LauncherLinearIssue{}, err
	}
	defer tx.Rollback()
	workID, err := launcherLinkedWorkTx(ctx, tx, humanKey, issueURL)
	if err != nil {
		return LauncherLinearIssue{}, err
	}
	productID, err := resolveLandingProjectProductTx(ctx, tx, workID, projectID, preferredProduct)
	if err != nil {
		return LauncherLinearIssue{}, err
	}
	return LauncherLinearIssue{WorkID: workID, ProductID: productID}, nil
}

// ResolveLauncherLinearIssueWork resolves the work item that records the
// Linear issue, so a caller can answer "is this issue recorded" separately
// from the landing-Project Product rule. The only unknown-scope refusal it
// returns is the unrecorded case.
func (s *Store) ResolveLauncherLinearIssueWork(ctx context.Context, humanKey, issueURL string) (string, error) {
	tx, err := beginRead(ctx, s, "launcher.forward")
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	return launcherLinkedWorkTx(ctx, tx, humanKey, issueURL)
}

func launcherLinkedWorkTx(ctx context.Context, tx *sql.Tx, humanKey, issueURL string) (string, error) {
	var workID string
	err := tx.QueryRowContext(ctx, `SELECT work_id FROM linear_issue_links
		WHERE human_key=? OR url=?
		ORDER BY work_id LIMIT 1`, humanKey, issueURL).Scan(&workID)
	if err == sql.ErrNoRows {
		return "", unknownScope("launcher.forward", "Linear issue is not linked to a work")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "launcher.forward", "cannot resolve Linear issue link", true, "retry once the database is readable", err)
	}
	return workID, nil
}

func (s *Store) QueryLauncherSearch(ctx context.Context, req LauncherSearchRequest) (LauncherSearchResult, error) {
	var out LauncherSearchResult
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "launcher.search", "store is not open", false, "open the authority database")
	}
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	if limit > 20 {
		return out, newFailure(KindInvalidFilter, "launcher.search", "launcher search limit must be between 1 and 20", false, "use the Product launcher bound")
	}
	if req.Product == "" || req.Query == "" {
		return out, newFailure(KindInvalidFilter, "launcher.search", "Product-scoped search requires Product and query", false, "supply an ambient Product and bounded query")
	}
	if len(req.Query) > 256 {
		return out, newFailure(KindInvalidFilter, "launcher.search", "bounded search text is too long", false, "limit text to 256 characters")
	}
	tx, err := beginRead(ctx, s, "launcher.search")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readProduct(ctx, tx, req.Product); err != nil {
		return out, err
	}
	needle := "%" + strings.ToLower(req.Query) + "%"
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.kind,w.title,COALESCE(NULLIF(l.human_key,''),json_extract(w.intent_json,'$.external_ref'),''),COALESCE(l.url,''),w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,
		(SELECT count(DISTINCT wp2.project_id) FROM work_projects wp2 JOIN product_projects pp2 ON pp2.project_id=wp2.project_id WHERE wp2.work_id=w.id AND pp2.product_id=?),
		EXISTS (SELECT 1 FROM relations br JOIN work_items b ON b.id=br.work_id_from WHERE br.work_id_to=w.id AND br.kind='blocks' AND b.lifecycle IN ('needed','in_progress'))
		FROM work_items w LEFT JOIN linear_issue_links l ON l.work_id=w.id WHERE EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=w.id AND pp.product_id=?) AND w.lifecycle IN ('needed','in_progress') AND lower(w.id || ' ' || w.title || ' ' || w.kind) LIKE ?
		ORDER BY w.urgency ASC,w.priority,w.created_at DESC,w.id LIMIT ?`, req.Product, req.Product, needle, limit+1)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.search", "cannot search Product work", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var item LauncherWork
		if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.LinearIssueKey, &item.LinearIssueURL, &item.Lifecycle, &item.Priority, &item.Urgency, &item.CreatedAt, &item.UpdatedAt, &item.ProjectCount, &item.Blocked); err != nil {
			rows.Close()
			return out, err
		}
		item.Ready = item.Lifecycle == "needed" && !item.Blocked
		out.Works = append(out.Works, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if len(out.Works) > limit {
		out.Works = out.Works[:limit]
		out.Omissions = append(out.Omissions, "Product work matches omitted by launcher limit")
	}
	ids := make([]string, len(out.Works))
	for i := range out.Works {
		ids[i] = out.Works[i].ID
	}
	blockers, err := launcherBlockersForWorks(ctx, tx, ids)
	if err != nil {
		return out, err
	}
	for i := range out.Works {
		out.Works[i].Blockers = blockers[out.Works[i].ID]
	}
	if out.Works == nil {
		out.Works = []LauncherWork{}
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "launcher.search", ResolvedScope{ProductID: req.Product}, []string{"urgency", "priority", "created_at", "id"})
	return out, err
}

// QueryLauncherProduct is one bounded transaction and intentionally has no
// per-work calls. The SQL joins Product membership before projecting work, so a
// cross-Project item remains one row with a breadth count.
func (s *Store) QueryLauncherProduct(ctx context.Context, req LauncherProductRequest) (LauncherProductResult, error) {
	var out LauncherProductResult
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	depth := req.Depth
	if depth == 0 {
		depth = 3
	}
	if depth < 1 || depth > 3 {
		return out, newFailure(KindInvalidFilter, "launcher.product", "relation depth must be between 1 and 3", false, "supply a bounded relation depth")
	}
	tx, err := beginRead(ctx, s, "launcher.product")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readProduct(ctx, tx, req.Product); err != nil {
		return out, err
	}
	// The work list orders by last activity derived from the event log, so
	// the ordering is correct whatever release wrote the events: a fold
	// generation that predates a stored marker cannot drift it (CD-0111
	// rolling upgrade). The derivation is migration 108's read-side twin:
	// the newest work_item-subject event time except work.removed, with
	// each RFC3339Nano stamp normalized to the fixed nine-digit fraction
	// form, so equal-width text order equals time order, falling back to
	// the same normalization of updated_at when the log retains nothing
	// for the row. domain_events_subject (subject_type, subject_id, seq)
	// serves the per-row scan.
	q := `SELECT w.id,w.kind,w.title,COALESCE(NULLIF(l.human_key,''),json_extract(w.intent_json,'$.external_ref'),''),COALESCE(l.url,''),w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,
		(SELECT count(DISTINCT wp2.project_id) FROM work_projects wp2 JOIN product_projects pp2 ON pp2.project_id=wp2.project_id WHERE wp2.work_id=w.id AND pp2.product_id=?),
		EXISTS (SELECT 1 FROM relations br JOIN work_items b ON b.id=br.work_id_from WHERE br.work_id_to=w.id AND br.kind='blocks' AND b.lifecycle IN ('needed','in_progress'))
		FROM work_items w LEFT JOIN linear_issue_links l ON l.work_id=w.id WHERE EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=w.id AND pp.product_id=? AND w.lifecycle IN ('needed','in_progress'))
		ORDER BY (COALESCE((SELECT MAX(substr(e.occurred_at, 1, 19) || '.' || substr(CASE WHEN substr(e.occurred_at, 20, 1) = '.' THEN substr(e.occurred_at, 21, length(e.occurred_at) - 21) ELSE '' END || '000000000', 1, 9) || 'Z') FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=w.id AND e.kind<>'work.removed'), substr(w.updated_at, 1, 19) || '.' || substr(CASE WHEN substr(w.updated_at, 20, 1) = '.' THEN substr(w.updated_at, 21, length(w.updated_at) - 21) ELSE '' END || '000000000', 1, 9) || 'Z')) DESC,w.id`
	rows, err := tx.QueryContext(ctx, q, req.Product, req.Product)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.product", "cannot read Product work", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var item LauncherWork
		if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.LinearIssueKey, &item.LinearIssueURL, &item.Lifecycle, &item.Priority, &item.Urgency, &item.CreatedAt, &item.UpdatedAt, &item.ProjectCount, &item.Blocked); err != nil {
			rows.Close()
			return out, err
		}
		item.Ready = item.Lifecycle == "needed" && !item.Blocked
		out.Works = append(out.Works, item)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// The completed-history drill-down segment. It is one grouped read in the
	// same transaction, not a per-work fan-out, and readiness is not computed
	// for terminal items: their marker is the terminal state itself.
	trows, err := tx.QueryContext(ctx, `SELECT w.id,w.kind,w.title,COALESCE(NULLIF(l.human_key,''),json_extract(w.intent_json,'$.external_ref'),''),COALESCE(l.url,''),w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,coalesce(w.terminal_time,''),
		(SELECT count(DISTINCT wp2.project_id) FROM work_projects wp2 JOIN product_projects pp2 ON pp2.project_id=wp2.project_id WHERE wp2.work_id=w.id AND pp2.product_id=?)
		FROM work_items w LEFT JOIN linear_issue_links l ON l.work_id=w.id WHERE EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=w.id AND pp.product_id=? AND w.lifecycle IN ('completed','cancelled','superseded'))
		ORDER BY w.terminal_time DESC,w.id LIMIT ?`, req.Product, req.Product, limit+1)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.product", "cannot read Product terminal work", true, "retry once the database is readable", err)
	}
	for trows.Next() {
		var item LauncherWork
		if err := trows.Scan(&item.ID, &item.Kind, &item.Title, &item.LinearIssueKey, &item.LinearIssueURL, &item.Lifecycle, &item.Priority, &item.Urgency, &item.CreatedAt, &item.UpdatedAt, &item.TerminalAt, &item.ProjectCount); err != nil {
			trows.Close()
			return out, err
		}
		item.Terminal = terminalState(item.Lifecycle)
		out.TerminalWorks = append(out.TerminalWorks, item)
	}
	if err := trows.Close(); err != nil {
		return out, err
	}
	if err := trows.Err(); err != nil {
		return out, err
	}
	if len(out.TerminalWorks) > limit {
		out.TerminalWorks = out.TerminalWorks[:limit]
		out.Omissions = append(out.Omissions, "Product terminal work omitted by launcher limit")
	}
	// All Product edges are read in the same transaction as the rows. The
	// blocked_by edge is a display inverse of a stored blocks edge, not a stored
	// relation. It carries the inverse label the relation vocabulary declares for
	// blocks, so it cannot be mistaken for the stored depends_on kind.
	erows, err := tx.QueryContext(ctx, `SELECT r.id,r.kind,r.work_id_from,r.work_id_to FROM relations r WHERE r.kind IN ('parent','blocks','supersedes','implements') AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=r.work_id_from AND pp.product_id=?) AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=r.work_id_to AND pp.product_id=?) ORDER BY r.kind,r.work_id_from,r.work_id_to LIMIT 201`, req.Product, req.Product)
	if err != nil {
		return out, err
	}
	for erows.Next() {
		var e RelationEdge
		var relationID int64
		if err := erows.Scan(&relationID, &e.Kind, &e.Source, &e.Target); err != nil {
			erows.Close()
			return out, err
		}
		e.Depth = 1
		e.RelationID = strconv.FormatInt(relationID, 10)
		out.Edges = append(out.Edges, e)
		if e.Kind == "blocks" {
			out.Edges = append(out.Edges, RelationEdge{Kind: "blocked_by", Source: e.Target, Target: e.Source, Depth: 1, RelationID: e.RelationID})
		}
	}
	if err := erows.Close(); err != nil {
		return out, err
	}
	if err := erows.Err(); err != nil {
		return out, err
	}
	if len(out.Edges) > 200 {
		// A stored blocks edge expands to a display-only blocked_by inverse, so
		// preserve the first 200 stored edges and their inverses as one unit.
		trimmed := make([]RelationEdge, 0, 400)
		stored := 0
		for _, edge := range out.Edges {
			if edge.Kind != "blocked_by" {
				if stored == 200 {
					break
				}
				stored++
			}
			trimmed = append(trimmed, edge)
		}
		out.Edges = trimmed
		out.Omissions = append(out.Omissions, "relation edges omitted by launcher limit")
	}
	ids := make([]string, len(out.Works))
	for i := range out.Works {
		ids[i] = out.Works[i].ID
	}
	blockers, err := launcherBlockersForWorks(ctx, tx, ids)
	if err != nil {
		return out, err
	}
	for i := range out.Works {
		out.Works[i].Blockers = blockers[out.Works[i].ID]
	}
	if out.Works == nil {
		out.Works = []LauncherWork{}
	}
	if out.TerminalWorks == nil {
		out.TerminalWorks = []LauncherWork{}
	}
	if out.Edges == nil {
		out.Edges = []RelationEdge{}
	}
	omissions := append([]string(nil), out.Omissions...)
	out.ResultMeta, err = queryMeta(ctx, tx, "launcher.product", ResolvedScope{ProductID: req.Product}, []string{"last_activity_at", "id"})
	out.Omissions = append(out.Omissions, omissions...)
	return out, err
}

func launcherBlockersForWorks(ctx context.Context, tx *sql.Tx, workIDs []string) (map[string][]LauncherBlocker, error) {
	out := make(map[string][]LauncherBlocker)
	if len(workIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(workIDs))
	args := make([]any, len(workIDs))
	for i, id := range workIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.work_id_to,b.id,b.title,COALESCE((SELECT c.resolution_authority FROM workflow_external_conditions c WHERE c.work_id=b.id AND c.condition_state='open' ORDER BY c.condition_id LIMIT 1),'canonical'),COALESCE((SELECT c.condition_id FROM workflow_external_conditions c WHERE c.work_id=b.id AND c.condition_state='open' ORDER BY c.condition_id LIMIT 1),''),b.created_at,COALESCE((SELECT l.human_key FROM linear_issue_links l WHERE l.work_id=b.id LIMIT 1),'') FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to IN (`+strings.Join(placeholders, ",")+`) AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress') ORDER BY r.work_id_to,b.created_at,b.id`, args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every work ID stays parameter-bound.
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var blockedWork string
		var b LauncherBlocker
		if err := rows.Scan(&blockedWork, &b.ID, &b.Title, &b.Authority, &b.ConditionID, &b.Age, &b.IssueKey); err != nil {
			return nil, err
		}
		b.External = b.ConditionID != ""
		out[blockedWork] = append(out[blockedWork], b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// LauncherDomainRow is one current Domain in the launcher's S2 Domain section.
type LauncherDomainRow struct {
	DomainID       string
	Name           string
	Purpose        string
	ParentDomainID string
	HomeDomain     bool
	// CurrentLawCount is the number of accepted law records homed on this
	// Domain (superseded law excluded).
	CurrentLawCount int
	// ActiveWorkCount is the number of nonterminal work items whose current
	// contract is bound to this Domain (home or affected footprint).
	ActiveWorkCount int
}

// LauncherDomainsResult is the bounded S2 Domain navigation read: the current
// Domain hierarchy with its law and work counts, and derived unresolved
// overlap — one Product, one registry watermark, no fourth screen.
// Architecture relation tuples stay in the exact Domain-detail read.
//
// The reads fail independently at their bounds. RegistryIncomplete marks a
// Domain row page that stopped short of the registry's end;
// OverlapsTruncated marks the overlap enumeration. A bounded overlap read
// never withholds the registry rows or their watermark.
type LauncherDomainsResult struct {
	ResultMeta
	Registry           *DomainRegistryView
	Domains            []LauncherDomainRow
	Overlaps           []DomainOverlapPair
	RegistryIncomplete bool
	OverlapsTruncated  bool
}

func (s *Store) QueryLauncherDomains(ctx context.Context, req LauncherProductRequest) (LauncherDomainsResult, error) {
	return queryLauncherDomains(ctx, s.db, req)
}

func queryLauncherDomains(ctx context.Context, q queryer, req LauncherProductRequest) (LauncherDomainsResult, error) {
	var out LauncherDomainsResult
	list, err := queryDomainList(ctx, q, DomainListRequest{Product: req.Product, Limit: domainListMaxLimit})
	if err != nil {
		return out, err
	}
	overlaps, err := queryDomainOverlaps(ctx, q, DomainOverlapsRequest{Product: req.Product})
	if err != nil {
		return out, err
	}
	for _, domain := range list.Domains {
		out.Domains = append(out.Domains, LauncherDomainRow{DomainID: domain.DomainID, Name: domain.Name, Purpose: domain.Purpose, ParentDomainID: domain.ParentID, HomeDomain: domain.HomeDomain})
	}
	// Current law and active Domain-bound work render per Domain row without
	// fan-out: two grouped bounded queries, matched in Go.
	lawCounts := map[string]int{}
	lawRows, err := q.QueryContext(ctx, `
		SELECT h.domain_id, count(*) FROM law_domain_homes h
		JOIN law_subjects s ON s.home_project_id=h.home_project_id AND s.home_locator_id=h.home_locator_id AND s.law_id=h.law_id
		WHERE h.product_id=? AND s.status='accepted'
		GROUP BY h.domain_id`, req.Product)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot read current law", true, "retry once the knowledge projection is readable", err)
	}
	for lawRows.Next() {
		var domainID string
		var count int
		if err := lawRows.Scan(&domainID, &count); err != nil {
			lawRows.Close()
			return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot decode current law", true, "retry once the knowledge projection is readable", err)
		}
		lawCounts[domainID] = count
	}
	if err := lawRows.Err(); err != nil {
		lawRows.Close()
		return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot enumerate current law", true, "retry once the knowledge projection is readable", err)
	}
	lawRows.Close()
	workCounts := map[string]int{}
	workRows, err := q.QueryContext(ctx, `
		SELECT b.home_domain_id, count(DISTINCT c.work_id) FROM workflow_contracts c
		JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version
		JOIN work_items w ON w.id=c.work_id
		WHERE c.superseded_by IS NULL AND w.lifecycle NOT IN ('completed','cancelled','superseded') AND b.product_id=?
		  AND (SELECT count(*) FROM workflow_contracts c2 WHERE c2.work_id=c.work_id AND c2.superseded_by IS NULL)=1
		GROUP BY b.home_domain_id`, req.Product)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot read Domain-bound work", true, "retry once the workflow projection is readable", err)
	}
	for workRows.Next() {
		var domainID string
		var count int
		if err := workRows.Scan(&domainID, &count); err != nil {
			workRows.Close()
			return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot decode Domain-bound work", true, "retry once the workflow projection is readable", err)
		}
		workCounts[domainID] = count
	}
	if err := workRows.Err(); err != nil {
		workRows.Close()
		return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot enumerate Domain-bound work", true, "retry once the workflow projection is readable", err)
	}
	workRows.Close()
	for i := range out.Domains {
		out.Domains[i].CurrentLawCount = lawCounts[out.Domains[i].DomainID]
		out.Domains[i].ActiveWorkCount = workCounts[out.Domains[i].DomainID]
	}
	out.Registry = list.Registry
	out.Overlaps = overlaps.Pairs
	if overlaps.Truncated {
		out.OverlapsTruncated = true
	}
	if list.NextCursor != nil {
		out.RegistryIncomplete = true
	}
	omissions := append([]string{}, list.Omissions...)
	omissions = append(omissions, overlaps.Omissions...)
	out.ResultMeta = ResultMeta{QueryID: "C14.DomainNav", ContractVersion: "C14/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: "authoritative", OrderingKeys: []string{"name", "domain_id"}, Omissions: omissions}
	if out.RegistryIncomplete {
		out.Omissions = append(out.Omissions, "domain_registry_rows_omitted")
	}
	if out.OverlapsTruncated {
		out.Omissions = append(out.Omissions, "domain_overlaps_bounded")
	}
	return out, nil
}
