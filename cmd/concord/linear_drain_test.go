package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

// seedLinearCLIWork seeds a work item with primary membership in the Product's
// project so the enqueue path resolves exactly one Product.
func seedLinearCLIWork(t *testing.T, dbPath, workID, projectID, title string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	intent, _ := json.Marshal(map[string]any{"title": title, "value_statement": "CLI drain value statement", "kind": "task", "priority": 0, "urgency": "standard"})
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', ?, 'needed', 0, 'standard', 1, ?, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`, workID, title, string(intent)); err != nil {
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

// seedLinearCLIProjectLink records the confirmed Linear Project of one
// Initiative, as the drain completion does, so an update addresses it.
func seedLinearCLIProjectLink(t *testing.T, dbPath, initiative, remoteUUID string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_project_links(work_id, remote_project_uuid, name, url, created_at, updated_at) VALUES(?, ?, ?, '', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`, initiative, remoteUUID, initiative); err != nil {
		t.Fatalf("seed project link %s: %v", initiative, err)
	}
}

func enableLinearProduct(t *testing.T, dbPath, productID string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var projectID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT project_id FROM product_projects WHERE product_id=? AND role='primary'`, productID).Scan(&projectID); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	runOperatorJSON(t, dbPath, []string{"product-mode-set"}, map[string]any{
		"product_id": productID, "planning_mode": "linear_enabled",
		"reason": "CD-0121 activation for the drain test", "expected_version": 2,
	})
	runOperatorJSON(t, dbPath, []string{"resource-create"}, map[string]any{
		"event_id": "drain-conn-" + productID, "resource_id": "drain-conn-" + productID, "product_id": productID,
		"display_name": "Linear connection", "class": "saas", "kind": "saas_account", "purpose": "Linear planning connection",
		"stage_maturity": "prototype", "stage_audience_commitment": "operator_only", "environments": []string{"production"},
		"metadata_schema_version": "linear-connection-v1",
		"metadata": map[string]any{"linear": map[string]any{
			"workspace_url": "https://linear.app/example", "team_id": "68d52710-76d9-4b41-ba45-778511d0e2ed", "auth_mode": "personal_api_key",
			"status_ids": map[string]string{"needed": "state-needed", "in_progress": "state-in-progress", "cancelled": "state-cancelled", "completed": "state-completed", "superseded": "state-superseded"},
			// CD-0171 D3: every synced issue carries its repository label, so
			// the fixture connection maps the Product's primary project.
			"label_ids": map[string]string{"project:" + projectID: "label-repo"},
		}},
		"expected_product_version": 3,
	})
}

func TestLinearEnqueueAndDrainCLI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "drain-product", "drain-product-project")
	enableLinearProduct(t, dbPath, "drain-product")
	seedLinearCLIWork(t, dbPath, "drain-work", "drain-product-project", "Drain title")
	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "drain-label-update", "resource_id": "drain-conn-drain-product", "product_id": "drain-product",
		"label_ids": map[string]string{"task": "label-task", "project:drain-product-project": "label-repo"}, "expected_resource_version": 1,
	})

	var sawAuth, sawLabels, sawStatus, sawPriority bool
	// CD-0171 D4: a work item outside every Initiative syncs with an empty
	// Project field, so the create input carries no projectId at all.
	var sawNoProject bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "issueCreate") {
			if r.Header.Get("Authorization") == "lin_api_cli_test" {
				sawAuth = true
			}
			sawNoProject = !strings.Contains(string(body), "projectId")
			sawLabels = strings.Contains(string(body), `"labelIds":["label-task","label-repo"]`)
			sawStatus = strings.Contains(string(body), `"stateId":"state-needed"`)
			sawPriority = strings.Contains(string(body), `"priority":3`)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "issueCreate") {
			_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-1","url":"https://linear.app/example/issue/SHA-1","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-1","url":"https://linear.app/example/issue/SHA-1","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_cli_test")

	// A missing key refuses before any remote call, with nothing queued.
	t.Setenv(linearclient.EnvAPIKey, "")
	var out, errOut strings.Builder
	t.Setenv(dbOverrideEnv, dbPath)
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"drain-product"}`), &out, &errOut); code == 0 {
		t.Fatalf("drain without a key must exit non-zero; stdout=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "missing_credential") {
		t.Fatalf("stderr=%q", errOut.String())
	}
	out.Reset()
	errOut.Reset()

	// Happy path: enqueue once, drain, done, confirmed link.
	t.Setenv(linearclient.EnvAPIKey, "lin_api_cli_test")
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "drain-product", "work_id": "drain-work", "op_kind": "issue_create"})
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"drain-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if !sawAuth || !sawNoProject || !sawLabels || !sawStatus || !sawPriority {
		t.Fatalf("the drain request lacked authorization, the empty Project field, labels, the birth status, or the seeded priority: auth=%t noProject=%t labels=%t status=%t priority=%t", sawAuth, sawNoProject, sawLabels, sawStatus, sawPriority)
	}
	var drained struct {
		OK         bool `json:"ok"`
		Drained    int  `json:"drained"`
		Operations []struct {
			OperationID string `json:"operation_id"`
			Outcome     string `json:"outcome"`
			Identifier  string `json:"identifier"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &drained); err != nil {
		t.Fatalf("drain output %q: %v", out.String(), err)
	}
	if !drained.OK || drained.Drained != 1 || len(drained.Operations) != 1 || drained.Operations[0].Outcome != "done" || drained.Operations[0].Identifier != "SHA-1" {
		t.Fatalf("drain result = %+v", drained)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var outboxState, linkState, remoteUUID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM linear_outbox WHERE operation_id=?`, drained.Operations[0].OperationID).Scan(&outboxState); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT link_state, remote_issue_uuid FROM linear_issue_links WHERE work_id='drain-work'`).Scan(&linkState, &remoteUUID); err != nil {
		t.Fatal(err)
	}
	if outboxState != "done" || linkState != "confirmed" || remoteUUID != "68d52710-76d9-4b41-ba45-778511d0e2ed" {
		t.Fatalf("terminal = %s/%s/%s, want done/confirmed/remote uuid", outboxState, linkState, remoteUUID)
	}

	// A local_only Product refuses enqueue of its own work with the typed
	// planning refusal.
	seedCLIProduct(t, dbPath, "local-product", "local-product-project")
	seedLinearCLIWork(t, dbPath, "local-work", "local-product-project", "Local title")
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear-issue-enqueue"}, strings.NewReader(`{"product_id":"local-product","work_id":"local-work","op_kind":"issue_create"}`), &out, &errOut); code == 0 {
		t.Fatal("enqueue against a local_only Product must exit non-zero")
	}
	if !strings.Contains(errOut.String(), "local_only") {
		t.Fatalf("stderr=%q", errOut.String())
	}
}

func TestLinearIssueUpdateDrainReportsDoneAndMirrorsTerminalStatus(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "update-drain-product", "update-drain-project")
	enableLinearProduct(t, dbPath, "update-drain-product")
	seedLinearCLIWork(t, dbPath, "update-drain-work", "update-drain-project", "Cancelled title")
	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "update-drain-label-update", "resource_id": "drain-conn-update-drain-product", "product_id": "update-drain-product",
		"label_ids": map[string]string{"task": "label-task", "project:update-drain-project": "label-repo"}, "expected_resource_version": 1,
	})

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{
		EventID: "update-drain-cancelled", Kind: "work.transitioned", SubjectType: store.SubjectWorkItem, SubjectID: "update-drain-work", Actor: "operator", OccurredAt: fixedLinearTestTime(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"from":"needed","to":"cancelled","reason":"cancelled for test","expected_version":1,"resulting_version":2}`),
	}}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "update-drain-work"): 1}}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(context.Background(), "update-drain-work", "remote-update", "SHA-3", "https://linear.app/example/issue/SHA-3", "", "", store.LinearLinkUnpublished); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(context.Background(), "update-drain-work", "remote-update", "SHA-3", "https://linear.app/example/issue/SHA-3", "", "", store.LinearLinkPending); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(context.Background(), "update-drain-work", "remote-update", "SHA-3", "https://linear.app/example/issue/SHA-3", "", "", store.LinearLinkConfirmed); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()

	var sawStatus, sawLabels, sawResentPriority bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "issueUpdate") {
			sawStatus = strings.Contains(string(body), `"stateId":"state-cancelled"`)
			sawLabels = strings.Contains(string(body), `"addedLabelIds":["label-task","label-repo"]`)
			if strings.Contains(string(body), `"priority":`) {
				sawResentPriority = true
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "issueUpdate") {
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"remote-update","identifier":"SHA-3","url":"https://linear.app/example/issue/SHA-3","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-update","identifier":"SHA-3","url":"https://linear.app/example/issue/SHA-3","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"started"},"team":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_update_test")
	t.Setenv(dbOverrideEnv, dbPath)
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "update-drain-product", "work_id": "update-drain-work", "op_kind": "issue_update"})
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"update-drain-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if !sawStatus || !sawLabels {
		t.Fatalf("drain did not send the declared status and label: status=%t labels=%t", sawStatus, sawLabels)
	}
	if sawResentPriority {
		t.Fatal("an issueUpdate drain resent a priority; Linear owns triage after creation")
	}
	var drained struct {
		Operations []struct {
			Outcome string `json:"outcome"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &drained); err != nil {
		t.Fatal(err)
	}
	if len(drained.Operations) != 1 || drained.Operations[0].Outcome != "done" {
		t.Fatalf("drain result = %+v", drained)
	}
}

func TestLinearIssueUpdateDrainOmitsUnchangedContent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "unchanged-product", "unchanged-project")
	enableLinearProduct(t, dbPath, "unchanged-product")
	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "unchanged-connection-update", "resource_id": "drain-conn-unchanged-product", "product_id": "unchanged-product",
		"team_id": "68d52710-76d9-4b41-ba45-778511d0e2ed", "status_ids": map[string]string{"needed": "state-needed", "in_progress": "state-in-progress", "cancelled": "state-cancelled", "completed": "state-completed", "superseded": "state-superseded"}, "expected_resource_version": 1,
	})
	seedLinearCLIWork(t, dbPath, "unchanged-work", "unchanged-project", "Unchanged title")

	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{store.LinearLinkUnpublished, store.LinearLinkPending} {
		if err := s.RecordLinearLink(ctx, "unchanged-work", "remote-unchanged", "SHA-4", "https://linear.app/example/issue/SHA-4", "", "", state); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	description := "## Value statement\n\nCLI drain value statement\n\ntask · Resume: `concord zl unchanged-work --`"
	if err := s.RecordLinearLink(ctx, "unchanged-work", "remote-unchanged", "SHA-4", "https://linear.app/example/issue/SHA-4", "", linearContentHash("Unchanged title", description), store.LinearLinkConfirmed); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if _, err := s.EnqueueLinearIssueForWork(ctx, "unchanged-work", store.LinearOpIssueUpdate); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()

	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("decode request: %v", err)
		} else if strings.Contains(string(body), "issueUpdate") {
			if err := json.Unmarshal(body, &requestBody); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "issueUpdate") {
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"remote-unchanged","identifier":"SHA-4","url":"https://linear.app/example/issue/SHA-4","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-unchanged","identifier":"SHA-4","url":"https://linear.app/example/issue/SHA-4","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"team-uuid-1"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_unchanged_test")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"unchanged-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	variables, ok := requestBody["variables"].(map[string]any)
	if !ok {
		t.Fatalf("request variables = %#v", requestBody["variables"])
	}
	input, ok := variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("request input = %#v", variables["input"])
	}
	if _, present := input["title"]; present {
		t.Fatalf("unchanged update sent title: %#v", input)
	}
	if _, present := input["description"]; present {
		t.Fatalf("unchanged update sent description: %#v", input)
	}
	if input["stateId"] != "state-needed" {
		t.Fatalf("request input = %#v, want stateId", input)
	}
}

// CD-0171 review correction: an issue_update carries the issue's full Project
// and Concord-managed label state. When no Initiative Project applies, the
// input sends an explicit null projectId so Linear clears the field (the
// omitted field left a stale Project behind after the last Initiative entry
// left), and labels under the project:* and optional keys that no longer
// apply ride removedLabelIds instead of lingering remotely.
func TestLinearIssueUpdateDrainClearsProjectAndRemovesStaleLabels(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "syncfix-product", "syncfix-project")
	enableLinearProduct(t, dbPath, "syncfix-product")
	seedLinearCLIWork(t, dbPath, "syncfix-work", "syncfix-project", "Sync fix title")
	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "syncfix-label-update", "resource_id": "drain-conn-syncfix-product", "product_id": "syncfix-product",
		"label_ids": map[string]string{"task": "label-task", "project:syncfix-project": "label-repo", "optional": "label-optional"}, "expected_resource_version": 1,
	})

	// The work item is a required entry of one Initiative whose issue is
	// already confirmed. Its remote issue still carries the optional label
	// from an earlier non-required entry, one managed repository label, and
	// one label Concord never manages.
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	initiativeIntent := `{"title":"Sync initiative","value_statement":"Sync value","kind":"initiative","priority":0,"urgency":"standard"}`
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES('syncfix-initiative', 'initiative', 'Sync initiative', 'needed', 0, 'standard', 1, ?, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`, initiativeIntent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES('syncfix-initiative', 'syncfix-project', 'primary')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES('syncfix-initiative', 'syncfix-work', 0, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES('syncfix-work', 'remote-syncfix-1', 'SF-1', '', '', '', 'confirmed', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.Close()

	var updateBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(text, "issueUpdate") {
			updateBodies = append(updateBodies, text)
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"remote-syncfix-1","identifier":"SF-1","url":"https://linear.app/example/issue/SF-1","updatedAt":"2026-09-23T12:00:00Z"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-syncfix-1","identifier":"SF-1","url":"https://linear.app/example/issue/SF-1","title":"Sync fix title","updatedAt":"2026-09-23T12:00:00Z","state":{"type":"started"},"team":{"id":"sync-team"},"labels":{"nodes":[{"id":"label-task"},{"id":"label-repo"},{"id":"label-optional"},{"id":"label-foreign"}]}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_syncfix_test")
	t.Setenv(dbOverrideEnv, dbPath)

	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "syncfix-product", "work_id": "syncfix-work", "op_kind": "issue_update"})
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"syncfix-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("first drain exit=%d stderr=%q", code, errOut.String())
	}
	if len(updateBodies) != 1 {
		t.Fatalf("issueUpdate calls = %d, want 1", len(updateBodies))
	}
	first := updateBodies[0]
	if !strings.Contains(first, `"projectId":null`) {
		t.Fatalf("first update body = %q, want the explicit null projectId", first)
	}
	if !strings.Contains(first, `"removedLabelIds":["label-optional"]`) {
		t.Fatalf("first update body = %q, want the stale optional label removed", first)
	}
	if strings.Contains(first, "label-foreign") {
		t.Fatalf("first update body = %q, want the unmanaged label left alone", first)
	}

	// Once the owning Initiative's Project exists, the update carries it.
	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_project_links(work_id, remote_project_uuid, name, url, created_at, updated_at) VALUES('syncfix-initiative', 'remote-project-syncfix', 'Sync initiative', '', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	updateBodies = updateBodies[:0]
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "syncfix-product", "work_id": "syncfix-work", "op_kind": "issue_update"})
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"syncfix-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("second drain exit=%d stderr=%q", code, errOut.String())
	}
	if len(updateBodies) != 1 {
		t.Fatalf("second drain issueUpdate calls = %d, want 1", len(updateBodies))
	}
	if !strings.Contains(updateBodies[0], `"projectId":"remote-project-syncfix"`) {
		t.Fatalf("second update body = %q, want the owning Initiative's project uuid", updateBodies[0])
	}

	// Leaving the Initiative clears the Project again (CD-0171 D4/D6): this
	// is the exact path the omitted projectId left broken.
	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM initiative_entries WHERE initiative_work_id='syncfix-initiative' AND child_work_id='syncfix-work'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	updateBodies = updateBodies[:0]
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "syncfix-product", "work_id": "syncfix-work", "op_kind": "issue_update"})
	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"syncfix-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("third drain exit=%d stderr=%q", code, errOut.String())
	}
	if len(updateBodies) != 1 {
		t.Fatalf("third drain issueUpdate calls = %d, want 1", len(updateBodies))
	}
	third := updateBodies[0]
	if !strings.Contains(third, `"projectId":null`) {
		t.Fatalf("third update body = %q, want the Project cleared after the entry left", third)
	}
	// The repository label stays: the work item keeps its Concord project
	// membership, and repository identity rides the label (CD-0171 D3).
	if !strings.Contains(third, `"labelIds":["label-task","label-repo"]`) && !strings.Contains(third, `"addedLabelIds":["label-task","label-repo"]`) {
		t.Fatalf("third update body = %q, want the repository label kept", third)
	}
	if strings.Contains(third, "remote-project-syncfix") {
		t.Fatalf("third update body = %q, want no stale Initiative Project", third)
	}
}

func TestLinearDrainSuppressesStaleRetry(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "stale-retry-product", "stale-retry-project")
	enableLinearProduct(t, dbPath, "stale-retry-product")
	seedLinearCLIWork(t, dbPath, "stale-retry-work", "stale-retry-project", "Stale retry title")

	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{store.LinearLinkUnpublished, store.LinearLinkPending} {
		if err := s.RecordLinearLink(ctx, "stale-retry-work", "remote-stale-retry", "SHA-5", "https://linear.app/example/issue/SHA-5", "", "", state); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	newDescription := "new authoritative description"
	if err := s.RecordLinearLink(ctx, "stale-retry-work", "remote-stale-retry", "SHA-5", "https://linear.app/example/issue/SHA-5", "", linearContentHash("new title", newDescription), store.LinearLinkConfirmed); err != nil {
		s.Close()
		t.Fatal(err)
	}
	oldPayload, err := json.Marshal(map[string]any{"client_uuid": "old-client-id", "product_id": "stale-retry-product", "title": "old title", "description": "old description", "team_id": "team-uuid-1", "connection_version": 1, "lifecycle": "needed", "status_id": "state-needed"})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	newPayload, err := json.Marshal(map[string]any{"client_uuid": "new-client-id", "product_id": "stale-retry-product", "title": "new title", "description": newDescription, "team_id": "team-uuid-1", "connection_version": 1, "lifecycle": "needed", "status_id": "state-needed"})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.EnqueueLinearOperation(ctx, store.LinearOutboxEntry{OperationID: "a-old-operation", WorkID: "stale-retry-work", OpKind: store.LinearOpIssueUpdate, IdempotencyKey: "a-old-idempotency", Payload: oldPayload}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.EnqueueLinearOperation(ctx, store.LinearOutboxEntry{OperationID: "z-new-operation", WorkID: "stale-retry-work", OpKind: store.LinearOpIssueUpdate, IdempotencyKey: "z-new-idempotency", Payload: newPayload}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.ClaimLinearOperation(ctx, "z-new-operation"); err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.CompleteLinearOperation(ctx, "z-new-operation", store.LinearRemoteIdentity{RemoteUUID: "remote-stale-retry", HumanKey: "SHA-5", URL: "https://linear.app/example/issue/SHA-5", RemoteUpdatedAt: "2026-09-09T12:00:00Z", ContentHash: linearContentHash("new title", newDescription)}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "issueUpdate") {
			calls++
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_stale_retry_test")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"stale-retry-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if calls != 0 {
		t.Fatalf("stale retry reached Linear %d times", calls)
	}
	var oldState, linkHash string
	s, err = store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM linear_outbox WHERE operation_id='a-old-operation'`).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM linear_issue_links WHERE work_id='stale-retry-work'`).Scan(&linkHash); err != nil {
		t.Fatal(err)
	}
	if oldState != store.LinearOutboxDone || linkHash != linearContentHash("new title", newDescription) {
		t.Fatalf("stale retry state = %s/%s, want done and newer content hash", oldState, linkHash)
	}
}

func TestLinearDrainRefusesQueuedOperationAfterConnectionChange(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "stale-product", "stale-project")
	enableLinearProduct(t, dbPath, "stale-product")
	seedLinearCLIWork(t, dbPath, "stale-work", "stale-project", "Stale title")
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "stale-product", "work_id": "stale-work", "op_kind": "issue_create"})
	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "stale-connection-update", "resource_id": "drain-conn-stale-product", "product_id": "stale-product",
		"team_id": "new-team", "status_ids": map[string]string{"needed": "new-needed", "in_progress": "new-in-progress", "cancelled": "new-cancelled", "completed": "new-completed", "superseded": "new-superseded"}, "expected_resource_version": 1,
	})

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "issueCreate") {
			calls++
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_stale_test")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"stale-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if calls != 0 {
		t.Fatalf("stale operation reached the provider %d times", calls)
	}
	var state string
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM linear_outbox WHERE work_id='stale-work'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != store.LinearOutboxFailed {
		t.Fatalf("stale operation state=%s, want failed", state)
	}
}

func TestLinearDrainRefusesLegacyQueuedOperationAfterConnectionChange(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "legacy-stale-product", "legacy-stale-project")
	enableLinearProduct(t, dbPath, "legacy-stale-product")
	seedLinearCLIWork(t, dbPath, "legacy-stale-work", "legacy-stale-project", "Legacy stale title")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"client_uuid": "legacy-client-uuid",
		"title":       "Legacy stale title",
		"description": "Legacy stale description",
		"team_id":     "previous-team",
	})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	if err := s.EnqueueLinearOperation(context.Background(), store.LinearOutboxEntry{
		OperationID: "legacy-stale-operation", WorkID: "legacy-stale-work", OpKind: store.LinearOpIssueCreate,
		IdempotencyKey: "legacy-stale-idempotency", Payload: payload,
	}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()

	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "legacy-stale-connection-update", "resource_id": "drain-conn-legacy-stale-product", "product_id": "legacy-stale-product",
		"team_id": "new-team", "status_ids": map[string]string{"needed": "new-needed", "in_progress": "new-in-progress", "cancelled": "new-cancelled", "completed": "new-completed", "superseded": "new-superseded"}, "expected_resource_version": 1,
	})

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_legacy_stale_test")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"legacy-stale-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if calls != 0 {
		t.Fatalf("legacy stale operation reached the provider %d times", calls)
	}
	var state, detail string
	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DatabaseForTesting().QueryRow(`SELECT state, last_error FROM linear_outbox WHERE operation_id='legacy-stale-operation'`).Scan(&state, &detail); err != nil {
		t.Fatal(err)
	}
	if state != store.LinearOutboxFailed || !strings.Contains(detail, "connection changed") {
		t.Fatalf("legacy stale operation = %s/%s, want failed connection-change detail", state, detail)
	}
}

func TestLinearDrainRefreshesConnectionPerClaimedOperation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "refresh-product", "refresh-project")
	enableLinearProduct(t, dbPath, "refresh-product")
	seedLinearCLIWork(t, dbPath, "refresh-work-a", "refresh-project", "Refresh A")
	seedLinearCLIWork(t, dbPath, "refresh-work-b", "refresh-project", "Refresh B")
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "refresh-product", "work_id": "refresh-work-a", "op_kind": "issue_create"})
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "refresh-product", "work_id": "refresh-work-b", "op_kind": "issue_create"})

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "issueCreate") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"refresh-remote","identifier":"SHA-9","url":"https://linear.app/example/issue/SHA-9","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"refresh-team"}}}}`))
			return
		}
		calls++
		if calls == 1 {
			s, err := store.Open(context.Background(), dbPath)
			if err != nil {
				t.Errorf("open update store: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			err = s.UpdateLinearConnection(context.Background(), store.LinearConnectionUpdateRequest{
				EventID: "refresh-connection-update", ResourceID: "drain-conn-refresh-product", ProductID: "refresh-product",
				TeamID: "refresh-team", StatusIDs: map[string]string{"needed": "refresh-needed", "in_progress": "refresh-in-progress", "cancelled": "refresh-cancelled", "completed": "refresh-completed", "superseded": "refresh-superseded"},
				ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: fixedLinearTestTime(),
			})
			s.Close()
			if err != nil {
				t.Errorf("update connection: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"refresh-remote","identifier":"SHA-9","url":"https://linear.app/example/issue/SHA-9","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_refresh_test")
	t.Setenv(dbOverrideEnv, dbPath)
	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"refresh-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want one before connection refresh stops the second", calls)
	}
}

// seedLinearInitiativeFixture seeds an Initiative with a narrative, an entry
// work item when entryID is non-empty, their Product memberships, and the
// entry row, mirroring the capture folds the drain tests drive.
func seedLinearInitiativeFixture(t *testing.T, dbPath, projectID, initiativeID, entryID, narrative string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	initiativeIntent := `{"title":"Initiative title","value_statement":"Initiative value","kind":"initiative","priority":0,"urgency":"standard"}`
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, narrative, created_at, updated_at) VALUES(?, 'initiative', 'Initiative title', 'needed', 0, 'standard', 1, ?, ?, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`, initiativeID, initiativeIntent, narrative); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, initiativeID, projectID); err != nil {
		t.Fatal(err)
	}
	if entryID != "" {
		entryIntent := `{"title":"Entry title","value_statement":"Entry value","kind":"task","priority":0,"urgency":"standard"}`
		if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', 'Entry title', 'needed', 0, 'standard', 1, ?, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`, entryID, entryIntent); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, entryID, projectID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES(?, ?, 0, 1)`, initiativeID, entryID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// CD-0171 review correction: the drain resolves the owning Initiative's
// confirmed Project at issue_create drain time, so neither claim order can
// strand an entry issue outside its Initiative's Project. When the Project
// drains first, the create carries the Project from the start; when the issue
// drains first, the project_create completion refreshes the now-confirmed
// entry with a queued update.
func TestLinearDrainResolvesProjectAtIssueCreateDrainTime(t *testing.T) {
	for name, projectFirst := range map[string]bool{"project drains first": true, "issue drains first": false} {
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "concord.db")
			seedCLIProduct(t, dbPath, "order-product", "order-project")
			enableLinearProduct(t, dbPath, "order-product")
			seedLinearInitiativeFixture(t, dbPath, "order-project", "order-initiative", "order-entry", "The order narrative.")

			s, err := store.Open(context.Background(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if projectFirst {
				if _, err := s.EnqueueLinearProjectForInitiative(context.Background(), "order-product", "order-initiative", store.LinearOpProjectCreate); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.EnqueueLinearIssueForProduct(context.Background(), "order-product", "order-entry", store.LinearOpIssueCreate); err != nil {
				t.Fatal(err)
			}
			if !projectFirst {
				if _, err := s.EnqueueLinearProjectForInitiative(context.Background(), "order-product", "order-initiative", store.LinearOpProjectCreate); err != nil {
					t.Fatal(err)
				}
			}
			s.Close()

			var projectBodies, issueCreateBodies, updateBodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				text := string(body)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.Contains(text, "projectCreate"):
					projectBodies = append(projectBodies, text)
					_, _ = w.Write([]byte(`{"data":{"projectCreate":{"success":true,"project":{"id":"remote-project-ordered","name":"Initiative title","description":"Initiative value","content":"The order narrative.","url":"https://linear.app/example/project/remote-project-ordered","updatedAt":"2026-09-23T01:00:00Z"}}}}`))
				case strings.Contains(text, "issueCreate"):
					issueCreateBodies = append(issueCreateBodies, text)
					_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"order-issue-remote","identifier":"OR-1","url":"https://linear.app/example/issue/OR-1","updatedAt":"2026-09-23T01:00:00Z"}}}}`))
				case strings.Contains(text, "issueUpdate"):
					updateBodies = append(updateBodies, text)
					_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"order-issue-remote","identifier":"OR-1","url":"https://linear.app/example/issue/OR-1","updatedAt":"2026-09-23T02:00:00Z"}}}}`))
				default:
					_, _ = w.Write([]byte(`{"data":{"issue":{"id":"order-issue-remote","identifier":"OR-1","url":"https://linear.app/example/issue/OR-1","updatedAt":"2026-09-23T03:00:00Z","state":{"type":"unstarted"},"team":{"id":"order-team"}}}}`))
				}
			}))
			defer server.Close()
			t.Setenv(linearclient.EnvEndpoint, server.URL)
			t.Setenv(linearclient.EnvAPIKey, "lin_api_order_test")
			t.Setenv(dbOverrideEnv, dbPath)

			var out, errOut strings.Builder
			if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"order-product"}`), &out, &errOut); code != 0 {
				t.Fatalf("first drain exit=%d stderr=%q", code, errOut.String())
			}
			s, err = store.Open(context.Background(), dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if projectFirst {
				// The create itself carried the Project: the completion refresh
				// could not have fixed the entry up, because its link was still
				// unpublished when the project_create completed.
				if len(issueCreateBodies) != 1 {
					t.Fatalf("issueCreate calls = %d, want 1", len(issueCreateBodies))
				}
				if !strings.Contains(issueCreateBodies[0], `"projectId":"remote-project-ordered"`) {
					t.Fatalf("issueCreate body = %q, want the owning Initiative's Project resolved at drain time", issueCreateBodies[0])
				}
				var queued int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_outbox WHERE work_id='order-entry' AND op_kind=? AND state=?`, store.LinearOpIssueUpdate, store.LinearOutboxQueued).Scan(&queued); err != nil {
					t.Fatal(err)
				}
				if queued != 0 {
					t.Fatalf("queued entry updates = %d, want 0: the create already carried the Project", queued)
				}
				return
			}
			// The issue drained before the Project existed, so the create body
			// carries no Project; the completion refresh queues the update
			// that moves the confirmed entry in.
			if len(issueCreateBodies) != 1 || strings.Contains(issueCreateBodies[0], "projectId") {
				t.Fatalf("issueCreate body = %q, want no Project before the Initiative's Project exists", issueCreateBodies)
			}
			var refreshPayload string
			if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM linear_outbox WHERE work_id='order-entry' AND op_kind=? AND state=?`, store.LinearOpIssueUpdate, store.LinearOutboxQueued).Scan(&refreshPayload); err != nil {
				t.Fatalf("the project completion queued no entry refresh: %v", err)
			}
			if !strings.Contains(refreshPayload, `"project_id":"remote-project-ordered"`) {
				t.Fatalf("entry refresh payload = %q, want the created Project", refreshPayload)
			}
			out.Reset()
			errOut.Reset()
			updateBodies = nil
			if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"order-product"}`), &out, &errOut); code != 0 {
				t.Fatalf("second drain exit=%d stderr=%q", code, errOut.String())
			}
			if len(updateBodies) != 1 || !strings.Contains(updateBodies[0], `"projectId":"remote-project-ordered"`) {
				t.Fatalf("issueUpdate bodies = %v, want the entry moved into the created Project", updateBodies)
			}
		})
	}
}

// CD-0171 review correction: the project_create drain sends the Initiative's
// current title, value statement, and narrative — not the payload snapshot —
// so a narrative revision that lands after enqueue, while no Project link
// exists yet, still ships with the create.
func TestLinearDrainSendsCurrentNarrativeOnProjectCreate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "narrdrain-product", "narrdrain-project")
	enableLinearProduct(t, dbPath, "narrdrain-product")
	seedLinearInitiativeFixture(t, dbPath, "narrdrain-project", "narrdrain-initiative", "", "The original narrative.")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueLinearProjectForInitiative(context.Background(), "narrdrain-product", "narrdrain-initiative", store.LinearOpProjectCreate); err != nil {
		t.Fatal(err)
	}
	// The narrative moves on before the drain; no Project link exists yet, so
	// the revision itself queues no project_update.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_items SET narrative='The revised narrative.' WHERE id='narrdrain-initiative'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	var createBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(text, "projectCreate") {
			createBody = text
			_, _ = w.Write([]byte(`{"data":{"projectCreate":{"success":true,"project":{"id":"remote-project-narrdrain","name":"Initiative title","description":"Initiative value","content":"The revised narrative.","url":"https://linear.app/example/project/remote-project-narrdrain","updatedAt":"2026-09-23T01:00:00Z"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"narrdrain-issue","identifier":"ND-1","url":"https://linear.app/example/issue/ND-1","updatedAt":"2026-09-23T01:00:00Z","state":{"type":"unstarted"},"team":{"id":"narrdrain-team"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_narrdrain_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"narrdrain-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(createBody, `"content":"The revised narrative."`) {
		t.Fatalf("projectCreate body = %q, want the narrative read at drain time", createBody)
	}
	if !strings.Contains(createBody, `"description":"Initiative value"`) || !strings.Contains(createBody, `"name":"Initiative title"`) {
		t.Fatalf("projectCreate body = %q, want the current title and value statement", createBody)
	}
}

// CD-0171 review correction: an issue_update resolves the owning Initiative's
// Project at drain time too, so a payload enqueued before the Initiative's
// project_create completed cannot clear a Project that exists by the time the
// update is sent.
func TestLinearIssueUpdateDrainResolvesProjectAtDrainTime(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "updresolve-product", "updresolve-project")
	enableLinearProduct(t, dbPath, "updresolve-product")
	seedLinearInitiativeFixture(t, dbPath, "updresolve-project", "updresolve-initiative", "updresolve-entry", "The resolve narrative.")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(context.Background(), "updresolve-entry", "updresolve-remote", "UR-1", "https://linear.app/example/issue/UR-1", "", "", store.LinearLinkUnpublished); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(context.Background(), "updresolve-entry", "updresolve-remote", "UR-1", "https://linear.app/example/issue/UR-1", "", "", store.LinearLinkPending); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordLinearLink(context.Background(), "updresolve-entry", "updresolve-remote", "UR-1", "https://linear.app/example/issue/UR-1", "", "", store.LinearLinkConfirmed); err != nil {
		t.Fatal(err)
	}
	// The update is enqueued while the Initiative holds no Project link, so
	// its payload snapshot carries none.
	if _, err := s.EnqueueLinearIssueForProduct(context.Background(), "updresolve-product", "updresolve-entry", store.LinearOpIssueUpdate); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// The Initiative's Project completes before the update drains.
	seedStore, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedStore.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_project_links(work_id, remote_project_uuid, name, url, created_at, updated_at) VALUES('updresolve-initiative', 'remote-project-live', 'Initiative title', '', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z'); DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	seedStore.Close()

	var updateBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(text, "issueUpdate") {
			updateBody = text
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"updresolve-remote","identifier":"UR-1","url":"https://linear.app/example/issue/UR-1","updatedAt":"2026-09-23T02:00:00Z"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"updresolve-remote","identifier":"UR-1","url":"https://linear.app/example/issue/UR-1","updatedAt":"2026-09-23T03:00:00Z","state":{"type":"unstarted"},"team":{"id":"updresolve-team"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_updresolve_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"updresolve-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if !strings.Contains(updateBody, `"projectId":"remote-project-live"`) {
		t.Fatalf("issueUpdate body = %q, want the Project resolved at drain time", updateBody)
	}
}

func fixedLinearTestTime() time.Time { return time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC) }

// TestLinearDrainProjectOperations drives CD-0171 D2 end to end: the drain
// creates one Linear Project per Initiative with the Concord-generated UUID
// as ProjectCreateInput.id, records the link, refreshes the entry issues, and
// a later project_update addresses the recorded remote Project.
func TestLinearDrainProjectOperations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "projdrain-product", "projdrain-project")
	enableLinearProduct(t, dbPath, "projdrain-product")

	// Seed the Initiative, its entry, and the entry's confirmed issue link.
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	intent := `{"title":"Initiative title","value_statement":"Initiative value statement","kind":"initiative","priority":0,"urgency":"standard"}`
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, narrative, created_at, updated_at) VALUES('projdrain-initiative', 'initiative', 'Initiative title', 'needed', 0, 'standard', 1, ?, 'The coordination narrative.', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES('projdrain-initiative', 'projdrain-project', 'primary')`); err != nil {
		t.Fatal(err)
	}
	childIntent := `{"title":"Entry title","value_statement":"Entry value","kind":"task","priority":0,"urgency":"standard"}`
	if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES('projdrain-entry', 'task', 'Entry title', 'in_progress', 0, 'standard', 1, ?, '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`, childIntent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES('projdrain-entry', 'projdrain-project', 'primary')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO initiative_entries(initiative_work_id, child_work_id, position, required) VALUES('projdrain-initiative', 'projdrain-entry', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO relations(work_id_from, work_id_to, kind, created_at) VALUES('projdrain-initiative', 'projdrain-entry', 'includes', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, remote_updated_at, content_hash, link_state, created_at, updated_at) VALUES('projdrain-entry', 'entry-issue-uuid-1', 'EX-1', '', '', '', 'confirmed', '2026-09-23T00:00:00Z', '2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Queue the project_create; the Product is already linear_enabled with a
	// declared connection from enableLinearProduct. The entry is optional, so
	// its mandated optional label must be mapped before any enqueue for it
	// succeeds (CD-0171 D5).
	if err := s.UpdateLinearConnection(context.Background(), store.LinearConnectionUpdateRequest{
		EventID: "projdrain-optional-mapping", ResourceID: "drain-conn-projdrain-product", ProductID: "projdrain-product",
		LabelIDs: map[string]string{
			"project:projdrain-project": "label-repo",
			"optional":                  "label-optional",
		},
		ExpectedResourceVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 23, 1, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	entry, err := s.EnqueueLinearProjectForInitiative(context.Background(), "projdrain-product", "projdrain-initiative", store.LinearOpProjectCreate)
	if err != nil {
		t.Fatalf("EnqueueLinearProjectForInitiative() error = %v", err)
	}
	s.Close()

	var projectCreateBodies, projectUpdateIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		switch {
		case strings.Contains(text, "projectCreate"):
			projectCreateBodies = append(projectCreateBodies, text)
			_, _ = w.Write([]byte(`{"data":{"projectCreate":{"success":true,"project":{"id":"remote-project-created","name":"Initiative title","description":"Initiative value statement","content":"The coordination narrative.","url":"https://linear.app/example/project/remote-project-created","updatedAt":"2026-09-23T01:00:00Z"}}}}`))
		case strings.Contains(text, "projectUpdate"):
			projectUpdateIDs = append(projectUpdateIDs, text)
			_, _ = w.Write([]byte(`{"data":{"projectUpdate":{"success":true,"project":{"id":"remote-project-created","name":"Initiative title","description":"Initiative value statement","content":"The revised narrative.","url":"https://linear.app/example/project/remote-project-created","updatedAt":"2026-09-23T02:00:00Z"}}}}`))
		case strings.Contains(text, "issueUpdate"):
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"entry-issue-uuid-1","identifier":"EX-1","url":"https://linear.app/example/issue/EX-1","updatedAt":"2026-09-23T01:30:00Z"}}}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_projdrain_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"projdrain-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if len(projectCreateBodies) != 1 {
		t.Fatalf("projectCreate calls = %d, want 1", len(projectCreateBodies))
	}
	if !strings.Contains(projectCreateBodies[0], `"id":"`+entry.IdempotencyKey+`"`) {
		t.Fatalf("projectCreate body = %q, want the Concord-generated UUID as ProjectCreateInput.id", projectCreateBodies[0])
	}
	if !strings.Contains(projectCreateBodies[0], `"teamIds":["`) || !strings.Contains(projectCreateBodies[0], `"name":"Initiative title"`) || !strings.Contains(projectCreateBodies[0], `"description":"Initiative value statement"`) || !strings.Contains(projectCreateBodies[0], `"content":"The coordination narrative."`) {
		t.Fatalf("projectCreate body = %q, want team, name, value statement, and narrative", projectCreateBodies[0])
	}

	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var linkUUID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT remote_project_uuid FROM linear_project_links WHERE work_id='projdrain-initiative'`).Scan(&linkUUID); err != nil {
		t.Fatalf("the drained create recorded no project link: %v", err)
	}
	if linkUUID != "remote-project-created" {
		t.Fatalf("project link = %q, want remote-project-created", linkUUID)
	}
	// The entry issue refresh is queued with the created Project set.
	var refreshPayload string
	var refreshKind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT op_kind, payload FROM linear_outbox WHERE work_id='projdrain-entry' ORDER BY rowid DESC LIMIT 1`).Scan(&refreshKind, &refreshPayload); err != nil {
		t.Fatalf("the project completion queued no entry refresh: %v", err)
	}
	if refreshKind != store.LinearOpIssueUpdate || !strings.Contains(refreshPayload, `"project_id":"remote-project-created"`) {
		t.Fatalf("entry refresh = %s / %s, want an update carrying the created Project", refreshKind, refreshPayload)
	}
	if _, err := s.EnqueueLinearProjectForInitiative(context.Background(), "projdrain-product", "projdrain-initiative", store.LinearOpProjectUpdate); err != nil {
		t.Fatalf("EnqueueLinearProjectForInitiative(update) error = %v", err)
	}
	s.Close()

	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"projdrain-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("second drain exit=%d stderr=%q", code, errOut.String())
	}
	if len(projectUpdateIDs) != 1 {
		t.Fatalf("projectUpdate calls = %d, want 1", len(projectUpdateIDs))
	}
	if !strings.Contains(projectUpdateIDs[0], `"id":"remote-project-created"`) || !strings.Contains(projectUpdateIDs[0], `"content":"The coordination narrative."`) || !strings.Contains(projectUpdateIDs[0], `"description":"Initiative value statement"`) {
		t.Fatalf("projectUpdate body = %q, want the recorded remote Project, the value statement, and the narrative", projectUpdateIDs[0])
	}
}

// CD-0171 review correction: the project_create can complete between the
// issue_create drain's Project resolution and its completion, where the
// project's entry refresh cannot see the still-unconfirmed issue link. The
// completion compares the sent Project against the link inside its own
// transaction and queues the converging update, so the entry still moves into
// its Initiative's Project.
func TestLinearDrainQueuesEntryUpdateWhenTheProjectLinksMidFlight(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "midflight-product", "midflight-project")
	enableLinearProduct(t, dbPath, "midflight-product")
	seedLinearInitiativeFixture(t, dbPath, "midflight-project", "midflight-initiative", "midflight-entry", "The midflight narrative.")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueLinearIssueForProduct(context.Background(), "midflight-product", "midflight-entry", store.LinearOpIssueCreate); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// The fake Linear server commits the project_create's link through this
	// handle while the issue_create is in flight: after the drain resolved an
	// empty Project, before the completion runs. The store's WAL and busy
	// timeout make the cross-pool write safe.
	linkStore, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer linkStore.Close()
	var issueCreateBodies, updateBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(text, "issueCreate"):
			issueCreateBodies = append(issueCreateBodies, text)
			if _, err := linkStore.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO linear_project_links(work_id, remote_project_uuid, name, url, created_at, updated_at) VALUES('midflight-initiative', 'remote-project-midflight', 'Initiative title', '', '2026-09-23T01:00:00Z', '2026-09-23T01:00:00Z'); DELETE FROM fold_guard`); err != nil {
				t.Errorf("seed the mid-flight project link: %v", err)
			}
			_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"midflight-issue-remote","identifier":"MF-1","url":"https://linear.app/example/issue/MF-1","updatedAt":"2026-09-23T01:00:00Z"}}}}`))
		case strings.Contains(text, "issueUpdate"):
			updateBodies = append(updateBodies, text)
			_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"midflight-issue-remote","identifier":"MF-1","url":"https://linear.app/example/issue/MF-1","updatedAt":"2026-09-23T02:00:00Z"}}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"midflight-issue-remote","identifier":"MF-1","url":"https://linear.app/example/issue/MF-1","updatedAt":"2026-09-23T03:00:00Z","state":{"type":"unstarted"},"team":{"id":"midflight-team"}}}}`))
		}
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_midflight_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"midflight-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("first drain exit=%d stderr=%q", code, errOut.String())
	}
	if len(issueCreateBodies) != 1 || strings.Contains(issueCreateBodies[0], "projectId") {
		t.Fatalf("issueCreate body = %q, want no Project before the link landed", issueCreateBodies)
	}
	s2, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var refreshPayload string
	if err := s2.DatabaseForTesting().QueryRow(`SELECT payload FROM linear_outbox WHERE work_id='midflight-entry' AND op_kind=? AND state=?`, store.LinearOpIssueUpdate, store.LinearOutboxQueued).Scan(&refreshPayload); err != nil {
		t.Fatalf("the completion queued no converging update: %v", err)
	}
	if !strings.Contains(refreshPayload, `"project_id":"remote-project-midflight"`) {
		t.Fatalf("converging update payload = %q, want the mid-flight Project", refreshPayload)
	}

	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"midflight-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("second drain exit=%d stderr=%q", code, errOut.String())
	}
	if len(updateBodies) != 1 || !strings.Contains(updateBodies[0], `"projectId":"remote-project-midflight"`) {
		t.Fatalf("issueUpdate bodies = %v, want the entry moved into the mid-flight Project", updateBodies)
	}
}

// CD-0171 d3 review correction: project_update carries the Initiative's full
// state, so an Initiative with no narrative sends an explicit empty content
// and the stale markdown leaves the Linear Project instead of lingering.
func TestLinearDrainProjectUpdateClearsAnEmptyNarrative(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "narrclear-product", "narrclear-project")
	enableLinearProduct(t, dbPath, "narrclear-product")
	seedLinearInitiativeFixture(t, dbPath, "narrclear-project", "narrclear-initiative", "", "")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedLinearCLIProjectLink(t, dbPath, "narrclear-initiative", "remote-project-narrclear")
	if _, err := s.EnqueueLinearProjectForInitiative(context.Background(), "narrclear-product", "narrclear-initiative", store.LinearOpProjectUpdate); err != nil {
		t.Fatalf("EnqueueLinearProjectForInitiative(update) error = %v", err)
	}
	s.Close()

	var updateBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		updateBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"projectUpdate":{"success":true,"project":{"id":"remote-project-narrclear","name":"Initiative title","description":"Initiative value","content":"","url":"https://linear.app/example/project/remote-project-narrclear","updatedAt":"2026-09-23T02:00:00Z"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_narrclear_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"narrclear-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	for _, want := range []string{`"description":"Initiative value"`, `"content":""`} {
		if !strings.Contains(updateBody, want) {
			t.Fatalf("projectUpdate body = %q, want %q: a cleared narrative must leave no stale markdown", updateBody, want)
		}
	}
}
