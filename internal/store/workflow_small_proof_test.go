package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These are query-level tests over the existing history fixture, not native
// dispatch or admission evidence. The small composition changes the producer
// and review steps; rejection, repair, and settlement keep their shared owner.
func TestSmallReviewDebtUsesProducerAndReviewSteps(t *testing.T) {
	t.Parallel()
	registered, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	definition := registered.Definition
	db, appendEvent := correctionHistoryFixture(t)
	ctx := context.Background()
	authorize := func(attempt, step, class string) int64 {
		appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "step_id": step, "worker_attempt_id": attempt})
		return appendEvent(WorkerDispatched, map[string]any{"attempt_id": attempt, "capability_class": class})
	}
	complete := func(attempt, verdict string) int64 {
		return appendEvent(WorkerCompleted, map[string]any{"attempt_id": attempt, "review": map[string]any{"verdict": verdict}})
	}
	dispose := func(attempt, step, action string) int64 {
		return appendEvent(WorkflowActionCompleted, map[string]any{"action_id": action, "step_id": step, "worker_attempt_id": attempt})
	}
	assertDebt := func(want bool, frontier int64) {
		t.Helper()
		got, err := workflowPostRejectionReviewOutstanding(ctx, db, "work-history", definition, "test")
		if err != nil || got != want {
			t.Fatalf("review debt = %v, %v; want %v", got, err, want)
		}
		gotFrontier, err := workflowPostRejectionFrontier(ctx, db, "work-history", definition, "test")
		if err != nil || gotFrontier != frontier {
			t.Fatalf("review frontier = %d, %v; want %d", gotFrontier, err, frontier)
		}
	}

	authorize("review-rejected", "acceptance", "review")
	complete("review-rejected", "no_ship")
	rejected := dispose("review-rejected", "acceptance", "reject_worker_result")
	assertDebt(true, rejected)

	// A review dispatched before a repair cannot cover it even when the
	// review completes and is accepted after that repair.
	authorize("review-old", "acceptance", "review")
	authorize("repair", "execution", "implementation")
	repaired := complete("repair", "")
	dispose("repair", "execution", "accept_worker_result")
	assertDebt(true, repaired)
	complete("review-old", "ship")
	dispose("review-old", "acceptance", "accept_worker_evidence")
	assertDebt(true, repaired)

	authorize("review-no-ship", "acceptance", "review")
	complete("review-no-ship", "no_ship")
	noShip := dispose("review-no-ship", "acceptance", "accept_worker_evidence")
	assertDebt(true, noShip)

	// A ship report accepted at a different step is not this composition's
	// review disposition.
	authorize("review-wrong-step", "planning", "review")
	complete("review-wrong-step", "ship")
	dispose("review-wrong-step", "planning", "accept_worker_evidence")
	assertDebt(true, noShip)

	authorize("review-fresh", "acceptance", "review")
	complete("review-fresh", "ship")
	dispose("review-fresh", "acceptance", "accept_worker_evidence")
	assertDebt(false, noShip)

	// Later producer activity makes the previous review stale again.
	later := authorize("repair-later", "execution", "implementation")
	assertDebt(true, later)
}

func TestSmallReviewDebtAdmissionKeepsRepairAndReviewRoutes(t *testing.T) {
	t.Parallel()
	registered, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	definition := registered.Definition
	for _, step := range []string{"acceptance", "release"} {
		state := WorkflowAdmissionState{
			Step: step, RefinementWorkflow: true, ReviewStep: true, ReviewDebt: ReviewDebtOutstanding,
			ActiveContracts: 1, ConfirmationAuthority: WorkflowConfirmationCoordinator, CorrectionRequestRecovery: true,
			RefineReviewEvidenceAttemptID: "review-ready",
		}
		action := "confirm_premise"
		if step == "release" {
			action = "complete"
		}
		decision := workflowAdmit(definition, state, action)
		if decision.Admitted || decision.Failure == nil || !strings.Contains(decision.Failure.Error(), "fresh accepted review") {
			t.Fatalf("%s at %s bypassed review debt: %+v", action, step, decision)
		}
		for _, recovery := range []string{"checkpoint_context", "request_correction"} {
			if decision := workflowAdmit(definition, state, recovery); !decision.Admitted {
				t.Fatalf("debt must preserve %s at %s: %+v", recovery, step, decision)
			}
		}
		if step == "acceptance" {
			for _, recovery := range []string{"dispatch_worker", "accept_worker_evidence"} {
				if decision := workflowAdmit(definition, state, recovery); !decision.Admitted {
					t.Fatalf("debt must preserve %s at acceptance: %+v", recovery, decision)
				}
			}
		}
		state.ReviewDebt = ReviewDebtNone
		if decision := workflowAdmit(definition, state, action); !decision.Admitted {
			t.Fatalf("settled debt must release %s at %s: %+v", action, step, decision)
		}
	}
	for step, want := range map[string]bool{"execution": false, "acceptance": true, "release": true} {
		if got := workflowPostRejectionReviewStep(definition, step); got != want {
			t.Fatalf("review-debt gate at %s = %v; want %v", step, got, want)
		}
	}
	state := WorkflowAdmissionState{Step: "execution", RefinementWorkflow: true, ReviewDebt: ReviewDebtOutstanding, ActiveContracts: 1}
	state.Delivery.Started = true
	if decision := workflowAdmit(definition, state, "record_delivery"); !decision.Admitted {
		t.Fatalf("repair must be able to return to the review step under debt: %+v", decision)
	}
}

func TestSmallReviewDebtRequiresAnExplicitShipVerdict(t *testing.T) {
	t.Parallel()
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	full, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		definition WorkflowDefinition
		step       string
		wantDebt   bool
	}{
		{"small", small.Definition, "acceptance", true},
		{"released full", full.Definition, "refine", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			roles := workflowReviewProofRoles(test.definition)
			for verdict, want := range map[string]bool{"": !test.wantDebt, "ship": true, "no_ship": false} {
				if got := workflowReviewSettlesDebt(roles, verdict); got != want {
					t.Fatalf("settlement of %q = %v; want %v", verdict, got, want)
				}
			}
			db, appendEvent := correctionHistoryFixture(t)
			appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "reject_worker_result", "step_id": test.step})
			appendEvent(WorkerDispatched, map[string]any{"attempt_id": "review", "capability_class": "review"})
			appendEvent(WorkerCompleted, map[string]any{"attempt_id": "review"})
			appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "accept_worker_evidence", "step_id": test.step, "worker_attempt_id": "review"})
			debt, err := workflowPostRejectionReviewOutstanding(context.Background(), db, "work-history", test.definition, "test")
			if err != nil || debt != test.wantDebt {
				t.Fatalf("debt = %v, %v; want %v", debt, err, test.wantDebt)
			}
		})
	}
}

func TestSmallProofRepairFrontierIsPrefixBound(t *testing.T) {
	t.Parallel()
	for _, alias := range []string{"repair", "completion", "authorization", "dispatch"} {
		t.Run(alias, func(t *testing.T) {
			t.Parallel()
			db, appendEvent := correctionHistoryFixture(t)
			auth := func(attempt string) int64 {
				return appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "dispatch_worker", "step_id": "execution", "worker_attempt_id": attempt})
			}
			dispatch := func(attempt string) int64 {
				return appendEvent(WorkerDispatched, map[string]any{"attempt_id": attempt, "capability_class": "implementation"})
			}
			complete := func(attempt string) int64 {
				return appendEvent(WorkerCompleted, map[string]any{"attempt_id": attempt})
			}
			var boundary, want int64
			switch alias {
			case "repair":
				auth("earlier")
				dispatch("earlier")
				want = complete("earlier")
				boundary = auth("later")
				dispatch("later")
				complete("later")
			case "completion":
				auth("later")
				want = dispatch("later")
				boundary = complete("later")
			case "authorization":
				dispatch("later")
				complete("later")
				boundary = auth("later")
			case "dispatch":
				auth("later")
				complete("later")
				boundary = dispatch("later")
			}
			frontier, err := workflowPostRejectionRepairFrontierBefore(context.Background(), db, "work-history", "execution", 0, "test", boundary)
			if err != nil || frontier != want {
				t.Fatalf("future %s leaked through prefix %d: %d, %v; want %d", alias, boundary, frontier, err, want)
			}
			live, err := workflowPostRejectionRepairFrontier(context.Background(), db, "work-history", "execution", 0, "test")
			if err != nil || live <= want {
				t.Fatalf("the intact-journal fixture has no later %s: %d, %v", alias, live, err)
			}
		})
	}
}

func TestSmallReviewEvidenceUsesTheDispositionOwner(t *testing.T) {
	t.Parallel()
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	for step, want := range map[string]bool{"alignment": false, "planning": false, "execution": false, "acceptance": true, "release": false} {
		if got := workflowRefineReviewEvidenceStep(small.Definition, step); got != want {
			t.Fatalf("exact review disposition at small %s = %v; want %v", step, got, want)
		}
	}
}

// The sizing/contract and step walker seed prerequisites; dispatch, worker
// completion and disposition use the existing native mutation seams. Oracle
// preparation fixtures make no execution claim. This tests disposition, not
// whole-item proof acceptance.
func smallReviewDispositionJourney(t *testing.T, workID string) *candidateJourney {
	t.Helper()
	s, definition := footprintBindingItem(t, workID, true)
	owner := WorkflowActor{PrincipalRef: "principal/agent", ClientRef: "client/concord", AgentRef: "agent/size", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	events, err := footprintBindingApproval(t, s, definition, workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendFootprintBindingEvents(s, workID, readWorkVersion(t, s, workID), events); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(context.Background(), s, workID, "execution", footprintBindingActorRef(t, workID)); err != nil {
		t.Fatal(err)
	}
	dir, _ := reviewSubjectClaim(t, s, workID)
	var branch string
	if err := s.DatabaseForTesting().QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND project_id='project'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, dir, "branch", "-m", branch)
	j := &candidateJourney{s: s, workID: workID, repo: dir, fixture: workContextFixture{store: s, workID: workID, owner: owner}}
	job := WorkerJobBinding{JobID: "job:small-disposition"}
	recordWorkerJobRevisionForTest(t, s, workID, owner, &job)
	j.dispatchJob(t, "small-implementation", "implement", "implementation", job)
	if err := os.MkdirAll(filepath.Join(dir, "internal/store"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal/store/value.txt"), []byte("bounded candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.commit(t, "worker@example.invalid", "deliver the bounded candidate")
	j.completeLane(t, "small-implementation", "implementation", &job, nil)
	if err := j.action(t, "accept_worker_result", map[string]any{"attempt_id": "small-implementation", "attempt_epoch": j.dispatchEpoch(t, "small-implementation")}, owner); err != nil {
		t.Fatalf("accept native implementation: %v", err)
	}
	verify := j.request("small-integration", nil)
	run, err := s.VerifyWorktree(context.Background(), verify)
	if err != nil || run.ExitCode != 0 {
		t.Fatalf("verify integration: %+v, %v", run, err)
	}
	if err := j.action(t, "bind_evidence", map[string]string{
		"evidence_kind": "verification", "evidence_ref": run.OperationRef, "immutable_subject_ref": run.OperationRef,
		"producer_id": verify.PrincipalRef, "producer_run_ref": run.OperationRef, "producer_watermark": verify.RequestID,
	}, owner); err != nil {
		t.Fatalf("bind native integration: %v", err)
	}
	if err := j.action(t, "record_delivery", map[string]string{"delivery_artifact": "artifact:small-candidate", "delivery_state": "asserted"}, owner); err != nil {
		t.Fatalf("deliver native implementation: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "acceptance")
	j.dispatchReview(t, "small-review")
	j.completeLane(t, "small-review", "review", nil, nil)
	return j
}

func TestSmallReviewDispositionRefusesTheWrongEpoch(t *testing.T) {
	j := smallReviewDispositionJourney(t, "small-review-disposition")
	payload := mustJSONValue(map[string]any{"attempt_id": "small-review", "attempt_epoch": j.dispatchEpoch(t, "small-review") + 1})
	version := readWorkVersion(t, j.s, j.workID)
	if err := WorkflowActionPreflightWithRegistry(context.Background(), j.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: j.workID, ExpectedVersion: version, ActionID: "accept_worker_evidence", Payload: json.RawMessage(payload), Actor: j.fixture.owner,
	}); err == nil {
		t.Error("preflight admitted a review under the wrong epoch")
	}
	if err := runVerdictActionAs(t, j.s, j.workID, "accept_worker_evidence", payload, 0, j.fixture.owner); err == nil {
		t.Error("direct action accepted a review under the wrong epoch")
	}
	if got := readWorkVersion(t, j.s, j.workID); got != version {
		t.Errorf("refused review disposition changed version %d to %d", version, got)
	}
}

func TestSmallReviewDispositionTracksTheCurrentNativeAttempt(t *testing.T) {
	j := smallReviewDispositionJourney(t, "small-current-review")
	ctx := context.Background()
	ownerRef := footprintBindingActorRef(t, j.workID)
	foldDisposition := func(id, attempt string, epoch int64) error {
		version := readWorkVersion(t, j.s, j.workID)
		event := workflowEventWithActor(id, WorkflowActionCompleted, j.workID, ownerRef, map[string]any{
			"work_id": j.workID, "expected_version": version, "resulting_version": version + 1,
			"step_id": "acceptance", "action_id": "accept_worker_evidence", "attempt_epoch": epoch,
			"worker_attempt_id": attempt, "actor_ref": ownerRef, "result_evidence_refs": []string{}, "changed_refs": []string{},
		})
		event.PayloadVersion = 5
		return appendFootprintBindingEvents(j.s, j.workID, version, []Event{event})
	}
	pin, err := ReadWorkPin(ctx, j.s, j.workID)
	if err != nil || !workPinContainsAction(pin.NextValidIntents, "accept_worker_evidence") {
		t.Fatalf("current native review is not offered: %+v, %v", pin.NextValidIntents, err)
	}
	if err := j.acceptEvidence(t, "small-review"); err != nil {
		t.Fatalf("accept the exact current native review: %v", err)
	}
	version := readWorkVersion(t, j.s, j.workID)
	if err := j.acceptEvidence(t, "small-review"); err == nil {
		t.Error("the same native review was accepted twice")
	}
	if got := readWorkVersion(t, j.s, j.workID); got != version {
		t.Errorf("repeated disposition changed version %d to %d", version, got)
	}
	if err := foldDisposition("small-repeat-review-disposition", "small-review", j.dispatchEpoch(t, "small-review")); err == nil || !strings.Contains(err.Error(), "current undispositioned completed review") {
		t.Errorf("the raw fold did not reject the repeated native review: %v", err)
	}
	if got := readWorkVersion(t, j.s, j.workID); got != version {
		t.Errorf("raw repeated disposition changed version %d to %d", version, got)
	}
	pin, err = ReadWorkPin(ctx, j.s, j.workID)
	if err != nil || workPinContainsAction(pin.NextValidIntents, "accept_worker_evidence") {
		t.Errorf("disposed review remains offered: %+v, %v", pin.NextValidIntents, err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "dispatch_worker") {
		t.Error("review disposition closed the fresh-review route")
	}
	j.dispatchReview(t, "small-review-fresh")
	j.completeLane(t, "small-review-fresh", "review", nil, nil)
	version = readWorkVersion(t, j.s, j.workID)
	if err := foldDisposition("small-old-review-disposition", "small-review", j.dispatchEpoch(t, "small-review-fresh")); err == nil || !strings.Contains(err.Error(), "current undispositioned completed review") {
		t.Errorf("the raw fold did not enforce the current native review: %v", err)
	}
	if got := readWorkVersion(t, j.s, j.workID); got != version {
		t.Errorf("refused raw disposition changed version %d to %d", version, got)
	}
	if err := j.acceptEvidence(t, "small-review-fresh"); err != nil {
		t.Fatalf("accept the fresh native review: %v", err)
	}
	if err := RebuildFromLog(ctx, j.s); err != nil {
		t.Fatalf("rebuild the accepted native review sequence: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "acceptance")
}
