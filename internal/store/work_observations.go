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

// WorkObservationRecorded is the typed event kind for one durable work
// observation (CD-0030). It carries generic append authority, so a public
// operation can record it on nonterminal work at any workflow step.
const WorkObservationRecorded = "work.observation_recorded"

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

// observationReadLimit is the one admitted page bound for observation reads:
// an unset or sub-one limit reads the default page and a limit above the cap
// clamps to it. The paged read and page re-limiting share it.
func observationReadLimit(limit int) int {
	if limit < 1 {
		return 10
	}
	if limit > 64 {
		return 64
	}
	return limit
}

// decodeObservationColumns decodes the stored refs and tags JSON columns. The
// fold writes only string arrays, so a value that does not decode into one is
// a projection defect: it refuses as a typed invariant violation instead of
// silently reading as an absent list.
func decodeObservationColumns(refs, tags string) ([]string, []string, error) {
	var outRefs, outTags []string
	if err := json.Unmarshal([]byte(refs), &outRefs); err != nil {
		return nil, nil, wrapFailure(KindInvariantViolation, "work_observations", "stored observation refs are not readable", false, "repair the live projection from its event log", err)
	}
	if err := json.Unmarshal([]byte(tags), &outTags); err != nil {
		return nil, nil, wrapFailure(KindInvariantViolation, "work_observations", "stored observation tags are not readable", false, "repair the live projection from its event log", err)
	}
	return outRefs, outTags, nil
}

// scanObservationRows decodes observation rows through the strict column
// decoder, so a corrupted projection refuses instead of reading as empty.
func scanObservationRows(rows *sql.Rows) ([]WorkObservation, error) {
	out := []WorkObservation{}
	for rows.Next() {
		var o WorkObservation
		var refs, tags string
		if err := rows.Scan(&o.ObservationID, &o.WorkID, &o.Statement, &refs, &tags, &o.RecordedAt); err != nil {
			return nil, wrapFailure(KindUnavailable, "work_observations", "cannot decode observation", true, "retry once the database is readable", err)
		}
		decodedRefs, decodedTags, err := decodeObservationColumns(refs, tags)
		if err != nil {
			return nil, err
		}
		o.Refs, o.Tags = decodedRefs, decodedTags
		out = append(out, o)
	}
	return out, rows.Err()
}

// workObservationsCollection is the collection identity that binds
// observation continuation cursors (work_collection_cursor.go) to this
// listing.
const workObservationsCollection = "observations"

// WorkObservationsRequest names one bounded observations page of one work
// item.
type WorkObservationsRequest struct {
	WorkID string
	Limit  int
	Cursor string
}

// WorkObservationPage is one complete observations page: the whole-work
// population total beside the bounded page, under the common result
// envelope.
type WorkObservationPage struct {
	ResultMeta
	Observations []WorkObservation `json:"observations"`
	Total        int64             `json:"total"`
}

// ReadWorkObservations reads one observations page and its whole-work total
// inside a single read transaction, ordered recorded_at descending and
// observation_id ascending with an authenticated-work continuation cursor.
func (s *Store) ReadWorkObservations(ctx context.Context, req WorkObservationsRequest) (WorkObservationPage, error) {
	if s == nil || s.db == nil {
		return WorkObservationPage{}, newFailure(KindUnavailable, "work_observations", "store is not open", false, "open the authority database")
	}
	tx, err := beginRead(ctx, s, "CD-0030.R1")
	if err != nil {
		return WorkObservationPage{}, err
	}
	defer tx.Rollback()
	page, err := observationsPageForWork(ctx, tx, req)
	if err != nil {
		return WorkObservationPage{}, err
	}
	// The envelope assignment replaces the whole embedded ResultMeta, so the
	// helper's continuation cursor is carried across it explicitly: a fresh
	// ResultMeta carries none.
	next := page.NextCursor
	page.ResultMeta, err = queryMeta(ctx, tx, "CD-0030.R1", ResolvedScope{WorkID: req.WorkID}, []string{"recorded_at", "observation_id"})
	if err != nil {
		return WorkObservationPage{}, err
	}
	page.NextCursor = next
	return page, nil
}

// observationsPageForWork reads the page data through the caller's read
// handle: the whole-work population count beside one keyset page fetched as
// limit+1 rows so the page knows whether more remain. The caller supplies
// the result envelope metadata, so this core composes inside any
// transaction.
func observationsPageForWork(ctx context.Context, q queryer, req WorkObservationsRequest) (WorkObservationPage, error) {
	limit := observationReadLimit(req.Limit)
	cursor, err := decodeWorkCollectionCursor(req.Cursor, req.WorkID, workObservationsCollection)
	if err != nil {
		return WorkObservationPage{}, err
	}
	var page WorkObservationPage
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_observations WHERE work_id=?`, req.WorkID).Scan(&page.Total); err != nil {
		return WorkObservationPage{}, wrapFailure(KindUnavailable, "work_observations", "cannot count observations", true, "retry once the database is readable", err)
	}
	query := `SELECT observation_id,work_id,statement,refs,tags,recorded_at FROM work_observations WHERE work_id=?`
	args := []any{req.WorkID}
	if req.Cursor != "" {
		// The keyset predicate mirrors the page ordering exactly: strictly
		// older rows first, then same-timestamp rows beyond the last emitted
		// identity, so continuation emits every row once and in order.
		query += ` AND (recorded_at < ? OR (recorded_at = ? AND observation_id > ?))`
		args = append(args, cursor.RecordedAt, cursor.RecordedAt, cursor.ID)
	}
	query += ` ORDER BY recorded_at DESC, observation_id ASC LIMIT ?`
	args = append(args, limit+1)
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return WorkObservationPage{}, wrapFailure(KindUnavailable, "work_observations", "cannot read observations", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	observations, err := scanObservationRows(rows)
	if err != nil {
		return WorkObservationPage{}, err
	}
	// Rows drop only behind a continuation cursor: the limit+1 probe row
	// proves more remain, and the cursor carries the last retained key.
	if len(observations) > limit {
		last := observations[limit-1]
		next, err := encodeWorkCollectionCursor(req.WorkID, workObservationsCollection, last.RecordedAt, last.ObservationID)
		if err != nil {
			return WorkObservationPage{}, err
		}
		page.NextCursor = &next
		observations = observations[:limit]
	}
	page.Observations = observations
	return page, nil
}

// LimitPage keeps a complete prefix of an intact captured page for a caller
// that must fit a byte budget. The retained prefix keeps the captured total
// and source metadata, and the continuation cursor is derived from the last
// retained observation, so no dropped row is dropped without a cursor. A
// prefix that already fits returns the page unchanged, source cursor
// included.
func (page WorkObservationPage) LimitPage(req WorkObservationsRequest, limit int) (WorkObservationPage, error) {
	limit = observationReadLimit(limit)
	if len(page.Observations) <= limit {
		return page, nil
	}
	last := page.Observations[limit-1]
	next, err := encodeWorkCollectionCursor(req.WorkID, workObservationsCollection, last.RecordedAt, last.ObservationID)
	if err != nil {
		return page, err
	}
	page.Observations = page.Observations[:limit]
	page.NextCursor = &next
	return page, nil
}
