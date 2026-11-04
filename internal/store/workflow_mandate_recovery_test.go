package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func planMandateRecoveryFixture(t *testing.T, workID string, added bool) workflowReturnRouteFixture {
	t.Helper()
	f := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanChange, "review", []string{}, nil)
	setAdmissionMandates(t, f.store, workID, []string{"CD-0189"})
	if added {
		// The isolated reservation uses the fixture contract's architecture binding.
		if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product','CD-0189',?,1,'root');
INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES(?,1,'product','CD-0189','root',?,1);
DELETE FROM write_guard`, workID, workID, workID); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func planMandateVerdictAdmission(t *testing.T, f workflowReturnRouteFixture, workID string) error {
	t.Helper()
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	payload := json.RawMessage(`{"verdicts":[{"predicate_id":"predicate:return-route","receipt_refs":["evidence:example"]}]}`)
	_, _, _, err = workflowPlanActionAdmissionTx(t.Context(), tx, workID, readWorkVersionTxForMandate(t, tx, workID), "record_verdict", payload)
	return err
}

func readWorkVersionTxForMandate(t *testing.T, tx queryer, workID string) int64 {
	t.Helper()
	var version int64
	if err := tx.QueryRowContext(t.Context(), `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func TestWorkflowPlanMandateRecoveryAdditionPublication(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "unseen", true: "published"}[published], func(t *testing.T) {
			workID := "plan-mandate-addition-unseen"
			if published {
				workID = "plan-mandate-addition-published"
			}
			f := planMandateRecoveryFixture(t, workID, true)
			if published {
				home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, f.store), HeadRef: "HEAD"}
				commitWorkflowFixtureLaw(t, home, "CD-0189", fixtureLawFolderLaw("CD-0189"))
			}
			failure := planAdmissionFailure(t, planMandateVerdictAdmission(t, f, workID), KindMissingEvidence)
			if !strings.Contains(failure.Detail, "one of the contract's own law additions") || strings.Contains(failure.Detail, "publishes") || strings.Contains(failure.Detail, "projection") ||
				!strings.Contains(failure.RecoveryAction, `bind_evidence on a frontier step before record_verdict only when the branch adds "CD-0189"`) ||
				!strings.Contains(failure.RecoveryAction, "otherwise use revise_intent with the complete contract") {
				t.Fatalf("added-law refusal=%+v", failure)
			}
		})
	}
}

func TestWorkflowPlanMandateRecoveryPropagatesQueryFailure(t *testing.T) {
	const workID = "plan-mandate-query-failure"
	f := planMandateRecoveryFixture(t, workID, true)
	if _, err := f.store.db.Exec(`DROP TABLE workflow_contract_law_additions`); err != nil {
		t.Fatal(err)
	}
	failure := planAdmissionFailure(t, planMandateVerdictAdmission(t, f, workID), KindUnavailable)
	if strings.Contains(failure.RecoveryAction, "bind_evidence") {
		t.Fatalf("unavailable query was treated as a binding instruction: %+v", failure)
	}
}
