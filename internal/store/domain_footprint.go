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
// returns the governing Domains, the current law revisions, and the checks
// those laws' coverage names. Behavior matching is Q9 unchanged; it runs
// first, outside any transaction, because it probes the home's git head
// (CD-0195 D2). The join then reads one snapshot.
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

type FootprintCheck struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type FootprintLaw struct {
	LawID        string           `json:"law_id"`
	Kind         string           `json:"kind"`
	Title        string           `json:"title"`
	Path         string           `json:"path"`
	ContentHash  string           `json:"content_hash"`
	HomeDomainID string           `json:"home_domain_id"`
	Basis        string           `json:"basis"`
	MatchedPaths []string         `json:"matched_paths"`
	Checks       []FootprintCheck `json:"checks"`
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

func (p preparedDomainFootprint) completeTx(ctx context.Context, q queryer) (DomainFootprintResult, error) {
	return footprintCompleteTx(ctx, q, p.req, p.home, p.paths, p.behaviorLaws, p.behaviorCommit)
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
// path, Domain, package check, and ordered law, with no law checks loaded.
type footprintSelection struct {
	result  DomainFootprintResult
	ordered []FootprintLaw
	pair    []any
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
	if err := loadFootprintLawChecks(ctx, q, selection.pair, page); err != nil {
		return DomainFootprintResult{}, err
	}
	out.Laws = page
	out.offset = offset
	return out, nil
}

// footprintCompleteTx reads the whole footprint in one snapshot: every law
// the selection reaches, each with its checks. A size record keeps it.
func footprintCompleteTx(ctx context.Context, q queryer, req DomainFootprintRequest, home KnowledgeHome, paths, behaviorLaws []string, behaviorCommit string) (DomainFootprintResult, error) {
	selection, err := footprintSelectTx(ctx, q, req, home, paths, behaviorLaws, behaviorCommit)
	if err != nil {
		return DomainFootprintResult{}, err
	}
	if err := loadFootprintLawChecks(ctx, q, selection.pair, selection.ordered); err != nil {
		return DomainFootprintResult{}, err
	}
	out := selection.result
	out.Laws = selection.ordered
	return out, nil
}

// loadFootprintLawChecks fills each law's checks. Domain-basis laws are
// context and carry no checks.
func loadFootprintLawChecks(ctx context.Context, q queryer, pair []any, laws []FootprintLaw) error {
	for index := range laws {
		laws[index].Checks = []FootprintCheck{}
		if footprintBasisRank[laws[index].Basis] > footprintBasisRank[FootprintBehaviorMatch] {
			continue
		}
		checks, err := footprintChecks(ctx, q, pair, laws[index].LawID)
		if err != nil {
			return err
		}
		laws[index].Checks = checks
	}
	return nil
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
	pair := []any{home.HomeProjectID, home.HomeLocatorID}

	packages, mapHash, err := footprintPackages(ctx, q, pair)
	if err != nil {
		return none, err
	}
	candidates := map[string]*footprintCandidate{}
	propose := func(lawID, basis, matchedPath string) {
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
	reached := map[string]bool{}
	packageChecks := map[string]bool{}
	out.Paths = make([]FootprintPath, 0, len(paths))
	for _, filePath := range paths {
		documents, err := footprintStrings(ctx, q, `SELECT law_id FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND path=? AND status='accepted' ORDER BY law_id`, append(pair, filePath)...)
		if err != nil {
			return none, err
		}
		for _, lawID := range documents {
			propose(lawID, FootprintIsLawDocument, filePath)
		}
		named, err := footprintStrings(ctx, q, `SELECT e.law_id FROM law_evidence_paths e JOIN law_subjects s ON s.home_project_id=e.home_project_id AND s.home_locator_id=e.home_locator_id AND s.law_id=e.law_id WHERE e.home_project_id=? AND e.home_locator_id=? AND e.path=? AND s.status='accepted' ORDER BY e.law_id`, append(pair, filePath)...)
		if err != nil {
			return none, err
		}
		for _, lawID := range named {
			propose(lawID, FootprintNamesPath, filePath)
		}
		entry := FootprintPath{Path: filePath, DomainIDs: []string{}}
		if owner := footprintOwningPackage(packages, filePath); owner != "" {
			entry.Package = owner
			entry.DomainIDs = packages[owner]
			for _, domainID := range packages[owner] {
				reached[domainID] = true
			}
			if strings.HasSuffix(filePath, ".go") && path.Dir(filePath) == owner {
				packageChecks[owner] = true
			}
		}
		entry.Unmapped = entry.Package == "" && len(documents) == 0
		out.Paths = append(out.Paths, entry)
	}
	for _, lawID := range behaviorLaws {
		propose(lawID, FootprintBehaviorMatch, "")
	}

	laws, err := footprintLawRows(ctx, q, pair, req.Product)
	if err != nil {
		return none, err
	}
	// A law found through a path reaches its home Domain. A behavior match
	// does not: it is a ranked text guess, and reaching its home would add
	// that Domain's whole law set as context on the strength of the guess.
	for lawID := range candidates {
		if law, ok := laws[lawID]; ok && candidates[lawID].basis != FootprintBehaviorMatch {
			reached[law.HomeDomainID] = true
		}
	}
	reachedIDs := sortedKeys(reached)
	for _, domainID := range reachedIDs {
		homed, err := footprintStrings(ctx, q, `SELECT h.law_id FROM law_domain_homes h JOIN law_subjects s ON s.home_project_id=h.home_project_id AND s.home_locator_id=h.home_locator_id AND s.law_id=h.law_id WHERE h.home_project_id=? AND h.home_locator_id=? AND h.product_id=? AND h.domain_id=? AND s.status='accepted'`, append(pair, req.Product, domainID)...)
		if err != nil {
			return none, err
		}
		for _, lawID := range homed {
			propose(lawID, FootprintHomedInDomain, "")
		}
		applies, err := footprintStrings(ctx, q, `SELECT a.law_id FROM law_domain_applicability a JOIN law_subjects s ON s.home_project_id=a.home_project_id AND s.home_locator_id=a.home_locator_id AND s.law_id=a.law_id WHERE a.home_project_id=? AND a.home_locator_id=? AND a.product_id=? AND a.domain_id=? AND s.status='accepted'`, append(pair, req.Product, domainID)...)
		if err != nil {
			return none, err
		}
		for _, lawID := range applies {
			propose(lawID, FootprintAppliesToDomain, "")
		}
	}

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

	out.Domains = make([]FootprintDomain, 0, len(reachedIDs))
	for _, domainID := range reachedIDs {
		domain := FootprintDomain{DomainID: domainID}
		if err := q.QueryRowContext(ctx, `SELECT name,status FROM domains WHERE product_id=? AND domain_id=?`, req.Product, domainID).Scan(&domain.Name, &domain.Status); err != nil {
			return none, wrapFailure(KindUnavailable, footprintQueryID, "cannot read a reached Domain", true, "rebuild the git-derived knowledge index", err)
		}
		out.Domains = append(out.Domains, domain)
	}
	out.PackageChecks = make([]FootprintPackageCheck, 0, len(packageChecks))
	for _, owner := range sortedKeys(packageChecks) {
		out.PackageChecks = append(out.PackageChecks, FootprintPackageCheck{Package: owner, Kind: "go_package", Value: "./" + owner})
	}
	out.binding = footprintCursorBinding(req, paths)
	out.KnowledgeCommit = commit
	out.RegistryContentHash = registry.ContentHash
	out.PackageMapContentHash = mapHash
	out.ResultMeta = ResultMeta{QueryID: footprintQueryID, ContractVersion: "C22/1.0", ResolvedScope: ResolvedScope{ProductID: req.Product}, Authority: "authoritative", OrderingKeys: []string{"basis", "law_id"}}
	return footprintSelection{result: out, ordered: ordered, pair: pair}, nil
}

// footprintOwningPackage resolves a path to the map entry with the longest
// directory prefix that contains it.
func footprintOwningPackage(packages map[string][]string, filePath string) string {
	for dir := filePath; dir != "." && dir != "/"; dir = path.Dir(dir) {
		if _, ok := packages[dir]; ok {
			return dir
		}
	}
	return ""
}

func footprintPackages(ctx context.Context, q queryer, pair []any) (map[string][]string, *string, error) {
	rows, err := q.QueryContext(ctx, `SELECT package_path,domain_id,map_content_hash FROM knowledge_package_domains WHERE home_project_id=? AND home_locator_id=? ORDER BY package_path,domain_id`, pair...)
	if err != nil {
		return nil, nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot read the package map", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	packages := map[string][]string{}
	var mapHash *string
	for rows.Next() {
		var packagePath, domainID, hash string
		if err := rows.Scan(&packagePath, &domainID, &hash); err != nil {
			return nil, nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot decode the package map", true, "retry once the database is readable", err)
		}
		packages[packagePath] = append(packages[packagePath], domainID)
		mapHash = &hash
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot enumerate the package map", true, "retry once the database is readable", err)
	}
	return packages, mapHash, nil
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

func footprintChecks(ctx context.Context, q queryer, pair []any, lawID string) ([]FootprintCheck, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind,value FROM law_coverage_anchors WHERE home_project_id=? AND home_locator_id=? AND law_id=? ORDER BY ordinal`, append(pair, lawID)...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot read law coverage anchors", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	checks := []FootprintCheck{}
	for rows.Next() {
		var check FootprintCheck
		if err := rows.Scan(&check.Kind, &check.Value); err != nil {
			return nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot decode a law coverage anchor", true, "retry once the database is readable", err)
		}
		checks = append(checks, check)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, footprintQueryID, "cannot enumerate law coverage anchors", true, "retry once the database is readable", err)
	}
	return checks, nil
}

func footprintStrings(ctx context.Context, q queryer, query string, args ...any) ([]string, error) {
	var out []string
	err := readOverlapString(ctx, q, query, &out, args...)
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
