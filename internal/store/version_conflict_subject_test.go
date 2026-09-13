package store

import (
	"strings"
	"testing"
)

// A two-sided fold validates one version per work item. The refusal must name
// the item whose version actually disagreed. Naming the event subject instead
// sends the caller to re-read an item that is already current, and the carried
// current_versions entry then asserts a version that item does not hold.
func TestVersionConflictNamesTheDisagreeingWorkItem(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "from-work")
	seedWork(t, s, "to-work")

	event := operationEvent("stale-to", "relation.added", SubjectWorkItem, "from-work", map[string]any{
		"from": "from-work", "to": "to-work", "kind": "depends_on", "reason": "test",
		"expected_version": 2, "resulting_version": 3,
		"to_expected_version": 99, "to_resulting_version": 100,
	})
	err := applyWorkEvent(t, s, event, workVersion("from-work", 2))
	assertFailureKind(t, err, KindVersionConflict)

	if strings.Contains(err.Error(), "from-work") {
		t.Fatalf("refusal names the event subject rather than the disagreeing item: %v", err)
	}
	if !strings.Contains(err.Error(), "to-work") {
		t.Fatalf("refusal does not name the disagreeing item to-work: %v", err)
	}

	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("version conflict is not a typed failure: %v", err)
	}
	if len(failure.CurrentVersions) != 1 {
		t.Fatalf("current_versions = %d entries, want 1", len(failure.CurrentVersions))
	}
	if failure.CurrentVersions[0].SubjectID != "to-work" {
		t.Fatalf("current_versions names %q, want to-work", failure.CurrentVersions[0].SubjectID)
	}
	if failure.CurrentVersions[0].Version != 2 {
		t.Fatalf("current_versions reports version %d, want to-work's actual 2", failure.CurrentVersions[0].Version)
	}
}
