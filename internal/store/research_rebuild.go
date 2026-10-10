package store

import (
	"context"
	"database/sql"
)

// researchRetirementDeleteGuardName is the migration 123 trigger that applies
// retirement authority to every delete from active_research_packs: only a
// pack whose owner is terminal and that carries no active consumer pin
// (required or optional) may be deleted, which is exactly the eligibility the
// explicit research_retire batch classifies. Projection rebuild stages and
// restores the pack rows byte for byte inside one transaction, so the rebuild
// drops this trigger before its staging delete and recreates it after the
// restore, following the migration 52 locator guard.
const researchRetirementDeleteGuardName = "active_research_packs_retirement_delete_guard"

func snapshotActiveResearchForRebuild(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"packs", "revisions", "findings", "sources", "finding_sources", "finding_scopes", "consumers"} {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp.rebuild_active_research_"+table); err != nil {
			return researchUnavailable("cannot reset active research rebuild snapshot", err)
		}
	}
	queries := map[string]string{
		"packs":           `CREATE TEMP TABLE rebuild_active_research_packs AS SELECT pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM active_research_packs`,
		"revisions":       `CREATE TEMP TABLE rebuild_active_research_revisions AS SELECT pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness FROM active_research_revisions`,
		"findings":        `CREATE TEMP TABLE rebuild_active_research_findings AS SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode FROM active_research_findings`,
		"sources":         `CREATE TEMP TABLE rebuild_active_research_sources AS SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM active_research_sources`,
		"finding_sources": `CREATE TEMP TABLE rebuild_active_research_finding_sources AS SELECT pack_id,revision,finding_id,source_id FROM active_research_finding_sources`,
		"finding_scopes":  `CREATE TEMP TABLE rebuild_active_research_finding_scopes AS SELECT pack_id,revision,finding_id,scope_kind,scope_id FROM active_research_finding_scopes`,
		"consumers":       `CREATE TEMP TABLE rebuild_active_research_consumers AS SELECT pack_id,revision,consumer_work_id,use_role,required,accepted_at FROM active_research_consumers`,
	}
	for _, table := range []string{"packs", "revisions", "findings", "sources", "finding_sources", "finding_scopes", "consumers"} {
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
		`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness) SELECT pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness FROM temp.rebuild_active_research_revisions`,
		`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode) SELECT pack_id,revision,finding_id,kind,statement,confidence,freshness,status,scope_mode FROM temp.rebuild_active_research_findings`,
		`INSERT INTO active_research_sources(pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at) SELECT pack_id,revision,source_id,kind,locator,title,publisher_or_author,published_at,accessed_at FROM temp.rebuild_active_research_sources`,
		`INSERT INTO active_research_finding_sources(pack_id,revision,finding_id,source_id) SELECT pack_id,revision,finding_id,source_id FROM temp.rebuild_active_research_finding_sources`,
		`INSERT INTO active_research_finding_scopes(pack_id,revision,finding_id,scope_kind,scope_id) SELECT pack_id,revision,finding_id,scope_kind,scope_id FROM temp.rebuild_active_research_finding_scopes`,
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
	for _, table := range []string{"packs", "revisions", "findings", "sources", "finding_sources", "finding_scopes", "consumers"} {
		if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS temp.rebuild_active_research_"+table); err != nil {
			return researchUnavailable("cannot remove active research rebuild snapshot", err)
		}
	}
	return nil
}
