package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// A changed criterion invalidates the accepted Review without replacing its
// candidate. The authenticated coordinator returns the parked gate to its
// producer through the evidence-bearing correction route.

// deliveryRecoveryLane returns the builtin lane for one capability class.
func deliveryRecoveryLane(t *testing.T, capabilityClass string) store.LaneDefinition {
	t.Helper()
	for _, candidate := range store.BuiltinLaneDefinitions() {
		if candidate.CapabilityClass == capabilityClass {
			return candidate
		}
	}
	t.Fatalf("no builtin lane carries capability class %q", capabilityClass)
	return store.LaneDefinition{}
}

// deliveryRecoveryPacket builds the worker packet for one dispatch on the
// given lane, consuming the pin's correction when one is durable.
func deliveryRecoveryPacket(t *testing.T, s *store.Store, stepID, attemptID, capabilityClass string) map[string]any {
	t.Helper()
	const workID = "work-1"
	lane := deliveryRecoveryLane(t, capabilityClass)
	inputs := map[string]any{"task": "advance the approved gate objective", "constraints": []string{"preserve the approved contract"}}
	pin, err := store.ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction != nil {
		inputs["correction"] = pin.Correction
	}
	// The dispatch selects the freshly recorded ready revision explicitly,
	// as the adapter does, so an earlier revision on the step never shadows
	// the one this dispatch binds.
	snapshot, snapshotErr := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: workID})
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if len(snapshot.ReadyWorkerJobs) > 0 {
		selected, selectErr := json.Marshal(snapshot.ReadyWorkerJobs[len(snapshot.ReadyWorkerJobs)-1])
		if selectErr != nil {
			t.Fatal(selectErr)
		}
		inputs["worker_job"] = json.RawMessage(selected)
	}
	return bindPacketToRecordedState(t, s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion, "attempt_id": attemptID, "lane_id": lane.ID, "lane_version": lane.Version, "lane_digest": lane.Digest, "work_id": workID, "step_id": stepID, "inputs": inputs})
}

// deliveryRecoveryCompletion records the lane dispatch and the completed
// report for one dispatched worker attempt. The completion lands at the
// current payload version through the live terminal seam: the implementation
// lane carries the core delivery observation the authenticated preparation
// sampled, and a review lane carries its typed review block.
func deliveryRecoveryCompletion(t *testing.T, s *store.Store, grant Authority, attemptID, capabilityClass string) {
	t.Helper()
	lane := deliveryRecoveryLane(t, capabilityClass)
	job := authorizedWorkerJob(t, s, attemptID)
	dispatch := store.Event{EventID: "delivery-recovery-dispatch-" + attemptID, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerDispatched), Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attemptID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: "sha256:" + strings.Repeat("c", 64), ReadbackModel: "openai/gpt-5.6-luna", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		enriched, err := store.PrepareLaneActorDispatch(context.Background(), tx, dispatch, grant.PrincipalRef, grant.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(context.Background(), tx, enriched)
		return err
	}); err != nil {
		t.Fatalf("seed delivery recovery dispatch %s: %v", attemptID, err)
	}
	payload := store.WorkerCompletedPayload{AttemptID: attemptID, ReadbackModel: "openai/gpt-5.6-luna", ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job, EvidenceOrigin: store.WorkerEvidenceReported}
	// A live completion on a lane that requires the typed review block
	// carries it (CD-0197). An oracle-bound dispatch's review findings carry
	// their oracle classification (CD-0198).
	if len(lane.RequiredReportBlocks) > 0 {
		payload.EvidenceOrigin = store.WorkerEvidenceLegacyUnavailable
		pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
		if err != nil || pin.Review == nil || pin.Review.Dependencies == nil {
			t.Fatalf("gate Review dependencies: %v", err)
		}
		path := agentWorkerDirectory(t, s, "work-1")
		candidate := store.WorkerReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, path, "rev-parse", "HEAD"), Tree: gitRun(t, path, "rev-parse", "HEAD^{tree}")}
		payload.Review = &store.WorkerReviewBlock{Disposition: "correction_required", InputCandidate: candidate, OutputCandidate: candidate, Dependencies: *pin.Review.Dependencies,
			Assessments: []store.WorkerReviewAssessment{}, RemainingObligations: []store.WorkerReviewObligation{},
			Findings: []store.WorkerReviewFinding{{FindingID: "finding:gate-result", Status: "open", Severity: "P0", Confidence: "high", Detail: "the refined result misses the approved contract", EvidenceRefs: []string{"evidence:gate-review"}, ClosureRefs: []string{}}}}
		if job != nil {
			if oracle := recordedAgentOracle(t, s, job); oracle != nil {
				owner := oracle.Owners[0]
				payload.Review.Findings[0].Oracle = &store.WorkerOracleFinding{
					Classification: store.OracleClassificationFollowUp,
					OwnerID:        owner.OwnerID, PredicateIDs: owner.PredicateIDs,
				}
			}
		}
	}
	agentWorkerCompletedThroughSeam(t, s, attemptID, "delivery-recovery-completed-"+attemptID, payload)
}

func TestDeliveryGateRecoveryReturnsStaleReviewThroughAgent(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	ctx := context.Background()
	seedCurrentWorkflowDomainFixture(t, s)

	worktree := t.TempDir()
	grant.Worktree = worktree

	currentDefinition, err := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	grantActorRef, err := store.WorkflowActorRef(grantActor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: currentDefinition, Actor: grantActor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE workflow_instances SET current_step='execution' WHERE work_id='work-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'approved gate return objective','internal_sqlite','["review"]','[]','now',?,'[]','[]',0,'prototype_internal')`, grantActorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:gate-recovery","immutable_subject_ref":"commit:gate-recovery","expected_result":"pass"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?)`, store.WorktreeSetID("work-1"), "project-1", "claim:gate-recovery", "gate-recovery", strings.Repeat("a", 40), worktree, "repo:gate-recovery", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	fixture := oAgentFixture(t, s, service, env, "work-1")
	oAgentControlRepository(t, fixture)
	invoke := func(input map[string]any) Envelope {
		t.Helper()
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	stepOf := func() string {
		t.Helper()
		var step string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id='work-1'`).Scan(&step); err != nil {
			t.Fatal(err)
		}
		return step
	}
	epochOf := func(actionID string) int64 {
		t.Helper()
		var epoch int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.attempt_epoch') FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')=? ORDER BY seq DESC LIMIT 1`, store.WorkflowActionStarted, actionID).Scan(&epoch); err != nil {
			t.Fatal(err)
		}
		return epoch
	}
	version := func() int64 { return workVersion(t, s, "work-1") }
	dispatch := func(stepID, attemptID, capabilityClass string) {
		t.Helper()
		// Each dispatch binds its own recorded ready revision: an accepted
		// job is satisfied and never redispatches (CD-0205).
		recordReadyRetryJob(t, s, service, env, "job:"+attemptID)
		if stepID == "refine" && capabilityClass == "implementation" {
			oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "continue", false)
		}
		response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "dispatch_worker", "fields": map[string]any{"attempt_id": attemptID, "worker_packet": deliveryRecoveryPacket(t, s, stepID, attemptID, capabilityClass)}, "idempotency_key": "gate-dispatch-" + attemptID})
		if response.Outcome != OutcomeOK {
			t.Fatalf("gate recovery dispatch %s: %+v", attemptID, response.Error)
		}
		if capabilityClass == "implementation" {
			deliverAgentFixtureChange(t, s, "work-1", attemptID)
		}
		deliveryRecoveryCompletion(t, s, grant, attemptID, capabilityClass)
	}

	// The execution pass, its bound review evidence, and the refinement pass.
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "start_execution", "idempotency_key": "gate-execution-start"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery start_execution: %+v", response.Error)
	}
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "review", "immutable_subject_ref": "evidence:gate-review"}, "idempotency_key": "gate-evidence-bind"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery bind_evidence: %+v", response.Error)
	}
	dispatch("execution", "attempt:work-1:gate-execution", "implementation")
	executionEpoch := epochOf("dispatch_worker")
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "accept_worker_result", "fields": map[string]any{"attempt_id": "attempt:work-1:gate-execution", "attempt_epoch": executionEpoch}, "idempotency_key": "gate-execution-accept"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery execution accept: %+v", response.Error)
	}
	// The job-bound accept is local acceptance and holds execution; the step
	// exits through its delivery assertion, which requires qualifying
	// core-owned worktree verification covering the job's Project (CD-0205 D3).
	integrationRef := oAgentVerify(t, fixture, []string{"go", "vet", "./..."})
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "bind_evidence", "fields": map[string]any{"evidence_kind": "verification", "evidence_ref": integrationRef}, "idempotency_key": "gate-execution-integration"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery integration bind: %+v", response.Error)
	}
	recordReadyRetryJob(t, s, service, env, "job:gate-final-review")
	oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "ready_for_final_proof", false)
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:gate-execution", "delivery_state": "asserted"}, "idempotency_key": "gate-execution-delivery"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery execution delivery: %+v", response.Error)
	}
	if stepOf() != "refine" {
		t.Fatalf("step after execution delivery = %q, want refine", stepOf())
	}
	// The canonical head derives the refine start from the qualifying
	// checkpoint pair; the callable start_refine retired with it.
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "checkpoint_refine", "fields": map[string]any{}, "idempotency_key": "gate-refine-start"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery checkpoint_refine: %+v", response.Error)
	}

	// A rejected review, then an accepted implement repair that changes the
	// candidate the earlier Review covered.
	dispatch("refine", "attempt:work-1:gate-review", "review")
	refineEpoch := attemptEpoch(t, s, "attempt:work-1:gate-review")
	reviewer := store.WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/gate-reviewer", SessionRef: "session/work-1-gate-reviewer", ActorClass: store.ActorAgent}
	runVerificationStoreAction(t, s, "reject_worker_result", map[string]any{
		"attempt_id": "attempt:work-1:gate-review", "attempt_epoch": refineEpoch,
		"diagnosis": "the review rejects the refined result", "strategy": "implement the review findings and resubmit",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:gate-review"},
	}, reviewer, nil, "gate-reject")
	dispatch("refine", "attempt:work-1:gate-repair", "implementation")
	if response := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "accept_worker_result", "fields": map[string]any{"attempt_id": "attempt:work-1:gate-repair", "attempt_epoch": attemptEpoch(t, s, "attempt:work-1:gate-repair")}, "idempotency_key": "gate-repair-accept"}); response.Outcome != OutcomeOK {
		t.Fatalf("gate recovery repair accept: %+v", response.Error)
	}

	// Main's repair acceptance changed the integrated candidate. The earlier
	// continue Review cannot authorize delivery of this different candidate.
	deliveryRefusal := invoke(map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "record_delivery", "fields": map[string]any{"delivery_artifact": "artifact:gate-recovery", "delivery_state": "asserted"}, "idempotency_key": "gate-recovery-delivery"})
	if deliveryRefusal.Outcome != OutcomeError || deliveryRefusal.Error == nil || deliveryRefusal.Error.Kind != "missing_evidence" || deliveryRefusal.Error.EffectState != "none" || !strings.Contains(deliveryRefusal.Error.Message, "accepted Review on the current candidate and depended-on revisions") {
		t.Fatalf("repair delivery exit = %+v, want a zero-effect current-candidate Review refusal", deliveryRefusal.Error)
	}
	recordReadyRetryJob(t, s, service, env, "job:gate-return-review")
	oAgentReview(t, oAgentFixture(t, s, service, env, "work-1"), "ready_for_final_proof", false)
	reviewed, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || reviewed.Review == nil || reviewed.Review.Accepted == nil || reviewed.Review.Candidate == nil || reviewed.Review.Dependencies == nil || !reviewed.Review.DependenciesCurrent || reviewed.Review.Accepted.Report.Disposition != "ready_for_final_proof" || reviewed.Review.Accepted.Report.OutputCandidate != *reviewed.Review.Candidate {
		t.Fatalf("ready Review does not cover the current candidate: %+v (%v)", reviewed.Review, err)
	}

	// Seed the contract-correction checkpoint for the successor's store-owned
	// setup, then park its unchanged candidate at the delivery gate below.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='acceptance' WHERE work_id='work-1';
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("seed contract-correction checkpoint: %v", err)
	}
	runVerificationStoreAction(t, s, "supersede_contract", map[string]any{
		"contract_version": 2, "premise": "approved gate return objective",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:gate-recovery", "immutable_subject_ref": "commit:gate-recovery", "expected_result": "fail"}}},
		"required_evidence":  []string{"review"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal", "decision_domains": []string{},
		"architecture_binding": workflowArchitectureBindingForSeededRegistry(t, s),
		"supersede_reason":     "the expected criterion result changed", "audit_evidence": []string{"evidence:gate-review"},
	}, grantActor, nil, "gate-recovery-successor")
	stale, err := store.ReadWorkPin(ctx, s, "work-1")
	if err != nil || stale.Review == nil || stale.Review.Accepted == nil || stale.Review.Candidate == nil || stale.Review.Dependencies == nil || stale.Review.DependenciesCurrent || *stale.Review.Candidate != *reviewed.Review.Candidate || stale.Review.Accepted.ReportRef != reviewed.Review.Accepted.ReportRef {
		t.Fatalf("successor did not stale the standing Review on the unchanged candidate: %+v (%v)", stale.Review, err)
	}
	if len(reviewed.Review.Dependencies.Criteria) != 1 || len(stale.Review.Dependencies.Criteria) != 1 || stale.Review.Dependencies.Criteria[0].PredicateID != "predicate:primary" || stale.Review.Dependencies.Criteria[0].ContentDigest == reviewed.Review.Dependencies.Criteria[0].ContentDigest {
		t.Fatal("successor did not change the depended-on criterion digest")
	}

	// Park the unchanged candidate with its now-inapplicable accepted Review.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step='delivery' WHERE work_id='work-1';
		DELETE FROM fold_guard`); err != nil {
		t.Fatalf("seed the parked gate position: %v", err)
	}

	// Correction binds the current criterion and durable evidence. Routine
	// recovery needs the authenticated coordinator, not an operator signature.
	correctionInput := map[string]any{"work_id": "work-1", "expected_version": version(), "action_id": "request_correction", "idempotency_key": "gate-recovery-return", "fields": map[string]any{
		"diagnosis": "the accepted Review judged the previous criterion revision", "strategy": "return to the execution step and reassess the candidate against the current criterion",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{"evidence:gate-review"},
	}}
	returned := invoke(correctionInput)
	if returned.Outcome != OutcomeOK {
		t.Fatalf("authenticated evidence-bearing gate return = %+v", returned.Error)
	}
	if got := stepOf(); got != "execution" {
		t.Fatalf("step after gate return = %q, want execution", got)
	}

	// The return asserted no further delivery: the execution pass's own
	// delivery assertion is the only one, and the parked change never
	// claimed one of its own.
	var deliveries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='record_delivery'`, store.WorkflowActionCompleted).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("gate recovery recorded %d delivery assertions, want the one execution-pass assertion", deliveries)
	}
	var definitionVersion int64
	var definitionDigest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_version,definition_digest FROM workflow_instances WHERE work_id='work-1'`).Scan(&definitionVersion, &definitionDigest); err != nil {
		t.Fatal(err)
	}
	if definitionVersion != currentDefinition.Definition.Version || definitionDigest != currentDefinition.Digest {
		t.Fatalf("definition after recovery = v%d %s, want the current definition %s", definitionVersion, definitionDigest, currentDefinition.Digest)
	}
}

// agentWorkerCompletedThroughSeam records one completed report through the
// live worker-evidence seam with the core delivery observation the
// authenticated terminal preparation sampled outside the transaction. A
// non-implementation attempt owes no observation and the preparation probes
// nothing.
func agentWorkerCompletedThroughSeam(t *testing.T, s *store.Store, attemptID, eventID string, payload store.WorkerCompletedPayload) {
	t.Helper()
	const workID = "work-1"
	ctx := context.Background()
	if payload.WorkerDirectory == "" {
		payload.WorkerDirectory = agentWorkerDirectory(t, s, workID)
	}
	if payload.EvidenceOrigin == "" {
		payload.EvidenceOrigin = store.WorkerEvidenceReported
	}
	// A reported completion discharges every lane obligation; a legacy
	// unavailable origin carries no reported evidence array.
	if payload.EvidenceOrigin == store.WorkerEvidenceReported && len(payload.Evidence) == 0 {
		payload.Evidence = agentEvidenceForLane(t, s, workID, attemptID)
	}
	completion := store.Event{EventID: eventID, Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerCompleted), Payload: retryJSON(payload)}
	prepared, err := store.PrepareWorkerTerminalDelivery(ctx, s, store.PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: attemptID, EventID: eventID})
	if err != nil {
		t.Fatalf("terminal delivery preparation for %s: %v", attemptID, err)
	}
	preparedCtx := store.WithWorkerTerminalPreparation(ctx, prepared)
	if err := s.Transact(preparedCtx, func(tx *store.Transaction) error {
		_, inner := store.ApplyWorkerEvidenceOperationTx(preparedCtx, tx, completion)
		return inner
	}); err != nil {
		t.Fatalf("completed report through the seam for %s: %v", attemptID, err)
	}
}

func agentWorkerDirectory(t *testing.T, s *store.Store, workID string) string {
	t.Helper()
	var directory string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND state='active' ORDER BY project_id LIMIT 1`, store.WorktreeSetID(workID)).Scan(&directory); err != nil {
		t.Fatal(err)
	}
	return directory
}

// agentEvidenceForLane discharges every obligation the attempt's lane
// declares, one entry each.
func agentEvidenceForLane(t *testing.T, s *store.Store, workID, attemptID string) []store.WorkerReportEvidence {
	t.Helper()
	var class string
	if err := s.DatabaseForTesting().QueryRow(`SELECT capability_class FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&class); err != nil {
		t.Fatalf("read the attempt lane for %s: %v", attemptID, err)
	}
	var laneID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lane_id FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&laneID); err != nil {
		t.Fatalf("read the attempt lane id for %s: %v", attemptID, err)
	}
	var lane *store.LaneDefinition
	for _, definition := range store.BuiltinLaneDefinitions() {
		if definition.ID == laneID {
			lane = &definition
			break
		}
	}
	if lane == nil {
		t.Fatalf("lane %s is not registered", laneID)
	}
	_ = class
	evidence := make([]store.WorkerReportEvidence, 0, len(lane.EvidenceObligations))
	for _, obligation := range lane.EvidenceObligations {
		evidence = append(evidence, store.WorkerReportEvidence{Obligation: obligation, Detail: "discharged " + obligation})
	}
	return evidence
}
