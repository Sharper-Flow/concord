package store

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func seedLateCriterionAssociation(t *testing.T, workID string) (workflowReturnRouteFixture, string) {
	t.Helper()
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	return seedCriterionAssociationAtStep(t, workID, definition, "complete")
}

func seedCriterionAssociationAtStep(t *testing.T, workID string, definition RegisteredDefinition, step string) (workflowReturnRouteFixture, string) {
	t.Helper()
	var predicates []map[string]any
	for ordinal, id := range []string{"predicate:first", "predicate:second"} {
		predicates = append(predicates, map[string]any{"predicate_id": id, "ordinal": ordinal, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}})
	}
	route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, step, []string{}, []string{"artifact"}, predicates...)
	t.Cleanup(func() { _ = route.store.Close() })
	f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
	pack := seedResearchContextPack(t, f, "late-association-pack")
	bindResearchPack(t, f, pack.PackID, 1, true)
	selection := researchSelectionEntry(pack.PackID, 1, "f2", "root")
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

func TestCriterionAssociationAtPremiseQuestionWithoutDeclaration(t *testing.T) {
	const workID = "premise-late-association"
	// The live heads declare association beside confirmation. A local current
	// definition isolates the supported off-step premise-question shape without
	// retaining a historical definition in the built-in registry.
	d := cloneWorkflowDefinition(mustBuiltinDefinition(t, "workflow.research").Definition)
	for i := range d.StepGraph.Steps {
		switch d.StepGraph.Steps[i].ID {
		case "conclude":
			d.StepGraph.Steps[i].Actions = slices.DeleteFunc(d.StepGraph.Steps[i].Actions, func(id string) bool { return id == "record_verdict" })
		case "findings":
			d.StepGraph.Steps[i].Actions = append(d.StepGraph.Steps[i].Actions, "record_verdict")
		}
	}
	d.RecoveryRoutes = append(d.RecoveryRoutes, WorkflowRecoveryRoute{Step: "findings", Trigger: WorkflowRecoveryTriggerUnhealthyVerdict, Action: "request_correction", Target: "investigate"})
	registry := NewWorkflowDefinitionRegistry()
	_, err := registry.Register(d)
	if err != nil {
		t.Fatal(err)
	}
	f, ref := seedCriterionAssociationAtStep(t, workID, mustBuiltinDefinition(t, "workflow.research"), "conclude")
	seedComparisonObservation(t, f.store, workID)
	preflight := func(action string, payload json.RawMessage) error {
		request := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), ActionID: action, Payload: payload, Actor: f.owner}
		if action == "confirm_premise" {
			request.SelectedChoice = "confirm"
		}
		return WorkflowActionPreflightWithRegistry(t.Context(), f.store, registry, request)
	}
	if err := preflight("confirm_premise", json.RawMessage(`{}`)); !hasFailureKind(err, KindMissingEvidence) {
		t.Fatalf("unproved confirmation=%v", err)
	}
	payload := criterionAssociationPayload(ref, "predicate:first", "predicate:second")
	if err := preflight("record_verdict", payload); err != nil {
		t.Fatal(err)
	}
	if err := runApprovalActionTx(t, f.store, registry, workID, "record_verdict", f.owner, nil, "", "", string(payload)); err != nil {
		t.Fatal(err)
	}
	if step := currentStep(t, f.store, workID); step != "conclude" {
		t.Fatalf("association moved premise-question step to %s", step)
	}
	if err := preflight("confirm_premise", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := runApprovalActionTx(t, f.store, registry, workID, "confirm_premise", f.owner, nil, "confirm", "", `{}`); err != nil {
		t.Fatal(err)
	}
	if step := currentStep(t, f.store, workID); step != "complete" {
		t.Fatalf("confirmation held at %s", step)
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
		if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains,definition_ref,definition_version,definition_digest)
			SELECT work_id,?,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains,definition_ref,definition_version,definition_digest FROM workflow_contracts WHERE work_id=? AND contract_version=1`, tc.version, workID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE workflow_contracts SET superseded_by=? WHERE work_id=? AND contract_version<?`, tc.version, workID, tc.version); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) SELECT work_id,?,predicate_id,ordinal,outcome_kind,CASE WHEN predicate_id='predicate:first' THEN ? ELSE outcome_payload END FROM workflow_contract_predicates WHERE work_id=? AND contract_version=1`, tc.version, tc.payload, workID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
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
		if issue1013HasIntent(pin, "record_verdict") != (tc.count < 2) {
			t.Fatalf("contract %d: late association intent disagrees with criterion applicability", tc.version)
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
		"step_id": "complete", "action_id": "record_verdict", "attempt_epoch": 1, "actor_ref": actor,
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
