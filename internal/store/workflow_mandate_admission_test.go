package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func setAdmissionMandates(t *testing.T, s *Store, workID string, laws []string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET spec_mandate=? WHERE work_id=? AND superseded_by IS NULL; DELETE FROM write_guard`, string(mustJSONValue(laws)), workID); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowPlanMandateBlocksDeliveryUntilBound(t *testing.T) {
	const workID = "plan-mandate-delivery"
	f, _ := deliveryPlanFixture(t, workID, []string{}, "ready_for_final_proof")
	setAdmissionMandates(t, f.store, workID, []string{"spec:one"})
	version := readWorkVersion(t, f.store, workID)
	payload := json.RawMessage(`{"delivery_artifact":"artifact:mandate","delivery_state":"asserted"}`)
	request := planAdmissionRequest(workID, "record_delivery", version, f.owner, payload)
	first := planAdmissionFailure(t, planAdmissionPreflight(t, f.store, request), KindMissingEvidence)
	second := planAdmissionFailure(t, planAdmissionDispatch(t, f.store, request), KindMissingEvidence)
	if first.Detail != second.Detail || !strings.Contains(first.Detail, "spec:one") || readWorkVersion(t, f.store, workID) != version {
		t.Fatalf("delivery mandate refusal: %v / %v", first, second)
	}
	bind := planAdmissionRequest(workID, "bind_evidence", version, f.owner, json.RawMessage(`{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}`))
	if err := planAdmissionDispatch(t, f.store, bind); err != nil {
		t.Fatalf("bind mandate: %v", err)
	}
	request = planAdmissionRequest(workID, "record_delivery", readWorkVersion(t, f.store, workID), f.owner, payload)
	if err := planAdmissionPreflight(t, f.store, request); err != nil {
		t.Fatalf("bound delivery preflight: %v", err)
	}
	if err := planAdmissionDispatch(t, f.store, request); err != nil {
		t.Fatalf("bound delivery dispatch: %v", err)
	}
}

func TestWorkflowPlanMandateLawAdditionAndInputOrder(t *testing.T) {
	for _, laws := range [][]string{{"CD-0189"}, {"CD-0189", "spec:one"}, {"spec:one", "CD-0189"}} {
		for _, inputBound := range []bool{false, true} {
			name := fmt.Sprintf("%s/bound-%t", strings.Join(laws, ","), inputBound)
			t.Run(name, func(t *testing.T) {
				workID := fmt.Sprintf("plan-mandate-addition-%d-%t-%t", len(laws), laws[0] == "CD-0189", inputBound)
				f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", []string{}, nil)
				setAdmissionMandates(t, f.store, workID, laws)
				// The contract's existing synthetic architecture binding owns the
				// reservation and addition rows used by this isolated gate test.
				if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product','CD-0189',?,1,'root');
INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES(?,1,'product','CD-0189','root',?,1);
DELETE FROM write_guard`, workID, workID, workID); err != nil {
					t.Fatal(err)
				}
				if inputBound {
					if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO workflow_evidence_bindings(work_id,binding_seq,binding_id,actor,payload) VALUES(?,999999,'binding:mandate-input',?,'{"evidence_kind":"artifact","immutable_subject_ref":"spec:one"}'); DELETE FROM write_guard`, workID, mustActorRef(t, f.owner)); err != nil {
						t.Fatal(err)
					}
				}
				tx, err := f.store.db.BeginTx(context.Background(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				plan, err := readWorkflowPlanTx(t.Context(), tx, workID)
				if err != nil || plan == nil {
					t.Fatalf("read plan=%v err=%v", plan, err)
				}
				for _, actionID := range []string{"record_review", "complete_step", "record_verdict", "record_delivery"} {
					action, err := catalogAction(actionID)
					if err != nil {
						t.Fatal(err)
					}
					if !workflowPlanAllowsAction(plan, "review", actionID) {
						t.Fatalf("%s is not declared on the review frontier", actionID)
					}
					err = guardPlanMandate(t.Context(), tx, workID, plan, "review", action)
					missingInput := len(laws) > 1 && !inputBound
					if actionID == "record_review" && !missingInput {
						if err != nil {
							t.Fatalf("local Review refused added law: %v", err)
						}
						continue
					}
					failure := planAdmissionFailure(t, err, KindMissingEvidence)
					if actionID == "record_review" && !strings.Contains(failure.Detail, "spec:one") {
						t.Fatalf("local Review masked its missing input: %v", failure)
					}
					if actionID != "record_review" && !strings.Contains(failure.RecoveryAction, "bind_evidence") {
						t.Fatalf("%s has no binding recovery: %v", actionID, failure)
					}
				}
			})
		}
	}
}
