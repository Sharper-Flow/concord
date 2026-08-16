package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// WorkflowReadRequest bounds the existing workflow trace/read projection. It
// is read-only: no condition is resolved and no warning is acknowledged while
// constructing the response.
type WorkflowReadRequest struct {
	WorkID string
	Limit  int
	// Now pins the read-time clock for await-health derivation; zero means
	// wall clock.
	Now time.Time
}

type WorkflowReadDefinition struct {
	Ref     string `json:"ref"`
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
}

type WorkflowReadContract struct {
	Version          int64    `json:"version"`
	Premise          string   `json:"premise"`
	OutcomeKind      string   `json:"outcome_kind"`
	OutcomePayload   string   `json:"outcome_payload"`
	RequiredEvidence []string `json:"required_evidence"`
	RouteConventions []string `json:"route_conventions"`
	SpecMandate      []string `json:"spec_mandate"`
	LawModifies      []string `json:"law_modifies"`
	RigorClass       string   `json:"rigor_class"`
}

type WorkflowReadCondition struct {
	ID                  string `json:"id"`
	AwaitType           string `json:"await_type"`
	AwaitRef            string `json:"await_ref"`
	ResolutionAuthority string `json:"resolution_authority"`
	State               string `json:"state"`
	// ExpectedWithinSeconds is the declared wait bound (0 = none declared).
	// Overdue/Health are derived at read time against that bound (issue #87):
	// an await beyond its bound reads overdue — unverified, never resolved.
	ExpectedWithinSeconds int64  `json:"expected_within_seconds"`
	AgeSeconds            int64  `json:"age_seconds"`
	Overdue               bool   `json:"overdue"`
	Health                string `json:"health"`
}

type WorkflowReadNotice struct {
	NoticeID              string  `json:"notice_id"`
	SourceWorkID          string  `json:"source_work_id"`
	SourceContractVersion int64   `json:"source_contract_version"`
	EntityKind            string  `json:"entity_kind"`
	EntityRef             string  `json:"entity_ref"`
	TargetWorkID          string  `json:"target_work_id"`
	EdgeOwnerWorkID       string  `json:"edge_owner_work_id"`
	EdgeID                string  `json:"edge_id"`
	OldHash               *string `json:"old_hash,omitempty"`
	NewHash               *string `json:"new_hash,omitempty"`
	Severity              string  `json:"severity"`
}

// WorkflowReadProjection is the bounded observation exposed through existing
// read surfaces. All values are derived from event-folded projections and the
// pinned definition; it is intentionally not an authority for mutation.
type WorkflowReadProjection struct {
	WorkID               string                    `json:"work_id"`
	State                string                    `json:"state"`
	CurrentStep          string                    `json:"current_step"`
	Definition           WorkflowReadDefinition    `json:"definition"`
	Contract             *WorkflowReadContract     `json:"contract,omitempty"`
	OperatorQuestion     *WorkflowOperatorQuestion `json:"operator_question,omitempty"`
	CandidateIDs         []string                  `json:"candidate_ids"`
	Conditions           []WorkflowReadCondition   `json:"conditions"`
	UnresolvedConditions []string                  `json:"unresolved_conditions"`
	// OverdueAwaits lists condition ids whose wait exceeded the declared
	// bound, derived at read time — the waiting/never-completable split.
	OverdueAwaits        []string                `json:"overdue_awaits"`
	AwaitHealth          []WorkflowReadCondition `json:"await_health"`
	UnreadableConditions []string                `json:"unreadable_conditions"`
	Ready                bool                    `json:"ready"`
	BlockingConditions   []string                `json:"blocking_conditions"`
	ImpactNotices        []WorkflowReadNotice    `json:"impact_notices"`
	CompletionWarnings   []string                `json:"completion_warnings"`
}

// ReadWorkflowProjection returns one bounded, point-in-time workflow
// observation. The database snapshot is never modified by this function.
func ReadWorkflowProjection(ctx context.Context, s *Store, request WorkflowReadRequest) (WorkflowReadProjection, error) {
	var out WorkflowReadProjection
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "workflow_read", "store is not open", false, "open the authority database")
	}
	if len(request.WorkID) < 2 || len(request.WorkID) > 128 {
		return out, newFailure(KindInvalidOperation, "workflow_read", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	if request.Limit <= 0 {
		request.Limit = 32
	}
	if request.Limit > 100 {
		return out, newFailure(KindInvalidOperation, "workflow_read", "workflow read limit exceeds the bounded maximum", false, "reduce the read limit")
	}
	var activeActor sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT instance_state,current_step,definition_ref,definition_version,definition_digest,execution_actor_ref FROM workflow_instances WHERE work_id=?`, request.WorkID).Scan(&out.State, &out.CurrentStep, &out.Definition.Ref, &out.Definition.Version, &out.Definition.Digest, &activeActor); err != nil {
		if err == sql.ErrNoRows {
			return out, newFailure(KindProjectionNotFound, "workflow_read", "workflow instance is not recorded", false, "reread_entities")
		}
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow instance", true, "retry once the database is readable", err)
	}
	out.WorkID = request.WorkID
	out.CandidateIDs = []string{}
	out.Conditions = []WorkflowReadCondition{}
	out.UnresolvedConditions = []string{}
	out.OverdueAwaits = []string{}
	out.AwaitHealth = []WorkflowReadCondition{}
	out.UnreadableConditions = []string{}
	out.BlockingConditions = []string{}
	out.ImpactNotices = []WorkflowReadNotice{}
	out.CompletionWarnings = []string{}

	var contract WorkflowReadContract
	var required, routes, mandates, modifies string
	err := s.db.QueryRowContext(ctx, `SELECT contract_version,premise,outcome_kind,outcome_payload,required_evidence,route_conventions,spec_mandate,law_modifies,rigor_class FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, request.WorkID).Scan(&contract.Version, &contract.Premise, &contract.OutcomeKind, &contract.OutcomePayload, &required, &routes, &mandates, &modifies, &contract.RigorClass)
	if err == nil {
		if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
			return out, newFailure(KindInvariantViolation, "workflow_read", "workflow contract projection contains malformed arrays", false, "rebuild projections from the event log")
		}
		out.Contract = &contract
	} else if err != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow contract", true, "retry once the database is readable", err)
	}
	if out.Contract != nil {
		out.OperatorQuestion, err = ReadWorkflowOperatorQuestion(ctx, s, request.WorkID)
		if err != nil {
			return out, err
		}
	}

	rows, err := s.db.QueryContext(ctx, `SELECT candidate_ref FROM workflow_candidate_sets WHERE work_id=? AND contract_version=COALESCE((SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1),0) ORDER BY candidate_ref LIMIT ?`, request.WorkID, request.WorkID, request.Limit)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow candidates", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow candidate", true, "retry once the database is readable", err)
		}
		out.CandidateIDs = append(out.CandidateIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow candidates", true, "retry once the database is readable", err)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `SELECT condition_id,await_type,await_ref,resolution_authority,condition_state,coalesce(expected_within_seconds,0),recorded_at FROM workflow_external_conditions WHERE work_id=? ORDER BY condition_id LIMIT ?`, request.WorkID, request.Limit)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow conditions", true, "retry once the database is readable", err)
	}
	health := []WorkflowReadCondition{}
	for rows.Next() {
		var condition WorkflowReadCondition
		var waitingSince string
		if err := rows.Scan(&condition.ID, &condition.AwaitType, &condition.AwaitRef, &condition.ResolutionAuthority, &condition.State, &condition.ExpectedWithinSeconds, &waitingSince); err != nil {
			rows.Close()
			return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow condition", true, "retry once the database is readable", err)
		}
		out.Conditions = append(out.Conditions, condition)
		if condition.State == "open" {
			out.UnresolvedConditions = append(out.UnresolvedConditions, condition.ID)
			// Read-time health derivation (issue #87): compare the wait's
			// age to the declared bound. No state change, no timer.
			if recorded, parseErr := time.Parse(time.RFC3339Nano, waitingSince); parseErr == nil {
				clock := request.Now
				if clock.IsZero() {
					clock = time.Now().UTC()
				}
				condition.AgeSeconds = int64(clock.Sub(recorded).Seconds())
				if condition.AgeSeconds < 0 {
					condition.AgeSeconds = 0
				}
			}
			condition.Health = "waiting"
			if condition.ExpectedWithinSeconds > 0 && condition.AgeSeconds > condition.ExpectedWithinSeconds {
				condition.Overdue = true
				condition.Health = "overdue"
				out.OverdueAwaits = append(out.OverdueAwaits, condition.ID)
			}
			health = append(health, condition)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow conditions", true, "retry once the database is readable", err)
	}
	rows.Close()
	out.AwaitHealth = health
	ready, err := DeriveWorkflowReady(ctx, s, request.WorkID)
	if err != nil {
		return out, err
	}
	out.Ready = ready.Ready
	out.BlockingConditions = append(out.BlockingConditions, ready.BlockingConditions...)
	out.UnreadableConditions = append(out.UnreadableConditions, ready.UnknownConditions...)

	rows, err = s.db.QueryContext(ctx, `SELECT notice_id,source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,edge_owner_work_id,edge_id,old_hash,new_hash,severity FROM workflow_impact_notices WHERE target_work_id=? OR source_work_id=? ORDER BY recorded_at,notice_id LIMIT ?`, request.WorkID, request.WorkID, request.Limit)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow impact notices", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var notice WorkflowReadNotice
		if err := rows.Scan(&notice.NoticeID, &notice.SourceWorkID, &notice.SourceContractVersion, &notice.EntityKind, &notice.EntityRef, &notice.TargetWorkID, &notice.EdgeOwnerWorkID, &notice.EdgeID, &notice.OldHash, &notice.NewHash, &notice.Severity); err != nil {
			rows.Close()
			return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow impact notice", true, "retry once the database is readable", err)
		}
		out.ImpactNotices = append(out.ImpactNotices, notice)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return out, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow impact notices", true, "retry once the database is readable", err)
	}
	rows.Close()

	var warnings string
	if err := s.db.QueryRowContext(ctx, `SELECT json_extract(payload,'$.warnings') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, request.WorkID, WorkflowCompleted).Scan(&warnings); err == nil && warnings != "" && warnings != "null" {
		if json.Unmarshal([]byte(warnings), &out.CompletionWarnings) != nil {
			return out, newFailure(KindInvariantViolation, "workflow_read", "completion warnings are malformed", false, "rebuild projections from the event log")
		}
	}
	persisted, warningErr := readWorkflowStalenessWarnings(ctx, s.db, request.WorkID)
	if warningErr != nil {
		return out, warningErr
	}
	for _, warning := range persisted {
		found := false
		for _, existing := range out.CompletionWarnings {
			if existing == warning {
				found = true
				break
			}
		}
		if !found {
			out.CompletionWarnings = append(out.CompletionWarnings, warning)
		}
	}
	return out, nil
}

// WorkflowResumeBoundary is the production read of the last committed
// checkpoint. The boundary is event-log-derived: the checkpoint projection is
// joined back to its persisted checkpoint event before it is returned.
type WorkflowResumeBoundary struct {
	StepID       string `json:"step_id"`
	CheckpointID string `json:"checkpoint_id"`
	Source       string `json:"source"`
}

func ResumeWorkflow(ctx context.Context, s *Store, workID string) (WorkflowResumeBoundary, error) {
	var boundary WorkflowResumeBoundary
	var resumeCursor, eventID string
	if s == nil || s.db == nil {
		return boundary, newFailure(KindUnavailable, "workflow_resume", "store is not open", false, "open the authority database")
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT c.resume_cursor,c.checkpoint_id,e.event_id
		FROM workflow_checkpoints c
		JOIN domain_events e ON e.subject_type=? AND e.subject_id=c.work_id AND e.kind=? AND json_extract(e.payload,'$.checkpoint_id')=c.checkpoint_id
		WHERE c.work_id=? AND e.kind=?
		ORDER BY e.seq DESC LIMIT 1`, SubjectWorkItem, WorkflowActionCheckpointed, workID, WorkflowActionCheckpointed).Scan(&resumeCursor, &boundary.CheckpointID, &eventID)
	if err == sql.ErrNoRows {
		return boundary, newFailure(KindProjectionNotFound, "workflow_resume", "workflow has no committed checkpoint", false, "reread_entities")
	}
	if err != nil {
		return boundary, wrapFailure(KindUnavailable, "workflow_resume", "cannot read committed checkpoint", true, "retry once the database is readable", err)
	}
	if eventID == "" || resumeCursor == "" {
		return boundary, newFailure(KindInvariantViolation, "workflow_resume", "checkpoint event has no resume boundary", false, "rebuild projections from the event log")
	}
	boundary.StepID = resumeCursor
	boundary.Source = "event_log"
	return boundary, nil
}

// WorkflowReplayEvidence exposes the registered upcaster result observed by a
// completed rebuild. It never invents a successful replay: the stored event,
// registered event metadata, and rebuilt work projection must all agree.
type WorkflowReplayEvidence struct {
	EventID              string `json:"event_id"`
	Kind                 string `json:"kind"`
	StoredPayloadVersion int    `json:"stored_payload_version"`
	ReplayPayloadVersion int    `json:"replay_payload_version"`
	ProjectionVersion    int64  `json:"projection_version"`
}

func ReadWorkflowReplayEvidence(ctx context.Context, s *Store, workID, kind string) (WorkflowReplayEvidence, error) {
	var evidence WorkflowReplayEvidence
	var event Event
	var occurredAt string
	if s == nil || s.db == nil {
		return evidence, newFailure(KindUnavailable, "workflow_replay", "store is not open", false, "open the authority database")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? ORDER BY seq LIMIT 1`, SubjectWorkItem, workID, kind).Scan(&event.EventID, &event.Kind, &event.SubjectType, &event.SubjectID, &event.Actor, &occurredAt, &event.PayloadVersion, &event.Payload); err != nil {
		if err == sql.ErrNoRows {
			return evidence, newFailure(KindProjectionNotFound, "workflow_replay", "replay event is missing", false, "reread_entities")
		}
		return evidence, wrapFailure(KindUnavailable, "workflow_replay", "cannot read replay event", true, "retry once the database is readable", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
	if err != nil {
		return evidence, newFailure(KindInvalidEvent, "workflow_replay", "replay event timestamp is invalid", false, "repair the event log")
	}
	event.OccurredAt = parsed
	replayed, err := upcastEvent(event)
	if err != nil {
		return evidence, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&evidence.ProjectionVersion); err != nil {
		return evidence, wrapFailure(KindUnavailable, "workflow_replay", "cannot read rebuilt workflow projection", true, "retry once the database is readable", err)
	}
	evidence.EventID = event.EventID
	evidence.Kind = event.Kind
	evidence.StoredPayloadVersion = event.PayloadVersion
	evidence.ReplayPayloadVersion = replayed.PayloadVersion
	return evidence, nil
}

// ReadWorkflow is a concise alias used by read adapters.
func ReadWorkflow(ctx context.Context, s *Store, workID string) (WorkflowReadProjection, error) {
	return ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
}

// readWorkflowSummaryTx is the transaction-local adapter used by the existing
// PM1.Q7 history surface. It intentionally mirrors only bounded read fields;
// callers still receive the complete store projection through ReadWorkflow.
func readWorkflowSummaryTx(ctx context.Context, tx *sql.Tx, workID string) (*WorkflowReadProjection, error) {
	var out WorkflowReadProjection
	if tx == nil {
		return nil, newFailure(KindUnavailable, "workflow_read", "read transaction is not open", false, "open the authority database")
	}
	if err := tx.QueryRowContext(ctx, `SELECT instance_state,current_step,definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&out.State, &out.CurrentStep, &out.Definition.Ref, &out.Definition.Version, &out.Definition.Digest); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow history summary", true, "retry once the database is readable", err)
	}
	out.WorkID = workID
	out.Ready = true
	out.CandidateIDs = []string{}
	out.Conditions = []WorkflowReadCondition{}
	out.UnresolvedConditions = []string{}
	out.OverdueAwaits = []string{}
	out.AwaitHealth = []WorkflowReadCondition{}
	out.UnreadableConditions = []string{}
	out.BlockingConditions = []string{}
	out.ImpactNotices = []WorkflowReadNotice{}
	out.CompletionWarnings = []string{}
	var contract WorkflowReadContract
	var required, routes, mandates string
	var modifies string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,premise,outcome_kind,outcome_payload,required_evidence,route_conventions,spec_mandate,law_modifies FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, workID).Scan(&contract.Version, &contract.Premise, &contract.OutcomeKind, &contract.OutcomePayload, &required, &routes, &mandates, &modifies); err == nil {
		if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
			return nil, newFailure(KindInvariantViolation, "workflow_read", "workflow history contract arrays are malformed", false, "rebuild projections from the event log")
		}
		out.Contract = &contract
		var workVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&workVersion); err != nil {
			return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow history version", true, "retry once the database is readable", err)
		}
		out.OperatorQuestion, err = workflowOperatorQuestionTx(workID, out.CurrentStep, workVersion, out.Definition, contract)
		if err != nil {
			return nil, err
		}
	} else if err != sql.ErrNoRows {
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow history contract", true, "retry once the database is readable", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT condition_id,await_type,await_ref,resolution_authority,condition_state FROM workflow_external_conditions WHERE work_id=? ORDER BY condition_id LIMIT 32`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot read workflow history conditions", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var condition WorkflowReadCondition
		if err := rows.Scan(&condition.ID, &condition.AwaitType, &condition.AwaitRef, &condition.ResolutionAuthority, &condition.State); err != nil {
			rows.Close()
			return nil, wrapFailure(KindUnavailable, "workflow_read", "cannot scan workflow history condition", true, "retry once the database is readable", err)
		}
		out.Conditions = append(out.Conditions, condition)
		if condition.State != "open" {
			continue
		}
		out.UnresolvedConditions = append(out.UnresolvedConditions, condition.ID)
		out.Ready = false
		var owner string
		if strings.HasPrefix(condition.ResolutionAuthority, "durable_operation:") && tx.QueryRowContext(ctx, `SELECT work_id FROM durable_operations WHERE op_id=? ORDER BY attempt_epoch DESC LIMIT 1`, strings.TrimPrefix(condition.ResolutionAuthority, "durable_operation:")).Scan(&owner) == nil && owner == workID {
			out.BlockingConditions = append(out.BlockingConditions, condition.ID)
		} else {
			out.UnreadableConditions = append(out.UnreadableConditions, condition.ID)
		}
	}
	rows.Close()
	if out.Conditions == nil {
		out.Ready = true
	}
	return &out, nil
}
