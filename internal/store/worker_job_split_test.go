package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const splitPredecessorJobID = "job:predecessor"

// splitPredecessorFields authors the capped predecessor: two inventory
// paths under one scope entry, cap 400.
func splitPredecessorFields(t *testing.T, workID string, fixture workflowReturnRouteFixture) map[string]any {
	t.Helper()
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["job_id"] = splitPredecessorJobID
	fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
		{"path": "internal/store/worker_jobs.go", "class": "production"},
		{"path": "internal/store/worker_oracle.go", "class": "production"},
	}
	return fields
}

// splitSuccessorFields authors one successor that carries every predecessor
// obligation unchanged, under cap 200, inventorying one path.
func splitSuccessorFields(t *testing.T, workID string, fixture workflowReturnRouteFixture, jobID, path string) map[string]any {
	t.Helper()
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["job_id"] = jobID
	fields["production_size_max"] = 200
	fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{{"path": path, "class": "production"}}
	return fields
}

// changedSplitOracle authors the fixture oracle with one owner obligation
// changed and seeds the native preparation that oracle needs, so only the
// split's own byte-equality rule can refuse it.
func changedSplitOracle(t *testing.T, workID string, fixture workflowReturnRouteFixture) map[string]any {
	t.Helper()
	s := workerJobSizeStore(fixture)
	oracle := acceptanceOracleFieldsWithoutPreparationsForTest(t, s, workID)
	oracle["owners"].([]map[string]any)[0]["obligation"] = "a different obligation"
	seedOraclePreparationFieldsFixture(t, s, workID, oracle)
	return oracle
}

// recordSplitPredecessor records the predecessor and returns its binding.
func recordSplitPredecessor(t *testing.T, workID string, fixture workflowReturnRouteFixture) WorkerJobBinding {
	t.Helper()
	if err := recordWorkerJobActionForTest(t, workerJobSizeStore(fixture), workID, fixture.owner, splitPredecessorFields(t, workID, fixture)); err != nil {
		t.Fatalf("predecessor refused: %v", err)
	}
	return latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
}

func latestSplitBinding(t *testing.T, fixture workflowReturnRouteFixture, workID, jobID string) WorkerJobBinding {
	t.Helper()
	views, err := readWorkerJobRevisions(context.Background(), workerJobSizeStore(fixture).db, workID)
	if err != nil {
		t.Fatalf("read revisions: %v", err)
	}
	var binding WorkerJobBinding
	for _, view := range views {
		if view.Binding.JobID == jobID {
			binding = view.Binding
		}
	}
	if binding.JobID == "" {
		t.Fatalf("job %s is not recorded", jobID)
	}
	return binding
}

// splitFields assembles one split call: two successors, each allocated one
// predecessor inventory path.
func splitFields(t *testing.T, workID string, fixture workflowReturnRouteFixture, predecessor WorkerJobBinding, mutate func(successors []map[string]any, allocation []map[string]any) ([]map[string]any, []map[string]any)) map[string]any {
	t.Helper()
	successors := []map[string]any{
		splitSuccessorFields(t, workID, fixture, "job:successor-a", "internal/store/worker_jobs.go"),
		splitSuccessorFields(t, workID, fixture, "job:successor-b", "internal/store/worker_oracle.go"),
	}
	allocation := []map[string]any{
		{"job_id": "job:successor-a", "paths": []string{"internal/store/worker_jobs.go"}, "evidence_refs": []string{"evidence:split-a"}},
		{"job_id": "job:successor-b", "paths": []string{"internal/store/worker_oracle.go"}, "evidence_refs": []string{"evidence:split-b"}},
	}
	if mutate != nil {
		successors, allocation = mutate(successors, allocation)
	}
	return map[string]any{"split": map[string]any{
		"predecessor": map[string]any{"job_id": predecessor.JobID, "revision": predecessor.Revision, "digest": predecessor.Digest},
		"successors":  successors,
		"allocation":  allocation,
	}}
}

// A complete split records both successors and one split record in one
// action, replaces the predecessor for revision and dispatch, and folds to
// the same state on rebuild.
func TestWorkerJobSplitRecordsSuccessorsAndReplacesPredecessor(t *testing.T) {
	const workID = "worker-job-split-happy"
	fixture := workerJobSizeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, predecessor, nil)); err != nil {
		t.Fatalf("complete split refused: %v", err)
	}
	assertSplitState := func(label string) {
		t.Helper()
		views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
		if err != nil {
			t.Fatalf("%s: read revisions: %v", label, err)
		}
		ready := map[string]bool{}
		for _, view := range views {
			ready[view.Binding.JobID] = view.Ready
		}
		if len(views) != 3 || ready[splitPredecessorJobID] || !ready["job:successor-a"] || !ready["job:successor-b"] {
			t.Fatalf("%s: revisions = %+v, want a replaced predecessor and two ready successors", label, ready)
		}
		splits, err := readWorkerJobSplitsTx(context.Background(), s.db, workID)
		if err != nil || len(splits) != 1 {
			t.Fatalf("%s: splits = %+v, error = %v", label, splits, err)
		}
		if splits[0].Predecessor != predecessor || splits[0].BaseCommit != testJobSizeBaseCommit() || len(splits[0].Successors) != 2 {
			t.Fatalf("%s: split = %+v, want the predecessor binding, its base, and two successors", label, splits[0])
		}
		err = requireWorkerJobRevisionStateReadyTx(context.Background(), s.db, workID, predecessor)
		if err == nil || !strings.Contains(err.Error(), "replaced by a split") {
			t.Fatalf("%s: predecessor dispatch admission error = %v, want the replaced refusal", label, err)
		}
	}
	assertSplitState("live")
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild from the log: %v", err)
	}
	assertSplitState("rebuilt")

	revision := splitPredecessorFields(t, workID, fixture)
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, revision); err == nil || !strings.Contains(err.Error(), "is replaced by its split and cannot record another revision") {
		t.Fatalf("predecessor revision error = %v, want the replaced refusal", err)
	}
	again := splitFields(t, workID, fixture, predecessor, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		successors[0]["job_id"], successors[1]["job_id"] = "job:successor-c", "job:successor-d"
		allocation[0]["job_id"], allocation[1]["job_id"] = "job:successor-c", "job:successor-d"
		return successors, allocation
	})
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, again); err == nil || !strings.Contains(err.Error(), "an earlier split already replaced") {
		t.Fatalf("second split error = %v, want the already-replaced refusal", err)
	}
}

// Every refusal leaves no successor and no split recorded.
func TestWorkerJobSplitRefusals(t *testing.T) {
	const workID = "worker-job-split-refusals"
	fixture := workerJobSizeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	type edit = func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any)
	for _, refusal := range []struct {
		name     string
		mutate   edit
		wantText string
	}{
		{"successor cap above the predecessor", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			successors[0]["production_size_max"] = 401
			return successors, allocation
		}, "is not capped at or below the predecessor's cap 400"},
		{"dropped check", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			for _, successor := range successors {
				successor["checks"] = []string{"go vet ./internal/store/"}
			}
			return successors, allocation
		}, "no successor carries predecessor check go test ./internal/store/"},
		{"changed oracle", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			for _, successor := range successors {
				successor["acceptance_oracle"] = changedSplitOracle(t, workID, fixture)
			}
			return successors, allocation
		}, "no successor carries predecessor oracle owner:"},
		{"single successor", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			return successors[:1], allocation[:1]
		}, "split"},
		{"predecessor prerequisite", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			successors[1]["prerequisites"] = []map[string]any{{"job_id": splitPredecessorJobID, "revision": 1}}
			return successors, allocation
		}, "requires predecessor-lineage job job:predecessor"},
		{"sibling cycle", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			successors[0]["prerequisites"] = []map[string]any{{"job_id": "job:successor-b", "revision": 1}}
			successors[1]["prerequisites"] = []map[string]any{{"job_id": "job:successor-a", "revision": 1}}
			return successors, allocation
		}, "successor prerequisites cycle"},
		{"unallocated inventory path", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			return successors, allocation[:1]
		}, "leaves predecessor inventory path internal/store/worker_oracle.go unallocated"},
		{"allocation outside the successor scope", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			allocation[0]["paths"] = []string{"internal/store/worker_jobs.go", "adapter/opencode/packet.ts"}
			return successors, allocation
		}, "whose path_scope does not contain it"},
		{"successor names the predecessor", func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
			successors[1]["job_id"] = splitPredecessorJobID
			return successors, allocation
		}, "twice or as the predecessor"},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, predecessor, refusal.mutate))
			if err == nil || !strings.Contains(err.Error(), refusal.wantText) {
				t.Fatalf("%s error = %v, want text %q", refusal.name, err, refusal.wantText)
			}
		})
	}
	stale := predecessor
	stale.Revision = 2
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, stale, nil)); err == nil || !strings.Contains(err.Error(), "names stale predecessor revision 2") {
		t.Fatalf("stale predecessor error = %v, want the stale refusal", err)
	}
	beside := splitFields(t, workID, fixture, predecessor, nil)
	beside["objective"] = "an ordinary member beside the split"
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, beside); err == nil {
		t.Fatal("an ordinary member beside the split was admitted")
	}
	views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
	if err != nil || len(views) != 1 {
		t.Fatalf("revisions after refusals = %+v, error = %v; want only the predecessor", views, err)
	}
	if splits, err := readWorkerJobSplitsTx(context.Background(), s.db, workID); err != nil || len(splits) != 0 {
		t.Fatalf("splits after refusals = %+v, error = %v; want none", splits, err)
	}
}

// A later revision of a successor keeps every oracle item and allocated
// path the split assigned to it.
func TestWorkerJobSplitBindsSuccessorRevisions(t *testing.T) {
	const workID = "worker-job-split-successor-revision"
	fixture := workerJobSizeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	extra := splitFields(t, workID, fixture, predecessor, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		allocation[1]["paths"] = []string{"internal/store/worker_oracle.go", "internal/store/worker_lanes.go"}
		return successors, allocation
	})
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, extra); err != nil {
		t.Fatalf("complete split refused: %v", err)
	}
	narrowed := splitSuccessorFields(t, workID, fixture, "job:successor-a", "internal/store/worker_jobs.go")
	narrowed["path_scope"] = []string{"internal/store/worker_jobs.go"}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, narrowed); err != nil {
		t.Fatalf("successor revision keeping its allocated path refused: %v", err)
	}
	dropped := splitSuccessorFields(t, workID, fixture, "job:successor-b", "internal/store/worker_oracle.go")
	dropped["path_scope"] = []string{"internal/store/worker_oracle.go"}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, dropped); err == nil || !strings.Contains(err.Error(), "drops allocated path internal/store/worker_lanes.go") {
		t.Fatalf("successor revision dropping its allocated path error = %v, want the allocation refusal", err)
	}
	changed := splitSuccessorFields(t, workID, fixture, "job:successor-b", "internal/store/worker_oracle.go")
	changed["acceptance_oracle"] = changedSplitOracle(t, workID, fixture)
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, changed); err == nil || !strings.Contains(err.Error(), "drops or changes oracle item owner:") {
		t.Fatalf("successor revision changing its oracle error = %v, want the oracle refusal", err)
	}
	cyclic := splitSuccessorFields(t, workID, fixture, "job:successor-b", "internal/store/worker_oracle.go")
	cyclic["prerequisites"] = []map[string]any{{"job_id": splitPredecessorJobID, "revision": 1}}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, cyclic); err == nil || !strings.Contains(err.Error(), "requires predecessor-lineage job job:predecessor") {
		t.Fatalf("successor revision requiring its predecessor error = %v, want the lineage refusal", err)
	}
}

// The fold refuses a split record that names an unrecorded successor, so a
// damaged log cannot replay a split the action never authored.
func TestWorkerJobSplitFoldRefusesUnrecordedSuccessor(t *testing.T) {
	const workID = "worker-job-split-fold"
	fixture := workerJobSizeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	version := readWorkVersion(t, s, workID)
	resulting := version + 1
	ghost := WorkerJobBinding{JobID: "job:ghost", Revision: 1, Digest: "sha256:" + strings.Repeat("b", 64)}
	payload := WorkerJobSplitPayload{
		WorkflowVersionFields: WorkflowVersionFields{WorkID: workID, ExpectedVersion: &version, ResultingVersion: &resulting},
		Predecessor:           predecessor, Successors: []WorkerJobBinding{ghost, {JobID: "job:ghost-2", Revision: 1, Digest: ghost.Digest}},
		Allocation: []WorkerJobSplitAllocation{{JobID: "job:ghost", Paths: []string{"internal/store/worker_jobs.go"}, EvidenceRefs: []string{"evidence:ghost"}}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "split-ghost", Kind: WorkerJobSplit, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(29, 0).UTC(), PayloadVersion: 1, Payload: raw}
	err = applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
	if err == nil || !strings.Contains(err.Error(), "names job job:ghost revision 1, which is not recorded") {
		t.Fatalf("ghost split fold error = %v, want the unrecorded-successor refusal", err)
	}
}

func TestWorkerJobSplitRefusesUnsettledPredecessorAttempt(t *testing.T) {
	const workID = "worker-job-split-unsettled"
	fixture := workerJobSizeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	const attemptID = "attempt:split-unsettled"
	recordSizeDispatchAuthorization(t, s, workID, attemptID, predecessor)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, &predecessor); err != nil {
		t.Fatalf("dispatch the predecessor: %v", err)
	}
	for _, state := range []struct{ name, want string }{
		{"active", "while its attempt " + attemptID + " is active"},
		{"completed", "its completed attempt " + attemptID + " awaits disposition"},
	} {
		t.Run(state.name, func(t *testing.T) {
			if state.name == "completed" {
				if err := completeJobBoundAttemptForTest(s, workID, attemptID, reviewGateLane(t, "implementation"), &predecessor); err != nil {
					t.Fatalf("complete the predecessor: %v", err)
				}
			}
			if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, predecessor, nil)); err == nil || !strings.Contains(err.Error(), state.want) {
				t.Fatalf("split with %s attempt: %v, want %q", state.name, err, state.want)
			}
		})
	}
	if splits, err := readWorkerJobSplitsTx(context.Background(), s.db, workID); err != nil || len(splits) != 0 {
		t.Fatalf("splits after refusals = %+v, error = %v; want none", splits, err)
	}
}

func TestWorkerJobSplitReleasesCorrectionPathsForSuccessorDispatch(t *testing.T) {
	const workID = "worker-job-split-correction-paths"
	fixture := sizeDispatchFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	recordSizeDispatchAttempt(t, fixture, workID, "attempt:predecessor", predecessor)
	measured, err := workerJobCorrectionMeasuredTx(context.Background(), s.db, workID)
	if err != nil || len(measured[predecessor.JobID]) == 0 {
		t.Fatalf("correction before split = %+v, error = %v; want predecessor paths", measured, err)
	}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, predecessor, nil)); err != nil {
		t.Fatalf("split rejected predecessor: %v", err)
	}
	successor := latestSplitBinding(t, fixture, workID, "job:successor-a")
	lane := reviewGateLane(t, "implementation")
	packet := joinPacketFor(t, s, workID, "repair", "attempt:successor", lane.ID, lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = sizeDispatchPacketJob(t, s, workID, successor, nil)
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	if got, _, err := validateWorkerPacketJob(context.Background(), s.db, definition, workID, lane, mustJSONValue(packet)); err != nil || got == nil || *got != successor {
		t.Fatalf("successor packet admission = %+v, error = %v; want successor binding", got, err)
	}
	measured, err = workerJobCorrectionMeasuredTx(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := measured[predecessor.JobID]; present {
		t.Fatalf("replaced predecessor remains in correction: %+v", measured)
	}
}

func TestWorkerJobSplitSuccessorRequiresOracleAtPreflight(t *testing.T) {
	const workID = "worker-job-split-oracle-preflight"
	fixture := workerJobSizeFixture(t, workID)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	fields := splitFields(t, workID, fixture, predecessor, nil)
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	if err := validateWorkflowActionPayload(definition, "record_worker_job", mustJSONValue(fields)); err != nil {
		t.Fatalf("complete split preflight: %v", err)
	}
	successors := fields["split"].(map[string]any)["successors"].([]map[string]any)
	delete(successors[0], "acceptance_oracle")
	if err := validateWorkflowActionPayload(definition, "record_worker_job", mustJSONValue(fields)); err == nil {
		t.Fatal("split preflight admitted an oracle-free successor")
	}
}

func TestWorkerJobSplitFoldRejectsInventedBaseAndUnrelatedSuccessors(t *testing.T) {
	const workID = "worker-job-split-fold-evidence"
	fixture := workerJobSizeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	var successors []WorkerJobBinding
	for _, jobID := range []string{"job:ordinary-a", "job:ordinary-b"} {
		fields := splitSuccessorFields(t, workID, fixture, jobID, "internal/store/worker_jobs.go")
		if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, fields); err != nil {
			t.Fatal(err)
		}
		successors = append(successors, latestSplitBinding(t, fixture, workID, jobID))
	}
	for _, tc := range []struct{ name, base, want string }{
		{"invented-base", strings.Repeat("b", 40), "does not match the predecessor's recorded measurement basis"},
		{"ordinary-successors", testJobSizeBaseCommit(), "successor was not recorded by the same split action"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := readWorkVersion(t, s, workID)
			event := workflowTypedEvent("split-"+tc.name, WorkerJobSplit, workID, "worker:test", time.Unix(29, 0).UTC(), version, map[string]any{
				"predecessor": predecessor, "base_commit": tc.base, "successors": successors,
				"allocation": []WorkerJobSplitAllocation{{JobID: successors[0].JobID, Paths: []string{"internal/store/worker_jobs.go"}, EvidenceRefs: []string{"evidence:allocation"}}},
			})
			err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("fold error = %v, want %q", err, tc.want)
			}
		})
	}
}
