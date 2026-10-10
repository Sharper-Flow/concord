package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	events := append(agentFixtureProductEvents("rs", "product-1", "project-1", "Research Surface", "Research Project", "research surface fixture"),
		store.Event{EventID: "rs-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"bug","title":"Research Surface Work","priority":1}`)},
		store.Event{EventID: "rs-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	)
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
	t.Parallel()
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
		"fields":            map[string]any{},
		"research_bindings": []map[string]any{{"pack_id": packID, "revision": 1, "use_role": "context", "required": true}},
	})
	if action.Outcome != OutcomeOK {
		t.Fatalf("action with fresh required binding failed: %+v", action.Error)
	}
	freshness, err := s.RequiredResearchFreshness(ctx, packID, "work-1")
	if err != nil || freshness != store.ResearchCurrent {
		t.Fatalf("binding freshness=%v err=%v", freshness, err)
	}

	// The alignment step records the backlog search before diagnose.
	aligned := invoke("workflow_action", map[string]any{
		"work_id": "work-1", "expected_version": 5, "action_id": "record_alignment", "idempotency_key": "rs-action-align",
		"fields": map[string]any{"searched": "The bounded backlog search statement.", "outcome": "none_found"},
	})
	if aligned.Outcome != OutcomeOK {
		t.Fatalf("alignment with fresh required binding failed: %+v", aligned.Error)
	}

	// 5. Stale the pack; a second required reliance on the stale revision is
	// refused fail-closed at the boundary (CD-0009 D6 / CD-0025).
	if r := invoke("research_freshness_set", map[string]any{
		"pack_id": packID, "expected_version": 4, "freshness": "stale", "idempotency_key": "rs-stale-1",
	}); r.Outcome != OutcomeOK {
		t.Fatalf("freshness set failed: %+v", r.Error)
	}
	refused := invoke("workflow_action", map[string]any{
		"work_id": "work-1", "expected_version": 7, "action_id": "record_root_cause", "idempotency_key": "rs-action-2",
		"fields":            map[string]any{},
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
	t.Parallel()
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
	case op == "research_retire":
		return "concord_work_define"
	case strings.HasPrefix(op, "research_pack_create"), strings.HasPrefix(op, "research_revision_append"), strings.HasPrefix(op, "research_finding_record"), strings.HasPrefix(op, "research_source_record"), strings.HasPrefix(op, "research_freshness_set"):
		return "concord_work_define"
	case strings.HasPrefix(op, "research"):
		return "concord_work_trace"
	default:
		return "concord_work_transition"
	}
}

func researchRetireFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Authority, []store.ResearchRetirementCandidate) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	events := agentFixtureProductEvents("retire", "product-1", "project-1", "Retirement", "Retirement Project", "retirement fixture")
	events = append(events, agentFixtureProductEvents("foreign-retire", "product-2", "project-2", "Other Product", "Other Project", "retirement foreign fixture")...)
	versions := map[store.SubjectRef]int64{
		store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0,
		store.VersionRef(store.SubjectProduct, "product-2"): 0, store.VersionRef(store.SubjectProject, "project-2"): 0,
	}
	for i, project := range []string{"project-1", "project-1", "project-2"} {
		work := fmt.Sprintf("retire-owner-%d", i)
		versions[store.VersionRef(store.SubjectWorkItem, work)] = 0
		events = append(events,
			store.Event{EventID: work + ":create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: work, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Retirement Owner","priority":1}`)},
			store.Event{EventID: work + ":membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: work, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(fmt.Sprintf(`{"memberships":[{"project_id":%q,"role":"primary"}],"expected_version":1,"resulting_version":2}`, project))},
		)
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: versions}); err != nil {
		t.Fatal(err)
	}
	candidates := []store.ResearchRetirementCandidate{}
	for i := range 3 {
		work := fmt.Sprintf("retire-owner-%d", i)
		if err := s.Transact(ctx, func(tx *store.Transaction) error {
			pack, err := store.CreateResearchPackWithinTx(ctx, tx, store.CreateResearchPackRequest{OwnerWorkID: work, Revision: storeResearchRevision(researchRevisionInput{Question: "What can retire?", Method: "source inspection"}), Freshness: store.ResearchCurrent})
			if err == nil {
				candidates = append(candidates, store.ResearchRetirementCandidate{PackID: pack.PackID, ExpectedVersion: pack.ExpectedVersion})
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{{EventID: work + ":terminal", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: work, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"cancelled","reason":"finished","expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, work): 2}}); err != nil {
			t.Fatal(err)
		}
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", capabilities, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	return s, service, grant, candidates
}

func invokeResearchRetire(t *testing.T, s *store.Store, service *Service, grant Authority, product string, candidates []store.ResearchRetirementCandidate, dry bool, key string) Envelope {
	t.Helper()
	version, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"product_id": product, "candidates": candidates, "dry_run": dry, "idempotency_key": key})
	if err != nil {
		t.Fatal(err)
	}
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "research_retire", Input: raw}, mutationEnvelope(grant, version))
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestResearchRetireAuthorizationBeforeBatchEffects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caps    []Capability
		product string
		indices []int
	}{
		{"capability", []Capability{"product_read", "work_define"}, "product-1", []int{0}},
		{"product", []Capability{"product_read", "research"}, "product-2", []int{2}},
		{"later owner", []Capability{"product_read", "research"}, "product-1", []int{0, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, all := researchRetireFixture(t, tc.caps)
			candidates := []store.ResearchRetirementCandidate{}
			for _, i := range tc.indices {
				candidates = append(candidates, all[i])
			}
			response := invokeResearchRetire(t, s, service, grant, tc.product, candidates, false, "denied-retirement")
			if response.Error == nil || response.Error.Kind != "unauthorized" {
				t.Fatalf("expected unauthorized, got outcome=%s error=%+v", response.Outcome, response.Error)
			}
			var packs, receipts int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs`).Scan(&packs); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='research_retire'`).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if packs != 3 || receipts != 0 {
				t.Fatalf("denied batch wrote: packs=%d receipts=%d", packs, receipts)
			}
		})
	}
}

func TestResearchRetireDryRunAndReplay(t *testing.T) {
	s, service, grant, all := researchRetireFixture(t, []Capability{"product_read", "research"})
	candidates := all[:2]
	var changesBefore, changesAfter int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT total_changes()`).Scan(&changesBefore); err != nil {
		t.Fatal(err)
	}
	dry := invokeResearchRetire(t, s, service, grant, "product-1", candidates, true, "dry-retirement")
	if dry.Outcome != OutcomeOK || dry.Replayed {
		t.Fatalf("dry run outcome=%s error=%+v", dry.Outcome, dry.Error)
	}
	var classification store.RetireResearchPacksResult
	if err := json.Unmarshal(dry.Result, &classification); err != nil {
		t.Fatal(err)
	}
	if !classification.DryRun || len(classification.Candidates) != 2 || classification.Candidates[0].Classification != store.ResearchRetirementEligible {
		t.Fatalf("dry classification=%+v", classification)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT total_changes()`).Scan(&changesAfter); err != nil || changesBefore != changesAfter {
		t.Fatalf("dry run wrote: changes %d -> %d err=%v", changesBefore, changesAfter, err)
	}
	repeatedDry := invokeResearchRetire(t, s, service, grant, "product-1", candidates, true, "dry-retirement")
	if repeatedDry.Outcome != OutcomeOK || repeatedDry.Replayed || !bytes.Equal(dry.Result, repeatedDry.Result) {
		t.Fatalf("repeated dry outcome=%s error=%+v replayed=%v", repeatedDry.Outcome, repeatedDry.Error, repeatedDry.Replayed)
	}
	var receipts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='research_retire'`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("dry receipts=%d err=%v", receipts, err)
	}
	retired := invokeResearchRetire(t, s, service, grant, "product-1", candidates, false, "retirement")
	if retired.Outcome != OutcomeOK {
		t.Fatalf("retire error=%+v", retired.Error)
	}
	replay := invokeResearchRetire(t, s, service, grant, "product-1", candidates, false, "retirement")
	if replay.Outcome != OutcomeOK || !replay.Replayed || !bytes.Equal(retired.Result, replay.Result) {
		t.Fatalf("replay outcome=%s error=%+v replayed=%v results=%s / %s", replay.Outcome, replay.Error, replay.Replayed, retired.Result, replay.Result)
	}
	conflict := invokeResearchRetire(t, s, service, grant, "product-1", candidates[:1], false, "retirement")
	if conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("expected idempotency conflict, got %+v", conflict.Error)
	}
	var packs int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs`).Scan(&packs); err != nil || packs != 1 {
		t.Fatalf("pack count=%d err=%v", packs, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='research_retire'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipt count=%d err=%v", receipts, err)
	}
}

// A retirement receipt is durable state, not session state. After a store
// close and reopen, the same request still replays the one cached result with
// Replayed set, writes no second receipt, and the retired packs stay absent.
func TestResearchRetireReplaySurvivesCloseAndReopen(t *testing.T) {
	s, service, grant, candidates := researchRetireFixture(t, []Capability{"product_read", "research"})
	retired := invokeResearchRetire(t, s, service, grant, "product-1", candidates[:2], false, "restart-retirement")
	if retired.Outcome != OutcomeOK {
		t.Fatalf("retirement error=%+v", retired.Error)
	}
	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := NewService(reopened)
	restarted.Now = fixedTime
	resolveProjectAuthority(restarted, store.ProjectResolution{ProjectID: "project-1"})
	replay := invokeResearchRetire(t, reopened, restarted, grant, "product-1", candidates[:2], false, "restart-retirement")
	if replay.Outcome != OutcomeOK || !replay.Replayed || !bytes.Equal(retired.Result, replay.Result) {
		t.Fatalf("reopened replay outcome=%s error=%+v replayed=%v results=%s / %s", replay.Outcome, replay.Error, replay.Replayed, retired.Result, replay.Result)
	}
	var receipts int
	if err := reopened.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='research_retire'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipt count after restart=%d err=%v", receipts, err)
	}
	var absent int
	if err := reopened.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs WHERE pack_id IN (?, ?)`, candidates[0].PackID, candidates[1].PackID).Scan(&absent); err != nil || absent != 0 {
		t.Fatalf("retired packs present after restart: %d err=%v", absent, err)
	}
}

// Every replay re-authorizes each cached candidate's owner against the live
// Project memberships before the cached result is served. Owner removal
// deletes the work row, so the owner no longer resolves into the requested
// Product and the replay refuses unauthorized fail-closed: the cached result
// is never served and the receipt neither grows nor is touched. The refusal
// kind is unauthorized, not stale_context: stale_context's recovery action is
// refresh_context, and no refresh restores a removed owner, so the refusal is
// terminal for the caller rather than a context-freshness signal. This
// witness pins the fail-closed behavior; it does not weaken it.
func TestResearchRetireReplayAfterOwnerRemovalFailsClosed(t *testing.T) {
	s, service, grant, candidates := researchRetireFixture(t, []Capability{"product_read", "research"})
	if retired := invokeResearchRetire(t, s, service, grant, "product-1", candidates[:1], false, "removal-replay"); retired.Outcome != OutcomeOK {
		t.Fatalf("retirement error=%+v", retired.Error)
	}
	req := store.WorkRemovalRequest{
		OperationID: "remove-retire-owner-0", IdempotencyKey: "remove-retire-owner-0-key",
		WorkID: "retire-owner-0", ExpectedVersion: 3, Reason: "shelved", Actor: "operator",
		Handoff: store.WorkRemovalHandoff{
			Findings: []string{"finding"}, RemainingScope: []string{"scope"}, Blockers: []string{"blocker"},
			Artifacts: []string{"artifact"}, RenewalConditions: []string{"new approval"},
		},
		ExecutionRelinquished: true, WritesReconciled: true, EffectsReconciled: true,
		DependenciesResolved: true, ArtifactsVerified: true,
	}
	if _, err := s.ShelveWork(context.Background(), req); err != nil {
		t.Fatalf("owner removal refused: %v", err)
	}
	replay := invokeResearchRetire(t, s, service, grant, "product-1", candidates[:1], false, "removal-replay")
	if replay.Error == nil || replay.Error.Kind != "unauthorized" || replay.Replayed {
		t.Fatalf("replay after owner removal outcome=%s error=%+v replayed=%v", replay.Outcome, replay.Error, replay.Replayed)
	}
	var receipts, packs int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='research_retire'`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipt count after refused replay=%d err=%v", receipts, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs`).Scan(&packs); err != nil || packs != 2 {
		t.Fatalf("pack count after owner removal=%d err=%v", packs, err)
	}
}

func TestResearchRetireReplayRechecksRetainedOwnerScope(t *testing.T) {
	s, service, grant, candidates := researchRetireFixture(t, []Capability{"product_read", "research"})
	if retired := invokeResearchRetire(t, s, service, grant, "product-1", candidates[:1], false, "scope-replay"); retired.Outcome != OutcomeOK {
		t.Fatalf("retirement error=%+v", retired.Error)
	}
	event := store.Event{EventID: "retire-owner-moved", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "retire-owner-0", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-2","role":"primary"}],"expected_version":3,"resulting_version":4}`)}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "retire-owner-0"): 3}}); err != nil {
		t.Fatal(err)
	}
	replay := invokeResearchRetire(t, s, service, grant, "product-1", candidates[:1], false, "scope-replay")
	if replay.Error == nil || replay.Error.Kind != "unauthorized" || replay.Replayed {
		t.Fatalf("replay of foreign owner outcome=%s error=%+v replayed=%v", replay.Outcome, replay.Error, replay.Replayed)
	}
}

func TestResearchRetireChecksEveryOwnerProduct(t *testing.T) {
	s, service, grant, candidates := researchRetireFixture(t, []Capability{"product_read", "research"})
	event := store.Event{EventID: "retire-owner-shared", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "retire-owner-0", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"},{"project_id":"project-2","role":"secondary"}],"expected_version":3,"resulting_version":4}`)}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "retire-owner-0"): 3}}); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		response := invokeResearchRetire(t, s, service, grant, "product-1", candidates[:1], dry, "shared-retirement")
		if response.Error == nil || response.Error.Kind != "unauthorized" {
			t.Fatalf("dry=%v unauthorized secondary Product error=%+v", dry, response.Error)
		}
	}
	var packs int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs`).Scan(&packs); err != nil || packs != 3 {
		t.Fatalf("shared owner pack count=%d err=%v", packs, err)
	}
}

func TestResearchRetireDuplicatePackRefusesBeforeEffects(t *testing.T) {
	s, service, grant, candidates := researchRetireFixture(t, []Capability{"product_read", "research"})
	duplicate := []store.ResearchRetirementCandidate{candidates[0], {PackID: candidates[0].PackID, ExpectedVersion: 2}}
	response := invokeResearchRetire(t, s, service, grant, "product-1", duplicate, false, "duplicate-retirement")
	if response.Outcome != OutcomeError {
		t.Fatalf("duplicate pack retired: %s", response.Result)
	}
	var packs, receipts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs`).Scan(&packs); err != nil || packs != 3 {
		t.Fatalf("duplicate pack count=%d err=%v", packs, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='research_retire'`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("duplicate receipt count=%d err=%v", receipts, err)
	}
}

func TestResearchRetireCrossProductAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cross bool
		dry   bool
		want  string
	}{
		{"missing cross capability", false, false, "unauthorized"},
		{"dry run needs no approval", true, true, ""},
		{"deletion requires approval", true, false, "approval_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, service, grant, candidates := researchRetireFixture(t, []Capability{"product_read", "research"})
			ctx := context.Background()
			event := store.Event{EventID: "cross-retire-owner", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "retire-owner-0", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"},{"project_id":"project-2","role":"secondary"}],"expected_version":3,"resulting_version":4}`)}
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "retire-owner-0"): 3}}); err != nil {
				t.Fatal(err)
			}
			caps := []Capability{"product_read", "research"}
			if tc.cross {
				caps = append(caps, "cross_scope")
			}
			if err := service.UpdateTrustedClientPolicy(ctx, "client-1", TrustedClientPolicy{PrincipalRef: "human-1", Capabilities: caps, ProductScope: []string{"product-1", "product-2"}, ProjectScope: []string{"project-1", "project-2"}, AgentScope: testFixtureAgents}); err != nil {
				t.Fatal(err)
			}
			response := invokeResearchRetire(t, s, service, grant, "product-1", candidates[:1], tc.dry, "cross-retirement")
			if tc.want == "" {
				if response.Outcome != OutcomeOK {
					t.Fatalf("dry run failed: %+v", response.Error)
				}
			} else if response.Error == nil || response.Error.Kind != tc.want {
				t.Fatalf("want %s, got outcome=%s error=%+v", tc.want, response.Outcome, response.Error)
			}
			var packs, receipts int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs`).Scan(&packs); err != nil || packs != 3 {
				t.Fatalf("cross admission pack count=%d err=%v", packs, err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE operation_kind='research_retire'`).Scan(&receipts); err != nil || receipts != 0 {
				t.Fatalf("cross admission receipt count=%d err=%v", receipts, err)
			}
		})
	}
}

func TestResearchRetireSchemaBounds(t *testing.T) {
	valid := map[string]any{"product_id": "product-1", "candidates": []store.ResearchRetirementCandidate{{PackID: "pack-1", ExpectedVersion: 1}}, "dry_run": true, "idempotency_key": "bounded-retirement"}
	for _, tc := range []struct {
		name       string
		candidates []store.ResearchRetirementCandidate
	}{
		{"empty", []store.ResearchRetirementCandidate{}},
		{"too many", make([]store.ResearchRetirementCandidate, 101)},
		{"zero version", []store.ResearchRetirementCandidate{{PackID: "pack-1", ExpectedVersion: 0}}},
		{"negative version", []store.ResearchRetirementCandidate{{PackID: "pack-1", ExpectedVersion: -1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{}
			for key, value := range valid {
				input[key] = value
			}
			input["candidates"] = tc.candidates
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateOperationPayload("concord_work_define", "research_retire", raw, false); err == nil {
				t.Fatal("invalid retirement input passed schema validation")
			}
		})
	}
	for _, count := range []int{1, 100} {
		candidates := make([]store.ResearchRetirementCandidate, count)
		for i := range candidates {
			candidates[i] = store.ResearchRetirementCandidate{PackID: fmt.Sprintf("pack-%d", i), ExpectedVersion: 1}
		}
		valid["candidates"] = candidates
		raw, _ := json.Marshal(valid)
		if err := ValidateOperationPayload("concord_work_define", "research_retire", raw, false); err != nil {
			t.Fatalf("batch size %d refused: %v", count, err)
		}
	}
}

func TestResearchRetireMaximumBatchAndReplay(t *testing.T) {
	s, service, grant, _ := researchRetireFixture(t, []Capability{"product_read", "research"})
	ctx := context.Background()
	events := []store.Event{
		{EventID: "batch-owner-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "batch-owner", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"Batch Owner","priority":1}`)},
		{EventID: "batch-owner-member", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "batch-owner", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "batch-owner"): 0}}); err != nil {
		t.Fatal(err)
	}
	candidates := []store.ResearchRetirementCandidate{}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		for range 100 {
			pack, err := store.CreateResearchPackWithinTx(ctx, tx, store.CreateResearchPackRequest{OwnerWorkID: "batch-owner", Revision: storeResearchRevision(researchRevisionInput{Question: "What can retire?", Method: "source inspection"}), Freshness: store.ResearchCurrent})
			if err != nil {
				return err
			}
			candidates = append(candidates, store.ResearchRetirementCandidate{PackID: pack.PackID, ExpectedVersion: 1})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	event := store.Event{EventID: "batch-owner-terminal", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "batch-owner", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"from":"needed","to":"cancelled","reason":"finished","expected_version":2,"resulting_version":3}`)}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "batch-owner"): 2}}); err != nil {
		t.Fatal(err)
	}
	retired := invokeResearchRetire(t, s, service, grant, "product-1", candidates, false, "maximum-retirement")
	if retired.Outcome != OutcomeOK {
		t.Fatalf("maximum batch failed: %+v", retired.Error)
	}
	var result store.RetireResearchPacksResult
	if err := json.Unmarshal(retired.Result, &result); err != nil || len(result.Candidates) != 100 {
		t.Fatalf("batch result count=%d err=%v", len(result.Candidates), err)
	}
	for _, candidate := range result.Candidates {
		if candidate.Classification != store.ResearchRetirementRetired {
			t.Fatalf("candidate did not retire: %+v", candidate)
		}
	}
	if retired.ChangedRefs == nil || len(*retired.ChangedRefs) != MaxChangedRefs || len(retired.Omissions) != 1 || retired.Omissions[0].Count != 100-MaxChangedRefs {
		t.Fatalf("batch change refs=%+v omissions=%+v", retired.ChangedRefs, retired.Omissions)
	}
	replay := invokeResearchRetire(t, s, service, grant, "product-1", candidates, false, "maximum-retirement")
	if replay.Outcome != OutcomeOK || !replay.Replayed || !bytes.Equal(retired.Result, replay.Result) || len(replay.Omissions) != 1 || replay.Omissions[0].Count != 100-MaxChangedRefs {
		t.Fatalf("maximum replay outcome=%s error=%+v replayed=%v omissions=%+v", replay.Outcome, replay.Error, replay.Replayed, replay.Omissions)
	}
}
