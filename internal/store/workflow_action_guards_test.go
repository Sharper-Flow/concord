package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func seedMandateRecoveryItem(t *testing.T, workID string) *Store {
	t.Helper()
	s, _, _ := seedItemAtAcceptance(t, workID)
	moveWorkflowPlanFixtureFrontier(t, s, workID, "review")
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

func TestDeclaredStepBindsAllEvidenceKinds(t *testing.T) {
	for _, kind := range workflowEvidenceKinds {
		t.Run(string(kind), func(t *testing.T) {
			workID := "declared-bind-" + string(kind)
			fixture := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, "investigate", []string{"artifact"}, nil)
			ref := "evidence:" + string(kind)
			if kind == EvidenceNativeRun {
				ctx := t.Context()
				event, err := buildNativeRunEvent("native:"+workID, workID, fixture.owner, time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), readWorkVersion(t, fixture.store, workID), "start", "run-declared", "routing://synthetic", "started", "https://evidence.invalid/run", "sha256:"+repeatHex("a"), "2026-08-20T12:00:00Z")
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.store.Transact(ctx, func(tx *Transaction) error {
					_, err := applyWorkflowOperationTx(ctx, tx.tx, Operation{Events: []Event{event}}, newWriteScope(tx.tx))
					return err
				}); err != nil {
					t.Fatal(err)
				}
				ref = readNativeRuns(t, fixture.store, workID)[0].ObservationID
				if err := fixture.store.Transact(ctx, func(tx *Transaction) error {
					return appendExternalObservationVerificationTx(ctx, tx, workID, fixture.owner.PrincipalRef, time.Date(2026, 8, 20, 12, 2, 0, 0, time.UTC), ExternalObservationVerification{ObservationID: ref, VerificationMethod: VerifyTrustedClientReport, VerifiedAt: "2026-08-20T12:02:00Z", VerifyingAuthorityRef: "client:verifier", Result: VerificationMatched})
				}); err != nil {
					t.Fatal(err)
				}
			}
			payload := mustJSONValue(map[string]any{"evidence_kind": string(kind), "immutable_subject_ref": ref})
			if err := issue1013Preflight(t, fixture.store, workID, "bind_evidence", payload, fixture.owner); err != nil {
				t.Errorf("declared binding preflight: %v", err)
			}
			if err := runVerdictActionAs(t, fixture.store, workID, "bind_evidence", payload, 0, fixture.owner); err != nil {
				t.Errorf("declared binding: %v", err)
			}
		})
	}
}

func TestLateBindingRefusesUndeclaredEvidenceKind(t *testing.T) {
	const workID = "late-bind-undeclared"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "review")
	payload := json.RawMessage(`{"evidence_kind":"undeclared","immutable_subject_ref":"evidence:unrelated"}`)
	assertRecoveryRefusalWithoutEffect(t, f.store, workID, KindInvalidPayload, func() error {
		return issue1013Preflight(t, f.store, workID, "bind_evidence", payload, f.owner)
	})
	assertRecoveryRefusalWithoutEffect(t, f.store, workID, KindInvalidPayload, func() error {
		return runVerdictActionAs(t, f.store, workID, "bind_evidence", payload, 0, f.owner)
	})
	declared := json.RawMessage(`{"evidence_kind":"approval","immutable_subject_ref":"evidence:unrelated"}`)
	if err := issue1013Preflight(t, f.store, workID, "bind_evidence", declared, f.owner); err != nil {
		t.Fatalf("declared kind preflight: %v", err)
	}
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", declared, 0, f.owner); err != nil {
		t.Fatalf("declared kind binding: %v", err)
	}
	if step := currentStep(t, f.store, workID); step != "review" {
		t.Fatalf("binding advanced frontier to %q", step)
	}
}

// Fixture assertions separate declared requirements from predicate recovery.
func outstandingWorkflowEvidenceRequirementsForWork(ctx context.Context, q queryer, workID string) ([]workflowEvidenceRequirement, error) {
	required, mandates, obligations, err := workflowEvidenceRequirementInputs(ctx, q, workID)
	if err != nil {
		if workflowProjectionInvariant(err, "approved workflow contract is missing") {
			return nil, nil
		}
		return nil, err
	}
	return outstandingWorkflowEvidenceRequirements(ctx, q, workID, required, mandates, obligations)
}

func TestCorrectedContractExternalCheckBindingAtAcceptance(t *testing.T) {
	const workID = "corrected-contract-external-check"
	subject := "ci:required-checks:corrected-subject"
	f, repo, refs := pReceiptFixture(t, workID, 1, 1, func(f workContextFixture) {
		s := f.store
		successor := pContractSuccessor(t, s, workID, "Correct the checked subject.")
		successor["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:p0", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:github/required-checks", "immutable_subject_ref": subject, "expected_result": "pass"}}}
		if err := reviseContractRecord(t, s, workID, f.owner, successor, readWorkVersion(t, s, workID)); err != nil {
			t.Fatal(err)
		}
	})
	s := f.store
	payload := pReceiptBatch(refs, 0)
	outstanding, err := outstandingWorkflowEvidenceRequirementsForWork(t.Context(), s.db, workID)
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
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "ci:unrelated"}), 0, f.owner); err != nil {
		t.Fatalf("bind verification evidence: %v", err)
	}
	assertRecoveryRefusalWithoutEffect(t, s, workID, KindMissingEvidence, func() error {
		return runVerdictActionAs(t, s, workID, "record_verdict", criterionAssociationPayload("ci:unrelated", "predicate:p0"), 0, f.owner)
	})
	seedWorkflowAuthority(t, s, "corrected-check-source", workID, "principal/check", "request/check", []string{subject})
	bind := mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": subject, "producer_id": "principal/check", "producer_run_ref": "corrected-check-source", "producer_watermark": "request/check"})
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", bind, 0, f.owner); err != nil {
		t.Fatal(err)
	}
	if step := workflowPlanFixtureFrontier(t, s, workID); step != "review" {
		t.Fatalf("binding advanced to %s", step)
	}
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", bind, 0, f.owner); err != nil {
		t.Fatalf("repeat binding with current version: %v", err)
	}
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
	if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatal("acceptance omitted ordinary binding after proof and required kinds were satisfied")
	}
	if err := pConfirmReceiptPremise(t, f); err != nil {
		t.Fatalf("confirm successor: %v", err)
	}
	completeWorkflowPlanFixture(t, s, workID)
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
		{"review-kind", func(p map[string]any) { p["evidence_kind"] = "review" }, ""},
		{"artifact-kind", func(p map[string]any) { p["evidence_kind"] = "artifact" }, ""},
		{"unrelated-subject", func(p map[string]any) { p["immutable_subject_ref"] = "ci:unrelated" }, KindInvariantViolation},
		{"unrecorded-source", func(p map[string]any) { p["producer_run_ref"] = "operation:missing" }, KindInvariantViolation},
		{"wrong-producer", func(p map[string]any) { p["producer_id"] = "principal/other" }, KindInvariantViolation},
		{"wrong-watermark", func(p map[string]any) { p["producer_watermark"] = "request/other" }, KindInvariantViolation},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			workID := "external-check-refusal-" + test.name
			fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "review")
			s := fixture.store
			subject := "commit:" + workID
			authority := "external-check-source-" + test.name
			seedWorkflowAuthority(t, s, authority, workID, "principal/check", "request/check", []string{subject})
			payload := map[string]any{"evidence_kind": "verification", "immutable_subject_ref": subject, "producer_id": "principal/check", "producer_run_ref": authority, "producer_watermark": "request/check"}
			test.mutate(payload)
			if test.kind == "" {
				if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(payload), 0, fixture.owner); err != nil {
					t.Fatalf("binding a declared kind: %v", err)
				}
				return
			}
			assertRecoveryRefusalWithoutEffect(t, s, workID, test.kind, func() error {
				return runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(payload), 0, fixture.owner)
			})
		})
	}
	t.Run("stale-version", func(t *testing.T) {
		t.Parallel()
		const workID = "external-check-stale-version"
		fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "review")
		assertRecoveryRefusalWithoutEffect(t, fixture.store, workID, KindVersionConflict, func() error {
			return runVerdictActionAs(t, fixture.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "commit:" + workID}), readWorkVersion(t, fixture.store, workID)-1, fixture.owner)
		})
	})
}

func TestEvidenceBindingAvailableForCurrentVerdicts(t *testing.T) {
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
			if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
				t.Fatalf("binding missing for associated=%t: %v", associated, pin.NextValidIntents)
			}
			if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": refs[0][0]}), 0, f.owner); err != nil {
				t.Fatalf("binding existing subject: %v", err)
			}
		})
	}
}

func TestExternalCheckRecoveryCompletedEventUsesSharedEligibility(t *testing.T) {
	for _, kind := range []string{"verification", "review"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			workID := "external-check-event-" + kind
			fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "review")
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
				"step_id": "review", "action_id": "bind_evidence", "attempt_epoch": 1, "actor_ref": actorRef,
				"result_evidence_refs": []string{subject}, "changed_refs": []string{workID},
			})
			apply := func() error {
				return applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{binding, completed}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
			}
			if err := apply(); err != nil {
				t.Fatalf("fold eligible external-check binding without a caller guard: %v", err)
			}
		})
	}
}

func TestOrdinaryEvidenceBindingStageAndLifecycle(t *testing.T) {
	for _, planType := range []WorkflowStepPlanType{WorkflowStepPlanChange, WorkflowStepPlanFix} {
		for _, tc := range []struct {
			step, instance, lifecycle string
			contracts                 int64
			want                      bool
		}{
			{"plan", "running", "in_progress", 1, false},
			{"review", "running", "in_progress", 1, true},
			{"review", "running", "in_progress", 0, false},
			{"review", "completed", "completed", 1, false},
			{"review", "running", "cancelled", 1, false},
		} {
			t.Run(fmt.Sprintf("%s-%s-%s-%s-%d", planType, tc.step, tc.instance, tc.lifecycle, tc.contracts), func(t *testing.T) {
				workID := "binding-stage"
				f := seedWorkflowReturnRouteFixture(t, workID, planType, tc.step)
				if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec(`UPDATE workflow_instances SET instance_state=? WHERE work_id=?`, tc.instance, workID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec(`UPDATE work_items SET lifecycle=? WHERE id=?`, tc.lifecycle, workID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.db.Exec(`DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
				if tc.contracts == 0 {
					if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
						t.Fatal(err)
					}
					if _, err := f.store.db.Exec(`UPDATE workflow_contracts SET superseded_by=1 WHERE work_id=?`, workID); err != nil {
						t.Fatal(err)
					}
					if _, err := f.store.db.Exec(`DELETE FROM write_guard`); err != nil {
						t.Fatal(err)
					}
				}
				var active int64
				if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil || active != tc.contracts {
					t.Fatalf("active contracts=%d, want %d: %v", active, tc.contracts, err)
				}
				payload := json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"evidence:stage"}`)
				_, err := WorkflowActionDefinitionFor(t.Context(), f.store, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), ActionID: "bind_evidence", Payload: payload, Actor: f.owner})
				if got := err == nil; got != tc.want {
					t.Errorf("admitted=%t, want %t: %v", got, tc.want, err)
				}
			})
		}
	}
}

func TestRecoveryRequiredKindStillNeedsDurableProducerAuthority(t *testing.T) {
	const workID = "recovery-kind-authority"
	ctx := context.Background()
	fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "review")
	if _, err := fixture.store.DatabaseForTesting().Exec(`DELETE FROM durable_operations WHERE op_id=?`, "return-authority-verification-"+workID); err != nil {
		t.Fatal(err)
	}
	outstanding, err := outstandingWorkflowEvidenceRequirementsForWork(ctx, fixture.store.db, workID)
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
			fixture := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "review")
			s := fixture.store
			request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "checkpoint_context", Payload: json.RawMessage(`{"checkpoint_id":"checkpoint:test","active_unit":"implementation","hypothesis":"hypothesis","diagnosis":"diagnosis","strategy":"strategy","touched_refs":["path:test"],"evidence_refs":["evidence:test"],"pending_questions":[],"pending_decisions":[]}`), Actor: fixture.owner}
			_, before := WorkflowActionDefinitionFor(ctx, s, request)
			if before != nil {
				t.Fatalf("discover context checkpoint before corrupting the predicate: %v", before)
			}
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			result, err := s.DatabaseForTesting().Exec(`UPDATE workflow_contract_predicates SET outcome_kind=?,outcome_payload=? WHERE work_id=? AND contract_version=1`, test.kind, test.payload, workID)
			if err != nil {
				t.Fatal(err)
			}
			if n, err := result.RowsAffected(); err != nil || n == 0 {
				t.Fatalf("predicate fixture updated %d rows: %v", n, err)
			}
			if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			if _, err := WorkflowActionDefinitionFor(ctx, s, request); err != nil {
				t.Errorf("binding predicate validation blocked another action's discovery: %v", err)
			}
			payload := json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"ci:result"}`)
			assertRecoveryRefusalWithoutEffect(t, s, workID, KindInvariantViolation, func() error {
				err := runVerdictActionAs(t, s, workID, "bind_evidence", payload, 0, fixture.owner)
				var failure *Failure
				if !failureAs(err, &failure) || failure.Detail != "approved outcome predicate is malformed or mismatched" {
					t.Errorf("binding with a %s predicate = %v, want the predicate refusal", test.name, err)
				}
				return err
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

func TestBindEvidencePastBindingStepOnceMandateBound(t *testing.T) {
	const workID = "mandate-recovery-narrow"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload()); err != nil {
		t.Fatalf("initial mandate binding refused: %v", err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload()); err != nil {
		t.Fatalf("late binding after mandate was satisfied: %v", err)
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
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"verification","evidence_ref":"spec:one"}`)); err != nil {
		t.Fatalf("binding after obligation was satisfied: %v", err)
	}
}

func TestBindEvidenceObligationAllowedOnceSatisfied(t *testing.T) {
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
	if err := runVerdictAction(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"review","evidence_ref":"spec:one"}`)); err != nil {
		t.Fatalf("satisfied obligation binding: %v", err)
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
	beforeStep := workflowPlanFixtureFrontier(t, s, workID)
	var beforeContract string
	if err := db.QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&beforeContract); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"review", "commit"} {
		payload := json.RawMessage(`{"evidence_kind":"` + kind + `","evidence_ref":"evidence:` + kind + `"}`)
		if err := runVerdictAction(t, s, workID, "bind_evidence", payload); err != nil {
			t.Fatalf("required %s recovery binding refused: %v", kind, err)
		}
	}
	afterStep := workflowPlanFixtureFrontier(t, s, workID)
	var afterContract string
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
