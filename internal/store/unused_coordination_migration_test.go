package store

import (
	"path/filepath"
	"testing"
)

func TestUnusedCoordinationTablesAbsent(t *testing.T) {
	s := openTemp(t)
	for _, table := range []string{"work_conflict_applications", "work_size_overrides", "resource_claims", "workflow_action_obligations", "domain_project_attachment_edges", "domain_project_attachment_sets", "product_knowledge_sources", "law_cross_source_relations", "workflow_impact_notices", "workflow_external_conditions", "workflow_staleness_observations"} {
		var present bool
		if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name=?)`, table).Scan(&present); err != nil || present {
			t.Fatalf("%s present=%v err=%v", table, present, err)
		}
	}
}

func TestWorkflowCoordinationRetirementPreservesComposition(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "coordination-163.db"), 163)
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES
('source','task','Source','in_progress',0,7,'now','now'),('successor','task','Successor','needed',0,1,'now','now');
INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_model,result_disposition,unhealthy_verdict_seq,latest_non_dispatch_start_seq)
VALUES('source','workflow.implementation',?,'execution','awaiting_condition','test-model','deferred',11,12);
INSERT INTO workflow_impact_edges(work_id,edge_id,edge_kind,edge_class,target_work_id,target_kind,severity,recorded_at)
VALUES('source','edge:successor','forward_link','hard','successor','work_item','informational','now');
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest) VALUES
('observation',1,'workflow.staleness_observed','work_item','source','fixture',1,?),
('notice',2,'workflow.impact_notice_recorded','work_item','source','fixture',1,?),
('condition',3,'workflow.condition_added','work_item','source','fixture',1,?),
('successor',4,'workflow.successor_linked','work_item','source','fixture',1,?);
INSERT INTO workflow_staleness_observations(event_id,work_id,rule_id,severity,drifted,accepted_inputs_digest,observed_at)
VALUES('observation','source','rule:one','warning',1,?,'2026-10-29T00:00:00Z');
INSERT INTO workflow_external_conditions(work_id,condition_id,await_type,await_ref,resolution_authority,condition_state,recorded_at)
VALUES('source','condition:one','ci_result','run:one','durable_operation:fixture','open','now');
DELETE FROM write_guard`, testManifestDigest, testManifestDigest, testManifestDigest, testManifestDigest, testManifestDigest, testManifestDigest); err != nil {
		t.Fatal(err)
	}
	advanceHistoricalPrefix(t, db, 164)
	var state, model, disposition string
	var unhealthy, nonDispatch, version int
	if err := db.QueryRow(`SELECT instance_state,execution_model,result_disposition,unhealthy_verdict_seq,latest_non_dispatch_start_seq,(SELECT version FROM work_items WHERE id='source') FROM workflow_instances WHERE work_id='source'`).Scan(&state, &model, &disposition, &unhealthy, &nonDispatch, &version); err != nil || state != "blocked" || model != "test-model" || disposition != "deferred" || unhealthy != 11 || nonDispatch != 12 || version != 7 {
		t.Fatalf("retained instance=%s/%s/%s/%d/%d/v%d err=%v", state, model, disposition, unhealthy, nonDispatch, version, err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT instance_state,execution_model,unhealthy_verdict_seq,(SELECT version FROM work_items WHERE id='source') FROM workflow_instances WHERE work_id='source'`).Scan(&state, &model, &unhealthy, &version); err != nil || state != "blocked" || model != "test-model" || unhealthy != 11 || version != 7 {
		t.Fatalf("current instance=%s/%s/%d/v%d err=%v", state, model, unhealthy, version, err)
	}
	var edges, receipts, guards int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM workflow_impact_edges WHERE work_id='source' AND target_work_id='successor' AND edge_kind='forward_link'),(SELECT count(*) FROM effect_receipts),(SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND name IN ('workflow_instances_guard_insert','workflow_instances_guard_update','workflow_instances_guard_delete','workflow_law_baselines_completed'))`).Scan(&edges, &receipts, &guards); err != nil || edges != 1 || receipts != 1 || guards != 4 {
		t.Fatalf("retained edges=%d receipts=%d guards=%d err=%v", edges, receipts, guards, err)
	}
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='awaiting_condition' WHERE work_id='source'`); err == nil {
		t.Fatal("workflow instance admitted a state outside its current vocabulary")
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
