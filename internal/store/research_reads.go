package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"sort"
)

func (s *Store) ReadResearchPack(ctx context.Context, req ResearchReadRequest) (ResearchPack, error) {
	if s == nil || s.db == nil {
		return ResearchPack{}, researchUnavailable("store is not open", nil)
	}
	if req.PackID == "" || req.Revision < 0 || len(req.FindingIDs) > 32 || len(req.FindingIDs) > 0 && req.Revision == 0 {
		return ResearchPack{}, researchInvalid("pack_id and an exact revision for selected findings are required")
	}
	ids := append([]string(nil), req.FindingIDs...)
	sort.Strings(ids)
	for i, id := range ids {
		if !ValidReference(id) || i > 0 && id == ids[i-1] {
			return ResearchPack{}, researchInvalid("finding_ids must contain distinct valid references")
		}
	}
	req.FindingIDs = ids
	if req.Limit <= 0 || req.Limit > 1000 {
		req.Limit = 1000
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return ResearchPack{}, researchUnavailable("cannot begin research read", err)
	}
	defer tx.Rollback()
	var pack ResearchPack
	if req.Revision == 0 {
		pack, err = readResearchPackTx(ctx, tx, req.PackID, req.Limit)
	} else {
		pack, err = readResearchSelectionTx(ctx, tx, req)
	}
	if err != nil {
		return ResearchPack{}, err
	}
	if err := tx.Commit(); err != nil {
		return ResearchPack{}, researchUnavailable("cannot commit research read", err)
	}
	return pack, nil
}

func (s *Store) ResearchFreshness(ctx context.Context, packID string) (ResearchFreshnessResult, error) {
	return ResearchFreshnessForPack(ctx, s, packID)
}
func ResearchFreshnessForPack(ctx context.Context, s *Store, packID string) (ResearchFreshnessResult, error) {
	var out ResearchFreshnessResult
	if s == nil || s.db == nil {
		return out, researchUnavailable("store is not open", nil)
	}
	var freshness string
	if err := s.db.QueryRowContext(ctx, `SELECT freshness FROM active_research_packs WHERE pack_id=?`, packID).Scan(&freshness); err == sql.ErrNoRows {
		return out, researchNotFound("research pack does not exist")
	} else if err != nil {
		return out, researchUnavailable("cannot read research freshness", err)
	}
	out.Status = ResearchFreshness(freshness)
	var id, status string
	err := s.db.QueryRowContext(ctx, `SELECT consumer_work_id,status FROM (`+researchConsumerFreshnessSQL+`) WHERE pack_id=? AND required=1 AND status<>'current' ORDER BY consumer_work_id LIMIT 1`, packID).Scan(&id, &status)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, researchUnavailable("cannot read research consumers", err)
	}
	out.Blocked = true
	out.Reasons = []string{id + ":" + status}
	return out, nil
}

// ResearchPacksByOwner reads only descriptors, ordered by updated_at DESC and
// pack_id ASC. The continuation belongs to this owner and that keyset.
func (s *Store) ResearchPacksByOwner(ctx context.Context, ownerWorkID string, limit int, cursor string) (ResearchPackPage, error) {
	out := ResearchPackPage{Packs: []ResearchPackDescriptor{}}
	if s == nil || s.db == nil {
		return out, researchUnavailable("store is not open", nil)
	}
	if ownerWorkID == "" {
		return out, researchInvalid("owner work id is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	var after struct {
		Owner     string `json:"owner"`
		UpdatedAt string `json:"updated_at"`
		PackID    string `json:"pack_id"`
	}
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || decodeCursorFields(data, &after) != nil || after.Owner != ownerWorkID || after.UpdatedAt == "" || after.PackID == "" {
			return out, newFailure(KindInvalidCursor, "research_read", "research owner cursor does not match the query", false, "restart the owner query without the cursor")
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM active_research_packs WHERE owner_work_id=? AND (?='' OR updated_at<? OR (updated_at=? AND pack_id>?)) ORDER BY updated_at DESC, pack_id ASC LIMIT ?`, ownerWorkID, cursor, after.UpdatedAt, after.UpdatedAt, after.PackID, limit+1)
	if err != nil {
		return out, researchUnavailable("cannot list research pack descriptors", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p ResearchPackDescriptor
		if err := rows.Scan(&p.PackID, &p.OwnerWorkID, &p.CurrentRevision, &p.Freshness, &p.ExpectedVersion, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return out, researchUnavailable("cannot decode research descriptor", err)
		}
		out.Packs = append(out.Packs, p)
	}
	if err := rows.Err(); err != nil {
		return out, researchUnavailable("cannot read research descriptors", err)
	}
	if len(out.Packs) > limit {
		out.Packs = out.Packs[:limit]
		last := out.Packs[limit-1]
		after.Owner, after.UpdatedAt, after.PackID = ownerWorkID, last.UpdatedAt, last.PackID
		data, err := json.Marshal(after)
		if err != nil {
			return out, researchUnavailable("cannot encode research continuation", err)
		}
		next := base64.RawURLEncoding.EncodeToString(data)
		out.NextCursor = &next
	}
	return out, nil
}

func (s *Store) RequiredResearchFreshness(ctx context.Context, packID, consumerWorkID string) (ResearchFreshness, error) {
	return requiredResearchFreshness(ctx, s.db, packID, consumerWorkID)
}

const researchConsumerFreshnessSQL = `SELECT c.pack_id,c.consumer_work_id,c.required,
	CASE WHEN c.required=0 THEN 'current'
		WHEN r.freshness IS NULL THEN 'unknown'
		ELSE r.freshness END AS status
	FROM active_research_consumers c
	LEFT JOIN active_research_revisions r ON r.pack_id=c.pack_id AND r.revision=c.revision
	JOIN work_items w ON w.id=c.consumer_work_id
	WHERE w.lifecycle NOT IN ('completed','cancelled','superseded')`

func requiredResearchFreshness(ctx context.Context, q queryer, packID, consumerWorkID string) (ResearchFreshness, error) {
	var status string
	// Required freshness belongs to the pinned revision, not the pack summary.
	err := q.QueryRowContext(ctx, `SELECT status FROM (`+researchConsumerFreshnessSQL+`) WHERE pack_id=? AND consumer_work_id=?`, packID, consumerWorkID).Scan(&status)
	if err == sql.ErrNoRows {
		return ResearchUnknown, researchNotFound("active research consumer binding does not exist")
	}
	if err != nil {
		return ResearchUnknown, researchUnavailable("cannot read consumer freshness", err)
	}
	return ResearchFreshness(status), nil
}

func readResearchPackRow(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, packID string, _ int) (ResearchPack, error) {
	var p ResearchPack
	err := q.QueryRowContext(ctx, `SELECT pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM active_research_packs WHERE pack_id=?`, packID).Scan(&p.PackID, &p.OwnerWorkID, &p.CurrentRevision, &p.Freshness, &p.ExpectedVersion, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return p, researchNotFound("research pack does not exist")
	}
	if err != nil {
		return p, researchUnavailable("cannot read research pack", err)
	}
	return p, nil
}

func readResearchPackTx(ctx context.Context, tx *sql.Tx, packID string, limit int) (ResearchPack, error) {
	p, err := readResearchPackRow(ctx, tx, packID, limit)
	if err != nil {
		return p, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness FROM active_research_revisions WHERE pack_id=? ORDER BY revision LIMIT ?`, packID, limit+1)
	if err != nil {
		return p, researchUnavailable("cannot read research revisions", err)
	}
	var revisions []ResearchRevision
	for rows.Next() {
		var r ResearchRevision
		r.PackID = packID
		if err := rows.Scan(&r.Revision, &r.Question, &r.ScopeIn, &r.ScopeOut, &r.DoneWhen, &r.Method, &r.CreatedAt, &r.Freshness); err != nil {
			_ = rows.Close()
			return p, researchUnavailable("cannot decode research revision", err)
		}
		revisions = append(revisions, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return p, researchUnavailable("cannot read research revisions", err)
	}
	_ = rows.Close()
	if len(revisions) > limit {
		return ResearchPack{}, researchReadBounded("research revisions exceed the bounded read limit")
	}
	for _, r := range revisions {
		r.Findings, err = readFindingsTx(ctx, tx, packID, r.Revision, limit)
		if err != nil {
			return p, err
		}
		r.Sources, err = readSourcesTx(ctx, tx, packID, r.Revision, limit)
		if err != nil {
			return p, err
		}
		p.Revisions = append(p.Revisions, r)
	}
	rows, err = tx.QueryContext(ctx, `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers WHERE pack_id=? ORDER BY revision,consumer_work_id LIMIT ?`, packID, limit+1)
	if err != nil {
		return p, researchUnavailable("cannot read research consumers", err)
	}
	for rows.Next() {
		var c ResearchConsumer
		var required int
		if err := rows.Scan(&c.PackID, &c.Revision, &c.ConsumerWorkID, &c.UseRole, &required, &c.AcceptedAt); err != nil {
			_ = rows.Close()
			return p, researchUnavailable("cannot decode research consumer", err)
		}
		c.Required = required != 0
		p.Consumers = append(p.Consumers, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return p, researchUnavailable("cannot read research consumers", err)
	}
	_ = rows.Close()
	if len(p.Consumers) > limit {
		return ResearchPack{}, researchReadBounded("research consumers exceed the bounded read limit")
	}
	return p, nil
}

func readRevisionRowTx(ctx context.Context, tx *sql.Tx, pack string, revision int64) (ResearchRevision, error) {
	r := ResearchRevision{PackID: pack, Revision: revision}
	err := tx.QueryRowContext(ctx, `SELECT question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness FROM active_research_revisions WHERE pack_id=? AND revision=?`, pack, revision).Scan(&r.Question, &r.ScopeIn, &r.ScopeOut, &r.DoneWhen, &r.Method, &r.CreatedAt, &r.Freshness)
	if err == sql.ErrNoRows {
		return r, researchNotFound("research revision does not exist")
	}
	if err != nil {
		return r, researchUnavailable("cannot read research revision", err)
	}
	return r, nil
}

func readResearchSelectionTx(ctx context.Context, tx *sql.Tx, req ResearchReadRequest) (ResearchPack, error) {
	p, err := readResearchPackRow(ctx, tx, req.PackID, req.Limit)
	if err != nil {
		return ResearchPack{}, err
	}
	r, err := readRevisionRowTx(ctx, tx, req.PackID, req.Revision)
	if err != nil {
		return ResearchPack{}, err
	}
	if len(req.FindingIDs) == 0 {
		r.Findings, err = readFindingsTx(ctx, tx, req.PackID, req.Revision, req.Limit)
		if err == nil {
			r.Sources, err = readSourcesTx(ctx, tx, req.PackID, req.Revision, req.Limit)
		}
	} else {
		if len(req.FindingIDs) > req.Limit {
			return ResearchPack{}, researchReadBounded("selected findings exceed the bounded read limit")
		}
		sourceIDs := map[string]bool{}
		linkCount := 0
		for _, id := range req.FindingIDs {
			f, readErr := readFindingTx(ctx, tx, req.PackID, req.Revision, id)
			if readErr != nil {
				return ResearchPack{}, readErr
			}
			rows, readErr := tx.QueryContext(ctx, `SELECT source_id FROM active_research_finding_sources WHERE pack_id=? AND revision=? AND finding_id=? ORDER BY source_id LIMIT ?`, req.PackID, req.Revision, id, req.Limit-linkCount+1)
			if readErr != nil {
				return ResearchPack{}, researchUnavailable("cannot read selected provenance", readErr)
			}
			for rows.Next() {
				var sourceID string
				if err := rows.Scan(&sourceID); err != nil {
					rows.Close()
					return ResearchPack{}, researchUnavailable("cannot decode selected provenance", err)
				}
				linkCount++
				if linkCount > req.Limit {
					rows.Close()
					return ResearchPack{}, researchReadBounded("selected finding-source links exceed the bounded read limit")
				}
				f.SourceIDs = append(f.SourceIDs, sourceID)
				sourceIDs[sourceID] = true
			}
			readErr = rows.Err()
			rows.Close()
			if readErr != nil {
				return ResearchPack{}, researchUnavailable("cannot read selected provenance", readErr)
			}
			r.Findings = append(r.Findings, f)
		}
		ids := make([]string, 0, len(sourceIDs))
		for id := range sourceIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			source, readErr := readSourceTx(ctx, tx, req.PackID, req.Revision, id)
			if readErr != nil {
				return ResearchPack{}, readErr
			}
			r.Sources = append(r.Sources, source)
		}
	}
	if err != nil {
		return ResearchPack{}, err
	}
	p.Revisions = []ResearchRevision{r}
	return p, nil
}

func readRevisionTx(ctx context.Context, tx *sql.Tx, pack string, revision int64) (ResearchRevision, error) {
	r, err := readRevisionRowTx(ctx, tx, pack, revision)
	if err != nil {
		return r, err
	}
	r.Findings, err = readFindingsTx(ctx, tx, pack, revision, 1000)
	if err != nil {
		return r, err
	}
	r.Sources, err = readSourcesTx(ctx, tx, pack, revision, 1000)
	return r, err
}

func readFindingTx(ctx context.Context, tx *sql.Tx, pack string, revision int64, id string) (ResearchFinding, error) {
	var f ResearchFinding
	err := tx.QueryRowContext(ctx, `SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode FROM active_research_findings WHERE pack_id=? AND revision=? AND finding_id=?`, pack, revision, id).Scan(&f.PackID, &f.Revision, &f.FindingID, &f.Kind, &f.Statement, &f.Confidence, &f.Freshness, &f.Status, &f.Scopes.Mode)
	if err == sql.ErrNoRows {
		return f, researchNotFound("finding does not exist")
	}
	if err != nil {
		return f, researchUnavailable("cannot read finding", err)
	}
	f.Scopes, err = readResearchFindingScopes(ctx, tx, pack, revision, id, f.Scopes.Mode)
	if err != nil {
		return f, err
	}
	return f, nil
}

func readFindingsTx(ctx context.Context, tx *sql.Tx, pack string, revision int64, limit int) ([]ResearchFinding, error) {
	rows, err := tx.QueryContext(ctx, `SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode FROM active_research_findings WHERE pack_id=? AND revision=? ORDER BY finding_id LIMIT ?`, pack, revision, limit+1)
	if err != nil {
		return nil, researchUnavailable("cannot read findings", err)
	}
	var out []ResearchFinding
	for rows.Next() {
		var f ResearchFinding
		if err := rows.Scan(&f.PackID, &f.Revision, &f.FindingID, &f.Kind, &f.Statement, &f.Confidence, &f.Freshness, &f.Status, &f.Scopes.Mode); err != nil {
			_ = rows.Close()
			return nil, researchUnavailable("cannot decode finding", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, researchUnavailable("cannot read findings", err)
	}
	_ = rows.Close()
	if len(out) > limit {
		return nil, researchReadBounded("research findings exceed the bounded read limit")
	}
	rows, err = tx.QueryContext(ctx, `SELECT finding_id,source_id FROM active_research_finding_sources WHERE pack_id=? AND revision=? ORDER BY finding_id,source_id LIMIT ?`, pack, revision, limit+1)
	if err != nil {
		return nil, researchUnavailable("cannot read finding sources", err)
	}
	defer rows.Close()
	findingByID := make(map[string]*ResearchFinding, len(out))
	for i := range out {
		findingByID[out[i].FindingID] = &out[i]
	}
	linkCount := 0
	for rows.Next() {
		linkCount++
		if linkCount > limit {
			return nil, researchReadBounded("finding-source links exceed the bounded read limit")
		}
		var findingID, sourceID string
		if err := rows.Scan(&findingID, &sourceID); err != nil {
			return nil, researchUnavailable("cannot decode finding source", err)
		}
		finding, ok := findingByID[findingID]
		if !ok {
			return nil, researchInvalid("finding-source link references a finding outside the bounded result")
		}
		finding.SourceIDs = append(finding.SourceIDs, sourceID)
	}
	if err := rows.Err(); err != nil {
		return nil, researchUnavailable("cannot read finding sources", err)
	}
	for i := range out {
		out[i].Scopes, err = readResearchFindingScopes(ctx, tx, pack, revision, out[i].FindingID, out[i].Scopes.Mode)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readSourceTx(ctx context.Context, tx *sql.Tx, pack string, revision int64, id string) (ResearchSource, error) {
	var s ResearchSource
	var published sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM active_research_sources WHERE pack_id=? AND revision=? AND source_id=?`, pack, revision, id).Scan(&s.PackID, &s.Revision, &s.SourceID, &s.Kind, &s.Locator, &s.Title, &s.PublisherOrAuthor, &published, &s.AccessedAt)
	if err == sql.ErrNoRows {
		return s, researchNotFound("source does not exist")
	}
	if err != nil {
		return s, researchUnavailable("cannot read source", err)
	}
	s.PublishedAt = published.String
	return s, nil
}

func readSourcesTx(ctx context.Context, tx *sql.Tx, pack string, revision int64, limit int) ([]ResearchSource, error) {
	rows, err := tx.QueryContext(ctx, `SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM active_research_sources WHERE pack_id=? AND revision=? ORDER BY source_id LIMIT ?`, pack, revision, limit+1)
	if err != nil {
		return nil, researchUnavailable("cannot read sources", err)
	}
	defer rows.Close()
	var out []ResearchSource
	for rows.Next() {
		var s ResearchSource
		var published sql.NullString
		if err := rows.Scan(&s.PackID, &s.Revision, &s.SourceID, &s.Kind, &s.Locator, &s.Title, &s.PublisherOrAuthor, &published, &s.AccessedAt); err != nil {
			return nil, researchUnavailable("cannot decode source", err)
		}
		s.PublishedAt = published.String
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, researchUnavailable("cannot read sources", err)
	}
	if len(out) > limit {
		return nil, researchReadBounded("research sources exceed the bounded read limit")
	}
	return out, nil
}

func readConsumerTx(ctx context.Context, tx *sql.Tx, pack string, revision int64, id string) (ResearchConsumer, error) {
	var c ResearchConsumer
	var required int
	err := tx.QueryRowContext(ctx, `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers WHERE pack_id=? AND revision=? AND consumer_work_id=?`, pack, revision, id).Scan(&c.PackID, &c.Revision, &c.ConsumerWorkID, &c.UseRole, &required, &c.AcceptedAt)
	if err == sql.ErrNoRows {
		return c, researchNotFound("consumer binding does not exist")
	}
	if err != nil {
		return c, researchUnavailable("cannot read consumer binding", err)
	}
	c.Required = required != 0
	return c, nil
}
