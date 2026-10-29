package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

// Bounded Product → Domain reads. These are the primary navigation path for
// current Domains, their current law, typed architecture relations, local
// attachments, and unresolved Domain overlap. The Git-derived registry is the
// only authority; a Product without a projected registry refuses with a typed
// failure rather than an empty page.

const (
	domainListDefaultLimit = 20
	domainListMaxLimit     = 100
	domainOverlapPairLimit = 50
)

// DomainRegistryView is the authority watermark every Domain read carries: it
// names the projected registry the answer came from, so callers can distinguish
// authoritative-empty (registry present, no matching rows) from absent
// (no registry at all, a typed refusal).
type DomainRegistryView struct {
	ProductID       string `json:"product_id"`
	ProductKey      string `json:"product_key"`
	RootDomainID    string `json:"root_domain_id"`
	ContentHash     string `json:"content_hash"`
	ScannedCommit   string `json:"scanned_commit_oid"`
	AttachmentWater string `json:"-"`
}

type DomainSummary struct {
	DomainID   string `json:"domain_id"`
	Name       string `json:"name"`
	Purpose    string `json:"purpose"`
	ParentID   string `json:"parent_domain_id,omitempty"`
	Status     string `json:"status"`
	HomeDomain bool   `json:"home_domain"`
}

type DomainListRequest struct {
	Product string
	Limit   int
	Cursor  string
}

type DomainListResult struct {
	ResultMeta
	Registry *DomainRegistryView `json:"registry"`
	Domains  []DomainSummary     `json:"domains"`
}

type DomainLawRecord struct {
	LawID         string   `json:"law_id"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	Path          string   `json:"path"`
	ContentHash   string   `json:"content_hash"`
	AppliesTo     []string `json:"applies_to,omitempty"`
	ScannedCommit string   `json:"scanned_commit_oid"`
}

type DomainRelationView struct {
	Kind          string   `json:"kind"`
	SourceDomain  string   `json:"source_domain_id"`
	TargetDomain  string   `json:"target_domain_id"`
	State         string   `json:"state"`
	GoverningLaws []string `json:"governing_law_ids"`
}

type DomainDetailRequest struct {
	Product string
	Domain  string
}

type DomainDetailResult struct {
	ResultMeta
	Registry     *DomainRegistryView  `json:"registry"`
	Domain       DomainSummary        `json:"domain"`
	CurrentLaw   []DomainLawRecord    `json:"current_law"`
	Relations    []DomainRelationView `json:"relations"`
	Observations []DomainObservation  `json:"observations"`
}

type DomainAttachmentView struct {
	ProjectEdges   []DomainProjectAttachment `json:"project_attachments"`
	ProjectVersion int64                     `json:"project_set_version"`
}

type DomainAttachmentsRequest struct {
	Product string
	Domain  string
}

type DomainAttachmentsResult struct {
	ResultMeta
	Registry    *DomainRegistryView  `json:"registry"`
	Domain      DomainSummary        `json:"domain"`
	Attachments DomainAttachmentView `json:"attachments"`
}

type DomainOverlapPair struct {
	FromWorkID      string   `json:"from_work_id"`
	ToWorkID        string   `json:"to_work_id"`
	SharedDomainIDs []string `json:"shared_domain_ids"`
	SharedLawIDs    []string `json:"shared_law_ids,omitempty"`
	ResolutionState string   `json:"resolution_state"`
	ResolutionKind  string   `json:"resolution_kind,omitempty"`
}

type DomainOverlapsRequest struct {
	Product string
	Domain  string
}

type DomainOverlapsResult struct {
	ResultMeta
	Registry  *DomainRegistryView `json:"registry"`
	Pairs     []DomainOverlapPair `json:"pairs"`
	Truncated bool                `json:"truncated"`
}

// Every Domain result type embeds ResultMeta, and Go promotes an embedded
// struct's fields into a whole-struct marshal. Those meta fields belong to the
// envelope, and each Domain result schema closes its object shape without
// declaring any of them, so a Domain result value is not itself a wire payload.
//
// The payload types below are that wire projection. Each one names every field
// the agent surface carries and nothing else, so reaching an agent requires an
// explicit entry here: a field added to a result type — promoted or declared —
// stays off the wire until it is named, and a field named here has nowhere to
// hide from the schema.
//
// The payload types reuse a result type's element structs wherever the two
// shapes are identical. DomainAttachmentView is the one place they are not:
// DomainProjectAttachment is the attachment write request's element type, and
// the read keeps its own element type carrying the same fields so either
// surface can change without moving the other.

type DomainListPayload struct {
	Registry *DomainRegistryView `json:"registry"`
	Domains  []DomainSummary     `json:"domains"`
}

func NewDomainListPayload(result DomainListResult) DomainListPayload {
	return DomainListPayload{Registry: result.Registry, Domains: sliceOrEmpty(result.Domains)}
}

type DomainDetailPayload struct {
	Registry     *DomainRegistryView  `json:"registry"`
	Domain       DomainSummary        `json:"domain"`
	CurrentLaw   []DomainLawRecord    `json:"current_law"`
	Relations    []DomainRelationView `json:"relations"`
	Observations []DomainObservation  `json:"observations"`
}

func NewDomainDetailPayload(result DomainDetailResult) DomainDetailPayload {
	return DomainDetailPayload{Registry: result.Registry, Domain: result.Domain, CurrentLaw: sliceOrEmpty(result.CurrentLaw), Relations: sliceOrEmpty(result.Relations), Observations: sliceOrEmpty(result.Observations)}
}

type DomainAttachmentViewPayload struct {
	ProjectEdges   []DomainProjectAttachment `json:"project_attachments"`
	ProjectVersion int64                     `json:"project_set_version"`
}

type DomainAttachmentsPayload struct {
	Registry    *DomainRegistryView         `json:"registry"`
	Domain      DomainSummary               `json:"domain"`
	Attachments DomainAttachmentViewPayload `json:"attachments"`
}

func NewDomainAttachmentsPayload(result DomainAttachmentsResult) DomainAttachmentsPayload {
	return DomainAttachmentsPayload{
		Registry: result.Registry,
		Domain:   result.Domain,
		Attachments: DomainAttachmentViewPayload{
			ProjectEdges:   sliceOrEmpty(result.Attachments.ProjectEdges),
			ProjectVersion: result.Attachments.ProjectVersion,
		},
	}
}

type DomainOverlapsPayload struct {
	Registry  *DomainRegistryView `json:"registry"`
	Pairs     []DomainOverlapPair `json:"pairs"`
	Truncated bool                `json:"truncated"`
}

func NewDomainOverlapsPayload(result DomainOverlapsResult) DomainOverlapsPayload {
	return DomainOverlapsPayload{Registry: result.Registry, Pairs: sliceOrEmpty(result.Pairs), Truncated: result.Truncated}
}

// sliceOrEmpty renders an absent collection as an empty JSON array. Every
// Domain read schema requires its collections, so a nil slice would marshal to
// null and be refused.
func sliceOrEmpty[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

func readDomainRegistry(ctx context.Context, q queryer, product string) (*DomainRegistryView, error) {
	var view DomainRegistryView
	err := q.QueryRowContext(ctx, `SELECT r.product_id,r.product_key,r.root_domain_id,r.content_hash,r.scanned_commit_oid FROM domain_registries r WHERE r.product_id=?`, product).Scan(&view.ProductID, &view.ProductKey, &view.RootDomainID, &view.ContentHash, &view.ScannedCommit)
	if err == sql.ErrNoRows {
		return nil, newFailure(KindDomainRegistryAbsent, "domain_read", "Product has no projected Domain registry", false, "rebuild the knowledge index from the Product Git home before reading Domains")
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "domain_read", "cannot read the Domain registry", true, "retry once the knowledge projection is readable", err)
	}
	return &view, nil
}

// QueryDomainList returns every current Domain for a Product in the bounded
// page order (status, name, domain_id). Deprecated Domains stay out of the
// default view; they remain reachable through registry history.
func (s *Store) QueryDomainList(ctx context.Context, req DomainListRequest) (DomainListResult, error) {
	var out DomainListResult
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C22.DomainList", "store is not open", false, "open a store before reading Domains")
	}
	return queryDomainList(ctx, s.db, req)
}

func queryDomainList(ctx context.Context, q queryer, req DomainListRequest) (DomainListResult, error) {
	var out DomainListResult
	registry, err := readDomainRegistry(ctx, q, req.Product)
	if err != nil {
		return out, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = domainListDefaultLimit
	}
	if limit < 1 || limit > domainListMaxLimit {
		return out, newFailure(KindInvalidFilter, "C22.DomainList", "Domain list limit is out of bounds", false, "use a limit between 1 and 100")
	}
	cursorName, cursorID, err := decodeDomainCursor(req.Cursor)
	if err != nil {
		return out, err
	}
	rows, err := q.QueryContext(ctx, `
		SELECT domain_id,name,purpose,parent_domain_id,status FROM domains
		WHERE product_id=? AND status='current' AND (name,domain_id) > (?,?)
		ORDER BY name,domain_id LIMIT ?`, req.Product, cursorName, cursorID, limit+1)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C22.DomainList", "cannot read Domains", true, "retry once the knowledge projection is readable", err)
	}
	defer rows.Close()
	out.Domains = []DomainSummary{}
	for rows.Next() {
		var d DomainSummary
		var parent sql.NullString
		if err := rows.Scan(&d.DomainID, &d.Name, &d.Purpose, &parent, &d.Status); err != nil {
			return out, wrapFailure(KindUnavailable, "C22.DomainList", "cannot decode Domain", true, "retry once the knowledge projection is readable", err)
		}
		d.ParentID = parent.String
		d.HomeDomain = d.DomainID == registry.RootDomainID
		out.Domains = append(out.Domains, d)
	}
	if err := rows.Err(); err != nil {
		return out, wrapFailure(KindUnavailable, "C22.DomainList", "cannot enumerate Domains", true, "retry once the knowledge projection is readable", err)
	}
	if len(out.Domains) > limit {
		out.Domains = out.Domains[:limit]
		last := out.Domains[len(out.Domains)-1]
		next := encodeDomainCursor(last.Name, last.DomainID)
		out.NextCursor = &next
	}
	out.Registry = registry
	out.ResultMeta = ResultMeta{QueryID: "C22.DomainList", ContractVersion: "C22/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: "authoritative", OrderingKeys: []string{"status", "name", "domain_id"}}
	return out, nil
}

func decodeDomainCursor(cursor string) (string, string, error) {
	if cursor == "" {
		return "", "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", newFailure(KindInvalidFilter, "C22", "Domain cursor is malformed", false, "continue from the previous page cursor")
	}
	parts := strings.SplitN(string(raw), "\x1f", 2)
	if len(parts) != 2 {
		return "", "", newFailure(KindInvalidFilter, "C22", "Domain cursor is malformed", false, "continue from the previous page cursor")
	}
	return parts[0], parts[1], nil
}

func encodeDomainCursor(name, id string) string {
	return base64.StdEncoding.EncodeToString([]byte(name + "\x1f" + id))
}

// QueryDomainDetail returns one Domain with its current law page and typed
// architecture relations. Superseded law is absent from the default view; the
// applicability fan-out names the other Domains each current law reaches.
func (s *Store) QueryDomainDetail(ctx context.Context, req DomainDetailRequest) (DomainDetailResult, error) {
	var out DomainDetailResult
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C22.DomainDetail", "store is not open", false, "open a store before reading Domains")
	}
	return queryDomainDetail(ctx, s.db, req)
}

func queryDomainDetail(ctx context.Context, q queryer, req DomainDetailRequest) (DomainDetailResult, error) {
	var out DomainDetailResult
	registry, err := readDomainRegistry(ctx, q, req.Product)
	if err != nil {
		return out, err
	}
	summary, err := domainExistsCurrent(ctx, q, req.Product, req.Domain)
	if err != nil {
		return out, err
	}
	summary.HomeDomain = summary.DomainID == registry.RootDomainID
	out.Domain = summary
	out.Registry = registry
	out.CurrentLaw = []DomainLawRecord{}
	out.Relations = []DomainRelationView{}

	lawRows, err := q.QueryContext(ctx, `
		SELECT s.law_id,s.kind,s.title,s.path,s.content_hash,s.scanned_commit_oid
		FROM law_subjects s
		JOIN law_domain_homes h ON h.home_project_id=s.home_project_id AND h.home_locator_id=s.home_locator_id AND h.law_id=s.law_id
		WHERE h.product_id=? AND h.domain_id=? AND s.status='accepted'
		ORDER BY s.kind,s.law_id`, req.Product, req.Domain)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot read current law", true, "retry once the knowledge projection is readable", err)
	}
	defer lawRows.Close()
	for lawRows.Next() {
		var record DomainLawRecord
		if err := lawRows.Scan(&record.LawID, &record.Kind, &record.Title, &record.Path, &record.ContentHash, &record.ScannedCommit); err != nil {
			return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot decode current law", true, "retry once the knowledge projection is readable", err)
		}
		out.CurrentLaw = append(out.CurrentLaw, record)
	}
	if err := lawRows.Err(); err != nil {
		return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot enumerate current law", true, "retry once the knowledge projection is readable", err)
	}
	lawRows.Close()
	for i := range out.CurrentLaw {
		appRows, err := q.QueryContext(ctx, `SELECT domain_id FROM law_domain_applicability WHERE product_id=? AND law_id=? ORDER BY domain_id`, req.Product, out.CurrentLaw[i].LawID)
		if err != nil {
			return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot read law applicability", true, "retry once the knowledge projection is readable", err)
		}
		for appRows.Next() {
			var domain string
			if err := appRows.Scan(&domain); err != nil {
				appRows.Close()
				return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot decode law applicability", true, "retry once the knowledge projection is readable", err)
			}
			out.CurrentLaw[i].AppliesTo = append(out.CurrentLaw[i].AppliesTo, domain)
		}
		if err := appRows.Err(); err != nil {
			appRows.Close()
			return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot enumerate law applicability", true, "retry once the knowledge projection is readable", err)
		}
		appRows.Close()
	}

	relRows, err := q.QueryContext(ctx, `
		SELECT r.kind,r.source_domain_id,r.target_domain_id,r.state,
			(SELECT json_group_array(l.law_id ORDER BY l.law_id) FROM domain_relation_governing_laws l
			 WHERE l.home_project_id=r.home_project_id AND l.home_locator_id=r.home_locator_id AND l.product_id=r.product_id
			   AND l.source_domain_id=r.source_domain_id AND l.kind=r.kind AND l.target_domain_id=r.target_domain_id) AS laws
		FROM domain_architecture_relations r
		WHERE r.product_id=? AND (r.source_domain_id=? OR r.target_domain_id=?)
		ORDER BY r.kind,r.source_domain_id,r.target_domain_id`, req.Product, req.Domain, req.Domain)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot read Domain relations", true, "retry once the knowledge projection is readable", err)
	}
	defer relRows.Close()
	for relRows.Next() {
		var view DomainRelationView
		var laws string
		if err := relRows.Scan(&view.Kind, &view.SourceDomain, &view.TargetDomain, &view.State, &laws); err != nil {
			return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot decode Domain relations", true, "retry once the knowledge projection is readable", err)
		}
		if err := json.Unmarshal([]byte(laws), &view.GoverningLaws); err != nil {
			return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot decode governing laws", true, "retry once the knowledge projection is readable", err)
		}
		out.Relations = append(out.Relations, view)
	}
	if err := relRows.Err(); err != nil {
		return out, wrapFailure(KindUnavailable, "C22.DomainDetail", "cannot enumerate Domain relations", true, "retry once the knowledge projection is readable", err)
	}
	relRows.Close()

	// CD-0224: the open observations join the existing Domain surface, so
	// the operator meets them where the next decision about the Domain is
	// made. They are not authority — the result stays authoritative on law and
	// relations, and an observation satisfies nothing.
	observations, err := observationsForDomain(ctx, q, req.Product, req.Domain, DomainObservationOpenWindow)
	if err != nil {
		return out, err
	}
	out.Observations = observations

	out.ResultMeta = ResultMeta{QueryID: "C22.DomainDetail", ContractVersion: "C22/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product, DomainID: req.Domain}, Authority: "authoritative", OrderingKeys: []string{"kind", "source_domain_id", "target_domain_id"}}
	return out, nil
}

// QueryDomainAttachments returns the local Project and resource attachment
// sets for one current Domain, with their set versions.
func (s *Store) QueryDomainAttachments(ctx context.Context, req DomainAttachmentsRequest) (DomainAttachmentsResult, error) {
	var out DomainAttachmentsResult
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C22.DomainAttachments", "store is not open", false, "open a store before reading Domains")
	}
	return queryDomainAttachments(ctx, s.db, req)
}

func queryDomainAttachments(ctx context.Context, q queryer, req DomainAttachmentsRequest) (DomainAttachmentsResult, error) {
	var out DomainAttachmentsResult
	registry, err := readDomainRegistry(ctx, q, req.Product)
	if err != nil {
		return out, err
	}
	summary, err := domainExistsCurrent(ctx, q, req.Product, req.Domain)
	if err != nil {
		return out, err
	}
	summary.HomeDomain = summary.DomainID == registry.RootDomainID
	out.Domain = summary
	out.Registry = registry
	// An unattached Domain is an authoritative empty edge set, so the edge
	// collection serializes as an array rather than null.
	out.Attachments.ProjectEdges = []DomainProjectAttachment{}
	if err := q.QueryRowContext(ctx, `SELECT version FROM domain_project_attachment_sets WHERE product_id=? AND domain_id=?`, req.Product, req.Domain).Scan(&out.Attachments.ProjectVersion); err != nil && err != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "C22.DomainAttachments", "cannot read Project attachments", true, "retry once the database is readable", err)
	}
	prows, err := q.QueryContext(ctx, `SELECT project_id,role FROM domain_project_attachment_edges WHERE product_id=? AND domain_id=? ORDER BY role DESC,project_id`, req.Product, req.Domain)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C22.DomainAttachments", "cannot read Project attachments", true, "retry once the database is readable", err)
	}
	defer prows.Close()
	for prows.Next() {
		var edge DomainProjectAttachment
		if err := prows.Scan(&edge.ProjectID, &edge.Role); err != nil {
			return out, wrapFailure(KindUnavailable, "C22.DomainAttachments", "cannot decode Project attachments", true, "retry once the database is readable", err)
		}
		out.Attachments.ProjectEdges = append(out.Attachments.ProjectEdges, edge)
	}
	if err := prows.Err(); err != nil {
		return out, wrapFailure(KindUnavailable, "C22.DomainAttachments", "cannot enumerate Project attachments", true, "retry once the database is readable", err)
	}
	prows.Close()
	out.ResultMeta = ResultMeta{QueryID: "C22.DomainAttachments", ContractVersion: "C22/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product, DomainID: req.Domain}, Authority: "authoritative", OrderingKeys: []string{"role", "project_id"}}
	return out, nil
}

// QueryDomainOverlaps reports exact shared-write pairs with at least one
// execution-started side, including their current resolution. Its footprint
// and freshness derivation are shared with consequential admission.
func (s *Store) QueryDomainOverlaps(ctx context.Context, req DomainOverlapsRequest) (DomainOverlapsResult, error) {
	var out DomainOverlapsResult
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C22.DomainOverlaps", "store is not open", false, "open a store before reading Domains")
	}
	return queryDomainOverlaps(ctx, s.db, req)
}

func queryDomainOverlaps(ctx context.Context, q queryer, req DomainOverlapsRequest) (DomainOverlapsResult, error) {
	var out DomainOverlapsResult
	omissions, err := duplicateActiveContractOmissions(ctx, q, req.Product)
	if err != nil {
		return out, err
	}
	registry, err := readDomainRegistry(ctx, q, req.Product)
	if err != nil {
		return out, err
	}
	out.Pairs = []DomainOverlapPair{}
	if req.Domain != "" {
		if _, err := domainExistsCurrent(ctx, q, req.Product, req.Domain); err != nil {
			return out, err
		}
	}
	// Every subject keeps its prospective footprint. The shared peer reader
	// requires execution start, so two unstarted subjects never form a pair.
	var subjects []string
	if err := readOverlapString(ctx, q, `SELECT DISTINCT c.work_id FROM workflow_contracts c JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version JOIN work_items w ON w.id=c.work_id WHERE c.superseded_by IS NULL AND w.lifecycle NOT IN ('completed','cancelled','superseded') AND (SELECT count(*) FROM workflow_contracts c3 WHERE c3.work_id=c.work_id AND c3.superseded_by IS NULL)=1 AND b.product_id=? ORDER BY c.work_id`, &subjects, req.Product); err != nil {
		return out, err
	}
	seen := map[[2]string]bool{}
	for _, subject := range subjects {
		self, peers, err := readWorkflowDomainOverlapCandidatesTx(ctx, q, subject)
		if err != nil {
			return out, err
		}
		for _, peer := range peers {
			overlap, intersects := workflowDomainOverlapPair(self, peer)
			if !intersects {
				continue
			}
			key := [2]string{overlap.FromWorkID, overlap.ToWorkID}
			if seen[key] || (req.Domain != "" && !slices.Contains(overlap.SharedAffectedDomainIDs, req.Domain)) {
				continue
			}
			seen[key] = true
			state, kind, err := currentWorkflowOverlapResolutionTx(ctx, q, overlap)
			if err != nil {
				return out, err
			}
			if state == "unresolved" {
				state = "absent"
			}
			out.Pairs = append(out.Pairs, DomainOverlapPair{FromWorkID: overlap.FromWorkID, ToWorkID: overlap.ToWorkID, SharedDomainIDs: overlap.SharedAffectedDomainIDs, SharedLawIDs: overlap.SharedLawIDs, ResolutionState: state, ResolutionKind: kind})
			// A prospective subject can discover an earlier started peer after
			// later-sorting pairs. Keep the smallest bounded canonical set.
			boundDomainOverlapPairs(&out, seen)
		}
	}
	out.Registry = registry
	out.ResultMeta = ResultMeta{QueryID: "C22.DomainOverlaps", ContractVersion: "C22/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: "authoritative", OrderingKeys: []string{"from_work_id", "to_work_id"}, Omissions: omissions}
	return out, nil
}

func boundDomainOverlapPairs(out *DomainOverlapsResult, seen map[[2]string]bool) {
	sort.Slice(out.Pairs, func(i, j int) bool {
		if out.Pairs[i].FromWorkID == out.Pairs[j].FromWorkID {
			return out.Pairs[i].ToWorkID < out.Pairs[j].ToWorkID
		}
		return out.Pairs[i].FromWorkID < out.Pairs[j].FromWorkID
	})
	if len(out.Pairs) > domainOverlapPairLimit {
		last := out.Pairs[domainOverlapPairLimit]
		delete(seen, [2]string{last.FromWorkID, last.ToWorkID})
		out.Pairs = out.Pairs[:domainOverlapPairLimit]
		out.Truncated = true
	}
}

func domainExistsCurrent(ctx context.Context, q queryer, product, domain string) (DomainSummary, error) {
	var summary DomainSummary
	var parent sql.NullString
	err := q.QueryRowContext(ctx, `SELECT domain_id,name,purpose,parent_domain_id,status FROM domains WHERE product_id=? AND domain_id=?`, product, domain).Scan(&summary.DomainID, &summary.Name, &summary.Purpose, &parent, &summary.Status)
	if err == sql.ErrNoRows {
		return summary, newFailure(KindUnknownDomain, "domain_read", "Domain does not exist in the Product registry", false, "read the Domain list for the current registry before requesting detail")
	}
	if err != nil {
		return summary, wrapFailure(KindUnavailable, "domain_read", "cannot read Domain", true, "retry once the knowledge projection is readable", err)
	}
	if summary.Status != "current" {
		return summary, newFailure(KindUnknownDomain, "domain_read", "Domain is deprecated in the current registry", false, "read current Domains only; deprecated Domains remain historical")
	}
	summary.ParentID = parent.String
	return summary, nil
}

func readOverlapString(ctx context.Context, q queryer, query string, target *[]string, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "domain_read", "cannot read Domain footprint", true, "retry once the workflow projection is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return wrapFailure(KindUnavailable, "domain_read", "cannot decode Domain footprint", true, "retry once the workflow projection is readable", err)
		}
		*target = append(*target, value)
	}
	return rows.Err()
}
