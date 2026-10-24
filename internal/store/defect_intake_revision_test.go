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
// prior defect from the sibling matching query.

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
