package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Boundary tests for the shared packet-bounds guard on the dispatch-time
// caller exclusions (worker_packet5_worker_job): at most
// WorkerJobCallerExclusionsMax paths of at most
// WorkerJobCallerExclusionPathMax Unicode characters each. The computation
// owner refuses the dispatch on its error, and the continuity ready
// projection omits the whole set, so the read stays available and the
// dispatch refuses with its recovery.
func TestValidateWorkerJobPacketExclusionsBounds(t *testing.T) {
	const jobID = "job:exclusion-bounds"
	t.Run("empty set passes", func(t *testing.T) {
		if err := validateWorkerJobPacketExclusions(jobID, nil); err != nil {
			t.Fatalf("empty exclusions refused: %v", err)
		}
		if err := validateWorkerJobPacketExclusions(jobID, []string{}); err != nil {
			t.Fatalf("empty exclusions refused: %v", err)
		}
	})
	t.Run("count bound edge", func(t *testing.T) {
		paths := make([]string, WorkerJobCallerExclusionsMax)
		for i := range paths {
			paths[i] = fmt.Sprintf("internal/store/path%d.go", i)
		}
		if err := validateWorkerJobPacketExclusions(jobID, paths); err != nil {
			t.Fatalf("%d paths refused: %v", len(paths), err)
		}
		paths = append(paths, "internal/store/one-too-many.go")
		err := validateWorkerJobPacketExclusions(jobID, paths)
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatalf("%d paths error = %v, want a Failure", len(paths), err)
		}
		if failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, fmt.Sprintf("would carry %d paths", len(paths))) {
			t.Fatalf("over-count refusal = %s, want the exact count diagnostic", failure.Detail)
		}
	})
	t.Run("character bound is unicode, not bytes", func(t *testing.T) {
		runes := []rune(strings.Repeat("ä", WorkerJobCallerExclusionPathMax-3) + ".go")
		path := string(runes)
		if utf8.RuneCountInString(path) != WorkerJobCallerExclusionPathMax {
			t.Fatalf("fixture path carries %d runes, want %d", utf8.RuneCountInString(path), WorkerJobCallerExclusionPathMax)
		}
		if err := validateWorkerJobPacketExclusions(jobID, []string{path}); err != nil {
			t.Fatalf("%d-rune path refused: %v", utf8.RuneCountInString(path), err)
		}
		over := path + "x"
		if utf8.RuneCountInString(over) != WorkerJobCallerExclusionPathMax+1 {
			t.Fatalf("over-bound fixture path carries %d runes", utf8.RuneCountInString(over))
		}
		err := validateWorkerJobPacketExclusions(jobID, []string{over})
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatalf("over-bound path error = %v, want a Failure", err)
		}
		if failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, fmt.Sprintf("above the packet's %d-character bound", WorkerJobCallerExclusionPathMax)) {
			t.Fatalf("over-bound refusal = %s, want the exact character-bound diagnostic", failure.Detail)
		}
	})
}

// callerRepairAllowanceFields authors one worker-job recording with a
// minimal direct-caller allowance whose single mechanism is the job's own
// recorded scope entry, so a contained repair names the recorded mechanism
// and a claimed different owner refuses.
func callerRepairAllowanceFields(t *testing.T, s *Store, workID, jobID string, withAllowance bool) map[string]any {
	t.Helper()
	var project string
	if err := s.DatabaseForTesting().QueryRow(`SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		t.Fatalf("read primary Project for %s: %v", workID, err)
	}
	inventory := "internal/store/" + jobID
	fields := map[string]any{
		"job_id": jobID, "objective": "bounded caller-scope objective for " + jobID,
		"stopping_condition":   "the recorded checks pass with no unresolved reference",
		"checks":               []string{"go test ./internal/store/"},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
		"path_scope": []string{inventory},
	}
	if !withAllowance {
		return fields
	}
	domain := acceptanceOracleFieldsWithoutPreparationsForTest(t, s, workID)["owners"].([]map[string]any)[0]["domain_id"].(string)
	fields["direct_caller_allowance"] = map[string]any{
		"project_id":   project,
		"mechanisms":   []map[string]any{{"project_id": project, "path": inventory, "entry_point": "recorded job"}},
		"domain_ids":   []string{domain},
		"law_bindings": []map[string]any{},
	}
	return fields
}

// completeJobBoundAttemptWithRepairs appends the worker.completed report of
// the named attempt carrying the caller repairs under test, then returns so
// the caller drives the acceptance.
func completeJobBoundAttemptWithRepairs(t *testing.T, s *Store, workID, attemptID string, job *WorkerJobBinding, repairs []WorkerDirectCallerRepair) {
	t.Helper()
	var directory string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, WorktreeSetID(workID)).Scan(&directory); err != nil {
		t.Fatalf("read the work's active worktree: %v", err)
	}
	var laneID string
	var laneVersion int64
	var laneDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lane_id,lane_version,lane_digest FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&laneID, &laneVersion, &laneDigest); err != nil {
		t.Fatalf("read the attempt's lane: %v", err)
	}
	lane, err := LookupLane(laneID, laneVersion, laneDigest)
	if err != nil {
		t.Fatal(err)
	}
	completed := jobBoundCompletionEvent(workID, "caller-completed-"+attemptID, attemptID, lane, time.Unix(16, 0).UTC(), job, directory)
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(completed.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.DirectCallerRepairs = repairs
	completed.Payload = mustJSONValue(payload)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("record completed caller-repair report %s: %v", attemptID, err)
	}
}

func acceptCallerAttempt(t *testing.T, s *Store, workID, attemptID string, acceptor WorkflowActor) error {
	t.Helper()
	return runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor)
}

// TestWorkerJobCallerRepairContainmentAtAcceptance holds the acceptance
// containment (CD-0227 D2/D3): a reported repair binds to the dispatched
// job's recorded allowance, stays off the dispatch's caller exclusions, and
// keeps its outside-scope paths free of another work item's recorded scope;
// a job without the allowance cannot acquire one from a worker report. Each
// scenario seeds its own work, so the sibling correction that produces the
// caller exclusions stays that scenario's only open window.
func TestWorkerJobCallerRepairContainmentAtAcceptance(t *testing.T) {
	worker := func(workID string) WorkflowActor {
		return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID + "-worker", ActorClass: ActorAgent}
	}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/acceptor", SessionRef: "session/caller-repair-acceptor", ActorClass: ActorAgent}

	seedScenarioWork := func(t *testing.T, workID string) (*Store, WorkflowActor) {
		t.Helper()
		fixture := seedHistoricalWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", 34, "repair")
		if !testWorkPinCallerCapable(t, fixture.store, workID) {
			t.Fatal("the fixture pinned no caller-capable definition")
		}
		// A sibling job dispatched and failed first: its recorded scope is
		// the correction this work's implementation dispatches carry as
		// caller exclusions.
		sibling := &WorkerJobBinding{JobID: "job:caller-sibling", Revision: 1}
		recordWorkerJobRevisionForTest(t, fixture.store, workID, fixture.owner, sibling)
		siblingAttempt := "attempt:caller-sibling"
		dispatchJobBoundAttempt(t, fixture.store, workID, "repair", siblingAttempt, nil, worker(workID), issue1013Pin(t, fixture.store, workID).Version, "caller-sibling-dispatch-"+workID, sibling)
		s2WorkerFailedAttributedLive(t, fixture.store, workID, siblingAttempt, "lane fallback blocked before the model ran")
		return fixture.store, fixture.owner
	}

	dispatchScenarioJob := func(t *testing.T, s *Store, workID string, coordinator WorkflowActor, jobID string, withAllowance bool) (*WorkerJobBinding, string) {
		t.Helper()
		fields := callerRepairAllowanceFields(t, s, workID, jobID, withAllowance)
		if err := recordWorkerJobFieldsForTest(t, s, workID, coordinator, fields); err != nil {
			t.Fatalf("record job %s: %v", jobID, err)
		}
		job := &WorkerJobBinding{JobID: jobID, Revision: 1}
		views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range views {
			if view.Binding.JobID == jobID {
				*job = view.Binding
			}
		}
		attempt := "attempt:" + jobID
		dispatchJobBoundAttempt(t, s, workID, "repair", attempt, issue1013Pin(t, s, workID).Correction, worker(workID), issue1013Pin(t, s, workID).Version, "caller-dispatch-"+jobID, job)
		return job, attempt
	}

	repairs := func(owner, caller, changed string) []WorkerDirectCallerRepair {
		return []WorkerDirectCallerRepair{{
			Owner:  WorkerCallerMechanism{Path: owner, EntryPoint: "recorded job"},
			Caller: WorkerCallerMechanism{Path: caller, EntryPoint: "recorded job"},
			Edge:   "Use invokes Value", Exposing: "go test ./internal/store/ on the owner-only change",
			ChangedPaths: []string{changed},
			Checks: WorkerCallerRepairChecks{
				BaselinePass:  WorkerCallerRepairCheck{Observed: "ok"},
				OwnerOnlyFail: WorkerCallerRepairCheck{Observed: "compile error", InferredCause: "Use still passes no argument"},
				CombinedPass:  WorkerCallerRepairCheck{Observed: "ok"},
			},
		}}
	}

	t.Run("contained repair accepts", func(t *testing.T) {
		workID := "caller-repair-contained"
		s, coordinator := seedScenarioWork(t, workID)
		defer s.Close()
		job, attempt := dispatchScenarioJob(t, s, workID, coordinator, "job:caller-contained", true)
		completeJobBoundAttemptWithRepairs(t, s, workID, attempt, job, repairs("internal/store/job:caller-contained", "internal/store/job:caller-contained", "internal/store/job:caller-contained/worker.go"))
		if err := acceptCallerAttempt(t, s, workID, attempt, acceptor); err != nil {
			t.Fatalf("contained caller acceptance refused: %v", err)
		}
	})

	t.Run("wrong owner mechanism refuses", func(t *testing.T) {
		workID := "caller-repair-wrong-owner"
		s, coordinator := seedScenarioWork(t, workID)
		defer s.Close()
		job, attempt := dispatchScenarioJob(t, s, workID, coordinator, "job:caller-wrong-owner", true)
		bad := repairs("internal/store/job:caller-wrong-owner", "internal/store/job:caller-wrong-owner", "internal/store/job:caller-wrong-owner/worker.go")
		bad[0].Owner.EntryPoint = "AnotherOwner"
		completeJobBoundAttemptWithRepairs(t, s, workID, attempt, job, bad)
		err := acceptCallerAttempt(t, s, workID, attempt, acceptor)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "which is not a mechanism of its direct-caller allowance") {
			t.Fatalf("wrong-owner acceptance = %v, want the allowance-mechanism refusal", err)
		}
	})

	t.Run("excluded path refuses", func(t *testing.T) {
		workID := "caller-repair-excluded"
		s, coordinator := seedScenarioWork(t, workID)
		defer s.Close()
		job, attempt := dispatchScenarioJob(t, s, workID, coordinator, "job:caller-excluded", true)
		completeJobBoundAttemptWithRepairs(t, s, workID, attempt, job, repairs("internal/store/job:caller-excluded", "internal/store/job:caller-sibling", "internal/store/job:caller-sibling/worker.go"))
		err := acceptCallerAttempt(t, s, workID, attempt, acceptor)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "excluded as another correction's recorded path") {
			t.Fatalf("excluded-path acceptance = %v, want the caller-exclusions refusal", err)
		}
	})

	t.Run("repairs without an allowance refuse", func(t *testing.T) {
		workID := "caller-repair-no-allowance"
		s, coordinator := seedScenarioWork(t, workID)
		defer s.Close()
		job, attempt := dispatchScenarioJob(t, s, workID, coordinator, "job:caller-no-allowance", false)
		completeJobBoundAttemptWithRepairs(t, s, workID, attempt, job, repairs("internal/store/job:caller-no-allowance", "internal/store/job:caller-no-allowance", "internal/store/job:caller-no-allowance/worker.go"))
		err := acceptCallerAttempt(t, s, workID, attempt, acceptor)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "records no direct-caller allowance") {
			t.Fatalf("no-allowance acceptance = %v, want the missing-allowance refusal", err)
		}
	})

	t.Run("foreign-owned path refuses", func(t *testing.T) {
		workID := "caller-repair-foreign"
		s, coordinator := seedScenarioWork(t, workID)
		defer s.Close()
		// Another work item in the same Project holds a path claim over the
		// path this item's repair claims.
		const otherWork = "caller-repair-foreign-owner"
		seedWork(t, s, otherWork)
		ctx := context.Background()
		foreignPath := "internal/store/foreign-scope/worker.go"
		version := readWorkVersion(t, s, otherWork)
		resulting := version + 1
		claim := resourceClaimedPayload{ResourceKey: "path:" + foreignPath, Reason: "another item's held path claim", HolderAgent: "agent/other", HolderSession: "session/other"}
		claim.WorkflowVersionFields = WorkflowVersionFields{WorkID: otherWork, ExpectedVersion: &version, ResultingVersion: &resulting}
		event := Event{EventID: "claim-foreign-scope", Kind: "work.resource_claimed", SubjectType: SubjectWorkItem, SubjectID: otherWork, Actor: "worker:test", OccurredAt: time.Unix(19, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(claim)}
		if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, otherWork): version}}); err != nil {
			t.Fatalf("record the foreign item's path claim: %v", err)
		}

		job, attempt := dispatchScenarioJob(t, s, workID, coordinator, "job:caller-foreign", true)
		claimed := repairs("internal/store/job:caller-foreign", foreignPath, foreignPath)
		claimed[0].Caller.EntryPoint = "Use"
		completeJobBoundAttemptWithRepairs(t, s, workID, attempt, job, claimed)
		err := acceptCallerAttempt(t, s, workID, attempt, acceptor)
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidOperation || !strings.Contains(failure.Detail, "holds through claim path:") {
			t.Fatalf("foreign-path acceptance = %v, want the foreign-owner refusal", err)
		}
	})
}
