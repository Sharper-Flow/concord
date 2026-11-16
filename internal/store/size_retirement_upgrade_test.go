package store

import "testing"

func TestSizeEventAppendIsRetired(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "retired-size")
	err := applyWorkEvent(t, s, operationEvent("retired-size-event", "work.sized", SubjectWorkItem, "retired-size", map[string]any{}), nil)
	assertFailureKind(t, err, KindInvalidOperation)
}
