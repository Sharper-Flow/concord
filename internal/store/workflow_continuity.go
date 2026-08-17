package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strconv"
)

const continuityMaxOffset = 1000000

type ContextCheckpoint struct {
	CheckpointID     string   `json:"checkpoint_id"`
	WorkVersion      int64    `json:"work_version"`
	Sequence         int64    `json:"sequence"`
	StepID           string   `json:"step_id"`
	AttemptEpoch     int64    `json:"attempt_epoch"`
	ActiveUnit       string   `json:"active_unit"`
	Hypothesis       string   `json:"hypothesis"`
	Diagnosis        string   `json:"diagnosis"`
	Strategy         string   `json:"strategy"`
	TouchedRefs      []string `json:"touched_refs"`
	EvidenceRefs     []string `json:"evidence_refs"`
	PendingQuestions []string `json:"pending_questions"`
	PendingDecisions []string `json:"pending_decisions"`
}

type ContextBoundary struct {
	BoundaryID         string `json:"boundary_id"`
	Sequence           int64  `json:"sequence"`
	Kind               string `json:"kind"`
	CheckpointID       string `json:"checkpoint_id"`
	CheckpointSequence int64  `json:"checkpoint_sequence"`
	Summary            string `json:"summary"`
	RecordedAt         string `json:"recorded_at"`
}

type ContextFailure struct {
	Kind         string `json:"kind"`
	Recoverable  bool   `json:"recoverable"`
	StepID       string `json:"step_id"`
	AttemptEpoch int64  `json:"attempt_epoch"`
}

type ContinuitySnapshot struct {
	WorkID                   string                    `json:"work_id"`
	ProductIdentity          []string                  `json:"product_identity"`
	WorkflowStep             string                    `json:"workflow_step"`
	Contract                 *WorkflowReadContract     `json:"contract"`
	SpecMandate              []string                  `json:"spec_mandate"`
	PendingOperatorDecision  *WorkflowOperatorQuestion `json:"pending_operator_decision"`
	LatestCheckpoint         *ContextCheckpoint        `json:"latest_checkpoint"`
	UnresolvedFailure        *ContextFailure           `json:"unresolved_failure"`
	Boundaries               []ContextBoundary         `json:"boundaries"`
	BoundaryCount            int64                     `json:"boundary_count"`
	NextCursor               *string                   `json:"next_cursor"`
	Watermark                string                    `json:"watermark"`
	RestartAvailable         bool                      `json:"restart_available"`
	RestartUnavailableReason string                    `json:"restart_unavailable_reason"`
	// PendingMessages counts sent peer messages awaiting this work's next
	// session (CD-0029). The pointer survives restarts because the snapshot
	// itself is re-derived per call.
	PendingMessages int64 `json:"pending_messages"`
	// Observations carries the work's un-promoted observations, newest first,
	// bounded (CD-0030 D2). Read-time visibility: no gate consumes this.
	Observations     []WorkObservation `json:"observations"`
	StaleLawRevision *StaleLawRevision `json:"stale_law_revision,omitempty"`
}

type ContinuityRequest struct {
	Work   string
	Limit  int
	Cursor string
}

func ReadWorkflowContinuity(ctx context.Context, s *Store, req ContinuityRequest) (ContinuitySnapshot, error) {
	var out ContinuitySnapshot
	if s == nil || s.db == nil {
		return out, newFailure(KindUnavailable, "C19.Continuity", "store is not open", false, "open the authority database")
	}
	if len(req.Work) < 2 || len(req.Work) > 128 {
		return out, newFailure(KindInvalidOperation, "C19.Continuity", "work ID is out of bounds", false, "supply one bounded work ID")
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 20 {
		return out, newFailure(KindInvalidOperation, "C19.Continuity", "continuity history page exceeds 20", false, "reduce the history page")
	}
	offset, err := continuityOffset(req.Cursor, req.Work)
	if err != nil {
		return out, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot open a consistent continuity snapshot", true, "retry once the database is readable", err)
	}
	defer tx.Rollback()
	out.WorkID = req.Work
	out.Boundaries = []ContextBoundary{}
	out.ProductIdentity = []string{}
	out.SpecMandate = []string{}
	var currentStep string
	var definition WorkflowReadDefinition
	var workVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT current_step,definition_ref,definition_version,definition_digest,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, req.Work).Scan(&currentStep, &definition.Ref, &definition.Version, &definition.Digest, &workVersion); err != nil {
		if err == sql.ErrNoRows {
			return out, newFailure(KindProjectionNotFound, "C19.Continuity", "workflow instance is not recorded", false, "reread_entities")
		}
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read workflow step", true, "retry once the database is readable", err)
	}
	out.WorkflowStep = currentStep
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id LIMIT 65`, req.Work)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read Product identity", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		out.ProductIdentity = append(out.ProductIdentity, id)
	}
	rows.Close()
	if len(out.ProductIdentity) > 64 {
		return out, newFailure(KindLimitExceeded, "C19.Continuity", "Product identity exceeds the continuity snapshot bound", false, "reduce_limit")
	}
	var contract WorkflowReadContract
	var required, routes, mandates, modifies string
	if err := tx.QueryRowContext(ctx, `SELECT contract_version,premise,outcome_kind,outcome_payload,required_evidence,route_conventions,spec_mandate,law_modifies,rigor_class FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`, req.Work).Scan(&contract.Version, &contract.Premise, &contract.OutcomeKind, &contract.OutcomePayload, &required, &routes, &mandates, &modifies, &contract.RigorClass); err == nil {
		if json.Unmarshal([]byte(required), &contract.RequiredEvidence) != nil || json.Unmarshal([]byte(routes), &contract.RouteConventions) != nil || json.Unmarshal([]byte(mandates), &contract.SpecMandate) != nil || json.Unmarshal([]byte(modifies), &contract.LawModifies) != nil {
			return out, newFailure(KindInvariantViolation, "C19.Continuity", "workflow contract projection contains malformed arrays", false, "rebuild projections from the event log")
		}
		out.Contract = &contract
		contract.LawRevisions, err = readWorkflowLawRevisions(ctx, tx, req.Work, contract.Version)
		if err != nil {
			return out, err
		}
		if len(contract.SpecMandate) != 0 {
			homeProjectID, homeLocatorID, homeErr := workflowLawHomeTx(ctx, tx, req.Work)
			if homeErr != nil {
				return out, homeErr
			}
			out.StaleLawRevision, err = findStaleWorkflowLawRevision(ctx, tx, homeProjectID, homeLocatorID, req.Work, contract.Version, contract.SpecMandate)
			if err != nil {
				return out, err
			}
		}
		out.SpecMandate = append([]string(nil), contract.SpecMandate...)
		out.PendingOperatorDecision, err = workflowOperatorQuestionTx(req.Work, currentStep, workVersion, definition, contract)
		if err != nil {
			return out, err
		}
	} else if err != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read workflow contract", true, "retry once the database is readable", err)
	}
	var checkpoint ContextCheckpoint
	var touched, evidence, questions, decisions string
	if err := tx.QueryRowContext(ctx, `SELECT checkpoint_id,work_version,checkpoint_sequence,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions FROM workflow_context_checkpoints WHERE work_id=? ORDER BY checkpoint_sequence DESC LIMIT 1`, req.Work).Scan(&checkpoint.CheckpointID, &checkpoint.WorkVersion, &checkpoint.Sequence, &checkpoint.StepID, &checkpoint.AttemptEpoch, &checkpoint.ActiveUnit, &checkpoint.Hypothesis, &checkpoint.Diagnosis, &checkpoint.Strategy, &touched, &evidence, &questions, &decisions); err == nil {
		if json.Unmarshal([]byte(touched), &checkpoint.TouchedRefs) != nil || json.Unmarshal([]byte(evidence), &checkpoint.EvidenceRefs) != nil || json.Unmarshal([]byte(questions), &checkpoint.PendingQuestions) != nil || json.Unmarshal([]byte(decisions), &checkpoint.PendingDecisions) != nil {
			return out, newFailure(KindInvariantViolation, "C19.Continuity", "context checkpoint projection contains malformed arrays", false, "rebuild projections from the event log")
		}
		out.LatestCheckpoint = &checkpoint
	} else if err != sql.ErrNoRows {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read latest context checkpoint", true, "retry once the database is readable", err)
	}
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT instance_state FROM workflow_instances WHERE work_id=?`, req.Work).Scan(&state); err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read workflow failure state", true, "retry once the database is readable", err)
	}
	if state == "blocked" {
		var failure ContextFailure
		if err := tx.QueryRowContext(ctx, `SELECT json_extract(f.payload,'$.failure_kind'),json_extract(f.payload,'$.recoverable'),json_extract(f.payload,'$.step_id'),json_extract(f.payload,'$.attempt_epoch')
FROM domain_events f
WHERE f.subject_type='work_item' AND f.subject_id=? AND f.kind=?
  AND NOT EXISTS (
    SELECT 1 FROM domain_events c
    WHERE c.subject_type=f.subject_type AND c.subject_id=f.subject_id AND c.kind=? AND c.seq>f.seq
      AND json_extract(c.payload,'$.step_id')=json_extract(f.payload,'$.step_id')
      AND json_extract(c.payload,'$.attempt_epoch')=json_extract(f.payload,'$.attempt_epoch')
  )
ORDER BY f.seq DESC LIMIT 1`, req.Work, WorkflowActionFailed, WorkflowActionCompleted).Scan(&failure.Kind, &failure.Recoverable, &failure.StepID, &failure.AttemptEpoch); err == nil {
			out.UnresolvedFailure = &failure
		} else if err != sql.ErrNoRows {
			return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read latest workflow failure", true, "retry once the database is readable", err)
		}
	}
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM workflow_context_boundaries WHERE work_id=?`, req.Work).Scan(&total); err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot count context boundaries", true, "retry once the database is readable", err)
	}
	out.BoundaryCount = total
	rows, err = tx.QueryContext(ctx, `SELECT boundary_id,boundary_sequence,boundary_kind,checkpoint_id,checkpoint_sequence,summary,recorded_at FROM workflow_context_boundaries WHERE work_id=? ORDER BY boundary_sequence DESC LIMIT ? OFFSET ?`, req.Work, req.Limit+1, offset)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read context boundary history", true, "retry once the database is readable", err)
	}
	for rows.Next() {
		var item ContextBoundary
		if err := rows.Scan(&item.BoundaryID, &item.Sequence, &item.Kind, &item.CheckpointID, &item.CheckpointSequence, &item.Summary, &item.RecordedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.Boundaries = append(out.Boundaries, item)
	}
	rows.Close()
	if len(out.Boundaries) > req.Limit {
		last := out.Boundaries[req.Limit-1]
		out.Boundaries = out.Boundaries[:req.Limit]
		raw, _ := json.Marshal(map[string]any{"v": 1, "work": req.Work, "offset": offset + req.Limit, "last": last.Sequence})
		next := base64.RawURLEncoding.EncodeToString(raw)
		out.NextCursor = &next
	}
	var watermark int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, req.Work).Scan(&watermark); err != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read continuity watermark", true, "retry once the database is readable", err)
	}
	out.Watermark = "seq:" + strconv.FormatInt(watermark, 10)
	out.RestartAvailable = false
	out.RestartUnavailableReason = "typed restart is deliberately excluded (CD-0027); pinned continuity is re-derived per call"
	// Tx-scoped: this function holds a read transaction, and a second
	// connection would deadlock on SQLite's single writer.
	if countErr := tx.QueryRowContext(ctx, `SELECT count(*) FROM work_messages WHERE recipient_work_id=? AND state='sent'`, req.Work).Scan(&out.PendingMessages); countErr != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot count pending messages", true, "retry once the database is readable", countErr)
	}
	obsRows, obsErr := tx.QueryContext(ctx, `SELECT observation_id,work_id,statement,refs,tags,recorded_at FROM work_observations WHERE work_id=? ORDER BY recorded_at DESC, observation_id LIMIT 16`, req.Work)
	if obsErr != nil {
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot read observations", true, "retry once the database is readable", obsErr)
	}
	out.Observations = []WorkObservation{}
	for obsRows.Next() {
		var o WorkObservation
		var obsRefs, obsTags string
		if err := obsRows.Scan(&o.ObservationID, &o.WorkID, &o.Statement, &obsRefs, &obsTags, &o.RecordedAt); err != nil {
			obsRows.Close()
			return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot decode observation", true, "retry once the database is readable", err)
		}
		_ = json.Unmarshal([]byte(obsRefs), &o.Refs)
		_ = json.Unmarshal([]byte(obsTags), &o.Tags)
		out.Observations = append(out.Observations, o)
	}
	if err := obsRows.Err(); err != nil {
		obsRows.Close()
		return out, wrapFailure(KindUnavailable, "C19.Continuity", "cannot enumerate observations", true, "retry once the database is readable", err)
	}
	obsRows.Close()
	return out, nil
}

func continuityOffset(cursor, work string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, newFailure(KindInvalidCursor, "C19.Continuity", "continuity cursor is malformed", false, "use the cursor returned for this continuity query")
	}
	var value struct {
		V      int    `json:"v"`
		Work   string `json:"work"`
		Offset int    `json:"offset"`
	}
	if json.Unmarshal(b, &value) != nil || value.V != 1 || value.Work != work || value.Offset < 0 {
		return 0, newFailure(KindInvalidCursor, "C19.Continuity", "continuity cursor does not match the work", false, "use the cursor returned for this continuity query")
	}
	if value.Offset > continuityMaxOffset {
		return 0, newFailure(KindLimitExceeded, "C19.Continuity", "continuity cursor offset exceeds the bounded history", false, "reduce_limit")
	}
	return value.Offset, nil
}
