package store

import (
	"context"
	"encoding/json"
)

// Migration 148 backfills work-context tables that migration 186 drops.
// No live read or write path may call this frozen migration replay code.

// WorkContextFinding is the declared-claim shape historical
// workflow.work_context_recorded and workflow.action_completed payloads
// carried. It discharged no predicate and recorded no verdict.
type WorkContextFinding struct {
	Kind                 string   `json:"kind"`
	Statement            string   `json:"statement"`
	SubjectRef           string   `json:"subject_ref"`
	EvidenceRefs         []string `json:"evidence_refs"`
	DomainID             string   `json:"domain_id"`
	ProductWideRationale string   `json:"product_wide_rationale,omitempty"`
}

// WorkContextReadingSource is the closed source union shape historical
// work-context readings and frozen review-migration records carry.
type WorkContextReadingSource struct {
	Kind        string `json:"kind"`
	ProjectID   string `json:"project_id,omitempty"`
	Path        string `json:"path,omitempty"`
	CommitOID   string `json:"commit_oid,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
	LawID       string `json:"law_id,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

func workContextMigrationFailure(detail string) error {
	return newFailure(KindInvariantViolation, "work_context_migration", detail, false, "restore a verified backup")
}

// writeWorkContextFacts replays one source event's declared findings into the
// work-context fact tables migration 148 builds.
func writeWorkContextFacts(ctx context.Context, q migrationRunner, event Event) error {
	if event.SubjectType != SubjectWorkItem {
		return nil
	}
	if !workContextSourceHoldsFindings(event.Kind) {
		return nil
	}
	generic, ok := workContextTerminalFindingsAtEvent(event)
	if !ok {
		return workContextMigrationFailure("the admitted context findings are malformed")
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
	return nil
}

func workContextSourceHoldsFindings(kind string) bool {
	return kind == WorkflowWorkContextRecorded || kind == WorkflowActionCompleted || kind == "worker.completed" || kind == "worker.failed"
}

func workContextTerminalFindingsAtEvent(event Event) ([]WorkContextFinding, bool) {
	var payload struct {
		ContextFindings []WorkContextFinding `json:"context_findings"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil, false
	}
	return payload.ContextFindings, true
}

// backfillWorkContextFacts is migration 148's replay backfill. Live readers
// were removed with the declaration surface; only the migration runner calls
// this.
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
