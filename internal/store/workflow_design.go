package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

type workflowDesignContent struct {
	Approach    string                          `json:"approach"`
	Decisions   []workflowDesignDecisionPayload `json:"decisions"`
	TouchedRefs []string                        `json:"touched_refs"`
}

func validateWorkflowDesignContent(p workflowDesignContent) error {
	if len(p.Approach) < 2 || len(p.Approach) > 4096 || len(p.Decisions) < 1 || len(p.Decisions) > 16 || !workflowList(p.TouchedRefs, 64, 1) {
		return newFailure(KindInvalidPayload, "fold_event", "design record is incomplete or outside its bounds", false, "supply the bounded design approach, decisions, and touched references")
	}
	for _, decision := range p.Decisions {
		if !ValidReference(decision.ID) || len(decision.Question) < 1 || len(decision.Question) > 512 || len(decision.Choice) < 1 || len(decision.Choice) > 1024 || len(decision.Rationale) < 1 || len(decision.Rationale) > 1024 || len(decision.Rejected) > 8 {
			return newFailure(KindInvalidPayload, "fold_event", "design decision is incomplete or outside its bounds", false, "supply each bounded design decision with a non-empty choice")
		}
		for _, rejected := range decision.Rejected {
			if len(rejected) < 1 || len(rejected) > 1024 {
				return newFailure(KindInvalidPayload, "fold_event", "design decision rejected choice is outside its bounds", false, "supply bounded rejected choices")
			}
		}
	}
	return nil
}

func workflowDesignRecordedEvents(definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64) ([]Event, error) {
	if declared, err := workflowDeclaresDocument(definition, "record_design"); err != nil || !declared {
		return nil, err
	}
	var content workflowDesignContent
	if err := decodeWorkflowActionPayloadStrict(raw, &content); err != nil {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "design record is not a closed typed document", false, "supply only approach, decisions and touched_refs")
	}
	if err := validateWorkflowDesignContent(content); err != nil {
		return nil, err
	}
	return []Event{workflowTypedEvent(eventID, WorkflowDesignRecorded, request.WorkID, actor, request.Now, expected, map[string]any{
		"approach": content.Approach, "decisions": content.Decisions, "touched_refs": content.TouchedRefs,
	})}, nil
}

func appendWorkflowDesignCorrection(ctx context.Context, tx *sql.Tx, definition WorkflowDefinition, request WorkflowActionExecutionRequest, actor string, raw json.RawMessage, eventID string, expected int64, events []Event) ([]Event, error) {
	if raw == nil {
		return events, nil
	}
	var recorded int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_design_records WHERE work_id=?`, request.WorkID).Scan(&recorded); err != nil {
		return nil, workflowProjectionError(err, "cannot inspect design correction ownership")
	}
	if recorded == 0 {
		return nil, newFailure(KindInvalidPayload, "workflow_action", "design correction requires an existing typed design", false, "omit design_record for work without a typed design")
	}
	if err := validateWorkflowActionPayload(definition, "record_design", raw); err != nil {
		return nil, err
	}
	designEvents, err := workflowDesignRecordedEvents(definition, request, actor, raw, eventID+":design", expected+1)
	if err != nil {
		return nil, err
	}
	return append(events, designEvents...), nil
}

// The latest recorded design owns architecture content. Contract counters do
// not invalidate unchanged architecture; Review compares its content digest.
func readCurrentWorkflowDesign(ctx context.Context, q queryer, workID string) (*WorkflowDesignRecord, error) {
	var design WorkflowDesignRecord
	var decisions, touched string
	err := q.QueryRowContext(ctx, `SELECT work_version,approach,decisions,touched_refs,recorded_at FROM workflow_design_records WHERE work_id=? ORDER BY work_version DESC LIMIT 1`, workID).Scan(&design.WorkVersion, &design.Approach, &decisions, &touched, &design.RecordedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read workflow design")
	}
	if json.Unmarshal([]byte(decisions), &design.Decisions) != nil || json.Unmarshal([]byte(touched), &design.TouchedRefs) != nil {
		return nil, newFailure(KindInvariantViolation, "workflow_design", "design projection contains malformed arrays", false, "restore a verified backup into a new path with concord restore, then swap")
	}
	return &design, nil
}
