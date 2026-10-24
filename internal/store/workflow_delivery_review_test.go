package store

import (
	"strings"
	"testing"
)

func TestCombinedAcceptRequiresAcceptedReadyDisposition(t *testing.T) {
	for _, family := range acceptDeliveryFamilies {
		t.Run(family.ref, func(t *testing.T) {
			c := acceptDeliveryCandidateAt(t, "combined-review-disposition", family.ref, "refine", false)
			acceptCurrentReviewForTest(t, c.f, c.j.repo, "correction_required")
			c.attempt = "attempt:combined-review-disposition:ready"
			c.epoch = acceptDeliveryCompleteReview(t, c, c.attempt)
			acceptDeliveryBindFresh(t, c, "combined-review-disposition")
			before := c.j.effects(t)
			err := runDeliveryAdmissionAction(t, c.f.store, c.f.workID, "accept_worker_result", acceptDeliveryCombinedPayload(c, "artifact:combined-review-disposition"), nil, reviewGateAcceptor(c.f.workID))
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "correction_required") {
				t.Fatalf("combined delivery behind correction_required = %v, want the accepted-disposition refusal", err)
			}
			if c.j.effects(t) != before {
				t.Fatal("refused combined delivery changed durable state")
			}
		})
	}
}
