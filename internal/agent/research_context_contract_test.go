package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// Public publication contract for selected research. This test proves the
// promoted heads publish the selection path end to end: a byte-oriented
// Invoke on the authoritative workflow_action route, at the live
// implementation head whose closed payload declares research_finding_refs,
// with a valid registry Domain, authorizes the selected owner, publishes the
// resolved view into the work pin, and replays the exact selected payload
// without a second effect.

// seedResearchContextDomainRegistry commits one current Domain registry for
// the fixture Product at its designated knowledge home: a root Domain and one
// child. Every selection below travels the public Invoke route, which reads
// the committed registry.
func seedResearchContextDomainRegistry(t *testing.T, f researchScopeEnv) (root, child string) {
	t.Helper()
	root, child = "product-root:product-a", "registry-scope"
	registry := store.KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "product-a", RootDomainID: root,
		Domains: []store.KnowledgeDomain{
			{DomainID: root, Name: "fixture domain " + root, Purpose: "the root Domain of the research-context fixture", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
			{DomainID: child, Name: "fixture domain " + child, Purpose: "the child Domain of the research-context fixture", ParentDomainID: root, Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
		},
	}
	if _, err := pm1fixture.SeedProductDomainRegistry(context.Background(), f.store, "product-a", "project-a", t.TempDir(), registry); err != nil {
		t.Fatal(err)
	}
	return root, child
}

// researchContextSelectionAction builds one public workflow_action input on
// record_work_context: an optional same-action binding beside the selected
// references, at the consumer's current version.
func researchContextSelectionAction(t *testing.T, f researchScopeEnv, key string, bindings []map[string]any, selections []map[string]any) map[string]any {
	t.Helper()
	input := map[string]any{
		"work_id": researchScopeConsumer, "expected_version": workVersion(t, f.store, researchScopeConsumer),
		"action_id": "record_work_context", "idempotency_key": key,
		"fields": map[string]any{
			"required_reading": []any{}, "finding_refs": []any{}, "context_findings": []any{},
			"research_finding_refs": selections,
		},
	}
	if bindings != nil {
		input["research_bindings"] = bindings
	}
	return input
}

func researchContextSelectedRef(pack, finding, domain, rationale string) map[string]any {
	selection := map[string]any{"pack_id": pack, "revision": 1, "finding_id": finding, "domain_id": domain}
	if rationale != "" {
		selection["product_wide_rationale"] = rationale
	}
	return selection
}

// seedResearchContextLinkedPack authors one A-owned pack whose finding links
// its source descriptor, so the public resolved view proves complete
// selected provenance: the S3 shared fixture's finding stays accurately
// unsourced (sources resolve to the empty array).
func seedResearchContextLinkedPack(t *testing.T, f researchScopeEnv, packID string) {
	t.Helper()
	ctx := context.Background()
	pack, err := createResearchPack(ctx, f.store, store.CreateResearchPackRequest{
		PackID: packID, OwnerWorkID: researchScopeOwnerA, Freshness: store.ResearchCurrent,
		Revision: store.ResearchRevisionInput{Question: researchScopeMarkerA + " linked question", Method: "source inspection", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("seed linked pack %s: %v", packID, err)
	}
	if err := recordResearchSource(ctx, f.store, store.ResearchSourceRequest{
		PackID: packID, Revision: 1, ExpectedVersion: pack.ExpectedVersion,
		Source: store.ResearchSource{SourceID: "src-" + packID, Kind: store.SourceCode, Locator: "internal/agent/research_context_contract_test.go", Title: "research context fixture", PublisherOrAuthor: "concord", AccessedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatalf("seed linked source %s: %v", packID, err)
	}
	if err := recordResearchFinding(ctx, f.store, store.ResearchFindingRequest{
		PackID: packID, Revision: 1, ExpectedVersion: pack.ExpectedVersion + 1,
		Finding: store.ResearchFinding{FindingID: "f-" + packID, Kind: store.FindingObservation, Statement: researchScopeMarkerA + " linked finding statement", Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive, SourceIDs: []string{"src-" + packID}, Scopes: store.ResearchScopes{Mode: "home"}},
	}); err != nil {
		t.Fatalf("seed linked finding %s: %v", packID, err)
	}
}

// researchContextAssertResolvedView proves the published view actually
// carries the selected research: the resolved entry with its native finding,
// its linked source descriptors, and the group id namespace admitting the
// research view id.
func researchContextAssertResolvedView(t *testing.T, f researchScopeEnv, packID string, wantSources int) {
	t.Helper()
	pin, err := store.ReadWorkPin(context.Background(), f.store, researchScopeConsumer)
	if err != nil {
		t.Fatalf("published view read refused: %v", err)
	}
	if pin.WorkContext == nil || len(pin.WorkContext.ResearchFindings) != 1 {
		t.Fatalf("published view lost the selection: %+v", pin.WorkContext)
	}
	entry := pin.WorkContext.ResearchFindings[0]
	if entry.Selection.PackID != packID || entry.Selection.FindingID != "f-"+packID || entry.Selection.Revision != 1 {
		t.Fatalf("published entry lost its selection references: %+v", entry.Selection)
	}
	if entry.State != store.WorkContextResearchStateResolved || entry.Finding == nil || entry.Sources == nil {
		t.Fatalf("published entry state=%q finding=%v sources=%v, want the resolved research entry", entry.State, entry.Finding, entry.Sources)
	}
	if entry.Finding.FindingID != "f-"+packID || entry.Finding.Kind != store.FindingObservation || entry.Finding.Scopes.Mode != "home" {
		t.Fatalf("published entry lost the native finding: %+v", entry.Finding)
	}
	sources := *entry.Sources
	if len(sources) != wantSources {
		t.Fatalf("published entry sources = %+v, want %d linked descriptor(s)", sources, wantSources)
	}
	if wantSources == 1 && (sources[0].SourceID != "src-"+packID || sources[0].Locator == "") {
		t.Fatalf("published entry lost the linked descriptor: %+v", sources)
	}
	found := false
	for _, group := range pin.WorkContext.DomainGroups {
		for _, id := range group.FindingIDs {
			if strings.HasPrefix(id, "research:") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the published domain groups carry no research view id: %+v", pin.WorkContext.DomainGroups)
	}
}

func TestResearchContextContractPublishesSelectedResearch(t *testing.T) {
	t.Parallel()

	t.Run("same_product_selection_publishes_resolved_view", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		_, child := seedResearchContextDomainRegistry(t, f)
		linked := "pack-rc-linked"
		seedResearchContextLinkedPack(t, f, linked)
		input := researchContextSelectionAction(t, f, "rc-publish-a",
			[]map[string]any{researchScopeBinding(linked, 1, true)},
			[]map[string]any{researchContextSelectedRef(linked, "f-"+linked, child, "")})
		published := researchScopeInvoke(t, f, "workflow_action", input)
		if published.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("same-Product selection refused on the promoted head: %+v", published.Error)
		}
		researchContextAssertResolvedView(t, f, linked, 1)
	})

	t.Run("root_domain_selection_carries_rationale", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		root, _ := seedResearchContextDomainRegistry(t, f)
		input := researchContextSelectionAction(t, f, "rc-publish-root",
			[]map[string]any{researchScopeBinding(researchScopePackA, 1, true)},
			[]map[string]any{researchContextSelectedRef(researchScopePackA, "f-"+researchScopePackA, root, "the selection governs every Domain of the Product")})
		published := researchScopeInvoke(t, f, "workflow_action", input)
		if published.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("root-Domain selection refused: %+v", published.Error)
		}
		// The S3 fixture's finding links no source: the resolved entry stays
		// accurately unsourced as the empty array.
		researchContextAssertResolvedView(t, f, researchScopePackA, 0)
	})

	t.Run("selection_without_pin_refuses_whole", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		_, child := seedResearchContextDomainRegistry(t, f)
		input := researchContextSelectionAction(t, f, "rc-publish-nopin", nil,
			[]map[string]any{researchContextSelectedRef(researchScopePackA, "f-"+researchScopePackA, child, "")})
		before := researchScopeSnapshot(t, f.store, researchScopePackA)
		refused := researchScopeInvoke(t, f, "workflow_action", input)
		researchScopeAssertRefused(t, refused, "invalid_input")
		researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, researchScopePackA))
	})

	t.Run("cross_product_selection_denied_without_cross_scope_atomically", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		_, child := seedResearchContextDomainRegistry(t, f)
		input := researchContextSelectionAction(t, f, "rc-publish-b",
			[]map[string]any{researchScopeBinding(researchScopePackB, 1, true)},
			[]map[string]any{researchContextSelectedRef(researchScopePackB, "f-"+researchScopePackB, child, "")})
		before := researchScopeSnapshot(t, f.store, researchScopePackB)
		refused := researchScopeInvoke(t, f, "workflow_action", input)
		researchScopeAssertRefused(t, refused, "unauthorized", researchScopePackB, researchScopeMarkerB)
		researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, researchScopePackB))
	})

	t.Run("covered_cross_product_selection_publishes_with_grant_and_cross_scope", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition", "cross_scope"}, []string{"product-a", "product-b"})
		_, child := seedResearchContextDomainRegistry(t, f)
		input := researchContextSelectionAction(t, f, "rc-publish-b-granted",
			[]map[string]any{researchScopeBinding(researchScopePackB, 1, true)},
			[]map[string]any{researchContextSelectedRef(researchScopePackB, "f-"+researchScopePackB, child, "")})
		published := researchScopeInvoke(t, f, "workflow_action", input)
		if published.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("granted cross-Product selection refused: %+v", published.Error)
		}
		researchContextAssertResolvedView(t, f, researchScopePackB, 0)
	})

	t.Run("malformed_and_unknown_owner_selections_refuse_whole", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		root, child := seedResearchContextDomainRegistry(t, f)
		before := researchScopeSnapshot(t, f.store, researchScopePackA)
		bound := func(key string, mutate func(ref map[string]any)) map[string]any {
			ref := researchContextSelectedRef(researchScopePackA, "f-"+researchScopePackA, child, "")
			mutate(ref)
			return researchContextSelectionAction(t, f, key,
				[]map[string]any{researchScopeBinding(researchScopePackA, 1, true)}, []map[string]any{ref})
		}
		cases := []struct {
			name  string
			input map[string]any
			kind  string
		}{
			{"absent_pack", researchContextSelectionAction(t, f, "rc-absent", nil,
				[]map[string]any{researchContextSelectedRef("pack-absent", "f-pack-absent", child, "")}), "unknown_scope"},
			{"zero_revision", bound("rc-zero", func(ref map[string]any) { ref["revision"] = 0 }), "invalid_input"},
			{"unknown_domain", bound("rc-domain", func(ref map[string]any) { ref["domain_id"] = "registry-absent" }), "unknown_scope"},
			{"root_domain_without_rationale", bound("rc-root-bare", func(ref map[string]any) { ref["domain_id"] = root }), "invalid_input"},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				refused := researchScopeInvoke(t, f, "workflow_action", test.input)
				researchScopeAssertRefused(t, refused, test.kind)
				researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, researchScopePackA))
			})
		}
	})

	t.Run("worker_actions_carry_no_selection_authority", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		_, child := seedResearchContextDomainRegistry(t, f)
		// The promoted record_proposal payload never declares the selection
		// member, so the coordinator declaration action is the only authoring
		// surface: the closed payload refuses the field before any effect.
		input := researchScopeBindingAction(t, f, "record_proposal", "rc-worker-authority", nil)
		input["fields"] = map[string]any{
			"problem": "Synthetic bounded change", "affected": []string{"Synthetic users"},
			"stakes": "Preserve the test boundary", "user_outcomes": []string{"A bounded verified result"},
			"research_finding_refs": []map[string]any{researchContextSelectedRef(researchScopePackA, "f-"+researchScopePackA, child, "")},
		}
		before := researchScopeSnapshot(t, f.store, researchScopePackA)
		refused := researchScopeInvoke(t, f, "workflow_action", input)
		// The generated action schema refuses the undeclared member outright.
		researchScopeAssertRefused(t, refused, "invalid_input")
		researchScopeAssertNoEffects(t, before, researchScopeSnapshot(t, f.store, researchScopePackA))
	})

	t.Run("exact_selected_payload_replay_has_no_duplicate_effect", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		_, child := seedResearchContextDomainRegistry(t, f)
		linked := "pack-rc-replay"
		seedResearchContextLinkedPack(t, f, linked)
		input := researchContextSelectionAction(t, f, "rc-replay",
			[]map[string]any{researchScopeBinding(linked, 1, true)},
			[]map[string]any{researchContextSelectedRef(linked, "f-"+linked, child, "")})
		first := researchScopeInvoke(t, f, "workflow_action", input)
		if first.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("selected declaration failed: %+v", first.Error)
		}
		afterFirst := researchScopeSnapshot(t, f.store, linked)
		var events int64
		if err := f.store.DatabaseForTesting().QueryRow(`SELECT effect_seq FROM work_items WHERE id=?`, researchScopeConsumer).Scan(&events); err != nil {
			t.Fatal(err)
		}
		replay := researchScopeInvoke(t, f, "workflow_action", input)
		if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
			t.Fatalf("exact replay outcome=%q replayed=%v error=%+v", replay.Outcome, replay.Replayed, replay.Error)
		}
		researchScopeAssertNoEffects(t, afterFirst, researchScopeSnapshot(t, f.store, linked))
		var replayEvents int64
		if err := f.store.DatabaseForTesting().QueryRow(`SELECT effect_seq FROM work_items WHERE id=?`, researchScopeConsumer).Scan(&replayEvents); err != nil {
			t.Fatal(err)
		}
		if replayEvents != events {
			t.Fatalf("replay appended %d events, want no duplicate declaration", replayEvents-events)
		}
		researchContextAssertResolvedView(t, f, linked, 1)
	})

	t.Run("retained_reads_survive_unavailable_author_grant", func(t *testing.T) {
		t.Parallel()
		f := researchScopeFixture(t, []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-a"})
		_, child := seedResearchContextDomainRegistry(t, f)
		linked := "pack-rc-retained"
		seedResearchContextLinkedPack(t, f, linked)
		input := researchContextSelectionAction(t, f, "rc-retained",
			[]map[string]any{researchScopeBinding(linked, 1, true)},
			[]map[string]any{researchContextSelectedRef(linked, "f-"+linked, child, "")})
		published := researchScopeInvoke(t, f, "workflow_action", input)
		if published.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("selected declaration failed: %+v", published.Error)
		}
		policy := store.TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "human-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `["agent-1"]`}
		if err := f.store.UpdateTrustedClientPolicy(context.Background(), "client-1", policy, fixedTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
			t.Fatal(err)
		}
		// The shared reader never reauthorizes: the retained selection
		// resolves for the work pin and continuity share after the authoring
		// grant became unavailable.
		researchContextAssertResolvedView(t, f, linked, 1)
	})
}
