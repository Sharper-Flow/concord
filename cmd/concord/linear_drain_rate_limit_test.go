package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sharper-flow/concord/internal/linearclient"
	"github.com/sharper-flow/concord/internal/store"
)

// seedRateLimitWorksWithLabels seeds a Product whose connection maps the task
// and repository labels, plus one queued issue_create per work id, so a drain
// claim order follows the given order and a successful create converges
// without queueing a follow-up update.
func seedRateLimitWorksWithLabels(t *testing.T, dbPath, productID, projectID string, workIDs ...string) {
	t.Helper()
	seedCLIProduct(t, dbPath, productID, projectID)
	enableLinearProduct(t, dbPath, productID)
	runOperatorJSON(t, dbPath, []string{"linear-connection-update"}, map[string]any{
		"event_id": "rate-label-update-" + productID, "resource_id": "drain-conn-" + productID, "product_id": productID,
		"label_ids": map[string]string{"task": "label-task", "project:" + projectID: "label-repo"}, "expected_resource_version": 1,
	})
	for _, workID := range workIDs {
		seedLinearCLIWork(t, dbPath, workID, projectID, "Rate limit title "+workID)
		runOperatorJSON(t, dbPath, []string{"linear-issue-enqueue"}, map[string]any{"product_id": productID, "work_id": workID, "op_kind": "issue_create"})
	}
}

func readOutboxRow(t *testing.T, dbPath, workID string) (state string, attempts int, lastError string) {
	t.Helper()
	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.DatabaseForTesting().QueryRow(`SELECT state, attempts, coalesce(last_error, '') FROM linear_outbox WHERE work_id=?`, workID).Scan(&state, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	return state, attempts, lastError
}

// A Linear rate limit defers the pass; it must never move an operation toward
// failed. Every pass answers 429: after more passes than linearMaxAttempts,
// all operations are still queued, no attempt stayed spent, and the deferred
// operation records the refusal as its last_error.
func TestLinearDrainRateLimitNeverAdvancesAttempts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedRateLimitWorksWithLabels(t, dbPath, "limit-product", "limit-project", "limit-work-a", "limit-work-b", "limit-work-c")

	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "issueCreate") {
			sends.Add(1)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour. RATELIMITED"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"limit-remote","identifier":"RL-1","url":"https://linear.app/example/issue/RL-1","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"limit-team"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_limit_test")
	t.Setenv(dbOverrideEnv, dbPath)

	const passes = 6
	for pass := 0; pass < passes; pass++ {
		var out, errOut strings.Builder
		if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"limit-product"}`), &out, &errOut); code != 0 {
			t.Fatalf("pass %d exit=%d stderr=%q", pass, code, errOut.String())
		}
	}
	if got := sends.Load(); got != passes {
		t.Fatalf("issueCreate sends = %d, want %d (one deferred send per pass, no further sends after it)", got, passes)
	}
	state, attempts, lastError := readOutboxRow(t, dbPath, "limit-work-a")
	if state != store.LinearOutboxQueued || attempts != 0 {
		t.Fatalf("deferred operation = %s/%d, want queued with no attempt spent", state, attempts)
	}
	if !strings.Contains(lastError, "rate_limited") {
		t.Fatalf("deferred operation last_error = %q, want the rate-limit refusal", lastError)
	}
	for _, workID := range []string{"limit-work-b", "limit-work-c"} {
		state, attempts, lastError = readOutboxRow(t, dbPath, workID)
		if state != store.LinearOutboxQueued || attempts != 0 || lastError != "" {
			t.Fatalf("unsent operation %s = %s/%d/%q, want queued, no attempt spent, no failure recorded", workID, state, attempts, lastError)
		}
	}
}

// The pass stops at the first rate_limited send: the operation after the
// refusal is never sent, and the link-refresh sweep — one request per stale
// link — waits for a later drain.
func TestLinearDrainRateLimitStopsPassAndSkipsRefresh(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedRateLimitWorksWithLabels(t, dbPath, "stop-product", "stop-project", "stop-work-a", "stop-work-b", "stop-work-c")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedLinearCLIWork(t, dbPath, "stop-refresh-work", "stop-project", "Refresh link title")
	seedConfirmedLink(t, s, "stop-refresh-work", "remote-stop-refresh", "OLD-R")
	s.Close()
	seedStore, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ageLinkRefresh(t, seedStore, "stop-refresh-work", "2020-01-01T00:00:00Z")
	seedStore.Close()

	var createsTotal, createsLimited, sweepQueries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		text := string(body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(text, "issueCreate"):
			createsTotal.Add(1)
			if createsTotal.Load() == 1 {
				_, _ = w.Write([]byte(`{"data":{"issueCreate":{"success":true,"issue":{"id":"stop-remote-a","identifier":"RS-1","url":"https://linear.app/example/issue/RS-1","updatedAt":"2026-09-09T12:00:00Z"}}}}`))
				return
			}
			createsLimited.Add(1)
			_, _ = w.Write([]byte(`{"errors":[{"message":"Rate limit exceeded. Only 2500 requests are allowed per 1 hour. RATELIMITED"}]}`))
		case strings.Contains(text, "remote-stop-refresh"):
			sweepQueries.Add(1)
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-stop-refresh","identifier":"OLD-R","url":"https://linear.app/example/issue/OLD-R","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"stop-team"}}}}`))
		default:
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"stop-remote","identifier":"RS-9","url":"https://linear.app/example/issue/RS-9","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"stop-team"}}}}`))
		}
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_stop_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"stop-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	if got := createsTotal.Load(); got != 2 {
		t.Fatalf("issueCreate sends = %d, want 2 (one success, then the refusal stops the pass)", got)
	}
	if got := createsLimited.Load(); got != 1 {
		t.Fatalf("rate-limited creates = %d, want 1 (the pass stops at the first refusal)", got)
	}
	if got := sweepQueries.Load(); got != 0 {
		t.Fatalf("link-refresh queries = %d, want 0 (the limited pass skips the sweep)", got)
	}
	var result struct {
		OK            bool `json:"ok"`
		LinkRefreshes []struct {
			WorkID string `json:"work_id"`
		} `json:"link_refreshes"`
		Operations []struct {
			OperationID string `json:"operation_id"`
			Outcome     string `json:"outcome"`
		} `json:"operations"`
	}
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatalf("drain output %q: %v", out.String(), err)
	}
	if !result.OK || len(result.LinkRefreshes) != 0 {
		t.Fatalf("drain result ok=%t link_refreshes=%+v, want ok and a skipped sweep", result.OK, result.LinkRefreshes)
	}
	if len(result.Operations) != 3 {
		t.Fatalf("operations = %+v, want one entry per claim", result.Operations)
	}
	if result.Operations[0].Outcome != "done" || result.Operations[1].Outcome != "rate_limited" || result.Operations[2].Outcome != "queued" {
		t.Fatalf("operation outcomes = %+v, want done, rate_limited, queued", result.Operations)
	}
	state, attempts, lastError := readOutboxRow(t, dbPath, "stop-work-b")
	if state != store.LinearOutboxQueued || attempts != 0 || !strings.Contains(lastError, "rate_limited") {
		t.Fatalf("deferred operation = %s/%d/%q, want queued, no attempt spent, rate-limit last_error", state, attempts, lastError)
	}
	state, attempts, lastError = readOutboxRow(t, dbPath, "stop-work-c")
	if state != store.LinearOutboxQueued || attempts != 0 || lastError != "" {
		t.Fatalf("unsent operation = %s/%d/%q, want queued, no attempt spent, no failure recorded", state, attempts, lastError)
	}
}

// Transport and malformed-response failures are retryable with the attempt
// the claim spent: the operation returns to queued with attempts advanced, so
// only a rate limit releases attempts.
func TestLinearDrainTransportFailureStillSpendsAttempt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedRateLimitWorksWithLabels(t, dbPath, "transport-product", "transport-project", "transport-work-a", "transport-work-b")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := server.URL
	server.Close()
	t.Setenv(linearclient.EnvEndpoint, endpoint)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_transport_test")
	t.Setenv(dbOverrideEnv, dbPath)

	for pass, wantAttempts := range []int{1, 2} {
		var out, errOut strings.Builder
		if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"transport-product"}`), &out, &errOut); code != 0 {
			t.Fatalf("pass %d exit=%d stderr=%q", pass, code, errOut.String())
		}
		for _, workID := range []string{"transport-work-a", "transport-work-b"} {
			state, attempts, lastError := readOutboxRow(t, dbPath, workID)
			if state != store.LinearOutboxQueued || attempts != wantAttempts {
				t.Fatalf("pass %d operation %s = %s/%d, want queued with the attempt spent (%d)", pass, workID, state, attempts, wantAttempts)
			}
			if !strings.Contains(lastError, "request did not complete") {
				t.Fatalf("pass %d operation %s last_error = %q, want the transport refusal", pass, workID, lastError)
			}
		}
	}
}
