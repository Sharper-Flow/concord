package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// Product authorization for research packs: a pack-ID read, an
// explicit research_bindings declaration, and a pack-content authoring
// operation must all resolve the pack's immutable owner work item and run the
// one work-reference rule against the caller's grant before any content or
// effect. The fixtures keep Products A and B disjoint, enter every assertion
// through the byte-oriented Invoke boundary, and snapshot full durable pack
// and binding state around every refused mutation.

const (
	researchScopeConsumer = "consumer-a"
	researchScopePackA    = "pack-scope-a"
	researchScopePackB    = "pack-scope-b"
	researchScopeOwnerA   = "owner-a"
	researchScopeOwnerB   = "owner-b"
	researchScopeMarkerA  = "Synthetic A-owned control"
	researchScopeMarkerB  = "Synthetic B-only"
)

type researchScopeEnv struct {
	store   *store.Store
	service *Service
	grant   Authority
}

func researchScopeFixture(t *testing.T, capabilities []agentcontract.Capability, products []string) researchScopeEnv {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := append(agentFixtureProductEvents("sc-a", "product-a", "project-a", "Scope A", "Scope A Project", "research scope fixture A"),
		agentFixtureProductEvents("sc-b", "product-b", "project-b", "Scope B", "Scope B Project", "research scope fixture B")...)
	owners := []struct{ id, memberships string }{
		{researchScopeOwnerA, `[{"project_id":"project-a","role":"primary"}]`},
		{researchScopeOwnerB, `[{"project_id":"project-b","role":"primary"}]`},
		{"owner-mixed", `[{"project_id":"project-b","role":"primary"},{"project_id":"project-a","role":"secondary"}]`},
		{"owner-move", `[{"project_id":"project-a","role":"primary"}]`},
		{"owner-done", `[{"project_id":"project-a","role":"primary"}]`},
		{"owner-none", `[{"project_id":"project-a","role":"primary"}]`},
		{researchScopeConsumer, `[{"project_id":"project-a","role":"primary"}]`},
	}
	for _, owner := range owners {
		events = append(events, store.Event{EventID: "sc-work-" + owner.id, Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: owner.id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Scope ` + owner.id + `","priority":1}`)})
		if owner.memberships != "" {
			events = append(events, store.Event{EventID: "sc-members-" + owner.id, Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: owner.id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":` + owner.memberships + `,"expected_version":1,"resulting_version":2}`)})
		}
	}
	expected := map[store.SubjectRef]int64{
		store.VersionRef(store.SubjectProduct, "product-a"): 0, store.VersionRef(store.SubjectProduct, "product-b"): 0,
		store.VersionRef(store.SubjectProject, "project-a"): 0, store.VersionRef(store.SubjectProject, "project-b"): 0,
	}
	for _, owner := range owners {
		expected[store.VersionRef(store.SubjectWorkItem, owner.id)] = 0
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
	seedResearchScopePack(t, s, researchScopePackB, researchScopeOwnerB, researchScopeMarkerB+" research question", researchScopeMarkerB+" finding statement")
	seedResearchScopePack(t, s, researchScopePackA, researchScopeOwnerA, researchScopeMarkerA+" research question", researchScopeMarkerA+" finding statement")
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: researchScopeConsumer, Definition: definition, Actor: store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, products, []string{"project-a"}, store.ProjectResolution{ProjectID: "project-a"})
	return researchScopeEnv{store: s, service: service, grant: grant}
}

func seedResearchScopePack(t *testing.T, s *store.Store, packID, owner, question, finding string) {
	t.Helper()
	ctx := context.Background()
	pack, err := createResearchPack(ctx, s, store.CreateResearchPackRequest{
		PackID: packID, OwnerWorkID: owner, Freshness: store.ResearchCurrent,
		Revision: store.ResearchRevisionInput{Question: question, Method: "source inspection", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("seed pack %s: %v", packID, err)
	}
	if err := recordResearchSource(ctx, s, store.ResearchSourceRequest{
		PackID: packID, Revision: 1, ExpectedVersion: pack.ExpectedVersion,
		Source: store.ResearchSource{SourceID: "src-" + packID, Kind: store.SourceCode, Locator: "internal/agent/research_scope_authorization_test.go", Title: "research scope fixture", PublisherOrAuthor: "concord", AccessedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatalf("seed source %s: %v", packID, err)
	}
	if err := recordResearchFinding(ctx, s, store.ResearchFindingRequest{
		PackID: packID, Revision: 1, ExpectedVersion: pack.ExpectedVersion + 1,
		Finding: store.ResearchFinding{FindingID: "f-" + packID, Kind: store.FindingObservation, Statement: finding, Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive, Scopes: store.ResearchScopes{Mode: "home"}},
	}); err != nil {
		t.Fatalf("seed finding %s: %v", packID, err)
	}
}

func researchScopeCompleteWork(t *testing.T, s *store.Store, id string) {
	t.Helper()
	version := workVersion(t, s, id)
	payload, err := json.Marshal(map[string]any{"from": "needed", "to": "completed", "reason": "research scope fixture", "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: "sc-complete-" + id, Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, id): version}}); err != nil {
		t.Fatal(err)
	}
}

func researchScopeMoveWorkToProjectB(t *testing.T, s *store.Store, id string) {
	t.Helper()
	version := workVersion(t, s, id)
	payload, err := json.Marshal(map[string]any{"memberships": []map[string]any{{"project_id": "project-b", "role": "primary"}}, "expected_version": version, "resulting_version": version + 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: "sc-move-" + id, Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, id): version}}); err != nil {
		t.Fatal(err)
	}
}

// researchScopeNarrowGrantToProductA replaces client-1's live policy with an
// A-only, cross_scope-free statement. The authorize path reads agent_clients
// on every call, so every later invocation runs under the narrowed grant with
// the same actor tuple.
func researchScopeNarrowGrantToProductA(t *testing.T, f researchScopeEnv) {
	t.Helper()
	must := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	policy := store.TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "human-1", CapabilitiesJSON: must([]string{"product_read", "work_transition"}), ProductScopeJSON: must([]string{"product-a"}), ProjectScopeJSON: must([]string{"project-a"}), AgentScopeJSON: must([]string{"agent-1"})}
	if err := f.store.UpdateTrustedClientPolicy(context.Background(), "client-1", policy, fixedTime().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

// researchScopeEnvelope builds the call envelope with the ambient Project's
// current scope version, so a fixture membership move can never strand an
// invocation behind a stale_context refusal.
func researchScopeEnvelope(f researchScopeEnv) (CallEnvelope, error) {
	scopeVersion, _, err := f.store.ScopeVersion(context.Background(), "project-a")
	if err != nil {
		return CallEnvelope{}, err
	}
	return CallEnvelope{SchemaVersion: "1.0", RequestID: "research-scope-request", ClientRef: f.grant.ClientRef, PrincipalRef: f.grant.PrincipalRef, SessionRef: f.grant.SessionRef, AgentRef: f.grant.AgentRef, Directory: f.grant.Directory, Worktree: f.grant.Worktree, AmbientProjectID: "project-a", SelectedProductID: "product-a", ScopeVersion: scopeVersion, ManifestDigest: f.grant.ManifestDigest}, nil
}

func researchScopeInvokeEnvelope(t *testing.T, f researchScopeEnv, operation string, input map[string]any, envelope *CallEnvelope) agentcontract.Envelope {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if envelope == nil {
		built, err := researchScopeEnvelope(f)
		if err != nil {
			t.Fatal(err)
		}
		envelope = &built
	}
	envelopeBytes, err := json.Marshal(*envelope)
	if err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(InvokeRequest{CallEnvelope: envelopeBytes, Tool: toolForOperation(operation), Operation: operation, Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Invoke(context.Background(), f.store, f.service, request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func researchScopeInvoke(t *testing.T, f researchScopeEnv, operation string, input map[string]any) agentcontract.Envelope {
	t.Helper()
	return researchScopeInvokeEnvelope(t, f, operation, input, nil)
}

func researchScopeAuthoringAllowed(t *testing.T, f researchScopeEnv, operation string, input map[string]any) agentcontract.Envelope {
	t.Helper()
	return researchScopeInvoke(t, f, operation, input)
}

func researchScopePackVersion(t *testing.T, s *store.Store, packID string) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, packID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// researchScopeEffects carries the full durable surface a forbidden research
// call could touch: the whole pack row, the ordered content of every child
// table, every consumer binding tuple, the consumer's work/step/event state,
// and the global receipt, durable-operation, and approval-consumption state.
type researchScopeEffects struct {
	packOwner       string
	packRevision    int64
	packFreshness   string
	packVersion     int64
	packCreatedAt   string
	packUpdatedAt   string
	revisionCount   int64
	revisions       string
	findingCount    int64
	findings        string
	sourceCount     int64
	sources         string
	pinCount        int64
	pins            string
	consumerVersion int64
	consumerStep    string
	events          int64
	consumerEvents  int64
	receipts        int64
	durableOps      int64
}

func researchScopeSnapshot(t *testing.T, s *store.Store, packID string) researchScopeEffects {
	t.Helper()
	db := s.DatabaseForTesting()
	load := func(query string, args []any, dest ...any) {
		t.Helper()
		if err := db.QueryRow(query, args...).Scan(dest...); err != nil {
			t.Fatalf("snapshot %s: %v", query, err)
		}
	}
	var e researchScopeEffects
	load(`SELECT owner_work_id,current_revision,freshness,expected_version,created_at,updated_at FROM active_research_packs WHERE pack_id=?`, []any{packID}, &e.packOwner, &e.packRevision, &e.packFreshness, &e.packVersion, &e.packCreatedAt, &e.packUpdatedAt)
	load(`SELECT count(*),COALESCE(group_concat(x),'') FROM (SELECT revision||'|'||question||'|'||method||'|'||created_at AS x FROM active_research_revisions WHERE pack_id=? ORDER BY revision)`, []any{packID}, &e.revisionCount, &e.revisions)
	load(`SELECT count(*),COALESCE(group_concat(x),'') FROM (SELECT revision||'|'||finding_id||'|'||kind||'|'||statement||'|'||confidence||'|'||freshness||'|'||status AS x FROM active_research_findings WHERE pack_id=? ORDER BY revision,finding_id)`, []any{packID}, &e.findingCount, &e.findings)
	load(`SELECT count(*),COALESCE(group_concat(x),'') FROM (SELECT revision||'|'||source_id||'|'||kind||'|'||locator||'|'||title||'|'||accessed_at AS x FROM active_research_sources WHERE pack_id=? ORDER BY revision,source_id)`, []any{packID}, &e.sourceCount, &e.sources)
	load(`SELECT count(*),COALESCE(group_concat(x),'') FROM (SELECT pack_id||'/'||consumer_work_id||'/'||revision||'/'||use_role||'/'||required||'/'||accepted_at AS x FROM active_research_consumers ORDER BY pack_id,consumer_work_id,revision)`, nil, &e.pinCount, &e.pins)
	load(`SELECT version FROM work_items WHERE id=?`, []any{researchScopeConsumer}, &e.consumerVersion)
	load(`SELECT current_step FROM workflow_instances WHERE work_id=?`, []any{researchScopeConsumer}, &e.consumerStep)
	load(`SELECT last FROM store_sequences WHERE name='effect'`, nil, &e.events)
	load(`SELECT effect_seq FROM work_items WHERE id=?`, []any{researchScopeConsumer}, &e.consumerEvents)
	load(`SELECT count(*) FROM idempotency_records`, nil, &e.receipts)
	load(`SELECT count(*) FROM durable_operations`, nil, &e.durableOps)
	return e
}

func researchScopeAssertNoEffects(t *testing.T, before, after researchScopeEffects) {
	t.Helper()
	if before != after {
		t.Fatalf("forbidden call changed durable state:\n before %+v\n after  %+v", before, after)
	}
}

func researchScopeAssertRefused(t *testing.T, read agentcontract.Envelope, wantKind string, secret ...string) {
	t.Helper()
	if read.Outcome != agentcontract.OutcomeError || read.Error == nil {
		t.Fatalf("expected a refusal, got outcome %q result %s", read.Outcome, read.Result)
	}
	if read.Error.Kind != wantKind {
		t.Fatalf("expected kind %s, got %+v", wantKind, read.Error)
	}
	if read.Error.EffectState != agentcontract.EffectNone {
		t.Fatalf("refusal must carry no effect, got %+v", read.Error)
	}
	serialized, err := json.Marshal(read)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range secret {
		if strings.Contains(string(serialized), marker) {
			t.Fatalf("refusal leaked %q: %s", marker, serialized)
		}
	}
}

// researchScopeBindingAction builds a workflow_action input at the consumer's
// current version. record_proposal carries its retained required fields;
// record_work_context is the retained hold-step action with an empty
// declaration, so a follow-up action never advances or retires the proposal.
func researchScopeBindingAction(t *testing.T, f researchScopeEnv, action, key string, bindings []map[string]any) map[string]any {
	t.Helper()
	fields := map[string]any{}
	if action == "record_proposal" {
		fields = map[string]any{"problem": "Synthetic bounded change", "affected": []string{"Synthetic users"}, "stakes": "Preserve the test boundary", "user_outcomes": []string{"A bounded verified result"}}
	}
	input := map[string]any{"work_id": researchScopeConsumer, "expected_version": workVersion(t, f.store, researchScopeConsumer), "action_id": action, "idempotency_key": key, "fields": fields}
	if bindings != nil {
		input["research_bindings"] = bindings
	}
	return input
}

func researchScopeBinding(pack string, revision int64, required bool) map[string]any {
	return map[string]any{"pack_id": pack, "revision": revision, "use_role": "context", "required": required}
}

// researchScopeAuthoringInputs is the shared public input table for the four
// content-authoring operations, used by both the foreign-pack refusals and
// the policy matrix.
func researchScopeAuthoringInputs() []struct {
	name  string
	input func(pack string, version int64) map[string]any
} {
	return []struct {
		name  string
		input func(pack string, version int64) map[string]any
	}{
		{"research_revision_append", func(pack string, version int64) map[string]any {
			return map[string]any{"pack_id": pack, "expected_version": version, "revision": map[string]any{"question": "Synthetic appended question", "method": "source inspection"}, "idempotency_key": "sc-append-" + pack}
		}},
		{"research_finding_record", func(pack string, version int64) map[string]any {
			return map[string]any{"pack_id": pack, "expected_version": version, "finding": map[string]any{"finding_id": "f-scope", "kind": "observation", "statement": "Synthetic finding statement", "confidence": "high"}, "idempotency_key": "sc-finding-" + pack}
		}},
		{"research_source_record", func(pack string, version int64) map[string]any {
			return map[string]any{"pack_id": pack, "expected_version": version, "source": map[string]any{"source_id": "src-scope", "kind": "source_code", "locator": "internal/agent", "title": "Scope", "publisher_or_author": "concord", "accessed_at": "2026-01-01T00:00:00Z"}, "idempotency_key": "sc-source-" + pack}
		}},
		{"research_freshness_set", func(pack string, version int64) map[string]any {
			return map[string]any{"pack_id": pack, "expected_version": version, "freshness": "stale", "idempotency_key": "sc-fresh-" + pack}
		}},
	}
}

// R1: every pack-ID materialization variant of concord_work_trace.research
// authorizes the pack owner's Products before returning content. The same
// selectors stay deliverable for an in-scope control pack.
func TestResearchScopePackReadRefusesForeignOwner(t *testing.T) {
	t.Parallel()
	f := researchScopeFixture(t, []agentcontract.Capability{"product_read"}, []string{"product-a"})
	variants := []struct {
		name  string
		input func(pack string) map[string]any
	}{
		{"legacy_omitted_version", func(pack string) map[string]any { return map[string]any{"product_id": "product-a", "pack_id": pack} }},
		{"legacy_explicit_version", func(pack string) map[string]any {
			return map[string]any{"product_id": "product-a", "pack_id": pack, "result_version": 1}
		}},
		{"v2_full_pack", func(pack string) map[string]any {
			return map[string]any{"product_id": "product-a", "pack_id": pack, "result_version": 2}
		}},
		{"v2_exact_revision", func(pack string) map[string]any {
			return map[string]any{"product_id": "product-a", "pack_id": pack, "result_version": 2, "revision": 1}
		}},
		{"v2_exact_finding", func(pack string) map[string]any {
			return map[string]any{"product_id": "product-a", "pack_id": pack, "result_version": 2, "revision": 1, "finding_ids": []string{"f-" + pack}}
		}},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			read := researchScopeInvoke(t, f, "research", variant.input(researchScopePackB))
			researchScopeAssertRefused(t, read, "unauthorized", researchScopePackB, researchScopeOwnerB, researchScopeMarkerB)
			control := researchScopeInvoke(t, f, "research", variant.input(researchScopePackA))
			if control.Outcome != agentcontract.OutcomeOK || strings.Contains(string(control.Result), researchScopeMarkerB) {
				t.Fatalf("in-scope control must deliver its own content: outcome %q result %s", control.Outcome, control.Result)
			}
		})
	}
	t.Run("legacy_control_shape", func(t *testing.T) {
		read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "pack_id": researchScopePackA})
		if read.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("legacy control failed: %+v", read.Error)
		}
		var pack store.ResearchPack
		if err := json.Unmarshal(read.Result, &pack); err != nil {
			t.Fatal(err)
		}
		if pack.PackID != researchScopePackA || pack.OwnerWorkID != researchScopeOwnerA || pack.CurrentRevision != 1 || len(pack.Revisions) != 1 || len(pack.Revisions[0].Findings) != 1 {
			t.Fatalf("legacy control lost its content: %+v", pack)
		}
	})
	t.Run("v2_selection_controls", func(t *testing.T) {
		exact := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "pack_id": researchScopePackA, "result_version": 2, "revision": 1})
		if exact.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("exact control failed: %+v", exact.Error)
		}
		var wrapper struct {
			ResultVersion int                `json:"result_version"`
			Pack          store.ResearchPack `json:"pack"`
		}
		if err := json.Unmarshal(exact.Result, &wrapper); err != nil {
			t.Fatal(err)
		}
		if wrapper.ResultVersion != 2 || len(wrapper.Pack.Revisions) != 1 || wrapper.Pack.Revisions[0].Revision != 1 {
			t.Fatalf("exact control lost its selection: %+v", wrapper)
		}
		selected := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "pack_id": researchScopePackA, "result_version": 2, "revision": 1, "finding_ids": []string{"f-" + researchScopePackA}})
		if selected.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("selected control failed: %+v", selected.Error)
		}
		if err := json.Unmarshal(selected.Result, &wrapper); err != nil {
			t.Fatal(err)
		}
		if len(wrapper.Pack.Revisions[0].Findings) != 1 || wrapper.Pack.Revisions[0].Findings[0].FindingID != "f-"+researchScopePackA {
			t.Fatalf("selected control lost its finding: %+v", wrapper.Pack)
		}
	})
}

// Owner descriptor pages keep their existing top-level work_id gate: refused
// for an out-of-grant owner, readable under a grant that covers the owner,
// and still refusing the version 1 owner read.
func TestResearchScopeOwnerPagesKeepTheirGate(t *testing.T) {
	t.Parallel()
	t.Run("foreign_owner_page_refused", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read"}, []string{"product-a"})
		read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "work_id": researchScopeOwnerB, "result_version": 2})
		researchScopeAssertRefused(t, read, "unauthorized", researchScopePackB, researchScopeMarkerB)
	})
	t.Run("covered_owner_page_readable_without_cross_scope", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read"}, []string{"product-a", "product-b"})
		read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "work_id": researchScopeOwnerB, "result_version": 2})
		if read.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("covered owner page failed: %+v", read.Error)
		}
		var page struct {
			ResultVersion int              `json:"result_version"`
			Packs         []map[string]any `json:"packs"`
		}
		if err := json.Unmarshal(read.Result, &page); err != nil {
			t.Fatal(err)
		}
		if page.ResultVersion != 2 || len(page.Packs) != 1 || page.Packs[0]["pack_id"] != researchScopePackB {
			t.Fatalf("covered owner page lost the pack: %+v", page)
		}
	})
	t.Run("legacy_owner_read_still_refused", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read"}, []string{"product-a"})
		read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "work_id": researchScopeOwnerA})
		if read.Error == nil || read.Error.Kind != "invalid_input" || !strings.Contains(read.Error.Message, "result_version") {
			t.Fatalf("legacy owner read must refuse invalid_input naming result_version, got %+v", read.Error)
		}
	})
}

// R2: a declared binding on a foreign pack refuses the whole action before
// any effect, whatever the requiredness or the declared revision. Owner
// authorization must precede revision freshness and existence details, and it
// adds no capability beyond the action's own work_transition requirement.
func TestResearchScopeBindingRefusesForeignPack(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		capabilities []agentcontract.Capability
		stale        bool
		binding      func(pack string) map[string]any
		wantKind     string
		wantNoDetail string
	}{
		{"required_current", []agentcontract.Capability{"product_read", "work_transition"}, false, func(pack string) map[string]any { return researchScopeBinding(pack, 1, true) }, "unauthorized", ""},
		{"optional_current", []agentcontract.Capability{"product_read", "work_transition"}, false, func(pack string) map[string]any { return researchScopeBinding(pack, 1, false) }, "unauthorized", ""},
		{"optional_stale", []agentcontract.Capability{"product_read", "work_transition"}, true, func(pack string) map[string]any { return researchScopeBinding(pack, 1, false) }, "unauthorized", ""},
		{"forbidden_revision", []agentcontract.Capability{"product_read", "work_transition"}, false, func(pack string) map[string]any { return researchScopeBinding(pack, 999, false) }, "unauthorized", "revision"},
		{"transition_capability_only", []agentcontract.Capability{"work_transition"}, false, func(pack string) map[string]any { return researchScopeBinding(pack, 1, true) }, "unauthorized", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := researchScopeFixture(t, test.capabilities, []string{"product-a"})
			if test.stale {
				if err := setResearchFreshness(context.Background(), f.store, store.SetResearchFreshnessRequest{
					PackID: researchScopePackB, Revision: 1, ExpectedVersion: researchScopePackVersion(t, f.store, researchScopePackB), Freshness: store.ResearchStale,
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := researchScopeSnapshot(t, f.store, researchScopePackB)
			input := researchScopeBindingAction(t, f, "record_proposal", "bind-"+test.name, []map[string]any{test.binding(researchScopePackB)})
			read := researchScopeInvoke(t, f, "workflow_action", input)
			researchScopeAssertRefused(t, read, test.wantKind, researchScopePackB, researchScopeOwnerB, researchScopeMarkerB)
			if test.wantNoDetail != "" && strings.Contains(read.Error.Message, test.wantNoDetail) {
				t.Fatalf("refusal reported %s details before owner authorization: %s", test.wantNoDetail, read.Error.Message)
			}
			researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, researchScopePackB))
		})
	}
}

// A foreign declaration replaces nothing and poisons nothing. Three witnesses:
// an action replacing an in-scope pin, a mixed allowed and foreign batch, and
// a caller whose authorization narrowed after an allowed pin — the retained
// pin survives, a declaration-free action still passes, and an explicit
// replacement of the now-foreign pin refuses whole.
func TestResearchScopeBindingReplacementAndBatchRefuseAtomically(t *testing.T) {
	t.Parallel()
	t.Run("replacement_by_foreign_declaration", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition", "research"}, []string{"product-a"})
		pinned := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "pin-a", []map[string]any{researchScopeBinding(researchScopePackA, 1, true)}))
		if pinned.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("in-scope pin failed: %+v", pinned.Error)
		}
		before := researchScopeSnapshot(t, f.store, researchScopePackA)
		refused := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_work_context", "replace-b", []map[string]any{researchScopeBinding(researchScopePackB, 1, true)}))
		researchScopeAssertRefused(t, refused, "unauthorized", researchScopePackB, researchScopeMarkerB)
		researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, researchScopePackA))
		// The required pin on revision 1 seals only that revision: all four
		// content-authoring paths stay deliverable on the pack's new current
		// revision, so a retained pin never freezes a pack shut.
		for _, operation := range researchScopeAuthoringInputs() {
			version := researchScopePackVersion(t, f.store, researchScopePackA)
			authored := researchScopeInvoke(t, f, operation.name, operation.input(researchScopePackA, version))
			if authored.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("pinned-pack %s failed: %+v", operation.name, authored.Error)
			}
			if got := researchScopePackVersion(t, f.store, researchScopePackA); got != version+1 {
				t.Fatalf("pinned-pack %s left the pack at version %d, want %d", operation.name, got, version+1)
			}
		}
	})
	t.Run("mixed_allowed_and_foreign_batch", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		beforeA := researchScopeSnapshot(t, f.store, researchScopePackA)
		beforeB := researchScopeSnapshot(t, f.store, researchScopePackB)
		refused := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "mixed-batch", []map[string]any{
			researchScopeBinding(researchScopePackA, 1, true),
			researchScopeBinding(researchScopePackB, 1, true),
		}))
		researchScopeAssertRefused(t, refused, "unauthorized", researchScopePackB, researchScopeMarkerB)
		researchScopeAssertNoEffects(t, beforeA, researchScopeSnapshot(t, f.store, researchScopePackA))
		researchScopeAssertNoEffects(t, beforeB, researchScopeSnapshot(t, f.store, researchScopePackB))
	})
	t.Run("narrowed_caller_replaces_nothing", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		pack := "pack-scope-moved"
		seedResearchScopePack(t, f.store, pack, "owner-move", researchScopeMarkerA+" moved question", researchScopeMarkerA+" moved finding")
		initial := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "pin-moved", []map[string]any{researchScopeBinding(pack, 1, true)}))
		if initial.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("initial in-scope pin failed: %+v", initial.Error)
		}
		researchScopeMoveWorkToProjectB(t, f.store, "owner-move")
		free := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_work_context", "free-after-move", nil))
		if free.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("retained pin must not demand owner recheck: %+v", free.Error)
		}
		before := researchScopeSnapshot(t, f.store, pack)
		replacement := researchScopeBinding(pack, 1, true)
		replacement["use_role"] = "design_input"
		refused := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_work_context", "replace-after-move", []map[string]any{replacement}))
		researchScopeAssertRefused(t, refused, "unauthorized", pack, researchScopeMarkerA)
		researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, pack))
	})
}

// R3: the four content-authoring operations authorize the pack owner before
// any effect, and stay deliverable on an in-scope control pack.
func TestResearchScopeAuthoringRefusesForeignPack(t *testing.T) {
	t.Parallel()
	f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "research"}, []string{"product-a"})
	for _, operation := range researchScopeAuthoringInputs() {
		t.Run("foreign_"+operation.name, func(t *testing.T) {
			before := researchScopeSnapshot(t, f.store, researchScopePackB)
			read := researchScopeInvoke(t, f, operation.name, operation.input(researchScopePackB, before.packVersion))
			researchScopeAssertRefused(t, read, "unauthorized", researchScopePackB, researchScopeOwnerB, researchScopeMarkerB)
			researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, researchScopePackB))
		})
	}
	for _, operation := range researchScopeAuthoringInputs() {
		t.Run("control_"+strings.TrimPrefix(operation.name, "research_"), func(t *testing.T) {
			pack := "pack-ctl-" + strings.TrimPrefix(operation.name, "research_")
			seedResearchScopePack(t, f.store, pack, researchScopeOwnerA, researchScopeMarkerA+" question", researchScopeMarkerA+" finding")
			before := researchScopePackVersion(t, f.store, pack)
			read := researchScopeInvoke(t, f, operation.name, operation.input(pack, before))
			if read.Outcome != agentcontract.OutcomeOK {
				t.Fatalf("in-scope %s failed: %+v", operation.name, read.Error)
			}
			if got := researchScopePackVersion(t, f.store, pack); got != before+1 {
				t.Fatalf("%s left the pack at version %d, want %d", operation.name, got, before+1)
			}
		})
	}
}

// C1: the exact policy matrix across read, all four authoring operations, and
// bind. Reads need the owner's Products inside the grant; mutations
// additionally need cross_scope when the owner is outside the selected
// Product. The authoring operations run before the binding for two ordering
// reasons: a binding seals its revision against later content writes, and the
// append runs first so the no-revision freshness set lands on the new current
// revision and leaves revision 1 current for the required bind. cross_scope
// alone never widens the grant.
func TestResearchScopePolicyMatrix(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		products   []string
		crossScope bool
		readOK     bool
		authorOK   bool
		bindOK     bool
	}{
		{"grant_a", []string{"product-a"}, false, false, false, false},
		{"grant_a_cross_scope", []string{"product-a"}, true, false, false, false},
		{"grant_ab", []string{"product-a", "product-b"}, false, true, false, false},
		{"grant_ab_cross_scope", []string{"product-a", "product-b"}, true, true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			capabilities := []agentcontract.Capability{"product_read", "work_transition", "research"}
			if test.crossScope {
				capabilities = append(capabilities, agentcontract.Capability("cross_scope"))
			}
			f := researchScopeFixture(t, capabilities, test.products)
			// Separate subtests over one fixture: a red read must not conceal
			// the authoring and bind matrices behind an early fatal.
			t.Run("read", func(t *testing.T) {
				read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "pack_id": researchScopePackB, "result_version": 2})
				if test.readOK != (read.Outcome == agentcontract.OutcomeOK) {
					t.Fatalf("read outcome %q error %+v, want ok=%t", read.Outcome, read.Error, test.readOK)
				}
				if !test.readOK {
					researchScopeAssertRefused(t, read, "unauthorized", researchScopeMarkerB)
				}
			})
			t.Run("authoring", func(t *testing.T) {
				for _, operation := range researchScopeAuthoringInputs() {
					var author agentcontract.Envelope
					if test.authorOK {
						author = researchScopeAuthoringAllowed(t, f, operation.name, operation.input(researchScopePackB, researchScopePackVersion(t, f.store, researchScopePackB)))
					} else {
						author = researchScopeInvoke(t, f, operation.name, operation.input(researchScopePackB, researchScopePackVersion(t, f.store, researchScopePackB)))
					}
					if test.authorOK != (author.Outcome == agentcontract.OutcomeOK) {
						t.Fatalf("%s outcome %q error %+v, want ok=%t", operation.name, author.Outcome, author.Error, test.authorOK)
					}
					if !test.authorOK {
						researchScopeAssertRefused(t, author, "unauthorized", researchScopeMarkerB)
					}
				}
			})
			t.Run("bind", func(t *testing.T) {
				binding := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "bind-matrix", []map[string]any{researchScopeBinding(researchScopePackB, 1, true)}))
				if test.bindOK != (binding.Outcome == agentcontract.OutcomeOK) {
					t.Fatalf("bind outcome %q error %+v, want ok=%t", binding.Outcome, binding.Error, test.bindOK)
				}
				if test.bindOK {
					var pins int
					if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, researchScopePackB, researchScopeConsumer).Scan(&pins); err != nil || pins != 1 {
						t.Fatalf("allowed bind left no pin: pins=%d err=%v", pins, err)
					}
				} else {
					researchScopeAssertRefused(t, binding, "unauthorized", researchScopeMarkerB)
				}
			})
		})
	}
}

// C1 edges: secondary owner membership counts as in-Product scope without
// cross_scope, a membership-less owner refuses unknown_scope, a missing pack
// keeps its typed absence refusal, and a terminal owner keeps the retained
// read/bind reuse while content authoring stays read-only.
func TestResearchScopeSpecialOwners(t *testing.T) {
	t.Parallel()
	t.Run("secondary_membership_reads_and_binds_authoring_needs_every_owner_product", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition", "research"}, []string{"product-a"})
		pack := "pack-scope-mixed"
		seedResearchScopePack(t, f.store, pack, "owner-mixed", researchScopeMarkerA+" mixed question", researchScopeMarkerA+" mixed finding")
		// Authoring scopes the owner through the mutation plan, the same
		// mechanism as pack create and every other owner-work mutation: each
		// derived owner Product must sit inside the grant. The owner's primary
		// Product B is outside the A-only grant, so authoring refuses whole.
		for _, operation := range researchScopeAuthoringInputs() {
			before := researchScopeSnapshot(t, f.store, pack)
			authored := researchScopeInvoke(t, f, operation.name, operation.input(pack, before.packVersion))
			researchScopeAssertRefused(t, authored, "unauthorized")
			researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, pack))
		}
		read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "pack_id": pack, "result_version": 2})
		if read.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("secondary-membership read failed: %+v", read.Error)
		}
		bound := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "bind-mixed", []map[string]any{researchScopeBinding(pack, 1, true)}))
		if bound.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("secondary-membership bind failed: %+v", bound.Error)
		}
	})
	t.Run("membershipless_owner_refuses_unknown_scope", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition", "research"}, []string{"product-a"})
		pack := "pack-scope-orphan"
		seedResearchScopePack(t, f.store, pack, "owner-none", researchScopeMarkerA+" orphan question", researchScopeMarkerA+" orphan finding")
		// A missing membership is corrupt retained state, not a legal work
		// creation: isolate the fault after the fixture passes admission.
		if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM work_projects WHERE work_id='owner-none'; DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		before := researchScopeSnapshot(t, f.store, pack)
		read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "pack_id": pack, "result_version": 2})
		researchScopeAssertRefused(t, read, "unknown_scope")
		bound := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "bind-orphan", []map[string]any{researchScopeBinding(pack, 1, true)}))
		researchScopeAssertRefused(t, bound, "unknown_scope")
		authored := researchScopeInvoke(t, f, "research_freshness_set", map[string]any{"pack_id": pack, "expected_version": before.packVersion, "freshness": "stale", "idempotency_key": "sc-orphan-fresh"})
		researchScopeAssertRefused(t, authored, "unknown_scope")
		researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, pack))
	})
	t.Run("missing_pack_keeps_typed_absence", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition", "research"}, []string{"product-a"})
		read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "pack_id": "pack-absent", "result_version": 2})
		researchScopeAssertRefused(t, read, "unknown_scope")
		bound := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "bind-absent", []map[string]any{researchScopeBinding("pack-absent", 1, true)}))
		researchScopeAssertRefused(t, bound, "unknown_scope")
		for _, operation := range researchScopeAuthoringInputs() {
			authored := researchScopeInvoke(t, f, operation.name, operation.input("pack-absent", 1))
			researchScopeAssertRefused(t, authored, "unknown_scope")
		}
	})
	t.Run("terminal_owner_keeps_read_and_bind_refuses_authoring", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition", "research"}, []string{"product-a"})
		pack := "pack-scope-done"
		seedResearchScopePack(t, f.store, pack, "owner-done", researchScopeMarkerA+" done question", researchScopeMarkerA+" done finding")
		researchScopeCompleteWork(t, f.store, "owner-done")
		read := researchScopeInvoke(t, f, "research", map[string]any{"product_id": "product-a", "pack_id": pack, "result_version": 2})
		if read.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("terminal-owner read failed: %+v", read.Error)
		}
		bound := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "bind-done", []map[string]any{researchScopeBinding(pack, 1, true)}))
		if bound.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("terminal-owner bind failed: %+v", bound.Error)
		}
		before := researchScopeSnapshot(t, f.store, pack)
		authored := researchScopeInvoke(t, f, "research_finding_record", map[string]any{
			"pack_id": pack, "expected_version": before.packVersion,
			"finding": map[string]any{"finding_id": "f-late", "kind": "observation", "statement": "Synthetic late finding", "confidence": "high"}, "idempotency_key": "sc-done-finding",
		})
		researchScopeAssertRefused(t, authored, "invalid_input")
		researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, pack))
	})
}

// C2: an allowed binding replay returns the original result without a second
// effect; after the caller's grant narrows, the retained required pin still
// passes a declaration-free action while a new declaration refuses; and a
// forbidden call leaves no receipt a replay could reuse.
func TestResearchScopeBindingReplayAndRetainedPins(t *testing.T) {
	t.Parallel()
	t.Run("allowed_replay_bumps_once_then_narrowed_grant", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition", "cross_scope"}, []string{"product-a", "product-b"})
		input := researchScopeBindingAction(t, f, "record_proposal", "bind-replay", []map[string]any{researchScopeBinding(researchScopePackB, 1, true)})
		first := researchScopeInvoke(t, f, "workflow_action", input)
		if first.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("allowed bind failed: %+v", first.Error)
		}
		afterFirst := researchScopeSnapshot(t, f.store, researchScopePackB)
		replay := researchScopeInvoke(t, f, "workflow_action", input)
		if replay.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("allowed replay failed: %+v", replay.Error)
		}
		researchScopeAssertNoEffects(t, afterFirst, researchScopeSnapshot(t, f.store, researchScopePackB))
		researchScopeNarrowGrantToProductA(t, f)
		free := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_work_context", "free-after-narrow", nil))
		if free.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("narrowed caller must keep the retained required pin without owner recheck: %+v", free.Error)
		}
		fresh := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_work_context", "declare-after-narrow", []map[string]any{researchScopeBinding(researchScopePackB, 1, true)}))
		researchScopeAssertRefused(t, fresh, "unauthorized", researchScopeMarkerB)
	})
	t.Run("forbidden_call_leaves_no_replay_receipt", func(t *testing.T) {
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		input := researchScopeBindingAction(t, f, "record_proposal", "bind-forbidden-replay", []map[string]any{researchScopeBinding(researchScopePackB, 1, true)})
		refused := researchScopeInvoke(t, f, "workflow_action", input)
		researchScopeAssertRefused(t, refused, "unauthorized", researchScopeMarkerB)
		var receipts int
		if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE idempotency_key=?`, "bind-forbidden-replay").Scan(&receipts); err != nil || receipts != 0 {
			t.Fatalf("forbidden call left %d replay receipts (err=%v)", receipts, err)
		}
		again := researchScopeInvoke(t, f, "workflow_action", input)
		researchScopeAssertRefused(t, again, "unauthorized", researchScopeMarkerB)
	})
}
