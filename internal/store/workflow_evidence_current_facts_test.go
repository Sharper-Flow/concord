package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"
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
	bound, err := oracleClosureProducerBoundTx(ctx, s.db, work, "evidence:gate-proof", "current-proof-producer")
	if err != nil || !bound {
		t.Fatalf("positive binding fixture: bound=%v err=%v", bound, err)
	}
	fields := json.RawMessage(`{"staleness_rule_id":"staleness:observed","observed_drift":{"severity":"warning","drifted":true}}`)
	if err := AppendWorkflowStalenessObservation(ctx, s, "evidence-current-observation", work, "actor:staleness", testManifestDigest, fields, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	definition := cloneWorkflowDefinition(BuiltinWorkflowDefinitions()[0])
	definition.StalenessRules = []WorkflowStalenessRule{
		{ID: "staleness:observed", InputRef: "input:observed", Severity: "warning"},
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
		"P25-oracle-producer-binding": func() (any, error) {
			return oracleClosureProducerBoundTx(ctx, s.db, work, "evidence:gate-proof", "current-proof-producer")
		},
		"P38-blocking-staleness": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) { return nil, verifyBlockingStaleness(ctx, tx, work, definition, nil) })
		},
		"P39-warning-staleness": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) { return workflowStalenessWarnings(ctx, tx, work, definition, nil) })
		},
		"P40-observations": func() (any, error) { return workflowStalenessObservationState(ctx, s.db, work) },
		"P53-evidence-inventory": func() (any, error) {
			return inTx(func(tx *sql.Tx) (any, error) { return workPinVerdictEvidenceTx(ctx, tx, work) })
		},
		"G24-tuple": func() (any, error) {
			return workflowEvidenceTupleBound(ctx, s.db, work, "verification", "evidence:gate-proof", 0)
		},
		"G24-kind": func() (any, error) { return workflowEvidenceKindBound(ctx, s.db, work, "verification", 0) },
		"G24-reference": func() (any, error) {
			return workflowEvidenceReferenceBound(ctx, s.db, work, "evidence:gate-proof", "workflow_action", 0)
		},
		"G24-verification-order": func() (any, error) { return workflowVerificationBindingsAfter(ctx, s.db, work, 0, "workflow_action") },
		"G24-requirements": func() (any, error) {
			return unboundWorkflowEvidenceRequirements(ctx, s.db, work, []workflowEvidenceRequirement{{Kind: "verification"}, {Reference: "evidence:gate-proof"}}, 0, 0)
		},
	})
}

func TestWorkflowEvidenceCurrentFactsMigrationBackfill(t *testing.T) {
	const work = "current-evidence-backfill"
	s, _ := seedCompletionGateCase(t, work, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	defer s.Close()
	ctx := context.Background()
	for i, drifted := range []bool{true, false} {
		fields, _ := json.Marshal(map[string]any{"staleness_rule_id": "staleness:current", "observed_drift": map[string]any{"severity": "warning", "drifted": drifted}})
		if err := AppendWorkflowStalenessObservation(ctx, s, []string{"old-observation", "new-observation"}[i], work, "actor:current", testManifestDigest, fields, time.Unix(int64(200-i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	queries := []string{
		`SELECT json_array(work_id,step_id,attempt_epoch,latest_start_seq,latest_start_event_id,latest_start_payload,latest_dispatch_start_seq,latest_dispatch_start_event_id,latest_dispatch_start_payload) FROM workflow_step_epochs ORDER BY work_id,step_id`,
		`SELECT json_array(work_id,action_id,exit_seq) FROM workflow_phase_exits ORDER BY work_id,action_id`,
		`SELECT json_array(work_id,step_id,action_id,production_seq,verdict_seq,verdict_production_seq) FROM workflow_artifact_actions ORDER BY work_id,step_id,action_id`,
		`SELECT json_array(work_id,contract_version,supersession_seq) FROM workflow_contracts ORDER BY work_id,contract_version`,
		`SELECT json_array(work_id,binding_seq,binding_id,actor,payload) FROM workflow_evidence_bindings ORDER BY work_id,binding_seq`,
		`SELECT json_array(o.work_id,o.rule_id,r.effect_seq,o.severity,o.drifted,o.accepted_inputs_digest,o.observed_at) FROM workflow_staleness_observations o JOIN effect_receipts r ON r.event_id=o.event_id ORDER BY o.work_id,o.rule_id,r.effect_seq`,
		`SELECT json_array(event_id,payload_digest) FROM effect_receipts WHERE subject_type='work_item' AND subject_id IN (SELECT id FROM work_items) ORDER BY effect_seq`,
	}
	want := workerFactsQueryRows(t, s, queries)
	if _, err := s.db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES
 ('removed-evidence','workflow.evidence_bound','work_item','removed-work','actor:removed','2026-10-01T00:00:00Z',1,'{}'),
 ('removed-observation','workflow.staleness_observed','work_item','removed-work','actor:removed','2026-10-01T00:00:00Z',1,'{"rule_id":"staleness:removed"}'),
 ('removed-completion','workflow.action_completed','work_item','removed-work','actor:removed','2026-10-01T00:00:00Z',1,'{"action_id":"confirm_premise"}')`); err != nil {
		t.Fatal(err)
	}
	repopulateWorkflowCurrentFactsForTest(t, s)
	if _, err := s.db.Exec(`ALTER TABLE domain_events RENAME TO domain_events_hidden`); err != nil {
		t.Fatal(err)
	}
	observations, err := workflowStalenessObservationState(ctx, s.db, work)
	if err != nil || len(observations) != 1 || observations["staleness:current"].Drifted || observations["staleness:current"].ObservedAt != time.Unix(199, 0).UTC().Format(time.RFC3339Nano) {
		t.Fatalf("latest observation must follow effect order, not wall clock: %+v, %v", observations, err)
	}
	if got := workerFactsQueryRows(t, s, queries); !reflect.DeepEqual(got, want) {
		t.Fatalf("migration changed current facts:\ngot %v\nwant %v", got, want)
	}
}

func repopulateWorkflowCurrentFactsForTest(t *testing.T, s *Store) {
	t.Helper()
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
DROP TABLE workflow_phase_exits;
DROP TABLE workflow_artifact_actions;
DROP TABLE workflow_action_obligations;
DROP TABLE workflow_worker_actors;
DROP TABLE workflow_step_completions;
DROP TABLE workflow_correction_state;
DROP TABLE workflow_predicate_verdicts;
DROP TABLE workflow_delivery_state;
DROP TABLE compaction_publications;
DROP TABLE workflow_evidence_bindings;
DROP TABLE workflow_staleness_observations;
DROP TABLE effect_receipts;
ALTER TABLE workflow_contracts DROP COLUMN supersession_seq;
ALTER TABLE workflow_contracts DROP COLUMN last_healthy_seq;
ALTER TABLE workflow_instances DROP COLUMN completion_payload;
ALTER TABLE workflow_instances DROP COLUMN completion_occurred_at;
ALTER TABLE workflow_instances DROP COLUMN unhealthy_verdict_seq;
ALTER TABLE work_observations DROP COLUMN recorded_seq;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_start_seq;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_start_event_id;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_start_payload;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_dispatch_start_seq;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_dispatch_start_event_id;
ALTER TABLE workflow_step_epochs DROP COLUMN latest_dispatch_start_payload;
`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.Version == 142 || m.Version == 145 || m.Version == 147 {
			if err := applyMigration(t.Context(), tx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := backfillEffectReceiptDigests(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
