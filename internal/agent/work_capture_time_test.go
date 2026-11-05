package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkSummaryPreservesNativeCaptureTimestamp(t *testing.T) {
	t.Parallel()
	for _, capturedAt := range []string{"2026-08-08T12:00:00.123456789Z", ""} {
		raw, err := json.Marshal(summary(store.WorkItem{ID: "work-1", CreatedAt: capturedAt}))
		if err != nil {
			t.Fatal(err)
		}
		var item map[string]any
		if err := json.Unmarshal(raw, &item); err != nil {
			t.Fatal(err)
		}
		if got := item["created_at"]; got != capturedAt {
			t.Errorf("created_at = %#v, want the unchanged stored value %q", got, capturedAt)
		}
	}
}

func TestWorkReadsKeepCaptureTimeAcrossRevisionAndResume(t *testing.T) {
	t.Parallel()
	s, ctx, service, env, workID := seedIntentRoundtripWork(t, map[string]any{
		"title": "Read native capture time",
		"kind":  "task", "task": "Original instruction",
		"human_key": "ZZ-7011", "remote_issue_uuid": "fixture-capture-time",
		"url": "https://linear.app/example/issue/ZZ-7011", "idempotency_key": "capture-time",
	})
	var capturedAt string
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT created_at FROM work_items WHERE id=?`, workID).Scan(&capturedAt); err != nil {
		t.Fatal(err)
	}
	if capturedAt != fixedTime().Format(time.RFC3339Nano) {
		t.Fatalf("stored capture time = %q, want the capture clock", capturedAt)
	}
	readTime := fixedTime().Add(24 * time.Hour)
	service.Now = func() time.Time { return readTime }
	s.Clock = service.Now
	assertReads := func(t *testing.T) {
		t.Helper()
		reads := map[string]func(*testing.T) []map[string]any{
			"full list": func(t *testing.T) []map[string]any {
				return []map[string]any{roundtripFullListItem(t, ctx, s, service, env, workID)}
			},
			"summary list": func(t *testing.T) []map[string]any {
				return roundtripReadItems(t, ctx, s, service, env, "concord_work_browse", "list", map[string]any{"product_id": "product-1", "work_ids": []string{workID}}, "items")
			},
			"snapshot": func(t *testing.T) []map[string]any {
				return roundtripReadItems(t, ctx, s, service, env, "concord_product_view", "snapshot", map[string]any{"product_id": "product-1"}, "previews")
			},
		}
		for name, read := range reads {
			t.Run(name, func(t *testing.T) {
				found := false
				for _, item := range read(t) {
					if item["id"] != workID {
						continue
					}
					found = true
					if got := item["created_at"]; got != capturedAt {
						t.Errorf("%s created_at = %#v, want stored capture time %q, not current clock %s", workID, got, capturedAt, readTime.Format(time.RFC3339Nano))
					}
				}
				if !found {
					t.Fatalf("read omitted captured work %s", workID)
				}
			})
		}
	}
	t.Run("later read", assertReads)
	before := roundtripFullListItem(t, ctx, s, service, env, workID)
	roundtripDispatchOK(t, ctx, s, service, env, "capture-time-revise", "concord_work_define", "revise_intent",
		carriedTaskOnlyRevision(t, before, workID, "Revised instruction", "capture-time-revise"))
	t.Run("after revision", assertReads)

	path := s.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	service = NewService(s)
	readTime = readTime.Add(24 * time.Hour)
	service.Now = func() time.Time { return readTime }
	s.Clock = service.Now
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: "project-1"})
	env.SessionRef = "resumed-session"
	t.Run("resumed session", assertReads)
}
