package store

import (
	"context"
	"database/sql"
)

// The one resume view (CON-1575 end state 1). A session resuming a work item
// reads the item's stored step plan (CON-1574 end state 7) with the derived
// next steps and the work version. Before the type is picked, the plan is nil
// and the read derives the remaining opening steps from the recorded intake
// and clarification prelude.

// WorkflowResumeRead is the single resume read's bounded answer. Steps are the
// stored plan exactly as written, attempts included; NextSteps holds the ids of
// the derived frontier steps in plan order; OpeningSteps replaces Steps before
// the type is picked. A terminal item still answers with its stored plan.
type WorkflowResumeRead struct {
	WorkID       string               `json:"work_id"`
	Version      int64                `json:"version"`
	State        string               `json:"state"`
	Type         WorkflowStepPlanType `json:"type,omitempty"`
	Steps        []WorkflowPlanStep   `json:"steps,omitempty"`
	NextSteps    []string             `json:"next_steps,omitempty"`
	OpeningSteps []string             `json:"opening_steps,omitempty"`
}

// ReadWorkflowResume answers the one resume read for one work item. The read
// derives; it never writes. An item whose execution state is not retained
// answers the typed published-absence failure, like every per-item read.
func ReadWorkflowResume(ctx context.Context, s *Store, workID string) (WorkflowResumeRead, error) {
	var out WorkflowResumeRead
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C19.Resume", "store is not open", false, "open the authority database")
	}
	if len(workID) < 2 || len(workID) > 128 {
		return out, newFailure(KindInvalidPayload, "C19.Resume", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Resume", "cannot open a consistent resume read", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "C19.Resume"); err != nil {
		return out, err
	}
	var typ, raw sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT w.version,i.instance_state,i.workflow_type,i.step_plan FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&out.Version, &out.State, &typ, &raw)
	if err == sql.ErrNoRows {
		exists, existsErr := workExistsCore(ctx, tx, workID)
		if existsErr != nil {
			return out, existsErr
		}
		if !exists {
			return out, newFailure(KindProjectionNotFound, "C19.Resume", "work item is not recorded", false, "reread_entities")
		}
		return out, newFailure(KindProjectionNotFound, "C19.Resume", "work item carries no workflow instance", false, "reread_entities")
	}
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Resume", "cannot read the workflow plan", true, "retry once the database is readable", err)
	}
	out.WorkID = workID
	if !typ.Valid && !raw.Valid {
		// Intake: no type has been picked, so the plan is nil and the read
		// answers the remaining opening steps from the recorded prelude.
		prelude, preludeErr := readResumePreludeTx(ctx, tx, workID)
		if preludeErr != nil {
			return out, preludeErr
		}
		out.OpeningSteps = workflowOpeningSteps(prelude)
		return out, nil
	}
	plan, err := decodeStoredPlanTx(typ, raw)
	if err != nil {
		return out, err
	}
	out.Type = plan.Type
	out.Steps = plan.Steps
	frontier := WorkflowStepPlanFrontier(*plan)
	out.NextSteps = make([]string, 0, len(frontier))
	for _, step := range frontier {
		out.NextSteps = append(out.NextSteps, step.ID)
	}
	return out, nil
}

// readResumePreludeTx reads the intake/clarification prelude for the resume
// read. Unlike readWorkflowPreludeTx, which guards writes, a terminal item
// still answers: the read reports state, it never refuses on lifecycle.
func readResumePreludeTx(ctx context.Context, tx *sql.Tx, workID string) (*workflowPreludeState, error) {
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT prelude_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&raw); err != nil && err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "C19.Resume", "cannot read the workflow prelude", true, "retry once the database is readable", err)
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

// workflowOpeningSteps derives the remaining opening steps from the recorded
// prelude: intake first, clarify only while intake found open questions and no
// clarification is recorded, then picking the type. The outcomes that end or
// replace the item without a type pick leave no opening step (CON-1574 end
// state 9).
func workflowOpeningSteps(prelude *workflowPreludeState) []string {
	if prelude == nil {
		return []string{"intake"}
	}
	switch prelude.Intake.Outcome {
	case WorkflowIntakeProceed:
		return []string{"pick_type"}
	case WorkflowIntakeClarify:
		if prelude.Clarify == nil {
			return []string{"clarify", "pick_type"}
		}
		return []string{"pick_type"}
	default:
		return []string{}
	}
}
