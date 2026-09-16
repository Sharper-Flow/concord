package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	storetest "github.com/sharper-flow/concord/internal/store/storetest"
)

// issue_adopt is the agent-surface route that records an existing Linear issue
// for an unlinked work item. The mutation queues the adoption; the drain owns
// every remote effect. The refusals mirror the store: a confirmed link, an
// issue another work item links, and missing planning setup.
func TestDispatchIssueAdoptQueuesOutboxOperation(t *testing.T) {
	t.Parallel()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "adopt-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "adopt-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "adopt-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProductPlanningMode(ctx, "product-1", store.PlanningModeLinear, "pilot adoption", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedAdoptionConnectionResource(t, s)
	seedAdoptionWorkItem(t, s, "adopt-agent-work", "project-1")

	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := CallEnvelope{SchemaVersion: "1.0", RequestID: "adopt-request-1", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}

	request := InvokeRequest{Tool: "concord_work_define", Operation: "issue_adopt", Input: json.RawMessage(`{"work_id":"adopt-agent-work","remote_issue_uuid":"cccccccc-0000-0000-0000-000000000003","idempotency_key":"adopt-idem-1"}`)}
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("issue_adopt response error=%+v err=%v", response.Error, err)
	}
	if len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].EntityKind != "linear_outbox_operation" {
		t.Fatalf("changed refs=%#v", response.ChangedRefs)
	}
	operationID := (*response.ChangedRefs)[0].ID
	var opKind, state, payload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT op_kind, state, payload FROM linear_outbox WHERE operation_id=?`, operationID).Scan(&opKind, &state, &payload); err != nil {
		t.Fatal(err)
	}
	if opKind != "issue_adopt" || state != "queued" {
		t.Fatalf("outbox = %s/%s, want queued/issue_adopt", opKind, state)
	}
	var decoded struct {
		ProductID       string `json:"product_id"`
		TeamID          string `json:"team_id"`
		RemoteIssueUUID string `json:"remote_issue_uuid"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ProductID != "product-1" || decoded.TeamID != "team-uuid-1" || decoded.RemoteIssueUUID != "cccccccc-0000-0000-0000-000000000003" {
		t.Fatalf("payload = %+v", decoded)
	}

	// The same request replays; a different request under the same key
	// refuses as an idempotency conflict.
	env.RequestID = "adopt-request-2"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("adopt replay=%+v err=%v", replay, err)
	}
	request.Input = json.RawMessage(`{"work_id":"adopt-agent-work","remote_issue_uuid":"dddddddd-0000-0000-0000-000000000004","idempotency_key":"adopt-idem-1"}`)
	env.RequestID = "adopt-request-3"
	conflict, err := Dispatch(ctx, s, service, request, env)
	if err != nil || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("adopt digest conflict=%+v err=%v", conflict, err)
	}
}

func TestDispatchIssueAdoptRefusesConfirmedAndForeignLinks(t *testing.T) {
	t.Parallel()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "adopt2-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "adopt2-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "adopt2-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetProductPlanningMode(ctx, "product-1", store.PlanningModeLinear, "pilot adoption", "operator", 2); err != nil {
		t.Fatal(err)
	}
	seedAdoptionConnectionResource(t, s)
	seedAdoptionWorkItem(t, s, "adopt-confirmed-work", "project-1")
	seedAdoptionWorkItem(t, s, "adopt-probe-work", "project-1")
	if err := s.RecordLinearLink(ctx, "adopt-confirmed-work", "aaaaaaaa-0000-0000-0000-000000000001", "EX-1", "https://linear.app/example/issue/EX-1", "", "", store.LinearLinkPending); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(ctx, "adopt-confirmed-work", "aaaaaaaa-0000-0000-0000-000000000001", "EX-1", "https://linear.app/example/issue/EX-1", "", "", store.LinearLinkConfirmed); err != nil {
		t.Fatal(err)
	}
	// A second work item's placeholder also reserves its issue.
	if err := s.RecordLinearLink(ctx, "adopt-holder-work", "bbbbbbbb-0000-0000-0000-000000000002", "EX-2", "https://linear.app/example/issue/EX-2", "", "", store.LinearLinkUnpublished); err != nil {
		t.Fatal(err)
	}

	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := CallEnvelope{SchemaVersion: "1.0", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}

	confirmed := InvokeRequest{Tool: "concord_work_define", Operation: "issue_adopt", Input: json.RawMessage(`{"work_id":"adopt-confirmed-work","remote_issue_uuid":"cccccccc-0000-0000-0000-000000000003","idempotency_key":"adopt-confirmed-1"}`)}
	env.RequestID = "adopt-confirmed-request"
	response, err := Dispatch(ctx, s, service, confirmed, env)
	if err != nil || response.Error == nil || !strings.Contains(response.Error.Message, "already holds a confirmed link") {
		t.Fatalf("confirmed link response error=%+v err=%v", response.Error, err)
	}

	foreign := InvokeRequest{Tool: "concord_work_define", Operation: "issue_adopt", Input: json.RawMessage(`{"work_id":"adopt-probe-work","remote_issue_uuid":"bbbbbbbb-0000-0000-0000-000000000002","idempotency_key":"adopt-foreign-1"}`)}
	env.RequestID = "adopt-foreign-request"
	response, err = Dispatch(ctx, s, service, foreign, env)
	if err != nil || response.Error == nil || !strings.Contains(response.Error.Message, "another work item already links that issue") {
		t.Fatalf("foreign link response error=%+v err=%v", response.Error, err)
	}
}

func seedAdoptionConnectionResource(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"linear": map[string]any{
		"workspace_url": "https://linear.app/example", "team_id": "team-uuid-1",
		"project_ids": map[string]string{"project-1": "project-uuid-1"},
		"auth_mode":   "personal_api_key",
	}})
	if _, err := tx.Exec(`INSERT INTO managed_resources(resource_id, display_name, class, kind, purpose, stage_maturity, stage_audience_commitment, environments, metadata_schema_version, metadata, version, created_at, updated_at) VALUES ('adopt-conn', 'Linear connection', 'saas', 'saas_account', 'Linear planning connection', 'prototype', 'operator_only', '["production"]', 'linear-connection-v1', ?, 1, '2026-09-16T00:00:00Z', '2026-09-16T00:00:00Z')`, string(metadata)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO resource_products(resource_id, product_id, role, purpose, environments) VALUES ('adopt-conn', 'product-1', 'owner', 'Linear planning connection', '["production"]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedAdoptionWorkItem(t *testing.T, s *store.Store, workID, projectID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', 'Adopt agent title', 'needed', 0, 'standard', 1, '{"title":"Adopt agent title","value_statement":"Adopt value","kind":"task","priority":0,"urgency":"standard"}', '2026-09-16T00:00:00Z', '2026-09-16T00:00:00Z')`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, workID, projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

}
