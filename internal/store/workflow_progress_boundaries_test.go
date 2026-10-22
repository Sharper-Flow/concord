package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The progress fold's satisfactions own
// the nonprogress window alone (CD-0164 D2a): they must never move the
// worker recovery boundary, the correction counting window, the failed
// attempt identity, a consumed convergence basis, or the
// unresolved failed-job and unconfirmed checks-repair debts. These tests run
// the boundary on the fresh obligation-satisfaction definition heads through
// real recorded events and real admission reads.

// progressBoundariesSuccessorPayload supersedes the seeded contract with one
// that repeats the active predicate byte for byte and adds one unbound
// required evidence kind, so a real bind_evidence of that kind is the
// successor's first qualified evidence satisfaction. The successor rides the
// ordinary contract-correction route at the worker-dispatch checkpoint.
func progressBoundariesSuccessorPayload(t *testing.T, s *Store, workID string) json.RawMessage {
	t.Helper()
	projection, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Contract == nil || len(projection.Contract.OutcomePredicates) == 0 {
		t.Fatalf("active contract declares no predicate to repeat: %+v", projection.Contract)
	}
	predicate := projection.Contract.OutcomePredicates[0]
	binding := `{"domain_registry_content_hash":"sha256:` + strings.Repeat("b", 64) + `","home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
	successor := map[string]any{
		"contract_version": 2,
		"premise":          projection.Contract.Premise,
		"outcome_predicates": []map[string]any{{
			"predicate_id":    predicate.PredicateID,
			"ordinal":         0,
			"outcome_kind":    predicate.OutcomeKind,
			"outcome_payload": json.RawMessage(predicate.OutcomePayload),
		}},
		"required_evidence":    []string{"verification", "artifact", "commit"},
		"route_conventions":    []string{},
		"spec_mandate":         []string{},
		"law_modifies":         []string{},
		"decision_domains":     []string{},
		"rigor_class":          "prototype_internal",
		"architecture_binding": json.RawMessage(binding),
		"supersede_reason":     "the correction journey must name one required evidence kind the history has not bound",
		"audit_evidence":       []string{"evidence:" + workID + "-progress-supersede"},
	}
	raw, err := json.Marshal(successor)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// progressBoundariesSeed parks a real break_fix fresh-head instance at the
// repair step, so the boundary journeys run real recorded events on the
// obligation-satisfaction head.
func progressBoundariesSeed(t *testing.T, workID, step string) (workflowReturnRouteFixture, WorkflowActor) {
	t.Helper()
	fixture := seedChecksRepairFixture(t, workID, step)
	reviewSubjectClaim(t, fixture.store, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	return fixture, worker
}

// TestCoordinatorSatisfactionPreservesFailedJobCorrection
// A coordinator evidence binding cannot discharge a failed job or substitute
// another correction source for the failed attempt's own record.
func TestCoordinatorSatisfactionPreservesFailedJobCorrection(t *testing.T) {
	ctx := context.Background()
	const workID = "progress-boundary-retry-binding"
	const step = "repair"
	fixture, worker := progressBoundariesSeed(t, workID, step)
	s := fixture.store
	defer s.Close()
	const jobID = "job:progress-boundary"
	if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, map[string]any{
		"job_id": jobID, "objective": "Satisfy the failed obligation behind the coordinator satisfaction",
		"stopping_condition": "the recorded checks pass", "checks": []string{"go test ./internal/store"},
		"ready": true, "readiness_evidence": []string{"evidence:progress-boundary-ready"},
	}); err != nil {
		t.Fatal(err)
	}
	views, err := readWorkerJobRevisions(ctx, s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	var revision *WorkerJobBinding
	for i := range views {
		if views[i].Binding.JobID == jobID {
			binding := views[i].Binding
			revision = &binding
		}
	}
	if revision == nil {
		t.Fatal("the job revision was not recorded")
	}
	attempt := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, step, attempt, nil, worker, 0, "progress-boundary-attempt-1", revision)
	s2WorkerFailedAttributedLive(t, s, workID, attempt, "the attempt failed before a judgeable result")
	if records := s2DerivedFailureRecords(t, s, workID, attempt); len(records) != 1 {
		t.Fatalf("derived failure records = %d, want one", len(records))
	}
	before, err := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, step, "")
	if err != nil || before == nil || before.FailedAttemptID != attempt {
		t.Fatalf("failed correction=%+v err=%v", before, err)
	}
	if err := runVerdictActionAs(t, s, workID, "supersede_contract", progressBoundariesSuccessorPayload(t, s, workID), 0, fixture.owner); err != nil {
		t.Fatalf("supersede the contract at the dispatch checkpoint: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "commit", "immutable_subject_ref": "commit:" + workID}), 0, fixture.owner); err != nil {
		t.Fatalf("bind the newly required evidence kind: %v", err)
	}
	after, err := workflowCorrectionContextForDispatch(ctx, s.DatabaseForTesting(), workID, step, "")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("evidence binding changed the failed correction: before=%+v after=%+v err=%v", before, after, err)
	}
	history, err := workflowCorrectionWalkWorkerSatisfaction(ctx, s.DatabaseForTesting(), workID, 1<<62, "progress_boundaries")
	if err != nil {
		t.Fatal(err)
	}
	if !history.unresolved[workerJobKey(revision.JobID, revision.Revision)] {
		t.Fatalf("evidence binding discharged the failed job: %v", history.unresolved)
	}
}

// TestProgressSatisfactionSpendsUnderChecksRepairDebtAndClearsOnlyWithConfirmation
// proves the debt boundary at the fold: a qualified coordinator satisfaction
// recorded beside failed-job debt and an accepted, unconfirmed checks repair
// is spent — it renews nothing, discharges no failed obligation, and neither
// confirms nor voids the repair — and only the confirming review's accepted
// ship verdict clears both debts in one move and reopens the window.
func TestProgressSatisfactionSpendsUnderChecksRepairDebtAndClearsOnlyWithConfirmation(t *testing.T) {
	t.Parallel()
	db, appendEvent, read := progressHistory(t, false)
	coordinator := progressActorRecord(t, db, "coordinator", "agent")
	bound := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")

	// A failed job-bound attempt creates debt; the checks repair records the
	// repairing revision naming it; the repairing result's own acceptance
	// opens the repair without discharging the named obligation.
	const jobID = "job:debt-repair"
	appendEvent(WorkerJobRecorded, WorkerJobRecordedPayload{JobID: jobID, Revision: 1, ContractVersion: 1, Objective: "satisfy the failed obligation", Checks: []string{"go test ./internal/store"}})
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:failed", jobID, 1))
	appendEvent(WorkerFailed, map[string]any{"attempt_id": "attempt:failed"})
	repair := WorkerJobRecordedPayload{JobID: jobID, Revision: 2, ContractVersion: 1, Objective: "satisfy the failed obligation", Checks: []string{"go test ./internal/store -run TestRepair"},
		ChecksRepair: &WorkerJobChecksRepair{Reason: "the named revision's checks cannot run as written", Revisions: []WorkerJobChecksRepairEntry{{WorkerJob: WorkerJobBinding{JobID: jobID, Revision: 1}, Checks: []string{"go test ./internal/store"}}}}}
	appendEvent(WorkerJobRecorded, repair)
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:repairing", jobID, 2))

	// The qualified coordinator satisfaction lands beside both debts: spent.
	confirmAttempt := "attempt:confirming"
	appendEvent(WorkflowActionCompleted, map[string]any{
		"action_id": "dispatch_worker", "worker_attempt_id": confirmAttempt,
		"checks_repair_review": WorkerChecksRepairReviewAdmission{WorkerJob: WorkerJobBinding{JobID: jobID, Revision: 2}, Funded: true},
	})
	spentSeq := appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	h := read()
	if h.nonprogressBoundary != bound || h.boundary != 0 {
		t.Fatalf("anchors = %d/%d, want %d/0 — the satisfaction under debt renewed or discharged", h.nonprogressBoundary, h.boundary, bound)
	}
	if len(h.progress.satisfactions) != 1 {
		t.Fatalf("satisfactions = %d, want only the pre-debt evidence binding", len(h.progress.satisfactions))
	}
	if !h.unresolved[workerJobKey(jobID, 1)] {
		t.Fatalf("unresolved = %v, want the failed revision's obligation intact", h.unresolved)
	}
	repairState := h.checksRepairs[workerJobKey(jobID, 2)]
	if repairState == nil || !repairState.openUnconfirmed() || repairState.confirmed || repairState.voided {
		t.Fatalf("checks repair state = %+v, want open unconfirmed and untouched", repairState)
	}
	if repairState.confirmAttempt != confirmAttempt {
		t.Fatalf("confirming attempt = %q, want %q retained beside the spent satisfaction", repairState.confirmAttempt, confirmAttempt)
	}
	if seq := appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"}); read().nonprogressBoundary != bound {
		t.Fatalf("a repeat confirmation at %d renewed the spent obligation", seq)
	}

	// Only the confirming review's accepted ship verdict confirms the repair,
	// discharges the named obligation, and moves the acceptance boundary.
	appendEvent(WorkerCompleted, WorkerCompletedPayload{AttemptID: confirmAttempt, Review: &WorkerReviewBlock{Disposition: "ready_for_final_proof"}})
	confirmed := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", confirmAttempt, "", 0))
	h = read()
	confirmedRepair := h.checksRepairs[workerJobKey(jobID, 2)]
	if h.boundary != confirmed {
		t.Fatalf("acceptance boundary = %d, want the confirming acceptance at %d", h.boundary, confirmed)
	}
	if len(h.unresolved) != 0 || confirmedRepair == nil || !confirmedRepair.confirmed {
		t.Fatalf("unresolved = %v confirmed = %+v, want the named obligation discharged with the repair confirmed", h.unresolved, confirmedRepair)
	}
	// The productive acceptance renews the nonprogress window the worker way:
	// the anchor names the acceptance, and the spent premise satisfaction
	// minted no credit of its own.
	if h.nonprogressBoundary != confirmed || h.progress.qualification != "satisfying_worker_acceptance" {
		t.Fatalf("nonprogress renewal = %d/%q, want the confirming acceptance %d as the worker acceptance", h.nonprogressBoundary, h.progress.qualification, confirmed)
	}
	if len(h.progress.satisfactions) != 1 {
		t.Fatalf("satisfactions = %d, want the spent premise to have minted none", len(h.progress.satisfactions))
	}

	// A genuinely distinct obligation recorded after the debt clears renews.
	fresh := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, SpecMandate: []string{"law:fresh"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &fresh})
	renewed := appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:fresh", ProducerID: "producer:x", ProducerRunRef: "run:fresh", ProducerWatermark: "request:fresh", ObservedAt: "2026-10-20T00:00:00Z"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != renewed {
		t.Fatalf("renewal after the debt cleared = %d, want the fresh mandate binding at %d", h.nonprogressBoundary, renewed)
	}
	_ = spentSeq
}

// TestProgressSatisfactionKeepsConsumedConvergenceScopeAtTheAcceptanceBoundary
// proves the consumed-allowance scope: a recorded host_changed basis rides a
// dispatch completion, and a later coordinator satisfaction moves only the
// nonprogress anchor. The acceptance boundary — the seq boundary the
// consumed-basis read scopes its consumed test with — never moves for a
// satisfaction, so a consumed host_changed allowance cannot be renewed by
// coordinator progress; only a productive acceptance moves it.
func TestProgressSatisfactionKeepsConsumedConvergenceScopeAtTheAcceptanceBoundary(t *testing.T) {
	t.Parallel()
	db, appendEvent, read := progressHistory(t, false)
	appendEvent(WorkflowActionCompleted, map[string]any{
		"action_id": "dispatch_worker", "worker_attempt_id": "attempt:converged",
		"retry_convergence": WorkflowRetryConvergence{Basis: "host_changed", LatestRecordSeq: 7, HostPID: 4242, HostPIDStart: 99},
	})
	bound := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	h := read()
	if h.nonprogressBoundary != bound {
		t.Fatalf("nonprogress boundary = %d, want the satisfaction at %d", h.nonprogressBoundary, bound)
	}
	if h.boundary != 0 {
		t.Fatalf("acceptance boundary = %d, want 0 — a coordinator satisfaction must never move the consumed-basis scope", h.boundary)
	}
	if got, err := workflowCorrectionWindowBoundary(context.Background(), db, "work-history", 1<<62, "progress_test"); err != nil || got != 0 {
		t.Fatalf("correction window boundary = (%d, %v), want 0 so the recorded consumed basis stays inside the consumed test", got, err)
	}
	// The same dispatch completion carrying the consumed basis still sits
	// above the boundary the host convergence read scans.
	var consumed int64
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id='work-history' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.retry_convergence.basis')='host_changed'`, WorkflowActionCompleted).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 {
		t.Fatalf("recorded host_changed completions = %d, want the consumed basis retained", consumed)
	}
}

// TestArmedProgressWallWallsOnlyWorkerDispatch proves the armed wall's reach
// on a real walled history: the wall refuses the next worker dispatch with
// the convergence refusal, folds its populations into the readable window,
// and refuses no coordinator-only action the parked step declares — the wall
// implies nothing about coordinator-only actions, so a satisfaction path can
// finish the work without another dispatch.
func TestArmedProgressWallWallsOnlyWorkerDispatch(t *testing.T) {
	t.Parallel()
	const workID = "progress-armed-wall-coordinator-only"
	f := seedResearchWalledCorrection(t, workID)
	s := f.s
	defer s.Close()

	// The next worker dispatch refuses at the wall and records nothing.
	counters := walledResearchReadCounters(t, f)
	walledResearchAssertRefused(t, f, dispatchCorrectionAuthorize(t, f, "attempt:"+workID+":5", f.researchLane, f.dispatcher), counters, "the dispatch behind the armed wall")

	// The wall walls dispatch alone. Every other declared action answers the
	// folded admission state without the convergence refusal, so the
	// coordinator-only satisfaction path stays open beside the armed wall.
	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	var state WorkflowAdmissionState
	state, _, _, loadErr := loadWorkflowAdmissionStateTxForRegistry(context.Background(), s.DatabaseForTesting(), workID, entry.Definition, f.step, "progress_boundaries", BuiltinWorkflowRegistry())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if !state.retryEscalated() {
		t.Fatalf("folded state behind the walled correction = %+v, want the wall armed", state)
	}
	decided := workflowAdmit(entry.Definition, state, "dispatch_worker")
	if decided.Admitted || !decided.ConvergenceRequired || decided.Failure == nil || decided.Failure.Kind != KindMissingEvidence {
		t.Fatalf("dispatch decision = %+v, want the convergence refusal", decided)
	}
	walled := 0
	for _, action := range entry.Definition.AvailableActions {
		if action == "dispatch_worker" {
			continue
		}
		decision := workflowAdmit(entry.Definition, state, action)
		if decision.ConvergenceRequired || (decision.Failure != nil && strings.Contains(decision.Failure.Detail, "three-attempt limit")) {
			t.Fatalf("action %s behind the armed wall = %+v, want no convergence refusal on a coordinator-only action", action, decision)
		}
		if decision.Admitted {
			walled++
		}
	}
	if walled == 0 {
		t.Fatal("no coordinator-only action stands admitted beside the armed wall; the satisfaction path is closed")
	}
}
