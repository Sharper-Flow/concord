package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The footprint read joins the designated knowledge home's package map with
// its law index (CD-0222 D2): given repository paths or a behavior phrase it
// returns the governing Domains and the current law revisions. Behavior
// matching is Q9 unchanged; it runs first, outside any transaction, because
// it probes the home's git head (CD-0195 D2). The join then reads one
// snapshot.
const (
	footprintQueryID       = "C22.DomainFootprint"
	footprintMaxPaths      = 64
	footprintMaxPath       = 512
	footprintMaxBehavior   = 256
	footprintBehaviorLimit = 8
)

// Footprint bases, strongest first. A law appears once, under its strongest
// basis, and the page orders by basis then law_id.
const (
	FootprintIsLawDocument   = "is_law_document"
	FootprintNamesPath       = "names_path"
	FootprintBehaviorMatch   = "behavior_match"
	FootprintHomedInDomain   = "homed_in_domain"
	FootprintAppliesToDomain = "applies_to_domain"
)

var footprintBasisRank = map[string]int{
	FootprintIsLawDocument:   0,
	FootprintNamesPath:       1,
	FootprintBehaviorMatch:   2,
	FootprintHomedInDomain:   3,
	FootprintAppliesToDomain: 4,
}

var footprintLawKinds = []string{"constitution", "decision", "spec"}

type DomainFootprintRequest struct {
	Product  string
	Paths    []string
	Behavior string
	Limit    int
	Cursor   string
}

type FootprintPath struct {
	Path      string   `json:"path"`
	Package   string   `json:"package,omitempty"`
	DomainIDs []string `json:"domain_ids"`
	Unmapped  bool     `json:"unmapped"`
}

type FootprintDomain struct {
	DomainID string `json:"domain_id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
}

type FootprintLaw struct {
	LawID        string   `json:"law_id"`
	Kind         string   `json:"kind"`
	Title        string   `json:"title"`
	Path         string   `json:"path"`
	ContentHash  string   `json:"content_hash"`
	HomeDomainID string   `json:"home_domain_id"`
	Basis        string   `json:"basis"`
	MatchedPaths []string `json:"matched_paths"`
}

type FootprintPackageCheck struct {
	Package string `json:"package"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
}

type DomainFootprintResult struct {
	ResultMeta
	KnowledgeCommit       string                  `json:"knowledge_commit"`
	RegistryContentHash   string                  `json:"registry_content_hash"`
	PackageMapContentHash *string                 `json:"package_map_content_hash"`
	Paths                 []FootprintPath         `json:"paths"`
	Domains               []FootprintDomain       `json:"domains"`
	Laws                  []FootprintLaw          `json:"laws"`
	PackageChecks         []FootprintPackageCheck `json:"package_checks"`
	Shapes                []DomainShapeResult     `json:"shapes,omitempty"`
	offset                int
	binding               string
}

// DomainFootprintPayload is the wire projection of a footprint result; see
// the payload note in domain_reads.go.
type DomainFootprintPayload struct {
	KnowledgeCommit       string                  `json:"knowledge_commit"`
	RegistryContentHash   string                  `json:"registry_content_hash"`
	PackageMapContentHash *string                 `json:"package_map_content_hash"`
	Paths                 []FootprintPath         `json:"paths"`
	Domains               []FootprintDomain       `json:"domains"`
	Laws                  []FootprintLaw          `json:"laws"`
	PackageChecks         []FootprintPackageCheck `json:"package_checks"`
	Shapes                []DomainShapeResult     `json:"shapes,omitempty"`
}

func NewDomainFootprintPayload(result DomainFootprintResult) DomainFootprintPayload {
	return DomainFootprintPayload{
		KnowledgeCommit:       result.KnowledgeCommit,
		RegistryContentHash:   result.RegistryContentHash,
		PackageMapContentHash: result.PackageMapContentHash,
		Paths:                 sliceOrEmpty(result.Paths),
		Domains:               sliceOrEmpty(result.Domains),
		Laws:                  sliceOrEmpty(result.Laws),
		PackageChecks:         sliceOrEmpty(result.PackageChecks),
		Shapes:                result.Shapes,
	}
}

// LimitPage keeps a complete prefix of the captured law page and derives the
// continuation from its offset. It does not re-query.
func (r DomainFootprintResult) LimitPage(_ DomainFootprintRequest, limit int) (DomainFootprintResult, error) {
	limit, err := queryLimit(limit)
	if err != nil || len(r.Laws) <= limit {
		return r, err
	}
	r.Laws = r.Laws[:limit]
	next := encodeFootprintCursor(r.KnowledgeCommit, r.binding, r.offset+limit)
	r.NextCursor = &next
	return r, nil
}

func footprintInvalid(message, recovery string) error {
	return newFailure(KindInvalidInput, footprintQueryID, message, false, recovery)
}

// validFootprintPath admits a normalized relative repository path.
func validFootprintPath(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > footprintMaxPath || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	return value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func validateDomainFootprintRequest(req DomainFootprintRequest) ([]string, error) {
	if req.Product == "" {
		return nil, footprintInvalid("footprint requires a Product", "supply product_id")
	}
	if len(req.Paths) == 0 && strings.TrimSpace(req.Behavior) == "" {
		return nil, footprintInvalid("footprint requires paths or a behavior", "supply at least one path or a behavior phrase")
	}
	if len(req.Paths) > footprintMaxPaths {
		return nil, footprintInvalid("footprint accepts at most 64 paths", "split the paths across calls")
	}
	if utf8.RuneCountInString(req.Behavior) > footprintMaxBehavior {
		return nil, footprintInvalid("footprint behavior is longer than 256 characters", "shorten the behavior phrase")
	}
	for _, value := range req.Paths {
		if !validFootprintPath(value) {
			return nil, footprintInvalid("footprint path is not a normalized relative repository path: "+value, "supply paths relative to the repository root without '.' or '..' segments")
		}
	}
	paths := uniqueSorted(req.Paths)
	if len(paths) != len(req.Paths) {
		return nil, footprintInvalid("footprint paths repeat", "supply each path once")
	}
	return paths, nil
}

// footprintCursorBinding names the request a cursor continues, so a cursor
// issued for other paths or another behavior refuses instead of paging them.
// Each field is length-prefixed, so no split of one request's bytes across
// fields reproduces another request's binding.
func footprintCursorBinding(req DomainFootprintRequest, paths []string) string {
	var key strings.Builder
	for _, field := range append([]string{req.Product, req.Behavior}, paths...) {
		key.WriteString(strconv.Itoa(len(field)))
		key.WriteByte(':')
		key.WriteString(field)
	}
	sum := sha256.Sum256([]byte(key.String()))
	return hex.EncodeToString(sum[:])
}

func encodeFootprintCursor(commit, binding string, offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(commit + "\x1f" + binding + "\x1f" + strconv.Itoa(offset)))
}

func decodeFootprintCursor(cursor, commit, binding string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, newFailure(KindInvalidCursor, footprintQueryID, "footprint cursor is malformed", false, "restart_query")
	}
	parts := strings.Split(string(raw), "\x1f")
	offset := 0
	if len(parts) == 3 {
		offset, err = strconv.Atoi(parts[2])
	}
	if len(parts) != 3 || err != nil || offset < 1 {
		return 0, newFailure(KindInvalidCursor, footprintQueryID, "footprint cursor is malformed", false, "restart_query")
	}
	if parts[1] != binding {
		return 0, newFailure(KindInvalidCursor, footprintQueryID, "footprint cursor was issued for another request", false, "restart_query")
	}
	if parts[0] != commit {
		return 0, newFailure(KindStaleContext, footprintQueryID, "the knowledge commit moved since the footprint cursor was issued", true, "restart_query")
	}
	return offset, nil
}

// QueryDomainFootprint answers one footprint page.
func (s *Store) QueryDomainFootprint(ctx context.Context, req DomainFootprintRequest) (DomainFootprintResult, error) {
	var out DomainFootprintResult
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, footprintQueryID, "store is not open", false, "open a store before reading Domains")
	}
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	prepared, err := s.prepareDomainFootprint(ctx, req)
	if err != nil {
		return out, err
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return out, wrapFailure(KindUnavailable, footprintQueryID, "cannot open the footprint snapshot", true, "retry once the database is readable", err)
	}
	defer func() { _ = tx.Rollback() }()
	return prepared.queryTx(ctx, tx, limit)
}

// preparedDomainFootprint is the part of a footprint read that must run
// outside a transaction: the validated request, the resolved knowledge home,
// and the behavior match, which probes git. queryTx finishes the read inside
// any transaction and refuses stale_context when the knowledge commit moved
// since the behavior match.
type preparedDomainFootprint struct {
	req            DomainFootprintRequest
	home           KnowledgeHome
	paths          []string
	behaviorLaws   []string
	behaviorCommit string
}

func (s *Store) prepareDomainFootprint(ctx context.Context, req DomainFootprintRequest) (preparedDomainFootprint, error) {
	paths, err := validateDomainFootprintRequest(req)
	if err != nil {
		return preparedDomainFootprint{}, err
	}
	home, err := resolveKnowledgeQueryHome(ctx, s.db, req.Product, "", KnowledgeHome{}, footprintQueryID)
	if err != nil {
		return preparedDomainFootprint{}, err
	}
	behaviorLaws, behaviorCommit, err := footprintBehaviorLaws(ctx, s, home, req.Behavior)
	if err != nil {
		return preparedDomainFootprint{}, err
	}
	return preparedDomainFootprint{req: req, home: home, paths: paths, behaviorLaws: behaviorLaws, behaviorCommit: behaviorCommit}, nil
}

func (p preparedDomainFootprint) queryTx(ctx context.Context, q queryer, limit int) (DomainFootprintResult, error) {
	return queryDomainFootprintTx(ctx, q, p.req, p.home, p.paths, p.behaviorLaws, p.behaviorCommit, limit)
}

// footprintBehaviorLaws runs Q9 over the designated home's indexed law kinds
// and returns the matched law IDs and the watermark they were read at. It
// takes the store, not a transaction, because Q9 probes git.
func footprintBehaviorLaws(ctx context.Context, s *Store, home KnowledgeHome, behavior string) ([]string, string, error) {
	if strings.TrimSpace(behavior) == "" {
		return nil, "", nil
	}
	kinds, err := footprintIndexedLawKinds(ctx, s.db, home)
	if err != nil || len(kinds) == 0 {
		return nil, "", err
	}
	result, err := queryQ9(ctx, s.db, Q9Request{Home: home, Kinds: kinds, Text: behavior, Limit: footprintBehaviorLimit}, s.now())
	if err != nil {
		return nil, "", err
	}
	laws := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		laws = append(laws, item.ID)
	}
	return laws, result.IndexWatermark, nil
}

func footprintIndexedLawKinds(ctx context.Context, q queryer, home KnowledgeHome) ([]string, error) {
	args := []any{home.HomeProjectID, home.HomeLocatorID, home.HeadRef}
	for _, kind := range footprintLawKinds {
		args = append(args, kind)
	}
	return footprintStrings(ctx, q, `SELECT c.kind FROM knowledge_kind_coverage c JOIN knowledge_index_watermark w ON w.home_project_id=c.home_project_id AND w.home_locator_id=c.home_locator_id AND w.head_ref=c.head_ref AND w.scanned_commit_oid=c.scanned_commit_oid WHERE c.home_project_id=? AND c.home_locator_id=? AND c.head_ref=? AND c.kind IN (?`+strings.Repeat(",?", len(footprintLawKinds)-1)+`) AND c.coverage='indexed' ORDER BY c.kind`, args...)
}

type footprintCandidate struct {
	basis   string
	matched map[string]bool
}

// footprintSelection is the complete footprint before paging: every reached
// path, Domain, package check, and ordered law.
type footprintSelection struct {
	result  DomainFootprintResult
	ordered []FootprintLaw
}

func queryDomainFootprintTx(ctx context.Context, q queryer, req DomainFootprintRequest, home KnowledgeHome, paths, behaviorLaws []string, behaviorCommit string, limit int) (DomainFootprintResult, error) {
	selection, err := footprintSelectTx(ctx, q, req, home, paths, behaviorLaws, behaviorCommit)
	if err != nil {
		return DomainFootprintResult{}, err
	}
	out := selection.result
	offset, err := decodeFootprintCursor(req.Cursor, out.KnowledgeCommit, out.binding)
	if err != nil {
		return DomainFootprintResult{}, err
	}
	if offset > len(selection.ordered) {
		return DomainFootprintResult{}, newFailure(KindInvalidCursor, footprintQueryID, "footprint cursor is past the result", false, "restart_query")
	}
	page := selection.ordered[offset:]
	if len(page) > limit {
		page = page[:limit]
		next := encodeFootprintCursor(out.KnowledgeCommit, out.binding, offset+limit)
		out.NextCursor = &next
	}
	out.Laws = page
	out.offset = offset
	return out, nil
}

func footprintSelectTx(ctx context.Context, q queryer, req DomainFootprintRequest, home KnowledgeHome, paths, behaviorLaws []string, behaviorCommit string) (footprintSelection, error) {
	var out DomainFootprintResult
	var none footprintSelection
	registry, err := readDomainRegistry(ctx, q, req.Product)
	if err != nil {
		return none, err
	}
	var commit string
	if err := q.QueryRowContext(ctx, `SELECT scanned_commit_oid FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef).Scan(&commit); err == sql.ErrNoRows {
		return none, newFailure(KindIndexDegraded, footprintQueryID, "the designated knowledge home has no index", true, "rebuild the git-derived knowledge index")
	} else if err != nil {
		return none, wrapFailure(KindUnavailable, footprintQueryID, "cannot read the knowledge watermark", true, "retry once the database is readable", err)
	}
	if behaviorCommit != "" && behaviorCommit != commit {
		return none, newFailure(KindStaleContext, footprintQueryID, "the knowledge commit moved between the behavior match and the join", true, "retry the footprint read")
	}

	selected, err := footprintSelectPathsTx(ctx, q, home, req.Product, paths)
	if err != nil {
		return none, err
	}
	out.Paths, out.Shapes = selected.paths, selected.shapes
	for _, lawID := range behaviorLaws {
		footprintPropose(selected.candidates, lawID, FootprintBehaviorMatch, "")
	}
	pair := []any{home.HomeProjectID, home.HomeLocatorID}
	laws, err := footprintLawRows(ctx, q, pair, req.Product)
	if err != nil {
		return none, err
	}
	// A law found through a path reaches its home Domain. A behavior match
	// does not: it is a ranked text guess, and reaching its home would add
	// that Domain's whole law set as context on the strength of the guess.
	for lawID, candidate := range selected.candidates {
		if law, ok := laws[lawID]; ok && candidate.basis != FootprintBehaviorMatch {
			selected.reached[law.HomeDomainID] = true
		}
	}
	reachedIDs := sortedKeys(selected.reached)
	if err := footprintDomainCandidatesTx(ctx, q, pair, req.Product, reachedIDs, selected.candidates); err != nil {
		return none, err
	}
	ordered := footprintOrderedLaws(laws, selected.candidates)
	out.Domains, err = footprintReachedDomainsTx(ctx, q, req.Product, reachedIDs)
	if err != nil {
		return none, err
	}
	out.PackageChecks = make([]FootprintPackageCheck, 0, len(selected.packageChecks))
	for _, owner := range sortedKeys(selected.packageChecks) {
		out.PackageChecks = append(out.PackageChecks, FootprintPackageCheck{Package: owner, Kind: "go_package", Value: "./" + owner})
	}
	out.binding = footprintCursorBinding(req, paths)
	out.KnowledgeCommit = commit
	out.RegistryContentHash = registry.ContentHash
	out.PackageMapContentHash = selected.mapHash
	out.ResultMeta = ResultMeta{QueryID: footprintQueryID, ContractVersion: "C22/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: "authoritative", OrderingKeys: []string{"basis", "law_id"}}
	return footprintSelection{result: out, ordered: ordered}, nil
}

type footprintSelectedPaths struct {
	paths         []FootprintPath
	shapes        []DomainShapeResult
	reached       map[string]bool
	packageChecks map[string]bool
	candidates    map[string]*footprintCandidate
	mapHash       *string
}

func footprintPropose(candidates map[string]*footprintCandidate, lawID, basis, matchedPath string) {
	candidate, ok := candidates[lawID]
	if !ok {
		candidate = &footprintCandidate{basis: basis, matched: map[string]bool{}}
		candidates[lawID] = candidate
	} else if footprintBasisRank[basis] < footprintBasisRank[candidate.basis] {
		candidate.basis = basis
	}
	if matchedPath != "" {
		candidate.matched[matchedPath] = true
	}
}

// Law evidence and behavior ranking retain their existing bases. Root ownership
// and facts come only from the same bounded selected read as QueryDomainShape.
func footprintSelectPathsTx(ctx context.Context, q queryer, home KnowledgeHome, product string, paths []string) (footprintSelectedPaths, error) {
	out := footprintSelectedPaths{paths: []FootprintPath{}, reached: map[string]bool{}, packageChecks: map[string]bool{}, candidates: map[string]*footprintCandidate{}}
	pair := []any{home.HomeProjectID, home.HomeLocatorID}
	if len(paths) == 0 {
		snapshot, err := readDomainShapeSnapshotTx(ctx, q, home, product)
		if err != nil {
			return out, err
		}
		if snapshot.MapContentHash != "" {
			out.mapHash = &snapshot.MapContentHash
		}
	}
	for _, filePath := range paths {
		documents, err := footprintPathCandidatesTx(ctx, q, pair, filePath, out.candidates)
		if err != nil {
			return out, err
		}
		shape, err := selectedDomainShapeTx(ctx, q, home, product, DomainShapeSelector{Path: filePath})
		if err != nil {
			return out, err
		}
		out.shapes = append(out.shapes, shape)
		if shape.PackageMapContentHash != "" {
			hash := shape.PackageMapContentHash
			out.mapHash = &hash
		}
		entry := FootprintPath{Path: filePath, DomainIDs: shape.DomainIDs}
		if len(shape.Roots) != 0 {
			entry.Package = shape.Roots[0].Path
		}
		out.addPath(entry, documents)
	}
	return out, nil
}

func (out *footprintSelectedPaths) addPath(entry FootprintPath, documents []string) {
	if entry.Package != "" {
		for _, domain := range entry.DomainIDs {
			out.reached[domain] = true
		}
		if strings.HasSuffix(entry.Path, ".go") && path.Dir(entry.Path) == entry.Package {
			out.packageChecks[entry.Package] = true
		}
	}
	entry.Unmapped = entry.Package == "" && len(documents) == 0
	out.paths = append(out.paths, entry)
}

func footprintPathCandidatesTx(ctx context.Context, q queryer, pair []any, filePath string, candidates map[string]*footprintCandidate) ([]string, error) {
	documents, err := footprintStrings(ctx, q, `SELECT law_id FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND path=? AND status='accepted' ORDER BY law_id`, append(pair, filePath)...)
	if err != nil {
		return nil, err
	}
	for _, lawID := range documents {
		footprintPropose(candidates, lawID, FootprintIsLawDocument, filePath)
	}
	named, err := footprintStrings(ctx, q, `SELECT e.law_id FROM law_evidence_paths e JOIN law_subjects s ON s.home_project_id=e.home_project_id AND s.home_locator_id=e.home_locator_id AND s.law_id=e.law_id WHERE e.home_project_id=? AND e.home_locator_id=? AND e.path=? AND s.status='accepted' ORDER BY e.law_id`, append(pair, filePath)...)
	if err != nil {
		return nil, err
	}
	for _, lawID := range named {
		footprintPropose(candidates, lawID, FootprintNamesPath, filePath)
	}
	return documents, nil
}

func footprintDomainCandidatesTx(ctx context.Context, q queryer, pair []any, product string, domains []string, candidates map[string]*footprintCandidate) error {
	for _, domainID := range domains {
		homed, err := footprintStrings(ctx, q, `SELECT h.law_id FROM law_domain_homes h JOIN law_subjects s ON s.home_project_id=h.home_project_id AND s.home_locator_id=h.home_locator_id AND s.law_id=h.law_id WHERE h.home_project_id=? AND h.home_locator_id=? AND h.product_id=? AND h.domain_id=? AND s.status='accepted'`, append(pair, product, domainID)...)
		if err != nil {
			return err
		}
		for _, lawID := range homed {
			footprintPropose(candidates, lawID, FootprintHomedInDomain, "")
		}
		applies, err := footprintStrings(ctx, q, `SELECT a.law_id FROM law_domain_applicability a JOIN law_subjects s ON s.home_project_id=a.home_project_id AND s.home_locator_id=a.home_locator_id AND s.law_id=a.law_id WHERE a.home_project_id=? AND a.home_locator_id=? AND a.product_id=? AND a.domain_id=? AND s.status='accepted'`, append(pair, product, domainID)...)
		if err != nil {
			return err
		}
		for _, lawID := range applies {
			footprintPropose(candidates, lawID, FootprintAppliesToDomain, "")
		}
	}
	return nil
}

func footprintOrderedLaws(laws map[string]FootprintLaw, candidates map[string]*footprintCandidate) []FootprintLaw {
	ordered := make([]FootprintLaw, 0, len(candidates))
	for lawID, candidate := range candidates {
		law, ok := laws[lawID]
		if !ok {
			continue // superseded, or a Q9 match outside the accepted law set
		}
		law.Basis = candidate.basis
		law.MatchedPaths = sortedKeys(candidate.matched)
		ordered = append(ordered, law)
	}
	sort.Slice(ordered, func(left, right int) bool {
		a, b := ordered[left], ordered[right]
		if footprintBasisRank[a.Basis] != footprintBasisRank[b.Basis] {
			return footprintBasisRank[a.Basis] < footprintBasisRank[b.Basis]
		}
		return a.LawID < b.LawID
	})

	return ordered
}

func footprintReachedDomainsTx(ctx context.Context, q queryer, product string, domains []string) ([]FootprintDomain, error) {
	out := make([]FootprintDomain, 0, len(domains))
	for _, domainID := range domains {
		domain := FootprintDomain{DomainID: domainID}
		if err := q.QueryRowContext(ctx, `SELECT name,status FROM domains WHERE product_id=? AND domain_id=?`, product, domainID).Scan(&domain.Name, &domain.Status); err != nil {
			return nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot read a reached Domain", true, "rebuild the git-derived knowledge index", err)
		}
		out = append(out, domain)
	}
	return out, nil
}

// footprintLawRows reads every accepted, Domain-homed law of the home.
func footprintLawRows(ctx context.Context, q queryer, pair []any, product string) (map[string]FootprintLaw, error) {
	rows, err := q.QueryContext(ctx, `SELECT s.law_id,s.kind,s.title,s.path,s.content_hash,h.domain_id FROM law_subjects s JOIN law_domain_homes h ON h.home_project_id=s.home_project_id AND h.home_locator_id=s.home_locator_id AND h.law_id=s.law_id WHERE s.home_project_id=? AND s.home_locator_id=? AND h.product_id=? AND s.status='accepted'`, append(pair, product)...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot read current law", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	laws := map[string]FootprintLaw{}
	for rows.Next() {
		var law FootprintLaw
		if err := rows.Scan(&law.LawID, &law.Kind, &law.Title, &law.Path, &law.ContentHash, &law.HomeDomainID); err != nil {
			return nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot decode current law", true, "retry once the database is readable", err)
		}
		laws[law.LawID] = law
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot enumerate current law", true, "retry once the database is readable", err)
	}
	return laws, nil
}

func footprintStrings(ctx context.Context, q queryer, query string, args ...any) ([]string, error) {
	var out []string
	err := readDomainStringList(ctx, q, query, &out, args...)
	return out, err
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
