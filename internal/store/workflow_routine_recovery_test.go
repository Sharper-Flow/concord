package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The recovery consent owner keeps every recovery wall and classifies only
// the consent bit. Under consequence_v1 a supersession or correction request
// is routine record-keeping: the authenticated coordinator acts unsigned, the
// duplicate-recovery and complete-step routes consent without a consumed
// operator approval, self-repair may be classified only by the real recorded
// agent-class coordinator, and a classified contract gains no CD-0150 overlap
// exemption. Every retained gate holds unchanged: stale-law and complete-step
// state gates, audit-evidence and predecessor-set walls, exact failed
// attempt/epoch retry binding, the convergence wall, failure counts, and the
// legacy operator identity requirement on old pins.
//
// New-policy owners are exercised through verified-definition seams exactly
// as S3/S4: pure and transactional owners that take a definition value see
// the synthetic policy copy, and the builtin-only fold resolutions keep their
// legacy live coverage. No test registers a definition into the builtin
// registry, and no new-policy event is authored on a legacy pin: live
// builtin-registry end-to-end new-policy witnesses wait for the promotion
// slice.

// routineRecoveryPolicyDefinition copies a registered definition and declares
// consequence_v1 on the copy. The registry keeps the legacy version; only the
// definition-consuming owners under test see the declared policy.
func routineRecoveryPolicyDefinition(registered RegisteredDefinition) WorkflowDefinition {
	definition := registered.Definition
	definition.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
	return definition
}

// seedDuplicateContractProjection folds a second approved contract into the
// projection the way a historical duplicate recovery fixture does, leaving
// two active contracts for one work item on the legacy fixture pin.
func seedDuplicateContractProjection(t *testing.T, s *Store, workID, ownerRef string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	legacyApproval, marshalErr := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 2, "premise": "duplicate projection approval",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:duplicate", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
		"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		"routine-recovery-duplicate-approval-"+workID, WorkflowContractApproved, SubjectWorkItem, workID, ownerRef, "2026-09-22T00:00:00Z", 3, legacyApproval); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("duplicate fixture active contracts = %d, want 2", active)
	}
}

// duplicateRecoveryEventFields names the exact duplicate recovery: every
// active predecessor, one consecutive successor, reason, and audit evidence.
// No approval binding member appears — the payload an unsigned coordinator
// authors under the new policy.
func duplicateRecoveryEventFields(workID string, version int64) map[string]any {
	return map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"previous_contract_version": int64(1), "predecessor_contract_versions": []int64{1, 2}, "new_contract_version": int64(3),
		"supersede_reason": "repair the duplicate projection", "audit_evidence": []string{"evidence:duplicate-recovery"},
		"successor_contract": map[string]any{
			"contract_version": 3, "premise": "recovered premise", "outcome_predicates": []map[string]any{{
				"predicate_id": "predicate:return-route", "ordinal": 0, "outcome_kind": "check",
				"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:recovery", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
			}},
			"required_evidence": []string{"verification", "review", "artifact"}, "route_conventions": []string{}, "spec_mandate": []string{},
			"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
			"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		},
	}
}

func recoveryEventFieldsWith(fields map[string]any, key string, value any) map[string]any {
	clone := make(map[string]any, len(fields)+1)
	for name, member := range fields {
		clone[name] = member
	}
	clone[key] = value
	return clone
}

func setConcordProductKey(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE domain_registries SET product_key='concord' WHERE product_id='product';
		DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

func setContractSelfRepair(t *testing.T, s *Store, workID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_contracts SET self_repair_json='{"refusal_kind":"domain_overlap","blocked_operation":"workflow_action.dispatch_worker","evidence_refs":["obs:routine-repair"]}' WHERE work_id=? AND contract_version=1;
		DELETE FROM fold_guard`, workID); err != nil {
		t.Fatal(err)
	}
}

// recordRoutineSelfRepairActor records one complete actor tuple directly and
// returns its reference, so an invoker's recorded class is exactly the test's
// declaration.
func recordRoutineSelfRepairActor(t *testing.T, s *Store, actor WorkflowActor) string {
	t.Helper()
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, actorRef, actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef, string(actor.ActorClass), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		_ = leaveFold(context.Background(), tx)
		t.Fatal(err)
	}
	if err := leaveFold(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return actorRef
}

func TestRoutineRecovery(t *testing.T) {
	t.Parallel()

	t.Run("duplicate recovery authorization follows the policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-duplicate-policy"
		ctx := context.Background()
		s, owner, _ := seedItemAtAcceptance(t, workID, false)
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		seedDuplicateContractProjection(t, s, workID, ownerRef)
		version := readWorkVersion(t, s, workID)
		event := workflowEventWithActor("routine-recovery-gate-"+workID, WorkflowContractSuperseded, workID, ownerRef, duplicateRecoveryEventFields(workID, version))
		var payload workflowContractSupersededPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		// consequence_v1 supersedes routinely: the unsigned attributed event
		// passes the authorization branch the live fold delegates to.
		if err := requireWorkflowDuplicateRecoveryApprovalTx(ctx, tx, event, payload, routineRecoveryPolicyDefinition(workflowFixtureDefinition(t, 2))); err != nil {
			t.Fatalf("unsigned consequence_v1 duplicate recovery authorization refused: %v", err)
		}
		// The legacy pin keeps the recorded operator approval requirement on
		// the same event.
		err = requireWorkflowDuplicateRecoveryApprovalTx(ctx, tx, event, payload, workflowFixtureDefinition(t, 2).Definition)
		if err == nil || !strings.Contains(err.Error(), "operator approval") {
			t.Fatalf("legacy duplicate recovery authorization returned %v, want the operator approval refusal", err)
		}
	})

	t.Run("legacy duplicate recovery folds still require the recorded operator approval", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-duplicate-legacy"
		ctx := context.Background()
		s, owner, _ := seedItemAtAcceptance(t, workID, false)
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		seedDuplicateContractProjection(t, s, workID, ownerRef)
		version := readWorkVersion(t, s, workID)
		event := workflowEventWithActor("routine-recovery-duplicate-"+workID, WorkflowContractSuperseded, workID, ownerRef, duplicateRecoveryEventFields(workID, version))
		err = applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
		if err == nil || !strings.Contains(err.Error(), "operator approval") {
			t.Fatalf("legacy unsigned duplicate recovery returned %v, want the operator approval refusal", err)
		}
		if got := readWorkVersion(t, s, workID); got != version {
			t.Fatalf("refused legacy recovery changed the work version from %d to %d", version, got)
		}
	})

	t.Run("guard admits unsigned correction and refuses operator substitution under the new policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-guard-new"
		fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, workflowFixtureDefinition(t, 2), "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
		operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
		substituted := routineGuardContext(t, fixture.store, workID, "supersede_contract", &operator, true)
		substituted.admissionState = &WorkflowAdmissionState{ContractCorrectionAvailable: true}
		err := guardOperatorPremiseActor(substituted)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindUnauthorized || !strings.Contains(failure.Detail, "takes no operator substitution") {
			t.Fatalf("consequence_v1 correction with a supplied operator actor returned %v, want the no-substitution refusal", err)
		}
		// routineGuardContext parks on the single-connection pool while its
		// transaction is open, so release it before the next context.
		_ = substituted.tx.Rollback()
		unsigned := routineGuardContext(t, fixture.store, workID, "supersede_contract", nil, true)
		unsigned.admissionState = &WorkflowAdmissionState{ContractCorrectionAvailable: true}
		if err := guardOperatorPremiseActor(unsigned); err != nil {
			t.Fatalf("unsigned consequence_v1 contract correction refused: %v", err)
		}
	})

	t.Run("guard keeps the legacy correction consent", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-guard-legacy"
		fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, workflowFixtureDefinition(t, 2), "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
		unsigned := routineGuardContext(t, fixture.store, workID, "supersede_contract", nil, false)
		unsigned.admissionState = &WorkflowAdmissionState{ContractCorrectionAvailable: true}
		err := guardOperatorPremiseActor(unsigned)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindApprovalRequired || !strings.Contains(failure.Detail, "verified operator approval identity") {
			t.Fatalf("legacy unsigned correction returned %v, want the approval-identity refusal", err)
		}
		_ = unsigned.tx.Rollback()
		operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
		substituted := routineGuardContext(t, fixture.store, workID, "supersede_contract", &operator, false)
		substituted.admissionState = &WorkflowAdmissionState{ContractCorrectionAvailable: true}
		if err := guardOperatorPremiseActor(substituted); err != nil {
			t.Fatalf("legacy operator-superseded correction refused: %v", err)
		}
	})

	t.Run("the complete-step gate stays state-owned under both policies", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-complete-step-gate"
		ctx := context.Background()
		fixture := routineRecoveryAtCompleteStep(t, workID)
		s := fixture.store
		registered := mustBuiltinDefinition(t, "workflow.implementation")
		definition := registered.Definition
		policyDefinition := routineRecoveryPolicyDefinition(registered)
		// Without the contradiction record the route is closed for both
		// policies: the gate is state, never consent.
		for name, candidate := range map[string]WorkflowDefinition{"legacy": definition, "consequence_v1": policyDefinition} {
			available, err := workflowCompleteStepCorrectionAvailable(ctx, s.DatabaseForTesting(), workID, candidate, "release", "test", 0)
			if err != nil || available {
				t.Fatalf("%s complete-step gate before the contradiction record = %t, %v, want closed", name, available, err)
			}
		}
		recordCompleteStepContradictionObservation(t, s, workID, fixture.owner)
		for name, candidate := range map[string]WorkflowDefinition{"legacy": definition, "consequence_v1": policyDefinition} {
			available, err := workflowCompleteStepCorrectionAvailable(ctx, s.DatabaseForTesting(), workID, candidate, "release", "test", 0)
			if err != nil || !available {
				t.Fatalf("%s complete-step gate after the contradiction record = %t, %v, want open", name, available, err)
			}
		}
	})

	t.Run("the stale-law recovery wall demands the accepted successor law", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-stale-law"
		ctx := context.Background()
		fixture := routineRecoveryAtCompleteStep(t, workID)
		s := fixture.store
		// The shared stale-law fixture: contract 1 mandates spec:one, and the
		// Git-derived law projection supersedes it with accepted spec:two.
		// The route-level legacy execution of this exact shape stays covered
		// unchanged by TestCompleteStepCorrectionStaleLawStaysBehindTheGate;
		// this witness pins the wall owner both policies share.
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
			UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND contract_version=1;
			UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one';
			INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','.concord/docs/spec-two.md','Synthetic successor law','sha256:`+strings.Repeat("b", 64)+`','test');
			INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','supersedes','spec:one','test');
			INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid,product_wide_rationale) VALUES('project','workflow-law-locator','spec:two','product','root','sha256:`+strings.Repeat("b", 64)+`','test','');
			DELETE FROM fold_guard`, workID); err != nil {
			t.Fatal(err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = validateStaleWorkflowContractRecoverySuccessorTx(ctx, tx, workID, []int64{1}, nil)
		if err == nil || !strings.Contains(err.Error(), "must pin every accepted successor law revision") {
			t.Fatalf("stale-law recovery without the successor pin returned %v, want the wall refusal", err)
		}
		if err := validateStaleWorkflowContractRecoverySuccessorTx(ctx, tx, workID, []int64{1}, []WorkflowLawRevision{{LawID: "spec:two", ContentHash: "sha256:" + strings.Repeat("b", 64)}}); err != nil {
			t.Fatalf("stale-law recovery pinning the accepted successor law refused: %v", err)
		}
	})

	t.Run("shared fold walls hold for audit evidence and predecessor sets under both policies", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-walls-new"
		ctx := context.Background()
		s, owner, _ := seedItemAtAcceptance(t, workID, false)
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		seedDuplicateContractProjection(t, s, workID, ownerRef)
		version := readWorkVersion(t, s, workID)
		base := duplicateRecoveryEventFields(workID, version)
		// The audit-evidence wall keeps both bounded-list edges: the empty
		// set and the over-bound set refuse before any effect.
		for name, audit := range map[string][]string{"empty": {}, "oversized": make([]string, 33)} {
			if name == "oversized" {
				for index := range audit {
					audit[index] = "evidence:routine-recovery-" + workID
				}
			}
			event := workflowEventWithActor("routine-recovery-audit-"+name+"-"+workID, WorkflowContractSuperseded, workID, ownerRef, recoveryEventFieldsWith(base, "audit_evidence", audit))
			err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
			if err == nil || !strings.Contains(err.Error(), "invalid version, reason, or audit evidence") {
				t.Fatalf("%s audit evidence recovery returned %v, want the audit wall refusal", name, err)
			}
		}
		if got := readWorkVersion(t, s, workID); got != version {
			t.Fatalf("refused recoveries changed the work version from %d to %d", version, got)
		}
		// A recovery naming a predecessor set the active projection does not
		// carry refuses regardless of consent: the overlap wall is state, not
		// approval. One active contract cannot match the two-predecessor set.
		const singleID = "routine-recovery-walls-single"
		loneFixture := seedWorkflowReturnRouteFixtureWithDefinition(t, singleID, workflowFixtureDefinition(t, 2), "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
		lone := loneFixture.store
		loneRef, err := WorkflowActorRef(loneFixture.owner)
		if err != nil {
			t.Fatal(err)
		}
		loneVersion := readWorkVersion(t, lone, singleID)
		phantom := workflowEventWithActor("routine-recovery-phantom-"+singleID, WorkflowContractSuperseded, singleID, loneRef, duplicateRecoveryEventFields(singleID, loneVersion))
		err = applyWorkflowTestOperation(ctx, lone, Operation{Events: []Event{phantom}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, singleID): loneVersion}})
		if err == nil || !strings.Contains(err.Error(), "does not match active workflow contracts") {
			t.Fatalf("mismatched-predecessor recovery returned %v, want the overlap wall refusal", err)
		}
		if got := readWorkVersion(t, lone, singleID); got != loneVersion {
			t.Fatalf("refused recovery changed the work version from %d to %d", loneVersion, got)
		}
	})

	t.Run("correction preserves the failed attempt identity", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-retry-race"
		ctx := context.Background()
		f, attemptID, epoch := seedCurrentRepairWorker(t, workID)
		s := f.store
		// The attributed terminal derives the failure record in the same
		// commit (CD-0198 D5).
		lane := laneForID(t)
		if err := s2WorkerFailedLive(t, s, s2WorkerFailedAttributedEvent(workID, attemptID, "failed-"+workID, preferredModelForLane(lane), WorkerFailureWorkerError, "the repair attempt failed", time.Unix(40, 0).UTC())); err != nil {
			t.Fatalf("failed repair terminal: %v", err)
		}
		pin := issue1013Pin(t, s, workID)
		if pin.Correction == nil || pin.Correction.FailedAttemptID != attemptID || pin.Correction.FailedAttemptEpoch != epoch {
			t.Fatalf("correction = %+v, want the failed attempt %s at epoch %d", pin.Correction, attemptID, epoch)
		}
		var step string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
			t.Fatal(err)
		}
		stalePayload := json.RawMessage(`{"attempt_id":"` + attemptID + `","worker_packet":{}}`)
		err := validateFailedWorkerRetryIdentity(ctx, s.DatabaseForTesting(), workID, step, stalePayload)
		var failure *Failure
		if err == nil || !failureAs(err, &failure) || failure.Kind != KindStaleAttempt {
			t.Fatalf("retry reusing the failed identity returned %v, want the stale-attempt refusal", err)
		}
		freshPayload := json.RawMessage(`{"attempt_id":"attempt:routine-retry-fresh","worker_packet":{}}`)
		if err := validateFailedWorkerRetryIdentity(ctx, s.DatabaseForTesting(), workID, step, freshPayload); err != nil {
			t.Fatalf("fresh retry identity refused: %v", err)
		}
	})

	t.Run("self-repair follows the policy, the actor class, and loses the overlap exemption", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-self-repair-new"
		fixture := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, workflowFixtureDefinition(t, 2), "execution", []string{"verification"}, []string{"verification", "review", "artifact"})
		s := fixture.store
		setConcordProductKey(t, s)
		coordinator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/coordinator", SessionRef: "session/" + workID + "-coordinator", ActorClass: ActorAgent}
		coordinatorRef := recordRoutineSelfRepairActor(t, s, coordinator)
		// The seed already recorded the fixture operator's tuple.
		operatorRef, err := WorkflowActorRef(fixture.operator)
		if err != nil {
			t.Fatal(err)
		}
		unknownRef, err := WorkflowActorRef(WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/unrecorded", SessionRef: "session/" + workID + "-unrecorded", ActorClass: ActorAgent})
		if err != nil {
			t.Fatal(err)
		}
		workerRef := recordRoutineSelfRepairActor(t, s, WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/repair-worker", SessionRef: "session/" + workID + "-worker", ActorClass: ActorAgent})
		lane := BuiltinLaneDefinitions()[0]
		if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
			EventID: "routine-self-repair-dispatch-" + workID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID,
			Actor: workerRef, OccurredAt: time.Unix(45, 0).UTC(), PayloadVersion: 2,
			Payload: mustJSONValue(WorkerDispatchedPayload{AttemptID: "attempt:routine-self-repair-" + workID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, ReadbackModel: preferredModelForLane(lane), PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion, LaneActorRef: workerRef}),
		}}}); err != nil {
			t.Fatal(err)
		}
		policyDefinition := routineRecoveryPolicyDefinition(workflowFixtureDefinition(t, 2))
		legacyDefinition := workflowFixtureDefinition(t, 2).Definition
		classify := func(actorRef string, definition WorkflowDefinition) error {
			tx, txErr := s.DatabaseForTesting().BeginTx(context.Background(), nil)
			if txErr != nil {
				t.Fatal(txErr)
			}
			defer tx.Rollback()
			return validateWorkflowSelfRepairAuthorityForDefinitionTx(context.Background(), tx, workID, actorRef, definition)
		}
		// The recorded agent-class coordinator classifies its own contract.
		if err := classify(coordinatorRef, policyDefinition); err != nil {
			t.Fatalf("consequence_v1 coordinator self-repair classification refused: %v", err)
		}
		// An operator-class invoker is not a coordinator.
		err = classify(operatorRef, policyDefinition)
		var operatorFailure *Failure
		if err == nil || !failureAs(err, &operatorFailure) || operatorFailure.Kind != KindInvalidOperation || !strings.Contains(operatorFailure.Detail, "actor class is not authorized") {
			t.Fatalf("consequence_v1 self-repair classification by an operator-class actor returned %v, want the agent-class refusal", err)
		}
		// An unrecorded actor carries no authority at all.
		err = classify(unknownRef, policyDefinition)
		var unknownFailure *Failure
		if err == nil || !failureAs(err, &unknownFailure) || unknownFailure.Kind != KindInvalidOperation || !strings.Contains(unknownFailure.Detail, "not recorded") {
			t.Fatalf("consequence_v1 self-repair classification by an unrecorded actor returned %v, want the recorded-actor refusal", err)
		}
		// A worker's authority ends at its report.
		err = classify(workerRef, policyDefinition)
		var workerFailure *Failure
		if err == nil || !failureAs(err, &workerFailure) || workerFailure.Kind != KindUnauthorized || !strings.Contains(workerFailure.Detail, "worker actors cannot classify") {
			t.Fatalf("consequence_v1 self-repair classification by a worker returned %v, want the worker refusal", err)
		}
		// The legacy pin keeps its recorded operator requirement on the same
		// seeded store.
		if err := classify(coordinatorRef, legacyDefinition); err == nil || !strings.Contains(err.Error(), "operator-approved contract") {
			t.Fatalf("legacy self-repair classification by an agent returned %v, want the operator requirement refusal", err)
		}
		if err := classify(operatorRef, legacyDefinition); err != nil {
			t.Fatalf("legacy operator classification refused: %v", err)
		}
		// The overlap exemption follows the same policy answer. The legacy
		// classified contract resolves through the real exemption owner; the
		// consequence_v1 classification exempts nothing.
		setContractSelfRepair(t, s, workID)
		exempt, err := workflowSelfRepairExemptTx(context.Background(), s.DatabaseForTesting(), workID)
		if err != nil {
			t.Fatal(err)
		}
		if !exempt {
			t.Fatal("legacy self-repair classification lost the CD-0150 overlap exemption")
		}
		if workflowSelfRepairOverlapExempt(policyDefinition) {
			t.Fatal("consequence_v1 self-repair classification kept the CD-0150 overlap exemption")
		}
		if !workflowSelfRepairOverlapExempt(legacyDefinition) {
			t.Fatal("legacy self-repair classification lost the CD-0150 overlap exemption")
		}
	})

	t.Run("correction and supersession payload registrations keep their semantics", func(t *testing.T) {
		t.Parallel()
		superseded, ok := registeredEventKind(WorkflowContractSuperseded)
		if !ok || superseded.CurrentVersion != workflowContractSupersededLawChangePayloadVersion {
			t.Fatalf("contract_superseded registration = %#v, want v%d", superseded, workflowContractSupersededLawChangePayloadVersion)
		}
		for version := 1; version < superseded.CurrentVersion; version++ {
			if superseded.Upcasters[version] == nil {
				t.Fatalf("contract_superseded registration = %#v, want an upcaster from v%d", superseded, version)
			}
		}
		// A correction request records no dedicated event kind: it is a
		// workflow.action_completed whose action_id is request_correction.
		// Its registration is v7 with upcasters 1-6, and replay must keep a
		// stored pre-attribution correction completion's semantics: the
		// correction fields survive the upcast and the legacy attribution
		// marker is stamped explicitly.
		completed, ok := registeredEventKind(WorkflowActionCompleted)
		if !ok || completed.CurrentVersion != 7 || completed.Upcasters[1] == nil || completed.Upcasters[4] == nil || completed.Upcasters[5] == nil || completed.Upcasters[6] == nil {
			t.Fatalf("action_completed registration = %#v, want v7 with upcasters 1-6", completed)
		}
		correction := map[string]any{
			"work_id": "routine-upcast-work", "expected_version": 2, "resulting_version": 3,
			"step_id": "verify", "action_id": "request_correction", "attempt_epoch": 1,
			"correction_diagnosis": "the delivered subject is stale", "correction_strategy": "re-produce the artifact",
			"correction_predicate_ids": []string{"predicate:return-route"}, "correction_evidence_refs": []string{"evidence:stale"},
		}
		event := workflowEventWithActor("routine-recovery-correction-upcast", WorkflowActionCompleted, "routine-upcast-work", "actor:test", correction)
		event.PayloadVersion = 4
		upcast, err := upcastEvent(event)
		if err != nil {
			t.Fatalf("recorded v4 correction completion did not upcast: %v", err)
		}
		if upcast.PayloadVersion != completed.CurrentVersion || upcast.replaySourcePayloadVersion != 4 {
			t.Fatalf("correction upcast payload version = %d source = %d, want %d with source 4", upcast.PayloadVersion, upcast.replaySourcePayloadVersion, completed.CurrentVersion)
		}
		var payload workflowActionCompletedPayload
		if err := json.Unmarshal(upcast.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ActionID != "request_correction" || payload.CorrectionDiagnosis != "the delivered subject is stale" || payload.CorrectionStrategy != "re-produce the artifact" || len(payload.CorrectionPredicateIDs) != 1 || payload.CorrectionEvidenceRefs[0] != "evidence:stale" {
			t.Fatalf("correction upcast lost historical correction semantics: %#v", payload)
		}
		if payload.FailureAttribution == nil {
			t.Fatal("correction upcast did not stamp the explicit legacy attribution marker")
		}
		// The supersession registration keeps the same guarantee for its
		// approval binding bytes: a recorded v1 supersession upcasts to the
		// current version with its attribution preserved and its replay
		// provenance intact.
		binding := map[string]any{
			"work_id": "routine-upcast-work", "expected_version": 2, "resulting_version": 3,
			"previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "legacy replacement", "audit_evidence": []string{"evidence:legacy"},
			"approval_ref":              strings.Repeat("a", 64),
			"approval_operation_digest": "sha256:" + strings.Repeat("c", 64),
			"approval_scope_json":       `{}`,
			"approval_versions_json":    `{"work_id":"routine-upcast-work"}`,
			"approval_consequence":      "recovery",
		}
		supersessionEvent := workflowEventWithActor("routine-recovery-upcast", WorkflowContractSuperseded, "routine-upcast-work", "actor:test", binding)
		supersededUpcast, err := upcastEvent(supersessionEvent)
		if err != nil {
			t.Fatalf("recorded v1 supersession did not upcast: %v", err)
		}
		if supersededUpcast.PayloadVersion != workflowContractSupersededLawChangePayloadVersion || supersededUpcast.replaySourcePayloadVersion != 1 {
			t.Fatalf("upcast payload version = %d source = %d, want %d with source 1", supersededUpcast.PayloadVersion, supersededUpcast.replaySourcePayloadVersion, workflowContractSupersededLawChangePayloadVersion)
		}
		var supersessionPayload workflowContractSupersededPayload
		if err := json.Unmarshal(supersededUpcast.Payload, &supersessionPayload); err != nil {
			t.Fatal(err)
		}
		if supersessionPayload.ApprovalRef != strings.Repeat("a", 64) || supersessionPayload.ApprovalConsequence != "recovery" || supersessionPayload.SupersedeReason != "legacy replacement" {
			t.Fatalf("upcast payload lost historical approval or attribution bytes: %#v", supersessionPayload)
		}
	})

	t.Run("legacy duplicate recovery binding stays exact", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-binding-exact"
		ctx := context.Background()
		s, owner, _ := seedItemAtAcceptance(t, workID, false)
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		seedDuplicateContractProjection(t, s, workID, ownerRef)
		version := readWorkVersion(t, s, workID)
		fields := duplicateRecoveryEventFields(workID, version)
		fields["approval_ref"] = strings.Repeat("b", 64)
		fields["approval_operation_digest"] = "sha256:" + strings.Repeat("c", 64)
		fields["approval_scope_json"] = `{}`
		fields["approval_versions_json"] = `{"work_id":"` + workID + `"}`
		fields["approval_consequence"] = "recovery"
		event := workflowEventWithActor("routine-recovery-unbound-"+workID, WorkflowContractSuperseded, workID, ownerRef, fields)
		err = applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
		if err == nil || !strings.Contains(err.Error(), "operator approval") {
			t.Fatalf("unbound legacy recovery returned %v, want the operator approval refusal", err)
		}
		if got := readWorkVersion(t, s, workID); got != version {
			t.Fatalf("refused recovery changed the work version from %d to %d", version, got)
		}
	})
}

// routineRecoveryAtCompleteStep parks a current implementation item on its
// release step: a reviewed candidate with associated current proof and Main's
// premise confirmation, the state the complete-step recovery routes read.
func routineRecoveryAtCompleteStep(t *testing.T, workID string) acceptanceRecoveryFixture {
	t.Helper()
	ctx := context.Background()
	fixture := qCurrentAcceptance(t, workID)
	if err := fixture.run(ctx, "confirm_premise", "confirm", fixture.version(ctx, t), nil, nil); err != nil {
		t.Fatalf("confirm the premise: %v", err)
	}
	if step := currentStep(t, fixture.store, workID); step != "release" {
		t.Fatalf("step after confirmation = %q, want release", step)
	}
	return fixture
}
