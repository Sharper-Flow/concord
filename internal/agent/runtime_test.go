package agent

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestDispatchProductResolveReturnsGeneratedPayload(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	productPayload, _ := json.Marshal(map[string]any{"display_name": "Product", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only"})
	projectPayload, _ := json.Marshal(map[string]any{"display_name": "Project"})
	membershipPayload, _ := json.Marshal(map[string]any{"product_id": "product-1", "project_id": "project-1", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})
	events := []store.Event{{EventID: "p", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: productPayload}, {EventID: "pr", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: projectPayload}, {EventID: "m", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: membershipPayload}}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(s.DB())
	service.Now = func() time.Time { return fixedTime() }
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"product_read"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	grant, err := service.IssueGrant(ctx, grantRequest(privateKey, "dispatch-nonce-0001"))
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_product_view", Operation: "resolve", Input: json.RawMessage(`{"project_id":"project-1"}`)}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "request-1", GrantRef: grant.Token, ClientRef: grant.ClientRef, ClientVersion: grant.ClientVersion, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", ScopeVersion: scopeVersion, SurfaceVersion: grant.SurfaceVersion, EnvelopeVersion: grant.EnvelopeVersion, ManifestDigest: grant.ManifestDigest}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("response=%+v", response)
	}
}

func TestDecodeInvokeRequestRejectsInvalidTrailingJSON(t *testing.T) {
	valid := `{"call_envelope":{"schema_version":"1.0","request_id":"request-1","grant_ref":"grant-1"},"tool":"concord_product_view","operation":"resolve","input":{}}`
	for _, suffix := range []string{" {}", " garbage"} {
		if _, _, err := DecodeInvokeRequest([]byte(valid + suffix)); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
			t.Fatalf("suffix %q error = %v, want trailing JSON rejection", suffix, err)
		}
	}
}

func TestDispatchCaptureCreatesWorkAndMembershipsAtomically(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "capture-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "capture-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "capture-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service := NewService(s.DB())
	service.Now = func() time.Time { return fixedTime() }
	publicKey, privateKey, _ := ed25519.GenerateKey(cryptorand.Reader)
	if err := service.RegisterTrustedClient(ctx, ClientRegistration{ClientRef: "client-1", KeyID: "key", PublicKey: publicKey, Policy: TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: []Capability{"work_define"}, ProductScope: []string{"product-1"}, ProjectScope: []string{"project-1"}}}); err != nil {
		t.Fatal(err)
	}
	grantReq := grantRequest(privateKey, "capture-nonce-0001")
	grantReq.Assertion.RequestedCapabilities = []Capability{"work_define"}
	grantReq.Assertion.Signature = ed25519.Sign(privateKey, CanonicalAssertion(grantReq.Assertion))
	grant, err := service.IssueGrant(ctx, grantReq)
	if err != nil {
		t.Fatal(err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Need","value_statement":"Need value","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-idem-1"}`)}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "capture-request-1", GrantRef: grant.Token, ClientRef: grant.ClientRef, ClientVersion: grant.ClientVersion, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, SurfaceVersion: grant.SurfaceVersion, EnvelopeVersion: grant.EnvelopeVersion, ManifestDigest: grant.ManifestDigest}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("capture response=%+v err=%v", response, err)
	}
	if len(response.ChangedRefs) != 1 || response.ChangedRefs[0].Version != "2" {
		t.Fatalf("changed refs=%#v", response.ChangedRefs)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM work_items w JOIN work_projects p ON p.work_id=w.id WHERE w.title='Need' AND p.project_id='project-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("capture projection count=%d err=%v", count, err)
	}
	env.RequestID = "capture-request-2"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("capture replay=%+v err=%v", replay, err)
	}
	revise := InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: json.RawMessage(`{"work_id":"work-` + replay.ChangedRefs[0].ID[len("work-"):] + `","expected_version":2,"title":"Need revised","value_statement":"Revised value","kind":"task","priority":3,"tags":[],"reason":"clarified","idempotency_key":"revise-idem-1"}`)}
	env.RequestID = "revise-request-1"
	revised, err := Dispatch(ctx, s, service, revise, env)
	if err != nil || revised.Outcome != OutcomeOK {
		t.Fatalf("revise response=%+v err=%v", revised, err)
	}
	request.Input = json.RawMessage(`{"title":"Changed","value_statement":"Need value","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-idem-1"}`)
	conflict, err := Dispatch(ctx, s, service, request, env)
	if err != nil || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("capture digest conflict=%+v err=%v", conflict, err)
	}
}

func TestAuthenticatedCursorBindsOperationAndRejectsTampering(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := SignedCursor{Tool: "concord_work_browse", Operation: "list", Scope: "product|project", Filter: "filters", Detail: "summary", Order: "priority", Source: "7", Last: "work-a", Inner: "inner-cursor"}
	token, err := EncodeCursor(context.Background(), s.DB(), want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCursor(context.Background(), s.DB(), token, want)
	if err != nil {
		t.Fatal(err)
	}
	if got.Inner != want.Inner || !strings.Contains(token, ".") {
		t.Fatalf("cursor=%+v token=%q", got, token)
	}
	if _, err := DecodeCursor(context.Background(), s.DB(), token, SignedCursor{Tool: "concord_work_trace", Operation: "history", Scope: want.Scope, Filter: want.Filter, Detail: want.Detail, Order: want.Order}); err == nil {
		t.Fatal("wrong operation accepted")
	}
	first := byte('A')
	if token[0] == first {
		first = 'B'
	}
	tampered := string(first) + token[1:]
	if _, err := DecodeCursor(context.Background(), s.DB(), tampered, want); err == nil {
		t.Fatal("tampered cursor accepted")
	}
}

func TestMutationDigestBindsIntentNotApprovalTransportReference(t *testing.T) {
	env := CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1"}
	without := []byte(`{"work_id":"work-1","expected_version":2,"reason":"complete"}`)
	with := []byte(`{"work_id":"work-1","expected_version":2,"reason":"complete","approval":{"approval_ref":"` + strings.Repeat("a", 64) + `"}}`)
	if mutationDigest("concord_work_transition", "lifecycle", env, without) != mutationDigest("concord_work_transition", "lifecycle", env, with) {
		t.Fatal("approval transport reference changed canonical intent digest")
	}
}

func TestResultPayloadNeverSerializesUnsignedStoreCursor(t *testing.T) {
	rawCursor := "store-offset-cursor"
	r := runtime{}
	response, err := r.q3(NewBase("request", "concord_work_browse", "list", ManifestVersion), store.Q3Result{
		ResultMeta: store.ResultMeta{QueryID: "PM1.Q3", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}, NextCursor: &rawCursor},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.NextCursor == nil {
		t.Fatal("envelope continuation cursor missing")
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["next_cursor"]; ok {
		t.Fatalf("unsigned payload cursor serialized: %#v", payload)
	}
}

func TestBudgetFieldsRefuseOrBoundResultsStructurally(t *testing.T) {
	base := NewBase("request", "concord_work_browse", "list", ManifestVersion)
	meta := store.ResultMeta{QueryID: "PM1.Q3", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: store.Freshness{ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)}}
	items := []store.WorkItem{{ID: "one", Kind: "task", Title: "one", Lifecycle: "needed"}, {ID: "two", Kind: "task", Title: "two", Lifecycle: "needed"}}
	response, err := (runtime{Budget: budgetInput{MaxItems: 1}}).q3(base, store.Q3Result{ResultMeta: meta, Items: items})
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "budget_refused" {
		t.Fatalf("max_items was ignored: %#v", response)
	}
	response, err = (runtime{Budget: budgetInput{MaxBytes: 1}}).q3(base, store.Q3Result{ResultMeta: meta, Items: items[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "budget_refused" {
		t.Fatalf("max_bytes was ignored: %#v", response)
	}
	ctx, cancel, budget, err := applyBudget(context.Background(), []byte(`{"budget":{"max_bytes":65536,"max_items":1,"max_millis":1}}`))
	defer cancel()
	if err != nil || budget.MaxMillis != 1 {
		t.Fatalf("max_millis was not accepted: budget=%#v err=%v", budget, err)
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("max_millis did not install a context deadline")
	}
}

func TestKnowledgeReferenceHonorsSelectedProductContainment(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir()+"/concord.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES (1); INSERT INTO archived_work (id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES ('knowledge-b','lesson','B','2026-08-07T00:00:00Z','published','[]','completed',1,'summary','home','locator','note.md','commit','hash'); INSERT INTO archived_work_products(work_id,product_id) VALUES ('knowledge-b','product-b'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Input: json.RawMessage(`{"knowledge_id":"knowledge-b"}`)}
	err = validateRequestedScope(context.Background(), s, CallEnvelope{SelectedProductID: "product-a"}, Grant{ProductScope: []string{"product-a", "product-b"}}, request)
	var failure *runtimeFailure
	if !errors.As(err, &failure) || failure.kind != "unauthorized" {
		t.Fatalf("knowledge outside selected Product accepted: %v", err)
	}
}

func TestKnowledgeResolveNoteDelegatesHomeScopeToQ10(t *testing.T) {
	s := runtimeKnowledgeStore(t, "home-knowledge", "lesson", "home", nil, map[string]string{"product-a": "member"})
	defer s.Close()
	request := InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(`{"knowledge_id":"home-knowledge"}`)}
	if err := validateRequestedScope(context.Background(), s, CallEnvelope{SelectedProductID: "product-a"}, Grant{ProductScope: []string{"product-a"}}, request); err != nil {
		t.Fatalf("Q10 runtime pre-scope validation rejected home record: %v", err)
	}
	response := runtimeResolveNote(t, s, "product-a", request.Input)
	assertRuntimeKnowledgeState(t, response, "canonical")
}

func TestKnowledgeResolveNoteUnscopedUsesRecordedLocator(t *testing.T) {
	s := runtimeKnowledgeStore(t, "unscoped-knowledge", "lesson", "home", nil, nil)
	defer s.Close()
	response := runtimeResolveNote(t, s, "", json.RawMessage(`{"knowledge_id":"unscoped-knowledge"}`))
	assertRuntimeKnowledgeState(t, response, "canonical")
}

func TestKnowledgeResolveNotePreservesFrozenWorkNoteScope(t *testing.T) {
	s := runtimeKnowledgeStore(t, "frozen-work", "work_note", "home", []string{"product-a"}, map[string]string{"product-b": "member"})
	defer s.Close()
	request := json.RawMessage(`{"work_id":"frozen-work"}`)
	assertRuntimeKnowledgeState(t, runtimeResolveNote(t, s, "product-a", request), "canonical")
	response := runtimeResolveNote(t, s, "product-b", request)
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unknown_scope" {
		t.Fatalf("frozen work note followed current membership: %+v", response)
	}
}

func TestKnowledgeResolveNoteRejectsUnrelatedSelectedProduct(t *testing.T) {
	s := runtimeKnowledgeStore(t, "scoped-knowledge", "lesson", "home", nil, map[string]string{"product-a": "member"})
	defer s.Close()
	response := runtimeResolveNote(t, s, "product-b", json.RawMessage(`{"knowledge_id":"scoped-knowledge"}`))
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unknown_scope" {
		t.Fatalf("unrelated Product was not typed unknown_scope: %+v", response)
	}
}

func runtimeResolveNote(t *testing.T, s *store.Store, product string, input json.RawMessage) Envelope {
	t.Helper()
	r := runtime{Store: s, Tool: "concord_knowledge", Operation: "resolve_note", Envelope: CallEnvelope{SelectedProductID: product}}
	response, err := r.read(context.Background(), NewBase("runtime-q10", r.Tool, r.Operation, ManifestVersion), input, "PM1.Q10")
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertRuntimeKnowledgeState(t *testing.T, response Envelope, want string) {
	t.Helper()
	if response.Outcome != OutcomeOK {
		t.Fatalf("Q10 response=%+v error=%#v", response, response.Error)
	}
	var result struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.State != want {
		t.Fatalf("Q10 state=%q want %q response=%+v", result.State, want, response)
	}
}

func runtimeKnowledgeStore(t *testing.T, id, kind, scopeMode string, frozenProducts []string, memberships map[string]string) *store.Store {
	t.Helper()
	repo := t.TempDir()
	notePath := "docs/lessons/" + id + ".md"
	content := "Durable knowledge.\n"
	if kind == "work_note" {
		notePath = "docs/work/" + id + ".md"
		content = "---\n" +
			"concord_work_id: " + id + "\n" +
			"work_type: implementation\n" +
			"title: Auth release\n" +
			"completed_at: 2026-08-10T00:00:00Z\n" +
			"outcome_tag: shipped\n" +
			"lesson_tags: [sqlite, state-authority]\n" +
			"terminal_state: completed\n" +
			"priority: 2\n" +
			"summary: Bounded summary\n" +
			"product_ids: [product-a]\n" +
			"project_ids: [stored-project]\n" +
			"component_ids: [auth]\n" +
			"tag_ids: [auth, release]\n" +
			"---\n\nDurable note.\n"
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, filepath.FromSlash(notePath))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(notePath)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(content))
	contentHash := "sha256:" + hex.EncodeToString(hash[:])
	if kind != "work_note" {
		manifest := map[string]any{
			"schema_version":  "1.0",
			"supported_kinds": []string{"work_note", "decision", "spec", "lesson", "research"},
			"indexed_kinds":   []string{"work_note", "decision", "spec", "lesson"},
			"records": []any{map[string]any{
				"id": id, "kind": kind, "path": notePath, "status": "published", "date": "2026-08-10T00:00:00Z",
				"title": "Durable lesson", "summary": "Durable summary", "tags": []string{},
				"scopes": map[string]any{"mode": scopeMode, "product_ids": []string{}, "project_ids": []string{}, "component_ids": []string{}, "tag_ids": []string{}},
				"sha256": contentHash,
			}},
		}
		manifestBytes, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "docs/concord-knowledge-index.v1.json"), append(manifestBytes, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runRuntimeGit(t, repo, "init", "-q")
	runRuntimeGit(t, repo, "config", "user.email", "test@example.com")
	runRuntimeGit(t, repo, "config", "user.name", "Concord Test")
	runRuntimeGit(t, repo, "add", ".")
	runRuntimeGit(t, repo, "commit", "-q", "-m", "knowledge")
	commit := strings.TrimSpace(runRuntimeGit(t, repo, "rev-parse", "HEAD"))

	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "concord.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('stored-project','Stored project',1,'now','now'); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('stored-locator','stored-project','canonical_path',?,?, 'now','now')`, repo, repo); err != nil {
		s.Close()
		t.Fatal(err)
	}
	for product := range memberships {
		if _, err := s.DB().Exec(`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?, ?, 'prototype', 'operator_only', 1, 'now', 'now'); INSERT INTO product_projects(product_id,project_id,role) VALUES(?, 'stored-project', 'secondary')`, product, product); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, kind, "Durable lesson", "2026-08-10T00:00:00Z", "published", "[]", "completed", 1, "Durable summary", "stored-project", "stored-locator", notePath, commit, contentHash, scopeMode); err != nil {
		s.Close()
		t.Fatal(err)
	}
	for _, product := range frozenProducts {
		if _, err := s.DB().Exec(`INSERT INTO archived_work_products(work_id,product_id) VALUES(?,?)`, id, product); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`DELETE FROM fold_guard`); err != nil {
		s.Close()
		t.Fatal(err)
	}
	return s
}

func runRuntimeGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
