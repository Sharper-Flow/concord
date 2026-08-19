package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func seedAgentOverlapFixture(t *testing.T) (*store.Store, *Service, Grant, ed25519.PrivateKey, CallEnvelope, []byte) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_relate"})
	events := []store.Event{
		{EventID: "overlap-agent-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Other work","priority":1}`)},
		{EventID: "overlap-agent-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	hash := "sha256:" + strings.Repeat("c", 64)
	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-1")
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		name  string
		query string
		args  []any
	}{
		{"fold guard", `INSERT INTO fold_guard(active) VALUES(1)`, nil},
		{"locator", `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('overlap-locator','project-1','canonical_path',?,?,'now','now')`, []any{repo, repo}},
		{"knowledge home", `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-1','project-1','overlap-locator')`, nil},
		{"registry", `INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product-1','project-1','overlap-locator','product-1','root','1.0',?,'test')`, []any{hash}},
		{"domain", `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project-1','overlap-locator','product-1','root','Root','Product law','current',?,'test')`, []any{hash}},
		{"actor", `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-1','operator','now')`, []any{actorRef}},
		{"work-1 contract", `INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'overlap','check','{"kind":"check"}','internal_sqlite','[]','[]','now',?,'[]','[]',1,'prototype_internal')`, []any{actorRef}},
		{"work-2 contract", `INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-2',1,'overlap','check','{"kind":"check"}','internal_sqlite','[]','[]','now',?,'[]','[]',1,'prototype_internal')`, []any{actorRef}},
		{"bindings", `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES('work-1',1,'product-1',?,'root',?),('work-2',1,'product-1',?,'root',?)`, []any{hash, hash, hash, hash}},
		{"affected domains", `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES('work-1',1,'root'),('work-2',1,'root')`, nil},
		{"leave fold", `DELETE FROM fold_guard`, nil},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatalf("seed %s: %v", statement.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	input := []byte(`{"from_work_id":"work-1","to_work_id":"work-2","from_expected_version":2,"to_expected_version":2,"from_contract_version":1,"to_contract_version":1,"resolution_kind":"compatible_with","reason":"operator confirms compatible work","idempotency_key":"resolve-overlap-1"}`)
	return s, service, grant, privateKey, env, input
}

func TestResolveOverlapApprovalBindsDirectionKindVersionsAndPersistsConsumedApproval(t *testing.T) {
	s, service, _, privateKey, env, input := seedAgentOverlapFixture(t)
	var err error
	first, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
	if err != nil {
		t.Fatal(err)
	}
	if first.Error == nil || first.Error.Kind != "approval_required" {
		t.Fatalf("first overlap resolution outcome=%s error=%+v", first.Outcome, first.Error)
	}
	challengeRef, ok := first.Error.Details["approval_ref"].(string)
	if !ok || challengeRef == "" {
		t.Fatalf("missing approval challenge: %+v", first.Error.Details)
	}
	if first.Error.Details["resolution_kind"] != "compatible_with" || first.Error.Details["from_work_id"] != "work-1" || first.Error.Details["to_work_id"] != "work-2" {
		t.Fatalf("approval challenge omitted resolution direction: %+v", first.Error.Details)
	}
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]any{"from": int64(2), "to": int64(2), "from_contract": int64(1), "to_contract": int64(1)}
	baseScope := map[string]any{
		"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"},
		"work_ids": []string{"work-1", "work-2"}, "scope_version": env.ScopeVersion,
	}

	wrongKind := []byte(strings.Replace(string(withApproval), `"resolution_kind":"compatible_with"`, `"resolution_kind":"blocks"`, 1))
	wrongDigest := mutationDigest("concord_work_relate", "resolve_overlap", env, wrongKind)
	wrongEnv := env
	wrongEnv.HostApproval = signedHostApproval(privateKey, challengeRef, wrongDigest, baseScope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	wrong, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: wrongKind}, wrongEnv)
	if err != nil {
		t.Fatal(err)
	}
	if wrong.Error == nil || wrong.Error.Kind != "approval_invalid" {
		t.Fatalf("changed resolution kind reused approval: %+v", wrong)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 0 {
		t.Fatalf("invalid approval wrote %d resolutions", got)
	}
	if workVersion(t, s, "work-1") != 2 || workVersion(t, s, "work-2") != 2 {
		t.Fatal("invalid approval changed endpoint versions")
	}

	digest := mutationDigest("concord_work_relate", "resolve_overlap", env, withApproval)
	approvedEnv := env
	approvedEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, baseScope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: withApproval}, approvedEnv)
	if err != nil {
		t.Fatal(err)
	}
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved overlap resolution outcome=%s error=%+v", approved.Outcome, approved.Error)
	}
	var approvalRef, eventApprovalRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT approval_ref FROM workflow_overlap_resolutions`).Scan(&approvalRef); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.approval_ref') FROM domain_events WHERE kind='workflow.overlap_resolved'`).Scan(&eventApprovalRef); err != nil {
		t.Fatal(err)
	}
	if approvalRef == "" || approvalRef != eventApprovalRef || approvalRef == challengeRef {
		t.Fatalf("persisted approval projection=%q event=%q challenge=%q", approvalRef, eventApprovalRef, challengeRef)
	}
	var usedCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT used_count FROM agent_approvals WHERE approval_ref=?`, approvalRef).Scan(&usedCount); err != nil || usedCount != 1 {
		t.Fatalf("consumed approval %q used_count=%d err=%v", approvalRef, usedCount, err)
	}
}
