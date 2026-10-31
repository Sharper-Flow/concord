package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
)

// The closed read schema retains the failure disposition without claiming
// that a failed review is an accepted successful result.
func checkpointFailedReviewCorrectionContext() map[string]any {
	return map[string]any{
		"disposition":   "failed",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"},
		"diagnosis":       "the checkpoint has no accepted Review and the latest verification verdict is not healthy",
		"strategy":        "return to the repair step and obtain current Review",
		"source_event_id": "request_correction-work-1-9:semantic", "source_event_seq": 9,
	}
}

func TestFailedReviewCorrectionContextFitsTheClosedSchemas(t *testing.T) {
	context := checkpointFailedReviewCorrectionContext()
	raw, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("workflow_correction_context", raw); err != nil {
		t.Fatalf("failed-review correction context rejected by the closed pin schema: %v", err)
	}
	input := map[string]any{
		"work_id": "work-1", "expected_version": 7, "action_id": "request_correction", "idempotency_key": "checkpoint-correction-1",
		"fields": map[string]any{
			"diagnosis":     "the review failed while the delivered subject still mismatches",
			"strategy":      "rebuild the helper and re-verify with a fresh review",
			"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"},
		},
	}
	inputRaw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("work_transition_action_input", inputRaw); err != nil {
		t.Fatalf("failed-review request_correction input rejected by the closed input schema: %v", err)
	}
	padded := checkpointFailedReviewCorrectionContext()
	padded["accepted_delivery"] = "evidence:return-route-verification"
	paddedRaw, err := json.Marshal(padded)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("workflow_correction_context", paddedRaw); err == nil {
		t.Fatal("the closed correction schema accepted an undeclared accepted-delivery claim")
	}
}

func TestFailedReviewCorrectionBoundaryRequiresAcceptedCurrentReview(t *testing.T) {
	f := newSpineExecutionFixture(t)
	f.verify(t)
	f.implement(t)
	f.proof = f.verify(t)
	fields := map[string]any{
		"diagnosis":     "The review failed before producing an accepted correction disposition.",
		"strategy":      "Obtain current Review before requesting implementation correction.",
		"predicate_ids": []string{"predicate:primary"}, "evidence_refs": []string{f.proof},
	}
	// The failed review lane is not a correction prerequisite: without an
	// accepted current Review the closed refusal names the missing verdict
	// class and records nothing.
	before := premiseConsentSnapshot(t, f.s)
	refused := f.action(t, "request_correction", fields)
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil || !strings.Contains(refused.Error.Message, "current non-ok verification verdict") || refused.Error.EffectState != agentcontract.EffectNone {
		t.Fatalf("failed review granted correction authority: %+v", refused.Error)
	}
	if !reflect.DeepEqual(before, premiseConsentSnapshot(t, f.s)) {
		t.Fatal("refused failed-review correction changed workflow authority")
	}
	// Current Review, not a caller-authored non-ok verdict or operator
	// substitution, owns the correction prerequisite.
	f.review(t, "correction_required", "")
	accepted := f.read(t)
	if accepted.Pinned.Pin.Review == nil || accepted.Pinned.Pin.Review.Accepted == nil || accepted.Pinned.Pin.Review.Accepted.Report.Disposition != "correction_required" {
		t.Fatalf("retry did not accept a current correction disposition: %+v", accepted.Pinned.Pin.Review)
	}
	reportRef := accepted.Pinned.Pin.Review.Accepted.ReportRef
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": reportRef}))
	fields["evidence_refs"] = []string{reportRef}
	for _, vector := range []struct {
		name, field, message string
		value                []string
	}{
		{"unapproved predicate", "predicate_ids", "predicate", []string{"predicate:elsewhere"}},
		{"unbound evidence", "evidence_refs", "not durably bound", []string{"evidence:unbound"}},
	} {
		t.Run(vector.name, func(t *testing.T) {
			invalid := map[string]any{}
			for key, value := range fields {
				invalid[key] = value
			}
			invalid[vector.field] = vector.value
			before := premiseConsentSnapshot(t, f.s)
			response := f.action(t, "request_correction", invalid)
			if response.Outcome != agentcontract.OutcomeError || response.Error == nil || !strings.Contains(response.Error.Message, vector.message) || response.Error.EffectState != agentcontract.EffectNone {
				t.Fatalf("invalid correction = %+v, want %s refusal", response.Error, vector.name)
			}
			if !reflect.DeepEqual(before, premiseConsentSnapshot(t, f.s)) {
				t.Fatal("invalid correction changed workflow authority")
			}
		})
	}
	beforeCorrection := f.read(t)
	input := map[string]any{
		"work_id": f.work, "expected_version": beforeCorrection.Pinned.Pin.Version, "action_id": "request_correction",
		"fields": fields, "idempotency_key": "failed-review-current-correction",
	}
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", input))
	current := f.read(t)
	correction := current.Pinned.Pin.Correction
	if current.Pinned.Step != "execution" || correction == nil || correction.SourceEventID == "" || !reflect.DeepEqual(correction.PredicateIDs, []string{"predicate:primary"}) || !reflect.DeepEqual(correction.EvidenceRefs, []string{reportRef}) {
		t.Fatalf("current correction lost its bounded return context: %+v", current.Pinned)
	}
	if !reflect.DeepEqual(beforeCorrection.Pinned.Contract, current.Pinned.Contract) || !reflect.DeepEqual(beforeCorrection.Pinned.Pin.Review, current.Pinned.Pin.Review) {
		t.Fatal("correction changed contract or Review")
	}
	before = premiseConsentSnapshot(t, f.s)
	if replay := spineOK(t, f.call(t, "concord_work_transition", "workflow_action", input)); !replay.Replayed || !reflect.DeepEqual(before, premiseConsentSnapshot(t, f.s)) {
		t.Fatal("correction replay changed authority")
	}
	f.review(t, "ready_for_final_proof", f.proof)
	// A ready Review closes the correction prerequisite: the superseded
	// disposition cannot reopen the return.
	before = premiseConsentSnapshot(t, f.s)
	stale := f.action(t, "request_correction", fields)
	if stale.Outcome != agentcontract.OutcomeError || stale.Error == nil || stale.Error.EffectState != agentcontract.EffectNone || !reflect.DeepEqual(before, premiseConsentSnapshot(t, f.s)) {
		t.Fatalf("superseded correction disposition reopened the return: %+v", stale.Error)
	}
}
