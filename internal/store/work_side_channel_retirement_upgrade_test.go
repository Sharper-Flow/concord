package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func cutoverRetainedContractRows(t *testing.T, db *sql.DB) [][]any {
	t.Helper()
	return cutoverContractRows(t, db)
}

func TestWorkSideChannelRetirementUpgradeSnapshot(t *testing.T) {
	useStampedBuild(t)
	db, path := retirementUpgradeFixture(t)
	advanceHistoricalPrefix(t, db, 178)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state) VALUES('work-upgrade-from','workflow.implementation',?,'proposal','planned')`, "sha256:"+strings.Repeat("a", 64))
	retirementUpgradeExec(t, db, `INSERT INTO workflow_premise_confirmations(work_id,contract_version,confirmed_by,confirmed_at) SELECT work_id,contract_version,approved_by,approved_at FROM workflow_contracts WHERE work_id='work-upgrade-from'`)
	retirementUpgradeExec(t, db, `INSERT INTO work_messages(message_id,sender_work_id,recipient_work_id,body,state,sent_at) VALUES('msg:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','work-upgrade-from','work-upgrade-to','Retain the live message in the snapshot','sent',?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at,recorded_seq) VALUES('obs:aaaaaaaaaaaaaaaa','work-upgrade-from','Retain the observation','[]','[]',?,4)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO work_context_state(work_id,source_frontier,declaration_seq,declaration_id,declaration_payload) VALUES('work-upgrade-from',4,4,'context-upgrade','{"required_reading":[],"context_findings":[],"finding_refs":[]}')`)
	retirementUpgradeExec(t, db, `INSERT INTO work_context_findings(work_id,source_seq,ordinal,finding_class,finding) VALUES('work-upgrade-from',4,0,'generic','{"statement":"Retain the context finding"}')`)
	retirementUpgradeExec(t, db, `INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('research:upgrade','work-upgrade-from',1,'current',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES('research:upgrade',1,'Does research survive the upgrade?','[]','[]','[]','snapshot comparison',?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status) VALUES('research:upgrade',1,'finding:upgrade','conclusion','Keep the current research authority','high','current','active')`)
	retirementUpgradeExec(t, db, `INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES('research:upgrade',1,'work-upgrade-from','context',1,?)`, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,observation_id,verification_state) SELECT 'work-upgrade-from','run:upgrade','start','started','event:run-upgrade','client-upgrade',approved_by,'run:upgrade',?,'evidence:upgrade',?,?,?,'trusted_client_report','{}','freshness-upgrade','divergence-upgrade','xobs:upgrade','verified' FROM workflow_contracts WHERE work_id='work-upgrade-from'`, testManifestDigest, testManifestDigest, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	before := retirementUpgradeSnapshot(t, db)
	retained := retirementUpgradeSnapshot(t, db)
	ordinaryRecordsUpgradeExpectations(t, db, retained)
	for _, table := range []string{"work_messages", "work_observations", "work_context_state", "work_context_findings", "workflow_premise_confirmations", "workflow_native_runs", "active_research_packs", "active_research_revisions", "active_research_findings", "active_research_consumers"} {
		if len(before[table]) != 1 {
			t.Fatalf("predecessor witness %s has %d rows, want one", table, len(before[table]))
		}
	}
	snapshot := filepath.Join(t.TempDir(), "side-channel-predecessor.db")
	manifest, err := BackupDatabase(t.Context(), path, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackup(t.Context(), snapshot)
	if err != nil || verified.DBChecksum != manifest.DBChecksum || verified.SchemaVersion != 178 {
		t.Fatalf("verified predecessor = %+v, %v", verified, err)
	}
	contracts := cutoverContractRows(t, db)
	instances := cutoverInstanceExpectations(t, db, "")
	work := historicalCaptureWorkRows(t, db, "")
	if _, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot}); err != nil {
		t.Fatal(err)
	}
	retired := map[string]bool{"work_messages": true, "work_observations": true, "work_context_state": true, "work_context_findings": true, "project_governing_requirements": true, "workflow_law_baselines": true, "workflow_artifact_actions": true, "workflow_correction_state": true, "workflow_step_completions": true, "workflow_contract_predicates": true, "workflow_predicate_verdicts": true, "workflow_review_state": true, "workflow_context_checkpoints": true, "workflow_context_boundaries": true}
	for table, want := range retained {
		if retirementUpgradeResearchTable(t, db, table) {
			continue
		}
		if retired[table] {
			if rows := retirementUpgradeRows(t, db, `SELECT name FROM sqlite_schema WHERE name=? OR tbl_name=?`, table, table); len(rows) != 0 {
				t.Errorf("retired schema survives: %v", rows)
			}
			continue
		}
		if table == "schema" || table == "schema_migrations" || table == "store_revision" {
			continue
		}
		if table == "workflow_contracts" || table == "workflow_instances" {
			continue
		}
		if table == "work_items" {
			if got := retirementUpgradeRows(t, db, `SELECT * FROM work_items ORDER BY id`); !reflect.DeepEqual(got, work) {
				t.Errorf("retained work changed: got %v want %v", got, work)
			}
			continue
		}
		if got := retirementUpgradeRows(t, db, `SELECT * FROM "`+table+`" ORDER BY rowid`); !reflect.DeepEqual(got, want) {
			t.Errorf("unrelated current authority %s changed", table)
		}
	}
	if got := cutoverContractRows(t, db); !reflect.DeepEqual(got, contracts) {
		t.Errorf("unrelated current authority workflow_contracts changed")
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_instances ORDER BY rowid`); !reflect.DeepEqual(got, instances) {
		t.Errorf("retained workflow instance fields changed: before=%v after=%v", instances, got)
	}
	backup, err := sql.Open(driverName, readOnlyDataSource(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if got := retirementUpgradeSnapshot(t, backup); !reflect.DeepEqual(got, before) {
		t.Fatal("verified snapshot lost predecessor schema or rows")
	}
	retirementUpgradeHealthy(t, db)
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pin, err := ReadWorkPin(t.Context(), s, "work-upgrade-from")
	if err != nil {
		t.Fatalf("current work pin after retirement: %v", err)
	}
	if pin.Step != "intake" || len(pin.NextValidIntents) == 0 {
		t.Fatalf("undelivered work did not return to intake: %+v", pin)
	}
}
