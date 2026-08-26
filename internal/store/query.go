package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

const queryContractVersion = "PM1/1.0"

// ResultMeta is the common, machine-readable envelope for live PM1 results.
type ResultMeta struct {
	QueryID                string        `json:"query_id"`
	ContractVersion        string        `json:"contract_version"`
	ResolvedScope          ResolvedScope `json:"resolved_scope"`
	SourceVersionWatermark int64         `json:"source_version_watermark"`
	Authority              string        `json:"authority"`
	Freshness              Freshness     `json:"freshness"`
	OrderingKeys           []string      `json:"ordering_keys"`
	NextCursor             *string       `json:"next_cursor"`
	Omissions              []string      `json:"omissions"`
	Warnings               []string      `json:"warnings"`
}

type ResolvedScope struct {
	ProductID  string   `json:"product_id,omitempty"`
	ProjectID  string   `json:"project_id,omitempty"`
	WorkID     string   `json:"work_id,omitempty"`
	DomainID   string   `json:"domain_id,omitempty"`
	ProductIDs []string `json:"product_ids,omitempty"`
	ProjectIDs []string `json:"project_ids,omitempty"`
}

type Freshness struct {
	ObservedAt string `json:"observed_at"`
	Age        int64  `json:"age"`
	Stale      bool   `json:"stale"`
}

type Q1Request struct {
	Product string
	Project string
	Limit   int
	Cursor  string
}

type Q2Request struct {
	Product      string
	ProjectIDs   []string
	PreviewLimit int
}

type Q3Request struct {
	Product         string
	Project         string
	ProjectIDs      []string
	WorkIDs         []string
	LifecycleStates []string
	Kind            string
	TagIDs          []string
	PriorityMin     *int64
	PriorityMax     *int64
	TerminalSince   *string
	Detail          string
	Limit           int
	Cursor          string
}

type Q4Request struct {
	Product   string
	Project   string
	Work      string
	Kind      string
	Limit     int
	Cursor    string
	Offset    int
	Depth     int
	NodeLimit int
	EdgeLimit int
}

type Q5Request struct {
	Product string
	Project string
	Kind    string
	Limit   int
	Cursor  string
	Offset  int
}

type Q6Request struct {
	Product string
	Project string
	Work    string
	Limit   int
	Cursor  string
	Offset  int
}

type Q7Request struct {
	Work       string
	Direction  string
	EventKinds []string
	Limit      int
	Cursor     string
}

type Q8Request struct {
	Work          string
	RelationKinds []string
	Direction     string
	Depth         int
}

type Q1Result struct {
	ResultMeta
	Products     []Product           `json:"products,omitempty"`
	Product      *Product            `json:"product,omitempty"`
	Projects     []ProjectMembership `json:"projects,omitempty"`
	CandidateIDs []string            `json:"candidate_ids,omitempty"`
	Result       *Q1ResultPayload    `json:"result,omitempty"`
}

// Q1ResultPayload mirrors specialized fields to reconcile PM1's universal envelope with
// accepted scenario paths without duplicating database state.
type Q1ResultPayload struct {
	Products     []Product           `json:"products,omitempty"`
	Product      *Product            `json:"product,omitempty"`
	Projects     []ProjectMembership `json:"projects,omitempty"`
	CandidateIDs []string            `json:"candidate_ids,omitempty"`
}

type DerivedCounts struct {
	Blocked  int `json:"blocked"`
	Ready    int `json:"ready"`
	Active   int `json:"active"`
	Terminal int `json:"terminal"`
}

type Q2Result struct {
	ResultMeta
	LifecycleCounts map[string]int `json:"lifecycle_counts"`
	DerivedCounts   DerivedCounts  `json:"derived_counts"`
	Items           []WorkItem     `json:"items"`
}

type Q3Result struct {
	ResultMeta
	Items []WorkItem `json:"items"`
}

type WorkItem struct {
	ID         string              `json:"id"`
	Kind       string              `json:"kind"`
	Title      string              `json:"title"`
	Lifecycle  string              `json:"lifecycle"`
	Priority   int64               `json:"priority"`
	Urgency    string              `json:"urgency"`
	CreatedAt  string              `json:"created_at"`
	UpdatedAt  string              `json:"updated_at"`
	TerminalAt string              `json:"terminal_at,omitempty"`
	Narrative  string              `json:"narrative,omitempty"`
	Projects   []ProjectMembership `json:"projects,omitempty"`
	Blocked    bool                `json:"blocked"`
	Ready      bool                `json:"ready"`
	Active     bool                `json:"active"`
	Terminal   bool                `json:"terminal"`
	Blockers   []WorkItem          `json:"blockers,omitempty"`
}

type Q4Result struct {
	ResultMeta
	Items      []WorkItem `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

type Q5Result struct {
	ResultMeta
	Items      []WorkItem `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

type Q6Result struct {
	ResultMeta
	Work       *WorkItem            `json:"work,omitempty"`
	Items      []WorkItem           `json:"items,omitempty"`
	Result     *Q6WorkResultPayload `json:"result,omitempty"`
	NextCursor *string              `json:"next_cursor"`
}

type q6Cursor struct {
	Version int    `json:"version"`
	Product string `json:"product"`
	Project string `json:"project"`
	Order   string `json:"order"`
	Offset  int    `json:"offset"`
}

type Q6WorkResultPayload struct {
	Work *WorkItem `json:"work"`
}

type TimelineEvent struct {
	EventID      string   `json:"event_id"`
	Seq          int      `json:"seq"`
	GlobalSeq    int64    `json:"global_seq,omitempty"`
	Kind         string   `json:"kind"`
	Actor        string   `json:"actor"`
	OccurredAt   string   `json:"occurred_at"`
	From         *string  `json:"from"`
	To           *string  `json:"to"`
	Reason       string   `json:"reason,omitempty"`
	EvidenceRefs []string `json:"evidence_refs"`
}

type Q7Result struct {
	ResultMeta
	Events   []TimelineEvent         `json:"events"`
	Result   *Q7ResultPayload        `json:"result"`
	Workflow *WorkflowReadProjection `json:"workflow,omitempty"`
}

type Q7ResultPayload struct {
	Events   []TimelineEvent         `json:"events"`
	Workflow *WorkflowReadProjection `json:"workflow,omitempty"`
}

type RelationEdge struct {
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Target string `json:"target"`
	Depth  int    `json:"depth"`
}

type Q8Result struct {
	ResultMeta
	Edges  []RelationEdge   `json:"edges"`
	Result *Q8ResultPayload `json:"result"`
}

type Q8ResultPayload struct {
	Edges []RelationEdge `json:"edges"`
}

const (
	queryDefaultLimit = 20
	queryMaxLimit     = 100
	q4DefaultDepth    = 1
	q4MaxDepth        = 3
	// Q4 graph caps keep recursive blocker reads bounded when callers omit caps.
	q4DefaultNodeLimit = 100
	q4MaxNodeLimit     = 100
	q4DefaultEdgeLimit = 100
	q4MaxEdgeLimit     = 200
)

func queryLimit(got int) (int, error) {
	if got == 0 {
		return queryDefaultLimit, nil
	}
	if got < 0 || got > queryMaxLimit {
		return 0, newFailure(KindInvalidFilter, "query", "limit must be between 1 and 100", false, "supply a bounded limit")
	}
	return got, nil
}

func q4GraphBounds(req Q4Request) (int, int, int, error) {
	depth := req.Depth
	if depth == 0 {
		depth = q4DefaultDepth
	}
	if depth < 1 || depth > q4MaxDepth {
		return 0, 0, 0, newFailure(KindInvalidFilter, "PM1.Q4", "depth must be between 1 and 3", false, "supply a bounded graph depth")
	}
	nodes := req.NodeLimit
	if nodes == 0 {
		nodes = q4DefaultNodeLimit
	}
	if nodes < 1 || nodes > q4MaxNodeLimit {
		return 0, 0, 0, newFailure(KindInvalidFilter, "PM1.Q4", "node_limit must be between 1 and 1000", false, "supply a bounded node cap")
	}
	edges := req.EdgeLimit
	if edges == 0 {
		edges = q4DefaultEdgeLimit
	}
	if edges < 1 || edges > q4MaxEdgeLimit {
		return 0, 0, 0, newFailure(KindInvalidFilter, "PM1.Q4", "edge_limit must be between 1 and 1000", false, "supply a bounded edge cap")
	}
	return depth, nodes, edges, nil
}

func beginRead(ctx context.Context, s *Store, op string) (*sql.Tx, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, op, "store is not open", true, "open a live store")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, op, "cannot begin live read snapshot", true, "retry once the database is readable", err)
	}
	return tx, nil
}

func queryMeta(ctx context.Context, tx *sql.Tx, id string, scope ResolvedScope, keys []string) (ResultMeta, error) {
	var watermark sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT max(seq) FROM domain_events`).Scan(&watermark); err != nil {
		return ResultMeta{}, wrapFailure(KindUnavailable, id, "cannot read the live source watermark", true, "retry once the database is readable", err)
	}
	now := time.Now().UTC()
	return ResultMeta{
		QueryID: id, ContractVersion: queryContractVersion, ResolvedScope: scope,
		SourceVersionWatermark: watermark.Int64, Authority: "authoritative",
		Freshness:    Freshness{ObservedAt: now.Format(time.RFC3339Nano), Age: 0, Stale: false},
		OrderingKeys: keys, Omissions: []string{}, Warnings: []string{},
	}, nil
}

func unknownScope(op, detail string) *Failure {
	return newFailure(KindUnknownScope, op, detail, false, "supply an existing Product, Project, or work reference")
}

func readProduct(ctx context.Context, tx *sql.Tx, id string) (Product, error) {
	var p Product
	err := tx.QueryRowContext(ctx, `SELECT id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at FROM products WHERE id = ?`, id).
		Scan(&p.ID, &p.DisplayName, &p.StageMaturity, &p.StageAudienceCommitment, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return p, unknownScope("query", "Product does not exist")
	}
	if err != nil {
		return p, wrapFailure(KindUnavailable, "query", "cannot read Product", true, "retry once the database is readable", err)
	}
	return p, nil
}

func readProjectMemberships(ctx context.Context, tx *sql.Tx, productID string) ([]ProjectMembership, error) {
	rows, err := tx.QueryContext(ctx, `SELECT projects.id, projects.display_name, product_projects.role FROM product_projects JOIN projects ON projects.id = product_projects.project_id WHERE product_projects.product_id = ? ORDER BY CASE product_projects.role WHEN 'primary' THEN 0 ELSE 1 END, projects.display_name, projects.id`, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "query", "cannot read Product Projects", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []ProjectMembership
	for rows.Next() {
		var p ProjectMembership
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.Role); err != nil {
			return nil, wrapFailure(KindUnavailable, "query", "cannot decode Project membership", true, "retry once the database is readable", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "query", "cannot read Project memberships", true, "retry once the database is readable", err)
	}
	return out, nil
}

func (s *Store) QueryQ1(ctx context.Context, req Q1Request) (Q1Result, error) {
	var out Q1Result
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	tx, err := beginRead(ctx, s, "PM1.Q1")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if req.Product != "" && req.Project == "" {
		p, err := readProduct(ctx, tx, req.Product)
		if err != nil {
			return out, err
		}
		projects, err := readProjectMemberships(ctx, tx, req.Product)
		if err != nil {
			return out, err
		}
		out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q1", ResolvedScope{ProductID: req.Product}, []string{"project_role", "project_display_name", "project_id"})
		if err != nil {
			return out, err
		}
		out.Product, out.Projects = &p, projects
		out.Result = &Q1ResultPayload{Product: out.Product, Projects: out.Projects}
		return out, nil
	}
	if req.Project != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, req.Project).Scan(&exists); err == sql.ErrNoRows {
			return out, unknownScope("PM1.Q1", "Project does not exist")
		} else if err != nil {
			return out, wrapFailure(KindUnavailable, "PM1.Q1", "cannot resolve Project", true, "retry once the database is readable", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT products.id FROM products JOIN product_projects ON product_projects.product_id = products.id WHERE product_projects.project_id = ? ORDER BY products.id`, req.Project)
		if err != nil {
			return out, wrapFailure(KindUnavailable, "PM1.Q1", "cannot resolve Project ownership", true, "retry once the database is readable", err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return out, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
		if len(ids) == 0 {
			return out, unknownScope("PM1.Q1", "Project has no Product membership")
		}
		if req.Product != "" {
			found := false
			for _, id := range ids {
				if id == req.Product {
					found = true
					break
				}
			}
			if !found {
				return out, unknownScope("PM1.Q1", "explicit Product does not own Project")
			}
			ids = []string{req.Product}
		}
		if len(ids) > 1 {
			out.CandidateIDs = ids
			failure := newFailure(KindAmbiguousScope, "PM1.Q1", "Project belongs to multiple Products", false, "supply an explicit Product scope")
			failure.CandidateIDs = ids
			return out, failure
		}
		p, err := readProduct(ctx, tx, ids[0])
		if err != nil {
			return out, err
		}
		projects, err := readProjectMemberships(ctx, tx, ids[0])
		if err != nil {
			return out, err
		}
		out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q1", ResolvedScope{ProductID: ids[0], ProjectID: req.Project}, []string{"project_role", "project_display_name", "project_id"})
		if err != nil {
			return out, err
		}
		out.Product = &p
		out.Projects = projects
		out.Result = &Q1ResultPayload{Product: out.Product, Projects: out.Projects}
		return out, nil
	}
	where := ""
	args := []any{}
	if req.Cursor != "" {
		var cursor struct {
			Version           int `json:"version"`
			QueryID, Name, ID string
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(req.Cursor)
		if decodeErr != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.QueryID != "PM1.Q1" || cursor.Name == "" || cursor.ID == "" {
			return out, newFailure(KindInvalidCursor, "PM1.Q1", "cursor is not valid for the Product listing", false, "restart the bounded Product listing")
		}
		where = " WHERE display_name > ? OR (display_name = ? AND id > ?)"
		args = []any{cursor.Name, cursor.Name, cursor.ID}
	}
	query := `SELECT id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at FROM products` + where + ` ORDER BY display_name, id LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q1", "cannot list Products", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.StageMaturity, &p.StageAudienceCommitment, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return out, err
		}
		out.Products = append(out.Products, p)
	}
	var nextCursor *string
	if len(out.Products) > limit {
		last := out.Products[limit-1]
		cursor := struct {
			Version           int `json:"version"`
			QueryID, Name, ID string
		}{1, "PM1.Q1", last.DisplayName, last.ID}
		encoded, encodeErr := json.Marshal(cursor)
		if encodeErr != nil {
			return out, encodeErr
		}
		value := base64.RawURLEncoding.EncodeToString(encoded)
		nextCursor = &value
		out.Products = out.Products[:limit]
	}
	if out.Products == nil {
		out.Products = []Product{}
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q1", ResolvedScope{}, []string{"display_name", "id"})
	if err != nil {
		return out, err
	}
	out.NextCursor = nextCursor
	out.Result = &Q1ResultPayload{Products: out.Products}
	return out, rows.Err()
}

func productWorkScopeSQL(projectIDs []string) (string, []any) {
	args := []any{}
	filter := ""
	if len(projectIDs) > 0 {
		ph := make([]string, len(projectIDs))
		for i, id := range projectIDs {
			ph[i] = "?"
			args = append(args, id)
		}
		filter = " AND wp.project_id IN (" + strings.Join(ph, ",") + ")"
	}
	return filter, args
}

func scopeArgs(product string, projectIDs []string) []any {
	args := []any{product}
	for _, id := range projectIDs {
		args = append(args, id)
	}
	return args
}

func (s *Store) QueryQ2(ctx context.Context, req Q2Request) (Q2Result, error) {
	var out Q2Result
	limit := req.PreviewLimit
	if limit == 0 {
		limit = queryDefaultLimit
	}
	if limit < 1 || limit > queryMaxLimit {
		return out, newFailure(KindInvalidFilter, "PM1.Q2", "preview_limit must be between 1 and 100", false, "supply a bounded preview_limit")
	}
	tx, err := beginRead(ctx, s, "PM1.Q2")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readProduct(ctx, tx, req.Product); err != nil {
		return out, err
	}
	filter, projectArgs := productWorkScopeSQL(req.ProjectIDs)
	// Product scope is a distinct CTE, so every count and preview uses one canonical work identity.
	scope := `WITH scoped AS (SELECT DISTINCT wp.work_id FROM work_projects wp JOIN product_projects pp ON pp.project_id = wp.project_id WHERE pp.product_id = ?` + filter + `)`
	var counts [9]int
	countQuery := scope + ` SELECT
		COALESCE(SUM(CASE WHEN w.lifecycle='needed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle='in_progress' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle='completed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle='superseded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN EXISTS (SELECT 1 FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to=w.id AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress')) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle='needed' AND NOT EXISTS (SELECT 1 FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to=w.id AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress')) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle='in_progress' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle IN ('completed','cancelled','superseded') THEN 1 ELSE 0 END),0)
		FROM work_items w JOIN scoped s ON s.work_id=w.id`
	if err := tx.QueryRowContext(ctx, countQuery, scopeArgs(req.Product, projectArgsToIDs(projectArgs))...).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6], &counts[7], &counts[8]); err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q2", "cannot read Product snapshot", true, "retry once the database is readable", err)
	}
	previewQuery := scope + ` SELECT w.id,w.kind,w.title,w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,coalesce(w.terminal_time,'') FROM work_items w JOIN scoped s ON s.work_id=w.id ORDER BY w.urgency ASC, CASE WHEN w.lifecycle IN ('completed','cancelled','superseded') THEN w.terminal_time ELSE w.created_at END DESC, w.priority, w.id LIMIT ?`
	args2 := scopeArgs(req.Product, projectArgsToIDs(projectArgs))
	args2 = append(args2, limit)
	items, err := scanWorkItems(ctx, tx, previewQuery, args2...)
	if err != nil {
		return out, err
	}
	items, err = attachDerivedFlags(ctx, tx, items)
	if err != nil {
		return out, err
	}
	out.LifecycleCounts = map[string]int{"needed": counts[0], "in_progress": counts[1], "completed": counts[2], "cancelled": counts[3], "superseded": counts[4]}
	out.DerivedCounts = DerivedCounts{Blocked: counts[5], Ready: counts[6], Active: counts[7], Terminal: counts[8]}
	out.Items = items
	if out.Items == nil {
		out.Items = []WorkItem{}
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q2", ResolvedScope{ProductID: req.Product}, []string{"urgency", "relevant_time", "priority", "id"})
	if err != nil {
		return out, err
	}
	return out, nil
}

func projectArgsToIDs(args []any) []string {
	out := make([]string, len(args))
	for i, v := range args {
		out[i] = v.(string)
	}
	return out
}

func validateLifecycleFilters(states []string) ([]string, error) {
	out := append([]string(nil), states...)
	for _, state := range out {
		if !lifecycleStates[state] {
			return nil, newFailure(KindInvalidFilter, "PM1.Q3", "unknown lifecycle state "+state, false, "use one of needed, in_progress, completed, cancelled, superseded")
		}
	}
	sort.Strings(out)
	return out, nil
}

type q3Cursor struct {
	Version                          int `json:"version"`
	QueryID, Product, Project, Order string
	States                           []string `json:"states"`
	Urgency                          string   `json:"urgency"`
	Priority                         int64    `json:"priority"`
	Timestamp, ID                    string
}

func encodeQ3Cursor(c q3Cursor) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func decodeQ3Cursor(raw string, req Q3Request, states []string, order string) (q3Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return q3Cursor{}, newFailure(KindInvalidCursor, "PM1.Q3", "cursor is not valid base64url", false, "restart the bounded listing")
	}
	var c q3Cursor
	if json.Unmarshal(b, &c) != nil || c.Version != 1 || c.QueryID != "PM1.Q3" || c.Product != req.Product || c.Project != req.Project || c.Order != order || !equalStrings(c.States, states) || c.ID == "" || c.Timestamp == "" {
		return q3Cursor{}, newFailure(KindInvalidCursor, "PM1.Q3", "cursor does not match the requested listing", false, "use a cursor returned for the same query and filters")
	}
	if c.Urgency != "" && c.Urgency != "standard" && c.Urgency != "expedite" {
		return q3Cursor{}, newFailure(KindInvalidCursor, "PM1.Q3", "cursor has an invalid urgency value", false, "restart the bounded listing")
	}
	if _, err := time.Parse(time.RFC3339Nano, c.Timestamp); err != nil {
		return q3Cursor{}, newFailure(KindInvalidCursor, "PM1.Q3", "cursor has an invalid ordering timestamp", false, "restart the bounded listing")
	}
	return c, nil
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Store) QueryQ3(ctx context.Context, req Q3Request) (Q3Result, error) {
	var out Q3Result
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	states, err := validateLifecycleFilters(req.LifecycleStates)
	if err != nil {
		return out, err
	}
	if req.Detail != "" && req.Detail != "summary" && req.Detail != "full" {
		return out, newFailure(KindInvalidFilter, "PM1.Q3", "detail must be summary or full", false, "choose a closed detail value")
	}
	if len(req.TagIDs) > 0 {
		return out, newFailure(KindInvalidFilter, "PM1.Q3", "component and tag filters are not yet modeled in the live work projection", false, "remove the unsupported filter or use a Product/Project/work filter")
	}
	if req.PriorityMin != nil && req.PriorityMax != nil && *req.PriorityMin > *req.PriorityMax {
		return out, newFailure(KindInvalidFilter, "PM1.Q3", "priority_min exceeds priority_max", false, "supply an ordered priority range")
	}
	terminalOnly := terminalOnlyQ3(states)
	order := "urgency:asc,priority:asc,relevant_time:desc,id:asc"
	orderSQL := "urgency ASC, priority ASC, relevant_time DESC, id ASC"
	if terminalOnly {
		order = "urgency:asc,terminal_time:desc,priority:asc,id:asc"
		orderSQL = "urgency ASC, relevant_time DESC, priority ASC, id ASC"
	}
	tx, err := beginRead(ctx, s, "PM1.Q3")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readProduct(ctx, tx, req.Product); err != nil {
		return out, err
	}
	projectIDs := req.ProjectIDs
	if len(projectIDs) == 0 && req.Project != "" {
		projectIDs = []string{req.Project}
	}
	filter, projectArgs := productWorkScopeSQL(projectIDs)
	args := scopeArgs(req.Product, projectArgsToIDs(projectArgs))
	placeholders := ""
	stateArgs := []any{}
	if len(states) > 0 {
		ph := make([]string, len(states))
		for i, state := range states {
			ph[i] = "?"
			stateArgs = append(stateArgs, state)
		}
		placeholders = " AND lifecycle IN (" + strings.Join(ph, ",") + ")"
	}
	if req.Kind != "" {
		placeholders += " AND kind = ?"
		stateArgs = append(stateArgs, req.Kind)
	}
	if len(req.WorkIDs) > 0 {
		ph := make([]string, len(req.WorkIDs))
		for i := range ph {
			ph[i] = "?"
			stateArgs = append(stateArgs, req.WorkIDs[i])
		}
		placeholders += " AND id IN (" + strings.Join(ph, ",") + ")"
	}
	if req.PriorityMin != nil {
		placeholders += " AND priority >= ?"
		stateArgs = append(stateArgs, *req.PriorityMin)
	}
	if req.PriorityMax != nil {
		placeholders += " AND priority <= ?"
		stateArgs = append(stateArgs, *req.PriorityMax)
	}
	if req.TerminalSince != nil {
		placeholders += " AND terminal_time >= ?"
		stateArgs = append(stateArgs, *req.TerminalSince)
	}
	cursorSQL := ""
	cursorArgs := []any{}
	if req.Cursor != "" {
		c, err := decodeQ3Cursor(req.Cursor, req, states, order)
		if err != nil {
			return out, err
		}
		if terminalOnly {
			cursorSQL = ` AND (urgency > ? OR (urgency = ? AND relevant_time < ?) OR (urgency = ? AND relevant_time = ? AND priority > ?) OR (urgency = ? AND relevant_time = ? AND priority = ? AND id > ?))`
			cursorArgs = []any{c.Urgency, c.Urgency, c.Timestamp, c.Urgency, c.Timestamp, c.Priority, c.Urgency, c.Timestamp, c.Priority, c.ID}
		} else {
			cursorSQL = ` AND (urgency > ? OR (urgency = ? AND priority > ?) OR (urgency = ? AND priority = ? AND relevant_time < ?) OR (urgency = ? AND priority = ? AND relevant_time = ? AND id > ?))`
			cursorArgs = []any{c.Urgency, c.Urgency, c.Priority, c.Urgency, c.Priority, c.Timestamp, c.Urgency, c.Priority, c.Timestamp, c.ID}
		}
	}
	scope := `WITH scoped AS (SELECT DISTINCT wp.work_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE pp.product_id=?` + filter + `), base AS (SELECT w.id,w.kind,w.title,w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,coalesce(w.terminal_time,'') AS terminal_time, CASE WHEN w.lifecycle IN ('completed','cancelled','superseded') THEN w.terminal_time ELSE w.created_at END AS relevant_time FROM work_items w JOIN scoped s ON s.work_id=w.id)`
	query := scope + ` SELECT id,kind,title,lifecycle,priority,urgency,created_at,updated_at,terminal_time FROM base WHERE 1=1` + placeholders + cursorSQL + ` ORDER BY ` + orderSQL + ` LIMIT ?`
	args = append(args, stateArgs...)
	args = append(args, cursorArgs...)
	args = append(args, limit+1)
	items, err := scanWorkItems(ctx, tx, query, args...)
	if err != nil {
		return out, err
	}
	items, err = attachDerivedFlags(ctx, tx, items)
	if err != nil {
		return out, err
	}
	var nextCursor *string
	if len(items) > limit {
		last := items[limit-1]
		ts := last.CreatedAt
		if terminalOnly {
			ts = last.TerminalAt
		}
		encoded, e := encodeQ3Cursor(q3Cursor{Version: 1, QueryID: "PM1.Q3", Product: req.Product, Project: req.Project, States: states, Order: order, Urgency: last.Urgency, Priority: last.Priority, Timestamp: ts, ID: last.ID})
		if e != nil {
			return out, e
		}
		nextCursor = &encoded
		items = items[:limit]
	}
	out.Items = items
	if out.Items == nil {
		out.Items = []WorkItem{}
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q3", ResolvedScope{ProductID: req.Product, ProjectID: req.Project}, []string{"urgency", "priority", "relevant_time", "id"})
	if err != nil {
		return out, err
	}
	out.NextCursor = nextCursor
	return out, nil
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func terminalOnlyQ3(states []string) bool {
	if len(states) == 0 {
		return false
	}
	for _, state := range states {
		if !terminalState(state) {
			return false
		}
	}
	return true
}

func scanWorkItems(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]WorkItem, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "query", "cannot read work items", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []WorkItem
	for rows.Next() {
		var w WorkItem
		if err := rows.Scan(&w.ID, &w.Kind, &w.Title, &w.Lifecycle, &w.Priority, &w.Urgency, &w.CreatedAt, &w.UpdatedAt, &w.TerminalAt); err != nil {
			return nil, wrapFailure(KindInvariantViolation, "query", "cannot decode work item projection", false, "repair the live projection from its event log", err)
		}
		w.Terminal = terminalState(w.Lifecycle)
		w.Active = w.Lifecycle == "in_progress"
		w.Ready = w.Lifecycle == "needed"
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "query", "cannot read work items", true, "retry once the database is readable", err)
	}
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	return attachWorkMemberships(ctx, tx, out, ids)
}

func terminalState(lifecycle string) bool {
	return lifecycle == "completed" || lifecycle == "cancelled" || lifecycle == "superseded"
}

func attachDerivedFlags(ctx context.Context, tx *sql.Tx, items []WorkItem) ([]WorkItem, error) {
	if len(items) == 0 {
		return items, nil
	}
	ph := make([]string, len(items))
	args := make([]any, len(items))
	byID := make(map[string]int, len(items))
	for i := range items {
		ph[i] = "?"
		args[i] = items[i].ID
		byID[items[i].ID] = i
	}
	rows, err := tx.QueryContext(ctx, `SELECT w.id, EXISTS (SELECT 1 FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to=w.id AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress')) FROM work_items w WHERE w.id IN (`+strings.Join(ph, ",")+`)`, args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every work ID stays parameter-bound.
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "query", "cannot derive work readiness", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var blocked bool
		if err := rows.Scan(&id, &blocked); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			items[i].Blocked = blocked
			items[i].Ready = items[i].Lifecycle == "needed" && !blocked
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func attachWorkMemberships(ctx context.Context, tx *sql.Tx, items []WorkItem, ids []string) ([]WorkItem, error) {
	if len(ids) == 0 {
		return items, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	byID := map[string]int{}
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
		byID[id] = i
	}
	rows, err := tx.QueryContext(ctx, `SELECT wp.work_id, p.id,p.display_name,wp.role FROM work_projects wp JOIN projects p ON p.id=wp.project_id WHERE wp.work_id IN (`+strings.Join(ph, ",")+`) ORDER BY CASE wp.role WHEN 'primary' THEN 0 ELSE 1 END,p.id`, args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every work ID stays parameter-bound.
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "query", "cannot read work memberships", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var wid string
		var p ProjectMembership
		if err := rows.Scan(&wid, &p.ID, &p.DisplayName, &p.Role); err != nil {
			return nil, err
		}
		if i, ok := byID[wid]; ok {
			items[i].Projects = append(items[i].Projects, p)
		}
	}
	return items, rows.Err()
}

func (s *Store) QueryQ4(ctx context.Context, req Q4Request) (Q4Result, error) {
	var out Q4Result
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	if req.Cursor != "" {
		offset, parseErr := strconv.Atoi(req.Cursor)
		if parseErr != nil || offset < 0 {
			return out, newFailure(KindInvalidCursor, "PM1.Q4", "blocked cursor is invalid", false, "restart the blocked query")
		}
		req.Offset = offset
	}
	depth, nodeLimit, edgeLimit, err := q4GraphBounds(req)
	if err != nil {
		return out, err
	}
	tx, err := beginRead(ctx, s, "PM1.Q4")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readProduct(ctx, tx, req.Product); err != nil {
		return out, err
	}
	// Stage one bounds blocked work identities before any blocker join. This
	// prevents one work's many blockers from consuming the work-item limit.
	scopeFilter := ""
	queryArgs := []any{req.Product}
	if req.Project != "" {
		scopeFilter += " AND pp.project_id=?"
		queryArgs = append(queryArgs, req.Project)
	}
	workFilter := ""
	if req.Work != "" {
		workFilter += " AND w.id=?"
		queryArgs = append(queryArgs, req.Work)
	}
	if req.Kind != "" {
		workFilter += " AND w.kind=?"
		queryArgs = append(queryArgs, req.Kind)
	}
	q := `WITH scoped AS (
		SELECT DISTINCT wp.work_id
		FROM work_projects wp
		JOIN product_projects pp ON pp.project_id=wp.project_id
		WHERE pp.product_id=?` + scopeFilter + `
	), ranked AS (
		SELECT w.id,w.kind,w.title,w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,coalesce(w.terminal_time,'') AS terminal_time,MIN(b.created_at) AS oldest_blocker
		FROM scoped s
		JOIN work_items w ON w.id=s.work_id
		JOIN relations r ON r.work_id_to=w.id AND r.kind='blocks'
		JOIN work_items b ON b.id=r.work_id_from AND b.lifecycle IN ('needed','in_progress')` + workFilter + `
		GROUP BY w.id,w.kind,w.title,w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,w.terminal_time
	)
	SELECT id,kind,title,lifecycle,priority,urgency,created_at,updated_at,terminal_time
	FROM ranked
	ORDER BY urgency ASC,priority,oldest_blocker,id
	LIMIT ? OFFSET ?`
	queryArgs = append(queryArgs, limit+1, req.Offset)
	rows, err := tx.QueryContext(ctx, q, queryArgs...)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q4", "cannot select blocked work", true, "retry once the database is readable", err)
	}
	var selected []WorkItem
	for rows.Next() {
		var item WorkItem
		if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.Lifecycle, &item.Priority, &item.Urgency, &item.CreatedAt, &item.UpdatedAt, &item.TerminalAt); err != nil {
			rows.Close()
			return out, err
		}
		item.Blocked = true
		item.Ready = false
		item.Active = !terminalState(item.Lifecycle)
		item.Terminal = terminalState(item.Lifecycle)
		selected = append(selected, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	if len(selected) > limit {
		selected = selected[:limit]
		next := strconv.Itoa(req.Offset + limit)
		out.NextCursor = &next
	}
	edgeCapped, nodeCapped := false, false
	if len(selected) == 0 {
		out.Items = []WorkItem{}
	} else {
		var items []WorkItem
		items, edgeCapped, nodeCapped, err = readQ4BlockerGraph(ctx, tx, selected, depth, nodeLimit, edgeLimit)
		if err != nil {
			return out, err
		}
		out.Items = items
	}
	if out.Items == nil {
		out.Items = []WorkItem{}
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q4", ResolvedScope{ProductID: req.Product}, []string{"urgency", "priority", "oldest_blocker", "id"})
	if err != nil {
		return out, err
	}
	if edgeCapped {
		out.Warnings = append(out.Warnings, "Q4 edge cap reached")
		out.Omissions = append(out.Omissions, "unresolved blocker edges omitted by edge_limit")
	}
	if nodeCapped {
		out.Warnings = append(out.Warnings, "Q4 node cap reached")
		out.Omissions = append(out.Omissions, "unresolved blocker nodes omitted by node_limit")
	}
	return out, err
}

type q4GraphNode struct {
	item     WorkItem
	children []*q4GraphNode
	seen     map[string]bool
}

func (n *q4GraphNode) workItem() WorkItem {
	item := n.item
	if len(n.children) > 0 {
		item.Blockers = make([]WorkItem, 0, len(n.children))
		for _, child := range n.children {
			item.Blockers = append(item.Blockers, child.workItem())
		}
	}
	return item
}

func readQ4BlockerGraph(ctx context.Context, tx *sql.Tx, selected []WorkItem, depth, nodeLimit, edgeLimit int) ([]WorkItem, bool, bool, error) {
	values := make([]string, len(selected))
	args := make([]any, 0, len(selected)*2+2)
	for i, item := range selected {
		values[i] = "(?,?)"
		args = append(args, item.ID, i)
	}
	args = append(args, depth, edgeLimit+1)
	//nolint:gosec // values contains only fixed (?,?) tuples and all IDs, positions, and limits stay parameter-bound.
	q := `WITH RECURSIVE selected(id,position) AS (VALUES ` + strings.Join(values, ",") + `), graph(root_id,parent_id,blocker_id,depth) AS (
		SELECT s.id,s.id,r.work_id_from,1
		FROM selected s
		JOIN relations r ON r.work_id_to=s.id AND r.kind='blocks'
		JOIN work_items b ON b.id=r.work_id_from AND b.lifecycle IN ('needed','in_progress')
		UNION
		SELECT g.root_id,g.blocker_id,r.work_id_from,g.depth+1
		FROM graph g
		JOIN relations r ON r.work_id_to=g.blocker_id AND r.kind='blocks'
		JOIN work_items b ON b.id=r.work_id_from AND b.lifecycle IN ('needed','in_progress')
		WHERE g.depth < ?
	)
	SELECT g.root_id,g.parent_id,g.blocker_id,g.depth,b.kind,b.title,b.lifecycle,b.priority,b.urgency,b.created_at,b.updated_at,coalesce(b.terminal_time,'')
	FROM graph g
	JOIN selected s ON s.id=g.root_id
	JOIN work_items b ON b.id=g.blocker_id
	ORDER BY s.position,g.depth,g.parent_id,b.created_at,b.id
	LIMIT ?`
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, false, wrapFailure(KindUnavailable, "PM1.Q4", "cannot read blockers", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	roots := make([]*q4GraphNode, len(selected))
	nodes := make(map[string]*q4GraphNode)
	for i, item := range selected {
		root := &q4GraphNode{item: item, seen: map[string]bool{}}
		roots[i] = root
		nodes[item.ID+"|"+item.ID] = root
	}
	edgeCount := 0
	nodeCount := 0
	edgeCapped := false
	nodeCapped := false
	for rows.Next() {
		var rootID, parentID, blockerID, kind, title, lifecycle, urgency, createdAt, updatedAt, terminalAt string
		var graphDepth, priority int
		if err := rows.Scan(&rootID, &parentID, &blockerID, &graphDepth, &kind, &title, &lifecycle, &priority, &urgency, &createdAt, &updatedAt, &terminalAt); err != nil {
			return nil, false, false, err
		}
		if edgeCount >= edgeLimit {
			edgeCapped = true
			continue
		}
		root, ok := nodes[rootID+"|"+rootID]
		if !ok {
			continue
		}
		parent, ok := nodes[rootID+"|"+parentID]
		if !ok {
			continue
		}
		if root.seen[blockerID] {
			continue
		}
		if nodeCount >= nodeLimit {
			nodeCapped = true
			continue
		}
		root.seen[blockerID] = true
		node := &q4GraphNode{item: WorkItem{ID: blockerID, Kind: kind, Title: title, Lifecycle: lifecycle, Priority: int64(priority), Urgency: urgency, CreatedAt: createdAt, UpdatedAt: updatedAt, TerminalAt: terminalAt, Active: !terminalState(lifecycle), Terminal: terminalState(lifecycle)}, seen: map[string]bool{}}
		parent.children = append(parent.children, node)
		nodes[rootID+"|"+blockerID] = node
		nodeCount++
		edgeCount++
		_ = graphDepth
	}
	if err := rows.Err(); err != nil {
		return nil, false, false, err
	}
	out := make([]WorkItem, len(roots))
	for i, root := range roots {
		out[i] = root.workItem()
	}
	return out, edgeCapped, nodeCapped, nil
}

func (s *Store) QueryQ5(ctx context.Context, req Q5Request) (Q5Result, error) {
	var out Q5Result
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	if req.Cursor != "" {
		offset, parseErr := strconv.Atoi(req.Cursor)
		if parseErr != nil || offset < 0 {
			return out, newFailure(KindInvalidCursor, "PM1.Q5", "ready cursor is invalid", false, "restart the ready query")
		}
		req.Offset = offset
	}
	tx, err := beginRead(ctx, s, "PM1.Q5")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readProduct(ctx, tx, req.Product); err != nil {
		return out, err
	}
	scopeFilter := ""
	queryArgs := []any{req.Product}
	if req.Project != "" {
		scopeFilter += " AND pp.project_id=?"
		queryArgs = append(queryArgs, req.Project)
	}
	workFilter := ""
	if req.Kind != "" {
		workFilter += " AND w.kind=?"
		queryArgs = append(queryArgs, req.Kind)
	}
	q := `WITH scoped AS (SELECT DISTINCT wp.work_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE pp.product_id=?` + scopeFilter + `) SELECT w.id,w.kind,w.title,w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,coalesce(w.terminal_time,'') FROM work_items w JOIN scoped s ON s.work_id=w.id WHERE w.lifecycle='needed'` + workFilter + ` AND NOT EXISTS (SELECT 1 FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to=w.id AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress')) ORDER BY w.urgency ASC,w.priority,w.created_at DESC,w.id LIMIT ? OFFSET ?`
	queryArgs = append(queryArgs, limit+1, req.Offset)
	out.Items, err = scanWorkItems(ctx, tx, q, queryArgs...)
	if err != nil {
		return out, err
	}
	out.Items, err = attachDerivedFlags(ctx, tx, out.Items)
	if err != nil {
		return out, err
	}
	if out.Items == nil {
		out.Items = []WorkItem{}
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		next := strconv.Itoa(req.Offset + limit)
		out.NextCursor = &next
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q5", ResolvedScope{ProductID: req.Product}, []string{"urgency", "priority", "created_at", "id"})
	return out, err
}

func readOneWork(ctx context.Context, tx *sql.Tx, id string) (WorkItem, error) {
	items, err := scanWorkItems(ctx, tx, `SELECT id,kind,title,lifecycle,priority,urgency,created_at,updated_at,coalesce(terminal_time,'') FROM work_items WHERE id=?`, id)
	if err != nil {
		return WorkItem{}, err
	}
	if len(items) == 0 {
		return WorkItem{}, unknownScope("query", "work item does not exist")
	}
	// The bounded narrative rides the single-record read only; list queries keep
	// their eight-column shape.
	if err := tx.QueryRowContext(ctx, `SELECT narrative FROM work_items WHERE id=?`, id).Scan(&items[0].Narrative); err != nil {
		return WorkItem{}, wrapFailure(KindUnavailable, "query", "cannot read work narrative", true, "retry once the database is readable", err)
	}
	return items[0], nil
}
func readWorkProjects(ctx context.Context, tx *sql.Tx, id string) ([]ProjectMembership, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.display_name,wp.role FROM work_projects wp JOIN projects p ON p.id=wp.project_id WHERE wp.work_id=? ORDER BY CASE wp.role WHEN 'primary' THEN 0 ELSE 1 END,p.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectMembership
	for rows.Next() {
		var p ProjectMembership
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.Role); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) QueryQ6(ctx context.Context, req Q6Request) (Q6Result, error) {
	var out Q6Result
	if req.Work == "" && req.Project == "" {
		return out, unknownScope("PM1.Q6", "Q6 requires a work or Project reference")
	}
	if req.Cursor != "" {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(req.Cursor)
		var cursor q6Cursor
		if decodeErr != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.Product != req.Product || cursor.Project != req.Project || cursor.Order != "urgency:asc,priority:asc,updated_at:desc,id:asc" || cursor.Offset < 0 {
			return out, newFailure(KindInvalidCursor, "PM1.Q6", "scope cursor is invalid", false, "restart the scope query")
		}
		req.Offset = cursor.Offset
	}
	tx, err := beginRead(ctx, s, "PM1.Q6")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if req.Work != "" {
		w, err := readOneWork(ctx, tx, req.Work)
		if err != nil {
			return out, err
		}
		if req.Product != "" {
			if _, err := readProduct(ctx, tx, req.Product); err != nil {
				return out, err
			}
			var inScope bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
				SELECT 1
				FROM work_projects wp
				JOIN product_projects pp ON pp.project_id = wp.project_id
				WHERE wp.work_id = ? AND pp.product_id = ?
			)`, req.Work, req.Product).Scan(&inScope); err != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q6", "cannot validate work Product scope", true, "retry once the database is readable", err)
			}
			if !inScope {
				return out, unknownScope("PM1.Q6", "work item is not in the explicit Product scope")
			}
		}
		if req.Project != "" {
			var inScope bool
			query := `SELECT EXISTS (SELECT 1 FROM work_projects WHERE work_id = ? AND project_id = ?)`
			args := []any{req.Work, req.Project}
			if req.Product != "" {
				query = `SELECT EXISTS (
					SELECT 1
					FROM work_projects wp
					JOIN product_projects pp ON pp.project_id = wp.project_id
					WHERE wp.work_id = ? AND wp.project_id = ? AND pp.product_id = ?
				)`
				args = append(args, req.Product)
			}
			if err := tx.QueryRowContext(ctx, query, args...).Scan(&inScope); err != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q6", "cannot validate work Project scope", true, "retry once the database is readable", err)
			}
			if !inScope {
				return out, unknownScope("PM1.Q6", "work item is not in the explicit Project scope")
			}
		}
		w.Projects, err = readWorkProjects(ctx, tx, req.Work)
		if err != nil {
			return out, err
		}
		out.Work = &w
		out.Result = &Q6WorkResultPayload{Work: out.Work}
		out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q6", ResolvedScope{WorkID: req.Work}, []string{"work_id", "project_role", "project_id"})
		return out, err
	}
	if req.Product != "" {
		if _, err := readProduct(ctx, tx, req.Product); err != nil {
			return out, err
		}
		var owns int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?`, req.Product, req.Project).Scan(&owns); err == sql.ErrNoRows {
			return out, unknownScope("PM1.Q6", "Project is not owned by Product")
		} else if err != nil {
			return out, err
		}
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id=?`, req.Project).Scan(&exists); err == sql.ErrNoRows {
		return out, unknownScope("PM1.Q6", "Project does not exist")
	}
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	q := `WITH scoped AS (SELECT DISTINCT wp.work_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.project_id=?` + func() string {
		if req.Product != "" {
			return " AND pp.product_id=?"
		}
		return ""
	}() + `) SELECT w.id,w.kind,w.title,w.lifecycle,w.priority,w.urgency,w.created_at,w.updated_at,coalesce(w.terminal_time,'') FROM work_items w JOIN scoped s ON s.work_id=w.id ORDER BY w.urgency ASC,w.priority,w.updated_at DESC,w.id LIMIT ? OFFSET ?`
	args := []any{req.Project}
	if req.Product != "" {
		args = append(args, req.Product)
	}
	args = append(args, limit+1, req.Offset)
	out.Items, err = scanWorkItems(ctx, tx, q, args...)
	if err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		encoded, encodeErr := json.Marshal(q6Cursor{Version: 1, Product: req.Product, Project: req.Project, Order: "urgency:asc,priority:asc,updated_at:desc,id:asc", Offset: req.Offset + limit})
		if encodeErr != nil {
			return out, wrapFailure(KindInvariantViolation, "PM1.Q6", "cannot encode scope cursor", false, "restart the scope query", encodeErr)
		}
		next := base64.RawURLEncoding.EncodeToString(encoded)
		out.NextCursor = &next
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q6", ResolvedScope{ProductID: req.Product, ProjectID: req.Project}, []string{"urgency", "priority", "updated_at", "id"})
	return out, err
}

func (s *Store) QueryQ7(ctx context.Context, req Q7Request) (Q7Result, error) {
	var out Q7Result
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	direction := req.Direction
	if direction == "" {
		direction = "newest_first"
	}
	if direction != "oldest_first" && direction != "newest_first" {
		return out, newFailure(KindInvalidFilter, "PM1.Q7", "direction must be oldest_first or newest_first", false, "choose a closed event direction")
	}
	tx, err := beginRead(ctx, s, "PM1.Q7")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readOneWork(ctx, tx, req.Work); err != nil {
		return out, err
	}
	where := ""
	args := []any{req.Work}
	if req.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(req.Cursor)
		var c struct {
			Version                  int `json:"version"`
			QueryID, Work, Direction string
			Seq                      int64 `json:"seq"`
		}
		if e != nil || json.Unmarshal(b, &c) != nil || c.Version != 1 || c.QueryID != "PM1.Q7" || c.Work != req.Work || c.Direction != direction || c.Seq < 1 {
			return out, newFailure(KindInvalidCursor, "PM1.Q7", "cursor does not match the event listing", false, "use a cursor returned for the same work and direction")
		}
		if direction == "oldest_first" {
			where = " AND seq > ?"
		} else {
			where = " AND seq < ?"
		}
		args = append(args, c.Seq)
	}
	order := "DESC"
	if direction == "oldest_first" {
		order = "ASC"
	}
	eventKinds := req.EventKinds
	if len(eventKinds) == 0 {
		eventKinds = []string{"work.created", "work.transitioned", "work.reopened", "work.superseded", "work.reopened_from_superseded"}
	}
	for _, kind := range eventKinds {
		if _, ok := eventKindRegistry[kind]; !ok {
			return out, newFailure(KindInvalidFilter, "PM1.Q7", "event kind is not recognized", false, "choose an accepted work event kind")
		}
	}
	kindPlaceholders := make([]string, len(eventKinds))
	for i, kind := range eventKinds {
		kindPlaceholders[i] = "?"
		args = append(args, kind)
	}
	q := `SELECT event_id,seq,kind,actor,occurred_at,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind IN (` + strings.Join(kindPlaceholders, ",") + `)` + where + ` ORDER BY seq ` + order + ` LIMIT ?` //nolint:gosec // placeholders are fixed, where and order are closed branches, and all request values stay parameter-bound.
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	var raw []TimelineEvent
	for rows.Next() {
		var eventID string
		var global int64
		var kind, actor, occurred, payload string
		if err := rows.Scan(&eventID, &global, &kind, &actor, &occurred, &payload); err != nil {
			return out, err
		}
		e, err := decodeTimelineEvent(eventID, global, kind, actor, occurred, []byte(payload))
		if err != nil {
			return out, err
		}
		raw = append(raw, e)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if out.Events == nil {
		out.Events = []TimelineEvent{}
	}
	if len(raw) > limit {
		last := raw[limit-1]
		raw = raw[:limit]
		c := struct {
			Version                  int `json:"version"`
			QueryID, Work, Direction string
			Seq                      int64 `json:"seq"`
		}{1, "PM1.Q7", req.Work, direction, last.GlobalSeq}
		b, _ := json.Marshal(c)
		encoded := base64.RawURLEncoding.EncodeToString(b)
		out.NextCursor = &encoded
	}
	for i := range raw {
		raw[i].Seq = i + 1
	}
	out.Events = raw
	out.Result = &Q7ResultPayload{Events: out.Events}
	if workflow, workflowErr := readWorkflowSummaryTx(ctx, tx, req.Work); workflowErr != nil {
		return out, workflowErr
	} else if workflow != nil {
		out.Workflow = workflow
		out.Result.Workflow = workflow
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q7", ResolvedScope{WorkID: req.Work}, []string{"seq"})
	return out, err
}
func decodeTimelineEvent(eventID string, global int64, kind, actor, occurred string, payload []byte) (TimelineEvent, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return TimelineEvent{}, newFailure(KindInvariantViolation, "PM1.Q7", "event payload is malformed", false, "repair the event log before reading history")
	}
	e := TimelineEvent{EventID: eventID, GlobalSeq: global, Kind: kind, Actor: actor, OccurredAt: occurred, EvidenceRefs: []string{}}
	for name, dst := range map[string]**string{"from": &e.From, "to": &e.To} {
		if raw, ok := fields[name]; ok && string(raw) != "null" {
			var v string
			if json.Unmarshal(raw, &v) != nil {
				return TimelineEvent{}, newFailure(KindInvariantViolation, "PM1.Q7", "event lifecycle field is malformed", false, "repair the event log before reading history")
			}
			*dst = &v
		}
	}
	if raw, ok := fields["reason"]; ok {
		if err := json.Unmarshal(raw, &e.Reason); err != nil {
			return TimelineEvent{}, newFailure(KindInvariantViolation, "PM1.Q7", "event reason is malformed", false, "repair the event log before reading history")
		}
	}
	if raw, ok := fields["evidence_refs"]; ok {
		if err := json.Unmarshal(raw, &e.EvidenceRefs); err != nil {
			return TimelineEvent{}, newFailure(KindInvariantViolation, "PM1.Q7", "event evidence references are malformed", false, "repair the event log before reading history")
		}
	}
	return e, nil
}

type relationSpec struct {
	label, stored string
	invert        bool
}

func relationSpecs(kinds []string) ([]relationSpec, error) {
	if len(kinds) == 0 {
		kinds = relationDefaultQueryLabels
	}
	out := make([]relationSpec, 0, len(kinds))
	for _, k := range kinds {
		s, ok := relationQueryLabels[k]
		if !ok {
			return nil, newFailure(KindInvalidFilter, "PM1.Q8", "unknown relation kind "+k, false, "use an accepted PM4 relation or inverse label")
		}
		out = append(out, s)
	}
	return out, nil
}

func readRelationDepth(ctx context.Context, tx *sql.Tx, work string, specs []relationSpec, direction string, depth int) ([]RelationEdge, error) {
	var out []RelationEdge
	for _, spec := range specs {
		anchor := "r.work_id_from=?"
		recurse := "r.work_id_from=g.target"
		if direction == "incoming" {
			anchor = "r.work_id_to=?"
			recurse = "r.work_id_to=g.source"
		}
		if spec.invert {
			if direction == "outgoing" {
				anchor = "r.work_id_to=?"
				recurse = "r.work_id_to=g.target"
			} else {
				anchor = "r.work_id_from=?"
				recurse = "r.work_id_from=g.source"
			}
		}
		q := `WITH RECURSIVE graph(depth,source,target) AS (
SELECT 1, CASE WHEN ?=1 THEN r.work_id_to ELSE r.work_id_from END, CASE WHEN ?=1 THEN r.work_id_from ELSE r.work_id_to END FROM relations r WHERE r.kind=? AND ` + anchor + `
UNION
SELECT g.depth+1, CASE WHEN ?=1 THEN r.work_id_to ELSE r.work_id_from END, CASE WHEN ?=1 THEN r.work_id_from ELSE r.work_id_to END FROM graph g JOIN relations r ON r.kind=? AND ` + recurse + ` WHERE g.depth < ?
) SELECT ? AS kind,source,target,depth FROM graph ORDER BY depth,source,target`
		args := []any{spec.invert, spec.invert, spec.stored, work, spec.invert, spec.invert, spec.stored, depth, spec.label}
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "PM1.Q8", "cannot read relation graph", true, "retry once the database is readable", err)
		}
		for rows.Next() {
			var edge RelationEdge
			if err := rows.Scan(&edge.Kind, &edge.Source, &edge.Target, &edge.Depth); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, edge)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}
func (s *Store) QueryQ8(ctx context.Context, req Q8Request) (Q8Result, error) {
	var out Q8Result
	if req.Work == "" {
		return out, unknownScope("PM1.Q8", "Q8 requires a work reference")
	}
	direction := req.Direction
	if direction == "" {
		direction = "outgoing"
	}
	if direction != "incoming" && direction != "outgoing" && direction != "both" {
		return out, newFailure(KindInvalidFilter, "PM1.Q8", "direction must be incoming, outgoing, or both", false, "choose a closed relation direction")
	}
	depth := req.Depth
	if depth == 0 {
		depth = 1
	}
	if depth < 1 || depth > 3 {
		return out, newFailure(KindInvalidFilter, "PM1.Q8", "depth must be between 1 and 3", false, "supply a bounded relation depth")
	}
	specs, err := relationSpecs(req.RelationKinds)
	if err != nil {
		return out, err
	}
	if depth > 1 {
		for _, spec := range specs {
			if !relationTransitiveKinds[spec.stored] {
				return out, newFailure(KindInvalidFilter, "PM1.Q8", "relation kind "+spec.stored+" is not transitive", false, "query a transitive relation kind or use depth 1")
			}
		}
	}
	tx, err := beginRead(ctx, s, "PM1.Q8")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readOneWork(ctx, tx, req.Work); err != nil {
		return out, err
	}
	if depth > 1 || direction == "both" {
		allEdges := []RelationEdge{}
		for _, dir := range []string{direction} {
			if dir == "both" {
				for _, one := range []string{"outgoing", "incoming"} {
					edges, e := readRelationDepth(ctx, tx, req.Work, specs, one, depth)
					if e != nil {
						return out, e
					}
					allEdges = append(allEdges, edges...)
				}
			} else {
				edges, e := readRelationDepth(ctx, tx, req.Work, specs, dir, depth)
				if e != nil {
					return out, e
				}
				allEdges = append(allEdges, edges...)
			}
		}
		sort.Slice(allEdges, func(i, j int) bool {
			if allEdges[i].Kind != allEdges[j].Kind {
				return allEdges[i].Kind < allEdges[j].Kind
			}
			if allEdges[i].Source != allEdges[j].Source {
				return allEdges[i].Source < allEdges[j].Source
			}
			if allEdges[i].Target != allEdges[j].Target {
				return allEdges[i].Target < allEdges[j].Target
			}
			return allEdges[i].Depth < allEdges[j].Depth
		})
		seen := map[string]bool{}
		out.Edges = out.Edges[:0]
		for _, edge := range allEdges {
			key := edge.Kind + "|" + edge.Source + "|" + edge.Target
			if !seen[key] {
				seen[key] = true
				// Sorting places the shallowest path first, so first-wins keeps its depth.
				out.Edges = append(out.Edges, edge)
			}
		}
		out.Result = &Q8ResultPayload{Edges: out.Edges}
		out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q8", ResolvedScope{WorkID: req.Work}, []string{"kind", "source", "target", "depth"})
		return out, err
	}
	parts := make([]string, 0, len(specs))
	args := []any{}
	for _, spec := range specs {
		parts = append(parts, `SELECT ? AS kind, CASE WHEN ?=1 THEN r.work_id_to ELSE r.work_id_from END AS source, CASE WHEN ?=1 THEN r.work_id_from ELSE r.work_id_to END AS target, 1 AS depth FROM relations r WHERE r.kind=? AND `+func() string {
			if spec.invert && direction == "outgoing" || !spec.invert && direction == "incoming" {
				return "r.work_id_to=?"
			}
			return "r.work_id_from=?"
		}())
		args = append(args, spec.label, spec.invert, spec.invert, spec.stored, req.Work)
	}
	q := strings.Join(parts, " UNION ALL ") + ` ORDER BY kind,source,target` //nolint:gosec // parts use one fixed SQL template and closed direction branches while every relation value stays parameter-bound.
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q8", "cannot read relations", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e RelationEdge
		if err := rows.Scan(&e.Kind, &e.Source, &e.Target, &e.Depth); err != nil {
			return out, err
		}
		out.Edges = append(out.Edges, e)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if out.Edges == nil {
		out.Edges = []RelationEdge{}
	}
	out.Result = &Q8ResultPayload{Edges: out.Edges}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q8", ResolvedScope{WorkID: req.Work}, []string{"kind", "source", "target", "depth"})
	return out, err
}
