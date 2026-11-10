package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
)

func TestOrdinaryDiagnosisReplacementRollback(t *testing.T) {
	const work = "diagnosis-replacement-rollback"
	f := seedWorkflowReturnRouteFixture(t, work, WorkflowStepPlanFix, "diagnose")
	s := f.store
	if err := runWorkflowActionWithIdentity(t, s, work, "record_diagnosis", diagnosisPayload("first reproduction", "first cause"), 0, f.owner, "first"); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, work)
	if err := runVerdictActionAtVersion(t, s, work, "record_diagnosis", diagnosisPayload("stale reproduction", "stale cause"), version-1, f.owner); err == nil {
		t.Fatal("stale diagnosis was accepted")
	}
	if got := readWorkVersion(t, s, work); got != version {
		t.Fatalf("stale diagnosis changed version: %d -> %d", version, got)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_diagnosis_receipt BEFORE INSERT ON effect_receipts WHEN NEW.kind='workflow.diagnosis_recorded' BEGIN SELECT RAISE(ABORT, 'injected diagnosis failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := runWorkflowActionWithIdentity(t, s, work, "record_diagnosis", diagnosisPayload("second reproduction", "second cause"), 0, f.owner, "failure"); err == nil {
		t.Fatal("injected receipt failure was accepted")
	}
	if got := readWorkVersion(t, s, work); got != version || currentStep(t, s, work) != "diagnose" {
		t.Fatalf("failed diagnosis changed version or frontier: %d", got)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_diagnosis_receipt`); err != nil {
		t.Fatal(err)
	}
	if err := runWorkflowActionWithIdentity(t, s, work, "record_diagnosis", diagnosisPayload("second reproduction", "second cause"), 0, f.owner, "second"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowRecordActionOwnership(t *testing.T) {
	for _, action := range []string{"record_intake", "record_clarify", "record_plan", "record_diagnosis", "record_delivery", "record_review"} {
		if !workflowRecordAction(action) {
			t.Errorf("%s must own a current record", action)
		}
	}
	for _, action := range []string{"record_proposal", "record_design", "record_alignment", "record_finding", "record_conclusion", "record_report", "record_decision", "record_research", "record_option"} {
		if workflowRecordAction(action) {
			t.Errorf("%s is not a current-record owner", action)
		}
	}
}

func TestWorkflowCurrentRecordsIncludesPlanAndPrelude(t *testing.T) {
	const work = "current-plan-and-prelude"
	f := seedWorkflowReturnRouteFixture(t, work, WorkflowStepPlanChange, "implement")
	ctx := context.Background()
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	scope, err := beginWrite(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	prelude := workflowPreludeState{Intake: workflowIntakeRecord{Outcome: WorkflowIntakeProceed}}
	if err := writeWorkflowPreludeTx(ctx, tx, work, prelude); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_plan_records(work_id,work_version,payload,recorded_at,actor,event_ref) SELECT ?,1,'{}','now',actor_ref,'event:test' FROM workflow_actors LIMIT 1`, work); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_clarify_records(work_id,work_version,payload) VALUES(?,1,'{"resolved_questions":["question"],"note":"resolved"}')`, work); err != nil {
		t.Fatal(err)
	}
	records, err := workflowCurrentRecords(ctx, tx, work)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"record_intake", "record_clarify", "record_plan"} {
		if !records[action] {
			t.Errorf("missing %s from current records: %v", action, records)
		}
	}
	if err := scope.close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryDeliveryWritesHoldProgress(t *testing.T) {
	for _, variant := range []string{"passed_step", "completed", "migrated"} {
		completed := variant != "passed_step"
		name := "passed_step"
		if completed {
			name = variant
		}
		t.Run(name, func(t *testing.T) {
			work := "ordinary-delivery-" + name
			f := qCurrentAcceptanceWithDelivery(t, work, "file:internal/store/output.go")
			s := f.store
			ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			if completed {
				if err := f.run(ctx, "confirm_premise", "confirm", readWorkVersion(t, s, work), nil, nil); err != nil {
					t.Fatal(err)
				}
				completeWorkflowPlanFixture(t, s, work)
				if err := f.run(ctx, "complete", "complete", readWorkVersion(t, s, work), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
					t.Fatal(err)
				}
				if variant == "migrated" {
					if _, err := s.db.ExecContext(ctx, `INSERT INTO write_guard VALUES(1); UPDATE workflow_instances SET workflow_type=NULL, step_plan=NULL WHERE work_id=?; DELETE FROM write_guard`, work); err != nil {
						t.Fatal(err)
					}
				}
			}
			var plan sql.NullString
			var state, lifecycle string
			if err := s.db.QueryRow(`SELECT step_plan,instance_state,(SELECT lifecycle FROM work_items WHERE id=work_id) FROM workflow_instances WHERE work_id=?`, work).Scan(&plan, &state, &lifecycle); err != nil {
				t.Fatal(err)
			}
			for _, artifact := range []string{"https://github.com/Sharper-Flow/concord/pull/100", "https://github.com/Sharper-Flow/concord/pull/101"} {
				payload, _ := json.Marshal(map[string]any{"delivery_artifact": artifact, "delivery_state": "asserted"})
				version := readWorkVersion(t, s, work)
				if err := f.run(ctx, "record_delivery", artifact, version, payload, nil); err != nil {
					t.Fatalf("ordinary delivery write: %v", err)
				}
				err := f.run(ctx, "record_delivery", "stale-"+artifact, version, payload, nil)
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindVersionConflict || !reflect.DeepEqual(failure.InterveningActions, []InterveningAction{{ActionID: "record_delivery", SessionRef: f.owner.SessionRef}}) {
					t.Errorf("stale delivery diagnostic: %v (%+v)", err, failure)
				}
				var gotPlan sql.NullString
				var gotState, gotLifecycle string
				if err := s.db.QueryRow(`SELECT step_plan,instance_state,(SELECT lifecycle FROM work_items WHERE id=work_id) FROM workflow_instances WHERE work_id=?`, work).Scan(&gotPlan, &gotState, &gotLifecycle); err != nil {
					t.Fatal(err)
				}
				if gotPlan != plan || gotState != state || gotLifecycle != lifecycle {
					t.Fatalf("record write changed plan or lifecycle: %s/%s -> %s/%s", state, lifecycle, gotState, gotLifecycle)
				}
				var current string
				if err := s.db.QueryRowContext(ctx, `SELECT json_extract(payload,'$.delivery_artifact') FROM workflow_delivery_state WHERE work_id=?`, work).Scan(&current); err != nil || current != artifact {
					t.Fatalf("delivery=%q error=%v", current, err)
				}
			}
			if variant == "migrated" {
				if _, err := s.db.ExecContext(ctx, `INSERT INTO write_guard VALUES(1); DELETE FROM workflow_delivery_state WHERE work_id=?; DELETE FROM write_guard`, work); err != nil {
					t.Fatal(err)
				}
				request := WorkflowActionPreflightRequest{WorkID: work, ExpectedVersion: readWorkVersion(t, s, work), ActionID: "record_delivery", Payload: json.RawMessage(`{"delivery_artifact":"artifact:missing","delivery_state":"asserted"}`), Actor: f.owner}
				if _, err := WorkflowActionDefinitionFor(ctx, s, request); !hasFailureKind(err, KindInvalidOperation) {
					t.Fatalf("replacement without a delivery row: %v", err)
				}
			}
		})
	}
}

func TestOrdinaryRecordReplacementKeepsPlan(t *testing.T) {
	const work = "ordinary-record-plan"
	f := seedWorkflowReturnRouteFixture(t, work, WorkflowStepPlanFix, "diagnose")
	s := f.store
	before, err := readWorkflowPlanTx(t.Context(), s.db, work)
	if err != nil || before == nil {
		t.Fatalf("read plan before diagnosis: %v", err)
	}
	for _, reproduction := range []string{"first reproduction", "replacement reproduction"} {
		if err := runVerdictActionAs(t, s, work, "record_diagnosis", diagnosisPayload(reproduction, "The cause"), 0, f.owner); err != nil {
			t.Fatalf("diagnosis %s: %v", reproduction, err)
		}
	}
	after, err := readWorkflowPlanTx(t.Context(), s.db, work)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("diagnosis changed plan: before=%+v after=%+v err=%v", before, after, err)
	}
	if step := currentStep(t, s, work); step != "diagnose" {
		t.Fatalf("diagnosis moved frontier to %q", step)
	}
}
