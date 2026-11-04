package store

import (
	"context"
	"database/sql"
	"strconv"
)

// WorkPin is the one point-in-time projection that a caller needs to prepare
// the next operation for a work item.
type WorkPin struct {
	WorkID                  string          `json:"work_id"`
	Title                   string          `json:"title"`
	LinearIssueKey          string          `json:"linear_issue_key"`
	ProjectID               string          `json:"project_id"`
	ProjectDisplayName      string          `json:"project_display_name"`
	Version                 int64           `json:"version"`
	Lifecycle               string          `json:"lifecycle"`
	CancelledInstanceCloses int64           `json:"cancelled_instance_closes"`
	WorkflowType            string          `json:"workflow_type"`
	Step                    string          `json:"step"`
	StepSkill               *string         `json:"step_skill,omitempty"`
	StepHelper              *string         `json:"step_helper,omitempty"`
	StepReviewed            *bool           `json:"step_reviewed,omitempty"`
	Watermark               string          `json:"watermark"`
	NextValidIntents        []WorkPinIntent `json:"next_valid_intents"`
	// DrivingSessions lists the distinct agent sessions that have driven this
	// workflow, with each session's most recent action and action time. It is
	// derived from workflow actors and actions, not session identity evidence.
	DrivingSessions []WorkPinDrivingSession `json:"driving_sessions"`
	// WorkContext carries the strict reader's exact view for dispatch.
	// WorkContextUnavailable distinguishes an over-bound view from absence.
	// The partial recovery pin does not evaluate either field.
	WorkContext            *WorkContextView        `json:"work_context,omitempty"`
	WorkContextUnavailable *WorkContextUnavailable `json:"work_context_unavailable,omitempty"`
}

type WorkPinDrivingSession struct {
	SessionRef   string `json:"session_ref"`
	LastActionID string `json:"last_action_id"`
	LastActedAt  string `json:"last_acted_at"`
}

type WorkPinIntent struct {
	Tool            string   `json:"tool"`
	Operation       string   `json:"operation"`
	ReasonCode      string   `json:"reason_code"`
	ActionID        string   `json:"action_id"`
	RequiredFields  []string `json:"required_fields"`
	ExpectedVersion int64    `json:"expected_version"`
}

// ReadWorkPin opens one read transaction and derives a complete pin.
func ReadWorkPin(ctx context.Context, s *Store, workID string) (WorkPin, error) {
	var pin WorkPin
	if s == nil || s.db == nil {
		return pin, newFailure(KindUnavailable, "work_pin", "store is not open", false, "open the authority database")
	}
	// The pin is advisory: when the verification is unavailable the read
	// proceeds without a proof, and the folds report law currency and the
	// confirmation unverified instead of failing the pin.
	if verified, err := s.PrepareWorkLawProof(ctx, workID); err == nil {
		ctx = verified
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return pin, wrapFailure(KindUnavailable, "work_pin", "cannot open a consistent work pin snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	return ReadWorkPinTx(ctx, tx, workID)
}

// ReadMutationWorkPinTx derives a mutation pin in the caller's transaction.
func ReadMutationWorkPinTx(ctx context.Context, transaction *Transaction, workID string, ownEventIDs []string) (WorkPin, bool, error) {
	tx, err := transactionSQL(transaction, "work_pin")
	if err != nil {
		return WorkPin{}, false, err
	}
	return readMutationWorkPinTx(ctx, tx, workID, ownEventIDs)
}

func readMutationWorkPinTx(ctx context.Context, tx *sql.Tx, workID string, ownEventIDs []string) (WorkPin, bool, error) {
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "work_pin"); err != nil {
		return WorkPin{}, false, err
	}
	identity := WorkPin{WorkID: workID}
	err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&identity.Version)
	var instance bool
	switch err {
	case sql.ErrNoRows:
		removed := false
		for _, eventID := range ownEventIDs {
			var expected int64
			err := tx.QueryRowContext(ctx, `SELECT expected_version FROM work_removal_operations WHERE work_id=? AND event_id=? AND state='committed'`, workID, eventID).Scan(&expected)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return WorkPin{}, false, err
			}
			identity.Version = expected + 1
			removed = true
			break
		}
		if !removed {
			return WorkPin{}, false, newFailure(KindProjectionNotFound, "work_pin", "work item is not recorded", false, "reread_entities")
		}
	case nil:
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_instances WHERE work_id=?)`, workID).Scan(&instance); err != nil {
			return WorkPin{}, false, err
		}
	default:
		return WorkPin{}, false, err
	}
	if !instance {
		return identity, false, nil
	}
	pin, err := ReadWorkPinTx(ctx, tx, workID)
	return pin, true, err
}

// ReadWorkPinTx derives a pin from the transaction supplied by its caller.
func ReadWorkPinTx(ctx context.Context, tx *sql.Tx, workID string) (WorkPin, error) {
	var pin WorkPin
	if tx == nil {
		return pin, newFailure(KindUnavailable, "work_pin", "transaction is not open", false, "supply an active read transaction")
	}
	if len(workID) < 2 || len(workID) > 128 {
		return pin, newFailure(KindInvalidOperation, "work_pin", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	if err := workPinReadIdentityTx(ctx, tx, workID, &pin); err != nil {
		return pin, err
	}
	_, err := workPinReadPlanTx(ctx, tx, workID, &pin)
	if err != nil {
		return pin, err
	}
	// The pin reads; it does not adjudicate. A work item whose projection
	// carries duplicate active contracts must stay readable, because the
	// operator reaches the recovery that retires them through this pin. A
	// strict selection here would make the ambiguity permanent.
	//
	// Every enrichment below this point resolves the one approved contract,
	// and an ambiguous projection has none. The pin therefore reports where
	// the work item stands and the one route that repairs it, and stops.
	activeContractVersions, contractErr := activeWorkflowContractVersions(ctx, tx, workID)
	if contractErr != nil {
		return pin, contractErr
	}
	if len(activeContractVersions) > 1 {
		return pin, nil
	}
	if err := workPinReadWorkContextTx(ctx, tx, workID, &pin); err != nil {
		return pin, err
	}
	return pin, nil
}

// workPinReadWorkContextTx uses the caller's transaction, never the pooled connection.
func workPinReadWorkContextTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) error {
	view, unavailable, err := readWorkContextProjection(ctx, tx, workID)
	pin.WorkContext, pin.WorkContextUnavailable = view, unavailable
	return err
}

// workPinReadIdentityTx fills the pin's item identity: version, lifecycle,
// title, Linear key, primary project, the closed-instance count, the
// watermark, and the driving sessions. The watermark belongs on every pin
// this read returns with a nil error, so it is assigned before any phase
// that can return partial data.
func workPinReadIdentityTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) error {
	if err := publishedWorkAbsenceQ(ctx, tx, workID, "work_pin"); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT w.version,w.lifecycle,w.title,COALESCE(l.human_key,''),COALESCE(p.id,''),COALESCE(p.display_name,'') FROM work_items w LEFT JOIN linear_issue_links l ON l.work_id=w.id LEFT JOIN work_projects wp ON wp.work_id=w.id AND wp.role='primary' LEFT JOIN projects p ON p.id=wp.project_id WHERE w.id=?`, workID).Scan(&pin.Version, &pin.Lifecycle, &pin.Title, &pin.LinearIssueKey, &pin.ProjectID, &pin.ProjectDisplayName); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindProjectionNotFound, "work_pin", "work item is not recorded", false, "reread_entities")
		}
		return wrapFailure(KindUnavailable, "work_pin", "cannot read work item", true, "retry once the database is readable", err)
	}
	pin.WorkID = workID
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_instances wi INDEXED BY workflow_instances_state JOIN work_items w ON w.id=wi.work_id JOIN work_projects wp ON wp.work_id=w.id WHERE wi.instance_state='cancelled' AND w.lifecycle='completed' AND wp.project_id=?`, pin.ProjectID).Scan(&pin.CancelledInstanceCloses); err != nil {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read cancelled-instance lifecycle closes", true, "retry once the database is readable", err)
	}
	watermark, err := readWorkEffectSeq(ctx, tx, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, "work_pin", "cannot read work watermark", true, "retry once the database is readable", err)
	}
	pin.Watermark = "seq:" + strconv.FormatInt(watermark, 10)
	sessions, err := workPinDrivingSessionsTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	pin.DrivingSessions = sessions
	return nil
}

// workPinReadPlanTx derives the next actions from the current stored plan.
func workPinReadPlanTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin) (string, error) {
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return "", newFailure(KindProjectionNotFound, "work_pin", "workflow instance is not recorded", false, "reread_entities")
		}
		return "", wrapFailure(KindUnavailable, "work_pin", "cannot read workflow instance", true, "retry once the database is readable", err)
	}
	plan, err := readWorkflowPlanTx(ctx, tx, workID)
	if err != nil {
		return "", err
	}
	pin.NextValidIntents = []WorkPinIntent{}
	if plan != nil {
		return workPinReadExistingPlanTx(ctx, tx, workID, pin, state, plan)
	}
	return workPinReadPreludeTx(ctx, tx, workID, pin, state)
}

func workPinReadExistingPlanTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, state string, plan *WorkflowStepPlan) (string, error) {
	pin.WorkflowType = string(plan.Type)
	frontier := WorkflowStepPlanFrontier(*plan)
	if len(frontier) > 0 {
		pin.Step = frontier[0].ID
		pin.StepSkill = frontier[0].Skill
		pin.StepHelper = frontier[0].Helper
		pin.StepReviewed = &frontier[0].Reviewed
	} else if WorkflowStepPlanComplete(*plan) && len(plan.Steps) > 0 {
		pin.Step = plan.Steps[len(plan.Steps)-1].ID
	}
	if isTerminalLifecycle(state) || isTerminalLifecycle(pin.Lifecycle) {
		return state, nil
	}
	ids := []string{"insert_step", "pick_type"}
	if WorkflowStepPlanComplete(*plan) {
		ids = append(ids, "complete")
	}
	for _, step := range frontier {
		actions := WorkflowStepPlanTemplateActions(plan.Type, step.ID)
		if actions == nil {
			actions = []string{"complete_step", "record_review", "record_delivery", "link_successor", "bind_evidence"}
		}
		for _, action := range actions {
			if action != "complete" {
				ids = append(ids, action)
			}
		}
	}
	return state, workPinAddCatalogIntents(pin, ids)
}

func workPinReadPreludeTx(ctx context.Context, tx *sql.Tx, workID string, pin *WorkPin, state string) (string, error) {
	pin.Step = "intake"
	if isTerminalLifecycle(state) || isTerminalLifecycle(pin.Lifecycle) {
		return state, nil
	}
	prelude, err := readWorkflowPreludeTx(ctx, tx, workID)
	if err != nil {
		return "", err
	}
	var ids []string
	switch {
	case prelude == nil:
		ids = []string{"record_intake"}
	case prelude.Intake.Outcome == WorkflowIntakeClarify && prelude.Intake.OpenQuestionsFound && prelude.Clarify == nil:
		pin.Step = "clarify"
		ids = []string{"record_clarify"}
	case prelude.Intake.Outcome == WorkflowIntakeProceed && !prelude.Intake.OpenQuestionsFound,
		prelude.Intake.Outcome == WorkflowIntakeClarify && prelude.Intake.OpenQuestionsFound && prelude.Clarify != nil:
		ids = []string{"pick_type"}
	}
	return state, workPinAddCatalogIntents(pin, ids)
}

func workPinAddCatalogIntents(pin *WorkPin, ids []string) error {
	for _, id := range ids {
		if workPinContainsAction(pin.NextValidIntents, id) {
			continue
		}
		action, err := catalogAction(id)
		if err != nil {
			return err
		}
		pin.NextValidIntents = append(pin.NextValidIntents, workPinIntentForAction(action, pin.Version, "declared_step_action"))
	}
	return nil
}

func workPinDrivingSessionsTx(ctx context.Context, tx *sql.Tx, workID string) ([]WorkPinDrivingSession, error) {
	rows, err := tx.QueryContext(ctx, workPinDrivingSessionsSQL, workPinDrivingSessionsArgs(workID)...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot read driving coordinator sessions", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	return scanWorkPinDrivingSessions(rows)
}

func workPinDrivingSessionsArgs(workID string) []any {
	return []any{workID, string(ActorAgent), workID, workID}
}

const workPinDrivingSessionsSQL = `
		SELECT a.session_ref,
		       COALESCE(f.last_action_id,'workflow.actor_recorded'),
		       COALESCE(f.last_acted_at,a.first_seen_at) AS last_acted_at
		FROM workflow_actors a
		LEFT JOIN workflow_coordinator_actions f ON f.work_id=? AND f.actor_ref=a.actor_ref
		WHERE a.actor_class=?
		  AND (
			 a.actor_ref=(SELECT execution_actor_ref FROM workflow_instances WHERE work_id=?)
			 OR a.actor_ref IN (
				SELECT actor_ref FROM workflow_coordinator_actions WHERE work_id=?
			 )
		  )
		ORDER BY last_acted_at DESC,a.session_ref
		LIMIT 17`

func scanWorkPinDrivingSessions(rows *sql.Rows) ([]WorkPinDrivingSession, error) {
	sessions := make([]WorkPinDrivingSession, 0, 4)
	for rows.Next() {
		var session WorkPinDrivingSession
		if err := rows.Scan(&session.SessionRef, &session.LastActionID, &session.LastActedAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_pin", "cannot scan driving coordinator session", true, "retry once the database is readable", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "work_pin", "cannot enumerate driving coordinator sessions", true, "retry once the database is readable", err)
	}
	if len(sessions) > 16 {
		return nil, newFailure(KindLimitExceeded, "work_pin", "driving coordinator sessions exceed the pin bound", false, "reduce the number of active coordinator sessions")
	}
	return sessions, nil
}

func workPinContainsAction(intents []WorkPinIntent, actionID string) bool {
	for _, intent := range intents {
		if intent.ActionID == actionID {
			return true
		}
	}
	return false
}

func workPinIntentForAction(action WorkflowActionDefinition, version int64, reason string) WorkPinIntent {
	payload := publicWorkflowActionPayload(action)
	fields := make([]string, 0, len(payload.Fields))
	for _, field := range payload.Fields {
		if field.Required {
			fields = append(fields, field.Name)
		}
	}
	return WorkPinIntent{Tool: "concord_work_transition", Operation: "workflow_action", ReasonCode: reason, ActionID: action.ID, RequiredFields: fields, ExpectedVersion: version}
}

func publicWorkflowActionPayload(action WorkflowActionDefinition) WorkflowPayloadDefinition {
	if action.PublicPayload != nil {
		return *action.PublicPayload
	}
	if policy, ok := builtinActionPolicies[action.ID]; ok && policy.PublicPayload != nil {
		return *policy.PublicPayload
	}
	return action.Payload
}
