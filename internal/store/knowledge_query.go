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
	Component     string
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
	ComponentIDs  []string `json:"component_ids,omitempty"`
	TagIDs        []string `json:"tag_ids,omitempty"`
	HomeProjectID string   `json:"home_project_id"`
	HomeLocatorID string   `json:"home_locator_id"`
	NotePath      string   `json:"path"`
	NotePathRef   string   `json:"note_path"`
	Commit        string   `json:"commit"`
	CommitOID     string   `json:"commit_oid"`
	ContentHash   string   `json:"content_hash"`
	ScopeMode     string   `json:"scope_mode"`
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
	resolvedHome, err := s.ResolveKnowledgeQueryHome(ctx, req.Product, req.Project, req.Home, "PM1.Q9")
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
	watermark, authority, err := validateKnowledgeHomeForQuery(ctx, s, req.Home, req.AllowDegraded, "PM1.Q9")
	if err != nil {
		return out, err
	}
	if watermark == "" {
		watermark = "unindexed"
	}
	if authority == "authoritative" {
		if err := validateKnowledgeCoverage(ctx, s, req.Home, watermark, kinds); err != nil {
			return out, err
		}
	}
	if req.Cursor != "" {
		if _, err := decodeKnowledgeCursor(req.Cursor, req, kinds, tags); err != nil {
			return out, err
		}
	}
	query, args := buildKnowledgeQuery(req, kinds, tags, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "PM1.Q9", "cannot search the git knowledge index", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	items := make([]KnowledgeItem, 0, limit)
	for rows.Next() {
		item, err := scanKnowledgeItem(rows)
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
		encoded, err := encodeKnowledgeCursor(knowledgeCursor{Version: 1, Product: req.Product, Project: req.Project, Component: req.Component, Kinds: kinds, Tags: tags, Text: req.Text, Since: req.Since, Until: req.Until, HomeProjectID: req.Home.HomeProjectID, HomeLocatorID: req.Home.HomeLocatorID, HeadRef: req.Home.HeadRef, CompletedAt: last.CompletedAt, ID: last.ID})
		if err != nil {
			return out, err
		}
		cursor = &encoded
	}
	meta := knowledgeWatermarkMeta("PM1.Q9", req.Home, watermark, authority)
	meta.ResolvedScope = ResolvedScope{ProductID: req.Product, ProjectID: req.Project}
	if authority == "authoritative" {
		meta.Omissions = append(meta.Omissions, knowledgeCoverageOmissions(ctx, s.db, req.Home, watermark)...)
	}
	if authority != "authoritative" {
		meta.Omissions = []string{"knowledge_index_lagging_or_unreachable"}
	}
	meta.NextCursor = cursor
	out.ResultMeta, out.Items, out.IndexWatermark = meta, items, watermark
	return out, nil
}

func scanKnowledgeItem(rows *sql.Rows) (KnowledgeItem, error) {
	var item KnowledgeItem
	var lessonTags, productIDs, projectIDs, componentIDs, tagIDs string
	if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.CompletedAt, &item.OutcomeTag, &lessonTags, &item.Summary, &item.HomeProjectID, &item.HomeLocatorID, &item.NotePath, &item.Commit, &item.ContentHash, &item.ScopeMode, &productIDs, &projectIDs, &componentIDs, &tagIDs); err != nil {
		return item, wrapFailure(KindUnavailable, "PM1.Q9", "cannot decode a knowledge index row", true, "retry once the database is readable", err)
	}
	if json.Unmarshal([]byte(lessonTags), &item.LessonTags) != nil {
		return item, newFailure(KindInvariantViolation, "PM1.Q9", "indexed lesson_tags are malformed", false, "rebuild the git-derived knowledge index")
	}
	for _, scope := range []struct {
		raw    string
		target *[]string
	}{{productIDs, &item.ProductIDs}, {projectIDs, &item.ProjectIDs}, {componentIDs, &item.ComponentIDs}, {tagIDs, &item.TagIDs}} {
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
	if (req.Work == "") == (req.KnowledgeID == "") {
		return out, newFailure(KindInvalidFilter, "PM1.Q10", "Q10 requires exactly one stable reference", false, "supply either work or knowledge_id")
	}
	out.ResultMeta = q10EmptyMeta(req)
	var note CanonicalNote
	var homeProject, homeLocator, path, commit, hash, kind, title, date, status, lessonTagsJSON, summary, successor, scopeMode string
	lookupID := req.Work
	if lookupID == "" {
		lookupID = req.KnowledgeID
	}
	err := s.db.QueryRowContext(ctx, `SELECT home_project_id,home_locator_id,note_path,commit_oid,content_hash,type,title,completed_at,outcome_tag,lesson_tags,summary,COALESCE(successor_work_id,''),scope_mode FROM archived_work WHERE id = ?`, lookupID).Scan(&homeProject, &homeLocator, &path, &commit, &hash, &kind, &title, &date, &status, &lessonTagsJSON, &summary, &successor, &scopeMode)
	if err == sql.ErrNoRows {
		if req.Work != "" {
			var exists bool
			if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM work_items WHERE id = ?)`, lookupID).Scan(&exists); err != nil {
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
			if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM archived_work_products WHERE work_id=? AND product_id=?)`, lookupID, req.Product).Scan(&inScope); err != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot validate knowledge Product scope", true, "retry once the database is readable", err)
			}
		} else if scopeMode == "home" {
			if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM product_projects WHERE product_id=? AND project_id=?)`, req.Product, homeProject).Scan(&inScope); err != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot validate knowledge Product scope", true, "retry once the database is readable", err)
			}
		}
		if !inScope {
			return out, unknownScope("PM1.Q10", "knowledge note is not in the requested Product scope")
		}
	}
	storedHome, locatorErr := s.KnowledgeHomeForLocator(ctx, homeProject, homeLocator, "")
	if locatorErr != nil {
		return q10HistoricalFailure(&out, req.AllowDegraded, "recorded canonical locator is unavailable", locatorErr)
	}
	if err := compareQ10HistoricalHome(req.Home, storedHome); err != nil {
		return out, err
	}
	note = CanonicalNote{HomeProjectID: homeProject, HomeLocatorID: homeLocator, NotePath: path, NotePathRef: path, Commit: commit, CommitOID: commit, ContentHash: hash}
	out.ResultMeta = q10HistoricalMeta(req, commit)
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
		record := KnowledgeRecord{ID: lookupID, Kind: kind, Path: path, Status: status, Date: date, Title: title, Summary: summary, Tags: tags, Scopes: KnowledgeRecordScopes{Mode: scopeMode}, Successor: successor, SHA256: hash}
		for table, target := range map[string]*[]string{"archived_work_products": &record.Scopes.ProductIDs, "archived_work_projects": &record.Scopes.ProjectIDs, "archived_work_components": &record.Scopes.ComponentIDs, "archived_work_tags": &record.Scopes.TagIDs} {
			column := map[string]string{"archived_work_products": "product_id", "archived_work_projects": "project_id", "archived_work_components": "component_id", "archived_work_tags": "tag_id"}[table]
			rows, queryErr := s.db.QueryContext(ctx, "SELECT "+column+" FROM "+table+" WHERE work_id=? ORDER BY "+column, lookupID)
			if queryErr != nil {
				return out, wrapFailure(KindUnavailable, "PM1.Q10", "cannot read manifest record scope", true, "retry once the database is readable", queryErr)
			}
			for rows.Next() {
				var value string
				if scanErr := rows.Scan(&value); scanErr != nil {
					rows.Close()
					return out, scanErr
				}
				*target = append(*target, value)
			}
			if closeErr := rows.Close(); closeErr != nil {
				return out, closeErr
			}
		}
		if err := verifyManifestRecord(ctx, storedHome.RepoPath, commit, record); err != nil {
			return q10HistoricalFailure(&out, req.AllowDegraded, "recorded manifest declaration or blob could not be verified", err)
		}
	}
	out.Status, out.Note, out.Result = "canonical", &note, &Q10Payload{Status: "canonical", Note: &note}
	return out, nil
}

func compareQ10HistoricalHome(supplied, stored KnowledgeHome) error {
	if supplied.HomeProjectID != "" && supplied.HomeProjectID != stored.HomeProjectID || supplied.HomeLocatorID != "" && supplied.HomeLocatorID != stored.HomeLocatorID || supplied.RepoPath != "" && supplied.RepoPath != stored.RepoPath {
		return newFailure(KindInvalidFilter, "PM1.Q10", "caller KnowledgeHome does not match the recorded historical locator", false, "omit Home or supply the recorded locator evidence")
	}
	return nil
}

func q10HistoricalMeta(req Q10Request, commit string) ResultMeta {
	now := time.Now().UTC()
	return ResultMeta{QueryID: "PM1.Q10", ContractVersion: queryContractVersion, ResolvedScope: ResolvedScope{ProductID: req.Product, WorkID: req.Work}, Authority: "authoritative", Freshness: Freshness{ObservedAt: now.Format(time.RFC3339Nano), Age: 0, Stale: false}, OrderingKeys: []string{"canonical_locator"}, Omissions: []string{}, Warnings: []string{"historical_locator_commit:" + commit, "current_head_not_used_for_proof"}}
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
	allowed := map[string]bool{"work_note": true, "lesson": true, "decision": true, "spec": true, "research": true}
	values = orderedStrings(nonEmptyStrings(values))
	for _, value := range values {
		if !allowed[value] {
			return nil, newFailure(KindInvalidFilter, "PM1.Q9", "unknown knowledge kind "+value, false, "use work_note, lesson, decision, spec, or research")
		}
	}
	return values, nil
}

type knowledgeCursor struct {
	Version                               int `json:"version"`
	Product, Project, Component, Text     string
	Since, Until                          string
	Kinds, Tags                           []string
	HomeProjectID, HomeLocatorID, HeadRef string
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
	if err != nil || json.Unmarshal(b, &cursor) != nil || cursor.Version != 1 || cursor.Product != req.Product || cursor.Project != req.Project || cursor.Component != req.Component || cursor.Text != req.Text || cursor.Since != req.Since || cursor.Until != req.Until || cursor.HomeProjectID != req.Home.HomeProjectID || cursor.HomeLocatorID != req.Home.HomeLocatorID || cursor.HeadRef != req.Home.HeadRef || !equalStrings(cursor.Kinds, kinds) || !equalStrings(cursor.Tags, tags) || cursor.CompletedAt == "" || cursor.ID == "" {
		return knowledgeCursor{}, newFailure(KindInvalidCursor, "PM1.Q9", "cursor does not match the requested knowledge query", false, "use a cursor returned for the same query and filters")
	}
	return cursor, nil
}

func buildKnowledgeQuery(req Q9Request, kinds, tags []string, limit int) (string, []any) {
	where := []string{"aw.home_project_id = ?", "aw.home_locator_id = ?"}
	args := []any{req.Home.HomeProjectID, req.Home.HomeLocatorID}
	if req.Product != "" {
		where = append(where, "(aw.scope_mode = 'home' OR EXISTS (SELECT 1 FROM archived_work_products p WHERE p.work_id = aw.id AND p.product_id = ?))")
		args = append(args, req.Product)
	}
	if req.Project != "" {
		where = append(where, "(aw.scope_mode = 'home' OR EXISTS (SELECT 1 FROM archived_work_projects p WHERE p.work_id = aw.id AND p.project_id = ?))")
		args = append(args, req.Project)
	}
	if req.Component != "" {
		where = append(where, "EXISTS (SELECT 1 FROM archived_work_components c WHERE c.work_id = aw.id AND c.component_id = ?)")
		args = append(args, req.Component)
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
	if req.Text != "" {
		where = append(where, "(aw.title LIKE ? OR aw.summary LIKE ?)")
		needle := "%" + req.Text + "%"
		args = append(args, needle, needle)
	}
	if req.Since != "" {
		where = append(where, "aw.completed_at >= ?")
		args = append(args, req.Since)
	}
	if req.Until != "" {
		where = append(where, "aw.completed_at <= ?")
		args = append(args, req.Until)
	}
	if req.Cursor != "" {
		cursor, _ := decodeKnowledgeCursor(req.Cursor, req, kinds, tags)
		where = append(where, "(aw.completed_at < ? OR (aw.completed_at = ? AND aw.id > ?))")
		args = append(args, cursor.CompletedAt, cursor.CompletedAt, cursor.ID)
	}
	args = append(args, limit)
	return `SELECT aw.id,aw.type,aw.title,aw.completed_at,aw.outcome_tag,aw.lesson_tags,aw.summary,aw.home_project_id,aw.home_locator_id,aw.note_path,aw.commit_oid,aw.content_hash,aw.scope_mode,` +
		`COALESCE((SELECT json_group_array(product_id) FROM (SELECT product_id FROM archived_work_products WHERE work_id=aw.id ORDER BY product_id)), '[]'),` +
		`COALESCE((SELECT json_group_array(project_id) FROM (SELECT project_id FROM archived_work_projects WHERE work_id=aw.id ORDER BY project_id)), '[]'),` +
		`COALESCE((SELECT json_group_array(component_id) FROM (SELECT component_id FROM archived_work_components WHERE work_id=aw.id ORDER BY component_id)), '[]'),` +
		`COALESCE((SELECT json_group_array(tag_id) FROM (SELECT tag_id FROM archived_work_tags WHERE work_id=aw.id ORDER BY tag_id)), '[]') ` +
		`FROM archived_work aw WHERE ` + strings.Join(where, " AND ") + ` ORDER BY aw.completed_at DESC, aw.id ASC LIMIT ?`, args
}

func enrichKnowledgeScopes(ctx context.Context, db *sql.DB, item *KnowledgeItem) error {
	for _, scope := range []struct {
		table, column string
		target        *[]string
	}{{"archived_work_products", "product_id", &item.ProductIDs}, {"archived_work_projects", "project_id", &item.ProjectIDs}, {"archived_work_components", "component_id", &item.ComponentIDs}, {"archived_work_tags", "tag_id", &item.TagIDs}} {
		rows, err := db.QueryContext(ctx, "SELECT "+scope.column+" FROM "+scope.table+" WHERE work_id = ? ORDER BY "+scope.column, item.ID)
		if err != nil {
			return wrapFailure(KindUnavailable, "PM1.Q9", "cannot read indexed knowledge scope", true, "retry once the database is readable", err)
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return err
			}
			*scope.target = append(*scope.target, value)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}
