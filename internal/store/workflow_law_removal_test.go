package store

import (
	"testing"
	"time"
)

func removeLawChangeLaw(t *testing.T, s *Store) {
	t.Helper()
	// A law retires by deleting its committed file: folder membership is the
	// acceptance authority, so the deletion is the removal.
	removeWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), "law-remove")
}

func TestLawRemovalThroughCurrentSuccessor(t *testing.T) {
	const id = "law-removal-successor"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove", "derived", "agent-surface")
	attributionItem(t, s, id, []string{"law-remove"}, []string{})
	removeLawChangeLaw(t, s)
	entry := mustBuiltinDefinition(t, "workflow.implementation")
	fields := lawChangeConstructorFields(t, s, 2, []string{}, []string{})
	fields[workflowLawChangeReasonField] = mustJSONValue("remove the obsolete law")
	fields[workflowLawChangeRemovalsField] = mustJSONValue([]string{"law-remove"})
	change, binding := constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, ownerRefForTest(t, lawChangeConstructorActor), fields)
	if change == nil || len(change.LawIDs) != 1 || change.LawBefore[0].Absent {
		t.Fatalf("successor lost the deletion's owning-start scope: %+v", change)
	}
	if err := enactLawChange(t, s, id, change, binding); err != nil {
		t.Fatalf("declared removal cannot enact after current-law recovery: %v", err)
	}
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, id)); err != nil {
		t.Fatalf("declared removal cannot complete: %v", err)
	}
}

func TestLawRemovalClassifiedEnactment(t *testing.T) {
	const id = "law-removal-classified"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-remove", "legislated", "agent-surface")
	startLawChange(t, s, id)
	change, err := deriveLawChange(t, s, id, lawChangeV1, lawChangeFields(t, map[string]any{
		workflowLawChangeReasonField: "remove the obsolete law", workflowLawChangeRemovalsField: []string{"law-remove"},
	}), []string{"law-remove"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowLawChangeShape(change, []string{"law-remove"}, nil); err != nil {
		t.Fatal(err)
	}
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindOutcomeMismatch, "declared removal still present")
	removeLawChangeLaw(t, s)
	if err := enactLawChange(t, s, id, change, nil); err != nil {
		t.Fatalf("classified removal cannot enact: %v", err)
	}
}

func TestLawRemovalDeclarationSafeguards(t *testing.T) {
	const id = "law-removal-safeguards"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-remove", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-retain", "derived", "agent-surface")
	startLawChange(t, s, id)
	for name, removals := range map[string]any{
		"undeclared": []string{"law-other"}, "duplicate": []string{"law-remove", "law-remove"},
		"null": nil, "not a list": "law-remove",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := deriveLawChange(t, s, id, lawChangeV1, lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "remove", workflowLawChangeRemovalsField: removals}), []string{"law-remove"}, nil)
			requireFailureKind(t, err, KindInvalidPayload, name)
		})
	}
	fields := lawChangeFields(t, map[string]any{workflowLawChangeReasonField: "remove one, revise one", workflowLawChangeRemovalsField: []string{"law-remove"}})
	change, err := deriveLawChange(t, s, id, lawChangeV1, fields, []string{"law-remove", "law-retain"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	removeLawChangeLaw(t, s)
	if err := enactLawChange(t, s, id, change, nil); err != nil {
		t.Fatal(err)
	}
	editLawChangeLaw(t, s, "law-retain")
	if err := enactLawChange(t, s, id, change, nil); err != nil {
		t.Fatal(err)
	}
	// A retained revision must still carry the derived tier: commit the
	// legislated revision and the enactment refuses, then the derived
	// revision again and it passes.
	seedLawChangeLaw(t, s, "law-retain", "legislated", "agent-surface")
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindMissingEvidence, "a removal does not exempt a retained revision from the derived tier")
	seedLawChangeLaw(t, s, "law-retain", "derived", "agent-surface")
	if err := enactLawChange(t, s, id, change, nil); err != nil {
		t.Fatal(err)
	}
	change.Removals = nil
	requireFailureKind(t, enactLawChange(t, s, id, change, nil), KindMissingEvidence, "agent cannot silently turn a text revision into removal")
	change.Removals = []string{"law-other"}
	requireFailureKind(t, validateWorkflowLawChangeShape(change, change.LawIDs, nil), KindInvalidPayload, "recorded removal outside scope")
	change.Removals = []string{"law-remove"}
	change.LawBefore[0].Absent = true
	change.LawBefore[0].ContentHash = ""
	change.LawBefore[0].Tier = ""
	change.LawBefore[0].DomainIDs = []string{}
	requireFailureKind(t, validateWorkflowLawChangeShape(change, change.LawIDs, nil), KindInvalidPayload, "a new law cannot be declared removed")
}

func TestLawRemovalClassifiedCurrentSuccessor(t *testing.T) {
	const id = "law-classified-successor"
	s := seedLawChangeFixture(t, id)
	seedLawChangeLaw(t, s, "law-remove", "legislated", "agent-surface")
	entry := mustBuiltinDefinition(t, "workflow.implementation")
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: id, Definition: entry, Actor: lawChangeConstructorActor})
	}); err != nil {
		t.Fatal(err)
	}
	owner := ownerRefForTest(t, lawChangeConstructorActor)
	for version := 1; version <= 2; version++ {
		laws := []string{"law-remove"}
		if version == 2 {
			removeLawChangeLaw(t, s)
			laws = []string{}
		}
		fields := lawChangeConstructorFields(t, s, version, laws, []string{})
		fields[workflowLawChangeReasonField] = mustJSONValue("remove the obsolete law")
		fields[workflowLawChangeRemovalsField] = mustJSONValue([]string{"law-remove"})
		fields["decision_domains"] = mustJSONValue([]string{"product"})
		change, binding := constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, owner, fields)
		if version == 2 {
			if err := enactLawChange(t, s, id, change, binding); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := advanceWorkflowTestInstanceToStep(t.Context(), s, id, "execution", owner); err != nil {
		t.Fatal(err)
	}
	event := attributionCompletionWithConfirmation(t, s, id, nil, func(f acceptanceRecoveryFixture) error {
		v := readWorkVersion(t, s, id)
		tx := lawChangeTx(t, s)
		request := WorkflowActionExecutionRequest{WorkID: id, Actor: lawChangeConstructorActor, Now: time.Unix(20, 0).UTC()}
		events, err := workflowConfirmPremiseEventsForDefinition(t.Context(), tx, entry.Definition, request, owner, nil, "law-removal-confirm", v)
		_ = tx.Rollback()
		if err != nil {
			return err
		}
		return applyWorkflowTestOperation(t.Context(), s, Operation{Events: events, ExpectedVersions: workVersion(id, v)})
	})
	if err := CompleteWorkflow(t.Context(), s, event); err != nil {
		t.Fatalf("classified removal cannot complete after current-law recovery: %v", err)
	}
}
