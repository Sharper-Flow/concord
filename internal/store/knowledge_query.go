package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
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
}

type Q10Request struct {
	Work          string
	KnowledgeID   string
	Product       string
	AllowDegraded bool
	Home          KnowledgeHome
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
	Status string         `json:"status"`
	Note   *CanonicalNote `json:"note,omitempty"`
}

func (s *Store) QueryQ9(ctx context.Context, req Q9Request) (Q9Result, error) {
	var out Q9Result
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "PM1.Q9", "store is not open", false, "open a store before querying knowledge")
	}
	return queryQ9(ctx, s.db, req, s.now())
}

func queryQ9(ctx context.Context, q queryer, req Q9Request, observedAt time.Time) (Q9Result, error) {
	var out Q9Result
	resolvedHome, err := resolveKnowledgeQueryHome(ctx, q, req.Product, req.Project, req.Home, "PM1.Q9")
	if err != nil {
		return out, err
	}
	req.Home = resolvedHome
	limit, err := knowledgeLimit(req.Limit)
	if err != nil {
		return out, err
	}
	if len(req.Text) > 256 {
		return out, newFailure(KindInvalidFilter, "PM1.Q9", "bounded knowledge text is too long", false, "limit text to 256 characters")
	}
	if req.Since != "" {
		if _, err := time.Parse(time.RFC3339Nano, req.Since); err != nil {
			return out, newFailure(KindInvalidFilter, "PM1.Q9", "since must be RFC3339", false, "supply a valid time window")
		}
	}
	if req.Until != "" {
		if _, err := time.Parse(time.RFC3339Nano, req.Until); err != nil {
			return out, newFailure(KindInvalidFilter, "PM1.Q9", "until must be RFC3339", false, "supply a valid time window")
		}
	}
	kinds, err := knowledgeKinds(req.Kinds)
	if err != nil {
		return out, err
	}
	tags := orderedStrings(nonEmptyStrings(req.Tags))
	watermark, authority, err := validateKnowledgeHomeForQueryCore(ctx, q, req.Home, req.AllowDegraded, "PM1.Q9")
	if err != nil {
		return out, err
	}
	if watermark == "" {
		watermark = "unindexed"
	}
	if authority == "authoritative" {
		if err := validateKnowledgeCoverageCore(ctx, q, req.Home, watermark, kinds); err != nil {
			return out, err
		}
	}
	if req.Cursor != "" {
		if _, err := decodeKnowledgeCursor(req.Cursor, req, kinds, tags); err != nil {
			return out, err
		}
	}
	query, args := buildKnowledgeQueryForScope(req, kinds, tags, limit)
	rows, err := q.QueryContext(ctx, query, args...)
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
		encoded, err := encodeKnowledgeCursor(knowledgeCursor{Version: 2, Product: req.Product, Project: req.Project, Kinds: kinds, Tags: tags, Text: req.Text, Since: req.Since, Until: req.Until, HomeProjectID: req.Home.HomeProjectID, HomeLocatorID: req.Home.HomeLocatorID, HeadRef: req.Home.HeadRef, MatchClass: last.MatchClass, CompletedAt: last.CompletedAt, ID: last.ID})
		if err != nil {
			return out, err
		}
		cursor = &encoded
	}
	meta := knowledgeWatermarkMeta("PM1.Q9", watermark, authority, observedAt)
	meta.ResolvedScope = ResolvedScope{ProductID: req.Product, ProjectID: req.Project}
	if authority == "authoritative" {
		meta.Omissions = append(meta.Omissions, knowledgeCoverageOmissions(ctx, q, req.Home, watermark)...)
	}
	if authority != "authoritative" {
		meta.Omissions = []string{"knowledge_index_lagging_or_unreachable"}
	}
	meta.NextCursor = cursor
	out.ResultMeta, out.Items, out.IndexWatermark = meta, items, watermark
	return out, nil
}

func validateKnowledgeHomeForQueryCore(ctx context.Context, q queryer, home KnowledgeHome, allowDegraded bool, op string) (string, string, error) {
	current, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		if allowDegraded {
			return "unreachable", "degraded", nil
		}
		return "", "", newFailure(KindUnreachable, op, "git knowledge authority is unreachable", true, "restore the git home and retry")
	}
	watermark, err := readKnowledgeWatermark(ctx, q, home, current)
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

func scanKnowledgeItem(rows *sql.Rows) (KnowledgeItem, error) {
	return scanKnowledgeItemForScope(rows)
}

func scanKnowledgeItemForScope(rows *sql.Rows) (KnowledgeItem, error) {
	var item KnowledgeItem
	var lessonTags, productIDs, projectIDs, domainIDs, tagIDs string
	args := []any{&item.ID, &item.Kind, &item.Title, &item.CompletedAt, &item.OutcomeTag, &lessonTags, &item.Summary, &item.HomeProjectID, &item.HomeLocatorID, &item.NotePath, &item.Commit, &item.ContentHash, &item.ScopeMode, &productIDs, &projectIDs, &domainIDs, &tagIDs}
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
	return queryQ10(ctx, s.db, req)
}

func queryQ10(ctx context.Context, q queryer, req Q10Request) (Q10Result, error) {
	var out Q10Result
	if (req.Work == "") == (req.KnowledgeID == "") {
		return out, newFailure(KindInvalidFilter, "PM1.Q10", "Q10 requires exactly one stable reference", false, "supply either work or knowledge_id")
	}
	out.ResultMeta = q10EmptyMeta(req)
	var note CanonicalNote
	var homeProject, homeLocator, path, commit, hash, kind, title, date, status, lessonTagsJSON, summary, successor, scopeMode, manifestSchemaVersion string
	lookupID := req.Work
	if lookupID == "" {
		lookupID = req.KnowledgeID
	}
	err := q.QueryRowContext(ctx, `SELECT home_project_id,home_locator_id,note_path,commit_oid,content_hash,type,title,completed_at,outcome_tag,lesson_tags,summary,COALESCE(successor_work_id,''),scope_mode,manifest_schema_version FROM archived_work WHERE id = ?`, lookupID).Scan(&homeProject, &homeLocator, &path, &commit, &hash, &kind, &title, &date, &status, &lessonTagsJSON, &summary, &successor, &scopeMode, &manifestSchemaVersion)
	if err == sql.ErrNoRows {
		if req.Work != "" {
			var exists bool
			if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM work_items WHERE id = ?)`, lookupID).Scan(&exists); err != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot inspect live work", true, "retry once the database is readable", err)
			}
			if exists {
				out.Status, out.Result = "not_compacted", &Q10Payload{Status: "not_compacted"}
				return out, nil
			}
		}
		out.Status, out.Result = "missing", &Q10Payload{Status: "missing"}
		return out, nil
	}
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot read the canonical note locator", true, "retry once the database is readable", err)
	}
	if req.Product != "" {
		var inScope bool
		if kind == "work_note" || scopeMode == "explicit" {
			if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM archived_work_products WHERE work_id=? AND product_id=?)`, lookupID, req.Product).Scan(&inScope); err != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot validate knowledge Product scope", true, "retry once the database is readable", err)
			}
		} else if scopeMode == "home" {
			if err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, req.Product, homeProject).Scan(&inScope); err != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot validate knowledge Product scope", true, "retry once the database is readable", err)
			}
		}
		if !inScope {
			return out, unknownScope("PM1.Q10", "knowledge note is not in the requested Product scope")
		}
	}
	storedHome, locatorErr := knowledgeHomeForLocator(ctx, q, homeProject, homeLocator, "")
	if locatorErr != nil {
		return q10HistoricalFailure(&out, req.AllowDegraded, "recorded canonical locator is unavailable", locatorErr)
	}
	if err := compareQ10HistoricalHome(req.Home, storedHome); err != nil {
		return out, err
	}
	note = CanonicalNote{HomeProjectID: homeProject, HomeLocatorID: homeLocator, NotePath: path, NotePathRef: path, Commit: commit, CommitOID: commit, ContentHash: hash}
	out.ResultMeta = q10HistoricalMeta(req)
	if kind == "work_note" {
		verified, verifyErr := VerifyCommittedNote(ctx, storedHome.RepoPath, commit, path, hash)
		if verifyErr != nil {
			return q10HistoricalFailure(&out, req.AllowDegraded, "recorded canonical note proof is unavailable", verifyErr)
		}
		if verified.ID != lookupID || verified.Kind != "work_note" {
			out.Status, out.Result = "ambiguous", &Q10Payload{Status: "ambiguous"}
			return out, nil
		}
	} else {
		var tags []string
		if err := json.Unmarshal([]byte(lessonTagsJSON), &tags); err != nil {
			return out, newFailure(KindInvariantViolation, "PM1.Q10", "indexed manifest tags are malformed", false, "rebuild the git-derived knowledge index")
		}
		manifest, missing, manifestErr := readKnowledgeManifest(ctx, storedHome.RepoPath, commit)
		if manifestErr != nil || missing {
			if manifestErr == nil {
				manifestErr = newFailure(KindInvalidNoteProof, "PM1.Q10", "recorded manifest is missing at the historical commit", false, "restore the committed manifest")
			}
			return q10HistoricalFailure(&out, req.AllowDegraded, "recorded manifest schema is unavailable", manifestErr)
		}
		if manifestSchemaVersion != "" && manifestSchemaVersion != manifest.SchemaVersion {
			return out, newFailure(KindInvariantViolation, "PM1.Q10", "indexed manifest schema version disagrees with the historical manifest", false, "rebuild the git-derived knowledge index")
		}
		manifestSchemaVersion = manifest.SchemaVersion
		record := KnowledgeRecord{ID: lookupID, Kind: kind, Path: path, Status: status, Date: date, Title: title, Summary: summary, Tags: tags, Scopes: KnowledgeRecordScopes{Mode: scopeMode}, Successor: successor, SHA256: hash}
		for _, scope := range []struct {
			table, column string
			target        *[]string
		}{{"archived_work_products", "product_id", &record.Scopes.ProductIDs}, {"archived_work_projects", "project_id", &record.Scopes.ProjectIDs}, {"archived_work_tags", "tag_id", &record.Scopes.TagIDs}} {
			values, queryErr := archivedScopeIDs(ctx, q, scope.table, scope.column, lookupID)
			if queryErr != nil {
				return out, queryErr
			}
			*scope.target = values
		}
		values, queryErr := archivedScopeIDs(ctx, q, "archived_work_domains", "domain_id", lookupID)
		if queryErr != nil {
			return out, queryErr
		}
		record.Scopes.DomainIDs = values
		if kind == "decision" || kind == "spec" {
			if err := q.QueryRowContext(ctx, `SELECT domain_id,product_wide_rationale FROM law_domain_homes WHERE home_project_id=? AND home_locator_id=? AND law_id=? AND law_content_hash=?`, homeProject, homeLocator, lookupID, hash).Scan(&record.HomeDomainID, &record.ProductWideRationale); err != nil {
				return out, q10LawDomainProjectionFailure(err)
			}
			record.homeDomainPresent = true
			record.productWideRationalePresent = record.ProductWideRationale != ""
			values, queryErr = archivedLawApplicability(ctx, q, homeProject, homeLocator, lookupID)
			if queryErr != nil {
				return out, queryErr
			}
			record.AppliesToDomainIDs, record.appliesToDomainsPresent = values, true
		}
		if err := verifyManifestRecord(ctx, storedHome.RepoPath, commit, record); err != nil {
			return q10HistoricalFailure(&out, req.AllowDegraded, "recorded manifest declaration or blob could not be verified", err)
		}
	}
	out.Status, out.Note, out.Result = "canonical", &note, &Q10Payload{Status: "canonical", Note: &note}
	return out, nil
}

func archivedScopeIDs(ctx context.Context, q queryer, table, column, workID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+column+" FROM "+table+" WHERE work_id=? ORDER BY "+column, workID)
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
		copy := *failure
		copy.Op = "PM1.Q10"
		switch copy.Kind {
		case KindUnknownScope:
			copy.Kind = KindKnowledgeUnavailable
		case KindGitUnreachable, KindUnreachable:
			copy.Kind = KindUnreachable
		case KindInvalidNoteProof:
			if copy.Err != nil {
				copy.Kind = KindUnreachable
			} else {
				copy.Kind = KindKnowledgeMissing
			}
		}
		copy.Detail = detail + ": " + copy.Detail
		return &copy
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
	Product, Project, Text                string
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
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.Version != 2 || cursor.Product != req.Product || cursor.Project != req.Project || cursor.Text != req.Text || cursor.Since != req.Since || cursor.Until != req.Until || cursor.HomeProjectID != req.Home.HomeProjectID || cursor.HomeLocatorID != req.Home.HomeLocatorID || cursor.HeadRef != req.Home.HeadRef || !equalStrings(cursor.Kinds, kinds) || !equalStrings(cursor.Tags, tags) || cursor.MatchClass < 0 || cursor.MatchClass > 1 || req.Text == "" && cursor.MatchClass != 0 || cursor.CompletedAt == "" || cursor.ID == "" {
		return knowledgeCursor{}, newFailure(KindInvalidCursor, "PM1.Q9", "cursor does not match the requested knowledge query", false, "use a cursor returned for the same query and filters")
	}
	return cursor, nil
}

func buildKnowledgeQuery(req Q9Request, kinds, tags []string, limit int) (string, []any) {
	return buildKnowledgeQueryForScope(req, kinds, tags, limit)
}

func buildKnowledgeQueryForScope(req Q9Request, kinds, tags []string, limit int) (string, []any) {
	where := []string{"aw.home_project_id = ?", "aw.home_locator_id = ?"}
	args := []any{req.Text, req.Home.HomeProjectID, req.Home.HomeLocatorID}
	if req.Product != "" {
		where = append(where, "(aw.scope_mode = 'home' OR EXISTS (SELECT 1 FROM archived_work_products p WHERE p.work_id = aw.id AND p.product_id = ?))")
		args = append(args, req.Product)
	}
	if req.Project != "" {
		where = append(where, "(aw.scope_mode = 'home' OR EXISTS (SELECT 1 FROM archived_work_projects p WHERE p.work_id = aw.id AND p.project_id = ?))")
		args = append(args, req.Project)
	}
	if req.Domain != "" {
		where = append(where, "EXISTS (SELECT 1 FROM archived_work_domains d WHERE d.work_id = aw.id AND d.domain_id = ?)")
		args = append(args, req.Domain)
	}
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, kind := range kinds {
			placeholders[i], args = "?", append(args, kind)
		}
		where = append(where, "aw.type IN ("+strings.Join(placeholders, ",")+")")
	}
	for _, tag := range tags {
		where = append(where, "(EXISTS (SELECT 1 FROM archived_work_tags t WHERE t.work_id = aw.id AND t.tag_id = ?) OR (aw.type <> 'work_note' AND EXISTS (SELECT 1 FROM json_each(aw.lesson_tags) WHERE value = ?)))")
		args = append(args, tag, tag)
	}
	exactScopeMatch := `EXISTS (SELECT 1 FROM archived_work_domains exact_domain WHERE exact_domain.work_id = aw.id AND lower(exact_domain.domain_id) = lower(input.text))`
	exactMatch := `(lower(aw.id) = lower(input.text)
		OR lower(aw.title) = lower(input.text)
		OR EXISTS (SELECT 1 FROM archived_work_tags exact_tag WHERE exact_tag.work_id = aw.id AND lower(exact_tag.tag_id) = lower(input.text))
		OR EXISTS (SELECT 1 FROM json_each(aw.lesson_tags) exact_lesson_tag WHERE lower(exact_lesson_tag.value) = lower(input.text))
		OR (` + exactScopeMatch + `))`
	boundedTextMatch := `(instr(lower(aw.title), lower(input.text)) > 0 OR instr(lower(aw.summary), lower(input.text)) > 0)`
	where = append(where, `(input.text = '' OR `+exactMatch+` OR `+boundedTextMatch+`)`)
	if req.Since != "" {
		where = append(where, "aw.completed_at >= ?")
		args = append(args, req.Since)
	}
	if req.Until != "" {
		where = append(where, "aw.completed_at <= ?")
		args = append(args, req.Until)
	}
	cursorWhere := ""
	if req.Cursor != "" {
		cursor, _ := decodeKnowledgeCursor(req.Cursor, req, kinds, tags)
		cursorWhere = " WHERE (aw.match_class > ? OR (aw.match_class = ? AND (aw.completed_at < ? OR (aw.completed_at = ? AND aw.id > ?))))"
		args = append(args, cursor.MatchClass, cursor.MatchClass, cursor.CompletedAt, cursor.CompletedAt, cursor.ID)
	}
	args = append(args, limit)
	scopeSelect := `COALESCE((SELECT json_group_array(domain_id) FROM (SELECT domain_id FROM archived_work_domains WHERE work_id=aw.id ORDER BY domain_id)), '[]'),`
	return `WITH input(text) AS (VALUES (?)), ranked AS (` +
		`SELECT aw.*, CASE WHEN input.text = '' OR ` + exactMatch + ` THEN 0 ELSE 1 END AS match_class ` +
		`FROM archived_work aw CROSS JOIN input WHERE ` + strings.Join(where, " AND ") + `) ` +
		`SELECT aw.id,aw.type,aw.title,aw.completed_at,aw.outcome_tag,aw.lesson_tags,aw.summary,aw.home_project_id,aw.home_locator_id,aw.note_path,aw.commit_oid,aw.content_hash,aw.scope_mode,` +
		`COALESCE((SELECT json_group_array(product_id) FROM (SELECT product_id FROM archived_work_products WHERE work_id=aw.id ORDER BY product_id)), '[]'),` +
		`COALESCE((SELECT json_group_array(project_id) FROM (SELECT project_id FROM archived_work_projects WHERE work_id=aw.id ORDER BY project_id)), '[]'),` +
		scopeSelect +
		`COALESCE((SELECT json_group_array(tag_id) FROM (SELECT tag_id FROM archived_work_tags WHERE work_id=aw.id ORDER BY tag_id)), '[]'),aw.match_class ` +
		`FROM ranked aw` + cursorWhere + ` ORDER BY aw.match_class ASC, aw.completed_at DESC, aw.id ASC LIMIT ?`, args
}
