package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// seedActiveBreakFixContract gives a seeded break-fix instance an active
// contract, whose confirmation folds the coordinator authority.
func seedActiveBreakFixContract(t *testing.T, s *Store, workID string) {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := WorkflowDefinitionDigest(registered.Definition)
	if err != nil {
		t.Fatal(err)
	}
	actorRef := DeriveWorkflowActorRef("principal/withheld", "client/withheld", "agent/withheld", "session/withheld")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains,definition_ref,definition_digest) VALUES(?,1,'active contract premise','internal_sqlite','[]','[]','2026-08-01T00:00:00Z',?,'[]','[]',1,'prototype_internal','[]',?,?);
		DELETE FROM write_guard`, workID, actorRef, registered.Definition.Ref, digest); err != nil {
		t.Fatal(err)
	}
}

// TestWorkPinEngineeringConfirmPremiseOmitsDigest holds the pin side of the
// coordinator confirmation: an active contract opens no operator question,
// the pin hides confirm_premise while the confirmation's verdict deliverable
// is missing, and the advertised intent requires the closed choice alone,
// because the confirmation refuses a human digest.
func TestWorkPinEngineeringConfirmPremiseOmitsDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "confirm-pin-engineering"
	f, _, refs := pReceiptFixture(t, workID, 1, 1)
	s := f.store
	seedComparisonObservation(t, s, workID)
	missingPin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("pin read without the verdict failed: %v", err)
	}
	for _, intent := range missingPin.NextValidIntents {
		if intent.ActionID == "confirm_premise" {
			t.Fatalf("pin advertises confirm_premise while the confirmation refuses its missing verdict: %v", missingPin.NextValidIntents)
		}
	}
	if missingPin.PendingOperatorDecision != nil || missingPin.WithheldOperatorDecision != nil {
		t.Fatalf("active-contract pin published question=%+v withheld=%+v, want none", missingPin.PendingOperatorDecision, missingPin.WithheldOperatorDecision)
	}
	if err := runVerdictActionAs(t, s, workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
		t.Fatal(err)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("pin read failed: %v", err)
	}
	if pin.PendingOperatorDecision != nil {
		t.Fatalf("engineering pin opened operator question %+v", pin.PendingOperatorDecision)
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "confirm_premise" {
			if len(intent.RequiredFields) != 1 || intent.RequiredFields[0] != "selected_choice" {
				t.Fatalf("engineering confirm_premise required_fields = %v, want [selected_choice]", intent.RequiredFields)
			}
			return
		}
	}
	t.Fatalf("engineering pin intents %v omit confirm_premise", pin.NextValidIntents)
}

// TestWithheldReasonNamesObservationRefFormat holds the discoverability side
// of the premise gate: the confirmation refusal names the observation ref
// format the gate enforces, so a refused caller can repair in one read
// instead of reading the store source.
func TestWithheldReasonNamesObservationRefFormat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	workID, otherWorkID := "confirm-refusal-format", "confirm-refusal-format-other"
	seedWork(t, s, workID)
	seedWork(t, s, otherWorkID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	seedWithheldQuestionWorkflow(t, s, workID)
	seedActiveBreakFixContract(t, s, workID)
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
			SelectedChoice: "confirm",
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
