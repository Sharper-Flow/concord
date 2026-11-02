package store

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

func TestWorkflowEvidenceCurrentFactsWithoutLog(t *testing.T) {
	const work = "evidence-current-facts"
	s, _ := seedCompletionGateCase(t, work, completionGateCase{requiredEvidence: []string{"verification", "review"}})
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
	bound, err := workflowEvidenceTupleBound(ctx, s.db, work, "verification", "evidence:gate-proof")
	if err != nil || !bound {
		t.Fatalf("positive binding fixture: bound=%v err=%v", bound, err)
	}
	definition := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	definition.StalenessRules = []WorkflowStalenessRule{
		{ID: "staleness:missing", InputRef: "input:missing", Severity: "warning"},
		{ID: "staleness:bound", InputRef: "evidence:gate-proof", Severity: "warning"},
	}
	inTx := func(read func(*sql.Tx) (any, error)) (any, error) {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		return read(tx)
	}
	assertWorkerFactsLogBlind(t, s, map[string]func() (any, error){
		"P38-blocking-staleness": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) { return nil, verifyBlockingStaleness(ctx, tx, work, definition, nil) })
		},
		"P39-warning-staleness": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) { return workflowStalenessWarnings(ctx, tx, work, definition, nil) })
		},
		"P53-evidence-inventory": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) { return workPinVerdictEvidenceTx(ctx, tx, work) })
		},
		"G24-tuple": func() (any, error) {
			return workflowEvidenceTupleBound(ctx, s.db, work, "verification", "evidence:gate-proof")
		},
		"G24-kind": func() (any, error) { return workflowEvidenceKindBound(ctx, s.db, work, "verification") },
		"G24-reference": func() (any, error) {
			return workflowEvidenceReferenceBound(ctx, s.db, work, "evidence:gate-proof", "workflow_action", 0)
		},
		"G24-verification-order": func() (any, error) { return workflowVerificationBindingsAfter(ctx, s.db, work, 0, "workflow_action") },
		"G24-requirements": func() (any, error) {
			return unboundWorkflowEvidenceRequirements(ctx, s.db, work, []workflowEvidenceRequirement{{Kind: "verification"}, {Reference: "evidence:gate-proof"}}, 0)
		},
	})
}

func TestWorkflowEvidenceCurrentFactsMigrationBackfill(t *testing.T) {
	const work = "current-evidence-backfill"
	s, _ := seedCompletionGateCase(t, work, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	defer s.Close()
	queries := []string{
		`SELECT json_array(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload) FROM workflow_step_epochs ORDER BY work_id,step_id`,
		`SELECT json_array(work_id,action_id,exit_seq) FROM workflow_phase_exits ORDER BY work_id,action_id`,
		`SELECT json_array(work_id,step_id,action_id,production_seq,verdict_seq,verdict_production_seq) FROM workflow_artifact_actions ORDER BY work_id,step_id,action_id`,
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
