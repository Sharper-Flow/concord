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
	confirmCLIWorkLink(t, dbPath, "divergence-work", "remote-linked")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var request struct {
			Variables struct {
				ID string `json:"id"`
			} `json:"variables"`
		}
		_ = json.Unmarshal(body, &request)
		w.Header().Set("Content-Type", "application/json")
		stateID, stateType := "state-in-progress", "started"
		if request.Variables.ID == "remote-linked" {
			stateID, stateType = "state-completed", "completed"
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
	if len(divergence.Divergences) != 1 || divergence.Divergences[0].WorkID != "divergence-work" || divergence.Divergences[0].Expected != "state-needed" || divergence.Divergences[0].Actual != "state-completed" || divergence.Divergences[0].Outcome != "diverged" {
		t.Fatalf("divergence report = %+v", divergence)
	}

	out.Reset()
	errOut.Reset()
	if code := runWithInput([]string{"linear", "unlinked-remote-in-progress"}, strings.NewReader(`{"product_id":"divergence-product","remote_issue_uuid":"remote-unlinked"}`), &out, &errOut); code != 0 {
		t.Fatalf("unlinked report exit=%d stderr=%q", code, errOut.String())
	}
	var unlinked struct {
		Report struct {
			Reported bool `json:"reported"`
			Linked   bool `json:"linked"`
		} `json:"report"`
	}
	if err := json.Unmarshal([]byte(out.String()), &unlinked); err != nil {
		t.Fatal(err)
	}
	if !unlinked.Report.Reported || unlinked.Report.Linked {
		t.Fatalf("unlinked report = %+v", unlinked.Report)
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
