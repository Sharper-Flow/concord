package store

import (
	"context"
	"database/sql"
	"time"
)

// RecordWorkflowStaleness persists the last bounded staleness observation for
// a workflow. It is an external authority observation, not a workflow
// projection write, and is therefore recorded through its own durable API.
func RecordWorkflowStaleness(ctx context.Context, s *Store, workID, ruleID, severity string, drifted bool, observedAt time.Time) error {
	if s == nil || s.db == nil {
		return newFailure(KindUnavailable, "workflow_staleness", "store is not open", false, "open the authority database")
	}
	if workID == "" || ruleID == "" || (severity != "warning" && severity != "block") || observedAt.IsZero() {
		return newFailure(KindInvalidOperation, "workflow_staleness", "staleness observation is incomplete", false, "supply work, rule, severity, and observation time")
	}
	if !drifted {
		_, err := s.db.ExecContext(ctx, `DELETE FROM workflow_staleness_warnings WHERE work_id=? AND rule_id=?`, workID, ruleID)
		if err != nil {
			return wrapFailure(KindUnavailable, "workflow_staleness", "cannot clear staleness observation", true, "retry once the database is writable", err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO workflow_staleness_warnings(work_id,rule_id,severity,observed_at) VALUES(?,?,?,?) ON CONFLICT(work_id,rule_id) DO UPDATE SET severity=excluded.severity,observed_at=excluded.observed_at`, workID, ruleID, severity, observedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return wrapFailure(KindUnavailable, "workflow_staleness", "cannot persist staleness observation", true, "retry once the database is writable", err)
	}
	return nil
}

// ObserveWorkflowCompletionInput is the owning production boundary for a
// completion-time staleness observation. It accepts the same structured action
// payload that completion consumes, persists the observation before the
// completion attempt, and leaves the subsequent bounded read as its authority.
func ObserveWorkflowCompletionInput(ctx context.Context, s *Store, workID string, fields map[string]any, observedAt time.Time) error {
	raw, ok := fields["observed_drift"]
	if !ok {
		return nil
	}
	drift, ok := raw.(map[string]any)
	if !ok {
		return newFailure(KindInvalidOperation, "workflow_staleness", "observed_drift is not an object", false, "supply the typed staleness observation")
	}
	ruleID, _ := fields["staleness_rule_id"].(string)
	severity, _ := drift["severity"].(string)
	drifted, _ := drift["drifted"].(bool)
	if ruleID == "" {
		return newFailure(KindInvalidOperation, "workflow_staleness", "staleness_rule_id is required", false, "supply the owning staleness rule")
	}
	return RecordWorkflowStaleness(ctx, s, workID, ruleID, severity, drifted, observedAt)
}

func readWorkflowStalenessWarnings(ctx context.Context, db *sql.DB, workID string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT rule_id FROM workflow_staleness_warnings WHERE work_id=? ORDER BY rule_id`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_staleness", "cannot read staleness warnings", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var warnings []string
	for rows.Next() {
		var ruleID string
		if err := rows.Scan(&ruleID); err != nil {
			return nil, err
		}
		warnings = append(warnings, ruleID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return warnings, nil
}
