package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestSupersedeContractPayloadIsRepresentableAtAgentBoundary(t *testing.T) {
	t.Parallel()
	payload, err := json.Marshal(map[string]any{
		"work_id": "work-stale", "expected_version": 7, "action_id": "supersede_contract", "idempotency_key": "supersede-contract",
		"fields": map[string]any{
			// The canonical authoring shape carries explicit outcome
			// predicates alone; the legacy outcome pair left the union
			// (CD-0218 D2).
			"contract_version": 2, "premise": "continue under the accepted successor law",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:successor", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:successor", "immutable_subject_ref": "commit:successor", "expected_result": "pass"}}},
			"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:successor"}, "law_modifies": []string{}, "rigor_class": "prototype_internal", "supersede_reason": "replace the stale contract", "audit_evidence": []string{"evidence:audit"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "workflow_action", payload, false); err != nil {
		t.Fatalf("supersede_contract payload is not representable at the agent boundary: %v", err)
	}
}

func TestDispatchProductResolveReturnsGeneratedPayload(t *testing.T) {
	t.Parallel()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	productPayload, _ := json.Marshal(map[string]any{"display_name": "Product", "stage_maturity": "prototype", "stage_audience_commitment": "operator_only"})
	projectPayload, _ := json.Marshal(map[string]any{"display_name": "Project"})
	membershipPayload, _ := json.Marshal(map[string]any{"product_id": "product-1", "project_id": "project-1", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})
	events := []store.Event{{EventID: "p", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: productPayload}, {EventID: "pr", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: projectPayload}, {EventID: "m", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: membershipPayload}}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_product_view", Operation: "resolve", Input: json.RawMessage(`{"project_id":"project-1"}`)}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "request-1", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("response=%+v", response)
	}
}

func TestRetiredPlanningOperationsRefuseBeforeEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_define", "work_transition", "work_relate", "work_compact"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	// The event count catches durable writes, and the projection counts and
	// work version catch partial work, contract, dispatch, relation, or
	// observation writes.
	snapshot := func() [9]int64 {
		t.Helper()
		var counts [9]int64
		for i, table := range []string{"effect_receipts", "work_items", "workflow_instances", "workflow_contracts", "relations", "domain_observations", "durable_operations"} {
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM ` + table).Scan(&counts[i]); err != nil {
				t.Fatal(err)
			}
		}
		counts[8] = workVersion(t, s, "work-1")
		return counts
	}
	before := snapshot()
	for _, pair := range []struct{ tool, operation string }{
		{"concord_domain", "active_work"},
		{"concord_domain", "observation_record"},
		{"concord_domain", "observation_dismiss"},
		{"concord_work_relate", "link"},
		{"concord_work_relate", "unlink"},
		{"concord_work_relate", "supersede"},
		{"concord_work_relate", "restore_superseded"},
		{"concord_product_view", "portfolio"},
		{"concord_work_compact", "lesson_publish"},
		{"concord_knowledge", "unprocessed"},
		{"concord_knowledge", "search"},
		{"concord_knowledge", "resolve_note"},
		{"concord_knowledge", "observe_publication"},
	} {
		t.Run(pair.tool+"."+pair.operation, func(t *testing.T) {
			if _, ok := agentcontract.ValidateContractOperation(pair.tool, pair.operation); ok {
				t.Fatal("retired operation is still registered")
			}
			_, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: pair.tool, Operation: pair.operation, Input: json.RawMessage(`{}`)}, env)
			if err == nil || err.Error() != "unsupported tool operation" {
				t.Fatalf("retired operation dispatch = %v, want unsupported tool operation", err)
			}
			if after := snapshot(); after != before {
				t.Fatalf("retired operation changed state: before=%v after=%v", before, after)
			}
		})
	}
}

func TestDecodeInvokeRequestRejectsInvalidTrailingJSON(t *testing.T) {
	t.Parallel()
	valid := `{"call_envelope":{"schema_version":"1.0","request_id":"request-1","unexpected_field":"value"},"tool":"concord_product_view","operation":"resolve","input":{}}`
	for _, suffix := range []string{" {}", " garbage"} {
		if _, _, err := DecodeInvokeRequest([]byte(valid + suffix)); err == nil || !strings.Contains(err.Error(), "trailing JSON") {
			t.Fatalf("suffix %q error = %v, want trailing JSON rejection", suffix, err)
		}
	}
}

// The request transport carries a dispatch whose packet holds the approved
// 64 KiB context aggregate beside its other members, so the invoke request
// decode admits requests above the 64 KiB response/envelope era and refuses
// only past its own derived bound. The bound refuses whole; it never
// truncates.
func TestDecodeInvokeRequestAdmitsRequestCapacity(t *testing.T) {
	t.Parallel()
	big := make([]byte, 0, 96*1024)
	big = append(big, `{"call_envelope":{"schema_version":"1.0","request_id":"request-1","client_ref":"client/concord-1","principal_ref":"principal/operator","session_ref":"session/s","agent_ref":"agent/a","directory":"/tmp/d","worktree":"/tmp/w","ambient_project_id":"project-1","scope_version":"sha256:0000000000000000000000000000000000000000000000000000000000000000","manifest_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"},"tool":"concord_product_view","operation":"resolve","input":{"filler":"`...)
	big = append(big, bytes.Repeat([]byte("x"), 80*1024)...)
	big = append(big, `"}}`...)
	request, envelope, err := DecodeInvokeRequest(big)
	if err != nil {
		t.Fatalf("invoke request within the request bound refused: %v", err)
	}
	if request.Tool != "concord_product_view" || envelope.RequestID != "request-1" {
		t.Fatalf("decoded request = %q envelope request id = %q, want the carried values", request.Tool, envelope.RequestID)
	}
	oversize := append([]byte(nil), big...)
	for len(oversize) <= MaxRequestBytes {
		oversize = append(oversize, ' ')
	}
	if _, _, err := DecodeInvokeRequest(oversize); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("invoke request past the request bound error = %v, want the whole-request size refusal", err)
	}
}

func TestDispatchCaptureCreatesWorkAndMembershipsAtomically(t *testing.T) {
	t.Parallel()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "capture-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "capture-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "capture-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Need","value_statement":"Need value","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-idem-1"}`)}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "capture-request-1", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("capture response=%+v err=%v", response, err)
	}
	// Every capture pins a workflow instance (#650), so the changed version
	// reflects the definition-selected events, not the bare creation pair.
	if len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].Version != "4" {
		t.Fatalf("changed refs=%#v", response.ChangedRefs)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items w JOIN work_projects p ON p.work_id=w.id WHERE w.title='Need' AND p.project_id='project-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("capture projection count=%d err=%v", count, err)
	}
	env.RequestID = "capture-request-2"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("capture replay=%+v err=%v", replay, err)
	}
	revise := InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: json.RawMessage(`{"work_id":"work-` + (*replay.ChangedRefs)[0].ID[len("work-"):] + `","expected_version":4,"title":"Need revised","value_statement":"Revised value","kind":"task","priority":3,"tags":[],"reason":"clarified","evidence":[{"kind":"commit","authority":"git","locator_kind":"commit","locator":"commit:7b83cbf41af2f9fa7990294a41a50cb75a1d6d1e"}],"idempotency_key":"revise-idem-1"}`)}
	env.RequestID = "revise-request-1"
	revised, err := Dispatch(ctx, s, service, revise, env)
	if err != nil || revised.Outcome != agentcontract.OutcomeOK {
		errorJSON, _ := json.Marshal(revised.Error)
		t.Fatalf("revise response=%+v error=%s err=%v", revised, errorJSON, err)
	}
	var revisionDigest, revisionActor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest,actor FROM effect_receipts WHERE kind='work.intent_revised' AND subject_id=?`, (*replay.ChangedRefs)[0].ID).Scan(&revisionDigest, &revisionActor); err != nil {
		t.Fatal(err)
	}
	expectedRevision, err := json.Marshal(map[string]any{"title": "Need revised", "value_statement": "Revised value", "kind": "task", "priority": 3, "urgency": "standard", "tags": []string{}, "workflow_type_ref": "", "reason": "clarified", "evidence_refs": []string{"commit:7b83cbf41af2f9fa7990294a41a50cb75a1d6d1e"}, "expected_version": 4, "resulting_version": 5})
	if err != nil {
		t.Fatal(err)
	}
	if revisionDigest != fmt.Sprintf("sha256:%x", sha256.Sum256(expectedRevision)) || revisionActor != grant.PrincipalRef {
		t.Fatalf("revision receipt does not bind the exact requested evidence and actor: digest=%s actor=%s expected=%s", revisionDigest, revisionActor, expectedRevision)
	}
	request.Input = json.RawMessage(`{"title":"Changed","value_statement":"Need value","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-idem-1"}`)
	conflict, err := Dispatch(ctx, s, service, request, env)
	if err != nil || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("capture digest conflict=%+v err=%v", conflict, err)
	}
}

func TestAuthenticatedCursorBindsOperationAndRejectsTampering(t *testing.T) {
	t.Parallel()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := SignedCursor{Tool: "concord_work_browse", Operation: "list", Scope: "product|project", Filter: "filters", Detail: "summary", Order: "priority", Source: "7", Last: "work-a", Inner: "inner-cursor"}
	token, err := s.EncodeCursor(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.DecodeCursor(context.Background(), token, want)
	if err != nil {
		t.Fatal(err)
	}
	if got.Inner != want.Inner || !strings.Contains(token, ".") {
		t.Fatalf("cursor=%+v token=%q", got, token)
	}
	if _, err := s.DecodeCursor(context.Background(), token, SignedCursor{Tool: "concord_work_trace", Operation: "history", Scope: want.Scope, Filter: want.Filter, Detail: want.Detail, Order: want.Order}); err == nil {
		t.Fatal("wrong operation accepted")
	}
	first := byte('A')
	if token[0] == first {
		first = 'B'
	}
	tampered := string(first) + token[1:]
	if _, err := s.DecodeCursor(context.Background(), tampered, want); err == nil {
		t.Fatal("tampered cursor accepted")
	}
}

func TestResultPayloadNeverSerializesUnsignedStoreCursor(t *testing.T) {
	t.Parallel()
	rawCursor := "store-offset-cursor"
	r := runtime{}
	response, err := r.q3(agentcontract.NewBase("request", "concord_work_browse", "list"), store.Q3Result{
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

func TestBudgetObjectIsRetiredEverywhereOnTheSurface(t *testing.T) {
	t.Parallel()
	// CD-0038 amendment (2026-10-02): the result-size budget object is
	// retired. Size is bounded by limit/page, time by requested_budget_seconds,
	// so no result path consults a caller budget object and the seconds
	// parser cannot resurrect one.
	for _, op := range agentcontract.ContractOperations {
		payload := []byte(`{"budget":{"max_bytes":65536,"max_items":1,"max_millis":1}}`)
		if err := agentcontract.ValidateOperationPayload(op.Tool, op.Operation, payload, false); err == nil {
			t.Fatalf("%s admitted the retired budget object", op.ID)
		}
		if err := agentcontract.ValidateOperationPayload(op.Tool, op.Operation, []byte(`{}`), false); err != nil && strings.Contains(err.Error(), "budget") {
			t.Fatalf("%s refusal names the retired object: %v", op.ID, err)
		}
	}
}

// runtimeKnowledgeStore seeds one archived work note under a committed home,
// the state the published-execution absence wire reads. The lesson-record
// branch of this helper served the retired knowledge resolve family.
func runtimeKnowledgeStore(t *testing.T, id, kind string, frozenProducts []string, memberships map[string]string) *store.Store {
	t.Helper()
	repo := t.TempDir()
	notePath := ".concord/docs/work/" + id + ".md"
	content := "---\n" +
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
		"domain_ids: [auth]\n" +
		"tag_ids: [auth, release]\n" +
		"---\n\nDurable note.\n"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, filepath.FromSlash(notePath))), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(notePath)), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(content))
	contentHash := "sha256:" + hex.EncodeToString(hash[:])
	runRuntimeGit(t, repo, "init", "-q")
	runRuntimeGit(t, repo, "config", "user.email", "test@example.com")
	runRuntimeGit(t, repo, "config", "user.name", "Concord Test")
	runRuntimeGit(t, repo, "add", ".")
	runRuntimeGit(t, repo, "commit", "-q", "-m", "knowledge")
	commit := strings.TrimSpace(runRuntimeGit(t, repo, "rev-parse", "HEAD"))

	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('stored-project','Stored project',1,'now','now'); INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('stored-locator','stored-project','canonical_path',?,?, 'now','now')`, repo, repo); err != nil {
		s.Close()
		t.Fatal(err)
	}
	for product := range memberships {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?, ?, 'prototype', 'operator_only', 1, 'now', 'now'); INSERT INTO product_projects(product_id,project_id,role) VALUES(?, 'stored-project', 'secondary')`, product, product); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, kind, "Durable lesson", "2026-08-10T00:00:00Z", "published", "[]", "completed", 1, "Durable summary", "stored-project", "stored-locator", notePath, commit, contentHash, "home"); err != nil {
		s.Close()
		t.Fatal(err)
	}
	for _, product := range frozenProducts {
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO archived_work_products(home_project_id,home_locator_id,work_id,product_id) VALUES (?,?,?,?)`, "stored-project", "stored-locator", id, product); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
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

func contractOpFor(t *testing.T, tool, operation string) agentcontract.ContractOperation {
	t.Helper()
	op, ok := agentcontract.ValidateContractOperation(tool, operation)
	if !ok {
		t.Fatalf("no contract operation %s.%s", tool, operation)
	}
	return op
}
