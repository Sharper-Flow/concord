package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// One bind-family action records one durable evidence_kind. A mixed-kind
// evidence array used to bind only the first entry's kind and silently drop
// the rest, wedging later steps that declare no bind_evidence (#945).
const ekbMixed = "evidence array carries more than one kind"

func ekbDispatch(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, grant Authority, privateKey ed25519.PrivateKey, workID string, version int64, actionID string, fields map[string]any, evidence []EvidenceRef, key string) (Envelope, int64) {
	t.Helper()
	input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "idempotency_key": key}
	if fields != nil {
		input["fields"] = fields
	}
	if evidence != nil {
		input["evidence"] = evidence
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:" + key + ":" + strconv.FormatInt(version, 10)
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if response.Error != nil && response.Error.Kind == "approval_required" {
		challengeRef, _ := response.Error.Details["approval_ref"].(string)
		if challengeRef == "" {
			t.Fatalf("%s minted no challenge", actionID)
		}
		input["approval"] = map[string]any{"approval_ref": challengeRef}
		approvedRaw, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		scope := map[string]any{"product_id": "product-1", "project_ids": []string{"project-1"}, "work_ids": []string{workID}, "scope_version": env.ScopeVersion}
		approvalEnv := env
		approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "workflow_action", env, approvedRaw), scope, map[string]any{"work": version}, grant.SessionRef, grant.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
		response = dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: approvedRaw}, approvalEnv)
	}
	after := agentCompositionWorkVersion(t, s, workID)
	return response, after
}

// ekbWorkAtExecute captures a generic one-off work item and advances it to
// the execute step, where bind_evidence is a declared action.
func ekbWorkAtExecute(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, grant Authority, privateKey ed25519.PrivateKey, key string) (string, int64) {
	t.Helper()
	workID := captureCompositionWork(t, ctx, s, service, env, "Evidence kind binding "+key, "task", "workflow.generic_one_off", "ekb-capture-"+key)
	version := agentCompositionWorkVersion(t, s, workID)
	approved, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, version, "approve_contract", map[string]any{
		"premise":            "one bind records one evidence kind",
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"completed"}}}},
		"required_evidence":  []string{"artifact"},
		"spec_mandate":       []string{},
		"law_modifies":       []string{},
	}, nil, "ekb-approve-"+key)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approve_contract: %+v", approved.Error)
	}
	started, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, after, "start_action", nil, nil, "ekb-start-"+key)
	if started.Outcome != OutcomeOK {
		t.Fatalf("start_action: %+v", started.Error)
	}
	return workID, after
}

func ekbEvidenceBoundEvents(t *testing.T, s *store.Store, workID string) []string {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT json_extract(payload,'$.evidence_kind') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq`, workID, store.WorkflowEvidenceBound)
	if err != nil {
		t.Fatalf("read evidence_bound events: %v", err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatalf("scan evidence_bound kind: %v", err)
		}
		kinds = append(kinds, kind)
	}
	return kinds
}

func TestBindEvidenceRefusesMixedKindEvidenceArray(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, version := ekbWorkAtExecute(t, ctx, s, service, env, grant, privateKey, "mixed")

	mixed := []EvidenceRef{
		{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:one"},
		{Kind: "review", Authority: "concord-1", LocatorKind: "pull_request_review", Locator: "review:one"},
	}
	response, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, version, "bind_evidence", nil, mixed, "ekb-mixed")
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("mixed bind outcome=%q error=%+v, want invalid_input", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, ekbMixed) || !strings.Contains(response.Error.Message, "verification") || !strings.Contains(response.Error.Message, "review") {
		t.Fatalf("mixed bind message=%q, want both kinds named", response.Error.Message)
	}
	if after != version {
		t.Fatalf("refused mixed bind changed version from %d to %d", version, after)
	}
	if kinds := ekbEvidenceBoundEvents(t, s, workID); len(kinds) != 0 {
		t.Fatalf("refused mixed bind recorded evidence kinds %v", kinds)
	}
}

func TestBindEvidenceRecordsOneKindForHomogeneousArray(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, version := ekbWorkAtExecute(t, ctx, s, service, env, grant, privateKey, "homogeneous")

	homogeneous := []EvidenceRef{
		{Kind: "artifact", Authority: "github", LocatorKind: "pull_request", Locator: "pr:one"},
		{Kind: "artifact", Authority: "github", LocatorKind: "pull_request", Locator: "pr:two"},
	}
	response, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, version, "bind_evidence", nil, homogeneous, "ekb-homogeneous")
	if response.Outcome != OutcomeOK {
		t.Fatalf("homogeneous bind: %+v", response.Error)
	}
	if after == version {
		t.Fatal("homogeneous bind did not advance the work version")
	}
	kinds := ekbEvidenceBoundEvents(t, s, workID)
	if len(kinds) != 1 || kinds[0] != "artifact" {
		t.Fatalf("homogeneous bind recorded kinds %v, want exactly one artifact", kinds)
	}
	var boundRefs int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE work_id=? AND result_kind='completed' AND EXISTS (SELECT 1 FROM json_each(durable_operations.evidence_refs) WHERE value IN ('pr:one','pr:two'))`, workID).Scan(&boundRefs); err != nil {
		t.Fatalf("read durable operation evidence refs: %v", err)
	}
	if boundRefs != 1 {
		t.Fatalf("homogeneous bind durable operations with both locators=%d, want 1", boundRefs)
	}
}

func TestBindEvidenceRefusesContradictedExplicitKind(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID, version := ekbWorkAtExecute(t, ctx, s, service, env, grant, privateKey, "contradiction")

	response, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, version, "bind_evidence", map[string]any{"evidence_kind": "review"}, []EvidenceRef{{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:one"}}, "ekb-contradiction")
	if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
		t.Fatalf("contradicted kind outcome=%q error=%+v, want invalid_input", response.Outcome, response.Error)
	}
	if !strings.Contains(response.Error.Message, "contradicts") {
		t.Fatalf("contradicted kind message=%q", response.Error.Message)
	}
	if after != version {
		t.Fatalf("refused contradiction changed version from %d to %d", version, after)
	}
	if kinds := ekbEvidenceBoundEvents(t, s, workID); len(kinds) != 0 {
		t.Fatalf("refused contradiction recorded evidence kinds %v", kinds)
	}
}

func TestEvidenceKindRefusalCoversTheBindingFamily(t *testing.T) {
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_define", "work_transition"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	mixed := []EvidenceRef{
		{Kind: "verification", Authority: "github-actions", LocatorKind: "check_run", Locator: "check:family"},
		{Kind: "artifact", Authority: "github", LocatorKind: "pull_request", Locator: "pr:family"},
	}
	for _, actionID := range []string{"bind_evidence", "record_research", "record_report", "accept_decision", "approve_operation"} {
		workID, version := ekbWorkAtExecute(t, ctx, s, service, env, grant, privateKey, "family-"+actionID)
		response, after := ekbDispatch(t, ctx, s, service, env, grant, privateKey, workID, version, actionID, nil, mixed, "ekb-family-"+actionID)
		if response.Outcome != OutcomeError || response.Error == nil || response.Error.Kind != "invalid_input" {
			t.Fatalf("%s mixed bind outcome=%q error=%+v, want invalid_input", actionID, response.Outcome, response.Error)
		}
		if !strings.Contains(response.Error.Message, ekbMixed) {
			t.Fatalf("%s mixed bind message=%q", actionID, response.Error.Message)
		}
		if after != version {
			t.Fatalf("%s refused mixed bind changed version from %d to %d", actionID, version, after)
		}
		if kinds := ekbEvidenceBoundEvents(t, s, workID); len(kinds) != 0 {
			t.Fatalf("%s refused mixed bind recorded evidence kinds %v", actionID, kinds)
		}
	}
}
