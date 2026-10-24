package store

import (
	"strings"
	"testing"
)

func TestCombinedAcceptRequiresAcceptedReadyDisposition(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		for _, disposition := range []string{"continue", "correction_required", "revision_required"} {
			t.Run(family.ref+"/"+disposition, func(t *testing.T) {
				c := acceptDeliveryCandidateAt(t, "combined-review-disposition", family.ref, "refine", false)
				acceptCurrentReviewForTest(t, c.f, c.j.repo, "ready_for_final_proof")
				c.attempt = "attempt:combined-review-disposition:pending"
				c.epoch = acceptDeliveryCompleteReview(t, c, c.attempt, disposition)
				acceptDeliveryBindFresh(t, c, "combined-review-disposition")
				before := c.j.effects(t)
				err := runDeliveryAdmissionAction(t, c.f.store, c.f.workID, "accept_worker_result", acceptDeliveryCombinedPayload(c, "artifact:combined-review-disposition"), nil, reviewGateAcceptor(c.f.workID))
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "not "+disposition) {
					t.Fatalf("combined delivery accepting %s = %v, want the accepted-disposition refusal", disposition, err)
				}
				if c.j.effects(t) != before {
					t.Fatal("refused combined delivery changed durable state")
				}
			})
		}
	}
}
