package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// A task-only revision carries the complete intent from a full-detail list read.
func TestTaskOnlyRevisionCarriesCompleteIntentThroughFullListRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind, title, original, replacement, key, issue string
	}{
		{"task", "Preserve the recorded intent", "Original recorded instruction", "Replacement instruction that changes nothing else", "1", "ZZ-7005"},
		{"bug", "Preserve intent through the list read", "Original list instruction", "Replacement instruction from the list read", "2", "ZZ-7006"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			s, ctx, service, env, workID := seedIntentRoundtripWork(t, map[string]any{
				"title": tc.title, "task": tc.original, "kind": tc.kind,
				"human_key": tc.issue, "remote_issue_uuid": "fixture-roundtrip-capture-" + tc.key,
				"url": "https://linear.app/example/issue/" + tc.issue, "idempotency_key": "roundtrip-capture-" + tc.key,
			})
			before := roundtripFullListItem(t, ctx, s, service, env, workID)
			assertCompleteIntentRead(t, before, map[string]any{"kind": tc.kind, "title": tc.title, "task": tc.original})
			key := "roundtrip-revise-" + tc.key
			revise := carriedTaskOnlyRevision(t, before, workID, tc.replacement, key)
			revised := roundtripDispatchOK(t, ctx, s, service, env, key, "concord_work_define", "revise_intent", revise)
			if len(*revised.ChangedRefs) != 1 || (*revised.ChangedRefs)[0].Version != strconv.FormatInt(int64(before["version"].(float64))+1, 10) {
				t.Fatalf("changed refs=%#v, want the expected version to advance by exactly one", revised.ChangedRefs)
			}
			after := roundtripFullListItem(t, ctx, s, service, env, workID)
			assertCompleteIntentRead(t, after, map[string]any{"kind": tc.kind, "title": tc.title, "task": tc.replacement})
		})
	}
}

// An absent task stays absent until a revision supplies it.
func TestTaskOnlyRevisionPreservesZeroEmptyAndAbsentIntentStates(t *testing.T) {
	t.Parallel()
	s, ctx, service, env, workID := seedIntentRoundtripWork(t, map[string]any{
		"title":     "Default states survive",
		"kind":      "bug",
		"human_key": "ZZ-7007", "remote_issue_uuid": "fixture-roundtrip-capture-3",
		"url":             "https://linear.app/example/issue/ZZ-7007",
		"idempotency_key": "roundtrip-capture-3",
	})

	before := roundtripFullListItem(t, ctx, s, service, env, workID)
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

	after := roundtripFullListItem(t, ctx, s, service, env, workID)
	if after["task"] != "First recorded instruction" {
		t.Fatalf("task after revision = %v, want the replacement", after["task"])
	}
}

// external_ref is capture-owned (TS4 2.1): the closed revise_intent input
// must refuse it, so a coordinator cannot overwrite the captured reference
// and the fold's carry-forward is the only path it survives by.
func TestReviseIntentRefusesTheCaptureOwnedExternalRef(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"work_id":"work-1","expected_version":2,"title":"T","kind":"task","external_ref":"linear:CON-835-4","reason":"probe","idempotency_key":"probe"}`)
	if err := agentcontract.ValidateOperationPayload("concord_work_define", "revise_intent", payload, false); err == nil {
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
	service, _, grant := newAuthorizedService(t, s, "reader-client", "human-1", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "roundtrip-unauthorized", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "revise_intent", Input: json.RawMessage(`{"work_id":"work-1","expected_version":1,"title":"T","kind":"task","reason":"probe","idempotency_key":"probe"}`)}, env)
	if err == nil && response.Outcome == agentcontract.OutcomeOK {
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
		{EventID: "roundtrip-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"display_name":"Project"}`)},
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
	service, _, grant := newAuthorizedService(t, s, "roundtrip-client", "human-1", []agentcontract.Capability{"work_define", "product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "roundtrip-capture", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	capture["project_ids"] = []string{"project-1"}
	response := roundtripDispatchOK(t, ctx, s, service, env, capture["idempotency_key"].(string), "concord_work_define", "capture", capture)
	if len(*response.ChangedRefs) != 2 {
		t.Fatalf("capture changed refs=%#v, want the work item and its Linear link", response.ChangedRefs)
	}
	return s, ctx, service, env, (*response.ChangedRefs)[0].ID
}

func roundtripDispatchOK(t *testing.T, ctx context.Context, s *store.Store, service *Service, env CallEnvelope, requestID, tool, operation string, input map[string]any) agentcontract.Envelope {
	t.Helper()
	env.RequestID = requestID
	response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: tool, Operation: operation, Input: mustJSON(t, input)}, env)
	if err != nil || response.Outcome != agentcontract.OutcomeOK {
		errorJSON, _ := json.Marshal(response.Error)
		t.Fatalf("%s.%s response=%+v error=%s err=%v", tool, operation, response, errorJSON, err)
	}
	return response
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
		"title": read["title"], "kind": read["kind"],
		"task": replacementTask, "reason": "task-only revision carrying the recorded intent",
		"evidence":        []map[string]any{{"kind": "commit", "authority": "git", "locator_kind": "commit", "locator": "commit:7b83cbf41af2f9fa7990294a41a50cb75a1d6d1e"}},
		"idempotency_key": idempotencyKey,
	}
	for _, field := range []string{"priority", "urgency", "tags"} {
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
	for field, value := range want {
		got, ok := read[field]
		if !ok {
			t.Fatalf("typed read omitted the revisable field %q; a revision would reset it (read=%#v)", field, read)
		}
		if fmt.Sprint(got) != fmt.Sprint(value) {
			t.Fatalf("typed read changed %q: got %#v, want %#v", field, got, value)
		}
	}
}
