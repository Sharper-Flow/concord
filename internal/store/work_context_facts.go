package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Findings remain selectable claims until work cleanup. Their coordinates are
// public identities, not inputs for replaying a declaration or terminal report.
func writeWorkContextFacts(ctx context.Context, q migrationRunner, event Event) error {
	if event.SubjectType != SubjectWorkItem {
		return nil
	}
	if !workContextSourceHoldsFindings(event.Kind) {
		return nil
	}
	generic, ranked, ok := workContextTerminalFindingsAtEvent(event)
	if !ok {
		return workContextFailure(KindInvariantViolation, "work_context", "the admitted context findings are malformed", "restore a verified backup")
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO work_context_state(work_id,source_frontier) VALUES(?,?) ON CONFLICT(work_id) DO UPDATE SET source_frontier=excluded.source_frontier`, event.SubjectID, event.Seq); err != nil {
		return err
	}
	if event.Kind == WorkflowWorkContextRecorded {
		if _, err := q.ExecContext(ctx, `UPDATE work_context_state SET declaration_seq=?,declaration_id=?,declaration_payload=? WHERE work_id=?`, event.Seq, event.EventID, string(event.Payload), event.SubjectID); err != nil {
			return err
		}
	}
	write := func(ordinal int, class string, finding any) error {
		raw, err := json.Marshal(finding)
		if err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `INSERT INTO work_context_findings(work_id,source_seq,ordinal,finding_class,finding) VALUES(?,?,?,?,?)`, event.SubjectID, event.Seq, ordinal, class, string(raw))
		return err
	}
	for ordinal, finding := range generic {
		if err := write(ordinal, "generic", finding); err != nil {
			return err
		}
	}
	for ordinal, finding := range ranked {
		if err := write(len(generic)+ordinal, "ranked", finding); err != nil {
			return err
		}
	}
	return nil
}

func readWorkContextFindingSource(ctx context.Context, q queryer, workID string, seq int64) (Event, []WorkContextFinding, []ReviewFinding, error) {
	var source Event
	err := q.QueryRowContext(ctx, `SELECT event_id,kind FROM effect_receipts WHERE effect_seq=? AND subject_type='work_item' AND subject_id=?`, seq, workID).Scan(&source.EventID, &source.Kind)
	if err != nil {
		return source, nil, nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT finding_class,finding FROM work_context_findings WHERE work_id=? AND source_seq=? ORDER BY ordinal`, workID, seq)
	if err != nil {
		return source, nil, nil, err
	}
	defer rows.Close()
	var generic []WorkContextFinding
	var ranked []ReviewFinding
	for rows.Next() {
		var class, raw string
		if err := rows.Scan(&class, &raw); err != nil {
			return source, nil, nil, err
		}
		if class == "generic" {
			var finding WorkContextFinding
			if err := json.Unmarshal([]byte(raw), &finding); err != nil {
				return source, nil, nil, workContextFailure(KindInvariantViolation, "work_context_read", "stored context finding facts are malformed", "restore a verified backup into a new path with concord restore, then swap")
			}
			generic = append(generic, finding)
		} else {
			var finding ReviewFinding
			if err := json.Unmarshal([]byte(raw), &finding); err != nil {
				return source, nil, nil, workContextFailure(KindInvariantViolation, "work_context_read", "stored context finding facts are malformed", "restore a verified backup into a new path with concord restore, then swap")
			}
			ranked = append(ranked, finding)
		}
	}
	return source, generic, ranked, rows.Err()
}

func workContextSourceHoldsFindings(kind string) bool {
	return kind == WorkflowWorkContextRecorded || kind == WorkflowActionCompleted || kind == "worker.completed" || kind == "worker.failed"
}

func workContextTerminalFindingsAtEvent(event Event) ([]WorkContextFinding, []ReviewFinding, bool) {
	var payload struct {
		ContextFindings []WorkContextFinding `json:"context_findings"`
		Review          *ReviewBlock         `json:"review"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil, nil, false
	}
	var ranked []ReviewFinding
	if payload.Review != nil {
		ranked = payload.Review.Findings
	}
	return payload.ContextFindings, ranked, true
}

// Only migration148 reads source envelopes. Live readers use claims and the
// single current declaration, while effect receipts supply source identity.
func backfillWorkContextFacts(ctx context.Context, q migrationRunner) error {
	rows, err := q.QueryContext(ctx, `SELECT seq,event_id,kind,subject_type,subject_id,payload FROM domain_events WHERE subject_type='work_item' AND kind IN (?,'worker.completed','worker.failed') AND subject_id IN (SELECT id FROM work_items) ORDER BY seq`, WorkflowWorkContextRecorded)
	if err != nil {
		return err
	}
	var events []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(&event.Seq, &event.EventID, &event.Kind, &event.SubjectType, &event.SubjectID, &event.Payload); err != nil {
			rows.Close()
			return err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, event := range events {
		if err := writeWorkContextFacts(ctx, q, event); err != nil {
			return err
		}
	}
	return nil
}

func readWorkContextSourceFrontier(ctx context.Context, q queryer, workID string) (int64, error) {
	var frontier int64
	err := q.QueryRowContext(ctx, `SELECT source_frontier FROM work_context_state WHERE work_id=?`, workID).Scan(&frontier)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return frontier, err
}
