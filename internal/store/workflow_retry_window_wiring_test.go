package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The CD-0164 D5 wiring: the workflow read projection and the work pin carry
// the retry window, the window's populations reuse the admission derivation's
// own counters, and every derived anchor names its exact event-log row or
// stays explicitly null. The journeys below run on the store's own fixtures,
// so the window, the dispatched packet's correction context, and the wall
// refusal answer the same counts for the same log state.

func readRetryWindow(t *testing.T, s *Store, workID, step string) *WorkflowRetryWindow {
	t.Helper()
	var window *WorkflowRetryWindow
	if err := s.readSnapshot(context.Background(), func(q queryer) error {
		var err error
		window, err = workflowRetryWindowRead(context.Background(), q, workID, step, "workflow_read")
		return err
	}); err != nil {
		t.Fatalf("read retry window: %v", err)
	}
	return window
}

func TestWorkflowRetryWindowExposesPopulationsAtTheEscalatedWall(t *testing.T) {
	t.Parallel()
	const workID = "retry-window-escalated-wall"
	s, _, pin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	window := readRetryWindow(t, s, workID, pin.Step)
	if window.Population != "distinct_failed_rejected_no_ship" {
		t.Fatalf("population = %q", window.Population)
	}
	if window.Policy != "worker_acceptance" {
		t.Fatalf("policy = %q; the recorded legacy pin carries no progress policy", window.Policy)
	}
	if window.Count != 3 || window.Limit != workflowCorrectionAttemptLimit {
		t.Fatalf("nonprogress population = %d/%d, want the three failed attempts at the limit", window.Count, window.Limit)
	}
	if window.DispatchCount != 3 || window.DispatchLimit != workflowCorrectionAttemptLimit {
		t.Fatalf("dispatch population = %d/%d, want the three dispatched attempts at the limit", window.DispatchCount, window.DispatchLimit)
	}
	if window.VerificationCount != 0 || window.VerificationLimit != workflowCorrectionAttemptLimit {
		t.Fatalf("verification population = %d/%d, want no correction requests", window.VerificationCount, window.VerificationLimit)
	}
	if !window.CorrectionEscalated || window.BlockingPopulation != "both" {
		t.Fatalf("escalated=%v blocking=%q, want both populations behind the wall", window.CorrectionEscalated, window.BlockingPopulation)
	}
	if window.Step != pin.Step {
		t.Fatalf("window step = %q, want the derivation step %q", window.Step, pin.Step)
	}
	// The legacy journey holds no acceptance, no coordinator satisfaction,
	// and no healthy verdict: every anchor is an explicit null, never a
	// zero-value or invented identity.
	for name, anchor := range map[string]*WorkflowProgressAnchor{
		"nonprogress":           window.NonprogressAnchor,
		"worker_satisfaction":   window.WorkerSatisfactionAnchor,
		"verification_baseline": window.VerificationBaseline,
	} {
		if anchor != nil {
			t.Fatalf("%s anchor = %+v, want an explicit null on a window that opened at the log start", name, anchor)
		}
	}
	if window.ConvergenceBasis != "" || window.FundedConfirmingReviews != 0 {
		t.Fatalf("convergence basis %q with %d funded reviews; the wall stands without either", window.ConvergenceBasis, window.FundedConfirmingReviews)
	}
	if marshaled, err := json.Marshal(window); err != nil {
		t.Fatal(err)
	} else if err := payloadschema.Validate("workflow_retry_window", marshaled); err != nil {
		t.Fatalf("produced window violates the published contract: %v", err)
	}

	// The work pin carries the same window: it reuses the pin's folded
	// admission state, so the wall's count, escalation, and convergence on
	// the pin are the loader's answer and equal the standalone read.
	if pin.RetryWindow == nil {
		t.Fatal("the work pin carries no retry window")
	}
	if !reflect.DeepEqual(pin.RetryWindow, window) {
		t.Fatalf("pin retry window = %+v, want the standalone window %+v", pin.RetryWindow, window)
	}
	if marshaled, err := json.Marshal(pin); err != nil {
		t.Fatal(err)
	} else if err := payloadschema.Validate("work_pin", marshaled); err != nil {
		t.Fatalf("pin with its retry window violates the closed pin contract: %v", err)
	}

	// The workflow read projection carries the same window from its own
	// derivation, so both read surfaces answer identically.
	projection, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if projection.RetryWindow == nil || !reflect.DeepEqual(projection.RetryWindow, window) {
		t.Fatalf("projection retry window = %+v, want the standalone window %+v", projection.RetryWindow, window)
	}
}

func TestWorkflowRetryWindowSurvivesRebuildWithTheSameWindow(t *testing.T) {
	t.Parallel()
	const workID = "retry-window-rebuild"
	s, _, _ := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	before := readRetryWindow(t, s, workID, "repair")
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild from the log: %v", err)
	}
	after := readRetryWindow(t, s, workID, "repair")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rebuilt window = %+v, want the live window %+v", after, before)
	}
}

func TestWorkflowRetryWindowSplitLeafBasisSerializes(t *testing.T) {
	t.Parallel()
	const workID = "retry-window-split-leaf"
	fixture := splitProgressRawFixture(t, workID)
	acceptanceSeq := splitProgressAcceptLeaf(t, fixture, workID, testJobSizeBaseCommit(), "job:successor-a", "internal/store/worker_jobs.go", 1, false)
	if acceptanceSeq == 0 {
		t.Fatal("the split-leaf acceptance recorded no sequence")
	}
	s := workerJobSizeStore(fixture)
	window := readRetryWindow(t, s, workID, currentStep(t, s, workID))
	// The funded split journey escalates the wall and derives the upstream
	// split_leaf_progress basis; the window carries it verbatim.
	if window.BlockingPopulation == "none" {
		t.Fatalf("blocking population = %q, want the escalated wall behind the funded split", window.BlockingPopulation)
	}
	if window.ConvergenceBasis != "split_leaf_progress" {
		t.Fatalf("convergence basis = %q, want split_leaf_progress", window.ConvergenceBasis)
	}
	if marshaled, err := json.Marshal(window); err != nil {
		t.Fatal(err)
	} else if err := payloadschema.Validate("workflow_retry_window", marshaled); err != nil {
		t.Fatalf("the produced split-leaf window violates the published contract: %v", err)
	}
	pin := issue1013Pin(t, s, workID)
	if pin.RetryWindow == nil || pin.RetryWindow.ConvergenceBasis != "split_leaf_progress" {
		t.Fatalf("pin retry window = %+v, want the split_leaf_progress basis", pin.RetryWindow)
	}
	if marshaled, err := json.Marshal(pin); err != nil {
		t.Fatal(err)
	} else if err := payloadschema.Validate("work_pin", marshaled); err != nil {
		t.Fatalf("the split-leaf pin violates the closed pin contract: %v", err)
	}
}

func TestWorkflowRetryWindowDuplicateContractPinStaysReadableWithoutAWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)
	before, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	if before.RetryWindow == nil {
		t.Fatal("the managed pin carried no retry window before the duplication")
	}
	duplicateActiveWorkflowContract(ctx, t, f.store, f.workID)
	pin, err := ReadWorkPin(ctx, f.store, f.workID)
	if err != nil {
		t.Fatalf("duplicate active contracts must keep the pin readable: %v", err)
	}
	// The ambiguous projection is the recovery's subject: the duplicate pin
	// keeps its readable identity and carries no retry window, because the
	// singular active-contract reads inside the window would either refuse
	// or invent a baseline the projection cannot answer.
	if pin.RetryWindow != nil {
		t.Fatalf("duplicate-contract pin carries a retry window = %+v, want none over the ambiguous projection", pin.RetryWindow)
	}
	if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("pin omits supersede_contract while the projection is ambiguous; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
}

func TestWorkflowRetryWindowRegistryThreadedPinKeepsTheSameWindow(t *testing.T) {
	t.Parallel()
	const workID = "retry-window-registry-threaded"
	s, _, builtinPin := seedIssue1013EscalatedCorrection(t, workID)
	defer s.Close()
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewWorkflowDefinitionRegistry()
	registered, err := registry.Register(entry.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if registered.Digest != entry.Digest {
		t.Fatalf("caller registry digest = %s, want the recorded %s", registered.Digest, entry.Digest)
	}
	var threaded WorkPin
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		var txErr error
		threaded, _, txErr = ReadMutationWorkPinTxForRegistry(context.Background(), tx, registry, workID, nil)
		return txErr
	}); err != nil {
		t.Fatalf("registry-threaded pin: %v", err)
	}
	if threaded.RetryWindow == nil {
		t.Fatal("the registry-threaded pin carries no retry window")
	}
	if !reflect.DeepEqual(threaded.RetryWindow, builtinPin.RetryWindow) {
		t.Fatalf("registry-threaded window = %+v, want the builtin pin's window %+v", threaded.RetryWindow, builtinPin.RetryWindow)
	}
}

func TestWorkflowRetryWindowVerificationBaselineNamesTheHealthyVerdict(t *testing.T) {
	t.Parallel()
	const workID = "retry-window-healthy-baseline"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	window := readRetryWindow(t, fixture.store, workID, "complete")
	if window.VerificationBaseline == nil {
		t.Fatal("a recorded healthy verdict set names no verification baseline anchor")
	}
	if window.VerificationBaseline.Kind != WorkflowVerdictRecorded || window.VerificationBaseline.Qualification != "comparable_healthy_verdict_baseline" {
		t.Fatalf("baseline anchor = %+v, want the recorded verdict's own row", window.VerificationBaseline)
	}
	var eventID, kind string
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT event_id,kind FROM domain_events WHERE seq=? AND subject_type='work_item' AND subject_id=?`, window.VerificationBaseline.Seq, workID).Scan(&eventID, &kind); err != nil {
		t.Fatal(err)
	}
	if window.VerificationBaseline.EventID != eventID || window.VerificationBaseline.Kind != kind {
		t.Fatalf("baseline anchor names (%s,%s), want the log row (%s,%s)", window.VerificationBaseline.EventID, window.VerificationBaseline.Kind, eventID, kind)
	}
	// The verdict-step journey records the healthy verdict without a worker
	// attempt: no productive acceptance exists, so the satisfaction anchor
	// is an explicit null and the dispatch population counts nothing.
	if window.WorkerSatisfactionAnchor != nil {
		t.Fatalf("worker satisfaction anchor = %+v, want an explicit null on a journey with no accepted worker result", window.WorkerSatisfactionAnchor)
	}
	if window.DispatchCount != 0 {
		t.Fatalf("dispatch count = %d, want zero on a journey with no dispatch", window.DispatchCount)
	}
	if window.BlockingPopulation != "none" {
		t.Fatalf("blocking population = %q on a healthy journey", window.BlockingPopulation)
	}
	if window.CorrectionEscalated {
		t.Fatal("a healthy journey reports the correction wall")
	}
}

func TestWorkflowRetryWindowCutoffBaselineInventsNoAnchor(t *testing.T) {
	t.Parallel()
	const workID = "retry-window-cutoff-baseline"
	fixture, _ := seedCompleteStepCorrection(t, workID, "workflow.break_fix", "verify", "complete")
	s := fixture.store
	before := readRetryWindow(t, s, workID, "complete")
	if before.VerificationBaseline == nil || before.VerificationBaseline.Kind != WorkflowVerdictRecorded {
		t.Fatalf("pre-cutoff baseline = %+v, want the recorded healthy verdict", before.VerificationBaseline)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", completeStepSuccessorPayload(workID), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("complete-step correction supersession: %v", err)
	}
	after := readRetryWindow(t, s, workID, currentStep(t, s, workID))
	// The complete-step cutoff sits above the last healthy verdict. The
	// verification window counts from the cutoff, and its boundary names no
	// verdict event: the anchor stays an explicit null instead of relabeling
	// the supersession as a healthy verdict.
	if after.VerificationBaseline != nil {
		t.Fatalf("post-cutoff baseline = %+v, want an explicit null: the cutoff is a supersession, not a verdict", after.VerificationBaseline)
	}
	if after.VerificationCount != 0 {
		t.Fatalf("post-cutoff verification count = %d, want the requests since the cutoff", after.VerificationCount)
	}
	if after.CorrectionEscalated || after.BlockingPopulation != "none" {
		t.Fatalf("post-cutoff wall = escalated %v blocking %q, want neither", after.CorrectionEscalated, after.BlockingPopulation)
	}
	pin := issue1013Pin(t, s, workID)
	if pin.RetryWindow == nil || pin.RetryWindow.VerificationBaseline != nil {
		t.Fatalf("pin retry window = %+v, want the same truthful null baseline", pin.RetryWindow)
	}
}

func TestWorkflowRetryWindowCountsTheVerificationPopulationLikeAdmission(t *testing.T) {
	t.Parallel()
	const workID = "retry-window-verification-count"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "execution")
	s, owner := fixture.store, fixture.owner
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	reviewer := acceptReturnRouteWorker(t, fixture, workID, ownerRef)
	if err := runVerdictActionAs(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":1,"verdicts":[{"predicate_id":"predicate:return-route","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:return-route-verification"],"incomparable_with_approved":true}]}`), 0, reviewer); err != nil {
		t.Fatalf("record non-ok verdict: %v", err)
	}
	payload := json.RawMessage(`{"diagnosis":"the delivered subject does not satisfy the approved predicate","strategy":"repeat the implementation external effect","predicate_ids":["predicate:return-route"],"evidence_refs":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "request_correction", payload, owner, fixture.operator); err != nil {
		t.Fatalf("request correction: %v", err)
	}
	step := currentStep(t, s, workID)
	window := readRetryWindow(t, s, workID, step)
	// The verification population and the dispatched packet's correction
	// context are one derivation: the window's count equals the context's.
	correction, err := workflowCorrectionContextForDispatch(context.Background(), s.db, workID, step, "")
	if err != nil {
		t.Fatal(err)
	}
	if correction == nil || correction.Disposition != "verification" || correction.AttemptCount != 1 {
		t.Fatalf("verification context = %+v, want the one counted request", correction)
	}
	if window.VerificationCount != correction.AttemptCount {
		t.Fatalf("window verification count = %d, want the context's %d", window.VerificationCount, correction.AttemptCount)
	}
	if window.BlockingPopulation != "none" || window.CorrectionEscalated {
		t.Fatalf("one request stands below the limit: blocking=%q escalated=%v", window.BlockingPopulation, window.CorrectionEscalated)
	}
	if window.WorkerSatisfactionAnchor == nil || window.WorkerSatisfactionAnchor.ActionID != "accept_worker_result" {
		t.Fatalf("worker satisfaction anchor = %+v, want the accepted delivery", window.WorkerSatisfactionAnchor)
	}
	pin := issue1013Pin(t, s, workID)
	if pin.RetryWindow == nil || pin.RetryWindow.VerificationCount != 1 {
		t.Fatalf("pin retry window = %+v, want the counted verification population", pin.RetryWindow)
	}
}
