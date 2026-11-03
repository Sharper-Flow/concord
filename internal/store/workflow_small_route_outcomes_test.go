package store

import (
	"encoding/json"
	"testing"
)

// This fixture uses the change plan with a current accepted Review and proof.
func smallImplementationCompletePlan(t *testing.T) acceptDeliveryCandidate {
	t.Helper()
	c := acceptDeliveryCandidateAt(t, "small-route-outcomes", WorkflowStepPlanChange, "review")
	review, err := loadWorkflowReviewState(t.Context(), c.f.store.db, c.f.workID)
	if err != nil || review.Dependencies == nil || !review.Dependencies.ArchitectureNotApplicable || review.Accepted == nil || review.Accepted.Report.Disposition != "ready_for_final_proof" {
		t.Fatalf("shallow preparation lost its accepted Review: %+v, %v", review, err)
	}

	acceptDeliveryBindFresh(t, c, "small-delivery")
	delivery := json.RawMessage(`{"delivery_artifact":"artifact:small-route","delivery_state":"asserted"}`)
	if err := runDeliveryAdmissionAction(t, c.f.store, c.f.workID, delivery, c.f.owner); err != nil {
		t.Fatalf("delivery: %v", err)
	}
	if err := c.j.action(t, "record_verdict", criterionAssociationPayload(c.early.OperationRef, "predicate:return-route"), c.f.owner); err != nil {
		t.Fatal(err)
	}
	if err := c.j.action(t, "confirm_premise", map[string]any{"contract_version": 1}, c.f.owner); err != nil {
		t.Fatal(err)
	}
	completeWorkflowPlanFixture(t, c.f.store, c.f.workID)
	return c
}

func TestSmallImplementationCompletionDoesNotRecheckAcceptance(t *testing.T) {
	cases := []struct {
		name   string
		damage string
	}{
		{name: "complete"},
		{name: "missing-contract-predicates", damage: `DELETE FROM workflow_contract_predicates WHERE work_id='small-route-outcomes'`},
		{name: "missing-criterion-proof", damage: `DELETE FROM workflow_predicate_verdicts WHERE work_id='small-route-outcomes'`},
		{name: "missing-confirmation", damage: `DELETE FROM workflow_premise_confirmations WHERE work_id='small-route-outcomes'`},
		{name: "missing-report", damage: `DELETE FROM workflow_evidence_bindings WHERE work_id='small-route-outcomes' AND json_type(payload,'$.verification_report')='object'`},
		{name: "review-not-accepted", damage: `UPDATE workflow_review_state SET state=json_set(state,'$.accepted',json('null')) WHERE work_id='small-route-outcomes'`},
		{name: "blocking-review", damage: `UPDATE workflow_review_state SET state=json_set(state,'$.accepted.report.disposition','correction_required') WHERE work_id='small-route-outcomes'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := smallImplementationCompletePlan(t)
			// Change acceptance facts after successful confirmation.
			if tc.damage != "" {
				execInFold(t, c.f.store, tc.damage)
			}
			err := c.j.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}, c.f.owner)
			if err != nil {
				t.Fatal(err)
			}
			var state string
			if err := c.f.store.db.QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, c.f.workID).Scan(&state); err != nil || state != "completed" {
				t.Fatalf("instance state = %q, %v, want completed", state, err)
			}
		})
	}
}
