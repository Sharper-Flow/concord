package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// refinementContextLimit bounds one contextual amendment-context page
// (PM1 Q10 opt-in current_amendment_context). The universal default is 20;
// the accepted contract caps a contextual page at 32 edges.
const (
	refinementContextDefaultLimit = 20
	refinementContextMaxLimit     = 32
)

// KnowledgeRefinementContextRequest asks for the source-qualified one-hop
// amendment context of one or more root laws. Roots take the qualified
// "project_id/law_id" form (CD-0200 D4) or a bare law ID that reads across
// the whole requested source set. Sources is the verified source set the
// caller resolved; an explicit single Home is the one-element form. The
// query reads only authored law_relations and
// law_cross_source_relations projections: no inference, no transitive
// edges, no clause authority, and no precedence derivation.
type KnowledgeRefinementContextRequest struct {
	Product       string
	Roots         []string
	Limit         int
	Cursor        string
	AllowDegraded bool
	Home          KnowledgeHome
	Sources       []KnowledgeHome
}

// KnowledgeRefinementEdge is one authored one-hop relation. Direction
// "incoming" marks a direct accepted refinement of the root
// (kind='refines', endpoint is the refining law, whole-record status
// accepted). Direction "outgoing" marks an explicitly declared relation
// from the root, same-home or cross-source, with a qualified endpoint.
type KnowledgeRefinementEdge struct {
	RootID              string `json:"root_id"`
	Direction           string `json:"direction"`
	Kind                string `json:"kind"`
	EndpointProjectID   string `json:"endpoint_project_id"`
	EndpointLocatorID   string `json:"endpoint_locator_id"`
	EndpointLawID       string `json:"endpoint_law_id"`
	EndpointKind        string `json:"endpoint_kind,omitempty"`
	EndpointStatus      string `json:"endpoint_status,omitempty"`
	EndpointTitle       string `json:"endpoint_title,omitempty"`
	EndpointPath        string `json:"endpoint_path,omitempty"`
	EndpointContentHash string `json:"endpoint_content_hash,omitempty"`
	SourceProjectID     string `json:"source_project_id"`
	SourceLocatorID     string `json:"source_locator_id"`
	ScannedCommitOID    string `json:"scanned_commit_oid"`
}

// KnowledgeRefinementContextResult is the bounded one-hop answer. Roots
// whose edges exceeded the page are named in IncompleteRoots; NextCursor
// continues the identical snapshot or refuses.
type KnowledgeRefinementContextResult struct {
	ResultMeta
	Edges            []KnowledgeRefinementEdge  `json:"edges"`
	Roots            []string                   `json:"roots"`
	IncompleteRoots  []string                   `json:"incomplete_roots,omitempty"`
	SourceWatermarks []KnowledgeSourceWatermark `json:"source_watermarks,omitempty"`
}

// refinementCursor binds the page to the roots, the source-set digest, the
// scanned identity of every participating source, the full qualifying edge
// snapshot digest, and the last ordering key. Any authored relation or
// source-commit change between pages refuses continuation instead of
// splicing two snapshots.
type refinementCursor struct {
	Version        int      `json:"version"`
	Roots          []string `json:"roots"`
	SourcesDigest  string   `json:"sources_digest"`
	SnapshotDigest string   `json:"snapshot_digest"`
	LastKind       string   `json:"last_kind"`
	LastDirection  string   `json:"last_direction"`
	LastEndpoint   string   `json:"last_endpoint"`
	LastRoot       string   `json:"last_root"`
}

// refinementEdgeKey is the deterministic ordering key: kind, direction,
// endpoint source and ID, then root for cross-root workflow pages.
func refinementEdgeKey(edge KnowledgeRefinementEdge) (string, string, string, string) {
	endpoint := edge.EndpointProjectID + "/" + edge.EndpointLocatorID + "/" + edge.EndpointLawID
	return edge.Kind, edge.Direction, endpoint, edge.RootID
}

func refinementEdgeLess(a, b KnowledgeRefinementEdge) bool {
	ak, ad, ae, ar := refinementEdgeKey(a)
	bk, bd, be, br := refinementEdgeKey(b)
	if ak != bk {
		return ak < bk
	}
	if ad != bd {
		return ad < bd
	}
	if ae != be {
		return ae < be
	}
	return ar < br
}

func refinementSnapshotDigest(edges []KnowledgeRefinementEdge, commits []string) string {
	hash := sha256.New()
	for _, commit := range commits {
		hash.Write([]byte("commit\x00"))
		hash.Write([]byte(commit))
		hash.Write([]byte("\x00"))
	}
	for _, edge := range edges {
		k, d, e, r := refinementEdgeKey(edge)
		for _, part := range []string{k, d, e, r, edge.SourceProjectID, edge.SourceLocatorID, edge.ScannedCommitOID, edge.EndpointContentHash} {
			hash.Write([]byte(part))
			hash.Write([]byte("\x00"))
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func encodeRefinementCursor(cursor refinementCursor) (string, error) {
	b, err := json.Marshal(cursor)
	if err != nil {
		return "", wrapFailure(KindInvalidCursor, "PM1.Q10.amendment_context", "cannot encode the amendment-context cursor", false, "restart the bounded amendment-context query", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// decodeRefinementCursor accepts a continuation only when it binds the
// same requested roots, the same source set, and the same qualifying-edge
// snapshot the current read produced.
func decodeRefinementCursor(raw string, roots []string, sourcesDigest, snapshotDigest string) (refinementCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	var cursor refinementCursor
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.Version != 1 || !equalStrings(cursor.Roots, roots) || cursor.SourcesDigest != sourcesDigest || cursor.SnapshotDigest != snapshotDigest || cursor.LastKind == "" || cursor.LastDirection == "" || cursor.LastEndpoint == "" || cursor.LastRoot == "" {
		return refinementCursor{}, newFailure(KindInvalidCursor, "PM1.Q10.amendment_context", "amendment-context cursor does not match the requested roots, source set, or relation snapshot", false, "restart the amendment-context query at the current snapshot")
	}
	return cursor, nil
}

// refinementResolvedRoot is one requested root after source-qualified
// identity resolution. A qualified root carries the one canonical home it
// resolved through, so its reads stay inside that source; a bare root
// reads across the whole requested set (CD-0200 federation).
type refinementResolvedRoot struct {
	original string
	bare     string
	scope    *KnowledgeHome
}

// refinementSourceVerification carries what the *sql.DB wrapper proved
// about the source set before the tx-scoped core runs: git-backed source
// verification must never run inside an open transaction (CD-0195 D2), so
// the wrapper owns it and hands the core only its conclusions.
type refinementSourceVerification struct {
	scanned    []string
	watermarks []KnowledgeSourceWatermark
	omissions  []string
	degraded   bool
}

// QueryKnowledgeRefinementContext returns the deterministic one-hop
// amendment context for the requested roots over verified sources. It is
// the single store-owned reader shared by contextual note resolution
// (Q10 opt-in current_amendment_context) and workflow law_context.
func (s *Store) QueryKnowledgeRefinementContext(ctx context.Context, req KnowledgeRefinementContextRequest) (KnowledgeRefinementContextResult, error) {
	if s == nil || s.db == nil {
		var out KnowledgeRefinementContextResult
		return out, newFailure(KindUnavailable, "PM1.Q10.amendment_context", "store is not open", false, "open a store before querying amendment context")
	}
	return queryKnowledgeRefinementContextDB(ctx, s.db, req)
}

// queryKnowledgeRefinementContextDB owns source verification and the
// tx-scoped core on the pool connection. Q10's canonical note read calls it
// directly on the same *sql.DB: no transaction is open on that path, so the
// git-backed verification may safely use the pool.
func queryKnowledgeRefinementContextDB(ctx context.Context, db *sql.DB, req KnowledgeRefinementContextRequest) (KnowledgeRefinementContextResult, error) {
	var out KnowledgeRefinementContextResult
	sources := req.Sources
	if len(sources) == 0 {
		if req.Home.HomeProjectID == "" && req.Home.RepoPath == "" {
			return out, newFailure(KindUnknownScope, "PM1.Q10.amendment_context", "amendment context requires an explicit source set or home", false, "supply the resolved knowledge sources")
		}
		sources = []KnowledgeHome{req.Home}
	}
	roots := orderedStrings(nonEmptyStrings(req.Roots))
	if len(roots) == 0 {
		return out, newFailure(KindInvalidFilter, "PM1.Q10.amendment_context", "amendment context requires at least one root law", false, "supply one or more root law references")
	}
	if len(roots) > refinementContextMaxLimit {
		return out, newFailure(KindInvalidFilter, "PM1.Q10.amendment_context", "at most 32 root laws are allowed per amendment-context query", false, "split the root set into bounded pages")
	}
	for _, root := range roots {
		if _, _, _, err := parseQualifiedKnowledgeID("PM1.Q10.amendment_context", root); err != nil {
			return out, err
		}
	}
	limit := req.Limit
	if limit == 0 {
		limit = refinementContextDefaultLimit
	}
	if limit < 1 || limit > refinementContextMaxLimit {
		return out, newFailure(KindInvalidFilter, "PM1.Q10.amendment_context", "limit must be between 1 and 32", false, "supply a bounded amendment-context limit")
	}
	verification := refinementSourceVerification{
		watermarks: make([]KnowledgeSourceWatermark, 0, len(sources)),
	}
	for _, source := range sources {
		label := source.HomeProjectID + "/" + source.HomeLocatorID
		scanned, authority, err := validateKnowledgeHomeForQueryCore(ctx, db, source, req.AllowDegraded, "PM1.Q10.amendment_context")
		if err != nil {
			return out, err
		}
		if authority != "authoritative" {
			verification.degraded = true
			verification.omissions = append(verification.omissions, "knowledge_source_degraded:"+label)
		} else {
			verification.scanned = append(verification.scanned, label+"@"+scanned)
		}
		verification.watermarks = append(verification.watermarks, KnowledgeSourceWatermark{ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID, Watermark: scanned, Authority: authority})
	}
	// Strict contextual reads refuse failed required-source verification; a
	// degraded-allowed read names every omission and never reads as an
	// authoritative no-amendments graph.
	if verification.degraded && !req.AllowDegraded {
		return out, newFailure(KindUnreachable, "PM1.Q10.amendment_context", "a required amendment-context source is unreachable or stale", true, "restore the source and retry, or explicitly allow degradation")
	}
	// One read transaction now owns every projection read below: the source
	// watermark identity check, the relation reads, the endpoint reads, and
	// the cursor snapshot all resolve against this single consistent
	// snapshot, so a projection refresh that commits after out-of-transaction
	// verification is either invisible to the page or refused as drift. The
	// git-backed verification above stays on the pool (CD-0195 D2).
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot open a consistent amendment-context read snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	page, err := queryKnowledgeRefinementContext(ctx, tx, req, sources, roots, limit, verification)
	if err != nil {
		return out, err
	}
	if err := tx.Rollback(); err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot close the amendment-context read snapshot", true, "retry once the database is readable", err)
	}
	return page, nil
}

// refinementOmissions collects named omissions deterministically: source
// degradation plus every inspected endpoint incompleteness.
type refinementOmissions struct {
	values []string
	seen   map[string]bool
}

func (o *refinementOmissions) mark(omission string) {
	if !o.seen[omission] {
		o.seen[omission] = true
		o.values = append(o.values, omission)
	}
}

// refinementVerifiedWatermarks indexes the sources the pool verifier proved
// authoritative, by home label, with the scanned commit each proof bound.
func refinementVerifiedWatermarks(verification refinementSourceVerification) map[string]string {
	verified := map[string]string{}
	for _, watermark := range verification.watermarks {
		if watermark.Authority == "authoritative" {
			verified[watermark.ProjectID+"/"+watermark.LocatorID] = watermark.Watermark
		}
	}
	return verified
}

// refinementWatermarkDrift establishes, inside the caller's read snapshot,
// that every source the pool verifier proved authoritative still carries
// the identical projection identity it proved. A projection refresh that
// commits between pool verification and this read snapshot must never join
// the old verified proof to the refreshed relation rows: the per-home
// rebuild rewrites law_relations, law_subjects, and the watermark in one
// transaction, so a committed state always equals the watermark. Git and
// content probes stay on the pool connection (CD-0195 D2); this reads only
// the watermark projection through the caller's queryer, so an empty graph
// gets the same identity check as a populated one.
func refinementWatermarkDrift(ctx context.Context, q queryer, sources []KnowledgeHome, verification refinementSourceVerification) (string, string, string, bool) {
	verified := refinementVerifiedWatermarks(verification)
	if len(verified) == 0 {
		return "", "", "", false
	}
	for _, source := range sources {
		label := source.HomeProjectID + "/" + source.HomeLocatorID
		proof, authoritative := verified[label]
		if !authoritative {
			continue
		}
		var current string
		err := q.QueryRowContext(ctx, `SELECT scanned_commit_oid FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, source.HomeProjectID, source.HomeLocatorID, source.HeadRef).Scan(&current)
		if err == sql.ErrNoRows || (err == nil && current != proof) {
			return label, proof, current, true
		}
		if err != nil {
			// An unreadable watermark cannot establish identity either; the
			// read refuses rather than guessing which snapshot it saw.
			return label, proof, "", true
		}
	}
	return "", "", "", false
}

// refinementRefuseSnapshotDrift is the shared drift boundary: strict
// contextual reads refuse, degraded-allowed reads name the omission and
// keep the page away from an authoritative claim.
func refinementRefuseSnapshotDrift(label, proof, current string) error {
	return newFailure(KindStaleContext, "PM1.Q10.amendment_context", "the verified amendment-context source snapshot changed before the read: "+label+" was verified at "+proof+" but the read snapshot holds "+current, true, "re-verify the amendment-context sources and retry the read")
}

// queryKnowledgeRefinementContext is the tx-scoped core: every database
// read goes through the caller's queryer, so a caller holding an open
// transaction reuses its own handle under the single-connection invariant
// instead of parking on the pool. The caller must supply one coherent read
// snapshot (a read transaction): the watermark identity check, the relation
// reads, the endpoint reads, and the cursor snapshot then resolve against
// the same snapshot, so no bracket re-read is needed or possible. The
// *sql.DB wrapper owns git-backed source verification; the core owns root
// qualification, the authored relation reads, endpoint inspection,
// ordering, paging, and the cursor.
func queryKnowledgeRefinementContext(ctx context.Context, q queryer, req KnowledgeRefinementContextRequest, sources []KnowledgeHome, roots []string, limit int, verification refinementSourceVerification) (KnowledgeRefinementContextResult, error) {
	var out KnowledgeRefinementContextResult
	if label, proof, current, drifted := refinementWatermarkDrift(ctx, q, sources, verification); drifted {
		if !req.AllowDegraded {
			return out, refinementRefuseSnapshotDrift(label, proof, current)
		}
		verification.degraded = true
		verification.omissions = append(verification.omissions, "source_snapshot_drift:"+label+":"+proof+"->"+current)
	}
	resolved, err := resolveRefinementRoots(ctx, q, roots, sources)
	if err != nil {
		return out, err
	}
	digest := knowledgeSourceSetDigest(sources)
	omissions := &refinementOmissions{seen: map[string]bool{}}
	for _, omission := range verification.omissions {
		omissions.mark(omission)
	}
	edges, err := refinementContextEdges(ctx, q, sources, resolved, omissions)
	if err != nil {
		return out, err
	}
	// Relation rows must carry the same scanned identity the proof bound: a
	// row refreshed inside the read snapshot cannot ride an older verified
	// watermark, and a row older than the watermark would equally splice two
	// scans. Both directions refuse or degrade through the same boundary.
	verified := refinementVerifiedWatermarks(verification)
	for _, edge := range edges {
		if proof, authoritative := verified[edge.SourceProjectID+"/"+edge.SourceLocatorID]; authoritative && edge.ScannedCommitOID != proof {
			if !req.AllowDegraded {
				return out, refinementRefuseSnapshotDrift(edge.SourceProjectID+"/"+edge.SourceLocatorID, proof, edge.ScannedCommitOID)
			}
			omissions.mark("source_snapshot_drift:" + edge.SourceProjectID + "/" + edge.SourceLocatorID + ":" + proof + "->" + edge.ScannedCommitOID)
			verification.degraded = true
		}
	}
	// The caller's single read snapshot owns identity: every statement above
	// — the watermark check, the root resolution, the relation reads, and the
	// endpoint inspection — resolved against the same transaction, so the
	// verified proof and the returned relations cannot come from two commits.
	// A projection refresh that commits after the pool verification is either
	// invisible to this snapshot or refused as drift by the opening check.
	snapshot := refinementSnapshotDigest(edges, verification.scanned)
	var resume *refinementCursor
	if req.Cursor != "" {
		decoded, err := decodeRefinementCursor(req.Cursor, roots, digest, snapshot)
		if err != nil {
			return out, err
		}
		resume = &decoded
	}
	// Apply the cursor resume key, then the deterministic global order.
	if resume != nil {
		cursor := resume
		resumed := make([]KnowledgeRefinementEdge, 0, len(edges))
		for _, edge := range edges {
			k, d, e, r := refinementEdgeKey(edge)
			if k > cursor.LastKind || k == cursor.LastKind && d > cursor.LastDirection || k == cursor.LastKind && d == cursor.LastDirection && e > cursor.LastEndpoint || k == cursor.LastKind && d == cursor.LastDirection && e == cursor.LastEndpoint && r > cursor.LastRoot {
				resumed = append(resumed, edge)
			}
		}
		edges = resumed
	}
	sort.Slice(edges, func(i, j int) bool { return refinementEdgeLess(edges[i], edges[j]) })
	paged := edges
	hasMore := false
	if len(paged) > limit {
		paged = paged[:limit]
		hasMore = true
	}
	// Incomplete-root accounting runs over the requested root forms: every
	// edge counts toward each requested root whose effective scope covers
	// the edge's source, so a qualified root never reports another
	// source's edges as its own.
	rootTotals := refinementRootCoverage(edges, resolved)
	rootCounts := refinementRootCoverage(paged, resolved)
	incomplete := make([]string, 0)
	if hasMore {
		for _, root := range resolved {
			if rootTotals[root.original] > rootCounts[root.original] {
				incomplete = append(incomplete, root.original)
			}
		}
	}
	var next *string
	if hasMore {
		last := paged[len(paged)-1]
		k, d, e, r := refinementEdgeKey(last)
		encoded, err := encodeRefinementCursor(refinementCursor{Version: 1, Roots: roots, SourcesDigest: digest, SnapshotDigest: snapshot, LastKind: k, LastDirection: d, LastEndpoint: e, LastRoot: r})
		if err != nil {
			return out, err
		}
		next = &encoded
	}
	sort.Strings(omissions.values)
	authority := "authoritative"
	if verification.degraded {
		authority = "degraded"
	} else if len(omissions.values) > 0 {
		// An incomplete graph — an endpoint outside the set, an ambiguous
		// endpoint identity, or a missing projection — never claims
		// authoritative no-amendments (CD-0200 D5).
		authority = "partial"
	}
	now := time.Now().UTC()
	meta := ResultMeta{QueryID: "PM1.Q10.amendment_context", ContractVersion: queryContractVersion, ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: authority, Freshness: Freshness{ObservedAt: now.Format(time.RFC3339Nano), Age: 0, Stale: false}, OrderingKeys: []string{"kind", "direction", "endpoint_source", "endpoint_id", "root"}, Omissions: omissions.values, Warnings: []string{"one_hop_authored_relations_only", "no_inferred_or_transitive_edges"}}
	meta.NextCursor = next
	out.ResultMeta, out.Edges, out.Roots, out.IncompleteRoots, out.SourceWatermarks = meta, paged, roots, incomplete, verification.watermarks
	return out, nil
}

// resolveRefinementRoots resolves every requested root's source-qualified
// identity through the existing owner: a qualified root names its Project
// and resolves only through that Project's canonical knowledge locator
// (CD-0200 D4), with the same zero-candidate and ambiguity refusals Q10
// gives, and the resolved home must participate in the requested source
// set. A bare root keeps its federated read across the whole set. A
// Project's canonical locator is resolved once per distinct Project, not
// once per root: statement count stays flat as the requested root set
// grows inside one source.
func resolveRefinementRoots(ctx context.Context, q queryer, roots []string, sources []KnowledgeHome) ([]refinementResolvedRoot, error) {
	resolved := make([]refinementResolvedRoot, 0, len(roots))
	canonicalByProject := map[string][]KnowledgeHome{}
	for _, root := range roots {
		projectID, lawID, qualified, err := parseQualifiedKnowledgeID("PM1.Q10.amendment_context", root)
		if err != nil {
			return nil, err
		}
		if !qualified {
			resolved = append(resolved, refinementResolvedRoot{original: root, bare: lawID})
			continue
		}
		candidates, cached := canonicalByProject[projectID]
		if !cached {
			candidates, err = projectCanonicalHomeCandidates(ctx, q, projectID)
			if err != nil {
				return nil, err
			}
			canonicalByProject[projectID] = candidates
		}
		if len(candidates) == 0 {
			return nil, newFailure(KindKnowledgeUnavailable, "PM1.Q10.amendment_context", "qualified root names a Project with no canonical-path knowledge locator", false, "designate the Project's canonical-path locator before resolving through it")
		}
		if len(candidates) > 1 {
			return nil, newAmbiguousScopeFailure("PM1.Q10.amendment_context", "qualified root names a Project with multiple canonical-path locators", "leave exactly one canonical Project locator", knowledgeHomeCandidateIDs(candidates))
		}
		candidate := candidates[0]
		member := false
		for _, source := range sources {
			if source.HomeProjectID == candidate.HomeProjectID && source.HomeLocatorID == candidate.HomeLocatorID {
				member = true
				break
			}
		}
		if !member {
			return nil, newFailure(KindUnknownScope, "PM1.Q10.amendment_context", "qualified root names a source outside the requested amendment-context source set", false, "supply a source set that contains the qualified root's source")
		}
		resolved = append(resolved, refinementResolvedRoot{original: root, bare: lawID, scope: &candidate})
	}
	if err := refuseAmbiguousBareRefinementRoots(ctx, q, roots, sources); err != nil {
		return nil, err
	}
	return resolved, nil
}

// refuseAmbiguousBareRefinementRoots refuses a bare root whose law ID is
// held by more than one source in the requested set (CD-0200 D4): the set
// does not federate same-ID subjects into one graph, so the caller must
// qualify the root instead of reading an unspecified merge. One bounded
// statement per source, indexed by the home prefix of law_subjects_lookup.
func refuseAmbiguousBareRefinementRoots(ctx context.Context, q queryer, roots []string, sources []KnowledgeHome) error {
	bare := make([]string, 0, len(roots))
	for _, root := range roots {
		if _, lawID, qualified, err := parseQualifiedKnowledgeID("PM1.Q10.amendment_context", root); err != nil {
			return err
		} else if !qualified {
			bare = append(bare, lawID)
		}
	}
	if len(bare) == 0 || len(sources) < 2 {
		return nil
	}
	bare = orderedStrings(bare)
	holders := map[string]map[string]bool{}
	for _, source := range sources {
		rows, err := q.QueryContext(ctx, `SELECT DISTINCT law_id FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id IN (`+placeholdersFor(bare)+`)`, append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(bare)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
		if err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot read the law subjects of source "+source.HomeProjectID, true, "retry once the database is readable", err)
		}
		for rows.Next() {
			var lawID string
			if err := rows.Scan(&lawID); err != nil {
				rows.Close()
				return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode a law subject of source "+source.HomeProjectID, true, "retry once the database is readable", err)
			}
			if holders[lawID] == nil {
				holders[lawID] = map[string]bool{}
			}
			holders[lawID][source.HomeProjectID+"/"+source.HomeLocatorID] = true
		}
		if err := wrapFailureRows("PM1.Q10.amendment_context", "cannot finish reading the law subjects of source "+source.HomeProjectID, rows.Err()); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	for _, lawID := range bare {
		if len(holders[lawID]) > 1 {
			held := make([]string, 0, len(holders[lawID]))
			for source := range holders[lawID] {
				held = append(held, source)
			}
			sort.Strings(held)
			return newAmbiguousScopeFailure("PM1.Q10.amendment_context", "bare root "+lawID+" is held by more than one source in the requested set", "qualify the root as project_id/"+lawID+" or narrow the source set", held)
		}
	}
	return nil
}

// refinementRootsForSource lists the distinct bare root IDs a source's SQL
// reads may match: every bare root plus every qualified root that resolved
// through this exact source.
func refinementRootsForSource(resolved []refinementResolvedRoot, source KnowledgeHome) []string {
	seen := map[string]bool{}
	roots := make([]string, 0, len(resolved))
	for _, root := range resolved {
		if root.scope != nil && (root.scope.HomeProjectID != source.HomeProjectID || root.scope.HomeLocatorID != source.HomeLocatorID) {
			continue
		}
		if !seen[root.bare] {
			seen[root.bare] = true
			roots = append(roots, root.bare)
		}
	}
	return roots
}

// refinementRootCoverage counts, per requested root form, the edges whose
// source lies inside that root's effective scope.
func refinementRootCoverage(edges []KnowledgeRefinementEdge, resolved []refinementResolvedRoot) map[string]int {
	counts := map[string]int{}
	for _, edge := range edges {
		for _, root := range resolved {
			if root.bare != edge.RootID {
				continue
			}
			if root.scope != nil && (root.scope.HomeProjectID != edge.SourceProjectID || root.scope.HomeLocatorID != edge.SourceLocatorID) {
				continue
			}
			counts[root.original]++
		}
	}
	return counts
}

// refinementContextEdges reads every qualifying authored one-hop edge in
// bounded statements per source: same-home edges, cross-source outgoing
// edges, and one endpoint-identity resolution. No per-edge or per-root
// application fan-out. Cross-source endpoints are inspected against the
// requested set: outside-set, missing-projection, and ambiguous identities
// become named omissions, never silent picks.
func refinementContextEdges(ctx context.Context, q queryer, sources []KnowledgeHome, resolved []refinementResolvedRoot, omissions *refinementOmissions) ([]KnowledgeRefinementEdge, error) {
	edges := make([]KnowledgeRefinementEdge, 0)
	crossLookups := map[string]bool{}
	markOmission := omissions.mark
	for _, source := range sources {
		sourceRoots := refinementRootsForSource(resolved, source)
		if len(sourceRoots) == 0 {
			continue
		}
		rows, err := q.QueryContext(ctx, `
SELECT r.target_law_id AS root, 'incoming' AS direction, r.kind, r.source_law_id AS endpoint,
       ls.kind, ls.status, ls.title, ls.path, ls.content_hash, r.scanned_commit_oid
FROM law_relations r
LEFT JOIN law_subjects ls ON ls.home_project_id=r.home_project_id AND ls.home_locator_id=r.home_locator_id AND ls.law_id=r.source_law_id
WHERE r.home_project_id=? AND r.home_locator_id=? AND r.kind='refines' AND r.target_law_id IN (`+placeholdersFor(sourceRoots)+`)
UNION ALL
SELECT r.source_law_id AS root, 'outgoing' AS direction, r.kind, r.target_law_id AS endpoint,
       ls.kind, ls.status, ls.title, ls.path, ls.content_hash, r.scanned_commit_oid
FROM law_relations r
LEFT JOIN law_subjects ls ON ls.home_project_id=r.home_project_id AND ls.home_locator_id=r.home_locator_id AND ls.law_id=r.target_law_id
WHERE r.home_project_id=? AND r.home_locator_id=? AND r.source_law_id IN (`+placeholdersFor(sourceRoots)+`)
ORDER BY 3,2,4,1`, append(append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(sourceRoots)...), append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(sourceRoots)...)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot read the authored law relations of source "+source.HomeProjectID, true, "retry once the database is readable", err)
		}
		if err := scanRefinementEdges(rows, source, &edges, markOmission); err != nil {
			return nil, err
		}
		crossRows, err := q.QueryContext(ctx, `
SELECT r.source_law_id AS root, 'outgoing' AS direction, r.kind, r.target_project_id, r.target_law_id, r.scanned_commit_oid
FROM law_cross_source_relations r
WHERE r.home_project_id=? AND r.home_locator_id=? AND r.source_law_id IN (`+placeholdersFor(sourceRoots)+`)
ORDER BY 3,4,5,1`, append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(sourceRoots)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot read the authored cross-source law relations of source "+source.HomeProjectID, true, "retry once the database is readable", err)
		}
		if err := scanRefinementCrossEdges(crossRows, source, &edges, crossLookups); err != nil {
			return nil, err
		}
	}
	if len(crossLookups) == 0 {
		return edges, nil
	}
	keys := make([]string, 0, len(crossLookups))
	for key := range crossLookups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Structured endpoint lookup: group the distinct endpoint keys by their
	// source Project and law ID, then read each Project's candidates with an
	// indexed home_project_id equality plus an IN list. The earlier
	// concatenated home_project_id || '/' || law_id IN (...) form matched no
	// index column, so SQLite could only scan law_subjects whole; the
	// structured equality drives the law_subjects_lookup prefix, and the
	// statement count stays tied to distinct endpoint Projects instead of to
	// roots or edges. Rows outside the requested set are discarded here, so
	// a foreign source can neither answer for the set nor overwrite an
	// endpoint identity.
	endpointLawsByProject := map[string][]string{}
	endpointSeen := map[string]bool{}
	for _, key := range keys {
		project, lawID, found := strings.Cut(key, "/")
		if !found {
			continue
		}
		pair := project + "\x00" + lawID
		if endpointSeen[pair] {
			continue
		}
		endpointSeen[pair] = true
		endpointLawsByProject[project] = append(endpointLawsByProject[project], lawID)
	}
	endpointProjects := make([]string, 0, len(endpointLawsByProject))
	for project := range endpointLawsByProject {
		endpointProjects = append(endpointProjects, project)
	}
	sort.Strings(endpointProjects)
	setMembership := map[string]bool{}
	setProjects := map[string]bool{}
	for _, source := range sources {
		setMembership[source.HomeProjectID+"\x00"+source.HomeLocatorID] = true
		setProjects[source.HomeProjectID] = true
	}
	type endpointSubject struct {
		locator, kind, status, title, path, hash string
	}
	subjects := map[string][]endpointSubject{}
	for _, project := range endpointProjects {
		laws := endpointLawsByProject[project]
		rows, err := q.QueryContext(ctx, `SELECT home_project_id, home_locator_id, law_id, kind, status, title, path, content_hash FROM law_subjects WHERE home_project_id=? AND law_id IN (`+placeholdersFor(laws)+`)`, append([]any{project}, stringArgs(laws)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot resolve amendment-context endpoint identities", true, "retry once the database is readable", err)
		}
		for rows.Next() {
			var rowProject, locator, lawID, kind, status, title, path, hash string
			if err := rows.Scan(&rowProject, &locator, &lawID, &kind, &status, &title, &path, &hash); err != nil {
				rows.Close()
				return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an amendment-context endpoint identity", true, "retry once the database is readable", err)
			}
			if !setMembership[rowProject+"\x00"+locator] {
				continue
			}
			subjects[rowProject+"/"+lawID] = append(subjects[rowProject+"/"+lawID], endpointSubject{locator: locator, kind: kind, status: status, title: title, path: path, hash: hash})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot finish reading amendment-context endpoint identities", true, "retry once the database is readable", err)
		}
		rows.Close()
	}
	// Enrichment resolves each distinct endpoint key once, then applies the
	// resolved identity to every edge sharing it: two roots relating to the
	// same cross-source endpoint both carry locator/title/status/hash.
	resolvedSubjects := map[string]endpointSubject{}
	for _, key := range keys {
		candidates := subjects[key]
		switch {
		case !setProjects[strings.SplitN(key, "/", 2)[0]]:
			markOmission("endpoint_source_not_in_set:" + key)
		case len(candidates) == 0:
			markOmission("endpoint_projection_missing:" + key)
		case len(candidates) > 1:
			markOmission("endpoint_identity_ambiguous:" + key)
		default:
			resolvedSubjects[key] = candidates[0]
		}
	}
	for index := range edges {
		edge := &edges[index]
		if edge.EndpointLocatorID != "" {
			continue
		}
		subject, ok := resolvedSubjects[edge.EndpointProjectID+"/"+edge.EndpointLawID]
		if !ok {
			continue
		}
		edge.EndpointLocatorID = subject.locator
		edge.EndpointKind = subject.kind
		edge.EndpointStatus = subject.status
		edge.EndpointTitle = subject.title
		edge.EndpointPath = subject.path
		edge.EndpointContentHash = subject.hash
	}
	return edges, nil
}

// scanRefinementEdges decodes the same-home page rows. An incoming edge
// qualifies only when its endpoint's whole-record status is accepted; when
// the endpoint projection is missing entirely the edge stays on the page as
// an inspected omission instead of vanishing, because a hidden authored
// relation would read as an authoritative no-amendments graph.
func scanRefinementEdges(rows *sql.Rows, source KnowledgeHome, edges *[]KnowledgeRefinementEdge, markOmission func(string)) error {
	defer rows.Close()
	for rows.Next() {
		var root, direction, kind, endpoint, commit string
		var endpointKind, endpointStatus, endpointTitle, endpointPath, endpointHash sql.NullString
		if err := rows.Scan(&root, &direction, &kind, &endpoint, &endpointKind, &endpointStatus, &endpointTitle, &endpointPath, &endpointHash, &commit); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an authored law relation", true, "retry once the database is readable", err)
		}
		if !endpointStatus.Valid || endpointStatus.String == "" {
			markOmission("endpoint_projection_missing:" + source.HomeProjectID + "/" + source.HomeLocatorID + "/" + endpoint)
			if direction == "incoming" {
				// Acceptance cannot be proven without the projection; the
				// omission carries the incompleteness, and the edge keeps
				// its authored shape with no endpoint status claim.
				*edges = append(*edges, KnowledgeRefinementEdge{
					RootID: root, Direction: direction, Kind: kind,
					EndpointProjectID: source.HomeProjectID, EndpointLawID: endpoint,
					SourceProjectID: source.HomeProjectID, SourceLocatorID: source.HomeLocatorID,
					ScannedCommitOID: commit,
				})
				continue
			}
		} else if direction == "incoming" && endpointStatus.String != "accepted" {
			continue
		}
		*edges = append(*edges, KnowledgeRefinementEdge{
			RootID: root, Direction: direction, Kind: kind,
			EndpointProjectID: source.HomeProjectID, EndpointLocatorID: source.HomeLocatorID, EndpointLawID: endpoint,
			EndpointKind: endpointKind.String, EndpointStatus: endpointStatus.String, EndpointTitle: endpointTitle.String, EndpointPath: endpointPath.String,
			EndpointContentHash: endpointHash.String,
			SourceProjectID:     source.HomeProjectID, SourceLocatorID: source.HomeLocatorID, ScannedCommitOID: commit,
		})
	}
	return wrapFailureRows("PM1.Q10.amendment_context", "cannot finish reading authored law relations", rows.Err())
}

func scanRefinementCrossEdges(rows *sql.Rows, source KnowledgeHome, edges *[]KnowledgeRefinementEdge, crossLookups map[string]bool) error {
	defer rows.Close()
	for rows.Next() {
		var root, direction, kind, targetProject, targetLaw, commit string
		if err := rows.Scan(&root, &direction, &kind, &targetProject, &targetLaw, &commit); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an authored cross-source law relation", true, "retry once the database is readable", err)
		}
		*edges = append(*edges, KnowledgeRefinementEdge{
			RootID: root, Direction: direction, Kind: kind,
			EndpointProjectID: targetProject, EndpointLawID: targetLaw,
			SourceProjectID: source.HomeProjectID, SourceLocatorID: source.HomeLocatorID, ScannedCommitOID: commit,
		})
		crossLookups[targetProject+"/"+targetLaw] = true
	}
	return wrapFailureRows("PM1.Q10.amendment_context", "cannot finish reading authored cross-source law relations", rows.Err())
}

// placeholdersFor returns a comma-separated placeholder fragment for an IN
// list of the given length. Callers keep every value parameter-bound.
func placeholdersFor(values []string) string {
	if len(values) == 0 {
		return "''"
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
}

func wrapFailureRows(op, detail string, err error) error {
	if err != nil {
		return wrapFailure(KindUnavailable, op, detail, true, "retry once the database is readable", err)
	}
	return nil
}
