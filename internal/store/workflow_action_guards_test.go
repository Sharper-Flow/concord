package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func seedMandateRecoveryItem(t *testing.T, workID string) *Store {
	t.Helper()
	s, _, _ := seedItemAtAcceptance(t, workID)
	owner := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/owner", "session/"+workID)
	if err := advanceWorkflowTestInstanceToStep(t.Context(), s, workID, "release", owner); err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	return s
}

func recoveryBindPayload() json.RawMessage {
	return json.RawMessage(`{"evidence_kind":"artifact","evidence_ref":"spec:one"}`)
}

// Fixture assertions separate declared requirements from predicate recovery.
func outstandingWorkflowEvidenceRequirementsForWork(ctx context.Context, q queryer, workID string, definition WorkflowDefinition) ([]workflowEvidenceRequirement, error) {
	required, mandates, obligations, cutoff, err := workflowEvidenceRequirementInputs(ctx, q, workID)
	if err != nil {
		if workflowMissingContractProjection(err) {
			return nil, nil
		}
		return nil, err
	}
	return outstandingWorkflowEvidenceRequirements(ctx, q, workID, required, mandates, definition, obligations, cutoff)
}

func TestCorrectedContractExternalCheckRecoveryAtAcceptance(t *testing.T) {
	const workID = "corrected-contract-external-check"
	subject := "ci:required-checks:corrected-subject"
	f, repo, refs := pReceiptFixture(t, workID, 1, 1, func(f workContextFixture) {
		s := f.store
		// Fold the successor to isolate evidence recovery from correction routing.
		version := readWorkVersion(t, s, workID)
		actor, _ := WorkflowActorRef(f.owner)
		contract, err := readWorkflowContractVersion(t.Context(), s.db, workID, 1)
		if err != nil {
			t.Fatal(err)
		}
		successor := map[string]any{
			"premise": contract.Premise, "rigor_class": contract.RigorClass,
			"required_evidence": contract.RequiredEvidence, "route_conventions": contract.RouteConventions,
			"spec_mandate": contract.SpecMandate, "law_modifies": contract.LawModifies,
		}
		var lawBoundaryVersion int
		var consequenceClass string
		if err := s.db.QueryRow(`SELECT law_boundary_version,consequence_class FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&lawBoundaryVersion, &consequenceClass); err != nil {
			t.Fatal(err)
		}
		successor["law_boundary_version"] = lawBoundaryVersion
		successor["consequence_class"] = consequenceClass
		if contract.DecisionDomains != nil {
			successor["decision_domains"] = contract.DecisionDomains
		}
		if contract.ArchitectureBinding != nil {
			successor["architecture_binding"] = contract.ArchitectureBinding
		}
		successor["contract_version"] = 2
		successor["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:p0", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:github/required-checks", "immutable_subject_ref": subject, "expected_result": "pass"}}}
		event := workflowEventWithActor(workID+":successor", WorkflowContractSuperseded, workID, actor, map[string]any{"work_id": workID, "expected_version": version, "resulting_version": version + 1, "previous_contract_version": 1, "new_contract_version": 2, "supersede_reason": "Correct the checked subject.", "audit_evidence": []string{"evidence:corrected-check"}, "successor_contract": successor})
		if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
			t.Fatal(err)
		}
	})
	s := f.store
	payload := pReceiptBatch(refs, 0)
	entry, err := VerifyWorkflowInstanceDefinition(t.Context(), s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	outstanding, err := outstandingWorkflowEvidenceRequirementsForWork(t.Context(), s.db, workID, entry.Definition)
	if err != nil || len(outstanding) != 0 {
		t.Fatalf("required evidence before external check = %v: %v", outstanding, err)
	}
	pin, err := ReadWorkPin(t.Context(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatalf("acceptance omits recovery for unproved check: %v", pin.NextValidIntents)
	}
	assertRecoveryRefusalWithoutEffect(t, s, workID, KindIllegalLifecycleTransition, func() error {
		return runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "ci:unrelated"}), 0, f.owner)
	})
	seedWorkflowAuthority(t, s, "corrected-check-source", workID, "principal/check", "request/check", []string{subject})
	bind := mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": subject, "producer_id": "principal/check", "producer_run_ref": "corrected-check-source", "producer_watermark": "request/check"})
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", bind, 0, f.owner); err != nil {
		t.Fatal(err)
	}
	if step := currentStep(t, s, workID); step != "acceptance" {
		t.Fatalf("binding advanced to %s", step)
	}
	assertRecoveryRefusalWithoutEffect(t, s, workID, KindIllegalLifecycleTransition, func() error { return runVerdictActionAs(t, s, workID, "bind_evidence", bind, 0, f.owner) })
	assertRecoveryRefusalWithoutEffect(t, s, workID, KindMissingEvidence, func() error {
		return runVerdictActionAs(t, s, workID, "record_verdict", criterionAssociationPayload(subject, "predicate:p0"), 0, f.owner)
	})
	if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, f.owner); err != nil {
		t.Fatal(err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), s.db, workID, 2)
	if err != nil || len(verdicts) != 1 || verdicts[0].ContractVersion != 2 || verdicts[0].CriterionDigest == "" {
		t.Fatalf("successor proof=%+v err=%v", verdicts, err)
	}
	pin, err = ReadWorkPin(t.Context(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatal("acceptance advertises recovery after proof and required kinds are satisfied")
	}
	seedWork(t, s, workID+"-compared")
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("9", 16), []string{"root", workID + "-compared"})
	// The confirmation's investigation-Domain fold reads the verified
	// committed registry inside its transaction, so the proof is established
	// before the action transaction opens.
	if err := (acceptanceRecoveryFixture{store: s, workID: workID, owner: f.owner, operator: f.operator}).run(t.Context(), "confirm_premise", "confirm", readWorkVersion(t, s, workID), nil, nil); err != nil {
		t.Fatalf("confirm successor: %v", err)
	}
	bindCompletionClosure(t, s, workID, f.owner)
	j := &candidateJourney{fixture: f, s: s, workID: workID, repo: repo}
	if err := j.terminalAction(t, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), f.owner); err != nil {
		t.Fatalf("complete successor: %v", err)
	}
}

func assertRecoveryRefusalWithoutEffect(t *testing.T, s *Store, workID string, kind FailureKind, action func() error) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var failure *Failure
	err := action()
	if !failureAs(err, &failure) || failure.Kind != kind {
		t.Fatalf("recovery refusal = %v, want %s", err, kind)
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if got := readWorkVersion(t, s, workID); got != version || after != before {
		t.Fatalf("refused recovery changed version %d -> %d or event count %d -> %d", version, got, before, after)
	}
}

func TestExternalCheckRecoveryPreservesEvidenceRefusals(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
		kind   FailureKind
	}{
		{"review-kind", func(p map[string]any) { p["evidence_kind"] = "review" }, KindIllegalLifecycleTransition},
		{"artifact-kind", func(p map[string]any) { p["evidence_kind"] = "artifact" }, KindIllegalLifecycleTransition},
		{"unrelated-subject", func(p map[string]any) { p["immutable_subject_ref"] = "ci:unrelated" }, KindIllegalLifecycleTransition},
		{"unrecorded-source", func(p map[string]any) { p["producer_run_ref"] = "operation:missing" }, KindInvariantViolation},
		{"wrong-producer", func(p map[string]any) { p["producer_id"] = "principal/other" }, KindInvariantViolation},
		{"wrong-watermark", func(p map[string]any) { p["producer_watermark"] = "request/other" }, KindInvariantViolation},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			workID := "external-check-refusal-" + test.name
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
			s := fixture.store
			subject := "commit:" + workID
			authority := "external-check-source-" + test.name
			seedWorkflowAuthority(t, s, authority, workID, "principal/check", "request/check", []string{subject})
			payload := map[string]any{"evidence_kind": "verification", "immutable_subject_ref": subject, "producer_id": "principal/check", "producer_run_ref": authority, "producer_watermark": "request/check"}
			test.mutate(payload)
			assertRecoveryRefusalWithoutEffect(t, s, workID, test.kind, func() error {
				return runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(payload), 0, fixture.owner)
			})
		})
	}
	t.Run("stale-version", func(t *testing.T) {
		t.Parallel()
		const workID = "external-check-stale-version"
		fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
		assertRecoveryRefusalWithoutEffect(t, fixture.store, workID, KindVersionConflict, func() error {
			return runVerdictActionAs(t, fixture.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "commit:" + workID}), readWorkVersion(t, fixture.store, workID)-1, fixture.owner)
		})
	})
}

func TestExternalCheckRecoveryClosedForCurrentVerdicts(t *testing.T) {
	for _, associated := range []bool{false, true} {
		t.Run(fmt.Sprint(associated), func(t *testing.T) {
			t.Parallel()
			workID := fmt.Sprintf("external-check-closed-%t", associated)
			f, _, refs := pReceiptFixture(t, workID, 1, 1)
			if associated {
				if err := runVerdictActionAs(t, f.store, workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
					t.Fatal(err)
				}
			}
			pin, err := ReadWorkPin(t.Context(), f.store, workID)
			if err != nil {
				t.Fatal(err)
			}
			if workPinContainsAction(pin.NextValidIntents, "bind_evidence") == associated {
				t.Fatalf("binding recovery intent disagrees with current criterion proof: associated=%t intents=%v", associated, pin.NextValidIntents)
			}
			// Main's proof association never reopens an already bound tuple.
			assertRecoveryRefusalWithoutEffect(t, f.store, workID, KindIllegalLifecycleTransition, func() error {
				return runVerdictActionAs(t, f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": refs[0][0]}), 0, f.owner)
			})
		})
	}
}

func TestExternalCheckRecoveryCompletedEventUsesSharedEligibility(t *testing.T) {
	for _, kind := range []string{"verification", "review"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			workID := "external-check-event-" + kind
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
			s := fixture.store
			subject := "commit:" + workID
			authority := "external-check-event-source-" + kind
			seedWorkflowAuthority(t, s, authority, workID, "principal/check", "request/check", []string{subject})
			actorRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			version := readWorkVersion(t, s, workID)
			binding := workflowEventWithActor(workID+":binding", WorkflowEvidenceBound, workID, actorRef, map[string]any{
				"work_id": workID, "expected_version": version, "resulting_version": version + 1,
				"evidence_kind": kind, "immutable_subject_ref": subject, "producer_id": "principal/check",
				"producer_run_ref": authority, "producer_watermark": "request/check", "observed_at": "2026-10-13T00:00:00Z",
			})
			completed := workflowEventWithActor(workID+":completed", WorkflowActionCompleted, workID, actorRef, map[string]any{
				"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2,
				"step_id": "acceptance", "action_id": "bind_evidence", "attempt_epoch": 1, "actor_ref": actorRef,
				"result_evidence_refs": []string{subject}, "changed_refs": []string{workID},
			})
			apply := func() error {
				return applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{binding, completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
			}
			if kind != "verification" {
				assertRecoveryRefusalWithoutEffect(t, s, workID, KindMissingEvidence, apply)
			} else if err := apply(); err != nil {
				t.Fatalf("fold eligible external-check binding without a caller guard: %v", err)
			}
		})
	}
}

func TestRecoveryRequiredKindStillNeedsDurableProducerAuthority(t *testing.T) {
	const workID = "recovery-kind-authority"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
	if _, err := fixture.store.DatabaseForTesting().Exec(`DELETE FROM durable_operations WHERE op_id=?`, "return-authority-verification-"+workID); err != nil {
		t.Fatal(err)
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, fixture.store, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		t.Fatal(err)
	}
	outstanding, err := outstandingWorkflowEvidenceRequirementsForWork(ctx, fixture.store.db, workID, entry.Definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range outstanding {
		if requirement.Kind == "verification" && requirement.Reference == "" {
			return
		}
	}
	t.Fatalf("verification kind satisfied without its durable producer authority; outstanding = %v", outstanding)
}

func TestRecoveryPredicateRefusalStaysLocalToBinding(t *testing.T) {
	for _, test := range []struct{ name, kind, payload string }{
		{"malformed", "check", `{"kind":"check"}`},
		{"mismatched", "exists", `{"kind":"check","check_ref":"check:ci","immutable_subject_ref":"ci:result","expected_result":"pass"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			workID := "recovery-predicate-refusal-" + test.name
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
			s := fixture.store
			entry, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
			if err != nil {
				t.Fatal(err)
			}
			_, _, before := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
			if before != nil {
				t.Fatalf("discover correction before corrupting the predicate: %v", before)
			}
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contract_predicates SET outcome_kind=?,outcome_payload=? WHERE work_id=? AND contract_version=1; DELETE FROM write_guard`, test.kind, test.payload, workID); err != nil {
				t.Fatal(err)
			}
			state, err := loadWorkflowAdmissionStateTxForRegistry(ctx, s.db, workID, entry.Definition, "acceptance", "test", BuiltinWorkflowRegistry())
			if state.EvidenceRecoveryRoute || err != nil {
				t.Errorf("binding availability with a %s predicate = (%t, %v), want a hidden route without an action-independent failure", test.name, state.EvidenceRecoveryRoute, err)
			}
			if _, _, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract"); err != nil {
				t.Errorf("binding predicate validation blocked another action's discovery: %v", err)
			}
			payload := json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"ci:result"}`)
			var failure *Failure
			if _, err := guardRecoveryEvidenceBind(ctx, s.db, workID, entry.Definition, "acceptance", payload, "test"); !failureAs(err, &failure) || failure.Kind != KindInvariantViolation || failure.Detail != "approved outcome predicate is malformed or mismatched" {
				t.Errorf("strict binding guard with a %s predicate = %v, want the predicate refusal", test.name, err)
			}
			assertRecoveryRefusalWithoutEffect(t, s, workID, KindInvariantViolation, func() error {
				return runVerdictActionAs(t, s, workID, "bind_evidence", payload, 0, fixture.owner)
			})
		})
	}
}

func TestBindEvidenceAdmittedPastBindingStepWhileMandateUnbound(t *testing.T) {
	const workID = "mandate-recovery-admit"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload()); err != nil {
		t.Fatalf("late mandate binding refused: %v", err)
	}
}

func TestBindEvidenceRefusedPastBindingStepOnceMandateBound(t *testing.T) {
	const workID = "mandate-recovery-narrow"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload()); err != nil {
		t.Fatalf("initial mandate binding refused: %v", err)
	}
	err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload())
	if err == nil {
		t.Fatal("late binding passed after the mandate was bound")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("late binding error = %v, want %s", err, KindIllegalLifecycleTransition)
	}
}

func TestRecordVerdictPassesAfterRecoveryBind(t *testing.T) {
	const workID = "mandate-recovery-verdict"
	f, _, refs := pReceiptFixture(t, workID, 1, 1)
	s := f.store
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND contract_version=1`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload()); err != nil {
		t.Fatalf("mandate recovery binding refused: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
		t.Fatalf("verdict after mandate recovery binding refused: %v", err)
	}
}

// A declared verification obligation stays recoverable after the same law's
// mandate reference is bound under another kind (#905): the obligation gate
// reads the exact (evidence kind, law reference) tuple, so an artifact
// mandate bind must not lock a verification or review obligation out of the
// late binding path.
func TestBindEvidenceObligationRecoverableAfterMandateBound(t *testing.T) {
	const workID = "obligation-recovery-admit"
	s := seedMandateRecoveryItem(t, workID)
	db := s.DatabaseForTesting()
	var contractVersion int64
	if err := db.QueryRow(`SELECT MAX(contract_version) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	seedTx, seedErr := db.BeginTx(context.Background(), nil)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	if _, err := seedTx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES(?,?, 'product','root',?)`, workID, contractVersion, "sha256:"+strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_contract_verification_obligations(work_id,contract_version,law_id,obligation_id) VALUES(?,?,'spec:one','verification')`, workID, contractVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload()); err != nil {
		t.Fatalf("mandate binding refused: %v", err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","evidence_ref":"spec:one"}`)); err != nil {
		t.Fatalf("obligation recovery binding refused after the mandate was bound: %v", err)
	}
	// Once the obligation tuple is bound, the same bind refuses: recovery
	// does not reopen evidence binding without limit.
	err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","evidence_ref":"spec:one"}`))
	if err == nil {
		t.Fatal("obligation recovery admitted a second identical bind")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("repeat obligation bind error = %v, want %s", err, KindIllegalLifecycleTransition)
	}
}

// An obligation tuple that is already satisfied does not reopen recovery:
// the guard admits only what the completion gate still demands.
func TestBindEvidenceObligationRefusedOnceSatisfied(t *testing.T) {
	const workID = "obligation-recovery-satisfied"
	s := seedMandateRecoveryItem(t, workID)
	db := s.DatabaseForTesting()
	var contractVersion int64
	if err := db.QueryRow(`SELECT MAX(contract_version) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	seedTx, seedErr := db.BeginTx(context.Background(), nil)
	if seedErr != nil {
		t.Fatal(seedErr)
	}
	if _, err := seedTx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES(?,?,'product','root',?)`, workID, contractVersion, "sha256:"+strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_contract_verification_obligations(work_id,contract_version,law_id,obligation_id) VALUES(?,?,'spec:one','review')`, workID, contractVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES(?,1,'ob-satisfied-ev','operator',?)`, workID, `{"evidence_kind":"review","immutable_subject_ref":"spec:one"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := seedTx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"review","evidence_ref":"spec:one"}`))
	if err == nil {
		t.Fatal("satisfied obligation reopened recovery")
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindIllegalLifecycleTransition {
		t.Fatalf("satisfied obligation bind error = %v, want %s", err, KindIllegalLifecycleTransition)
	}
}

func TestBindEvidenceRequiredKindRecoveryWithEmptyMandate(t *testing.T) {
	const workID = "required-kind-recovery-empty-mandate"
	s, _, _ := seedItemAtAcceptance(t, workID)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_contracts SET required_evidence='["verification","review","commit"]',spec_mandate='[]' WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	var beforeStep, beforeContract string
	if err := db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&beforeStep); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&beforeContract); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"review", "commit"} {
		payload := json.RawMessage(`{"evidence_kind":"` + kind + `","evidence_ref":"evidence:` + kind + `"}`)
		if err := runVerdictAction(t, s, workID, "bind_evidence", payload); err != nil {
			t.Fatalf("required %s recovery binding refused: %v", kind, err)
		}
	}
	var afterStep, afterContract string
	if err := db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&afterStep); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&afterContract); err != nil {
		t.Fatal(err)
	}
	if afterStep != beforeStep || afterContract != beforeContract {
		t.Fatalf("recovery changed workflow authority: step %q/%q contract %q/%q", beforeStep, afterStep, beforeContract, afterContract)
	}
	for _, kind := range []string{"review", "commit"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=? AND json_extract(payload,'$.evidence_kind')=?`, workID, kind).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("recovered %s binding count=%d, want 1", kind, count)
		}
	}
}

func TestBindEvidenceRequiredKindRecoveryAfterMandateBound(t *testing.T) {
	const workID = "required-kind-recovery-after-mandate"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload()); err != nil {
		t.Fatalf("mandate binding refused: %v", err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_contracts SET required_evidence='["verification","review"]' WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"review","evidence_ref":"evidence:review-after-mandate"}`)); err != nil {
		t.Fatalf("required-kind recovery after mandate binding refused: %v", err)
	}
}
