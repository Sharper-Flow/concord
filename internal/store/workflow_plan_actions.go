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
	ChecksFound        []string              `json:"checks_found,omitempty"`
	Outcome            WorkflowIntakeOutcome `json:"outcome"`
	OpenQuestionsFound bool                  `json:"open_questions_found"`
	Note               string                `json:"note,omitempty"`
}

type workflowClarifyRecord struct {
	ResolvedQuestions []string `json:"resolved_questions"`
	Note              string   `json:"note"`
}

type workflowPreludeState struct {
	Intake  workflowIntakeRecord   `json:"intake"`
	Clarify *workflowClarifyRecord `json:"clarify,omitempty"`
}

type workflowIntakeRecordedPayload struct {
	WorkflowVersionFields
	workflowIntakeRecord
}

type workflowClarifyRecordedPayload struct {
	WorkflowVersionFields
	workflowClarifyRecord
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
	if state == "completed" || state == "cancelled" || state == "superseded" || lifecycle == "completed" || lifecycle == "cancelled" || lifecycle == "superseded" {
		return nil, newFailure(KindInvalidOperation, "fold_event", "terminal workflow cannot change", false, "reread the work item")
	}
	if !raw.Valid {
		return nil, nil
	}
	var prelude workflowPreludeState
	if err := json.Unmarshal([]byte(raw.String), &prelude); err != nil || prelude.Intake.Outcome == "" {
		return nil, newFailure(KindInvariantViolation, "fold_event", "stored workflow prelude is invalid", false, "restore a verified backup")
	}
	return &prelude, nil
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
	if !slices.Contains([]WorkflowIntakeOutcome{WorkflowIntakeProceed, WorkflowIntakeClarify, WorkflowIntakeAlreadyHandled, WorkflowIntakeDecline, WorkflowIntakeSplitOrMerge}, p.Outcome) || len(p.ChecksFound) > 14 || (p.Note != "" && !workflowString(p.Note, 4096)) || (p.Outcome == WorkflowIntakeProceed && p.OpenQuestionsFound) || (p.Outcome == WorkflowIntakeClarify && !p.OpenQuestionsFound) {
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
	if !workflowList(p.ResolvedQuestions, 16, 1) || !workflowString(p.Note, 4096) {
		return newFailure(KindInvalidPayload, "fold_event", "clarification requires question resolutions and a note", false, "supply question resolutions and a note")
	}
	if err := advanceWorkflowVersion(ctx, tx, event, p.WorkflowVersionFields); err != nil {
		return err
	}
	prelude, err := readWorkflowPreludeTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if prelude == nil || prelude.Intake.Outcome != WorkflowIntakeClarify || !prelude.Intake.OpenQuestionsFound || prelude.Clarify != nil {
		return newFailure(KindInvalidOperation, "fold_event", "clarification is not available", false, "reread the workflow prelude")
	}
	plan, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if plan != nil {
		return newFailure(KindInvalidOperation, "fold_event", "workflow already has a plan", false, "reread the workflow")
	}
	prelude.Clarify = &p.workflowClarifyRecord
	return writeWorkflowPreludeTx(ctx, tx, event.SubjectID, *prelude)
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
	prelude, err := readWorkflowPreludeTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if prelude == nil || !((prelude.Intake.Outcome == WorkflowIntakeProceed && !prelude.Intake.OpenQuestionsFound) || (prelude.Intake.Outcome == WorkflowIntakeClarify && prelude.Intake.OpenQuestionsFound && prelude.Clarify != nil)) {
		return newFailure(KindInvalidOperation, "fold_event", "workflow intake does not permit a type pick", false, "complete intake and clarification first")
	}
	old, err := readWorkflowPlanTx(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if old != nil {
		old.Type = p.Type
		for _, step := range template.Steps {
			if !stepIDExists(old.Steps, step.ID) {
				old.Steps = append(old.Steps, step)
			}
		}
		template = *old
	}
	return writeWorkflowPlanTx(ctx, tx, event.SubjectID, template)
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
	plan.Steps = append(plan.Steps, WorkflowPlanStep{ID: p.ID, After: p.After, Skill: p.Skill, Helper: p.Helper, Reviewed: p.Reviewed})
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
	for i := range plan.Steps {
		if plan.Steps[i].ID == p.StepID {
			plan.Steps[i].Attempts = append(plan.Steps[i].Attempts, WorkflowPlanAttempt{Note: p.Note, CompletedAt: event.OccurredAt.UTC().Format(time.RFC3339Nano), CompletionSeq: int64(event.Seq), Actor: p.Actor, Reviewer: p.Reviewer, Result: p.Result, Commit: p.Commit, Link: p.Link})
			return writeWorkflowPlanTx(ctx, tx, event.SubjectID, *plan)
		}
	}
	return newFailure(KindInvalidOperation, "fold_event", "workflow step does not exist", false, "reread the plan")
}
