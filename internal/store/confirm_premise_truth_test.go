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
	actorRef := DeriveWorkflowActorRef("principal/withheld", "client/withheld", "agent/withheld", "session/withheld")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,decision_domains) VALUES(?,1,'active contract premise','internal_sqlite','[]','2026-08-01T00:00:00Z',?,'[]','[]',1,'prototype_internal','[]');
		DELETE FROM write_guard`, workID, actorRef); err != nil {
		t.Fatal(err)
	}
}

// TestWorkPinEngineeringConfirmPremiseOmitsDigest holds the pin side of the
// coordinator confirmation: the advertised intent requires no fields.
func TestWorkPinEngineeringConfirmPremiseOmitsDigest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "confirm-pin-engineering"
	f := rAcceptance(t, workID, nil, false)
	s := f.store
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("pin read failed: %v", err)
	}
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "confirm_premise" {
			if len(intent.RequiredFields) != 0 {
				t.Fatalf("confirm_premise required_fields = %v, want none", intent.RequiredFields)
			}
			return
		}
	}
	t.Fatalf("engineering pin intents %v omit confirm_premise", pin.NextValidIntents)
}

// TestConfirmPremisePayloadValidation checks the closed action payload.
func TestConfirmPremisePayloadValidation(t *testing.T) {
	t.Parallel()
	action, err := WorkflowPlanActionContract("confirm_premise")
	if err != nil {
		t.Fatal(err)
	}
	if len(action.Payload.Fields) == 0 {
		t.Fatal("the current confirm_premise declaration declares no fields")
	}
	cases := []struct {
		name        string
		payload     string
		wantRefusal string
	}{
		{name: "optional contract version validates", payload: `{"contract_version":1}`},
		{name: "an empty fields object validates", payload: `{}`},
		{name: "choice is not a field", payload: `{"selected_choice":"confirm"}`, wantRefusal: `"selected_choice"`},
		{name: "digest is not a field", payload: `{"decision_context_digest":"sha256:bad"}`, wantRefusal: `"decision_context_digest"`},
	}
	for _, testCase := range cases {
		_, err := validateWorkflowPlanActionPayload("confirm_premise", json.RawMessage(testCase.payload))
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
