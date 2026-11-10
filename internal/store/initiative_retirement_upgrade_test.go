package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func initiativeRetirementFixture(t *testing.T) (*sql.DB, string) {
	t.Helper()
	useStampedBuild(t)
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 181)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json,narrative,urgency)
VALUES('initiative-old','initiative','Old initiative','needed',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','{}','','standard'),
('work-live','task','Live work','needed',0,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','{}','','standard');
INSERT INTO workflow_design_records(work_id,work_version,approach,decisions,touched_refs,recorded_at) VALUES('initiative-old',1,'Approach','[1]','[1]','time');
INSERT INTO workflow_proposal_records(work_id,work_version,problem,affected,stakes,user_outcomes,constraints,open_questions,recorded_at) VALUES('initiative-old',1,'Problem','[1]','Stakes','[1]','[]','[]','time');
INSERT INTO work_projects VALUES('initiative-old','project-upgrade','primary'),('work-live','project-upgrade','primary');
INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('initiative-old','work-live','blocks','2026-01-01T00:00:00Z');
INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('work-upgrade-from','work-live','blocks','2026-01-01T00:00:00Z');
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES('initiative-receipt',50,'work.created','work_item','initiative-old','operator',1,'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES('initiative-op',1,'initiative-old','workflow.test','digest','step','internal_sqlite','digest','{}','completed','{}','[]','[]','operator','request','t','t','digest');
INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,first_observed_at,last_observed_at) VALUES('operator','concord_work_transition','workflow_action','initiative-key','digest','initiative-op','[]','t','t');
DELETE FROM write_guard`)
	return db, path
}

func TestInitiativeRetirementSchema181Owners(t *testing.T) {
	db, _ := initiativeRetirementFixture(t)
	live := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`)
	tables := make(map[string]bool, len(live))
	for _, row := range live {
		tables[row[0].(string)] = true
	}
	for _, table := range initiativeRetirementProjectionTables {
		if !tables[table] {
			t.Errorf("schema 181 lacks cleanup table %s", table)
		}
	}
	owners := retirementUpgradeRows(t, db, `SELECT s.name, f."from", f.on_delete FROM sqlite_schema s, pragma_foreign_key_list(s.name) f WHERE s.type='table' AND f."table"='work_items' AND upper(f.on_delete)<>'CASCADE' ORDER BY s.name, f."from"`)
	separate := map[string]bool{
		"workflow_backlog_alignment.work_id": false, "workflow_backlog_alignment.related_work_id": false,
		"active_research_packs.owner_work_id": false, "active_research_consumers.consumer_work_id": false,
		"workflow_context_checkpoints.work_id": false, "workflow_context_boundaries.work_id": false,
	}
	for _, row := range owners {
		table, column := row[0].(string), row[1].(string)
		key := table + "." + column
		if _, ok := separate[key]; ok {
			separate[key] = true
			continue
		}
		if !slices.Contains(initiativeRetirementProjectionTables, table) {
			t.Errorf("schema 181 owner %s.%s is not cleared", table, column)
			continue
		}
		if columns, special := workRemovalProjectionOwnerColumns[table]; special {
			if !slices.Contains(columns, column) {
				t.Errorf("schema 181 owner %s.%s has no deletion predicate", table, column)
			}
		} else if column != "work_id" {
			t.Errorf("schema 181 owner %s.%s has no deletion predicate", table, column)
		}
	}
	for owner, present := range separate {
		if !present {
			t.Errorf("schema 181 lacks separately handled or refused owner %s", owner)
		}
	}
}

func TestInitiativeRetirementContextOwnerRefusesWithoutMutation(t *testing.T) {
	for _, tc := range []struct{ name, table, insert string }{
		{"checkpoint", "workflow_context_checkpoints", `INSERT INTO workflow_context_checkpoints(work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,workflow_definition_digest,actor_ref,request_id,recorded_at) SELECT 'initiative-old',1,1,'checkpoint','step',1,'unit','hypothesis','diagnosis','strategy','[1]','[1]','[]','[]','workflow.legacy',?,actor_ref,'request','time' FROM workflow_actors LIMIT 1`},
		{"boundary", "workflow_context_boundaries", `INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_digest,actor_ref,request_id,recorded_at) SELECT 'initiative-old',1,1,1,'boundary','summary','checkpoint',1,1,'summary','workflow.legacy',?,actor_ref,'request','time' FROM workflow_actors LIMIT 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := initiativeRetirementFixture(t)
			retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
			retirementUpgradeExec(t, db, tc.insert, testDigest("historical-context"))
			retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
			before := retirementUpgradeSnapshot(t, db)
			if len(before[tc.table]) != 1 {
				t.Fatalf("missing context fixture in %s", tc.table)
			}
			_, err := Upgrade(t.Context(), path, nil)
			if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				t.Fatalf("upgrade error = %v, want historical foreign-key refusal", err)
			}
			if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("context-owner refusal changed predecessor state")
			}
		})
	}
}

func TestInitiativeRetirementUpgrade(t *testing.T) {
	db, path := initiativeRetirementFixture(t)
	beforeLive := historicalCaptureWorkRows(t, db, `WHERE kind<>'initiative'`)
	beforeReceipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	beforeOperations := retirementUpgradeRows(t, db, `SELECT * FROM durable_operations ORDER BY op_id`)
	beforeIdempotency := retirementUpgradeRows(t, db, `SELECT * FROM idempotency_records ORDER BY idempotency_key`)
	if s, err := Open(t.Context(), path); err == nil {
		s.Close()
		t.Fatal("ordinary open applied a breaking retirement")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	snapshot := filepath.Join(t.TempDir(), "before.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("verify snapshot: %+v, %v", verified, err)
	}
	archived, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer archived.Close()
	if got := retirementUpgradeRows(t, archived, `SELECT id FROM work_items WHERE kind='initiative'`); len(got) != 1 {
		t.Fatalf("snapshot lacks initiative: %v", got)
	}
	if _, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT id,kind FROM work_items WHERE id IN ('initiative-old','work-live') ORDER BY id`); len(got) != 1 || got[0][0] != "work-live" {
		t.Fatalf("work after upgrade = %v; want only actionable work", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM work_items WHERE kind<>'initiative' ORDER BY id`); !reflect.DeepEqual(got, beforeLive) {
		t.Fatalf("live work changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, beforeReceipts) {
		t.Fatalf("receipts changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM durable_operations ORDER BY op_id`); !reflect.DeepEqual(got, beforeOperations) {
		t.Fatalf("operations changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM idempotency_records ORDER BY idempotency_key`); !reflect.DeepEqual(got, beforeIdempotency) {
		t.Fatalf("idempotency changed: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM relations WHERE work_id_from='initiative-old' OR work_id_to='initiative-old'`); len(got) != 0 {
		t.Fatalf("initiative edges remain: %v", got)
	}
	retirementUpgradeHealthy(t, db)
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	list, err := s.QueryQ3(t.Context(), Q3Request{Product: "product-upgrade"})
	if err != nil {
		t.Fatalf("read upgraded work: %v", err)
	}
	for _, item := range list.Items {
		if item.ID == "initiative-old" || item.Kind == "initiative" {
			t.Fatalf("retired initiative in readback: %+v", item)
		}
	}
	if len(list.Items) != 3 {
		t.Fatalf("list = %#v, want three live work items", list.Items)
	}
	ready, err := s.QueryQ3(t.Context(), Q3Request{Product: "product-upgrade", Project: "project-upgrade", Detail: "full"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, item := range ready.Items {
		if item.Ready {
			ids[item.ID] = true
		}
	}
	if len(ids) != 2 || !ids["work-upgrade-from"] || !ids["work-upgrade-to"] {
		t.Errorf("ready IDs = %v", ids)
	}
	blocked, err := s.QueryQ8(t.Context(), Q8Request{Work: "work-live", RelationKinds: []string{"blocked_by"}, Direction: "outgoing", Depth: 3})
	if err != nil || len(blocked.Edges) != 1 || blocked.Edges[0].Source != "work-live" || blocked.Edges[0].Target != "work-upgrade-from" {
		t.Errorf("blocked = %#v, %v", blocked, err)
	}
	productSnapshot, err := s.QueryQ2(t.Context(), Q2Request{Product: "product-upgrade"})
	if err != nil || productSnapshot.LifecycleCounts["needed"] != 3 || productSnapshot.DerivedCounts.Ready != 2 || productSnapshot.DerivedCounts.Blocked != 1 {
		t.Errorf("snapshot = %#v, %v", productSnapshot, err)
	}
	projectScope, err := s.QueryQ3(t.Context(), Q3Request{Product: "product-upgrade", Project: "project-upgrade", Detail: "full"})
	if err != nil || len(projectScope.Items) != 3 {
		t.Errorf("project scope = %#v, %v", projectScope, err)
	}
	if scope, err := s.QueryQ3(t.Context(), Q3Request{Product: "product-upgrade", WorkIDs: []string{"initiative-old"}, Detail: "full"}); err == nil && len(scope.Items) != 0 {
		t.Errorf("retired initiative remains in scope: %#v", scope)
	}
	if _, err := db.Exec(`INSERT INTO write_guard VALUES(1); UPDATE work_items SET kind='initiative' WHERE id='work-live'`); err == nil || !strings.Contains(err.Error(), "work_items kind is not a stored work kind") {
		t.Fatalf("retired kind registry error = %v", err)
	}
}

func TestInitiativeRetirementUpgradeProtectsDomainAdditions(t *testing.T) {
	db, path := initiativeRetirementFixture(t)
	snapshot := filepath.Join(t.TempDir(), "before.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatalf("upgrade predecessor with a retiring initiative: %v", err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT id FROM work_items WHERE id='initiative-old'`); len(got) != 0 {
		t.Fatalf("retired initiative remains: %v", got)
	}
	_, err := db.Exec(`INSERT INTO workflow_contract_domain_additions(work_id,contract_version,domain_id) VALUES('work-live',1,'new-domain')`)
	if err == nil || !strings.Contains(err.Error(), "workflow_contract_domain_additions is write-seam-only") {
		t.Fatalf("unguarded Domain addition after upgrade: %v", err)
	}
}

func TestInitiativeRetirementPartialFailureRollback(t *testing.T) {
	db, path := initiativeRetirementFixture(t)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json,narrative,urgency) VALUES('initiative-z','initiative','Second','needed',0,1,'t','t','{}','','standard');
INSERT INTO work_projects VALUES('initiative-z','project-upgrade','primary'); DELETE FROM write_guard;
CREATE TRIGGER fail_initiative_retirement BEFORE DELETE ON work_items WHEN OLD.id='initiative-z' BEGIN SELECT RAISE(ABORT, 'injected retirement failure'); END`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "initiative-z") {
		t.Fatalf("upgrade error = %v, want failed initiative identity", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("partially failed retirement changed the predecessor database")
	}
}

func TestInitiativeRetirementForeignOwnerRollback(t *testing.T) {
	db, path := initiativeRetirementFixture(t)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE work_items SET kind='initiative' WHERE id='work-upgrade-to'; DELETE FROM write_guard`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "workflow_overlap_resolutions") || !strings.Contains(err.Error(), "work-upgrade-to") || !strings.Contains(err.Error(), "work-upgrade-from") {
		t.Fatalf("upgrade error = %v, want named foreign-owned dependency", err)
	}
	assertFailureKind(t, err, KindUpgradeBlocked)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("foreign-owned dependency refusal changed the predecessor database")
	}
}
