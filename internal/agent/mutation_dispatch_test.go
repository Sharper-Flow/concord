package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestDispatchRejectsObsoleteEpicSurfaceAndGenericInitiativeCapture(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_define"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	obsolete, obsoleteErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_epic", Operation: "create", Input: json.RawMessage(`{"title":"obsolete","value_statement":"must reject","project_ids":["project-1"],"idempotency_key":"obsolete"}`)}, env)
	if obsoleteErr == nil && (obsolete.Error == nil || obsolete.Error.Kind != "invalid_input" && obsolete.Error.Kind != "unauthorized") {
		t.Fatalf("obsolete Epic surface response=%+v err=%v", obsolete, obsoleteErr)
	}
	for _, kind := range []string{"epic", "initiative"} {
		response, responseErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(fmt.Sprintf(`{"title":"%s","value_statement":"must reject","kind":"%s","project_ids":["project-1"],"idempotency_key":"capture-%s"}`, kind, kind, kind))}, env)
		if responseErr == nil && (response.Error == nil || response.Error.Kind != "invalid_input") {
			t.Fatalf("generic %s capture response=%+v err=%v", kind, response, responseErr)
		}
	}
}

func TestDispatchApprovalChallengeRoundTripIsDurableAndSingleUse(t *testing.T) {
	t.Parallel()
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT status,used_count,max_uses FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&status, &used, &maxUses); err != nil {
		t.Fatal(err)
	}
	if status != "active" || used != 0 || maxUses != 1 {
		t.Fatalf("challenge state status=%s used=%d max=%d", status, used, maxUses)
	}

	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 2}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", fixedTime(), "approval-nonce-0001")
	request.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-1","approval":{"approval_ref":"` + challengeRef + `"}}`)
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved response=%+v err=%v", approved, err)
	}
	if usedCount(t, s.DatabaseForTesting(), "SELECT used_count FROM agent_approval_challenges WHERE challenge_ref=?", challengeRef) != 1 {
		t.Fatal("challenge was not consumed exactly once")
	}
	if usedCount(t, s.DatabaseForTesting(), "SELECT used_count FROM agent_approvals WHERE approval_ref=(SELECT approval_ref FROM agent_approvals WHERE protected_evidence_ref=?)", "approval-challenge:"+challengeRef) != 1 {
		t.Fatal("issued approval was not consumed exactly once")
	}
	if version := workVersion(t, s, "work-1"); version != 3 {
		t.Fatalf("work version=%d, want 3", version)
	}

	second := request
	second.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-2","approval":{"approval_ref":"` + challengeRef + `"}}`)
	secondEnv := env
	secondEnv.RequestID = "approval-second"
	secondEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", fixedTime(), "approval-nonce-0002")
	reused, err := Dispatch(ctx, s, service, second, secondEnv)
	if err != nil || reused.Outcome != OutcomeError || reused.Error == nil || reused.Error.Kind != "approval_invalid" {
		t.Fatalf("reused challenge response=%+v err=%v", reused, err)
	}
	if version := workVersion(t, s, "work-1"); version != 3 {
		t.Fatalf("reused challenge changed work version=%d", version)
	}
	// A late retry of the spent challenge refuses closed too: the consumed
	// state, not elapsed time, is what blocks it.
	service.Now = func() time.Time { return fixedTime().Add(11 * time.Minute) }
	secondEnv.RequestID = "approval-second-late"
	secondEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", fixedTime().Add(11*time.Minute), "approval-nonce-0003")
	lateReused, err := Dispatch(ctx, s, service, second, secondEnv)
	if err != nil || lateReused.Outcome != OutcomeError || lateReused.Error == nil || lateReused.Error.Kind != "approval_invalid" {
		t.Fatalf("late reused challenge response=%+v err=%v", lateReused, err)
	}
	if version := workVersion(t, s, "work-1"); version != 3 {
		t.Fatalf("late reused challenge changed work version=%d", version)
	}
}

// TestLateHostApprovalNeedsOneChallenge reproduces the late-answer exchange: the
// bounded host driver is the adapter bridge, so each approval_required
// challenge asks the operator once and resubmits the identical marshaled
// request, and an approval_invalid refusal sends the agent back to dispatch
// the same intent without the stale approval. The clock advances while the
// first answer is pending, so the operator answers a challenge that has
// already expired. One affirmative host answer must complete the intent by
// minting exactly one challenge and asking exactly once.
func TestLateHostApprovalNeedsOneChallenge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"late-answer-1"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	dispatchWire := func(request InvokeRequest, env CallEnvelope) Envelope {
		t.Helper()
		request.CallEnvelope, err = json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		decoded, decodedEnv, err := DecodeInvokeRequest(wire)
		if err != nil {
			t.Fatal(err)
		}
		response, err := Dispatch(ctx, s, service, decoded, decodedEnv)
		if err != nil {
			t.Fatal(err)
		}
		wire, err = json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(wire, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	answerAt := fixedTime().Add(11 * time.Minute)
	asks := 0
	var summary *ConsequenceSummary
	var approvalRef string
	var expiredRefusal *TypedError
	var approved Envelope
	for round := 0; round < 4; round++ {
		response := dispatchWire(request, env)
		switch {
		case response.Error != nil && response.Error.Kind == "approval_required":
			summary = response.Error.ConsequenceSummary
			approvalRef, _ = response.Error.Details["approval_ref"].(string)
			if summary == nil || len(approvalRef) != 64 {
				t.Fatalf("challenge lost its exact bindings: %+v", response.Error)
			}
			asks++
			// The prompt stays open past the ten-minute mint deadline while
			// the answer is pending; the operator's answer is fresh.
			service.Now = func() time.Time { return answerAt }
			var input map[string]any
			if err := json.Unmarshal(request.Input, &input); err != nil {
				t.Fatal(err)
			}
			input["approval"] = map[string]string{"approval_ref": approvalRef}
			request.Input, err = json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			env.HostApproval = &HostApprovalAssertion{ChallengeRef: approvalRef, RequestDigest: summary.OperationDigest, Scope: summary.Scope, Versions: summary.Versions, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Worktree: env.Worktree, IssuedAt: answerAt.Format(time.RFC3339Nano)}
		case response.Error != nil && response.Error.Kind == "approval_invalid":
			expiredRefusal = response.Error
			// The agent retries the same intent and drops the stale approval
			// and its host assertion.
			env.HostApproval = nil
			var input map[string]any
			if err := json.Unmarshal(request.Input, &input); err != nil {
				t.Fatal(err)
			}
			delete(input, "approval")
			request.Input, err = json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
		default:
			approved = response
			round = 4
		}
	}
	challenges := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
	if challenges != 1 || asks != 1 {
		t.Fatalf("one affirmative host answer must suffice: approval_required challenges=%d host asks=%d expired_refusal=%+v", challenges, asks, expiredRefusal)
	}
	if expiredRefusal != nil {
		t.Fatalf("the late affirmative answer was refused and demanded a second answer: %+v", expiredRefusal)
	}
	if approved.Outcome != OutcomeOK || approved.Error != nil {
		t.Fatalf("late answer did not complete the intent: %+v, error=%+v", approved, approved.Error)
	}
	if workVersion(t, s, "work-1") != 3 {
		t.Fatalf("late answer work version=%d, want exactly one mutation", workVersion(t, s, "work-1"))
	}
	if usedCount(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approval_challenges WHERE challenge_ref=?`, approvalRef) != 1 {
		t.Fatal("the late exchange did not consume the challenge exactly once")
	}
	if usedCount(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals WHERE approval_ref=(SELECT approval_ref FROM agent_approvals WHERE protected_evidence_ref=?)`, "approval-challenge:"+approvalRef) != 1 {
		t.Fatal("the late exchange did not consume the derived approval exactly once")
	}
	var issuedAt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT issued_at FROM agent_approval_challenges WHERE challenge_ref=?`, approvalRef).Scan(&issuedAt); err != nil {
		t.Fatal(err)
	}
	if issuedAt != fixedTime().Format(time.RFC3339Nano) {
		t.Fatalf("re-arm moved the challenge issue time to %s", issuedAt)
	}
	replayed := dispatchWire(request, env)
	if replayed.Outcome != OutcomeOK || !replayed.Replayed || workVersion(t, s, "work-1") != 3 {
		t.Fatalf("late-answer replay = %+v", replayed)
	}
}

func TestDispatchRejectsInvalidHostApprovalAssertions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, authority, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-invalid"}`)}
	env := mutationEnvelope(authority, scopeVersion)
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("missing approval response=%+v err=%v", missing, err)
	}
	challengeRef, ok := missing.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval challenge ref=%v", missing.Error.Details["approval_ref"])
	}
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 2}
	variants := []struct {
		name      string
		assertion *HostApprovalAssertion
	}{
		{"scope", signedHostApproval(nil, challengeRef, digest, map[string]any{"product_id": "product-2", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}, versions, "session-1", "agent-1", "/repo-wt", fixedTime(), "")},
		{"digest", signedHostApproval(nil, challengeRef, "sha256:"+strings.Repeat("f", 64), scope, versions, "session-1", "agent-1", "/repo-wt", fixedTime(), "")},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			trial := request
			trial.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"membership-` + variant.name + `","approval":{"approval_ref":"` + challengeRef + `"}}`)
			trialEnv := env
			trialEnv.RequestID = "invalid-" + variant.name
			trialEnv.HostApproval = variant.assertion
			response, dispatchErr := Dispatch(ctx, s, service, trial, trialEnv)
			if dispatchErr != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "approval_invalid" {
				t.Fatalf("response=%+v err=%v", response, dispatchErr)
			}
		})
	}
}

func TestDispatchFailedDomainEffectRollsBackGrantAndApproval(t *testing.T) {
	t.Parallel()
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
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, map[string]any{"work": 2}, "session-1", "agent-1", "/repo-wt", fixedTime(), "rollback-approval-0001")
	request.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"rollback-approval","approval":{"approval_ref":"` + challengeRef + `"}}`)
	failed, err := Dispatch(ctx, s, service, request, env)
	if err != nil || failed.Outcome != OutcomeError || failed.Error == nil || failed.Error.Kind != "version_conflict" {
		t.Fatalf("failed mutation=%+v err=%v", failed, err)
	}
	var challengeStatus string
	var challengeUsed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT status,used_count FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&challengeStatus, &challengeUsed); err != nil {
		t.Fatal(err)
	}
	if challengeStatus != "active" || challengeUsed != 0 {
		t.Fatalf("approval challenge consumed on failed effect: status=%s used=%d", challengeStatus, challengeUsed)
	}
}

// TestDispatchFailedDomainEffectRollsBackRearmedDeadline pins the atomicity of
// the pending-challenge exchange: when the domain effect fails after a late
// answer re-armed an expired invitation, the re-armed deadline, the derived
// approval, and the challenge's single-use state roll back together with the
// refused intent.
func TestDispatchFailedDomainEffectRollsBackRearmedDeadline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_relate"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "set_memberships", Input: json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"rollback-rearm"}`)}
	env := mutationEnvelope(grant, scopeVersion)
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil {
		t.Fatal(err)
	}
	challengeRef := missing.Error.Details["approval_ref"].(string)
	summary := missing.Error.ConsequenceSummary
	// A concurrent mutation moves the expected version while the prompt is
	// open, so the late exchange re-arms and then fails at the domain effect.
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{EventID: "rollback-rearm-version", Kind: "work.intent_revised", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"title":"Changed","value_statement":"Changed","kind":"task","priority":1,"tags":[],"reason":"race","expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-1"): 2}}); err != nil {
		t.Fatal(err)
	}
	answerAt := fixedTime().Add(11 * time.Minute)
	service.Now = func() time.Time { return answerAt }
	env.HostApproval = &HostApprovalAssertion{ChallengeRef: challengeRef, RequestDigest: summary.OperationDigest, Scope: summary.Scope, Versions: summary.Versions, SessionRef: env.SessionRef, AgentRef: env.AgentRef, Worktree: env.Worktree, IssuedAt: answerAt.Format(time.RFC3339Nano)}
	request.Input = json.RawMessage(`{"work_id":"work-1","expected_version":2,"memberships":[{"project_id":"project-1","role":"primary"}],"idempotency_key":"rollback-rearm","approval":{"approval_ref":"` + challengeRef + `"}}`)
	failed, err := Dispatch(ctx, s, service, request, env)
	if err != nil || failed.Outcome != OutcomeError || failed.Error == nil || failed.Error.Kind != "version_conflict" {
		t.Fatalf("failed late mutation=%+v err=%v", failed, err)
	}
	var challengeStatus, issuedAt, expiresAt string
	var challengeUsed int
	if err := s.DatabaseForTesting().QueryRow(`SELECT status, used_count, issued_at, expires_at FROM agent_approval_challenges WHERE challenge_ref=?`, challengeRef).Scan(&challengeStatus, &challengeUsed, &issuedAt, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if challengeStatus != "active" || challengeUsed != 0 || issuedAt != fixedTime().Format(time.RFC3339Nano) || expiresAt != fixedTime().Add(10*time.Minute).Format(time.RFC3339Nano) {
		t.Fatalf("failed late effect kept the re-armed deadline: status=%s used=%d issued=%s expires=%s", challengeStatus, challengeUsed, issuedAt, expiresAt)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 {
		t.Fatal("failed late effect kept a derived approval row")
	}
	if version := workVersion(t, s, "work-1"); version != 3 {
		t.Fatalf("failed late effect changed work version=%d", version)
	}
}

func TestDispatchReconcileLinksVerifiedOrphanWithoutSecondNote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
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
	content := "---\nconcord_work_id: work-orphan\nwork_type: task\ntitle: Orphan\ncompleted_at: 2026-08-07T00:00:00Z\noutcome_tag: shipped\nlesson_tags: [test]\nterminal_state: completed\npriority: 1\nsummary: Orphan summary\nproduct_ids: [product-1]\nproject_ids: [project-1]\ndomain_ids: []\ntag_ids: []\n---\n\nOrphan.\n"
	sum := sha256.Sum256([]byte(content))
	proofDigest := "sha256:" + hex.EncodeToString(sum[:])
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "orphan-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "orphan-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "orphan-product-project", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "orphan-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Orphan","priority":1}`)},
		{EventID: "orphan-work-project", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "orphan-complete", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "work-orphan", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-orphan"): 0}}); err != nil {
		t.Fatal(err)
	}
	notePath := filepath.Join(repo, ".concord/docs/work/2026-08-07-orphan-work.md")
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
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-1','project-1','canonical_path',?,?,'now','now'); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-1','project-1','locator-1'); DELETE FROM write_guard`, repo, repo); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_compact"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	grant.Worktree = "/unrelated-worktree"
	claimScope := `{"product_id":"product-1","project_ids":["project-1"],"work_ids":["work-orphan"],"scope_version":"","work_version":3,"content_digest":"` + proofDigest + `","home_project_id":"project-1","home_locator_id":"locator-1","head_ref":"HEAD"}`
	claim, err := store.ClaimStepAuthorized(ctx, s, store.ClaimRequest{OpID: "orphan-operation", WorkID: "work-orphan", WorkflowTypeRef: "concord.pm6.compaction", WorkflowTypeVersion: 1, StepID: "git_proof", StepKind: store.StepCrossAuthority, AcceptedInputsDigest: "sha256:" + strings.Repeat("0", 64), AcceptedScopeSnapshot: claimScope, PrincipalRef: grant.PrincipalRef, Tool: "concord_work_compact", IdempotencyKey: "publish-key", RequestID: "publish-request", ObservedAt: fixedTime(), ContractDigest: ManifestDigest}, nil)
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
	if count := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM archived_work WHERE id='work-orphan'`); count != 1 {
		t.Fatalf("orphan link count=%d", count)
	}
	if count := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind='compaction_link.published' AND subject_id='work-orphan'`); count != 1 {
		t.Fatalf("compaction event count=%d", count)
	}
}

func TestDispatchIdempotentReplaySurvivesAmbientScopeDrift(t *testing.T) {
	t.Parallel()
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	env.ScopeVersion = "drifted-scope-version"
	env.RequestID = "replay-after-drift"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsAfter); err != nil {
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
	t.Parallel()
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
	if count := countRows(t, deniedStore.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE title='Cross'`); count != 0 {
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
	assertion := signedHostApproval(privateKey, challengeRef, digest, scope, map[string]any{}, "session-1", "agent-1", "/repo-wt", fixedTime(), "cross-approval-0001")
	request.Input = json.RawMessage(`{"title":"Cross","value_statement":"Cross value","kind":"task","project_ids":["project-1"],"idempotency_key":"cross-approved","approval":{"approval_ref":"` + challengeRef + `"}}`)
	env.HostApproval = assertion
	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("cross approved response=%+v err=%v", approved, err)
	}
	// The derived cross-product set is authority bookkeeping: it binds the
	// stored idempotency snapshot, while resolved_scope carries only the
	// members the envelope law declares.
	var snapshot string
	if err := s.DatabaseForTesting().QueryRow(`SELECT authorized_scope_snapshot FROM idempotency_records WHERE idempotency_key=?`, "cross-approved").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, `"product_ids":["product-1","product-2"]`) {
		t.Fatalf("stored authorized scope snapshot=%s", snapshot)
	}
	if count := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM work_items WHERE title='Cross'`); count != 1 {
		t.Fatalf("approved capture work count=%d", count)
	}
}

func crossProductDispatchFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Authority, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "cross-product-1", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product One","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-product-2", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product Two","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "cross-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Shared Project"}`)},
		{EventID: "cross-membership-1", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-membership-2", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-2","project_id":"project-1","role":"secondary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-work-1", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Cross One","priority":1}`)},
		{EventID: "cross-work-1-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "cross-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Cross Two","priority":1}`)},
		{EventID: "cross-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProduct, "product-2"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, []string{"product-1", "product-2"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	privateKey := mustKey(t)
	return s, service, grant, privateKey
}

func countRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func mutationDispatchFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Authority, ed25519.PrivateKey) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "fixture-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("product.created"), Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "fixture-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "fixture-product-project", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("product_project.added"), Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "fixture-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Work","priority":1}`)},
		{EventID: "fixture-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.memberships_replaced"), Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	privateKey := mustKey(t)
	return s, service, grant, privateKey
}

func mutationEnvelope(grant Authority, scopeVersion string) CallEnvelope {
	return CallEnvelope{SchemaVersion: "1.0", RequestID: "dispatcher-request", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
}

func signedHostApproval(privateKey ed25519.PrivateKey, challenge, digest string, scope, versions map[string]any, session, agent, worktree string, issued time.Time, nonce string) *HostApprovalAssertion {
	_ = privateKey
	_ = nonce
	assertion := &HostApprovalAssertion{ChallengeRef: challenge, RequestDigest: digest, Scope: approvalScopeBindings(scope), Versions: approvalVersionBindings(versions), SessionRef: session, AgentRef: agent, Worktree: worktree, IssuedAt: issued.Format(time.RFC3339Nano)}
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func workLifecycle(t *testing.T, s *store.Store, id string) string {
	t.Helper()
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, id).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	return lifecycle
}
