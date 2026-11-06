package store

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRetiredWorkflowDefinitionSelectedAppendCommitsNothing(t *testing.T) {
	s := worktreeFixture(t)
	version := readWorkVersion(t, s, "work-w")
	before := outsideRepairDatabaseSnapshot(t, s)
	receipts := currentEffectRows(t, s)
	event := Event{
		EventID: "retired-definition-selection", Kind: WorkflowDefinitionSelected,
		SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "host:fixture",
		OccurredAt: time.Unix(40, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(map[string]any{"work_id": "work-w", "expected_version": version, "resulting_version": version + 1, "ref": "workflow.change", "digest": "sha256:retired", "work_kind": "task"}),
	}
	err := ApplyOperation(t.Context(), s, Operation{Events: []Event{event}})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(err.Error(), "CD-0229 D1") {
		t.Fatalf("append = %v, want invalid_operation naming CD-0229 D1", err)
	}
	if !reflect.DeepEqual(currentEffectRows(t, s), receipts) || outsideRepairDatabaseSnapshot(t, s) != before || readWorkVersion(t, s, "work-w") != version {
		t.Fatal("refusal changed state")
	}
	assertFoldGuardEmpty(t, s)
}
