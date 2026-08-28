package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// CD-0025: author a pack through the tool surface, then declare reliance on
// it at a workflow boundary. The engine binds the consumer and proves
// freshness fail-closed; a stale required revision refuses the action.

func researchSurfaceFixture(t *testing.T) (*store.Store, *Service, Authority, string) {
	return researchSurfaceFixtureWithCapabilities(t, []Capability{"product_read", "work_define", "work_transition", "research"})
}

func researchSurfaceFixtureWithCapabilities(t *testing.T, capabilities []Capability) (*store.Store, *Service, Authority, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := []store.Event{
		{EventID: "rs-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Research Surface","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "rs-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Research Project"}`)},
		{EventID: "rs-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"research surface fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "rs-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"bug","title":"Research Surface Work","priority":1}`)},
		{EventID: "rs-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}

	definition, defErr := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if defErr != nil {
		t.Fatal(defErr)
	}
	execActor := store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: store.ActorAgent}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: execActor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}

	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant, "session-1"
}

func TestResearchAuthorBindProveAndRead(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := researchSurfaceFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	invoke := func(op string, input any) Envelope {
		t.Helper()
		raw, _ := json.Marshal(input)
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: toolForOperation(op), Operation: op, Input: raw}, mutationEnvelope(grant, scopeVersion))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	// 1. Author: create the pack on the owner work item.
	create := invoke("research_pack_create", map[string]any{
		"owner_work_id":   "work-1",
		"revision":        map[string]any{"question": "Which store path owns research?", "method": "source inspection"},
		"idempotency_key": "rs-create-1",
	})
	if create.Outcome != OutcomeOK {
		t.Fatalf("pack create failed: %+v", create.Error)
	}
	var created struct {
		ChangedRefs []struct {
			ID string `json:"id"`
		} `json:"changed_refs"`
	}
	if err := json.Unmarshal(create.Result, &created); err != nil || len(created.ChangedRefs) != 1 {
		t.Fatalf("create result=%s err=%v", create.Result, err)
	}
	packID := created.ChangedRefs[0].ID

	// 2. Record a finding and a source on the pack.
	if r := invoke("research_finding_record", map[string]any{
		"pack_id": packID, "expected_version": 1,
		"finding":         map[string]any{"finding_id": "f-1", "kind": "observation", "statement": "The store owns the pack-operation boundary.", "confidence": "high"},
		"idempotency_key": "rs-finding-1",
	}); r.Outcome != OutcomeOK {
		t.Fatalf("finding record failed: %+v", r.Error)
	}
	if r := invoke("research_source_record", map[string]any{
		"pack_id": packID, "expected_version": 2,
		"source":          map[string]any{"source_id": "s-1", "kind": "source_code", "locator": "internal/store/research_mutations.go", "title": "Pack operation boundary", "publisher_or_author": "concord", "accessed_at": "2026-08-14T00:00:00Z"},
		"idempotency_key": "rs-source-1",
	}); r.Outcome != OutcomeOK {
		t.Fatalf("source record failed: %+v", r.Error)
	}

	// 3. Read the pack back through the trace surface.
	read := invoke("research", map[string]any{"product_id": "product-1", "pack_id": packID})
	if read.Outcome != OutcomeOK {
		t.Fatalf("research read failed: %+v", read.Error)
	}
	var pack store.ResearchPack
	if err := json.Unmarshal(read.Result, &pack); err != nil || pack.PackID != packID || pack.CurrentRevision != 1 {
		t.Fatalf("read pack=%+v err=%v", pack, err)
	}

	// 4. Declare reliance at a workflow boundary on a fresh pack: the action
	// succeeds and the consumer binding lands in the same transaction.
	action := invoke("workflow_action", map[string]any{
		"work_id": "work-1", "expected_version": 4, "action_id": "record_reproduction", "idempotency_key": "rs-action-1",
		"fields":            map[string]any{"payload": map[string]any{"work": "work-1"}, "title": "Research Surface Work", "value_statement": "fixture value statement"},
		"research_bindings": []map[string]any{{"pack_id": packID, "revision": 1, "use_role": "context", "required": true}},
	})
	if action.Outcome != OutcomeOK {
		t.Fatalf("action with fresh required binding failed: %+v", action.Error)
	}
	freshness, err := s.RequiredResearchFreshness(ctx, packID, "work-1")
	if err != nil || freshness != store.ResearchCurrent {
		t.Fatalf("binding freshness=%v err=%v", freshness, err)
	}

	// 5. Stale the pack; a second required reliance on the stale revision is
	// refused fail-closed at the boundary (CD-0009 D6 / CD-0025).
	if r := invoke("research_freshness_set", map[string]any{
		"pack_id": packID, "expected_version": 4, "freshness": "stale", "idempotency_key": "rs-stale-1",
	}); r.Outcome != OutcomeOK {
		t.Fatalf("freshness set failed: %+v", r.Error)
	}
	refused := invoke("workflow_action", map[string]any{
		"work_id": "work-1", "expected_version": 5, "action_id": "record_root_cause", "idempotency_key": "rs-action-2",
		"fields":            map[string]any{"payload": map[string]any{"work": "work-1"}, "title": "Research Surface Work", "value_statement": "fixture value statement"},
		"research_bindings": []map[string]any{{"pack_id": packID, "revision": 1, "use_role": "context", "required": true}},
	})
	if refused.Outcome == OutcomeOK {
		t.Fatal("required reliance on stale revision must refuse the action")
	}
	if refused.Error == nil || refused.Error.Kind != "stale_requires_review" || !strings.Contains(refused.Error.Message, "stale") {
		t.Fatalf("expected stale_requires_review refusal, got %+v", refused.Error)
	}
}

// Capability requirements derive from the per-operation contract registry.
// A grant scoped to only the research capability must execute research
// mutations; a grant without research must be refused for them.
func TestResearchMutationCapabilityFollowsContract(t *testing.T) {
	ctx := context.Background()

	scopedInvoke := func(capabilities []Capability, op string, input any) Envelope {
		t.Helper()
		s, service, grant, _ := researchSurfaceFixtureWithCapabilities(t, capabilities)
		scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(input)
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: toolForOperation(op), Operation: op, Input: raw}, mutationEnvelope(grant, scopeVersion))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	create := scopedInvoke([]Capability{"product_read", "research"}, "research_pack_create", map[string]any{
		"owner_work_id":   "work-1",
		"revision":        map[string]any{"question": "Which capability authorizes this pack?", "method": "contract registry"},
		"idempotency_key": "cap-research-only-1",
	})
	if create.Outcome != OutcomeOK {
		t.Fatalf("research-only grant must execute research_pack_create: %+v", create.Error)
	}

	refused := scopedInvoke([]Capability{"product_read", "work_define"}, "research_pack_create", map[string]any{
		"owner_work_id":   "work-1",
		"revision":        map[string]any{"question": "Which capability authorizes this pack?", "method": "contract registry"},
		"idempotency_key": "cap-work-define-only-1",
	})
	if refused.Outcome == OutcomeOK {
		t.Fatal("grant without research capability must not execute research_pack_create")
	}
	if refused.Error == nil || refused.Error.Kind != "unauthorized" {
		t.Fatalf("expected unauthorized refusal, got %+v", refused.Error)
	}
}

func toolForOperation(op string) string {
	switch {
	case strings.HasPrefix(op, "research_pack_create"), strings.HasPrefix(op, "research_revision_append"), strings.HasPrefix(op, "research_finding_record"), strings.HasPrefix(op, "research_source_record"), strings.HasPrefix(op, "research_freshness_set"):
		return "concord_work_define"
	case strings.HasPrefix(op, "research"):
		return "concord_work_trace"
	default:
		return "concord_work_transition"
	}
}
