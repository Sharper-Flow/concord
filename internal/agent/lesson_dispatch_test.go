package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// CD-0026: a lesson is published through the archive tool surface with a
// separately accepted approval, lands in git with its manifest record, and
// replays without a second commit. A reflection is the same operation with a
// reflection tag.

func lessonDispatchFixture(t *testing.T) (*store.Store, *Service, Grant, ed25519.PrivateKey, string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	run("config", "user.email", "concord@example.invalid")
	run("config", "user.name", "Concord Lesson Test")
	if err := os.MkdirAll(filepath.Join(repo, "docs/lessons"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "{\n  \"schema_version\": \"1.1\",\n  \"supported_kinds\": [\"work_note\", \"decision\", \"spec\", \"lesson\", \"research\"],\n  \"indexed_kinds\": [\"work_note\", \"decision\", \"spec\", \"lesson\"],\n  \"records\": []\n}\n"
	if err := os.WriteFile(filepath.Join(repo, "docs/concord-knowledge-index.v1.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "empty manifest")

	events := []store.Event{
		{EventID: "lesson-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Lesson Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "lesson-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Lesson Project"}`)},
		{EventID: "lesson-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"lesson fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "lesson-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-lesson", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Lesson Work","priority":1}`)},
		{EventID: "lesson-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-lesson", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "lesson-complete", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "work-lesson", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-lesson"): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-lesson','project-1','canonical_path',?,?,'now','now'); DELETE FROM fold_guard; INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-1','project-1','locator-lesson')`, repo, repo); err != nil {
		t.Fatal(err)
	}

	service := NewService(s)
	service.Now = fixedTime
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key-1", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"work_compact"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	grantReq := grantRequest(privateKey, "lesson-dispatch-nonce")
	grantReq.Assertion.RequestedCapabilities = []Capability{"work_compact"}
	grantReq.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(grantReq.Assertion))
	grant, err := service.IssueGrant(ctx, grantReq)
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, privateKey, repo
}

func lessonInput() json.RawMessage {
	return json.RawMessage(`{"work_id":"work-lesson","lesson_id":"lesson-dispatch-probe","title":"Dispatch publishes lessons","summary":"The archive surface carries separately accepted lessons into git with their manifest record.","content":"# Dispatch publishes lessons\n\nApproval first, then one commit.\n","tags":["testing"],"scopes":{"mode":"explicit","project_ids":["project-1"]},"evidence":["internal/agent/lesson_dispatch_test.go"],"idempotency_key":"lesson-key-1"}`)
}

func TestDispatchLessonPublishApprovalRoundTripAndReplay(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey, repo := lessonDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "lesson_publish", Input: lessonInput()}

	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Outcome != OutcomeError || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("missing approval response kind=%s msg=%s details=%v err=%v", missing.Error.Kind, missing.Error.Message, missing.Error.Details, err)
	}
	challengeRef, ok := missing.Error.Details["approval_ref"].(string)
	if !ok {
		t.Fatalf("challenge ref=%v", missing.Error.Details)
	}
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-lesson"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "lesson-approval-0001")

	approvedInput, _ := json.Marshal(map[string]any{
		"work_id": "work-lesson", "lesson_id": "lesson-dispatch-probe",
		"title": "Dispatch publishes lessons", "summary": "The archive surface carries separately accepted lessons into git with their manifest record.",
		"content": "# Dispatch publishes lessons\n\nApproval first, then one commit.\n",
		"tags":    []string{"testing"}, "scopes": map[string]any{"mode": "explicit", "project_ids": []string{"project-1"}},
		"evidence":        []string{"internal/agent/lesson_dispatch_test.go"},
		"idempotency_key": "lesson-key-1", "approval": map[string]any{"approval_ref": challengeRef},
	})
	// Rebind the digest to the approved input the retry actually sends.
	request.Input = approvedInput
	digest2 := mutationDigest(request.Tool, request.Operation, env, request.Input)
	versions2 := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest2, scope, versions2, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "lesson-approval-0002")

	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved response=%+v err=%v", approved, err)
	}
	if approved.Error != nil {
		t.Fatalf("approved error=%+v", approved.Error)
	}
	manifestBytes, _ := os.ReadFile(filepath.Join(repo, "docs/concord-knowledge-index.v1.json"))
	if !strings.Contains(string(manifestBytes), "lesson-dispatch-probe") {
		t.Fatalf("manifest lacks the lesson:\n%s", manifestBytes)
	}
	commits := strings.TrimSpace(gitOut(t, repo, "rev-list", "--count", "HEAD"))

	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if after := strings.TrimSpace(gitOut(t, repo, "rev-list", "--count", "HEAD")); after != commits {
		t.Fatalf("replay created a commit: before=%s after=%s", commits, after)
	}
}

func TestDispatchLessonPublishReflectionTagRidesTheSamePath(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey, _ := lessonDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	raw := `{"work_id":"work-lesson","lesson_id":"lesson-reflection-probe","title":"How the work went","summary":"A reflection on execution friction, durable by riding the lesson path.","content":"# How the work went\n\nBoundary handoffs cost the most.\n","tags":["reflection"],"idempotency_key":"lesson-key-2"}`
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "lesson_publish", Input: json.RawMessage(raw)}
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("missing approval response=%+v err=%v", missing, err)
	}
	challengeRef := missing.Error.Details["approval_ref"].(string)
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-lesson"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "lesson-approval-0003")
	approved := `{"work_id":"work-lesson","lesson_id":"lesson-reflection-probe","title":"How the work went","summary":"A reflection on execution friction, durable by riding the lesson path.","content":"# How the work went\n\nBoundary handoffs cost the most.\n","tags":["reflection"],"idempotency_key":"lesson-key-2","approval":{"approval_ref":"` + challengeRef + `"}}`
	request.Input = json.RawMessage(approved)
	digest2 := mutationDigest(request.Tool, request.Operation, env, request.Input)
	versions2 := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest2, scope, versions2, "session-1", "agent-1", "/repo-wt", "1.0.0", fixedTime(), "lesson-approval-0004")
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("reflection response kind=%s msg=%s err=%v", response.Error.Kind, response.Error.Message, err)
	}
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
