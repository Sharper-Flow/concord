package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// EpicEntry is the folded relation/order/requiredness projection. These fields
// are owned by the Epic events, never copied into the child work item.
type EpicEntry struct {
	EpicWorkID  string `json:"epic_work_id"`
	ChildWorkID string `json:"child_work_id"`
	Position    int64  `json:"position"`
	Required    bool   `json:"required"`
}

const maxEpicEntriesRead = 1000

type epicEntryPayload struct {
	ChildWorkID      string `json:"child_work_id"`
	Position         int64  `json:"position"`
	Required         bool   `json:"required"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

func foldEpicEntryAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p epicEntryPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ChildWorkID == "" || p.Position < 0 || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "epic_entry.added payload is invalid", false, "supply child, non-negative position, and consecutive versions")
	}
	if err := validateEpicEntryScope(ctx, tx, event.SubjectID, p.ChildWorkID); err != nil {
		return err
	}
	ep, err := readEpic(ctx, tx, event.SubjectID)
	if err != nil {
		return err
	}
	if ep != "epic" {
		return newFailure(KindEpicScopeViolation, "fold_event", "entry owner is not an Epic", false, "create the Epic work item before adding entries")
	}
	childKind, err := readWorkKind(ctx, tx, p.ChildWorkID)
	if err != nil {
		return err
	}
	if childKind == "epic" {
		return newFailure(KindEpicScopeViolation, "fold_event", "nested Epics are not allowed", false, "add a non-Epic canonical work item")
	}
	if err := validateWorkVersion(event, mustVersion(ctx, tx, event.SubjectID), p.ExpectedVersion, p.ResultingVersion); err != nil {
		return err
	}
	if cycle, err := relationWouldCycle(ctx, tx, event.SubjectID, p.ChildWorkID, "parent"); err != nil {
		return err
	} else if cycle {
		return newFailure(KindCycleDetected, "fold_event", "Epic entry would create a parent cycle", false, "choose a child outside the existing parent graph")
	}
	if err := insertRelation(ctx, tx, event, relationPayload{From: event.SubjectID, To: p.ChildWorkID, Kind: "parent"}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO epic_entries(epic_work_id,child_work_id,position,required) VALUES(?,?,?,?)`, event.SubjectID, p.ChildWorkID, p.Position, boolInt(p.Required)); err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindEpicEntryConflict, "fold_event", "Epic child or position already exists", false, "use one unique child and position per Epic")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot add Epic entry", true, "retry once the database is writable", err)
	}
	return updateWorkVersion(ctx, tx, event, p.ExpectedVersion, p.ResultingVersion)
}

func foldEpicEntryRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	var p epicEntryPayload
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ChildWorkID == "" || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "epic_entry.removed payload is invalid", false, "supply child and consecutive versions")
	}
	if err := validateEpicOwnerAndVersion(ctx, tx, event.SubjectID, p.ExpectedVersion); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM epic_entries WHERE epic_work_id=? AND child_work_id=?`, event.SubjectID, p.ChildWorkID).Scan(&exists); err == sql.ErrNoRows {
		return newFailure(KindEpicEntryConflict, "fold_event", "Epic entry does not exist", false, "remove an existing Epic entry")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect Epic entry", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM epic_entries WHERE epic_work_id=? AND child_work_id=?`, event.SubjectID, p.ChildWorkID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove Epic entry", true, "retry once the database is writable", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relations WHERE work_id_from=? AND work_id_to=? AND kind='parent'`, event.SubjectID, p.ChildWorkID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove Epic parent relation", true, "retry once the database is writable", err)
	}
	return updateWorkVersion(ctx, tx, event, p.ExpectedVersion, p.ResultingVersion)
}

func foldEpicEntryReordered(ctx context.Context, tx *sql.Tx, event Event) error {
	var p epicEntryPayload
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ChildWorkID == "" || p.Position < 0 || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "epic_entry.reordered payload is invalid", false, "supply child, position, and consecutive versions")
	}
	if err := validateEpicOwnerAndVersion(ctx, tx, event.SubjectID, p.ExpectedVersion); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT child_work_id,position FROM epic_entries WHERE epic_work_id=? ORDER BY position,child_work_id`, event.SubjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Epic order", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var entries []EpicEntry
	for rows.Next() {
		var e EpicEntry
		e.EpicWorkID = event.SubjectID
		if err := rows.Scan(&e.ChildWorkID, &e.Position); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot decode Epic order", true, "retry once the database is readable", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Epic order", true, "retry once the database is readable", err)
	}
	idx := -1
	for i := range entries {
		if entries[i].ChildWorkID == p.ChildWorkID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return newFailure(KindEpicEntryConflict, "fold_event", "Epic entry does not exist", false, "reorder an existing Epic entry")
	}
	entry := entries[idx]
	entries = append(entries[:idx], entries[idx+1:]...)
	if p.Position > int64(len(entries)) {
		return newFailure(KindEpicEntryConflict, "fold_event", "Epic position is outside the bounded entry list", false, "use a position from zero through the entry count")
	}
	entries = append(entries, EpicEntry{})
	copy(entries[p.Position+1:], entries[p.Position:])
	entries[p.Position] = entry
	if _, err := tx.ExecContext(ctx, `UPDATE epic_entries SET position=position+1000000 WHERE epic_work_id=?`, event.SubjectID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot stage Epic reorder", true, "retry once the database is writable", err)
	}
	for i, e := range entries {
		if _, err := tx.ExecContext(ctx, `UPDATE epic_entries SET position=? WHERE epic_work_id=? AND child_work_id=?`, i, event.SubjectID, e.ChildWorkID); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot apply Epic reorder", true, "retry once the database is writable", err)
		}
	}
	return updateWorkVersion(ctx, tx, event, p.ExpectedVersion, p.ResultingVersion)
}

func foldEpicEntryRequirednessChanged(ctx context.Context, tx *sql.Tx, event Event) error {
	var p epicEntryPayload
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ChildWorkID == "" || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "epic_entry.requiredness_changed payload is invalid", false, "supply child and consecutive versions")
	}
	if err := validateEpicOwnerAndVersion(ctx, tx, event.SubjectID, p.ExpectedVersion); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE epic_entries SET required=? WHERE epic_work_id=? AND child_work_id=?`, boolInt(p.Required), event.SubjectID, p.ChildWorkID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update Epic requiredness", true, "retry once the database is writable", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return newFailure(KindEpicEntryConflict, "fold_event", "Epic entry does not exist", false, "change requiredness on an existing Epic entry")
	}
	return updateWorkVersion(ctx, tx, event, p.ExpectedVersion, p.ResultingVersion)
}

func validateEpicOwnerAndVersion(ctx context.Context, tx *sql.Tx, id string, expected int64) error {
	kind, err := readEpic(ctx, tx, id)
	if err != nil {
		return err
	}
	if kind != "epic" {
		return newFailure(KindEpicScopeViolation, "fold_event", "work item is not an Epic", false, "use an Epic work item")
	}
	return validateWorkVersion(Event{SubjectID: id}, mustVersion(ctx, tx, id), expected, expected+1)
}
func mustVersion(ctx context.Context, tx *sql.Tx, id string) int64 {
	var v int64
	_ = tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, id).Scan(&v)
	return v
}
func readEpic(ctx context.Context, tx *sql.Tx, id string) (string, error) {
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

func validateEpicEntryScope(ctx context.Context, tx *sql.Tx, epic, child string) error {
	epicProducts, err := workProductIDs(ctx, tx, epic)
	if err != nil {
		return err
	}
	childProducts, err := workProductIDs(ctx, tx, child)
	if err != nil {
		return err
	}
	if len(epicProducts) != 1 || len(childProducts) != 1 {
		return newFailure(KindEpicScopeViolation, "fold_event", "Epic and child must each derive exactly one Product", false, "assign one unambiguous shared Product scope")
	}
	if epicProducts[0] != childProducts[0] {
		return newFailure(KindEpicScopeViolation, "fold_event", "Epic child belongs to a different Product", false, "add only children in the Epic Product scope")
	}
	return nil
}
func workProductIDs(ctx context.Context, tx *sql.Tx, id string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id=? ORDER BY pp.product_id`, id)
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

func validateEpicInvariantsTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM work_items WHERE kind='epic' ORDER BY id`)
	if err != nil {
		return wrapFailure(KindUnavailable, "epic_invariants", "cannot read Epics", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var epic string
		if err := rows.Scan(&epic); err != nil {
			return wrapFailure(KindUnavailable, "epic_invariants", "cannot decode Epic", true, "retry once the database is readable", err)
		}
		products, err := workProductIDs(ctx, tx, epic)
		if err != nil {
			return err
		}
		if len(products) != 1 {
			return newFailure(KindEpicScopeViolation, "epic_invariants", fmt.Sprintf("Epic %s does not derive exactly one Product", epic), false, "repair the Epic membership operation")
		}
		entries, err := readEpicEntriesTx(ctx, tx, epic)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			kind, err := readWorkKind(ctx, tx, entry.ChildWorkID)
			if err != nil {
				return err
			}
			if kind == "epic" {
				return newFailure(KindEpicScopeViolation, "epic_invariants", "nested Epic entry exists", false, "remove the nested Epic entry")
			}
			if err := validateEpicEntryScope(ctx, tx, epic, entry.ChildWorkID); err != nil {
				return err
			}
			var parent int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM relations WHERE kind='parent' AND work_id_from=? AND work_id_to=?`, epic, entry.ChildWorkID).Scan(&parent); err != nil {
				return wrapFailure(KindUnavailable, "epic_invariants", "cannot verify Epic parent relation", true, "retry once the database is readable", err)
			}
			if parent != 1 {
				return newFailure(KindEpicScopeViolation, "epic_invariants", "Epic entry and parent relation diverged", false, "rebuild the Epic projection from events")
			}
		}
	}
	return rows.Err()
}
func readEpicEntriesTx(ctx context.Context, tx *sql.Tx, epic string) ([]EpicEntry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT epic_work_id,child_work_id,position,required FROM epic_entries WHERE epic_work_id=? ORDER BY position,child_work_id`, epic)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "epic_entries", "cannot read Epic entries", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []EpicEntry
	for rows.Next() {
		var e EpicEntry
		var required int
		if err := rows.Scan(&e.EpicWorkID, &e.ChildWorkID, &e.Position, &required); err != nil {
			return nil, wrapFailure(KindUnavailable, "epic_entries", "cannot decode Epic entry", true, "retry once the database is readable", err)
		}
		e.Required = required != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func epicRequiredChildrenComplete(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var child string
	err := tx.QueryRowContext(ctx, `SELECT e.child_work_id FROM epic_entries e JOIN work_items w ON w.id=e.child_work_id WHERE e.epic_work_id=? AND e.required=1 AND w.lifecycle NOT IN ('completed','cancelled','superseded') LIMIT 1`, id).Scan(&child)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, wrapFailure(KindUnavailable, "fold_event", "cannot inspect required Epic children", true, "retry once the database is readable", err)
	}
	return false, newFailure(KindEpicCompletionBlocked, "fold_event", "required Epic child is not terminal: "+child, false, "complete, cancel, or remove every required child")
}

// ReadEpicEntries returns the deterministic folded entry order.
func (s *Store) ReadEpicEntries(ctx context.Context, epicID string) ([]EpicEntry, error) {
	return ReadEpicEntries(ctx, s, epicID)
}
func ReadEpicEntries(ctx context.Context, s *Store, epicID string) ([]EpicEntry, error) {
	if s == nil || s.db == nil {
		return nil, researchUnavailable("store is not open", nil)
	}
	return readEpicEntriesDB(ctx, s.db, epicID)
}
func readEpicEntriesDB(ctx context.Context, db *sql.DB, id string) ([]EpicEntry, error) {
	rows, err := db.QueryContext(ctx, `SELECT epic_work_id,child_work_id,position,required FROM epic_entries WHERE epic_work_id=? ORDER BY position,child_work_id LIMIT ?`, id, maxEpicEntriesRead+1)
	if err != nil {
		return nil, researchUnavailable("cannot read Epic entries", err)
	}
	defer rows.Close()
	var out []EpicEntry
	for rows.Next() {
		var e EpicEntry
		var req int
		if err := rows.Scan(&e.EpicWorkID, &e.ChildWorkID, &e.Position, &req); err != nil {
			return nil, researchUnavailable("cannot decode Epic entry", err)
		}
		e.Required = req != 0
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxEpicEntriesRead {
		return nil, newFailure(KindInvalidOperation, "epic_entries", "Epic entries exceed the bounded read limit", false, "request a narrower Epic or use an accepted bounded query surface")
	}
	return out, nil
}

// EpicEntryEvent builds one of the four event-folded Epic entry changes.
func EpicEntryEvent(eventID, kind, epicID string, entry EpicEntry, actor string, occurredAt time.Time, expectedVersion int64) (Event, error) {
	if _, ok := eventKindRegistry[kind]; !ok {
		return Event{}, newFailure(KindInvalidOperation, "epic_entry_event", "unknown Epic entry event kind", false, "use a registered Epic entry event")
	}
	payload, _ := json.Marshal(epicEntryPayload{ChildWorkID: entry.ChildWorkID, Position: entry.Position, Required: entry.Required, ExpectedVersion: expectedVersion, ResultingVersion: expectedVersion + 1})
	return Event{EventID: eventID, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: epicID, Actor: actor, OccurredAt: occurredAt, PayloadVersion: 1, Payload: payload}, nil
}
