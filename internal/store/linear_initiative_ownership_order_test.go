package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// CD-0171 D6: the earliest-joined Initiative owns a shared entry's Linear
// Project, and the join instant is the child's initiative_entry.added event
// seq in the log. initiative_entries has no INTEGER PRIMARY KEY, so its
// rowids change under VACUUM (https://sqlite.org/lang_vacuum.html) and a
// rebuild or migration copy can reorder its rows; ownership must never follow
// them. Within one Initiative, entry enumeration follows the declared
// position column.

// seedInitiativeEntryEvent seeds one Initiative entry row, its includes
// relation, and the matching initiative_entry.added log event at an explicit
// position, with fold guards open. tag keeps the log event id distinct when
// one membership is seeded more than once.
func seedInitiativeEntryEvent(t *testing.T, s *Store, initiative, child, tag string, position int64, required bool) {
	t.Helper()
	ctx := context.Background()
	payload, err := json.Marshal(initiativeEntryPayload{ChildWorkID: child, Position: position, Required: required, ExpectedVersion: 1, ResultingVersion: 2})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES(?, ?, ?, ?)`, []any{initiative, child, position, boolInt(required)}},
		{`INSERT INTO relations(work_id_from, work_id_to, kind, created_at) VALUES(?, ?, 'includes', '2026-09-23T00:00:00Z')`, []any{initiative, child}},
		{`INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES(?, 'initiative_entry.added', 'work_item', ?, 'operator', '2026-09-23T00:00:00Z', 1, ?)`, []any{initiative + ":" + child + ":" + tag, initiative, string(payload)}},
	} {
		if _, err := tx.ExecContext(ctx, step.query, step.args...); err != nil {
			t.Fatalf("seed entry %s->%s: %v", initiative, child, err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// TestLinearInitiativeOwnershipFollowsTheAddedEventNotRowOrder proves the
// owning Initiative comes from the lowest added-event seq even after the
// projection's physical rows are reordered, and that VACUUM cannot move a
// shared child's issue to another Initiative's Project.
func TestLinearInitiativeOwnershipFollowsTheAddedEventNotRowOrder(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "ownorder-product")
	seedLinearWorkOfKind(t, s, "ownorder-early", "ownorder-product-project", "Early initiative", "Early value")
	seedLinearWorkOfKind(t, s, "ownorder-late", "ownorder-product-project", "Late initiative", "Late value")
	seedLinearWorkItem(t, s, "ownorder-shared", "ownorder-product-project", "Shared title", "Shared value")
	// The shared child joins ownorder-early first, so its added event carries
	// the lower seq and ownorder-early owns the Project field.
	seedInitiativeEntryEvent(t, s, "ownorder-early", "ownorder-shared", "added", 0, true)
	seedInitiativeEntryEvent(t, s, "ownorder-late", "ownorder-shared", "added", 0, true)
	seedLinearProjectLink(t, s, "ownorder-early", "remote-project-early")
	seedLinearProjectLink(t, s, "ownorder-late", "remote-project-late")

	resolved, err := s.ResolveLinearProjectIDForWork(ctx, "ownorder-shared")
	if err != nil {
		t.Fatalf("ResolveLinearProjectIDForWork() error = %v", err)
	}
	if resolved != "remote-project-early" {
		t.Fatalf("resolved project id = %q, want remote-project-early from the lowest added-event seq", resolved)
	}

	// A rebuild copy that reverses physical order must not move the issue:
	// the early row lands after the late row, where a rowid-ordered read
	// would hand the Project to the late Initiative.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE initiative_entries SET rowid=rowid+1000 WHERE child_work_id='ownorder-shared' AND initiative_work_id='ownorder-early'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	var physicalOrder []string
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `SELECT initiative_work_id FROM initiative_entries WHERE child_work_id='ownorder-shared' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var initiative string
		if err := rows.Scan(&initiative); err != nil {
			t.Fatal(err)
		}
		physicalOrder = append(physicalOrder, initiative)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if !reflect.DeepEqual(physicalOrder, []string{"ownorder-late", "ownorder-early"}) {
		t.Fatalf("physical row order = %v, want the late Initiative first so the reorder is real", physicalOrder)
	}
	resolved, err = s.ResolveLinearProjectIDForWork(ctx, "ownorder-shared")
	if err != nil {
		t.Fatalf("ResolveLinearProjectIDForWork() after row reorder error = %v", err)
	}
	if resolved != "remote-project-early" {
		t.Fatalf("resolved project id after row reorder = %q, want remote-project-early: ownership follows the log, not row order", resolved)
	}

	// VACUUM rebuilds the database file and may change these rowids again;
	// ownership must survive it.
	if _, err := s.DatabaseForTesting().ExecContext(ctx, "VACUUM"); err != nil {
		t.Fatalf("VACUUM error = %v", err)
	}
	resolved, err = s.ResolveLinearProjectIDForWork(ctx, "ownorder-shared")
	if err != nil {
		t.Fatalf("ResolveLinearProjectIDForWork() after VACUUM error = %v", err)
	}
	if resolved != "remote-project-early" {
		t.Fatalf("resolved project id after VACUUM = %q, want remote-project-early", resolved)
	}
}

// TestLinearInitiativeOwnershipRestartsTheClockOnReAdd pins which added event
// counts: a removed and re-added membership joins again at its new event, so
// the Initiative it rejoined late no longer owns even though its first add
// carries the lowest seq of all.
func TestLinearInitiativeOwnershipRestartsTheClockOnReAdd(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "readd-product")
	seedLinearWorkOfKind(t, s, "readd-first", "readd-product-project", "First initiative", "First value")
	seedLinearWorkOfKind(t, s, "readd-second", "readd-product-project", "Second initiative", "Second value")
	seedLinearWorkItem(t, s, "readd-shared", "readd-product-project", "Shared title", "Shared value")
	seedInitiativeEntryEvent(t, s, "readd-first", "readd-shared", "added-1", 0, true)
	seedInitiativeEntryEvent(t, s, "readd-second", "readd-shared", "added", 0, true)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
DELETE FROM initiative_entries WHERE initiative_work_id='readd-first' AND child_work_id='readd-shared';
DELETE FROM relations WHERE work_id_from='readd-first' AND work_id_to='readd-shared' AND kind='includes';
INSERT INTO domain_events(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload) VALUES('readd-first:readd-shared:removed', 'initiative_entry.removed', 'work_item', 'readd-first', 'operator', '2026-09-23T00:00:00Z', 1, '{"child_work_id":"readd-shared","expected_version":1,"resulting_version":2}');
DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	seedInitiativeEntryEvent(t, s, "readd-first", "readd-shared", "added-2", 0, true)
	seedLinearProjectLink(t, s, "readd-first", "remote-project-first")
	seedLinearProjectLink(t, s, "readd-second", "remote-project-second")

	resolved, err := s.ResolveLinearProjectIDForWork(ctx, "readd-shared")
	if err != nil {
		t.Fatalf("ResolveLinearProjectIDForWork() error = %v", err)
	}
	if resolved != "remote-project-second" {
		t.Fatalf("resolved project id = %q, want remote-project-second: the re-added membership joins at its new event", resolved)
	}
}

// TestLinearInitiativeEntryRefreshEnumeratesByPosition proves the completion
// refresh walks one Initiative's entries by the declared position column, so
// the queued issue updates carry the Initiative's order even when the
// projection's physical row order disagrees.
func TestLinearInitiativeEntryRefreshEnumeratesByPosition(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "entryorder-product")
	setupLinearLabelConnection(t, s, "entryorder-product", map[string]string{"task": "label-task", "optional": "label-optional", "project:entryorder-product-project": "label-repo"})
	enableLinearPlanning(t, s, "entryorder-product", 2)
	seedLinearWorkOfKind(t, s, "entryorder-initiative", "entryorder-product-project", "Order initiative", "Order value")
	seedLinearWorkItem(t, s, "entryorder-pos5", "entryorder-product-project", "Fifth title", "Fifth value")
	seedLinearWorkItem(t, s, "entryorder-pos0", "entryorder-product-project", "First title", "First value")
	seedLinearWorkItem(t, s, "entryorder-pos2", "entryorder-product-project", "Third title", "Third value")
	// Physical insert order 5, 0, 2 disagrees with the declared positions.
	seedInitiativeEntryEvent(t, s, "entryorder-initiative", "entryorder-pos5", "added", 5, true)
	seedInitiativeEntryEvent(t, s, "entryorder-initiative", "entryorder-pos0", "added", 0, false)
	seedInitiativeEntryEvent(t, s, "entryorder-initiative", "entryorder-pos2", "added", 2, true)
	for i, child := range []string{"entryorder-pos5", "entryorder-pos0", "entryorder-pos2"} {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES(?, ?, ?, '', '', '', 'confirmed', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`, child, child+"-uuid", fmt.Sprintf("EX-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	claimed, err := enqueueLinearIssueUpdatesForInitiativeEntriesTx(ctx, tx, "entryorder-initiative", time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("enqueueLinearIssueUpdatesForInitiativeEntriesTx() error = %v", err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(claimed))
	for _, op := range claimed {
		got = append(got, op.WorkID)
	}
	want := []string{"entryorder-pos0", "entryorder-pos2", "entryorder-pos5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enumeration order = %v, want %v: entries walk by declared position", got, want)
	}
}
