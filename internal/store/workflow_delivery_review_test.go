package store

import (
	"strings"
	"testing"
)

func TestDeliveryRequiresRecordedReadyDisposition(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		for _, disposition := range []string{"continue", "correction_required", "revision_required"} {
			t.Run(string(family)+"/"+disposition, func(t *testing.T) {
				c := acceptDeliveryCandidateAt(t, "combined-review-disposition", family, "review")
				acceptCurrentReviewForTest(t, c.f, "ready_for_final_proof")
				completeCurrentReviewForTest(t, c.f, disposition)
				state, err := loadWorkflowReviewState(t.Context(), c.f.store.db, c.f.workID)
				if err != nil || state.Accepted == nil || state.Accepted.Report.Disposition != disposition {
					t.Fatalf("recorded Review = %+v, err=%v, want %s", state.Accepted, err, disposition)
				}
				acceptDeliveryBindFresh(t, c, "combined-review-disposition")
				before := c.j.effects(t)
				payload := mustJSONValue(map[string]any{"delivery_artifact": "artifact:review-disposition", "delivery_state": "asserted"})
				_, err = WorkflowActionDefinitionFor(t.Context(), c.f.store, WorkflowActionPreflightRequest{WorkID: c.f.workID, ExpectedVersion: readWorkVersion(t, c.f.store, c.f.workID), ActionID: "record_delivery", Payload: payload, Actor: reviewGateAcceptor(c.f.workID)})
				if !hasFailureKind(err, KindMissingEvidence) || !strings.Contains(err.Error(), "Review has not reached ready_for_final_proof") {
					t.Fatalf("delivery preflight under %s = %v", disposition, err)
				}
				err = runDeliveryAdmissionAction(t, c.f.store, c.f.workID, payload, reviewGateAcceptor(c.f.workID))
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "Review has not reached ready_for_final_proof without remaining obligations") {
					t.Fatalf("delivery under %s = %v, want the ready-Review refusal", disposition, err)
				}
				if c.j.effects(t) != before {
					t.Fatal("refused combined delivery changed durable state")
				}
				reviewGateRequireStep(t, c.f.store, c.f.workID, "review")
				acceptCurrentReviewForTest(t, c.f, "ready_for_final_proof")
				if err := reviewGateRecordDelivery(t, c.f.store, c.f.workID, reviewGateAcceptor(c.f.workID)); err != nil {
					t.Fatalf("delivery after recorded ready Review: %v", err)
				}
				reviewGateRequireStep(t, c.f.store, c.f.workID, "review")
			})
		}
	}
}
