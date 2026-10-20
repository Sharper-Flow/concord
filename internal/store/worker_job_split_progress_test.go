package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// splitProgressRecordDispatchAuthorization records the dispatch_worker start
// and completion the recorded-history route needs, carrying the actor_ref the
// live failure ingest reads when it derives the failure record. The epoch
// opens the step's next fenced attempt, exactly as a real authorization does.
func splitProgressRecordDispatchAuthorization(t *testing.T, s *Store, workID, attemptID string, job WorkerJobBinding, actor WorkflowActor) int64 {
	t.Helper()
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	epoch := latestStepStartEpoch(t, s, workID, "repair") + 1
	var directory string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, WorktreeSetID(workID)).Scan(&directory); err != nil {
		t.Fatalf("read the claimed worktree of %s: %v", workID, err)
	}
	started := map[string]any{"work_id": workID, "action_id": "dispatch_worker", "step_id": "repair", "attempt_epoch": epoch}
	authorization := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"action_id": "dispatch_worker", "step_id": "repair", "attempt_epoch": epoch,
		"worker_attempt_id": attemptID, "actor_ref": actorRef,
		"worker_capability_class":  "implementation",
		"worker_job":               map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest},
		"worker_packet_digest":     "sha256:" + strings.Repeat("e", 64),
		"worker_worktree_identity": workerWorktreeIdentity(filepath.Clean(directory)),
	}
	tx, txErr := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if txErr != nil {
		t.Fatal(txErr)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_started', 'work_item', ?, ?, ?, 2, ?), (?, 'workflow.action_completed', 'work_item', ?, ?, ?, 2, ?)`,
		workID+":split-progress-started:"+attemptID, workID, actorRef, "2026-10-19T00:00:00Z", string(mustJSONValue(started)),
		workID+":split-progress-authorization:"+attemptID, workID, actorRef, "2026-10-19T00:00:00Z", string(mustJSONValue(authorization))); err != nil {
		t.Fatalf("record the dispatch authorization of %s: %v", attemptID, err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// splitProgressRawFixture raises the non-progress wall with three failed
// predecessor attempts, then records the ordinary split into successor-a and
// successor-b, so an accepted leaf stands behind an escalated wall.
func splitProgressRawFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	t.Helper()
	fixture := workerJobSizeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := recordSplitPredecessor(t, workID, fixture)
	for n := 1; n <= 3; n++ {
		attempt := fmt.Sprintf("attempt:%s:%d", workID, n)
		splitProgressRecordDispatchAuthorization(t, s, workID, attempt, predecessor, fixture.owner)
		if err := appendJobBoundWorkerDispatch(t, s, workID, attempt, &predecessor); err != nil {
			t.Fatalf("record the wall attempt %d: %v", n, err)
		}
		s2WorkerFailedAttributedLive(t, s, workID, attempt, "the bounded job attempt failed before delivering a measurable tree")
	}
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitFields(t, workID, fixture, predecessor, nil)); err != nil {
		t.Fatalf("ordinary split refused: %v", err)
	}
	return fixture
}

// splitProgressAcceptLeaf records one implementation-lane dispatch of the
// named successor through the recorded-history route, completes it with the
// caller's production count, and accepts the result through the real accept
// action. blocker mints one unresolved delivery-blocker finding on the
// leaf's own report. It returns the acceptance completion's seq.
func splitProgressAcceptLeaf(t *testing.T, route workflowReturnRouteFixture, workID, base, jobID, path string, count int64, blocker bool) int64 {
	t.Helper()
	s := workerJobSizeStore(route)
	binding := latestSplitBinding(t, route, workID, jobID)
	attempt := "attempt:" + workID + ":" + strings.ReplaceAll(strings.TrimPrefix(jobID, "job:"), ":", "-")
	epoch := splitProgressRecordDispatchAuthorization(t, s, workID, attempt, binding, route.owner)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attempt, &binding); err != nil {
		t.Fatalf("record the implementation dispatch of %s: %v", jobID, err)
	}
	tree := strings.Repeat("4", 40)
	completed := reportBindingCompletion(t, s, workID, attempt, reviewGateLane(t, "implementation"), &binding, func(payload *WorkerCompletedPayload) {
		payload.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, State: "clean", Base: base, Paths: []WorkerObservedPath{{Path: path, Added: count}}}
		payload.ProductionSize = &WorkerProductionSize{Base: base, Tree: tree, Count: count}
		if blocker {
			payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
				Severity: "P1", Confidence: "high", Detail: "the leaf's fixture control fails on the delivered tree",
				Oracle: &WorkerOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:fixture", FailureFamily: "path-a", ControlIDs: []string{"control:fixture"}, EvidenceRefs: []string{"evidence:repro"}},
			}}}
		}
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("record the completed report of %s: %v", jobID, err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}), 0, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("accept %s: %v", jobID, err)
	}
	var seq int64
	if err := s.db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

// splitProgressRejectLeaf disposes one completed sibling attempt with a
// recorded rejection, so a later rejection stands after the acceptance.
func splitProgressRejectLeaf(t *testing.T, route workflowReturnRouteFixture, workID, base, jobID, path string) {
	t.Helper()
	s := workerJobSizeStore(route)
	binding := latestSplitBinding(t, route, workID, jobID)
	attempt := "attempt:" + workID + ":" + strings.ReplaceAll(strings.TrimPrefix(jobID, "job:"), ":", "-")
	epoch := splitProgressRecordDispatchAuthorization(t, s, workID, attempt, binding, route.owner)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attempt, &binding); err != nil {
		t.Fatalf("record the implementation dispatch of %s: %v", jobID, err)
	}
	tree := strings.Repeat("5", 40)
	completed := reportBindingCompletion(t, s, workID, attempt, reviewGateLane(t, "implementation"), &binding, func(payload *WorkerCompletedPayload) {
		payload.ObservedDelivery = &WorkerObservedDelivery{Commit: tree, State: "clean", Base: base, Paths: []WorkerObservedPath{{Path: path, Added: 3}}}
		payload.ProductionSize = &WorkerProductionSize{Base: base, Tree: tree, Count: 3}
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("record the completed report of %s: %v", jobID, err)
	}
	rejection := map[string]any{
		"work_id": workID, "expected_version": readWorkVersion(t, s, workID), "resulting_version": readWorkVersion(t, s, workID) + 1,
		"action_id": "reject_worker_result", "step_id": "repair", "attempt_epoch": epoch,
		"worker_attempt_id": attempt, "worker_job": map[string]any{"job_id": binding.JobID, "revision": binding.Revision, "digest": binding.Digest},
		"diagnosis": "the delivered tree does not satisfy the approved predicate",
		"strategy":  "retry the bounded job from its recorded base",
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_completed', 'work_item', ?, 'principal/operator', '2026-10-19T00:00:00Z', 2, ?)`,
		workID+":split-progress-rejection:"+attempt, workID, string(mustJSONValue(rejection))); err != nil {
		t.Fatalf("record the rejection of %s: %v", jobID, err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func splitProgressBasis(t *testing.T, fixture workflowReturnRouteFixture, workID string) WorkflowRetryConvergence {
	t.Helper()
	convergence, err := workflowRetryConvergence(context.Background(), workerJobSizeStore(fixture).db, workID, "repair")
	if err != nil {
		t.Fatalf("derive convergence: %v", err)
	}
	return convergence
}

// splitProgressThreeLeafFixture records a three-path capped predecessor,
// raises the wall, and splits it into three leaves, returning the b and c
// bindings beside the fixture.
func splitProgressThreeLeafFixture(t *testing.T, workID string) (workflowReturnRouteFixture, WorkerJobBinding, WorkerJobBinding) {
	t.Helper()
	fixture := workerJobSizeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := WorkerJobBinding{JobID: splitPredecessorJobID, Revision: 1}
	fields := workerJobSizeEditingFields(t, workID, fixture)
	fields["job_id"] = splitPredecessorJobID
	fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
		{"path": "internal/store/worker_jobs.go", "class": "production"},
		{"path": "internal/store/worker_oracle.go", "class": "production"},
		{"path": "internal/store/workflow.go", "class": "production"},
	}
	recordEditingJobForTest(t, fixture, workID, fields, &predecessor)
	for n := 1; n <= 3; n++ {
		attempt := fmt.Sprintf("attempt:%s:%d", workID, n)
		splitProgressRecordDispatchAuthorization(t, s, workID, attempt, predecessor, fixture.owner)
		if err := appendJobBoundWorkerDispatch(t, s, workID, attempt, &predecessor); err != nil {
			t.Fatalf("record the wall attempt %d: %v", n, err)
		}
		s2WorkerFailedAttributedLive(t, s, workID, attempt, "the bounded job attempt failed before delivering a measurable tree")
	}
	split := splitFields(t, workID, fixture, predecessor, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		successors = append(successors, splitSuccessorFields(t, workID, fixture, "job:successor-c", "internal/store/workflow.go"))
		allocation = append(allocation, map[string]any{"job_id": "job:successor-c", "paths": []string{"internal/store/workflow.go"}, "evidence_refs": []string{"evidence:split-c"}})
		return successors, allocation
	})
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, split); err != nil {
		t.Fatalf("three-leaf split refused: %v", err)
	}
	return fixture, latestSplitBinding(t, fixture, workID, "job:successor-b"), latestSplitBinding(t, fixture, workID, "job:successor-c")
}

// A previously dispatched but still-unsatisfied sibling must not discard the
// funding: the derivation skips it and the untouched sibling stays the funded
// target.
func TestWorkerJobSplitProgressSkipsDispatchedSiblingForUntouchedTarget(t *testing.T) {
	const workID = "split-progress-dispatched-sibling"
	fixture, bindingB, bindingC := splitProgressThreeLeafFixture(t, workID)
	s := workerJobSizeStore(fixture)
	attemptB := "attempt:" + workID + ":leaf-b"
	splitProgressRecordDispatchAuthorization(t, s, workID, attemptB, bindingB, fixture.owner)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptB, &bindingB); err != nil {
		t.Fatalf("record the dispatched sibling: %v", err)
	}
	s2WorkerFailedAttributedLive(t, s, workID, attemptB, "the dispatched sibling failed before delivering a measurable tree")
	acceptanceSeq := splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)

	basis := splitProgressBasis(t, fixture, workID)
	if basis.Basis != "split_leaf_progress" || basis.LatestRecordSeq != acceptanceSeq || !basis.valid() {
		t.Fatalf("basis = %+v, want split_leaf_progress consuming acceptance seq %d despite the dispatched sibling", basis, acceptanceSeq)
	}

	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	convergence := WorkflowRetryConvergence{Basis: "split_leaf_progress", LatestRecordSeq: acceptanceSeq}
	if guardErr := admitWorkerJobSplitProgressDispatchTx(context.Background(), tx, workID, convergence, &bindingC, BuiltinWorkflowRegistry()); guardErr != nil {
		t.Fatalf("untouched sibling dispatch = %v, want admission", guardErr)
	}
	if guardErr := admitWorkerJobSplitProgressDispatchTx(context.Background(), tx, workID, convergence, &bindingB, BuiltinWorkflowRegistry()); guardErr == nil || !strings.Contains(guardErr.Error(), "job:successor-c") || strings.Contains(guardErr.Error(), "job:successor-b,") {
		t.Fatalf("dispatched sibling dispatch = %v, want a refusal naming only the untouched sibling", guardErr)
	}
}

// splitProgressNestedFixture nests one split inside successor-a: leaves c and
// d replace a, while outer sibling b stays untouched. d ends dispatched and
// satisfied, c ends accepted under cap, so the accepted leaf's outermost
// undischarged lineage holds the untouched outer sibling.
func splitProgressNestedFixture(t *testing.T, workID string) (workflowReturnRouteFixture, WorkerJobBinding) {
	t.Helper()
	fixture := splitProgressRawFixture(t, workID)
	s := workerJobSizeStore(fixture)
	bindingA := latestSplitBinding(t, fixture, workID, "job:successor-a")
	nested := splitFields(t, workID, fixture, bindingA, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		successors[0]["job_id"], successors[1]["job_id"] = "job:leaf-c", "job:leaf-d"
		allocation[0]["job_id"], allocation[1]["job_id"] = "job:leaf-c", "job:leaf-d"
		return successors, allocation
	})
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, nested); err != nil {
		t.Fatalf("nested split refused: %v", err)
	}
	splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:leaf-d", "internal/store/worker_oracle.go", 1, false)
	splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:leaf-c", "internal/store/worker_jobs.go", 1, false)
	return fixture, latestSplitBinding(t, fixture, workID, "job:successor-b")
}

// The accepted leaf's outermost undischarged lineage owns the funding, so the
// untouched outer sibling stays fundable even though the nested lineage that
// also contains the accepted leaf is exhausted.
func TestWorkerJobSplitProgressFundsOuterSiblingAcrossNestedSplit(t *testing.T) {
	const workID = "split-progress-nested-outer"
	fixture, bindingB := splitProgressNestedFixture(t, workID)
	acceptanceSeq := int64(0)
	if err := workerJobSizeStore(fixture).db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='accept_worker_result' ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&acceptanceSeq); err != nil {
		t.Fatal(err)
	}
	basis := splitProgressBasis(t, fixture, workID)
	if basis.Basis != "split_leaf_progress" || basis.LatestRecordSeq != acceptanceSeq || !basis.valid() {
		t.Fatalf("basis = %+v, want split_leaf_progress from the outer lineage consuming acceptance seq %d", basis, acceptanceSeq)
	}
	tx, err := workerJobSizeStore(fixture).DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	convergence := WorkflowRetryConvergence{Basis: "split_leaf_progress", LatestRecordSeq: acceptanceSeq}
	if guardErr := admitWorkerJobSplitProgressDispatchTx(context.Background(), tx, workID, convergence, &bindingB, BuiltinWorkflowRegistry()); guardErr != nil {
		t.Fatalf("outer sibling dispatch = %v, want admission", guardErr)
	}
}

// The closed basis vocabulary carries split_leaf_progress beside the existing
// bases: the funding acceptance's sequence is the exact reference the one
// dispatch consumes.
func TestWorkerJobSplitProgressBasisShape(t *testing.T) {
	t.Parallel()
	if valid := (WorkflowRetryConvergence{Basis: "split_leaf_progress", LatestRecordSeq: 9}); !valid.valid() {
		t.Fatal("a funding acceptance reference does not validate")
	}
	for _, broken := range []WorkflowRetryConvergence{
		{Basis: "split_leaf_progress"},
		{Basis: "split_leaf_progress", LatestRecordSeq: 9, PreviousRecordSeq: 3},
		{Basis: "split_leaf_progress", LatestRecordSeq: 9, SupersededSeq: 2},
		{Basis: "split_leaf_progress", LatestRecordSeq: 9, HostPID: 4, HostPIDStart: 5},
	} {
		if broken.valid() {
			t.Fatalf("%+v validated as a split-progress basis", broken)
		}
	}
}

func TestWorkerJobSplitProgressDerivation(t *testing.T) {
	t.Run("an under-cap leaf acceptance funds one sibling", func(t *testing.T) {
		const workID = "split-progress-derives"
		fixture := splitProgressRawFixture(t, workID)
		acceptanceSeq := splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
		basis := splitProgressBasis(t, fixture, workID)
		if basis.Basis != "split_leaf_progress" || basis.LatestRecordSeq != acceptanceSeq || basis.PreviousRecordSeq != 0 || !basis.valid() {
			t.Fatalf("basis = %+v, want split_leaf_progress consuming acceptance seq %d", basis, acceptanceSeq)
		}
	})
	t.Run("an at-cap leaf acceptance funds nothing", func(t *testing.T) {
		const workID = "split-progress-at-cap"
		fixture := splitProgressRawFixture(t, workID)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 200, false)
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none for a count at the cap", basis)
		}
	})
	t.Run("unresolved blocking findings stop the route", func(t *testing.T) {
		const workID = "split-progress-blocked"
		fixture := splitProgressRawFixture(t, workID)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, true)
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none while a delivery blocker stands open", basis)
		}
	})
	t.Run("a later rejection stops the route", func(t *testing.T) {
		const workID = "split-progress-rejected"
		fixture := splitProgressRawFixture(t, workID)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
		splitProgressRejectLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-b", "internal/store/worker_oracle.go")
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none after the sibling rejection", basis)
		}
	})
	t.Run("a later failure stops the route", func(t *testing.T) {
		const workID = "split-progress-failed"
		fixture := splitProgressRawFixture(t, workID)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
		binding := latestSplitBinding(t, fixture, workID, "job:successor-b")
		attempt := "attempt:" + workID + ":leaf-b"
		splitProgressRecordDispatchAuthorization(t, workerJobSizeStore(fixture), workID, attempt, binding, fixture.owner)
		if err := appendJobBoundWorkerDispatch(t, workerJobSizeStore(fixture), workID, attempt, &binding); err != nil {
			t.Fatalf("record the sibling dispatch: %v", err)
		}
		s2WorkerFailedAttributedLive(t, workerJobSizeStore(fixture), workID, attempt, "the sibling attempt failed before delivering a measurable tree")
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none after the sibling failure", basis)
		}
	})
	t.Run("no never-dispatched sibling remains", func(t *testing.T) {
		const workID = "split-progress-exhausted"
		fixture := splitProgressRawFixture(t, workID)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-b", "internal/store/worker_oracle.go", 1, false)
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none once every leaf was dispatched", basis)
		}
	})
	t.Run("an accepted job outside any split lineage funds nothing", func(t *testing.T) {
		const workID = "split-progress-foreign"
		fixture := workerJobSizeFixture(t, workID)
		s := workerJobSizeStore(fixture)
		predecessor := recordSplitPredecessor(t, workID, fixture)
		for n := 1; n <= 3; n++ {
			attempt := fmt.Sprintf("attempt:%s:%d", workID, n)
			splitProgressRecordDispatchAuthorization(t, s, workID, attempt, predecessor, fixture.owner)
			if err := appendJobBoundWorkerDispatch(t, s, workID, attempt, &predecessor); err != nil {
				t.Fatalf("record the wall attempt %d: %v", n, err)
			}
			s2WorkerFailedAttributedLive(t, s, workID, attempt, "the bounded job attempt failed before delivering a measurable tree")
		}
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), splitPredecessorJobID, "internal/store/worker_jobs.go", 1, false)
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none without a split lineage", basis)
		}
	})
	t.Run("a voided leaf satisfaction funds nothing", func(t *testing.T) {
		const workID = "split-progress-voided"
		fixture := splitProgressRawFixture(t, workID)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
		db := workerJobSizeStore(fixture).db
		if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worker_job_revisions SET state='recorded', satisfied_result_ref='' WHERE work_id=? AND job_id='job:successor-a'; DELETE FROM fold_guard`, workID); err != nil {
			t.Fatal(err)
		}
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none once the leaf's satisfaction is voided", basis)
		}
	})
	t.Run("a later jobless acceptance is the latest disposition and funds nothing", func(t *testing.T) {
		const workID = "split-progress-jobless"
		fixture := splitProgressRawFixture(t, workID)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
		db := workerJobSizeStore(fixture).db
		jobless := mustJSONValue(map[string]any{
			"work_id": workID, "action_id": "accept_worker_result", "step_id": "repair", "attempt_epoch": int64(1),
			"changed_refs": []string{workID}, "result_evidence_refs": []string{},
		})
		if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?, 'workflow.action_completed', 'work_item', ?, 'principal/operator', '2026-10-19T01:00:00Z', 2, ?); DELETE FROM fold_guard`, workID+":split-progress-jobless-accept", workID, string(jobless)); err != nil {
			t.Fatal(err)
		}
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none once a jobless acceptance is the latest disposition", basis)
		}
	})
	t.Run("a leaf under superseded parent authority funds nothing", func(t *testing.T) {
		const workID = "split-progress-stale-authority"
		fixture := splitProgressRawFixture(t, workID)
		splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
		db := workerJobSizeStore(fixture).db
		if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worker_job_revisions SET contract_version=contract_version+1 WHERE work_id=? AND job_id='job:successor-a'; DELETE FROM fold_guard`, workID); err != nil {
			t.Fatal(err)
		}
		if basis := splitProgressBasis(t, fixture, workID); basis.Basis != "" {
			t.Fatalf("basis = %+v, want none for a stranded leaf authority", basis)
		}
	})
}

// The full flow on the real-Git journey fixture: behind an escalated wall,
// the accepted under-cap leaf funds exactly one real first dispatch of its
// never-dispatched sibling, and the recorded authorization consumes the
// basis even though no worker materialized.
func TestWorkerJobSplitProgressFundsFirstSiblingDispatch(t *testing.T) {
	const workID = "split-progress-funded-sibling"
	f, dir, base := sizeJourneyFixture(t, workID)
	route := workflowReturnRouteFixture{store: f.store, owner: f.owner}
	s := f.store

	predecessor := WorkerJobBinding{JobID: splitPredecessorJobID, Revision: 1}
	fields := workerJobSizeEditingFields(t, workID, route)
	fields["job_id"] = splitPredecessorJobID
	fields["production_size_max"] = 10
	fields["measurement_basis"].(map[string]any)["base_commit"] = base
	fields["measurement_basis"].(map[string]any)["paths"] = []map[string]any{
		{"path": "internal/store/worker_jobs.go", "class": "production"},
		{"path": "internal/store/worker_oracle.go", "class": "production"},
	}
	recordEditingJobForTest(t, route, workID, fields, &predecessor)

	for n := 1; n <= 3; n++ {
		attempt := fmt.Sprintf("attempt:%s:%d", workID, n)
		if err := sizeJourneyDispatch(t, f, dir, attempt, predecessor, nil); err != nil {
			t.Fatalf("wall attempt %d: %v", n, err)
		}
		if err := appendJobBoundWorkerDispatch(t, s, workID, attempt, &predecessor); err != nil {
			t.Fatalf("wall attempt %d evidence: %v", n, err)
		}
		s2WorkerFailedAttributedLive(t, s, workID, attempt, "the bounded job attempt failed before delivering a measurable tree")
	}

	binding := latestSplitBinding(t, route, workID, splitPredecessorJobID)
	split := splitFields(t, workID, route, binding, func(successors, allocation []map[string]any) ([]map[string]any, []map[string]any) {
		for _, successor := range successors {
			successor["production_size_max"] = 5
			successor["measurement_basis"].(map[string]any)["base_commit"] = base
		}
		return successors, allocation
	})
	if err := recordWorkerJobActionForTest(t, s, workID, f.owner, split); err != nil {
		t.Fatalf("split refused: %v", err)
	}

	acceptanceSeq := splitProgressAcceptLeaf(t, route, workID, base, "job:successor-a", "internal/store/worker_jobs.go", 1, false)

	bindingB := latestSplitBinding(t, route, workID, "job:successor-b")
	leafB := "attempt:" + workID + ":leaf-b"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	if err := sizeJourneyDispatch(t, f, dir, leafB, bindingB, nil); err != nil {
		t.Fatalf("sibling dispatch funded by the accepted leaf: %v", err)
	}

	var raw []byte
	if err := s.db.QueryRow(`SELECT json_extract(payload,'$.retry_convergence') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, workID, WorkflowActionCompleted, leafB).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var basis WorkflowRetryConvergence
	if json.Unmarshal(raw, &basis) != nil || basis.Basis != "split_leaf_progress" || basis.LatestRecordSeq != acceptanceSeq || !basis.valid() {
		t.Fatalf("recorded basis = %s, want split_leaf_progress consuming acceptance seq %d", raw, acceptanceSeq)
	}

	var dispatched int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerDispatched, leafB).Scan(&dispatched); err != nil || dispatched != 0 {
		t.Fatalf("sibling dispatch materialization = %d, %v; want none", dispatched, err)
	}
	convergence, err := workflowRetryConvergence(context.Background(), s.db, workID, "repair")
	if err != nil || convergence.Basis != "" {
		t.Fatalf("basis after the funded dispatch = %+v, %v; want the acceptance consumed without worker materialization", convergence, err)
	}
}
