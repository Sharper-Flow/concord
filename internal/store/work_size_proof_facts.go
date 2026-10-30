package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Only the open pass and proofs since the latest assessment are maintained.
// Contract changes close the pass, but do not retract proofs already earned.
type workSizeProofFacts struct {
	Accepted bool     `json:"accepted"`
	Refined  bool     `json:"refined"`
	Proofs   []string `json:"proofs"`
}

func readWorkSizeProofFacts(ctx context.Context, q queryer, workID string) (workSizeProofFacts, error) {
	var state workSizeProofFacts
	var raw string
	err := q.QueryRowContext(ctx, `SELECT state FROM work_size_proof_facts WHERE work_id=?`, workID).Scan(&raw)
	if err == sql.ErrNoRows {
		return state, nil
	}
	if err != nil {
		return state, wrapFailure(KindUnavailable, "work_size", "cannot read the review window", true, "retry once the database is readable", err)
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return state, wrapFailure(KindInvariantViolation, "work_size", "stored review window is malformed", false, "restore a verified backup into a new path with concord restore, then swap", err)
	}
	return state, nil
}

func writeWorkSizeProofFacts(ctx context.Context, q migrationRunner, workID string, state workSizeProofFacts) error {
	_, err := q.ExecContext(ctx, `INSERT INTO work_size_proof_facts(work_id,state) VALUES(?,?)
		ON CONFLICT(work_id) DO UPDATE SET state=excluded.state`, workID, workflowJSON(state))
	return err
}

// Called only after admission, on completed actions and approved/superseded
// contracts. The completed action carries the exact recorded Review.
func advanceWorkSizeProofFactsTx(ctx context.Context, tx *sql.Tx, event Event) error {
	actionID := ""
	review := false
	switch event.Kind {
	case WorkflowContractApproved, WorkflowContractSuperseded:
	case WorkflowActionCompleted:
		var action workflowActionCompletedPayload
		if err := json.Unmarshal(event.Payload, &action); err != nil {
			return wrapFailure(KindInvariantViolation, "work_size", "a stored review completion is malformed", false, "restore a verified backup into a new path with concord restore, then swap", err)
		}
		actionID = action.ActionID
		switch actionID {
		case "checkpoint_refine":
		case "record_review":
			review = action.Review != nil
		default:
			return nil
		}
	default:
		return nil
	}
	state, err := readWorkSizeProofFacts(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if err := advanceWorkSizeProofWindow(&state, event.Kind, actionID, event.EventID, review, func() (bool, error) {
		return workSizeReviewFindsInScope(string(event.Payload), event.Seq)
	}); err != nil {
		return err
	}
	return writeWorkSizeProofFacts(ctx, tx, event.SubjectID, state)
}

func advanceWorkSizeProofWindow(state *workSizeProofFacts, kind, actionID, eventID string, review bool, qualifies func() (bool, error)) error {
	switch {
	case kind != WorkflowActionCompleted:
		state.Accepted, state.Refined = false, false
	case actionID == "checkpoint_refine":
		state.Refined = state.Accepted
	case review && actionID == "record_review":
		if state.Refined {
			found, err := qualifies()
			if err != nil {
				return err
			}
			if found {
				state.Proofs = append(state.Proofs, eventID)
			}
		}
		state.Accepted, state.Refined = true, false
	}
	return nil
}

func resetWorkSizeProofFactsTx(ctx context.Context, tx *sql.Tx, workID string) error {
	return writeWorkSizeProofFacts(ctx, tx, workID, workSizeProofFacts{})
}

// Migration-only selection matches the former window and report queries. The
// caller owns the transaction and guard; no second connection or guard opens.
func backfillWorkSizeProofFacts(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT id FROM work_items ORDER BY id`)
	if err != nil {
		return err
	}
	var workIDs []string
	for rows.Next() {
		var workID string
		if err := rows.Scan(&workID); err != nil {
			rows.Close()
			return err
		}
		workIDs = append(workIDs, workID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, workID := range workIDs {
		if err := backfillWorkSizeProofWindow(ctx, q, workID); err != nil {
			return err
		}
	}
	return nil
}

func backfillWorkSizeProofWindow(ctx context.Context, q migrationRunner, workID string) error {
	boundary, err := workSizeAssessmentFrontierTx(ctx, q, workID)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT e.seq,e.event_id,e.kind,
		coalesce(json_extract(e.payload,'$.action_id'),''),coalesce(json_extract(e.payload,'$.worker_attempt_id'),''),
		(e.kind='workflow.action_completed' AND json_extract(e.payload,'$.review') IS NOT NULL)
		FROM domain_events e WHERE e.subject_type='work_item' AND e.subject_id=? AND e.seq>?
		AND e.kind IN (?,?,?) ORDER BY e.seq`, workID, boundary, WorkflowActionCompleted, WorkflowContractApproved, WorkflowContractSuperseded)
	if err != nil {
		return err
	}
	type effect struct {
		seq                       int64
		id, kind, action, attempt string
		review                    bool
	}
	var effects []effect
	for rows.Next() {
		var e effect
		if err := rows.Scan(&e.seq, &e.id, &e.kind, &e.action, &e.attempt, &e.review); err != nil {
			rows.Close()
			return err
		}
		effects = append(effects, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	var state workSizeProofFacts
	for _, e := range effects {
		if e.action == "accept_worker_evidence" || e.action == "accept_worker_result" {
			var terminalSeq int64
			var raw string
			err := q.QueryRowContext(ctx, `SELECT seq,payload FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind='worker.completed' AND json_extract(payload,'$.attempt_id')=? AND seq<? AND json_extract(payload,'$.review') IS NOT NULL ORDER BY seq DESC LIMIT 1`, workID, e.attempt, e.seq).Scan(&terminalSeq, &raw)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			e.action, e.review = "record_review", true
			if err := advanceWorkSizeProofWindow(&state, e.kind, e.action, e.id, true, func() (bool, error) { return workSizeReviewFindsInScope(raw, terminalSeq) }); err != nil {
				return err
			}
			continue
		}
		if e.kind == WorkflowActionCompleted && e.action != "checkpoint_refine" && !(e.review && e.action == "record_review") {
			continue
		}
		if err := advanceWorkSizeProofWindow(&state, e.kind, e.action, e.id, e.review, func() (bool, error) {
			var raw string
			if err := q.QueryRowContext(ctx, `SELECT payload FROM domain_events WHERE seq=?`, e.seq).Scan(&raw); err != nil {
				return false, err
			}
			return workSizeReviewFindsInScope(raw, e.seq)
		}); err != nil {
			return err
		}
	}
	return writeWorkSizeProofFacts(ctx, q, workID, state)
}
