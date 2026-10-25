package store

import (
	"context"
	"strings"
)

// retireEventLog runs only inside the schema transaction. Guard definitions are
// carried through the table rebuilds without changing their write restrictions.
func retireEventLog(ctx context.Context, q migrationRunner, ddl string) error {
	if err := backfillEffectReceiptDigests(ctx, q); err != nil {
		return err
	}
	var ready bool
	if err := q.QueryRowContext(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM fold_guard)
 AND NOT EXISTS(SELECT 1 FROM domain_events e LEFT JOIN effect_receipts r ON r.event_id=e.event_id
   WHERE r.event_id IS NULL OR r.effect_seq IS NOT e.seq OR r.kind IS NOT e.kind
   OR r.subject_type IS NOT e.subject_type OR r.subject_id IS NOT e.subject_id
   OR r.actor IS NOT e.actor OR r.payload_version IS NOT e.payload_version)
 AND NOT EXISTS(SELECT 1 FROM effect_receipts r LEFT JOIN domain_events e ON e.event_id=r.event_id WHERE e.event_id IS NULL)
 AND NOT EXISTS(SELECT 1 FROM sqlite_schema s,pragma_foreign_key_list(s.name) f
   WHERE s.type='table' AND f."table"='domain_events'
   AND s.name NOT IN ('workflow_overlap_resolutions','external_observations','work_size_overrides'))
 AND NOT EXISTS(SELECT 1 FROM sqlite_schema WHERE type IN ('view','trigger')
   AND instr(lower(sql),'domain_events')>0
   AND name NOT IN ('domain_events_no_update','domain_events_no_delete','domain_events_advance_effect_sequence'))
 AND NOT EXISTS(SELECT 1 FROM effect_receipts WHERE payload_digest IS NULL
   OR length(payload_digest)<>71 OR substr(payload_digest,1,7)<>'sha256:'
   OR substr(payload_digest,8) GLOB '*[^0-9a-f]*')
 AND EXISTS(SELECT 1 FROM store_sequences WHERE name='effect' AND last>=max(
   (SELECT coalesce(max(seq),0) FROM domain_events),
   (SELECT coalesce(max(seq),0) FROM sqlite_sequence WHERE name='domain_events'),
   (SELECT coalesce(max(effect_seq),0) FROM effect_receipts)))
 AND EXISTS(SELECT 1 FROM store_sequences WHERE name='relation' AND last>=max(
   (SELECT coalesce(max(relation_seq),0) FROM effect_receipts),
   (SELECT count(*) FROM domain_events WHERE kind IN
    ('relation.added','work.reopened_from_superseded','work.superseded','workflow.overlap_resolved','workflow.successor_linked')
    OR (kind='work.created' AND json_extract(payload,'$.raised_from_work_id')<>''))))`).Scan(&ready); err != nil {
		return wrapFailure(KindUnavailable, "migrate", "cannot check event log retirement prerequisites", false, "restore a verified snapshot before retrying the upgrade", err)
	}
	if !ready {
		return newFailure(KindInvariantViolation, "migrate", "event log retirement requires complete effect receipts, monotone sequences, and an unowned write guard", false, "restore a verified snapshot before retrying the upgrade")
	}
	rows, err := q.QueryContext(ctx, `SELECT name,sql FROM sqlite_schema WHERE type='trigger' AND instr(sql,'fold_guard')>0 ORDER BY name`)
	if err != nil {
		return err
	}
	type guardDefinition struct{ name, ddl string }
	var guards []guardDefinition
	for rows.Next() {
		var guard guardDefinition
		if err := rows.Scan(&guard.name, &guard.ddl); err != nil {
			_ = rows.Close()
			return err
		}
		guards = append(guards, guard)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, guard := range guards {
		if _, err := q.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(guard.name, `"`, `""`)+`"`); err != nil {
			return err
		}
	}
	if _, err := q.ExecContext(ctx, ddl); err != nil {
		return wrapFailure(KindUnavailable, "migrate", "migration 151 (event_log_retirement) failed", false, "restore a verified snapshot before retrying the upgrade", err)
	}
	for _, guard := range guards {
		definition := strings.ReplaceAll(strings.ReplaceAll(guard.ddl, "fold_guard", "write_guard"), "fold-only", "write-seam-only")
		if _, err := q.ExecContext(ctx, definition); err != nil {
			return err
		}
	}
	var invalid bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_foreign_key_check('workflow_overlap_resolutions'))
 OR EXISTS(SELECT 1 FROM pragma_foreign_key_check('external_observations'))
 OR EXISTS(SELECT 1 FROM pragma_foreign_key_check('work_size_overrides'))
 OR EXISTS(SELECT 1 FROM sqlite_schema WHERE name IN ('domain_events','fold_guard')
 OR instr(lower(sql),'domain_events')>0 OR instr(lower(sql),'fold_guard')>0 OR instr(sql,'fold-only')>0)`).Scan(&invalid); err != nil {
		return err
	}
	if invalid {
		return newFailure(KindInvariantViolation, "migrate", "event log retirement left an obsolete dependency or invalid foreign key", false, "restore a verified snapshot before retrying the upgrade")
	}
	return nil
}
