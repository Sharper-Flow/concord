package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"reflect"
	"strconv"
	"strings"
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

type workflowCompletionTiming struct {
	RootWorkID         string                   `json:"root_work_id"`
	DefineStartEventID *string                  `json:"define_start_event_id"`
	Submission         workflowTimingSubmission `json:"submission"`
}

type workflowTimingSubmission struct {
	Kind     string `json:"kind"`
	Artifact string `json:"artifact,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// The registered Project owns merge-route identity; no caller repository or
// external queue state participates in this classification.
func workflowTimingRepositories(ctx context.Context, q queryer, workID string) ([]string, error) {
	var projectID string
	err := q.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	locators, err := projectLocators(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	var repositories []string
	for _, locator := range locators {
		if locator.Kind == LocatorGitRemote {
			if repository, ok := GitHubRepositoryFromRemote(locator.NormalizedValue); ok {
				repositories = append(repositories, repository)
			}
		}
	}
	return repositories, nil
}

func workflowTimingPullRequest(artifact string, repositories []string) bool {
	parsed, err := url.Parse(artifact)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return false
	}
	number, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != parts[3] {
		return false
	}
	for _, repository := range repositories {
		if strings.EqualFold(parts[0]+"/"+parts[1], repository) {
			return true
		}
	}
	return false
}

func deriveWorkflowCompletionTiming(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, artifact, state string, beforeSeq int64) (*workflowCompletionTiming, error) {
	timing, err := readWorkflowTiming(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	result := &workflowCompletionTiming{RootWorkID: timing.RootWorkID, Submission: workflowTimingSubmission{Kind: "unavailable", Reason: "legacy_definition"}}
	if timing.DefineEventID != "" {
		var prior bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM domain_events WHERE event_id=? AND (?=0 OR seq<?))`, timing.DefineEventID, beforeSeq, beforeSeq).Scan(&prior); err != nil {
			return nil, err
		}
		if prior {
			result.DefineStartEventID = &timing.DefineEventID
		}
	}
	if !workflowCapabilities(definition).has(WorkflowCapabilitySizedTiming) {
		return result, nil
	}
	if (artifact == "") != (state == "") || state != "" && state != "asserted" {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "completion submission requires delivery_artifact and delivery_state=asserted together", false, "supply both delivery fields or omit both")
	}
	_, sized, err := readEffectiveWorkSize(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if !sized {
		result.Submission = workflowTimingSubmission{Kind: "not_applicable", Reason: "unsized"}
		return result, nil
	}
	if definition.WorkKind == WorkKindResearch {
		result.Submission = workflowTimingSubmission{Kind: "not_applicable", Reason: "research_outcome"}
		return result, nil
	}
	repositories, err := workflowTimingRepositories(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if len(repositories) == 0 {
		result.Submission = workflowTimingSubmission{Kind: "unavailable", Reason: "unregistered_merge_route"}
		return result, nil
	}
	if state != "asserted" || !workflowTimingPullRequest(artifact, repositories) {
		return nil, newFailure(KindMissingEvidence, "workflow_action", "sized completion requires the delivering actor's merge-route submission assertion with a PR artifact for its registered Project", false, "supply delivery_artifact naming that Project's PR and delivery_state=asserted")
	}
	result.Submission = workflowTimingSubmission{Kind: "merge_route", Artifact: artifact}
	return result, nil
}

func workflowCompletionTimingForPayload(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, fields map[string]json.RawMessage) (*workflowCompletionTiming, error) {
	return deriveWorkflowCompletionTiming(ctx, q, workID, definition, workflowFieldStringDefault(fields, "delivery_artifact", ""), workflowFieldStringDefault(fields, "delivery_state", ""), 0)
}

func foldWorkflowCompletionTiming(ctx context.Context, tx *sql.Tx, event Event, definition WorkflowDefinition, timing *workflowCompletionTiming) error {
	if event.replaySourcePayloadVersion != 0 && event.replaySourcePayloadVersion < 3 {
		if timing != nil {
			return newFailure(KindInvalidPayload, "fold_event", "legacy completion cannot carry timing", false, "preserve the recorded completion payload version")
		}
		return nil
	}
	if timing == nil {
		if !workflowCapabilities(definition).has(WorkflowCapabilitySizedTiming) {
			return nil
		}
		return newFailure(KindInvalidPayload, "fold_event", "timed completion requires its native timing record", false, "complete through the native workflow action")
	}
	state := ""
	if timing.Submission.Kind == "merge_route" {
		state = "asserted"
	}
	want, err := deriveWorkflowCompletionTiming(ctx, tx, event.SubjectID, definition, timing.Submission.Artifact, state, event.Seq)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(timing, want) {
		return newFailure(KindInvalidPayload, "fold_event", "completion timing does not match the recorded size, root, start or registered merge route", false, "preserve the native completion timing")
	}
	if timing.Submission.Kind != "merge_route" {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_timing(work_id,queue_event_id,queue_entered_at,queue_artifact) VALUES(?,?,?,?)
		ON CONFLICT(work_id) DO UPDATE SET queue_event_id=excluded.queue_event_id,queue_entered_at=excluded.queue_entered_at,queue_artifact=excluded.queue_artifact
		WHERE workflow_timing.queue_event_id IS NULL`, event.SubjectID, event.EventID, event.OccurredAt.UTC().Format(time.RFC3339Nano), timing.Submission.Artifact)
	return err
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

// recordWorkflowDefineStartTx preserves the first qualifying agent action's
// provenance across resizing; delivery assertions cannot stamp submission.
func recordWorkflowDefineStartTx(ctx context.Context, tx *sql.Tx, event Event) error {
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
	return nil
}
