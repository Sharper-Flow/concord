package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestOrdinaryAlignmentReplacement(t *testing.T) {
	for _, tc := range []struct{ kind, action string }{{"task", "record_alignment"}, {"bug", "record_diagnosis"}} {
		t.Run(tc.action, func(t *testing.T) {
			work := "replacement-" + tc.kind
			s, version := seedBootstrapCapturedWork(t, work, tc.kind)
			seedWorkflowLaw(t, s)
			for _, id := range []string{"related-a", "related-b", "related-c"} {
				seedWork(t, s, id)
			}
			actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + work, ActorClass: ActorAgent}
			if tc.kind == "task" {
				version = issue31WorkflowActionWithPayload(t, s, work, version, "record_proposal", work+"-proposal", actor, json.RawMessage(`{"problem":"A problem","affected":["People"],"stakes":"The stakes","user_outcomes":["An outcome"]}`))
			}
			payload := func(search, outcome string, ids []string) json.RawMessage {
				fields := map[string]any{"searched": search, "outcome": outcome}
				if ids != nil {
					fields["related_ids"] = ids
				}
				if tc.kind == "bug" {
					fields["reproduction"] = "The reproduction"
					fields["root_cause"] = "The cause"
				}
				data, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			apply := func(key string, v int64, p json.RawMessage) error {
				ctx, err := s.PrepareWorkflowActionProof(context.Background(), tc.action, work)
				if err != nil {
					return err
				}
				tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				_, err = applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{WorkID: work, ExpectedVersion: v, ActionID: tc.action, Payload: p, Actor: actor, AcceptedInputsDigest: "sha256:replacement", IdempotencyIdentity: key, OperationID: key, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key, RequestID: "request:" + key, ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)})
				if err != nil {
					_ = tx.Rollback()
					return err
				}
				return tx.Commit()
			}
			assertRows := func(search, outcome string, ids []string) {
				t.Helper()
				rows := diagnosisAlignmentRows(t, s, work)
				var got []string
				for _, row := range rows {
					if row.Searched != search || row.Outcome != outcome {
						t.Fatalf("unexpected row: %+v", row)
					}
					got = append(got, row.Related)
				}
				want := append([]string{""}, ids...)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("rows=%v want=%v", got, want)
				}
				read, err := readWorkflowBacklogAlignment(context.Background(), s.db, work)
				if err != nil || read == nil || read.Searched != search || read.Outcome != outcome || !reflect.DeepEqual(read.RelatedIDs, append([]string{}, ids...)) {
					t.Fatalf("alignment read=%+v err=%v", read, err)
				}
			}
			if err := apply(work+"-first", version, payload("first search", "related_found", []string{"related-a", "related-b"})); err != nil {
				t.Fatal(err)
			}
			step := currentStep(t, s, work)
			version = readWorkVersion(t, s, work)
			if err := apply(work+"-second", version, payload("second search", "related_found", []string{"related-b", "related-c"})); err != nil {
				t.Fatalf("replacement: %v", err)
			}
			if got := currentStep(t, s, work); got != step {
				t.Fatalf("progress moved: %s -> %s", step, got)
			}
			assertRows("second search", "related_found", []string{"related-b", "related-c"})
			version = readWorkVersion(t, s, work)
			for _, bad := range []struct {
				key string
				v   int64
				p   json.RawMessage
			}{{"invalid", version, payload("invalid search", "none_found", []string{"related-a"})}, {"stale", version - 1, payload("stale search", "none_found", nil)}, {"missing", version, payload("missing search", "related_found", []string{"missing-related"})}} {
				if err := apply(work+"-"+bad.key, bad.v, bad.p); err == nil {
					t.Fatalf("%s accepted", bad.key)
				}
				if got := readWorkVersion(t, s, work); got != version {
					t.Fatalf("%s changed version %d -> %d", bad.key, version, got)
				}
				assertRows("second search", "related_found", []string{"related-b", "related-c"})
			}
			if _, err := s.db.Exec(`CREATE TRIGGER fail_alignment_replacement BEFORE INSERT ON workflow_backlog_alignment WHEN NEW.related_work_id='related-c' BEGIN SELECT RAISE(ABORT, 'injected replacement failure'); END`); err != nil {
				t.Fatal(err)
			}
			if err := apply(work+"-rollback", version, payload("failed search", "related_found", []string{"related-a", "related-c"})); err == nil {
				t.Fatal("injected insert failure was accepted")
			}
			if _, err := s.db.Exec(`DROP TRIGGER fail_alignment_replacement`); err != nil {
				t.Fatal(err)
			}
			if got := readWorkVersion(t, s, work); got != version {
				t.Fatalf("failed replacement changed version %d -> %d", version, got)
			}
			assertRows("second search", "related_found", []string{"related-b", "related-c"})
			if err := apply(work+"-empty", version, payload("empty search", "none_found", nil)); err != nil {
				t.Fatal(err)
			}
			assertRows("empty search", "none_found", nil)
		})
	}
}

func TestContractStepAdmitsOnlyHeldOffStepRecord(t *testing.T) {
	entry, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	definition := entry.Definition
	state := WorkflowAdmissionState{Step: "planning", CurrentRecords: map[string]bool{"record_alignment": true}}
	var decision WorkflowAdmissionDecision
	if workflowAdmitStepRefused(definition, state, "record_alignment", &decision) {
		t.Fatalf("held off-step record refused: %v", decision.Failure)
	}
	for i := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[i].ID == "planning" {
			definition.StepGraph.Steps[i].Actions = append(definition.StepGraph.Steps[i].Actions, "record_alignment")
		}
	}
	decision = WorkflowAdmissionDecision{}
	if !workflowAdmitStepRefused(definition, state, "record_alignment", &decision) || decision.Failure == nil {
		t.Fatal("declared advancing action skipped contract approval")
	}
}

func TestWorkflowRecordActionOwnership(t *testing.T) {
	for _, action := range []string{"record_finding", "record_conclusion", "record_report", "record_decision", "record_research", "record_option"} {
		if workflowRecordAction(action) {
			t.Errorf("%s is not a current-record owner", action)
		}
	}
	if !workflowRecordAction("revise_candidates") {
		t.Error("candidate delta must remain a current-record action")
	}
}

func TestOrdinaryCandidateDeltaHoldsProgress(t *testing.T) {
	const work = "ordinary-candidate-delta"
	f := seedCurrentWorkflowReturnRouteFixture(t, work, "workflow.research", "investigate")
	s := f.store
	version := readWorkVersion(t, s, work)
	version = issue31WorkflowActionWithPayload(t, s, work, version, "revise_candidates", work+"-first", f.owner, json.RawMessage(`{"added":["candidate:old","candidate:keep"]}`))
	step := currentStep(t, s, work)
	if step == "investigate" {
		t.Fatal("first candidate action did not pass its step")
	}
	issue31WorkflowActionWithPayload(t, s, work, version, "revise_candidates", work+"-second", f.owner, json.RawMessage(`{"added":["candidate:new"],"removed":["candidate:old"]}`))
	if got := currentStep(t, s, work); got != step {
		t.Fatalf("candidate delta moved progress: %s -> %s", step, got)
	}
	out, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: work})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"candidate:keep", "candidate:new"}; !reflect.DeepEqual(out.CandidateIDs, want) {
		t.Fatalf("candidates=%v want=%v", out.CandidateIDs, want)
	}
}

func TestOrdinaryDeliveryWritesHoldProgress(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "passed_step"
		if completed {
			name = "completed"
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
				if err := f.run(ctx, "complete", "complete", readWorkVersion(t, s, work), json.RawMessage(`{"impact_verdict":"non-breaking"}`), nil); err != nil {
					t.Fatal(err)
				}
			}
			var step, state, lifecycle string
			if err := s.db.QueryRow(`SELECT current_step,instance_state,(SELECT lifecycle FROM work_items WHERE id=work_id) FROM workflow_instances WHERE work_id=?`, work).Scan(&step, &state, &lifecycle); err != nil {
				t.Fatal(err)
			}
			for _, artifact := range []string{"https://github.com/Sharper-Flow/concord/pull/100", "https://github.com/Sharper-Flow/concord/pull/101"} {
				payload, _ := json.Marshal(map[string]any{"delivery_artifact": artifact, "delivery_state": "asserted"})
				version := readWorkVersion(t, s, work)
				if err := f.run(ctx, "record_delivery", artifact, version, payload, nil); err != nil {
					t.Fatalf("ordinary delivery write: %v", err)
				}
				if completed {
					err := f.run(ctx, "record_delivery", "stale-"+artifact, version, payload, nil)
					var failure *Failure
					if !failureAs(err, &failure) || failure.Kind != KindVersionConflict || !reflect.DeepEqual(failure.InterveningActions, []InterveningAction{{ActionID: "record_delivery", SessionRef: f.owner.SessionRef}}) {
						t.Errorf("stale delivery diagnostic: %v (%+v)", err, failure)
					}
				}
				var gotStep, gotState, gotLifecycle string
				if err := s.db.QueryRow(`SELECT current_step,instance_state,(SELECT lifecycle FROM work_items WHERE id=work_id) FROM workflow_instances WHERE work_id=?`, work).Scan(&gotStep, &gotState, &gotLifecycle); err != nil {
					t.Fatal(err)
				}
				if gotStep != step || gotState != state || gotLifecycle != lifecycle {
					t.Fatalf("record write moved progress: %s/%s/%s -> %s/%s/%s", step, state, lifecycle, gotStep, gotState, gotLifecycle)
				}
				current, err := workflowDeliveryAssertionRead(ctx, s.db, work)
				if err != nil || current == nil || current.Artifact != artifact {
					t.Fatalf("delivery=%+v error=%v", current, err)
				}
			}
		})
	}
}

func TestOrdinaryRecordReplacementKeepsDerivedStepPass(t *testing.T) {
	const work = "ordinary-record-derived-pass"
	f := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "refine")
	s := f.store
	if err := runVerdictActionAs(t, s, work, "checkpoint_refine", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	start := workflowCurrentStartAtStep(t, s, work, "refine")
	if err := runVerdictActionAs(t, s, work, "record_diagnosis", diagnosisPayload("The reproduction", "search", "none_found", nil, "The cause"), 0, f.owner); err != nil {
		t.Fatalf("ordinary diagnosis replacement: %v", err)
	}
	if got := currentStep(t, s, work); got != "refine" {
		t.Fatalf("replacement moved progress to %s", got)
	}
	if err := runVerdictActionAs(t, s, work, "checkpoint_refine", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatalf("checkpoint after replacement: %v", err)
	}
	if after := workflowCurrentStartAtStep(t, s, work, "refine"); after == nil || *after != *start {
		t.Fatalf("replacement opened a new step pass: %+v -> %+v", start, after)
	}
}
