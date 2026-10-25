package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

func TestAuditReclaimReportsCommittedEffectWithThePhysicalTree(t *testing.T) {
	t.Parallel()
	s, _, _, service, grant, _ := tiersFixture(t)
	completeWork(t, s, "work-2")
	vacateLinkedWorktree(t, s, service, grant, filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2"), "post-commit-vacate-2")
	livePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	if err := os.WriteFile(filepath.Join(livePath, "in-flight.md"), []byte("# in flight\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, livePath, "add", "in-flight.md")
	gitRun(t, livePath, "-c", "user.email=fixture@example.com", "-c", "user.name=fixture", "commit", "-m", "in-flight work")

	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	op, ok := ValidateContractOperation("concord_work_transition", "worktree_audit_reclaim")
	if !ok {
		t.Fatal("worktree_audit_reclaim is not registered")
	}
	raw, _ := json.Marshal(map[string]any{"product_id": "product-1", "default_ref": "main", "idempotency_key": "post-commit-budget"})
	r := runtime{Store: s, Authority: service, Envelope: mutationEnvelope(grant, scopeVersion), Tool: "concord_work_transition", Operation: "worktree_audit_reclaim", Reader: grant}
	response, err := r.mutateWorktreeAuditReclaim(context.Background(), NewBase("post-commit", r.Tool, r.Operation), raw, grant, op)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("response=%+v", response)
	}
	if response.ChangedRefs == nil || len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].ID != "work-2" {
		t.Fatalf("changed refs=%+v", response.ChangedRefs)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-2")); !os.IsNotExist(err) {
		t.Fatalf("reclaim effect was not committed: %v", err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("response is not contract-valid: %v", err)
	}
}

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
	if err := ValidateOperationPayload("concord_work_transition", "workflow_action", payload, false); err != nil {
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
	events := []store.Event{{EventID: "p", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: productPayload}, {EventID: "pr", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: projectPayload}, {EventID: "m", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: membershipPayload}}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
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
	if response.Outcome != OutcomeOK {
		t.Fatalf("response=%+v", response)
	}
}

func TestRetiredPlanningOperationsRefuseBeforeEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_define", "work_transition", "work_relate", "work_compact"})
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
		for i, table := range []string{"effect_receipts", "work_items", "workflow_instances", "workflow_contracts", "worker_attempts", "relations", "domain_observations", "durable_operations"} {
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
	} {
		t.Run(pair.tool+"."+pair.operation, func(t *testing.T) {
			if _, ok := ValidateContractOperation(pair.tool, pair.operation); ok {
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
		{EventID: "capture-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "capture-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Need","value_statement":"Need value","kind":"task","project_ids":["project-1"],"idempotency_key":"capture-idem-1"}`)}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "capture-request-1", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
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
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("capture replay=%+v err=%v", replay, err)
	}
	revise := InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: json.RawMessage(`{"work_id":"work-` + (*replay.ChangedRefs)[0].ID[len("work-"):] + `","expected_version":4,"title":"Need revised","value_statement":"Revised value","kind":"task","priority":3,"tags":[],"reason":"clarified","evidence":[{"kind":"commit","authority":"git","locator_kind":"commit","locator":"commit:7b83cbf41af2f9fa7990294a41a50cb75a1d6d1e"}],"idempotency_key":"revise-idem-1"}`)}
	env.RequestID = "revise-request-1"
	revised, err := Dispatch(ctx, s, service, revise, env)
	if err != nil || revised.Outcome != OutcomeOK {
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

func TestMutationDigestBindsIntentNotApprovalTransportReference(t *testing.T) {
	t.Parallel()
	env := CallEnvelope{SelectedProductID: "product-1", AmbientProjectID: "project-1"}
	without := []byte(`{"work_id":"work-1","expected_version":2,"reason":"complete"}`)
	with := []byte(`{"work_id":"work-1","expected_version":2,"reason":"complete","approval":{"approval_ref":"` + strings.Repeat("a", 64) + `"}}`)
	if mutationDigest("concord_work_transition", "lifecycle", env, without) != mutationDigest("concord_work_transition", "lifecycle", env, with) {
		t.Fatal("approval transport reference changed canonical intent digest")
	}
}

func TestResultPayloadNeverSerializesUnsignedStoreCursor(t *testing.T) {
	t.Parallel()
	rawCursor := "store-offset-cursor"
	r := runtime{}
	response, err := r.q3(NewBase("request", "concord_work_browse", "list"), store.Q3Result{
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
	for _, op := range ContractOperations {
		payload := []byte(`{"budget":{"max_bytes":65536,"max_items":1,"max_millis":1}}`)
		if err := ValidateOperationPayload(op.Tool, op.Operation, payload, false); err == nil {
			t.Fatalf("%s admitted the retired budget object", op.ID)
		}
		if err := ValidateOperationPayload(op.Tool, op.Operation, []byte(`{}`), false); err != nil && strings.Contains(err.Error(), "budget") {
			t.Fatalf("%s refusal names the retired object: %v", op.ID, err)
		}
	}
}

func TestKnowledgeReferenceHonorsSelectedProductContainment(t *testing.T) {
	t.Parallel()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES (1);
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('home','home',1,'t','t');
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES ('anchor-home','anchor-home','prototype','operator_only',1,'t','t');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('anchor-home','home','primary');
		INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES ('locator','home','canonical_path','/test/locator','/test/locator','t','t');
		INSERT INTO archived_work (id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES ('knowledge-b','lesson','B','2026-08-07T00:00:00Z','published','[]','completed',1,'summary','home','locator','note.md','commit','hash'); INSERT INTO archived_work_products(home_project_id,home_locator_id,work_id,product_id) VALUES ('home','locator','knowledge-b','product-b'); DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	request := InvokeRequest{Input: json.RawMessage(`{"knowledge_id":"knowledge-b"}`)}
	err = validateRequestedScope(context.Background(), s, CallEnvelope{SelectedProductID: "product-a"}, Authority{ProductScope: []string{"product-a", "product-b"}}, request, OperationRead)
	var failure *runtimeFailure
	if !errors.As(err, &failure) || failure.kind != "unauthorized" {
		t.Fatalf("knowledge outside selected Product accepted: %v", err)
	}
}

func TestKnowledgeResolveNoteDelegatesHomeScopeToQ10(t *testing.T) {
	t.Parallel()
	s := runtimeKnowledgeStore(t, "home-knowledge", "lesson", nil, map[string]string{"product-a": "member"})
	defer s.Close()
	request := InvokeRequest{Tool: "concord_knowledge", Operation: "resolve_note", Input: json.RawMessage(`{"knowledge_id":"home-knowledge"}`)}
	if err := validateRequestedScope(context.Background(), s, CallEnvelope{SelectedProductID: "product-a"}, Authority{ProductScope: []string{"product-a"}}, request, OperationRead); err != nil {
		t.Fatalf("Q10 runtime pre-scope validation rejected home record: %v", err)
	}
	response := runtimeResolveNote(t, s, "product-a", request.Input)
	assertRuntimeKnowledgeState(t, response, "canonical")
}

func TestKnowledgeResolveNoteUnscopedUsesRecordedLocator(t *testing.T) {
	t.Parallel()
	s := runtimeKnowledgeStore(t, "unscoped-knowledge", "lesson", nil, nil)
	defer s.Close()
	response := runtimeResolveNote(t, s, "", json.RawMessage(`{"knowledge_id":"unscoped-knowledge"}`))
	assertRuntimeKnowledgeState(t, response, "canonical")
}

func TestConcurrentKnowledgeResolveNoteFailuresStayTyped(t *testing.T) {
	t.Parallel()
	s := runtimeKnowledgeStore(t, "drifted-knowledge", "lesson", nil, nil)
	defer s.Close()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE archived_work SET title='Drifted' WHERE id='drifted-knowledge'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}

	const calls = 8
	type result struct {
		response Envelope
		err      error
	}
	results := make(chan result, calls)
	for i := range calls {
		go func() {
			r := runtime{Store: s, Tool: "concord_knowledge", Operation: "resolve_note", Envelope: CallEnvelope{}}
			response, err := r.read(context.Background(), NewBase(fmt.Sprintf("runtime-q10-%d", i), r.Tool, r.Operation), json.RawMessage(`{"knowledge_id":"drifted-knowledge"}`), "PM1.Q10")
			results <- result{response: response, err: err}
		}()
	}
	for range calls {
		got := <-results
		if got.err != nil {
			t.Fatalf("concurrent read returned an adapter error: %v", got.err)
		}
		if got.response.Outcome != OutcomeError || got.response.Error == nil || got.response.Error.Kind != "invalid_input" {
			t.Fatalf("concurrent read lost the typed core failure: %+v", got.response)
		}
		if _, err := json.Marshal(got.response); err != nil {
			t.Fatalf("concurrent failure response is not a valid envelope: %v", err)
		}
	}
}

func TestKnowledgeResolveNotePreservesFrozenWorkNoteScope(t *testing.T) {
	t.Parallel()
	s := runtimeKnowledgeStore(t, "frozen-work", "work_note", []string{"product-a"}, map[string]string{"product-b": "member"})
	defer s.Close()
	request := json.RawMessage(`{"work_id":"frozen-work"}`)
	assertRuntimeKnowledgeState(t, runtimeResolveNote(t, s, "product-a", request), "canonical")
	response := runtimeResolveNote(t, s, "product-b", request)
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unknown_scope" {
		t.Fatalf("frozen work note followed current membership: %+v", response)
	}
}

func TestKnowledgeResolveNoteRejectsUnrelatedSelectedProduct(t *testing.T) {
	t.Parallel()
	s := runtimeKnowledgeStore(t, "scoped-knowledge", "lesson", nil, map[string]string{"product-a": "member"})
	defer s.Close()
	response := runtimeResolveNote(t, s, "product-b", json.RawMessage(`{"knowledge_id":"scoped-knowledge"}`))
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "unknown_scope" {
		t.Fatalf("unrelated Product was not typed unknown_scope: %+v", response)
	}
}

func runtimeResolveNote(t *testing.T, s *store.Store, product string, input json.RawMessage) Envelope {
	t.Helper()
	r := runtime{Store: s, Tool: "concord_knowledge", Operation: "resolve_note", Envelope: CallEnvelope{SelectedProductID: product}}
	response, err := r.read(context.Background(), NewBase("runtime-q10", r.Tool, r.Operation), input, "PM1.Q10")
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

// recordAuthorityForKind gives a manifest fixture the authority object the
// record shape requires. A law-bearing kind is legislated and names the
// contract that fixed its standing; every other kind is derived.
func recordAuthorityForKind(kind string) map[string]any {
	if kind == "constitution" || kind == "decision" {
		return map[string]any{"tier": "legislated", "legislated_by": "work-0f1e2d3c4b5a6978", "contract_version": 1}
	}
	return map[string]any{"tier": "derived"}
}

func runtimeKnowledgeStore(t *testing.T, id, kind string, frozenProducts []string, memberships map[string]string) *store.Store {
	t.Helper()
	repo := t.TempDir()
	notePath := ".concord/docs/lessons/" + id + ".md"
	content := "Durable knowledge.\n"
	if kind == "work_note" {
		notePath = ".concord/docs/work/" + id + ".md"
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
			"domain_ids: [auth]\n" +
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
			"schema_version":  "1.2",
			"supported_kinds": []string{"work_note", "decision", "spec", "lesson", "research"},
			"indexed_kinds":   []string{"work_note", "decision", "spec", "lesson"},
			"domain_registry": map[string]any{
				"schema_version": "1.0", "product_key": "runtime-product", "root_domain_id": "product-root:runtime-product",
				"domains": []any{map[string]any{"domain_id": "product-root:runtime-product", "name": "Runtime product", "purpose": "Product-wide runtime fixture law", "status": "current", "architecture_relations": []any{}}},
			},
			"records": []any{map[string]any{
				"id": id, "kind": kind, "path": notePath, "status": "published", "date": "2026-08-10T00:00:00Z",
				"title": "Durable lesson", "summary": "Durable summary", "tags": []string{},
				"authority": recordAuthorityForKind(kind),
				"scopes":    map[string]any{"mode": "home", "product_ids": []string{}, "project_ids": []string{}, "domain_ids": []string{}, "tag_ids": []string{}},
				"sha256":    contentHash,
			}},
		}
		manifestBytes, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		var seed store.KnowledgeManifest
		if err := json.Unmarshal(manifestBytes, &seed); err != nil {
			t.Fatal(err)
		}
		if err := pm1fixture.WriteKnowledgeShards(repo, seed); err != nil {
			t.Fatal(err)
		}
	}
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

func contractOpFor(t *testing.T, tool, operation string) ContractOperation {
	t.Helper()
	op, ok := ValidateContractOperation(tool, operation)
	if !ok {
		t.Fatalf("no contract operation %s.%s", tool, operation)
	}
	return op
}
