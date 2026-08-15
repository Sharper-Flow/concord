package store

import (
	"context"
	"database/sql"
)

func (s *Store) ReadResearchPack(ctx context.Context, packID string, limit int) (ResearchPack, error) {
	return ReadResearchPack(ctx, s, packID, limit)
}
func ReadResearchPack(ctx context.Context, s *Store, packID string, limit int) (ResearchPack, error) {
	if s == nil || s.db == nil {
		return ResearchPack{}, researchUnavailable("store is not open", nil)
	}
	if packID == "" {
		return ResearchPack{}, researchInvalid("pack_id is required")
	}
	if err := reconcileTerminalResearchOwners(ctx, s); err != nil {
		return ResearchPack{}, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	return readResearchPack(ctx, s, packID, limit)
}
func (s *Store) ReadCompleteResearchPack(ctx context.Context, packID string) (ResearchPack, error) {
	return ReadResearchPack(ctx, s, packID, 1000)
}
func (s *Store) GetResearchPack(ctx context.Context, packID string, limit int) (ResearchPack, error) {
	return ReadResearchPack(ctx, s, packID, limit)
}
func ReadCompleteResearchPack(ctx context.Context, s *Store, packID string) (ResearchPack, error) {
	return ReadResearchPack(ctx, s, packID, 1000)
}
func GetResearchPack(ctx context.Context, s *Store, packID string, limit int) (ResearchPack, error) {
	return ReadResearchPack(ctx, s, packID, limit)
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
	err := s.db.QueryRowContext(ctx, `SELECT consumer_work_id, status FROM (
		SELECT c.consumer_work_id,
			CASE
				WHEN NOT EXISTS(SELECT 1 FROM active_research_revisions r WHERE r.pack_id=c.pack_id AND r.revision=c.revision) THEN 'unknown'
				WHEN p.freshness='stale' THEN 'stale'
				WHEN p.freshness='unknown' THEN 'unknown'
				ELSE 'current'
			END AS status
		FROM active_research_consumers c
		JOIN active_research_packs p ON p.pack_id=c.pack_id
		JOIN work_items w ON w.id=c.consumer_work_id
		WHERE c.pack_id=? AND c.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded')
	) WHERE status <> 'current' ORDER BY consumer_work_id LIMIT 1`, packID).Scan(&id, &status)
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
func CheckResearchFreshness(ctx context.Context, s *Store, packID string) (ResearchFreshnessResult, error) {
	return ResearchFreshnessForPack(ctx, s, packID)
}

// ResearchPacksByOwner lists the active packs owned by one work item, newest
// update first, bounded by limit. It backs the agent read surface where a
// consumer resolves a pack by its owning work item.
func ResearchPacksByOwner(ctx context.Context, s *Store, ownerWorkID string, limit int) ([]ResearchPack, error) {
	if s == nil || s.db == nil {
		return nil, researchUnavailable("store is not open", nil)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT pack_id FROM active_research_packs WHERE owner_work_id=? ORDER BY updated_at DESC, pack_id LIMIT ?`, ownerWorkID, limit)
	if err != nil {
		return nil, researchUnavailable("cannot list research packs by owner", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, researchUnavailable("cannot decode research pack id", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, researchUnavailable("cannot read research pack ids", err)
	}
	rows.Close()
	packs := make([]ResearchPack, 0, len(ids))
	for _, id := range ids {
		pack, err := readResearchPack(ctx, s, id, limit)
		if err != nil {
			return nil, err
		}
		packs = append(packs, pack)
	}
	return packs, nil
}

func (s *Store) RequiredResearchFreshness(ctx context.Context, packID, consumerWorkID string) (ResearchFreshness, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN c.required=0 THEN 'current' WHEN NOT EXISTS(SELECT 1 FROM active_research_revisions r WHERE r.pack_id=c.pack_id AND r.revision=c.revision) THEN 'unknown' WHEN p.freshness='stale' THEN 'stale' WHEN p.freshness='unknown' THEN 'unknown' ELSE 'current' END FROM active_research_consumers c JOIN active_research_packs p ON p.pack_id=c.pack_id JOIN work_items w ON w.id=c.consumer_work_id WHERE c.pack_id=? AND c.consumer_work_id=? AND w.lifecycle NOT IN ('completed','cancelled','superseded')`, packID, consumerWorkID).Scan(&status)
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
}, packID string, limit int) (ResearchPack, error) {
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
	rows, err := tx.QueryContext(ctx, `SELECT revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at FROM active_research_revisions WHERE pack_id=? ORDER BY revision LIMIT ?`, packID, limit)
	if err != nil {
		return p, researchUnavailable("cannot read research revisions", err)
	}
	var revisions []ResearchRevision
	for rows.Next() {
		var r ResearchRevision
		r.PackID = packID
		if err := rows.Scan(&r.Revision, &r.Question, &r.ScopeIn, &r.ScopeOut, &r.DoneWhen, &r.Method, &r.CreatedAt); err != nil {
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
	rows, err = tx.QueryContext(ctx, `SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers WHERE pack_id=? ORDER BY revision,consumer_work_id LIMIT ?`, packID, limit)
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
	return p, nil
}

func readResearchPack(ctx context.Context, s *Store, packID string, limit int) (ResearchPack, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ResearchPack{}, researchUnavailable("cannot begin research read", err)
	}
	pack, err := readResearchPackTx(ctx, tx, packID, limit)
	if err != nil {
		_ = tx.Rollback()
		return pack, err
	}
	if err := tx.Commit(); err != nil {
		return ResearchPack{}, researchUnavailable("cannot commit research read", err)
	}
	return pack, nil
}

func readRevisionTx(ctx context.Context, tx *sql.Tx, pack string, revision int64) (ResearchRevision, error) {
	p, err := readResearchPackTx(ctx, tx, pack, 1000)
	if err != nil {
		return ResearchRevision{}, err
	}
	for _, r := range p.Revisions {
		if r.Revision == revision {
			return r, nil
		}
	}
	return ResearchRevision{}, researchNotFound("research revision does not exist")
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
