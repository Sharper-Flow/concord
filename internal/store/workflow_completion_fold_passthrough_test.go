package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// A fold failure inside the clause-7 completion transaction is a typed,
// attributed failure. Masking it as a clause-7 operation_conflict with a
// reconcile_operation recovery sends the caller to reconcile a transaction
// that already rolled back and holds no terminal item.

func TestWorkflowCompletionTerminalFoldFailureReachesCallerUnmasked(t *testing.T) {
	t.Parallel()
	workID := "completion-fold-passthrough"
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true,
		includePremise: true, verdictKind: "ok",
	})
	// Every completion gate passes while the workflow instance is already
	// terminal: only the completion fold reads the instance state.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET instance_state='completed' WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	eventsBefore, versionBefore := completionFoldBookkeeping(t, s, workID)
	err := CompleteWorkflow(context.Background(), s, completion)
	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("completion error is not a typed failure: %v", err)
	}
	if failure.Kind != KindInvalidOperation || failure.Clause != 7 {
		t.Fatalf("failure kind=%s clause=%d, want kind=%s clause=7", failure.Kind, failure.Clause, KindInvalidOperation)
	}
	if failure.Op != "fold_event" || failure.Detail != "workflow is already terminal or missing" || failure.RecoveryAction != "complete an active workflow once" {
		t.Fatalf("inner fold failure did not reach the caller: op=%q detail=%q recovery=%q", failure.Op, failure.Detail, failure.RecoveryAction)
	}
	assertCompletionFoldRolledBack(t, s, workID, eventsBefore, versionBefore)
}

func TestWorkflowCompletionAcceptsEvidenceCountAbove32(t *testing.T) {
	t.Parallel()
	workID := "evidence-count-over-32"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{
		requiredEvidence: []string{"verification", "review"}, includeSpec: true, includeVerdict: true,
		includePremise: true, verdictKind: "ok",
	})
	const extra = 30
	var initialBound int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=?`, workID, WorkflowEvidenceBound).Scan(&initialBound); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	events := make([]Event, 0, extra)
	for i := 0; i < extra; i++ {
		ordinal := fmt.Sprintf("%02d", i)
		runRef := "over32-" + workID + "-" + ordinal
		locator := "evidence:over32-" + ordinal
		seedWorkflowAuthority(t, s, runRef, workID, "principal/bulk", "request/bulk-"+ordinal, []string{locator})
		events = append(events, workflowEvent("over32-evidence-"+ordinal, WorkflowEvidenceBound, workID, map[string]any{
			"work_id": workID, "expected_version": version + int64(i), "resulting_version": version + int64(i) + 1,
			"evidence_kind": "verification", "immutable_subject_ref": locator,
			"producer_id": "principal/bulk", "producer_run_ref": runRef, "producer_watermark": "request/bulk-" + ordinal,
			"observed_at": "2026-08-09T00:00:00Z",
		}))
	}
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	var bound int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=?`, workID, WorkflowEvidenceBound).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound != initialBound+extra || bound <= 32 {
		t.Fatalf("bound evidence events = %d, want %d fixture bindings plus %d bulk bindings and a total above 32", bound, initialBound, extra)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	operator := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/owner", "session/"+workID)
	completion := workflowEventWithActor("over32-completion-"+workID, WorkflowCompleted, workID, operator, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": operator,
		"premise_confirmed": true, "evidence_count": bound, "changed_refs_digest": "sha256:" + strings.Repeat("a", 64),
		"impact_verdict": "non-breaking",
	})
	if err := CompleteWorkflow(context.Background(), s, completion); err != nil {
		t.Fatalf("completion with %d bound evidence events failed: %v", bound, err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("workflow instance state = %q, want completed", state)
	}
}

func completionFoldBookkeeping(t *testing.T, s *Store, workID string) (int, int64) {
	t.Helper()
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return events, version
}

func assertCompletionFoldRolledBack(t *testing.T, s *Store, workID string, events int, version int64) {
	t.Helper()
	assertTableCount(t, s, "effect_receipts", events)
	var versionAfter int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	if versionAfter != version {
		t.Fatalf("work version = %d after rollback, want %d", versionAfter, version)
	}
}
