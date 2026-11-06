package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"
)

const (
	WorkflowIntakeRecorded  = "workflow.intake_recorded"
	WorkflowClarifyRecorded = "workflow.clarify_recorded"
	WorkflowTypePicked      = "workflow.type_picked"
	WorkflowStepInserted    = "workflow.step_inserted"
	WorkflowStepCompleted   = "workflow.step_completed"
)

type workflowIntakeRecord struct {
	ChecksFound []string              `json:"checks_found,omitempty"`
	Outcome     WorkflowIntakeOutcome `json:"outcome"`
	Note        string                `json:"note,omitempty"`
}

type workflowPreludeState struct {
	Intake         workflowIntakeRecord `json:"intake"`
	TypePickReason string               `json:"type_pick_reason,omitempty"`
}

type workflowIntakeRecordedPayload struct {
	WorkflowVersionFields
	workflowIntakeRecord
}

type workflowClarifyRecordedPayload struct {
	WorkflowVersionFields
	Document json.RawMessage `json:"document"`
}

func readWorkflowPreludeTx(ctx context.Context, tx *sql.Tx, workID string) (*workflowPreludeState, error) {
	var raw sql.NullString
	var state, lifecycle string
	err := tx.QueryRowContext(ctx, `SELECT i.prelude_state,i.instance_state,w.lifecycle FROM workflow_instances i JOIN work_items w ON w.id=i.work_id WHERE i.work_id=?`, workID).Scan(&raw, &state, &lifecycle)
	if err == sql.ErrNoRows {
		return nil, newFailure(KindProjectionNotFound, "fold_event", "workflow instance does not exist", false, "reread the work item")
	}
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read workflow prelude")
	}
	if state == "completed" || state == "cancelled" || lifecycle == "completed" || lifecycle == "cancelled" {
		return nil, newFailure(KindInvalidOperation, "fold_event", "terminal workflow cannot change", false, "reread the work item")
	}
	if !raw.Valid {
		return nil, nil
	}
	var prelude workflowPreludeState
	if err := unmarshalWorkflowPrelude(raw.String, &prelude); err != nil {
		return nil, err
	}
	return &prelude, nil
}

// unmarshalWorkflowPrelude decodes one stored prelude projection. A prelude
// without an intake outcome is an invariant violation, not an empty prelude.
func unmarshalWorkflowPrelude(raw string, prelude *workflowPreludeState) error {
	if err := json.Unmarshal([]byte(raw), prelude); err != nil || prelude.Intake.Outcome == "" {
		return newFailure(KindInvariantViolation, "fold_event", "stored workflow prelude is invalid", false, "restore a verified backup")
	}
	return nil
}

// workflowPreludeAdmitsTypePick reports whether the recorded prelude admits
// pick_type after intake proceeds.
func workflowPreludeAdmitsTypePick(prelude *workflowPreludeState) bool {
	return prelude != nil && prelude.Intake.Outcome == WorkflowIntakeProceed
}

func writeWorkflowPreludeTx(ctx context.Context, tx *sql.Tx, workID string, prelude workflowPreludeState) error {
	raw, err := json.Marshal(prelude)
	if err != nil {
		return workflowProjectionError(err, "cannot encode workflow prelude")
	}
	_, err = tx.ExecContext(ctx, `UPDATE workflow_instances SET prelude_state=? WHERE work_id=?`, string(raw), workID)
	if err != nil {
		return workflowProjectionError(err, "cannot write workflow prelude")
	}
	return nil
}

func foldWorkflowIntakeRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowIntakeRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if !slices.Contains([]WorkflowIntakeOutcome{WorkflowIntakeProceed, WorkflowIntakeAlreadyHandled, WorkflowIntakeDecline, WorkflowIntakeSplitOrMerge}, p.Outcome) || len(p.ChecksFound) > 14 || (p.Note != "" && !workflowString(p.Note, 4096)) {
		return newFailure(KindInvalidPayload, "fold_event", "intake outcome or fields are invalid", false, "supply a consistent intake outcome")
	}
	seen := map[string]bool{}
	for _, check := range p.ChecksFound {
		if !slices.Contains(WorkflowIntakeChecks, check) || seen[check] {
			return newFailure(KindInvalidPayload, "fold_event", "intake checks are invalid", false, "supply distinct declared checks")
		}
		seen[check] = true
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	prelude, err := readWorkflowPreludeTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if prelude != nil {
		return newFailure(KindInvalidOperation, "fold_event", "intake is already recorded", false, "reread the workflow")
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan != nil {
		return newFailure(KindInvalidOperation, "fold_event", "workflow already has a plan", false, "reread the workflow")
	}
	return writeWorkflowPreludeTx(ctx, tx, event.SubjectID, workflowPreludeState{Intake: p.workflowIntakeRecord})
}

func foldWorkflowClarifyRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowClarifyRecordedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if _, err := validateWorkflowPlanActionPayload("record_clarify", p.Document); err != nil {
		return err
	}
	if err := admitWorkflowPlanDocumentTx(ctx, tx, event, p.WorkflowVersionFields, "clarify", "record_clarify"); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO workflow_clarify_records(work_id,work_version,payload,recorded_at,actor,event_ref) VALUES(?,?,?,?,?,?) ON CONFLICT(work_id) DO UPDATE SET work_version=excluded.work_version,payload=excluded.payload,recorded_at=excluded.recorded_at,actor=excluded.actor,event_ref=excluded.event_ref`, event.SubjectID, *p.ResultingVersion, string(p.Document), event.OccurredAt.UTC().Format(time.RFC3339Nano), event.Actor, event.EventID)
	return workflowProjectionError(err, "cannot write workflow clarification record")
}

type workflowTypePickedPayload struct {
	WorkflowVersionFields
	Type   WorkflowStepPlanType `json:"type"`
	Reason string               `json:"reason"`
}

type workflowStepInsertedPayload struct {
	WorkflowVersionFields
	ID       string   `json:"id"`
	After    []string `json:"after"`
	Skill    *string  `json:"skill,omitempty"`
	Helper   *string  `json:"helper,omitempty"`
	Reviewed bool     `json:"reviewed"`
	Reason   string   `json:"reason"`
}

type workflowStepCompletedPayload struct {
	WorkflowVersionFields
	StepID   string `json:"step_id"`
	Note     string `json:"note"`
	Actor    string `json:"actor,omitempty"`
	Reviewer string `json:"reviewer,omitempty"`
	Result   string `json:"result,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Link     string `json:"link,omitempty"`
}

func foldWorkflowTypePicked(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowTypePickedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if !workflowString(p.Reason, 2048) {
		return newFailure(KindInvalidPayload, "fold_event", "type pick requires a reason", false, "supply a reason")
	}
	template, err := WorkflowStepPlanTemplate(p.Type)
	if err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	prelude, err := readWorkflowPreludeTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if !workflowPreludeAdmitsTypePick(prelude) {
		return newFailure(KindInvalidOperation, "fold_event", "workflow intake does not permit a type pick", false, "complete intake first")
	}
	old, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if old != nil {
		if old.Type == p.Type {
			return newFailure(KindInvalidOperation, "fold_event", "workflow already has this type", false, "pick a different type to switch, or insert a step")
		}
		template = mergeWorkflowPlanTemplate(*old, template)
	}
	if err := writeWorkflowPlanTx(ctx, tx, event.SubjectID, template); err != nil {
		return err
	}
	prelude.TypePickReason = p.Reason
	return writeWorkflowPreludeTx(ctx, tx, event.SubjectID, *prelude)
}

func mergeWorkflowPlanTemplate(old, template WorkflowStepPlan) WorkflowStepPlan {
	old.Type = template.Type
	grandfathered := !stepIDExists(old.Steps, "clarify")
	clarify, _ := stepByID(template.Steps, "clarify")
	for _, step := range template.Steps {
		// A missing Clarify is the only marker for a grandfathered plan.
		if stepIDExists(old.Steps, step.ID) || (grandfathered && step.ID == "clarify") {
			continue
		}
		if grandfathered && slices.Contains(step.After, "clarify") {
			after := make([]string, 0, len(step.After)+len(clarify.After))
			for _, edge := range step.After {
				replacements := []string{edge}
				if edge == "clarify" {
					replacements = clarify.After
				}
				for _, replacement := range replacements {
					if !slices.Contains(after, replacement) {
						after = append(after, replacement)
					}
				}
			}
			step.After = after
		}
		old.Steps = append(old.Steps, step)
	}
	return old
}

func foldWorkflowStepInserted(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowStepInsertedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if !workflowString(p.Reason, 2048) || len(p.After) == 0 || len(p.After) > 16 {
		return newFailure(KindInvalidPayload, "fold_event", "inserted step requires a reason and after edges", false, "supply a reason and after edges")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	if _, err := readWorkflowPreludeTx(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan == nil {
		return newFailure(KindInvalidOperation, "fold_event", "workflow has no plan", false, "pick a workflow type")
	}
	seen := map[string]bool{}
	var reviewedAncestor func(string) bool
	reviewedAncestor = func(id string) bool {
		if seen[id] {
			return false
		}
		seen[id] = true
		step, found := stepByID(plan.Steps, id)
		if !found {
			return false
		}
		if step.Reviewed {
			return true
		}
		return slices.ContainsFunc(step.After, reviewedAncestor)
	}
	p.Reviewed = p.Reviewed || slices.ContainsFunc(p.After, reviewedAncestor)
	plan.Steps = append(plan.Steps, WorkflowPlanStep{ID: p.ID, After: p.After, Skill: p.Skill, Helper: p.Helper, Reviewed: p.Reviewed, Reason: p.Reason})
	return writeWorkflowPlanTx(ctx, tx, event.SubjectID, *plan)
}

func foldWorkflowStepCompleted(ctx context.Context, tx *sql.Tx, event Event) error {
	var p workflowStepCompletedPayload
	if err := decodeWorkflowPayload(event, &p); err != nil {
		return err
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	if err := beginWorkflowLifecycleTx(ctx, tx, event); err != nil {
		return err
	}
	if _, err := readWorkflowPreludeTx(ctx, tx, event.SubjectID); err != nil {
		return err
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan == nil {
		return newFailure(KindInvalidOperation, "fold_event", "workflow has no plan", false, "pick a workflow type")
	}
	if err := requireWorkflowPlanDocumentTx(ctx, tx, event.SubjectID, plan, p.StepID); err != nil {
		return err
	}
	for i := range plan.Steps {
		if plan.Steps[i].ID == p.StepID {
			if plan.Steps[i].Reviewed {
				if err := admitWorkflowPlanReview(p); err != nil {
					return err
				}
			}
			plan.Steps[i].Attempts = append(plan.Steps[i].Attempts, WorkflowPlanAttempt{Note: p.Note, CompletedAt: event.OccurredAt.UTC().Format(time.RFC3339Nano), CompletionSeq: event.Seq, Actor: p.Actor, Reviewer: p.Reviewer, Result: p.Result, Commit: p.Commit, Link: p.Link})
			return writeWorkflowPlanTx(ctx, tx, event.SubjectID, *plan)
		}
	}
	return newFailure(KindInvalidOperation, "fold_event", "workflow step does not exist", false, "reread the plan")
}

func admitWorkflowPlanReview(p workflowStepCompletedPayload) error {
	if p.Reviewer != "concord-review" {
		return newFailure(KindInvalidPayload, "workflow_review", "reviewed step requires concord-review as reviewer", false, "name concord-review")
	}
	if (p.Result != "pass" && p.Result != "fixed") || !workflowString(p.Commit, 2048) || !workflowString(p.Link, 2048) {
		return newFailure(KindInvalidPayload, "workflow_review", "reviewed step requires a result, commit and link", false, "supply pass or fixed and the reviewed commit and link")
	}
	return nil
}
