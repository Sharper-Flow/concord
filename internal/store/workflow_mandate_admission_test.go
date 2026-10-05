package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Isolated mandate fixtures omit a full workflow instance. They load the
// same gate facts and exercise the pure mandate owner with those facts.
func mandateFixtureAdmission(ctx context.Context, q queryer, workID string, definition WorkflowDefinition, step, actionID string) error {
	const subject = "workflow_action"
	state := WorkflowAdmissionState{}
	var err error
	state.ContractCorrectionAvailable, err = workflowContractCorrectionAvailable(ctx, q, workID, definition, step, subject)
	if err != nil {
		return err
	}
	rejected, err := workflowRejectedWorkerResultAvailable(ctx, q, workID, definition, step, subject, 0)
	if err != nil {
		return err
	}
	state.WorkerFailureRecovery, err = workflowWorkerFailureRecoveryAvailable(ctx, q, workID, definition, step, subject)
	if err != nil {
		return err
	}
	mandate, err := loadWorkflowMandateAdmission(ctx, q, workID, definition, step, subject, state, rejected)
	if err != nil {
		return err
	}
	if failure := workflowAdmitMandate(definition, step, mandate, actionID, subject); failure != nil {
		return failure
	}
	return nil
}

func setAdmissionMandate(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE workflow_contracts SET spec_mandate='["spec:one"]' WHERE work_id=? AND superseded_by IS NULL`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,'spec:one','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`, workID); err != nil {
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestMandateAdmissionAgreement(t *testing.T) {
	const workID = "mandate-admission-agreement"
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.implementation", "acceptance")
	s := fixture.store
	ctx := context.Background()
	setAdmissionMandate(t, s, workID)
	for _, bound := range []bool{false, true} {
		if bound {
			payload := json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}`)
			if err := runVerdictActionAs(t, s, workID, "bind_evidence", payload, 0, fixture.owner); err != nil {
				t.Fatalf("bind the mandated law: %v", err)
			}
		}
		pin := issue1013Pin(t, s, workID)
		definition, state := loadedAdmissionForTest(t, s, workID, pin)
		mandate := "missing"
		if bound {
			mandate = "bound"
		}
		conformanceCheckpoint(t, s, workID, definition, admissionModelState{
			step: pin.Step, debt: ReviewDebtNone, contracts: 1, mandate: mandate,
		}, mandate)
		decision := workflowAdmit(definition, state, "record_verdict")
		payload := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:return-route","verdict_kind":"ok"}`)
		preflightErr := InspectWorkflowActionAdmission(ctx, s, WorkflowActionPreflightRequest{
			WorkID: workID, ExpectedVersion: pin.Version, ActionID: "record_verdict", Actor: fixture.operator, Payload: payload,
		})
		advertised := workPinContainsAction(pin.NextValidIntents, "record_verdict")
		if bound {
			if !decision.Admitted || !advertised || preflightErr != nil {
				t.Fatalf("bound mandate: admitted=%v, advertised=%v, preflight=%v", decision.Admitted, advertised, preflightErr)
			}
			continue
		}
		var failure *Failure
		if decision.Admitted || advertised || !failureAs(preflightErr, &failure) || failure.Kind != KindMissingEvidence ||
			decision.Failure.Kind != failure.Kind || decision.Failure.Detail != failure.Detail || !strings.Contains(failure.Detail, "spec:one") {
			t.Fatalf("unbound mandate: admitted=%v, advertised=%v, pure=%v, preflight=%v", decision.Admitted, advertised, decision.Failure, preflightErr)
		}
		if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
			t.Fatal("mandate refusal hides its evidence-binding recovery")
		}
	}
}

func TestReadyReviewCannotBypassMandate(t *testing.T) {
	for _, verdict := range []string{"ship", "no_ship"} {
		t.Run(verdict, func(t *testing.T) {
			workID := "mandate-ready-review-" + verdict
			fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
			s := fixture.store
			ownerRef, err := WorkflowActorRef(fixture.owner)
			if err != nil {
				t.Fatal(err)
			}
			at := int64(100)
			epoch := reviewGateDriveRejection(t, fixture, workID, &at)
			attempt := "attempt:" + workID + ":review-2"
			reviewGateRunAttemptWithVerdict(t, s, workID, attempt, epoch, reviewGateLane(t, "review"), ownerRef, verdict, at)
			refineProofSeedGreenRun(t, s, workID, strings.Repeat("e", 64))
			setAdmissionMandate(t, s, workID)
			payload := json.RawMessage(fmt.Sprintf(`{"attempt_id":%q,"attempt_epoch":%d}`, attempt, epoch))
			if verdict == "ship" {
				payload = json.RawMessage(fmt.Sprintf(`{"attempt_id":%q,"attempt_epoch":%d,"delivery_artifact":"artifact:mandate-agreement","delivery_state":"asserted"}`, attempt, epoch))
			}
			for _, bound := range []bool{false, true} {
				if bound {
					if err := runVerdictActionAs(t, s, workID, "bind_evidence", json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}`), 0, fixture.owner); err != nil {
						t.Fatalf("bind mandated law: %v", err)
					}
				}
				pin := issue1013Pin(t, s, workID)
				definition, state := loadedAdmissionForTest(t, s, workID, pin)
				decision := workflowAdmit(definition, state, "accept_worker_result")
				advertised := workPinContainsAction(pin.NextValidIntents, "accept_worker_result")
				preflightErr := InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{
					WorkID: workID, ExpectedVersion: pin.Version, ActionID: "accept_worker_result", Actor: reviewGateAcceptor(workID), Payload: payload,
				})
				executionErr := runVerdictActionAs(t, s, workID, "accept_worker_result", payload, 0, reviewGateAcceptor(workID))
				if bound {
					if !decision.FreshReviewRequired || decision.ReadyReviewAttemptID != attempt || !advertised || preflightErr != nil || executionErr != nil {
						t.Fatalf("bound mandate: admission=%+v, advertised=%v, preflight=%v, execution=%v", decision, advertised, preflightErr, executionErr)
					}
					wantStep := "refine"
					if verdict == "ship" {
						wantStep = "delivery"
					}
					reviewGateRequireStep(t, s, workID, wantStep)
					continue
				}
				var preflightFailure, executionFailure *Failure
				if decision.Admitted || decision.FreshReviewRequired || advertised ||
					!failureAs(preflightErr, &preflightFailure) || !failureAs(executionErr, &executionFailure) ||
					preflightFailure.Kind != KindMissingEvidence || executionFailure.Kind != KindMissingEvidence ||
					decision.Failure.Detail != preflightFailure.Detail || decision.Failure.Detail != executionFailure.Detail ||
					!strings.Contains(executionFailure.Detail, "spec:one") {
					t.Fatalf("ready review bypasses unbound mandate: admission=%+v, advertised=%v, preflight=%v, execution=%v", decision, advertised, preflightErr, executionErr)
				}
				reviewGateRequireStep(t, s, workID, "refine")
				if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
					t.Fatal("unbound mandate hides evidence binding")
				}
			}
		})
	}
}
