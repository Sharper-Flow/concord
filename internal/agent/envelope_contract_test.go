package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestCommittedLifecycleEnvelopeSatisfiesGeneratedContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"in_progress","reason":"begin work","idempotency_key":"701-lifecycle"}`)}
	committed, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || committed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("lifecycle response=%+v err=%v", committed, err)
	}
	encoded, err := committed.Encode()
	if err != nil {
		t.Fatalf("committed lifecycle envelope failed its own producer validation: %v", err)
	}
	if err := payloadschema.ValidateEnvelope(encoded); err != nil {
		t.Fatalf("committed lifecycle envelope failed the generated TS7 contract: %v; envelope=%s", err, encoded)
	}
	var read agentcontract.Envelope
	if err := json.Unmarshal(encoded, &read); err != nil {
		t.Fatalf("committed lifecycle envelope cannot be re-read as a TS7 envelope: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	scope, ok := document["resolved_scope"].(map[string]any)
	if !ok {
		t.Fatalf("committed envelope has no resolved_scope: %s", encoded)
	}
	if _, present := scope["product_ids"]; present {
		t.Fatalf("resolved_scope carries derived product identifiers the envelope law does not declare: %s", encoded)
	}
}

func TestGeneratedContractRejectsUndeclaredScopeMembers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"in_progress","reason":"begin work","idempotency_key":"701-negative"}`)}
	committed, err := Dispatch(ctx, s, service, request, mutationEnvelope(grant, scopeVersion))
	if err != nil || committed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("lifecycle response=%+v err=%v", committed, err)
	}
	encoded, err := committed.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	scope, ok := document["resolved_scope"].(map[string]any)
	if !ok {
		t.Fatalf("committed envelope has no resolved_scope: %s", encoded)
	}
	scope["product_ids"] = []string{"product-1"}
	tampered, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.ValidateEnvelope(tampered); err == nil {
		t.Fatal("generated TS7 contract accepted resolved_scope.product_ids, the exact #701 drift shape")
	}
}

func TestGeneratedContractRejectsUndeclaredAllOfMembers(t *testing.T) {
	base := agentcontract.NewBase("allof-unknown", "concord_work_transition", "lifecycle")
	errorEnvelope := agentcontract.NewCoreError(base, agentcontract.TypedError{Kind: "invalid_input", RetrySafe: false, RecoveryAction: agentcontract.RecoveryAction{Kind: "reread_entities"}, EffectState: agentcontract.EffectNone})
	raw, err := json.Marshal(map[string]any{
		"schema_version":           errorEnvelope.SchemaVersion,
		"manifest_digest":          errorEnvelope.ManifestDigest,
		"request_id":               errorEnvelope.RequestID,
		"origin":                   errorEnvelope.Origin,
		"tool":                     errorEnvelope.Tool,
		"operation":                errorEnvelope.Operation,
		"outcome":                  errorEnvelope.Outcome,
		"resolved_scope":           nil,
		"authority":                errorEnvelope.Authority,
		"freshness":                nil,
		"source_version_watermark": []agentcontract.Watermark{},
		"ordering_keys":            []string{},
		"next_cursor":              nil,
		"omissions":                []agentcontract.Notice{},
		"warnings":                 []agentcontract.Notice{},
		"evidence_refs":            []agentcontract.EvidenceRef{},
		"replayed":                 false,
		"error":                    errorEnvelope.Error,
		"unknown_top_level":        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.ValidateEnvelope(raw); err == nil {
		t.Fatal("generated contract accepted an undeclared allOf member")
	}
}

func TestErrorOutcomeContractAdmitsChangedRefs(t *testing.T) {
	base := agentcontract.NewBase("error-changed-refs", "concord_work_transition", "workflow_action")
	errorEnvelope := agentcontract.NewCoreError(base, agentcontract.TypedError{Kind: "budget_refused", RetrySafe: false, RecoveryAction: agentcontract.RecoveryAction{Kind: "adjust_budget"}, EffectState: agentcontract.EffectPossible, SupportedBudgetSeconds: 300})
	refs := []agentcontract.ChangedRef{{EntityKind: "work_item", ID: "work-2", Version: "5"}}
	errorEnvelope.ChangedRefs = &refs
	if err := errorEnvelope.Validate(); err != nil {
		t.Fatalf("error outcome with committed refs is invalid: %v", err)
	}
}
