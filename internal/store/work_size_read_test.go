package store

import (
	"context"
	"testing"
)

func sizedWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	seedWork(t, s, workID)
	inputs := workSizeInputsFixture()
	size, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil || size != WorkSizeSmall {
		t.Fatalf("fixture inputs decide %q, %v; want small", size, err)
	}
	if err := applyWorkEvent(t, s, workSizedEvent(workID+"-sized-1", workID, WorkSizeModelVersion, size, inputs), nil); err != nil {
		t.Fatal(err)
	}
}

func readEffectiveWorkSizeForTest(t *testing.T, s *Store, workID string) (WorkSizeDecision, bool) {
	t.Helper()
	decision, sized, err := readEffectiveWorkSize(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	return decision, sized
}
