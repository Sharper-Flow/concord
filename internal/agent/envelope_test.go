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
			return NewOKRead(NewBase("req-1", "concord_product_view", "resolve", "1.0.0"), "PM1.Q1", json.RawMessage(`{"product_id":"p-1","projects":[]}`), false)
		}},
		{"ok mutation", func() Envelope {
			return NewOKMutation(NewBase("req-2", "concord_work_define", "capture", "1.0.0"), json.RawMessage(`{"changed_refs":[],"next_valid_intents":[]}`), []ChangedRef{{EntityKind: "work", ID: "w-1", Version: "2"}}, []NextIntent{{Tool: "concord_work_browse", Operation: "scope", QueryID: "PM1.Q6", ReasonCode: "created"}})
		}},
		{"pending", func() Envelope {
			return NewPending(NewBase("req-3", "concord_work_compact", "publish", "1.0.0"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPending, CurrentStep: "git", UpdatedAt: fixedTime()}, RecoveryAction{Kind: "reconcile_operation"})
		}},
		{"partial", func() Envelope {
			return NewPartial(NewBase("req-4", "concord_work_compact", "publish", "1.0.0"), OperationRef{ID: "op-1", Kind: "publish", Version: "2", State: OperationPartial, CurrentStep: "sqlite", UpdatedAt: fixedTime()}, []string{"git"}, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial})
		}},
		{"core error", func() Envelope {
			e := NewBase("req-5", "concord_work_browse", "list", "1.0.0")
			e.Authority = AuthorityUnreachable
			return NewCoreError(e, TypedError{Kind: "unreachable", RecoveryAction: RecoveryAction{Kind: "contact_operator"}, EffectState: EffectNone})
		}},
		{"adapter error", func() Envelope {
			return NewAdapterError("req-6", "concord_product_view", "resolve", "1.0", "malformed_core_response", "malformed_response")
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
	base := NewBase("req", "concord_product_view", "resolve", "1.0.0")
	base.Outcome = Outcome("surprise")
	if err := base.Validate(); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	valid := NewOKRead(NewBase("req", "concord_product_view", "resolve", "1.0.0"), "PM1.Q1", json.RawMessage(`{"product_id":"p-1","projects":[]}`), false)
	raw, _ := valid.Encode()
	raw = append(raw[:len(raw)-1], []byte(`,"unknown":true}`)...)
	if _, err := DecodeEnvelope(raw); err == nil {
		t.Fatal("unknown envelope field accepted")
	}
}

func TestEnvelopeAllowsBoundedListErrorDetails(t *testing.T) {
	e := NewBase("req", "concord_work_define", "capture", "1.0.0")
	e.Authority = AuthorityAuthoritative
	envelope := NewCoreError(e, TypedError{
		Kind:           "approval_required",
		RecoveryAction: RecoveryAction{Kind: "request_approval"},
		EffectState:    EffectNone,
		Details: map[string]any{
			"approval_ref":     "apr-1",
			"scope":            []string{"product:PM1", "project:PM1.Q1"},
			"versions":         []string{"work:3"},
			"operation_digest": "sha256:abc",
		},
	})
	if _, err := envelope.Encode(); err != nil {
		t.Fatalf("bounded approval details rejected: %v", err)
	}
}

func TestEnvelopeRejectsNestedErrorDetails(t *testing.T) {
	e := NewBase("req", "concord_work_define", "capture", "1.0.0")
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

func TestEnvelopeRejectsUnknownFieldsAcrossEveryOutcome(t *testing.T) {
	envelopes := []Envelope{
		NewOKRead(NewBase("ok", "concord_product_view", "resolve", "1.0.0"), "PM1.Q1", json.RawMessage(`{"product_id":"p-1","projects":[]}`), false),
		NewPending(NewBase("pending", "concord_work_compact", "publish", "1.0.0"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPending, CurrentStep: "git", UpdatedAt: fixedTime()}, RecoveryAction{Kind: "reconcile_operation"}),
		NewPartial(NewBase("partial", "concord_work_compact", "publish", "1.0.0"), OperationRef{ID: "op-1", Kind: "publish", Version: "1", State: OperationPartial, CurrentStep: "sqlite", UpdatedAt: fixedTime()}, []string{"git"}, TypedError{Kind: "operation_conflict", RetrySafe: true, RecoveryAction: RecoveryAction{Kind: "reconcile_operation"}, EffectState: EffectPartial}),
		NewCoreError(NewBase("error", "concord_work_transition", "lifecycle", "1.0.0"), TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: RecoveryAction{Kind: "reread_entities"}, EffectState: EffectNone}),
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
	e := NewOKRead(NewBase("req", "concord_product_view", "resolve", "1.0.0"), "PM1.Q1", payload, false)
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
	valid := NewOKRead(NewBase("request-1", "concord_product_view", "resolve", ManifestVersion), "PM1.Q1", json.RawMessage(`{"product_id":"product-1","projects":[]}`), false)
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
