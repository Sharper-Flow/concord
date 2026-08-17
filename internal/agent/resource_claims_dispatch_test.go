package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0028 / issue #88: uncontended claim, contended claim, release,
// holder-session-gone, and the no-authority property — holding a claim
// bypasses nothing.

func claimsFixture(t *testing.T) (*store.Store, *Service, Grant) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "claims-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Claims","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "claims-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Claims Project"}`)},
		{EventID: "claims-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"claims fixture","expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	// Two works: the holder and the contender.
	for _, w := range []struct{ id, title string }{{"work-holder", "Holder"}, {"work-contender", "Contender"}} {
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
			{EventID: w.id + "-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: w.id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"` + w.title + `","priority":1}`)},
			{EventID: w.id + "-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: w.id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, w.id): 0}}); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(s.DB())
	service.Now = fixedTime
	_, service, grant, _ := claimsGrant(t, s, service)
	return s, service, grant
}

func claimsGrant(t *testing.T, s *store.Store, service *Service) (*store.Store, *Service, Grant, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read", "work_relate", "work_define", "work_transition"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	req := grantRequest(privateKey, "claims-dispatch-nonce")
	req.Assertion.RequestedCapabilities = []Capability{"product_read", "work_relate", "work_define", "work_transition"}
	req.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(req.Assertion))
	grant, err := service.IssueGrant(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, privateKey
}

func TestResourceClaimLifecycle(t *testing.T) {
	ctx := context.Background()
	s, service, grant := claimsFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(op string, input any) Envelope {
		t.Helper()
		raw, _ := json.Marshal(input)
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: toolForClaimsOp(op), Operation: op, Input: raw}, mutationEnvelope(grant, scopeVersion))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	// Uncontended claim.
	claim := invoke("resource_claim", map[string]any{"work_id": "work-holder", "resource_key": "fence:prod-pause", "reason": "Holding the fleet pause while data cleanup runs.", "expected_version": 2, "idempotency_key": "claim-1"})
	if claim.Outcome != OutcomeOK {
		t.Fatalf("uncontended claim failed: %+v", claim.Error)
	}

	// Contended claim: another work item sees a typed refusal naming coordination.
	contended := invoke("resource_claim", map[string]any{"work_id": "work-contender", "resource_key": "fence:prod-pause", "reason": "Designing hardened tooling for the same pause.", "expected_version": 2, "idempotency_key": "claim-2"})
	if contended.Outcome == OutcomeOK || contended.Error == nil {
		t.Fatal("contended claim must refuse")
	}
	raw, _ := json.Marshal(contended.Error)
	if !strings.Contains(string(raw), "already claimed") {
		t.Fatalf("refusal=%s", raw)
	}

	// Discovery: exact key shows holder and reason.
	discover := invoke("resource_claims", map[string]any{"product_id": "product-1", "resource_key": "fence:prod-pause"})
	if discover.Outcome != OutcomeOK {
		t.Fatalf("discovery failed: %+v", discover.Error)
	}
	var page struct {
		Claims []store.ResourceClaim `json:"claims"`
	}
	if err := json.Unmarshal(discover.Result, &page); err != nil || len(page.Claims) != 1 {
		t.Fatalf("claims=%+v err=%v", page.Claims, err)
	}
	if page.Claims[0].HolderWorkID != "work-holder" || page.Claims[0].State != "held" || page.Claims[0].Reason == "" {
		t.Fatalf("claim=%+v", page.Claims[0])
	}

	// Release by the holder.
	release := invoke("resource_release", map[string]any{"work_id": "work-holder", "resource_key": "fence:prod-pause", "expected_version": 3, "idempotency_key": "release-1"})
	if release.Outcome != OutcomeOK {
		t.Fatalf("release failed: %+v", release.Error)
	}

	// After release the contender may claim.
	reclaim := invoke("resource_claim", map[string]any{"work_id": "work-contender", "resource_key": "fence:prod-pause", "reason": "Now free to exercise the pause tooling.", "expected_version": 2, "idempotency_key": "claim-3"})
	if reclaim.Outcome != OutcomeOK {
		t.Fatalf("post-release claim failed: %+v", reclaim.Error)
	}

	// Holder-session-gone + terminal release: terminalizing the holding work
	// releases everything it holds. (The claim survives session restart by
	// construction — the holder is the work item, not the session.)
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "contender-terminal", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "work-contender", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"completed","reason":"fixture terminal","expected_version":3,"resulting_version":4}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-contender"): 3}}); err != nil {
		t.Fatal(err)
	}
	after := invoke("resource_claims", map[string]any{"product_id": "product-1", "resource_key": "fence:prod-pause"})
	if err := json.Unmarshal(after.Result, &page); err != nil || len(page.Claims) != 1 || page.Claims[0].State != "released" {
		t.Fatalf("claims after terminal=%+v err=%v", page.Claims, err)
	}
}

func TestResourceClaimGrantsNoAuthority(t *testing.T) {
	ctx := context.Background()
	s, service, grant := claimsFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	// Hold a claim.
	raw, _ := json.Marshal(map[string]any{"work_id": "work-holder", "resource_key": "db:test-migration", "reason": "Migration in flight.", "expected_version": 2, "idempotency_key": "auth-claim"})
	claim, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resource_claim", Input: raw}, mutationEnvelope(grant, scopeVersion))
	if err != nil || claim.Outcome != OutcomeOK {
		t.Fatalf("claim=%+v kind=%s msg=%s err=%v", claim, claim.Error.Kind, claim.Error.Message, err)
	}
	// A terminal lifecycle transition still demands approval and evidence —
	// holding a claim changes nothing about authority. Both gates are
	// asserted: without evidence the refusal is missing_evidence, and with
	// evidence supplied the claim still buys no approval. Asserting only the
	// first would leave the approval gate unexercised here.
	terminalInput, _ := json.Marshal(map[string]any{"work_id": "work-holder", "expected_version": 3, "target": "completed", "reason": "done", "idempotency_key": "auth-terminal"})
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: terminalInput}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "missing_evidence" {
		t.Fatalf("claim holder must still need evidence, got %+v", response.Error)
	}
	withEvidence, _ := json.Marshal(map[string]any{"work_id": "work-holder", "expected_version": 3, "target": "completed", "reason": "done", "idempotency_key": "auth-terminal-evidence",
		"evidence": []map[string]any{{"kind": "verification", "authority": "native_run", "locator_kind": "run_ref", "locator": "claim-holder-verification"}}})
	approvalGated, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: withEvidence}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if approvalGated.Outcome != OutcomeError || approvalGated.Error == nil || approvalGated.Error.Kind != "approval_required" {
		t.Fatalf("claim holder must still need approval once evidence is supplied, got %+v", approvalGated.Error)
	}
	if ref, _ := approvalGated.Error.Details["approval_ref"].(string); len(ref) != 64 {
		t.Fatalf("approval refusal must carry an actionable approval_ref, got %v", approvalGated.Error.Details["approval_ref"])
	}
}

func toolForClaimsOp(op string) string {
	switch op {
	case "resource_claim", "resource_release":
		return "concord_work_relate"
	default:
		return "concord_work_browse"
	}
}
