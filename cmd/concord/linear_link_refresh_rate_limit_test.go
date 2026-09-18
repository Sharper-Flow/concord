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

func seedConfirmedLink(t *testing.T, s *store.Store, workID, remoteUUID, humanKey string) {
	t.Helper()
	for _, state := range []string{store.LinearLinkUnpublished, store.LinearLinkPending, store.LinearLinkConfirmed} {
		if err := s.RecordLinearLink(context.Background(), workID, remoteUUID, humanKey, "https://linear.app/example/issue/"+humanKey, "", "", state); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
}

func ageLinkRefresh(t *testing.T, s *store.Store, workID, refreshedAt string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT OR IGNORE INTO fold_guard(active) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE linear_issue_links SET refreshed_at=? WHERE work_id=?`, refreshedAt, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
}

// One drain sweep costs one Linear request per confirmed link. The sweep is
// gated by a refresh interval: a link whose identity was checked inside the
// interval is not checked again, so back-to-back drains stop multiplying the
// request count by the link count.
func TestLinearDrainSkipsLinkRefreshInsideInterval(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "ttl-product", "ttl-project")
	enableLinearProduct(t, dbPath, "ttl-product")
	seedLinearCLIWork(t, dbPath, "ttl-work", "ttl-project", "TTL title")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedConfirmedLink(t, s, "ttl-work", "remote-ttl", "OLD-9")
	s.Close()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-ttl","identifier":"OLD-9","url":"https://linear.app/example/issue/OLD-9","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"team-ttl"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_ttl_test")
	t.Setenv(dbOverrideEnv, dbPath)

	drain := func() []struct {
		WorkID  string `json:"work_id"`
		Outcome string `json:"outcome"`
	} {
		t.Helper()
		var out, errOut strings.Builder
		if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"ttl-product"}`), &out, &errOut); code != 0 {
			t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
		}
		var result struct {
			LinkRefreshes []struct {
				WorkID  string `json:"work_id"`
				Outcome string `json:"outcome"`
			} `json:"link_refreshes"`
		}
		if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
			t.Fatal(err)
		}
		return result.LinkRefreshes
	}

	if first := drain(); len(first) != 1 || first[0].Outcome != "unchanged" {
		t.Fatalf("first drain refreshes = %+v", first)
	}
	if second := drain(); len(second) != 0 {
		t.Fatalf("drain inside the refresh interval re-checked the link: %+v", second)
	}

	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ageLinkRefresh(t, s, "ttl-work", "2020-01-01T00:00:00Z")
	if third := drain(); len(third) != 1 || third[0].Outcome != "unchanged" {
		t.Fatalf("drain after the refresh interval did not re-check the link: %+v", third)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("Linear requests = %d, want 2 (one per due refresh)", got)
	}
}

// When Linear defers a request, every later request in the same sweep is
// deferred too. The sweep stops at the first rate-limit refusal and reports
// the links it never attempted as skipped instead of burning more quota.
func TestLinearDrainStopsLinkRefreshSweepAtRateLimit(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "rate-product", "rate-project")
	enableLinearProduct(t, dbPath, "rate-product")
	seedLinearCLIWork(t, dbPath, "rate-a", "rate-project", "Rate A")
	seedLinearCLIWork(t, dbPath, "rate-b", "rate-project", "Rate B")
	seedLinearCLIWork(t, dbPath, "rate-c", "rate-project", "Rate C")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seedConfirmedLink(t, s, "rate-a", "remote-a", "OLD-A")
	seedConfirmedLink(t, s, "rate-b", "remote-b", "OLD-B")
	seedConfirmedLink(t, s, "rate-c", "remote-c", "OLD-C")
	s.Close()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "remote-a") {
			_, _ = w.Write([]byte(`{"errors":[{"message":"You have exceeded your request quota. RATELIMITED"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-b","identifier":"NEW-B","url":"https://linear.app/example/issue/NEW-B","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"team-rate"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_rate_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"rate-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	var result struct {
		OK            bool `json:"ok"`
		LinkRefreshes []struct {
			WorkID  string `json:"work_id"`
			Outcome string `json:"outcome"`
			Detail  string `json:"detail"`
		} `json:"link_refreshes"`
	}
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("drain reported failure: %+v", result)
	}
	byWork := map[string]string{}
	for _, refresh := range result.LinkRefreshes {
		byWork[refresh.WorkID] = refresh.Outcome
	}
	if byWork["rate-a"] != "failed" || byWork["rate-b"] != "skipped" || byWork["rate-c"] != "skipped" {
		t.Fatalf("refresh outcomes = %+v", result.LinkRefreshes)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("Linear requests = %d, want 1 (sweep stops at the first rate-limit refusal)", got)
	}
}
