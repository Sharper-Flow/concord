package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func initiativeRetirementAlignmentFixture(t *testing.T, lifecycle, instance string, survivingMatch bool) (*sql.DB, string) {
	t.Helper()
	db, path := initiativeRetirementFixture(t)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1); UPDATE work_items SET lifecycle=? WHERE id='work-live'`, lifecycle)
	actor := "actor:" + strings.Repeat("a", 64)
	for _, related := range []any{nil, "initiative-old"} {
		retirementUpgradeExec(t, db, `INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by) VALUES('work-live',?,'Checked work and prior direction.','related_found',?,?)`, related, retirementUpgradeTime, actor)
	}
	if survivingMatch {
		for _, related := range []string{"work-upgrade-from", "work-upgrade-to"} {
			retirementUpgradeExec(t, db, `INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by) VALUES('work-live',?,'Checked work and prior direction.','related_found',?,?)`, related, retirementUpgradeTime, actor)
		}
	}
	if instance != "" {
		retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state) VALUES('work-live','workflow.implementation',?,'acceptance',?)`, testDigest("predecessor-definition"), instance)
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	return db, path
}

func TestInitiativeRetirementTerminalAlignment(t *testing.T) {
	for _, test := range []struct{ lifecycle, instance string }{
		{"completed", "cancelled"},
		{"cancelled", "completed"},
		{"superseded", "superseded"},
		{"completed", ""},
	} {
		t.Run(test.lifecycle+"_"+test.instance, func(t *testing.T) {
			db, path := initiativeRetirementAlignmentFixture(t, test.lifecycle, test.instance, true)
			before := retirementUpgradeSnapshot(t, db)
			beforeWork := retirementUpgradeRows(t, db, `SELECT * FROM work_items WHERE kind<>'initiative' ORDER BY rowid`)
			alignment := retirementUpgradeRows(t, db, `SELECT * FROM workflow_backlog_alignment WHERE related_work_id IS NULL OR related_work_id<>'initiative-old' ORDER BY related_work_id`)
			snapshot := filepath.Join(t.TempDir(), "before.db")
			if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
				t.Fatalf("candidate cannot verify its pre-upgrade snapshot: %v", err)
			}
			archived, err := sql.Open(driverName, readOnlyDataSource(snapshot))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = archived.Close() })
			if got := retirementUpgradeRows(t, archived, `SELECT * FROM workflow_backlog_alignment ORDER BY rowid`); !reflect.DeepEqual(got, before["workflow_backlog_alignment"]) {
				t.Fatal("predecessor snapshot changed the original alignment")
			}
			if _, err := Upgrade(t.Context(), path, nil); err != nil {
				t.Fatalf("terminal alignment blocks retirement: %v", err)
			}
			for _, table := range []string{"effect_receipts", "durable_operations", "idempotency_records", "workflow_instances"} {
				if got := retirementUpgradeRows(t, db, `SELECT * FROM "`+table+`" ORDER BY rowid`); !reflect.DeepEqual(got, before[table]) {
					t.Errorf("%s changed", table)
				}
			}
			if got := retirementUpgradeRows(t, db, `SELECT * FROM work_items WHERE kind<>'initiative' ORDER BY rowid`); !reflect.DeepEqual(got, beforeWork) {
				t.Error("surviving work changed")
			}
			if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_backlog_alignment ORDER BY related_work_id`); !reflect.DeepEqual(got, alignment) {
				t.Error("search declaration or surviving matches changed")
			}
			projection, err := readWorkflowBacklogAlignment(t.Context(), db, "work-live")
			if err != nil || projection == nil || projection.Outcome != "related_found" || len(projection.RelatedIDs) != 2 {
				t.Fatalf("alignment readback = %+v, %v", projection, err)
			}
			retirementUpgradeHealthy(t, db)
			restored := filepath.Join(t.TempDir(), "restored.db")
			if _, err := RestoreBackup(t.Context(), snapshot, restored); err != nil {
				t.Fatalf("candidate cannot restore its predecessor snapshot: %v", err)
			}
		})
	}
}

func TestInitiativeRetirementAlignmentRefusal(t *testing.T) {
	for _, test := range []struct {
		name, lifecycle, instance string
		surviving                 bool
	}{
		{"needed_owner", "needed", "", true},
		{"active_owner", "in_progress", "running", true},
		{"terminal_owner_planned_instance", "completed", "planned", true},
		{"terminal_owner_ready_instance", "completed", "ready", true},
		{"terminal_owner_running_instance", "completed", "running", true},
		{"terminal_owner_blocked_instance", "completed", "blocked", true},
		{"terminal_owner_verifying_instance", "completed", "verifying", true},
		{"sole_initiative_match", "completed", "cancelled", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, path := initiativeRetirementAlignmentFixture(t, test.lifecycle, test.instance, test.surviving)
			before := retirementUpgradeSnapshot(t, db)
			_, err := Upgrade(t.Context(), path, nil)
			if err == nil || !strings.Contains(err.Error(), "workflow_backlog_alignment") || !strings.Contains(err.Error(), "work-live") {
				t.Fatalf("upgrade error = %v, want named alignment refusal", err)
			}
			assertFailureKind(t, err, KindUpgradeBlocked)
			if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
				t.Fatal("alignment refusal changed predecessor state")
			}
		})
	}
}

func TestInitiativeRetirementTerminalAlignmentRollback(t *testing.T) {
	db, _ := initiativeRetirementAlignmentFixture(t, "completed", "cancelled", true)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,intent_json,narrative,urgency) VALUES('initiative-z','initiative','Second','needed',0,1,'t','t','{}','','standard');
DELETE FROM write_guard;
CREATE TRIGGER fail_terminal_alignment_retirement BEFORE DELETE ON work_items WHEN OLD.id='initiative-z' BEGIN SELECT RAISE(ABORT, 'injected retirement failure'); END`)
	before := retirementUpgradeSnapshot(t, db)
	if err := Migrate(t.Context(), db); err == nil || !strings.Contains(err.Error(), "initiative-z") {
		t.Fatalf("migration error = %v, want failure after allowed match retirement", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed retirement changed the original alignment or predecessor state")
	}
}
