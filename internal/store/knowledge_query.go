package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type Q9Request struct {
	Product       string
	Project       string
	Domain        string
	Kinds         []string
	Tags          []string
	Text          string
	Since         string
	Until         string
	Limit         int
	Cursor        string
	AllowDegraded bool
	Home          KnowledgeHome
}

type KnowledgeItem struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	CompletedAt   string   `json:"completed_at"`
	OutcomeTag    string   `json:"outcome_tag"`
	SuccessorID   string   `json:"successor_id,omitempty"`
	LessonTags    []string `json:"lesson_tags"`
	Summary       string   `json:"summary"`
	ProductIDs    []string `json:"product_ids,omitempty"`
	ProjectIDs    []string `json:"project_ids,omitempty"`
	DomainIDs     []string `json:"domain_ids,omitempty"`
	TagIDs        []string `json:"tag_ids,omitempty"`
	HomeProjectID string   `json:"home_project_id"`
	HomeLocatorID string   `json:"home_locator_id"`
	NotePath      string   `json:"path"`
	NotePathRef   string   `json:"note_path"`
	Commit        string   `json:"commit"`
	CommitOID     string   `json:"commit_oid"`
	ContentHash   string   `json:"content_hash"`
	ScopeMode     string   `json:"scope_mode"`
	MatchClass    int      `json:"-"`
}

type Q9Result struct {
	ResultMeta
	Items          []KnowledgeItem `json:"items"`
	IndexWatermark string          `json:"index_watermark"`
	// SourceWatermarks carries the per-source freshness verdict of a
	// Product-wide Q9 over a registered source set (CD-0200). It stays empty
	// for the single-source path, whose output is unchanged.
	SourceWatermarks []KnowledgeSourceWatermark `json:"source_watermarks,omitempty"`
}

// KnowledgeSourceWatermark is one registered source's verified freshness
// verdict inside a federated Q9 answer.
type KnowledgeSourceWatermark struct {
	ProjectID string `json:"project_id"`
	LocatorID string `json:"locator_id"`
	Watermark string `json:"watermark"`
	Authority string `json:"authority"`
}

type Q10Request struct {
	Work        string
	KnowledgeID string
	Product     string
	// AllowDegraded is the historical read's own degradation opt-in
	// (CON-830 review): it degrades the bare-ID population verification
	// and the recorded locator/manifest/blob proof paths below. The
	// resolve_note surface never sets it, so a surfaced historical proof
	// failure always refuses.
	AllowDegraded bool
	Home          KnowledgeHome
	// IncludeAmendmentContext opts the read into the separate
	// current_amendment_context section (CON-830): independently verified
	// current-source proof that never inherits the historical locator
	// proof above. Historical-only reads keep the existing shape.
	IncludeAmendmentContext bool
	// AmendmentContextLimit and AmendmentContextCursor bound and continue
	// the opt-in amendment-context page (1-32 edges, snapshot-bound).
	AmendmentContextLimit  int
	AmendmentContextCursor string
	// AmendmentContextAllowDegraded degrades the current_amendment_context
	// section alone: strict refusal stays the default for required current
	// sources, and a degraded page names its omissions instead of claiming
	// an authoritative no-amendments graph. It never reaches AllowDegraded:
	// the historical locator, manifest, and blob proof paths above cannot
	// be degraded by a contextual opt-in.
	AmendmentContextAllowDegraded bool
}

// parseQualifiedKnowledgeID splits the source-qualified reference form
// "project_id/law_id" (CD-0200). Project IDs cannot contain '/', and the
// rebuild refuses '/' inside a law ID, so the first '/' is the exact split
// point and a second '/' is a malformed reference, never a nested path. A
// bare reference is its own law ID.
func parseQualifiedKnowledgeID(op, value string) (projectID, lawID string, qualified bool, err error) {
	projectID, lawID, qualified = strings.Cut(value, "/")
	if !qualified {
		return "", value, false, nil
	}
	if projectID == "" || lawID == "" || strings.Contains(lawID, "/") {
		return "", "", true, newFailure(KindInvalidFilter, op, "qualified knowledge reference must be project_id/law_id", false, "supply a Project identifier and a law ID separated by one '/'")
	}
	return projectID, lawID, true, nil
}

type CanonicalNote struct {
	HomeProjectID string `json:"home_project_id"`
	HomeLocatorID string `json:"home_locator_id"`
	NotePath      string `json:"path"`
	NotePathRef   string `json:"note_path"`
	Commit        string `json:"commit"`
	CommitOID     string `json:"commit_oid"`
	ContentHash   string `json:"content_hash"`
}

type Q10Result struct {
	ResultMeta
	Status string         `json:"status"`
	Note   *CanonicalNote `json:"note,omitempty"`
	Result *Q10Payload    `json:"result"`
}

type Q10Payload struct {
	Status      string         `json:"status"`
	Note        *CanonicalNote `json:"note,omitempty"`
	LawStatus   string         `json:"law_status,omitempty"`
	SuccessorID string         `json:"successor_id,omitempty"`
	// CurrentAmendmentContext is the opt-in separately verified one-hop
	// amendment graph of the resolved law record. Absent on every
	// historical-only read and on work notes.
	CurrentAmendmentContext *KnowledgeRefinementContextResult `json:"current_amendment_context,omitempty"`
}

// KnowledgeLawStatus reports the record's law status (CD-0020 D3) when the
// indexed outcome tag carries one: accepted or superseded for law, published
// for lessons, references, and research. A work-note outcome tag is free
// front-matter text rather than a law status, so a work note projects no
// status whatever its tag says.
func KnowledgeLawStatus(kind, outcomeTag string) string {
	if kind == "work_note" {
		return ""
	}
	switch outcomeTag {
	case "accepted", "superseded", "published":
		return outcomeTag
	}
	return ""
}

func (s *Store) QueryQ9(ctx context.Context, req Q9Request) (Q9Result, error) {
	var out Q9Result
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "PM1.Q9", "store is not open", false, "open a store before querying knowledge")
	}
	return queryQ9(ctx, s.db, req, s.now())
}

// validateQ9SharedFilters applies the bounded-text and time-window rules both
// Q9 paths share (CD-0200): a federated Product-wide answer refuses a
// malformed filter exactly where the single-home path refuses it, never
// answers authoritatively past one.
func validateQ9SharedFilters(req Q9Request) error {
	if len(req.Text) > 256 {
		return newFailure(KindInvalidFilter, "PM1.Q9", "bounded knowledge text is too long", false, "limit text to 256 characters")
	}
	if req.Since != "" {
		if _, err := time.Parse(time.RFC3339Nano, req.Since); err != nil {
			return newFailure(KindInvalidFilter, "PM1.Q9", "since must be RFC3339", false, "supply a valid time window")
		}
	}
	if req.Until != "" {
		if _, err := time.Parse(time.RFC3339Nano, req.Until); err != nil {
			return newFailure(KindInvalidFilter, "PM1.Q9", "until must be RFC3339", false, "supply a valid time window")
		}
	}
	return nil
}

// queryQ9 searches the projected knowledge index and probes the home's git
// head for freshness. It takes *sql.DB because the git probe must never run
// inside an open transaction (CD-0195 D2): the type makes a transaction
// caller a compile-time error.
func queryQ9(ctx context.Context, db *sql.DB, req Q9Request, observedAt time.Time) (Q9Result, error) {
	var out Q9Result
	if err := validateQ9SharedFilters(req); err != nil {
		return out, err
	}
	// CD-0200: a Product scope resolves the full registered source set. A
	// one-element set takes the single-home path below unchanged; a larger
	// set federates the bounded query over every registered source.
	if req.Product != "" {
		sources, err := resolveKnowledgeQuerySources(ctx, db, req.Product, "PM1.Q9")
		if err != nil {
			return out, err
		}
		if len(sources) > 1 {
			if req.Project != "" {
				var member bool
				if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, req.Product, req.Project).Scan(&member); err != nil {
					return out, wrapFailure(KindUnavailable, "PM1.Q9", "cannot validate Product/Project membership", true, "retry once the database is readable", err)
				}
				if !member {
					return out, newFailure(KindUnknownScope, "PM1.Q9", "Project is not a member of the requested Product", false, "supply a Project belonging to the Product")
				}
			}
			return queryQ9Federated(ctx, db, req, sources, observedAt)
		}
	}
	resolvedHome, err := resolveKnowledgeQueryHome(ctx, db, req.Product, req.Project, req.Home, "PM1.Q9")
	if err != nil {
		return out, err
	}
	req.Home = resolvedHome
	limit, err := knowledgeLimit(req.Limit)
	if err != nil {
		return out, err
	}
	kinds, err := knowledgeKinds(req.Kinds)
	if err != nil {
		return out, err
	}
	tags := orderedStrings(nonEmptyStrings(req.Tags))
	watermark, authority, err := validateKnowledgeHomeForQueryCore(ctx, db, req.Home, req.AllowDegraded, "PM1.Q9")
	if err != nil {
		return out, err
	}
	if watermark == "" {
		watermark = "unindexed"
	}
	if authority == "authoritative" {
		if err := validateKnowledgeCoverageCore(ctx, db, req.Home, watermark, kinds); err != nil {
			return out, err
		}
	}
	var resume *knowledgeResumeKey
	if req.Cursor != "" {
		cursor, err := decodeKnowledgeCursor(req.Cursor, req, kinds, tags)
		if err != nil {
			return out, err
		}
		resume = knowledgeResumeKeyFromCursor(cursor)
	}
	query, args := buildKnowledgeQueryForScope(req, kinds, tags, limit, resume)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q9", "cannot search the git knowledge index", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	items := make([]KnowledgeItem, 0, limit)
	for rows.Next() {
		item, err := scanKnowledgeItemForScope(rows)
		if err != nil {
			return out, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q9", "cannot finish the knowledge index query", true, "retry once the database is readable", err)
	}
	var cursor *string
	if len(items) == limit {
		last := items[len(items)-1]
		encoded, err := encodeKnowledgeCursor(knowledgeCursor{Version: 2, Product: req.Product, Project: req.Project, Domain: req.Domain, Kinds: kinds, Tags: tags, Text: req.Text, Since: req.Since, Until: req.Until, HomeProjectID: req.Home.HomeProjectID, HomeLocatorID: req.Home.HomeLocatorID, HeadRef: req.Home.HeadRef, MatchClass: last.MatchClass, CompletedAt: last.CompletedAt, ID: last.ID})
		if err != nil {
			return out, err
		}
		cursor = &encoded
	}
	meta := knowledgeWatermarkMeta("PM1.Q9", watermark, authority, observedAt)
	meta.ResolvedScope = ResolvedScope{ProductID: req.Product, ProjectID: req.Project}
	if authority == "authoritative" {
		meta.Omissions = append(meta.Omissions, knowledgeCoverageOmissions(ctx, db, req.Home, watermark)...)
	}
	if authority != "authoritative" {
		meta.Omissions = []string{"knowledge_index_lagging_or_unreachable"}
	}
	meta.NextCursor = cursor
	out.ResultMeta, out.Items, out.IndexWatermark = meta, items, watermark
	return out, nil
}

// validateKnowledgeHomeForQueryCore reads the freshness verdict for one home.
// It takes *sql.DB because its probe reaches the home's git head; a
// transaction caller is a compile-time error (CD-0195 D2).
func validateKnowledgeHomeForQueryCore(ctx context.Context, db *sql.DB, home KnowledgeHome, allowDegraded bool, op string) (string, string, error) {
	pool := newGitProverPool(ctx)
	defer pool.close()
	return validateKnowledgeHomeProven(ctx, db, pool, home, allowDegraded, op)
}

// validateKnowledgeHomeProven is the same freshness verdict through a
// caller-owned prover pool: the live head commit, the content digest, and
// the projected-blob comparison all resolve through one read-scoped batch
// process per repository, pinned to the live commit OID that process
// returned. A pass that verifies several sources shares its pool, so the
// process count stays bounded by distinct repositories.
func validateKnowledgeHomeProven(ctx context.Context, db *sql.DB, pool *gitProverPool, home KnowledgeHome, allowDegraded bool, op string) (string, string, error) {
	if err := validateKnowledgeHomeFields(home); err != nil {
		return "", "", err
	}
	unreachable := func() (string, string, error) {
		if allowDegraded {
			return "unreachable", "degraded", nil
		}
		return "", "", newFailure(KindUnreachable, op, "git knowledge authority is unreachable", true, "restore the git home and retry")
	}
	prover, err := pool.prover(home.RepoPath)
	if err != nil {
		return unreachable()
	}
	current, currentRoot, err := prover.resolveHeadWithRoot(home.HeadRef)
	if err != nil {
		return unreachable()
	}
	watermark, err := readKnowledgeWatermarkProver(ctx, db, prover, home, current, currentRoot)
	if err != nil {
		if allowDegraded {
			return "unreachable", "degraded", nil
		}
		return "", "", err
	}
	if !watermark.Fresh {
		if allowDegraded {
			return watermark.Scanned, "degraded", nil
		}
		return "", "", newFailure(KindIndexDegraded, op, "knowledge index watermark is stale or incomplete", true, "rebuild the git-derived knowledge index")
	}
	return watermark.Scanned, "authoritative", nil
}

// validateKnowledgeHomeFields admits only complete explicit homes and
// refs a prover request can carry safely.
func validateKnowledgeHomeFields(home KnowledgeHome) error {
	if home.HomeProjectID == "" || home.HomeLocatorID == "" || home.RepoPath == "" || home.HeadRef == "" {
		return newFailure(KindInvalidFilter, "knowledge_home", "KnowledgeHome requires stable IDs, repository path, and head ref", false, "supply a complete explicit KnowledgeHome")
	}
	return validateKnowledgeHomeRef(home.HeadRef)
}

func validateKnowledgeCoverageCore(ctx context.Context, q queryer, home KnowledgeHome, commit string, kinds []string) error {
	if len(kinds) == 0 {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT kind,coverage,scanned_commit_oid FROM knowledge_kind_coverage WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef)
	if err != nil {
		return wrapFailure(KindUnavailable, "PM1.Q9", "cannot read knowledge kind coverage", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	available := map[string]bool{}
	for rows.Next() {
		var kind, coverage, scanned string
		if err := rows.Scan(&kind, &coverage, &scanned); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q9", "cannot decode knowledge kind coverage", true, "retry once the database is readable", err)
		}
		if coverage == "indexed" && scanned == commit {
			available[kind] = true
		}
	}
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, "PM1.Q9", "cannot finish reading knowledge kind coverage", true, "retry once the database is readable", err)
	}
	missing := make([]string, 0)
	for _, kind := range kinds {
		if !available[kind] {
			missing = append(missing, kind)
		}
	}
	if len(missing) > 0 {
		failure := newFailure(KindKnowledgeUnavailable, "PM1.Q9", "explicitly requested knowledge kinds are unavailable: "+strings.Join(missing, ","), false, "publish and rebuild the canonical kind, or remove it from the filter")
		failure.UnavailableKinds = missing
		failure.CandidateIDs = append([]string(nil), missing...)
		return failure
	}
	return nil
}

func scanKnowledgeItemForScope(rows *sql.Rows) (KnowledgeItem, error) {
	var item KnowledgeItem
	var lessonTags, productIDs, projectIDs, domainIDs, tagIDs string
	args := []any{&item.ID, &item.Kind, &item.Title, &item.CompletedAt, &item.OutcomeTag, &item.SuccessorID, &lessonTags, &item.Summary, &item.HomeProjectID, &item.HomeLocatorID, &item.NotePath, &item.Commit, &item.ContentHash, &item.ScopeMode, &productIDs, &projectIDs, &domainIDs, &tagIDs}
	args = append(args, &item.MatchClass)
	if err := rows.Scan(args...); err != nil {
		return item, wrapFailure(KindUnavailable, "PM1.Q9", "cannot decode a knowledge index row", true, "retry once the database is readable", err)
	}
	if json.Unmarshal([]byte(lessonTags), &item.LessonTags) != nil {
		return item, newFailure(KindInvariantViolation, "PM1.Q9", "indexed lesson_tags are malformed", false, "rebuild the git-derived knowledge index")
	}
	for _, scope := range []struct {
		raw    string
		target *[]string
	}{{productIDs, &item.ProductIDs}, {projectIDs, &item.ProjectIDs}, {domainIDs, &item.DomainIDs}, {tagIDs, &item.TagIDs}} {
		if scope.raw == "" {
			continue
		}
		if json.Unmarshal([]byte(scope.raw), scope.target) != nil {
			return item, newFailure(KindInvariantViolation, "PM1.Q9", "indexed scope is malformed", false, "rebuild the git-derived knowledge index")
		}
	}
	item.CommitOID = item.Commit
	item.NotePathRef = item.NotePath
	return item, nil
}

func (s *Store) QueryQ10(ctx context.Context, req Q10Request) (Q10Result, error) {
	var out Q10Result
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "PM1.Q10", "store is not open", false, "open a store before querying knowledge")
	}
	return queryQ10(ctx, s.db, s.EnsureKnowledgeIndexFresh, s.readKnowledgeManifestCached, req)
}

// knowledgeManifestReader reads one home's composed manifest at one commit.
// Production passes the store's memoizing reader; a nil reader reads through
// the uncached owner.
type knowledgeManifestReader func(ctx context.Context, repo, commit string, role knowledgeManifestRole) (KnowledgeManifest, bool, error)

func (read knowledgeManifestReader) orDirect() knowledgeManifestReader {
	if read == nil {
		return readKnowledgeManifest
	}
	return read
}

// queryQ10 verifies one canonical note against its recorded git proof. It takes
// *sql.DB because the git verification must never run inside an open
// transaction (CD-0195 D2): the type makes a transaction caller a compile-time
// error. freshen is the existing demand-freshness owner the opt-in current
// context reuses before its current-source proof; it stays nil on every
// store-internal caller that must keep historical verdicts byte-identical.
// readManifest serves the historical manifest proof; the store passes its
// commit-keyed memo so a repeated read of one immutable commit does not
// re-compose and re-validate the whole record corpus per call.
func queryQ10(ctx context.Context, db *sql.DB, freshen func(context.Context, KnowledgeHome) error, readManifest knowledgeManifestReader, req Q10Request) (Q10Result, error) {
	var out Q10Result
	if (req.Work == "") == (req.KnowledgeID == "") {
		return out, newFailure(KindInvalidFilter, "PM1.Q10", "Q10 requires exactly one stable reference", false, "supply either work or knowledge_id")
	}
	out.ResultMeta = q10EmptyMeta(req)
	var note CanonicalNote
	var homeProject, homeLocator, path, commit, hash, kind, title, date, status, lessonTagsJSON, summary, successor, scopeMode, manifestSchemaVersion string
	lookupID := req.Work
	// amendmentRoot keeps the caller's original root form for the opt-in
	// current_amendment_context section: qualified stays qualified.
	amendmentRoot := req.KnowledgeID
	// qualifiedProjectID remembers the Project a qualified reference named, so
	// a later negative can re-derive its canonical designation inside the
	// lookup's read snapshot instead of trusting the pre-verification read.
	qualifiedProjectID := ""
	if lookupID == "" {
		lookupID = req.KnowledgeID
		if projectID, lawID, qualified, parseErr := parseQualifiedKnowledgeID("PM1.Q10", lookupID); parseErr != nil {
			return out, parseErr
		} else if qualified {
			// CD-0200 source-qualified identity: a qualified reference names
			// its source Project, and resolves only through that Project's
			// canonical knowledge locator.
			candidates, err := projectCanonicalHomeCandidates(ctx, db, projectID)
			if err != nil {
				return out, err
			}
			if len(candidates) == 0 {
				return out, newFailure(KindKnowledgeUnavailable, "PM1.Q10", "qualified reference names a Project with no canonical-path knowledge locator", false, "designate the Project's canonical-path locator before resolving through it")
			}
			if len(candidates) > 1 {
				return out, newAmbiguousScopeFailure("PM1.Q10", "qualified reference names a Project with multiple canonical-path locators", "leave exactly one canonical Project locator", knowledgeHomeCandidateIDs(candidates))
			}
			req.Home = candidates[0]
			lookupID = lawID
			qualifiedProjectID = projectID
		}
	}
	// Note identity is scoped to the knowledge home: the same stable id can
	// exist in two homes (concord and pokeedge both number decisions CD-####).
	// A caller-supplied Home selects its own row; when that row is absent the
	// bare-id retry lets the historical-home comparison report a mismatched
	// caller home instead of a missing note. Without a Home, an id held by more
	// than one home is ambiguous rather than an arbitrary pick.
	homeSupplied := req.Home.HomeProjectID != "" || req.Home.HomeLocatorID != ""
	// CD-0200: a bare law-ID resolution reads inside the requested Product's
	// registered source set, and its ambiguity reads inside that set. Another
	// Product's law with the same ID can neither make this Product's law
	// ambiguous nor answer for it. A work lookup keeps the historical
	// whole-corpus read: an archived work note is frozen evidence whose
	// recorded home is part of its identity, so a later home designation or
	// source registration can neither hide the note nor relocate its answer.
	// A Product whose source set does not resolve (no designated home) keeps
	// the historical whole-corpus law lookup too: Q10 proves a recorded
	// locator, and the current source set governs Product-wide search, not
	// the historical note read.
	isLawLookup := req.KnowledgeID != ""
	var sourceScope []KnowledgeHome
	if !homeSupplied && req.Product != "" && isLawLookup {
		sources, srcErr := resolveKnowledgeQuerySources(ctx, db, req.Product, "PM1.Q10")
		if srcErr == nil {
			sourceScope = sources
		} else {
			var failure *Failure
			if !errors.As(srcErr, &failure) || failure.Kind != KindUnknownScope && failure.Kind != KindAmbiguousScope {
				return out, srcErr
			}
		}
	}
	sourceScopeWhere := ""
	if len(sourceScope) > 0 {
		sourceScopeWhere = ` AND (EXISTS (SELECT 1 FROM product_knowledge_homes h WHERE h.product_id = ? AND h.project_id = archived_work.home_project_id AND h.locator_id = archived_work.home_locator_id) OR EXISTS (SELECT 1 FROM product_knowledge_sources s WHERE s.product_id = ? AND s.project_id = archived_work.home_project_id AND s.locator_id = archived_work.home_locator_id))`
	}
	// CD-0200: a bare law-ID answer asserts uniqueness over the registered
	// source set, so the set must be verified before the answer is
	// authoritative. A registered source that has never indexed, or whose
	// watermark is stale or unreachable, could hold a second copy of the same
	// ID that this projection cannot see — the same population refusal Q9
	// gives. A one-element set, a caller-supplied home, a qualified
	// reference, and a work lookup verify trivially or not at all, exactly
	// as before. Contextual degradation permits an incomplete current source
	// population, not an unverified historical locator or blob. Such an
	// answer names omissions and cannot assert authoritative uniqueness.
	populationOmissions := make([]string, 0)
	// The two degradation policies stay independent (CON-830 review): the
	// historical AllowDegraded governs historical proof failures, and only
	// the section-scoped AmendmentContextAllowDegraded may relax a
	// current-source population verification. A historical allowance must
	// never bypass a strict current negative verification, and a current
	// allowance must never launder an unverified historical locator.
	populationAllowDegraded := req.AllowDegraded
	if req.IncludeAmendmentContext {
		populationAllowDegraded = req.AmendmentContextAllowDegraded
	}
	// populationProofs carries the scanned commit each verified source's
	// authority was bound to, so a later negative can seal its lookup
	// snapshot against the same identity.
	var populationProofs []KnowledgeSourceWatermark
	if len(sourceScope) > 1 && !homeSupplied {
		var populationErr error
		var proofs []KnowledgeSourceWatermark
		populationOmissions, proofs, populationErr = verifyQ10Population(ctx, db, freshen, req, sourceScope, populationAllowDegraded, "bare_id_population_unverified:")
		if populationErr != nil {
			return out, populationErr
		}
		populationProofs = proofs
	}
	// contextualNegativeSources names the current sources a contextual law
	// read must verify before it asserts a negative verdict — missing or
	// ambiguous — that the pre-lookup population loop above did not already
	// cover: a qualified reference or a caller-supplied
	// home verifies that exact home, and a bare Product-scoped reference
	// with a one-element registered set verifies that one source, because
	// an authoritative absence or uniqueness claim over an unverified
	// current source is the same defect at any set size. A multi-source
	// bare read already verified above. A contextual negative without a
	// resolvable current set refuses or explicitly names that omission.
	contextualNegativeSources := []KnowledgeHome(nil)
	contextualNegativeOmission := "bare_id_population_unverified:"
	if req.IncludeAmendmentContext && isLawLookup {
		if homeSupplied {
			contextualNegativeSources = []KnowledgeHome{req.Home}
			contextualNegativeOmission = "current_source_unverified:"
		} else if len(sourceScope) == 1 {
			contextualNegativeSources = sourceScope
		}
	}
	// applyPopulationOmissions keeps an unverified bare-ID population
	// visible on every classified result: a missing or
	// ambiguous answer over an incomplete source set is never an
	// authoritative absence or uniqueness claim, exactly as the canonical
	// path below already marks it.
	applyPopulationOmissions := func() {
		if len(populationOmissions) > 0 {
			out.Authority = "degraded"
			out.Omissions = append(out.Omissions, populationOmissions...)
		}
	}
	scanNote := func(q queryer, homeScoped bool) error {
		query := `SELECT home_project_id,home_locator_id,note_path,commit_oid,content_hash,type,title,completed_at,outcome_tag,lesson_tags,summary,COALESCE(successor_work_id,''),scope_mode,manifest_schema_version FROM archived_work WHERE id = ?`
		scanArgs := []any{lookupID}
		if homeScoped {
			query += ` AND home_project_id = ? AND home_locator_id = ?`
			scanArgs = append(scanArgs, req.Home.HomeProjectID, req.Home.HomeLocatorID)
		}
		if sourceScopeWhere != "" {
			query += sourceScopeWhere
			scanArgs = append(scanArgs, req.Product, req.Product)
		}
		return q.QueryRowContext(ctx, query, scanArgs...).Scan(&homeProject, &homeLocator, &path, &commit, &hash, &kind, &title, &date, &status, &lessonTagsJSON, &summary, &successor, &scopeMode, &manifestSchemaVersion)
	}
	runScan := func(q queryer) error {
		scanErr := scanNote(q, homeSupplied)
		if scanErr == sql.ErrNoRows && homeSupplied {
			scanErr = scanNote(q, false)
		}
		return scanErr
	}
	countHomes := func(q queryer) (int, error) {
		countQuery := `SELECT COUNT(DISTINCT home_project_id || ':' || home_locator_id) FROM archived_work WHERE id = ?`
		countArgs := []any{lookupID}
		if sourceScopeWhere != "" {
			countQuery += sourceScopeWhere
			countArgs = append(countArgs, req.Product, req.Product)
		}
		var homes int
		if scanErr := q.QueryRowContext(ctx, countQuery, countArgs...).Scan(&homes); scanErr != nil {
			return 0, wrapFailure(KindUnavailable, "PM1.Q10", "cannot inspect note home multiplicity", true, "retry once the database is readable", scanErr)
		}
		return homes, nil
	}
	err := runScan(db)
	ambiguous := false
	if !homeSupplied {
		homes, countErr := countHomes(db)
		if countErr != nil {
			return out, countErr
		}
		ambiguous = homes > 1
	}
	// A contextual law read never asserts a negative — missing or
	// ambiguous — over a current source population it has not verified. It
	// verifies and demand-freshens that population once through the
	// existing freshness owner on the pool, then retries the lookup inside
	// one read-only snapshot that is sealed against the verified
	// population: the registered set is re-derived in the snapshot and
	// compared with the verified set, and each verified source's watermark
	// is compared with the scanned commit its proof bound. A source
	// registered, removed, re-designated, or rebuilt between verification
	// and the lookup is drift: a strict read refuses, a degraded read
	// names the omission, and neither presents an authoritative negative
	// over a population it did not verify (CON-830 review). A newly
	// committed law still resolves canonically: the retry runs after the
	// demand rebuild. A canonical first answer keeps its pre-freshen
	// historical locator proof: the opt-in amendment-context section below
	// proves the current sources separately, so historical commit A stays
	// independent of current commit B. Historical-only reads take this
	// path unchanged, and no git probe runs inside the snapshot
	// (CD-0195 D2).
	if req.IncludeAmendmentContext && isLawLookup && (ambiguous || err == sql.ErrNoRows) && (homeSupplied || len(sourceScope) > 0) {
		verifiedSources := contextualNegativeSources
		if len(contextualNegativeSources) > 0 {
			omissions, proofs, populationErr := verifyQ10Population(ctx, db, freshen, req, contextualNegativeSources, req.AmendmentContextAllowDegraded, contextualNegativeOmission)
			if populationErr != nil {
				return out, populationErr
			}
			populationOmissions = append(populationOmissions, omissions...)
			populationProofs = proofs
		} else {
			// A multi-source bare read: the pre-lookup loop already
			// verified this population and collected its proofs; only
			// the sealed retry was missing.
			verifiedSources = sourceScope
		}
		tx, txErr := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if txErr != nil {
			return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot open a consistent negative-lookup read snapshot", true, "retry once the database is readable", txErr)
		}
		// Strictness derives from the caller's independent current-context
		// degradation policy, not from the omissions the pre-lookup
		// verification happened to collect: an explicitly degraded-allowed
		// first read over a healthy population names a healthy-to-degraded
		// drift as an omission instead of refusing it, matching the shared
		// refinement reader (CON-830 review).
		driftOmissions, sealErr := q10SealContextualNegative(ctx, tx, req, homeSupplied, qualifiedProjectID, verifiedSources, populationProofs, !req.AmendmentContextAllowDegraded)
		if sealErr != nil {
			tx.Rollback()
			return out, sealErr
		}
		populationOmissions = append(populationOmissions, driftOmissions...)
		scanErr := runScan(tx)
		ambiguous = false
		var countErr error
		if !homeSupplied {
			var homes int
			homes, countErr = countHomes(tx)
			ambiguous = homes > 1
		}
		if closeErr := tx.Rollback(); closeErr != nil {
			return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot close the negative-lookup read snapshot", true, "retry once the database is readable", closeErr)
		}
		if countErr != nil {
			return out, countErr
		}
		err = scanErr
	}
	if req.IncludeAmendmentContext && isLawLookup && (ambiguous || err == sql.ErrNoRows) && !homeSupplied && len(sourceScope) == 0 {
		if !req.AmendmentContextAllowDegraded {
			return out, newFailure(KindUnknownScope, "PM1.Q10", "current amendment context cannot verify a negative without a resolvable source set", false, "supply a registered Product or explicit knowledge home")
		}
		populationOmissions = append(populationOmissions, "current_source_set_unavailable")
	}
	if ambiguous {
		applyPopulationOmissions()
		out.Status, out.Result = "ambiguous", &Q10Payload{Status: "ambiguous"}
		return out, nil
	}
	if err == sql.ErrNoRows {
		status, classified, classifyErr := classifyQ10MissingNote(ctx, db, req, lookupID)
		if classifyErr != nil {
			return out, classifyErr
		}
		if classified {
			applyPopulationOmissions()
			out.Status, out.Result = status, &Q10Payload{Status: status}
			return out, nil
		}
	}
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot read the canonical note locator", true, "retry once the database is readable", err)
	}
	if err := q10ProductScopeCheck(ctx, db, req, lookupID, kind, scopeMode, homeProject, homeLocator); err != nil {
		return out, err
	}
	storedHome, locatorErr := knowledgeHomeForLocator(ctx, db, homeProject, homeLocator, "")
	if locatorErr != nil {
		applyPopulationOmissions()
		return q10HistoricalFailure(&out, req.AllowDegraded, "recorded canonical locator is unavailable", locatorErr)
	}
	if err := compareQ10HistoricalHome(req.Home, storedHome); err != nil {
		return out, err
	}
	note = CanonicalNote{HomeProjectID: homeProject, HomeLocatorID: homeLocator, NotePath: path, NotePathRef: path, Commit: commit, CommitOID: commit, ContentHash: hash}
	out.ResultMeta = q10HistoricalMeta(req)
	applyPopulationOmissions()
	if kind == "work_note" {
		verified, verifyErr := VerifyCommittedNote(ctx, storedHome.RepoPath, commit, path, hash)
		if verifyErr != nil {
			return q10HistoricalFailure(&out, req.AllowDegraded, "recorded canonical note proof is unavailable", verifyErr)
		}
		if verified.ID != lookupID || verified.Kind != "work_note" {
			out.Status, out.Result = "ambiguous", &Q10Payload{Status: "ambiguous"}
			return out, nil
		}
	} else if stop, err := verifyQ10ManifestRecord(ctx, db, readManifest.orDirect(), req, lookupID, kind, status, date, title, summary, successor, hash, scopeMode, lessonTagsJSON, manifestSchemaVersion, homeProject, homeLocator, path, commit, storedHome.RepoPath, &out); stop || err != nil {
		return out, err
	}
	payload := &Q10Payload{Status: "canonical", Note: &note, SuccessorID: successor}
	if law := KnowledgeLawStatus(kind, status); law != "" {
		payload.LawStatus = law
	}
	out.Status, out.Note, out.Result = "canonical", &note, payload
	// CON-830: the opt-in current_amendment_context section rides only a
	// canonically verified law record and carries its own current-source
	// proof through the same store-owned refinement query. Work notes and
	// historical-only reads keep the shape above untouched. The current
	// context reads the Product's whole registered source set — the set is
	// a property of the Product, not of the root form, so a qualified root
	// resolves its historical locator through one source while its current
	// context still spans every registered source (CD-0200 D4/D5). The
	// root form itself keeps the caller's qualification: a qualified root
	// stays scoped to its Project's canonical home inside the context read,
	// never a bare federated lookup that same-ID peer sources would turn
	// ambiguous.
	if req.IncludeAmendmentContext && payload.LawStatus != "" {
		amendment, amendmentErr := q10CurrentAmendmentContext(ctx, db, freshen, req, storedHome, sourceScope, amendmentRoot)
		if amendmentErr != nil {
			return out, amendmentErr
		}
		payload.CurrentAmendmentContext = &amendment
	}
	return out, nil
}

// verifyQ10Population uses the same source verifier for uniqueness and
// classified negatives. Historical reads verify their recorded sources;
// contextual reads route through the one shared current-source proof owner
// — watermark and head verification with demand freshening plus the live
// object proof of the relation-projected endpoint subjects — so a
// contextual negative can never claim an authoritative absence over a
// source whose current objects are missing or tampered (CON-830 review).
// The returned watermarks carry each source's scanned commit and authority,
// so a later negative can seal its lookup snapshot against the same
// identity.
func verifyQ10Population(ctx context.Context, db *sql.DB, freshen func(context.Context, KnowledgeHome) error, req Q10Request, sources []KnowledgeHome, allowDegraded bool, omissionPrefix string) ([]string, []KnowledgeSourceWatermark, error) {
	if req.IncludeAmendmentContext {
		verification, err := verifyKnowledgeContextSourceSet(ctx, db, freshen, sources, allowDegraded, omissionPrefix, "PM1.Q10")
		if err != nil {
			return nil, nil, err
		}
		return append([]string{}, verification.omissions...), verification.watermarks, nil
	}
	omissions := make([]string, 0)
	watermarks := make([]KnowledgeSourceWatermark, 0, len(sources))
	for _, source := range sources {
		scanned, authority, err := validateKnowledgeHomeForQueryCore(ctx, db, source, allowDegraded, "PM1.Q10")
		if err != nil {
			return nil, nil, err
		}
		if authority != "authoritative" {
			omissions = append(omissions, omissionPrefix+source.HomeProjectID+"/"+source.HomeLocatorID)
		}
		watermarks = append(watermarks, KnowledgeSourceWatermark{ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID, Watermark: scanned, Authority: authority})
	}
	return omissions, watermarks, nil
}

// q10SealContextualNegative seals a classified contextual negative against
// its lookup read snapshot. It re-derives, inside the caller's read-only
// transaction, the registered population the negative claims to have
// verified — the Product's registered source set for a bare read, or the
// Project's canonical knowledge locators for a qualified read — and compares
// it with the set the pool verifier proved, then checks each verified
// source's watermark against the scanned commit its proof bound. Drift
// refuses a read whose caller refused current-context degradation and
// names one omission per finding on a read that explicitly allows it —
// the same policy the shared refinement reader applies — so a
// healthy-to-degraded first read degrades instead of refusing, and
// neither can present an authoritative negative (CON-830 review, PM1 Q10
// proof separation). Every read stays on
// the caller's queryer: no git probe and no nested pool read runs here
// (CD-0195 D2, single-connection invariant).
func q10SealContextualNegative(ctx context.Context, q queryer, req Q10Request, homeSupplied bool, qualifiedProjectID string, verified []KnowledgeHome, proofs []KnowledgeSourceWatermark, strict bool) ([]string, error) {
	omissions := make([]string, 0)
	degrade := func(omission string) {
		omissions = append(omissions, omission)
	}
	refuse := func(detail string) error {
		return newFailure(KindStaleContext, "PM1.Q10", "the verified current source population changed before the negative lookup: "+detail, true, "re-verify the current knowledge sources and retry the read")
	}
	drift := func(detail string, omission string) error {
		if strict {
			return refuse(detail)
		}
		degrade(omission)
		return nil
	}
	verifiedLabels := func(homes []KnowledgeHome) []string {
		labels := make([]string, 0, len(homes))
		for _, home := range homes {
			labels = append(labels, home.HomeProjectID+"/"+home.HomeLocatorID)
		}
		return labels
	}
	if !homeSupplied && req.Product != "" {
		current, srcErr := resolveKnowledgeQuerySources(ctx, q, req.Product, "PM1.Q10")
		if srcErr != nil {
			var failure *Failure
			if errors.As(srcErr, &failure) && (failure.Kind == KindUnknownScope || failure.Kind == KindAmbiguousScope) {
				if err := drift("the registered source set is unresolved for "+req.Product, "current_source_set_unresolved:"+req.Product); err != nil {
					return nil, err
				}
			} else {
				return nil, srcErr
			}
		} else {
			gained, lost := refinementSourceSetDrift(verifiedLabels(current), verifiedLabels(verified))
			for _, label := range gained {
				if err := drift("gained "+label, "source_set_gained_unverified_source:"+label); err != nil {
					return nil, err
				}
			}
			for _, label := range lost {
				if err := drift("removed "+label, "source_set_member_unregistered:"+label); err != nil {
					return nil, err
				}
			}
		}
	}
	if qualifiedProjectID != "" {
		candidates, candErr := projectCanonicalHomeCandidates(ctx, q, qualifiedProjectID)
		if candErr != nil {
			if err := drift("the canonical designation for "+qualifiedProjectID+" is unresolved", "current_source_designation_drift:"+qualifiedProjectID); err != nil {
				return nil, err
			}
		} else if len(candidates) != 1 || candidates[0].HomeProjectID != req.Home.HomeProjectID || candidates[0].HomeLocatorID != req.Home.HomeLocatorID {
			if err := drift("the canonical designation for "+qualifiedProjectID+" changed", "current_source_designation_drift:"+qualifiedProjectID); err != nil {
				return nil, err
			}
		}
	}
	watermarkDrifts, _ := refinementWatermarkDrift(ctx, q, verified, refinementSourceVerification{watermarks: proofs})
	for _, drifted := range watermarkDrifts {
		if strict {
			return nil, refuse(drifted.label + " was verified at " + drifted.proof + " but the read snapshot holds " + drifted.current)
		}
		degrade("source_snapshot_drift:" + drifted.label + ":" + drifted.proof + "->" + drifted.current)
	}
	return omissions, nil
}

// q10CurrentAmendmentContext resolves and proves the current source set
// independently of the already verified historical note.
func q10CurrentAmendmentContext(ctx context.Context, db *sql.DB, freshen func(context.Context, KnowledgeHome) error, req Q10Request, storedHome KnowledgeHome, sourceScope []KnowledgeHome, amendmentRoot string) (KnowledgeRefinementContextResult, error) {
	amendmentSources := sourceScope
	var seededOmissions []string
	if len(amendmentSources) == 0 && req.Product != "" {
		if sources, srcErr := resolveKnowledgeQuerySources(ctx, db, req.Product, "PM1.Q10.amendment_context"); srcErr == nil {
			amendmentSources = sources
		} else {
			var failure *Failure
			if !errors.As(srcErr, &failure) || failure.Kind != KindUnknownScope && failure.Kind != KindAmbiguousScope {
				return KnowledgeRefinementContextResult{}, srcErr
			}
			if !req.AmendmentContextAllowDegraded {
				return KnowledgeRefinementContextResult{}, srcErr
			}
			seededOmissions = append(seededOmissions, "current_source_set_unresolved:"+req.Product)
		}
	}
	if len(amendmentSources) == 0 {
		amendmentSources = []KnowledgeHome{storedHome}
	}
	return queryKnowledgeRefinementContextDB(ctx, db, freshen, KnowledgeRefinementContextRequest{
		Product: req.Product, Roots: []string{amendmentRoot}, Limit: req.AmendmentContextLimit,
		Cursor: req.AmendmentContextCursor, AllowDegraded: req.AmendmentContextAllowDegraded,
		Sources: amendmentSources, SeededOmissions: seededOmissions,
	})
}

// verifyQ10ManifestRecord rebuilds one manifest-indexed record from its
// archived projection rows, checks it against the historical manifest, and
// verifies the committed declaration. readManifest serves the committed
// manifest; the store passes its commit-keyed memo so a repeated read of
// one immutable commit does not re-compose and re-validate the whole
// record corpus, while the per-record declaration and blob proof still
// runs against git on every read. A historical failure reports through
// the out parameter's degraded allowance; stop says the caller must return
// the out as it now stands instead of continuing to the canonical result.
func verifyQ10ManifestRecord(ctx context.Context, db *sql.DB, readManifest knowledgeManifestReader, req Q10Request, lookupID, kind, status, date, title, summary, successor, hash, scopeMode, lessonTagsJSON, manifestSchemaVersion, homeProject, homeLocator, path, commit, repoPath string, out *Q10Result) (bool, error) {
	var tags []string
	if err := json.Unmarshal([]byte(lessonTagsJSON), &tags); err != nil {
		return true, newFailure(KindInvariantViolation, "PM1.Q10", "indexed manifest tags are malformed", false, "rebuild the git-derived knowledge index")
	}
	manifestRole, roleErr := resolveKnowledgeManifestRole(ctx, db, KnowledgeHome{HomeProjectID: homeProject, HomeLocatorID: homeLocator})
	if roleErr != nil {
		return true, roleErr
	}
	manifest, missing, manifestErr := readManifest(ctx, repoPath, commit, manifestRole)
	if manifestErr != nil || missing {
		if manifestErr == nil {
			manifestErr = newFailure(KindInvalidNoteProof, "PM1.Q10", "recorded manifest is missing at the historical commit", false, "restore the committed manifest")
		}
		_, err := q10HistoricalFailure(out, req.AllowDegraded, "recorded manifest schema is unavailable", manifestErr)
		return true, err
	}
	if manifestSchemaVersion != "" && manifestSchemaVersion != manifest.SchemaVersion {
		return true, newFailure(KindInvariantViolation, "PM1.Q10", "indexed manifest schema version disagrees with the historical manifest", false, "rebuild the git-derived knowledge index")
	}
	record := KnowledgeRecord{ID: lookupID, Kind: kind, Path: path, Status: status, Date: date, Title: title, Summary: summary, Tags: tags, Scopes: KnowledgeRecordScopes{Mode: scopeMode}, Successor: successor, SHA256: hash}
	for _, scope := range []struct {
		table, column string
		target        *[]string
	}{{"archived_work_products", "product_id", &record.Scopes.ProductIDs}, {"archived_work_projects", "project_id", &record.Scopes.ProjectIDs}, {"archived_work_tags", "tag_id", &record.Scopes.TagIDs}} {
		values, queryErr := archivedScopeIDs(ctx, db, scope.table, scope.column, lookupID, homeProject, homeLocator)
		if queryErr != nil {
			return true, queryErr
		}
		*scope.target = values
	}
	values, queryErr := archivedScopeIDs(ctx, db, "archived_work_domains", "domain_id", lookupID, homeProject, homeLocator)
	if queryErr != nil {
		return true, queryErr
	}
	record.Scopes.DomainIDs = values
	if manifestLawBearingKinds[kind] {
		if err := db.QueryRowContext(ctx, `SELECT domain_id,product_wide_rationale FROM law_domain_homes WHERE home_project_id=? AND home_locator_id=? AND law_id=? AND law_content_hash=?`, homeProject, homeLocator, lookupID, hash).Scan(&record.HomeDomainID, &record.ProductWideRationale); err != nil {
			return true, q10LawDomainProjectionFailure(err)
		}
		record.homeDomainPresent = true
		record.productWideRationalePresent = record.ProductWideRationale != ""
		applicability, applicabilityErr := archivedLawApplicability(ctx, db, homeProject, homeLocator, lookupID)
		if applicabilityErr != nil {
			return true, applicabilityErr
		}
		record.AppliesToDomainIDs, record.appliesToDomainsPresent = applicability, true
	}
	if err := verifyManifestDeclaration(ctx, manifest, repoPath, commit, record); err != nil {
		_, verifyErr := q10HistoricalFailure(out, req.AllowDegraded, "recorded manifest declaration or blob could not be verified", err)
		return true, verifyErr
	}
	return false, nil
}

// classifyQ10MissingNote classifies an absent archived note: a live work
// reference is not_compacted, everything else is missing. classified is
// false only when the live-work probe itself failed.
func classifyQ10MissingNote(ctx context.Context, db *sql.DB, req Q10Request, lookupID string) (string, bool, error) {
	if req.Work != "" {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM work_items WHERE id = ?)`, lookupID).Scan(&exists); err != nil {
			return "", false, wrapFailure(KindUnavailable, "PM1.Q10", "cannot inspect live work", true, "retry once the database is readable", err)
		}
		if exists {
			return "not_compacted", true, nil
		}
	}
	return "missing", true, nil
}

// q10ProductScopeCheck refuses a note outside the requested Product scope:
// explicit-scoped and work notes carry archived Product membership rows,
// home-scoped notes ride the home Project's membership.
func q10ProductScopeCheck(ctx context.Context, db *sql.DB, req Q10Request, lookupID, kind, scopeMode, homeProject, homeLocator string) error {
	if req.Product == "" {
		return nil
	}
	var inScope bool
	if kind == "work_note" || scopeMode == "explicit" {
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM archived_work_products WHERE work_id=? AND product_id=? AND home_project_id=? AND home_locator_id=?)`, lookupID, req.Product, homeProject, homeLocator).Scan(&inScope); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10", "cannot validate knowledge Product scope", true, "retry once the database is readable", err)
		}
	} else if scopeMode == "home" {
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, req.Product, homeProject).Scan(&inScope); err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10", "cannot validate knowledge Product scope", true, "retry once the database is readable", err)
		}
	}
	if !inScope {
		return unknownScope("PM1.Q10", "knowledge note is not in the requested Product scope")
	}
	return nil
}

func archivedScopeIDs(ctx context.Context, q queryer, table, column, workID, homeProject, homeLocator string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+column+" FROM "+table+" WHERE work_id=? AND home_project_id=? AND home_locator_id=? ORDER BY "+column, workID, homeProject, homeLocator)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "PM1.Q10", "cannot read manifest record scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func archivedLawApplicability(ctx context.Context, q queryer, homeProject, homeLocator, lawID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT domain_id FROM law_domain_applicability WHERE home_project_id=? AND home_locator_id=? AND law_id=? ORDER BY domain_id`, homeProject, homeLocator, lawID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "PM1.Q10", "cannot read law Domain applicability", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func q10LawDomainProjectionFailure(err error) error {
	if err == sql.ErrNoRows {
		return newFailure(KindInvariantViolation, "PM1.Q10", "indexed Domain law projection is incomplete", false, "rebuild the git-derived knowledge index")
	}
	return wrapFailure(KindUnavailable, "PM1.Q10", "cannot read law Domain home", true, "retry once the database is readable", err)
}

func compareQ10HistoricalHome(supplied, stored KnowledgeHome) error {
	if supplied.HomeProjectID != "" && supplied.HomeProjectID != stored.HomeProjectID || supplied.HomeLocatorID != "" && supplied.HomeLocatorID != stored.HomeLocatorID || supplied.RepoPath != "" && supplied.RepoPath != stored.RepoPath {
		return newFailure(KindInvalidFilter, "PM1.Q10", "caller KnowledgeHome does not match the recorded historical locator", false, "omit Home or supply the recorded locator evidence")
	}
	return nil
}

func q10HistoricalMeta(req Q10Request) ResultMeta {
	now := time.Now().UTC()
	return ResultMeta{QueryID: "PM1.Q10", ContractVersion: queryContractVersion, ResolvedScope: ResolvedScope{ProductID: req.Product, WorkID: req.Work}, Authority: "authoritative", Freshness: Freshness{ObservedAt: now.Format(time.RFC3339Nano), Age: 0, Stale: false}, OrderingKeys: []string{"canonical_locator"}, Omissions: []string{}, Warnings: []string{"historical_locator_commit", "current_head_not_used_for_proof"}}
}

func q10EmptyMeta(req Q10Request) ResultMeta {
	now := time.Now().UTC()
	return ResultMeta{QueryID: "PM1.Q10", ContractVersion: queryContractVersion, ResolvedScope: ResolvedScope{ProductID: req.Product, WorkID: req.Work}, Authority: "authoritative", Freshness: Freshness{ObservedAt: now.Format(time.RFC3339Nano), Age: 0, Stale: false}, OrderingKeys: []string{"canonical_locator"}, Omissions: []string{}, Warnings: []string{"historical_locator_not_required_for_empty_result"}}
}

func q10HistoricalFailure(out *Q10Result, allowDegraded bool, detail string, err error) (Q10Result, error) {
	failure := classifyQ10HistoricalFailure(detail, err)
	if allowDegraded {
		out.Authority, out.Status = "degraded", "missing"
		out.Warnings = append(out.Warnings, detail)
		out.Result = &Q10Payload{Status: "missing"}
		return *out, nil
	}
	return *out, failure
}

func classifyQ10HistoricalFailure(detail string, err error) error {
	var failure *Failure
	if errors.As(err, &failure) {
		classified := *failure
		classified.Op = "PM1.Q10"
		switch classified.Kind {
		case KindUnknownScope:
			classified.Kind = KindKnowledgeUnavailable
		case KindGitUnreachable, KindUnreachable:
			classified.Kind = KindUnreachable
		case KindInvalidNoteProof:
			if classified.Err != nil {
				classified.Kind = KindUnreachable
			} else {
				classified.Kind = KindKnowledgeMissing
			}
		}
		classified.Detail = detail + ": " + classified.Detail
		return &classified
	}
	return wrapFailure(KindKnowledgeUnavailable, "PM1.Q10", detail, true, "restore the recorded locator or git proof and retry", err)
}

func knowledgeLimit(limit int) (int, error) {
	if limit == 0 {
		return 20, nil
	}
	if limit < 1 || limit > knowledgeQueryLimit {
		return 0, newFailure(KindInvalidFilter, "PM1.Q9", "limit must be between 1 and 100", false, "supply a bounded knowledge limit")
	}
	return limit, nil
}

func knowledgeKinds(values []string) ([]string, error) {
	values = orderedStrings(nonEmptyStrings(values))
	for _, value := range values {
		if !knowledgeKindsClosed[value] {
			return nil, newFailure(KindInvalidFilter, "PM1.Q9", "unknown knowledge kind "+value, false, "use one of "+strings.Join(sortedKnowledgeKinds(), ", "))
		}
	}
	return values, nil
}

type knowledgeCursor struct {
	Version                               int `json:"version"`
	Product, Project, Domain, Text        string
	Since, Until                          string
	Kinds, Tags                           []string
	HomeProjectID, HomeLocatorID, HeadRef string
	MatchClass                            int `json:"match_class"`
	CompletedAt, ID                       string
}

func encodeKnowledgeCursor(cursor knowledgeCursor) (string, error) {
	b, err := json.Marshal(cursor)
	if err != nil {
		return "", wrapFailure(KindInvalidCursor, "PM1.Q9", "cannot encode the knowledge cursor", false, "restart the bounded knowledge query", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeKnowledgeCursor(raw string, req Q9Request, kinds, tags []string) (knowledgeCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	var cursor knowledgeCursor
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.Version != 2 || cursor.Product != req.Product || cursor.Project != req.Project || cursor.Domain != req.Domain || cursor.Text != req.Text || cursor.Since != req.Since || cursor.Until != req.Until || cursor.HomeProjectID != req.Home.HomeProjectID || cursor.HomeLocatorID != req.Home.HomeLocatorID || cursor.HeadRef != req.Home.HeadRef || !equalStrings(cursor.Kinds, kinds) || !equalStrings(cursor.Tags, tags) || cursor.MatchClass < 0 || cursor.MatchClass > 2 || req.Text == "" && cursor.MatchClass != 0 || cursor.CompletedAt == "" || cursor.ID == "" {
		return knowledgeCursor{}, newFailure(KindInvalidCursor, "PM1.Q9", "cursor does not match the requested knowledge query", false, "use a cursor returned for the same query and filters")
	}
	return cursor, nil
}

// knowledgeResumeKey is the cursor-format-agnostic paging position. The v2
// single-home cursor and the v3 federated cursor (CD-0200) both decode into
// it; the bounded SQL predicate is identical. The source identity rides the
// key, so two sources holding the same law ID page as two records instead of
// one swallowing the other.
type knowledgeResumeKey struct {
	MatchClass    int
	CompletedAt   string
	ID            string
	HomeProjectID string
	HomeLocatorID string
}

func knowledgeResumeKeyFromCursor(cursor knowledgeCursor) *knowledgeResumeKey {
	return &knowledgeResumeKey{MatchClass: cursor.MatchClass, CompletedAt: cursor.CompletedAt, ID: cursor.ID, HomeProjectID: cursor.HomeProjectID, HomeLocatorID: cursor.HomeLocatorID}
}

// federatedKnowledgeCursor pages a Product-wide Q9 over a registered source
// set. The cursor binds to the SHA-256 digest of the source set it paged
// over and to the requested Project filter, so a registration, a removal, or
// a scope change between pages invalidates the outstanding cursor instead of
// silently changing coverage (CD-0200). The recorded source identity makes a
// colliding ID in two sources two records, not one.
type federatedKnowledgeCursor struct {
	Version                        int `json:"version"`
	Product, Project, Domain, Text string
	Since, Until                   string
	Kinds, Tags                    []string
	SourcesDigest                  string
	HomeProjectID, HomeLocatorID   string
	MatchClass                     int `json:"match_class"`
	CompletedAt, ID                string
}

func encodeFederatedKnowledgeCursor(cursor federatedKnowledgeCursor) (string, error) {
	b, err := json.Marshal(cursor)
	if err != nil {
		return "", wrapFailure(KindInvalidCursor, "PM1.Q9", "cannot encode the federated knowledge cursor", false, "restart the bounded knowledge query", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeFederatedKnowledgeCursor(raw string, req Q9Request, kinds, tags []string, sourcesDigest string) (federatedKnowledgeCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	var cursor federatedKnowledgeCursor
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.Version != 3 || cursor.Product != req.Product || cursor.Project != req.Project || cursor.Domain != req.Domain || cursor.Text != req.Text || cursor.Since != req.Since || cursor.Until != req.Until || cursor.SourcesDigest != sourcesDigest || !equalStrings(cursor.Kinds, kinds) || !equalStrings(cursor.Tags, tags) || cursor.MatchClass < 0 || cursor.MatchClass > 2 || req.Text == "" && cursor.MatchClass != 0 || cursor.CompletedAt == "" || cursor.ID == "" || cursor.HomeProjectID == "" || cursor.HomeLocatorID == "" {
		return federatedKnowledgeCursor{}, newFailure(KindInvalidCursor, "PM1.Q9", "cursor does not match the requested knowledge query or the Product's current source set", false, "restart the query without the cursor, or use a cursor returned for the same source set")
	}
	return cursor, nil
}

// queryQ9Federated runs one bounded Q9 request over the Product's full
// registered source set (CD-0200). Each source's watermark is verified
// separately; an unreachable or stale source refuses the whole answer unless
// the caller allows degradation, in which case the answer carries an explicit
// omission per missing source and never reads as an authoritative negative.
// Items merge under the global accepted ordering
// (structured_match, completed_at desc, id), so a single-source Product
// running through this path would return exactly the single-home answer.
func queryQ9Federated(ctx context.Context, db *sql.DB, req Q9Request, sources []KnowledgeHome, observedAt time.Time) (Q9Result, error) {
	var out Q9Result
	limit, err := knowledgeLimit(req.Limit)
	if err != nil {
		return out, err
	}
	kinds, err := knowledgeKinds(req.Kinds)
	if err != nil {
		return out, err
	}
	tags := orderedStrings(nonEmptyStrings(req.Tags))
	digest := knowledgeSourceSetDigest(sources)
	var resume *knowledgeResumeKey
	if req.Cursor != "" {
		cursor, err := decodeFederatedKnowledgeCursor(req.Cursor, req, kinds, tags, digest)
		if err != nil {
			return out, err
		}
		resume = knowledgeResumeKeyFromCursor(knowledgeCursor{MatchClass: cursor.MatchClass, CompletedAt: cursor.CompletedAt, ID: cursor.ID, HomeProjectID: cursor.HomeProjectID, HomeLocatorID: cursor.HomeLocatorID})
	}
	paged := req
	paged.Cursor = ""
	merged := make([]KnowledgeItem, 0, limit)
	watermarks := make([]KnowledgeSourceWatermark, 0, len(sources))
	omissions := make([]string, 0)
	degraded := false
	for _, source := range sources {
		label := source.HomeProjectID + "/" + source.HomeLocatorID
		scanned, authority, err := validateKnowledgeHomeForQueryCore(ctx, db, source, req.AllowDegraded, "PM1.Q9")
		if err != nil {
			return out, federatedSourceFailure(err, label)
		}
		verdict := scanned
		if authority != "authoritative" {
			verdict = "unreachable"
			if scanned != "" {
				verdict = scanned
			}
			degraded = true
			omissions = append(omissions, "knowledge_source_degraded:"+label)
		}
		watermarks = append(watermarks, KnowledgeSourceWatermark{ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID, Watermark: verdict, Authority: authority})
		if authority != "authoritative" {
			continue
		}
		if err := validateKnowledgeCoverageCore(ctx, db, source, scanned, kinds); err != nil {
			return out, err
		}
		for _, omission := range knowledgeCoverageOmissions(ctx, db, source, scanned) {
			omissions = append(omissions, label+":"+omission)
		}
		paged.Home = source
		query, args := buildKnowledgeQueryForScope(paged, kinds, tags, limit, resume)
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return out, wrapFailure(KindUnavailable, "PM1.Q9", "cannot search the git knowledge index of source "+label, true, "retry once the database is readable", err)
		}
		items, err := scanKnowledgeRows(rows)
		if err != nil {
			return out, err
		}
		merged = append(merged, items...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.MatchClass != b.MatchClass {
			return a.MatchClass < b.MatchClass
		}
		if a.CompletedAt != b.CompletedAt {
			return a.CompletedAt > b.CompletedAt
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		// CD-0200: two sources may hold the same law ID. The source identity
		// breaks the tie, so the colliding records order deterministically
		// and the continuation key below can name each one.
		if a.HomeProjectID != b.HomeProjectID {
			return a.HomeProjectID < b.HomeProjectID
		}
		return a.HomeLocatorID < b.HomeLocatorID
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	var cursor *string
	if len(merged) == limit {
		last := merged[len(merged)-1]
		encoded, err := encodeFederatedKnowledgeCursor(federatedKnowledgeCursor{Version: 3, Product: req.Product, Project: req.Project, Domain: req.Domain, Text: req.Text, Since: req.Since, Until: req.Until, Kinds: kinds, Tags: tags, SourcesDigest: digest, HomeProjectID: last.HomeProjectID, HomeLocatorID: last.HomeLocatorID, MatchClass: last.MatchClass, CompletedAt: last.CompletedAt, ID: last.ID})
		if err != nil {
			return out, err
		}
		cursor = &encoded
	}
	authority := "authoritative"
	if degraded {
		authority = "degraded"
	}
	meta := knowledgeWatermarkMeta("PM1.Q9", "", authority, observedAt)
	meta.ResolvedScope = ResolvedScope{ProductID: req.Product, ProjectID: req.Project}
	meta.Omissions = omissions
	meta.NextCursor = cursor
	out.ResultMeta = meta
	out.Items = merged
	out.IndexWatermark = watermarks[0].Watermark
	out.SourceWatermarks = watermarks
	return out, nil
}

// federatedSourceFailure names the source a federated refusal belongs to, so
// an operator sees which registered source refused rather than a bare
// authority verdict.
func federatedSourceFailure(err error, label string) error {
	var failure *Failure
	if failureAs(err, &failure) {
		classified := *failure
		classified.Detail = "knowledge source " + label + ": " + classified.Detail
		return &classified
	}
	return err
}

func scanKnowledgeRows(rows *sql.Rows) ([]KnowledgeItem, error) {
	defer rows.Close()
	items := make([]KnowledgeItem, 0)
	for rows.Next() {
		item, err := scanKnowledgeItemForScope(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "PM1.Q9", "cannot finish the knowledge index query", true, "retry once the database is readable", err)
	}
	return items, nil
}

func buildKnowledgeQueryForScope(req Q9Request, kinds, tags []string, limit int, resume *knowledgeResumeKey) (string, []any) {
	where := []string{"aw.home_project_id = ?", "aw.home_locator_id = ?"}
	args := []any{req.Text, req.Home.HomeProjectID, req.Home.HomeLocatorID}
	if req.Product != "" {
		where = append(where, "(aw.scope_mode = 'home' OR EXISTS (SELECT 1 FROM archived_work_products p WHERE p.work_id = aw.id AND p.home_project_id = aw.home_project_id AND p.home_locator_id = aw.home_locator_id AND p.product_id = ?))")
		args = append(args, req.Product)
	}
	if req.Project != "" {
		where = append(where, "(aw.scope_mode = 'home' OR EXISTS (SELECT 1 FROM archived_work_projects p WHERE p.work_id = aw.id AND p.home_project_id = aw.home_project_id AND p.home_locator_id = aw.home_locator_id AND p.project_id = ?))")
		args = append(args, req.Project)
	}
	// One law Domain membership expression decides that a record belongs to a
	// Domain: the note's declared Domain scopes, the law's home Domain, or the
	// law's declared applicability (the same projection Q10 reads). It drives
	// the Domain filter, the exact Domain text match, and the returned
	// domain_ids. A Domain matches only itself.
	domainMember := func(predicate func(column string) string) string {
		return `(EXISTS (SELECT 1 FROM archived_work_domains d WHERE d.work_id = aw.id AND d.home_project_id = aw.home_project_id AND d.home_locator_id = aw.home_locator_id AND ` + predicate("d.domain_id") + `)` +
			` OR EXISTS (SELECT 1 FROM law_domain_homes h WHERE h.home_project_id = aw.home_project_id AND h.home_locator_id = aw.home_locator_id AND h.law_id = aw.id AND ` + predicate("h.domain_id") + `)` +
			` OR EXISTS (SELECT 1 FROM law_domain_applicability a WHERE a.home_project_id = aw.home_project_id AND a.home_locator_id = aw.home_locator_id AND a.law_id = aw.id AND ` + predicate("a.domain_id") + `))`
	}
	if req.Domain != "" {
		where = append(where, domainMember(func(column string) string { return column + " = ?" }))
		args = append(args, req.Domain, req.Domain, req.Domain)
	}
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, kind := range kinds {
			placeholders[i], args = "?", append(args, kind)
		}
		where = append(where, "aw.type IN ("+strings.Join(placeholders, ",")+")")
	}
	for _, tag := range tags {
		where = append(where, "(EXISTS (SELECT 1 FROM archived_work_tags t WHERE t.work_id = aw.id AND t.home_project_id = aw.home_project_id AND t.home_locator_id = aw.home_locator_id AND t.tag_id = ?) OR (aw.type <> 'work_note' AND EXISTS (SELECT 1 FROM json_each(aw.lesson_tags) WHERE value = ?)))")
		args = append(args, tag, tag)
	}
	exactScopeMatch := domainMember(func(column string) string { return "lower(" + column + ") = lower(input.text)" })
	exactMatch := `(lower(aw.id) = lower(input.text)
		OR lower(aw.title) = lower(input.text)
		OR EXISTS (SELECT 1 FROM archived_work_tags exact_tag WHERE exact_tag.work_id = aw.id AND exact_tag.home_project_id = aw.home_project_id AND exact_tag.home_locator_id = aw.home_locator_id AND lower(exact_tag.tag_id) = lower(input.text))
		OR EXISTS (SELECT 1 FROM json_each(aw.lesson_tags) exact_lesson_tag WHERE lower(exact_lesson_tag.value) = lower(input.text))
		OR (` + exactScopeMatch + `))`
	boundedTextMatch := `(instr(lower(aw.title), lower(input.text)) > 0 OR instr(lower(aw.summary), lower(input.text)) > 0)`
	bodyTextMatch := `EXISTS (SELECT 1 FROM law_bodies lb WHERE lb.home_project_id = aw.home_project_id AND lb.home_locator_id = aw.home_locator_id AND lb.law_id = aw.id AND instr(lower(lb.body), lower(input.text)) > 0)`
	where = append(where, `(input.text = '' OR `+exactMatch+` OR `+boundedTextMatch+` OR `+bodyTextMatch+`)`)
	if req.Since != "" {
		where = append(where, "aw.completed_at >= ?")
		args = append(args, req.Since)
	}
	if req.Until != "" {
		where = append(where, "aw.completed_at <= ?")
		args = append(args, req.Until)
	}
	cursorWhere := ""
	if resume != nil {
		// The continuation key carries the record's source identity, so a
		// colliding ID in two registered sources resumes past exactly the
		// records already returned (CD-0200). In a single-home query the key's
		// identity equals the query's own home, which keeps the predicate's
		// inclusion set identical to the home-only form it replaces.
		cursorWhere = " WHERE (aw.match_class > ? OR (aw.match_class = ? AND (aw.completed_at < ? OR (aw.completed_at = ? AND (aw.id > ? OR (aw.id = ? AND (aw.home_project_id > ? OR (aw.home_project_id = ? AND aw.home_locator_id > ?))))))))"
		args = append(args, resume.MatchClass, resume.MatchClass, resume.CompletedAt, resume.CompletedAt, resume.ID, resume.ID, resume.HomeProjectID, resume.HomeProjectID, resume.HomeLocatorID)
	}
	args = append(args, limit)
	scopeSelect := `COALESCE((SELECT json_group_array(domain_id) FROM (` +
		`SELECT domain_id FROM archived_work_domains WHERE work_id=aw.id AND home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id ` +
		`UNION SELECT domain_id FROM law_domain_homes WHERE home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id AND law_id=aw.id ` +
		`UNION SELECT domain_id FROM law_domain_applicability WHERE home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id AND law_id=aw.id ` +
		`ORDER BY domain_id)), '[]'),`
	return `WITH input(text) AS (VALUES (?)), ranked AS (` +
		`SELECT aw.*, CASE WHEN input.text = '' OR ` + exactMatch + ` THEN 0 WHEN ` + boundedTextMatch + ` THEN 1 ELSE 2 END AS match_class ` +
		`FROM archived_work aw CROSS JOIN input WHERE ` + strings.Join(where, " AND ") + `) ` +
		`SELECT aw.id,aw.type,aw.title,aw.completed_at,aw.outcome_tag,COALESCE(aw.successor_work_id,''),aw.lesson_tags,aw.summary,aw.home_project_id,aw.home_locator_id,aw.note_path,aw.commit_oid,aw.content_hash,aw.scope_mode,` +
		`COALESCE((SELECT json_group_array(product_id) FROM (SELECT product_id FROM archived_work_products WHERE work_id=aw.id AND home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id ORDER BY product_id)), '[]'),` +
		`COALESCE((SELECT json_group_array(project_id) FROM (SELECT project_id FROM archived_work_projects WHERE work_id=aw.id AND home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id ORDER BY project_id)), '[]'),` +
		scopeSelect +
		`COALESCE((SELECT json_group_array(tag_id) FROM (SELECT tag_id FROM archived_work_tags WHERE work_id=aw.id AND home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id ORDER BY tag_id)), '[]'),aw.match_class ` +
		`FROM ranked aw` + cursorWhere + ` ORDER BY aw.match_class ASC, aw.completed_at DESC, aw.id ASC LIMIT ?`, args
}
