package store

import (
	"fmt"
	"reflect"
	"testing"
)

func TestCoordinatorEvidencePreservesCorrectionDirection(t *testing.T) {
	t.Parallel()
	db, appendEvent := correctionHistoryFixture(t)
	appendEvent(WorkflowActionCompleted, correctionHistoryRequest("standing"))
	before, err := readWorkflowCorrectionContext(t.Context(), db, "work-history")
	if err != nil || before == nil {
		t.Fatalf("standing correction=%+v, %v", before, err)
	}
	appendEvent(WorkflowEvidenceBound, progressEvidence("artifact", "evidence:qualified"))
	appendEvent(WorkflowPremiseConfirmed, map[string]any{"contract_version": 1, "confirming_actor_ref": "actor:coordinator", "confirmation_authority": "coordinator"})
	after, err := readWorkflowCorrectionContext(t.Context(), db, "work-history")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("coordinator facts changed correction: %+v -> %+v, %v", before, after, err)
	}
}

func TestRepeatedCorrectionKeepsCoordinatorRoutes(t *testing.T) {
	t.Parallel()
	j := pReturnReviewFixture(t, "repeated-correction-coordinator", "workflow.break_fix", "correction_required")
	for cycle := 1; cycle <= 4; cycle++ {
		if cycle > 1 {
			pReturnProduce(t, j, fmt.Sprintf("correction-%d", cycle))
			acceptCurrentReviewForTest(t, j.fixture, "correction_required")
		}
		if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
			t.Fatalf("cycle %d corrective return: %v", cycle, err)
		}
		pin := issue1013Pin(t, j.s, j.workID)
		if pin.Step != "repair" || pin.Correction == nil {
			t.Fatalf("cycle %d lost its producer/direction: %+v", cycle, pin)
		}
		for _, action := range []string{"record_work_context", "record_review", "supersede_contract"} {
			if _, _, err := WorkflowActionDefinitionFor(t.Context(), j.s, BuiltinWorkflowRegistry(), j.workID, action); err != nil {
				t.Fatalf("cycle %d hid coordinator action %s: %v", cycle, action, err)
			}
		}
	}
}
