package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	return smallReviewDispositionJourneyAtRoot(t, workID, t.TempDir())
}

func smallReviewDispositionJourneyAtRoot(t *testing.T, workID, root string) *candidateJourney {
	t.Helper()
	return smallReviewDispositionJourneyWithChecks(t, workID, root, nil, nil)
}

func smallReviewDispositionJourneyWithChecks(t *testing.T, workID, root string, checks []FootprintPackageCheck, files map[string]string) *candidateJourney {
	t.Helper()
	s, definition := footprintBindingItemWithChecks(t, workID, true, checks)
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
	dir, _ := reviewSubjectClaimAtRoot(t, s, workID, root)
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
	for name, contents := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
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

func TestSmallProofConfirmationCannotSkipNativeProof(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		name := "without-accepted-review"
		if accepted {
			name = "without-outcome-verdict"
		}
		t.Run(name, func(t *testing.T) {
			j := smallReviewDispositionJourney(t, "small-proof-"+name)
			if accepted {
				if err := j.acceptEvidence(t, "small-review"); err != nil {
					t.Fatal(err)
				}
			}
			version := readWorkVersion(t, j.s, j.workID)
			actor := footprintBindingActorRef(t, j.workID)
			event := workflowEventWithActor("small-proof-raw-confirm", WorkflowPremiseConfirmed, j.workID, actor, map[string]any{
				"work_id": j.workID, "expected_version": version, "resulting_version": version + 1,
				"contract_version": 1, "confirming_actor_ref": actor, "confirmation_authority": "coordinator",
			})
			event.PayloadVersion = eventKindRegistry[WorkflowPremiseConfirmed].CurrentVersion
			if err := appendFootprintBindingEvents(j.s, j.workID, version, []Event{event}); err == nil {
				t.Error("raw confirmation bypassed mandatory small proof")
			}
			if got := readWorkVersion(t, j.s, j.workID); got != version {
				t.Errorf("refused confirmation changed version %d to %d", version, got)
			}
		})
	}
}

func smallProofRecordVerdict(t *testing.T, j *candidateJourney) {
	t.Helper()
	evaluator := WorkflowActor{PrincipalRef: "principal/agent", ClientRef: "client/concord", AgentRef: "agent/small-evaluator", SessionRef: "session/small-evaluator", ActorClass: ActorAgent, Model: "model/small-evaluator"}
	runRef := worktreeVerifyOperationRef(j.request("small-integration", nil).LeaseID)
	if err := j.action(t, "record_verdict", map[string]any{
		"contract_version": 1, "verdicts": []map[string]any{{"predicate_id": "predicate:primary", "verdict_kind": "ok", "evaluation_evidence": []string{runRef}}},
	}, evaluator); err != nil {
		t.Fatalf("record independent outcome verdict: %v", err)
	}
}

func smallProofRecordInvestigation(t *testing.T, j *candidateJourney) {
	t.Helper()
	if err := ApplyOperation(context.Background(), j.s, Operation{Events: []Event{{
		EventID: "small-proof-investigation", Kind: WorkObservationRecorded, SubjectType: SubjectWorkItem, SubjectID: j.workID,
		Actor: "actor:test", OccurredAt: time.Unix(8, 0).UTC(), PayloadVersion: 1,
		Payload: mustJSONValue(map[string]any{"observation_id": "obs:" + strings.Repeat("a", 16), "statement": "technical investigation", "refs": []string{"root"}, "tags": []string{}}),
	}}}); err != nil {
		t.Fatal(err)
	}
}

func TestSmallProofNativeReviewAndCurrentOutcomesPermitConfirmation(t *testing.T) {
	j := smallReviewDispositionJourney(t, "small-proof-current")
	if err := j.acceptEvidence(t, "small-review"); err != nil {
		t.Fatal(err)
	}
	smallProofRecordVerdict(t, j)
	smallProofRecordInvestigation(t, j)
	ctx := context.Background()
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireWorkflowSmallProofBefore(ctx, j.s.db, j.workID, small.Definition, 0); err != nil {
		t.Fatalf("native proof derivation: %v", err)
	}
	pin, err := ReadWorkPin(ctx, j.s, j.workID)
	if err != nil || !workPinContainsAction(pin.NextValidIntents, "confirm_premise") {
		t.Fatalf("complete small proof did not open confirmation: %+v, %v", pin.NextValidIntents, err)
	}
	smallProofConfirm(t, j)
	if err := RebuildFromLog(ctx, j.s); err != nil {
		t.Fatalf("rebuild native small proof: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "release")
}

func smallProofConfirm(t *testing.T, j *candidateJourney) {
	t.Helper()
	ctx := context.Background()
	prepared, err := PrepareWorkflowAcceptanceFreshness(ctx, j.s, PrepareWorkerDispatchRequest{WorkID: j.workID, SessionWorktree: j.repo})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithWorkerDispatchPreparation(ctx, prepared)
	version := readWorkVersion(t, j.s, j.workID)
	if err := j.s.Transact(ctx, func(transaction *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, transaction, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: j.workID, ExpectedVersion: version, ActionID: "confirm_premise", SelectedChoice: "confirm", Actor: j.fixture.owner,
			AcceptedInputsDigest: nativeDigest([]byte("small-confirm")), IdempotencyIdentity: "small-confirm", OperationID: "small-confirm",
			PrincipalRef: j.fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "small-confirm", RequestID: "request:small-confirm", ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	}); err != nil {
		t.Fatalf("confirm complete native small proof: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "release")
}

func TestSmallProofExactFootprintReceiptAndPrefix(t *testing.T) {
	j := smallReviewDispositionJourneyWithChecks(t, "small-proof-check", t.TempDir(), []FootprintPackageCheck{{Package: workSizeFootprintFixture().Paths[0].Package, Kind: "validator", Value: "scripts/check-small.py"}}, map[string]string{"scripts/check-small.py": "raise SystemExit(0)\n"})
	if err := j.acceptEvidence(t, "small-review"); err != nil {
		t.Fatal(err)
	}
	smallProofRecordVerdict(t, j)
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	assertProof := func(before int64, want bool) {
		t.Helper()
		err := requireWorkflowSmallProofBefore(ctx, j.s.db, j.workID, small.Definition, before)
		if (err == nil) != want {
			t.Fatalf("proof before %d = %v, want qualified=%v", before, err, want)
		}
	}
	bind := func(request WorktreeVerifyRequest, run WorktreeVerifyResult) error {
		t.Helper()
		return j.action(t, "bind_evidence", map[string]string{
			"evidence_kind": "verification", "evidence_ref": run.OperationRef, "immutable_subject_ref": run.OperationRef,
			"producer_id": request.PrincipalRef, "producer_run_ref": run.OperationRef, "producer_watermark": request.RequestID,
		}, j.fixture.owner)
	}
	run := func(id string, argv []string) (WorktreeVerifyRequest, WorktreeVerifyResult) {
		t.Helper()
		request := j.request(id, nil)
		request.Command = argv
		result, err := j.s.VerifyWorktree(ctx, request)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("native check: %+v, %v", result, err)
		}
		return request, result
	}
	assertProof(0, false)
	version := readWorkVersion(t, j.s, j.workID)
	if err := WorkflowActionPreflightWithRegistry(ctx, j.s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: j.workID, ExpectedVersion: version, ActionID: "confirm_premise", Actor: j.fixture.owner, SelectedChoice: "confirm",
	}); err == nil || !strings.Contains(err.Error(), "exact bound receipt") {
		t.Fatalf("preflight did not consume the missing footprint proof: %v", err)
	}
	if err := j.action(t, "confirm_premise", map[string]any{}, j.fixture.owner); err == nil || !strings.Contains(err.Error(), "exact bound receipt") {
		t.Fatalf("direct action did not consume the missing footprint proof: %v", err)
	}
	evaluatorRef, err := WorkflowActorRef(WorkflowActor{PrincipalRef: "principal/agent", ClientRef: "client/concord", AgentRef: "agent/small-evaluator", SessionRef: "session/small-evaluator", ActorClass: ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	completion := workflowEventWithActor("small-proof-raw-completion", WorkflowCompleted, j.workID, footprintBindingActorRef(t, j.workID), map[string]any{
		"work_id": j.workID, "expected_version": version, "resulting_version": version + 1,
		"terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": evaluatorRef,
		"evidence_count": 0, "changed_refs_digest": nativeDigest([]byte("candidate")), "impact_verdict": "non-breaking", "warnings": []string{},
	})
	if err := j.s.Transact(ctx, func(transaction *Transaction) error {
		return foldWorkflowCompleted(ctx, transaction.tx, completion, nil)
	}); err == nil || !strings.Contains(err.Error(), "exact bound receipt") {
		t.Fatalf("raw completion did not consume the missing footprint proof: %v", err)
	}
	if got := readWorkVersion(t, j.s, j.workID); got != version {
		t.Fatalf("proof refusals changed version %d to %d", version, got)
	}
	wrongRequest, wrong := run("wrong-check", []string{"python3", "scripts/check-small.py", "--unrelated"})
	if err := bind(wrongRequest, wrong); err == nil {
		t.Fatal("unrelated green acquired a footprint binding recovery route")
	}
	assertProof(0, false)
	request, exact := run("exact-check", []string{"python3", "scripts/check-small.py"})
	assertProof(0, false)
	if err := bind(request, exact); err != nil {
		t.Fatal(err)
	}
	var bindingSeq int64
	if err := j.s.db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=?`, j.workID, WorkflowEvidenceBound, exact.OperationRef).Scan(&bindingSeq); err != nil {
		t.Fatal(err)
	}
	assertProof(bindingSeq, false)
	assertProof(bindingSeq+1, true)
	assertProof(0, true)
	recovery, err := workflowEvidenceBindingRecoveryEligibility(ctx, j.s.db, small.Definition, j.workID, "acceptance", bindingSeq)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.Requirements) != 1 || recovery.Requirements[0].Reference != exact.OperationRef {
		t.Fatalf("binding prefix lost its exact recovery requirement: %+v", recovery)
	}

	// A completed but undispositioned newer review retires the old report,
	// but cannot leak backward through the historical proof boundary.
	j.dispatchReview(t, "small-review-newer")
	j.completeLane(t, "small-review-newer", "review", nil, nil)
	assertProof(0, false)
	assertProof(bindingSeq+1, true)
	if err := j.acceptEvidence(t, "small-review-newer"); err != nil {
		t.Fatal(err)
	}
	assertProof(0, true)
	if err := RebuildFromLog(ctx, j.s); err != nil {
		t.Fatalf("rebuild exact native footprint proof: %v", err)
	}
	assertProof(0, true)
}

func TestSmallProofRealGoReceiptsPermitConfirmationAndRebuild(t *testing.T) {
	checks := []FootprintPackageCheck{
		{Package: workSizeFootprintFixture().Paths[0].Package, Kind: "go_test", Value: "pkg.TestValue"},
		{Package: workSizeFootprintFixture().Paths[0].Package, Kind: "go_package", Value: "./pkg"},
	}
	j := smallReviewDispositionJourneyWithChecks(t, "small-proof-real-go", t.TempDir(), checks, map[string]string{
		"go.mod":            "module example.invalid/small\n\ngo 1.26.0\n",
		"pkg/value.go":      "package oracle\nfunc Value() int { return 7 }\n",
		"pkg/value_test.go": candidateJourneyHarness,
	})
	if err := j.acceptEvidence(t, "small-review"); err != nil {
		t.Fatal(err)
	}
	smallProofRecordVerdict(t, j)
	ctx := t.Context()
	small, err := builtinWorkflowDefinitionForSize("workflow.implementation", WorkSizeSmall)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, j.s, j.workID)
	actor := footprintBindingActorRef(t, j.workID)
	event := workflowEventWithActor("small-go-raw-confirm", WorkflowPremiseConfirmed, j.workID, actor, map[string]any{
		"work_id": j.workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "confirming_actor_ref": actor, "confirmation_authority": "coordinator",
	})
	event.PayloadVersion = eventKindRegistry[WorkflowPremiseConfirmed].CurrentVersion
	if err := appendFootprintBindingEvents(j.s, j.workID, version, []Event{event}); err == nil || !strings.Contains(err.Error(), "exact bound receipt") {
		t.Fatalf("raw confirmation bypassed the unexecuted Go footprint: %v", err)
	}
	if got := readWorkVersion(t, j.s, j.workID); got != version {
		t.Fatalf("refused Go proof changed version %d to %d", version, got)
	}
	for index, check := range checks {
		if err := requireWorkflowSmallProofBefore(ctx, j.s.db, j.workID, small.Definition, 0); err == nil {
			t.Fatalf("unexecuted Go check %s already qualified", check.Value)
		}
		request := j.request(check.Kind, nil)
		request.Command, _ = workflowFootprintCheckCommand(FootprintCheck{Kind: check.Kind, Value: check.Value})
		run, err := j.s.VerifyWorktree(ctx, request)
		if err != nil || run.ExitCode != 0 || run.GoSuite == nil || !run.GoSuite.Complete {
			t.Fatalf("real Go footprint %s: %+v, %v", check.Value, run, err)
		}
		if err := requireWorkflowSmallProofBefore(ctx, j.s.db, j.workID, small.Definition, 0); err == nil {
			t.Fatalf("unbound Go run %s already qualified", check.Value)
		}
		if err := j.action(t, "bind_evidence", map[string]string{
			"evidence_kind": "verification", "evidence_ref": run.OperationRef, "immutable_subject_ref": run.OperationRef,
			"producer_id": request.PrincipalRef, "producer_run_ref": run.OperationRef, "producer_watermark": request.RequestID,
		}, j.fixture.owner); err != nil {
			t.Fatalf("bind exact Go footprint %s: %v", check.Value, err)
		}
		var bindingSeq int64
		if err := j.s.db.QueryRow(`SELECT seq FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=?`, j.workID, WorkflowEvidenceBound, run.OperationRef).Scan(&bindingSeq); err != nil {
			t.Fatal(err)
		}
		if err := requireWorkflowSmallProofBefore(ctx, j.s.db, j.workID, small.Definition, bindingSeq); err == nil {
			t.Fatalf("future binding of %s leaked into its prefix", check.Value)
		}
		if err := requireWorkflowSmallProofBefore(ctx, j.s.db, j.workID, small.Definition, bindingSeq+1); (err == nil) != (index == len(checks)-1) {
			t.Fatalf("proof after Go check %s: %v", check.Value, err)
		}
	}
	smallProofRecordInvestigation(t, j)
	smallProofConfirm(t, j)
	if err := RebuildFromLog(ctx, j.s); err != nil {
		t.Fatalf("rebuild real-Go small proof: %v", err)
	}
	reviewGateRequireStep(t, j.s, j.workID, "release")
	if err := requireWorkflowSmallProofBefore(ctx, j.s.db, j.workID, small.Definition, 0); err != nil {
		t.Fatalf("rebuild lost real-Go small proof: %v", err)
	}
}
