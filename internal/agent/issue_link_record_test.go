package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	storetest "github.com/sharper-flow/concord/internal/store/storetest"
)

// issueLinkRecordFixture opens a store with one Product, one primary Project,
// and the named work items, plus a work_define grant.
func issueLinkRecordFixture(t *testing.T, workIDs ...string) (*store.Store, *Service, CallEnvelope) {
	t.Helper()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	events := []store.Event{
		{EventID: "link-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "link-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "link-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	for _, workID := range workIDs {
		seedLinkWorkItem(t, s, workID, "project-1")
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := CallEnvelope{SchemaVersion: "1.0", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Directory, Worktree: grant.Worktree, AmbientProjectID: "project-1", SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
	return s, service, env
}

// issue_link_record records the Linear issue identity the agent read from the
// Linear MCP server. Concord makes no Linear call, queues nothing, and the
// stored row is exactly the reported key, UUID, and URL (CD-0213 D3, D7).
func TestDispatchIssueLinkRecordStoresReportedIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, env := issueLinkRecordFixture(t, "link-agent-work")

	request := InvokeRequest{Tool: "concord_work_define", Operation: "issue_link_record", Input: json.RawMessage(`{"work_id":"link-agent-work","human_key":"EX-3","remote_issue_uuid":"cccccccc-0000-0000-0000-000000000003","url":"https://linear.app/example/issue/EX-3","idempotency_key":"link-idem-1"}`)}
	env.RequestID = "link-request-1"
	response, err := Dispatch(ctx, s, service, request, env)
	if err != nil || response.Outcome != OutcomeOK {
		t.Fatalf("issue_link_record response error=%+v err=%v", response.Error, err)
	}
	if len(*response.ChangedRefs) != 1 || (*response.ChangedRefs)[0].EntityKind != "linear_issue_link" || (*response.ChangedRefs)[0].ID != "link-agent-work" {
		t.Fatalf("changed refs=%#v", response.ChangedRefs)
	}
	link, err := s.ReadLinearLink(ctx, "link-agent-work")
	if err != nil {
		t.Fatal(err)
	}
	want := store.LinearIssueLink{WorkID: "link-agent-work", RemoteIssueUUID: "cccccccc-0000-0000-0000-000000000003", HumanKey: "EX-3", URL: "https://linear.app/example/issue/EX-3"}
	if link != want {
		t.Fatalf("recorded link = %+v, want %+v", link, want)
	}

	// The same request replays; a different request under the same key
	// refuses as an idempotency conflict.
	env.RequestID = "link-request-2"
	replay, err := Dispatch(ctx, s, service, request, env)
	if err != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("link replay=%+v err=%v", replay, err)
	}
	request.Input = json.RawMessage(`{"work_id":"link-agent-work","human_key":"EX-4","remote_issue_uuid":"dddddddd-0000-0000-0000-000000000004","url":"https://linear.app/example/issue/EX-4","idempotency_key":"link-idem-1"}`)
	env.RequestID = "link-request-3"
	conflict, err := Dispatch(ctx, s, service, request, env)
	if err != nil || conflict.Error == nil || conflict.Error.Kind != "idempotency_conflict" {
		t.Fatalf("link digest conflict=%+v err=%v", conflict, err)
	}
}

// A work item holds at most one identity, and one issue belongs to at most
// one work item. A malformed key refuses before any write.
func TestDispatchIssueLinkRecordRefusesSecondAndForeignIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, env := issueLinkRecordFixture(t, "link-held-work", "link-probe-work")
	if _, err := s.RecordLinearIssueLink(ctx, store.LinearIssueLink{WorkID: "link-held-work", RemoteIssueUUID: "aaaaaaaa-0000-0000-0000-000000000001", HumanKey: "EX-1", URL: "https://linear.app/example/issue/EX-1"}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, input, want string
	}{
		{"second identity", `{"work_id":"link-held-work","human_key":"EX-3","remote_issue_uuid":"cccccccc-0000-0000-0000-000000000003","url":"https://linear.app/example/issue/EX-3","idempotency_key":"link-second-1"}`, "already records a different Linear issue identity"},
		{"foreign issue", `{"work_id":"link-probe-work","human_key":"EX-1","remote_issue_uuid":"aaaaaaaa-0000-0000-0000-000000000001","url":"https://linear.app/example/issue/EX-1","idempotency_key":"link-foreign-1"}`, "another work item already records that Linear issue"},
		{"malformed key", `{"work_id":"link-probe-work","human_key":"not a key","remote_issue_uuid":"eeeeeeee-0000-0000-0000-000000000005","url":"https://linear.app/example/issue/EX-5","idempotency_key":"link-malformed-1"}`, "pattern at $.human_key"},
	}
	for _, tc := range cases {
		env.RequestID = "link-" + strings.ReplaceAll(tc.name, " ", "-")
		response, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "issue_link_record", Input: json.RawMessage(tc.input)}, env)
		if tc.name == "malformed key" {
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: err=%v, want schema refusal %q", tc.name, err, tc.want)
			}
			continue
		}
		if err != nil || response.Error == nil || !strings.Contains(response.Error.Message, tc.want) {
			t.Fatalf("%s: response error=%+v err=%v", tc.name, response.Error, err)
		}
	}
	if link, err := s.ReadLinearLink(ctx, "link-held-work"); err != nil || link.HumanKey != "EX-1" {
		t.Fatalf("held link = %+v err=%v", link, err)
	}
	if _, err := s.ReadLinearLink(ctx, "link-probe-work"); err == nil {
		t.Fatal("probe work recorded a refused identity")
	}
}

func seedLinkWorkItem(t *testing.T, s *store.Store, workID, projectID string) {
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
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', 'Link agent title', 'needed', 0, 'standard', 1, '{"title":"Link agent title","value_statement":"Link value","kind":"task","priority":0,"urgency":"standard"}', '2026-09-16T00:00:00Z', '2026-09-16T00:00:00Z')`, workID); err != nil {
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
