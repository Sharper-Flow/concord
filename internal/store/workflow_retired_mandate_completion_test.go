package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func retiredMandateCompletion(t *testing.T, s *Store, id string, event Event) Event {
	t.Helper()
	v := readWorkVersion(t, s, id)
	owner := ownerRefForTest(t, lawChangeConstructorActor)
	event.EventID += "-retry"
	event.Payload = mustJSONValue(map[string]any{
		"work_id": id, "expected_version": v, "resulting_version": v + 1, "terminal_state": "completed", "final_verdict_kind": "ok", "verdict_actor_ref": owner,
		"premise_confirmed": true, "evidence_count": 1, "changed_refs_digest": lawChangeHashA, "impact_verdict": "non-breaking",
	})
	return event
}

func requireRetiredMandateStep(t *testing.T, s *Store, id, want string) {
	t.Helper()
	var step string
	if err := s.db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, id).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != want {
		t.Fatalf("step = %q, want %q", step, want)
	}
}

// The confirmed contract mandates a law and declares its removal; the reviewed
// deletion has merged. Completion accepts the declared absence.
func TestRetiredMandateDeclaredRemovalCompletes(t *testing.T) {
	const id = "retired-mandate-declared"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove", "derived", "agent-surface")
	attributionItem(t, s, id, []string{"law-remove"}, []string{}, func(fields map[string]json.RawMessage) {
		fields[workflowLawChangeRemovalsField] = mustJSONValue([]string{"law-remove"})
	})
	removeLawChangeLaw(t, s)
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, id)); err != nil {
		t.Fatalf("declared retirement of a mandated law cannot complete: %v", err)
	}
}

// A mandated law outside the declared removals still refuses when absent.
func TestRetiredMandateUndeclaredMandateStillRefuses(t *testing.T) {
	const id = "retired-mandate-other"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove", "derived", "agent-surface")
	seedLawChangeLaw(t, s, "law-retain", "derived", "agent-surface")
	attributionItem(t, s, id, []string{"law-remove", "law-retain"}, []string{}, func(fields map[string]json.RawMessage) {
		fields[workflowLawChangeRemovalsField] = mustJSONValue([]string{"law-remove"})
	})
	removeLawChangeLaw(t, s)
	removeWorkflowFixtureLaw(t, lawChangeFixtureHome(t, s), "law-retain")
	err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, id))
	requireFailureKind(t, err, KindProjectionNotFound, "an undeclared absent mandate")
	if !strings.Contains(err.Error(), "law-retain") || strings.Contains(err.Error(), "law-remove") {
		t.Fatalf("refusal names the wrong mandate: %v", err)
	}
}

// The confirmed contract mandates and modifies a law without declaring its
// removal; the reviewed deletion has merged. An ordinary update declares the
// retirement, the coordinator confirms the updated version at the terminal
// step without moving it, and the work completes.
func TestRetiredMandateUndeclaredRemovalRecoversAtRelease(t *testing.T) {
	const id = "retired-mandate-undeclared"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove", "derived", "agent-surface")
	attributionItem(t, s, id, []string{"law-remove"}, []string{})
	removeLawChangeLaw(t, s)
	var confirmed acceptanceRecoveryFixture
	event := attributionCompletionWithConfirmation(t, s, id, nil, func(f acceptanceRecoveryFixture) error {
		confirmed = f
		return f.run(t.Context(), "confirm_premise", "confirm", readWorkVersion(t, s, id), nil, nil)
	})
	requireFailureKind(t, CompleteWorkflow(t.Context(), s, event), KindProjectionNotFound, "an undeclared retirement")
	// Before any update the active version is confirmed: no late route opens.
	requireFailureKind(t, confirmed.run(t.Context(), "confirm_premise", "confirm-early", readWorkVersion(t, s, id), nil, nil), KindIllegalLifecycleTransition, "late confirmation of a confirmed version")

	entry := mustBuiltinDefinition(t, "workflow.implementation")
	fields := lawChangeConstructorFields(t, s, 2, []string{}, []string{})
	fields[workflowLawChangeReasonField] = mustJSONValue("retire the obsolete law")
	fields[workflowLawChangeRemovalsField] = mustJSONValue([]string{"law-remove"})
	constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, ownerRefForTest(t, lawChangeConstructorActor), fields)
	requireRetiredMandateStep(t, s, id, "release")

	pin, err := ReadWorkPin(t.Context(), s, id)
	if err != nil {
		t.Fatal(err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "confirm_premise") || workPinContainsAction(pin.NextValidIntents, "complete") {
		t.Fatalf("work pin intents = %+v, want late confirm_premise without complete", pin.NextValidIntents)
	}
	requireFailureKind(t, CompleteWorkflow(t.Context(), s, retiredMandateCompletion(t, s, id, event)), KindMissingEvidence, "an unconfirmed updated version")

	if err := confirmed.run(t.Context(), "confirm_premise", "confirm-late", readWorkVersion(t, s, id), nil, nil); err != nil {
		t.Fatalf("late confirmation of the updated version: %v", err)
	}
	requireRetiredMandateStep(t, s, id, "release")
	requireFailureKind(t, confirmed.run(t.Context(), "confirm_premise", "confirm-again", readWorkVersion(t, s, id), nil, nil), KindIllegalLifecycleTransition, "a second late confirmation")
	if err := CompleteWorkflow(t.Context(), s, retiredMandateCompletion(t, s, id, event)); err != nil {
		t.Fatalf("reconciled retirement cannot complete: %v", err)
	}
}

// An update that changes a criterion leaves no current proof for it: the late
// confirmation refuses instead of confirming unproved criteria.
func TestRetiredMandateLateConfirmationRequiresCurrentProof(t *testing.T) {
	const id = "retired-mandate-stale-proof"
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-remove", "derived", "agent-surface")
	attributionItem(t, s, id, []string{"law-remove"}, []string{})
	removeLawChangeLaw(t, s)
	var confirmed acceptanceRecoveryFixture
	attributionCompletionWithConfirmation(t, s, id, nil, func(f acceptanceRecoveryFixture) error {
		confirmed = f
		return f.run(t.Context(), "confirm_premise", "confirm", readWorkVersion(t, s, id), nil, nil)
	})
	entry := mustBuiltinDefinition(t, "workflow.implementation")
	fields := lawChangeConstructorFields(t, s, 2, []string{}, []string{})
	fields[workflowLawChangeReasonField] = mustJSONValue("retire the obsolete law")
	fields[workflowLawChangeRemovalsField] = mustJSONValue([]string{"law-remove"})
	fields["outcome_predicates"] = mustJSONValue([]map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:law-changed", "immutable_subject_ref": "commit:law", "expected_result": "pass"}}})
	constructLawChangeContract(t, s, entry.Definition, BuiltinWorkflowRegistry(), id, ownerRefForTest(t, lawChangeConstructorActor), fields)
	err := confirmed.run(t.Context(), "confirm_premise", "confirm-late", readWorkVersion(t, s, id), nil, nil)
	requireFailureKind(t, err, KindMissingEvidence, "late confirmation without current criterion proof")
	if !strings.Contains(err.Error(), "predicate:primary") {
		t.Fatalf("refusal does not name the unproved criterion: %v", err)
	}
	requireRetiredMandateStep(t, s, id, "release")
}
