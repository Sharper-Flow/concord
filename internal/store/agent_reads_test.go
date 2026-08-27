package store

import (
	"context"
	"strings"
	"testing"
)

func TestStoreCursorRejectsTamperingAndBindingMismatch(t *testing.T) {
	s := openTemp(t)
	defer s.Close()

	want := SignedCursor{Version: 1, Tool: "concord_work_browse", Operation: "list", Scope: "product|project", Filter: "filters", Detail: "summary", Order: "priority", Source: "7", Last: "work-a", Inner: "inner-cursor"}
	token, err := s.EncodeCursor(context.Background(), want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.DecodeCursor(context.Background(), token, want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("cursor=%+v want=%+v", got, want)
	}

	_, err = s.DecodeCursor(context.Background(), token, SignedCursor{Tool: "concord_work_trace", Operation: "history", Scope: want.Scope, Filter: want.Filter, Detail: want.Detail, Order: want.Order})
	assertFailureKind(t, err, KindInvalidCursor)

	tampered := "B" + token[1:]
	if strings.HasPrefix(token, "B") {
		tampered = "A" + token[1:]
	}
	_, err = s.DecodeCursor(context.Background(), tampered, want)
	assertFailureKind(t, err, KindInvalidCursor)
}

func TestAgentReadWatermarksAreScoped(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	ctx := context.Background()
	if got, err := s.DomainEventWatermark(ctx); err != nil || got != 0 {
		t.Fatalf("empty domain-event watermark=%d err=%v", got, err)
	}

	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1);
		INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES('home','home',1,'t','t'),('other','other',1,'t','t');
		INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('anchor-home','anchor-home','prototype','operator_only',1,'t','t'),('anchor-other','anchor-other','prototype','operator_only',1,'t','t');
		INSERT OR IGNORE INTO product_projects(product_id,project_id,role) VALUES('anchor-home','home','secondary'),('anchor-other','other','secondary');
		INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES
			('locator-a','home','canonical_path','/test/locator-a','/test/locator-a','t','t'),
			('locator-b','home','canonical_path','/test/locator-b','/test/locator-b','t','t'),
			('locator-a-other','other','canonical_path','/test/locator-a-other','/test/locator-a-other','t','t');
		INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_at,complete) VALUES (?,?,?,?,?,1),(?,?,?,?,?,1),(?,?,?,?,?,1); DELETE FROM fold_guard`,
		"home", "locator-a", "HEAD", "commit-a", "now",
		"home", "locator-b", "HEAD", "commit-b", "now",
		"other", "locator-a-other", "HEAD", "commit-z", "now"); err != nil {
		t.Fatal(err)
	}

	got, err := s.KnowledgeIndexWatermark(ctx, "home", "locator-a", "HEAD")
	if err != nil || got != "commit-a" {
		t.Fatalf("scoped watermark=%q err=%v", got, err)
	}
	got, err = s.KnowledgeIndexWatermark(ctx, "home", "", "")
	if err != nil || got != "commit-b" {
		t.Fatalf("project watermark=%q err=%v", got, err)
	}
	got, err = s.KnowledgeIndexWatermark(ctx, "other", "locator-a-other", "HEAD")
	if err != nil || got != "commit-z" {
		t.Fatalf("other watermark=%q err=%v", got, err)
	}
}

func TestReadWorkItemSummaryNotFoundIsTyped(t *testing.T) {
	s := seedQueryFixture(t)
	defer s.Close()

	got, err := s.ReadWorkItemSummary(context.Background(), "blocker")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "task" {
		t.Fatalf("kind=%q", got.Kind)
	}
	_, err = s.ReadWorkItemSummary(context.Background(), "missing-initiative")
	assertFailureKind(t, err, KindProjectionNotFound)
}
