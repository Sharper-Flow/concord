package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The recovery consent owner keeps every recovery wall and classifies only
// the consent bit. Under consequence_v1 a supersession or correction request
// is routine record-keeping: the authenticated coordinator acts unsigned, the
// duplicate-recovery and complete-step routes consent without a consumed
// operator approval. Every retained gate holds unchanged: stale-law and complete-step
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

// seedDuplicateContractProjection creates the representable duplicate-active
// state that the current refusal and recovery owners must handle.
func seedDuplicateContractProjection(t *testing.T, s *Store, workID, ownerRef string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,definition_ref,definition_digest)
SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,?,spec_mandate,law_modifies,law_boundary_version,rigor_class,definition_ref,definition_digest FROM workflow_contracts WHERE work_id=? AND contract_version=1;
INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
SELECT work_id,2,predicate_id,ordinal,outcome_kind,outcome_payload FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1;
DELETE FROM write_guard`, ownerRef, workID, workID); err != nil {
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

func TestRoutineRecovery(t *testing.T) {
	t.Parallel()

	t.Run("duplicate recovery authorization follows the policy", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-duplicate-policy"
		ctx := context.Background()
		s, owner, _ := seedItemAtAcceptance(t, workID)
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
		s, owner, _ := seedItemAtAcceptance(t, workID)
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

	t.Run("the clone recovery wall refuses a deleted mandated law", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-stale-law"
		ctx := context.Background()
		fixture := routineRecoveryAtCompleteStep(t, workID)
		s := fixture.store
		attachWorkflowLawPin(t, s, workID, "sha256:"+strings.Repeat("a", 64))
		// A clone keeps the deleted mandate; complete successors may drop it
		// or bind any current accepted law (CD-0036 D3).
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
			UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND contract_version=1;
			DELETE FROM law_domain_homes WHERE law_id='spec:one';
			DELETE FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one';
			DELETE FROM write_guard`, workID); err != nil {
			t.Fatal(err)
		}
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		err = validateClonedWorkflowContractLawRevisionsTx(ctx, tx, workID, []int64{1})
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "retain a deleted mandated law") {
			t.Fatalf("clone recovery retaining deleted law returned %v, want the wall refusal", err)
		}
	})

	t.Run("shared fold walls hold for audit evidence and predecessor sets under both policies", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-walls-new"
		ctx := context.Background()
		s, owner, _ := seedItemAtAcceptance(t, workID)
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

	t.Run("correction and supersession payload registrations keep their semantics", func(t *testing.T) {
		t.Parallel()
		superseded, ok := registeredEventKind(WorkflowContractSuperseded)
		if !ok || superseded.CurrentVersion != workflowContractSupersededLawChangePayloadVersion {
			t.Fatalf("contract_superseded registration = %#v, want v%d", superseded, workflowContractSupersededLawChangePayloadVersion)
		}
		// A correction request records no dedicated event kind: it is a
		// workflow.action_completed whose action_id is request_correction.
		completed, ok := registeredEventKind(WorkflowActionCompleted)
		if !ok || completed.CurrentVersion != 8 {
			t.Fatalf("action_completed registration = %#v, want v8", completed)
		}
	})

	t.Run("legacy duplicate recovery binding stays exact", func(t *testing.T) {
		t.Parallel()
		const workID = "routine-recovery-binding-exact"
		ctx := context.Background()
		s, owner, _ := seedItemAtAcceptance(t, workID)
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
