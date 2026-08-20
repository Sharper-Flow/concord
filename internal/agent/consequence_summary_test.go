package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// CD-0037: the approval prompt is a typed object derived from the challenge's
// own facts, present exactly when a challenge was minted.

func TestConsequenceSummaryDerivesFromTheChallengeSpec(t *testing.T) {
	spec := ApprovalChallengeSpec{
		OperationDigest: "sha256:" + make64Hex(t),
		Scope:           map[string]any{"product_id": "prod-a", "work_ids": []string{"work-b", "work-a"}},
		Versions:        map[string]any{"work": int64(3)},
		Consequence:     "production_write",
		ExpiresAt:       time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
	}
	summary := consequenceSummaryFor("concord_work_transition", "workflow_action", spec)
	if summary.Tool != "concord_work_transition" || summary.Operation != "workflow_action" {
		t.Fatalf("identity fields must come from the invocation: %#v", summary)
	}
	if summary.Consequence != "production_write" || summary.OperationDigest != spec.OperationDigest {
		t.Fatalf("challenge facts must pass through unchanged: %#v", summary)
	}
	// The canonical renderers are sorted, so the summary's bindings equal what
	// approval consumption compares byte-for-byte.
	if summary.Scope[0] != "product_id:prod-a" || summary.Scope[1] != "work_ids:work-a" || summary.Scope[2] != "work_ids:work-b" {
		t.Fatalf("scope bindings not canonical: %v", summary.Scope)
	}
	if summary.Versions[0] != "work:3" {
		t.Fatalf("version bindings not canonical: %v", summary.Versions)
	}
	if summary.ExpiresAt != "2026-08-20T12:00:00Z" {
		t.Fatalf("expiry not RFC3339 UTC: %q", summary.ExpiresAt)
	}
}

func TestValidateErrorAcceptsTheDerivedSummary(t *testing.T) {
	base := TypedError{Kind: "approval_required", RecoveryAction: RecoveryAction{Kind: "request_approval"}, EffectState: EffectNone}
	base.ConsequenceSummary = consequenceSummaryFor("concord_work_transition", "workflow_action", ApprovalChallengeSpec{
		OperationDigest: "sha256:" + make64Hex(t),
		Scope:           map[string]any{"product_id": "prod-a"},
		Versions:        map[string]any{"work": int64(1)},
		Consequence:     "production_write",
		ExpiresAt:       time.Now().UTC().Add(10 * time.Minute),
	})
	if err := validateError(base); err != nil {
		t.Fatalf("derived summary rejected by envelope validation: %v", err)
	}
	base.ConsequenceSummary.Scope = []string{"work_ids:work-b", "work_ids:work-a"}
	if err := validateError(base); err == nil {
		t.Fatal("unsorted scope bindings accepted; the summary must be canonical")
	}
	base.ConsequenceSummary.Scope = []string{"product_id:prod-a"}
	base.ConsequenceSummary.OperationDigest = "not-a-digest"
	if err := validateError(base); err == nil {
		t.Fatal("malformed digest accepted")
	}
}

func make64Hex(t *testing.T) string {
	t.Helper()
	digest := make([]byte, 64)
	for i := range digest {
		digest[i] = byte('a' + i%6)
	}
	return string(digest)
}

func TestMintedChallengeRefusalCarriesTheTypedSummary(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	resp, err := Dispatch(context.Background(), s, service, InvokeRequest{
		Tool:      "concord_work_transition",
		Operation: "lifecycle",
		Input:     json.RawMessage(`{"work_id":"work-1","expected_version":2,"target":"completed","reason":"complete","idempotency_key":"cd0037-summary","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"verification-pass"}]}`),
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Outcome != OutcomeError || resp.Error == nil || resp.Error.Kind != "approval_required" {
		t.Fatalf("expected approval_required, got outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if _, ok := resp.Error.Details["approval_ref"].(string); !ok {
		t.Fatal("challenge was not minted; the summary coupling cannot be tested")
	}
	summary := resp.Error.ConsequenceSummary
	if summary == nil {
		t.Fatal("minted challenge refusal carries no typed consequence summary")
	}
	if summary.Tool != "concord_work_transition" || summary.Operation != "lifecycle" {
		t.Fatalf("summary identity not from the invocation: %#v", summary)
	}
	if _, err := time.Parse(time.RFC3339Nano, summary.ExpiresAt); err != nil {
		t.Fatalf("summary expiry not RFC3339: %q", summary.ExpiresAt)
	}
	if err := validateError(*resp.Error); err != nil {
		t.Fatalf("refusal envelope violates the law it implements: %v", err)
	}
}
