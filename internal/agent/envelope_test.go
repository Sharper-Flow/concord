package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeGoldenOutcomes(t *testing.T) {
	tests := []struct {
		name string
		make func() Envelope
	}{
		{"ok read", func() Envelope {
			return NewOKRead(NewBase("req-1", "concord_product_view", "resolve"), "PM1.Q1", json.RawMessage(`{"product_id":"p-1","projects":[]}`), false)
		}},
		{"ok mutation", func() Envelope {
			return NewOKMutation(NewBase("req-2", "concord_work_define", "capture"), json.RawMessage(`{"changed_refs":[],"next_valid_intents":[]}`), []ChangedRef{{EntityKind: "work", ID: "w-1", Version: "2"}}, []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "created"}})
		}},
		{"pending", func() Envelope {
			return NewPending(NewBase("req-3", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPending, CurrentStep: "git", UpdatedAt: fixedTime()}, RecoveryAction{Kind: "reconcile_operation"})
		}},
		{"partial", func() Envelope {
			return NewPartial(NewBase("req-4", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "2", State: OperationPartial, CurrentStep: "sqlite", UpdatedAt: fixedTime()}, []string{"git"}, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial})
		}},
		{"core error", func() Envelope {
			e := NewBase("req-5", "concord_work_browse", "list")
			e.Authority = AuthorityUnreachable
			return NewCoreError(e, TypedError{Kind: "unreachable", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone})
		}},
		{"adapter error", func() Envelope {
			return NewAdapterError("req-6", "concord_product_view", "resolve", "malformed_core_response", "malformed_response")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := tt.make().Encode()
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeEnvelope(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, mustEncode(decoded)) {
				t.Fatalf("round trip changed JSON:\n%s\n%s", encoded, mustEncode(decoded))
			}
		})
	}
}

func TestEnvelopeRejectsUnknownVariantsAndFields(t *testing.T) {
	base := NewBase("req", "concord_product_view", "resolve")
	base.Outcome = Outcome("surprise")
	if err := base.Validate(); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	valid := NewOKRead(NewBase("req", "concord_product_view", "resolve"), "PM1.Q1", json.RawMessage(`{"product_id":"p-1","projects":[]}`), false)
	raw, _ := valid.Encode()
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	if _, err := DecodeEnvelope(raw); err == nil {
		t.Fatal("unknown envelope field accepted")
	}
}

func TestOutcomeMismatchIsClosedAndCannotDowngrade(t *testing.T) {
	base := NewBase("outcome-mismatch", "concord_work_transition", "workflow_action")
	valid := NewCoreError(base, TypedError{Kind: "outcome_mismatch", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone})
	if _, err := valid.Encode(); err != nil {
		t.Fatalf("closed outcome_mismatch rejected: %v", err)
	}
	invalid := NewCoreError(base, TypedError{Kind: "outcome_mismatch", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "retry_same_request"}, EffectState: EffectNone})
	if _, err := invalid.Encode(); err == nil {
		t.Fatal("outcome_mismatch was downgraded to retryable recovery")
	}
}

func TestStaleLawRevisionRequiresSHA256Proofs(t *testing.T) {
	base := NewBase("stale-law", "concord_work_transition", "workflow_action")
	valid := NewCoreError(base, TypedError{
		Kind: "stale_law_revision", RecoveryAction: RecoveryAction{Kind: "request_approval"}, EffectState: EffectNone,
		StaleLawRevision: &StaleLawRevision{OldLawID: "spec:old", OldContentHash: "sha256:" + strings.Repeat("a", 64), AcceptedSuccessorLawID: "spec:new", AcceptedSuccessorContentHash: "sha256:" + strings.Repeat("b", 64), RecoveryActions: []string{"supersede_contract"}},
	})
	if _, err := valid.Encode(); err != nil {
		t.Fatalf("valid stale law revision rejected: %v", err)
	}
	valid.Error.StaleLawRevision.OldContentHash = "sha256:not-a-proof"
	if _, err := valid.Encode(); err == nil {
		t.Fatal("stale law revision accepted an invalid old content hash")
	}
}

func TestDomainOverlapSequencedAndGloballyBoundedDetailsValidate(t *testing.T) {
	base := NewBase("overlap", "concord_work_transition", "workflow_action")
	longID := strings.Repeat("d", 256)
	detail := DomainOverlapDetail{
		ProductID: "product", FromWorkID: "from", ToWorkID: "to", FromContractVersion: 4, ToContractVersion: 7,
		SharedAffectedDomainIDs: []string{longID}, SharedLawIDs: []string{longID}, SharedDomainModifications: []string{longID},
		SharedRelationTuples: []DomainOverlapRelationTuple{{SourceDomainID: longID, Kind: "depends_on", TargetDomainID: longID}},
		OverlapClasses:       []string{"architecture"}, ResolutionState: "sequenced", ResolutionKind: "depends_on",
		RecoveryActions: []string{"wait", "resolve_overlap", "terminal_work"}, SharedAffectedDomainCount: 20, SharedLawCount: 20,
		SharedDomainModificationCount: 20, SharedRelationTupleCount: 20, DetailTruncated: true,
	}
	err := NewCoreError(base, TypedError{Kind: "domain_overlap", RecoveryAction: RecoveryAction{Kind: "request_approval"}, EffectState: EffectNone, DomainOverlap: &DomainOverlap{Overlaps: []DomainOverlapDetail{detail}, TotalOverlaps: 20, ReturnedOverlaps: 1, Truncated: true}})
	encoded, encodeErr := err.Encode()
	if encodeErr != nil {
		t.Fatalf("bounded sequenced overlap rejected: %v", encodeErr)
	}
	if len(encoded) >= MaxEnvelopeBytes {
		t.Fatalf("bounded overlap envelope is too close to the transport cap: %d", len(encoded))
	}
	detail.ResolutionState = "current"
	invalid := NewCoreError(base, TypedError{Kind: "domain_overlap", RecoveryAction: RecoveryAction{Kind: "request_approval"}, EffectState: EffectNone, DomainOverlap: &DomainOverlap{Overlaps: []DomainOverlapDetail{detail}, TotalOverlaps: 1, ReturnedOverlaps: 1}})
	if _, encodeErr := invalid.Encode(); encodeErr == nil {
		t.Fatal("schema-invalid current resolution state was accepted")
	}
}

func TestEnvelopeAllowsBoundedListErrorDetails(t *testing.T) {
	e := NewBase("req", "concord_work_define", "capture")
	e.Authority = AuthorityAuthoritative
	envelope := NewCoreError(e, TypedError{
		Kind:           "approval_required",
		RecoveryAction: RecoveryAction{Kind: "request_approval"},
		EffectState:    EffectNone,
		Details: map[string]any{
			"approval_ref":     "apr-0000000000000000000000000000000000000001",
			"scope":            []string{"product:PM1", "project:PM1.Q1"},
			"versions":         []string{"work:3"},
			"operation_digest": "sha256:abc",
		},
		ConsequenceSummary: &ConsequenceSummary{Tool: "concord_work_define", Operation: "capture", Consequence: "product_write", OperationDigest: "0123456789abcdef0123456789abcdef", Scope: []string{"product_id:PM1"}, Versions: []string{"work:3"}, ExpiresAt: "2026-08-20T00:00:00Z"},
	})
	if _, err := envelope.Encode(); err != nil {
		t.Fatalf("bounded approval details rejected: %v", err)
	}
}

func TestEnvelopeRejectsNestedErrorDetails(t *testing.T) {
	e := NewBase("req", "concord_work_define", "capture")
	e.Authority = AuthorityAuthoritative
	envelope := NewCoreError(e, TypedError{
		Kind:           "approval_required",
		RecoveryAction: RecoveryAction{Kind: "request_approval"},
		EffectState:    EffectNone,
		Details:        map[string]any{"scope": map[string]any{"product_ids": []string{"PM1"}}},
	})
	if _, err := envelope.Encode(); err == nil {
		t.Fatal("nested error details accepted")
	}
}

// TestGoverningConflictOptionsAreClosedAndCoupled pins CD-0035 D1: options is a
// typed operator-choice list, permitted only on a governing-law conflict, and
// permitted rather than required so existing invariant_violation emitters are
// untouched.
func TestGoverningConflictOptionsAreClosedAndCoupled(t *testing.T) {
	base := func() Envelope { return NewBase("req", "concord_work_define", "capture") }
	governing := []string{"clarify", "amend_contract", "accept_scope_cut"}

	valid := NewCoreError(base(), TypedError{Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: governing})
	if _, err := valid.Encode(); err != nil {
		t.Fatalf("governing-conflict options rejected: %v", err)
	}

	// Permitted, never required: an invariant_violation without options must
	// still validate, or every pre-existing emitter breaks.
	bare := NewCoreError(base(), TypedError{Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "reread_entities"}, EffectState: EffectNone})
	if _, err := bare.Encode(); err != nil {
		t.Fatalf("invariant_violation without options rejected: %v", err)
	}

	for name, bad := range map[string]TypedError{
		"wrong kind":      {Kind: "invalid_input", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: governing},
		"wrong recovery":  {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "reread_entities"}, EffectState: EffectNone, Options: governing},
		"unknown option":  {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: []string{"clarify", "make_it_smaller"}},
		"duplicate":       {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: []string{"clarify", "clarify"}},
		"empty string":    {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: []string{""}},
		"over vocabulary": {Kind: "invariant_violation", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone, Options: []string{"clarify", "amend_contract", "accept_scope_cut", "clarify_again"}},
	} {
		if _, err := NewCoreError(base(), bad).Encode(); err == nil {
			t.Errorf("%s: invalid options accepted", name)
		}
	}
}

func TestEnvelopeRejectsUnknownFieldsAcrossEveryOutcome(t *testing.T) {
	envelopes := []Envelope{
		NewOKRead(NewBase("ok", "concord_product_view", "resolve"), "PM1.Q1", json.RawMessage(`{"product_id":"p-1","projects":[]}`), false),
		NewPending(NewBase("pending", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPending, CurrentStep: "git", UpdatedAt: fixedTime()}, RecoveryAction{Kind: "reconcile_operation"}),
		NewPartial(NewBase("partial", "concord_work_compact", "publish"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPartial, CurrentStep: "sqlite", UpdatedAt: fixedTime()}, []string{"git"}, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial}),
		NewCoreError(NewBase("error", "concord_work_transition", "lifecycle"), TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "reread_entities"}, EffectState: EffectNone}),
	}
	for _, envelope := range envelopes {
		raw, err := envelope.Encode()
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		object["unknown_top_level"] = true
		mutated, _ := json.Marshal(object)
		if _, err := DecodeEnvelope(mutated); err == nil {
			t.Fatalf("unknown top-level field accepted for %s", envelope.Outcome)
		}
	}

	raw, _ := envelopes[0].Encode()
	var nested map[string]any
	_ = json.Unmarshal(raw, &nested)
	nested["resolved_scope"] = map[string]any{"product_id": "p-1", "unknown_nested": true}
	mutated, _ := json.Marshal(nested)
	if _, err := DecodeEnvelope(mutated); err == nil {
		t.Fatal("unknown nested field accepted")
	}
}

func TestEnvelopeHasHardSerializedLimit(t *testing.T) {
	payload := json.RawMessage(fmt.Sprintf(`{"value":%q}`, strings.Repeat("x", MaxEnvelopeBytes)))
	e := NewOKRead(NewBase("req", "concord_product_view", "resolve"), "PM1.Q1", payload, false)
	if _, err := e.Encode(); err == nil {
		t.Fatal("oversize envelope accepted")
	}
}

func TestOperationPayloadValidationUsesGeneratedClosedSchemas(t *testing.T) {
	validInput := []byte(`{"work_id":"w-1","expected_version":2,"target":"completed","reason":"done","idempotency_key":"idem-1"}`)
	if err := ValidateOperationPayload("concord_work_transition", "lifecycle", validInput, false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOperationPayload("concord_work_transition", "lifecycle", []byte(`{"work_id":"w-1","expected_version":2,"target":"bogus","reason":"done","idempotency_key":"idem-1"}`), false); err == nil {
		t.Fatal("invalid enum accepted by generated payload validator")
	}
	if err := ValidateOperationPayload("concord_work_transition", "lifecycle", []byte(`{"work_id":"w-1","expected_version":2,"target":"completed","reason":"done","idempotency_key":"idem-1","unknown":true}`), false); err == nil {
		t.Fatal("unknown payload field accepted")
	}
	if err := ValidateOperationPayload("concord_work_transition", "lifecycle", []byte(`{"work_id":"w-1","expected_version":2,"target":"completed","reason":"done","idempotency_key":"idem-1","evidence":[{"kind":"artifact","authority":"test","locator_kind":"file","locator":"README.md","unknown":true}]}`), false); err == nil {
		t.Fatal("unknown nested array property accepted")
	}
	if err := ValidateOperationPayload("concord_work_trace", "history", []byte(`{"events":[{"event_id":"event-1","kind":"work.created","version":"1","occurred_at":"2026-08-08T00:00:00Z","evidence":[{"kind":"artifact","authority":"test","locator_kind":"file","locator":"README.md","unknown":true}]}]}`), true); err == nil {
		t.Fatal("unknown deeply nested array property accepted")
	}
	if err := ValidateOperationPayload("concord_product_view", "resolve", []byte(`{"product_id":"p-1","unknown":true}`), false); err == nil {
		t.Fatal("unknown property in union branch accepted")
	}
	if err := ValidateOperationPayload("concord_work_compact", "reconcile", []byte(`{"operation_id":"op-1","expected_operation_version":2,"idempotency_key":"idem-1","evidence":[{"kind":"artifact","authority":"test","locator_kind":"file","locator":"README.md","unknown":true}]}`), false); err == nil {
		t.Fatal("unknown property inside union branch array accepted")
	}
	validResult := []byte(`{"changed_refs":[{"entity_kind":"work_item","id":"w-1","version":2}],"next_valid_intents":[]}`)
	if err := ValidateOperationPayload("concord_work_relate", "link", validResult, true); err != nil {
		t.Fatalf("valid generated mutation result rejected: %v", err)
	}
	if err := ValidateOperationPayload("concord_work_relate", "link", []byte(`{"changed_refs":[],"next_valid_intents":[],"unknown":true}`), true); err == nil {
		t.Fatal("unknown mutation result property accepted")
	}
}

func TestMutationResultProducerAcceptsCanonicalPayload(t *testing.T) {
	for _, operation := range []struct{ tool, operation string }{
		{"concord_work_define", "capture"}, {"concord_work_define", "revise_intent"},
		{"concord_work_transition", "lifecycle"}, {"concord_work_transition", "workflow_action"},
		{"concord_work_relate", "set_memberships"}, {"concord_work_relate", "link"}, {"concord_work_relate", "unlink"}, {"concord_work_relate", "supersede"}, {"concord_work_relate", "restore_superseded"},
		{"concord_work_compact", "publish"}, {"concord_work_compact", "reconcile"},
	} {
		t.Run(operation.tool+"."+operation.operation, func(t *testing.T) {
			base := NewBase("mutation-result", operation.tool, operation.operation)
			intent := NextIntent{Tool: "concord_work_browse", Operation: "list", QueryID: "PM1.Q3", ReasonCode: "inspect"}
			response := (runtime{Tool: base.Tool, Operation: base.Operation}).mutationResult(base, mutationPayload([]ChangedRef{{EntityKind: "work_item", ID: "w-1", Version: "2"}}, []NextIntent{intent}), []ChangedRef{{EntityKind: "work_item", ID: "w-1", Version: "2"}}, []NextIntent{intent})
			if response.Outcome != OutcomeOK {
				t.Fatalf("canonical mutation result rejected: %+v", response.Error)
			}
		})
	}
}

func TestMutationResultProducerRejectsMalformedAndOverBudgetResults(t *testing.T) {
	base := NewBase("mutation-result-reject", "concord_work_define", "capture")
	r := runtime{Tool: base.Tool, Operation: base.Operation}
	invalid := r.mutationResult(base, json.RawMessage(`{"changed_refs":[],"next_valid_intents":[],"unknown":true}`), nil, nil)
	if invalid.Outcome != OutcomeError || invalid.Error == nil || invalid.Error.Kind != "malformed_response" {
		t.Fatalf("invalid result=%+v", invalid)
	}
	largeItems := mutationPayload([]ChangedRef{{EntityKind: "work_item", ID: "w-1", Version: "1"}, {EntityKind: "work_item", ID: "w-2", Version: "1"}}, nil)
	itemLimited := (runtime{Tool: base.Tool, Operation: base.Operation, Budget: budgetInput{MaxItems: 1}}).mutationResult(base, largeItems, []ChangedRef{{EntityKind: "work_item", ID: "w-1", Version: "1"}, {EntityKind: "work_item", ID: "w-2", Version: "1"}}, nil)
	if itemLimited.Outcome != OutcomeError || itemLimited.Error == nil || itemLimited.Error.Kind != "budget_refused" {
		t.Fatalf("item-limited result=%+v", itemLimited)
	}
	byteLimited := (runtime{Tool: base.Tool, Operation: base.Operation, Budget: budgetInput{MaxBytes: 1}}).mutationResult(base, mutationPayload(nil, nil), nil, nil)
	if byteLimited.Outcome != OutcomeError || byteLimited.Error == nil || byteLimited.Error.Kind != "budget_refused" {
		t.Fatalf("byte-limited result=%+v", byteLimited)
	}
	base.EvidenceRefs = make([]EvidenceRef, 32)
	for i := range base.EvidenceRefs {
		base.EvidenceRefs[i] = EvidenceRef{Kind: "artifact", Authority: "test", LocatorKind: "file", Locator: strings.Repeat("x", 2048)}
	}
	hardLimited := r.mutationResult(base, mutationPayload(nil, nil), nil, nil)
	if hardLimited.Outcome != OutcomeError || hardLimited.Error == nil || hardLimited.Error.Kind != "limit_exceeded" {
		t.Fatalf("hard-limited result=%+v", hardLimited)
	}
	encoded, err := hardLimited.Encode()
	if err != nil {
		t.Fatalf("limit error must remain deliverable: %v", err)
	}
	if len(encoded) > MaxEnvelopeBytes {
		t.Fatalf("limit error size=%d, want <= %d", len(encoded), MaxEnvelopeBytes)
	}
	if len(hardLimited.EvidenceRefs) != 0 {
		t.Fatalf("limit error retained rejected success evidence: %d refs", len(hardLimited.EvidenceRefs))
	}
}

func TestStrictOperationUnions(t *testing.T) {
	for _, input := range []string{`{}`, `{"product_id":"p-1"}`, `{"project_id":"pr-1"}`} {
		if err := ValidateOperationPayload("concord_product_view", "resolve", []byte(input), false); err != nil {
			t.Errorf("resolve union rejected %s: %v", input, err)
		}
	}
	if err := ValidateOperationPayload("concord_product_view", "resolve", []byte(`{"product_id":"p-1","project_id":"pr-1"}`), false); err == nil {
		t.Fatal("resolve accepted product/project union overlap")
	} else if got := err.Error(); !strings.Contains(got, "oneOf mismatch at $") || !strings.Contains(got, "product_id") || !strings.Contains(got, "project_id") || strings.Contains(got, "p-1") || strings.Contains(got, "pr-1") {
		t.Fatalf("union diagnostic = %q, want accepted field variants without payload values", got)
	}
	if err := ValidateOperationPayload("concord_work_compact", "reconcile", []byte(`{"operation_id":"op-1","expected_operation_version":2,"idempotency_key":"idem"}`), false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOperationPayload("concord_work_compact", "reconcile", []byte(`{"work_id":"w-1","expected_work_version":2,"idempotency_key":"idem"}`), false); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOperationPayload("concord_work_compact", "reconcile", []byte(`{"operation_id":"op-1","expected_operation_version":2,"work_id":"w-1","expected_work_version":2,"idempotency_key":"idem"}`), false); err == nil {
		t.Fatal("reconcile accepted both branches")
	}
}

func TestDecodeEnvelopeRejectsInvalidTrailingJSON(t *testing.T) {
	valid := NewOKRead(NewBase("request-1", "concord_product_view", "resolve"), "PM1.Q1", json.RawMessage(`{"product_id":"product-1","projects":[]}`), false)
	raw, err := valid.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{" {}", " garbage"} {
		if _, err := DecodeEnvelope(append(raw, []byte(suffix)...)); err == nil {
			t.Fatalf("suffix %q was accepted", suffix)
		}
	}
}

func fixedTime() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }
func mustEncode(e Envelope) []byte {
	b, err := e.Encode()
	if err != nil {
		panic(err)
	}
	return b
}
