package store

import (
	"context"
	"reflect"
	"testing"
)

func TestWorkflowEvidenceCurrentFactsWithoutLog(t *testing.T) {
	const work = "evidence-current-facts"
	f := seedWorkflowReturnRouteFixture(t, work, WorkflowStepPlanChange, "implement")
	s := f.store
	defer s.Close()
	ctx := context.Background()
	seedWorkflowAuthority(t, s, "current-proof-producer", work, "principal/current-proof", "request/current-proof", []string{"evidence:gate-proof"})
	version := readWorkVersion(t, s, work)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{workflowEvent("current-proof-binding", WorkflowEvidenceBound, work, map[string]any{
		"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"evidence_kind": "verification", "immutable_subject_ref": "evidence:gate-proof", "producer_id": "principal/current-proof",
		"producer_run_ref": "current-proof-producer", "producer_watermark": "request/current-proof", "observed_at": "2026-10-01T00:00:00Z",
	})}}); err != nil {
		t.Fatal(err)
	}
	readBinding := func() (any, error) {
		var count int
		err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.immutable_subject_ref')=? AND json_extract(payload,'$.producer_watermark')=?`, work, "evidence:gate-proof", "request/current-proof").Scan(&count)
		return count, err
	}
	bound, err := readBinding()
	if err != nil || bound != 1 {
		t.Fatalf("positive binding fixture: count=%v err=%v", bound, err)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"binding": readBinding,
	})
}

func TestWorkflowEvidenceCurrentFactsMigrationBackfill(t *testing.T) {
	const work = "current-evidence-backfill"
	f := seedWorkflowReturnRouteFixture(t, work, WorkflowStepPlanChange, "implement")
	s := f.store
	defer s.Close()
	queries := []string{
		`SELECT json_array(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload) FROM workflow_step_epochs ORDER BY work_id,step_id`,
		`SELECT json_array(work_id,action_id,exit_seq) FROM workflow_phase_exits ORDER BY work_id,action_id`,
		`SELECT json_array(work_id,contract_version,supersession_seq) FROM workflow_contracts ORDER BY work_id,contract_version`,
		`SELECT json_array(work_id,binding_seq,binding_id,actor,payload) FROM workflow_evidence_bindings ORDER BY work_id,binding_seq`,
		`SELECT json_array(event_id,payload_digest) FROM effect_receipts WHERE subject_type='work_item' AND subject_id IN (SELECT id FROM work_items) ORDER BY effect_seq`,
	}
	want := workerFactsQueryRows(t, s, queries)
	assertWorkflowCurrentFactsWithIndependentMigration142(t, s)
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration changed current facts:\ngot %v\nwant %v", got, want)
	}
}

func assertWorkflowCurrentFactsWithIndependentMigration142(t *testing.T, s *Store) {
	t.Helper()
	assertEventLogAbsent(t, s)
	assertEvidenceCorpus142(t)
}
