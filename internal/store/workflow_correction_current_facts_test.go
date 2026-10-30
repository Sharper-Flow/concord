package store

import (
	"reflect"
	"testing"
)

func TestWorkflowCorrectionCurrentFactsWithoutLog(t *testing.T) {
	j := pReturnReviewFixture(t, "correction-current-facts", "workflow.break_fix", "correction_required")
	if err := j.action(t, "request_correction", pReviewCorrectionPayload(t), j.fixture.owner); err != nil {
		t.Fatal(err)
	}
	want, err := readWorkflowCorrectionContext(t.Context(), j.s.db, j.workID)
	if err != nil || want == nil || want.SourceEventID == "" || want.SourceEventSeq < 1 {
		t.Fatalf("current correction fixture: %+v, %v", want, err)
	}
	assertWorkflowCurrentFactsWithIndependentMigration142(t, j.s)
	got, err := readWorkflowCorrectionContext(t.Context(), j.s.db, j.workID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("migration changed correction identity/direction: got %+v, want %+v, %v", got, want, err)
	}
	assertWorkerFactsLogBlind(t, j.s, map[string]func() (any, error){
		"G28-current-direction": func() (any, error) { return readWorkflowCorrectionContext(t.Context(), j.s.db, j.workID) },
		"G28-work-pin-direction": func() (any, error) {
			pin, err := ReadWorkPin(t.Context(), j.s, j.workID)
			return pin.Correction, err
		},
	})
}
