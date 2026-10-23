package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCurrentCorrectionRefineIsRefusedByLiveWriter(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(ref, func(t *testing.T) {
			const workID = "current-correction-refusal"
			fixture := seedWorkflowReturnRouteFixture(t, workID, ref, "refine")
			s := fixture.store
			defer s.Close()
			events, projections := histEventRows(t, s), projectionSnapshot(t, s)
			payload := json.RawMessage(`{"diagnosis":"The result requires correction.","strategy":"Return to the producer.","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
			err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, fixture.owner, fixture.operator)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || failure.Op != "workflow_action" || failure.Detail != "request_correction requires a current non-ok verification verdict under the active workflow contract" || failure.RetrySafe || failure.RecoveryAction != "record the current verification verdict or reread the work pin" {
				t.Fatalf("refine correction refusal: %v", err)
			}
			if !reflect.DeepEqual(events, histEventRows(t, s)) || projections != projectionSnapshot(t, s) {
				t.Fatal("refused live correction changed retained events or projections")
			}
		})
	}
}

func TestRecordedCorrectionUsesCurrentDeclaredRoutes(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		entry, ok := BuiltinWorkflowRegistry().Current(definition.Ref)
		if !ok {
			t.Fatal(definition.Ref)
		}
		for _, step := range definition.StepGraph.Steps {
			got, err := workflowRecordedCorrectionReturnTarget(entry, step.ID)
			want := workflowCorrectionReturnTarget(definition, step.ID)
			if err != nil || got != want {
				t.Fatalf("%s %s: %q, %v; want %q", definition.Ref, step.ID, got, err, want)
			}
		}
		entry.Digest = "sha256:" + strings.Repeat("0", 64)
		if _, err := workflowRecordedCorrectionReturnTarget(entry, "complete"); err == nil {
			t.Fatal("unknown digest borrowed a declared correction route")
		}
	}
}

func TestFoldCorrectionReturnTargetSelectsResolverAndRefusesEmptyRoute(t *testing.T) {
	entry, ok := BuiltinWorkflowRegistry().Current("workflow.break_fix")
	if !ok {
		t.Fatal("missing break-fix definition")
	}
	for _, ctx := range []context.Context{context.Background(), workflowReplayContext(context.Background())} {
		want := workflowCorrectionReturnTarget(entry.Definition, "verify")
		got, err := workflowFoldCorrectionReturnTarget(ctx, entry, "verify", "verify")
		if err != nil || got == "" || got != want {
			t.Fatalf("return = %q, %v; want %q", got, err, want)
		}
		_, err = workflowFoldCorrectionReturnTarget(ctx, entry, "planning", "planning")
		wantFoldCorrectionFailure(t, err, "correction has no declared recovery route to a producer step", "repair the pinned workflow definition")
	}
	_, err := workflowFoldCorrectionReturnTarget(workflowReplayContext(context.Background()), entry, "verify", "refine")
	wantFoldCorrectionFailure(t, err, "recorded correction source step differs from the pinned current step", "restore a consistent recorded workflow history")
}

func TestRecordedCorrectionRefusalsAreAttributedAndAtomic(t *testing.T) {
	entry, _ := BuiltinWorkflowRegistry().Current("workflow.break_fix")
	for _, tc := range []struct {
		step    string
		version int
	}{
		{"missing-step", 3}, {"refine", eventKindRegistry[WorkflowActionCompleted].CurrentVersion + 1},
	} {
		t.Run(fmt.Sprintf("%s-v%d", tc.step, tc.version), func(t *testing.T) {
			const workID = "recorded-correction-refusal"
			fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, entry.Definition.Ref, entry.Definition.Version, "refine")
			s := fixture.store
			defer s.Close()
			actor, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			var version int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
				t.Fatal(err)
			}
			event := workflowEventWithActor(workID+":recorded-correction", WorkflowActionCompleted, workID, actor, map[string]any{
				"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"action_id": "request_correction", "step_id": tc.step, "attempt_epoch": 1,
				"actor_ref": actor, "result_evidence_refs": []string{}, "changed_refs": []string{workID},
				"correction_diagnosis": "The retained result requires correction.", "correction_strategy": "Return to the recorded producer.",
				"correction_predicate_ids": []string{"predicate:return-route"}, "correction_evidence_refs": []string{"evidence:return-route-verification"},
			})
			event.PayloadVersion = tc.version
			insertHistoricalEvent(t, s, event)
			before, events := projectionSnapshot(t, s), histEventRows(t, s)
			err = RebuildFromLog(context.Background(), s)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.EventID != event.EventID || failure.EventKind != event.Kind || failure.PayloadVersion != tc.version || failure.SubjectID != workID || failure.Sequence == 0 || failure.Stage == "" {
				t.Fatalf("unattributed refusal: %v", err)
			}
			if before != projectionSnapshot(t, s) || !reflect.DeepEqual(events, histEventRows(t, s)) {
				t.Fatal("refused replay changed projections or retained bytes")
			}
		})
	}
}

func wantFoldCorrectionFailure(t *testing.T, err error, detail, recovery string) {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Op != "fold_event" || failure.Detail != detail || failure.RetrySafe || failure.RecoveryAction != recovery {
		t.Fatalf("fold correction refusal = %v, want %s %q with recovery %q", err, KindIllegalLifecycleTransition, detail, recovery)
	}
}
