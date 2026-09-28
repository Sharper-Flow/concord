package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

// seedEnvelopeLinks bulk-seeds count work items in the Product's project and
// confirms one Linear link per work item, so the divergence verb sees a
// Product at the scale where the envelope used to overflow.
func seedEnvelopeLinks(t *testing.T, dbPath, projectID string, count int, workID func(i int) string, remoteID func(i int) string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		id := workID(i)
		title := "Envelope work " + id
		intent, _ := json.Marshal(map[string]any{"title": title, "value_statement": "Envelope scale value statement", "kind": "task", "priority": 0, "urgency": "standard"})
		if _, err := tx.Exec(`INSERT INTO work_items(id, kind, title, lifecycle, priority, urgency, version, intent_json, created_at, updated_at) VALUES(?, 'task', ?, 'needed', 0, 'standard', 1, ?, '2026-09-09T00:00:00Z', '2026-09-09T00:00:00Z')`, id, title, string(intent)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO work_projects(work_id, project_id, role) VALUES(?, ?, 'primary')`, id, projectID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		remote := remoteID(i)
		for _, state := range []string{store.LinearLinkUnpublished, store.LinearLinkPending, store.LinearLinkConfirmed} {
			if err := s.RecordLinearLink(ctx, workID(i), remote, "ENV-1", "https://linear.app/example/issue/ENV-1", "", "", state); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// runEnvelopeDivergenceScenario serves every batched id read with stateID and
// runs the divergence verb over a seeded Product. It reports the batch
// requests the client made.
func runEnvelopeDivergenceScenario(t *testing.T, dbPath, productID string, count int, stateID, stateType string) (string, int, []int) {
	t.Helper()
	var requests atomic.Int64
	var batchSizes atomic.Value
	batchSizes.Store([]int{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(string(body), "id: { in:") {
			_, _ = w.Write([]byte(`{"errors":[{"message":"unexpected query"}]}`))
			return
		}
		var request struct {
			Variables struct {
				IDs []string `json:"ids"`
			} `json:"variables"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("batch request is not JSON: %v", err)
		}
		requests.Add(1)
		current := batchSizes.Load().([]int)
		current = append(current, len(request.Variables.IDs))
		batchSizes.Store(current)
		nodes := make([]string, 0, len(request.Variables.IDs))
		for _, id := range request.Variables.IDs {
			nodes = append(nodes, `{"id":"`+id+`","identifier":"ENV-1","url":"https://linear.app/example/issue/ENV-1","updatedAt":"2026-09-16T00:00:00Z","state":{"id":"`+stateID+`","type":"`+stateType+`"},"team":{"id":"team-uuid-1"}}`)
		}
		_, _ = w.Write([]byte(`{"data":{"issues":{"nodes":[` + strings.Join(nodes, ",") + `]}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_envelope_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "divergence"}, strings.NewReader(`{"product_id":"`+productID+`"}`), &out, &errOut); code != 0 {
		t.Fatalf("divergence exit=%d stderr=%q", code, errOut.String())
	}
	return out.String(), int(requests.Load()), batchSizes.Load().([]int)
}

// TestLinearDivergenceEnvelopeAt500Links proves a 500-link Product answers
// inside the agent envelope whether every link matches or every link
// diverges, with matched rows omitted from the listing and non-matched rows
// capped at linearDivergenceRowCap.
func TestLinearDivergenceEnvelopeAt500Links(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "envelope-product", "envelope-project")
	enableLinearProduct(t, dbPath, "envelope-product")
	seedEnvelopeLinks(t, dbPath, "envelope-project", 500,
		func(i int) string { return fmt.Sprintf("envelope-work-%d", i) },
		func(i int) string { return fmt.Sprintf("envelope-remote-%d", i) })

	// Every issue sits in the mapped state for lifecycle needed, so all 500
	// links match and none may be listed.
	raw, requests, _ := runEnvelopeDivergenceScenario(t, dbPath, "envelope-product", 500, "state-needed", "unstarted")
	if len(raw) > agent.MaxEnvelopeBytes {
		t.Fatalf("all-matched output = %d bytes, exceeds %d", len(raw), agent.MaxEnvelopeBytes)
	}
	if requests > 10 {
		t.Fatalf("all-matched run issued %d Linear requests, budget is 10", requests)
	}
	var report struct {
		Checked     int           `json:"checked"`
		Matched     int           `json:"matched"`
		Diverged    int           `json:"diverged"`
		Unmapped    int           `json:"unmapped"`
		Failed      int           `json:"failed"`
		Divergences []interface{} `json:"divergences"`
		Omitted     int           `json:"omitted"`
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	if report.Checked != 500 || report.Matched != 500 || report.Diverged != 0 || report.Unmapped != 0 || report.Failed != 0 || len(report.Divergences) != 0 || report.Omitted != 0 {
		t.Fatalf("all-matched report = %+v", report)
	}

	// Every issue sits in a foreign state, so all 500 links diverge. The
	// counts stay complete while the listing caps at 100 rows and reports the
	// omitted remainder, and the whole answer stays inside the envelope.
	raw, requests, divergedBatches := runEnvelopeDivergenceScenario(t, dbPath, "envelope-product", 500, "state-completed", "completed")
	if len(raw) > agent.MaxEnvelopeBytes {
		t.Fatalf("all-diverged output = %d bytes, exceeds %d", len(raw), agent.MaxEnvelopeBytes)
	}
	if requests > 10 {
		t.Fatalf("all-diverged run issued %d Linear requests, budget is 10", requests)
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	if report.Checked != 500 || report.Matched != 0 || report.Diverged != 500 || report.Unmapped != 0 || report.Failed != 0 {
		t.Fatalf("all-diverged report = %+v", report)
	}
	if len(report.Divergences) != linearDivergenceRowCap || report.Omitted != 400 {
		t.Fatalf("all-diverged listing = %d rows, omitted = %d", len(report.Divergences), report.Omitted)
	}
	if len(divergedBatches) != 10 {
		t.Fatalf("all-diverged batch sizes = %v, want 10 batches of 50", divergedBatches)
	}
}

// TestLinearDivergenceBatchedIssueReads proves the issue states ride batches
// of at most 50 ids through Linear's issues(filter: { id: { in: $ids } })
// query: 120 mapped links answer in exactly three requests that together
// cover every linked uuid.
func TestLinearDivergenceBatchedIssueReads(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "batch-product", "batch-project")
	enableLinearProduct(t, dbPath, "batch-product")
	const count = 120
	seedEnvelopeLinks(t, dbPath, "batch-project", count,
		func(i int) string { return fmt.Sprintf("batch-work-%d", i) },
		func(i int) string { return fmt.Sprintf("batch-remote-%d", i) })

	raw, requests, batches := runEnvelopeDivergenceScenario(t, dbPath, "batch-product", count, "state-completed", "completed")
	if requests != 3 {
		t.Fatalf("batched reads issued %d requests, want 3 for %d links", requests, count)
	}
	covered := 0
	for _, size := range batches {
		if size > 50 {
			t.Fatalf("batch holds %d ids, cap is 50", size)
		}
		covered += size
	}
	if covered != count {
		t.Fatalf("batches %v cover %d ids, want %d", batches, covered, count)
	}
	var report struct {
		Checked  int `json:"checked"`
		Diverged int `json:"diverged"`
		Failed   int `json:"failed"`
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatal(err)
	}
	if report.Checked != count || report.Diverged != count || report.Failed != 0 {
		t.Fatalf("batched report = %+v", report)
	}
}
