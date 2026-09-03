package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"
)

// InitiativeEntry is the folded relation/order/requiredness projection. These fields
// are owned by the Initiative events, never copied into the child work item.
type InitiativeEntry struct {
	InitiativeWorkID string `json:"initiative_work_id"`
	ChildWorkID      string `json:"child_work_id"`
	Position         int64  `json:"position"`
	Required         bool   `json:"required"`
}

const maxInitiativeEntriesRead = 1000

// maxInitiativeEntryPosition matches the agent surface bound. The reorder
// fold stages rows at position+1000000, so positions at or beyond the staging
// band would collide on the unique index and misclassify as retryable
// unavailability. The event fold is the authority boundary; the bound holds
// there, not only in the payload schema.
const maxInitiativeEntryPosition = 1000

// maxInitiativeNarrativeLength bounds the Initiative coordination narrative in characters,
// matching SQLite length() and JSON Schema maxLength semantics and the accepted
// summary bound used by workflow context boundaries.
const maxInitiativeNarrativeLength = 16384

type initiativeNarrativePayload struct {
	Narrative        string `json:"narrative"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

// foldInitiativeNarrativeRevised makes the Initiative coordination narrative a living
// artifact: the projection updates only through this fenced event, never at
// creation-only or direct writes.
func foldInitiativeNarrativeRevised(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p initiativeNarrativePayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.Narrative == "" || utf8.RuneCountInString(p.Narrative) > maxInitiativeNarrativeLength || p.Reason == "" || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "initiative.narrative_revised payload is invalid", false, "supply a bounded narrative, a revision reason, and consecutive versions")
	}
	kind, err := readWorkKind(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if kind != "initiative" {
		return newFailure(KindInitiativeScopeViolation, "fold_event", "narrative revision target is not an Initiative", false, "revise the narrative on an Initiative work item")
	}
	current, err := readWork(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if isTerminalLifecycle(current.lifecycle) {
		return newFailure(KindIllegalLifecycleTransition, "fold_event", "Initiative narrative cannot be revised on terminal work", false,
			"reopen the Initiative before revising its narrative")
	}
	if err := validateWorkVersion(event, current.version, p.ExpectedVersion, p.ResultingVersion); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET narrative=?, version=?, updated_at=? WHERE id=? AND version=?`, p.Narrative, p.ResultingVersion, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.SubjectID, p.ExpectedVersion)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot revise Initiative narrative", true, "retry once the database is writable", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "Initiative does not exist at the expected version", false, "reload the Initiative before revising its narrative")
	}
	return nil
}

type initiativeEntryPayload struct {
	ChildWorkID      string `json:"child_work_id"`
	Position         int64  `json:"position"`
	Required         bool   `json:"required"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

func foldInitiativeEntryAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p initiativeEntryPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ChildWorkID == "" || p.Position < 0 || p.Position > maxInitiativeEntryPosition || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "initiative_entry.added payload is invalid", false, "supply child, a position from zero through 1000, and consecutive versions")
	}
	if err := validateInitiativeEntryScope(ctx, tx, event.SubjectID, p.ChildWorkID); err != nil {
		return err
	}
	ep, err := readInitiative(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if ep != "initiative" {
		return newFailure(KindInitiativeScopeViolation, "fold_event", "entry owner is not an Initiative", false, "create the Initiative work item before adding entries")
	}
	childKind, err := readWorkKind(ctx, tx, p.ChildWorkID)
	if err != nil {
		return err
	}
	if childKind == "initiative" {
		return newFailure(KindInitiativeScopeViolation, "fold_event", "nested Initiatives are not allowed", false, "add a non-Initiative canonical work item")
	}
	subjectVersion, versionErr := mustVersion(ctx, tx, event.SubjectID)
	if versionErr != nil {
		return versionErr
	}
	if err := validateWorkVersion(event, subjectVersion, p.ExpectedVersion, p.ResultingVersion); err != nil {
		return err
	}
	if cycle, err := relationWouldCycle(ctx, tx, event.SubjectID, p.ChildWorkID, "includes"); err != nil {
		return err
	} else if cycle {
		return newFailure(KindCycleDetected, "fold_event", "Initiative entry would create an includes cycle", false, "choose a child outside the existing Initiative graph")
	}
	if err := insertRelation(ctx, tx, event, relationPayload{From: event.SubjectID, To: p.ChildWorkID, Kind: "includes"}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO initiative_entries(initiative_work_id,child_work_id,position,required) VALUES(?,?,?,?)`, event.SubjectID, p.ChildWorkID, p.Position, boolInt(p.Required)); err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindInitiativeEntryConflict, "fold_event", "Initiative child or position already exists", false, "use one unique child and position per Initiative")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot add Initiative entry", true, "retry once the database is writable", err)
	}
	return updateWorkVersion(ctx, tx, event, p.ExpectedVersion, p.ResultingVersion)
}

func foldInitiativeEntryRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	var p initiativeEntryPayload
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ChildWorkID == "" || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "initiative_entry.removed payload is invalid", false, "supply child and consecutive versions")
	}
	if err := validateInitiativeOwnerAndVersion(ctx, tx, event.SubjectID, p.ExpectedVersion); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM initiative_entries WHERE initiative_work_id=? AND child_work_id=?`, event.SubjectID, p.ChildWorkID).Scan(&exists); err == sql.ErrNoRows {
		return newFailure(KindInitiativeEntryConflict, "fold_event", "Initiative entry does not exist", false, "remove an existing Initiative entry")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect Initiative entry", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM initiative_entries WHERE initiative_work_id=? AND child_work_id=?`, event.SubjectID, p.ChildWorkID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove Initiative entry", true, "retry once the database is writable", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relations WHERE work_id_from=? AND work_id_to=? AND kind='includes'`, event.SubjectID, p.ChildWorkID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove Initiative includes relation", true, "retry once the database is writable", err)
	}
	return updateWorkVersion(ctx, tx, event, p.ExpectedVersion, p.ResultingVersion)
}

func foldInitiativeEntryReordered(ctx context.Context, tx *sql.Tx, event Event) error {
	var p initiativeEntryPayload
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ChildWorkID == "" || p.Position < 0 || p.Position > maxInitiativeEntryPosition || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "initiative_entry.reordered payload is invalid", false, "supply child, a position from zero through 1000, and consecutive versions")
	}
	if err := validateInitiativeOwnerAndVersion(ctx, tx, event.SubjectID, p.ExpectedVersion); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT child_work_id,position FROM initiative_entries WHERE initiative_work_id=? ORDER BY position,child_work_id`, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Initiative order", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var entries []InitiativeEntry
	for rows.Next() {
		var e InitiativeEntry
		e.InitiativeWorkID = event.SubjectID
		if err := rows.Scan(&e.ChildWorkID, &e.Position); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot decode Initiative order", true, "retry once the database is readable", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Initiative order", true, "retry once the database is readable", err)
	}
	idx := -1
	for i := range entries {
		if entries[i].ChildWorkID == p.ChildWorkID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return newFailure(KindInitiativeEntryConflict, "fold_event", "Initiative entry does not exist", false, "reorder an existing Initiative entry")
	}
	entry := entries[idx]
	entries = append(entries[:idx], entries[idx+1:]...)
	if p.Position > int64(len(entries)) {
		return newFailure(KindInitiativeEntryConflict, "fold_event", "Initiative position is outside the bounded entry list", false, "use a position from zero through the entry count")
	}
	entries = append(entries, InitiativeEntry{})
	copy(entries[p.Position+1:], entries[p.Position:])
	entries[p.Position] = entry
	if _, err := tx.ExecContext(ctx, `UPDATE initiative_entries SET position=position+1000000 WHERE initiative_work_id=?`, event.SubjectID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot stage Initiative reorder", true, "retry once the database is writable", err)
	}
	for i, e := range entries {
		if _, err := tx.ExecContext(ctx, `UPDATE initiative_entries SET position=? WHERE initiative_work_id=? AND child_work_id=?`, i, event.SubjectID, e.ChildWorkID); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot apply Initiative reorder", true, "retry once the database is writable", err)
		}
	}
	return updateWorkVersion(ctx, tx, event, p.ExpectedVersion, p.ResultingVersion)
}

func foldInitiativeEntryRequirednessChanged(ctx context.Context, tx *sql.Tx, event Event) error {
	var p initiativeEntryPayload
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ChildWorkID == "" || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "initiative_entry.requiredness_changed payload is invalid", false, "supply child and consecutive versions")
	}
	if err := validateInitiativeOwnerAndVersion(ctx, tx, event.SubjectID, p.ExpectedVersion); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE initiative_entries SET required=? WHERE initiative_work_id=? AND child_work_id=?`, boolInt(p.Required), event.SubjectID, p.ChildWorkID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update Initiative requiredness", true, "retry once the database is writable", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return newFailure(KindInitiativeEntryConflict, "fold_event", "Initiative entry does not exist", false, "change requiredness on an existing Initiative entry")
	}
	return updateWorkVersion(ctx, tx, event, p.ExpectedVersion, p.ResultingVersion)
}

func validateInitiativeOwnerAndVersion(ctx context.Context, tx *sql.Tx, id string, expected int64) error {
	kind, err := readInitiative(ctx, tx, id)
	if err != nil {
		return err
	}
	if kind != "initiative" {
		return newFailure(KindInitiativeScopeViolation, "fold_event", "work item is not an Initiative", false, "use an Initiative work item")
	}
	version, versionErr := mustVersion(ctx, tx, id)
	if versionErr != nil {
		return versionErr
	}
	return validateWorkVersion(Event{SubjectID: id}, version, expected, expected+1)
}
func mustVersion(ctx context.Context, tx *sql.Tx, id string) (int64, error) {
	var v int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, id).Scan(&v); err != nil {
		return 0, wrapFailure(KindUnavailable, "fold_event", "cannot read the work item version", true, "retry once the database is readable", err)
	}
	return v, nil
}
func readInitiative(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	return readWorkKind(ctx, tx, id)
}
func readWorkKind(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, id).Scan(&kind)
	if err == sql.ErrNoRows {
		return "", newFailure(KindProjectionNotFound, "fold_event", "work item does not exist", false, "create the work item first")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "fold_event", "cannot read work item kind", true, "retry once the database is readable", err)
	}
	return kind, nil
}

func validateInitiativeEntryScope(ctx context.Context, tx *sql.Tx, initiative, child string) error {
	initiativeProducts, err := workProductIDs(ctx, tx, initiative)
	if err != nil {
		return err
	}
	childProducts, err := workProductIDs(ctx, tx, child)
	if err != nil {
		return err
	}
	if len(initiativeProducts) != 1 || len(childProducts) != 1 {
		return newFailure(KindInitiativeScopeViolation, "fold_event", "Initiative and child must each derive exactly one Product", false, "assign one unambiguous shared Product scope")
	}
	if initiativeProducts[0] != childProducts[0] {
		return newFailure(KindInitiativeScopeViolation, "fold_event", "Initiative child belongs to a different Product", false, "add only children in the Initiative Product scope")
	}
	return nil
}
func workProductIDs(ctx context.Context, q queryer, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id`, id)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "fold_event", "cannot derive Product scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, wrapFailure(KindUnavailable, "fold_event", "cannot decode Product scope", true, "retry once the database is readable", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func validateInitiativeInvariantsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM work_items WHERE kind='initiative' ORDER BY id`)
	if err != nil {
		return wrapFailure(KindUnavailable, "initiative_invariants", "cannot read Initiatives", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var initiative string
		if err := rows.Scan(&initiative); err != nil {
			return wrapFailure(KindUnavailable, "initiative_invariants", "cannot decode Initiative", true, "retry once the database is readable", err)
		}
		products, err := workProductIDs(ctx, tx, initiative)
		if err != nil {
			return err
		}
		if len(products) != 1 {
			return newFailure(KindInitiativeScopeViolation, "initiative_invariants", fmt.Sprintf("Initiative %s does not derive exactly one Product", initiative), false, "repair the Initiative membership operation")
		}
		entries, err := readInitiativeEntriesTx(ctx, tx, initiative)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			kind, err := readWorkKind(ctx, tx, entry.ChildWorkID)
			if err != nil {
				return err
			}
			if kind == "initiative" {
				return newFailure(KindInitiativeScopeViolation, "initiative_invariants", "nested Initiative entry exists", false, "remove the nested Initiative entry")
			}
			if err := validateInitiativeEntryScope(ctx, tx, initiative, entry.ChildWorkID); err != nil {
				return err
			}
			var includes int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE kind='includes' AND work_id_from=? AND work_id_to=?`, initiative, entry.ChildWorkID).Scan(&includes); err != nil {
				return wrapFailure(KindUnavailable, "initiative_invariants", "cannot verify Initiative includes relation", true, "retry once the database is readable", err)
			}
			if includes != 1 {
				return newFailure(KindInitiativeScopeViolation, "initiative_invariants", "Initiative entry and includes relation diverged", false, "rebuild the Initiative projection from events")
			}
		}
	}
	return rows.Err()
}
func readInitiativeEntriesTx(ctx context.Context, tx *sql.Tx, initiative string) ([]InitiativeEntry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT initiative_work_id,child_work_id,position,required FROM initiative_entries WHERE initiative_work_id=? ORDER BY position,child_work_id`, initiative)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "initiative_entries", "cannot read Initiative entries", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []InitiativeEntry
	for rows.Next() {
		var e InitiativeEntry
		var required int
		if err := rows.Scan(&e.InitiativeWorkID, &e.ChildWorkID, &e.Position, &required); err != nil {
			return nil, wrapFailure(KindUnavailable, "initiative_entries", "cannot decode Initiative entry", true, "retry once the database is readable", err)
		}
		e.Required = required != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func initiativeRequiredChildrenComplete(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var child string
	err := tx.QueryRowContext(ctx, `SELECT e.child_work_id FROM initiative_entries e JOIN work_items w ON w.id=e.child_work_id WHERE e.initiative_work_id=? AND e.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded') LIMIT 1`, id).Scan(&child)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot inspect required Initiative children", true, "retry once the database is readable", err)
	}
	return false, newFailure(KindInitiativeCompletionBlocked, "fold_event", "required Initiative child is not terminal: "+child, false, "complete, cancel, or remove every required child")
}

// ReadInitiativeEntries returns the deterministic folded entry order.
func (s *Store) ReadInitiativeEntries(ctx context.Context, initiativeID string) ([]InitiativeEntry, error) {
	return ReadInitiativeEntries(ctx, s, initiativeID)
}
func ReadInitiativeEntries(ctx context.Context, s *Store, initiativeID string) ([]InitiativeEntry, error) {
	if s == nil || s.db == nil {
		return nil, researchUnavailable("store is not open", nil)
	}
	return readInitiativeEntriesDB(ctx, s.db, initiativeID)
}
func readInitiativeEntriesDB(ctx context.Context, db *sql.DB, id string) ([]InitiativeEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT initiative_work_id,child_work_id,position,required FROM initiative_entries WHERE initiative_work_id=? ORDER BY position,child_work_id LIMIT ?`, id, maxInitiativeEntriesRead+1)
	if err != nil {
		return nil, researchUnavailable("cannot read Initiative entries", err)
	}
	defer rows.Close()
	var out []InitiativeEntry
	for rows.Next() {
		var e InitiativeEntry
		var req int
		if err := rows.Scan(&e.InitiativeWorkID, &e.ChildWorkID, &e.Position, &req); err != nil {
			return nil, researchUnavailable("cannot decode Initiative entry", err)
		}
		e.Required = req != 0
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxInitiativeEntriesRead {
		return nil, newFailure(KindInvalidOperation, "initiative_entries", "Initiative entries exceed the bounded read limit", false, "request a narrower Initiative or use an accepted bounded query surface")
	}
	return out, nil
}

// InitiativeEntryEvent builds one of the four event-folded Initiative entry changes.
func InitiativeEntryEvent(eventID, kind, initiativeID string, entry InitiativeEntry, actor string, occurredAt time.Time, expectedVersion int64) (Event, error) {
	if _, ok := eventKindRegistry[kind]; !ok {
		return Event{}, newFailure(KindInvalidOperation, "initiative_entry_event", "unknown Initiative entry event kind", false, "use a registered Initiative entry event")
	}
	payload, _ := json.Marshal(initiativeEntryPayload{ChildWorkID: entry.ChildWorkID, Position: entry.Position, Required: entry.Required, ExpectedVersion: expectedVersion, ResultingVersion: expectedVersion + 1})
	return Event{EventID: eventID, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: initiativeID, Actor: actor, OccurredAt: occurredAt, PayloadVersion: 1, Payload: payload}, nil
}

// InitiativeNarrativeEvent builds the event-folded Initiative narrative revision.
func InitiativeNarrativeEvent(eventID, initiativeID, narrative, reason, actor string, occurredAt time.Time, expectedVersion int64) (Event, error) {
	payload, err := json.Marshal(initiativeNarrativePayload{Narrative: narrative, Reason: reason, ExpectedVersion: expectedVersion, ResultingVersion: expectedVersion + 1})
	if err != nil {
		return Event{}, wrapFailure(KindInvalidPayload, "initiative_narrative_event", "cannot encode Initiative narrative revision", false, "supply a JSON-safe narrative", err)
	}
	return Event{EventID: eventID, Kind: "initiative.narrative_revised", SubjectType: SubjectWorkItem, SubjectID: initiativeID, Actor: actor, OccurredAt: occurredAt, PayloadVersion: 1, Payload: payload}, nil
}
