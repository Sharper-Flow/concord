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

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

// The adoption drain resolves the named existing issue, verifies its team, and
// completes a confirmed link — without creating a duplicate issue.
func TestLinearAdoptionEnqueueAndDrainCLI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "adopt-product", "adopt-product-project")
	enableLinearProduct(t, dbPath, "adopt-product")
	seedLinearCLIWork(t, dbPath, "adopt-cli-work", "adopt-product-project", "Adopt CLI title")

	var sawIssueQuery bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawIssueQuery = strings.Contains(string(body), `"query":"query($id: String!) { issue(id: $id) { id identifier url updatedAt state { type } team { id } } }"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"cccccccc-0000-0000-0000-000000000003","identifier":"EX-3","url":"https://linear.app/example/issue/EX-3","updatedAt":"2026-09-16T00:00:00Z","team":{"id":"68d52710-76d9-4b41-ba45-778511d0e2ed"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_cli_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{
		"product_id": "adopt-product", "work_id": "adopt-cli-work", "op_kind": "issue_adopt",
		"remote_issue_uuid": "cccccccc-0000-0000-0000-000000000003",
	})
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"adopt-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if !sawIssueQuery {
		t.Fatal("the adoption drain never queried the named issue")
	}
	var drained struct {
		OK         bool `json:"ok"`
		Operations []struct {
			OperationID string `json:"operation_id"`
			Outcome     string `json:"outcome"`
			Identifier  string `json:"identifier"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &drained); err != nil {
		t.Fatalf("drain output %q: %v", out.String(), err)
	}
	if !drained.OK || len(drained.Operations) != 1 || drained.Operations[0].Outcome != "done" || drained.Operations[0].Identifier != "EX-3" {
		t.Fatalf("drain result = %+v", drained)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var outboxState, linkState, remoteUUID, contentHash string
	if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM linear_outbox WHERE operation_id=?`, drained.Operations[0].OperationID).Scan(&outboxState); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT link_state, remote_issue_uuid, content_hash FROM linear_issue_links WHERE work_id='adopt-cli-work'`).Scan(&linkState, &remoteUUID, &contentHash); err != nil {
		t.Fatal(err)
	}
	if outboxState != "done" || linkState != "confirmed" || remoteUUID != "cccccccc-0000-0000-0000-000000000003" || contentHash != "" {
		t.Fatalf("terminal = %s/%s/%s/%q, want done/confirmed/remote uuid/empty hash", outboxState, linkState, remoteUUID, contentHash)
	}
}

// An adoption whose named issue belongs to another team lands in failed with a
// permanent refusal, and the link stays absent.
func TestLinearAdoptionDrainRefusesForeignTeam(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "team-product", "team-product-project")
	enableLinearProduct(t, dbPath, "team-product")
	seedLinearCLIWork(t, dbPath, "team-work", "team-product-project", "Team title")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"dddddddd-0000-0000-0000-000000000004","identifier":"OTHER-9","url":"https://linear.app/other/issue/OTHER-9","updatedAt":"2026-09-16T00:00:00Z","team":{"id":"another-team-uuid"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_cli_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{
		"product_id": "team-product", "work_id": "team-work", "op_kind": "issue_adopt",
		"remote_issue_uuid": "dddddddd-0000-0000-0000-000000000004",
	})
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"team-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	var drained struct {
		Operations []struct {
			Outcome string `json:"outcome"`
			Detail  string `json:"detail"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &drained); err != nil {
		t.Fatalf("drain output %q: %v", out.String(), err)
	}
	if len(drained.Operations) != 1 || drained.Operations[0].Outcome != "permanent" || !strings.Contains(drained.Operations[0].Detail, "not the Product's configured team") {
		t.Fatalf("drain result = %+v", drained)
	}
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var links int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM linear_issue_links`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("a refused adoption wrote %d link rows, want 0", links)
	}
}
