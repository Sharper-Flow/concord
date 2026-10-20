package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The split consumers (CD-0225 D3): the inclusive-prefix correction walk
// consumes the exact recorded split discharge, and delivery requires the
// latest transitive required leaves beside every split's recorded
// discharge, excluding the replaced predecessors.

// splitConsumerDischarge inserts one raw discharge event for the split
// whose predecessor job it names and returns its sequence, so a consumer
// case controls the discharge record in the log without the full
// integration action.
func splitConsumerDischarge(t *testing.T, fixture workflowReturnRouteFixture, workID, predecessorJobID, eventID string) int64 {
	t.Helper()
	var splitID string
	if err := workerJobSizeStore(fixture).db.QueryRow(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.predecessor.job_id')=?`, workID, WorkerJobSplit, predecessorJobID).Scan(&splitID); err != nil {
		t.Fatal(err)
	}
	insertSplitStateEvent(t, fixture, workID, eventID, "worker.job_split_discharged", splitStateDischargePayload(t, splitID, latestSplitBinding(t, fixture, workID, predecessorJobID)))
	var seq int64
	if err := workerJobSizeStore(fixture).db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND event_id=?`, workID, eventID).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func splitConsumerWatermark(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	var watermark int64
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	return watermark
}

// splitConsumerDeliveryFold folds the delivery job facet through the real
// loader the delivery guard reads.
func splitConsumerDeliveryFold(t *testing.T, fixture workflowReturnRouteFixture, workID string) workflowDeliveryAdmission {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	var delivery workflowDeliveryAdmission
	if _, err := loadWorkflowDeliveryJobAdmission(context.Background(), workerJobSizeStore(fixture).db, workID, registered.Definition, 0, false, &delivery); err != nil {
		t.Fatal(err)
	}
	return delivery
}

// The correction walk consumes the exact recorded discharge: the split's
// predecessor debt stays open through partial and full leaf acceptances and
// clears only at the discharge, and the window boundary opens at the
// discharge sequence once no other obligation remains.
func TestWorkflowCorrectionWalkConsumesSplitDischarge(t *testing.T) {
	const workID = "split-consumer-walk-discharge"
	fixture := splitStateFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
	key := workerJobKey(predecessor.JobID, predecessor.Revision)
	splitStateAcceptLeaf(t, fixture, workID, "attempt:consumer-discharge-a", latestSplitBinding(t, fixture, workID, "job:successor-a"))
	partial, err := workflowCorrectionWalk(context.Background(), s.db, workID, splitConsumerWatermark(t, s, workID), "test")
	if err != nil {
		t.Fatal(err)
	}
	if !partial.unresolved[key] || partial.boundary != 0 {
		t.Fatalf("partial-acceptance walk = debt %v boundary %d, want the predecessor debt with no boundary", partial.unresolved, partial.boundary)
	}
	splitStateAcceptLeaf(t, fixture, workID, "attempt:consumer-discharge-b", latestSplitBinding(t, fixture, workID, "job:successor-b"))
	accepted, err := workflowCorrectionWalk(context.Background(), s.db, workID, splitConsumerWatermark(t, s, workID), "test")
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.unresolved[key] || accepted.boundary != 0 {
		t.Fatalf("fully accepted walk = debt %v boundary %d, want the predecessor debt still open", accepted.unresolved, accepted.boundary)
	}
	dischargeSeq := splitConsumerDischarge(t, fixture, workID, splitPredecessorJobID, "discharge-consumer-"+workID)
	discharged, err := workflowCorrectionWalk(context.Background(), s.db, workID, splitConsumerWatermark(t, s, workID), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(discharged.unresolved) != 0 {
		t.Fatalf("discharged walk debt = %v, want none", discharged.unresolved)
	}
	if discharged.boundary != dischargeSeq {
		t.Fatalf("discharged walk boundary = %d, want the discharge sequence %d", discharged.boundary, dischargeSeq)
	}
}

// A nested inner-only discharge keeps the outer split's debt: each
// discharge joins its exact split and removes only that predecessor's
// debt, and the boundary opens only after the outer discharge.
func TestWorkflowCorrectionWalkNestedDischargeKeepsOuterDebt(t *testing.T) {
	const workID = "split-consumer-walk-nested"
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
	for index, jobID := range []string{"job:leaf-c", "job:leaf-d", "job:successor-b"} {
		splitStateAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:consumer-nested-%d", index), latestSplitBinding(t, fixture, workID, jobID))
	}
	outerKey := workerJobKey(splitPredecessorJobID, latestSplitBinding(t, fixture, workID, splitPredecessorJobID).Revision)
	innerKey := workerJobKey(intermediate.JobID, intermediate.Revision)
	innerSeq := splitConsumerDischarge(t, fixture, workID, intermediate.JobID, "discharge-consumer-nested-"+workID)
	inner, err := workflowCorrectionWalk(context.Background(), s.db, workID, splitConsumerWatermark(t, s, workID), "test")
	if err != nil {
		t.Fatal(err)
	}
	if inner.unresolved[innerKey] {
		t.Fatal("the inner discharge left the inner predecessor's debt open")
	}
	if !inner.unresolved[outerKey] || inner.boundary != 0 {
		t.Fatalf("inner-only discharge walk = debt %v boundary %d, want the outer debt with no boundary", inner.unresolved, inner.boundary)
	}
	outerSeq := splitConsumerDischarge(t, fixture, workID, splitPredecessorJobID, "discharge-consumer-outer-"+workID)
	if outerSeq <= innerSeq {
		t.Fatalf("outer discharge seq %d did not follow the inner discharge %d", outerSeq, innerSeq)
	}
	outer, err := workflowCorrectionWalk(context.Background(), s.db, workID, splitConsumerWatermark(t, s, workID), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(outer.unresolved) != 0 {
		t.Fatalf("fully discharged walk debt = %v, want none", outer.unresolved)
	}
	if outer.boundary != outerSeq {
		t.Fatalf("fully discharged walk boundary = %d, want the outer discharge sequence %d", outer.boundary, outerSeq)
	}
}

// A discharge removes only its own predecessor's debt: an unrelated job's
// open failure keeps the correction window open beside the discharged
// split.
func TestWorkflowCorrectionWalkDischargeKeepsUnrelatedDebt(t *testing.T) {
	const workID = "split-consumer-walk-unrelated"
	fixture := splitStateFixture(t, workID)
	s := workerJobSizeStore(fixture)
	if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, splitSuccessorFields(t, workID, fixture, "job:unrelated", "internal/store/worker_oracle.go")); err != nil {
		t.Fatalf("unrelated job refused: %v", err)
	}
	attemptID := "attempt:consumer-unrelated"
	job := latestSplitBinding(t, fixture, workID, "job:unrelated")
	recordSizeDispatchAuthorization(t, s, workID, attemptID, job)
	if err := appendJobBoundWorkerDispatch(t, s, workID, attemptID, &job); err != nil {
		t.Fatalf("record the unrelated dispatch: %v", err)
	}
	completed := reportBindingCompletion(t, s, workID, attemptID, reviewGateLane(t, "implementation"), &job, func(payload *WorkerCompletedPayload) {
		payload.ObservedDelivery = &WorkerObservedDelivery{Commit: strings.Repeat("3", 40), State: "clean", Base: testJobSizeBaseCommit(), Paths: []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 5}}}
		payload.ProductionSize = &WorkerProductionSize{Base: testJobSizeBaseCommit(), Tree: strings.Repeat("3", 40), Count: 5}
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{Severity: "P0", Confidence: "high", Detail: "an unrelated finding binds the job", Oracle: &WorkerOracleFinding{Classification: OracleClassificationFollowUp}}}}
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{completed}}); err != nil {
		t.Fatalf("record the unrelated no_ship report: %v", err)
	}
	dischargeSeq := splitConsumerDischarge(t, fixture, workID, splitPredecessorJobID, "discharge-consumer-"+workID)
	history, err := workflowCorrectionWalk(context.Background(), s.db, workID, splitConsumerWatermark(t, s, workID), "test")
	if err != nil {
		t.Fatal(err)
	}
	unrelatedKey := workerJobKey(job.JobID, job.Revision)
	if !history.unresolved[unrelatedKey] || len(history.unresolved) != 1 {
		t.Fatalf("walk debt after the discharge = %v, want only the unrelated job", history.unresolved)
	}
	if history.boundary != 0 {
		t.Fatalf("walk boundary = %d at discharge seq %d, want no boundary while unrelated debt stands", history.boundary, dischargeSeq)
	}
}

// The walk joins the discharge to the exact split record: a discharge
// naming a split the walked prefix holds no record of, or a predecessor the
// split did not record, fails the fold as a typed invariant instead of
// clearing any debt.
func TestWorkflowCorrectionWalkRefusesForeignDischargeJoin(t *testing.T) {
	t.Run("foreign split", func(t *testing.T) {
		const workID = "split-consumer-walk-foreign-split"
		fixture := splitStateFixture(t, workID)
		predecessor := latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
		insertSplitStateEvent(t, fixture, workID, "discharge-foreign-split", "worker.job_split_discharged",
			splitStateDischargePayload(t, "split:unknown", predecessor))
		_, err := workflowCorrectionWalk(context.Background(), workerJobSizeStore(fixture).db, workID, splitConsumerWatermark(t, workerJobSizeStore(fixture), workID), "test")
		if err == nil || !strings.Contains(err.Error(), "holds no record of") {
			t.Fatalf("foreign-split discharge walk = %v, want the exact-split refusal", err)
		}
	})
	t.Run("foreign predecessor", func(t *testing.T) {
		const workID = "split-consumer-walk-foreign-predecessor"
		fixture := splitStateFixture(t, workID)
		stranger := latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
		stranger.Digest = "sha256:" + strings.Repeat("c", 64)
		insertSplitStateEvent(t, fixture, workID, "discharge-foreign-predecessor", "worker.job_split_discharged",
			splitStateDischargePayload(t, splitStateSplitEventID(t, fixture, workID), stranger))
		_, err := workflowCorrectionWalk(context.Background(), workerJobSizeStore(fixture).db, workID, splitConsumerWatermark(t, workerJobSizeStore(fixture), workID), "test")
		if err == nil || !strings.Contains(err.Error(), "did not record") {
			t.Fatalf("foreign-predecessor discharge walk = %v, want the exact-binding refusal", err)
		}
	})
}

// The non-progress attempt population keeps the split-replaced overrun
// until the final discharge renews the window: every real dispatch counts
// once before it, and only the discharge drops it.
func TestWorkflowCorrectionAttemptBudgetRenewsAfterFinalDischarge(t *testing.T) {
	const workID = "split-consumer-budget-renewal"
	a := seedSplitReplacementAcceptance(t, workID, func(p *WorkerCompletedPayload) {
		p.ObservedDelivery.Paths = []WorkerObservedPath{{Path: "internal/store/worker_jobs.go", Added: 15}}
		p.ProductionSize.Count = 15
	})
	if err := recordWorkerJobActionForTest(t, a.s, a.workID, a.fixture.owner, replacementSplitFields(t, a, nil)); err != nil {
		t.Fatal(err)
	}
	for index, jobID := range []string{"job:successor-a", "job:successor-b"} {
		dischargeReplayAcceptLeaf(t, a.fixture, a.workID, fmt.Sprintf("attempt:consumer-budget-%d", index), latestSplitBinding(t, a.fixture, a.workID, jobID))
	}
	if count, err := workflowNonProgressAttemptCount(context.Background(), a.s.db, a.workID, "test"); err != nil || count != 1 {
		t.Fatalf("nonprogress before the discharge = %d, %v; want the replaced overrun counted once", count, err)
	}
	splitConsumerDischarge(t, a.fixture, a.workID, a.job.JobID, "discharge-consumer-"+a.workID)
	if count, err := workflowNonProgressAttemptCount(context.Background(), a.s.db, a.workID, "test"); err != nil || count != 0 {
		t.Fatalf("nonprogress after the final discharge = %d, %v; want the renewed window", count, err)
	}
}

// Historical-prefix parity: a prefix read before the final discharge keeps
// its original debt, boundary, and attempt count even after later
// acceptance and the discharge exist, the tip consumes the discharge, and a
// full-log rebuild folds the same prefix to the same record. Every route
// here is replayable, so the rebuild folds the complete log.
func TestWorkflowCorrectionHistoricalPrefixRetainsPreDischargeState(t *testing.T) {
	const workID = "split-consumer-historical-prefix"
	fixture := splitStateFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
	key := workerJobKey(predecessor.JobID, predecessor.Revision)
	dischargeReplayAcceptLeaf(t, fixture, workID, "attempt:consumer-history-a", latestSplitBinding(t, fixture, workID, "job:successor-a"))
	prefix := splitConsumerWatermark(t, s, workID)
	original, err := workflowCorrectionWalk(context.Background(), s.db, workID, prefix, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !original.unresolved[key] || original.boundary != 0 {
		t.Fatalf("original prefix walk = debt %v boundary %d, want the open predecessor debt", original.unresolved, original.boundary)
	}
	originalCount, err := workflowCorrectionAttemptCount(context.Background(), s.db, workID, prefix, "test")
	if err != nil {
		t.Fatal(err)
	}
	dischargeReplayAcceptLeaf(t, fixture, workID, "attempt:consumer-history-b", latestSplitBinding(t, fixture, workID, "job:successor-b"))
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("b", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatalf("bind the qualifying integration evidence: %v", err)
	}
	var dischargeSeq int64
	if err := s.db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkerJobSplitDischarged).Scan(&dischargeSeq); err != nil {
		t.Fatal(err)
	}
	tip, err := workflowCorrectionWalk(context.Background(), s.db, workID, splitConsumerWatermark(t, s, workID), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(tip.unresolved) != 0 || tip.boundary != dischargeSeq {
		t.Fatalf("tip walk = debt %v boundary %d, want the consumed discharge at boundary %d", tip.unresolved, tip.boundary, dischargeSeq)
	}
	historical, err := workflowCorrectionWalk(context.Background(), s.db, workID, prefix, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original.unresolved, historical.unresolved) || original.boundary != historical.boundary {
		t.Fatalf("live historical prefix walk drifted: original debt %v boundary %d, later read debt %v boundary %d", original.unresolved, original.boundary, historical.unresolved, historical.boundary)
	}
	if count, err := workflowCorrectionAttemptCount(context.Background(), s.db, workID, prefix, "test"); err != nil || count != originalCount {
		t.Fatalf("live historical attempt count = %d, %v; want the original %d", count, err, originalCount)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := workflowCorrectionWalk(context.Background(), s.db, workID, prefix, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original.unresolved, rebuilt.unresolved) || original.boundary != rebuilt.boundary {
		t.Fatalf("rebuilt prefix walk drifted: original debt %v boundary %d, rebuilt debt %v boundary %d", original.unresolved, original.boundary, rebuilt.unresolved, rebuilt.boundary)
	}
	if count, err := workflowCorrectionAttemptCount(context.Background(), s.db, workID, prefix, "test"); err != nil || count != originalCount {
		t.Fatalf("rebuilt attempt count = %d, %v; want the original %d", count, err, originalCount)
	}
}

// Delivery counts the transitive required leaves and no replaced
// predecessor: the split's own lineage read model owns the replaced
// lineage, and the ordinary latest-revision population stays.
func TestWorkflowDeliveryJobAdmissionExcludesReplacedPredecessor(t *testing.T) {
	const workID = "split-consumer-delivery-lineage"
	fixture := splitStateFixture(t, workID)
	delivery := splitConsumerDeliveryFold(t, fixture, workID)
	if !delivery.JobsRecorded {
		t.Fatal("control: the folded delivery facet recorded no jobs")
	}
	want := "job:successor-a|1,job:successor-b|1"
	if delivery.JobsUnsatisfiedKeys != want {
		t.Fatalf("delivery unsatisfied keys = %q, want the leaves only %q", delivery.JobsUnsatisfiedKeys, want)
	}
	if delivery.JobsUndischargedSplits != splitStateSplitEventID(t, fixture, workID) {
		t.Fatalf("delivery undischarged splits = %q, want the one recorded split", delivery.JobsUndischargedSplits)
	}
	if failure := workflowDeliveryJobsFailure(delivery, nil); failure == nil || !strings.Contains(failure.Detail, "job:successor-a|1") || !strings.Contains(failure.Detail, "job:successor-b|1") {
		t.Fatalf("delivery refusal = %v, want the unsatisfied leaf bindings named", failure)
	}
}

// An accepted-but-undischarged split refuses both delivery routes: the
// guard names the recorded discharge and directs binding integration, and
// the accepting attempt's own pending satisfaction never substitutes for
// the recorded discharge.
func TestWorkflowDeliveryRefusesUndischargedSplitOnBothRoutes(t *testing.T) {
	const workID = "split-consumer-delivery-undischarged"
	fixture := splitStateFixture(t, workID)
	for index, jobID := range []string{"job:successor-a", "job:successor-b"} {
		splitStateAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:consumer-delivery-%d", index), latestSplitBinding(t, fixture, workID, jobID))
	}
	delivery := splitConsumerDeliveryFold(t, fixture, workID)
	if delivery.JobsUnsatisfiedKeys != "" {
		t.Fatalf("delivery unsatisfied keys = %q after both leaves accepted, want none", delivery.JobsUnsatisfiedKeys)
	}
	if delivery.JobsUndischargedSplits != splitStateSplitEventID(t, fixture, workID) {
		t.Fatalf("delivery undischarged splits = %q, want the accepted-but-undischarged split", delivery.JobsUndischargedSplits)
	}
	state := WorkflowAdmissionState{Delivery: delivery}
	state.Delivery.Started = true
	if standalone := workflowAdmitDelivery(state, WorkflowAdmissionDecision{}); standalone.Failure == nil || !strings.Contains(standalone.Failure.Detail, delivery.JobsUndischargedSplits) {
		t.Fatalf("standalone delivery = %v, want the recorded-discharge refusal", standalone.Failure)
	}
	leaf := latestSplitBinding(t, fixture, workID, "job:successor-b")
	if combined := workflowAdmitDeliveryForAccept(state, WorkflowAdmissionDecision{}, &leaf); combined.Failure == nil || !strings.Contains(combined.Failure.Detail, delivery.JobsUndischargedSplits) {
		t.Fatalf("combined acceptance = %v, want the recorded-discharge refusal beside the accepting leaf", combined.Failure)
	}
}

// The fully integrated split admits delivery: the leaves are accepted, the
// qualifying binding's integration recorded the discharge, and the folded
// facet carries no unsatisfied leaf, so both routes admit.
func TestWorkflowDeliveryAdmitsFullyDischargedIntegratedSplit(t *testing.T) {
	const workID = "split-consumer-delivery-integrated"
	fixture := dischargeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	for index, job := range dischargeLeafBindings(t, fixture, workID) {
		dischargeAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:consumer-integrated-%d", index), job)
	}
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("b", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatalf("bind the qualifying integration evidence: %v", err)
	}
	if discharges := dischargeEvents(t, s, workID); len(discharges) != 1 {
		t.Fatalf("discharges = %d, want the automatic integration discharge", len(discharges))
	}
	delivery := splitConsumerDeliveryFold(t, fixture, workID)
	if delivery.JobsUnsatisfiedKeys != "" {
		t.Fatalf("delivery unsatisfied keys = %q, want none behind the recorded discharge", delivery.JobsUnsatisfiedKeys)
	}
	if delivery.JobsUndischargedSplits != "" {
		t.Fatalf("delivery undischarged splits = %q, want none behind the recorded discharge", delivery.JobsUndischargedSplits)
	}
	if !delivery.JobsIntegrated {
		t.Fatal("delivery lost the qualifying integration evidence behind the recorded discharge")
	}
	state := WorkflowAdmissionState{Delivery: delivery}
	state.Delivery.Started = true
	if standalone := workflowAdmitDelivery(state, WorkflowAdmissionDecision{}); standalone.Failure != nil {
		t.Fatalf("standalone delivery behind the recorded discharge = %v", standalone.Failure)
	}
	leaf := dischargeLeafBindings(t, fixture, workID)[1]
	if combined := workflowAdmitDeliveryForAccept(state, WorkflowAdmissionDecision{}, &leaf); combined.Failure != nil {
		t.Fatalf("combined acceptance behind the recorded discharge = %v", combined.Failure)
	}
}
