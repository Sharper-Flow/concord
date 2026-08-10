package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestDispatchApprovalChallengeRoundTripIsDurableAndSingleUse(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-1"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Outcome != OutcomeError || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("missing approval response=%+v err=%v", missing, err)
	}
	details := missing.Error.Details
	challengeRef, ok := details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval challenge ref=%v", details["approval_ref"])
	}
	var status string
	var used, maxUses int
	if err := s.DB().QueryRow(`SELECT status,used_count,max_uses FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&status, &used, &maxUses); err != nil {
		t.Fatal(err)
	}
	if status != "active" || used != 0 || maxUses != 1 {
		t.Fatalf("challenge state status=%s used=%d max=%d", status, used, maxUses)
	}

	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 2}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "approval-nonce-0001")
	request.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-1","approval":{"approval_ref":"` + challengeRef + `"}}`)
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved response=%+v err=%v", approved, err)
	}
	if usedCount(t, s.DB(), "SELECT used_count FROM agent_approval_challenges WHERE challenge_ref=?", challengeRef) != 1 {
		t.Fatal("challenge was not consumed exactly once")
	}
	if usedCount(t, s.DB(), "SELECT used_count FROM agent_approvals WHERE approval_ref=(SELECT approval_ref FROM agent_approvals WHERE protected_evidence_ref=?)", "approval-challenge:"+challengeRef) != 1 {
		t.Fatal("issued approval was not consumed exactly once")
	}
	if version := workVersion(t, s, "work-1"); version != 3 {
		t.Fatalf("work version=%d, want 3", version)
	}

	second := request
	second.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-2","approval":{"approval_ref":"` + challengeRef + `"}}`)
	secondEnv := env
	secondEnv.RequestID = "approval-second"
	secondEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "approval-nonce-0002")
	reused, err := Dispatch(ctx, s, service, second, secondEnv)
	if err != nil || reused.Outcome != OutcomeError || reused.Error == nil || reused.Error.Kind != "approval_invalid" {
		t.Fatalf("reused challenge response=%+v err=%v", reused, err)
	}
	if version := workVersion(t, s, "work-1"); version != 3 {
		t.Fatalf("reused challenge changed work version=%d", version)
	}
}

func TestDispatchRejectsInvalidSignedHostApprovalAssertions(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-invalid"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil {
		t.Fatal(err)
	}
	challengeRef := missing.Error.Details["approval_ref"].(string)
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 2}
	variants := []struct {
		name      string
		assertion *HostApprovalAssertion
	}{
		{"unsigned", &HostApprovalAssertion{ChallengeRef: challengeRef, RequestDigest: digest, Scope: approvalScopeBindings(scope), Versions: approvalVersionBindings(versions), SessionRef: "session-1", AgentRef: "agent-1", Worktree: "/repo-wt", ClientVersion: "1.0.0", IssuedAt: fixedTime().Format(time.RFC3339Nano), Nonce: "invalid-unsigned-0001"}},
		{"wrong-key", signedHostApproval(mustKey(t), challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "invalid-wrong-key-1")},
		{"stale", signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime().Add(-service.MaxClockSkew-time.Second), "invalid-stale-0001")},
		{"future", signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime().Add(service.MaxClockSkew+time.Second), "invalid-future-0001")},
		{"digest", signedHostApproval(privateKey, challengeRef, "sha256:"+strings.Repeat("f", 64), scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "invalid-digest-0001")},
		{"version", signedHostApproval(privateKey, challengeRef, digest, scope, map[string]any{"work": 3}, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "invalid-version-0001")},
		{"scope", signedHostApproval(privateKey, challengeRef, digest, map[string]any{"product_id": "product-2", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "invalid-scope-0001")},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			trial := request
			trial.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-` + variant.name + `","approval":{"approval_ref":"` + challengeRef + `"}}`)
			trialEnv := env
			trialEnv.RequestID = "invalid-" + variant.name
			trialEnv.HostApproval = variant.assertion
			response, err := Dispatch(ctx, s, service, trial, trialEnv)
			if err != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "approval_invalid" {
				t.Fatalf("response=%+v err=%v", response, err)
			}
		})
	}
	if _, err := s.DB().Exec(`UPDATE agent_approval_challenges SET issued_at=?,expires_at=? WHERE challenge_ref=?`, fixedTime().Add(-2*time.Minute).Format(time.RFC3339Nano), fixedTime().Add(-time.Minute).Format(time.RFC3339Nano), challengeRef); err != nil {
		t.Fatal(err)
	}
	expired := request
	expired.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-expired","approval":{"approval_ref":"` + challengeRef + `"}}`)
	expiredEnv := env
	expiredEnv.RequestID = "invalid-expired"
	expiredEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "invalid-expired-0001")
	expiredResponse, err := Dispatch(ctx, s, service, expired, expiredEnv)
	if err != nil || expiredResponse.Error == nil || expiredResponse.Error.Kind != "approval_invalid" {
		t.Fatalf("expired challenge response=%+v err=%v", expiredResponse, err)
	}
	newChallengeRequest := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-revoked"}`)}
	newChallengeResponse, err := Dispatch(ctx, s, service, newChallengeRequest, env)
	if err != nil {
		t.Fatal(err)
	}
	newChallenge := newChallengeResponse.Error.Details["approval_ref"].(string)
	if err := service.RevokeApprovalChallenge(ctx, newChallenge); err != nil {
		t.Fatal(err)
	}
	revoked := newChallengeRequest
	revoked.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-revoked","approval":{"approval_ref":"` + newChallenge + `"}}`)
	revokedEnv := env
	revokedEnv.RequestID = "invalid-revoked"
	revokedEnv.HostApproval = signedHostApproval(privateKey, newChallenge, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "invalid-revoked-0001")
	revokedResponse, err := Dispatch(ctx, s, service, revoked, revokedEnv)
	if err != nil || revokedResponse.Error == nil || revokedResponse.Error.Kind != "approval_invalid" {
		t.Fatalf("revoked challenge response=%+v err=%v", revokedResponse, err)
	}
	if err := service.RevokeClient(ctx, "client-1"); err != nil {
		t.Fatal(err)
	}
	revokedClient, err := Dispatch(ctx, s, service, revoked, revokedEnv)
	if err != nil || revokedClient.Error == nil || revokedClient.Error.Kind != "unauthorized" {
		t.Fatalf("revoked client response=%+v err=%v", revokedClient, err)
	}
	if version := workVersion(t, s, "work-1"); version != 2 {
		t.Fatalf("invalid assertions changed work version=%d", version)
	}
}

func TestDispatchFailedDomainEffectRollsBackGrantAndApproval(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"rollback-approval"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil {
		t.Fatal(err)
	}
	challengeRef := missing.Error.Details["approval_ref"].(string)
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{EventID: "rollback-version", Kind: "work.intent_revised", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"title":"Changed","value_statement":"Changed","kind":"task","priority":1,"tags":[],"reason":"race","expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-1"): 2}}); err != nil {
		t.Fatal(err)
	}
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, map[string]any{"work": 2}, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "rollback-approval-0001")
	request.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"rollback-approval","approval":{"approval_ref":"` + challengeRef + `"}}`)
	failed, err := Dispatch(ctx, s, service, request, env)
	if err != nil || failed.Outcome != OutcomeError || failed.Error == nil || failed.Error.Kind != "version_conflict" {
		t.Fatalf("failed mutation=%+v err=%v", failed, err)
	}
	var grantUsed int
	if err := s.DB().QueryRow(`SELECT used_count FROM agent_grants WHERE grant_ref=?`, grant.RecordID).Scan(&grantUsed); err != nil {
		t.Fatal(err)
	}
	if grantUsed != 0 {
		t.Fatalf("grant use committed on failed effect: %d", grantUsed)
	}
	var challengeStatus string
	var challengeUsed int
	if err := s.DB().QueryRow(`SELECT status,used_count FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&challengeStatus, &challengeUsed); err != nil {
		t.Fatal(err)
	}
	if challengeStatus != "active" || challengeUsed != 0 {
		t.Fatalf("approval challenge consumed on failed effect: status=%s used=%d", challengeStatus, challengeUsed)
	}
}

func TestDispatchReconcileLinksVerifiedOrphanWithoutSecondNote(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	repo := t.TempDir()
	if err := exec.Command("git", "init", "--quiet", repo).Run(); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repo, "config", "user.email", "test@example.invalid").Run()
	_ = exec.Command("git", "-C", repo, "config", "user.name", "Concord Test").Run()
	content := "---\nconcord_work_id: work-orphan\nwork_type: task\ntitle: Orphan\ncompleted_at: 2026-08-07T00:00:00Z\noutcome_tag: shipped\nlesson_tags: [test]\nterminal_state: completed\npriority: 1\nsummary: Orphan summary\nproduct_ids: [product-1]\nproject_ids: [project-1]\ncomponent_ids: []\ntag_ids: []\n---\n\nOrphan.\n"
	sum := sha256.Sum256([]byte(content))
	proofDigest := "sha256:" + hex.EncodeToString(sum[:])
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "orphan-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "orphan-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "orphan-product-project", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "orphan-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Orphan","priority":1}`)},
		{EventID: "orphan-work-project", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "orphan-complete", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-orphan"): 0}}); err != nil {
		t.Fatal(err)
	}
	notePath := filepath.Join(repo, "docs/work/2026-08-07-orphan-work.md")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "add", ".").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "commit", "--quiet", "-m", "orphan note").Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-1','project-1','canonical_path',?,?,'now','now'); DELETE FROM fold_guard; INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-1','project-1','locator-1')`, repo, repo); err != nil {
		t.Fatal(err)
	}
	service := NewService(s.DB())
	service.Now = func() time.Time { return fixedTime() }
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"work_compact"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	grantReq := grantRequest(privateKey, "orphan-dispatch-nonce")
	grantReq.Assertion.Worktree = "/unrelated-worktree"
	grantReq.Assertion.RequestedCapabilities = []Capability{"work_compact"}
	grantReq.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(grantReq.Assertion))
	grant, err := service.IssueGrant(ctx, grantReq)
	if err != nil {
		t.Fatal(err)
	}
	claimScope := `{"product_id":"product-1","project_ids":["project-1"],"work_ids":["work-orphan"],"scope_version":"","work_version":3,"content_digest":"` + proofDigest + `","home_project_id":"project-1","home_locator_id":"locator-1","head_ref":"HEAD"}`
	claim, err := store.ClaimStep(ctx, s, store.ClaimRequest{OpID: "orphan-operation", WorkID: "work-orphan", WorkflowTypeRef: "concord.pm6.compaction", WorkflowTypeVersion: 1, StepID: "git_proof", StepKind: store.StepCrossAuthority, AcceptedInputsDigest: "sha256:" + strings.Repeat("0", 64), AcceptedScopeSnapshot: claimScope, PrincipalRef: grant.PrincipalRef, Tool: "concord_work_compact", IdempotencyKey: "publish-key", RequestID: "publish-request", ObservedAt: fixedTime()})
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "reconcile", Input: json.RawMessage(`{"operation_id":"orphan-operation","expected_operation_version":` + strconv.FormatInt(claim.AttemptEpoch, 10) + `,"expected_proof_digest":"` + proofDigest + `","idempotency_key":"reconcile-key"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	env.Worktree = "/unrelated-worktree"
	env.RequestID = "reconcile-request"
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("reconcile=%+v err=%v", response, err)
	}
	if count := countRows(t, s.DB(), `SELECT count(*) FROM archived_work WHERE id='work-orphan'`); count != 1 {
		t.Fatalf("orphan link count=%d", count)
	}
	if count := countRows(t, s.DB(), `SELECT count(*) FROM domain_events WHERE kind='compaction_link.published' AND subject_id='work-orphan'`); count != 1 {
		t.Fatalf("compaction event count=%d", count)
	}
}

func TestDispatchIdempotentReplaySurvivesAmbientScopeDrift(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_define"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Replay","value_statement":"Replay value","kind":"task","project_ids":["project-1"],"idempotency_key":"replay-key"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	first, err := Dispatch(ctx, s, service, request, env)
	if err != nil || first.Outcome != OutcomeOK {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	var eventsBefore int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	env.ScopeVersion = "drifted-scope-version"
	env.RequestID = "replay-after-drift"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var eventsAfter int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("replay created effects: before=%d after=%d", eventsBefore, eventsAfter)
	}
	request.Input = json.RawMessage(`{"title":"Changed","value_statement":"Replay value","kind":"task","project_ids":["project-1"],"idempotency_key":"replay-key"}`)
	conflict, err := Dispatch(ctx, s, service, request, env)
	if err != nil || conflict.Outcome != OutcomeError || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
}

func TestDispatchCrossProductCaptureRequiresBoundApproval(t *testing.T) {
	ctx := context.Background()
	deniedStore, deniedService, deniedGrant, _ := crossProductDispatchFixture(t, []Capability{"work_define"})
	deniedScope, _, err := deniedStore.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Cross","value_statement":"Cross value","kind":"task","project_ids":["project-1"],"idempotency_key":"cross-denied"}`)}
	denied, err := Dispatch(ctx, deniedStore, deniedService, request, mutationEnvelope(deniedGrant, deniedScope))
	if err != nil || denied.Outcome != OutcomeError || denied.Error == nil || denied.Error.Kind != "unauthorized" {
		t.Fatalf("cross-product denial=%+v err=%v", denied, err)
	}
	if count := countRows(t, deniedStore.DB(), `SELECT count(*) FROM work_items WHERE title='Cross'`); count != 0 {
		t.Fatalf("denied capture created %d work items", count)
	}

	s, service, grant, privateKey := crossProductDispatchFixture(t, []Capability{"work_define", "cross_scope"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request = InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Cross","value_statement":"Cross value","kind":"task","project_ids":["project-1"],"idempotency_key":"cross-approved"}`)}
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("cross approval response=%+v kind=%s msg=%s details=%v err=%v", challenge, challenge.Error.Kind, challenge.Error.Message, challenge.Error.Details, err)
	}
	challengeRef := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_ids": []string{"project-1"}, "scope_version": scopeVersion}
	assertion := signedHostApproval(privateKey, challengeRef, digest, scope, map[string]any{}, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "cross-approval-0001")
	request.Input = json.RawMessage(`{"title":"Cross","value_statement":"Cross value","kind":"task","project_ids":["project-1"],"idempotency_key":"cross-approved","approval":{"approval_ref":"` + challengeRef + `"}}`)
	env.HostApproval = assertion
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("cross approved response=%+v err=%v", approved, err)
	}
	if approved.ResolvedScope == nil || len(approved.ResolvedScope.ProductIDs) != 2 || approved.ResolvedScope.ProductIDs[0] != "product-1" || approved.ResolvedScope.ProductIDs[1] != "product-2" {
		t.Fatalf("resulting Product scope=%+v", approved.ResolvedScope)
	}
	if count := countRows(t, s.DB(), `SELECT count(*) FROM work_items WHERE title='Cross'`); count != 1 {
		t.Fatalf("approved capture work count=%d", count)
	}
}

func TestDispatchRelationLinkAndUnlinkResolveEndpointVersionsAndRelationID(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "relation-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Second","priority":1}`)},
		{EventID: "relation-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	missingRelation := InvokeRequest{Tool: "concord_work_relate", Operation: "unlink", Input: json.RawMessage(`{"relation_id":"999999","expected_versions":[{"work_id":"work-1","version":2},{"work_id":"work-2","version":2}],"reason":"missing","idempotency_key":"relation-missing"}`)}
	missingResponse, err := Dispatch(ctx, s, service, missingRelation, env)
	if err != nil || missingResponse.Outcome != OutcomeError || missingResponse.Error == nil || missingResponse.Error.Kind != "invalid_relation" {
		t.Fatalf("missing relation response=%+v err=%v", missingResponse, err)
	}
	link := InvokeRequest{Tool: "concord_work_relate", Operation: "link", Input: json.RawMessage(`{"from_work_id":"work-1","to_work_id":"work-2","from_expected_version":2,"to_expected_version":2,"kind":"blocks","reason":"dispatcher relation","idempotency_key":"relation-link-1"}`)}
	linked, err := Dispatch(ctx, s, service, link, env)
	if err != nil || linked.Outcome != OutcomeOK {
		t.Fatalf("link=%+v err=%v", linked, err)
	}
	var relationID int64
	if err := s.DB().QueryRow(`SELECT id FROM relations WHERE work_id_from='work-1' AND work_id_to='work-2' AND kind='blocks'`).Scan(&relationID); err != nil {
		t.Fatal(err)
	}
	if workVersion(t, s, "work-1") != 3 || workVersion(t, s, "work-2") != 3 {
		t.Fatal("link did not version both endpoints")
	}
	unlink := InvokeRequest{Tool: "concord_work_relate", Operation: "unlink", Input: json.RawMessage(`{"relation_id":"` + strconv.FormatInt(relationID, 10) + `","expected_versions":[{"work_id":"work-1","version":3},{"work_id":"work-2","version":3}],"reason":"dispatcher unlink","idempotency_key":"relation-unlink-1"}`)}
	unlinked, err := Dispatch(ctx, s, service, unlink, env)
	if err != nil || unlinked.Outcome != OutcomeOK {
		t.Fatalf("unlink=%+v err=%v", unlinked, err)
	}
	if count := countRows(t, s.DB(), `SELECT count(*) FROM relations WHERE id=`+strconv.FormatInt(relationID, 10)); count != 0 {
		t.Fatalf("relation-ID unlink left %d rows", count)
	}
}

func TestDispatchCrossProductLinkRequiresCapabilityAndApproval(t *testing.T) {
	ctx := context.Background()
	sDenied, serviceDenied, grantDenied, _ := crossProductDispatchFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := sDenied.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	link := InvokeRequest{Tool: "concord_work_relate", Operation: "link", Input: json.RawMessage(`{"from_work_id":"work-1","to_work_id":"work-2","from_expected_version":2,"to_expected_version":2,"kind":"blocks","reason":"cross relation","idempotency_key":"cross-link-denied"}`)}
	denied, err := Dispatch(ctx, sDenied, serviceDenied, link, mutationEnvelope(grantDenied, scopeVersion))
	if err != nil || denied.Error == nil || denied.Error.Kind != "unauthorized" {
		t.Fatalf("cross link without capability=%+v err=%v", denied, err)
	}
	if count := countRows(t, sDenied.DB(), `SELECT count(*) FROM relations`); count != 0 {
		t.Fatalf("denied cross link created %d relations", count)
	}

	s, service, grant, privateKey := crossProductDispatchFixture(t, []Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	challenge, err := Dispatch(ctx, s, service, link, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("cross link missing approval=%+v grant_products=%v kind=%s msg=%s details=%v err=%v", challenge, grant.ProductScope, challenge.Error.Kind, challenge.Error.Message, challenge.Error.Details, err)
	}
	challengeRef := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest(link.Tool, link.Operation, env, link.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1", "work-2"}, "scope_version": scopeVersion}
	versions := map[string]any{"from": 2, "to": 2}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "cross-link-approval-1")
	link.Input = json.RawMessage(`{"from_work_id":"work-1","to_work_id":"work-2","from_expected_version":2,"to_expected_version":2,"kind":"blocks","reason":"cross relation","idempotency_key":"cross-link-denied","approval":{"approval_ref":"` + challengeRef + `"}}`)
	approved, err := Dispatch(ctx, s, service, link, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("cross link approved=%+v err=%v", approved, err)
	}
	if count := countRows(t, s.DB(), `SELECT count(*) FROM relations WHERE kind='blocks'`); count != 1 {
		t.Fatalf("approved cross link count=%d", count)
	}
}

func TestDispatchDisjointWorkCrossScopeLinkAndRelationUnlink(t *testing.T) {
	ctx := context.Background()
	sDenied, serviceDenied, grantDenied, _ := disjointRelationFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := sDenied.ScopeVersion(ctx, "ambient")
	if err != nil {
		t.Fatal(err)
	}
	link := InvokeRequest{Tool: "concord_work_relate", Operation: "link", Input: json.RawMessage(`{"from_work_id":"work-a","to_work_id":"work-b","from_expected_version":2,"to_expected_version":2,"kind":"blocks","reason":"disjoint","idempotency_key":"disjoint-link-denied"}`)}
	denied, err := Dispatch(ctx, sDenied, serviceDenied, link, disjointEnvelope(grantDenied, scopeVersion))
	if err != nil || denied.Error == nil || denied.Error.Kind != "unauthorized" {
		t.Fatalf("disjoint link denial=%+v err=%v", denied, err)
	}
	if count := countRows(t, sDenied.DB(), `SELECT count(*) FROM relations`); count != 0 {
		t.Fatalf("denied disjoint link created %d relations", count)
	}

	s, service, grant, privateKey := disjointRelationFixture(t, []Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err = s.ScopeVersion(ctx, "ambient")
	if err != nil {
		t.Fatal(err)
	}
	env := disjointEnvelope(grant, scopeVersion)
	challenge, err := Dispatch(ctx, s, service, link, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("disjoint link challenge=%+v err=%v", challenge, err)
	}
	challengeRef := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest(link.Tool, link.Operation, env, link.Input)
	scope := map[string]any{"product_id": "product-a", "product_ids": []string{"product-a", "product-b"}, "project_ids": []string{"ambient"}, "work_ids": []string{"work-a", "work-b"}, "scope_version": scopeVersion}
	versions := map[string]any{"from": 2, "to": 2}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "disjoint-link-approval")
	link.Input = json.RawMessage(`{"from_work_id":"work-a","to_work_id":"work-b","from_expected_version":2,"to_expected_version":2,"kind":"blocks","reason":"disjoint","idempotency_key":"disjoint-link-denied","approval":{"approval_ref":"` + challengeRef + `"}}`)
	approved, err := Dispatch(ctx, s, service, link, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("disjoint link approval=%+v err=%v", approved, err)
	}
	if count := countRows(t, s.DB(), `SELECT count(*) FROM relations WHERE kind='blocks'`); count != 1 {
		t.Fatalf("disjoint link count=%d", count)
	}

	var relationID int64
	if err := s.DB().QueryRow(`SELECT id FROM relations WHERE kind='blocks'`).Scan(&relationID); err != nil {
		t.Fatal(err)
	}
	unlink := InvokeRequest{Tool: "concord_work_relate", Operation: "unlink", Input: json.RawMessage(`{"relation_id":"` + strconv.FormatInt(relationID, 10) + `","expected_versions":[{"work_id":"work-a","version":3},{"work_id":"work-b","version":3}],"reason":"disjoint unlink","idempotency_key":"disjoint-unlink"}`)}
	deniedUnlink, err := Dispatch(ctx, sDenied, serviceDenied, unlink, disjointEnvelope(grantDenied, scopeVersion))
	if err != nil || deniedUnlink.Error == nil || deniedUnlink.Error.Kind != "invalid_relation" && deniedUnlink.Error.Kind != "unknown_scope" && deniedUnlink.Error.Kind != "unauthorized" {
		t.Fatalf("disjoint unlink denial=%+v err=%v", deniedUnlink, err)
	}
	challengeUnlink, err := Dispatch(ctx, s, service, unlink, env)
	if err != nil || challengeUnlink.Error == nil || challengeUnlink.Error.Kind != "approval_required" {
		t.Fatalf("disjoint unlink challenge=%+v err=%v", challengeUnlink, err)
	}
	unlinkRef := challengeUnlink.Error.Details["approval_ref"].(string)
	unlinkDigest := mutationDigest(unlink.Tool, unlink.Operation, env, unlink.Input)
	unlinkScope := map[string]any{"product_id": "product-a", "product_ids": []string{"product-a", "product-b"}, "project_ids": []string{"ambient"}, "work_ids": []string{"work-a", "work-b"}, "scope_version": scopeVersion}
	env.HostApproval = signedHostApproval(privateKey, unlinkRef, unlinkDigest, unlinkScope, map[string]any{"from": 3, "to": 3}, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "disjoint-unlink-approval")
	unlink.Input = json.RawMessage(`{"relation_id":"` + strconv.FormatInt(relationID, 10) + `","expected_versions":[{"work_id":"work-a","version":3},{"work_id":"work-b","version":3}],"reason":"disjoint unlink","idempotency_key":"disjoint-unlink","approval":{"approval_ref":"` + unlinkRef + `"}}`)
	removed, err := Dispatch(ctx, s, service, unlink, env)
	if err != nil || removed.Outcome != OutcomeOK {
		t.Fatalf("disjoint unlink approval=%+v err=%v", removed, err)
	}
	if count := countRows(t, s.DB(), `SELECT count(*) FROM relations WHERE id=`+strconv.FormatInt(relationID, 10)); count != 0 {
		t.Fatalf("disjoint unlink persisted relation")
	}
}

func TestDispatchDisjointCrossScopeSupersedeIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	input := json.RawMessage(`{"predecessor_id":"work-a","successor_id":"work-b","predecessor_expected_version":2,"successor_expected_version":2,"reason":"replace disjoint work","idempotency_key":"disjoint-supersede"}`)
	sDenied, serviceDenied, grantDenied, _ := disjointRelationFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := sDenied.ScopeVersion(ctx, "ambient")
	if err != nil {
		t.Fatal(err)
	}
	envDenied := disjointEnvelope(grantDenied, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "supersede", Input: input}
	beforeEvents := countRows(t, sDenied.DB(), `SELECT count(*) FROM domain_events`)
	denied, err := Dispatch(ctx, sDenied, serviceDenied, request, envDenied)
	if err != nil || denied.Error == nil || denied.Error.Kind != "unauthorized" {
		t.Fatalf("supersede without capability=%+v err=%v", denied, err)
	}
	if version := workVersion(t, sDenied, "work-a"); version != 2 {
		t.Fatalf("denied predecessor version=%d", version)
	}
	if lifecycle := workLifecycle(t, sDenied, "work-a"); lifecycle != "needed" {
		t.Fatalf("denied predecessor lifecycle=%s", lifecycle)
	}
	if count := countRows(t, sDenied.DB(), `SELECT count(*) FROM relations`); count != 0 {
		t.Fatalf("denied supersede relations=%d", count)
	}
	if after := countRows(t, sDenied.DB(), `SELECT count(*) FROM domain_events`); after != beforeEvents {
		t.Fatalf("denied supersede events before=%d after=%d", beforeEvents, after)
	}

	s, service, grant, privateKey := disjointRelationFixture(t, []Capability{"work_relate", "cross_scope"})
	scopeVersion, _, err = s.ScopeVersion(ctx, "ambient")
	if err != nil {
		t.Fatal(err)
	}
	env := disjointEnvelope(grant, scopeVersion)
	challenge, err := Dispatch(ctx, s, service, request, env)
	if err != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("supersede challenge=%+v err=%v", challenge, err)
	}
	challengeRef := challenge.Error.Details["approval_ref"].(string)
	digest := mutationDigest(request.Tool, request.Operation, env, input)
	scope := map[string]any{"product_id": "product-a", "product_ids": []string{"product-a", "product-b"}, "project_ids": []string{"ambient"}, "work_ids": []string{"work-a", "work-b"}, "scope_version": scopeVersion}
	versions := map[string]any{"predecessor": 2, "successor": 2}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "disjoint-supersede-approval")
	request.Input = json.RawMessage(`{"predecessor_id":"work-a","successor_id":"work-b","predecessor_expected_version":2,"successor_expected_version":2,"reason":"replace disjoint work","idempotency_key":"disjoint-supersede","approval":{"approval_ref":"` + challengeRef + `"}}`)
	beforeEvents = countRows(t, s.DB(), `SELECT count(*) FROM domain_events`)
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved supersede=%+v err=%v", approved, err)
	}
	if approved.ResolvedScope == nil || len(approved.ResolvedScope.ProductIDs) != 2 {
		t.Fatalf("supersede scope=%+v", approved.ResolvedScope)
	}
	if lifecycle := workLifecycle(t, s, "work-a"); lifecycle != "superseded" {
		t.Fatalf("superseded lifecycle=%s", lifecycle)
	}
	if version := workVersion(t, s, "work-a"); version != 3 {
		t.Fatalf("superseded predecessor version=%d", version)
	}
	if version := workVersion(t, s, "work-b"); version != 3 {
		t.Fatalf("superseded successor version=%d", version)
	}
	if count := countRows(t, s.DB(), `SELECT count(*) FROM relations WHERE kind='supersedes' AND work_id_from='work-b' AND work_id_to='work-a'`); count != 1 {
		t.Fatalf("supersession edge count=%d", count)
	}
	if after := countRows(t, s.DB(), `SELECT count(*) FROM domain_events`); after != beforeEvents+1 {
		t.Fatalf("supersede event count before=%d after=%d", beforeEvents, after)
	}

	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("supersede replay=%+v err=%v", replay, err)
	}
	if after := countRows(t, s.DB(), `SELECT count(*) FROM domain_events`); after != beforeEvents+1 {
		t.Fatalf("supersede replay created another event: %d", after)
	}
}

func crossProductDispatchFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Grant, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "cross-product-1", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product One","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-product-2", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product Two","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Shared Project"}`)},
		{EventID: "cross-membership-1", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-membership-2", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-1","role":"secondary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-work-1", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Cross One","priority":1}`)},
		{EventID: "cross-work-1-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Cross Two","priority":1}`)},
		{EventID: "cross-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProduct, "product-2"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(s.DB())
	service.Now = func() time.Time { return fixedTime() }
	service.ProjectResolver = func(context.Context, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "project-1"}, nil
	}
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	policy := TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: capabilities, ProductScope: []string{"product-1", "product-2"}, ProjectScope: []string{"project-1"}}
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	grantReq := grantRequest(privateKey, "cross-fixture-nonce")
	grantReq.Assertion.RequestedCapabilities = capabilities
	grantReq.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(grantReq.Assertion))
	grant, err := service.IssueGrant(ctx, grantReq)
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, privateKey
}

func disjointRelationFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Grant, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "disjoint-product-a", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-a", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"A","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "disjoint-project-a", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-a", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project A"}`)},
		{EventID: "disjoint-ambient-a", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "ambient", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Ambient"}`)},
		{EventID: "disjoint-a-pa", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-a", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-a","project_id":"project-a","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "disjoint-a-ambient", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-a", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-a","project_id":"ambient","role":"secondary","reason":"fixture","expected_version":2,"resulting_version":3}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-a"): 0, store.VersionRef(store.SubjectProject, "project-a"): 0, store.VersionRef(store.SubjectProject, "ambient"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "disjoint-product-b", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-b", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"B","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "disjoint-project-b", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-b", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project B"}`)},
		{EventID: "disjoint-b-pb", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-b", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-b","project_id":"project-b","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "disjoint-b-ambient", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-b", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-b","project_id":"ambient","role":"secondary","reason":"fixture","expected_version":2,"resulting_version":3}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-b"): 0, store.VersionRef(store.SubjectProject, "project-b"): 0}}); err != nil {
		t.Fatal(err)
	}
	for _, idProject := range []struct{ id, project string }{{"work-a", "project-a"}, {"work-b", "project-b"}} {
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{EventID: idProject.id + "-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: idProject.id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"` + idProject.id + `","priority":1}`)}, {EventID: idProject.id + "-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: idProject.id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"` + idProject.project + `","role":"primary"}],"expected_version":1,"resulting_version":2}`)}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, idProject.id): 0}}); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(s.DB())
	service.Now = func() time.Time { return fixedTime() }
	service.ProjectResolver = func(context.Context, string, string) (store.ProjectResolution, error) {
		return store.ProjectResolution{ProjectID: "ambient"}, nil
	}
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	policy := TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: capabilities, ProductScope: []string{"product-a", "product-b"}, ProjectScope: []string{"ambient", "project-a", "project-b"}}
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	grantReq := grantRequest(privateKey, "disjoint-fixture-nonce")
	grantReq.Assertion.RequestedProductID = "product-a"
	grantReq.Assertion.RequestedProjectIDs = []string{"ambient"}
	grantReq.Assertion.RequestedCapabilities = capabilities
	grantReq.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(grantReq.Assertion))
	grant, err := service.IssueGrant(ctx, grantReq)
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, privateKey
}

func disjointEnvelope(grant Grant, scopeVersion string) CallEnvelope {
	env := mutationEnvelope(grant, scopeVersion)
	env.AmbientProjectID = "ambient"
	env.SelectedProductID = "product-a"
	return env
}

func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func mutationDispatchFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Grant, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "fixture-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "fixture-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "fixture-product-project", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "fixture-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Work","priority":1}`)},
		{EventID: "fixture-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(s.DB())
	service.Now = func() time.Time { return fixedTime() }
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: capabilities, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	grantReq := grantRequest(privateKey, "dispatcher-fixture-nonce")
	grantReq.Assertion.RequestedCapabilities = capabilities
	grantReq.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(grantReq.Assertion))
	grant, err := service.IssueGrant(ctx, grantReq)
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, privateKey
}

func mutationEnvelope(grant Grant, scopeVersion string) CallEnvelope {
	return CallEnvelope{SchemaVersion: "1.0", RequestID: "dispatcher-request", GrantRef: grant.Token, ClientRef: grant.ClientRef, ClientVersion: grant.ClientVersion, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, SurfaceVersion: grant.SurfaceVersion, EnvelopeVersion: grant.EnvelopeVersion, ManifestDigest: grant.ManifestDigest}
}

func signedHostApproval(privateKey ed25519.PrivateKey, challenge, digest string, scope, versions map[string]any, session, agent, worktree, clientVersion string, issued time.Time, nonce string) *HostApprovalAssertion {
	if clientVersion == "1.0.0" {
		clientVersion = ManifestVersion
	}
	assertion := &HostApprovalAssertion{ChallengeRef: challenge, RequestDigest: digest, Scope: approvalScopeBindings(scope), Versions: approvalVersionBindings(versions), SessionRef: session, AgentRef: agent, Worktree: worktree, ClientVersion: clientVersion, IssuedAt: issued.Format(time.RFC3339Nano), Nonce: nonce, OperatorPrincipalRef: "human-1", OperatorAgentRef: "operator:" + agent, OperatorSessionRef: "operator:" + session}
	assertion.Signature = ed25519.Sign(privateKey, CanonicalHostApprovalAssertion(*assertion))
	return assertion
}

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func usedCount(t *testing.T, db *sql.DB, query, arg string) int {
	t.Helper()
	var value int
	if err := db.QueryRow(query, arg).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func workVersion(t *testing.T, s *store.Store, id string) int64 {
	t.Helper()
	var version int64
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id=?`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func workLifecycle(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	var lifecycle string
	if err := s.DB().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	return lifecycle
}
