package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// KnowledgeDemandFailure is one suppressed freshness-demand outcome the
// search handler defers to Q9's owning per-source degraded path (CD-0159 D6,
// CD-0020 D3 as amended by CD-0200 D3). It is handler-to-query state, not
// client input: the identities are exact source Project/locator pairs and the
// cause is the existing failure kind the demand refused with.
type KnowledgeDemandFailure struct {
	ProjectID   string
	LocatorID   string
	FailureKind string
}

type Q9Request struct {
	Product       string
	Project       string
	Domain        string
	Kinds         []string
	Tags          []string
	Text          string
	Limit         int
	Cursor        string
	AllowDegraded bool
	Home          KnowledgeHome
	// DemandFailures carries the per-source freshness-demand failures the
	// handler suppressed under AllowDegraded. Each named source degrades
	// through the same per-source path a stale watermark uses.
	DemandFailures []KnowledgeDemandFailure
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
	Items            []KnowledgeItem           `json:"items"`
	Publications     []KnowledgePublication    `json:"publications"`
	IndexWatermark   string                    `json:"index_watermark"`
	SourceOmissions  []KnowledgeSourceOmission `json:"source_omissions,omitempty"`
	publicationReads []knowledgePublicationRead
}

// knowledgePublicationRead retains the query owner's exact source and served
// revision until its database cursors and proof transactions have closed.
type knowledgePublicationRead struct {
	home       KnowledgeHome
	commit     string
	historical bool
}

// KnowledgeSourceOmission carries identities separately from the bounded
// notice family. Demand failures also carry the exact suppressed cause.
type KnowledgeSourceOmission struct {
	Kind      KnowledgeSourceOmissionKind `json:"kind"`
	ProjectID string                      `json:"project_id"`
	LocatorID string                      `json:"locator"`
	Cause     string                      `json:"cause,omitempty"`
}

type KnowledgeSourceOmissionKind string

const (
	KnowledgeSourceDegraded     KnowledgeSourceOmissionKind = "knowledge_source_degraded"
	KnowledgeSourceDemandFailed KnowledgeSourceOmissionKind = "knowledge_source_demand_failed"
)

type Q10Request struct {
	Work          string
	KnowledgeID   string
	Product       string
	AllowDegraded bool
	Home          KnowledgeHome
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
	Status              string                        `json:"status"`
	Note                *CanonicalNote                `json:"note,omitempty"`
	Publication         *KnowledgePublication         `json:"publication,omitempty"`
	Result              *Q10Payload                   `json:"result"`
	UnverifiedAuthority *KnowledgeUnverifiedAuthority `json:"unverified_authority,omitempty"`
	publicationRead     *knowledgePublicationRead
}

// KnowledgeUnverifiedAuthority identifies a declared contract whose work is
// not held locally. It is a read warning, never a change to the declared tier.
type KnowledgeUnverifiedAuthority struct {
	LawID           string `json:"law_id"`
	WorkID          string `json:"work_id"`
	ContractVersion int64  `json:"contract_version"`
}

type Q10Payload struct {
	Status      string         `json:"status"`
	Note        *CanonicalNote `json:"note,omitempty"`
	LawStatus   string         `json:"law_status,omitempty"`
	SuccessorID string         `json:"successor_id,omitempty"`
}

// KnowledgeLawStatus reports the record's law status (CD-0020 D3) when the
// indexed outcome tag carries one: accepted for law, published
// for lessons, references, and research. A work-note outcome tag is free
// front-matter text rather than a law status, so a work note projects no
// status whatever its tag says.
func KnowledgeLawStatus(kind, outcomeTag string) string {
	if kind == "work_note" {
		return ""
	}
	switch outcomeTag {
	case "accepted", "published":
		return outcomeTag
	}
	return ""
}

func (s *Store) QueryQ9(ctx context.Context, req Q9Request) (Q9Result, error) {
	var out Q9Result
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "PM1.Q9", "store is not open", false, "open a store before querying knowledge")
	}
	out, err := queryQ9(ctx, s.db, req, s.now())
	if err != nil {
		return out, err
	}
	out.Publications = make([]KnowledgePublication, 0, len(out.publicationReads))
	for _, read := range out.publicationReads {
		publication, err := s.KnowledgePublicationForRead(ctx, read.home, read.commit, read.historical)
		if err != nil {
			return out, err
		}
		out.Publications = append(out.Publications, publication)
	}
	out.publicationReads = nil
	return out, nil
}

// validateQ9SharedFilters applies the bounded-text rule before source resolution.
func validateQ9SharedFilters(req Q9Request) error {
	if len(req.Text) > 256 {
		return newFailure(KindInvalidFilter, "PM1.Q9", "bounded knowledge text is too long", false, "limit text to 256 characters")
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
	var sources []KnowledgeHome
	var err error
	if req.Product != "" {
		sources, err = resolveKnowledgeQuerySources(ctx, db, req.Product, "PM1.Q9")
		if err != nil {
			return out, err
		}
		if req.Project != "" {
			var member bool
			if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, req.Product, req.Project).Scan(&member); err != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q9", "cannot validate Product/Project membership", true, "retry once the database is readable", err)
			}
			if !member {
				return out, newFailure(KindUnknownScope, "PM1.Q9", "Project is not a member of the requested Product", false, "supply a Project belonging to the Product")
			}
		}
		if len(sources) == 1 {
			if err := compareKnowledgeHomeEvidence(req.Home, sources[0], "PM1.Q9"); err != nil {
				return out, err
			}
		}
	} else {
		home, err := resolveKnowledgeQueryHome(ctx, db, "", req.Project, req.Home, "PM1.Q9")
		if err != nil {
			return out, err
		}
		sources = []KnowledgeHome{home}
	}
	return queryQ9Home(ctx, db, req, sources[0], observedAt)
}

// validateKnowledgeHomeForQueryCore reads the freshness verdict for one home.
// It takes *sql.DB because its probe reaches the home's git head; a
// transaction caller is a compile-time error (CD-0195 D2).
func validateKnowledgeHomeForQueryCore(ctx context.Context, db *sql.DB, home KnowledgeHome, allowDegraded bool, op string) (string, string, error) {
	current, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		if allowDegraded {
			return "unreachable", "degraded", nil
		}
		return "", "", newFailure(KindUnreachable, op, "git knowledge authority is unreachable", true, "restore the git home and retry")
	}
	watermark, err := readKnowledgeWatermark(ctx, db, home, current)
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
	out, err := queryQ10(ctx, s.db, req)
	if err != nil || out.publicationRead == nil {
		return out, err
	}
	read := out.publicationRead
	publication, err := s.KnowledgePublicationForRead(ctx, read.home, read.commit, read.historical)
	if err != nil {
		return out, err
	}
	out.Publication, out.publicationRead = &publication, nil
	return out, nil
}

// queryQ10 verifies one canonical note against its recorded git proof. It takes
// *sql.DB because the git verification must never run inside an open
// transaction (CD-0195 D2): the type makes a transaction caller a compile-time
// error.
func queryQ10(ctx context.Context, db *sql.DB, req Q10Request) (Q10Result, error) {
	var out Q10Result
	if (req.Work == "") == (req.KnowledgeID == "") {
		return out, newFailure(KindInvalidFilter, "PM1.Q10", "Q10 requires exactly one stable reference", false, "supply either work or knowledge_id")
	}
	out.ResultMeta = q10EmptyMeta(req)
	var note CanonicalNote
	var homeProject, homeLocator, path, commit, hash, kind, title, date, status, lessonTagsJSON, summary, successor, scopeMode, manifestSchemaVersion string
	req, lookupID, err := resolveQ10Lookup(ctx, db, req)
	if err != nil {
		return out, err
	}
	// Note identity is scoped to the knowledge home: the same stable id can
	// exist in two homes (concord and pokeedge both number decisions CD-####).
	// A caller-supplied Home selects its own row; when that row is absent the
	// bare-id retry lets the historical-home comparison report a mismatched
	// caller home instead of a missing note. Without a Home, an id held by more
	// than one home is ambiguous rather than an arbitrary pick.
	homeSupplied := req.Home.HomeProjectID != "" || req.Home.HomeLocatorID != ""
	sourceScopeWhere, err := q10SourceScopeWhere(ctx, db, req, homeSupplied)
	if err != nil {
		return out, err
	}
	scanNote := func(homeScoped bool) error {
		query := `SELECT home_project_id,home_locator_id,note_path,commit_oid,content_hash,type,title,completed_at,outcome_tag,lesson_tags,summary,CASE WHEN type='work_note' THEN COALESCE(successor_work_id,'') ELSE '' END,scope_mode,manifest_schema_version FROM archived_work WHERE id = ?`
		scanArgs := []any{lookupID}
		if homeScoped {
			query += ` AND home_project_id = ? AND home_locator_id = ?`
			scanArgs = append(scanArgs, req.Home.HomeProjectID, req.Home.HomeLocatorID)
		}
		if sourceScopeWhere != "" {
			query += sourceScopeWhere
			scanArgs = append(scanArgs, req.Product)
		}
		return db.QueryRowContext(ctx, query, scanArgs...).Scan(&homeProject, &homeLocator, &path, &commit, &hash, &kind, &title, &date, &status, &lessonTagsJSON, &summary, &successor, &scopeMode, &manifestSchemaVersion)
	}
	err = scanNote(homeSupplied)
	if err == sql.ErrNoRows && homeSupplied {
		err = scanNote(false)
	}
	if !homeSupplied {
		ambiguous, scanErr := q10NoteHasMultipleHomes(ctx, db, req.Product, lookupID, sourceScopeWhere)
		if scanErr != nil {
			return out, scanErr
		}
		if ambiguous {
			out.Status, out.Result = "ambiguous", &Q10Payload{Status: "ambiguous"}
			return out, nil
		}
	}
	if err == sql.ErrNoRows {
		status, _, classifyErr := classifyQ10MissingNote(ctx, db, req, lookupID)
		if classifyErr != nil {
			return out, classifyErr
		}
		out.Status, out.Result = status, &Q10Payload{Status: status}
		return out, nil
	}
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot read the canonical note locator", true, "retry once the database is readable", err)
	}
	if err := q10ProductScopeCheck(ctx, db, req, lookupID, kind, scopeMode, homeProject, homeLocator); err != nil {
		return out, err
	}
	storedHome, locatorErr := knowledgeHomeForLocator(ctx, db, homeProject, homeLocator, "")
	if locatorErr != nil {
		return q10HistoricalFailure(&out, req.AllowDegraded, "recorded canonical locator is unavailable", locatorErr)
	}
	if err := compareQ10HistoricalHome(req.Home, storedHome); err != nil {
		return out, err
	}
	note = CanonicalNote{HomeProjectID: homeProject, HomeLocatorID: homeLocator, NotePath: path, NotePathRef: path, Commit: commit, CommitOID: commit, ContentHash: hash}
	out.ResultMeta = q10HistoricalMeta(req)
	if kind == "work_note" {
		if stop, err := verifyQ10WorkNote(ctx, storedHome, note, lookupID, req.AllowDegraded, &out); stop {
			return out, err
		}
	} else {
		record, recordErr := knowledgeProjectedRecordTx(ctx, db, lookupID, kind, status, date, title, summary, hash, scopeMode, lessonTagsJSON, homeProject, homeLocator, path)
		if recordErr != nil {
			return out, recordErr
		}
		manifestRecord, stop, err := verifyQ10ManifestRecord(ctx, req, record, manifestSchemaVersion, storedHome.RepoPath, commit, &out)
		if stop {
			return out, err
		}
		if err := admitQ10ManifestAuthority(ctx, db, manifestRecord, &out); err != nil {
			return out, err
		}
	}
	payload := &Q10Payload{Status: "canonical", Note: &note, SuccessorID: successor, LawStatus: KnowledgeLawStatus(kind, status)}
	out.Status, out.Note, out.Result = "canonical", &note, payload
	// Manifest lookup proves the indexed law at its recorded commit; that does
	// not make it an explicit historical read. Archived work notes do carry
	// that meaning, even when selected through knowledge_id. A missing or
	// unverified proof serves no note and therefore has no publication.
	out.publicationRead = &knowledgePublicationRead{home: storedHome, commit: note.CommitOID, historical: kind == "work_note" || req.Work != ""}
	return out, nil
}

func verifyQ10WorkNote(ctx context.Context, home KnowledgeHome, note CanonicalNote, lookupID string, allowDegraded bool, out *Q10Result) (bool, error) {
	verified, err := VerifyCommittedNote(ctx, home.RepoPath, note.Commit, note.NotePath, note.ContentHash)
	if err != nil {
		_, failure := q10HistoricalFailure(out, allowDegraded, "recorded canonical note proof is unavailable", err)
		return true, failure
	}
	if verified.ID != lookupID || verified.Kind != "work_note" {
		out.Status, out.Result = "ambiguous", &Q10Payload{Status: "ambiguous"}
		return true, nil
	}
	return false, nil
}

func resolveQ10Lookup(ctx context.Context, db *sql.DB, req Q10Request) (Q10Request, string, error) {
	if req.Work != "" {
		return req, req.Work, nil
	}
	lookupID := req.KnowledgeID
	projectID, lawID, qualified, err := parseQualifiedKnowledgeID("PM1.Q10", lookupID)
	if err != nil {
		return req, "", err
	}
	if !qualified {
		return req, lookupID, nil
	}
	// A qualified reference resolves only through its Project's canonical locator.
	candidates, err := projectCanonicalHomeCandidates(ctx, db, projectID)
	if err != nil {
		return req, "", err
	}
	if len(candidates) == 0 {
		return req, "", newFailure(KindKnowledgeUnavailable, "PM1.Q10", "qualified reference names a Project with no canonical-path knowledge locator", false, "designate the Project's canonical-path locator before resolving through it")
	}
	if len(candidates) > 1 {
		return req, "", newAmbiguousScopeFailure("PM1.Q10", "qualified reference names a Project with multiple canonical-path locators", "leave exactly one canonical Project locator", knowledgeHomeCandidateIDs(candidates))
	}
	req.Home = candidates[0]
	return req, lawID, nil
}

func q10SourceScopeWhere(ctx context.Context, db *sql.DB, req Q10Request, homeSupplied bool) (string, error) {
	// Work notes keep their recorded historical home. Product-scoped bare
	// knowledge IDs use the current designation when one exists.
	if homeSupplied || req.Product == "" || req.KnowledgeID == "" {
		return "", nil
	}
	if _, err := resolveKnowledgeQuerySources(ctx, db, req.Product, "PM1.Q10"); err != nil {
		var failure *Failure
		if errors.As(err, &failure) && (failure.Kind == KindUnknownScope || failure.Kind == KindAmbiguousScope) {
			return "", nil
		}
		return "", err
	}
	return ` AND EXISTS (SELECT 1 FROM product_knowledge_homes h WHERE h.product_id = ? AND h.project_id = archived_work.home_project_id AND h.locator_id = archived_work.home_locator_id)`, nil
}

func q10NoteHasMultipleHomes(ctx context.Context, db *sql.DB, product, lookupID, sourceScopeWhere string) (bool, error) {
	countQuery := `SELECT COUNT(DISTINCT home_project_id || ':' || home_locator_id) FROM archived_work WHERE id = ?`
	countArgs := []any{lookupID}
	if sourceScopeWhere != "" {
		countQuery += sourceScopeWhere
		countArgs = append(countArgs, product)
	}
	var homes int
	if scanErr := db.QueryRowContext(ctx, countQuery, countArgs...).Scan(&homes); scanErr != nil {
		return false, wrapFailure(KindUnavailable, "PM1.Q10", "cannot inspect note home multiplicity", true, "retry once the database is readable", scanErr)
	}
	return homes > 1, nil
}

func admitQ10ManifestAuthority(ctx context.Context, db *sql.DB, manifestRecord KnowledgeRecord, out *Q10Result) error {
	// CD-0159 D2: the verified manifest entry carries the record's
	// declared authority, so the exact historical contract resolves after
	// the Git proof, inside one short read transaction. A held negative
	// refuses directly regardless of the degraded allowance; an unknown
	// declared work is unverified, keeps its declared tier, and warns
	// through the existing envelope.
	if manifestRecord.Authority.Tier == "legislated" {
		tx, txErr := beginReadTx(ctx, db)
		if txErr != nil {
			return wrapFailure(KindUnavailable, "PM1.Q10", "cannot begin the canonical authority admission", true, "retry once the database is readable", txErr)
		}
		unverified, resolveErr := resolveKnowledgeLegislatedAuthorityTx(ctx, tx, manifestRecord)
		if rbErr := tx.Rollback(); rbErr != nil && resolveErr == nil {
			resolveErr = wrapFailure(KindUnavailable, "PM1.Q10", "cannot close the canonical authority admission", true, "retry once the database is readable", rbErr)
		}
		if resolveErr != nil {
			return resolveErr
		}
		if unverified {
			out.UnverifiedAuthority = &KnowledgeUnverifiedAuthority{
				LawID: manifestRecord.ID, WorkID: manifestRecord.Authority.LegislatedBy,
				ContractVersion: manifestRecord.Authority.ContractVersion,
			}
		}
	}
	return nil
}

// verifyQ10ManifestRecord checks one assembled manifest-indexed record
// against the historical manifest, verifies the committed declaration, and
// returns the authoritative manifest entry the proof verified. The SQL
// discovery already ran through knowledgeProjectedRecordTx and this
// function holds no queryer: the Git proof runs after the database reads
// closed (CD-0195 D2). A historical failure reports through the out
// parameter's degraded allowance; stop says the caller must return the out
// as it now stands instead of continuing to the canonical result. The
// caller resolves the returned entry's declared authority after this proof.
func verifyQ10ManifestRecord(ctx context.Context, req Q10Request, record KnowledgeRecord, manifestSchemaVersion, repoPath, commit string, out *Q10Result) (KnowledgeRecord, bool, error) {
	manifest, missing, manifestErr := readKnowledgeManifest(ctx, repoPath, commit)
	if manifestErr != nil || missing {
		if manifestErr == nil {
			manifestErr = newFailure(KindInvalidNoteProof, "PM1.Q10", "recorded manifest is missing at the historical commit", false, "restore the committed manifest")
		}
		_, err := q10HistoricalFailure(out, req.AllowDegraded, "recorded manifest schema is unavailable", manifestErr)
		return KnowledgeRecord{}, true, err
	}
	if manifestSchemaVersion != "" && manifestSchemaVersion != manifest.SchemaVersion {
		return KnowledgeRecord{}, true, newFailure(KindInvariantViolation, "PM1.Q10", "indexed manifest schema version disagrees with the historical manifest", false, "rebuild the git-derived knowledge index")
	}
	if err := verifyManifestRecord(ctx, repoPath, commit, record); err != nil {
		_, verifyErr := q10HistoricalFailure(out, req.AllowDegraded, "recorded manifest declaration or blob could not be verified", err)
		return KnowledgeRecord{}, true, verifyErr
	}
	// The Git proof refuses an undeclared projection, so the authoritative
	// entry is present once the proof succeeds; an absent entry here keeps
	// the proof failure instead of silently skipping the caller's authority
	// admission.
	manifestRecord, found := manifestRecordByID(manifest, record.ID)
	if !found {
		return KnowledgeRecord{}, true, newFailure(KindInvalidNoteProof, "PM1.Q10", "recorded projection has no manifest declaration", false, "rebuild from the manifest commit and preserve its metadata")
	}
	return manifestRecord, false, nil
}

// knowledgeProjectedRecordTx assembles the projected manifest record
// for one archived knowledge record over whichever queryer the
// caller already holds — the SQL discovery half of the committed-record
// proof, so the Git verification runs with no database access. A law-bearing
// record requires its projected law-domain home at the exact hash.
func knowledgeProjectedRecordTx(ctx context.Context, q queryer, lookupID, kind, status, date, title, summary, hash, scopeMode, lessonTagsJSON, homeProject, homeLocator, path string) (KnowledgeRecord, error) {
	var tags []string
	if err := json.Unmarshal([]byte(lessonTagsJSON), &tags); err != nil {
		return KnowledgeRecord{}, newFailure(KindInvariantViolation, "PM1.Q10", "indexed manifest tags are malformed", false, "rebuild the git-derived knowledge index")
	}
	record := KnowledgeRecord{ID: lookupID, Kind: kind, Path: path, Status: status, Date: date, Title: title, Summary: summary, Tags: tags, Scopes: KnowledgeRecordScopes{Mode: scopeMode}, SHA256: hash}
	for _, scope := range []struct {
		table, column string
		target        *[]string
	}{{"archived_work_products", "product_id", &record.Scopes.ProductIDs}, {"archived_work_projects", "project_id", &record.Scopes.ProjectIDs}, {"archived_work_tags", "tag_id", &record.Scopes.TagIDs}} {
		values, queryErr := archivedScopeIDs(ctx, q, scope.table, scope.column, lookupID, homeProject, homeLocator)
		if queryErr != nil {
			return KnowledgeRecord{}, queryErr
		}
		*scope.target = values
	}
	values, queryErr := archivedScopeIDs(ctx, q, "archived_work_domains", "domain_id", lookupID, homeProject, homeLocator)
	if queryErr != nil {
		return KnowledgeRecord{}, queryErr
	}
	record.Scopes.DomainIDs = values
	if manifestLawBearingKinds[kind] {
		if err := q.QueryRowContext(ctx, `SELECT domain_id,product_wide_rationale FROM law_domain_homes WHERE home_project_id=? AND home_locator_id=? AND law_id=? AND law_content_hash=?`, homeProject, homeLocator, lookupID, hash).Scan(&record.HomeDomainID, &record.ProductWideRationale); err != nil {
			return KnowledgeRecord{}, q10LawDomainProjectionFailure(err)
		}
		record.homeDomainPresent = true
		record.productWideRationalePresent = record.ProductWideRationale != ""
		applicability, applicabilityErr := archivedLawApplicability(ctx, q, homeProject, homeLocator, lookupID)
		if applicabilityErr != nil {
			return KnowledgeRecord{}, applicabilityErr
		}
		record.AppliesToDomainIDs, record.appliesToDomainsPresent = applicability, true
	}
	return record, nil
}

// manifestRecordByID locates the authoritative manifest entry a projection
// row was verified against.
func manifestRecordByID(manifest KnowledgeManifest, id string) (KnowledgeRecord, bool) {
	for _, record := range manifest.Records {
		if record.ID == id {
			return record, true
		}
	}
	return KnowledgeRecord{}, false
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

// knowledgeResumeKey carries the paging position within one knowledge home.
type knowledgeResumeKey struct {
	MatchClass  int
	CompletedAt string
	ID          string
}

// knowledgeCursor binds the query and the designated home's identity.
type knowledgeCursor struct {
	Version                        int `json:"version"`
	Product, Project, Domain, Text string
	Kinds, Tags                    []string
	SourcesDigest                  string
	HomeProjectID, HomeLocatorID   string
	MatchClass                     int `json:"match_class"`
	CompletedAt, ID                string
}

// decodeCursorFields decodes exactly one JSON object and refuses a key the
// current cursor shape does not declare. A cursor minted under a retired
// filter names that filter, so it cannot resume a query that no longer
// applies it.
func decodeCursorFields(b []byte, cursor any) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cursor); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("knowledge cursor carries trailing data")
	}
	return nil
}

func encodeKnowledgeCursor(cursor knowledgeCursor) (string, error) {
	b, err := json.Marshal(cursor)
	if err != nil {
		return "", wrapFailure(KindInvalidCursor, "PM1.Q9", "cannot encode the knowledge cursor", false, "restart the bounded knowledge query", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeKnowledgeCursor(raw string, req Q9Request, kinds, tags []string, sourcesDigest string) (knowledgeCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	var cursor knowledgeCursor
	if err != nil || decodeCursorFields(b, &cursor) != nil || cursor.Version != 3 || cursor.Product != req.Product || cursor.Project != req.Project || cursor.Domain != req.Domain || cursor.Text != req.Text || cursor.SourcesDigest != sourcesDigest || !slices.Equal(cursor.Kinds, kinds) || !slices.Equal(cursor.Tags, tags) || cursor.MatchClass < 0 || cursor.MatchClass > 2 || req.Text == "" && cursor.MatchClass != 0 || cursor.CompletedAt == "" || cursor.ID == "" || cursor.HomeProjectID == "" || cursor.HomeLocatorID == "" {
		return knowledgeCursor{}, newFailure(KindInvalidCursor, "PM1.Q9", "cursor does not match the requested knowledge query or its home", false, "restart the query without the cursor, or use a cursor returned for the same home")
	}
	return cursor, nil
}

func queryQ9Home(ctx context.Context, db *sql.DB, req Q9Request, home KnowledgeHome, observedAt time.Time) (Q9Result, error) {
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
	digest := knowledgeSourceSetDigest([]KnowledgeHome{home})
	var resume *knowledgeResumeKey
	if req.Cursor != "" {
		cursor, err := decodeKnowledgeCursor(req.Cursor, req, kinds, tags, digest)
		if err != nil {
			return out, err
		}
		resume = &knowledgeResumeKey{MatchClass: cursor.MatchClass, CompletedAt: cursor.CompletedAt, ID: cursor.ID}
	}
	page := knowledgeHomePage{
		merged:    make([]KnowledgeItem, 0, limit),
		omissions: make([]string, 0),
	}
	if err := page.readHome(ctx, db, req, home, kinds, tags, limit, resume, &out); err != nil {
		return out, err
	}
	merged, omissions := page.merged, page.omissions
	degraded := page.degraded
	var cursor *string
	if len(merged) == limit {
		last := merged[len(merged)-1]
		encoded, err := encodeKnowledgeCursor(knowledgeCursor{Version: 3, Product: req.Product, Project: req.Project, Domain: req.Domain, Text: req.Text, Kinds: kinds, Tags: tags, SourcesDigest: digest, HomeProjectID: last.HomeProjectID, HomeLocatorID: last.HomeLocatorID, MatchClass: last.MatchClass, CompletedAt: last.CompletedAt, ID: last.ID})
		if err != nil {
			return out, err
		}
		cursor = &encoded
	}
	authority := "authoritative"
	if degraded {
		authority = "degraded"
	}
	watermark := page.watermark
	meta := knowledgeWatermarkMeta("PM1.Q9", watermark, authority, observedAt)
	meta.ResolvedScope = ResolvedScope{ProductID: req.Product, ProjectID: req.Project}
	meta.Omissions = omissions
	meta.NextCursor = cursor
	out.ResultMeta = meta
	out.Items = merged
	out.IndexWatermark = watermark
	return out, nil
}

type knowledgeHomePage struct {
	merged    []KnowledgeItem
	watermark string
	omissions []string
	degraded  bool
}

func (page *knowledgeHomePage) readHome(ctx context.Context, db *sql.DB, req Q9Request, source KnowledgeHome, kinds, tags []string, limit int, resume *knowledgeResumeKey, out *Q9Result) error {
	label := source.HomeProjectID + "/" + source.HomeLocatorID
	scanned, authority, err := validateKnowledgeHomeForQueryCore(ctx, db, source, req.AllowDegraded, "PM1.Q9")
	if err != nil {
		return knowledgeHomeFailure(err, label)
	}
	verdict := scanned
	if verdict == "" {
		verdict = "unindexed"
	}
	if authority != "authoritative" {
		page.degraded = true
		page.omissions = append(page.omissions, "knowledge_index_lagging_or_unreachable")
	}
	// A suppressed demand failure degrades the source through this same
	// per-source path, naming the source and the cause; it never relabels
	// a freshness verdict the source's own watermark already carries.
	if cause := knowledgeDemandFailureCause(req, source); cause != "" {
		page.degraded = true
		authority = "degraded"
		out.SourceOmissions = append(out.SourceOmissions, KnowledgeSourceOmission{Kind: KnowledgeSourceDemandFailed, ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID, Cause: cause})
	}
	page.watermark = verdict
	served := scanned
	if scanned == "unreachable" {
		// The reachability verdict is not a commit. A degraded query still
		// reads the cached projection, including when it returns no rows.
		err := db.QueryRowContext(ctx, `SELECT scanned_commit_oid FROM knowledge_index_watermark WHERE home_project_id=? AND home_locator_id=? AND head_ref=?`, source.HomeProjectID, source.HomeLocatorID, source.HeadRef).Scan(&served)
		if errors.Is(err, sql.ErrNoRows) {
			served = ""
		} else if err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q9", "cannot read the served knowledge revision", true, "retry once the database is readable", err)
		}
	}
	out.publicationReads = append(out.publicationReads, knowledgePublicationRead{home: source, commit: served})
	if authority == "authoritative" {
		if err := validateKnowledgeCoverageCore(ctx, db, source, scanned, kinds); err != nil {
			return err
		}
		page.omissions = append(page.omissions, knowledgeCoverageOmissions(ctx, db, source, scanned)...)
	}
	paged := req
	paged.Cursor = ""
	paged.Home = source
	query, args := buildKnowledgeQueryForScope(paged, kinds, tags, limit, resume)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return wrapFailure(KindUnavailable, "PM1.Q9", "cannot search the git knowledge index of source "+label, true, "retry once the database is readable", err)
	}
	items, err := scanKnowledgeRows(rows)
	if err != nil {
		return err
	}
	page.merged = append(page.merged, items...)
	return nil
}

// knowledgeDemandFailureCause returns the cause recorded for this source's
// suppressed demand, or "" when its demand succeeded.
func knowledgeDemandFailureCause(req Q9Request, source KnowledgeHome) string {
	for _, failure := range req.DemandFailures {
		if failure.ProjectID == source.HomeProjectID && failure.LocatorID == source.HomeLocatorID {
			if failure.FailureKind == "" {
				return "unknown"
			}
			return failure.FailureKind
		}
	}
	return ""
}

func knowledgeHomeFailure(err error, label string) error {
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
	tokens := strings.Fields(req.Text)
	inputColumns, inputValues := []string{"text"}, []string{"?"}
	args := []any{req.Text}
	for i, token := range tokens {
		inputColumns = append(inputColumns, "token"+strconv.Itoa(i))
		inputValues = append(inputValues, "lower(?)")
		args = append(args, token)
	}
	args = append(args, req.Home.HomeProjectID, req.Home.HomeLocatorID)
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
	structuredMatches, tokenMatches := make([]string, 0, len(tokens)), make([]string, 0, len(tokens))
	for i := range tokens {
		token := "input." + inputColumns[i+1]
		structured := `(instr(lower(aw.id), ` + token + `) > 0
			OR instr(lower(aw.title), ` + token + `) > 0
			OR instr(lower(aw.summary), ` + token + `) > 0
			OR EXISTS (SELECT 1 FROM archived_work_tags t WHERE t.work_id = aw.id AND t.home_project_id = aw.home_project_id AND t.home_locator_id = aw.home_locator_id AND instr(lower(t.tag_id), ` + token + `) > 0)
			OR EXISTS (SELECT 1 FROM json_each(aw.lesson_tags) t WHERE instr(lower(t.value), ` + token + `) > 0))`
		body := `EXISTS (SELECT 1 FROM law_bodies lb WHERE lb.home_project_id = aw.home_project_id AND lb.home_locator_id = aw.home_locator_id AND lb.law_id = aw.id AND instr(lower(lb.body), ` + token + `) > 0)`
		structuredMatches = append(structuredMatches, structured)
		tokenMatches = append(tokenMatches, "("+structured+" OR "+body+")")
	}
	structuredTextMatch, tokenTextMatch := "1", "1"
	if len(tokens) > 0 {
		structuredTextMatch = strings.Join(structuredMatches, " AND ")
		tokenTextMatch = strings.Join(tokenMatches, " AND ")
	}
	where = append(where, `(input.text = '' OR `+exactMatch+` OR (`+tokenTextMatch+`))`)
	cursorWhere := ""
	if resume != nil {
		cursorWhere = " WHERE (aw.match_class > ? OR (aw.match_class = ? AND (aw.completed_at < ? OR (aw.completed_at = ? AND aw.id > ?))))"
		args = append(args, resume.MatchClass, resume.MatchClass, resume.CompletedAt, resume.CompletedAt, resume.ID)
	}
	args = append(args, limit)
	scopeSelect := `COALESCE((SELECT json_group_array(domain_id) FROM (` +
		`SELECT domain_id FROM archived_work_domains WHERE work_id=aw.id AND home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id ` +
		`UNION SELECT domain_id FROM law_domain_homes WHERE home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id AND law_id=aw.id ` +
		`UNION SELECT domain_id FROM law_domain_applicability WHERE home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id AND law_id=aw.id ` +
		`ORDER BY domain_id)), '[]'),`
	return `WITH input(` + strings.Join(inputColumns, ",") + `) AS (VALUES (` + strings.Join(inputValues, ",") + `)), ranked AS (` +
		`SELECT aw.*, CASE WHEN input.text = '' OR ` + exactMatch + ` THEN 0 WHEN ` + structuredTextMatch + ` THEN 1 ELSE 2 END AS match_class ` +
		`FROM archived_work aw CROSS JOIN input WHERE ` + strings.Join(where, " AND ") + `) ` +
		`SELECT aw.id,aw.type,aw.title,aw.completed_at,aw.outcome_tag,CASE WHEN aw.type='work_note' THEN COALESCE(aw.successor_work_id,'') ELSE '' END,aw.lesson_tags,aw.summary,aw.home_project_id,aw.home_locator_id,aw.note_path,aw.commit_oid,aw.content_hash,aw.scope_mode,` +
		`COALESCE((SELECT json_group_array(product_id) FROM (SELECT product_id FROM archived_work_products WHERE work_id=aw.id AND home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id ORDER BY product_id)), '[]'),` +
		`COALESCE((SELECT json_group_array(project_id) FROM (SELECT project_id FROM archived_work_projects WHERE work_id=aw.id AND home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id ORDER BY project_id)), '[]'),` +
		scopeSelect +
		`COALESCE((SELECT json_group_array(tag_id) FROM (SELECT tag_id FROM archived_work_tags WHERE work_id=aw.id AND home_project_id=aw.home_project_id AND home_locator_id=aw.home_locator_id ORDER BY tag_id)), '[]'),aw.match_class ` +
		`FROM ranked aw` + cursorWhere + ` ORDER BY aw.match_class ASC, aw.completed_at DESC, aw.id ASC LIMIT ?`, args
}
