package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOverlapInvalidationOwnership(t *testing.T) {
	ctx := context.Background()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "current.db"), CurrentSchemaVersion())
	exec := func(q string, args ...any) { retirementUpgradeExec(t, db, q, args...) }
	exec(`PRAGMA foreign_keys=ON`)
	exec(`INSERT INTO write_guard(active) VALUES(1)`)
	exec(`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('p','Product','prototype','operator_only',1,'t','t')`)
	actor := DeriveWorkflowActorRef("principal:one", "client:one", "agent:one", "session:one")
	exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal:one','client:one','agent:one','session:one','agent','t')`, actor)
	for _, id := range []string{"a", "b", "c", "d"} {
		exec(`INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES(?,'task','Work','needed',1,'t','t')`, id)
		exec(`INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version) VALUES(?,1,'internal_sqlite','t',?,'[]','[]',1)`, id, actor)
	}
	for i, id := range []string{"ab", "ba", "ac", "cb", "cd", "old", "bd", "pair-event", "reopen-event"} {
		exec(`INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version) VALUES(?,?,'workflow.overlap','work_item','a','actor',1)`, id, i+1)
	}
	for i, r := range []struct{ id, from, to string }{{"ab", "a", "b"}, {"ba", "b", "a"}, {"ac", "a", "c"}, {"cb", "c", "b"}, {"cd", "c", "d"}, {"old", "a", "b"}, {"bd", "b", "d"}} {
		var invalidated any
		if r.id == "old" {
			invalidated = 6
		}
		exec(`INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,created_at,invalidated_seq) VALUES(?,?,'p',?,?,1,1,'depends_on','reason','t',?)`, r.id, i+1, r.from, r.to, invalidated)
		if r.id != "old" {
			exec(`INSERT INTO relations(work_id_from,work_id_to,kind,created_at,resolution_id,reason,record_version) VALUES(?,?,'depends_on','t',?,'reason',1)`, r.from, r.to, r.id)
		}
	}
	exec(`INSERT INTO relations(work_id_from,work_id_to,kind,created_at,reason,record_version) VALUES('a','d','blocks','t','ordinary',1)`)
	exec(`DELETE FROM write_guard`)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := invalidateWorkflowOverlapResolutionPairTx(ctx, tx, "pair-event", "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT resolution_id,invalidated_seq FROM workflow_overlap_resolutions ORDER BY resolution_id`); !reflect.DeepEqual(got, [][]any{{"ab", int64(8)}, {"ac", nil}, {"ba", int64(8)}, {"bd", nil}, {"cb", nil}, {"cd", nil}, {"old", int64(6)}}) {
		t.Fatalf("pair invalidation: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT COALESCE(resolution_id,'ordinary') FROM relations ORDER BY 1`); !reflect.DeepEqual(got, [][]any{{"ac"}, {"bd"}, {"cb"}, {"cd"}, {"ordinary"}}) {
		t.Fatalf("pair relations: %v", got)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := invalidateWorkflowOverlapResolutionsForWorkTx(ctx, tx, "reopen-event", "c"); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT resolution_id,invalidated_seq FROM workflow_overlap_resolutions ORDER BY resolution_id`); !reflect.DeepEqual(got, [][]any{{"ab", int64(8)}, {"ac", int64(9)}, {"ba", int64(8)}, {"bd", nil}, {"cb", int64(9)}, {"cd", int64(9)}, {"old", int64(6)}}) {
		t.Fatalf("reopen invalidation: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT COALESCE(resolution_id,'ordinary') FROM relations ORDER BY 1`); !reflect.DeepEqual(got, [][]any{{"bd"}, {"ordinary"}}) {
		t.Fatalf("reopen relations: %v", got)
	}
}

func TestOverlapInvalidationIndexedPlans(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "plans.db"), CurrentSchemaVersion())
	retirementUpgradeExec(t, db, `PRAGMA foreign_keys=ON`)
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, err = %v", fk, err)
	}
	for _, tc := range []struct {
		name, query    string
		args           []any
		pair, relation bool
	}{
		{"pair_delete", overlapPairDeleteSQL, []any{"a", "b", "b", "a"}, true, true},
		{"pair_update", overlapPairUpdateSQL, []any{1, "a", "b", "b", "a"}, true, false},
		{"work_delete", overlapWorkDeleteSQL, []any{"a", "a"}, false, true},
		{"work_update", overlapWorkUpdateSQL, []any{1, "a", "a"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := retirementUpgradeRows(t, db, `EXPLAIN QUERY PLAN `+tc.query, tc.args...)
			var forward, reverse, relation bool
			pairSearches := 0
			for _, row := range rows {
				detail, _ := row[3].(string)
				if strings.Contains(detail, "SCAN workflow_overlap_resolutions") || strings.Contains(detail, "SCAN relations") {
					t.Errorf("full scan: %s", detail)
				}
				forward = forward || strings.Contains(detail, "(from_work_id=?") && (!tc.pair || strings.Contains(detail, "to_work_id=?"))
				reverse = reverse || strings.Contains(detail, "(to_work_id=?") && (!tc.pair || strings.Contains(detail, "from_work_id=?"))
				if strings.Contains(detail, "SEARCH workflow_overlap_resolutions USING") && strings.Contains(detail, "from_work_id=?") && strings.Contains(detail, "to_work_id=?") {
					pairSearches++
				}
				relation = relation || strings.Contains(detail, "SEARCH relations USING") && strings.Contains(detail, "resolution_id=?")
			}
			if tc.pair && pairSearches < 2 || !tc.pair && (!forward || !reverse) || tc.relation && !relation {
				t.Errorf("missing indexed endpoint or relation search: %v", rows)
			}
		})
	}
}

func TestRelationIdentityCollisionIsInvariantViolation(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"a", "b", "c"} {
		seedWork(t, s, id)
	}
	va := readWorkVersion(t, s, "a")
	if err := applyWorkEvent(t, s, relationAddedEvent("one", "blocks", "a", "b", va, va+1), workVersion("a", va)); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, s.db, `UPDATE store_sequences SET last=0 WHERE name='relation'`)
	vb := readWorkVersion(t, s, "b")
	err := applyWorkEvent(t, s, relationAddedEvent("two", "blocks", "b", "c", vb, vb+1), workVersion("b", vb))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || !strings.Contains(err.Error(), "relations.id") {
		t.Fatalf("identity collision must report invariant violation: %v", err)
	}
	if strings.Contains(failure.RecoveryAction, "duplicate") || strings.Contains(failure.RecoveryAction, "self-edge") {
		t.Fatalf("misleading recovery: %+v", failure)
	}
}
