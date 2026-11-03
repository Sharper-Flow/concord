package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The current reader's columns are compared on both sides of destructive upgrades.
const retainedWorkflowContractColumns = `work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,superseded_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,self_repair_json,definition_ref,definition_digest,supersession_seq,last_healthy_seq,decision_domains`

func retainedWorkflowContractRows(t *testing.T, db *sql.DB) [][]any {
	t.Helper()
	return retirementUpgradeRows(t, db, `SELECT `+retainedWorkflowContractColumns+` FROM workflow_contracts ORDER BY rowid`)
}

func TestLawAttributionStateRetired(t *testing.T) {
	s := openTemp(t)
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name='workflow_law_baselines'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("obsolete baseline table count=%d error=%v", count, err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('workflow_contracts') WHERE name='law_change'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("obsolete law_change column count=%d error=%v", count, err)
	}
}

func TestLawAttributionVerifiedSnapshotUpgrade(t *testing.T) {
	useStampedBuild(t)
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 178)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `UPDATE workflow_contracts SET law_change='{ "reason": "Retired carrier", "law_ids": ["old-law"] }' WHERE work_id='work-upgrade-from'`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_law_baselines(work_id,baseline_json,start_effect_seq) VALUES('work-upgrade-from','{"version":1,"laws":[{"law_id":"old-law"}]}',0)`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-upgrade-from',1,'predicate:retained',0,'check','{"kind":"check","check_ref":"check:upgrade","immutable_subject_ref":"commit:upgrade","expected_result":"pass"}')`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES('work-upgrade-from',24,'binding:retained','operator','{ "immutable_subject_ref": "artifact:retained", "evidence_kind": "artifact" }')`)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	before := retirementUpgradeSnapshot(t, db)
	retained := retirementUpgradeSnapshot(t, db)
	ordinaryRecordsUpgradeExpectations(t, db, retained)
	contracts := retainedWorkflowContractRows(t, db)
	if s, err := Open(t.Context(), path); err == nil {
		s.Close()
		t.Fatal("ordinary open crossed the breaking law-attribution retirement")
	} else {
		assertFailureKind(t, err, KindUpgradeRequired)
	}
	if after := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(after, before) {
		t.Fatal("refused open changed predecessor state")
	}
	snapshot := filepath.Join(t.TempDir(), "pre-law-attribution.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := VerifyBackup(t.Context(), snapshot); err != nil || verified.SchemaVersion != 178 || verified.DBChecksum != manifest.DBChecksum {
		t.Fatalf("verified predecessor=%+v error=%v", verified, err)
	}
	report, err := Upgrade(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var wantApplied []int
	for _, m := range migrations {
		if m.Version > 178 {
			wantApplied = append(wantApplied, m.Version)
		}
	}
	if !reflect.DeepEqual(report.Applied, wantApplied) {
		t.Fatalf("applied=%v, want %v", report.Applied, wantApplied)
	}
	for table, expected := range retained {
		switch table {
		case "schema", "schema_migrations", "store_revision", "workflow_law_baselines", "work_messages", "work_observations", "workflow_correction_state", "workflow_artifact_actions":
			continue
		case "workflow_contracts":
			if got := retainedWorkflowContractRows(t, db); !reflect.DeepEqual(got, contracts) {
				t.Errorf("current contract facts changed: got %v, want %v", got, contracts)
			}
			continue
		}
		if got := retirementUpgradeRows(t, db, `SELECT * FROM "`+table+`" ORDER BY rowid`); !reflect.DeepEqual(got, expected) {
			t.Errorf("unrelated %s changed", table)
		}
	}
	if got := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name='workflow_law_baselines' OR tbl_name='workflow_law_baselines'`); len(got) != 0 {
		t.Fatalf("retired baseline schema survives: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT name FROM pragma_table_info('workflow_contracts') WHERE name='law_change'`); len(got) != 0 {
		t.Fatalf("retired carrier survives: %v", got)
	}
	contract, err := workflowCompletionContractRows(t.Context(), db, "work-upgrade-from")
	if err != nil || contract.Version != 1 || len(contract.Predicates) != 1 || contract.Predicates[0].PredicateID != "predicate:retained" {
		t.Fatalf("retained contract is unreadable: %+v error=%v", contract, err)
	}
	backup, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if got := retirementUpgradeSnapshot(t, backup); !reflect.DeepEqual(got, before) {
		t.Fatal("verified predecessor lost schema or bytes")
	}
	retirementUpgradeHealthy(t, db)
	if got := retirementUpgradeRows(t, db, `PRAGMA integrity_check`); !reflect.DeepEqual(got, [][]any{{"ok"}}) {
		t.Fatalf("integrity_check=%v", got)
	}
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("idempotent upgrade: %v", err)
	}
}

func TestLawAttributionMigrationRollback(t *testing.T) {
	useStampedBuild(t)
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 178)
	// SQLite cannot drop a column still referenced by a schema object. The
	// failure follows DROP TABLE and must roll that deletion back atomically.
	retirementUpgradeExec(t, db, `CREATE INDEX law_attribution_rollback ON workflow_contracts(law_change)`)
	before := retirementUpgradeSnapshot(t, db)
	if _, err := Upgrade(t.Context(), path, nil); err == nil || !strings.Contains(err.Error(), "law_attribution_rollback") {
		t.Fatalf("upgrade did not reach the destructive rollback witness: %v", err)
	}
	if got := retirementUpgradeSnapshot(t, db); !reflect.DeepEqual(got, before) {
		t.Fatal("failed migration changed schema, rows, guards or manifest")
	}
	retirementUpgradeExec(t, db, `DROP INDEX law_attribution_rollback`)
	if _, err := Upgrade(t.Context(), path, nil); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	retirementUpgradeHealthy(t, db)
}
