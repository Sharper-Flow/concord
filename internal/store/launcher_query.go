package store

// This file is an in-process launcher read adapter. It deliberately is not a
// new Product-memory query: it composes the accepted projections in one read
// transaction for the two launcher modes.

import (
	"context"
	"database/sql"
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
}

type LauncherWork struct {
	ID           string
	Kind         string
	Title        string
	Lifecycle    string
	Priority     int64
	Urgency      string
	CreatedAt    string
	UpdatedAt    string
	ProjectCount int
	Blocked      bool
	Ready        bool
	Blockers     []LauncherBlocker
}

type LauncherProductResult struct {
	ResultMeta
	Works []LauncherWork
	Edges []RelationEdge
}

type LauncherWorkRequest struct {
	Product string
	Work    string
	Limit   int
}

type LauncherWorkResult struct {
	ResultMeta
	Work     LauncherWork
	Projects []ProjectMembership
	Events   []TimelineEvent
	Workflow *WorkflowReadProjection
	Edges    []RelationEdge
}

// LauncherSearchResult is a private launcher projection, not a new PM query or
// public tool. One operation owns the Product-scoped work and knowledge matches.
type LauncherSearchRequest struct {
	Product string
	Query   string
	Limit   int
}

type LauncherSearchResult struct {
	ResultMeta
	Works              []LauncherWork
	Knowledge          []KnowledgeItem
	KnowledgeWatermark string
	KnowledgeAuthority string
	KnowledgeOmissions []string
}

func (s *Store) QueryLauncherSearch(ctx context.Context, req LauncherSearchRequest) (LauncherSearchResult, error) {
	var out LauncherSearchResult
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
	home, homeErr := s.ResolveKnowledgeQueryHome(ctx, req.Product, "", KnowledgeHome{}, "launcher.search")
	knowledgeWatermark, knowledgeAuthority := "unavailable", "unavailable"
	knowledgeAvailable := homeErr == nil
	if knowledgeAvailable {
		knowledgeWatermark, knowledgeAuthority, err = validateKnowledgeHomeForQuery(ctx, s, home, true, "launcher.search")
		if err != nil {
			return out, err
		}
		if knowledgeWatermark == "" {
			knowledgeWatermark = "unindexed"
		}
	} else {
		out.KnowledgeOmissions = append(out.KnowledgeOmissions, "knowledge_home_unavailable")
	}
	kinds, err := knowledgeKinds(nil)
	if err != nil {
		return out, err
	}
	if knowledgeAvailable && knowledgeAuthority == "authoritative" {
		if err := validateKnowledgeCoverage(ctx, s, home, knowledgeWatermark, kinds); err != nil {
			return out, err
		}
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
	rows, err := tx.QueryContext(ctx, `SELECT w.id,w.kind,w.title,w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,
		(SELECT count(DISTINCT wp2.project_id) FROM work_projects wp2 JOIN product_projects pp2 ON pp2.project_id=wp2.project_id WHERE wp2.work_id=w.id AND pp2.product_id=?),
		EXISTS (SELECT 1 FROM relations br JOIN work_items b ON b.id=br.work_id_from WHERE br.work_id_to=w.id AND br.kind='blocks' AND b.lifecycle IN ('needed','in_progress'))
		FROM work_items w WHERE EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=w.id AND pp.product_id=?) AND w.lifecycle IN ('needed','in_progress') AND lower(w.id || ' ' || w.title || ' ' || w.kind) LIKE ?
		ORDER BY w.urgency ASC,w.priority,w.created_at DESC,w.id LIMIT ?`, req.Product, req.Product, needle, limit+1)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.search", "cannot search Product work", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var item LauncherWork
		if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.Lifecycle, &item.Priority, &item.Urgency, &item.CreatedAt, &item.UpdatedAt, &item.ProjectCount, &item.Blocked); err != nil {
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
	if knowledgeAvailable {
		knowledgeReq := Q9Request{Product: req.Product, Text: req.Query, Limit: limit + 1, AllowDegraded: true, Home: home}
		knowledgeQuery, args := buildKnowledgeQuery(knowledgeReq, kinds, nil, limit+1)
		knowledgeRows, err := tx.QueryContext(ctx, knowledgeQuery, args...)
		if err != nil {
			return out, wrapFailure(KindUnavailable, "launcher.search", "cannot search Product knowledge", true, "retry once the database is readable", err)
		}
		for knowledgeRows.Next() {
			item, err := scanKnowledgeItem(knowledgeRows)
			if err != nil {
				knowledgeRows.Close()
				return out, err
			}
			out.Knowledge = append(out.Knowledge, item)
		}
		if err := knowledgeRows.Close(); err != nil {
			return out, err
		}
		if err := knowledgeRows.Err(); err != nil {
			return out, err
		}
	}
	if len(out.Knowledge) > limit {
		out.Knowledge = out.Knowledge[:limit]
		out.KnowledgeOmissions = append(out.KnowledgeOmissions, "knowledge matches omitted by launcher limit")
	}
	if knowledgeAvailable && knowledgeAuthority == "authoritative" {
		// Tx-scoped: this function holds the launcher search read
		// transaction, and with one pooled connection a nested s.db query
		// would park on the pool forever.
		out.KnowledgeOmissions = append(out.KnowledgeOmissions, knowledgeCoverageOmissions(ctx, tx, home, knowledgeWatermark)...)
	} else if knowledgeAvailable {
		out.KnowledgeOmissions = append(out.KnowledgeOmissions, "knowledge_index_lagging_or_unreachable")
	}
	out.KnowledgeWatermark, out.KnowledgeAuthority = knowledgeWatermark, knowledgeAuthority
	omissions := append([]string(nil), out.Omissions...)
	out.ResultMeta, err = queryMeta(ctx, tx, "launcher.search", ResolvedScope{ProductID: req.Product}, []string{"urgency", "priority", "created_at", "id"})
	out.Omissions = append(out.Omissions, omissions...)
	out.Omissions = append(out.Omissions, out.KnowledgeOmissions...)
	if out.Works == nil {
		out.Works = []LauncherWork{}
	}
	if out.Knowledge == nil {
		out.Knowledge = []KnowledgeItem{}
	}
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
	q := `SELECT w.id,w.kind,w.title,w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,
		(SELECT count(DISTINCT wp2.project_id) FROM work_projects wp2 JOIN product_projects pp2 ON pp2.project_id=wp2.project_id WHERE wp2.work_id=w.id AND pp2.product_id=?),
		EXISTS (SELECT 1 FROM relations br JOIN work_items b ON b.id=br.work_id_from WHERE br.work_id_to=w.id AND br.kind='blocks' AND b.lifecycle IN ('needed','in_progress'))
		FROM work_items w WHERE EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=w.id AND pp.product_id=? AND w.lifecycle IN ('needed','in_progress'))
		ORDER BY w.urgency ASC,w.priority,w.created_at DESC,w.id LIMIT ?`
	rows, err := tx.QueryContext(ctx, q, req.Product, req.Product, limit+1)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.product", "cannot read Product work", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var item LauncherWork
		if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.Lifecycle, &item.Priority, &item.Urgency, &item.CreatedAt, &item.UpdatedAt, &item.ProjectCount, &item.Blocked); err != nil {
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
		out.Omissions = append(out.Omissions, "Product work omitted by launcher limit")
	}
	// All Product edges are read in the same transaction as the rows. The
	// depends_on inverse is a display label, not a second stored relation.
	erows, err := tx.QueryContext(ctx, `SELECT r.kind,r.work_id_from,r.work_id_to FROM relations r WHERE r.kind IN ('parent','includes','blocks','supersedes','implements') AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=r.work_id_from AND pp.product_id=?) AND EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=r.work_id_to AND pp.product_id=?) ORDER BY r.kind,r.work_id_from,r.work_id_to LIMIT 201`, req.Product, req.Product)
	if err != nil {
		return out, err
	}
	for erows.Next() {
		var e RelationEdge
		if err := erows.Scan(&e.Kind, &e.Source, &e.Target); err != nil {
			erows.Close()
			return out, err
		}
		out.Edges = append(out.Edges, e)
		if e.Kind == "blocks" {
			out.Edges = append(out.Edges, RelationEdge{Kind: "depends_on", Source: e.Target, Target: e.Source})
		}
	}
	if err := erows.Close(); err != nil {
		return out, err
	}
	if err := erows.Err(); err != nil {
		return out, err
	}
	if len(out.Edges) > 200 {
		// A stored blocks edge expands to a display-only depends_on inverse, so
		// preserve the first 200 stored edges and their inverses as one unit.
		trimmed := make([]RelationEdge, 0, 400)
		stored := 0
		for _, edge := range out.Edges {
			if edge.Kind != "depends_on" {
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
	if out.Edges == nil {
		out.Edges = []RelationEdge{}
	}
	omissions := append([]string(nil), out.Omissions...)
	out.ResultMeta, err = queryMeta(ctx, tx, "launcher.product", ResolvedScope{ProductID: req.Product}, []string{"urgency", "priority", "created_at", "id"})
	out.Omissions = append(out.Omissions, omissions...)
	return out, err
}

func launcherBlockers(ctx context.Context, tx *sql.Tx, workID string) ([]LauncherBlocker, error) {
	result, err := launcherBlockersForWorks(ctx, tx, []string{workID})
	return result[workID], err
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
	rows, err := tx.QueryContext(ctx, `SELECT r.work_id_to,b.id,b.title,COALESCE((SELECT c.resolution_authority FROM workflow_external_conditions c WHERE c.work_id=b.id AND c.condition_state='open' ORDER BY c.condition_id LIMIT 1),'canonical'),COALESCE((SELECT c.condition_id FROM workflow_external_conditions c WHERE c.work_id=b.id AND c.condition_state='open' ORDER BY c.condition_id LIMIT 1),''),b.created_at FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to IN (`+strings.Join(placeholders, ",")+`) AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress') ORDER BY r.work_id_to,b.created_at,b.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var blockedWork string
		var b LauncherBlocker
		if err := rows.Scan(&blockedWork, &b.ID, &b.Title, &b.Authority, &b.ConditionID, &b.Age); err != nil {
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

func (s *Store) QueryLauncherWork(ctx context.Context, req LauncherWorkRequest) (LauncherWorkResult, error) {
	var out LauncherWorkResult
	tx, err := beginRead(ctx, s, "launcher.work")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if req.Product == "" || req.Work == "" {
		return out, unknownScope("launcher.work", "work detail requires ambient Product and work")
	}
	if _, err := readProduct(ctx, tx, req.Product); err != nil {
		return out, err
	}
	base, err := readOneWork(ctx, tx, req.Work)
	if err != nil {
		return out, err
	}
	var inScope bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? AND pp.product_id=?)`, req.Work, req.Product).Scan(&inScope); err != nil {
		return out, err
	}
	if !inScope {
		return out, unknownScope("launcher.work", "work is not in ambient Product")
	}
	w := LauncherWork{ID: base.ID, Kind: base.Kind, Title: base.Title, Lifecycle: base.Lifecycle, Priority: base.Priority, Urgency: base.Urgency, CreatedAt: base.CreatedAt, UpdatedAt: base.UpdatedAt, Blocked: base.Blocked, Ready: base.Ready}
	w.ProjectCount, err = projectCount(ctx, tx, req.Work)
	if err != nil {
		return out, err
	}
	w.Blockers, err = launcherBlockers(ctx, tx, req.Work)
	if err != nil {
		return out, err
	}
	w.Blocked = len(w.Blockers) > 0
	w.Ready = w.Lifecycle == "needed" && !w.Blocked
	out.Work = w
	out.Projects, err = readWorkProjects(ctx, tx, req.Work)
	if err != nil {
		return out, err
	}
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	erows, err := tx.QueryContext(ctx, `SELECT event_id,seq,kind,actor,occurred_at,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? ORDER BY seq DESC LIMIT ?`, req.Work, limit)
	if err != nil {
		return out, err
	}
	for erows.Next() {
		var id, kind, actor, occurred, payload string
		var seq int64
		if err := erows.Scan(&id, &seq, &kind, &actor, &occurred, &payload); err != nil {
			erows.Close()
			return out, err
		}
		e, err := decodeTimelineEvent(id, seq, kind, actor, occurred, []byte(payload))
		if err != nil {
			erows.Close()
			return out, err
		}
		out.Events = append(out.Events, e)
	}
	if err := erows.Close(); err != nil {
		return out, err
	}
	if out.Events == nil {
		out.Events = []TimelineEvent{}
	}
	out.Workflow, err = readWorkflowSummaryTx(ctx, tx, req.Work)
	if err != nil {
		return out, err
	}
	// The work detail relation read is bounded and stays in this transaction.
	rrows, err := tx.QueryContext(ctx, `SELECT r.kind,r.work_id_from,r.work_id_to FROM relations r WHERE (r.work_id_from=? OR r.work_id_to=?) ORDER BY r.kind,r.work_id_from,r.work_id_to LIMIT 100`, req.Work, req.Work)
	if err != nil {
		return out, err
	}
	for rrows.Next() {
		var e RelationEdge
		if err := rrows.Scan(&e.Kind, &e.Source, &e.Target); err != nil {
			rrows.Close()
			return out, err
		}
		out.Edges = append(out.Edges, e)
	}
	if err := rrows.Close(); err != nil {
		return out, err
	}
	if out.Edges == nil {
		out.Edges = []RelationEdge{}
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "launcher.work", ResolvedScope{ProductID: req.Product, WorkID: req.Work}, []string{"seq"})
	return out, err
}

func projectCount(ctx context.Context, tx *sql.Tx, workID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(DISTINCT project_id) FROM work_projects WHERE work_id=?`, workID).Scan(&count)
	return count, err
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

// LauncherDomainRelation is one typed architecture relation edge.
type LauncherDomainRelation struct {
	Kind           string
	SourceDomainID string
	TargetDomainID string
	State          string
}

// LauncherDomainsResult is the bounded S2 Domain navigation read: the current
// Domain hierarchy, its typed architecture relations, and derived unresolved
// overlap — one Product, one registry watermark, no fourth screen.
type LauncherDomainsResult struct {
	ResultMeta
	Registry  *DomainRegistryView
	Domains   []LauncherDomainRow
	Relations []LauncherDomainRelation
	Overlaps  []DomainOverlapPair
	Truncated bool
}

func (s *Store) QueryLauncherDomains(ctx context.Context, req LauncherProductRequest) (LauncherDomainsResult, error) {
	var out LauncherDomainsResult
	list, err := s.QueryDomainList(ctx, DomainListRequest{Product: req.Product, Limit: domainListMaxLimit})
	if err != nil {
		return out, err
	}
	overlaps, err := s.QueryDomainOverlaps(ctx, DomainOverlapsRequest{Product: req.Product})
	if err != nil {
		return out, err
	}
	for _, domain := range list.Domains {
		out.Domains = append(out.Domains, LauncherDomainRow{DomainID: domain.DomainID, Name: domain.Name, Purpose: domain.Purpose, ParentDomainID: domain.ParentID, HomeDomain: domain.HomeDomain})
	}
	// Current law and active Domain-bound work render per Domain row without
	// fan-out: two grouped bounded queries, matched in Go.
	lawCounts := map[string]int{}
	lawRows, err := s.db.QueryContext(ctx, `
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
	workRows, err := s.db.QueryContext(ctx, `
		SELECT b.home_domain_id, count(*) FROM workflow_contracts c
		JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version
		JOIN work_items w ON w.id=c.work_id
		WHERE c.superseded_by IS NULL AND w.lifecycle NOT IN ('completed','cancelled','superseded') AND b.product_id=?
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
	rows, err := s.db.QueryContext(ctx, `SELECT kind,source_domain_id,target_domain_id,state FROM domain_architecture_relations WHERE product_id=? ORDER BY kind,source_domain_id,target_domain_id LIMIT ?`, req.Product, domainListMaxLimit+1)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot read Domain relations", true, "retry once the knowledge projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var relation LauncherDomainRelation
		if err := rows.Scan(&relation.Kind, &relation.SourceDomainID, &relation.TargetDomainID, &relation.State); err != nil {
			return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot decode Domain relations", true, "retry once the knowledge projection is readable", err)
		}
		out.Relations = append(out.Relations, relation)
	}
	if err := rows.Err(); err != nil {
		return out, wrapFailure(KindUnavailable, "launcher.domains", "cannot enumerate Domain relations", true, "retry once the knowledge projection is readable", err)
	}
	if len(out.Relations) > domainListMaxLimit {
		out.Relations = out.Relations[:domainListMaxLimit]
		out.Truncated = true
	}
	out.Registry = list.Registry
	out.Overlaps = overlaps.Pairs
	if overlaps.Truncated {
		out.Truncated = true
	}
	out.ResultMeta = ResultMeta{QueryID: "C14.DomainNav", ContractVersion: "C14/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: "authoritative", OrderingKeys: []string{"name", "domain_id", "kind", "source_domain_id", "target_domain_id"}}
	if out.Truncated {
		out.Omissions = []string{"domain_relations_bounded"}
	}
	return out, nil
}
