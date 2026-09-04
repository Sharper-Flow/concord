package store

import (
	"context"
	"database/sql"
	"errors"
)

// cleanupTerminalResearch is the archive reconciliation step. It is separate
// from the compaction event fold because active research is direct-table
// authority; repeating it is safe after a crash between the two local commits.
// The archived-work existence join matches work_note rows only: note ids are
// home scoped, and a foreign home may reuse this owner's id for a decision or
// lesson without this work being archived.
func cleanupTerminalResearch(ctx context.Context, s *Store, ownerWorkID string) error {
	if s == nil || s.db == nil {
		return researchUnavailable("store is not open", nil)
	}
	if ownerWorkID == "" {
		return researchInvalid("terminal research cleanup requires an owner work item")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return researchUnavailable("cannot begin terminal research cleanup", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.pack_id FROM active_research_packs p JOIN work_items w ON w.id=p.owner_work_id JOIN archived_work a ON a.id=p.owner_work_id AND a.type='work_note' WHERE p.owner_work_id=? AND w.lifecycle IN ('completed','cancelled','superseded') ORDER BY p.pack_id`, ownerWorkID)
	if err != nil {
		_ = tx.Rollback()
		return researchUnavailable("cannot find terminal research packs", err)
	}
	var packs []string
	for rows.Next() {
		var packID string
		if err := rows.Scan(&packID); err != nil {
			_ = rows.Close()
			_ = tx.Rollback()
			return researchUnavailable("cannot decode terminal research pack", err)
		}
		packs = append(packs, packID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		_ = tx.Rollback()
		return researchUnavailable("cannot read terminal research packs", err)
	}
	_ = rows.Close()
	var blockedConsumer string
	for _, packID := range packs {
		var blocked string
		err := tx.QueryRowContext(ctx, `SELECT c.consumer_work_id FROM active_research_consumers c JOIN work_items w ON w.id=c.consumer_work_id WHERE c.pack_id=? AND c.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded') LIMIT 1`, packID).Scan(&blocked)
		if err == nil {
			blockedConsumer = blocked
			break
		}
		if err != sql.ErrNoRows {
			_ = tx.Rollback()
			return researchUnavailable("cannot inspect terminal research consumers", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_packs WHERE pack_id=?`, packID); err != nil {
			_ = tx.Rollback()
			return researchUnavailable("cannot remove terminal research pack", err)
		}
	}
	if blockedConsumer != "" {
		_ = tx.Rollback()
		return newFailure(KindResearchConsumerBlocked, "terminal_research_cleanup", "required active consumer remains bound: "+blockedConsumer, false, "unbind, rebind, or terminalize every required active consumer")
	}
	if err := tx.Commit(); err != nil {
		return researchUnavailable("cannot commit terminal research cleanup", err)
	}
	return nil
}

func reconcileTerminalResearchOwners(ctx context.Context, s *Store) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT p.owner_work_id FROM active_research_packs p JOIN work_items w ON w.id=p.owner_work_id JOIN archived_work a ON a.id=p.owner_work_id AND a.type='work_note' WHERE w.lifecycle IN ('completed','cancelled','superseded') ORDER BY p.owner_work_id`)
	if err != nil {
		return researchUnavailable("cannot find terminal research owners", err)
	}
	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			_ = rows.Close()
			return researchUnavailable("cannot decode terminal research owner", err)
		}
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return researchUnavailable("cannot read terminal research owners", err)
	}
	_ = rows.Close()
	for _, owner := range owners {
		if err := cleanupTerminalResearch(ctx, s, owner); err != nil {
			var blocked *Failure
			if errors.As(err, &blocked) && blocked.Kind == KindResearchConsumerBlocked {
				continue
			}
			return err
		}
	}
	return nil
}

func snapshotActiveResearchForRebuild(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"packs", "revisions", "findings", "sources", "finding_sources", "consumers"} {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp.rebuild_active_research_"+table); err != nil {
			return researchUnavailable("cannot reset active research rebuild snapshot", err)
		}
	}
	queries := map[string]string{
		"packs":           `CREATE TEMP TABLE rebuild_active_research_packs AS SELECT pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM active_research_packs`,
		"revisions":       `CREATE TEMP TABLE rebuild_active_research_revisions AS SELECT pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at FROM active_research_revisions`,
		"findings":        `CREATE TEMP TABLE rebuild_active_research_findings AS SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status FROM active_research_findings`,
		"sources":         `CREATE TEMP TABLE rebuild_active_research_sources AS SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM active_research_sources`,
		"finding_sources": `CREATE TEMP TABLE rebuild_active_research_finding_sources AS SELECT pack_id,revision,finding_id,source_id FROM active_research_finding_sources`,
		"consumers":       `CREATE TEMP TABLE rebuild_active_research_consumers AS SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers`,
	}
	for _, table := range []string{"packs", "revisions", "findings", "sources", "finding_sources", "consumers"} {
		if _, err := tx.ExecContext(ctx, queries[table]); err != nil {
			return researchUnavailable("cannot snapshot direct-authority active research", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM active_research_packs`); err != nil {
		return researchUnavailable("cannot stage active research for projection rebuild", err)
	}
	return nil
}

func restoreActiveResearchAfterRebuild(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) SELECT pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM temp.rebuild_active_research_packs`,
		`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) SELECT pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at FROM temp.rebuild_active_research_revisions`,
		`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status) SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status FROM temp.rebuild_active_research_findings`,
		`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at) SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM temp.rebuild_active_research_sources`,
		`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) SELECT pack_id,revision,finding_id,source_id FROM temp.rebuild_active_research_finding_sources`,
		`INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM temp.rebuild_active_research_consumers`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return researchUnavailable("cannot restore direct-authority active research after projection rebuild", err)
		}
	}
	return nil
}

func dropActiveResearchRebuildSnapshot(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"packs", "revisions", "findings", "sources", "finding_sources", "consumers"} {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp.rebuild_active_research_"+table); err != nil {
			return researchUnavailable("cannot remove active research rebuild snapshot", err)
		}
	}
	return nil
}
