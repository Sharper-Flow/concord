package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// progressBoundariesSuccessorPayload supersedes the seeded contract with one
// that repeats the active predicate byte for byte and adds one unbound
// required evidence kind for a real bind_evidence action. The successor rides the
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
	binding := `{"home_domain_id":"root","affected_domain_ids":["root"],"domain_modifies":[],"domain_relation_modifies":[],"law_additions":[],"verification_obligations":[]}`
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

// progressBoundariesSeed parks a current break_fix instance at the repair
// step, so the boundary journeys run real recorded events.
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

// Coordinator facts cannot discharge a failed obligation or confirm its
// checks repair. Only the assigned confirming review's acceptance can.
func TestCoordinatorFactsPreserveChecksRepairDebtUntilConfirmation(t *testing.T) {
	t.Parallel()
	db, appendEvent, read := progressCorrectionHistory(t)
	coordinator := progressActorRecord(t, db, "coordinator", "agent")
	progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")

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

	confirmAttempt := "attempt:confirming"
	appendEvent(WorkflowActionCompleted, map[string]any{
		"action_id": "dispatch_worker", "worker_attempt_id": confirmAttempt,
		"checks_repair_review": WorkerChecksRepairReviewAdmission{WorkerJob: WorkerJobBinding{JobID: jobID, Revision: 2}, Funded: true},
	})
	appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	h := read()
	if h.boundary != 0 {
		t.Fatalf("acceptance boundary = %d, want no discharge under repair debt", h.boundary)
	}
	if !h.unresolved[workerJobKey(jobID, 1)] {
		t.Fatalf("unresolved = %v, want the failed revision's obligation intact", h.unresolved)
	}
	repairState := h.checksRepairs[workerJobKey(jobID, 2)]
	if repairState == nil || !repairState.openUnconfirmed() || repairState.confirmed || repairState.voided {
		t.Fatalf("checks repair state = %+v, want open unconfirmed and untouched", repairState)
	}
	if repairState.confirmAttempt != confirmAttempt {
		t.Fatalf("confirming attempt = %q, want %q retained beside the coordinator fact", repairState.confirmAttempt, confirmAttempt)
	}
	appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	if repeated := read(); repeated.boundary != 0 || !repeated.unresolved[workerJobKey(jobID, 1)] || !reflect.DeepEqual(repeated.checksRepairs[workerJobKey(jobID, 2)], repairState) {
		t.Fatalf("a repeated coordinator confirmation changed the repair debt: %+v", repeated)
	}

	// Only the confirming review's accepted disposition confirms the repair,
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
	// A new contract and evidence binding are still not worker acceptance.
	fresh := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, SpecMandate: []string{"law:fresh"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &fresh})
	appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:fresh", ProducerID: "producer:x", ProducerRunRef: "run:fresh", ProducerWatermark: "request:fresh", ObservedAt: "2026-10-20T00:00:00Z"})
	if h := read(); h.boundary != confirmed {
		t.Fatalf("evidence binding moved acceptance boundary to %d, want %d", h.boundary, confirmed)
	}
}

// A repeated correction leaves dispatch and coordinator-only routes available.
func TestRepeatedCorrectionKeepsCoordinatorRoutes(t *testing.T) {
	t.Parallel()
	const workID = "progress-armed-wall-coordinator-only"
	f := seedResearchWalledCorrection(t, workID)
	s := f.s
	defer s.Close()

	entry, err := VerifyWorkflowInstanceDefinition(context.Background(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	var state WorkflowAdmissionState
	state, _, _, loadErr := loadWorkflowAdmissionStateTxForRegistry(context.Background(), s.DatabaseForTesting(), workID, entry.Definition, f.step, "progress_boundaries", BuiltinWorkflowRegistry())
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	decided := workflowAdmit(entry.Definition, state, "dispatch_worker")
	if !decided.Admitted {
		t.Fatalf("dispatch decision = %+v, want the correction route available", decided)
	}
	walled := 0
	for _, action := range entry.Definition.AvailableActions {
		if action == "dispatch_worker" {
			continue
		}
		decision := workflowAdmit(entry.Definition, state, action)
		if decision.Admitted {
			walled++
		}
	}
	if walled == 0 {
		t.Fatal("no coordinator-only action stands admitted beside the armed wall; the satisfaction path is closed")
	}
}
