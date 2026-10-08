package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The capture-owned defect classification makes the captured kind immutable
// on live revision: a classified research item converting to bug would bypass
// recurrence admission, and a classified bug converting away would hide a
// prior defect from the sibling matching query. Historical revisions that
// predate the guard still replay.

// reviseDefectWork revises one work item's intent through the ordinary
// operation route.
func reviseDefectWork(t *testing.T, s *Store, workID, kind string) error {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"title": "Revised " + workID, "value_statement": "Revised value", "kind": kind, "priority": 1, "urgency": "standard", "tags": []string{}, "workflow_type_ref": "", "reason": "revision reason", "evidence_refs": []string{}, "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	op := Operation{Events: []Event{{EventID: "defect-revise-" + workID + "-" + kind + "-" + fmt.Sprint(version), Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(6, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}
	err = ApplyOperation(context.Background(), s, op)
	assertFoldGuardEmpty(t, s)
	return err
}

// A classified research item cannot convert to the bug kind: the conversion
// would mint a bug that never passed recurrence admission.
func TestClassifiedResearchRevisionToBugRefused(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-rev-research", "research", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("classified research capture refused: %v", err)
	}
	if err := reviseDefectWork(t, s, "work-rev-research", "bug"); err == nil {
		t.Fatal("classified research item was revised into the bug kind")
	} else if !strings.Contains(failureText(err), "kind") {
		t.Fatalf("conversion refusal %q does not name the kind", failureText(err))
	}
	var kind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT kind FROM work_items WHERE id=?`, "work-rev-research").Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "research" {
		t.Fatalf("refused conversion left kind=%q", kind)
	}
}

// A classified bug cannot leave the bug kind: the sibling matching query
// reads the stored kind, so the conversion would hide a prior defect from
// every later same-shape admission.
func TestClassifiedBugRevisionAwayRefused(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-rev-bug", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("classified bug capture refused: %v", err)
	}
	for _, kind := range []string{"task", "research"} {
		if err := reviseDefectWork(t, s, "work-rev-bug", kind); err == nil {
			t.Fatalf("classified bug item was revised into the %s kind", kind)
		} else if !strings.Contains(failureText(err), "kind") {
			t.Fatalf("conversion refusal %q does not name the kind", failureText(err))
		}
	}
	var kind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT kind FROM work_items WHERE id=?`, "work-rev-bug").Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "bug" {
		t.Fatalf("refused conversion left kind=%q", kind)
	}
	if block := defectBlock(t, s, "work-rev-bug"); block == nil {
		t.Fatal("refused conversion erased the classification")
	}
}

// An unclassified item still cannot convert into the bug kind: the conversion
// would bypass capture admission without any classification at all.
func TestUnclassifiedNonBugRevisionToBugStillRefused(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-rev-plain", "task", nil); err != nil {
		t.Fatalf("plain task capture refused: %v", err)
	}
	if err := reviseDefectWork(t, s, "work-rev-plain", "bug"); err == nil {
		t.Fatal("unclassified task item was revised into the bug kind")
	}
}

// A kind change a historical revision already accepted replays unchanged:
// the immutability guard is a live admission rule, and the log stays the
// replay's authority.
func TestHistoricalKindConversionReplaysThroughRebuild(t *testing.T) {
	t.Parallel()
	s := defectFixture(t)
	if err := captureDefectWork(t, s, "work-replay-bug", "bug", defectIntakeJSON("upload-checksum-mismatch", nil, "")); err != nil {
		t.Fatalf("classified bug capture refused: %v", err)
	}
	if err := captureDefectWork(t, s, "work-replay-research", "research", defectIntakeJSON("login-token-expiry", nil, "")); err != nil {
		t.Fatalf("classified research capture refused: %v", err)
	}
	// Record the historical conversions the way replay folds them.
	for _, workID := range []string{"work-replay-bug", "work-replay-research"} {
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(map[string]any{"title": "Historically converted", "value_statement": "Historical conversion", "kind": "task", "priority": 1, "urgency": "standard", "tags": []string{}, "workflow_type_ref": "", "reason": "historical kind change", "evidence_refs": []string{}, "expected_version": version, "resulting_version": version + 1})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		op := Operation{Events: []Event{{EventID: "defect-historical-" + workID, Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(7, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}
		if _, err := applyOperationTx(workflowReplayContext(context.Background()), tx, op, newFoldScope(tx), false); err != nil {
			tx.Rollback()
			t.Fatalf("historical kind change for %s did not replay: %v", workID, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild with historical kind changes refused: %v", err)
	}
	for _, workID := range []string{"work-replay-bug", "work-replay-research"} {
		if block := defectBlock(t, s, workID); block == nil {
			t.Fatalf("%s lost its classification through historical conversion replay", workID)
		}
		var kind string
		if err := s.DatabaseForTesting().QueryRow(`SELECT kind FROM work_items WHERE id=?`, workID).Scan(&kind); err != nil {
			t.Fatal(err)
		}
		if kind != "task" {
			t.Fatalf("%s kind after replay=%q, want the historically accepted task", workID, kind)
		}
	}
}
