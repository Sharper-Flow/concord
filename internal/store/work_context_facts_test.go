package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func seedContextFactTerminal(t *testing.T, f workContextFixture, kind string, fields map[string]any) int64 {
	t.Helper()
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	var frontier int64
	if err := tx.QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&frontier); err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: fmt.Sprintf("context-facts:%s:%d", f.workID, frontier+1), SubjectType: SubjectWorkItem, SubjectID: f.workID, Kind: kind, Actor: "fixture", PayloadVersion: 1, Payload: jsonRaw(workflowJSON(fields)), OccurredAt: time.Unix(1, 0)}
	seq, err := appendPreparedEvent(t.Context(), tx, event)
	if err != nil {
		t.Fatal(err)
	}
	event.Seq = seq
	if err := writeWorkContextFacts(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkEffectSeqTx(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestWorkContextFactsAnchorSelectionBackfillAndLogBlind(t *testing.T) {
	f := seedWorkContextFixture(t, "context-facts-selection")
	ctx := t.Context()
	if err := f.recordWorkContext(t, map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}}); err != nil {
		t.Fatal(err)
	}
	reportSeq := seedContextFactTerminal(t, f, WorkerCompleted, map[string]any{
		"context_findings": []WorkerContextFinding{contextFindingFixture()},
		"review":           map[string]any{"findings": []WorkerReviewFinding{{FindingID: "review:one", Severity: "high", Detail: "ranked claim", Status: "open"}}},
	})
	selected := fmt.Sprintf("finding:%d:1", reportSeq)
	if err := f.recordWorkContext(t, map[string]any{"finding_refs": []string{selected}, "context_findings": []map[string]any{workContextFinding(workContextTestChild, "direction", "current declaration", "")}}); err != nil {
		t.Fatal(err)
	}
	failedSeq := seedContextFactTerminal(t, f, WorkerFailed, map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}})
	emptySeq := seedContextFactTerminal(t, f, WorkerCompleted, map[string]any{})
	view, err := readWorkContextView(ctx, f.store.db, f.workID)
	if err != nil || view == nil {
		t.Fatalf("current view = %+v, %v", view, err)
	}
	if view.SourceEventFrontier != emptySeq || len(view.Findings) != 3 || view.Findings[0].Statement != "current declaration" || view.Findings[1].FindingID != selected || view.Findings[2].FindingID != fmt.Sprintf("finding:%d:0", failedSeq) {
		t.Fatalf("anchor/selection/report order or empty-report frontier changed: %+v", view)
	}
	tx, err := f.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM work_context_findings; DELETE FROM work_context_state`); err != nil {
		t.Fatal(err)
	}
	if err := backfillWorkContextFacts(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`ALTER TABLE domain_events RENAME TO retired_context_log`); err != nil {
		t.Fatal(err)
	}
	got, err := readWorkContextView(ctx, f.store.db, f.workID)
	if err != nil || !reflect.DeepEqual(got, view) {
		t.Fatalf("backfilled/log-blind view differs: got %+v err %v; want %+v", got, err, view)
	}
}

func TestWorkContextFactsReferenceAdmissionLogBlind(t *testing.T) {
	f := seedWorkContextFixture(t, "context-facts-refs")
	seq := seedContextFactTerminal(t, f, WorkerFailed, map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}})
	valid := fmt.Sprintf("finding:%d:0", seq)
	if _, err := f.store.db.Exec(`ALTER TABLE domain_events RENAME TO retired_context_log`); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := validateWorkContextFindingRefs(t.Context(), tx, f.workID, []string{valid}); err != nil {
		t.Fatalf("valid retained finding refused without log: %v", err)
	}
	for _, refs := range [][]string{{valid, valid}, {fmt.Sprintf("finding:%d:1", seq)}, {fmt.Sprintf("finding:%d:0", seq+1000)}, {"finding:1:0"}, {"not-a-finding"}} {
		if err := validateWorkContextFindingRefs(t.Context(), tx, f.workID, refs); !hasFailureKind(err, KindInvalidPayload) {
			t.Fatalf("invalid selection %v = %v, want invalid_payload", refs, err)
		}
	}
}

func TestWorkContextFactsGuardAndRollback(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "context-facts-guard")
	if _, err := s.db.Exec(`INSERT INTO work_context_state(work_id,source_frontier) VALUES('context-facts-guard',1)`); err == nil {
		t.Fatal("context state accepted an unguarded write")
	}
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	event := Event{Kind: WorkerFailed, SubjectType: SubjectWorkItem, SubjectID: "context-facts-guard", EventID: "rolled-back", Seq: 200, Payload: jsonRaw(workflowJSON(map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}}))}
	if err := writeWorkContextFacts(t.Context(), tx, event); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var state, claims int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM work_context_state),(SELECT count(*) FROM work_context_findings)`).Scan(&state, &claims); err != nil || state != 0 || claims != 0 {
		t.Fatalf("rolled-back context state/claims = %d/%d, %v", state, claims, err)
	}
}

func TestWorkContextFactsCorruptionIsNotRetryable(t *testing.T) {
	f := seedWorkContextFixture(t, "context-facts-corruption")
	seq := seedContextFactTerminal(t, f, WorkerFailed, map[string]any{"context_findings": []WorkerContextFinding{contextFindingFixture()}})
	if _, err := f.store.db.Exec(`INSERT INTO fold_guard VALUES(1); UPDATE work_context_findings SET finding='{"statement":12}' WHERE source_seq=?; DELETE FROM fold_guard`, seq); err != nil {
		t.Fatal(err)
	}
	_, err := readWorkContextView(t.Context(), f.store.db, f.workID)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation || failure.RetrySafe {
		t.Fatalf("corrupt claim = %+v, want non-retryable invariant violation", err)
	}
}
