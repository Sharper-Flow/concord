package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// Revising intent with the pinned family's reference again carries the
// instance onto that family's current definition, so an instance stranded
// behind a promotion has a same-family route onto the current version.
func TestReviseIntentCarriesTheSameFamilyForward(t *testing.T) {
	t.Parallel()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "carry-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "carry-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "carry-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	envelope := CallEnvelope{SchemaVersion: "1.0", RequestID: "carry-capture-1", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	capture := InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: json.RawMessage(`{"title":"Stranded repair","value_statement":"Repair value","kind":"bug","project_ids":["project-1"],"idempotency_key":"carry-capture-1"}`)}
	response, err := Dispatch(ctx, s, service, capture, envelope)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("capture response=%+v err=%v", response, err)
	}
	workID := (*response.ChangedRefs)[0].ID
	if ref, version, _ := agentInstancePin(t, s, workID); ref != "workflow.break_fix" || version != 13 {
		t.Fatalf("captured pin = %s v%d, want workflow.break_fix v13", ref, version)
	}
	// Strand the instance behind the promotion: re-pin it to the outgoing
	// version through the store, as the broken-window instances are pinned.
	older, ok := store.BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 12)
	if !ok {
		t.Fatal("workflow.break_fix version 12 is not registered")
	}
	if err := s.Transact(ctx, func(transaction *store.Transaction) error {
		return store.RepinWorkflowTx(ctx, transaction, store.WorkflowRepinRequest{WorkID: workID, EventID: workID + "-strand", Definition: older, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	if _, version, _ := agentInstancePin(t, s, workID); version != 12 {
		t.Fatalf("stranded pin = v%d, want v12", version)
	}
	var expectedVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&expectedVersion); err != nil {
		t.Fatal(err)
	}
	revise := InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: mustJSON(t, map[string]any{
		"work_id": workID, "expected_version": expectedVersion, "title": "Stranded repair", "value_statement": "Repair value",
		"kind": "bug", "workflow_type_ref": "workflow.break_fix", "reason": "carry onto the current definition",
		"evidence":        []map[string]any{{"kind": "commit", "authority": "git", "locator_kind": "commit", "locator": "commit:7b83cbf41af2f9fa7990294a41a50cb75a1d6d1e"}},
		"idempotency_key": "carry-revise-1",
	})}
	envelope.RequestID = "carry-revise-1"
	revised, err := Dispatch(ctx, s, service, revise, envelope)
	if err != nil || revised.Outcome != OutcomeOK {
		errorJSON, _ := json.Marshal(revised.Error)
		t.Fatalf("revise response=%+v error=%s err=%v", revised, errorJSON, err)
	}
	ref, version, digest := agentInstancePin(t, s, workID)
	current, err := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "workflow.break_fix" || version != 13 || digest != current.Digest {
		t.Fatalf("pin after revise = %s v%d %s, want the current workflow.break_fix v13 %s", ref, version, digest, current.Digest)
	}
}

func agentInstancePin(t *testing.T, s *store.Store, workID string) (string, int64, string) {
	t.Helper()
	var ref, digest string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version, &digest); err != nil {
		t.Fatal(err)
	}
	return ref, version, digest
}
