package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
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
INSERT INTO work_projects VALUES('initiative-old','project-upgrade','primary'),('work-live','project-upgrade','primary');
INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('initiative-old','work-live','blocks','2026-01-01T00:00:00Z');
INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('work-upgrade-from','work-live','blocks','2026-01-01T00:00:00Z');
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES('initiative-receipt',50,'work.created','work_item','initiative-old','operator',1,'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES('initiative-op',1,'initiative-old','workflow.test','digest','step','internal_sqlite','digest','{}','completed','{}','[]','[]','operator','request','t','t','digest');
INSERT INTO idempotency_records(principal_ref,tool,operation_kind,idempotency_key,canonical_digest,op_id,result_event_ids,first_observed_at,last_observed_at) VALUES('operator','concord_work_transition','workflow_action','initiative-key','digest','initiative-op','[]','t','t');
DELETE FROM write_guard`)
	return db, path
}

func TestInitiativeRetirementUpgrade(t *testing.T) {
	db, path := initiativeRetirementFixture(t)
	beforeLive := retirementUpgradeRows(t, db, `SELECT * FROM work_items WHERE kind<>'initiative' ORDER BY id`)
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
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
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
	ready, err := s.QueryQ5(t.Context(), Q5Request{Product: "product-upgrade", Project: "project-upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, item := range ready.Items {
		ids[item.ID] = true
	}
	if len(ids) != 2 || !ids["work-upgrade-from"] || !ids["work-upgrade-to"] {
		t.Errorf("ready IDs = %v", ids)
	}
	blocked, err := s.QueryQ4(t.Context(), Q4Request{Product: "product-upgrade", Project: "project-upgrade", Limit: 1, Depth: 3})
	if err != nil || len(blocked.Items) != 1 || blocked.Items[0].ID != "work-live" || len(blocked.Items[0].Blockers) != 1 || blocked.Items[0].Blockers[0].ID != "work-upgrade-from" || len(blocked.Items[0].Blockers[0].Blockers) != 0 || blocked.NextCursor != nil {
		t.Errorf("blocked = %#v, %v", blocked, err)
	}
	productSnapshot, err := s.QueryQ2(t.Context(), Q2Request{Product: "product-upgrade"})
	if err != nil || productSnapshot.LifecycleCounts["needed"] != 3 || productSnapshot.DerivedCounts.Ready != 2 || productSnapshot.DerivedCounts.Blocked != 1 {
		t.Errorf("snapshot = %#v, %v", productSnapshot, err)
	}
	projectScope, err := s.QueryQ6(t.Context(), Q6Request{Product: "product-upgrade", Project: "project-upgrade"})
	if err != nil || len(projectScope.Items) != 3 {
		t.Errorf("project scope = %#v, %v", projectScope, err)
	}
	if scope, err := s.QueryQ6(t.Context(), Q6Request{Product: "product-upgrade", Work: "initiative-old"}); err == nil {
		t.Errorf("retired initiative remains in scope: %#v", scope)
	}
	if _, err := db.Exec(`INSERT INTO write_guard VALUES(1); UPDATE work_items SET kind='initiative' WHERE id='work-live'`); err == nil || !strings.Contains(err.Error(), "work_items kind is not a stored work kind") {
		t.Fatalf("retired kind registry error = %v", err)
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

func TestInitiativeRetirementDependencyRollback(t *testing.T) {
	db, path := initiativeRetirementFixture(t)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('shared-pack','initiative-old',1,'current',1,'t','t'); DELETE FROM write_guard`)
	before := retirementUpgradeSnapshot(t, db)
	_, err := Upgrade(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "active_research_packs") || !strings.Contains(err.Error(), "initiative-old") {
		t.Fatalf("upgrade error = %v, want named research dependency", err)
	}
	assertFailureKind(t, err, KindUpgradeBlocked)
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("refused retirement changed the predecessor database")
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
