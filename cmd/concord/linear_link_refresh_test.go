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

func TestLinearDrainRefreshesConfirmedLinksWithoutOperations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "refresh-product", "refresh-project")
	enableLinearProduct(t, dbPath, "refresh-product")
	seedLinearCLIWork(t, dbPath, "refresh-work", "refresh-project", "Refresh title")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{store.LinearLinkUnpublished, store.LinearLinkPending} {
		if err := s.RecordLinearLink(context.Background(), "refresh-work", "remote-refresh", "OLD-1", "https://linear.app/example/issue/OLD-1", "2026-09-09T01:00:00Z", "", state); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	if err := s.RecordLinearLink(context.Background(), "refresh-work", "remote-refresh", "OLD-1", "https://linear.app/example/issue/OLD-1", "2026-09-09T01:00:00Z", "sha256:"+strings.Repeat("a", 64), store.LinearLinkConfirmed); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-refresh","identifier":"NEW-42","url":"https://linear.app/example/issue/NEW-42","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"team-refresh"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_refresh_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"refresh-product"}`), &out, &errOut); code != 0 {
		t.Fatalf("drain exit=%d stderr=%q", code, errOut.String())
	}
	var result struct {
		OK            bool `json:"ok"`
		Drained       int  `json:"drained"`
		LinkRefreshes []struct {
			WorkID  string `json:"work_id"`
			Outcome string `json:"outcome"`
		} `json:"link_refreshes"`
	}
	if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Drained != 0 || len(result.LinkRefreshes) != 1 || result.LinkRefreshes[0].WorkID != "refresh-work" || result.LinkRefreshes[0].Outcome != "updated" {
		t.Fatalf("drain result = %+v", result)
	}

	s, err = store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var humanKey, url string
	if err := s.DatabaseForTesting().QueryRow(`SELECT human_key, url FROM linear_issue_links WHERE work_id='refresh-work'`).Scan(&humanKey, &url); err != nil {
		t.Fatal(err)
	}
	if humanKey != "NEW-42" || url != "https://linear.app/example/issue/NEW-42" {
		t.Fatalf("link identity = %s/%s", humanKey, url)
	}
}

func TestLinearDrainReportsLinkRefreshFailureAndContinues(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concord.db")
	seedCLIProduct(t, dbPath, "refresh-failure-product", "refresh-failure-project")
	enableLinearProduct(t, dbPath, "refresh-failure-product")
	seedLinearCLIWork(t, dbPath, "refresh-failure-work", "refresh-failure-project", "Refresh failure")
	seedLinearCLIWork(t, dbPath, "refresh-mismatch-work", "refresh-failure-project", "Refresh mismatch")
	seedLinearCLIWork(t, dbPath, "refresh-success-work", "refresh-failure-project", "Refresh success")

	s, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for workID, remoteUUID := range map[string]string{"refresh-failure-work": "remote-failure", "refresh-mismatch-work": "remote-mismatch", "refresh-success-work": "remote-success"} {
		if err := s.RecordLinearLink(context.Background(), workID, remoteUUID, "OLD-1", "https://linear.app/example/issue/OLD-1", "", "", store.LinearLinkUnpublished); err != nil {
			s.Close()
			t.Fatal(err)
		}
		if err := s.RecordLinearLink(context.Background(), workID, remoteUUID, "OLD-1", "https://linear.app/example/issue/OLD-1", "", "", store.LinearLinkPending); err != nil {
			s.Close()
			t.Fatal(err)
		}
		if err := s.RecordLinearLink(context.Background(), workID, remoteUUID, "OLD-1", "https://linear.app/example/issue/OLD-1", "", "", store.LinearLinkConfirmed); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	s.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), "remote-failure") {
			_, _ = w.Write([]byte(`{"errors":[{"message":"issue is unavailable"}]}`))
			return
		}
		if strings.Contains(string(body), "remote-mismatch") {
			_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-other","identifier":"NEW-44","url":"https://linear.app/example/issue/NEW-44","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"team-refresh"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"issue":{"id":"remote-success","identifier":"NEW-43","url":"https://linear.app/example/issue/NEW-43","updatedAt":"2026-09-09T12:00:00Z","state":{"type":"unstarted"},"team":{"id":"team-refresh"}}}}`))
	}))
	defer server.Close()
	t.Setenv(linearclient.EnvEndpoint, server.URL)
	t.Setenv(linearclient.EnvAPIKey, "lin_api_refresh_failure_test")
	t.Setenv(dbOverrideEnv, dbPath)

	var out, errOut strings.Builder
	if code := runWithInput([]string{"linear", "outbox-drain"}, strings.NewReader(`{"product_id":"refresh-failure-product"}`), &out, &errOut); code != 0 {
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
	if !result.OK || len(result.LinkRefreshes) != 3 {
		t.Fatalf("drain result = %+v", result)
	}
	seenFailure, seenMismatch, seenSuccess := false, false, false
	for _, refresh := range result.LinkRefreshes {
		if refresh.WorkID == "refresh-failure-work" && refresh.Outcome == "failed" && strings.Contains(refresh.Detail, "issue is unavailable") {
			seenFailure = true
		}
		if refresh.WorkID == "refresh-mismatch-work" && refresh.Outcome == "failed" && strings.Contains(refresh.Detail, "returned UUID remote-other") {
			seenMismatch = true
		}
		if refresh.WorkID == "refresh-success-work" && refresh.Outcome == "updated" {
			seenSuccess = true
		}
	}
	if !seenFailure || !seenMismatch || !seenSuccess {
		t.Fatalf("link refresh results = %+v", result.LinkRefreshes)
	}
}
