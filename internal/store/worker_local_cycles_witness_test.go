package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A4 composed witness: the local edit/test cycles a worker performs inside
// one authorized job-bound attempt window leave no store trace. The cycles
// run as real in-process work on a real file in the claimed session
// worktree — an edit, a read-back check that fails, a second edit, the same
// check passing — with no store write, and the whole dispatch window, epoch,
// event, version, attempt, approval, and correction state must stay
// identical until the one valid completion lands through the normal
// terminal fold (CD-0205 dispatch ownership, CD-0056 terminal fold).
//
// Evidence limit: this is a deterministic store lifecycle witness, not a
// host worker round trip or an application quality check. The claimed
// worktree on this fixture is a synthetic temporary directory with no
// repository content, so the cycles rewrite a synthetic file instead of
// repository sources and the in-process read-back check stands in for a
// real verification command. The store cannot know external commands — that
// boundary is the property under test — so the witness proves the store
// lifecycle only.

// localCyclesSnapshot is the whole store state one local cycle must leave
// untouched.
type localCyclesSnapshot struct {
	version             int64
	events              int64
	attempts            int
	attemptLifecycle    string
	dispatchCompletions int64
	terminalReports     int
	correctionOpen      bool
}

// localCyclesDispatchCompletions counts the dispatch_worker completions of
// one work item.
func localCyclesDispatchCompletions(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var count int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_authorizations WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// localCyclesTerminalReports counts the worker.completed and worker.failed
// reports of one attempt.
func localCyclesTerminalReports(t *testing.T, s *Store, workID, attemptID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts WHERE work_id=? AND terminal_kind IN (?,?) AND attempt_id=?`, workID, WorkerCompleted, WorkerFailed, attemptID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkerLocalEditTestCyclesLeaveNoStoreTrace(t *testing.T) {
	t.Parallel()
	const workID = "worker-local-cycles-witness"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	lane := reviewGateLane(t, "implementation")

	// One job-bound attempt through the existing authorize-and-record route:
	// record_worker_job, the dispatch_worker admission, and the
	// worker.dispatched evidence bind the attempt to its recorded revision.
	job := &WorkerJobBinding{JobID: "job:local-cycles", Revision: 1}
	attempt := "attempt:" + workID + ":local-cycles"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt, nil, fixture.owner, 0, "local-cycles-"+workID, job)
	claimedWorktree := dispatchSessionWorktree(t, s, workID)

	attemptLifecycle := func() string {
		t.Helper()
		var lifecycle string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attempt).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		return lifecycle
	}
	snapshot := func() localCyclesSnapshot {
		t.Helper()
		correction, err := workflowCorrectionContext(ctx, s.db, workID, "repair")
		if err != nil {
			t.Fatal(err)
		}
		return localCyclesSnapshot{
			version:             readWorkVersion(t, s, workID),
			events:              sequenceWitnessEventCount(t, s, workID),
			attempts:            sequenceWitnessAttemptCount(t, s, workID),
			attemptLifecycle:    attemptLifecycle(),
			dispatchCompletions: localCyclesDispatchCompletions(t, s, workID),
			terminalReports:     localCyclesTerminalReports(t, s, workID, attempt),
			correctionOpen:      correction != nil,
		}
	}
	before := snapshot()
	startsBefore := workflowCurrentStartAtStep(t, s, workID, "repair")
	requireUnchanged := func(when string, before localCyclesSnapshot) {
		t.Helper()
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("local cycles %s changed the store:\nbefore: %+v\nafter:  %+v", when, before, after)
		}
		if starts := workflowCurrentStartAtStep(t, s, workID, "repair"); !reflect.DeepEqual(startsBefore, starts) {
			t.Fatalf("local cycles %s changed the step windows:\nbefore: %+v\nafter:  %+v", when, startsBefore, starts)
		}
	}

	// The simulated worker's local workspace is the session worktree the
	// dispatch authorization claimed; the store recorded the claim's path,
	// never its content.
	sourcePath := filepath.Join(claimedWorktree, "subject.txt")
	localCheck := func() error {
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "complete edit") {
			return fmt.Errorf("the recorded content %q carries no completing edit", string(data))
		}
		return nil
	}

	// Cycle one: the edit lands, the local check fails on it.
	if err := os.WriteFile(sourcePath, []byte("cycle one: partial edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := localCheck(); err == nil {
		t.Fatal("the local check passed on the partial edit")
	}
	requireUnchanged("after the failed local check", before)

	// Cycle two: the completing edit lands, the same local check passes.
	if err := os.WriteFile(sourcePath, []byte("cycle two: complete edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := localCheck(); err != nil {
		t.Fatalf("the local check failed on the completed edit: %v", err)
	}
	requireUnchanged("after the passing local check", before)

	// One valid completion through the existing terminal route: the report
	// claims exactly the dispatched revision and the fold moves the attempt
	// to its completed terminal state. No correction window opens, no new
	// window or epoch appears, and no approval surface is touched.
	dispatched, err := workflowDispatchedJobForAttempt(ctx, s.DatabaseForTesting(), workID, attempt)
	if err != nil || dispatched == nil || *dispatched != *job {
		t.Fatalf("the dispatched binding for %s = (%#v, %v), want %#v", attempt, dispatched, err, *job)
	}
	if err := completeJobBoundAttemptForTest(s, workID, attempt, lane, dispatched); err != nil {
		t.Fatalf("the one valid completion: %v", err)
	}
	if got := attemptLifecycle(); got != "completed" {
		t.Fatalf("attempt lifecycle after the completion = %q, want completed", got)
	}
	if got := localCyclesDispatchCompletions(t, s, workID); got != 1 {
		t.Fatalf("dispatch completions = %d, want exactly the one authorization", got)
	}
	if got := localCyclesTerminalReports(t, s, workID, attempt); got != 1 {
		t.Fatalf("terminal reports = %d, want exactly the one completion", got)
	}
	var reportedJobID string
	var reportedRevision int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(terminal_payload,'$.worker_job.job_id'), json_extract(terminal_payload,'$.worker_job.revision') FROM worker_attempts WHERE work_id=? AND terminal_kind=? AND attempt_id=?`, workID, WorkerCompleted, attempt).Scan(&reportedJobID, &reportedRevision); err != nil {
		t.Fatal(err)
	}
	if reportedJobID != job.JobID || reportedRevision != job.Revision {
		t.Fatalf("the completion binds %s@%d, want the dispatched %s@%d", reportedJobID, reportedRevision, job.JobID, job.Revision)
	}
	if correction, err := workflowCorrectionContext(ctx, s.db, workID, "repair"); err != nil || correction != nil {
		t.Fatalf("correction after the completion = (%+v, %v), want none", correction, err)
	}
	if starts := workflowCurrentStartAtStep(t, s, workID, "repair"); !reflect.DeepEqual(startsBefore, starts) {
		t.Fatalf("the completion changed the step windows:\nbefore: %+v\nafter:  %+v", startsBefore, starts)
	}
	after := snapshot()
	if after.events != before.events+1 {
		t.Fatalf("the completion moved the event count from %d to %d, want exactly the one report event", before.events, after.events)
	}
	if after.correctionOpen {
		t.Fatal("the completion opened a correction window")
	}
}
