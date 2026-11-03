package store

import "testing"

// Late criterion association and ordinary replacement agree across discovery,
// the work pin, preflight and mutation.
func TestIssue1013LateVerdictRecoveryPassesReadOnlyPreflight(t *testing.T) {
	t.Parallel()
	const workID = "issue1013-late-verdict-preflight"
	fixture, ref := seedLateCriterionAssociation(t, workID)
	s, owner := fixture.store, fixture.owner
	for _, predicate := range []string{"predicate:first", "predicate:second"} {
		payload := criterionAssociationPayload(ref, predicate)
		if !issue1013HasIntent(issue1013Pin(t, s, workID), "record_verdict") {
			t.Fatal("missing criterion proof has no late-association intent")
		}
		_, action, err := WorkflowActionDefinitionFor(t.Context(), s, BuiltinWorkflowRegistry(), workID, "record_verdict")
		if err != nil || action.ExecutionMode != ActionHold {
			t.Fatalf("late association discovery=%+v err=%v", action, err)
		}
		before := contractCorrectionEffectSnapshot(t, s, workID)
		if err := issue1013Preflight(t, s, workID, "record_verdict", payload, owner); err != nil {
			t.Fatalf("late association preflight: %v", err)
		}
		if after := contractCorrectionEffectSnapshot(t, s, workID); after != before {
			t.Fatal("read-only late association preflight changed state")
		}
		if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner); err != nil {
			t.Fatalf("late association: %v", err)
		}
		if got := currentStep(t, s, workID); got != "complete" {
			t.Fatalf("late association moved to %s", got)
		}
	}
	payload := criterionAssociationPayload(ref, "predicate:first")
	var currentRecord bool
	for _, intent := range issue1013Pin(t, s, workID).NextValidIntents {
		if intent.ActionID == "record_verdict" && intent.ReasonCode == "current_record" {
			currentRecord = true
		}
	}
	if !currentRecord {
		t.Fatal("completed verdict record is not advertised for replacement")
	}
	for _, err := range []error{issue1013Preflight(t, s, workID, "record_verdict", payload, owner), runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, owner)} {
		if err != nil {
			t.Fatalf("verdict replacement=%v", err)
		}
	}
}
