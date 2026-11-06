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
	s, _ := seedItemAtAcceptance(t, workID)
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
	seedWorkflowAuthority(t, s, "mandate-recovery-producer-"+workID, workID, "principal/mandate", "request/mandate-"+workID, []string{"spec:one"})
	return s
}

func recoveryBindPayload(workID string) json.RawMessage {
	return mustJSONValue(map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "spec:one", "producer_id": "principal/mandate", "producer_run_ref": "mandate-recovery-producer-" + workID, "producer_watermark": "request/mandate-" + workID})
}

func TestDeclaredStepBindsAllEvidenceKinds(t *testing.T) {
	for _, kind := range workflowEvidenceKinds {
		t.Run(string(kind), func(t *testing.T) {
			workID := "declared-bind-" + string(kind)
			fixture := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, "investigate", nil)
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
			producer := "declared-producer-" + workID
			seedWorkflowAuthority(t, fixture.store, producer, workID, "principal/declared", "request/declared", []string{ref})
			payload := mustJSONValue(map[string]any{"evidence_kind": string(kind), "immutable_subject_ref": ref, "producer_id": "principal/declared", "producer_run_ref": producer, "producer_watermark": "request/declared"})
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
	seedWorkflowAuthority(t, f.store, "late-bind-producer", workID, "principal/late", "request/late", []string{"evidence:unrelated"})
	payload := json.RawMessage(`{"evidence_kind":"undeclared","immutable_subject_ref":"evidence:unrelated","producer_id":"principal/late","producer_run_ref":"late-bind-producer","producer_watermark":"request/late"}`)
	assertRecoveryRefusalWithoutEffect(t, f.store, workID, KindInvalidPayload, func() error {
		return issue1013Preflight(t, f.store, workID, "bind_evidence", payload, f.owner)
	})
	assertRecoveryRefusalWithoutEffect(t, f.store, workID, KindInvalidPayload, func() error {
		return runVerdictActionAs(t, f.store, workID, "bind_evidence", payload, 0, f.owner)
	})
	declared := json.RawMessage(`{"evidence_kind":"approval","immutable_subject_ref":"evidence:unrelated","producer_id":"principal/late","producer_run_ref":"late-bind-producer","producer_watermark":"request/late"}`)
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

func TestCorrectedContractExternalCheckBindingAtAcceptance(t *testing.T) {
	const workID = "corrected-contract-external-check"
	subject := "ci:required-checks:corrected-subject"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "implement", nil)
	s := f.store
	successor := pContractSuccessor(t, s, workID, "Correct the checked subject.")
	if err := reviseContractRecord(t, s, workID, f.owner, successor, readWorkVersion(t, s, workID)); err != nil {
		t.Fatal(err)
	}
	moveWorkflowPlanFixtureFrontier(t, s, workID, "review")
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
	pin, err := ReadWorkPin(t.Context(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatal("acceptance omitted ordinary binding after proof and required kinds were satisfied")
	}
	if err := pConfirmReceiptPremise(t, workContextFixture{store: s, workID: workID, owner: f.owner}); err != nil {
		t.Fatalf("confirm successor: %v", err)
	}
	completeWorkflowPlanFixture(t, s, workID)
	j := &candidateJourney{s: s, workID: workID}
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
		subject := "commit:" + workID
		seedWorkflowAuthority(t, fixture.store, "stale-producer", workID, "principal/check", "request/check", []string{subject})
		payload := mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": subject, "producer_id": "principal/check", "producer_run_ref": "stale-producer", "producer_watermark": "request/check"})
		assertRecoveryRefusalWithoutEffect(t, fixture.store, workID, KindVersionConflict, func() error {
			return runVerdictActionAs(t, fixture.store, workID, "bind_evidence", payload, readWorkVersion(t, fixture.store, workID)-1, fixture.owner)
		})
	})
}

func TestEvidenceBindingAvailableAtReview(t *testing.T) {
	const workID = "external-check-review"
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", nil)
	pin, err := ReadWorkPin(t.Context(), f.store, workID)
	if err != nil || !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
		t.Fatalf("binding intent: %+v, %v", pin.NextValidIntents, err)
	}
	subject := "ci:review-check"
	seedWorkflowAuthority(t, f.store, "review-check-source", workID, "principal/check", "request/check", []string{subject})
	payload := mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": subject, "producer_id": "principal/check", "producer_run_ref": "review-check-source", "producer_watermark": "request/check"})
	if err := runVerdictActionAs(t, f.store, workID, "bind_evidence", payload, 0, f.owner); err != nil {
		t.Fatalf("binding subject: %v", err)
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
			{"review", "running", "in_progress", 0, true},
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
				payload := json.RawMessage(`{"evidence_kind":"verification","immutable_subject_ref":"evidence:stage","producer_id":"principal/stage","producer_run_ref":"stage-producer","producer_watermark":"request/stage"}`)
				_, err := WorkflowActionDefinitionFor(t.Context(), f.store, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), ActionID: "bind_evidence", Payload: payload, Actor: f.owner})
				if got := err == nil; got != tc.want {
					t.Errorf("admitted=%t, want %t: %v", got, tc.want, err)
				}
			})
		}
	}
	for _, contracts := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("authority-%d", contracts), func(t *testing.T) {
			workID := fmt.Sprintf("binding-authority-%d", contracts)
			f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", nil)
			s := f.store
			subject := "evidence:authority"
			seedWorkflowAuthority(t, s, "authority-producer", workID, "principal/authority", "request/authority", []string{subject})
			switch contracts {
			case 0:
				if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET superseded_by=1 WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
					t.Fatal(err)
				}
			case 2:
				duplicateActiveContractForTest(t.Context(), t, s, workID)
			}
			var active int
			if err := s.db.QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil || active != contracts {
				t.Fatalf("active contracts=%d want=%d err=%v", active, contracts, err)
			}
			payload := mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": subject, "producer_id": "principal/authority", "producer_run_ref": "authority-producer", "producer_watermark": "request/authority"})
			version := readWorkVersion(t, s, workID)
			request := planAdmissionRequest(workID, "bind_evidence", version, f.owner, payload)
			var beforeBindings, beforeReceipts int
			if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?1), (SELECT count(*) FROM effect_receipts WHERE subject_id=?1)`, workID).Scan(&beforeBindings, &beforeReceipts); err != nil {
				t.Fatal(err)
			}
			preflight := planAdmissionPreflight(t, s, request)
			dispatch := planAdmissionDispatch(t, s, request)
			if contracts <= 1 {
				if preflight != nil || dispatch != nil {
					t.Fatalf("contracts=%d: preflight=%v dispatch=%v", contracts, preflight, dispatch)
				}
				var after int
				if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, workID).Scan(&after); err != nil || after != beforeBindings+1 {
					t.Fatalf("binding count=%d want=%d err=%v", after, beforeBindings+1, err)
				}
				return
			}
			if !hasFailureKind(preflight, KindInvariantViolation) || !hasFailureKind(dispatch, KindInvariantViolation) {
				t.Fatalf("ambiguous contract: preflight=%v dispatch=%v", preflight, dispatch)
			}
			var afterBindings, afterReceipts int
			if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?1), (SELECT count(*) FROM effect_receipts WHERE subject_id=?1)`, workID).Scan(&afterBindings, &afterReceipts); err != nil {
				t.Fatal(err)
			}
			if got := readWorkVersion(t, s, workID); got != version || beforeBindings != afterBindings || beforeReceipts != afterReceipts {
				t.Fatalf("refusal changed state: version %d/%d bindings %d/%d receipts %d/%d", version, got, beforeBindings, afterBindings, beforeReceipts, afterReceipts)
			}
		})
	}
}

func TestBindEvidenceAdmittedPastBindingStepWhileMandateUnbound(t *testing.T) {
	const workID = "mandate-recovery-admit"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(workID)); err != nil {
		t.Fatalf("late mandate binding refused: %v", err)
	}
}

func TestBindEvidencePastBindingStepOnceMandateBound(t *testing.T) {
	const workID = "mandate-recovery-narrow"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(workID)); err != nil {
		t.Fatalf("initial mandate binding refused: %v", err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(workID)); err != nil {
		t.Fatalf("late binding after mandate was satisfied: %v", err)
	}
}

func TestBindEvidenceDifferentKindsForSameSubject(t *testing.T) {
	const workID = "obligation-recovery-admit"
	s := seedMandateRecoveryItem(t, workID)
	if err := runVerdictAction(t, s, workID, "bind_evidence", recoveryBindPayload(workID)); err != nil {
		t.Fatalf("mandate binding refused: %v", err)
	}
	payload := mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "spec:one", "producer_id": "principal/mandate", "producer_run_ref": "mandate-recovery-producer-" + workID, "producer_watermark": "request/mandate-" + workID})
	for i := 0; i < 2; i++ {
		if err := runVerdictAction(t, s, workID, "bind_evidence", payload); err != nil {
			t.Fatalf("verification binding %d: %v", i, err)
		}
	}
}

func TestBindEvidenceKindsKeepContractAndStep(t *testing.T) {
	const workID = "required-kind-recovery-empty-mandate"
	s, _ := seedItemAtAcceptance(t, workID)
	db := s.DatabaseForTesting()
	beforeStep := workflowPlanFixtureFrontier(t, s, workID)
	var beforeContract string
	if err := db.QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&beforeContract); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"review", "commit"} {
		ref, op := "evidence:"+kind, "kind-producer-"+kind
		seedWorkflowAuthority(t, s, op, workID, "principal/kind", "request/"+kind, []string{ref})
		payload := mustJSONValue(map[string]any{"evidence_kind": kind, "immutable_subject_ref": ref, "producer_id": "principal/kind", "producer_run_ref": op, "producer_watermark": "request/" + kind})
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
