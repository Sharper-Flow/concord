package store

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// splitStateFixture seeds the size fixture and records the ordinary split:
// the capped predecessor split into successor-a and successor-b, one
// allocated inventory path each.
func splitStateFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	fixture := workerJobSizeFixture(t, workID)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, splitFields(t, workID, fixture, predecessor, nil)); err != nil {
		t.Fatalf("ordinary split refused: %v", err)
	}
	return fixture
}

// splitStatesForTest derives the work's split states through the reader
// under test.
func splitStatesForTest(t *testing.T, fixture workflowReturnRouteFixture, workID string) []workerJobSplitState {
	t.Helper()
	states, err := readWorkerJobSplitStatesTx(context.Background(), workerJobSizeStore(fixture).db, workID)
	if err != nil {
		t.Fatalf("read split states: %v", err)
	}
	return states
}

// splitStateSplitEventID reads the one recorded split's event id.
func splitStateSplitEventID(t *testing.T, fixture workflowReturnRouteFixture, workID string) string {
	t.Helper()
	var eventID string
	if err := workerJobSizeStore(fixture).db.QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkerJobSplit).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	return eventID
}

// splitStateJobIDs projects the leaves' job identities.
func splitStateJobIDs(leaves []WorkerJobRevisionView) []string {
	ids := []string{}
	for _, leaf := range leaves {
		ids = append(ids, leaf.Binding.JobID)
	}
	return ids
}

// insertSplitStateEvent appends one raw event beside the recorded log
// through the fold-guard route, the path a damaged or pre-fold log reaches
// the derivation through.
func insertSplitStateEvent(t *testing.T, fixture workflowReturnRouteFixture, workID, eventID, kind, payload string) {
	t.Helper()
	tx, err := workerJobSizeStore(fixture).DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item',?,'principal/operator','2026-10-19T00:00:00Z',1,?)`, eventID, kind, workID, payload); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// splitStateAcceptLeaf records one implementation-lane dispatch of the named
// successor through the recorded-history route and accepts its result, so
// the fold satisfies the leaf revision the derivation reads.
func splitStateAcceptLeaf(t *testing.T, fixture workflowReturnRouteFixture, workID, attemptID string, job WorkerJobBinding) {
	t.Helper()
	s := workerJobSizeStore(fixture)
	recordSizeDispatchAuthorization(t, s, workID, attemptID, job)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, &job); err != nil {
		t.Fatalf("record the implementation dispatch of %s: %v", attemptID, err)
	}
	tree := strings.Repeat("4", 40)
	base := testJobSizeBaseCommit()
	completed := reportBindingCompletion(t, s, workID, attemptID, reviewGateLane(t, "implementation"), &job, func(payload *WorkerCompletedPayload) {
		payload.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, State: "clean", Base: base, Paths: []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 1}}}
		payload.ProductionSize = &WorkerProductionSize{Base: base, Tree: tree, Count: 1}
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("record the completed report of %s: %v", attemptID, err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": int64(1)}), 0, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("accept %s: %v", attemptID, err)
	}
}

// splitStateDischargePayload authors one discharge payload naming the split
// event and the given predecessor binding.
func splitStateDischargePayload(t *testing.T, splitID string, predecessor WorkerJobBinding) string {
	t.Helper()
	return string(mustJSONValue(map[string]any{
		"split_id":    splitID,
		"predecessor": map[string]any{"job_id": predecessor.JobID, "revision": predecessor.Revision, "digest": predecessor.Digest},
	}))
}

// An ordinary split derives both successors as required leaves, unsatisfied
// until each accepts; a satisfied leaf keeps its state and result reference,
// and the discharge id stays empty until one is recorded.
func TestWorkerJobSplitStatesDeriveRequiredLeaves(t *testing.T) {
	const workID = "worker-job-split-state-leaves"
	fixture := splitStateFixture(t, workID)

	states := splitStatesForTest(t, fixture, workID)
	if len(states) != 1 {
		t.Fatalf("states = %d, want the one ordinary split", len(states))
	}
	if states[0].SplitID != splitStateSplitEventID(t, fixture, workID) || states[0].DischargeID != "" {
		t.Fatalf("state identity = %q with discharge %q, want the split event id and no discharge", states[0].SplitID, states[0].DischargeID)
	}
	if got := splitStateJobIDs(states[0].Leaves); !slices.Equal(got, []string{"job:successor-a", "job:successor-b"}) {
		t.Fatalf("leaves = %v, want both successors", got)
	}
	want := []string{workerJobKey("job:successor-a", 1), workerJobKey("job:successor-b", 1)}
	if !slices.Equal(states[0].Unsatisfied, want) {
		t.Fatalf("unsatisfied = %v, want %v", states[0].Unsatisfied, want)
	}

	splitStateAcceptLeaf(t, fixture, workID, "attempt:split-state-a", latestSplitBinding(t, fixture, workID, "job:successor-a"))

	states = splitStatesForTest(t, fixture, workID)
	if !slices.Equal(states[0].Unsatisfied, []string{workerJobKey("job:successor-b", 1)}) {
		t.Fatalf("unsatisfied after the acceptance = %v, want only successor-b", states[0].Unsatisfied)
	}
	for _, leaf := range states[0].Leaves {
		if leaf.Binding.JobID == "job:successor-a" && (leaf.State != "satisfied" || leaf.SatisfiedResultRef == "") {
			t.Fatalf("accepted leaf = %+v, want satisfied with a nonempty result reference", leaf)
		}
	}
}

func TestWorkerJobSplitStatesDoNotTreatStaleAuthorityAsSatisfied(t *testing.T) {
	const workID = "worker-job-split-state-authority"
	fixture := splitStateFixture(t, workID)
	splitStateAcceptLeaf(t, fixture, workID, "attempt:split-state-authority", latestSplitBinding(t, fixture, workID, "job:successor-a"))
	db := workerJobSizeStore(fixture).db
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worker_job_revisions SET contract_version=contract_version+1 WHERE work_id=? AND job_id='job:successor-a'; DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
	states := splitStatesForTest(t, fixture, workID)
	if !slices.Equal(states[0].Unsatisfied, []string{"job:successor-a|1", "job:successor-b|1"}) {
		t.Fatalf("foreign-authority satisfied projection counted as split satisfaction: %v", states[0].Unsatisfied)
	}
}

// A leaf is the successor's latest recorded revision, not the revision the
// split binding named, and the derivation folds the log to the same states.
func TestWorkerJobSplitStatesUseLatestSuccessorRevision(t *testing.T) {
	const workID = "worker-job-split-state-latest"
	fixture := splitStateFixture(t, workID)
	s := workerJobSizeStore(fixture)
	narrowed := splitSuccessorFields(t, workID, fixture, "job:successor-a", "internal/store/worker_jobs.go")
	narrowed["path_scope"] = []string{"internal/store/worker_jobs.go"}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, narrowed); err != nil {
		t.Fatalf("successor revision 2 refused: %v", err)
	}
	states := splitStatesForTest(t, fixture, workID)
	want := []string{workerJobKey("job:successor-a", 2), workerJobKey("job:successor-b", 1)}
	if !slices.Equal(states[0].Unsatisfied, want) {
		t.Fatalf("unsatisfied = %v, want successor-a at its latest revision 2", states[0].Unsatisfied)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild from the log: %v", err)
	}
	if rebuilt := splitStatesForTest(t, fixture, workID); !reflect.DeepEqual(states, rebuilt) {
		t.Fatalf("rebuilt states differ:\nlive:    %+v\nrebuilt: %+v", states, rebuilt)
	}
}

// A successor a later split replaced contributes that split's successors;
// the nested split derives only its own leaves.
func TestWorkerJobSplitStatesReplaceNestedSuccessors(t *testing.T) {
	const workID = "worker-job-split-state-nested"
	fixture := splitStateFixture(t, workID)
	s := workerJobSizeStore(fixture)
	intermediate := latestSplitBinding(t, fixture, workID, "job:successor-a")
	nested := splitFields(t, workID, fixture, intermediate, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		successors[0]["job_id"], successors[1]["job_id"] = "job:leaf-c", "job:leaf-d"
		allocation[0]["job_id"], allocation[1]["job_id"] = "job:leaf-c", "job:leaf-d"
		return successors, allocation
	})
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, nested); err != nil {
		t.Fatalf("nested split refused: %v", err)
	}
	states := splitStatesForTest(t, fixture, workID)
	if len(states) != 2 {
		t.Fatalf("states = %d, want the outer and the nested split", len(states))
	}
	if got := splitStateJobIDs(states[0].Leaves); !slices.Equal(got, []string{"job:leaf-c", "job:leaf-d", "job:successor-b"}) {
		t.Fatalf("outer leaves = %v, want the nested successors beside the unreplaced successor", got)
	}
	if got := splitStateJobIDs(states[1].Leaves); !slices.Equal(got, []string{"job:leaf-c", "job:leaf-d"}) {
		t.Fatalf("nested leaves = %v, want the nested split's own successors", got)
	}
	want := []string{workerJobKey("job:leaf-c", 1), workerJobKey("job:leaf-d", 1), workerJobKey("job:successor-b", 1)}
	if !slices.Equal(states[0].Unsatisfied, want) {
		t.Fatalf("outer unsatisfied = %v, want %v", states[0].Unsatisfied, want)
	}
	if !slices.Equal(states[1].Unsatisfied, want[:2]) {
		t.Fatalf("nested unsatisfied = %v, want %v", states[1].Unsatisfied, want[:2])
	}
}

// A missing leaf revision and a cyclic required-leaf walk refuse as typed
// invariant failures instead of silently omitting a leaf.
func TestWorkerJobSplitStatesRefuseMissingLeafAndCycle(t *testing.T) {
	t.Run("missing leaf revision", func(t *testing.T) {
		const workID = "worker-job-split-state-missing"
		fixture := splitStateFixture(t, workID)
		s := workerJobSizeStore(fixture)
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := enterFold(context.Background(), tx); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM worker_job_revisions WHERE work_id=? AND job_id='job:successor-a'`, workID); err != nil {
			t.Fatal(err)
		}
		if err := leaveFold(context.Background(), tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		_, err = readWorkerJobSplitStatesTx(context.Background(), s.db, workID)
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation || !strings.Contains(err.Error(), "required leaf job job:successor-a holds no recorded revision") {
			t.Fatalf("missing leaf error = %v, want the invariant refusal", err)
		}
	})
	t.Run("cycle", func(t *testing.T) {
		const workID = "worker-job-split-state-cycle"
		fixture := splitStateFixture(t, workID)
		s := workerJobSizeStore(fixture)
		binding := map[string]WorkerJobBinding{}
		views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range views {
			binding[view.Binding.JobID] = view.Binding
		}
		raw := string(mustJSONValue(map[string]any{
			"predecessor": binding["job:successor-b"],
			"successors":  []WorkerJobBinding{binding["job:successor-a"]},
			"allocation":  []WorkerJobSplitAllocation{{JobID: "job:successor-a", Paths: []string{"internal/store/worker_jobs.go"}, EvidenceRefs: []string{"evidence:poison"}}},
		}))
		insertSplitStateEvent(t, fixture, workID, "split-state-poison", WorkerJobSplit, raw)
		_, err = readWorkerJobSplitStatesTx(context.Background(), s.db, workID)
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation || !strings.Contains(err.Error(), "required leaves cycle through job job:successor-a") {
			t.Fatalf("cycle error = %v, want the invariant refusal", err)
		}
	})
}

// A discharge joins only the exact split event id with the exact
// predecessor binding: once per split, never a foreign split or foreign
// predecessor.
func TestWorkerJobSplitStatesJoinExactDischarge(t *testing.T) {
	t.Run("exact join once", func(t *testing.T) {
		const workID = "worker-job-split-state-discharge"
		fixture := splitStateFixture(t, workID)
		splitID := splitStateSplitEventID(t, fixture, workID)
		insertSplitStateEvent(t, fixture, workID, "discharge-exact", "worker.job_split_discharged", splitStateDischargePayload(t, splitID, latestSplitBinding(t, fixture, workID, splitPredecessorJobID)))
		states := splitStatesForTest(t, fixture, workID)
		if states[0].DischargeID != "discharge-exact" {
			t.Fatalf("discharge id = %q, want the one matching discharge event", states[0].DischargeID)
		}
	})
	for _, refusal := range []struct {
		name, payload, want string
	}{
		{"duplicate discharge", "", "records a second discharge for split"},
		{"foreign split", `"split:unknown"`, "which the work holds no record of"},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			workID := "worker-job-split-state-discharge-" + strings.ReplaceAll(refusal.name, " ", "-")
			fixture := splitStateFixture(t, workID)
			splitID := splitStateSplitEventID(t, fixture, workID)
			predecessor := latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
			insertSplitStateEvent(t, fixture, workID, "discharge-first", "worker.job_split_discharged", splitStateDischargePayload(t, splitID, predecessor))
			second := splitStateDischargePayload(t, splitID, predecessor)
			if refusal.name == "foreign split" {
				second = `{"split_id":` + refusal.payload + `,"predecessor":{"job_id":"` + predecessor.JobID + `","revision":` + strconv.FormatInt(predecessor.Revision, 10) + `,"digest":"` + predecessor.Digest + `"}}`
			}
			insertSplitStateEvent(t, fixture, workID, "discharge-second", "worker.job_split_discharged", second)
			_, err := readWorkerJobSplitStatesTx(context.Background(), workerJobSizeStore(fixture).db, workID)
			if err == nil || !strings.Contains(err.Error(), refusal.want) {
				t.Fatalf("%s error = %v, want %q", refusal.name, err, refusal.want)
			}
		})
	}
	t.Run("foreign predecessor", func(t *testing.T) {
		const workID = "worker-job-split-state-discharge-foreign-predecessor"
		fixture := splitStateFixture(t, workID)
		splitID := splitStateSplitEventID(t, fixture, workID)
		stranger := latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
		stranger.Digest = "sha256:" + strings.Repeat("c", 64)
		insertSplitStateEvent(t, fixture, workID, "discharge-stranger", "worker.job_split_discharged", splitStateDischargePayload(t, splitID, stranger))
		_, err := readWorkerJobSplitStatesTx(context.Background(), workerJobSizeStore(fixture).db, workID)
		if err == nil || !strings.Contains(err.Error(), "which the discharged split did not record") {
			t.Fatalf("foreign predecessor error = %v, want the exact-binding refusal", err)
		}
	})
}
