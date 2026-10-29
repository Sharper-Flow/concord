package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// CD-0229 D5 retains mandatory outcomes at small size. This fixture omits
// optional architecture depth but uses the current shared implementation
// definition, real command receipts, and Main's accepted Review assessment.
// It does not claim a generic footprint-anchor execution mapping.
func smallImplementationAtRelease(t *testing.T) acceptDeliveryCandidate {
	t.Helper()
	c := acceptDeliveryCandidateAt(t, "small-route-outcomes", "workflow.implementation", "execution", false)
	inputs := workSizeInputsFixture()
	size, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil || size != WorkSizeSmall {
		t.Fatalf("small fixture model = %q, %v", size, err)
	}
	if err := applyWorkEvent(t, c.f.store, workSizedEvent("small-route-size", c.f.workID, WorkSizeModelVersion, size, inputs), nil); err != nil {
		t.Fatal(err)
	}
	recorded, ok, err := readWorkSize(t.Context(), c.f.store.db, c.f.workID)
	if err != nil || !ok || recorded.Size != WorkSizeSmall {
		t.Fatalf("recorded size = %+v, present=%v, err=%v", recorded, ok, err)
	}
	review, err := loadWorkflowReviewState(t.Context(), c.f.store.db, c.f.workID)
	if err != nil || review.Dependencies == nil || !review.Dependencies.ArchitectureNotApplicable || review.Accepted == nil || review.Accepted.Report.Disposition != "ready_for_final_proof" {
		t.Fatalf("shallow preparation lost its accepted Review: %+v, %v", review, err)
	}

	tooling := refineProofTooling(ProjectDeclaredTool{ID: "candidate-head", Invocation: "git rev-parse --verify HEAD^{commit}"})
	acceptDeliveryBindFresh(t, c, "small-delivery")
	delivery := json.RawMessage(`{"delivery_artifact":"artifact:small-route","delivery_state":"asserted"}`)
	for _, step := range []string{"execution", "refine", "delivery"} {
		reviewGateRequireStep(t, c.f.store, c.f.workID, step)
		if step == "refine" {
			if err := c.j.action(t, "checkpoint_refine", map[string]any{}, c.f.owner); err != nil {
				t.Fatal(err)
			}
		}
		if err := runDeliveryAdmissionAction(t, c.f.store, c.f.workID, "record_delivery", delivery, tooling, c.f.owner); err != nil {
			t.Fatalf("%s delivery: %v", step, err)
		}
	}
	reviewGateRequireStep(t, c.f.store, c.f.workID, "acceptance")
	seedPremiseDeliverablesInvestigation(t, c.f.store, c.f.workID)
	if err := c.j.action(t, "record_verdict", criterionAssociationPayload(c.early.OperationRef, "predicate:return-route"), c.f.owner); err != nil {
		t.Fatal(err)
	}
	if err := c.j.action(t, "confirm_premise", map[string]any{"contract_version": 1}, c.f.owner); err != nil {
		t.Fatal(err)
	}
	reviewGateRequireStep(t, c.f.store, c.f.workID, "release")
	bindCompletionClosure(t, c.f.store, c.f.workID, c.f.owner)
	return c
}

func TestSmallImplementationCompletionRetainsMandatoryOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		damage string
		kind   FailureKind
		detail string
	}{
		{name: "complete"},
		{name: "missing-contract-predicates", damage: `DELETE FROM workflow_contract_predicates WHERE work_id='small-route-outcomes'`, kind: KindMissingEvidence, detail: "accepted Review on the current candidate"},
		{name: "missing-criterion-proof", damage: `DELETE FROM workflow_predicate_verdicts WHERE work_id='small-route-outcomes'`, kind: KindMissingEvidence, detail: "workflow verdict is missing"},
		{name: "missing-producer-receipt", damage: `DELETE FROM durable_operations WHERE json_extract(result_payload,'$.lease_id')='candidate-observe-early' AND work_id='small-route-outcomes'`, kind: KindMissingEvidence, detail: "requires a passing producer receipt on the final candidate"},
		{name: "review-not-accepted", damage: `UPDATE workflow_review_state SET state=json_set(state,'$.accepted',json('null')) WHERE work_id='small-route-outcomes'`, kind: KindMissingEvidence, detail: "accepted Review on the current candidate"},
		{name: "blocking-review", damage: `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.report.disposition','correction_required') WHERE work_id='small-route-outcomes'`, kind: KindMissingEvidence, detail: "ready_for_final_proof"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := smallImplementationAtRelease(t)
			// Remove one current prerequisite after successful confirmation.
			// Completion must recheck it rather than trust an earlier advance.
			if tc.damage != "" {
				execInFold(t, c.f.store, tc.damage)
			}
			before := c.j.effects(t)
			err := c.j.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}, c.f.owner)
			if tc.damage == "" {
				if err != nil {
					t.Fatal(err)
				}
				var state string
				if err := c.f.store.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, c.f.workID).Scan(&state); err != nil || state != "completed" {
					t.Fatalf("instance state = %q, %v, want completed", state, err)
				}
				return
			}
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != tc.kind || !strings.Contains(failure.Detail, tc.detail) || failure.RecoveryAction == "" {
				t.Fatalf("completion = %v, want %s naming %q and recovery", err, tc.kind, tc.detail)
			}
			if after := c.j.effects(t); after != before {
				t.Fatalf("refused completion changed state:\nbefore=%s\nafter=%s", before, after)
			}
			reviewGateRequireStep(t, c.f.store, c.f.workID, "release")
		})
	}
}
