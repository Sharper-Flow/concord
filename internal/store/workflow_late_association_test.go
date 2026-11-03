package store

import (
	"encoding/json"
	"testing"
	"time"
)

func seedLateCriterionAssociation(t *testing.T, workID string) (workflowReturnRouteFixture, string) {
	t.Helper()
	return seedCriterionAssociationAtStep(t, workID, WorkflowStepPlanResearch, "conclude")
}

func seedCriterionAssociationAtStep(t *testing.T, workID string, planType WorkflowStepPlanType, step string) (workflowReturnRouteFixture, string) {
	t.Helper()
	var predicates []map[string]any
	for ordinal, id := range []string{"predicate:first", "predicate:second"} {
		predicates = append(predicates, map[string]any{"predicate_id": id, "ordinal": ordinal, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}})
	}
	route := seedWorkflowReturnRouteFixtureWithPlan(t, workID, planType, step, []string{}, []string{"artifact"}, predicates...)
	t.Cleanup(func() { _ = route.store.Close() })
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	pack := seedResearchContextPack(t, f, "late-association-pack")
	bindResearchPack(t, f, pack.PackID, 1, true)
	// The fixture registry's Product root is "product-root:product"; the
	// Product-wide rationale is valid only on that root Domain.
	selection := researchSelectionEntry(pack.PackID, 1, "f2", "product-root:product")
	selection["product_wide_rationale"] = "The selected finding supports the Product-wide research criteria."
	if err := f.recordWorkContext(t, map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil {
		t.Fatal(err)
	}
	view, err := readWorkContextView(t.Context(), f.store.db, workID)
	if err != nil || view == nil || len(view.ResearchFindings) != 1 {
		t.Fatalf("selected research proof: %+v err=%v", view, err)
	}
	return route, view.ResearchFindings[0].ViewID
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
	f, ref := seedCriterionAssociationAtStep(t, workID, WorkflowStepPlanResearch, "conclude")
	preflight := func(action string, payload json.RawMessage) error {
		request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), ActionID: action, Payload: payload, Actor: f.owner}
		_, err := WorkflowActionDefinitionFor(t.Context(), f.store, request)
		return err
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
}

func TestCriterionAssociationSurvivesUnchangedContractDigest(t *testing.T) {
	const workID = "criterion-digest-survival"
	f, ref := seedLateCriterionAssociation(t, workID)
	if err := runVerdictActionAs(t, f.store, workID, "record_verdict", criterionAssociationPayload(ref, "predicate:first", "predicate:second"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	// Contract projection changes isolate the association reader from the
	// unrelated contract-recovery admission. No association row is rewritten.
	for _, tc := range []struct {
		version int
		payload string
		count   int
	}{{2, `{"allowed":["report_recorded"],"kind":"outcome"}`, 2}, {3, `{"kind":"outcome","allowed":["resolved"]}`, 1}, {4, `{"kind":"outcome","allowed":["report_recorded"]}`, 2}} {
		tx, err := f.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback() })
		if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains)
			SELECT work_id,?,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains FROM workflow_contracts WHERE work_id=? AND contract_version=1`, tc.version, workID); err != nil {
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
			if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, workID, association); err != nil {
				t.Fatalf("unchanged criterion lost its original proof: %v", err)
			}
		}
		pin := issue1013Pin(t, f.store, workID)
		var currentRecord bool
		for _, intent := range pin.NextValidIntents {
			if intent.ActionID == "record_verdict" && intent.ReasonCode == "current_record" {
				currentRecord = true
			}
		}
		if !currentRecord {
			t.Fatalf("contract %d: current verdict record is not editable", tc.version)
		}
	}
}

func TestCriterionAssociationCompletionCannotBorrowPriorEffect(t *testing.T) {
	const workID = "late-association-completion"
	f, ref := seedLateCriterionAssociation(t, workID)
	if err := runVerdictActionAs(t, f.store, workID, "record_verdict", criterionAssociationPayload(ref, "predicate:first"), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, workID)
	event := workflowTypedEvent("unpaired-association-completion", WorkflowActionCompleted, workID, actor, time.Unix(15, 0), version, map[string]any{
		"step_id": "conclude", "action_id": "record_verdict", "attempt_epoch": 1, "actor_ref": actor,
		"result_evidence_refs": []string{ref}, "changed_refs": []string{workID},
	})
	before := contractCorrectionEffectSnapshot(t, f.store, workID)
	err = applyWorkflowTestOperationDirect(t.Context(), f.store, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
	if !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("unpaired completion=%v", err)
	}
	if after := contractCorrectionEffectSnapshot(t, f.store, workID); after != before {
		t.Fatal("unpaired completion changed state")
	}
}

func TestCriterionAssociationReplacementCompletionCannotBorrowPriorEffect(t *testing.T) {
	const workID = "association-replacement-completion"
	f, ref := seedLateCriterionAssociation(t, workID)
	payload := criterionAssociationPayload(ref, "predicate:first")
	for i := 0; i < 2; i++ {
		if err := runVerdictActionAs(t, f.store, workID, "record_verdict", payload, 0, f.owner); err != nil {
			t.Fatalf("verdict write %d: %v", i, err)
		}
	}
	actor, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, f.store, workID)
	event := workflowTypedEvent("unpaired-replacement-completion", WorkflowActionCompleted, workID, actor, time.Unix(15, 0), version, map[string]any{
		"step_id": "conclude", "action_id": "record_verdict", "attempt_epoch": 1, "actor_ref": actor,
		"result_evidence_refs": []string{ref}, "changed_refs": []string{workID},
	})
	before := contractCorrectionEffectSnapshot(t, f.store, workID)
	err = applyWorkflowTestOperationDirect(t.Context(), f.store, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
	if !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("unpaired replacement completion=%v", err)
	}
	if after := contractCorrectionEffectSnapshot(t, f.store, workID); after != before {
		t.Fatal("unpaired replacement completion changed state")
	}
}
