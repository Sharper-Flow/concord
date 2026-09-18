package store

import (
	"context"
	"strings"
	"testing"
)

func TestConfirmedLinearLinkIdentityRefreshPreservesRemoteState(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "refresh-product")
	seedLinearWorkItem(t, s, "refresh-work", "refresh-product-project", "Refresh title", "Refresh value")
	for _, state := range []string{LinearLinkUnpublished, LinearLinkPending} {
		if err := s.RecordLinearLink(ctx, "refresh-work", "remote-refresh", "OLD-1", "https://linear.app/example/issue/OLD-1", "2026-09-09T01:00:00Z", "sha256:"+strings.Repeat("a", 64), state); err != nil {
			t.Fatal(err)
		}
	}
	contentHash := "sha256:" + strings.Repeat("b", 64)
	if err := s.RecordLinearLink(ctx, "refresh-work", "remote-refresh", "OLD-1", "https://linear.app/example/issue/OLD-1", "2026-09-09T01:00:00Z", contentHash, LinearLinkConfirmed); err != nil {
		t.Fatal(err)
	}

	links, err := s.ReadConfirmedLinearLinksForProduct(ctx, "refresh-product", "9999-01-01T00:00:00Z")
	if err != nil || len(links) != 1 || links[0].RemoteIssueUUID != "remote-refresh" {
		t.Fatalf("confirmed links = %+v, error = %v", links, err)
	}
	if err := s.RefreshConfirmedLinearLink(ctx, "refresh-work", "NEW-42", "https://linear.app/example/issue/NEW-42"); err != nil {
		t.Fatalf("RefreshConfirmedLinearLink() error = %v", err)
	}

	var humanKey, url, remoteUpdatedAt, storedHash, state string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT human_key, url, remote_updated_at, content_hash, link_state FROM linear_issue_links WHERE work_id=?`, "refresh-work").Scan(&humanKey, &url, &remoteUpdatedAt, &storedHash, &state); err != nil {
		t.Fatal(err)
	}
	if humanKey != "NEW-42" || url != "https://linear.app/example/issue/NEW-42" || remoteUpdatedAt != "2026-09-09T01:00:00Z" || storedHash != contentHash || state != LinearLinkConfirmed {
		t.Fatalf("refreshed link = %s/%s/%s/%s/%s", humanKey, url, remoteUpdatedAt, storedHash, state)
	}
	if err := s.RefreshConfirmedLinearLink(ctx, "refresh-work", "", url); err == nil {
		t.Fatal("RefreshConfirmedLinearLink() accepted an empty human key")
	}
	if err := s.RefreshConfirmedLinearLink(ctx, "refresh-work", humanKey, ""); err == nil {
		t.Fatal("RefreshConfirmedLinearLink() accepted an empty URL")
	}
}

func TestConfirmedLinearLinkRefreshIntervalSkipsCheckedLinks(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	setupLinearProduct(t, s, "interval-product")
	seedLinearWorkItem(t, s, "interval-work", "interval-product-project", "Interval title", "Interval value")
	for _, state := range []string{LinearLinkUnpublished, LinearLinkPending, LinearLinkConfirmed} {
		if err := s.RecordLinearLink(ctx, "interval-work", "remote-interval", "OLD-1", "https://linear.app/example/issue/OLD-1", "", "", state); err != nil {
			t.Fatal(err)
		}
	}

	// A link that was never checked reads as stale under any cutoff.
	if links, err := s.ReadConfirmedLinearLinksForProduct(ctx, "interval-product", "2000-01-01T00:00:00Z"); err != nil || len(links) != 1 {
		t.Fatalf("never-checked link not returned: %+v, error = %v", links, err)
	}
	if err := s.MarkLinearLinkRefreshed(ctx, "interval-work"); err != nil {
		t.Fatalf("MarkLinearLinkRefreshed() error = %v", err)
	}
	// A cutoff far in the future still admits the link: the cutoff is the
	// stale-before bound, so only a check older than the interval reads stale.
	if links, err := s.ReadConfirmedLinearLinksForProduct(ctx, "interval-product", "9999-01-01T00:00:00Z"); err != nil || len(links) != 1 {
		t.Fatalf("checked link missing from an all-stale read: %+v, error = %v", links, err)
	}
	// A cutoff in the recent past skips the link: it was checked after that
	// bound, so the refresh interval has not elapsed.
	if links, err := s.ReadConfirmedLinearLinksForProduct(ctx, "interval-product", "2000-01-01T00:00:00Z"); err != nil || len(links) != 0 {
		t.Fatalf("freshly checked link still reads stale: %+v, error = %v", links, err)
	}
	// Marking leaves the identity untouched.
	var humanKey string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT human_key FROM linear_issue_links WHERE work_id=?`, "interval-work").Scan(&humanKey); err != nil {
		t.Fatal(err)
	}
	if humanKey != "OLD-1" {
		t.Fatalf("identity check rewrote identity: %s", humanKey)
	}
	if err := s.MarkLinearLinkRefreshed(ctx, "missing-work"); err == nil {
		t.Fatal("MarkLinearLinkRefreshed() accepted an unknown work item")
	}
}
