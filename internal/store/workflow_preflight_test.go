package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWorkflowActionPreflightNamesEveryMissingAndUndeclaredField(t *testing.T) {
	const workID = "preflight-closed-fields"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "plan")
	defer f.store.Close()
	payload := json.RawMessage(`{"zz_extra":true,"aa_extra":true}`)
	want := `workflow action payload field "outcomes" is required for action "record_plan"; workflow action payload field "approach" is required for action "record_plan"; workflow action payload field "decisions" is required for action "record_plan"; workflow action payload field "touches" is required for action "record_plan"; workflow action payload field "merge_split" is required for action "record_plan"; workflow action payload field "risks" is required for action "record_plan"; workflow action payload field "aa_extra" is not declared for action "record_plan"; workflow action payload field "zz_extra" is not declared for action "record_plan"`
	version := readWorkVersion(t, f.store, workID)
	var before int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, workID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		_, err := WorkflowActionDefinitionFor(t.Context(), f.store, WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, ActionID: "record_plan", Payload: payload, Actor: f.owner})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Detail != want || failure.RecoveryAction != "supply every required registered action field and use only fields declared by the pinned definition" {
			t.Fatalf("structural preflight=%v, want deterministic complete refusal", err)
		}
	}
	var after int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=?`, workID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if got := readWorkVersion(t, f.store, workID); got != version || after != before {
		t.Fatalf("preflight changed version %d -> %d or receipts %d -> %d", version, got, before, after)
	}
}

// The required-field scan must report the first declared missing field, in the
// catalog's declaration order. A map walk names whichever field the runtime
// yields first, so the same empty payload reports a different field per run
// and a caller cannot tell which field to correct first.
func TestMissingRequiredFieldNamesFirstDeclaredMissingField(t *testing.T) {
	// checkpoint_context declares eight required fields; active_unit is first.
	for i := 0; i < 100; i++ {
		_, failure := validateWorkflowPlanActionPayload("checkpoint_context", json.RawMessage(`{}`))
		if failure == nil {
			t.Fatal("REPRO: an empty checkpoint_context payload passed the required-field scan")
		}
		if !strings.Contains(failure.Error(), `"active_unit"`) {
			t.Fatalf("REPRO: iteration %d named a non-first declared field: %v", i, failure)
		}
	}

	// A payload that carries the first declared field must name the next one.
	_, failure := validateWorkflowPlanActionPayload("checkpoint_context", json.RawMessage(`{"active_unit":"catalog scan"}`))
	if failure == nil || !strings.Contains(failure.Error(), `"hypothesis"`) {
		t.Fatalf("partial payload did not name the next declared missing field: %v", failure)
	}
}
