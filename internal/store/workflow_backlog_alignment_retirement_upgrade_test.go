package store

import "testing"

func TestBacklogAlignmentEventAppendRefused(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "alignment-event")
	version := readWorkVersion(t, s, "alignment-event")
	err := applyWorkEvent(t, s, operationEvent("retired-alignment", "workflow.backlog_alignment_recorded", SubjectWorkItem, "alignment-event", map[string]any{}), nil)
	assertFailureKind(t, err, KindInvalidOperation)
	if got := readWorkVersion(t, s, "alignment-event"); got != version {
		t.Fatalf("refused event changed version: %d -> %d", version, got)
	}
}
