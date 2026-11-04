package store

import (
	"strings"
	"testing"
)

func TestDeliveryRecoversCandidateVerification(t *testing.T) {
	for _, planType := range []WorkflowStepPlanType{WorkflowStepPlanChange, WorkflowStepPlanFix} {
		t.Run(string(planType), func(t *testing.T) {
			const workID = "delivery-proof-recovery"
			c := acceptDeliveryUnobservedCandidateAt(t, workID, planType)
			payload := mustJSONValue(map[string]any{"delivery_artifact": "artifact:proof-recovery", "delivery_state": "asserted"})
			deliver := func() error {
				return runDeliveryAdmissionAction(t, c.f.store, workID, payload, c.f.owner)
			}
			before := readWorkVersion(t, c.f.store, workID)
			requireMissing := func(err error) {
				t.Helper()
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "passing verification report on the current reviewed candidate") {
					t.Fatalf("delivery refusal = %v, want missing candidate verification", err)
				}
			}
			requireMissing(deliver())
			if got := readWorkVersion(t, c.f.store, workID); got != before {
				t.Fatalf("refused delivery changed version %d to %d", before, got)
			}
			acceptDeliveryFailedReport(t, c)
			requireMissing(deliver())
			acceptDeliveryBindFresh(t, c, "recovery")
			if err := deliver(); err != nil {
				t.Fatalf("delivery after candidate verification: %v", err)
			}
			if got := workflowPlanFixtureFrontier(t, c.f.store, workID); got != "review" {
				t.Fatalf("delivery changed frontier to %q", got)
			}
		})
	}
}

func TestDeliveryRecoversCorrectedCandidateVerification(t *testing.T) {
	const workID = "delivery-corrected-proof"
	c := acceptDeliveryCorrectedCandidateAt(t, workID, WorkflowStepPlanChange)
	payload := mustJSONValue(map[string]any{"delivery_artifact": "artifact:corrected-proof", "delivery_state": "asserted"})
	before := readWorkVersion(t, c.f.store, workID)
	err := runDeliveryAdmissionAction(t, c.f.store, workID, payload, c.f.owner)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("delivery with stale proof = %v, want missing evidence", err)
	}
	if got := readWorkVersion(t, c.f.store, workID); got != before {
		t.Fatalf("refused delivery changed version %d to %d", before, got)
	}
	acceptDeliveryBindFresh(t, c, "corrected")
	if err := runDeliveryAdmissionAction(t, c.f.store, workID, payload, c.f.owner); err != nil {
		t.Fatalf("delivery with corrected candidate proof: %v", err)
	}
}

func TestDeliveryWithoutVerificationRequirementNeedsNoVerificationReport(t *testing.T) {
	const workID = "delivery-without-verification-requirement"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, WorkflowStepPlanChange)
	setRequiredEvidenceKinds(t, c.f.store, workID, "review", "artifact")
	payload := mustJSONValue(map[string]any{"delivery_artifact": "artifact:no-verification-requirement", "delivery_state": "asserted"})
	if err := runDeliveryAdmissionAction(t, c.f.store, workID, payload, c.f.owner); err != nil {
		t.Fatalf("delivery with ready review and no verification requirement: %v", err)
	}
	if got := workflowPlanFixtureFrontier(t, c.f.store, workID); got != "review" {
		t.Fatalf("delivery changed frontier to %q", got)
	}
}
