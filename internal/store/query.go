package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"slices"
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
	AmbientProject  string
	ProjectIDs      []string
	WorkIDs         []string
	LifecycleStates []string
	Kind            string
	TerminalSince   *string
	Detail          string
	Limit           int
	Cursor          string
}

type Q7Request struct {
	Work        string
	Direction   string
	RecordKinds []string
	Limit       int
	Cursor      string
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

// WorkItem is one projected work item. Task, the captured short note, rides
// only the authoritative intent reads — the full-detail list read — so
// bounded list and preview reads keep their column-only shape.
type WorkItem struct {
	ID                    string              `json:"id"`
	Kind                  string              `json:"kind"`
	Title                 string              `json:"title"`
	Lifecycle             string              `json:"lifecycle"`
	Version               int64               `json:"version"`
	CreatedAt             string              `json:"created_at"`
	UpdatedAt             string              `json:"updated_at"`
	TerminalAt            string              `json:"terminal_at,omitempty"`
	Task                  string              `json:"task,omitempty"`
	Projects              []ProjectMembership `json:"projects,omitempty"`
	Blocked               bool                `json:"blocked"`
	Ready                 bool                `json:"ready"`
	Active                bool                `json:"active"`
	Terminal              bool                `json:"terminal"`
	Liveness              *WorkLiveness       `json:"liveness,omitempty"`
	Blockers              []WorkItem          `json:"blockers,omitempty"`
	PendingProjectHandoff *ProjectHandoff     `json:"pending_project_handoff,omitempty"`
	LawContext            *WorkflowLawContext `json:"law_context,omitempty"`
	NativeRuns            []NativeRunReport   `json:"native_runs,omitempty"`
}

type Q7Result struct {
	ResultMeta
	Entries []HistoryEntry   `json:"entries"`
	Result  *Q7ResultPayload `json:"result"`
}

type Q7ResultPayload struct {
	Entries []HistoryEntry `json:"entries"`
}

type RelationEdge struct {
	Kind       string `json:"kind"`
	Source     string `json:"source"`
	Target     string `json:"target"`
	Depth      int    `json:"depth"`
	RelationID string `json:"relation_id"`
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

func beginRead(ctx context.Context, s *Store, op string) (*sql.Tx, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, op, "store is not open", true, "open a live store")
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, op, "cannot begin live read snapshot", true, "retry once the database is readable", err)
	}
	return tx, nil
}

func queryMeta(ctx context.Context, tx *sql.Tx, id string, scope ResolvedScope, keys []string) (ResultMeta, error) {
	watermark, err := storeRevision(ctx, tx)
	if err != nil {
		return ResultMeta{}, wrapFailure(KindUnavailable, id, "cannot read the live source watermark", true, "retry once the database is readable", err)
	}
	now := time.Now().UTC()
	return ResultMeta{
		QueryID: id, ContractVersion: queryContractVersion, ResolvedScope: scope,
		SourceVersionWatermark: watermark, Authority: "authoritative",
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
		productID, candidates, err := resolveQ1ProjectProduct(ctx, tx, req.Product, req.Project)
		if err != nil {
			out.CandidateIDs = candidates
			return out, err
		}
		p, err := readProduct(ctx, tx, productID)
		if err != nil {
			return out, err
		}
		projects, err := readProjectMemberships(ctx, tx, productID)
		if err != nil {
			return out, err
		}
		out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q1", ResolvedScope{ProductID: productID, ProjectID: req.Project}, []string{"project_role", "project_display_name", "project_id"})
		if err != nil {
			return out, err
		}
		out.Product = &p
		out.Projects = projects
		out.Result = &Q1ResultPayload{Product: out.Product, Projects: out.Projects}
		return out, nil
	}
	if err := readQ1ProductPage(ctx, tx, req.Cursor, limit, &out); err != nil {
		return out, err
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q1", ResolvedScope{}, []string{"display_name", "id"})
	if err != nil {
		return out, err
	}
	out.Result = &Q1ResultPayload{Products: out.Products}
	return out, nil
}

// resolveQ1ProjectProduct resolves the one Product a Project query names: an
// existing Project with Product memberships, narrowed by an explicit Product
// scope, refusing ambiguity with the owning Product ids as candidates.
func resolveQ1ProjectProduct(ctx context.Context, tx *sql.Tx, explicitProduct, project string) (string, []string, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ?`, project).Scan(&exists); err == sql.ErrNoRows {
		return "", nil, unknownScope("PM1.Q1", "Project does not exist")
	} else if err != nil {
		return "", nil, wrapFailure(KindUnavailable, "PM1.Q1", "cannot resolve Project", true, "retry once the database is readable", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT products.id FROM products JOIN product_projects ON product_projects.product_id = products.id WHERE product_projects.project_id = ? ORDER BY products.id`, project)
	if err != nil {
		return "", nil, wrapFailure(KindUnavailable, "PM1.Q1", "cannot resolve Project ownership", true, "retry once the database is readable", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	if len(ids) == 0 {
		return "", nil, unknownScope("PM1.Q1", "Project has no Product membership")
	}
	if explicitProduct != "" {
		found := false
		for _, id := range ids {
			if id == explicitProduct {
				found = true
				break
			}
		}
		if !found {
			return "", nil, unknownScope("PM1.Q1", "explicit Product does not own Project")
		}
		ids = []string{explicitProduct}
	}
	if len(ids) > 1 {
		return "", ids, newAmbiguousScopeFailure("PM1.Q1", "Project belongs to multiple Products", "supply an explicit Product scope", ids)
	}
	return ids[0], nil, nil
}

// readQ1ProductPage reads one bounded, cursor-paged page of every Product.
func readQ1ProductPage(ctx context.Context, tx *sql.Tx, cursorToken string, limit int, out *Q1Result) error {
	where := ""
	args := []any{}
	if cursorToken != "" {
		var cursor struct {
			Version           int `json:"version"`
			QueryID, Name, ID string
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(cursorToken)
		if decodeErr != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.QueryID != "PM1.Q1" || cursor.Name == "" || cursor.ID == "" {
			return newFailure(KindInvalidCursor, "PM1.Q1", "cursor is not valid for the Product listing", false, "restart the bounded Product listing")
		}
		where = " WHERE display_name > ? OR (display_name = ? AND id > ?)"
		args = []any{cursor.Name, cursor.Name, cursor.ID}
	}
	query := `SELECT id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at FROM products` + where + ` ORDER BY display_name, id LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "PM1.Q1", "cannot list Products", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.StageMaturity, &p.StageAudienceCommitment, &p.Version, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return err
		}
		out.Products = append(out.Products, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(out.Products) > limit {
		last := out.Products[limit-1]
		cursor := struct {
			Version           int `json:"version"`
			QueryID, Name, ID string
		}{1, "PM1.Q1", last.DisplayName, last.ID}
		encoded, encodeErr := json.Marshal(cursor)
		if encodeErr != nil {
			return encodeErr
		}
		value := base64.RawURLEncoding.EncodeToString(encoded)
		out.NextCursor = &value
		out.Products = out.Products[:limit]
	}
	if out.Products == nil {
		out.Products = []Product{}
	}
	return nil
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
		0,
		COALESCE(SUM(CASE WHEN EXISTS (SELECT 1 FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to=w.id AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress')) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle='needed' AND NOT EXISTS (SELECT 1 FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to=w.id AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress')) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle='in_progress' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN w.lifecycle IN ('completed','cancelled') THEN 1 ELSE 0 END),0)
		FROM work_items w JOIN scoped s ON s.work_id=w.id`
	if err := tx.QueryRowContext(ctx, countQuery, scopeArgs(req.Product, projectArgsToIDs(projectArgs))...).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5], &counts[6], &counts[7], &counts[8]); err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q2", "cannot read Product snapshot", true, "retry once the database is readable", err)
	}
	previewQuery := scope + ` SELECT w.id,w.kind,w.title,w.lifecycle,w.version,w.created_at,w.updated_at,coalesce(w.terminal_time,'') FROM work_items w JOIN scoped s ON s.work_id=w.id ORDER BY CASE WHEN w.lifecycle IN ('completed','cancelled') THEN w.terminal_time ELSE w.created_at END DESC, w.id LIMIT ?`
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
	out.LifecycleCounts = map[string]int{"needed": counts[0], "in_progress": counts[1], "completed": counts[2], "cancelled": counts[3]}
	out.DerivedCounts = DerivedCounts{Blocked: counts[5], Ready: counts[6], Active: counts[7], Terminal: counts[8]}
	out.Items = items
	if out.Items == nil {
		out.Items = []WorkItem{}
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q2", ResolvedScope{ProductID: req.Product}, []string{"relevant_time", "id"})
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
			return nil, newFailure(KindInvalidFilter, "PM1.Q3", "unknown lifecycle state "+state, false, "use one of needed, in_progress, completed, cancelled")
		}
	}
	sort.Strings(out)
	return out, nil
}

type q3Cursor struct {
	Version                          int `json:"version"`
	QueryID, Product, Project, Order string
	States                           []string `json:"states"`
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
	if json.Unmarshal(b, &c) != nil || c.Version != 1 || c.QueryID != "PM1.Q3" || c.Product != req.Product || c.Project != req.Project || c.Order != order || !slices.Equal(c.States, states) || c.ID == "" || c.Timestamp == "" {
		return q3Cursor{}, newFailure(KindInvalidCursor, "PM1.Q3", "cursor does not match the requested listing", false, "use a cursor returned for the same query and filters")
	}
	if _, err := time.Parse(time.RFC3339Nano, c.Timestamp); err != nil {
		return q3Cursor{}, newFailure(KindInvalidCursor, "PM1.Q3", "cursor has an invalid ordering timestamp", false, "restart the bounded listing")
	}
	return c, nil
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
	order, orderSQL := q3Order(states)
	projectIDs := req.ProjectIDs
	if len(projectIDs) == 0 && req.Project != "" {
		projectIDs = []string{req.Project}
	}
	filter, projectArgs := productWorkScopeSQL(projectIDs)
	args := scopeArgs(req.Product, projectArgsToIDs(projectArgs))
	placeholders, stateArgs := q3FilterSQL(req, states)
	cursorSQL := ""
	cursorArgs := []any{}
	if req.Cursor != "" {
		c, err := decodeQ3Cursor(req.Cursor, req, states, order)
		if err != nil {
			return out, err
		}
		cursorSQL = ` AND (relevant_time < ? OR (relevant_time = ? AND id > ?))`
		cursorArgs = []any{c.Timestamp, c.Timestamp, c.ID}
	}
	scope := `WITH scoped AS (SELECT DISTINCT wp.work_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE pp.product_id=?` + filter + `), base AS (SELECT w.id,w.kind,w.title,w.lifecycle,w.version,w.created_at,w.updated_at,coalesce(w.terminal_time,'') AS terminal_time, CASE WHEN w.lifecycle IN ('completed','cancelled') THEN w.terminal_time ELSE w.created_at END AS relevant_time FROM work_items w JOIN scoped s ON s.work_id=w.id)`
	query := scope + ` SELECT id,kind,title,lifecycle,version,created_at,updated_at,terminal_time FROM base WHERE 1=1` + placeholders + cursorSQL + ` ORDER BY ` + orderSQL + ` LIMIT ?`
	args = append(args, stateArgs...)
	args = append(args, cursorArgs...)
	args = append(args, limit+1)
	proofs, err := prepareItemScopedLawProofs(ctx, s, req)
	if err != nil {
		return out, err
	}
	tx, err := beginRead(ctx, s, "PM1.Q3")
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := readProduct(ctx, tx, req.Product); err != nil {
		return out, err
	}
	items, err := scanWorkItems(ctx, tx, query, args...)
	if err != nil {
		return out, err
	}
	items, err = attachDerivedFlags(ctx, tx, items)
	if err != nil {
		return out, err
	}
	if req.Detail == "full" {
		if err := attachQ3FullDetail(ctx, tx, items); err != nil {
			return out, err
		}
		if err := attachItemScopedResumeFacts(tx, items, proofs, req.AmbientProject); err != nil {
			return out, err
		}
	}
	out.Items = items
	if out.Items == nil {
		out.Items = []WorkItem{}
	}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q3", ResolvedScope{ProductID: req.Product, ProjectID: req.Project}, []string{"relevant_time", "id"})
	if err != nil {
		return out, err
	}
	return out.LimitPage(req, limit)
}

// prepareItemScopedLawProofs prepares the per-item verified law-folder
// contexts an item-scoped full-detail read needs, before its transaction
// opens (CD-0195 D2). Broad scans prepare nothing: law proof is per-item
// work and must not multiply over inventory reads.
func prepareItemScopedLawProofs(ctx context.Context, s *Store, req Q3Request) (map[string]context.Context, error) {
	proofs := map[string]context.Context{}
	if req.Detail != "full" || len(req.WorkIDs) == 0 {
		return proofs, nil
	}
	for _, id := range req.WorkIDs {
		if _, ok := proofs[id]; ok {
			continue
		}
		proof, err := s.PrepareWorkLawProof(ctx, id)
		if err != nil {
			return nil, err
		}
		proofs[id] = proof
	}
	return proofs, nil
}

// attachItemScopedResumeFacts attaches the three per-item facts the one
// resume view re-homes onto full detail: the pending project handoff, the
// resolved law context, and the attributed native runs. It runs inside the
// caller's transaction with the caller's prepared law proofs; items without
// a prepared proof keep the facts absent.
func attachItemScopedResumeFacts(tx *sql.Tx, items []WorkItem, proofs map[string]context.Context, ambientProject string) error {
	for i := range items {
		item := &items[i]
		itemCtx, ok := proofs[item.ID]
		if !ok {
			continue
		}
		version, err := activeWorkflowContractVersion(itemCtx, tx, item.ID, "PM1.Q3")
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		item.PendingProjectHandoff, err = selectProjectHandoffTx(itemCtx, tx, item.ID, ambientProject, version)
		if err != nil {
			return err
		}
		contract, err := readWorkflowContractVersion(itemCtx, tx, item.ID, version)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			item.LawContext, err = readWorkflowLawContext(itemCtx, tx, item.ID, &contract)
			if err != nil {
				return err
			}
		}
		item.NativeRuns, err = readWorkflowNativeRunsTx(itemCtx, tx, item.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func q3FilterSQL(req Q3Request, states []string) (string, []any) {
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
	if req.TerminalSince != nil {
		placeholders += " AND terminal_time >= ?"
		stateArgs = append(stateArgs, *req.TerminalSince)
	}
	return placeholders, stateArgs
}

func attachQ3FullDetail(ctx context.Context, tx *sql.Tx, items []WorkItem) error {
	return attachRecordedIntent(ctx, tx, items)
}

// attachRecordedIntent copies the recorded mutable-intent detail onto each
// item: task, value statement, tags, and workflow type reference from the
// stored intent projection. Only the authoritative intent reads call it, so
// bounded summary and preview reads never carry intent detail. A tag array
// the intent recorded as empty stays the empty array; a null or absent array
// stays nil.
func attachRecordedIntent(ctx context.Context, tx *sql.Tx, items []WorkItem) error {
	if len(items) == 0 {
		return nil
	}
	placeholders := make([]string, len(items))
	args := make([]any, len(items))
	byID := make(map[string]int, len(items))
	for i := range items {
		placeholders[i] = "?"
		args[i] = items[i].ID
		byID[items[i].ID] = i
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, intent_json FROM work_items WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every work ID stays parameter-bound.
	if err != nil {
		return wrapFailure(KindUnavailable, "query", "cannot read the recorded work intent", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, intentJSON string
		if err := rows.Scan(&id, &intentJSON); err != nil {
			return wrapFailure(KindInvariantViolation, "query", "cannot decode the recorded work intent", false, "restore the current rows from a verified snapshot", err)
		}
		if i, ok := byID[id]; ok {
			if err := decodeRecordedIntent(&items[i], intentJSON); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return wrapFailure(KindUnavailable, "query", "cannot read the recorded work intent", true, "retry once the database is readable", err)
	}
	return nil
}

// decodeRecordedIntent projects the captured note onto one item. It reads
// the same shape the capture and revise folds store.
func decodeRecordedIntent(item *WorkItem, intentJSON string) error {
	var intent workIntentProjection
	if err := json.Unmarshal([]byte(intentJSON), &intent); err != nil {
		return wrapFailure(KindInvariantViolation, "query", "stored work intent is not readable", false, "restore the current rows from a verified snapshot", err)
	}
	item.Task = intent.Task
	return nil
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

func scanWorkItems(ctx context.Context, tx queryer, query string, args ...any) ([]WorkItem, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "query", "cannot read work items", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []WorkItem
	for rows.Next() {
		var w WorkItem
		if err := rows.Scan(&w.ID, &w.Kind, &w.Title, &w.Lifecycle, &w.Version, &w.CreatedAt, &w.UpdatedAt, &w.TerminalAt); err != nil {
			return nil, wrapFailure(KindInvariantViolation, "query", "cannot decode work item projection", false, "restore the current rows from a verified snapshot", err)
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

func terminalState(lifecycle string) bool { return isTerminalLifecycle(lifecycle) }

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
	rows, err := tx.QueryContext(ctx, `SELECT w.id, EXISTS (SELECT 1 FROM relations r JOIN work_items b ON b.id=r.work_id_from WHERE r.work_id_to=w.id AND r.kind='blocks' AND b.lifecycle IN ('needed','in_progress')) FROM work_items w WHERE w.id IN (`+strings.Join(ph, ",")+`)`, args...) //nolint:gosec // placeholders and every work ID stay parameter-bound.
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
			items[i].Active = items[i].Lifecycle == "in_progress"
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		liveness, err := deriveWorkLivenessQ(ctx, tx, items[i].ID)
		if err != nil {
			return nil, err
		}
		items[i].Liveness = &liveness
	}
	return items, nil
}

func attachWorkMemberships(ctx context.Context, tx queryer, items []WorkItem, ids []string) ([]WorkItem, error) {
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

// requireWork refuses a work ID that was published away or never existed.
func requireWork(ctx context.Context, tx *sql.Tx, id string) error {
	if err := publishedWorkAbsenceQ(ctx, tx, id, "query"); err != nil {
		return err
	}
	exists, err := workExistsCore(ctx, tx, id)
	if err != nil {
		return wrapFailure(KindUnavailable, "query", "cannot read work items", true, "retry once the database is readable", err)
	}
	if !exists {
		return unknownScope("query", "work item does not exist")
	}
	return nil
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
		q := `WITH RECURSIVE graph(depth,relation_id,source,target) AS (
SELECT 1, r.id, CASE WHEN ?=1 THEN r.work_id_to ELSE r.work_id_from END, CASE WHEN ?=1 THEN r.work_id_from ELSE r.work_id_to END FROM relations r WHERE r.kind=? AND ` + anchor + `
UNION
SELECT g.depth+1, r.id, CASE WHEN ?=1 THEN r.work_id_to ELSE r.work_id_from END, CASE WHEN ?=1 THEN r.work_id_from ELSE r.work_id_to END FROM graph g JOIN relations r ON r.kind=? AND ` + recurse + ` WHERE g.depth < ?
) SELECT ? AS kind,source,target,depth,relation_id FROM graph ORDER BY depth,source,target`
		args := []any{spec.invert, spec.invert, spec.stored, work, spec.invert, spec.invert, spec.stored, depth, spec.label}
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "PM1.Q8", "cannot read relation graph", true, "retry once the database is readable", err)
		}
		for rows.Next() {
			var edge RelationEdge
			var relationID int64
			if err := rows.Scan(&edge.Kind, &edge.Source, &edge.Target, &edge.Depth, &relationID); err != nil {
				rows.Close()
				return nil, err
			}
			edge.RelationID = strconv.FormatInt(relationID, 10)
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
	if err := requireWork(ctx, tx, req.Work); err != nil {
		return out, err
	}
	if depth > 1 || direction == "both" {
		if err := readQ8DepthEdges(ctx, tx, req.Work, direction, depth, specs, &out); err != nil {
			return out, err
		}
		out.Result = &Q8ResultPayload{Edges: out.Edges}
		out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q8", ResolvedScope{WorkID: req.Work}, []string{"kind", "source", "target", "depth"})
		return out, err
	}
	if err := readQ8DirectEdges(ctx, tx, req.Work, direction, specs, &out); err != nil {
		return out, err
	}
	out.Result = &Q8ResultPayload{Edges: out.Edges}
	out.ResultMeta, err = queryMeta(ctx, tx, "PM1.Q8", ResolvedScope{WorkID: req.Work}, []string{"kind", "source", "target", "depth"})
	return out, err
}

// readQ8DepthEdges reads a depth-or-both query through the transitive walk,
// sorted with the shallowest path per edge winning.
func readQ8DepthEdges(ctx context.Context, tx *sql.Tx, work, direction string, depth int, specs []relationSpec, out *Q8Result) error {
	allEdges := []RelationEdge{}
	for _, dir := range []string{direction} {
		if dir == "both" {
			for _, one := range []string{"outgoing", "incoming"} {
				edges, e := readRelationDepth(ctx, tx, work, specs, one, depth)
				if e != nil {
					return e
				}
				allEdges = append(allEdges, edges...)
			}
		} else {
			edges, e := readRelationDepth(ctx, tx, work, specs, dir, depth)
			if e != nil {
				return e
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
	return nil
}

// readQ8DirectEdges reads a depth-1, one-direction query through one UNION
// ALL statement over the requested relation kinds.
func readQ8DirectEdges(ctx context.Context, tx *sql.Tx, work, direction string, specs []relationSpec, out *Q8Result) error {
	parts := make([]string, 0, len(specs))
	args := []any{}
	for _, spec := range specs {
		parts = append(parts, `SELECT ? AS kind, CASE WHEN ?=1 THEN r.work_id_to ELSE r.work_id_from END AS source, CASE WHEN ?=1 THEN r.work_id_from ELSE r.work_id_to END AS target, 1 AS depth, r.id AS relation_id FROM relations r WHERE r.kind=? AND `+func() string {
			if spec.invert && direction == "outgoing" || !spec.invert && direction == "incoming" {
				return "r.work_id_to=?"
			}
			return "r.work_id_from=?"
		}())
		args = append(args, spec.label, spec.invert, spec.invert, spec.stored, work)
	}
	q := strings.Join(parts, " UNION ALL ") + ` ORDER BY kind,source,target` //nolint:gosec // parts use one fixed SQL template and closed direction branches while every relation value stays parameter-bound.
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "PM1.Q8", "cannot read relations", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e RelationEdge
		var relationID int64
		if err := rows.Scan(&e.Kind, &e.Source, &e.Target, &e.Depth, &relationID); err != nil {
			return err
		}
		e.RelationID = strconv.FormatInt(relationID, 10)
		out.Edges = append(out.Edges, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if out.Edges == nil {
		out.Edges = []RelationEdge{}
	}
	return nil
}
