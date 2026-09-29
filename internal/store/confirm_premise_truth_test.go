package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// seedConfirmPremiseQuestion puts a break-fix instance at its verify step
// with an approved contract and a resolvable investigation artifact, so the
// confirm_premise question is open and the pin advertises the action.
func seedConfirmPremiseQuestion(t *testing.T, s *Store, workID, otherWorkID string) {
	t.Helper()
	seedWork(t, s, workID)
	seedWork(t, s, otherWorkID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	seedWithheldQuestionWorkflow(t, s, workID, "verify")
	insertInvestigationGateObservation(t, s, workID, "obs:"+strings.Repeat("7", 16), []string{"root", otherWorkID})
}

// TestWorkPinPublishesConfirmPremiseRequiredFields holds the pin side of the
// single declaration: a pending confirm_premise lists exactly the fields the
// generated envelope requires at the outer level, in declaration order, so a
// caller that prepares its call from the pin can no longer omit a field the
// validator demands.
func TestWorkPinPublishesConfirmPremiseRequiredFields(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID, otherWorkID := "confirm-pin-truth", "confirm-pin-truth-other"
	seedConfirmPremiseQuestion(t, s, workID, otherWorkID)
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("pin read failed: %v", err)
	}
	if pin.PendingOperatorDecision == nil || pin.PendingOperatorDecision.ActionID != "confirm_premise" {
		t.Fatalf("pending decision = %+v, want an open confirm_premise question", pin.PendingOperatorDecision)
	}
	var confirm *WorkPinIntent
	for i := range pin.NextValidIntents {
		if pin.NextValidIntents[i].ActionID == "confirm_premise" {
			confirm = &pin.NextValidIntents[i]
			break
		}
	}
	if confirm == nil {
		t.Fatalf("pin intents %v omit confirm_premise while its question is open", pin.NextValidIntents)
	}
	want := []string{"selected_choice", "decision_context_digest"}
	if len(confirm.RequiredFields) != len(want) {
		t.Fatalf("confirm_premise required_fields = %v, want %v", confirm.RequiredFields, want)
	}
	for i := range want {
		if confirm.RequiredFields[i] != want[i] {
			t.Fatalf("confirm_premise required_fields = %v, want %v", confirm.RequiredFields, want)
		}
	}
}

// TestConfirmPremiseDigestMismatchRefuses holds the checkpoint the digest
// protects: a selection whose decision_context_digest does not match the open
// question refuses as stale, while the question's own digest passes the
// semantic guard. Choice and digest validation are unchanged by the
// declaration; the pin merely states them.
func TestConfirmPremiseDigestMismatchRefuses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID, otherWorkID := "confirm-digest-mismatch", "confirm-digest-mismatch-other"
	seedConfirmPremiseQuestion(t, s, workID, otherWorkID)
	question, err := ReadWorkflowOperatorQuestion(ctx, s, workID)
	if err != nil {
		t.Fatalf("question read failed: %v", err)
	}
	if question == nil || question.ActionID != "confirm_premise" {
		t.Fatalf("question = %+v, want confirm_premise", question)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	run := func(choice, digest string) error {
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return validateWorkflowOperatorSelectionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: workID, ActionID: "confirm_premise", ExpectedVersion: version,
			SelectedChoice: choice, DecisionContextDigest: digest,
		})
	}
	if err := run("confirm", question.DecisionContextDigest); err != nil {
		t.Fatalf("the question's own digest must pass the guard: %v", err)
	}
	stale := run("confirm", "sha256:"+strings.Repeat("b", 64))
	var failure *Failure
	if !errors.As(stale, &failure) {
		t.Fatalf("a mismatched digest must refuse, got %v", stale)
	}
	if failure.Kind != KindStaleRequiresReview || !strings.Contains(failure.Detail, "stale or forged") {
		t.Fatalf("mismatch refusal = %v, want stale-requires-review naming the digest", stale)
	}
}

// TestWithheldReasonNamesObservationRefFormat holds the discoverability side
// of the premise gate: the withheld reason, which the work pin carries
// verbatim, names the ref format the gate enforces, so a refused caller can
// repair in one read instead of reading the store source.
func TestWithheldReasonNamesObservationRefFormat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID, otherWorkID := "confirm-refusal-format", "confirm-refusal-format-other"
	seedWork(t, s, workID)
	seedWork(t, s, otherWorkID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	seedWithheldQuestionWorkflow(t, s, workID, "verify")
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	validate := func() error {
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return validateWorkflowOperatorSelectionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
			WorkID: workID, ActionID: "confirm_premise", ExpectedVersion: version,
			SelectedChoice: "confirm", DecisionContextDigest: "sha256:" + strings.Repeat("a", 64),
		})
	}
	err := validate()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("confirm_premise without an investigation artifact must refuse, got %v", err)
	}
	for _, want := range []string{"bare domain_id", "another work item"} {
		if !strings.Contains(failure.Detail, want) || !strings.Contains(failure.RecoveryAction, want) {
			t.Fatalf("refusal detail %q / recovery %q must name the ref format %q", failure.Detail, failure.RecoveryAction, want)
		}
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("pin read failed: %v", err)
	}
	if pin.WithheldOperatorDecision == nil {
		t.Fatal("pin carries no withheld decision while the artifact is missing")
	}
	if pin.WithheldOperatorDecision.Reason != failure.Detail {
		t.Fatalf("pin reason %q differs from the gate's %q", pin.WithheldOperatorDecision.Reason, failure.Detail)
	}
	if !strings.Contains(pin.WithheldOperatorDecision.Remedy, "bare domain_id") {
		t.Fatalf("pin remedy %q must name the ref format", pin.WithheldOperatorDecision.Remedy)
	}
}

// TestPreflightValidatesEnvelopeCarriedConfirmPremiseFields holds the
// validator side of the projection: the preflight validates the
// envelope-carried values against the declared types, the fields object keeps
// carrying the declared non-envelope fields, and a declaration value absent
// from the envelope stays with the operator-selection guard.
func TestPreflightValidatesEnvelopeCarriedConfirmPremiseFields(t *testing.T) {
	t.Parallel()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	var payload WorkflowPayloadDefinition
	for _, action := range registered.Definition.ActionDefinitions {
		if action.ID == "confirm_premise" {
			payload = action.Payload
			break
		}
	}
	if len(payload.Fields) == 0 {
		t.Fatal("the current confirm_premise declaration declares no fields")
	}
	definition := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{{
		ID:      "confirm_premise",
		Payload: payload,
	}}}
	digest := "sha256:" + strings.Repeat("a", 64)
	cases := []struct {
		name        string
		payload     string
		choice      string
		digestValue string
		wantRefusal string
	}{
		{name: "envelope values with the declared optional field validate", payload: `{"contract_version":1}`, choice: "confirm", digestValue: digest},
		{name: "an empty fields object validates", payload: `{}`, choice: "confirm", digestValue: digest},
		{name: "an unknown choice refuses", payload: `{}`, choice: "restart", digestValue: digest, wantRefusal: `"selected_choice"`},
		{name: "a malformed digest refuses", payload: `{}`, choice: "confirm", digestValue: "not-a-digest", wantRefusal: `"decision_context_digest"`},
		{name: "the envelope value outranks a fields-object copy", payload: `{"selected_choice":"stop"}`, choice: "confirm", digestValue: digest},
		{name: "absent envelope values stay with the selection guard", payload: `{}`, choice: "", digestValue: ""},
	}
	for _, testCase := range cases {
		err := validateWorkflowActionEnvelopePayload(definition, WorkflowActionPreflightRequest{
			ActionID: "confirm_premise", Payload: json.RawMessage(testCase.payload),
			SelectedChoice: testCase.choice, DecisionContextDigest: testCase.digestValue,
		})
		if testCase.wantRefusal == "" {
			if err != nil {
				t.Fatalf("%s: %v", testCase.name, err)
			}
			continue
		}
		var failure *Failure
		if !errors.As(err, &failure) {
			t.Fatalf("%s: must refuse, got %v", testCase.name, err)
		}
		if !strings.Contains(failure.Detail, testCase.wantRefusal) {
			t.Fatalf("%s: refusal %q must name %q", testCase.name, failure.Detail, testCase.wantRefusal)
		}
	}
}
