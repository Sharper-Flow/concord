package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

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
		"metadata":                 map[string]any{"linear": map[string]any{"workspace_url": "https://linear.app/example", "team_id": "68d52710-76d9-4b41-ba45-778511d0e2ed", "auth_mode": "personal_api_key"}},
		"expected_product_version": 3,
	})
}

func TestLinearEnqueueAndDrainCLI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "drain-product", "drain-product-project")
	enableLinearProduct(t, dbPath, "drain-product")
	seedLinearCLIWork(t, dbPath, "drain-work", "drain-product-project", "Drain title")

	var sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "lin_api_cli_test" {
			sawAuth = true
		}
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
	if !sawAuth {
		t.Fatal("the drain never reached the remote endpoint")
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
