package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// CD-0028 / issue #88: uncontended claim, contended claim, release,
// holder-session-gone, and the no-authority property — holding a claim
// bypasses nothing.

func claimsFixture(t *testing.T) (*store.Store, *Service, Authority) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
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
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"product_read", "work_relate", "work_define", "work_transition"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant
}

func TestResourceClaimLifecycle(t *testing.T) {
	t.Parallel()
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
	claim := invoke("resource_claim", map[string]any{"work_id": "work-holder", "resource_key": "fence:prod/pause", "reason": "Holding the fleet pause while data cleanup runs.", "expected_version": 2, "idempotency_key": "claim-1"})
	if claim.Outcome != OutcomeOK {
		t.Fatalf("uncontended claim failed: %+v", claim.Error)
	}
	if claim.ChangedRefs == nil || len(*claim.ChangedRefs) != 1 || (*claim.ChangedRefs)[0] != (ChangedRef{EntityKind: "work_item", ID: "work-holder", Version: "3"}) {
		t.Fatalf("claim receipt=%+v, want the stored work identity and version", claim.ChangedRefs)
	}

	// Contended claim: another work item sees a typed refusal naming coordination.
	contended := invoke("resource_claim", map[string]any{"work_id": "work-contender", "resource_key": "fence:prod/pause", "reason": "Designing hardened tooling for the same pause.", "expected_version": 2, "idempotency_key": "claim-2"})
	if contended.Outcome == OutcomeOK || contended.Error == nil {
		t.Fatal("contended claim must refuse")
	}
	raw, _ := json.Marshal(contended.Error)
	if !strings.Contains(string(raw), "already claimed") {
		t.Fatalf("refusal=%s", raw)
	}

	// Discovery: exact key shows holder and reason.
	discover := invoke("resource_claims", map[string]any{"product_id": "product-1", "resource_key": "fence:prod/pause"})
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
	release := invoke("resource_release", map[string]any{"work_id": "work-holder", "resource_key": "fence:prod/pause", "expected_version": 3, "idempotency_key": "release-1"})
	if release.Outcome != OutcomeOK {
		t.Fatalf("release failed: %+v", release.Error)
	}
	if release.ChangedRefs == nil || len(*release.ChangedRefs) != 1 || (*release.ChangedRefs)[0] != (ChangedRef{EntityKind: "work_item", ID: "work-holder", Version: "4"}) {
		t.Fatalf("release receipt=%+v, want the stored work identity and version", release.ChangedRefs)
	}

	// After release the contender may claim.
	reclaim := invoke("resource_claim", map[string]any{"work_id": "work-contender", "resource_key": "fence:prod/pause", "reason": "Now free to exercise the pause tooling.", "expected_version": 2, "idempotency_key": "claim-3"})
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
	after := invoke("resource_claims", map[string]any{"product_id": "product-1", "resource_key": "fence:prod/pause"})
	if err := json.Unmarshal(after.Result, &page); err != nil || len(page.Claims) != 1 || page.Claims[0].State != "released" {
		t.Fatalf("claims after terminal=%+v err=%v", page.Claims, err)
	}
}

func TestResourceClaimGrantsNoAuthority(t *testing.T) {
	t.Parallel()
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

func TestResourceClaimReplayRefusalsAndAtomicity(t *testing.T) {
	ctx := context.Background()
	s, service, grant := claimsFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"work_id": "work-holder", "resource_key": "fence:prod/pause", "reason": "Holding the fleet pause while data cleanup runs.", "expected_version": 2, "idempotency_key": "claim-replay"}
	invoke := func(op string, value any) Envelope {
		t.Helper()
		raw, _ := json.Marshal(value)
		response, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: toolForClaimsOp(op), Operation: op, Input: raw}, mutationEnvelope(grant, scopeVersion))
		if dispatchErr != nil {
			t.Fatal(dispatchErr)
		}
		return response
	}
	first := invoke("resource_claim", input)
	if first.Outcome != OutcomeOK {
		t.Fatalf("claim failed: %+v", first.Error)
	}
	replay := invoke("resource_claim", input)
	if replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v", replay)
	}
	if version, versionErr := s.WorkVersion(ctx, "work-holder"); versionErr != nil || version != 3 {
		t.Fatalf("replayed claim version=%d err=%v", version, versionErr)
	}
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.resource_claimed' AND subject_id=?`, "work-holder").Scan(&events); err != nil || events != 1 {
		t.Fatalf("claim events=%d err=%v", events, err)
	}

	stale := invoke("resource_claim", map[string]any{"work_id": "work-holder", "resource_key": "fence:other/pause", "reason": "A stale claim.", "expected_version": 2, "idempotency_key": "claim-stale"})
	if stale.Outcome != OutcomeError || stale.Error == nil {
		t.Fatalf("stale claim=%+v", stale)
	}
	if version, versionErr := s.WorkVersion(ctx, "work-holder"); versionErr != nil || version != 3 {
		t.Fatalf("stale claim version=%d err=%v", version, versionErr)
	}

	ownership := invoke("resource_release", map[string]any{"work_id": "work-contender", "resource_key": "fence:prod/pause", "expected_version": 2, "idempotency_key": "release-not-owner"})
	if ownership.Outcome != OutcomeError || ownership.Error == nil {
		t.Fatalf("ownership refusal=%+v", ownership)
	}
	var state, holder string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state,holder_work_id FROM resource_claims WHERE resource_key=?`, "fence:prod/pause").Scan(&state, &holder); err != nil {
		t.Fatal(err)
	}
	if state != store.ResourceClaimHeld || holder != "work-holder" {
		t.Fatalf("claim after ownership refusal state=%q holder=%q", state, holder)
	}

	atomicEvents := []store.Event{
		{EventID: "atomic-valid", Kind: "work.resource_claimed", SubjectType: store.SubjectWorkItem, SubjectID: "work-contender", Actor: grant.PrincipalRef, OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"expected_version":2,"resulting_version":3,"resource_key":"fence:atomic/valid","reason":"atomicity","holder_agent":"agent-1","holder_session":"session-1"}`)},
		{EventID: "atomic-invalid", Kind: "work.resource_claimed", SubjectType: store.SubjectWorkItem, SubjectID: "work-contender", Actor: grant.PrincipalRef, OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"expected_version":3,"resulting_version":4,"resource_key":"invalid key","reason":"must fail","holder_agent":"agent-1","holder_session":"session-1"}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: atomicEvents, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-contender"): 2}}); err == nil {
		t.Fatal("invalid second claim must refuse")
	}
	if version, versionErr := s.WorkVersion(ctx, "work-contender"); versionErr != nil || version != 2 {
		t.Fatalf("partial atomic claim version=%d err=%v", version, versionErr)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM resource_claims WHERE resource_key=?`, "fence:atomic/valid").Scan(&events); err != nil || events != 0 {
		t.Fatalf("partial atomic claim rows=%d err=%v", events, err)
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
