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

func confirmCLIWorkLink(t *testing.T, dbPath, workID, remoteID string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, state := range []string{store.LinearLinkUnpublished, store.LinearLinkPending, store.LinearLinkConfirmed} {
		if err := s.RecordLinearLink(ctx, workID, remoteID, "CON-1", "https://linear.app/example/issue/CON-1", "", "", state); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLinearDivergenceAndUnlinkedInProgressRoutes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "divergence-product", "divergence-project")
	enableLinearProduct(t, dbPath, "divergence-product")
	seedLinearCLIWork(t, dbPath, "divergence-work", "divergence-project", "Divergence title")
	seedLinearCLIWork(t, dbPath, "divergence-work-2", "divergence-project", "Second title")
	seedLinearCLIWork(t, dbPath, "divergence-work-3", "divergence-project", "Third title")
	confirmCLIWorkLink(t, dbPath, "divergence-work", "remote-linked")
	confirmCLIWorkLink(t, dbPath, "divergence-work-2", "remote-linked-started")
	confirmCLIWorkLink(t, dbPath, "divergence-work-3", "remote-matched")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "issues(") {
			_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[` +
				`{"id":"remote-unlinked","identifier":"CON-22","url":"https://linear.app/example/issue/CON-22","title":"Pre-cutover import","updatedAt":"2026-09-16T00:00:00Z","state":{"id":"state-in-progress","type":"started"}},` +
				`{"id":"remote-linked-started","identifier":"CON-26","url":"https://linear.app/example/issue/CON-26","title":"Adopted card","updatedAt":"2026-09-16T00:00:00Z","state":{"id":"state-in-progress","type":"started"}}` +
				`],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}`))
			return
		}
		var request struct {
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		_ = json.Unmarshal(body, &request)
		stateID, stateType := "state-in-progress", "started"
		switch request.Variables.ID {
		case "remote-linked":
			stateID, stateType = "state-completed", "completed"
		case "remote-matched":
			stateID, stateType = "state-needed", "unstarted"
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"` + request.Variables.ID + `","identifier":"CON-1","url":"https://linear.app/example/issue/CON-1","updatedAt":"2026-09-16T00:00:00Z","state":{"id":"` + stateID + `","type":"` + stateType + `"},"team":{"id":"team-uuid-1"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_divergence_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "divergence"}, strings.NewReader(`{"product_id":"divergence-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("divergence exit=%d stderr=%q", code, errOut.String())
	}
	var divergence struct {
		Divergences []struct {
			WorkID   string `json:"work_id"`
			Expected string `json:"expected_status_id"`
			Actual   string `json:"actual_status_id"`
			Outcome  string `json:"outcome"`
		} `json:"divergences"`
	}
	if err := json.Unmarshal([]byte(out.String()), &divergence); err != nil {
		t.Fatal(err)
	}
	if len(divergence.Divergences) != 2 ||
		divergence.Divergences[0].WorkID != "divergence-work" || divergence.Divergences[0].Expected != "state-needed" || divergence.Divergences[0].Actual != "state-completed" || divergence.Divergences[0].Outcome != "diverged" ||
		divergence.Divergences[1].WorkID != "divergence-work-2" || divergence.Divergences[1].Actual != "state-in-progress" {
		t.Fatalf("divergence report = %+v", divergence)
	}

	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "unlinked-remote-in-progress"}, strings.NewReader(`{"product_id":"divergence-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("unlinked sweep exit=%d stderr=%q", code, errOut.String())
	}
	var sweep struct {
		OK       bool   `json:"ok"`
		TeamID   string `json:"team_id"`
		Checked  int    `json:"checked"`
		Unlinked []struct {
			RemoteIssueUUID string `json:"remote_issue_uuid"`
			HumanKey        string `json:"human_key"`
			StateType       string `json:"state_type"`
		} `json:"unlinked"`
	}
	if err := json.Unmarshal([]byte(out.String()), &sweep); err != nil {
		t.Fatal(err)
	}
	if !sweep.OK || sweep.TeamID != "68d52710-76d9-4b41-ba45-778511d0e2ed" || sweep.Checked != 2 {
		t.Fatalf("unlinked sweep = %+v", sweep)
	}
	// The enumerated issue holding a confirmed link row stays out of the
	// report; only the pre-cutover import with no link row is reported.
	if len(sweep.Unlinked) != 1 || sweep.Unlinked[0].RemoteIssueUUID != "remote-unlinked" || sweep.Unlinked[0].HumanKey != "CON-22" || sweep.Unlinked[0].StateType != "started" {
		t.Fatalf("unlinked list = %+v", sweep.Unlinked)
	}
}

func TestLinearOutboxDispositionCLI(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "disposition-cli-product", "disposition-cli-project")
	enableLinearProduct(t, dbPath, "disposition-cli-product")
	seedLinearCLIWork(t, dbPath, "disposition-cli-work", "disposition-cli-project", "Disposition title")

	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.EnqueueLinearIssueForWork(ctx, "disposition-cli-work", store.LinearOpIssueCreate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimLinearOperations(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.FailLinearOperation(ctx, op.OperationID, "permanent", "credential rejected"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-disposition"}, strings.NewReader(`{"product_id":"disposition-cli-product","reason":"nope","disposition":"retry"}`), &out, &errOut); code == 0 {
		t.Fatalf("a non-acknowledged disposition must exit non-zero; stdout=%q", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "outbox-disposition"}, strings.NewReader(`{"product_id":"disposition-cli-product","reason":"operator accepted the historical failure"}`), &out, &errOut); code != 0 {
		t.Fatalf("disposition exit=%d stderr=%q", code, errOut.String())
	}
	var disposed struct {
		OK         bool `json:"ok"`
		Count      int  `json:"count"`
		Operations []struct {
			OperationID string `json:"operation_id"`
			Disposition string `json:"disposition"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &disposed); err != nil {
		t.Fatal(err)
	}
	if !disposed.OK || disposed.Count != 1 || len(disposed.Operations) != 1 || disposed.Operations[0].OperationID != op.OperationID || disposed.Operations[0].Disposition != "acknowledged" {
		t.Fatalf("disposition report = %+v", disposed)
	}

	s, err = store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var state string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT state FROM linear_outbox WHERE operation_id=?`, op.OperationID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != store.LinearOutboxFailed {
		t.Fatalf("outbox state = %s, want failed", state)
	}
	remaining, err := s.AcknowledgeFailedLinearOperations(ctx, "disposition-cli-product", nil, "operator accepted the historical failure")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("undisposed failed rows remain: %+v", remaining)
	}
}
