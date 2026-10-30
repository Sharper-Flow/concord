package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest/neighbor"
)

func TestSQLiteNeighborProcess(t *testing.T) { neighbor.Child(t, store.SQLiteFileURI) }

type routineOverlapBinding struct {
	kind, fromWorkID, toWorkID             string
	fromVersion, toVersion                 int64
	fromContractVersion, toContractVersion int64
}

// One execution-claimed work and one prospective contender derive the pair
// through their real contract and checkpoint folds.
func routineCoordinationFixture(t *testing.T) (*store.Store, *Service, CallEnvelope) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_relate"})
	seedRoutineCoordinationDomainFixture(t, s, ctx)
	events := []store.Event{
		{EventID: "routine-coordination-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"bug","title":"Routine coordination peer","priority":1,"governing_requirements":[],"defect_intake":{"failure_shape":"routine-coordination-peer","reproduction":"Exercise coordination with a distinct peer work item.","searched":"The fixture contains no earlier defect with this failure shape.","related_defect_ids":[]}}`)},
		{EventID: "routine-coordination-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	for _, workID := range []string{"work-1", "work-2"} {
		verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Transact(verified, func(tx *store.Transaction) error {
			return store.InitializeWorkflowTx(verified, tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: fixedTime()})
		}); err != nil {
			t.Fatalf("initialize %s: %v", workID, err)
		}
	}
	routineCoordinationDriveWorkflow(t, s, "work-1", actor, true)
	routineCoordinationDriveWorkflow(t, s, "work-2", actor, false)
	for _, workID := range []string{"work-1", "work-2"} {
		if got := workLifecycle(t, s, workID); got != "in_progress" {
			t.Fatalf("fixture %s lifecycle = %q, want the action-fold in_progress", workID, got)
		}
	}
	return s, service, mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
}

func seedRoutineCoordinationDomainFixture(t *testing.T, s *store.Store, ctx context.Context) {
	t.Helper()
	homeDir := t.TempDir()
	var projectVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id=?`, "project-1").Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project-1", store.ProjectLocator{ID: "domain-locator-product-1", Kind: store.LocatorCanonicalPath, Value: homeDir}, projectVersion); err != nil {
		t.Fatalf("add the event-owned fixture locator: %v", err)
	}
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", homeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProjectLocator(ctx, "project-1", store.ProjectLocator{ID: home.HomeLocatorID, Kind: store.LocatorCanonicalPath, Value: home.RepoPath}, projectVersion+1); err != nil {
		t.Fatal(err)
	}
}

func routineCoordinationDriveWorkflow(t *testing.T, s *store.Store, workID string, actor store.WorkflowActor, enterExecution bool) {
	t.Helper()
	ctx := context.Background()
	sequence := 0
	act := func(actionID string, payload map[string]any) {
		t.Helper()
		sequence++
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		identity := fmt.Sprintf("routine-coordination-%s-%s-%d", workID, actionID, sequence)
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := store.WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: raw,
			Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("2", 64), IdempotencyIdentity: identity,
			OperationID: "routine-coordination-op-" + identity, PrincipalRef: actor.PrincipalRef, Tool: "concord-test", IdempotencyKey: "routine-coordination-key-" + identity,
			RequestID: "routine-coordination-request-" + identity, AcceptedScope: `{"project":"project-1"}`, ContractDigest: agentcontract.ManifestDigest, Now: fixedTime(),
		}
		preflight := store.WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: raw, Actor: actor}
		verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AuthorizeWorkflowActionAtBoundaryTx(verified, s, store.BuiltinWorkflowRegistry(), preflight, nil, func(tx *store.Transaction) error {
			_, err := store.ApplyWorkflowActionTx(verified, tx, store.BuiltinWorkflowRegistry(), request)
			return err
		}); err != nil {
			t.Fatalf("%s %s at v%d: %v", workID, actionID, version, err)
		}
	}
	act("record_diagnosis", map[string]any{
		"reproduction": "The bounded defect reproduction statement.",
		"searched":     "The bounded backlog search statement.", "outcome": "none_found", "root_cause": "The bounded root cause statement.",
	})
	fields := workflowContractFieldsFixture()
	binding := workflowArchitectureBindingForSeededRegistry(t, s)
	binding["domain_modifies"] = []string{binding["home_domain_id"].(string)}
	fields["architecture_binding"] = binding
	fields["law_change_reason"] = "The bounded repair changes the declared Domain."
	fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "absent", "outcome_payload": map[string]any{
		"kind": "absent", "surface": "behavior:defect", "subjects": []string{workID}, "distinguish_from": []string{"disabled"},
	}}}
	act("approve_contract", fields)
	if !enterExecution {
		return
	}
	act("checkpoint_repair", map[string]any{})
	var started any
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_started_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&started); err != nil || started == nil {
		t.Fatalf("fixture %s carries no durable execution start: %v", workID, err)
	}
}

func routineOverlapExactBinding(t *testing.T, s *store.Store, kind string) routineOverlapBinding {
	t.Helper()
	binding := routineOverlapBinding{
		kind: kind, fromWorkID: "work-1", toWorkID: "work-2",
		fromVersion: workVersion(t, s, "work-1"), toVersion: workVersion(t, s, "work-2"),
	}
	for _, side := range []struct {
		workID  string
		version *int64
	}{
		{workID: "work-1", version: &binding.fromContractVersion},
		{workID: "work-2", version: &binding.toContractVersion},
	} {
		if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, side.workID).Scan(side.version); err != nil {
			t.Fatalf("active contract for %s: %v", side.workID, err)
		}
	}
	return binding
}

func routineOverlapResolveInput(t *testing.T, binding routineOverlapBinding, key string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"from_work_id": binding.fromWorkID, "to_work_id": binding.toWorkID,
		"from_expected_version": binding.fromVersion, "to_expected_version": binding.toVersion,
		"from_contract_version": binding.fromContractVersion, "to_contract_version": binding.toContractVersion,
		"resolution_kind": binding.kind, "reason": "settle the exact overlap", "idempotency_key": key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sameResultPayload(t *testing.T, original, replayed json.RawMessage) bool {
	t.Helper()
	var originalObject, replayedObject map[string]any
	if err := json.Unmarshal(original, &originalObject); err != nil {
		t.Fatalf("original result is not a JSON object: %v", err)
	}
	if err := json.Unmarshal(replayed, &replayedObject); err != nil {
		t.Fatalf("replayed result is not a JSON object: %v", err)
	}
	return reflect.DeepEqual(originalObject, replayedObject)
}

func sameChangedRefs(t *testing.T, original, replayed *[]agentcontract.ChangedRef) bool {
	t.Helper()
	if original == nil || replayed == nil {
		return original == nil && replayed == nil
	}
	return reflect.DeepEqual(*original, *replayed)
}

func TestRoutineCoordinationCachedReplayPreservesOriginalResultWithoutNewEffects(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{store.ResolutionDependsOn, store.ResolutionBlocks, store.ResolutionMergedInto, store.ResolutionSupersedes} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, service, env := routineCoordinationFixture(t)
			binding := routineOverlapExactBinding(t, s, kind)
			input := routineOverlapResolveInput(t, binding, "routine-cached-replay")
			request := InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}
			first := dispatchMutation(t, s, service, request, env)
			if first.Outcome != agentcontract.OutcomeOK || first.Error != nil {
				t.Fatalf("resolution %s outcome=%s error=%+v", kind, first.Outcome, first.Error)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
				t.Fatalf("resolution count=%d, want one", got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved'`); got != 1 {
				t.Fatalf("resolution receipt count=%d, want one", got)
			}
			if workVersion(t, s, "work-1") != binding.fromVersion+1 || workVersion(t, s, "work-2") != binding.toVersion+1 {
				t.Fatal("resolution did not advance each endpoint exactly once")
			}
			fromState, toState := "in_progress", "in_progress"
			if kind == store.ResolutionMergedInto {
				fromState = "superseded"
			}
			if kind == store.ResolutionSupersedes {
				toState = "superseded"
			}
			if workLifecycle(t, s, "work-1") != fromState || workLifecycle(t, s, "work-2") != toState {
				t.Fatal("resolution changed the wrong lifecycle")
			}
			baseline := agentCurrentEffects(t, s)
			replay := dispatchMutation(t, s, service, request, env)
			if replay.Outcome != agentcontract.OutcomeOK || replay.Error != nil || !replay.Replayed || !sameResultPayload(t, first.Result, replay.Result) || !sameChangedRefs(t, first.ChangedRefs, replay.ChangedRefs) {
				t.Fatalf("replay lost the original response: %+v", replay)
			}
			if agentCurrentEffects(t, s) != baseline || workVersion(t, s, "work-1") != binding.fromVersion+1 || workVersion(t, s, "work-2") != binding.toVersion+1 {
				t.Fatal("replay changed durable progress")
			}
			if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT replayed_count FROM idempotency_records WHERE idempotency_key='routine-cached-replay'`) != 1 {
				t.Fatal("replay did not touch exactly the existing receipt")
			}
		})
	}
}

// The primary Project gives the contract one footprint Product. The shared
// ambient Project still makes the mutation's derived scope cross Products.
func seedRoutineCoordinationPrimaryProjectFixture(t *testing.T, s *store.Store, ctx context.Context) {
	t.Helper()
	var productVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM products WHERE id='product-1'`).Scan(&productVersion); err != nil {
		t.Fatal(err)
	}
	workVersions := map[string]int64{}
	for _, workID := range []string{"work-1", "work-2"} {
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		workVersions[workID] = version
	}
	memberships := func(workID string, version int64) store.Event {
		payload, err := json.Marshal(map[string]any{
			"memberships":      []map[string]any{{"project_id": "project-primary", "role": "primary"}, {"project_id": "project-1", "role": "secondary"}},
			"expected_version": version, "resulting_version": version + 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return store.Event{EventID: "cross-product-" + workID + "-primary-memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}
	}
	addedPayload, err := json.Marshal(map[string]any{
		"product_id": "product-1", "project_id": "project-primary", "role": "secondary", "reason": "fixture", "expected_version": productVersion, "resulting_version": productVersion + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []store.Event{
		{EventID: "cross-product-primary-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-primary", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("project.created"), Payload: json.RawMessage(`{"display_name":"Primary Project"}`)},
		{EventID: "cross-product-primary-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: addedPayload},
		memberships("work-1", workVersions["work-1"]), memberships("work-2", workVersions["work-2"]),
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{
		store.VersionRef(store.SubjectProduct, "product-1"): productVersion, store.VersionRef(store.SubjectProject, "project-primary"): 0,
		store.VersionRef(store.SubjectWorkItem, "work-1"): workVersions["work-1"], store.VersionRef(store.SubjectWorkItem, "work-2"): workVersions["work-2"],
	}}); err != nil {
		t.Fatal(err)
	}
}

func crossProductRoutineCoordinationFixture(t *testing.T, capabilities []agentcontract.Capability) (*store.Store, *Service, Authority, CallEnvelope) {
	t.Helper()
	ctx := context.Background()
	s, service, grant := crossProductDispatchFixture(t, capabilities)
	seedRoutineCoordinationPrimaryProjectFixture(t, s, ctx)
	seedRoutineCoordinationDomainFixture(t, s, ctx)
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	for _, workID := range []string{"work-1", "work-2"} {
		verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Transact(verified, func(tx *store.Transaction) error {
			return store.InitializeWorkflowTx(verified, tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: fixedTime()})
		}); err != nil {
			t.Fatalf("initialize %s: %v", workID, err)
		}
	}
	routineCoordinationDriveWorkflow(t, s, "work-1", actor, true)
	routineCoordinationDriveWorkflow(t, s, "work-2", actor, false)
	for _, workID := range []string{"work-1", "work-2"} {
		if got := workLifecycle(t, s, workID); got != "in_progress" {
			t.Fatalf("cross-Product fixture %s lifecycle=%s", workID, got)
		}
	}
	return s, service, grant, mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
}

func TestRoutineCoordinationCrossProductScopeAndReplay(t *testing.T) {
	t.Parallel()
	for _, selected := range []string{"product-1", "product-2"} {
		for _, kind := range []string{store.ResolutionDependsOn, store.ResolutionMergedInto, store.ResolutionSupersedes} {
			t.Run(selected+"/"+kind, func(t *testing.T) {
				t.Parallel()
				s, service, grant, env := crossProductRoutineCoordinationFixture(t, []agentcontract.Capability{"work_relate", "cross_scope"})
				env.SelectedProductID = selected
				binding := routineOverlapExactBinding(t, s, kind)
				request := InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: routineOverlapResolveInput(t, binding, "routine-cross-product")}
				first := dispatchMutation(t, s, service, request, env)
				if first.Outcome != agentcontract.OutcomeOK {
					t.Fatalf("cross-Product resolution: %+v", first.Error)
				}
				var eventActor, snapshot string
				if err := s.DatabaseForTesting().QueryRow(`SELECT r.actor FROM workflow_overlap_resolutions o JOIN effect_receipts r ON r.event_id=o.resolution_id WHERE r.kind='workflow.overlap_resolved'`).Scan(&eventActor); err != nil {
					t.Fatal(err)
				}
				if eventActor != grant.PrincipalRef {
					t.Fatalf("event actor=%s, want caller %s", eventActor, grant.PrincipalRef)
				}
				if err := s.DatabaseForTesting().QueryRow(`SELECT authorized_scope_snapshot FROM idempotency_records WHERE idempotency_key='routine-cross-product'`).Scan(&snapshot); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(snapshot, `"durable_commit":true`) || !strings.Contains(snapshot, `"product_ids":["product-1","product-2"]`) {
					t.Fatalf("lost durable Product scope: %s", snapshot)
				}
				fromState, toState := "in_progress", "in_progress"
				if kind == store.ResolutionMergedInto {
					fromState = "superseded"
				}
				if kind == store.ResolutionSupersedes {
					toState = "superseded"
				}
				if workLifecycle(t, s, "work-1") != fromState || workLifecycle(t, s, "work-2") != toState {
					t.Fatal("resolution changed the wrong lifecycle")
				}
				before := agentCurrentEffects(t, s)
				replay := dispatchMutation(t, s, service, request, env)
				if replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed || !sameResultPayload(t, first.Result, replay.Result) || !sameChangedRefs(t, first.ChangedRefs, replay.ChangedRefs) {
					t.Fatalf("cross-Product replay lost original result: %+v", replay)
				}
				if agentCurrentEffects(t, s) != before || workVersion(t, s, "work-1") != binding.fromVersion+1 || workVersion(t, s, "work-2") != binding.toVersion+1 {
					t.Fatal("cross-Product replay changed durable progress")
				}
				if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved'`) != 1 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`) != 1 {
					t.Fatal("cross-Product replay duplicated an effect or receipt")
				}
			})
		}
	}
}

func TestRoutineCoordinationCrossProductRoutineKindRefusesWithoutCrossScope(t *testing.T) {
	t.Parallel()
	s, service, _, env := crossProductRoutineCoordinationFixture(t, []agentcontract.Capability{"work_relate"})
	binding := routineOverlapExactBinding(t, s, store.ResolutionDependsOn)
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: routineOverlapResolveInput(t, binding, "routine-cross-product-no-grant")}, env)
	if refused.Error == nil || refused.Error.Kind != "unauthorized" || !strings.Contains(refused.Error.Message, "cross_scope") {
		t.Fatalf("cross-Product request without cross_scope: %+v", refused.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`) != 0 || countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved'`) != 0 || workVersion(t, s, "work-1") != binding.fromVersion || workVersion(t, s, "work-2") != binding.toVersion {
		t.Fatal("unauthorized request changed the pair")
	}
}

func TestRoutineCoordinationDestructiveKindWithEmptySelectedProduct(t *testing.T) {
	t.Parallel()
	s, service, env := routineCoordinationFixture(t)
	env.SelectedProductID = ""
	binding := routineOverlapExactBinding(t, s, store.ResolutionMergedInto)
	response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: routineOverlapResolveInput(t, binding, "routine-empty-selection")}, env)
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("empty Product selection: %+v", response.Error)
	}
	if countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`) != 1 || workLifecycle(t, s, "work-1") != "superseded" || workLifecycle(t, s, "work-2") != "in_progress" || workVersion(t, s, "work-1") != binding.fromVersion+1 || workVersion(t, s, "work-2") != binding.toVersion+1 {
		t.Fatal("empty-selection resolution lost its atomic terminal effect")
	}
}
