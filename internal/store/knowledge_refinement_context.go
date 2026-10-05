package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
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
	// SeededOmissions names source-set incompleteness the caller already
	// knows about (an unresolved current source set on a degraded read).
	// Seeded omissions mark the whole context degraded; they are never an
	// authoritative no-amendments claim.
	SeededOmissions []string
}

// KnowledgeRefinementEdge is one authored one-hop relation. Direction
// "incoming" marks a direct accepted refinement of the root
// (kind='refines', endpoint is the refining law, whole-record status
// accepted). Direction "outgoing" marks an explicitly declared relation
// from the root, same-home or cross-source, with a qualified endpoint.
//
// RootScopeProjectID is the Project whose source the edge's root lives in:
// the declaring source for same-home and outgoing relations, the declared
// target Project for an incoming cross-source relation. RelationClass is
// "same_home" or "cross_source". Together with the endpoint and source
// identity they form the deterministic total order the page and its cursor
// key on, so every ordering component is observable on the wire.
type KnowledgeRefinementEdge struct {
	RootID              string `json:"root_id"`
	RootScopeProjectID  string `json:"root_scope_project_id"`
	Direction           string `json:"direction"`
	Kind                string `json:"kind"`
	RelationClass       string `json:"relation_class"`
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

// refinementOrderingKeys names the deterministic total edge order, in
// order: kind, direction, endpoint Project and law, root, the root's scope
// Project, the relation class, then the declaring source. Every component
// is carried on the wire, and the per-source page reads order and resume on
// the same key, so no two distinct authored edges ever share an ordering
// key and a continuation can neither lose nor repeat an edge.
var refinementOrderingKeys = []string{"kind", "direction", "endpoint_project_id", "endpoint_law_id", "root_id", "root_scope_project_id", "relation_class", "source_project_id", "source_locator_id"}

// refinementEdgeKey is the deterministic total ordering key.
func refinementEdgeKey(edge KnowledgeRefinementEdge) [9]string {
	return [9]string{edge.Kind, edge.Direction, edge.EndpointProjectID, edge.EndpointLawID, edge.RootID, edge.RootScopeProjectID, edge.RelationClass, edge.SourceProjectID, edge.SourceLocatorID}
}

func refinementEdgeLess(a, b KnowledgeRefinementEdge) bool {
	ak, bk := refinementEdgeKey(a), refinementEdgeKey(b)
	for i := range ak {
		if ak[i] != bk[i] {
			return ak[i] < bk[i]
		}
	}
	return false
}

// refinementCursor binds the page to the roots, the source-set digest, the
// scanned commit and content identity of every participating source, the
// qualifying-edge snapshot digest (per-source root counts), and the last
// ordering key. Any authored relation or source-commit change between pages
// refuses continuation instead of splicing two snapshots. The cursor carries
// no cumulative completeness state: incomplete roots are derived inside every
// read from the snapshot and the keyset position, so a rewritten cursor can
// never suppress an incomplete-root marker.
type refinementCursor struct {
	Version             int      `json:"version"`
	Roots               []string `json:"roots"`
	SourcesDigest       string   `json:"sources_digest"`
	SnapshotDigest      string   `json:"snapshot_digest"`
	LastKind            string   `json:"last_kind"`
	LastDirection       string   `json:"last_direction"`
	LastEndpointProject string   `json:"last_endpoint_project"`
	LastEndpointLaw     string   `json:"last_endpoint_law"`
	LastRoot            string   `json:"last_root"`
	LastRootScope       string   `json:"last_root_scope"`
	LastRelationClass   string   `json:"last_relation_class"`
	LastSourceProject   string   `json:"last_source_project"`
	LastSourceLocator   string   `json:"last_source_locator"`
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
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.Version != 1 || !equalStrings(cursor.Roots, roots) || cursor.SourcesDigest != sourcesDigest || cursor.SnapshotDigest != snapshotDigest || refinementCursorKeyZero(cursor) {
		return refinementCursor{}, newFailure(KindInvalidCursor, "PM1.Q10.amendment_context", "amendment-context cursor does not match the requested roots, source set, or relation snapshot", false, "restart the amendment-context query at the current snapshot")
	}
	return cursor, nil
}

func refinementCursorKeyZero(cursor refinementCursor) bool {
	return cursor.LastKind == "" || cursor.LastDirection == "" || cursor.LastEndpointProject == "" || cursor.LastEndpointLaw == "" || cursor.LastRoot == "" || cursor.LastRootScope == "" || cursor.LastRelationClass == "" || cursor.LastSourceProject == "" || cursor.LastSourceLocator == ""
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
	// A caller-named source-set omission (an unresolved current source set
	// on a degraded read) marks the whole context degraded before any
	// projection is read.
	if len(req.SeededOmissions) > 0 {
		verification.degraded = true
		verification.omissions = append(verification.omissions, req.SeededOmissions...)
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

// refinementSourceDrift names one participating source whose read-snapshot
// projection identity no longer matches the proof the pool verifier bound.
type refinementSourceDrift struct {
	label   string
	proof   string
	current string
}

// refinementWatermarkDrift reads, inside the caller's read snapshot, the
// projection identity of every participating source — authoritative or
// degraded — and checks each authoritative source's proof against it. Every
// identity feeds the cursor's snapshot digest, so a projection refresh of any
// participating source (including one the verifier could only mark degraded)
// changes the digest and refuses an outstanding continuation even when the
// relation counts are unchanged. A projection refresh that commits between
// pool verification and this read snapshot must never join the old verified
// proof to the refreshed relation rows: the per-home rebuild rewrites
// law_relations, law_subjects, and the watermark in one transaction, so a
// committed state always equals the watermark. Git and content probes stay
// on the pool connection (CD-0195 D2); this reads only the watermark
// projection through the caller's queryer, so an empty graph gets the same
// identity check as a populated one. All identities are collected even when
// an earlier source drifts; the drifts list names every drifted source.
func refinementWatermarkDrift(ctx context.Context, q queryer, sources []KnowledgeHome, verification refinementSourceVerification) ([]refinementSourceDrift, map[string][3]string) {
	verified := refinementVerifiedWatermarks(verification)
	drifts := make([]refinementSourceDrift, 0)
	identities := map[string][3]string{}
	for _, source := range sources {
		label := source.HomeProjectID + "/" + source.HomeLocatorID
		var commit, content string
		var complete bool
		err := q.QueryRowContext(ctx, `SELECT scanned_commit_oid, scanned_content_digest, complete FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, source.HomeProjectID, source.HomeLocatorID, source.HeadRef).Scan(&commit, &content, &complete)
		switch {
		case err == sql.ErrNoRows:
			identities[label] = [3]string{"", "", "watermark_absent"}
		case err != nil:
			// An unreadable watermark cannot establish identity either; the
			// read records the unknown identity so a later readable state
			// still changes the digest, and refuses rather than guessing.
			identities[label] = [3]string{"", "", "watermark_unreadable"}
		default:
			identities[label] = [3]string{commit, content, strconv.FormatBool(complete)}
		}
		proof, authoritative := verified[label]
		if !authoritative {
			continue
		}
		switch {
		case err == sql.ErrNoRows:
			drifts = append(drifts, refinementSourceDrift{label, proof, ""})
		case err != nil:
			drifts = append(drifts, refinementSourceDrift{label, proof, "watermark_unreadable"})
		case !complete:
			// A committed watermark that matches the proof commit but is
			// marked incomplete is the same splice: the verified identity
			// cannot ride a projection that declares itself unfinished.
			drifts = append(drifts, refinementSourceDrift{label, proof, "incomplete_projection@" + commit})
		case commit != proof:
			drifts = append(drifts, refinementSourceDrift{label, proof, commit})
		}
	}
	return drifts, identities
}

// refinementRefuseSnapshotDrift is the shared drift boundary: strict
// contextual reads refuse, degraded-allowed reads name the omission and
// keep the page away from an authoritative claim.
func refinementRefuseSnapshotDrift(label, proof, current string) error {
	return newFailure(KindStaleContext, "PM1.Q10.amendment_context", "the verified amendment-context source snapshot changed before the read: "+label+" was verified at "+proof+" but the read snapshot holds "+current, true, "re-verify the amendment-context sources and retry the read")
}

// refinementCountRow is one per-source qualifying-edge count group. The
// same rows feed the cursor's snapshot digest and the incomplete-root
// accounting, so page selection never reads the unbounded edge population
// into application memory.
type refinementCountRow struct {
	class  string // same_home | cross_outgoing | cross_incoming
	source string // declaring source label
	a      string // root id, or the target Project for incoming cross edges
	b      string // direction or kind for same_home, root id for incoming cross edges
	c      string // kind for same_home groups, otherwise empty
	count  int
}

// refinementSnapshotDigest binds the qualifying-edge snapshot through
// bounded aggregates: every participating source's scanned commit, content
// identity, and projection completeness plus every per-source qualifying
// count group. A changed relation population, an added or removed edge, a
// watermark that appears, disappears, or loses completeness, or a refreshed
// projection changes the digest; application memory stays tied to the group
// count, never to the edge count.
func refinementSnapshotDigest(identities map[string][3]string, counts []refinementCountRow) string {
	labels := make([]string, 0, len(identities))
	for label := range identities {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	hash := sha256.New()
	for _, label := range labels {
		identity := identities[label]
		for _, part := range []string{"source", label, identity[0], identity[1], identity[2]} {
			hash.Write([]byte(part))
			hash.Write([]byte("\x00"))
		}
	}
	ordered := make([]refinementCountRow, len(counts))
	copy(ordered, counts)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].class != ordered[j].class {
			return ordered[i].class < ordered[j].class
		}
		if ordered[i].source != ordered[j].source {
			return ordered[i].source < ordered[j].source
		}
		if ordered[i].a != ordered[j].a {
			return ordered[i].a < ordered[j].a
		}
		if ordered[i].b != ordered[j].b {
			return ordered[i].b < ordered[j].b
		}
		return ordered[i].c < ordered[j].c
	})
	for _, row := range ordered {
		for _, part := range []string{row.class, row.source, row.a, row.b, row.c} {
			hash.Write([]byte(part))
			hash.Write([]byte("\x00"))
		}
		hash.Write([]byte(strconv.Itoa(row.count)))
		hash.Write([]byte("\x00"))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// queryKnowledgeRefinementContext is the tx-scoped core: every database
// read goes through the caller's queryer, so a caller holding an open
// transaction reuses its own handle under the single-connection invariant
// instead of parking on the pool. The caller must supply one coherent read
// snapshot (a read transaction): the watermark identity check, the bounded
// page reads, the count aggregates, the endpoint reads, and the cursor
// snapshot then resolve against the same snapshot. The *sql.DB wrapper
// owns git-backed source verification; the core owns root qualification,
// the bounded indexed page reads with keyset continuation, bounded
// endpoint enrichment, and the cursor.
func queryKnowledgeRefinementContext(ctx context.Context, q queryer, req KnowledgeRefinementContextRequest, sources []KnowledgeHome, roots []string, limit int, verification refinementSourceVerification) (KnowledgeRefinementContextResult, error) {
	var out KnowledgeRefinementContextResult
	drifts, identities := refinementWatermarkDrift(ctx, q, sources, verification)
	if len(drifts) > 0 {
		if !req.AllowDegraded {
			first := drifts[0]
			return out, refinementRefuseSnapshotDrift(first.label, first.proof, first.current)
		}
		for _, drift := range drifts {
			verification.degraded = true
			verification.omissions = append(verification.omissions, "source_snapshot_drift:"+drift.label+":"+drift.proof+"->"+drift.current)
		}
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
	counts, err := refinementContextCounts(ctx, q, sources, resolved, nil)
	if err != nil {
		return out, err
	}
	snapshot := refinementSnapshotDigest(identities, counts)
	var resume *refinementCursor
	if req.Cursor != "" {
		decoded, err := decodeRefinementCursor(req.Cursor, roots, digest, snapshot)
		if err != nil {
			return out, err
		}
		resume = &decoded
	}
	// Relation rows must carry the same scanned identity the proof bound: a
	// row refreshed inside the read snapshot cannot ride an older verified
	// watermark, and a row older than the watermark would equally splice two
	// scans. Both directions refuse or degrade through the same boundary.
	edges, err := refinementContextPage(ctx, q, sources, resolved, limit, resume, omissions)
	if err != nil {
		return out, err
	}
	verified := refinementVerifiedWatermarks(verification)
	for _, edge := range edges {
		if edgeProof, authoritative := verified[edge.SourceProjectID+"/"+edge.SourceLocatorID]; authoritative && edge.ScannedCommitOID != edgeProof {
			if !req.AllowDegraded {
				return out, refinementRefuseSnapshotDrift(edge.SourceProjectID+"/"+edge.SourceLocatorID, edgeProof, edge.ScannedCommitOID)
			}
			omissions.mark("source_snapshot_drift:" + edge.SourceProjectID + "/" + edge.SourceLocatorID + ":" + edgeProof + "->" + edge.ScannedCommitOID)
			verification.degraded = true
		}
	}
	hasMore := len(edges) > limit
	paged := edges
	if hasMore {
		paged = paged[:limit]
	}
	// Endpoint enrichment is bounded to the selected page: at most limit
	// edges reach the lookup, so the distinct endpoint keys, the lookup
	// statements, and the enriched metadata all stay inside the page bound.
	setProjects := map[string]bool{}
	for _, source := range sources {
		setProjects[source.HomeProjectID] = true
	}
	if err := refinementEnrichPageEndpoints(ctx, q, sources, setProjects, paged, omissions.mark); err != nil {
		return out, err
	}
	// Incomplete-root accounting derives from the read snapshot and the
	// keyset position alone: the same bounded count family, keyed past this
	// page's last ordering key, names every root whose qualifying edges
	// still remain. No cumulative count rides the cursor, so a rewritten
	// cursor can never present an incomplete root as complete, and the
	// count family runs on every page at the same source-scaled statement
	// count whether or not the page was cut.
	var afterKey []string
	if len(paged) > 0 {
		lastKey := refinementEdgeKey(paged[len(paged)-1])
		afterKey = lastKey[:]
	}
	remainingRows, err := refinementContextCounts(ctx, q, sources, resolved, afterKey)
	if err != nil {
		return out, err
	}
	remaining := refinementRootTotals(resolved, remainingRows)
	incomplete := make([]string, 0)
	for _, root := range resolved {
		if remaining[root.original] > 0 {
			incomplete = append(incomplete, root.original)
		}
	}
	var next *string
	if hasMore {
		last := paged[len(paged)-1]
		key := refinementEdgeKey(last)
		encoded, err := encodeRefinementCursor(refinementCursor{
			Version: 1, Roots: roots, SourcesDigest: digest, SnapshotDigest: snapshot,
			LastKind: key[0], LastDirection: key[1], LastEndpointProject: key[2], LastEndpointLaw: key[3], LastRoot: key[4], LastRootScope: key[5], LastRelationClass: key[6], LastSourceProject: key[7], LastSourceLocator: key[8],
		})
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
	meta := ResultMeta{QueryID: "PM1.Q10.amendment_context", ContractVersion: queryContractVersion, ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: authority, Freshness: Freshness{ObservedAt: now.Format(time.RFC3339Nano), Age: 0, Stale: false}, OrderingKeys: refinementOrderingKeys, Omissions: omissions.values, Warnings: []string{"one_hop_authored_relations_only", "no_inferred_or_transitive_edges"}}
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
		if err := wrapFailureRows("cannot finish reading the law subjects of source "+source.HomeProjectID, rows.Err()); err != nil {
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

// refinementRootTotals derives each requested root form's qualifying-edge
// count from per-source count groups: same-home and outgoing cross-source
// groups attribute to the root forms whose scope covers the declaring
// source, incoming cross-source groups to the root forms whose scope
// covers the declared target Project.
func refinementRootTotals(resolved []refinementResolvedRoot, counts []refinementCountRow) map[string]int {
	totals := map[string]int{}
	coversSource := func(root refinementResolvedRoot, label string, projectID, locatorID string) bool {
		if root.bare != label {
			return false
		}
		if root.scope == nil {
			return true
		}
		return root.scope.HomeProjectID == projectID && root.scope.HomeLocatorID == locatorID
	}
	for _, row := range counts {
		var projectID, locatorID string
		if row.source != "" {
			projectID, locatorID, _ = strings.Cut(row.source, "/")
		}
		switch row.class {
		case "same_home", "cross_outgoing":
			for _, root := range resolved {
				if coversSource(root, row.a, projectID, locatorID) {
					totals[root.original] += row.count
				}
			}
		case "cross_incoming":
			for _, root := range resolved {
				if root.bare != row.b {
					continue
				}
				if root.scope == nil || root.scope.HomeProjectID == row.a {
					totals[root.original] += row.count
				}
			}
		}
	}
	return totals
}

// refinementAllRootIDs lists the distinct bare root IDs any source's SQL
// reads may match, across every requested root form: an incoming
// cross-source relation is declared by the endpoint's source but targets
// the root's source, so the declaring source cannot be narrowed by root
// scope the way the same-home reads are.
func refinementAllRootIDs(resolved []refinementResolvedRoot) []string {
	seen := map[string]bool{}
	roots := make([]string, 0, len(resolved))
	for _, root := range resolved {
		if !seen[root.bare] {
			seen[root.bare] = true
			roots = append(roots, root.bare)
		}
	}
	return roots
}

// refinementResumeKey renders the cursor's last ordering key as the
// nine-component comparison tuple every page statement resumes after.
func refinementResumeKey(cursor *refinementCursor) []string {
	if cursor == nil {
		return nil
	}
	return []string{cursor.LastKind, cursor.LastDirection, cursor.LastEndpointProject, cursor.LastEndpointLaw, cursor.LastRoot, cursor.LastRootScope, cursor.LastRelationClass, cursor.LastSourceProject, cursor.LastSourceLocator}
}

// refinementKeysetPredicate emits the row-value resume predicate for one
// page branch. constant holds the branch's fixed key components with their
// positions in the total order; variable holds the SQL expressions for the
// remaining positions in ascending order. The composed row value mirrors
// the total order exactly, so the comparison decides the same order SQL
// orders by.
func refinementKeysetPredicate(constant []refinementKeyComponent, variable []string, resume []string) (string, []any) {
	if resume == nil {
		return "", nil
	}
	row := make([]string, 0, len(constant)+len(variable))
	args := make([]any, 0, len(constant)+len(resume))
	ci, vi := 0, 0
	for position := 0; position < 9; position++ {
		if ci < len(constant) && constant[ci].order == position {
			row = append(row, "?")
			args = append(args, constant[ci].value)
			ci++
			continue
		}
		row = append(row, variable[vi])
		vi++
	}
	predicate := "(" + strings.Join(row, ",") + ") > (" + strings.TrimSuffix(strings.Repeat("?,", len(resume)), ",") + ")"
	return predicate, append(args, stringArgs(resume)...)
}

// refinementKeyComponent is one constant component of a page branch's
// ordering key: its position in the total order and its bound value.
type refinementKeyComponent struct {
	order int
	value string
}

// refinementIncomingCoverage emits the incoming cross-source coverage
// filter: a bare root matches any set Project's target, a qualified root
// matches only its own resolved scope's Project (CD-0200 D4).
func refinementIncomingCoverage(resolved []refinementResolvedRoot, sources []KnowledgeHome) (string, []any) {
	setProjects := map[string]bool{}
	for _, source := range sources {
		setProjects[source.HomeProjectID] = true
	}
	bareRoots := map[string]bool{}
	for _, root := range resolved {
		if root.scope == nil {
			bareRoots[root.bare] = true
		}
	}
	disjuncts := make([]string, 0, 2)
	args := make([]any, 0)
	if len(bareRoots) > 0 && len(setProjects) > 0 {
		projects := make([]string, 0, len(setProjects))
		for project := range setProjects {
			projects = append(projects, project)
		}
		sort.Strings(projects)
		roots := make([]string, 0, len(bareRoots))
		for root := range bareRoots {
			roots = append(roots, root)
		}
		sort.Strings(roots)
		disjuncts = append(disjuncts, "r.target_law_id IN ("+placeholdersFor(roots)+") AND r.target_project_id IN ("+placeholdersFor(projects)+")")
		args = append(args, stringArgs(roots)...)
		args = append(args, stringArgs(projects)...)
	}
	scopes := map[string][]string{}
	for _, root := range resolved {
		if root.scope != nil {
			scopes[root.scope.HomeProjectID] = append(scopes[root.scope.HomeProjectID], root.bare)
		}
	}
	scopeProjects := make([]string, 0, len(scopes))
	for project := range scopes {
		scopeProjects = append(scopeProjects, project)
	}
	sort.Strings(scopeProjects)
	for _, project := range scopeProjects {
		laws := orderedStrings(scopes[project])
		disjuncts = append(disjuncts, "r.target_law_id IN ("+placeholdersFor(laws)+") AND r.target_project_id = ?")
		args = append(args, stringArgs(laws)...)
		args = append(args, project)
	}
	if len(disjuncts) == 0 {
		return "0", args
	}
	return "(" + strings.Join(disjuncts, " OR ") + ")", args
}

// refinementContextCounts reads every per-source qualifying count group in
// bounded statements: one same-home union group, one outgoing cross-source
// group, and one incoming cross-source group per source. With a nil keyset
// the groups cover the whole snapshot and feed the snapshot digest; with an
// ordering key they cover only the edges past that key and feed the
// incomplete-root accounting, so completeness derives from the snapshot and
// the keyset position instead of any client-carried cumulative count. No
// per-edge or per-root application fan-out exists.
func refinementContextCounts(ctx context.Context, q queryer, sources []KnowledgeHome, resolved []refinementResolvedRoot, after []string) ([]refinementCountRow, error) {
	counts := make([]refinementCountRow, 0)
	allRoots := refinementAllRootIDs(resolved)
	coverageSQL, coverageArgs := refinementIncomingCoverage(resolved, sources)
	for _, source := range sources {
		label := source.HomeProjectID + "/" + source.HomeLocatorID
		sourceRoots := refinementRootsForSource(resolved, source)
		if len(sourceRoots) > 0 {
			incomingCountPredicate, incomingCountArgs := refinementKeysetPredicate(
				[]refinementKeyComponent{{0, "refines"}, {1, "incoming"}, {2, source.HomeProjectID}, {5, source.HomeProjectID}, {6, "same_home"}, {7, source.HomeProjectID}, {8, source.HomeLocatorID}},
				[]string{"r.source_law_id", "r.target_law_id"}, after)
			outgoingCountPredicate, outgoingCountArgs := refinementKeysetPredicate(
				[]refinementKeyComponent{{1, "outgoing"}, {2, source.HomeProjectID}, {5, source.HomeProjectID}, {6, "same_home"}, {7, source.HomeProjectID}, {8, source.HomeLocatorID}},
				[]string{"r.kind", "r.target_law_id", "r.source_law_id"}, after)
			branchArgs := func(predicateArgs []any) []any {
				return append(append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(sourceRoots)...), predicateArgs...)
			}
			rows, err := q.QueryContext(ctx, `
SELECT root_id, direction, kind, COUNT(*) FROM (
  SELECT r.target_law_id AS root_id, 'incoming' AS direction, 'refines' AS kind
  FROM law_relations r
  LEFT JOIN law_subjects ls ON ls.home_project_id=r.home_project_id AND ls.home_locator_id=r.home_locator_id AND ls.law_id=r.source_law_id
  WHERE r.home_project_id=? AND r.home_locator_id=? AND r.kind='refines' AND r.target_law_id IN (`+placeholdersFor(sourceRoots)+`) AND (ls.status IS NULL OR ls.status='accepted')`+predicateSQL(incomingCountPredicate)+`
  UNION ALL
  SELECT r.source_law_id AS root_id, 'outgoing' AS direction, r.kind AS kind
  FROM law_relations r
  WHERE r.home_project_id=? AND r.home_locator_id=? AND r.source_law_id IN (`+placeholdersFor(sourceRoots)+`)`+predicateSQL(outgoingCountPredicate)+`
) GROUP BY root_id, direction, kind`, append(branchArgs(incomingCountArgs), branchArgs(outgoingCountArgs)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
			if err != nil {
				return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot count the authored law relations of source "+source.HomeProjectID, true, "retry once the database is readable", err)
			}
			for rows.Next() {
				var rootID, direction, kind string
				var count int
				if err := rows.Scan(&rootID, &direction, &kind, &count); err != nil {
					rows.Close()
					return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an authored law relation count", true, "retry once the database is readable", err)
				}
				counts = append(counts, refinementCountRow{class: "same_home", source: label, a: rootID, b: direction, c: kind, count: count})
			}
			if err := wrapFailureRows("cannot finish counting authored law relations", rows.Err()); err != nil {
				rows.Close()
				return nil, err
			}
			rows.Close()
			outgoingCrossCountPredicate, outgoingCrossCountArgs := refinementKeysetPredicate(
				[]refinementKeyComponent{{1, "outgoing"}, {5, source.HomeProjectID}, {6, "cross_source"}, {7, source.HomeProjectID}, {8, source.HomeLocatorID}},
				[]string{"r.kind", "r.target_project_id", "r.target_law_id", "r.source_law_id"}, after)
			crossArgs := append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(sourceRoots)...)
			crossArgs = append(crossArgs, outgoingCrossCountArgs...)
			crossRows, err := q.QueryContext(ctx, `
SELECT r.source_law_id, r.kind, COUNT(*)
FROM law_cross_source_relations r
WHERE r.home_project_id=? AND r.home_locator_id=? AND r.source_law_id IN (`+placeholdersFor(sourceRoots)+`)`+predicateSQL(outgoingCrossCountPredicate)+`
GROUP BY r.source_law_id, r.kind`, crossArgs...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
			if err != nil {
				return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot count the authored cross-source law relations of source "+source.HomeProjectID, true, "retry once the database is readable", err)
			}
			for crossRows.Next() {
				var rootID, kind string
				var count int
				if err := crossRows.Scan(&rootID, &kind, &count); err != nil {
					crossRows.Close()
					return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an authored cross-source relation count", true, "retry once the database is readable", err)
				}
				counts = append(counts, refinementCountRow{class: "cross_outgoing", source: label, a: rootID, b: kind, count: count})
			}
			if err := wrapFailureRows("cannot finish counting authored cross-source relations", crossRows.Err()); err != nil {
				crossRows.Close()
				return nil, err
			}
			crossRows.Close()
		}
		if len(allRoots) == 0 {
			continue
		}
		incomingCrossCountPredicate, incomingCrossCountArgs := refinementKeysetPredicate(
			[]refinementKeyComponent{{0, "refines"}, {1, "incoming"}, {2, source.HomeProjectID}, {6, "cross_source"}, {7, source.HomeProjectID}, {8, source.HomeLocatorID}},
			[]string{"r.source_law_id", "r.target_law_id", "r.target_project_id"}, after)
		incomingArgs := append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(allRoots)...)
		incomingArgs = append(incomingArgs, coverageArgs...)
		incomingArgs = append(incomingArgs, incomingCrossCountArgs...)
		incomingRows, err := q.QueryContext(ctx, `
SELECT r.target_project_id, r.target_law_id, COUNT(*)
FROM law_cross_source_relations r
LEFT JOIN law_subjects ls ON ls.home_project_id=r.home_project_id AND ls.home_locator_id=r.home_locator_id AND ls.law_id=r.source_law_id
WHERE r.home_project_id=? AND r.home_locator_id=? AND r.kind='refines' AND r.target_law_id IN (`+placeholdersFor(allRoots)+`) AND `+coverageSQL+` AND (ls.status IS NULL OR ls.status='accepted')`+predicateSQL(incomingCrossCountPredicate)+`
GROUP BY r.target_project_id, r.target_law_id`, incomingArgs...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot count the authored incoming cross-source refinements of source "+source.HomeProjectID, true, "retry once the database is readable", err)
		}
		for incomingRows.Next() {
			var targetProject, rootID string
			var count int
			if err := incomingRows.Scan(&targetProject, &rootID, &count); err != nil {
				incomingRows.Close()
				return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an authored incoming cross-source refinement count", true, "retry once the database is readable", err)
			}
			counts = append(counts, refinementCountRow{class: "cross_incoming", source: label, a: targetProject, b: rootID, count: count})
		}
		if err := wrapFailureRows("cannot finish counting authored incoming cross-source refinements", incomingRows.Err()); err != nil {
			incomingRows.Close()
			return nil, err
		}
		incomingRows.Close()
	}
	return counts, nil
}

// refinementContextPage reads one bounded page: each source's three page
// statements carry the keyset resume predicate, order by the total order's
// varying prefix, and LIMIT limit+1, so the merged candidate set stays
// bounded by branches times the limit while the global top-limit page is
// still exact. Endpoint enrichment runs only over the selected page, never
// over the unbounded qualifying population. Cross-source endpoints are
// inspected against the requested set: outside-set, missing-projection,
// and ambiguous identities become named omissions, never silent picks.
func refinementContextPage(ctx context.Context, q queryer, sources []KnowledgeHome, resolved []refinementResolvedRoot, limit int, resume *refinementCursor, omissions *refinementOmissions) ([]KnowledgeRefinementEdge, error) {
	edges := make([]KnowledgeRefinementEdge, 0)
	markOmission := omissions.mark
	allRoots := refinementAllRootIDs(resolved)
	resumeKey := refinementResumeKey(resume)
	pageLimit := limit + 1
	// Same-home and outgoing cross-source page statements key the root's
	// scope at the declaring source; the incoming cross-source statement
	// keys it at the declared target Project. The constant/variable split
	// below mirrors the total order exactly, so the row-value predicate
	// decides the same order SQL orders by and the merge sorts on.
	for _, source := range sources {
		sourceRoots := refinementRootsForSource(resolved, source)
		if len(sourceRoots) > 0 {
			incomingPredicate, incomingArgs := refinementKeysetPredicate(
				[]refinementKeyComponent{{0, "refines"}, {1, "incoming"}, {2, source.HomeProjectID}, {5, source.HomeProjectID}, {6, "same_home"}, {7, source.HomeProjectID}, {8, source.HomeLocatorID}},
				[]string{"r.source_law_id", "r.target_law_id"}, resumeKey)
			outgoingPredicate, outgoingArgs := refinementKeysetPredicate(
				[]refinementKeyComponent{{1, "outgoing"}, {2, source.HomeProjectID}, {5, source.HomeProjectID}, {6, "same_home"}, {7, source.HomeProjectID}, {8, source.HomeLocatorID}},
				[]string{"r.kind", "r.target_law_id", "r.source_law_id"}, resumeKey)
			args := make([]any, 0, 24+4*len(sourceRoots))
			args = append(args, source.HomeProjectID, source.HomeLocatorID)
			args = append(args, stringArgs(sourceRoots)...)
			args = append(args, incomingArgs...)
			args = append(args, source.HomeProjectID, source.HomeLocatorID)
			args = append(args, stringArgs(sourceRoots)...)
			args = append(args, outgoingArgs...)
			args = append(args, pageLimit)
			rows, err := q.QueryContext(ctx, `
SELECT kind, direction, endpoint_law, root_id, endpoint_kind, endpoint_status, endpoint_title, endpoint_path, endpoint_content_hash, scanned_commit FROM (
  SELECT 'refines' AS kind, 'incoming' AS direction, r.source_law_id AS endpoint_law, r.target_law_id AS root_id,
         ls.kind AS endpoint_kind, ls.status AS endpoint_status, ls.title AS endpoint_title, ls.path AS endpoint_path, ls.content_hash AS endpoint_content_hash, r.scanned_commit_oid AS scanned_commit
  FROM law_relations r
  LEFT JOIN law_subjects ls ON ls.home_project_id=r.home_project_id AND ls.home_locator_id=r.home_locator_id AND ls.law_id=r.source_law_id
  WHERE r.home_project_id=? AND r.home_locator_id=? AND r.kind='refines' AND r.target_law_id IN (`+placeholdersFor(sourceRoots)+`) AND (ls.status IS NULL OR ls.status='accepted')`+predicateSQL(incomingPredicate)+`
  UNION ALL
  SELECT r.kind AS kind, 'outgoing' AS direction, r.target_law_id AS endpoint_law, r.source_law_id AS root_id,
         ls.kind AS endpoint_kind, ls.status AS endpoint_status, ls.title AS endpoint_title, ls.path AS endpoint_path, ls.content_hash AS endpoint_content_hash, r.scanned_commit_oid AS scanned_commit
  FROM law_relations r
  LEFT JOIN law_subjects ls ON ls.home_project_id=r.home_project_id AND ls.home_locator_id=r.home_locator_id AND ls.law_id=r.target_law_id
  WHERE r.home_project_id=? AND r.home_locator_id=? AND r.source_law_id IN (`+placeholdersFor(sourceRoots)+`)`+predicateSQL(outgoingPredicate)+`
) ORDER BY kind, direction, endpoint_law, root_id LIMIT ?`, args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
			if err != nil {
				return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot read the authored law relations of source "+source.HomeProjectID, true, "retry once the database is readable", err)
			}
			if err := scanRefinementEdges(rows, source, &edges, markOmission); err != nil {
				return nil, err
			}
			outgoingCrossPredicate, outgoingCrossArgs := refinementKeysetPredicate(
				[]refinementKeyComponent{{1, "outgoing"}, {5, source.HomeProjectID}, {6, "cross_source"}, {7, source.HomeProjectID}, {8, source.HomeLocatorID}},
				[]string{"r.kind", "r.target_project_id", "r.target_law_id", "r.source_law_id"}, resumeKey)
			crossArgs := append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(sourceRoots)...)
			crossArgs = append(crossArgs, outgoingCrossArgs...)
			crossArgs = append(crossArgs, pageLimit)
			crossRows, err := q.QueryContext(ctx, `
SELECT r.kind AS kind, 'outgoing' AS direction, r.target_project_id AS endpoint_project, r.target_law_id AS endpoint_law, r.source_law_id AS root_id, r.scanned_commit_oid AS scanned_commit
FROM law_cross_source_relations r
WHERE r.home_project_id=? AND r.home_locator_id=? AND r.source_law_id IN (`+placeholdersFor(sourceRoots)+`)`+predicateSQL(outgoingCrossPredicate)+`
ORDER BY kind, endpoint_project, endpoint_law, root_id LIMIT ?`, crossArgs...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
			if err != nil {
				return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot read the authored cross-source law relations of source "+source.HomeProjectID, true, "retry once the database is readable", err)
			}
			if err := scanRefinementCrossEdges(crossRows, source, &edges); err != nil {
				return nil, err
			}
		}
		if len(allRoots) == 0 {
			continue
		}
		// Incoming cross-source refinements: this source declares
		// kind='refines' toward a root that lives in another set source.
		// The declaring projection owns the row, so the read stays inside
		// this source; the coverage filter decides which root form the
		// edge answers.
		incomingCrossPredicate, incomingCrossArgs := refinementKeysetPredicate(
			[]refinementKeyComponent{{0, "refines"}, {1, "incoming"}, {2, source.HomeProjectID}, {6, "cross_source"}, {7, source.HomeProjectID}, {8, source.HomeLocatorID}},
			[]string{"r.source_law_id", "r.target_law_id", "r.target_project_id"}, resumeKey)
		coverageSQL, coverageArgs := refinementIncomingCoverage(resolved, sources)
		incomingArgs := append([]any{source.HomeProjectID, source.HomeLocatorID}, stringArgs(allRoots)...)
		incomingArgs = append(incomingArgs, coverageArgs...)
		incomingArgs = append(incomingArgs, incomingCrossArgs...)
		incomingArgs = append(incomingArgs, pageLimit)
		incomingRows, err := q.QueryContext(ctx, `
SELECT 'refines' AS kind, 'incoming' AS direction, r.source_law_id AS endpoint_law, r.target_law_id AS root_id, r.target_project_id AS root_scope,
       ls.kind AS endpoint_kind, ls.status AS endpoint_status, ls.title AS endpoint_title, ls.path AS endpoint_path, ls.content_hash AS endpoint_content_hash, r.scanned_commit_oid AS scanned_commit
FROM law_cross_source_relations r
LEFT JOIN law_subjects ls ON ls.home_project_id=r.home_project_id AND ls.home_locator_id=r.home_locator_id AND ls.law_id=r.source_law_id
WHERE r.home_project_id=? AND r.home_locator_id=? AND r.kind='refines' AND r.target_law_id IN (`+placeholdersFor(allRoots)+`) AND `+coverageSQL+` AND (ls.status IS NULL OR ls.status='accepted')`+predicateSQL(incomingCrossPredicate)+`
ORDER BY endpoint_law, root_id, root_scope LIMIT ?`, incomingArgs...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot read the authored incoming cross-source refinements of source "+source.HomeProjectID, true, "retry once the database is readable", err)
		}
		if err := scanRefinementIncomingCrossEdges(incomingRows, source, &edges, markOmission); err != nil {
			return nil, err
		}
	}
	sort.Slice(edges, func(i, j int) bool { return refinementEdgeLess(edges[i], edges[j]) })
	return edges, nil
}

func predicateSQL(predicate string) string {
	if predicate == "" {
		return ""
	}
	return " AND " + predicate
}

// refinementEnrichPageEndpoints resolves the page's cross-source endpoint
// identities through the structured endpoint lookup: distinct endpoint
// keys grouped by source Project, each read with an indexed
// home_project_id equality plus an IN list. Rows outside the requested
// set are discarded here, so a foreign source can neither answer for the
// set nor overwrite an endpoint identity.
func refinementEnrichPageEndpoints(ctx context.Context, q queryer, sources []KnowledgeHome, setProjects map[string]bool, edges []KnowledgeRefinementEdge, markOmission func(string)) error {
	crossLookups := map[string]bool{}
	for _, edge := range edges {
		if edge.EndpointLocatorID == "" {
			crossLookups[edge.EndpointProjectID+"/"+edge.EndpointLawID] = true
		}
	}
	if len(crossLookups) == 0 {
		return nil
	}
	keys := make([]string, 0, len(crossLookups))
	for key := range crossLookups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
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
	for _, source := range sources {
		setMembership[source.HomeProjectID+"\x00"+source.HomeLocatorID] = true
	}
	type endpointSubject struct {
		locator, kind, status, title, path, hash string
	}
	subjects := map[string][]endpointSubject{}
	for _, project := range endpointProjects {
		laws := endpointLawsByProject[project]
		rows, err := q.QueryContext(ctx, `SELECT home_project_id, home_locator_id, law_id, kind, status, title, path, content_hash FROM law_subjects WHERE home_project_id=? AND law_id IN (`+placeholdersFor(laws)+`)`, append([]any{project}, stringArgs(laws)...)...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every value stays parameter-bound.
		if err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot resolve amendment-context endpoint identities", true, "retry once the database is readable", err)
		}
		for rows.Next() {
			var rowProject, locator, lawID, kind, status, title, path, hash string
			if err := rows.Scan(&rowProject, &locator, &lawID, &kind, &status, &title, &path, &hash); err != nil {
				rows.Close()
				return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an amendment-context endpoint identity", true, "retry once the database is readable", err)
			}
			if !setMembership[rowProject+"\x00"+locator] {
				continue
			}
			subjects[rowProject+"/"+lawID] = append(subjects[rowProject+"/"+lawID], endpointSubject{locator: locator, kind: kind, status: status, title: title, path: path, hash: hash})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot finish reading amendment-context endpoint identities", true, "retry once the database is readable", err)
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
	return nil
}

// scanRefinementEdges decodes the same-home page rows. An incoming edge
// qualifies only when its endpoint's whole-record status is accepted; when
// the endpoint projection is missing entirely the edge stays on the page as
// an inspected omission instead of vanishing, because a hidden authored
// relation would read as an authoritative no-amendments graph. The
// acceptance filter itself is pushed into SQL, so every returned row is a
// page candidate.
func scanRefinementEdges(rows *sql.Rows, source KnowledgeHome, edges *[]KnowledgeRefinementEdge, markOmission func(string)) error {
	defer rows.Close()
	for rows.Next() {
		var root, direction, kind, endpoint, commit string
		var endpointKind, endpointStatus, endpointTitle, endpointPath, endpointHash sql.NullString
		if err := rows.Scan(&kind, &direction, &endpoint, &root, &endpointKind, &endpointStatus, &endpointTitle, &endpointPath, &endpointHash, &commit); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an authored law relation", true, "retry once the database is readable", err)
		}
		if !endpointStatus.Valid || endpointStatus.String == "" {
			markOmission("endpoint_projection_missing:" + source.HomeProjectID + "/" + source.HomeLocatorID + "/" + endpoint)
		}
		*edges = append(*edges, KnowledgeRefinementEdge{
			RootID: root, RootScopeProjectID: source.HomeProjectID, Direction: direction, Kind: kind, RelationClass: "same_home",
			EndpointProjectID: source.HomeProjectID, EndpointLocatorID: source.HomeLocatorID, EndpointLawID: endpoint,
			EndpointKind: endpointKind.String, EndpointStatus: endpointStatus.String, EndpointTitle: endpointTitle.String, EndpointPath: endpointPath.String,
			EndpointContentHash: endpointHash.String,
			SourceProjectID:     source.HomeProjectID, SourceLocatorID: source.HomeLocatorID, ScannedCommitOID: commit,
		})
	}
	return wrapFailureRows("cannot finish reading authored law relations", rows.Err())
}

// scanRefinementIncomingCrossEdges decodes the incoming cross-source
// refinement rows a source declares toward roots living in other set
// sources. Coverage and acceptance are pushed into SQL; the endpoint (the
// declaring source's refining law) keeps the same acceptance rule as a
// same-home incoming edge.
func scanRefinementIncomingCrossEdges(rows *sql.Rows, source KnowledgeHome, edges *[]KnowledgeRefinementEdge, markOmission func(string)) error {
	defer rows.Close()
	for rows.Next() {
		var kind, direction, endpoint, root, rootScope, commit string
		var endpointKind, endpointStatus, endpointTitle, endpointPath, endpointHash sql.NullString
		if err := rows.Scan(&kind, &direction, &endpoint, &root, &rootScope, &endpointKind, &endpointStatus, &endpointTitle, &endpointPath, &endpointHash, &commit); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an authored incoming cross-source refinement", true, "retry once the database is readable", err)
		}
		if !endpointStatus.Valid || endpointStatus.String == "" {
			markOmission("endpoint_projection_missing:" + source.HomeProjectID + "/" + source.HomeLocatorID + "/" + endpoint)
		}
		*edges = append(*edges, KnowledgeRefinementEdge{
			RootID: root, RootScopeProjectID: rootScope, Direction: direction, Kind: kind, RelationClass: "cross_source",
			EndpointProjectID: source.HomeProjectID, EndpointLocatorID: source.HomeLocatorID, EndpointLawID: endpoint,
			EndpointKind: endpointKind.String, EndpointStatus: endpointStatus.String, EndpointTitle: endpointTitle.String, EndpointPath: endpointPath.String,
			EndpointContentHash: endpointHash.String,
			SourceProjectID:     source.HomeProjectID, SourceLocatorID: source.HomeLocatorID, ScannedCommitOID: commit,
		})
	}
	return wrapFailureRows("cannot finish reading authored incoming cross-source refinements", rows.Err())
}

func scanRefinementCrossEdges(rows *sql.Rows, source KnowledgeHome, edges *[]KnowledgeRefinementEdge) error {
	defer rows.Close()
	for rows.Next() {
		var kind, direction, targetProject, targetLaw, root, commit string
		if err := rows.Scan(&kind, &direction, &targetProject, &targetLaw, &root, &commit); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", "cannot decode an authored cross-source law relation", true, "retry once the database is readable", err)
		}
		*edges = append(*edges, KnowledgeRefinementEdge{
			RootID: root, RootScopeProjectID: source.HomeProjectID, Direction: direction, Kind: kind, RelationClass: "cross_source",
			EndpointProjectID: targetProject, EndpointLawID: targetLaw,
			SourceProjectID: source.HomeProjectID, SourceLocatorID: source.HomeLocatorID, ScannedCommitOID: commit,
		})
	}
	return wrapFailureRows("cannot finish reading authored cross-source law relations", rows.Err())
}

// placeholdersFor returns a comma-separated placeholder fragment for an IN
// list of the given length. Callers keep every value parameter-bound.
func placeholdersFor(values []string) string {
	if len(values) == 0 {
		return "''"
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
}

func wrapFailureRows(detail string, err error) error {
	if err != nil {
		return wrapFailure(KindUnavailable, "PM1.Q10.amendment_context", detail, true, "retry once the database is readable", err)
	}
	return nil
}
