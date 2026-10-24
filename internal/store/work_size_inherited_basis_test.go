package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestWorkSizeRefusesAnInheritedReviewBasis(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "sized-successor")
	model := workSizedPayload{ModelVersion: 2, Size: WorkSizeCrucial, Inputs: workSizeInputsFixture(), Footprint: workSizeFootprintFixture(), ReviewBasis: []string{"accepted-review"}}
	raw, _ := json.Marshal(model)
	event := Event{Seq: 1, EventID: "successor-model", Kind: WorkSized, SubjectType: SubjectWorkItem, SubjectID: "sized-successor", Actor: "operator", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: 2, Payload: raw}
	tx := lawChangeTx(t, s)
	defer func() { _ = tx.Rollback() }()
	if _, err := beginFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, foldWorkSized(context.Background(), tx, event), KindInvariantViolation, "live inherited proof")
}
