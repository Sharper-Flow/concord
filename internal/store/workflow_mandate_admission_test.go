package store

import (
	"context"
	"encoding/json"
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
	rejected, err := workflowRejectedWorkerResultAvailable(ctx, q, workID, definition, step, subject)
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

func setAdmissionMandate(t *testing.T, s *Store, workID string, law WorkflowLawRevision) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE workflow_contracts SET spec_mandate=? WHERE work_id=? AND superseded_by IS NULL`, string(mustJSONValue([]string{law.LawID})), workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES(?,1,?,?)`, workID, law.LawID, law.ContentHash); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
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
	setAdmissionMandate(t, s, workID, WorkflowLawRevision{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)})
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
		conformanceCheckpoint(t, s, BuiltinWorkflowRegistry(), workID, definition, admissionModelState{
			step: pin.Step, contracts: 1, mandate: mandate, engineering: true,
		}, mandate)
		decision := workflowAdmit(definition, state, "record_verdict")
		payload := json.RawMessage(`{"verdicts":[{"predicate_id":"predicate:return-route","receipt_refs":["worktree_verify:mandate-unreachable"]}]}`)
		preflightErr := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: workID, ExpectedVersion: pin.Version, ActionID: "record_verdict", Actor: fixture.operator, Payload: payload,
		})
		advertised := workPinContainsAction(pin.NextValidIntents, "record_verdict")
		if bound {
			// A bound mandate no longer refuses. The fixture carries no
			// integrated candidate, so the payload-bound record_verdict
			// validation refuses on that next requirement instead — the
			// mandate dimension itself is satisfied on both sides.
			if !decision.Admitted || !advertised {
				t.Fatalf("bound mandate: admitted=%v, advertised=%v", decision.Admitted, advertised)
			}
			var boundFailure *Failure
			if !failureAs(preflightErr, &boundFailure) || boundFailure.Kind != KindMissingEvidence ||
				!strings.Contains(boundFailure.Detail, "current integrated candidate") || strings.Contains(boundFailure.Detail, "spec:one") {
				t.Fatalf("bound mandate preflight = %v, want the candidate requirement past the satisfied mandate", preflightErr)
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
	for _, disposition := range []string{"continue", "correction_required", "revision_required", "ready_for_final_proof"} {
		t.Run(disposition, func(t *testing.T) {
			workID := "mandate-ready-review-" + disposition
			j, _ := descendantReviewFixture(t, workID)
			s := j.s
			// The mandate is pinned before Review judges it: a pin added after
			// the report would stale that Review (CD-0242 D10).
			law := WorkflowLawRevision{LawID: j.fixture.sources.lawProject + "/" + j.fixture.sources.lawID, ContentHash: j.fixture.sources.lawHash}
			setAdmissionMandate(t, s, workID, law)
			attempt, epoch := completeCurrentReviewForTest(t, j.fixture, j.repo, disposition)
			payload := map[string]any{"attempt_id": attempt, "attempt_epoch": epoch}
			for _, bound := range []bool{false, true} {
				if bound {
					if err := j.action(t, "bind_evidence", map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": law.LawID}, j.fixture.owner); err != nil {
						t.Fatalf("bind mandated law: %v", err)
					}
				}
				pin := issue1013Pin(t, s, workID)
				before := j.effects(t)
				beforeReview := string(mustJSONValue(pin.Review))
				definition, state := loadedAdmissionForTest(t, s, workID, pin)
				decision := workflowAdmit(definition, state, "accept_worker_result")
				advertised := workPinContainsAction(pin.NextValidIntents, "accept_worker_result")
				preflightErr := WorkflowActionPreflightWithRegistry(context.Background(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
					WorkID: workID, ExpectedVersion: pin.Version, ActionID: "accept_worker_result", Actor: j.fixture.owner, Payload: mustJSONValue(payload), SessionWorktree: j.repo,
				})
				if after := j.effects(t); after != before {
					t.Fatal("mandate preflight changed durable state")
				}
				executionErr := j.action(t, "accept_worker_result", payload, j.fixture.owner)
				accepted := issue1013Pin(t, s, workID)
				if bound {
					if !decision.Admitted || !advertised || preflightErr != nil || executionErr != nil {
						t.Fatalf("bound mandate: admission=%+v, advertised=%v, preflight=%v, execution=%v", decision, advertised, preflightErr, executionErr)
					}
					if accepted.Step != pin.Step || accepted.Review.Accepted == nil || accepted.Review.Accepted.AttemptID != attempt || accepted.Review.Accepted.Report.Disposition != disposition {
						t.Fatalf("local acceptance advanced execution or lost the exact Review: %+v", accepted)
					}
					continue
				}
				var preflightFailure, executionFailure *Failure
				if decision.Admitted || advertised ||
					!failureAs(preflightErr, &preflightFailure) || !failureAs(executionErr, &executionFailure) ||
					preflightFailure.Kind != KindMissingEvidence || executionFailure.Kind != KindMissingEvidence ||
					decision.Failure.Detail != preflightFailure.Detail || decision.Failure.Detail != executionFailure.Detail ||
					!strings.Contains(executionFailure.Detail, law.LawID) {
					t.Fatalf("ready review bypasses unbound mandate: admission=%+v, advertised=%v, preflight=%v, execution=%v", decision, advertised, preflightErr, executionErr)
				}
				if after := j.effects(t); after != before || string(mustJSONValue(accepted.Review)) != beforeReview {
					t.Fatal("refused mandate acceptance changed durable state or Review authority")
				}
				if disposition == "ready_for_final_proof" {
					combined := map[string]any{
						"attempt_id": attempt, "attempt_epoch": epoch,
						"delivery_artifact": "artifact:mandate-agreement", "delivery_state": "asserted",
					}
					preflightErr := WorkflowActionPreflightWithRegistry(t.Context(), s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
						WorkID: workID, ExpectedVersion: pin.Version, ActionID: "accept_worker_result", Actor: j.fixture.owner, Payload: mustJSONValue(combined), SessionWorktree: j.repo,
					})
					executionErr := j.action(t, "accept_worker_result", combined, j.fixture.owner)
					var preflightFailure, executionFailure *Failure
					if !failureAs(preflightErr, &preflightFailure) || !failureAs(executionErr, &executionFailure) ||
						preflightFailure.Kind != KindMissingEvidence || executionFailure.Kind != KindMissingEvidence ||
						preflightFailure.Detail != decision.Failure.Detail || executionFailure.Detail != decision.Failure.Detail {
						t.Fatalf("delivery-bearing acceptance bypasses the mandate: preflight=%v, execution=%v", preflightErr, executionErr)
					}
					if j.effects(t) != before || string(mustJSONValue(issue1013Pin(t, s, workID).Review)) != beforeReview {
						t.Fatal("refused delivery-bearing acceptance changed durable state or Review authority")
					}
				}
				if !workPinContainsAction(pin.NextValidIntents, "bind_evidence") {
					t.Fatal("unbound mandate hides evidence binding")
				}
			}
		})
	}
}
