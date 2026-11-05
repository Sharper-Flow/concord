package store

import (
	"slices"
	"testing"
)

func TestCoordinatorCorrectionFactsHaveNoWorkerConsumer(t *testing.T) {
	request := Event{Kind: WorkflowActionCompleted, EventID: "correction:one", Seq: 10, Payload: []byte(`{"action_id":"request_correction","correction_diagnosis":"reported failure","correction_strategy":"correct the candidate","correction_predicate_ids":["predicate:one"],"correction_evidence_refs":["verify:one"]}`)}
	state, changed, err := correctionStateForEvent(request)
	if err != nil || !changed || len(state.Records) != 1 || state.Records[0].SourceID != request.EventID {
		t.Fatalf("correction record=%+v changed=%v err=%v", state, changed, err)
	}
	request.Seq++
	request.Payload = []byte(`{"action_id":"record_review","review":{"disposition":"correction_required"}}`)
	if _, changed, err := correctionStateForEvent(request); err != nil || changed {
		t.Fatalf("unchanged corrective Review consumed its unfinished direction: %v %v", changed, err)
	}
	request.Payload = []byte(`{"action_id":"record_review","review":{"disposition":"ready_for_final_proof"}}`)
	state, changed, err = correctionStateForEvent(request)
	if err != nil || !changed || state.Boundary != request.Seq || len(state.Records) != 0 {
		t.Fatalf("recorded Review did not consume correction context: %+v %v %v", state, changed, err)
	}
	request.Payload = []byte(`{"action_id":"dispatch_worker"}`)
	if _, changed, err := correctionStateForEvent(request); err != nil || changed {
		t.Fatalf("retired worker action consumed correction: %v %v", changed, err)
	}
}

func TestCoordinatorReviewActionExists(t *testing.T) {
	plan := currentWorkflowPlanFixture(t, WorkflowStepPlanFix)
	for _, step := range plan.Steps {
		if slices.Contains(WorkflowStepPlanTemplateActions(plan.Type, step.ID), "record_review") {
			return
		}
	}
	t.Fatal("coordinator cannot record a review attempt")
}
