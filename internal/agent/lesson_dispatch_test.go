package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// CD-0026: a lesson is prepared through the archive tool surface with a
// separately accepted approval, lands as one isolated commit of the note, the
// record shard, and the coverage shard on the claimed worktree branch of the
// knowledge-home Project, and replays without a second commit. The response
// carries the branch and commit as prepared delivery evidence; the lesson is
// published only after the coordinator's pull request merges. A reflection is
// the same operation with a reflection tag.

const lessonFixtureBranch = "work/lesson-publish"

func lessonDispatchFixture(t *testing.T) (*store.Store, *Service, Authority, ed25519.PrivateKey, string, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
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
	manifest := "{\n  \"schema_version\": \"1.2\",\n  \"supported_kinds\": [\"work_note\", \"decision\", \"spec\", \"lesson\", \"research\"],\n  \"indexed_kinds\": [\"work_note\", \"decision\", \"spec\", \"lesson\"],\n  \"domain_registry\": {\"schema_version\": \"1.0\", \"product_key\": \"lesson-product\", \"root_domain_id\": \"product-root:lesson-product\", \"domains\": [{\"domain_id\": \"product-root:lesson-product\", \"name\": \"Lesson product\", \"purpose\": \"Product-wide lesson fixture law\", \"status\": \"current\", \"architecture_relations\": []}]},\n  \"records\": []\n}\n"
	var seed store.KnowledgeManifest
	if err := json.Unmarshal([]byte(manifest), &seed); err != nil {
		t.Fatal(err)
	}
	if err := pm1fixture.WriteKnowledgeShards(repo, seed); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "empty manifest")

	// The claimed worktree of the knowledge-home Project that a live
	// publication work owns; the source work itself is terminal, and the
	// lesson still names it.
	worktree := filepath.Join(t.TempDir(), "lesson-worktree")
	run("worktree", "add", "--quiet", "-b", lessonFixtureBranch, worktree)
	baseSHA := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))

	events := []store.Event{
		{EventID: "lesson-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Lesson Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "lesson-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Lesson Project"}`)},
		{EventID: "lesson-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"lesson fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "lesson-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-lesson", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Lesson Work","priority":1}`)},
		{EventID: "lesson-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-lesson", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
		{EventID: "lesson-complete", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "work-lesson", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)},
		{EventID: "lesson-pub-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-pub", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Lesson Publication Work","priority":1}`)},
		{EventID: "lesson-pub-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-pub", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-lesson"): 0, store.VersionRef(store.SubjectWorkItem, "work-pub"): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator-lesson','project-1','canonical_path',?,?,'now','now'); INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-1','project-1','locator-lesson'); DELETE FROM fold_guard`, repo, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_claims(op_id,work_id,project_id,set_id,repository_id,pinned_branch,pinned_base_sha,pinned_path,state,principal_ref,request_id,observed_at,updated_at) VALUES('wt-op-lesson','work-pub','project-1','set-lesson-pub','repo-lesson',?,?,?,'verified','operator','req-lesson','now','now')`, lessonFixtureBranch, baseSHA, worktree); err != nil {
		t.Fatal(err)
	}

	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_compact"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	// The calling session runs inside the claimed knowledge-home worktree:
	// the effect boundary binds the publication write surface to this
	// host-verified worktree, so the grant carries its path.
	grant.Worktree = worktree
	privateKey := mustKey(t)
	return s, service, grant, privateKey, repo, worktree
}

func lessonInput() json.RawMessage {
	return json.RawMessage(`{"work_id":"work-lesson","lesson_id":"lesson-dispatch-probe","title":"Dispatch publishes lessons","summary":"The archive surface carries separately accepted lessons into git with their manifest record.","content":"# Dispatch publishes lessons\n\nApproval first, then one commit.\n","tags":["testing"],"scopes":{"mode":"explicit","project_ids":["project-1"]},"evidence":["internal/agent/lesson_dispatch_test.go"],"coverage":{"state":"satisfied","evidence":[{"kind":"go_test","value":"internal/agent.TestDispatchLessonPublishApprovalRoundTripAndReplay"}]},"publication_work_id":"work-pub","idempotency_key":"lesson-key-1"}`)
}

func lessonApprovalScope(scopeVersion string) map[string]any {
	return map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-lesson", "work-pub"}, "scope_version": scopeVersion}
}

func TestDispatchLessonPublishApprovalRoundTripAndReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey, repo, worktree := lessonDispatchFixture(t)
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
	scope := lessonApprovalScope(scopeVersion)
	versions := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", worktree, fixedTime(), "lesson-approval-0001")

	approvedInput, _ := json.Marshal(map[string]any{
		"work_id": "work-lesson", "lesson_id": "lesson-dispatch-probe",
		"title": "Dispatch publishes lessons", "summary": "The archive surface carries separately accepted lessons into git with their manifest record.",
		"content": "# Dispatch publishes lessons\n\nApproval first, then one commit.\n",
		"tags":    []string{"testing"}, "scopes": map[string]any{"mode": "explicit", "project_ids": []string{"project-1"}},
		"evidence":            []string{"internal/agent/lesson_dispatch_test.go"},
		"coverage":            map[string]any{"state": "satisfied", "evidence": []map[string]any{{"kind": "go_test", "value": "internal/agent.TestDispatchLessonPublishApprovalRoundTripAndReplay"}}},
		"publication_work_id": "work-pub",
		"idempotency_key":     "lesson-key-1", "approval": map[string]any{"approval_ref": challengeRef},
	})
	// Rebind the digest to the approved input the retry actually sends.
	request.Input = approvedInput
	digest2 := mutationDigest(request.Tool, request.Operation, env, request.Input)
	versions2 := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest2, scope, versions2, "session-1", "agent-1", worktree, fixedTime(), "lesson-approval-0002")

	approved, err := Dispatch(ctx, s, service, request, env)
	if err != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved response=%+v err=%v", approved, err)
	}
	if approved.Error != nil {
		t.Fatalf("approved error=%+v", approved.Error)
	}
	branch, commitOID := lessonDelivery(t, approved)
	if commitOID == "" || branch != lessonFixtureBranch {
		t.Fatalf("the response carries prepared delivery branch=%q commit=%q", branch, commitOID)
	}
	if head := strings.TrimSpace(gitOut(t, worktree, "rev-parse", "HEAD")); head != commitOID {
		t.Fatalf("delivery commit %s is not the claimed branch head %s", commitOID, head)
	}
	if branch := strings.TrimSpace(gitOut(t, worktree, "rev-parse", "--abbrev-ref", "HEAD")); branch != lessonFixtureBranch {
		t.Fatalf("the delivery landed on %q", branch)
	}
	paths := committedFilePaths(t, worktree, commitOID)
	want := []string{
		"docs/knowledge/coverage/lesson-dispatch-probe.json",
		"docs/knowledge/records/lesson-dispatch-probe.json",
	}
	notePath := ""
	for _, path := range paths {
		if strings.HasPrefix(path, "docs/lessons/") {
			notePath = path
		}
	}
	sort.Strings(paths)
	want = append(want, notePath)
	sort.Strings(want)
	if len(paths) != 3 || strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("the prepared commit carries %v, want exactly %v", paths, want)
	}
	shardBytes, err := os.ReadFile(filepath.Join(worktree, "docs/knowledge/records/lesson-dispatch-probe.json"))
	if err != nil || !strings.Contains(string(shardBytes), "lesson-dispatch-probe") {
		t.Fatalf("record shard lacks the lesson (err=%v):\n%s", err, shardBytes)
	}
	if coverageBytes, err := os.ReadFile(filepath.Join(worktree, "docs/knowledge/coverage/lesson-dispatch-probe.json")); err != nil || !strings.Contains(string(coverageBytes), `"state": "satisfied"`) {
		t.Fatalf("coverage shard missing or wrong (err=%v):\n%s", err, coverageBytes)
	}
	commits := strings.TrimSpace(gitOut(t, worktree, "rev-list", "--count", "HEAD"))
	mainHead := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))

	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if after := strings.TrimSpace(gitOut(t, worktree, "rev-list", "--count", "HEAD")); after != commits {
		t.Fatalf("replay created a commit: before=%s after=%s", commits, after)
	}
	if _, replayCommit := lessonDelivery(t, replay); replayCommit != commitOID {
		t.Fatal("replay delivered a different commit")
	}
	if after := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD")); after != mainHead {
		t.Fatal("the canonical checkout moved during publication")
	}
}

// TestDispatchLessonPublishRefusesWithoutAClaimedWorktree holds the first
// observed failure: a knowledge home with no claimed worktree refuses typed
// instead of committing onto the canonical default checkout.
func TestDispatchLessonPublishRefusesWithoutAClaimedWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _, _, _ := lessonDispatchFixture(t)
	if _, err := s.DatabaseForTesting().Exec(`UPDATE worktree_claims SET state='reclaimed' WHERE work_id='work-pub'`); err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "lesson_publish", Input: lessonInput()}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unknown_scope" || !strings.Contains(response.Error.Message, "no claimed worktree") {
		t.Fatalf("expected typed no-claim refusal, got response=%+v err=%v", response.Error, err)
	}
}

// TestDispatchLessonPublishRefusesAWorktreeTheSessionDoesNotHold holds the
// second observed failure: a caller in one linked worktree cannot aim the
// lesson commit at another work item's claimed worktree. The effect boundary
// binds the resolved claim to the calling session's host-verified worktree.
func TestDispatchLessonPublishRefusesAWorktreeTheSessionDoesNotHold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey, _, worktree := lessonDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	// The calling session's verified worktree is not the claimed
	// knowledge-home worktree the publication resolves.
	elsewhere := filepath.Join(t.TempDir(), "another-worktree")
	grant.Worktree = elsewhere
	env := mutationEnvelope(grant, scopeVersion)
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "lesson_publish", Input: lessonInput()}
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("missing approval response=%+v err=%v", missing, err)
	}
	challengeRef := missing.Error.Details["approval_ref"].(string)
	approvedInput, _ := json.Marshal(map[string]any{
		"work_id": "work-lesson", "lesson_id": "lesson-dispatch-probe",
		"title": "Dispatch publishes lessons", "summary": "The archive surface carries separately accepted lessons into git with their manifest record.",
		"content": "# Dispatch publishes lessons\n\nApproval first, then one commit.\n",
		"tags":    []string{"testing"}, "scopes": map[string]any{"mode": "explicit", "project_ids": []string{"project-1"}},
		"evidence":            []string{"internal/agent/lesson_dispatch_test.go"},
		"coverage":            map[string]any{"state": "satisfied", "evidence": []map[string]any{{"kind": "go_test", "value": "internal/agent.TestDispatchLessonPublishApprovalRoundTripAndReplay"}}},
		"publication_work_id": "work-pub",
		"idempotency_key":     "lesson-key-1", "approval": map[string]any{"approval_ref": challengeRef},
	})
	request.Input = approvedInput
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, lessonApprovalScope(scopeVersion), map[string]any{"work": 3}, "session-1", "agent-1", elsewhere, fixedTime(), "lesson-approval-0005")
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unknown_scope" || !strings.Contains(response.Error.Message, "not the calling session's verified worktree") {
		t.Fatalf("expected typed foreign-worktree refusal, got response=%+v err=%v", response.Error, err)
	}
	if _, err := os.Stat(filepath.Join(worktree, "docs/knowledge/records/lesson-dispatch-probe.json")); !os.IsNotExist(err) {
		t.Fatalf("the foreign session wrote into the claimed worktree (stat err=%v)", err)
	}
}

func TestDispatchLessonPublishReflectionTagRidesTheSamePath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey, _, worktree := lessonDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	raw := `{"work_id":"work-lesson","lesson_id":"lesson-reflection-probe","title":"How the work went","summary":"A reflection on execution friction, durable by riding the lesson path.","content":"# How the work went\n\nBoundary handoffs cost the most.\n","tags":["reflection"],"coverage":{"state":"outstanding","issue":"CON-508"},"publication_work_id":"work-pub","idempotency_key":"lesson-key-2"}`
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "lesson_publish", Input: json.RawMessage(raw)}
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("missing approval response=%+v err=%v", missing, err)
	}
	challengeRef := missing.Error.Details["approval_ref"].(string)
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := lessonApprovalScope(scopeVersion)
	versions := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", worktree, fixedTime(), "lesson-approval-0003")
	approved := `{"work_id":"work-lesson","lesson_id":"lesson-reflection-probe","title":"How the work went","summary":"A reflection on execution friction, durable by riding the lesson path.","content":"# How the work went\n\nBoundary handoffs cost the most.\n","tags":["reflection"],"coverage":{"state":"outstanding","issue":"CON-508"},"publication_work_id":"work-pub","idempotency_key":"lesson-key-2","approval":{"approval_ref":"` + challengeRef + `"}}`
	request.Input = json.RawMessage(approved)
	digest2 := mutationDigest(request.Tool, request.Operation, env, request.Input)
	versions2 := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest2, scope, versions2, "session-1", "agent-1", worktree, fixedTime(), "lesson-approval-0004")
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("reflection response kind=%s msg=%s err=%v", response.Error.Kind, response.Error.Message, err)
	}
	coverageBytes, err := os.ReadFile(filepath.Join(worktree, "docs/knowledge/coverage/lesson-reflection-probe.json"))
	if err != nil || !strings.Contains(string(coverageBytes), `"issue": "CON-508"`) {
		t.Fatalf("reflection coverage shard missing or wrong (err=%v):\n%s", err, coverageBytes)
	}
}

// TestDispatchLessonPublishAcceptsAnIssueNumberCoverage carries the
// law-coverage issue union through the boundary: an outstanding declaration
// whose issue is a positive integer passes the published schema, decodes into
// the union input, and commits the number as a bare JSON integer.
func TestDispatchLessonPublishAcceptsAnIssueNumberCoverage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, privateKey, _, worktree := lessonDispatchFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	raw := `{"work_id":"work-lesson","lesson_id":"lesson-issue-number-probe","title":"Issue number coverage","summary":"An outstanding lesson can track a positive integer issue number.","content":"# Issue number coverage\n","tags":["coverage"],"coverage":{"state":"outstanding","issue":508},"publication_work_id":"work-pub","idempotency_key":"lesson-key-3"}`
	request := InvokeRequest{Tool: "concord_work_compact", Operation: "lesson_publish", Input: json.RawMessage(raw)}
	missing, err := Dispatch(ctx, s, service, request, env)
	if err != nil || missing.Error == nil || missing.Error.Kind != "approval_required" {
		t.Fatalf("missing approval response=%+v err=%v", missing, err)
	}
	challengeRef := missing.Error.Details["approval_ref"].(string)
	approved := raw[:len(raw)-1] + `,"approval":{"approval_ref":"` + challengeRef + `"}}`
	request.Input = json.RawMessage(approved)
	digest := mutationDigest(request.Tool, request.Operation, env, request.Input)
	scope := lessonApprovalScope(scopeVersion)
	versions := map[string]any{"work": 3}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, "session-1", "agent-1", worktree, fixedTime(), "lesson-approval-0006")
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("issue-number response kind=%s msg=%s err=%v", response.Error.Kind, response.Error.Message, err)
	}
	coverageBytes, err := os.ReadFile(filepath.Join(worktree, "docs/knowledge/coverage/lesson-issue-number-probe.json"))
	if err != nil || !strings.Contains(string(coverageBytes), `"issue": 508`) {
		t.Fatalf("issue-number coverage shard missing or wrong (err=%v):\n%s", err, coverageBytes)
	}
}

// lessonDelivery extracts the prepared delivery evidence a lesson_publish
// response carries: the claimed branch and the immutable commit the
// coordinator's pull request will carry.
func lessonDelivery(t *testing.T, response Envelope) (branch, commit string) {
	t.Helper()
	if response.Result == nil {
		return "", ""
	}
	var payload struct {
		Delivery struct {
			Branch string `json:"branch"`
			Commit string `json:"commit"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatalf("result payload is not a mutation result: %v", err)
	}
	return payload.Delivery.Branch, payload.Delivery.Commit
}

func committedFilePaths(t *testing.T, repo, commit string) []string {
	t.Helper()
	out := gitOut(t, repo, "show", "--name-only", "--format=", commit)
	paths := []string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			paths = append(paths, strings.TrimSpace(line))
		}
	}
	return paths
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
