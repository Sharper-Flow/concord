package store

import (
	"encoding/json"
	"testing"
)

func seedLateCriterionAssociation(t *testing.T, workID string) (workflowReturnRouteFixture, string) {
	t.Helper()
	return seedCriterionAssociationAtStep(t, workID, "conclude")
}

func seedCriterionAssociationAtStep(t *testing.T, workID, step string) (workflowReturnRouteFixture, string) {
	t.Helper()
	var predicates []map[string]any
	for ordinal, id := range []string{"predicate:first", "predicate:second"} {
		predicates = append(predicates, map[string]any{"predicate_id": id, "ordinal": ordinal, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}})
	}
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, WorkflowStepPlanResearch, "conclude", []string{}, []string{"artifact"}, predicates...)
	t.Cleanup(func() { _ = route.store.Close() })
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	proof := qAssociateCurrentProofAt(t, f, "conclude", "", false)
	if step != "conclude" {
		moveWorkflowPlanFixtureFrontier(t, route.store, workID, step)
	}
	return route, proof.receipt
}

func criterionAssociationPayload(ref string, predicates ...string) json.RawMessage {
	entries := make([]map[string]any, 0, len(predicates))
	for _, predicate := range predicates {
		entries = append(entries, map[string]any{"predicate_id": predicate, "receipt_refs": []string{ref}})
	}
	return mustJSONValue(map[string]any{"verdicts": entries})
}

func TestCriterionAssociationAtConclusionBeforeConfirmation(t *testing.T) {
	const workID = "premise-late-association"
	f, ref := seedCriterionAssociationAtStep(t, workID, "conclude")
	preflight := func(action string, payload json.RawMessage) error {
		request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), ActionID: action, Payload: payload, Actor: f.owner}
		_, err := WorkflowActionDefinitionFor(t.Context(), f.store, request)
		return err
	}
	before := outsideRepairDatabaseSnapshot(t, f.store)
	if err := runVerdictActionAs(t, f.store, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("unproved confirmation write=%v", err)
	}
	if outsideRepairDatabaseSnapshot(t, f.store) != before {
		t.Fatal("unproved confirmation changed durable state")
	}
	if err := preflight("confirm_premise", json.RawMessage(`{}`)); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("unproved confirmation=%v", err)
	}
	payload := criterionAssociationPayload(ref, "predicate:first", "predicate:second")
	if err := preflight("record_verdict", payload); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, f.store, workID, "record_verdict", payload, 0, f.owner); err != nil {
		t.Fatal(err)
	}
	if step := workflowPlanFixtureFrontier(t, f.store, workID); step != "conclude" {
		t.Fatalf("association moved premise-question step to %s", step)
	}
	if err := preflight("confirm_premise", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, f.store, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	if step := workflowPlanFixtureFrontier(t, f.store, workID); step != "conclude" {
		t.Fatalf("confirmation moved explicit plan frontier to %s", step)
	}
	state, err := loadWorkflowReviewState(t.Context(), f.store.db, workID)
	if err != nil || state.Accepted == nil || state.Candidate == nil {
		t.Fatalf("accepted review=%+v err=%v", state.Accepted, err)
	}
	completion := mustJSONValue(map[string]any{"step_id": "conclude", "note": "research reviewed", "reviewer": "concord-review", "result": "pass", "commit": state.Candidate.Commit, "link": state.Accepted.ReportRef})
	if err := runVerdictActionAs(t, f.store, workID, "complete_step", completion, 0, f.owner); err != nil {
		t.Fatalf("complete reviewed conclusion: %v", err)
	}
	if err := runVerdictActionAs(t, f.store, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), 0, f.owner); err != nil {
		t.Fatalf("complete research: %v", err)
	}
	var lifecycle string
	if err := f.store.db.QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, workID).Scan(&lifecycle); err != nil || lifecycle != "completed" {
		t.Fatalf("research lifecycle=%q err=%v", lifecycle, err)
	}
}

func TestCriterionAssociationSurvivesUnchangedContractDigest(t *testing.T) {
	const workID = "criterion-digest-survival"
	f, ref := seedCriterionAssociationAtStep(t, workID, "conclude")
	if err := runVerdictActionAs(t, f.store, workID, "record_verdict", criterionAssociationPayload(ref, "predicate:first", "predicate:second"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version int
		payload string
		count   int
	}{{2, `{"allowed":["report_recorded"],"kind":"outcome"}`, 2}, {3, `{"kind":"outcome","allowed":["resolved"]}`, 1}, {4, `{"kind":"outcome","allowed":["report_recorded"]}`, 2}} {
		tx, err := f.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains) SELECT work_id,?,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains FROM workflow_contracts WHERE work_id=? AND contract_version=1`, tc.version, workID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE workflow_contracts SET superseded_by=? WHERE work_id=? AND contract_version<?`, tc.version, workID, tc.version); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) SELECT work_id,?,predicate_id,ordinal,outcome_kind,CASE WHEN predicate_id='predicate:first' THEN ? ELSE outcome_payload END FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1`, tc.version, tc.payload, workID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, workID, int64(tc.version))
		if err != nil || len(verdicts) != tc.count {
			t.Fatalf("contract %d: applicable associations=%d want %d: %v", tc.version, len(verdicts), tc.count, err)
		}
		for _, association := range verdicts {
			if association.ContractVersion != 1 {
				t.Fatal("association provenance was rewritten")
			}
		}
		err = runVerdictActionAs(t, f.store, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner)
		if tc.count == 2 && err != nil {
			t.Fatalf("contract %d: confirmation of unchanged proof: %v", tc.version, err)
		}
		if tc.count != 2 && !hasFailureKind(err, KindMissingEvidence) {
			t.Fatalf("contract %d: confirmation without current proof = %v", tc.version, err)
		}
	}
	if err := runVerdictActionAs(t, f.store, workID, "record_verdict", criterionAssociationPayload(ref, "predicate:first"), 0, f.owner); err != nil {
		t.Fatalf("record current criterion association: %v", err)
	}
	verdicts, err := latestWorkflowVerdicts(t.Context(), f.store.db, workID, 4)
	if err != nil || len(verdicts) != 2 {
		t.Fatalf("current associations=%v err=%v", verdicts, err)
	}
	var replaced bool
	for _, association := range verdicts {
		if association.PredicateID == "predicate:first" && association.ContractVersion == 4 && association.CriterionDigest != "" {
			replaced = true
		}
		if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, workID, association); err != nil {
			t.Fatalf("current association lost proof: %v", err)
		}
	}
	if !replaced {
		t.Fatal("record_verdict did not replace the current criterion association")
	}
	if err := runVerdictActionAs(t, f.store, workID, "confirm_premise", json.RawMessage(`{}`), 0, f.owner); err != nil {
		t.Fatalf("confirmation after current association: %v", err)
	}
}
