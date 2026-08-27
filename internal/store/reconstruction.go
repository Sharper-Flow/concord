package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

// ReconstructionPurpose is intentionally closed. Historical reads are
// diagnostic observations, not a second mutation or history-editing API.
type ReconstructionPurpose string

const (
	PurposeAudit     ReconstructionPurpose = "audit"
	PurposeDiagnosis ReconstructionPurpose = "diagnosis"
)

// ReconstructionSnapshot contains only the requested subject and fields
// reachable through its own event sub-log. It is disposable and never a live
// projection authority.
type ReconstructionSnapshot struct {
	Purpose         ReconstructionPurpose `json:"purpose"`
	Subject         SubjectRef            `json:"subject"`
	AsOfSeq         int64                 `json:"as_of_seq"`
	Product         *Product              `json:"product,omitempty"`
	Project         *Project              `json:"project,omitempty"`
	Work            *WorkItem             `json:"work,omitempty"`
	ProductProjects []ProjectMembership   `json:"product_projects,omitempty"`
	ProjectProducts []ProductMembership   `json:"project_products,omitempty"`
	WorkProjects    []ProjectMembership   `json:"work_projects,omitempty"`
	Relations       []RelationEdge        `json:"relations,omitempty"`
}

// ReconstructSubjectAt folds only one subject's retained event sub-log into a
// disposable SQLite database. A historical fork creates a new work ID/event;
// reconstruction never rewrites the original history.
func ReconstructSubjectAt(ctx context.Context, s *Store, subject SubjectRef, asOfSeq int64, purpose ReconstructionPurpose) (ReconstructionSnapshot, error) {
	snapshot := ReconstructionSnapshot{Purpose: purpose, Subject: subject, AsOfSeq: asOfSeq}
	if purpose != PurposeAudit && purpose != PurposeDiagnosis {
		return snapshot, newFailure(KindInvalidOperation, "reconstruct_subject", "reconstruction purpose is not recognized", false,
			"use audit or diagnosis")
	}
	if asOfSeq <= 0 {
		return snapshot, newFailure(KindInvalidOperation, "reconstruct_subject", "as-of sequence must be positive", false,
			"supply a positive event sequence")
	}
	if s == nil || s.db == nil {
		return snapshot, newFailure(KindUnavailable, "reconstruct_subject", "store is not open", false,
			"open a live store before reconstructing a subject")
	}
	if !subject.Type.valid() || subject.ID == "" {
		return snapshot, newFailure(KindInvalidSubject, "reconstruct_subject", "subject reference is not recognized", false,
			"supply a recognized subject type and non-empty ID")
	}

	liveTx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot begin live diagnostic read", true,
			"retry once the database is readable", err)
	}
	events, err := readSubjectEvents(ctx, liveTx, subject, asOfSeq)
	_ = liveTx.Rollback()
	if err != nil {
		return snapshot, err
	}
	if len(events) == 0 {
		return snapshot, newFailure(KindProjectionNotFound, "reconstruct_subject", "subject has no retained events at the requested sequence", false,
			"choose a sequence at or after the subject's creation event")
	}

	scratch, err := newReconstructionStore(ctx)
	if err != nil {
		return snapshot, err
	}
	defer func() { _ = scratch.Close() }()
	tx, err := scratch.db.BeginTx(ctx, nil)
	if err != nil {
		return snapshot, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot begin scratch reconstruction", true,
			"retry once the temporary database is available", err)
	}
	rollback := func(cause error) (ReconstructionSnapshot, error) {
		_ = tx.Rollback()
		return snapshot, cause
	}
	for _, event := range events {
		if _, err := tx.ExecContext(ctx, `INSERT INTO domain_events (seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES (?,?,?,?,?,?,?,?,?)`,
			event.Seq, event.EventID, event.Kind, event.SubjectType, event.SubjectID, event.Actor,
			event.OccurredAt.UTC().Format(time.RFC3339Nano), event.PayloadVersion, string(event.Payload)); err != nil {
			return rollback(wrapFailure(KindUnavailable, "reconstruct_subject", "cannot copy the bounded event sub-log", true,
				"retry once the temporary database is available", err))
		}
	}
	if err := enterFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := seedReconstructionEndpoints(ctx, tx, subject, events); err != nil {
		return rollback(err)
	}
	replayCtx := workflowReplayContext(ctx)
	for _, event := range events {
		if prepared, err := prepareRegisteredEvent(event); err != nil {
			return rollback(attributeFailure(err, event, prepared.stage))
		}
		if excludedFromReconstructionSnapshot(event.Kind) {
			continue
		}
		if err := foldRegisteredEvent(replayCtx, tx, event); err != nil {
			return rollback(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(); err != nil {
		return snapshot, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot commit scratch reconstruction", true,
			"retry once the temporary database is available", err)
	}
	if err := populateReconstructionSnapshot(ctx, scratch, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

// Domain registries are Git authority, and managed-resource/attachment state
// is outside the bounded Product snapshot. Their events remain validated while
// excluded from the scratch projection fold.
func excludedFromReconstructionSnapshot(kind string) bool {
	switch kind {
	case managedResourceEventCreated, managedResourceEventConsumerAdded,
		"domain.project_attachments_replaced", "domain.resource_attachments_replaced":
		return true
	default:
		return false
	}
}

func readSubjectEvents(ctx context.Context, tx *sql.Tx, subject SubjectRef, asOfSeq int64) ([]Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events WHERE subject_type = ? AND subject_id = ? AND seq <= ? ORDER BY seq`, subject.Type, subject.ID, asOfSeq)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot read the subject event sub-log", true,
			"retry once the database is readable", err)
	}
	defer func() { _ = rows.Close() }()
	var events []Event
	for rows.Next() {
		var event Event
		var occurredAt string
		if err := rows.Scan(&event.Seq, &event.EventID, &event.Kind, &event.SubjectType, &event.SubjectID, &event.Actor, &occurredAt, &event.PayloadVersion, &event.Payload); err != nil {
			return nil, wrapFailure(KindInvalidEvent, "reconstruct_subject", "cannot decode a subject event", false,
				"repair the event log before reconstructing", err)
		}
		parsed, err := time.Parse(time.RFC3339Nano, occurredAt)
		if err != nil {
			return nil, wrapFailure(KindInvalidEvent, "reconstruct_subject", "subject event has an invalid occurrence time", false,
				"repair the event log before reconstructing", err)
		}
		event.OccurredAt = parsed
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot read the subject event sub-log", true,
			"retry once the database is readable", err)
	}
	return events, nil
}

func newReconstructionStore(ctx context.Context) (*Store, error) {
	db, err := sql.Open(driverName, "file::memory:?mode=memory&cache=private&_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot open scratch SQLite", true,
			"retry once the temporary database is available", err)
	}
	db.SetMaxOpenConns(1)
	scratch := &Store{db: db, path: ":memory:"}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return scratch, nil
}

func seedReconstructionEndpoints(ctx context.Context, tx *sql.Tx, subject SubjectRef, events []Event) error {
	for _, event := range events {
		current, err := upcastEvent(event)
		if err != nil {
			continue
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(current.Payload, &payload); err != nil {
			continue
		}
		id := func(key string) string {
			var value string
			_ = json.Unmarshal(payload[key], &value)
			return value
		}
		switch {
		case subject.Type == SubjectProduct && (current.Kind == "product_project.added" || current.Kind == "product_project.removed" || current.Kind == "product_project.role_changed"):
			if projectID := id("project_id"); projectID != "" {
				if err := insertScratchProject(ctx, tx, projectID); err != nil {
					return err
				}
			}
		case subject.Type == SubjectProduct && current.Kind == "product.knowledge_home_designated":
			if projectID := id("project_id"); projectID != "" {
				if err := insertScratchProject(ctx, tx, projectID); err != nil {
					return err
				}
			}
			if locatorID := id("locator_id"); locatorID != "" {
				if err := insertScratchLocator(ctx, tx, locatorID); err != nil {
					return err
				}
			}
		case subject.Type == SubjectWorkItem && (current.Kind == "work_project.added" || current.Kind == "work_project.removed" || current.Kind == "work_project.role_changed"):
			if projectID := id("project_id"); projectID != "" {
				if err := insertScratchProject(ctx, tx, projectID); err != nil {
					return err
				}
			}
		case subject.Type == SubjectWorkItem && (current.Kind == "relation.added" || current.Kind == "relation.removed"):
			if workID := id("to"); workID != "" && workID != subject.ID {
				if err := insertScratchWork(ctx, tx, workID); err != nil {
					return err
				}
			}
		case subject.Type == SubjectWorkItem && current.Kind == "work.superseded":
			if workID := id("successor"); workID != "" && workID != subject.ID {
				if err := insertScratchWork(ctx, tx, workID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func insertScratchProject(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO projects (id,display_name,version,created_at,updated_at) VALUES (?, ?, 1, 'reconstruction', 'reconstruction')`, id, "reconstructed project "+id)
	if err != nil {
		return wrapFailure(KindUnavailable, "reconstruct_subject", "cannot prepare a scoped Project endpoint", true,
			"retry once the temporary database is available", err)
	}
	return nil
}

func insertScratchLocator(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_locators (locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES (?, '', 'canonical_path', ?, ?, 'reconstruction', 'reconstruction')`, id, "reconstruction://"+id, "reconstruction://"+id)
	if err != nil {
		return wrapFailure(KindUnavailable, "reconstruct_subject", "cannot prepare a scoped Project locator endpoint", true,
			"retry once the temporary database is available", err)
	}
	return nil
}

func insertScratchWork(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO work_items (id,kind,title,lifecycle,priority,urgency,version,created_at,updated_at) VALUES (?, 'task', ?, 'needed', 0, 'standard', 1, 'reconstruction', 'reconstruction')`, id, "reconstructed work "+id)
	if err != nil {
		return wrapFailure(KindUnavailable, "reconstruct_subject", "cannot prepare a scoped work endpoint", true,
			"retry once the temporary database is available", err)
	}
	return nil
}

func populateReconstructionSnapshot(ctx context.Context, s *Store, snapshot *ReconstructionSnapshot) error {
	switch snapshot.Subject.Type {
	case SubjectProduct:
		var product Product
		if err := s.db.QueryRowContext(ctx, `SELECT id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at FROM products WHERE id = ?`, snapshot.Subject.ID).Scan(&product.ID, &product.DisplayName, &product.StageMaturity, &product.StageAudienceCommitment, &product.Version, &product.CreatedAt, &product.UpdatedAt); err != nil {
			return newFailure(KindProjectionNotFound, "reconstruct_subject", "Product was not created by the requested sequence", false, "choose a later sequence")
		}
		snapshot.Product = &product
		memberships, err := s.ProjectsForProduct(ctx, snapshot.Subject.ID)
		if err != nil {
			return err
		}
		snapshot.ProductProjects = memberships
	case SubjectProject:
		var project Project
		if err := s.db.QueryRowContext(ctx, `SELECT id,display_name,stage_maturity_override,stage_audience_commitment_override,version,created_at,updated_at FROM projects WHERE id = ?`, snapshot.Subject.ID).Scan(&project.ID, &project.DisplayName, &project.StageMaturityOverride, &project.StageAudienceCommitmentOverride, &project.Version, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return newFailure(KindProjectionNotFound, "reconstruct_subject", "Project was not created by the requested sequence", false, "choose a later sequence")
		}
		snapshot.Project = &project
		memberships, err := s.ProductsForProject(ctx, snapshot.Subject.ID)
		if err != nil {
			return err
		}
		snapshot.ProjectProducts = memberships
	case SubjectWorkItem:
		var work WorkItem
		var terminalTime sql.NullString
		if err := s.db.QueryRowContext(ctx, `SELECT id,kind,title,lifecycle,priority,urgency,created_at,updated_at,terminal_time FROM work_items WHERE id = ?`, snapshot.Subject.ID).Scan(&work.ID, &work.Kind, &work.Title, &work.Lifecycle, &work.Priority, &work.Urgency, &work.CreatedAt, &work.UpdatedAt, &terminalTime); err != nil {
			return newFailure(KindProjectionNotFound, "reconstruct_subject", "work item was not created by the requested sequence", false, "choose a later sequence")
		}
		if terminalTime.Valid {
			work.TerminalAt = terminalTime.String
		}
		projects, err := s.ProjectsForWork(ctx, snapshot.Subject.ID)
		if err != nil {
			return err
		}
		work.Projects = projects
		work.Active = work.Lifecycle == "in_progress"
		work.Terminal = work.Lifecycle == "completed" || work.Lifecycle == "cancelled" || work.Lifecycle == "superseded"
		work.Ready = work.Lifecycle == "needed"
		snapshot.WorkProjects = work.Projects
		snapshot.Relations, err = reconstructionRelations(ctx, s.db, snapshot.Subject.ID)
		if err != nil {
			return err
		}
		snapshot.Work = &work
	}
	return nil
}

func reconstructionRelations(ctx context.Context, db *sql.DB, workID string) ([]RelationEdge, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,kind,work_id_from,work_id_to FROM relations WHERE work_id_from = ? OR work_id_to = ? ORDER BY id`, workID, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot read reconstructed work relations", true,
			"retry once the temporary database is available", err)
	}
	defer func() { _ = rows.Close() }()
	var relations []RelationEdge
	for rows.Next() {
		var relation RelationEdge
		var relationID int64
		if err := rows.Scan(&relationID, &relation.Kind, &relation.Source, &relation.Target); err != nil {
			return nil, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot decode reconstructed work relations", true,
				"retry once the temporary database is available", err)
		}
		relation.RelationID = strconv.FormatInt(relationID, 10)
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "reconstruct_subject", "cannot read reconstructed work relations", true,
			"retry once the temporary database is available", err)
	}
	return relations, nil
}
