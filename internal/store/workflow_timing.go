package store

import (
	"context"
	"database/sql"
	"slices"
	"time"
)

type workflowTiming struct {
	RootWorkID      string
	DefineEventID   string
	DefineStartedAt string
	QueueEventID    string
	QueueEnteredAt  string
	QueueArtifact   string
}

const workflowDefineStartPredicate = `e.kind=? AND a.actor_class='agent'
	AND coalesce(json_extract(e.payload,'$.action_id'),'')<>'' AND substr(e.event_id,-?)<>?`

// Define provenance follows the resize root; a successor never inherits its
// predecessor's merge-route submission.
func readWorkflowTiming(ctx context.Context, q queryer, workID string) (workflowTiming, error) {
	lineage, err := readWorkLineage(ctx, q, workID)
	if err != nil {
		return workflowTiming{}, err
	}
	result := workflowTiming{RootWorkID: lineage.RootWorkID}
	err = q.QueryRowContext(ctx, `SELECT coalesce(d.define_event_id,''),coalesce(d.define_started_at,''),
		coalesce(s.queue_event_id,''),coalesce(s.queue_entered_at,''),coalesce(s.queue_artifact,'')
		FROM work_items w LEFT JOIN workflow_timing d ON d.work_id=?
		LEFT JOIN workflow_timing s ON s.work_id=w.id WHERE w.id=?`, lineage.RootWorkID, workID).
		Scan(&result.DefineEventID, &result.DefineStartedAt, &result.QueueEventID, &result.QueueEnteredAt, &result.QueueArtifact)
	if err != nil {
		return workflowTiming{}, workflowProjectionError(err, "cannot read workflow timing")
	}
	return result, nil
}

// recordWorkflowTimingTx folds provenance, not an observation of the external
// queue. The caller has already admitted the completion on its pinned step.
func recordWorkflowTimingTx(ctx context.Context, tx *sql.Tx, event Event, p workflowActionCompletedPayload, definition WorkflowDefinition) error {
	lineage, err := readWorkLineage(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	// Read the first qualifying action across the predecessors as well as this
	// item. A first sizing or a resize may adopt timing after that action, but
	// its source and clock stay the original recorded event's.
	var sourceID, sourceAt string
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE history(work_id) AS (
		SELECT ? UNION ALL SELECT l.predecessor_work_id FROM work_resize_lineage l JOIN history h ON l.successor_work_id=h.work_id)
		SELECT e.event_id,e.occurred_at FROM domain_events e JOIN workflow_actors a ON a.actor_ref=e.actor
		WHERE e.subject_type='work_item' AND e.subject_id IN (SELECT work_id FROM history)
		AND `+workflowDefineStartPredicate+` AND e.seq<=?
		ORDER BY e.seq LIMIT 1`, event.SubjectID, WorkflowActionCompleted, len(workflowDerivedWorkerFailureEventIDSuffix), workflowDerivedWorkerFailureEventIDSuffix, event.Seq).Scan(&sourceID, &sourceAt)
	if err != nil && err != sql.ErrNoRows {
		return workflowProjectionError(err, "cannot read the workflow's first agent action")
	}
	if err == nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_timing(work_id,define_event_id,define_started_at) VALUES(?,?,?)
			ON CONFLICT(work_id) DO UPDATE SET define_event_id=excluded.define_event_id,define_started_at=excluded.define_started_at
			WHERE workflow_timing.define_event_id IS NULL`, lineage.RootWorkID, sourceID, sourceAt); err != nil {
			return workflowProjectionError(err, "cannot record workflow define start")
		}
	}
	if p.ActionID != "record_delivery" || p.DeliveryState != "asserted" || p.DeliveryArtifact == "" || !slices.Contains(definition.StepGraph.TerminalSteps, p.StepID) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_timing(work_id,queue_event_id,queue_entered_at,queue_artifact) VALUES(?,?,?,?)
		ON CONFLICT(work_id) DO UPDATE SET queue_event_id=excluded.queue_event_id,queue_entered_at=excluded.queue_entered_at,queue_artifact=excluded.queue_artifact
		WHERE workflow_timing.queue_event_id IS NULL`, event.SubjectID, event.EventID, event.OccurredAt.UTC().Format(time.RFC3339Nano), p.DeliveryArtifact); err != nil {
		return workflowProjectionError(err, "cannot record workflow merge-route submission")
	}
	return nil
}
