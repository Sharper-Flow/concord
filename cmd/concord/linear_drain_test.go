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

func enableLinearProduct(t *testing.T, dbPath, productID string) {
	t.Helper()
	runOperatorJSON(t, dbPath, []string{"product-mode-set"}, map[string]any{
		"product_id": productID, "planning_mode": "linear_enabled",
		"reason": "CD-0121 activation for the drain test", "expected_version": 2,
	})
	runOperatorJSON(t, dbPath, []string{"resource-create"}, map[string]any{
		"event_id": "drain-conn-" + productID, "resource_id": "drain-conn-" + productID, "product_id": productID,
		"display_name": "Linear connection", "class": "saas", "kind": "saas_account", "purpose": "Linear planning connection",
		"stage_maturity": "prototype", "stage_audience_commitment": "operator_only", "environments": []string{"production"},
		"metadata_schema_version":  "linear-connection-v1",
		"metadata":                 map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "68d52710-76d9-4b41-ba45-778511d0e2ed", "project_id": "project-uuid-1", "auth_mode": "personal_api_key", "status_ids": map[string]string{"cancelled": "state-cancelled", "completed": "state-completed", "superseded": "state-superseded"}}},
		"expected_product_version": 3,
	})
}

func TestLinearEnqueueAndDrainCLI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "drain-product", "drain-product-project")
	enableLinearProduct(t, dbPath, "drain-product")
	seedLinearCLIWork(t, dbPath, "drain-work", "drain-product-project", "Drain title")

	var sawAuth, sawProject bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") == "lin_api_cli_test" {
			sawAuth = true
		}
		sawProject = strings.Contains(string(body), `"projectId":"project-uuid-1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed","identifier":"SHA-1","url":"https://linear.app/example/issue/SHA-1","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
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
	if !sawAuth || !sawProject {
		t.Fatalf("the drain request lacked authorization or project routing: auth=%t project=%t", sawAuth, sawProject)
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

	var sawStatus bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawStatus = strings.Contains(string(body), `"stateId":"state-cancelled"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issueUpdate":{"success":true,"issue":{"id":"remote-update","identifier":"SHA-3","url":"https://linear.app/example/issue/SHA-3","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
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
	if !sawStatus {
		t.Fatal("drain did not send the declared terminal status")
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

func TestLinearDrainRefusesQueuedOperationAfterConnectionChange(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "stale-product", "stale-project")
	enableLinearProduct(t, dbPath, "stale-product")
	seedLinearCLIWork(t, dbPath, "stale-work", "stale-project", "Stale title")
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": "stale-product", "work_id": "stale-work", "op_kind": "issue_create"})
	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "stale-connection-update", "resource_id": "drain-conn-stale-product", "product_id": "stale-product",
		"team_id": "new-team", "project_id": "new-project", "status_ids": map[string]string{"cancelled": "new-cancelled", "completed": "new-completed", "superseded": "new-superseded"}, "expected_resource_version": 1,
	})

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
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
		"team_id": "new-team", "project_id": "new-project", "status_ids": map[string]string{"cancelled": "new-cancelled", "completed": "new-completed", "superseded": "new-superseded"}, "expected_resource_version": 1,
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
				TeamID: "refresh-team", ProjectID: "refresh-new-project", StatusIDs: map[string]string{"cancelled": "refresh-cancelled", "completed": "refresh-completed", "superseded": "refresh-superseded"},
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

func fixedLinearTestTime() time.Time { return time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC) }
