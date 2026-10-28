package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestOperationPayloadValidationUsesGeneratedClosedSchemas(t *testing.T) {
	t.Parallel()
	validInput := []byte(`{"work_id":"w-1","expected_version":2,"target":"completed","reason":"done","idempotency_key":"idem-1"}`)
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "lifecycle", validInput, false); err != nil {
		t.Fatal(err)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "lifecycle", []byte(`{"work_id":"w-1","expected_version":2,"target":"bogus","reason":"done","idempotency_key":"idem-1"}`), false); err == nil {
		t.Fatal("invalid enum accepted by generated payload validator")
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "lifecycle", []byte(`{"work_id":"w-1","expected_version":2,"target":"completed","reason":"done","idempotency_key":"idem-1","unknown":true}`), false); err == nil {
		t.Fatal("unknown payload field accepted")
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "lifecycle", []byte(`{"work_id":"w-1","expected_version":2,"target":"completed","reason":"done","idempotency_key":"idem-1","evidence":[{"kind":"artifact","authority":"test","locator_kind":"file","locator":"README.md","unknown":true}]}`), false); err == nil {
		t.Fatal("unknown nested array property accepted")
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_trace", "history", []byte(`{"entries":[{"entry_id":"evidence_binding:one","entry_kind":"evidence_binding","version":1,"version_work_id":"work-1","position":{"effect_seq":1,"basis":"effect","unknown":true},"evidence_refs":["README.md"],"binding_id":"binding-1","evidence_kind":"artifact","immutable_subject_ref":"README.md","producer_id":"producer-1","producer_run_ref":"run-1","producer_watermark":"seq:1","observed_at":"2026-08-08T00:00:00Z"}]}`), true); err == nil {
		t.Fatal("unknown deeply nested array property accepted")
	}
	if err := agentcontract.ValidateOperationPayload("concord_product_view", "resolve", []byte(`{"product_id":"p-1","unknown":true}`), false); err == nil {
		t.Fatal("unknown property in union branch accepted")
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_compact", "reconcile", []byte(`{"operation_id":"op-1","expected_operation_version":2,"idempotency_key":"idem-1","evidence":[{"kind":"artifact","authority":"test","locator_kind":"file","locator":"README.md","unknown":true}]}`), false); err == nil {
		t.Fatal("unknown property inside union branch array accepted")
	}
	validResult := []byte(`{"changed_refs":[{"entity_kind":"work_item","id":"w-1","version":2}],"next_valid_intents":[]}`)
	if err := agentcontract.ValidateOperationPayload("concord_work_relate", "resolve_overlap", validResult, true); err != nil {
		t.Fatalf("valid generated mutation result rejected: %v", err)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_relate", "resolve_overlap", []byte(`{"changed_refs":[],"next_valid_intents":[],"unknown":true}`), true); err == nil {
		t.Fatal("unknown mutation result property accepted")
	}
}

func TestOperationPayloadValidationNamesSiblingOperationForUnknownField(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		tool, operation, payload, owner string
	}{
		{"concord_work_browse", "scope", `{"product_id":"p-1","detail":"summary"}`, "concord_work_browse.list"},
		{"concord_work_transition", "workflow_action", `{"work_id":"w-1","expected_version":1,"action_id":"do_work","idempotency_key":"i-1","actor":"actor-1"}`, "concord_work_transition.remove"},
	} {
		t.Run(testCase.tool+"."+testCase.operation, func(t *testing.T) {
			err := agentcontract.ValidateOperationPayload(testCase.tool, testCase.operation, []byte(testCase.payload), false)
			if err == nil {
				t.Fatal("unknown sibling operation field accepted")
			}
			if !strings.Contains(err.Error(), testCase.owner) {
				t.Fatalf("unknown field error = %q, want owning operation", err)
			}
		})
	}
}

// The generated payload validator refuses once per payload, not once per
// field: missing required fields in declared order, then unknown fields
// sorted by name. The instance walk is an unordered map walk, so the test
// repeats the call to prove identical output on every run.
func TestOperationPayloadValidationNamesEveryMissingAndUnknownField(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"work_id":"w-1","expected_version":1,"kind":"task","reason":"revise","idempotency_key":"idem-1","unknown_extra":true}`)
	want := "missing payload field title; missing payload field value_statement; unknown payload field unknown_extra"
	for range 50 {
		err := agentcontract.ValidateOperationPayload("concord_work_define", "revise_intent", payload, false)
		if err == nil {
			t.Fatal("a payload missing required fields was accepted")
		}
		if got := err.Error(); got != want {
			t.Fatalf("refusal = %q, want %q", got, want)
		}
	}
}

func TestMutationResultProducerAcceptsCanonicalPayload(t *testing.T) {
	t.Parallel()
	_, workID, pin := enrichmentPinFixture(t)
	changed := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: workID, Version: strconv.FormatInt(pin.Version, 10)}}
	payload, intents, err := enrichMutationPayload(mutationPayload(changed, nil), []store.WorkPin{pin})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct{ tool, operation string }{
		{"concord_work_define", "capture"}, {"concord_work_define", "revise_intent"},
		{"concord_work_transition", "lifecycle"}, {"concord_work_transition", "workflow_action"},
		{"concord_work_relate", "set_memberships"}, {"concord_work_relate", "resolve_overlap"},
		{"concord_work_compact", "publish"}, {"concord_work_compact", "reconcile"},
	} {
		t.Run(operation.tool+"."+operation.operation, func(t *testing.T) {
			base := agentcontract.NewBase("mutation-result", operation.tool, operation.operation)
			response := (runtime{Tool: base.Tool, Operation: base.Operation}).mutationResult(base, payload, changed, intents)
			if response.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("canonical mutation result rejected: %+v", response.Error)
			}
		})
	}
}

func TestMutationResultProducerRejectsMalformedAndOverBudgetResults(t *testing.T) {
	t.Parallel()
	base := agentcontract.NewBase("mutation-result-reject", "concord_work_define", "capture")
	r := runtime{Tool: base.Tool, Operation: base.Operation}
	invalid := r.mutationResult(base, json.RawMessage(`{"changed_refs":[],"next_valid_intents":[],"unknown":true}`), nil, nil)
	if invalid.Outcome != agentcontract.OutcomeError || invalid.Error == nil || invalid.Error.Kind != "malformed_response" {
		t.Fatalf("invalid result=%+v", invalid)
	}
	// CD-0038: no caller budget bounds results; the canonical envelope cap
	// is the only bound.
	base.EvidenceRefs = make([]agentcontract.EvidenceRef, 32)
	for i := range base.EvidenceRefs {
		base.EvidenceRefs[i] = agentcontract.EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 2048)}
	}
	hardLimited := r.mutationResult(base, mutationPayload(nil, nil), nil, nil)
	if hardLimited.Outcome != agentcontract.OutcomeError || hardLimited.Error == nil || hardLimited.Error.Kind != "limit_exceeded" || hardLimited.Error.RecoveryAction.Kind != "contact_operator" {
		t.Fatalf("hard-limited result=%+v", hardLimited)
	}
	encoded, err := hardLimited.Encode()
	if err != nil {
		t.Fatalf("limit error must remain deliverable: %v", err)
	}
	if len(encoded) > agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("limit error size=%d, want <= %d", len(encoded), agentcontract.MaxResultEnvelopeBytes)
	}
	if len(hardLimited.EvidenceRefs) != 0 {
		t.Fatalf("limit error retained rejected success evidence: %d refs", len(hardLimited.EvidenceRefs))
	}
}

func TestMutationResultMalformedEnvelopeReturnsBoundedError(t *testing.T) {
	t.Parallel()
	base := agentcontract.NewBase("bounded-error", "concord_work_define", "capture")
	base.EvidenceRefs = make([]agentcontract.EvidenceRef, 32)
	for i := range base.EvidenceRefs {
		base.EvidenceRefs[i] = agentcontract.EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 2048)}
	}
	r := runtime{Tool: base.Tool, Operation: base.Operation}
	response := r.mutationResult(base, mutationPayload(nil, nil), nil, []agentcontract.NextIntent{{Tool: "concord_work_trace", Operation: "history", QueryID: "C19.Continuity", ReasonCode: "invalid-query"}})
	if response.Outcome != agentcontract.OutcomeError || response.Error == nil || response.Error.Kind != "malformed_response" {
		t.Fatalf("response=%+v error=%+v, want malformed_response", response, response.Error)
	}
	encoded, err := response.Encode()
	if err != nil {
		t.Fatalf("malformed result must return a bounded error envelope: %v", err)
	}
	if len(encoded) > agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("error envelope size=%d, want <= %d", len(encoded), agentcontract.MaxResultEnvelopeBytes)
	}
}

func TestCoreErrorBoundsLongMessage(t *testing.T) {
	t.Parallel()
	response := coreError(agentcontract.NewBase("long-error", "concord_work_define", "capture"), "invalid_input", strings.Repeat("x", 2000), "reread_entities", false)
	if response.Error == nil || len(response.Error.Message) > 1000 {
		t.Fatalf("error message length=%d, want <= 1000", len(response.Error.Message))
	}
	if _, err := response.Encode(); err != nil {
		t.Fatalf("bounded error must encode: %v", err)
	}
}

func TestStrictOperationUnions(t *testing.T) {
	t.Parallel()
	for _, input := range []string{`{}`, `{"product_id":"p-1"}`, `{"project_id":"pr-1"}`} {
		if err := agentcontract.ValidateOperationPayload("concord_product_view", "resolve", []byte(input), false); err != nil {
			t.Errorf("resolve union rejected %s: %v", input, err)
		}
	}
	if err := agentcontract.ValidateOperationPayload("concord_product_view", "resolve", []byte(`{"product_id":"p-1","project_id":"pr-1"}`), false); err == nil {
		t.Fatal("resolve accepted product/project union overlap")
	} else if got := err.Error(); !strings.Contains(got, "oneOf mismatch at $") || !strings.Contains(got, "product_id") || !strings.Contains(got, "project_id") || strings.Contains(got, "p-1") || strings.Contains(got, "pr-1") {
		t.Fatalf("union diagnostic = %q, want accepted field variants without payload values", got)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_compact", "reconcile", []byte(`{"operation_id":"op-1","expected_operation_version":2,"idempotency_key":"idem"}`), false); err != nil {
		t.Fatal(err)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_compact", "reconcile", []byte(`{"work_id":"w-1","expected_work_version":2,"idempotency_key":"idem"}`), false); err != nil {
		t.Fatal(err)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_compact", "reconcile", []byte(`{"operation_id":"op-1","expected_operation_version":2,"work_id":"w-1","expected_work_version":2,"idempotency_key":"idem"}`), false); err == nil {
		t.Fatal("reconcile accepted both branches")
	}
}

func fixedTime() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }
