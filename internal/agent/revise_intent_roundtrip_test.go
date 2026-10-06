package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// CON-835 regression. revise_intent is complete replacement (TS4 2.1), so a
// coordinator that revises only the task must carry every other revisable
// value out of the authoritative typed read. The scope and full-list reads
// dropped urgency, tags, and workflow_type_ref, so a carried task-only
// revision silently reset them to their defaults. The field set below is
// derived from reviseMutationInput and the stored intent projection: kind,
// title, task, value_statement, priority, urgency, tags, workflow_type_ref.
// external_ref stays capture-owned: it is preserved by the fold and never
// enters revision input.
func TestTaskOnlyRevisionCarriesCompleteIntentThroughScopeRead(t *testing.T) {
	t.Parallel()
	s, ctx, service, env, workID := seedIntentRoundtripWork(t, map[string]any{
		"title": "Preserve the recorded intent", "value_statement": "Unrelated intent metadata must survive a task-only revision",
		"task": "Original recorded instruction", "kind": "task", "priority": -7, "urgency": "expedite",
		"tags": []string{"agent-plane", "storage"}, "workflow_type_ref": "workflow.break_fix",
		"external_ref": "linear:CON-835-1", "idempotency_key": "roundtrip-capture-1",
	})

	before := roundtripScopeWork(t, ctx, s, service, env, workID)
	assertCompleteIntentRead(t, before, map[string]any{
		"kind": "task", "title": "Preserve the recorded intent", "task": "Original recorded instruction",
		"value_statement": "Unrelated intent metadata must survive a task-only revision",
		"priority":        float64(-7), "urgency": "expedite",
		"tags": []any{"agent-plane", "storage"}, "workflow_type_ref": "workflow.break_fix",
	})

	revise := carriedTaskOnlyRevision(t, before, workID, "Replacement instruction that changes nothing else", "roundtrip-revise-1")
	revised := roundtripDispatchOK(t, ctx, s, service, env, "roundtrip-revise-1", "concord_work_define", "revise_intent", revise)
	if len(*revised.ChangedRefs) != 1 || (*revised.ChangedRefs)[0].Version != strconv.FormatInt(int64(before["version"].(float64))+1, 10) {
		t.Fatalf("changed refs=%#v, want the expected version to advance by exactly one", revised.ChangedRefs)
	}

	after := roundtripScopeWork(t, ctx, s, service, env, workID)
	assertCompleteIntentRead(t, after, map[string]any{
		"kind": "task", "title": "Preserve the recorded intent", "task": "Replacement instruction that changes nothing else",
		"value_statement": "Unrelated intent metadata must survive a task-only revision",
		"priority":        float64(-7), "urgency": "expedite",
		"tags": []any{"agent-plane", "storage"}, "workflow_type_ref": "workflow.break_fix",
	})
	assertStoredExternalRef(t, s, workID, "linear:CON-835-1")
}

// The full-detail list read is the other authoritative source a coordinator
// can carry into a task-only revision. It already carried the recorded task;
// it must also carry the value statement and the rest of the revisable
// intent, because a caller that cannot read a value cannot preserve it.
func TestTaskOnlyRevisionCarriesCompleteIntentThroughFullListRead(t *testing.T) {
	t.Parallel()
	s, ctx, service, env, workID := seedIntentRoundtripWork(t, map[string]any{
		"title": "Preserve intent through the list read", "value_statement": "Full-detail listings carry the complete revisable intent",
		"task": "Original list instruction", "kind": "bug", "priority": 42, "urgency": "expedite",
		"tags": []string{"agent-surface"}, "workflow_type_ref": "workflow.break_fix",
		"external_ref": "linear:CON-835-2", "idempotency_key": "roundtrip-capture-2",
	})

	before := roundtripFullListItem(t, ctx, s, service, env, workID)
	assertCompleteIntentRead(t, before, map[string]any{
		"kind": "bug", "title": "Preserve intent through the list read", "task": "Original list instruction",
		"value_statement": "Full-detail listings carry the complete revisable intent",
		"priority":        float64(42), "urgency": "expedite",
		"tags": []any{"agent-surface"}, "workflow_type_ref": "workflow.break_fix",
	})

	revise := carriedTaskOnlyRevision(t, before, workID, "Replacement instruction from the list read", "roundtrip-revise-2")
	revised := roundtripDispatchOK(t, ctx, s, service, env, "roundtrip-revise-2", "concord_work_define", "revise_intent", revise)
	if len(*revised.ChangedRefs) != 1 || (*revised.ChangedRefs)[0].Version != strconv.FormatInt(int64(before["version"].(float64))+1, 10) {
		t.Fatalf("changed refs=%#v, want the expected version to advance by exactly one", revised.ChangedRefs)
	}

	after := roundtripFullListItem(t, ctx, s, service, env, workID)
	assertCompleteIntentRead(t, after, map[string]any{
		"kind": "bug", "title": "Preserve intent through the list read", "task": "Replacement instruction from the list read",
		"value_statement": "Full-detail listings carry the complete revisable intent",
		"priority":        float64(42), "urgency": "expedite",
		"tags": []any{"agent-surface"}, "workflow_type_ref": "workflow.break_fix",
	})
	assertStoredExternalRef(t, s, workID, "linear:CON-835-2")
}

// Lawful zero, empty, and absent states must survive unchanged: an omitted
// priority stays zero, a default urgency stays standard, an explicitly empty
// tag array stays an empty array rather than becoming an invented value or a
// null, and an absent optional intent field stays absent. The pinned workflow
// instance is not a substitute for an absent recorded workflow_type_ref: a
// read that substituted it would make a carried revision pin workflow
// metadata the intent never declared.
func TestTaskOnlyRevisionPreservesZeroEmptyAndAbsentIntentStates(t *testing.T) {
	t.Parallel()
	s, ctx, service, env, workID := seedIntentRoundtripWork(t, map[string]any{
		"title": "Default states survive", "value_statement": "Zero and empty intent states are lawful",
		"kind": "bug", "tags": []string{}, "external_ref": "linear:CON-835-3", "idempotency_key": "roundtrip-capture-3",
	})

	before := roundtripScopeWork(t, ctx, s, service, env, workID)
	if got := before["urgency"]; got != "standard" {
		t.Fatalf("default urgency = %v, want standard", got)
	}
	if got, ok := before["priority"]; ok && got != float64(0) {
		t.Fatalf("omitted priority = %v, want zero", got)
	}
	tags, ok := before["tags"].([]any)
	if !ok || len(tags) != 0 {
		t.Fatalf("recorded empty tags = %#v, want an empty array", before["tags"])
	}
	if _, ok := before["workflow_type_ref"]; ok {
		t.Fatalf("scope read substituted a workflow_type_ref the intent never recorded: %#v", before["workflow_type_ref"])
	}
	if _, ok := before["task"]; ok {
		t.Fatalf("absent task = %#v, want the field to stay absent", before["task"])
	}
	var pinnedInstances int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id=?`, workID).Scan(&pinnedInstances); err != nil {
		t.Fatal(err)
	}
	if pinnedInstances == 0 {
		t.Fatal("fixture needs a pinned workflow instance so the absent intent ref is distinguishable from an unpinned item")
	}

	revise := carriedTaskOnlyRevision(t, before, workID, "First recorded instruction", "roundtrip-revise-3")
	roundtripDispatchOK(t, ctx, s, service, env, "roundtrip-revise-3", "concord_work_define", "revise_intent", revise)

	after := roundtripScopeWork(t, ctx, s, service, env, workID)
	if got := after["urgency"]; got != "standard" {
		t.Fatalf("urgency after revision = %v, want standard", got)
	}
	if got, ok := after["priority"]; ok && got != float64(0) {
		t.Fatalf("priority after revision = %v, want zero", got)
	}
	tags, ok = after["tags"].([]any)
	if !ok || len(tags) != 0 {
		t.Fatalf("tags after revision = %#v, want the empty array preserved", after["tags"])
	}
	if _, ok := after["workflow_type_ref"]; ok {
		t.Fatalf("workflow_type_ref appeared from a task-only revision: %#v", after["workflow_type_ref"])
	}
	if after["task"] != "First recorded instruction" {
		t.Fatalf("task after revision = %v, want the replacement", after["task"])
	}
	var tagType string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_type(intent_json, '$.tags') FROM work_items WHERE id=?`, workID).Scan(&tagType); err != nil {
		t.Fatal(err)
	}
	if tagType != "array" {
		t.Fatalf("stored tags json_type = %q, want array (an explicitly empty tag set must not decay to null)", tagType)
	}
	assertStoredExternalRef(t, s, workID, "linear:CON-835-3")
}

// external_ref is capture-owned (TS4 2.1): the closed revise_intent input
// must refuse it, so a coordinator cannot overwrite the captured reference
// and the fold's carry-forward is the only path it survives by.
func TestReviseIntentRefusesTheCaptureOwnedExternalRef(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"work_id":"work-1","expected_version":2,"title":"T","value_statement":"V","kind":"task","external_ref":"linear:CON-835-4","reason":"probe","idempotency_key":"probe"}`)
	if err := ValidateOperationPayload("concord_work_define", "revise_intent", payload, false); err == nil {
		t.Fatal("revise_intent input must refuse the capture-owned external_ref field")
	}
}

// The work_define capability keeps guarding revise_intent: a read-only
// client cannot revise intent, so the repair adds no mutation route.
func TestReviseIntentStaysBehindTheWorkDefineCapability(t *testing.T) {
	t.Parallel()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	seedRoundtripAuthority(t, s)
	service, _, grant := newAuthorizedService(t, s, "reader-client", "human-1", []Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "roundtrip-unauthorized", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: json.RawMessage(`{"work_id":"work-1","expected_version":1,"title":"T","value_statement":"V","kind":"task","reason":"probe","idempotency_key":"probe"}`)}, env)
	if err == nil && response.Outcome == OutcomeOK {
		t.Fatal("a product_read client must not revise intent; work_define still owns the mutation")
	}
}

// seedRoundtripAuthority creates the Product, Project, and membership a
// capture dispatch requires.
func seedRoundtripAuthority(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "roundtrip-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "roundtrip-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "roundtrip-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
}

// seedIntentRoundtripWork seeds the authority, captures one work item with
// the caller's intent, and returns the dispatch fixture.
func seedIntentRoundtripWork(t *testing.T, capture map[string]any) (*store.Store, context.Context, *Service, CallEnvelope, string) {
	t.Helper()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	seedRoundtripAuthority(t, s)
	service, _, grant := newAuthorizedService(t, s, "roundtrip-client", "human-1", []Capability{"work_define", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "roundtrip-capture", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	capture["project_ids"] = []string{"project-1"}
	response := roundtripDispatchOK(t, ctx, s, service, env, capture["idempotency_key"].(string), "concord_work_define", "capture", capture)
	if len(*response.ChangedRefs) != 1 {
		t.Fatalf("capture changed refs=%#v, want one work item", response.ChangedRefs)
	}
	return s, ctx, service, env, (*response.ChangedRefs)[0].ID
}

func roundtripDispatchOK(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, requestID, tool, operation string, input map[string]any) Envelope {
	t.Helper()
	env.RequestID = requestID
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: tool, Operation: operation, Input: mustJSON(t, input)}, env)
	if err != nil || response.Outcome != OutcomeOK {
		errorJSON, _ := json.Marshal(response.Error)
		t.Fatalf("%s.%s response=%+v error=%s err=%v", tool, operation, response, errorJSON, err)
	}
	return response
}

// roundtripScopeWork returns the single work summary the scope read carries.
func roundtripScopeWork(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, workID string) map[string]any {
	t.Helper()
	response := roundtripDispatchOK(t, ctx, s, service, env, "scope-read-"+workID, "concord_work_browse", "scope", map[string]any{"product_id": "product-1", "work_id": workID})
	var payload struct {
		Work map[string]any `json:"work"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Work == nil || payload.Work["id"] != workID {
		t.Fatalf("scope work = %#v, want the captured item", payload.Work)
	}
	return payload.Work
}

// roundtripFullListItem returns the item the full-detail list read carries.
func roundtripFullListItem(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, workID string) map[string]any {
	t.Helper()
	response := roundtripDispatchOK(t, ctx, s, service, env, "list-read-"+workID, "concord_work_browse", "list", map[string]any{"product_id": "product-1", "work_ids": []string{workID}, "detail": "full"})
	var payload struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0]["id"] != workID {
		t.Fatalf("full list items = %#v, want the captured item", payload.Items)
	}
	return payload.Items[0]
}

// carriedTaskOnlyRevision builds the complete replacement block a strict
// coordinator assembles from the typed read: identity, the read's expected
// version, every revisable value the read exposed, and only the task
// replaced. Values the read did not expose are not invented.
func carriedTaskOnlyRevision(t *testing.T, read map[string]any, workID, replacementTask, idempotencyKey string) map[string]any {
	t.Helper()
	revise := map[string]any{
		"work_id": workID, "expected_version": int64(read["version"].(float64)),
		"title": read["title"], "value_statement": read["value_statement"], "kind": read["kind"],
		"task": replacementTask, "reason": "task-only revision carrying the recorded intent",
		"evidence":        []map[string]any{{"kind": "commit", "authority": "git", "locator_kind": "commit", "locator": "commit:7b83cbf41af2f9fa7990294a41a50cb75a1d6d1e"}},
		"idempotency_key": idempotencyKey,
	}
	for _, field := range []string{"priority", "urgency", "tags", "workflow_type_ref"} {
		if value, ok := read[field]; ok {
			revise[field] = value
		}
	}
	return revise
}

// assertCompleteIntentRead proves the typed read exposes every revisable
// value exactly as recorded, without inventing or resetting metadata.
func assertCompleteIntentRead(t *testing.T, read map[string]any, want map[string]any) {
	t.Helper()
	for field, expected := range want {
		got, ok := read[field]
		if !ok {
			t.Fatalf("typed read omitted the revisable field %q; a task-only revision would reset it (read=%#v)", field, read)
		}
		if field == "tags" {
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("read %s = %#v, want %#v", field, got, expected)
			}
			continue
		}
		if got != expected {
			t.Fatalf("read %s = %#v, want %#v", field, got, expected)
		}
	}
}

func assertStoredExternalRef(t *testing.T, s *store.Store, workID, want string) {
	t.Helper()
	var stored any
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json, '$.external_ref') FROM work_items WHERE id=?`, workID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != want {
		t.Fatalf("stored external_ref = %v, want %q preserved by the revision fold", stored, want)
	}
}
