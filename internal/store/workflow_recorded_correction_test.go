package store

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCurrentCorrectionRefineIsRefusedByLiveWriter(t *testing.T) {
	for _, ref := range []string{"workflow.implementation", "workflow.break_fix"} {
		t.Run(ref, func(t *testing.T) {
			const workID = "current-correction-refusal"
			fixture := seedWorkflowReturnRouteFixture(t, workID, ref, "refine")
			s := fixture.store
			defer s.Close()
			events, projections := currentEffectRows(t, s), projectionSnapshot(t, s)
			payload := json.RawMessage(`{"diagnosis":"The result requires correction.","strategy":"Return to the producer.","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
			err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, fixture.owner, fixture.operator)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || failure.Op != "workflow_action" || failure.Detail != "request_correction requires a current non-ok verification verdict under the active workflow contract" || failure.RetrySafe || failure.RecoveryAction != "record the current verification verdict or reread the work pin" {
				t.Fatalf("refine correction refusal: %v", err)
			}
			if !reflect.DeepEqual(events, currentEffectRows(t, s)) || projections != projectionSnapshot(t, s) {
				t.Fatal("refused live correction changed retained events or projections")
			}
		})
	}
}

func TestFoldCorrectionReturnTargetSelectsResolverAndRefusesEmptyRoute(t *testing.T) {
	entry, ok := BuiltinWorkflowRegistry().Current("workflow.break_fix")
	if !ok {
		t.Fatal("missing break-fix definition")
	}
	want := workflowCorrectionReturnTarget(entry.Definition, "verify")
	got, err := workflowFoldCorrectionReturnTarget(entry, "verify")
	if err != nil || got == "" || got != want {
		t.Fatalf("return = %q, %v; want %q", got, err, want)
	}
	_, err = workflowFoldCorrectionReturnTarget(entry, "planning")
	wantFoldCorrectionFailure(t, err, "correction has no declared recovery route to a producer step", "repair the pinned workflow definition")
}

func wantFoldCorrectionFailure(t *testing.T, err error, detail, recovery string) {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition || failure.Op != "fold_event" || failure.Detail != detail || failure.RetrySafe || failure.RecoveryAction != recovery {
		t.Fatalf("fold correction refusal = %v, want %s %q with recovery %q", err, KindIllegalLifecycleTransition, detail, recovery)
	}
}
