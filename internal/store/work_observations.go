package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"time"
)

// CD-0030: an observation is the durable form of "I noticed something" —
// recorded mid-life, cheaply, without deciding work-hood. Observations are
// non-authoritative: no gate, no evidence kind, no workflow action reads them
// as authority. Promotion to work is the separate, unchanged CD-0018 path.

var observationIDPattern = regexp.MustCompile(`^obs:[0-9a-f]{16}$`)

// WorkObservation is one durable observation row.
type WorkObservation struct {
	ObservationID string   `json:"observation_id"`
	WorkID        string   `json:"work_id"`
	Statement     string   `json:"statement"`
	Refs          []string `json:"refs,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	RecordedAt    string   `json:"recorded_at"`
}

type workObservationRecordedPayload struct {
	ObservationID string   `json:"observation_id"`
	Statement     string   `json:"statement"`
	Refs          []string `json:"refs"`
	Tags          []string `json:"tags"`
}

// validateObservationBounds enforces the CD-0030 D1 statement, ref, and tag
// bounds. CD-0068 D1 carries them over to the Domain anchor unchanged, so both
// folds read the same limits from here rather than restating them.
func validateObservationBounds(statement string, refs, tags []string) error {
	if len(statement) < 1 || len(statement) > 512 {
		return newFailure(KindInvalidPayload, "fold_event", "observation statement must be a bounded non-empty string", false, "supply a statement of at most 512 characters")
	}
	if len(refs) > 16 {
		return newFailure(KindInvalidPayload, "fold_event", "observation carries too many refs", false, "supply at most sixteen refs")
	}
	for _, ref := range refs {
		if len(ref) < 1 || len(ref) > 256 {
			return newFailure(KindInvalidPayload, "fold_event", "observation refs must be bounded", false, "supply bounded refs")
		}
	}
	if len(tags) > 8 {
		return newFailure(KindInvalidPayload, "fold_event", "observation carries too many tags", false, "supply at most eight tags")
	}
	for _, tag := range tags {
		if len(tag) < 1 || len(tag) > 32 {
			return newFailure(KindInvalidPayload, "fold_event", "observation tags must be bounded", false, "supply bounded tags")
		}
	}
	return nil
}

func foldWorkObservationRecorded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p workObservationRecordedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if !observationIDPattern.MatchString(p.ObservationID) {
		return newFailure(KindInvalidPayload, "fold_event", "observation id must be an obs: identifier", false, "supply a generated observation id")
	}
	if err := validateObservationBounds(p.Statement, p.Refs, p.Tags); err != nil {
		return err
	}
	// The discovery channel belongs to active work: terminal items stop
	// recording but keep their existing observations (CD-0030 D4).
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, event.SubjectID).Scan(&lifecycle); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "work item is not recorded", false, "record observations on existing work")
	} else if err != nil {
		return err
	}
	if isTerminalLifecycle(lifecycle) {
		return newFailure(KindNotTerminal, "fold_event", "terminal work cannot record observations", false, "promote the observation through capture instead")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES(?,?,?,?,?,?)`,
		p.ObservationID, event.SubjectID, p.Statement, marshalStrings(p.Refs), marshalStrings(p.Tags), event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
		// Only a duplicate id is a conflict. Reporting every insert failure as
		// one told a caller its observation id collided when the real cause was
		// a violated column constraint.
		if isIdentityConflict(err) {
			return newFailure(KindProjectionConflict, "fold_event", "observation id already exists", false, "generate a new observation id")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot write the observation", true, "retry once the database is writable", err)
	}
	return nil
}

// ObservationsForWork lists a work item's observations, newest first,
// bounded. Read-time visibility (CD-0030 D2): no gate consumes this.
func (s *Store) ObservationsForWork(ctx context.Context, workID string, limit int) ([]WorkObservation, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "work_observations", "store is not open", false, "open the authority database")
	}
	return observationsForWork(ctx, s.db, workID, limit)
}

func observationsForWork(ctx context.Context, q queryer, workID string, limit int) ([]WorkObservation, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > 64 {
		limit = 64
	}
	rows, err := q.QueryContext(ctx, `SELECT observation_id,work_id,statement,refs,tags,recorded_at FROM work_observations WHERE work_id=? ORDER BY recorded_at DESC, observation_id LIMIT ?`, workID, limit)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "work_observations", "cannot read observations", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []WorkObservation{}
	for rows.Next() {
		var o WorkObservation
		var refs, tags string
		if err := rows.Scan(&o.ObservationID, &o.WorkID, &o.Statement, &refs, &tags, &o.RecordedAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_observations", "cannot decode observation", true, "retry once the database is readable", err)
		}
		_ = json.Unmarshal([]byte(refs), &o.Refs)
		_ = json.Unmarshal([]byte(tags), &o.Tags)
		out = append(out, o)
	}
	return out, rows.Err()
}
